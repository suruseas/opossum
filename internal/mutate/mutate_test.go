package mutate

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// The pattern missing is the failure this package exists to make impossible: a
// replacement that matches nothing changes nothing, the suite stays green, and
// the author writes "survived" about a mutation that was never applied. Both
// halves are silent, so the only defence is to count.
//
// The `from == to` and empty-`from` cases are the same failure arriving through
// the front door: an entry copied from another sweep and only half edited applies
// cleanly, changes nothing, and every test passes.
func TestApplyRefusesAnythingThatWouldNotChangeExactlyOnePlace(t *testing.T) {
	const src = "a := 1\nb := 1\n"
	for name, m := range map[string]Mutation{
		"not there":      {File: "x.go", From: "c := 1", To: "c := 2"},
		"twice":          {File: "x.go", From: ":= 1", To: ":= 2"},
		"from equals to": {File: "x.go", From: "a := 1", To: "a := 1"},
		"empty from":     {File: "x.go", From: "", To: "injected"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Apply(src, m); err == nil {
				t.Error("a mutation that would not change exactly one place must be refused")
			}
		})
	}
	got, err := Apply(src, Mutation{File: "x.go", From: "a := 1", To: "a := 2"})
	if err != nil {
		t.Fatalf("a unique pattern should apply: %v", err)
	}
	if got != "a := 2\nb := 1\n" {
		t.Errorf("applied = %q", got)
	}
	// An empty file is where an empty `from` used to slip through: strings.Count
	// of "" in "" is 1, so the uniqueness check passed and the whole file was
	// replaced by the `to`.
	if _, err := Apply("", Mutation{File: "x.go", From: "", To: "injected"}); err == nil {
		t.Error("an empty pattern must be refused even against an empty file")
	}
}

// "The package went red" is not attribution. A mutation to one file can break a
// test that has nothing to do with the change being defended, and crediting that
// to the test just written has put false claims in pull requests.
func TestFailuresNamesEachTestOnce(t *testing.T) {
	const out = `{"Action":"run","Test":"TestOne"}
{"Action":"fail","Test":"TestOne/sub_case"}
{"Action":"pass","Test":"TestTwo"}
{"Action":"fail","Test":"TestOne"}
{"Action":"fail","Test":"TestOne"}
{"Action":"fail","Package":"example.com/pkg"}
`
	got := Failures(out)
	want := []string{"TestOne", "TestOne/sub_case"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Failures = %v, want %v (sorted, deduplicated, passes and the package line ignored)", got, want)
	}
	if len(Failures(`{"Action":"pass","Package":"example.com/pkg"}`+"\n")) != 0 {
		t.Error("a green run names nobody")
	}
}

// Sorted, so the same sweep produces the same report twice — a table that
// reorders between runs cannot be diffed, and this one goes into pull requests.
func TestFailuresAreOrderedTheSameWayEveryTime(t *testing.T) {
	const out = `{"Action":"fail","Test":"TestZulu"}
{"Action":"fail","Test":"TestAlpha"}
{"Action":"fail","Test":"TestMike"}
`
	got := strings.Join(Failures(out), ",")
	if got != "TestAlpha,TestMike,TestZulu" {
		t.Errorf("Failures = %s, want them sorted rather than in the order they arrived", got)
	}
}

// A test that prints go test's own output — this repository has several — used to
// be read as a failure, because the old parse matched any indented `--- FAIL:`
// line and a log line is indented. The event stream cannot make that mistake: a
// failure is a field, not a line.
func TestFailuresIgnoresTestOutputThatQuotesGoTest(t *testing.T) {
	const out = `{"Action":"output","Test":"TestThatQuotes","Output":"    --- FAIL: TestFromTheFixture (0.00s)\n"}
{"Action":"pass","Test":"TestThatQuotes"}
`
	if got := Failures(out); len(got) != 0 {
		t.Errorf("Failures = %v, want none — that text is a test's own output, not its result", got)
	}
}

// failJSON is what `go test -json` emits for a run where those tests failed.
func failJSON(names ...string) string {
	var b strings.Builder
	for _, n := range names {
		b.WriteString(`{"Action":"fail","Test":"` + n + `"}` + "\n")
	}
	b.WriteString(`{"Action":"fail","Package":"example.com/p"}` + "\n")
	return b.String()
}

