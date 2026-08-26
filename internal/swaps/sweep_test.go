package swaps_test

import (
	"go/parser"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/swaps"
)

// sweep writes one file and returns the mutations and skips read out of it.
//
// It goes through the file rather than through a Site built by hand, because
// what Sweep promises is about the file: that each mutation names one place in
// it, and that applying one produces a different file.
func sweep(t *testing.T, body string) ([]swaps.Mutation, []swaps.Skip, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	src := "package x\n\nimport \"fmt\"\n\n" + body
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	sites, err := swaps.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	muts, skips, err := swaps.Sweep(sites)
	if err != nil {
		t.Fatal(err)
	}
	return muts, skips, src
}

// The two halves of a mutation are the call as written and the call with two
// arguments exchanged — and nothing else moved.
func TestAMutationExchangesTwoArgumentsAndNothingElse(t *testing.T) {
	muts, _, _ := sweep(t, "func f(a, b string) error { return fmt.Errorf(\"%s and %s\", a, b) }\n")
	if len(muts) != 1 {
		t.Fatalf("one pair, one mutation, got %d: %+v", len(muts), muts)
	}
	m := muts[0]
	if want := `fmt.Errorf("%s and %s", a, b)`; m.From != want {
		t.Errorf("From = %q, want the call as written %q", m.From, want)
	}
	if want := `fmt.Errorf("%s and %s", b, a)`; m.To != want {
		t.Errorf("To = %q, want %q", m.To, want)
	}
	// The format is what a reader compares the arguments against. A mutation
	// that moved it would be changing the sentence rather than exchanging what
	// fills it, and the survivor it produced would mean something else.
	if a, b := format(m.From), format(m.To); a != b {
		t.Errorf("the format has to survive the exchange: %q became %q", a, b)
	}
}

// What comes out is Go. An exchange built by slicing text could produce
// something that will not parse, and a sweep of those reports every mutation
// as "did not compile" — a whole run that measured nothing while looking busy.
func TestTheExchangedCallIsStillGo(t *testing.T) {
	muts, _, _ := sweep(t, "func f(a string, b, c int) error {\n"+
		"\treturn fmt.Errorf(\n\t\t\"%s: %d of %d\",\n\t\ta,\n\t\tb,\n\t\tc,\n\t)\n}\n")
	if len(muts) == 0 {
		t.Fatal("a call spread over lines still has a pair")
	}
	for _, m := range muts {
		if _, err := parser.ParseExpr(m.To); err != nil {
			t.Errorf("%q does not parse: %v", m.To, err)
		}
	}
}

// The sweep tool finds a mutation by its text and requires one match. A call
// written twice is two places one edit would land, so it is not offered.
func TestACallWrittenTwiceIsNotOffered(t *testing.T) {
	muts, skips, _ := sweep(t,
		"func f(a, b string) error { return fmt.Errorf(\"%s and %s\", a, b) }\n"+
			"func g(a, b string) error { return fmt.Errorf(\"%s and %s\", a, b) }\n")
	if len(muts) != 0 {
		t.Errorf("both copies are the same text, so neither names one place: %+v", muts)
	}
	if len(skips) != 2 || skips[0].Reason != swaps.SkipNotUnique {
		t.Errorf("and both are said out loud, got %+v", skips)
	}
}

// A directory named absolutely comes back absolutely. The go tool takes either,
// and gluing `./` onto one names a place below the working directory instead.
func TestAnAbsolutePathIsAlreadyAPackagePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.go"),
		[]byte("package x\n\nimport \"fmt\"\n\nfunc f(a, b string) { fmt.Printf(\"%s %s\", a, b) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sites, err := swaps.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	muts, _, err := swaps.Sweep(sites)
	if err != nil {
		t.Fatal(err)
	}
	if len(muts) != 1 {
		t.Fatalf("one pair, got %+v", muts)
	}
	if got := muts[0].Packages[0]; got != dir {
		t.Errorf("package = %q, want the directory as given, %q", got, dir)
	}
}

