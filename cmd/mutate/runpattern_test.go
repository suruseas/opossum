package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/mutate"
)

// Compared against a tree before the change, the two sweeps have to run the
// same tests. The change here only adds a test, and that test does not notice
// the mutation; the test that was there before does, in both trees. Narrowed to
// the new test in this tree and whole in the other, the comparison read "was
// caught before this change, and now survives" for a mutation nothing weakened.
// Both run whole, and it is what it is: already caught.
func TestABaselineComparisonRunsBothTreesWhole(t *testing.T) {
	mod := gitFixture(t)
	t.Chdir(mod)
	ownTemp(t)
	if err := os.WriteFile(filepath.Join(mod, "n_test.go"), []byte(
		"package m\n\nimport \"testing\"\n\nfunc TestTheAnswerIsPositive(t *testing.T) {\n\tif Answer() <= 0 {\n\t\tt.Fatal(\"not positive\")\n\t}\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sweep := `[{"name":"the answer changes","file":"m.go","from":"func Answer() int { return 42 }",` +
		`"to":"func Answer() int { return 43 }","packages":["./..."],"run":["TestTheAnswerIsPositive"]}]`
	var out, errOut bytes.Buffer
	code := run([]string{"-baseline", "HEAD", spec(t, sweep)}, &out, &errOut, nil, func(int) {})
	if code != exitAllCaught {
		t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitAllCaught, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "already caught before this change") || strings.Contains(out.String(), "now survives") {
		t.Errorf("the comparison should find it caught in both trees, got:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "run names are not used") {
		t.Errorf("the patterns being set aside should be said, got:\n%s", errOut.String())
	}
}

// A named subtest beside another name, against the real toolchain: the suite
// before any mutation runs the subtest, finds it red, and stops — rather than
// crediting every mutation to it.
func TestABaselineRunsANamedSubtestAndSeesItFail(t *testing.T) {
	mod := throwawayModule(t)
	if err := os.WriteFile(filepath.Join(mod, "other_test.go"), []byte(
		"package m\n\nimport \"testing\"\n\nfunc TestOther(t *testing.T) {\n\tt.Run(\"red\", func(t *testing.T) { t.Fatal(\"already red\") })\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sweep := `[{"name":"a","file":"m.go","from":"func Answer() int { return 42 }","to":"func Answer() int { return 43 }","packages":["./..."],"run":["TestAnswer"]},` +
		`{"name":"b","file":"m.go","from":"func Answer() int { return 42 }","to":"func Answer() int { return 44 }","packages":["./..."],"run":["TestOther/red"]}]`
	var out, errOut bytes.Buffer
	code := run([]string{spec(t, sweep)}, &out, &errOut, nil, func(int) {})
	if code != exitFailed || !strings.Contains(errOut.String(), "TestOther/red") || !strings.Contains(errOut.String(), "already failing") {
		t.Errorf("want the sweep refused for the red subtest, got exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
}

// A subtest name nothing has, against the real toolchain: its parent runs and
// passes, the name does not. The sweep stops before any mutation.
func TestANamedSubtestThatDoesNotExistStopsTheSweep(t *testing.T) {
	throwawayModule(t)
	sweep := `[{"name":"the typo","file":"m.go","from":"func Answer() int { return 42 }","to":"func Answer() int { return 43 }","packages":["./..."],"run":["TestAnswer","TestAnswer/typo"]}]`
	var out, errOut bytes.Buffer
	code := run([]string{spec(t, sweep)}, &out, &errOut, nil, func(int) {})
	if code != exitFailed || !strings.Contains(errOut.String(), "TestAnswer/typo did not run to a result before any mutation") {
		t.Errorf("want the sweep refused naming the subtest, got exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
}

// Names as `go test -json` writes them, against the real toolchain: a subtest
// whose name had a space (written with `_`) and one with characters a pattern
// would read. Both run, and both catch the mutation.
func TestNamedSubtestsAreFoundByTheNamesGoTestPrints(t *testing.T) {
	mod := throwawayModule(t)
	if err := os.WriteFile(filepath.Join(mod, "named_test.go"), []byte(
		"package m\n\nimport \"testing\"\n\nfunc TestNames(t *testing.T) {\n"+
			"\tt.Run(\"has space\", func(t *testing.T) { if Answer() != 42 { t.Fatal(\"wrong\") } })\n"+
			"\tt.Run(\"f[(]x|y\", func(t *testing.T) { if Answer() != 42 { t.Fatal(\"wrong\") } })\n"+
			"\tt.Run(\"c/\", func(t *testing.T) { if Answer() != 42 { t.Fatal(\"wrong\") } })\n"+
			"\tt.Run(\"e//f\", func(t *testing.T) { if Answer() != 42 { t.Fatal(\"wrong\") } })\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sweep := `[{"name":"the answer changes","file":"m.go","from":"func Answer() int { return 42 }","to":"func Answer() int { return 43 }","packages":["./..."],"run":["TestNames/has_space","TestNames/f[(]x|y","TestNames/c/","TestNames/e//f"]}]`
	var out, errOut bytes.Buffer
	code := run([]string{spec(t, sweep)}, &out, &errOut, nil, func(int) {})
	if code != exitAllCaught || !strings.Contains(out.String(), "TestNames/has_space") || !strings.Contains(out.String(), `TestNames/f[(]x\|y`) ||
		!strings.Contains(out.String(), "TestNames/c/,") || !strings.Contains(out.String(), "TestNames/e//f") {
		t.Errorf("want it caught by every named subtest, one with an empty level among them, got exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
}

// Two mutations naming two tests where the second needs the first to have run:
// each mutation's run runs both, as the suite before any mutation did, so
// neither reads the dependent test's red as a catch. Neither mutation touches
// what those tests check, so both survive.
func TestATestThatLeansOnAnotherIsNotCreditedWithACatch(t *testing.T) {
	mod := throwawayModule(t)
	if err := os.WriteFile(filepath.Join(mod, "order_test.go"), []byte(
		"package m\n\nimport \"testing\"\n\nvar ready bool\n\n"+
			"func TestSetup(t *testing.T) { ready = true }\n\n"+
			"func TestUses(t *testing.T) {\n\tif !ready {\n\t\tt.Fatal(\"setup did not run\")\n\t}\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sweep := `[{"name":"a","file":"m.go","from":"func Answer() int { return 42 }","to":"func Answer() int { return 43 }","packages":["./..."],"run":["TestSetup"]},` +
		`{"name":"b","file":"m.go","from":"func Answer() int { return 42 }","to":"func Answer() int { return 44 }","packages":["./..."],"run":["TestUses"]}]`
	var out, errOut bytes.Buffer
	code := run([]string{spec(t, sweep)}, &out, &errOut, nil, func(int) {})
	if code != exitSurvivor || strings.Contains(out.String(), "TestUses |") || strings.Contains(out.String(), "| caught |") ||
		!strings.Contains(errOut.String(), "mutate: 2 of 2 mutations survived — the defects they introduce are invisible to the tests the sweep ran\n") {
		t.Errorf("want both mutations survived and TestUses credited with nothing, got exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
}

// -baseline says it sets run names aside only when some mutation has them.
func TestTheBaselineNoteIsForSweepsThatNameTests(t *testing.T) {
	whole := mutate.Mutation{Name: "w"}
	named := mutate.Mutation{Name: "n", Run: []string{"TestA"}}
	if got := baselineNote([]mutate.Mutation{whole}); got != "" {
		t.Errorf("no names, no note; got %q", got)
	}
	want := "mutate: with -baseline, run names are not used — both trees run their packages whole, so the comparison is between the same tests"
	if got := baselineNote([]mutate.Mutation{whole, named}); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The closing line for survivors speaks for the tests the sweep ran — the
// named ones only when every mutation names tests.
func TestTheSurvivorSummarySpeaksForWhatTheSweepRan(t *testing.T) {
	// What the sweep recorded decides, not the names: a named row in a sweep
	// that ran whole.
	narrowed := mutate.Result{Mutation: mutate.Mutation{Name: "n", Run: []string{"TestA"}}, Outcome: mutate.Survived, Narrowed: true}
	wholeRun := mutate.Result{Mutation: mutate.Mutation{Name: "n", Run: []string{"TestA"}}, Outcome: mutate.Survived}
	whole := mutate.Result{Mutation: mutate.Mutation{Name: "w"}, Outcome: mutate.Caught}
	for _, tc := range []struct {
		name    string
		results []mutate.Result
		want    string
	}{
		{"every mutation names tests", []mutate.Result{narrowed}, "mutate: 1 of 1 mutations survived — the defects they introduce are invisible to the tests the sweep ran"},
		{"one names nothing", []mutate.Result{wholeRun, whole}, "mutate: 1 of 2 mutations survived — the defects they introduce are invisible to the suite"},
		{"a named row alone, from a sweep that ran whole", []mutate.Result{wholeRun}, "mutate: 1 of 1 mutations survived — the defects they introduce are invisible to the suite"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := wrongSummary(mutate.Wrong(tc.results), tc.results); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
