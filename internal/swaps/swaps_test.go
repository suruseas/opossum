package swaps_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/suruseas/opossum/internal/swaps"
)

// find writes one file and reads back what the package saw in it.
func find(t *testing.T, body string) []swaps.Site {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\n\nimport \"fmt\"\n\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := swaps.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestTwoStringsInOneCallCanBeExchanged(t *testing.T) {
	got := find(t, "func f(a, b string) error { return fmt.Errorf(\"%s and %s\", a, b) }\n")
	if len(got) != 1 || got[0].Pairs() != 1 {
		t.Fatalf("one call with two strings makes one pair, got %+v", got)
	}
}

// The counting is of exchanges, not of arguments: three of a kind can be
// exchanged three ways.
func TestThreeOfAKindMakeThreePairs(t *testing.T) {
	got := find(t, "func f(a, b, c string) error { return fmt.Errorf(\"%s %s %s\", a, b, c) }\n")
	if len(got) != 1 || got[0].Pairs() != 3 {
		t.Fatalf("three strings make three pairs, got %+v", got)
	}
}

// Different kinds do not pair: exchanging them would not compile, so the
// compiler is already the test.
func TestAStringAndAnIntDoNotPair(t *testing.T) {
	if got := find(t, "func f(a string, n int) error { return fmt.Errorf(\"%s %d\", a, n) }\n"); len(got) != 0 {
		t.Fatalf("a string and an int cannot be exchanged, got %+v", got)
	}
}

// A format written in pieces. This was a miss once, and closing it turned "no
// pairs here" into a live defect being found — so it is held here rather than
// left to the reader of the package comment.
func TestAFormatWrittenInPiecesIsRead(t *testing.T) {
	got := find(t, "func f(a, b string) error {\n\treturn fmt.Errorf(\"%s and \"+\n\t\t\"%s\", a, b)\n}\n")
	if len(got) != 1 || got[0].Pairs() != 1 {
		t.Fatalf("a format written in pieces is still a format, got %+v", got)
	}
}

// Explicit indexing moves the position and the verbs after it carry on from
// there, the way fmt does.
func TestExplicitIndexingIsFollowed(t *testing.T) {
	// `%[2]s %s` reads the second argument and then the third — fmt carries on
	// from where the bracket put it, and there is no third, so the call has one
	// verb with nothing behind it. Nothing to exchange either way.
	//
	// (An earlier comment here said the second verb read b again. It does not,
	// and the test was passing for a different reason than it claimed.)
	got := find(t, "func f(a, b string) error { return fmt.Errorf(\"%[2]s %s\", a, b) }\n")
	if len(got) != 0 {
		t.Fatalf("the second verb reads an argument that is not there, got %+v", got)
	}
}

// Below: the misses the package comment lists. Each one is a case this does not
// report, and the test says so — if one of them starts being reported, the
// comment is out of date and this is where that shows up.

func TestItDoesNotSeeAVerbWhoseKindIsUnknown(t *testing.T) {
	// Both take a string, and exchanging them compiles. %v says nothing about
	// what it will be handed, so nothing here pairs it.
	if got := find(t, "func f(a, b string) error { return fmt.Errorf(\"%v %s\", a, b) }\n"); len(got) != 0 {
		t.Fatalf("the package comment says %%v is not paired; it was: %+v", got)
	}
}

func TestItDoesNotSeeAFormatThatIsNotWrittenThere(t *testing.T) {
	body := "const f1 = \"%s and %s\"\n\nfunc f(a, b string) error { return fmt.Errorf(f1, a, b) }\n"
	if got := find(t, body); len(got) != 0 {
		t.Fatalf("the package comment says a format held elsewhere is not read; it was: %+v", got)
	}
}

func TestItDoesNotLookAcrossTwoCalls(t *testing.T) {
	body := "func f(a, b string) {\n\tfmt.Printf(\"%s\", a)\n\tfmt.Printf(\"%s\", b)\n}\n"
	if got := find(t, body); len(got) != 0 {
		t.Fatalf("the package comment says pairs are within one call; across two: %+v", got)
	}
}

