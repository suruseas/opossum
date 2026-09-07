package mutate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
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
				// vet writes plain text, as the real one does — and it names the
				// package it is about on a line of its own before it says
				// anything else. Without that line the shim's output is what a
				// bare tail of the capture would also produce, so a run that
				// stopped dropping the headers would look identical here.
				return "", "# example.com/p\nvet: ./x.go:3:2: undefined: gone", boolErr(f.vetFails[current])
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
	// And kept the way the table wants it: the reason, without the line that
	// only names the package. Taking the tail of the capture would pass every
	// check above and put "# example.com/p" in front of the reason.
	if want := "vet: ./x.go:3:2: undefined: gone"; got[0].Detail != want {
		t.Errorf("Detail = %q, want %q — the sweep should hand the table the reason, not the capture", got[0].Detail, want)
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

// The interrupt handler cancels the toolchain first and asks for the restore
// second. The killed `go vet` (or `go test`) hands control back to the sweep,
// whose own deferred restore can take the lock before the handler does — so
// the handler finds nothing pending and says "no mutation in flight" about a
// file that was mutated a moment ago. The tree was right, the message was
// wrong (seen on CI: a probe reported as "did not compile", then "no probe in
// flight"). Two things hold here: the killed run is not turned into a Result,
// and the handler is still told the file was put back.
func TestAnInterruptThatLandsInsideTheToolchainRunIsStillReportedAsPutBack(t *testing.T) {
	for _, phase := range []string{"vet", "test"} {
		t.Run("during "+phase, func(t *testing.T) {
			f := newFake(t, map[string]string{"x.go": "call()"})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.Runner.Ctx = ctx
			f.Runner.Go = func(args ...string) (string, string, error) {
				if args[0] == phase && mustGet(t, f, "x.go") == "noop()" {
					// The interrupt: the handler cancels, the child dies with
					// nothing on its streams, and the sweep gets the lock first.
					cancel()
					return "", "", errors.New("signal: interrupt")
				}
				return passJSON, "", nil
			}
			results, err := f.Sweep([]Mutation{mut()})
			if !errors.Is(err, errInterrupted) {
				t.Errorf("Sweep returned %v, want errInterrupted — a killed %s run is not a finding", err, phase)
			}
			for _, res := range results {
				t.Errorf("the killed run was reported as %q (%s); it measured nothing", res.Outcome, res.Detail)
			}
			if got := mustGet(t, f, "x.go"); got != "call()" {
				t.Fatalf("the sweep left the file as %q", got)
			}
			restored, rerr := f.RestorePending()
			if rerr != nil {
				t.Errorf("RestorePending: %v", rerr)
			}
			if !restored {
				t.Error("RestorePending said nothing was in flight — the file was mutated when " +
					"the interrupt arrived, and the author is about to be told the tree was never touched")
			}
		})
	}
}

// The other half of the same moment: the sweep got the lock first, tried to put
// the file back, and could not. The handler must then be told the file is still
// mutated — not "put back" (the flag must not be set on a failed restore) and
// not "nothing in flight" (the failure must be remembered), because the author
// reads that message and decides whether to look at `git diff`.
func TestARestoreThatFailsAfterTheCancelIsToldToTheHandler(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Runner.Ctx = ctx
	f.Runner.Go = func(args ...string) (string, string, error) {
		if args[0] == "vet" && mustGet(t, f, "x.go") == "noop()" {
			cancel()
			return "", "", errors.New("signal: interrupt")
		}
		return passJSON, "", nil
	}
	inner := f.Runner.Write
	f.Runner.Write = func(p string, b []byte) error {
		if string(b) == "call()" && ctx.Err() != nil {
			return errors.New("disk full")
		}
		return inner(p, b)
	}
	_, err := f.Sweep([]Mutation{mut()})
	if err == nil || !strings.Contains(err.Error(), "STILL MUTATED") {
		t.Errorf("Sweep returned %v, want the failed restore named", err)
	}
	if got := mustGet(t, f, "x.go"); got != "noop()" {
		t.Fatalf("the fixture did not fail the restore: x.go = %q", got)
	}
	restored, rerr := f.RestorePending()
	if restored {
		t.Error("RestorePending said the file was put back; it is still mutated")
	}
	if rerr == nil || !strings.Contains(rerr.Error(), "STILL MUTATED") {
		t.Errorf("RestorePending err = %v, want the failed restore — the handler reads this to say COULD NOT BE PUT BACK", rerr)
	}
}

// The same answer must not appear when nothing was in flight at the interrupt:
// a run that failed on its own, before any cancellation, is a finding, and a
// later interrupt finds the tree as the author left it.
func TestAToolchainFailureBeforeTheInterruptIsStillAFinding(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Runner.Ctx = ctx
	f.vetFails["noop()"] = true
	results, err := f.Sweep([]Mutation{mut()})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != Broken {
		t.Fatalf("results = %+v, want the one mutation reported as not compiling", results)
	}
	cancel()
	restored, rerr := f.RestorePending()
	if rerr != nil {
		t.Errorf("RestorePending: %v", rerr)
	}
	if restored {
		t.Error("RestorePending said a file was put back — nothing was in flight when the interrupt came")
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
	if !strings.Contains(out, "**none — this defect is invisible to the suite**") {
		t.Errorf("a survivor must say so in words, and in the bold that makes the cell "+
			"impossible to skim past — the words alone were all this checked:\n%s", out)
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
	out := Report([]Result{
		{Mutation: Mutation{Name: "a|b"}, Outcome: Survived},
		// Every cell this table builds from something an author or a toolchain
		// wrote: the mutation name, the test names, and the detail carried by a
		// survivor, by a run that named nobody, and by one that would not build.
		// The last two are quoted from `go test` and `go vet`, which nobody
		// chooses the wording of and which arrive with pipes and line breaks in
		// them.
		{Mutation: Mutation{Name: "m"}, Outcome: Caught, Killers: []string{"TestA|B", "TestC"}},
		{Mutation: Mutation{Name: "m"}, Outcome: Survived, Detail: "reach|unknown"},
		{Mutation: Mutation{Name: "m"}, Outcome: Inconclusive, Detail: "panic|timeout"},
		{Mutation: Mutation{Name: "m"}, Outcome: Broken, Detail: "vet: a.go:1:1: bad|worse\nvet: b.go:2:2: also"},
	})
	// A header, its rule, and one row per result. A detail that broke out of its
	// row would make more; a row that vanished would make fewer.
	if want := 2 + 5; len(strings.Split(strings.TrimRight(out, "\n"), "\n")) != want {
		t.Errorf("%d lines, and this table is a header, a rule and five rows:\n%s",
			len(strings.Split(strings.TrimRight(out, "\n"), "\n")), out)
	}
	for _, raw := range []string{"| a|b |", "TestA|B", "reach|unknown", "panic|timeout", "bad|worse"} {
		if strings.Contains(out, raw) {
			t.Errorf("the pipe in %q should be escaped:\n%s", raw, out)
		}
	}
	for _, escaped := range []string{`a\|b`, `TestA\|B`, `reach\|unknown`, `panic\|timeout`, `bad\|worse`} {
		if !strings.Contains(out, escaped) {
			t.Errorf("%s should still be readable:\n%s", escaped, out)
		}
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

// What the profile can speak for is a place — a line and a column — not a line.
//
// A coverage block runs from one column of one line to another column of another,
// and both ends can land in the middle of a line that has more on it. The block
// for an if-body ends at the closing brace, and an `else` on that same line
// belongs to no block: matched by line alone it inherits the if-body's count,
// which on a run that took the else branch is zero. A line the tests take every
// time is then written up as one nothing reaches.
//
// Decided one way only. A match that is ambiguous, a file the profile never
// mentions, a place no block covers — all of them say nothing, because saying
// "nothing reaches this" about code that runs sends the reader after the wrong
// problem, and saying nothing costs a note in a table.
func TestWhichPlacesTheProfileCanSpeakFor(t *testing.T) {
	// The last two lines of d/f.go are one block listed twice. Real profiles do
	// that: with -coverpkg every test binary reports on every package it was told
	// to count, so a package exercised by another package's tests comes back
	// counted zero by its own binary and counted by theirs.
	//
	// e/f.go is the shape that needs columns: a block that ends at the first
	// column of line 51, with code after it on that same line.
	const prof = "mode: set\n" +
		"example.com/m/internal/a/f.go:10.5,12.20 2 1\n" +
		"example.com/m/internal/a/f.go:20.5,22.20 2 0\n" +
		"example.com/m/internal/b/f.go:30.1,31.1 1 1\n" +
		"example.com/m/internal/d/f.go:40.1,41.1 1 0\n" +
		"example.com/m/internal/d/f.go:40.1,41.1 1 1\n" +
		"example.com/m/internal/e/f.go:50.3,51.1 1 0\n"
	for _, tc := range []struct {
		name         string
		file         string
		at           Pos
		ran, decided bool
	}{
		{"a place that ran", "internal/a/f.go", Pos{11, 1}, true, true},
		{"a place in a block nothing ran", "internal/a/f.go", Pos{21, 1}, false, true},
		{"where a block begins", "internal/a/f.go", Pos{10, 5}, true, true},
		// Before the block begins, on the block's own first line.
		{"left of where a block begins", "internal/a/f.go", Pos{10, 4}, false, false},
		{"inside a block's last line", "internal/a/f.go", Pos{12, 19}, true, true},
		// The end column is where the block stops, not part of it.
		{"where a block ends", "internal/a/f.go", Pos{12, 20}, false, false},
		// The `else` case: the only block naming line 51 stops at column 1.
		{"past the end of the only block on the line", "internal/e/f.go", Pos{51, 2}, false, false},
		{"inside that block", "internal/e/f.go", Pos{50, 3}, false, true},
		{"a line no block covers", "internal/a/f.go", Pos{99, 1}, false, false},
		{"a line before every block in the file", "internal/a/f.go", Pos{1, 1}, false, false},
		// Counted zero by one binary and counted by another: something ran it.
		{"a block listed twice, run by one of them", "internal/d/f.go", Pos{40, 1}, true, true},
		// Two files end the same way, so the tail cannot say which is meant.
		{"a name that matches more than one file", "f.go", Pos{11, 1}, false, false},
		// The profile never mentions it — the package may not have been run at all.
		{"a file the profile does not mention", "internal/c/f.go", Pos{1, 1}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ran, decided := Executed([]byte(prof), tc.file, tc.at)
			if ran != tc.ran || decided != tc.decided {
				t.Errorf("Executed(%q, %d.%d) = (%v, %v), want (%v, %v)",
					tc.file, tc.at.Line, tc.at.Col, ran, decided, tc.ran, tc.decided)
			}
		})
	}
}

// The line a mutation lands on, counted from the byte it starts at.
func TestTheLineAMutationLandsOn(t *testing.T) {
	src := []byte("package m\n\nfunc A() int {\n\treturn 1\n}\n")
	for _, tc := range []struct {
		name string
		at   int
		want int
	}{
		{"the first byte", 0, 1},
		{"just before a newline", 8, 1},
		{"just after a newline", 10, 2},
		{"inside the body", bytes.Index(src, []byte("return")), 4},
		{"the last byte", len(src), 6},
		// Not a position in this file at all: say nothing rather than a number
		// that reads like an answer.
		{"past the end", len(src) + 1, 0},
		{"before the start", -1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := LineOf(src, tc.at); got != tc.want {
				t.Errorf("LineOf(%d) = %d, want %d", tc.at, got, tc.want)
			}
		})
	}
}

// The baseline and the run that decides a mutation's fate are instrumented the
// same way, and only the baseline writes a profile.
//
// Coverage costs time. A baseline measured in a cheaper configuration than the
// runs it vouches for is no baseline at all: a test with a deadline in it passes
// there and fails under every mutation, and the sweep reports a suite that caught
// everything. So the instrumentation has to match.
//
// Only one profile is taken, and it is the baseline's, because which lines the
// tests run is a question about the tree the author wrote. Reading it from a
// mutated tree lets a mutation silence its own line and be reported as code no
// test reaches.
func TestTheBaselineAndTheDecidingRunAreInstrumentedAlike(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	f.testOut["noop()"] = passJSON
	var runs [][]string
	inner := f.Runner.Go
	f.Runner.Go = func(args ...string) (string, string, error) {
		if args[0] == "test" {
			runs = append(runs, append([]string(nil), args...))
		}
		return inner(args...)
	}

	if _, err := f.Sweep([]Mutation{mut()}); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("the suite ran %d times for one mutation; it should be the baseline and the "+
			"mutation, and a second measured run could disagree with the first", len(runs))
	}
	instrumented := func(args []string) (cover bool, pkg string, profiles int) {
		for _, a := range args {
			switch {
			case a == "-cover":
				cover = true
			case strings.HasPrefix(a, "-coverprofile="):
				cover, profiles = true, profiles+1
			case strings.HasPrefix(a, "-coverpkg="):
				pkg = a
			}
		}
		return
	}
	baseCover, basePkg, baseProfiles := instrumented(runs[0])
	mutCover, mutPkg, mutProfiles := instrumented(runs[1])
	if !baseCover || !mutCover {
		t.Errorf("instrumented: baseline %v, mutation %v — a baseline run in a cheaper "+
			"configuration vouches for a suite nobody else runs", baseCover, mutCover)
	}
	if basePkg != mutPkg {
		t.Errorf("the two runs disagree about which packages are counted: %q and %q", basePkg, mutPkg)
	}
	if basePkg == "" {
		t.Error("no -coverpkg: a package exercised only by another package's tests comes back " +
			"counted zero, and a live survivor is then reported as a line nothing reaches")
	}
	if baseProfiles != 1 || mutProfiles != 0 {
		t.Errorf("profiles written: baseline %d, mutation %d — reach is a question about the "+
			"tree as it was, so the baseline's is the only one that answers it", baseProfiles, mutProfiles)
	}
}

// A suite that passes plain and fails when measured is stopped at the baseline,
// not reported as a suite that catches everything.
//
// Coverage instrumentation makes the tests slower — enough that a test written
// with a deadline in it can pass one way and fail the other. If the baseline is
// run plain while the run that decides is measured, the baseline sees green, and
// then every mutation is red for a reason that has nothing to do with the
// mutation. The sweep reports that the suite caught all of them and exits 0: the
// most reassuring thing it can say, and it measured nothing.
//
// The baseline exists to notice exactly this, and it can only notice it if it is
// run the same way as the run it is a baseline for.
func TestASuiteThatOnlyFailsWhenMeasuredIsCaughtByTheBaseline(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	f.testOut["noop()"] = passJSON
	inner := f.Runner.Go
	f.Runner.Go = func(args ...string) (string, string, error) {
		for _, a := range args {
			if strings.HasPrefix(a, "-coverprofile=") {
				// The deadline in the test does not survive the instrumentation.
				return failJSON("TestFinishesInTime"), "", errors.New("exit 1")
			}
		}
		return inner(args...)
	}

	res, err := f.Sweep([]Mutation{mut()})
	if err == nil {
		t.Fatalf("the sweep ran to the end and reported %v; a suite that is red the way the "+
			"mutations are run cannot be a baseline for them", res)
	}
	if !strings.Contains(err.Error(), "TestFinishesInTime") {
		t.Errorf("the error should name the test that is already failing, got: %v", err)
	}
	for _, r := range res {
		if r.Outcome == Caught {
			t.Errorf("%q was reported as caught, by a test that fails without it", r.Mutation.Name)
		}
	}
}

// When the reach could not be measured, the report says so.
//
// The outcome is `Survived` either way — whether the tests reach the line is a
// note, not a verdict. But a survivor with nothing beside it reads as "a test
// runs this line and does not mind the defect", and that is a stronger claim
// than what happened. Saying that the reach could not be measured is the
// difference between a survivor and a survivor nobody could check.
func TestASurvivorWhoseReachCouldNotBeMeasuredSaysSo(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	f.testOut["noop()"] = passJSON
	inner := f.Runner.Go
	f.Runner.Go = func(args ...string) (string, string, error) {
		for _, a := range args {
			if p, ok := strings.CutPrefix(a, "-coverprofile="); ok {
				// A run that wrote no profile: killed, out of disk, a toolchain
				// that never got as far as writing one.
				os.Remove(p)
			}
		}
		return inner(args...)
	}

	res, err := f.Sweep([]Mutation{mut()})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(res) != 1 || res[0].Outcome != Survived {
		t.Fatalf("an unmeasurable reach must leave the louder reading, got %v", res)
	}
	if !strings.Contains(res[0].Detail, "could not be measured") {
		t.Errorf("the report should say the reach was not ruled out, got detail %q", res[0].Detail)
	}
}

// What a survivor's report could not establish is said in the table, not only in
// the running log.
//
// The table is what gets pasted into a pull request; the log scrolls past in a
// terminal. A survivor whose reach could not be measured is a weaker finding than
// one whose reach was measured — "invisible to the suite" is the louder of two
// readings there, not an established one — and the difference has to survive the
// trip into the report or it may as well not have been recorded.
func TestASurvivorsUnmeasuredReachSurvivesIntoTheTable(t *testing.T) {
	out := Report([]Result{{
		Mutation: Mutation{Name: "the wire is cut"},
		Outcome:  Survived,
		Detail:   "whether any test reaches m.go:5 could not be measured",
	}})
	if !strings.Contains(out, "could not be measured") {
		t.Errorf("the table dropped what the run could not establish:\n%s", out)
	}
	if !strings.Contains(out, "invisible to the suite") {
		t.Errorf("it is still a survivor and the table should say so:\n%s", out)
	}
}

// A mutation is measured where it actually changes something, and nowhere else.
//
// A pattern has to match exactly once, and the way to make it match once is to
// widen it, which the guidance for writing one says to do. Widened, it carries
// lines the mutation leaves exactly as they were — above it, below it, and in
// between when one mutation changes two places at once — and every one of them
// borrows in a direction that misleads. Above and below: a change the tests run,
// written up as unreachable code. In between: a change nothing runs, passing for
// one the tests watch.
//
// The column matters as much as the line. A pattern rarely starts at the left
// margin, and the block that covers the rest of that line may not cover where the
// pattern begins.
func TestWhereAMutationChangesSomething(t *testing.T) {
	src := []byte("package m\n\nfunc Total(xs []int) int {\n\ttotal := 0\n\n\tfor _, x := range xs {\n\t\ttotal += x\n\t}\n\treturn total\n}\n")
	for _, tc := range []struct {
		name, from, to string
		want           []Pos
	}{
		{"the pattern is the change", "return total", "return 0", []Pos{{9, 9}}},
		// The pattern starts on 8; nothing on 8 changes.
		{"widened with the closing brace above", "\t}\n\treturn total", "\t}\n\treturn 0", []Pos{{9, 9}}},
		// The pattern starts on 4, a line the tests certainly run.
		{"widened with a line of code above", "total := 0\n\n\tfor _, x := range xs {", "total := 0\n\n\tfor _, x := range nil {", []Pos{{6, 20}}},
		{"widened with a blank line above", "\n\tfor _, x := range xs {", "\n\tfor _, x := range nil {", []Pos{{6, 20}}},
		{"past the indentation, where the change is", "\t\ttotal += x", "\t\ttotal -= x", []Pos{{7, 9}}},
		{"the replacement only removes", "total += x", "total", []Pos{{7, 8}}},
		// Two changes, four lines apart, with an unchanged line carried between
		// them. The unchanged one is not part of the mutation.
		{"two changes with a passenger between", "total := 0\n\n\tfor _, x := range xs {\n\t\ttotal += x", "total := 1\n\n\tfor _, x := range xs {\n\t\ttotal -= x", []Pos{{4, 11}, {7, 9}}},
		// Different numbers of lines: which became which is a guess, so the whole
		// span counts as changed. An unchanged line in it can still silence the
		// note, which is the quiet direction.
		{"the replacement has more lines", "return total", "x := total\n\treturn x", []Pos{{9, 2}}},
		// The replacement is shorter, so whole lines go. Each of them is asked
		// about where its code starts: at the margin they would fall outside every
		// block and answer nothing — and these are exactly the lines that can show
		// the change being run and let the note fall silent.
		{"the replacement has fewer lines", "\tfor _, x := range xs {\n\t\ttotal += x\n\t}", "\ttotal = 0",
			[]Pos{{6, 2}, {7, 3}, {8, 2}}},
		// The pattern ends at a line boundary. It does not reach into the line
		// after it, and a place invented there is about code it never touched.
		{"the pattern ends with a break", "\tfor _, x := range xs {\n\t\ttotal += x\n\t}\n", "",
			[]Pos{{6, 2}, {7, 3}, {8, 2}}},
		{"a pattern that is not there", "return nothing", "x", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := changedPlaces(src, tc.from, tc.to)
			if len(got) != len(tc.want) {
				t.Fatalf("changedPlaces(%q -> %q) = %v, want %v", tc.from, tc.to, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("changedPlaces(%q -> %q)[%d] = %d.%d, want %d.%d — this is where the "+
						"note about reaching the mutation is looked up",
						tc.from, tc.to, i, got[i].Line, got[i].Col, tc.want[i].Line, tc.want[i].Col)
				}
			}
		})
	}
}

// Whatever the coverage says, a mutation the suite let through is a survivor.
//
// This is the guarantee the note rests on, and the reason it is allowed to be
// imprecise. Deciding the reach turned out to be hard: six attempts at making it
// an outcome produced six ways of calling a line the tests run a line nothing
// reaches, and each of those sent a reader off to work out why live code was
// dead. As a note it can still be wrong, and the reader is looking at a survivor
// with a misleading hint beside it — the work in front of them, writing the test,
// does not change.
//
// That only holds while nothing downstream reads the note. Pinned here because
// the cheap way to make the reach useful again is to let it decide something,
// and this is the line that must not be crossed.
func TestTheReachNeverDecidesTheOutcome(t *testing.T) {
	const reached = "mode: set\nexample.com/m/x.go:1.1,99.1 1 1\n"
	const notReached = "mode: set\nexample.com/m/x.go:1.1,99.1 1 0\n"
	for _, tc := range []struct{ name, profile string }{
		{"the coverage says the line runs", reached},
		{"the coverage says nothing runs it", notReached},
		{"there is no coverage at all", ""},
		{"the coverage is unreadable", "not a profile\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, map[string]string{"x.go": "call()"})
			f.testOut["noop()"] = passJSON
			f.Runner.reach = []byte(tc.profile)
			// The baseline overwrites reach, so hold this one in place.
			inner := f.Runner.Go
			f.Runner.Go = func(args ...string) (string, string, error) {
				out, errOut, err := inner(args...)
				f.Runner.reach = []byte(tc.profile)
				return out, errOut, err
			}

			res, err := f.Sweep([]Mutation{mut()})
			if err != nil {
				t.Fatalf("Sweep: %v", err)
			}
			if len(res) != 1 || res[0].Outcome != Survived {
				t.Fatalf("the suite passed with the defect in place; that is a survivor whatever "+
					"the coverage says, got %v", res)
			}
		})
	}
}

