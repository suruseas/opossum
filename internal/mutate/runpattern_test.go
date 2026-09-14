package mutate

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// resultJSON is a run in which each named test started and ended with the
// given action — pass, fail or skip — as the real toolchain writes it.
func resultJSON(action string, names ...string) string {
	var b strings.Builder
	for _, n := range names {
		b.WriteString(`{"Action":"run","Test":"` + n + `"}` + "\n")
		b.WriteString(`{"Action":"` + action + `","Test":"` + n + `"}` + "\n")
	}
	b.WriteString(`{"Action":"pass","Package":"example.com/p"}` + "\n")
	return b.String()
}

func named(name string, tests ...string) Mutation {
	m := mut()
	m.Name, m.Run = name, tests
	return m
}

func testRuns(f *fakeRunner) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, a := range f.goArgs {
		if a[0] == "test" {
			out = append(out, a)
		}
	}
	return out
}

// runs reports whether a `go test` command line asks for exactly this pattern.
func runs(args []string, pattern string) bool {
	i := slices.Index(args, "-run")
	return i >= 0 && i+1 < len(args) && args[i+1] == pattern
}

// Every test run of a named sweep — the suite before any mutation and each
// mutation's — runs the same tests: all the sweep names, in one binary. A
// mutation's run that ran only its own tests would read a test that leans on
// another having run first as caught, where the baseline saw it pass.
func TestEveryRunOfANamedSweepRunsTheSameTests(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	f.testOut["call()"] = resultJSON("pass", "TestTheWire", "TestTheOtherWire")
	f.testOut["noop()"] = failJSON("TestTheWire")
	got, err := f.Sweep([]Mutation{named("a", "TestTheWire"), named("b", "TestTheOtherWire")})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	tests := testRuns(f)
	if len(tests) != 3 {
		t.Fatalf("want a baseline and two mutation runs, got %q", tests)
	}
	for i, a := range tests {
		if !runs(a, "^TestTheWire$|^TestTheOtherWire$") {
			t.Errorf("run %d should ask for every named test, got %q", i, a)
		}
	}
	if got[0].Outcome != Caught || got[1].Outcome != Caught {
		t.Errorf("outcomes = %v, %v", got[0].Outcome, got[1].Outcome)
	}
}

// Each level of a name is matched whole and literally: a name is not a
// pattern, so what `go test` would read as a bracket, a group or an
// alternative in it is only a character.
func TestTheSweepPatternMatchesEachNameWholeAndLiterally(t *testing.T) {
	for _, tc := range []struct {
		name string
		ms   []Mutation
		want string
	}{
		{"one top-level test", []Mutation{named("a", "TestA")}, "^TestA$"},
		{"a subtest", []Mutation{named("a", "TestA/has_space")}, "^TestA$/^has_space$"},
		{"empty levels", []Mutation{named("a", "TestA/c/", "TestA//d")}, "^TestA$/^c$/^$|^TestA$/^$/^d$"},
		{"characters a pattern would read", []Mutation{named("a", "TestA/f[(]x|y.z")}, `^TestA$/^f\[\(\]x\|y\.z$`},
		{"a name two mutations share, once", []Mutation{named("a", "TestA", "TestB"), named("b", "TestB")}, "^TestA$|^TestB$"},
		{"a mutation that names nothing runs everything", []Mutation{named("a", "TestA"), mut()}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SweepPattern(tc.ms); got != tc.want {
				t.Errorf("SweepPattern = %q, want %q", got, tc.want)
			}
		})
	}
}

// A mutation that names nothing asks for its packages whole, so every run of
// the sweep goes whole.
func TestAWholeMutationMakesEveryRunWhole(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	f.testOut["call()"] = resultJSON("pass", "TestTheWire")
	if _, err := f.Sweep([]Mutation{named("narrow", "TestTheWire"), mut()}); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	for i, a := range testRuns(f) {
		if slices.Contains(a, "-run") {
			t.Errorf("run %d should not be narrowed when a mutation runs whole, got %q", i, a)
		}
	}
}