const passJSON = `{"Action":"pass","Package":"example.com/p"}` + "\n"

// fakeRunner is a Runner over an in-memory tree, so the sweep can be driven
// through every outcome without a checkout to damage.
type fakeRunner struct {
	*Runner
	// mu guards files. The sweep reads the tree from the goroutine driving it
	// while an interrupt writes it from another, and only the Runner's own lock
	// serialises the two writers — a reader outside that lock is a genuine race,
	// which the detector found on CI.
	mu    sync.Mutex
	files map[string]string
	// vetFails and testOut are keyed by the mutated content, so a fake can
	// answer differently depending on what the mutation actually wrote.
	vetFails map[string]bool
	testOut  map[string]string
	// testDies models a run that ends non-zero without naming anyone: a panic, a
	// timeout, a toolchain that could not start.
	testDies map[string]bool
	logged   []string
	// goArgs is every toolchain invocation, in order. Without it the baseline's
	// command line is unmeasured: a fake that only looks at args[0] answers the
	// same whether or not the run asked for `-json`, and `Failures` reads nothing
	// but JSON.
	goArgs [][]string
}

func newFake(t *testing.T, files map[string]string) *fakeRunner {
	t.Helper()
	f := &fakeRunner{
		files: files, vetFails: map[string]bool{},
		testOut: map[string]string{}, testDies: map[string]bool{},
	}
	f.Runner = &Runner{
		Read: func(p string) ([]byte, error) {
			s, ok := f.get(p)
			if !ok {
				return nil, os.ErrNotExist
			}
			return []byte(s), nil
		},
		Write: func(p string, b []byte) error { f.set(p, string(b)); return nil },
		Go: func(args ...string) (string, string, error) {
			f.mu.Lock()
			f.goArgs = append(f.goArgs, append([]string(nil), args...))
			f.mu.Unlock()
			current, _ := f.get("x.go")
			if args[0] == "vet" {
				// vet writes plain text, as the real one does.
				return "", "x.go:3:2: undefined: gone", boolErr(f.vetFails[current])
			}
			if f.testDies[current] {
				// A panic reaches the -json stream as Output events, not as plain
				// text — the shape the real toolchain produces, so that what the
				// report shows here is what it would show there.
				return `{"Action":"output","Output":"panic: test timed out after 10m0s\n"}` + "\n" +
					`{"Action":"output","Output":"\tgoroutine 1 [running]:\n"}` + "\n" +
					`{"Action":"fail","Package":"example.com/p"}` + "\n", "", errors.New("exit 2")
			}
			out := f.testOut[current]
			return out, "", boolErr(strings.Contains(out, `"Action":"fail"`))
		},
		Log: func(s string) { f.logged = append(f.logged, s) },
	}
	return f
}

func (f *fakeRunner) get(p string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.files[p]
	return s, ok
}

func (f *fakeRunner) set(p, v string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[p] = v
}

func boolErr(b bool) error {
	if b {
		return errors.New("exit 1")
	}
	return nil
}

// mustGet reads the fake tree under its lock.
func mustGet(t *testing.T, f *fakeRunner, p string) string {
	t.Helper()
	s, _ := f.get(p)
	return s
}

func mut() Mutation {
	return Mutation{Name: "the wire is cut", File: "x.go", From: "call()", To: "noop()", Packages: []string{"./p/"}}
}

// A mutation that does not compile ran no tests, so it says nothing about
// whether the defect is guarded. Reporting it as caught is the mistake — it is
// the shape of "I removed a field and the package went red", which proves only
// that the field was there.
func TestAMutationThatDoesNotCompileIsNotEvidence(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	f.vetFails["noop()"] = true
	f.testOut["noop()"] = failJSON("TestSomething") // would look "caught" if consulted

	got, err := f.Sweep([]Mutation{mut()})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if got[0].Outcome != Broken {
		t.Errorf("outcome = %v, want %v — no test ran, so nothing was proved", got[0].Outcome, Broken)
	}
	if len(got[0].Killers) != 0 {
		t.Errorf("a build failure must not be credited to any test, got %v", got[0].Killers)
	}
	if got[0].Detail == "" {
		t.Error("the build error should be kept — 'did not compile' with no reason leaves nothing to act on")
	}
}