// The note names the lines the coverage answered for, and no others.
//
// Two ways to name more than was measured. A range takes in the untouched lines
// a widened pattern carried between two changes — asked about deliberately, and
// deliberately left out — so a reader who checks one of them finds the note
// saying something false about it. And among the lines that were asked about,
// the ones no coverage block covers were not ruled out by anything: listed
// beside the ones that were, they borrow a certainty the profile never gave.
func TestTheNoteNamesOnlyWhatWasMeasured(t *testing.T) {
	// Lines 10 and 30 are covered and unrun. Line 20, between them, is covered
	// and run. Line 40 is in no block at all.
	const prof = "mode: set\n" +
		"example.com/m/x.go:10.1,10.9 1 0\n" +
		"example.com/m/x.go:20.1,20.9 1 1\n" +
		"example.com/m/x.go:30.1,30.9 1 0\n"
	r := &Runner{reach: []byte(prof)}
	for _, tc := range []struct {
		name   string
		places []Pos
		want   string
	}{
		{"two unrun lines with a run one between", []Pos{{10, 1}, {30, 1}},
			"no test this sweep ran appears to reach x.go:10, 30, in the baseline's coverage"},
		{"one of them unrun, one unmeasured", []Pos{{10, 1}, {40, 1}},
			"no test this sweep ran appears to reach x.go:10, in the baseline's coverage"},
		{"nothing measured at all", []Pos{{40, 1}, {41, 1}},
			"whether any test reaches x.go:40, 41 could not be measured"},
		{"one of them ran", []Pos{{10, 1}, {20, 1}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.noteFor("x.go", tc.places); got != tc.want {
				t.Errorf("note = %q, want %q", got, tc.want)
			}
		})
	}
}