// A file that will not parse is not a file with no pairs.
func TestABrokenFileIsAnErrorAndNotAnEmptyAnswer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\nfunc ( {"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := swaps.Find(dir); err == nil {
		t.Error("a file that does not parse should be said out loud, not counted as zero")
	}
}

// Test files are skipped: a fixture that could be exchanged is not shipped.
func TestItSkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	body := "package x\n\nimport \"fmt\"\n\nfunc f(a, b string) error { return fmt.Errorf(\"%s and %s\", a, b) }\n"
	if err := os.WriteFile(filepath.Join(dir, "x_test.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := swaps.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a test file is not shipped, got %+v", got)
	}
}

// A format function this repository writes is found by what it takes, not by
// being named here: `format string` followed by a variadic that accepts
// anything. The first version of the package listed the four functions in fmt
// and nothing else, so the two format functions of the package it was pointed at
// were invisible and the count it printed was a quarter short.
func TestItFindsTheFormatFunctionsTheFilesDefine(t *testing.T) {
	body := "func logf(format string, a ...interface{}) {}\n\n" +
		"func f(a, b string) { logf(\"%s and %s\", a, b) }\n"
	if got := find(t, body); len(got) != 1 || got[0].Pairs() != 1 {
		t.Fatalf("a local format function is a format function, got %+v", got)
	}
}

// The format is not always the first argument, and a function that takes
// something else first is where an off-by-one hides: nothing about the output
// would look wrong.
func TestItFindsTheFormatWhereverItSits(t *testing.T) {
	body := "type W struct{}\n\nfunc (W) warnf(code int, format string, a ...interface{}) {}\n\n" +
		"func f(w W, a, b string) { w.warnf(7, \"%s and %s\", a, b) }\n"
	if got := find(t, body); len(got) != 1 || got[0].Pairs() != 1 {
		t.Fatalf("the format is the second argument here, got %+v", got)
	}
	// fmt's own, one per entry in the table: a name that stops being read stops
	// being counted, and nothing about the total would look wrong.
	for name, call := range map[string]string{
		"Errorf":  "func f(a, b string) error { return fmt.Errorf(\"%s and %s\", a, b) }\n",
		"Sprintf": "func f(a, b string) string { return fmt.Sprintf(\"%s and %s\", a, b) }\n",
		"Printf":  "func f(a, b string) { fmt.Printf(\"%s and %s\", a, b) }\n",
		"Fprintf": "import \"os\"\n\nfunc f(a, b string) { fmt.Fprintf(os.Stdout, \"%s and %s\", a, b) }\n",
		"Sscanf":  "func f(s string, a, b *string) { fmt.Sscanf(s, \"%s and %s\", a, b) }\n",
	} {
		if got := find(t, call); len(got) != 1 || got[0].Pairs() != 1 {
			t.Errorf("fmt.%s takes a format and this did not read it: %+v", name, got)
		}
	}
}

// Taking a list of strings is not taking a format.
//
// The fixture has a string in front of the variadic on purpose. Written without
// one, the guard that refuses a variadic with nothing before it answers first,
// and the rule this test is about — the variadic has to accept anything — is
// never reached. It was written that way once, and it made the rule untested
// without making the test fail.
func TestAVariadicOfStringsIsNotAFormatFunction(t *testing.T) {
	body := "func note(name string, tags ...string) {}\n\nfunc f(a, b string) { note(\"%s and %s\", a, b) }\n"
	if got := find(t, body); len(got) != 0 {
		t.Fatalf("a list of strings is not a format, got %+v", got)
	}
}

// Pairs of arguments, not pairs of verbs. Reading one argument twice offers the
// same exchange twice.
func TestAnArgumentReadTwiceIsStillOnePair(t *testing.T) {
	got := find(t, "func f(a, b string) error { return fmt.Errorf(\"%s %[1]s %s\", a, b) }\n")
	if len(got) != 1 || got[0].Pairs() != 1 {
		t.Fatalf("a and b can be exchanged one way, however often a is printed, got %+v", got)
	}
}