func TestCaughtAndSurvivedAreDistinguished(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	f.testOut["noop()"] = failJSON("TestTheWireIsThere")
	got, _ := f.Sweep([]Mutation{mut()})
	if got[0].Outcome != Caught || len(got[0].Killers) != 1 || got[0].Killers[0] != "TestTheWireIsThere" {
		t.Errorf("want caught by TestTheWireIsThere, got %v %v", got[0].Outcome, got[0].Killers)
	}

	f2 := newFake(t, map[string]string{"x.go": "call()"})
	f2.testOut["noop()"] = passJSON
	got2, _ := f2.Sweep([]Mutation{mut()})
	if got2[0].Outcome != Survived {
		t.Errorf("a green suite under a mutation is the finding, got %v", got2[0].Outcome)
	}
}

// A run that ends red without naming anyone — a panic, a package timeout, a
// toolchain that would not start — is not "the suite is fine with this defect".
// It is the loudest lie the tool could tell, and the mutations most worth writing
// here (waits, loops, interrupt checks) are the ones that hang.
func TestARunThatNamesNobodyIsNotASurvivor(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	f.testDies["noop()"] = true

	got, err := f.Sweep([]Mutation{mut()})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if got[0].Outcome != Inconclusive {
		t.Errorf("outcome = %v, want %v", got[0].Outcome, Inconclusive)
	}
	if !strings.Contains(got[0].Detail, "timed out") || strings.Contains(got[0].Detail, `"Action"`) {
		t.Errorf("the reason should survive into the result, got %q", got[0].Detail)
	}
}

// The file must come back whatever happened — including when the mutated tree
// does not build, which is the path that returns early.
func TestTheFileIsRestoredWhateverHappened(t *testing.T) {
	for name, breakBuild := range map[string]bool{"tests ran": false, "build failed": true} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, map[string]string{"x.go": "call()"})
			f.vetFails["noop()"] = breakBuild
			f.testOut["noop()"] = failJSON("TestX")
			if _, err := f.Sweep([]Mutation{mut()}); err != nil {
				t.Fatalf("Sweep: %v", err)
			}
			if mustGet(t, f, "x.go") != "call()" {
				t.Errorf("the file was left as %q — a sweep must not leave a mutation behind", mustGet(t, f, "x.go"))
			}
		})
	}
}

// The write that applies the mutation truncates the file before it fills it, so a
// write that fails partway leaves it already destroyed. That is the one case
// where the file is certainly damaged, and it used to be the one case nothing put
// back: the restore was armed after the write returned.
func TestAFailedWriteStillLeavesTheFileAsItWas(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	f.Runner.Write = func(p string, b []byte) error {
		if string(b) == "noop()" {
			f.set(p, "") // truncated, as O_TRUNC does, then the disk fills
			return errors.New("no space left on device")
		}
		f.set(p, string(b))
		return nil
	}

	if _, err := f.Sweep([]Mutation{mut()}); err == nil {
		t.Fatal("a write that failed must be reported")
	}
	if mustGet(t, f, "x.go") != "call()" {
		t.Errorf("the file was left as %q — the author's uncommitted work is gone", mustGet(t, f, "x.go"))
	}
}

// A restore that silently failed would leave a mutation in the tree for the
// author to commit, which is the worst thing this can do. So it is verified by
// reading the file back, and a mismatch is an error even when the sweep itself
// went fine.
func TestARestoreThatDidNotTakeIsAnError(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	f.testOut["noop()"] = passJSON
	// The mutation is written; the restore is the write that reports success and
	// does nothing — a full disk, a read-only mount, an editor holding the file.
	// Failing every write instead would be a weaker test that passes for the
	// wrong reason: the mutation would never land, so the file would already
	// hold its original contents and the restore would trivially agree.
	real := f.Runner.Write
	f.Runner.Write = func(p string, b []byte) error {
		if string(b) == "call()" {
			return nil // swallow the restore
		}
		return real(p, b)
	}

	_, err := f.Sweep([]Mutation{mut()})
	if err == nil {
		t.Fatal("a restore that did not take must be reported")
	}
	if !strings.Contains(err.Error(), "do not commit") {
		t.Errorf("the error should tell the author what is at stake, got: %v", err)
	}
}