// The tally is the half of the report that a pull request quotes as a sentence,
// so it is pinned word for word: a number that moved, a category that vanished,
// or a note that stopped explaining itself all read as prose and all change what
// the reader believes was measured.
func TestTheTallyIsWordForWordWhatWeMeanToSay(t *testing.T) {
	// The two killers are named so that ordering them by how much they caught and
	// ordering them alphabetically disagree. Named the other way round, a tally
	// that had stopped counting at all would still print these two rows in this
	// order and this golden would not notice.
	// Two orderings are being pinned here, and both need inputs that can tell
	// their sorted and unsorted forms apart.
	//
	// The rows arrive worst-outcome-first so that listing the categories in the
	// order they were first seen and listing them in the order the outcomes are
	// declared give different sentences. Written the other way round — caught
	// first, as a passing sweep tends to come out — the sort is a line this
	// golden cannot see, which is how the same oversight survived once already in
	// the column below.
	//
	// The killers are named so that ordering them by how much they caught and
	// ordering them alphabetically disagree, and one of them carries a pipe: a
	// name that ends its column early shifts every cell after it, and the table
	// is markdown read by people.
	got := Tally([]Result{
		{Mutation: Mutation{Name: "a field is deleted"}, Outcome: Broken},
		{Mutation: Mutation{Name: "the wait is gone"}, Outcome: Survived},
		{Mutation: Mutation{Name: "the wire is cut"}, Outcome: Caught, Killers: []string{"TestWire|Cut", "TestAnswer"}},
		{Mutation: Mutation{Name: "the second look never happens"}, Outcome: Caught, Killers: []string{"TestWire|Cut"}},
	})
	want := strings.Join([]string{
		"",
		"**4 mutations: 2 caught, 1 SURVIVED, 1 did not compile.**",
		"",
		"| test | mutations it caught |",
		"|---|---|",
		`| TestWire\|Cut | 2 |`,
		"| TestAnswer | 1 |",
		"",
		"Each row counts the mutations that test failed on; a mutation two tests caught appears in both rows.",
		"",
	}, "\n")
	if got != want {
		t.Errorf("the tally is not what this file says it should be\n got:\n%s\nwant:\n%s", got, want)
	}
}