// A doubled percent is not a verb. Reading it as one shifts every argument after
// it, and the count that comes out is wrong without looking wrong.
func TestADoubledPercentIsNotAVerb(t *testing.T) {
	got := find(t, "func f(a, b string) error { return fmt.Errorf(\"100%% %s and %s\", a, b) }\n")
	if len(got) != 1 || got[0].Pairs() != 1 {
		t.Fatalf("the pair is a and b, whatever the percent sign did, got %+v", got)
	}
}

// A precision is part of the verb, not the start of the next one.
func TestAPrecisionIsPartOfItsVerb(t *testing.T) {
	got := find(t, "func f(x, y float64) error { return fmt.Errorf(\"%.2f and %.3f\", x, y) }\n")
	if len(got) != 1 || got[0].Pairs() != 1 {
		t.Fatalf("two floats make one pair whatever their precision, got %+v", got)
	}
}

// Anything under testdata is a fixture, for the same reason a _test.go file is.
func TestItSkipsTestdata(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "testdata", "shim"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "package main\n\nimport \"fmt\"\n\nfunc f(a, b string) { fmt.Printf(\"%s and %s\", a, b) }\n"
	if err := os.WriteFile(filepath.Join(dir, "testdata", "shim", "main.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := swaps.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a fake runtime under testdata is not the product, got %+v", got)
	}
}

// Parentheses around a format are still a format.
func TestAFormatInParenthesesIsRead(t *testing.T) {
	if got := find(t, "func f(a, b string) error { return fmt.Errorf((\"%s and %s\"), a, b) }\n"); len(got) != 1 {
		t.Fatalf("the parentheses are not part of the string, got %+v", got)
	}
}

// The two the package comment lists as read the other way: a width or precision
// taken from an argument shifts everything after it.
func TestItDoesNotSeeAWidthTakenFromAnArgument(t *testing.T) {
	if got := find(t, "func f(w int, a, b string) { fmt.Printf(\"%*s %s\", w, a, b) }\n"); len(got) != 0 {
		t.Fatalf("the package comment says %%*s is misread; it was not: %+v", got)
	}
	// The half that matters. Above, the star hides the only pair there was and
	// the site vanishes either way. Here there are two same-kind verbs after
	// the star, so counting from shifted positions still finds a pair — and
	// names the wrong two arguments. Given up whole is the only answer that is
	// not a wrong one.
	if got := find(t, "func f(w, n int, p, q string) { fmt.Printf(\"%*d %s %s\", w, n, p, q) }\n"); len(got) != 0 {
		t.Fatalf("a pair counted past a star points at the wrong arguments: %+v", got)
	}
	// And a precision taken from an argument, which is the same star one place
	// along and would be a separate way through.
	if got := find(t, "func f(w int, a, b float64) { fmt.Printf(\"%.*f %f %f\", w, a, b, b) }\n"); len(got) != 0 {
		t.Fatalf("a precision from an argument shifts the same way: %+v", got)
	}
	// The pair in front of the star. Every case above puts the star first, so
	// the site vanishes whether the format is given up whole or read only as
	// far as the star — the two answers are the same and neither is being
	// tested. Here they differ: read up to the star and this reports the pair
	// a and b, which is not wrong. It is given up anyway, because "given up
	// whole" is what the package comment promises and half a rule is the kind
	// nobody can predict.
	if got := find(t, "func f(a, b string, w, n int) { fmt.Printf(\"%s %s %*d\", a, b, w, n) }\n"); len(got) != 0 {
		t.Fatalf("the format is given up whole, not read as far as the star: %+v", got)
	}
}

func TestItDoesNotSeeIndexingWrittenAfterAFlag(t *testing.T) {
	if got := find(t, "func f(a, b string) { fmt.Printf(\"%-[2]s|%-[1]s\", a, b) }\n"); len(got) != 0 {
		t.Fatalf("the package comment says a flag before the bracket hides the format; it did not: %+v", got)
	}
}

// A variadic with no string in front of it is not a format function. Registering
// one put the format at minus one, and the next call to it read Args[-1].
func TestAVariadicAloneIsNotAFormatFunction(t *testing.T) {
	body := "func fatal(a ...any) {}\n\nfunc f(x, y string) { fatal(\"%s and %s\", x, y) }\n"
	if got := find(t, body); len(got) != 0 {
		t.Fatalf("a variadic with nothing in front of it takes no format, got %+v", got)
	}
}