// A named test that does not run to a result under a mutation measured
// nothing of what the mutation is about, whatever the reason: not there, only
// its parent there, or skipped. That is not a survivor.
func TestANamedTestThatDidNotRunToAResultIsNotASurvivor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		names   []string
		mutated string
		missing string
	}{
		{"nothing ran", []string{"TestTheWire"}, passJSON, "TestTheWire"},
		{"it was skipped", []string{"TestTheWire"}, resultJSON("skip", "TestTheWire"), "TestTheWire"},
		{"only its parent ran", []string{"TestTheWire/cut"}, resultJSON("pass", "TestTheWire"), "TestTheWire/cut"},
		{"one of two ran", []string{"TestTheWire", "TestTheOtherWire"}, resultJSON("pass", "TestTheWire"), "TestTheOtherWire"},
		{"two of three did not", []string{"TestTheWire", "TestTheOtherWire", "TestTheWire/cut"}, resultJSON("pass", "TestTheOtherWire"), "TestTheWire, TestTheWire/cut"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, map[string]string{"x.go": "call()"})
			f.testOut["call()"] = resultJSON("pass", "TestTheWire", "TestTheWire/cut", "TestTheOtherWire")
			f.testOut["noop()"] = tc.mutated
			got, err := f.Sweep([]Mutation{named("the wire is cut", tc.names...)})
			if err != nil {
				t.Fatalf("Sweep: %v", err)
			}
			if want := tc.missing + " did not run to a result in ./p/"; got[0].Outcome != Inconclusive || got[0].Detail != want {
				t.Errorf("want not measured with %q, got %v %q", want, got[0].Outcome, got[0].Detail)
			}
			if row := "| the wire is cut | " + Inconclusive.String() + " | n/a (measured nothing: " + tc.missing + " did not run to a result in ./p/) |"; !strings.Contains(Report(got), row) {
				t.Errorf("want the row %q, got:\n%s", row, Report(got))
			}
		})
	}
}

// Before any mutation, too: a named test the unmutated suite does not run to a
// result is one the sweep would read mutations' fates from unchecked.
func TestABaselineMissingANamedTestRefusesToRun(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	f.testOut["call()"] = resultJSON("pass", "TestTheWire")
	_, err := f.Sweep([]Mutation{named("a", "TestTheWire"), named("b", "TestTheWire", "TestOther/red")})
	if err == nil || !strings.HasPrefix(err.Error(), "b: TestOther/red did not run to a result before any mutation") {
		t.Fatalf("want the sweep refused naming the mutation and the test, got %v", err)
	}
}

// The empty name is refused before anything runs.
func TestAnEmptyNameIsRefusedBeforeAnythingRuns(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	_, err := f.Sweep([]Mutation{named("a", "TestA", "")})
	if err == nil || err.Error() != "a: an empty test name — name each test as `go test -json` names it" {
		t.Fatalf("want the empty name refused, got %v", err)
	}
	if n := len(testRuns(f)); n != 0 {
		t.Errorf("nothing should run before the sweep is checked, got %d runs", n)
	}
}

// A level of a name can be empty — `go test -json` prints `t.Run("c/")` as
// `TestA/c/` — and such a name is one the sweep runs by.
func TestANameWithAnEmptyLevelIsATestName(t *testing.T) {
	names := []string{"/TestA", "TestA/c/", "TestA//d", "TestA/e//f"}
	f := newFake(t, map[string]string{"x.go": "call()"})
	f.testOut["call()"] = resultJSON("pass", names...)
	f.testOut["noop()"] = failJSON("TestA//d")
	got, err := f.Sweep([]Mutation{named("a", names...)})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if got[0].Outcome != Caught {
		t.Errorf("outcome = %v %q", got[0].Outcome, got[0].Detail)
	}
}

// Whole names, not prefixes: a test whose name begins with a named one does
// not stand in for it.
func TestUnmeasuredMatchesWholeNames(t *testing.T) {
	got := Unmeasured([]string{"TestA", "TestA/sub", "TestB"}, []string{"TestAB", "TestA/subtle", "TestB"})
	if want := []string{"TestA", "TestA/sub"}; !slices.Equal(got, want) {
		t.Errorf("Unmeasured = %q, want %q", got, want)
	}
}

// In a sweep where one mutation names nothing, every run is whole, and the
// checks on names still hold: a named test the suite does not run to a result
// stops the sweep, and one a mutation's run leaves out makes that mutation
// not measured.
func TestAMixedSweepStillChecksTheNames(t *testing.T) {
	t.Run("before any mutation", func(t *testing.T) {
		f := newFake(t, map[string]string{"x.go": "call()"})
		f.testOut["call()"] = resultJSON("pass", "TestA")
		_, err := f.Sweep([]Mutation{mut(), named("named", "TestA", "TestNope")})
		if err == nil || !strings.HasPrefix(err.Error(), "named: TestNope did not run to a result before any mutation") {
			t.Fatalf("want the sweep refused, got %v", err)
		}
	})
	t.Run("under a mutation", func(t *testing.T) {
		f := newFake(t, map[string]string{"x.go": "call()"})
		f.testOut["call()"] = resultJSON("pass", "TestA")
		f.testOut["noop()"] = resultJSON("skip", "TestA")
		got, err := f.Sweep([]Mutation{mut(), named("named", "TestA")})
		if err != nil {
			t.Fatalf("Sweep: %v", err)
		}
		if got[1].Outcome != Inconclusive || got[1].Detail != "TestA did not run to a result in ./p/" {
			t.Errorf("want not measured, got %v %q", got[1].Outcome, got[1].Detail)
		}
	})
}