// The reason this exists is that a total and a breakdown written separately can
// disagree. They cannot here, and this is the test that says so: every result
// contributes to exactly one category, so the categories add to the count of
// results whatever the results are.
func TestTheTallysPartsAddUpToItsTotal(t *testing.T) {
	for name, rs := range map[string][]Result{
		// One result: the sentence changes shape there, and a shape that stopped
		// carrying a total would take this check with it.
		"one":            {{Outcome: Caught, Killers: []string{"T"}}},
		"nothing caught": {{Outcome: Survived}, {Outcome: Survived}, {Outcome: Broken}},
		"all four kinds": {
			{Outcome: Caught, Killers: []string{"T"}}, {Outcome: Survived},
			{Outcome: Broken}, {Outcome: Inconclusive}, {Outcome: Caught, Killers: []string{"U"}},
		},
		// A kind this file made up, twice over: whatever the results are means
		// results this function has no name for, and those have to add up too.
		"kinds nobody named": {
			{Outcome: Caught, Killers: []string{"T"}}, {Outcome: Outcome(91)},
			{Outcome: Outcome(92)}, {Outcome: Outcome(91)},
		},
	} {
		// Subtests, so that a set that comes back malformed does not take the
		// remaining sets with it: which inputs were measured is the thing this
		// test is claiming.
		t.Run(name, func(t *testing.T) {
			line := regexp.MustCompile(`\*\*(\d+) mutations?: ([^*]+)\.\*\*`).FindStringSubmatch(Tally(rs))
			if line == nil {
				t.Fatalf("no total in the tally for %d results:\n%s", len(rs), Tally(rs))
			}
			total, err := strconv.Atoi(line[1])
			if err != nil {
				t.Fatal(err)
			}
			if total != len(rs) {
				t.Errorf("the tally counted %d of %d results", total, len(rs))
			}
			sum := 0
			for _, part := range strings.Split(line[2], ", ") {
				n, err := strconv.Atoi(strings.Fields(part)[0])
				if err != nil {
					t.Fatalf("%q does not start with a number", part)
				}
				sum += n
			}
			if sum != total {
				t.Errorf("the parts of %q add to %d, not %d — a total and a breakdown that disagree "+
					"is the accident this function exists to make impossible", line[0], sum, total)
			}
		})
	}
}