// Two arguments that are the same expression exchange into the file they came
// from. A sweep of those reports a survivor for every one, and a survivor that
// changed nothing is not a defect anybody can act on.
func TestAnExchangeThatChangesNothingIsNotOffered(t *testing.T) {
	muts, skips, _ := sweep(t, "type T struct{ N string }\n\n"+
		"func f(t T) error { return fmt.Errorf(\"%s needs %s\", t.N, t.N) }\n")
	if len(muts) != 0 {
		t.Errorf("the same expression twice: %+v", muts)
	}
	if len(skips) != 1 || skips[0].Reason != swaps.SkipNoChange {
		t.Errorf("and it is said out loud, got %+v", skips)
	}
}

// Every mutation names one place in its file, which is what the sweep tool
// requires of the ones it is given. Checked over this repository rather than
// over a fixture: the shapes that break it are the ones nobody wrote on purpose.
func TestEveryMutationOfThisRepositoryNamesOnePlace(t *testing.T) {
	sites, err := swaps.Find("../..")
	if err != nil {
		t.Fatal(err)
	}
	muts, skips, err := swaps.Sweep(sites)
	if err != nil {
		t.Fatal(err)
	}
	if len(muts) == 0 {
		t.Fatal("this repository has format calls with exchangeable arguments; none came back")
	}
	pairs := 0
	for _, s := range sites {
		pairs += s.Pairs()
	}
	// Nothing vanishes between the two: every pair is a mutation or a skip.
	if len(muts)+len(skips) != pairs {
		t.Errorf("%d pairs came in and %d mutations + %d skips came out", pairs, len(muts), len(skips))
	}
	seen := map[string]string{}
	for _, m := range muts {
		b, err := os.ReadFile(m.File)
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(b), m.From); n != 1 {
			t.Errorf("%s: From appears %d times, and the sweep tool wants one:\n%s", m.Name, n, m.From)
		}
		if m.From == m.To {
			t.Errorf("%s: the exchange changes nothing", m.Name)
		}
		if prev, ok := seen[m.Name]; ok {
			t.Errorf("two mutations share the name %q (%q and %q)", m.Name, prev, m.To)
		}
		seen[m.Name] = m.To
		if want := "./" + filepath.ToSlash(filepath.Dir(m.File)) + "/"; m.Packages[0] != want {
			t.Errorf("%s: package = %q, want %q", m.Name, m.Packages[0], want)
		}
	}
}

// format is the first quoted string in a call, which for these is the format.
func format(call string) string {
	i := strings.Index(call, `"`)
	if i < 0 {
		return ""
	}
	j := strings.Index(call[i+1:], `"`)
	if j < 0 {
		return ""
	}
	return call[i : i+2+j]
}

// Two format calls on one line get two names. A Sprintf inside an Errorf is the
// ordinary way to write one, and two rows of a report that read identically are
// two findings a person cannot act on separately.
func TestTwoCallsOnOneLineAreNamedApart(t *testing.T) {
	muts, _, _ := sweep(t, "func f(a, b, c string) error {\n"+
		"\treturn fmt.Errorf(\"%s and %s\", fmt.Sprintf(\"%s-%s\", a, b), c)\n}\n")
	if len(muts) != 2 {
		t.Fatalf("the outer call and the inner one each offer a pair, got %d: %+v", len(muts), muts)
	}
	if muts[0].Name == muts[1].Name {
		t.Errorf("both are called %q, and they exchange different things:\n %q\n %q",
			muts[0].Name, muts[0].To, muts[1].To)
	}
}

// A pair the format does not read together is not offered at all. Reported, it
// would name a mutation that edits two arguments the message never puts side by
// side — and every mutation after it in the file would be measured against a
// build that `go vet` refuses, which the sweep reads as "nothing was measured".
func TestAPairReadPastAStarIsNotOffered(t *testing.T) {
	muts, skips, _ := sweep(t, "func f(w, n int, p, q string) { fmt.Printf(\"%*d %s %s\", w, n, p, q) }\n")
	if len(muts) != 0 || len(skips) != 0 {
		t.Errorf("the format is given up whole, not offered from shifted positions: %+v %+v", muts, skips)
	}
}