// A named sweep whose tests could not be run at all says that, not that the
// names were wrong.
func TestANamedSweepThatCannotRunSaysSo(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	f.testDies["call()"] = true
	_, err := f.Sweep([]Mutation{named("a", "TestA")})
	if err == nil || !strings.HasPrefix(err.Error(), "the suite could not be run before any mutation was applied") {
		t.Fatalf("want the could-not-run refusal, got %v", err)
	}
}

// A narrowed baseline that is red says so of the named tests, run together:
// the same tests may pass with the rest of their packages.
func TestANarrowedRedBaselineSpeaksForTheNamedTests(t *testing.T) {
	for _, tc := range []struct {
		name string
		ms   []Mutation
		want string
	}{
		{"narrowed", []Mutation{named("a", "TestA")}, "the tests the sweep names, run together without the rest of their packages, are already failing"},
		{"whole", []Mutation{mut()}, "the suite is already failing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, map[string]string{"x.go": "call()"})
			f.testOut["call()"] = failJSON("TestA")
			_, err := f.Sweep(tc.ms)
			if err == nil || !strings.HasPrefix(err.Error(), tc.want+" before any mutation is applied") {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

// A survivor of a named sweep escaped the tests the sweep named, which is what
// the table says — not the whole suite.
func TestANamedSurvivorSaysWhichTestsItEscaped(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	f.testOut["call()"] = resultJSON("pass", "TestA")
	f.testOut["noop()"] = resultJSON("pass", "TestA")
	got, err := f.Sweep([]Mutation{named("the wire is cut", "TestA")})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	table := Report(got)
	if !strings.Contains(table, "none of the tests the sweep named caught it") || strings.Contains(table, "invisible to the suite") {
		t.Errorf("the survivor row should speak for the named tests only, got:\n%s", table)
	}
}

// The survivor row speaks for the tests the sweep ran: the named ones when
// every mutation names tests, the suite when one does not — whatever the row
// itself names.
func TestTheSurvivorRowSpeaksForWhatTheSweepRan(t *testing.T) {
	for _, tc := range []struct {
		name string
		ms   []Mutation
		want string
	}{
		{"every mutation names tests", []Mutation{named("n", "TestA")}, "| n | SURVIVED | **none of the tests the sweep named caught it**"},
		{"one mutation names nothing", []Mutation{named("n", "TestA"), func() Mutation { m := mut(); m.Name = "w"; m.From, m.To = "other()", "gone()"; return m }()},
			"| n | SURVIVED | **none — this defect is invisible to the suite**"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, map[string]string{"x.go": "call()"})
			f.files["x.go"] = "call() other()"
			f.testOut["call() other()"] = resultJSON("pass", "TestA")
			f.testOut["noop() other()"] = resultJSON("pass", "TestA")
			f.testOut["call() gone()"] = failJSON("TestA")
			got, err := f.Sweep(tc.ms)
			if err != nil {
				t.Fatalf("Sweep: %v", err)
			}
			if table := Report(got); !strings.Contains(table, tc.want) {
				t.Errorf("want the row %q, got:\n%s", tc.want, table)
			}
		})
	}
}

// A sweep that stops partway returns the rows before the stop. Whether its
// runs were narrowed is what the sweep decided from every mutation, not what
// those rows alone would make of it: here the row that names nothing is the
// one that failed, so every run was whole, and the survivor ahead of it
// escaped the suite.
func TestAPartialSweepSpeaksForWhatTheSweepRan(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()", "y.go": "other()"})
	f.testOut["call()"] = resultJSON("pass", "TestA")
	f.testOut["noop()"] = resultJSON("pass", "TestA")
	write := f.Runner.Write
	f.Runner.Write = func(p string, b []byte) error {
		if p == "y.go" && string(b) != "other()" {
			return errors.New("permission denied")
		}
		return write(p, b)
	}
	whole := mut()
	whole.Name, whole.File, whole.From, whole.To = "w", "y.go", "other()", "gone()"
	got, err := f.Sweep([]Mutation{named("n", "TestA"), whole})
	if err == nil || len(got) != 1 {
		t.Fatalf("want the sweep stopped after one row, got %d rows, err %v", len(got), err)
	}
	for i, a := range testRuns(f) {
		if slices.Contains(a, "-run") {
			t.Errorf("run %d should be whole, got %q", i, a)
		}
	}
	if want := "| n | SURVIVED | **none — this defect is invisible to the suite**"; !strings.Contains(Report(got), want) {
		t.Errorf("want %q, got:\n%s", want, Report(got))
	}
	if Narrowed(got) {
		t.Error("the rows before the stop should say the sweep ran whole")
	}
}

func TestMeasuredIsWhatRanToAResult(t *testing.T) {
	out := resultJSON("pass", "TestA") + resultJSON("fail", "TestB") + resultJSON("skip", "TestC") +
		`{"Action":"run","Test":"TestD"}` + "\n"
	if got, want := Measured(out), []string{"TestA", "TestB"}; !slices.Equal(got, want) {
		t.Errorf("Measured = %q, want %q", got, want)
	}
}