// Which assertion caught a mutation is the thing the author gets wrong when
// writing it out by hand: thirteen mutations were once described as guarded by
// one golden when two of them were guarded by a different check in the same test.
// A mutation two tests caught belongs under both, and saying otherwise would
// under-report one of them.
func TestTheTallyCountsAMutationUnderEveryTestThatCaughtIt(t *testing.T) {
	got := Tally([]Result{
		{Outcome: Caught, Killers: []string{"TestGolden", "TestSummary"}},
		{Outcome: Caught, Killers: []string{"TestGolden"}},
	})
	if !strings.Contains(got, "| TestGolden | 2 |") {
		t.Errorf("a test that caught both should say 2:\n%s", got)
	}
	if !strings.Contains(got, "| TestSummary | 1 |") {
		t.Errorf("a test that caught one should say 1:\n%s", got)
	}
	if !strings.Contains(got, "appears in both rows") {
		t.Errorf("the double count has to explain itself, or the column reads as a partition:\n%s", got)
	}
}

// A sweep that stopped before it measured anything must not print a sentence
// with a number in it. "0 mutations" reads like a sweep that ran and found
// nothing to say.
func TestTheTallyOfNothingSaysNothing(t *testing.T) {
	if got := Tally(nil); got != "" {
		t.Errorf("Tally(nil) = %q, want nothing at all", got)
	}
}