// Two names on one parameter are two parameters, and the format is the last of
// them. Counting the group as one put the format one place to the left.
func TestTwoNamesOnOneParameterAreTwoArguments(t *testing.T) {
	body := "func logf(prefix, format string, a ...any) {}\n\n" +
		"func f(a, b string) { logf(\"p\", \"%s and %s\", a, b) }\n"
	if got := find(t, body); len(got) != 1 || got[0].Pairs() != 1 {
		t.Fatalf("the format is the second parameter here, got %+v", got)
	}
}

// A parameter with no name is still a parameter.
func TestAnUnnamedParameterStillCounts(t *testing.T) {
	body := "func logf(string, ...any) {}\n\nfunc f(a, b string) { logf(\"%s and %s\", a, b) }\n"
	if got := find(t, body); len(got) != 1 || got[0].Pairs() != 1 {
		t.Fatalf("a name is not what makes a parameter, got %+v", got)
	}
}

// fmt is read by the name in front of the dot, not by the name after it. A local
// function called Errorf used to answer for fmt.Errorf, and every fmt.Errorf in
// the tree then had a format that was not a literal — the sites disappeared
// without a word, which is the worst direction for a thing that counts.
func TestALocalNameDoesNotAnswerForFmt(t *testing.T) {
	body := "func Errorf(code int, format string, v ...any) error { return nil }\n\n" +
		"func f(a, b string) error { return fmt.Errorf(\"%s and %s\", a, b) }\n"
	got := find(t, body)
	if len(got) != 1 || got[0].Pairs() != 1 {
		t.Fatalf("fmt.Errorf takes its format first, whatever a local Errorf takes, got %+v", got)
	}
}

// Every verb this pairs on, one at a time.
//
// A case removed from the list takes its sites with it and the total simply
// comes out smaller. %q carries more of this repository than any other:
//
//	$ go run ./cmd/swaps .          169 sites, 367 pairs
//	$ (with 'q' out of kindOf)      105 sites, 199 pairs
func TestEveryVerbThisPairsOn(t *testing.T) {
	for _, c := range []struct {
		name, body string
	}{
		{"%s", "func f(a, b string) { fmt.Printf(\"%s %s\", a, b) }\n"},
		{"%q", "func f(a, b string) { fmt.Printf(\"%q %q\", a, b) }\n"},
		{"%s and %q", "func f(a, b string) { fmt.Printf(\"%s %q\", a, b) }\n"},
		{"%d", "func f(m, n int) { fmt.Printf(\"%d %d\", m, n) }\n"},
		{"%x", "func f(m, n int) { fmt.Printf(\"%x %x\", m, n) }\n"},
		{"%X", "func f(m, n int) { fmt.Printf(\"%X %X\", m, n) }\n"},
		{"%o", "func f(m, n int) { fmt.Printf(\"%o %o\", m, n) }\n"},
		{"%b", "func f(m, n int) { fmt.Printf(\"%b %b\", m, n) }\n"},
		{"%c", "func f(m, n rune) { fmt.Printf(\"%c %c\", m, n) }\n"},
		{"%f", "func f(x, y float64) { fmt.Printf(\"%f %f\", x, y) }\n"},
		{"%g", "func f(x, y float64) { fmt.Printf(\"%g %g\", x, y) }\n"},
		{"%e", "func f(x, y float64) { fmt.Printf(\"%e %e\", x, y) }\n"},
	} {
		if got := find(t, c.body); len(got) != 1 || got[0].Pairs() != 1 {
			t.Errorf("%s pairs with itself and this did not say so: %+v", c.name, got)
		}
	}
	// And across kinds it does not pair: exchanging them is a different mistake,
	// and `go vet` is the one that catches it.
	if got := find(t, "func f(a string, n int) { fmt.Printf(\"%s %d\", a, n) }\n"); len(got) != 0 {
		t.Errorf("a string and an int are not the same kind, got %+v", got)
	}
}