// The name says which two arguments, and says it the way the format reads them.
//
// It is the whole of what a report row shows: the sweep prints the name and the
// outcome, and a person deciding whether a survivor matters goes back to the
// line with those two numbers in hand. Reversed, every row of every report is
// wrong about the pair it names, and nothing else in this package would notice
// — this is a sentence, and the rest of the tests here look at code.
func TestTheNameSaysWhichTwoArgumentsInTheOrderTheFormatReadsThem(t *testing.T) {
	muts, _, _ := sweep(t, "func f(a, b, c string) error { return fmt.Errorf(\"%s %s %s\", a, b, c) }\n")
	if len(muts) != 3 {
		t.Fatalf("three of a kind exchange three ways, got %d: %+v", len(muts), muts)
	}
	want := []string{
		"reads arguments 1 and 2 the other way round",
		"reads arguments 1 and 3 the other way round",
		"reads arguments 2 and 3 the other way round",
	}
	for i, m := range muts {
		if !strings.HasSuffix(m.Name, want[i]) {
			t.Errorf("name %d = %q, want it to end %q", i, m.Name, want[i])
		}
		// And the exchange the name describes is the one it performs. The name
		// is written by one function and the text by another, so agreeing is
		// not automatic.
		args := strings.Split(strings.TrimSuffix(strings.SplitN(m.To, ", ", 2)[1], ")"), ", ")
		if len(args) != 3 {
			t.Fatalf("three arguments after the format, got %q", m.To)
		}
		lo, hi := i, 0
		switch i {
		case 0:
			lo, hi = 0, 1
		case 1:
			lo, hi = 0, 2
		case 2:
			lo, hi = 1, 2
		}
		plain := []string{"a", "b", "c"}
		if args[lo] != plain[hi] || args[hi] != plain[lo] {
			t.Errorf("%q says it exchanges %d and %d; it produced %q", m.Name, lo+1, hi+1, m.To)
		}
	}
}

// A call that is gone by the time the file is read back says so, and does not
// say the other thing.
//
// The two are one line apart in the code and a world apart to a reader: told a
// call is written twice, they go looking for the copy. This reaches it the way
// a long sweep would — the tree is edited while the sweep is being built, which
// over a repository-sized run is not a rare event but a slow one.
func TestACallThatWentAwayIsNotCalledADuplicate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	if err := os.WriteFile(path, []byte("package x\n\nimport \"fmt\"\n\n"+
		"func f(a, b string) { fmt.Printf(\"%s %s\", a, b) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sites, err := swaps.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 {
		t.Fatalf("one call, got %+v", sites)
	}
	if err := os.WriteFile(path, []byte("package x\n\nfunc f(a, b string) { _, _ = a, b }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	muts, skips, err := swaps.Sweep(sites)
	if err != nil {
		t.Fatal(err)
	}
	if len(muts) != 0 {
		t.Errorf("the call is not there to mutate: %+v", muts)
	}
	if len(skips) != 1 || skips[0].Reason != swaps.SkipGone {
		t.Errorf("skips = %+v, want one reading %q", skips, swaps.SkipGone)
	}
}

// Every reason this package can give is one a caller can be shown.
//
// The list exists because the prose about it went wrong: a doc comment said
// there were two while the code had three, in a change whose whole subject was
// sentences that quantify. A sentence cannot be held to a count, but the set
// can be — so the set is here, and a reason added without being named here
// fails rather than quietly making some doc comment false.
func TestTheReasonsAreTheOnesThisPackageCanGive(t *testing.T) {
	want := map[string]bool{
		swaps.SkipNotUnique: true,
		swaps.SkipNoChange:  true,
		swaps.SkipGone:      true,
	}
	if len(want) != 3 {
		t.Fatalf("three named reasons, and two of them are the same string: %v", want)
	}
	// Each reads as a clause about the pair, not as a code.
	for r := range want {
		if len(r) < 20 || strings.ContainsAny(r, "%_") {
			t.Errorf("a reason is printed to a person: %q", r)
		}
	}
	// And the ones a sweep of this repository actually produces are among them.
	sites, err := swaps.Find("../..")
	if err != nil {
		t.Fatal(err)
	}
	_, skips, err := swaps.Sweep(sites)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range skips {
		if !want[s.Reason] {
			t.Errorf("%s:%d came back with a reason nothing names: %q", s.File, s.Line, s.Reason)
		}
	}
}