// "1 mutations" in a sentence a pull request quotes. The plural is pinned here
// because the singular reaches the report through a branch nothing in this
// package was reading: the end-to-end test in cmd caught it, at forty times the
// cost, and only because a sweep of one happens to be what it runs.
func TestTheTallyCountsOneMutationInTheSingular(t *testing.T) {
	if got := Tally([]Result{{Outcome: Survived}}); !strings.Contains(got, "**1 mutation: 1 SURVIVED.**") {
		t.Errorf("one is not mutations:\n%s", got)
	}
	if got := Tally([]Result{{Outcome: Survived}, {Outcome: Survived}}); !strings.Contains(got, "**2 mutations:") {
		t.Errorf("two are:\n%s", got)
	}
}

// The counting is done by walking the results, not by asking after each kind of
// outcome in turn, and this is the test that says so: a kind this file invents
// on the spot has to come out in the sentence without Tally being told about it.
//
// Written the other way — a list of the four kinds, counted one at a time — the
// day a fifth is added is the day the total stops matching its parts, quietly,
// in a sentence whose whole job is that they match.
func TestTheTallyCountsAKindItWasNeverToldAbout(t *testing.T) {
	// Not one of the four. String() has no name for it and says so rather than
	// reading it as the nearest one it does know.
	const invented = Outcome(97)
	got := Tally([]Result{
		{Outcome: Caught, Killers: []string{"TestA"}},
		{Outcome: invented},
		{Outcome: invented},
	})
	if !strings.Contains(got, "3 mutations: 1 caught, 2 outcome 97.") {
		t.Errorf("an outcome nobody named should still be counted and named:\n%s", got)
	}
	if strings.Contains(got, "inconclusive") {
		t.Errorf("an unnamed outcome read as one of the four is the quiet way to be wrong:\n%s", got)
	}
	// The named one still has its name. Splitting it out of the default is what
	// made the line above possible, and it left nothing saying the word had
	// survived the split.
	if got := Inconclusive.String(); got != "inconclusive" {
		t.Errorf("Inconclusive.String() = %q, want the word the table has always used", got)
	}
}

// The table above names no test against anything that is not caught — it puts
// "none", or "n/a" and a reason, in that cell — so a killer counted from one of
// those rows would put a test in this column that no row names. Sweep never
// builds one; Tally is exported and Result is a plain struct, so nothing but
// this stops it.
func TestTheTallyIgnoresKillersNoRowAboveNames(t *testing.T) {
	got := Tally([]Result{
		{Outcome: Caught, Killers: []string{"TestReal"}},
		{Outcome: Survived, Killers: []string{"TestImpossible"}},
		{Outcome: Broken, Killers: []string{"TestImpossible"}},
	})
	if strings.Contains(got, "TestImpossible") {
		t.Errorf("the table says nothing caught those, and this must not disagree:\n%s", got)
	}
	if !strings.Contains(got, "| TestReal | 1 |") {
		t.Errorf("the caught row still counts:\n%s", got)
	}
}

// A sweep where nothing was caught has no column to print, and a header with no
// rows under it reads like a measurement that came back empty rather than one
// that had nothing to measure.
func TestTheTallyPrintsNoColumnWhenNothingWasCaught(t *testing.T) {
	got := Tally([]Result{{Outcome: Survived}, {Outcome: Broken}})
	if strings.Contains(got, "| test |") {
		t.Errorf("no test caught anything, so there is no column:\n%s", got)
	}
	if !strings.Contains(got, "2 mutations: 1 SURVIVED, 1 did not compile.") {
		t.Errorf("the counts are still owed:\n%s", got)
	}
}

// The table and the counts under it are one report, and a reader takes a test
// named in one of them as named by both. They read a single rule about which
// rows carry killers; written out twice, the two copies drifted apart and the
// comment describing the table was wrong about the table.
func TestTheTableAndTheTallyNameTheSameTests(t *testing.T) {
	rs := []Result{
		{Mutation: Mutation{Name: "the wire is cut"}, Outcome: Caught, Killers: []string{"TestReal"}},
		// Killers on rows the table does not credit: Sweep never builds these,
		// but Result is a plain struct and both of these functions are exported,
		// so nothing else stops one of them from counting a name the other hides.
		{Mutation: Mutation{Name: "the wait is gone"}, Outcome: Survived, Killers: []string{"TestGhost"}},
		{Mutation: Mutation{Name: "something new"}, Outcome: Outcome(97), Killers: []string{"TestGhost"}},
	}
	report, tally := Report(rs), Tally(rs)
	for _, name := range []string{"TestReal", "TestGhost"} {
		if strings.Contains(report, name) != strings.Contains(tally, name) {
			t.Errorf("%s is in one half of this report and not the other:\n%s%s", name, report, tally)
		}
	}
	if !strings.Contains(report, "TestReal") {
		t.Errorf("the row that was caught still names its killer:\n%s", report)
	}
	if strings.Contains(report, "TestGhost") {
		t.Errorf("a row the table does not credit must not name a test anyway:\n%s", report)
	}
	if !strings.Contains(report, "an outcome this table has no name for") {
		t.Errorf("an outcome the table cannot name has to say so, not fall through:\n%s", report)
	}
}