// Two of a name, and the first one keeps the entry.
//
// Two packages under one root, because that is the only way two declarations can
// share a name — and walking a tree of packages is what this is for. The two
// take the format in different places, so which one wins changes the answer;
// with both taking it first, this test would pass either way and say nothing.
func TestTheFirstDeclarationOfANameKeepsIt(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		// Walked first: a/ before b/.
		"a": "package a\n\nfunc logf(format string, v ...any) {}\n\n" +
			"func f(x, y string) { logf(\"%s and %s\", x, y) }\n",
		"b": "package b\n\nfunc logf(prefix, format string, v ...any) {}\n",
	} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, name+".go"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := swaps.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	// a's logf takes the format first, and a's call passes it first. Letting b's
	// declaration win would read the format out of the second argument, which is
	// x — not a literal, so the site would vanish.
	if len(got) != 1 || got[0].Pairs() != 1 {
		t.Fatalf("the first declaration of logf keeps the name, got %+v", got)
	}
}

// The misses this package lists that are about how a format function is reached.
// Each is a case it does not report, said out loud in the package comment and
// held here — a list of limits with nothing behind it is a claim.
func TestTheWaysAFormatFunctionIsReachedAndMissed(t *testing.T) {
	for name, body := range map[string]string{
		"through a variable holding it": "func logf(format string, v ...any) {}\n\n" +
			"func f(a, b string) { g := logf; g(\"%s and %s\", a, b) }\n",
		"through a generic": "func logf[T any](format string, v ...T) {}\n\n" +
			"func f(a, b string) { logf(\"%s and %s\", a, b) }\n",
		"declared with a named string type": "type F string\n\nfunc logf(format F, v ...any) {}\n\n" +
			"func f(a, b string) { logf(\"%s and %s\", a, b) }\n",
		"declared on an interface": "type L interface{ Logf(format string, v ...any) }\n\n" +
			"func f(l L, a, b string) { l.Logf(\"%s and %s\", a, b) }\n",
		"from a package that is not fmt": "import \"log\"\n\n" +
			"func f(a, b string) { log.Printf(\"%s and %s\", a, b) }\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got := find(t, body); len(got) != 0 {
				t.Fatalf("the package comment says this is missed; it was not: %+v", got)
			}
		})
	}
}

// The other half of each verb-shaped miss. The package comment names two forms
// apiece and only one of each was held.
func TestThePrecisionShapedMisses(t *testing.T) {
	for name, body := range map[string]string{
		"a precision taken from an argument": "func f(p int, x, y float64) { fmt.Printf(\"%.*f %f\", p, x, y) }\n",
		"indexing written after a precision": "func f(m, n int) { fmt.Printf(\"%.[2]d|%.[1]d\", m, n) }\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got := find(t, body); len(got) != 0 {
				t.Fatalf("the package comment says this is missed; it was not: %+v", got)
			}
		})
	}
}

// Directories the go tool does not build are not the product — and the one the
// caller named is never one of them. Asked to walk `..`, or a directory whose
// name begins with a dot, an earlier version skipped it and answered zero.
func TestItSkipsWhatIsNotBuiltButNeverTheRoot(t *testing.T) {
	body := "package x\n\nimport \"fmt\"\n\nfunc f(a, b string) { fmt.Printf(\"%s and %s\", a, b) }\n"
	for _, name := range []string{"vendor", ".hidden", "_scratch"} {
		t.Run("under "+name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name, "x.go"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := swaps.Find(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Fatalf("%s is not built and not counted, got %+v", name, got)
			}
		})
		t.Run("as the root: "+name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := swaps.Find(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 {
				t.Fatalf("the directory the caller named is the one to walk, got %+v", got)
			}
		})
	}
}

// The string has to be the last thing before the variadic. A parameter between
// them means the values after the format are not all the format's — counting
// them as its arguments would put a pair where the code and a name sit.
func TestSomethingBetweenTheFormatAndTheVariadicIsNotAFormatFunction(t *testing.T) {
	body := "func logf(format string, code int, v ...any) {}\n\n" +
		"func f(a, b string) { logf(\"%s and %s\", 7, a, b) }\n"
	if got := find(t, body); len(got) != 0 {
		t.Fatalf("the arguments after the format are not all the format's, got %+v", got)
	}
}