// Interrupting the process runs no deferred restore — `os.Exit` sees to that —
// so the file has to be recoverable from outside the sweep. Without this, a
// Ctrl-C mid-run leaves the author's uncommitted work mutated, which is the
// accident the whole package is for.
func TestAnInterruptedSweepCanPutTheFileBack(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	// Interrupt from inside the test run, which is where a long sweep spends its
	// time and where Ctrl-C actually lands.
	//
	// The first test run is the baseline, and it has to see the tree as it was —
	// a baseline taken over a mutated file would measure the mutation and call it
	// the starting point.
	runs := 0
	f.Runner.Go = func(args ...string) (string, string, error) {
		if args[0] == "test" {
			runs++
			if runs == 1 {
				if got := mustGet(t, f, "x.go"); got != "call()" {
					t.Errorf("the baseline ran over %q, not the tree as it was", got)
				}
				return passJSON, "", nil
			}
			if mustGet(t, f, "x.go") != "noop()" {
				t.Errorf("the mutation should be in the tree while the tests run, found %q", mustGet(t, f, "x.go"))
			}
			restored, err := f.RestorePending()
			if err != nil {
				t.Errorf("RestorePending: %v", err)
			}
			if !restored {
				t.Error("RestorePending said there was nothing to put back, mid-mutation")
			}
			if mustGet(t, f, "x.go") != "call()" {
				t.Errorf("an interrupt left the file as %q", mustGet(t, f, "x.go"))
			}
		}
		return passJSON, "", nil
	}

	if _, err := f.Sweep([]Mutation{mut()}); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if mustGet(t, f, "x.go") != "call()" {
		t.Errorf("the file ended as %q", mustGet(t, f, "x.go"))
	}
}

// A signal that arrives when nothing is in flight must not write anything — and
// it still means "stop", so no further mutation is written after it. Both halves
// matter: the first keeps a stray signal from touching a file, the second is what
// stops the sweep carrying on into the next mutation while the process is on its
// way out.
func TestAnInterruptWithNothingInFlightWritesNothingAndStopsTheSweep(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	f.testOut["noop()"] = passJSON
	restored, err := f.RestorePending()
	if restored {
		t.Error("it said a file was put back, with nothing in flight — saying so when nothing " +
			"was written tells the author something that did not happen")
	}
	if err != nil {
		t.Errorf("RestorePending with nothing in flight: %v", err)
	}
	if mustGet(t, f, "x.go") != "call()" {
		t.Errorf("it wrote something: %q", mustGet(t, f, "x.go"))
	}
	if _, err := f.Sweep([]Mutation{mut()}); err == nil {
		t.Error("after an interrupt the sweep must stop rather than write the next mutation")
	}
	if mustGet(t, f, "x.go") != "call()" {
		t.Errorf("a mutation was written after the interrupt: %q", mustGet(t, f, "x.go"))
	}
}

// The window between "this file is registered as mutated" and "the mutation is on
// disk" used to be open: an interrupt landing there restored a file nobody had
// touched, printed that it had put things back, and then the sweep wrote the
// mutation anyway. A tool that says "restored" over a mutated tree is worse than
// one that says nothing.
func TestAnInterruptCannotBeOvertakenByTheMutationItRestored(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	f.testOut["noop()"] = passJSON
	// Interrupt from inside the write itself, which is the whole window.
	var handler sync.WaitGroup
	inner := f.Runner.Write
	f.Runner.Write = func(p string, b []byte) error {
		if string(b) == "noop()" {
			handler.Add(1)
			// Would deadlock if the write did not already hold the lock, which is
			// itself the property being relied on.
			go func() { defer handler.Done(); _, _ = f.RestorePending() }()
		}
		return inner(p, b)
	}
	if _, err := f.Sweep([]Mutation{mut()}); err != nil && !errors.Is(err, errAborted) {
		t.Fatalf("Sweep: %v", err)
	}
	// Wait for the interrupt to finish before reading the tree. Reading it while
	// that goroutine may still be writing is a race — the two writers are
	// serialised by the Runner's lock, but this reader is not one of them, and the
	// race detector was right to say so (it did, on CI, intermittently).
	handler.Wait()
	// Whatever the interleaving, the file is not left mutated.
	if mustGet(t, f, "x.go") != "call()" {
		t.Errorf("the tree was left as %q", mustGet(t, f, "x.go"))
	}
}