// A mutation that would not build says why, in the table.
//
// The reason was captured all along and printed only in the running log. A table
// pasted into a pull request said "it did not compile" and stopped — on the row
// where a reader most needs the next step, because nothing about the tests was
// measured there.
func TestTheReportSaysWhyAMutationWouldNotBuild(t *testing.T) {
	out := Report([]Result{{
		Mutation: Mutation{Name: "the type stops matching"},
		Outcome:  Broken,
		Detail:   "vet: ./m.go:3:28: cannot use \"forty-two\" (untyped string constant) as int value",
	}})
	if !strings.Contains(out, "cannot use") || !strings.Contains(out, "m.go:3:28") {
		t.Errorf("the row should carry what vet said:\n%s", out)
	}
	if !strings.Contains(out, "not evidence") {
		t.Errorf("and still say that nothing was measured:\n%s", out)
	}
	// The two sit in one cell, so something has to join them. Checked on both
	// sides: a dash where there is a reason, and none where there is not. Only
	// the second half was checked at first, and a check for what is absent
	// passes just as well when the thing is absent everywhere.
	if !strings.Contains(out, "not evidence: no test ran — vet:") {
		t.Errorf("the reason is joined to the row, not dropped beside it:\n%s", out)
	}
	// A capture that came back empty is not a reason. The row should read as one
	// sentence either way, not as a dangling dash.
	bare := Report([]Result{{Mutation: Mutation{Name: "x"}, Outcome: Broken}})
	if strings.Contains(bare, "—") {
		t.Errorf("nothing to add, nothing added:\n%s", bare)
	}
}

// What `go vet` prints when it will not build begins by naming the package it
// is about — one line, and a second for the test build when the package has
// tests. Then it names the problem. The package lines are noise in a cell.
func TestWhyItWouldNotBuildDropsThePackageHeaders(t *testing.T) {
	const vet = "# example.com/m\n# [example.com/m]\nvet: ./m.go:3:28: undefined: nosuchthing"
	got := whyItWouldNotBuild(vet)
	if strings.HasPrefix(got, "#") || strings.Contains(got, "\n#") {
		t.Errorf("the package headers are not the reason: %q", got)
	}
	if !strings.Contains(got, "undefined: nosuchthing") {
		t.Errorf("the reason is: %q", got)
	}
	// A hash inside a line is not a header. The rule is what a header is — a
	// line that begins with one — and not where the character appears, because
	// vet quotes the source back and this repository has source with a `#` in
	// it. Read the other way, every problem line here would be dropped and the
	// cell would fall back to showing the headers it was written to remove.
	quoted := whyItWouldNotBuild("# example.com/h\n" +
		`vet: ./h.go:5:23: cannot use unreleasedHeading (untyped string constant "## [Unreleased]") as int value`)
	if !strings.Contains(quoted, "Unreleased") {
		t.Errorf("the problem quotes a heading; that does not make it one: %q", quoted)
	}
	if strings.HasPrefix(quoted, "#") {
		t.Errorf("the header still goes: %q", quoted)
	}
	// Several problems reported, and all of them kept: taking the last line
	// would read as one problem where there are three. This is the shape vet's
	// own findings arrive in — no header at all, one line each. The first is
	// the one to look for; the last survives "keep only the last line" too.
	two := whyItWouldNotBuild("a.go:6:14: Printf format %d has arg s of wrong type string\n" +
		"a.go:7:14: Printf format %s has arg n of wrong type int\n" +
		"a.go:8:14: Printf format %q has arg f of wrong type float64")
	for _, want := range []string{"wrong type string", "wrong type int", "wrong type float64"} {
		if !strings.Contains(two, want) {
			t.Errorf("three problems, three lines: %q missing from %q", want, two)
		}
	}
	two = whyItWouldNotBuild("# p\nvet: ./a.go:1:1: first\nvet: ./b.go:2:2: second")
	for _, want := range []string{"first", "second"} {
		if !strings.Contains(two, want) {
			t.Errorf("both problems belong in the cell, %q missing from %q", want, two)
		}
	}
	// Nothing but headers: better the headers than a row that will not say why.
	if got := whyItWouldNotBuild("# example.com/m\n# [example.com/m]"); got == "" {
		t.Error("a row that would not build has to say something")
	}
}

// More than three problems, and the cell says how many it is not showing.
//
// Cutting to the last three is a bound, and a bound that keeps quiet reads as
// the whole answer — which is the mistake this function exists to avoid one
// level up, where dropping the package headers was chosen over taking the last
// line for exactly that reason.
func TestWhyItWouldNotBuildSaysWhenItCutSomething(t *testing.T) {
	got := whyItWouldNotBuild("a.go:1:1: first\na.go:2:2: second\na.go:3:3: third\na.go:4:4: fourth\na.go:5:5: fifth")
	if !strings.Contains(got, "(2 more)") {
		t.Errorf("five problems and three shown: the cell owes the reader the count, got %q", got)
	}
	if !strings.Contains(got, "fifth") || strings.Contains(got, "first") {
		t.Errorf("the last three are the ones kept, got %q", got)
	}
	// Exactly three is not a cut.
	if got := whyItWouldNotBuild("a:1: x\nb:2: y\nc:3: z"); strings.Contains(got, "more)") {
		t.Errorf("nothing was cut, so nothing to say about it: %q", got)
	}
}

