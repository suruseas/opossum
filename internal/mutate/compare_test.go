package mutate

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func res(name string, o Outcome) Result {
	return Result{Mutation: Mutation{Name: name}, Outcome: o}
}

// section hands back cat's slice of the report: from its header to the next
// bold line. Rows asserted inside it are rows that could not have satisfied
// the test from some other category's section.
func section(t *testing.T, got, cat string) string {
	t.Helper()
	head := "**" + cat + " — "
	i := strings.Index(got, head)
	if i < 0 {
		t.Fatalf("no section %q in:\n%s", cat, got)
	}
	rest := got[i+len(head):]
	if j := strings.Index(rest, "\n**"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// One mutation in every category, and a total that is their sum — checked by
// adding the section counts back up out of the rendered text, so the claim
// "the total is computed from the rows" is read off the page rather than
// trusted to the code that printed it.
func TestEveryCategoryIsPrintedAndTheTotalIsTheirSum(t *testing.T) {
	now := []Result{
		res("newly", Caught),
		res("regressed", Survived),
		res("still", Survived),
		res("always", Caught),
		res("fresh", Caught),
		res("twice", Caught),
		res("unclean", Caught),
		res("only-now", Caught),
	}
	before := []Result{
		res("newly", Survived),
		res("regressed", Caught),
		res("still", Survived),
		res("always", Caught),
		res("unclean", Broken),
		res("only-before", Caught),
	}
	fresh := []NewSite{{Mutation: Mutation{Name: "fresh"}, Reason: "the pattern appears 0 times in that tree"}}
	ambiguous := []NewSite{{Mutation: Mutation{Name: "twice"}, Reason: "the pattern appears 2 times in that tree"}}

	got := CompareReport("main (abc)", now, before, fresh, ambiguous)
	// Each category is checked with the row that belongs in it, inside its own
	// section — a check on the counts alone stays green when two categories
	// swap their contents, since the counts swap with them. That mutation was
	// run, and this is the assertion it survived until.
	for cat, member := range map[string]string{
		NewlyCaught:             "- newly",
		CaughtBeforeSurvivesNow: "- regressed",
		StillSurvives:           "- still",
		AlreadyCaught:           "- always",
		NotPresentBefore:        "- fresh",
		AmbiguousBefore:         "- twice",
		NotMeasuredCleanly:      "- unclean",
		Unmatched:               "- only-now",
	} {
		if s := section(t, got, cat); !strings.Contains(s, member+"\n") && !strings.Contains(s, member+" ") {
			t.Errorf("section %q does not hold %q:\n%s\n\nfull report:\n%s", cat, member, s, got)
		}
	}
	// The grounds ride on the rows: "0 times" and "2 times" are different
	// facts, and each has to appear where its row does.
	if s := section(t, got, NotPresentBefore); !strings.Contains(s, "(the pattern appears 0 times in that tree)") {
		t.Errorf("the fresh row does not carry its reason:\n%s", s)
	}
	if s := section(t, got, AmbiguousBefore); !strings.Contains(s, "(the pattern appears 2 times in that tree)") {
		t.Errorf("the ambiguous row does not carry its reason:\n%s", s)
	}
	for cat, n := range map[string]int{
		NewlyCaught: 1, CaughtBeforeSurvivesNow: 1, StillSurvives: 1,
		AlreadyCaught: 1, NotPresentBefore: 1, AmbiguousBefore: 1, NotMeasuredCleanly: 1,
		Unmatched: 2, // only-now and only-before, one from each side
	} {
		want := fmt.Sprintf("**%s — %d**", cat, n)
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "**9 in all.**") {
		t.Errorf("the total is the sum of the sections, which is 9:\n%s", got)
	}
	if strings.Contains(got, "BUG IN THIS REPORT") {
		t.Errorf("every category above is a printed one, so the self-check must stay silent:\n%s", got)
	}

	// And the page agrees with itself: the counts printed on the section
	// headers add up to the printed total.
	sum := 0
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "**") || !strings.Contains(line, "— ") {
			continue
		}
		var n int
		tail := line[strings.LastIndex(line, "— ")+len("— "):]
		if _, err := fmt.Sscanf(tail, "%d**", &n); err == nil {
			sum += n
		}
	}
	if sum != 9 {
		t.Errorf("the section counts on the page add to %d, and the page says 9", sum)
	}
}

// A row filed under a category the printed list does not carry must not vanish
// into a total that still looks right. The report checks itself; this makes
// sure the check fires — by the only route that can reach it without editing
// the code, a category list with an entry removed.
func TestARowWithNoPrintedCategoryIsLoud(t *testing.T) {
	full := Categories
	defer func() { Categories = full }()
	Categories = Categories[:len(Categories)-1] // drop Unmatched from the printed list

	got := CompareReport("main (abc)", []Result{res("orphan", Caught)}, nil, nil, nil)
	if !strings.Contains(got, "BUG IN THIS REPORT") {
		t.Errorf("a filed row with no printed section should be the loudest thing on the page:\n%s", got)
	}
}

// Applicable answers per mutation: carried, or new with the reason, or — for a
// read that failed — nothing at all, because "could not look" must stop the
// comparison rather than be filed as "was not there".
func TestApplicableFilesEachMutationOrStops(t *testing.T) {
	ms := []Mutation{
		{Name: "carried", File: "old.go", From: "alpha", To: "beta"},
		{Name: "rewritten", File: "old.go", From: "gone", To: "x"},
		{Name: "doubled", File: "old.go", From: "dup", To: "x"},
		{Name: "new file", File: "new.go", From: "anything", To: "x"},
	}
	read := func(p string) ([]byte, bool, error) {
		if p == "old.go" {
			return []byte("alpha\ndup dup\n"), true, nil
		}
		return nil, false, nil
	}
	ok, fresh, ambiguous, err := Applicable(ms, read)
	if err != nil {
		t.Fatal(err)
	}
	if len(ok) != 1 || ok[0].Name != "carried" {
		t.Errorf("carried = %v, want the one whose text the baseline holds once", ok)
	}
	if len(fresh) != 2 {
		t.Fatalf("%d new sites, want the rewritten pattern and the new file", len(fresh))
	}
	// "Written twice" is not "not there": filing it as fresh would make the
	// heading say the opposite of the truth.
	if len(ambiguous) != 1 || ambiguous[0].Mutation.Name != "doubled" ||
		!strings.Contains(ambiguous[0].Reason, "2 times") {
		t.Errorf("ambiguous = %v, want the doubled pattern with its count", ambiguous)
	}
	for _, f := range append(fresh, ambiguous...) {
		if f.Reason == "" {
			t.Errorf("%s: a new site without a reason is a row nobody can check", f.Mutation.Name)
		}
	}

	_, _, _, err = Applicable(ms, func(string) ([]byte, bool, error) {
		return nil, false, errors.New("permission denied")
	})
	if err == nil {
		t.Error("a read that failed must stop the comparison, not read as absence")
	}
}