// A mutation with nowhere to run is not "survived": nothing was ever asked.
func TestAMutationWithNoPackagesIsRefused(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	m := mut()
	m.Packages = nil
	if _, err := f.Sweep([]Mutation{m}); err == nil {
		t.Error("a mutation with no packages to test must be refused, not reported as survived")
	}
}

// The whole promise is "it puts things back", which can only hold for files it is
// allowed to touch in the first place.
//
// Driven through a real Runner against a file that really exists outside the
// root, because the fake cannot show this: its Read fails for any path it has
// never heard of, so a sweep with no containment at all would still come back
// with an error and the test would pass on the wrong reason. It did — this was
// green while the check was disabled.
func TestAMutationCannotReachOutsideTheTree(t *testing.T) {
	outside := t.TempDir()
	root := filepath.Join(outside, "repo")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	const secret = "not the sweep's to touch"
	target := filepath.Join(outside, "outside.go")
	if err := os.WriteFile(target, []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{"../outside.go", target, "sub/../../outside.go", ".."} {
		r := NewRunner(root)
		r.Log = nil
		// The toolchain must never be reached: refusing has to happen before any
		// of this runs.
		r.Go = func(args ...string) (string, string, error) {
			t.Errorf("%q got as far as running the toolchain", p)
			return passJSON, "", nil
		}
		m := Mutation{Name: "reach out", File: p, From: "not the sweep's", To: "very much the sweep's",
			Packages: []string{"./..."}}
		if _, err := r.Sweep([]Mutation{m}); err == nil {
			t.Errorf("%q is outside the tree and must be refused", p)
		}
		if b, _ := os.ReadFile(target); string(b) != secret {
			t.Fatalf("%q was rewritten: %q", target, b)
		}
	}
}

// A test that is already failing fails again under every mutation, and a failing
// test is what this reads as "caught". One red test therefore makes a whole sweep
// report that everything is guarded while measuring nothing — the one thing the
// exit statuses promise to keep apart, arriving through the front door.
//
// It happened: a sweep reported a mutation as caught by a test that could not
// run at all, and the mutation it was measuring was never observed by anything.
func TestASweepOverAlreadyFailingTestsRefusesToRun(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	// Keyed by the UNMUTATED content: this is the tree as the sweep finds it.
	f.testOut["call()"] = failJSON("TestSomethingElseEntirely")
	// And the mutation would look caught by it, which is the trap.
	f.testOut["noop()"] = failJSON("TestSomethingElseEntirely")

	res, err := f.Sweep([]Mutation{mut()})
	if err == nil {
		t.Fatal("a sweep over a red tree ran, and every mutation in it would read as caught")
	}
	if len(res) != 0 {
		t.Errorf("it reported %d result(s); a sweep that never measured anything must report none", len(res))
	}
	if !strings.Contains(err.Error(), "TestSomethingElseEntirely") {
		t.Errorf("the error should name what is already failing, got: %v", err)
	}
	// And nothing was written on the way to finding out.
	if got := mustGet(t, f, "x.go"); got != "call()" {
		t.Errorf("the tree was mutated before the baseline was taken: %q", got)
	}
}

// The other half: a suite that cannot run at all is not a green baseline. A
// toolchain that will not start, or a package that panics before any test is
// named, leaves nobody to credit — and a sweep run on top of it would report
// whatever it liked.
func TestASweepRefusesToRunWhenTheSuiteCannotRunAtAll(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	f.testDies["call()"] = true
	if _, err := f.Sweep([]Mutation{mut()}); err == nil {
		t.Fatal("a sweep ran on top of a suite that could not run")
	} else if !strings.Contains(err.Error(), "could not be run") {
		t.Errorf("the error should say the suite could not be run, got: %v", err)
	}
}

// A fault in the sweep itself is found before the baseline, which takes as long
// as the suite does. Otherwise a typo in the seventh mutation costs a full test
// run before it is reported.
func TestTheWholeSweepIsCheckedBeforeAnythingRuns(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	ran := false
	f.Runner.Go = func(args ...string) (string, string, error) { ran = true; return passJSON, "", nil }
	good := mut()
	bad := mut()
	bad.Name, bad.From = "not there", "nothing matches this"
	if _, err := f.Sweep([]Mutation{good, bad}); err == nil {
		t.Fatal("a sweep holding a mutation that does not apply ran anyway")
	}
	if ran {
		t.Error("the toolchain ran before the sweep was known to be well formed")
	}
}

// The baseline reads failing test names out of `go test -json`, and asks for a
// fresh run rather than the cache. Neither is visible in what it returns, so
// dropping either leaves the baseline reporting a green tree it never looked at
// — and the whole point of it is to be the one run that is believed.
func TestTheBaselineAsksForTheOutputItReads(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	// Two mutations in the one package, so "once each" is a real question.
	if _, err := f.Sweep([]Mutation{mut(), mut()}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.goArgs) == 0 {
		t.Fatal("the toolchain was never called")
	}
	first := f.goArgs[0]
	if first[0] != "test" {
		t.Fatalf("the first thing run was %q, want the baseline test run", first)
	}
	for _, want := range []string{"-count=1", "-json", "./p/"} {
		if !slices.Contains(first, want) {
			t.Errorf("the baseline ran %q, which is missing %q", first, want)
		}
	}
	// Once each. Naming a package twice does not give a wrong answer — it makes
	// the run and the error that comes out of it say everything twice.
	if n := strings.Count(strings.Join(first, " "), "./p/"); n != 1 {
		t.Errorf("the baseline named ./p/ %d times: %q", n, first)
	}
}

// The baseline covers every package the sweep will touch, not just the first
// one's. A red test in the second package would otherwise sit there catching
// every mutation aimed at it, which is the failure this whole thing is for.
func TestTheBaselineCoversEveryPackageTheSweepWillTouch(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	var asked [][]string
	f.Runner.Go = func(args ...string) (string, string, error) {
		asked = append(asked, args)
		if slices.Contains(args, "./q/") && args[0] == "test" {
			return failJSON("TestRedOverInQ"), "", errors.New("exit 1")
		}
		return passJSON, "", nil
	}
	inP, inQ := mut(), mut()
	inQ.Name, inQ.Packages = "in q", []string{"./q/"}

	_, err := f.Sweep([]Mutation{inP, inQ})
	if err == nil {
		t.Fatal("the sweep ran with a red test in the second package; every mutation aimed there " +
			"would have been reported as caught by it")
	}
	if !strings.Contains(err.Error(), "TestRedOverInQ") {
		t.Errorf("the error should name what is failing, got: %v", err)
	}
	if len(asked) != 1 || !slices.Contains(asked[0], "./p/") || !slices.Contains(asked[0], "./q/") {
		t.Errorf("the baseline ran %q, want one run over both packages", asked)
	}
}

// A run that stopped before it measured anything prints no table. The header row
// on its own reads like a sweep that found nothing to say, which is the opposite
// of what happened.
func TestNothingMeasuredPrintsNoTable(t *testing.T) {
	if got := Report(nil); got != "" {
		t.Errorf("Report(nil) = %q, want nothing at all", got)
	}
	if got := Report([]Result{{Mutation: Mutation{Name: "x"}}}); got == "" {
		t.Error("a result was reported as nothing")
	}
}

// The progress line is what a long sweep shows while it works, and it names the
// tests as they are found.
func TestTheProgressLineSaysWhatHappened(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	f.testOut["noop()"] = failJSON("TestTheWireIsThere")
	if _, err := f.Sweep([]Mutation{mut()}); err != nil {
		t.Fatal(err)
	}
	// Two lines: the baseline says it is running before anything is mutated — a
	// sweep that sat silent through it would look stuck — and then the mutation.
	if len(f.logged) != 3 || !strings.Contains(f.logged[0], "baseline") ||
		!strings.Contains(f.logged[1], "green") {
		t.Fatalf("logged %q, want the baseline announced and then reported before any mutation", f.logged)
	}
	if !strings.Contains(f.logged[2], "TestTheWireIsThere") {
		t.Errorf("logged %q, want the mutation and its killer named", f.logged)
	}
}

// The report is what gets pasted into a pull request, so a mutation nothing
// caught has to be impossible to skim past — in both columns, since a reader
// scanning the outcome column would otherwise see "caught".
func TestTheReportMakesASurvivorImpossibleToMiss(t *testing.T) {
	out := Report([]Result{
		{Mutation: Mutation{Name: "the wire is cut"}, Outcome: Caught, Killers: []string{"TestA", "TestB"}},
		{Mutation: Mutation{Name: "the wait is gone"}, Outcome: Survived},
		{Mutation: Mutation{Name: "a field is deleted"}, Outcome: Broken},
		{Mutation: Mutation{Name: "the run hangs"}, Outcome: Inconclusive, Detail: "panic: test timed out"},
	})
	if !strings.Contains(out, "TestA, TestB") {
		t.Errorf("a caught mutation should name its killers:\n%s", out)
	}
	if !strings.Contains(out, "| SURVIVED |") {
		t.Errorf("the outcome column must say SURVIVED, not something a reader scans past:\n%s", out)
	}
	if !strings.Contains(out, "invisible to the suite") {
		t.Errorf("a survivor must say so in words, not by an empty cell:\n%s", out)
	}
	if !strings.Contains(out, "not evidence") {
		t.Errorf("a mutation that did not compile must not read as a result:\n%s", out)
	}
	if !strings.Contains(out, "timed out") {
		t.Errorf("an inconclusive run should carry why:\n%s", out)
	}
}

// A name with a pipe in it would end the column early and shift every cell after
// it — the table is markdown, and it is read by people.
func TestTheReportSurvivesAPipeInAName(t *testing.T) {
	out := Report([]Result{{Mutation: Mutation{Name: "a|b"}, Outcome: Survived}})
	if strings.Contains(out, "| a|b |") {
		t.Errorf("the pipe should be escaped:\n%s", out)
	}
	if !strings.Contains(out, `a\|b`) {
		t.Errorf("the name should still be readable:\n%s", out)
	}
}

// The real Runner reads and writes the actual tree; the fake above cannot show
// that those two agree on where a file is.
func TestNewRunnerReadsAndWritesRelativeToItsRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "f.go"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(root)
	b, err := r.Read("sub/f.go")
	if err != nil || string(b) != "original" {
		t.Fatalf("Read = %q, %v", b, err)
	}
	if err := r.Write("sub/f.go", []byte("changed")); err != nil {
		t.Fatal(err)
	}
	on, _ := os.ReadFile(filepath.Join(root, "sub", "f.go"))
	if string(on) != "changed" {
		t.Errorf("Write went somewhere else: file holds %q", on)
	}
}