// A blank line in a capture is not a problem it reported.
func TestWhyItWouldNotBuildDropsBlankLines(t *testing.T) {
	got := whyItWouldNotBuild("# p\n\nvet: ./a.go:1:1: only this\n\n")
	if got != "vet: ./a.go:1:1: only this" {
		t.Errorf("one problem, one line, got %q", got)
	}
}

// Cancelling the context ends the toolchain, and "the toolchain" is two
// processes: `go test` builds and then runs a test binary, and the second is
// the one doing the work. This used to end neither — the field was declared and
// never read — and the three comments that said otherwise were the reason the
// next person believed the interrupt path was covered (#607).
//
// The failure it left is not tidiness. cmd/mutate's interrupt handler stops the
// toolchain and then takes the baseline worktree apart; a `go test` that is
// still writing into that tree turns the removal into "directory not empty",
// which is the shape #602 reports.
func TestCancellingTheContextEndsTheToolchainAndWhatItStarted(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a fixture module")
	}
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The pid makes the name answer to this run alone: a second instance of
	// this very test — another session running the suite — builds a different
	// name, so neither can find (or, in the cleanup below, kill) the other's
	// processes.
	marker := fmt.Sprintf("mutatectxfixture%d", os.Getpid())
	write("go.mod", "module "+marker+"\n\ngo 1.25\n")
	write("slow_test.go", "package "+marker+"\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\n"+
		"func TestSleeps(t *testing.T) { time.Sleep(120 * time.Second) }\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := NewRunner(root)
	r.Ctx = ctx

	returned := make(chan error, 1)
	go func() {
		_, _, err := r.Go("test", "-count=1", "./...")
		returned <- err
	}()

	// Wait for the test binary itself, not just for `go test`: cancelling
	// during the build would leave the second process untested, which is the
	// half that was broken.
	deadline := time.Now().Add(90 * time.Second)
	waited := time.Now()
	for len(testBinaries(t, marker)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the fixture's test binary never started, so cancellation was never asked the question")
		}
		time.Sleep(200 * time.Millisecond)
	}

	t.Logf("the fixture's test binary came up after %.2fs: %v", time.Since(waited).Seconds(), testBinaries(t, marker))
	cancel()
	select {
	case <-returned:
	case <-time.After(30 * time.Second):
		// The failure being reported is "cancel does not work", so cancel is
		// not the cleanup: kill what was found before failing, or the sleeper
		// sits for its full two minutes.
		for _, pid := range testBinaries(t, marker) {
			if p, err := os.FindProcess(pid); err == nil {
				p.Kill()
			}
		}
		t.Fatal("Go did not return within 30s of the context being cancelled")
	}
	// The test sleeps for two minutes, so anything still here outlived the run
	// that started it.
	for i := 0; i < 25; i++ {
		if len(testBinaries(t, marker)) == 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	left := testBinaries(t, marker)
	for _, pid := range left {
		if p, err := os.FindProcess(pid); err == nil {
			p.Kill()
		}
	}
	t.Fatalf("%d process(es) outlived the cancelled run: %v", len(left), left)
}

// testBinaries returns the pids of running test binaries built from the named
// module. The build path plus a -test. flag is what is matched, so neither a
// shell that merely mentions the name nor the toolchain still building the
// binary is counted.
func testBinaries(t *testing.T, module string) []int {
	t.Helper()
	out, err := exec.Command("ps", "-ax", "-o", "pid,command").Output()
	if err != nil {
		t.Fatalf("could not look for processes: %v", err)
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		// The running binary, not the build of it: the linker's own command
		// line — `link -o .../<module>.test -importcfg ...` — carries the same
		// path, comes up ~55ms earlier on a cold cache, and made the wait
		// below return before there was a test binary to stop (about one run
		// in twelve, measured). What only the running binary has is its
		// arguments: `go test` always passes -test.* flags.
		if !strings.Contains(line, "/"+module+".test -test.") {
			continue
		}
		var pid int
		if _, err := fmt.Sscan(strings.TrimSpace(line), &pid); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// Once the run is cancelled, the sweep must stop rather than keep writing
// mutations into a tree and reading "context canceled" as an answer about them.
// The rounds after cancellation measure nothing, and their writes land in a
// tree the canceller may already be taking apart — cmd/mutate's interrupt
// handler removes the baseline worktree while its runner would still be
// spinning here.
func TestACancelledSweepStopsInsteadOfMeasuringNothing(t *testing.T) {
	f := newFake(t, map[string]string{"x.go": "call()"})
	f.testOut["call()"] = passJSON
	f.testOut["noop()"] = failJSON("TestTheWireIsThere")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.Ctx = ctx

	var wrote int
	inner := f.Write
	f.Write = func(p string, b []byte) error { wrote++; return inner(p, b) }

	got, err := f.Sweep([]Mutation{mut(), mut()})
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("a cancelled sweep should say so, got results=%v err=%v", got, err)
	}
	if len(got) != 0 {
		t.Errorf("no mutation was measured, but %d results came back", len(got))
	}
	if wrote != 0 {
		t.Errorf("a cancelled sweep wrote into the tree %d time(s)", wrote)
	}
}

// The declared default: a Runner whose Ctx was set to nil runs uncancellably
// rather than panicking. NewRunner's Go reads the field at call time exactly so
// that callers can set it late — and the guard is what stands between "unset"
// and a nil dereference inside CommandContext.
func TestANilContextMeansUncancellableRatherThanPanic(t *testing.T) {
	r := NewRunner(t.TempDir())
	r.Ctx = nil
	out, _, err := r.Go("env", "GOMOD")
	if err != nil {
		t.Fatalf("a nil-ctx run should still run: %v (%s)", err, out)
	}
	if strings.TrimSpace(out) == "" {
		t.Errorf("`go env GOMOD` printed nothing: %q", out)
	}
}