// A cached test result is the third way a green run can be a lie: `go test`
// without `-count=1` may answer from a previous run, so a mutation could be
// reported as survived on the strength of a result recorded before it existed.
// Nothing else here would notice the flag going missing — the fake does not
// cache — so the argument list is asserted directly.
func TestTheTestsAreAlwaysRunAfresh(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	var testArgs []string
	f.Runner.Go = func(args ...string) (string, string, error) {
		if args[0] == "test" {
			testArgs = args
		}
		return passJSON, "", nil
	}
	if _, err := f.Sweep([]Mutation{mut()}); err != nil {
		t.Fatal(err)
	}
	var sawCount bool
	for _, a := range testArgs {
		if a == "-count=1" {
			sawCount = true
		}
	}
	if !sawCount {
		t.Errorf("go %v — without -count=1 a cached result can be read as the mutation surviving", testArgs)
	}
}

// Everything above drives a fake toolchain. Nothing there would notice if the
// real Runner stopped invoking `go` at all — the seam's default is the half of a
// seam nobody looks at, and this repository has already shipped one that was
// quietly a no-op.
func TestNewRunnerActuallyRunsTheGoToolchain(t *testing.T) {
	r := NewRunner(t.TempDir())
	out, _, err := r.Go("env", "GOMOD")
	if err != nil {
		t.Fatalf("the wired command did not run: %v (%s)", err, out)
	}
	if strings.TrimSpace(out) == "" {
		t.Errorf("`go env GOMOD` printed nothing — something other than the toolchain ran: %q", out)
	}
	// And a failing command must come back as an error, or every mutated tree
	// would look like it built.
	if _, _, err := r.Go("this-is-not-a-go-subcommand"); err == nil {
		t.Error("a command that fails must report an error, or the build gate never fires")
	}
	// stdout and stderr must stay apart: the test output is parsed as JSON events
	// and a stray diagnostic folded into it can swallow a `fail` line, which is
	// the one thing this tool reads.
	so, se, _ := r.Go("this-is-not-a-go-subcommand")
	if so != "" || se == "" {
		t.Errorf("the complaint should be on stderr alone, got stdout=%q stderr=%q", so, se)
	}
}
