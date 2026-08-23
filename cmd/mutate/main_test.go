package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// spec writes a sweep file and returns its path.
func spec(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sweep.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A sweep file that does not describe a real sweep must stop the run rather than
// produce a report of nothing — an empty table reads exactly like "everything was
// caught".
func TestASweepThatSaysNothingIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"empty list":              `[]`,
		"not json":                `this is not json`,
		"a mutation with no name": `[{"file":"x.go","from":"a","to":"b","packages":["./p/"]}]`,
		// A mistyped key would leave the field empty and change what the sweep
		// does without saying so.
		"an unknown key": `[{"name":"n","file":"x.go","from":"a","to":"b","pkgs":["./p/"]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := run([]string{spec(t, body)}, &out, &errOut, nil, func(int) {}); code != exitFailed {
				t.Errorf("exit = %d, want %d (the run failed; nothing was measured)", code, exitFailed)
			}
			if out.Len() != 0 {
				t.Errorf("nothing was measured, so nothing should be reported: %q", out.String())
			}
			if errOut.Len() == 0 {
				t.Error("a refusal has to say why")
			}
		})
	}
}

func TestUsageIsAFailureNotAReport(t *testing.T) {
	var out, errOut bytes.Buffer
	for _, args := range [][]string{{}, {"a", "b"}} {
		if code := run(args, &out, &errOut, nil, func(int) {}); code != exitFailed {
			t.Errorf("run(%v) exit = %d, want %d", args, code, exitFailed)
		}
	}
	if !strings.Contains(errOut.String(), "usage:") {
		t.Errorf("stderr should show the usage, got %q", errOut.String())
	}
}

// A file that does not exist is the run failing, not a finding.
func TestAMissingSweepFileIsAFailure(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{filepath.Join(t.TempDir(), "absent.json")}, &out, &errOut, nil, func(int) {}); code != exitFailed {
		t.Errorf("exit = %d, want %d", code, exitFailed)
	}
}

// The three statuses have to mean different things: a survivor is the tool doing
// its job and finding something, while a broken sweep is the tool not running at
// all. A script that cannot tell them apart will treat a broken sweep as a clean
// bill of health — which is the failure mode this whole package exists to stop.
func TestTheExitStatusesSayWhichOfTheThreeHappened(t *testing.T) {
	if exitAllCaught == exitSurvivor || exitSurvivor == exitFailed || exitAllCaught == exitFailed {
		t.Fatal("all caught, a survivor, and a failed run must be distinguishable")
	}
	if exitAllCaught != 0 {
		t.Errorf("a sweep where everything was caught is a success, got %d", exitAllCaught)
	}
}

// A mutation that would not build, and a run that named nobody, measured nothing.
// Reporting either as 0 would say "everything is guarded" about a sweep that
// guarded nothing — and it hid a real defect: dropping `-json` makes every run
// name nobody, which was invisible while these exited 0.
func TestAMutationThatMeasuredNothingIsNotASuccess(t *testing.T) {
	mod := throwawayModule(t)
	for name, to := range map[string]string{
		"does not build": "func Answer() int { return undefinedThing() }",
		// A panic during package initialisation: the binary dies before any test
		// runs, so `go test` is red and names nobody.
		"names no tests": "func Answer() int { return 42 }\n\nfunc init() { panic(\"boom\") }",
	} {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run([]string{spec(t, sweepFor(to))}, &out, &errOut, nil, func(int) {})
			if code != exitFailed {
				t.Errorf("exit = %d, want %d — nothing was measured (%s)", code, exitFailed, out.String())
			}
		})
	}
	_ = mod
}

// The interrupt path is the one that was wrong, so it has to be reachable from a
// test: the command must listen for the signal at all, put the file back, and
// leave by the interrupted status.
//
// The handler runs on its own goroutine, so what it records has to be handed over
// rather than shared — a test that reads a plain slice here is a data race, and
// this one was.
// What an interrupt says it did. The branch that matters most is the one where
// nothing was in flight: with a baseline run in front of the sweep, that is the
// likeliest moment to interrupt, and it is the one where "the file has been put
// back" would be a lie.
func TestTheInterruptMessageSaysWhatActuallyHappened(t *testing.T) {
	for _, tc := range []struct {
		name     string
		restored bool
		err      error
		wantIn   string
		wantNot  string
	}{
		{"nothing in flight", false, nil, "no mutation in flight", "put back"},
		{"a file was restored", true, nil, "has been put back", "no mutation in flight"},
		{"the restore failed", true, errors.New("disk is full"), "COULD NOT BE PUT BACK", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := interruptMessage(tc.restored, tc.err)
			if !strings.Contains(got, tc.wantIn) {
				t.Errorf("message = %q, want it to say %q", got, tc.wantIn)
			}
			if tc.wantNot != "" && strings.Contains(got, tc.wantNot) {
				t.Errorf("message = %q, and it must not say %q", got, tc.wantNot)
			}
		})
	}
}

func TestAnInterruptRestoresTheFileAndExitsAsInterrupted(t *testing.T) {
	mod := throwawayModule(t)
	sigs := make(chan os.Signal, 1)
	exited := make(chan int, 4)
	var out bytes.Buffer
	errOut := &lockedBuf{}

	// Fire once the mutation is actually on disk, not after a delay. A timer lands
	// wherever the run happens to be, and when the baseline run was added in front
	// of the sweep it started landing there instead — leaving this test green while
	// no mutation had been written at all, which is the whole of what it is for.
	// The tell was that it got eight times faster.
	interrupted := make(chan bool, 1)
	go func() {
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
			b, _ := os.ReadFile(filepath.Join(mod, "m.go"))
			if strings.Contains(string(b), "return 43 }") {
				interrupted <- true
				sigs <- os.Interrupt
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		interrupted <- false
		sigs <- os.Interrupt // so the run does not sit here waiting for one
	}()
	// The window the mutation is on disk for is seconds wide (write, vet, then a
	// test that sleeps), and it takes a few seconds to get there, so twenty is
	// slack rather than a race. If it ever runs out, that is worth telling apart
	// from "the mutation was never written".

	run([]string{spec(t, sweepFor("func Answer() int { return 43 }"))}, &out, errOut, sigs,
		func(c int) { exited <- c })

	if !<-interrupted {
		b, _ := os.ReadFile(filepath.Join(mod, "m.go"))
		t.Fatalf("the interrupt never landed on a mutated tree: nothing wrote the mutation within "+
			"the deadline, so this test measured nothing. The file ended as %q — if that is the "+
			"original, the sweep never got as far as writing; if it is mutated, the deadline was "+
			"simply too short.", b)
	}

	select {
	case code := <-exited:
		if code != exitInterrupted {
			t.Errorf("exit = %d, want the interrupted status %d", code, exitInterrupted)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the signal was never heard: nothing tried to exit")
	}
	if s := errOut.String(); !strings.Contains(s, "put back") {
		t.Errorf("stderr = %q, want it to say the file was restored", s)
	}
	b, _ := os.ReadFile(filepath.Join(mod, "m.go"))
	if !strings.Contains(string(b), "return 42 }") {
		t.Errorf("the file was left mutated: %q", b)
	}
}

// lockedBuf is a bytes.Buffer two goroutines may write to — the handler writes
// its message while the sweep is still running.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// throwawayModule is a one-function module with a test that notices, and makes it
// the working directory for the duration.
func throwawayModule(t *testing.T) string {
	t.Helper()
	mod := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(mod, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.24\n")
	write("m.go", "package m\n\nfunc Answer() int { return 42 }\n")
	write("m_test.go", "package m\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\n"+
		"func TestAnswer(t *testing.T) {\n\ttime.Sleep(2 * time.Second)\n"+
		"\tif Answer() != 42 {\n\t\tt.Fatal(\"wrong\")\n\t}\n}\n")
	t.Chdir(mod)
	return mod
}

func sweepFor(to string) string {
	return `[{"name":"the answer changes","file":"m.go",` +
		`"from":"func Answer() int { return 42 }","to":` + quote(to) + `,"packages":["./..."]}]`
}

func TestASweepAgainstARealTreeReportsAndExits(t *testing.T) {
	mod := throwawayModule(t)

	for name, c := range map[string]struct {
		to   string
		want int
	}{
		"the test notices": {to: "func Answer() int { return 43 }", want: exitAllCaught},
		"nothing notices":  {to: "func Answer() int { return 42 /* same */ }", want: exitSurvivor},
	} {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run([]string{spec(t, sweepFor(c.to))}, &out, &errOut, nil, func(int) {})
			if code != c.want {
				t.Errorf("exit = %d, want %d (stderr: %s)", code, c.want, errOut.String())
			}
			if !strings.Contains(out.String(), "the answer changes") {
				t.Errorf("the report should name the mutation:\n%s", out.String())
			}
			// Whatever happened, the tree is as it was.
			b, _ := os.ReadFile(filepath.Join(mod, "m.go"))
			if !strings.Contains(string(b), "return 42 }") {
				t.Errorf("the file was left mutated: %q", b)
			}
		})
	}
}

func quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// A mutation on a line no test runs is reported as that, not as a survivor.
//
// Both come back with the suite green and they mean opposite things: a survivor
// says a test is missing where the code runs, and this says the code does not
// run. The tool could not tell them apart, so every unreached mutation read as a
// finding about the tests.
//
// End to end against a real toolchain, because the part that was missing is the
// wiring — asking for a profile, and reading it back — not the arithmetic.
func TestASurvivorNoTestReachesIsNotedAsOne(t *testing.T) {
	mod := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(mod, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.24\n")
	write("m.go", "package m\n\nfunc Answer() int { return 42 }\n\nfunc Unreached() int { return 7 }\n")
	write("m_test.go", "package m\n\nimport \"testing\"\n\n"+
		"func TestAnswer(t *testing.T) {\n\tif Answer() != 42 {\n\t\tt.Fatal(\"wrong\")\n\t}\n}\n")
	t.Chdir(mod)

	var out, errOut bytes.Buffer
	body := `[{"name":"a function nobody calls changes","file":"m.go",` +
		`"from":"return 7","to":"return 8","packages":["./..."]}]`
	code := run([]string{spec(t, body)}, &out, &errOut, nil, func(int) {})

	got := out.String() + errOut.String()
	if !strings.Contains(got, "appears to reach") || !strings.Contains(got, "m.go:5") {
		t.Errorf("a survivor on a line no test runs should say so, and name the line:\n%s", got)
	}
	// Still a survivor: the defect is invisible to the suite either way, and that
	// is what the reader acts on. The note says where to start.
	if !strings.Contains(got, "SURVIVED") {
		t.Errorf("the suite is green with the defect in place:\n%s", got)
	}
	if code != exitSurvivor {
		t.Errorf("exit = %d, want the survivor status — the note is not an outcome", code)
	}
	// The note has to read as what it is: an observation from one measurement,
	// not a fact about every test that exists. The baseline runs the packages this
	// sweep named, which is less than the suite, and a reader who cannot see that
	// has no way to doubt the note when it is wrong.
	if !strings.Contains(got, "this sweep ran") {
		t.Errorf("the note claims more than was measured — the baseline ran the packages this "+
			"sweep named, not every test:\n%s", got)
	}
	if !strings.Contains(got, "baseline") {
		t.Errorf("the note should say which measurement it came from:\n%s", got)
	}
}

// A line the tests do run is never called unreached — not when the mutation
// silences that very line, and not when the tests that run it live in another
// package.
//
// The two shapes below both leave a real survivor: the suite stays green with the
// defect in place, which is a missing test, not missing coverage. Reporting them
// as "no test runs this line" hides a live survivor behind a report about
// coverage, and turns a finding (exit 1) into a sweep that measured nothing
// (exit 2). Everything else in this file checks that an unreached line is called
// unreached; without these, only that direction is checked, and the way to pass
// every one of them is to say "unreached" more often.
func TestALineTheTestsDoRunIsNeverNotedAsUnreached(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		mut   string
		// Whether the reach can be decided at all, as opposed to which way it
		// goes. Coverage is recorded per block, and which lines a block spans is
		// the compiler's business — a `case` label sits inside its own block on
		// Go 1.26 and inside none at all on Go 1.27. Where no block covers the
		// line, "could not be measured" is the honest answer and the one this
		// tool gives. Where a block does cover it, an undecided answer would mean
		// the measurement quietly stopped working, so it is required.
		reachMustBeDecided bool
	}{
		{
			// The mutation is on a `case` label, which is where its coverage block
			// begins. With the label changed the label no longer matches, so the
			// block is counted zero on the mutated tree — while the tests, which
			// never look at the answer, stay green.
			name: "the mutation silences its own line",
			files: map[string]string{
				"go.mod": "module example.com/m\n\ngo 1.24\n",
				"m.go": "package m\n\nfunc Classify(s string) string {\n\tswitch s {\n" +
					"\tcase \"up\":\n\t\treturn \"rising\"\n\t}\n\treturn \"flat\"\n}\n",
				"m_test.go": "package m\n\nimport \"testing\"\n\n" +
					"func TestClassify(t *testing.T) {\n\tif Classify(\"up\") == \"\" {\n\t\tt.Fatal(\"empty\")\n\t}\n}\n",
			},
			mut: `{"name":"the label stops matching","file":"m.go",` +
				`"from":"case \"up\":","to":"case \"UP\":","packages":["./..."]}`,
		},
		{
			// helper has no test files of its own. Counted only in its own test
			// binary, every statement in it comes back zero — stated as zero, so it
			// reads as a definite "nothing runs this" rather than "not measured".
			name: "the tests that run it are in another package",
			files: map[string]string{
				"go.mod":         "module example.com/m\n\ngo 1.24\n",
				"helper/h.go":    "package helper\n\nfunc Double(n int) int {\n\treturn n * 2\n}\n",
				"user/u.go":      "package user\n\nimport \"example.com/m/helper\"\n\nfunc Quad(n int) int { return helper.Double(helper.Double(n)) }\n",
				"user/u_test.go": "package user\n\nimport \"testing\"\n\nfunc TestQuad(t *testing.T) {\n\tif Quad(2) == 0 {\n\t\tt.Fatal(\"zero\")\n\t}\n}\n",
			},
			mut: `{"name":"doubling stops doubling","file":"helper/h.go",` +
				`"from":"return n * 2","to":"return n * 3","packages":["./..."]}`,
			// A statement, so every toolchain puts it inside a block.
			reachMustBeDecided: true,
		},
		{
			// The same, named the way the guidance suggests: the one package whose
			// tests are worth running, not the whole module. The mutated file is not
			// in it. Coverage still has to reach across, and the sweep still has to
			// be able to answer.
			name: "the tests are in another package, named narrowly",
			files: map[string]string{
				"go.mod":         "module example.com/m\n\ngo 1.24\n",
				"helper/h.go":    "package helper\n\nfunc Double(n int) int {\n\treturn n * 2\n}\n",
				"user/u.go":      "package user\n\nimport \"example.com/m/helper\"\n\nfunc Quad(n int) int { return helper.Double(helper.Double(n)) }\n",
				"user/u_test.go": "package user\n\nimport \"testing\"\n\nfunc TestQuad(t *testing.T) {\n\tif Quad(2) == 0 {\n\t\tt.Fatal(\"zero\")\n\t}\n}\n",
			},
			mut: `{"name":"doubling stops doubling","file":"helper/h.go",` +
				`"from":"return n * 2","to":"return n * 3","packages":["./user/"]}`,
			reachMustBeDecided: true,
		},
		{
			// The change is on line 7, which the tests run. The pattern had to be
			// widened backwards to match once — `return "other"` appears twice —
			// and it now begins on line 5, which they do not. Followed to line 5,
			// the answer is that nothing reaches the code; the change is on 7.
			name: "the pattern is widened with a line nothing runs",
			files: map[string]string{
				"go.mod": "module example.com/m\n\ngo 1.24\n",
				"m.go": "package m\n\nfunc Classify(n int) string {\n\tif n < 0 {\n\t\treturn \"neg\"\n\t}\n\treturn \"other\"\n}\n\n" +
					"func Fallback() string { return \"other\" }\n",
				"m_test.go": "package m\n\nimport \"testing\"\n\n" +
					"func TestClassify(t *testing.T) {\n\tClassify(1)\n}\n",
			},
			mut: `{"name":"the classification stops being other","file":"m.go",` +
				`"from":"return \"neg\"\n\t}\n\treturn \"other\"",` +
				`"to":"return \"neg\"\n\t}\n\treturn \"OTHER\"","packages":["./..."]}`,
			reachMustBeDecided: true,
		},
		{
			// One mutation, two places: line 5, which nothing runs, and line 7,
			// which the tests run every time. Asked about only the first, the whole
			// defect is written off as code nothing reaches — but the tests run half
			// of it, so they run the defect.
			name: "the mutation changes two places, one of them run",
			files: map[string]string{
				"go.mod": "module example.com/m\n\ngo 1.24\n",
				"m.go":   "package m\n\nfunc Handle(x int) int {\n\tif x < 0 {\n\t\treturn -1\n\t}\n\treturn x * 2\n}\n",
				"m_test.go": "package m\n\nimport \"testing\"\n\n" +
					"func TestHandle(t *testing.T) {\n\t_ = Handle(3)\n}\n",
			},
			mut: `{"name":"both returns start lying","file":"m.go",` +
				`"from":"return -1\n\t}\n\treturn x * 2",` +
				`"to":"return 0\n\t}\n\treturn x","packages":["./..."]}`,
			reachMustBeDecided: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mod := t.TempDir()
			for name, body := range tc.files {
				path := filepath.Join(mod, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(mod)

			var out, errOut bytes.Buffer
			code := run([]string{spec(t, "["+tc.mut+"]")}, &out, &errOut, nil, func(int) {})

			got := out.String() + errOut.String()
			// The note is what can be wrong now, and this is the direction that
			// misleads: telling a reader that the code they are looking at is
			// never run, when their tests run it every time.
			if strings.Contains(got, "appears to reach") {
				t.Errorf("a line the tests run was noted as one nothing reaches:\n%s", got)
			}
			if !strings.Contains(got, "SURVIVED") {
				t.Errorf("the suite is green with the defect in place — that is a survivor:\n%s", got)
			}
			// Passing by not measuring is the way to satisfy this test without doing
			// the work: an unmeasured reach says nothing about which way it would
			// have gone. Where the line can be measured, it has to have been.
			if tc.reachMustBeDecided && strings.Contains(got, "could not be measured") {
				t.Errorf("the reach was never decided, so this says nothing about which way "+
					"it would have gone:\n%s", got)
			}
			if code != exitSurvivor {
				t.Errorf("exit = %d; a survivor is a finding, not a sweep that measured nothing", code)
			}
		})
	}
}

// A line nothing runs is still called unreached when the pattern that found it
// was widened with a line something does run.
//
// The other half of the same mistake. Read from the line the pattern starts on,
// a change on a line no test reaches borrows the coverage of the line above it
// and comes back as a plain survivor — "a test runs this and does not mind the
// defect" — stated with no reservation at all. That is a claim about a test that
// does not exist, and it is the reassuring direction, which is the one that gets
// believed.
func TestAWidenedPatternDoesNotBorrowTheCoverageOfTheLineAbove(t *testing.T) {
	mod := t.TempDir()
	for name, body := range map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.24\n",
		"m.go": "package m\n\nfunc Classify(n int) string {\n\tif n < 0 {\n\t\treturn \"neg\"\n\t}\n\treturn \"other\"\n}\n\n" +
			"func Fallback() string { return \"other\" }\n",
		"m_test.go": "package m\n\nimport \"testing\"\n\nfunc TestClassify(t *testing.T) {\n\tClassify(1)\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(mod, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(mod)

	var out, errOut bytes.Buffer
	// The change is on line 5, which no test reaches. The `if` on line 4, which
	// the pattern was widened to include, runs every time.
	body := `[{"name":"the negative branch stops saying neg","file":"m.go",` +
		`"from":"if n < 0 {\n\t\treturn \"neg\"",` +
		`"to":"if n < 0 {\n\t\treturn \"NEG\"","packages":["./..."]}]`
	code := run([]string{spec(t, body)}, &out, &errOut, nil, func(int) {})

	got := out.String() + errOut.String()
	if !strings.Contains(got, "appears to reach") {
		t.Errorf("a change on a line no test runs was reported with nothing said about it:\n%s", got)
	}
	if code != exitSurvivor {
		t.Errorf("exit = %d, want the survivor status — the note is not an outcome", code)
	}
	if !strings.Contains(got, "m.go:5") {
		t.Errorf("the note should name the line that changed, not the one the pattern starts on:\n%s", got)
	}
}

// A pattern does not borrow the coverage of a line it only carried along.
//
// One mutation can change two places at once, and to match once the pattern
// between them has to be swallowed whole. That middle stretch is not part of the
// mutation: when the tests run it and run nothing that changed, the note is still
// owed. Borrowed from there, a change nothing reaches passes for one the tests
// watch — the reassuring direction, and the one that gets believed.
func TestAPatternDoesNotBorrowTheCoverageOfWhatItCarried(t *testing.T) {
	mod := t.TempDir()
	for name, body := range map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.24\n",
		"m.go": "package m\n\nfunc F(x int) int {\n\tif x < 0 {\n\t\treturn -1\n\t}\n\t_ = x + 1\n" +
			"\tif x > 100 {\n\t\treturn 999\n\t}\n\treturn x\n}\n",
		"m_test.go": "package m\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) {\n\t_ = F(5)\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(mod, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(mod)

	var out, errOut bytes.Buffer
	// Lines 5 and 9 change; neither is ever run. Line 7, between them, is run
	// every time and is identical on both sides of the mutation.
	body := `[{"name":"both dead returns start lying","file":"m.go",` +
		`"from":"return -1\n\t}\n\t_ = x + 1\n\tif x > 100 {\n\t\treturn 999",` +
		`"to":"return 0\n\t}\n\t_ = x + 1\n\tif x > 100 {\n\t\treturn 998","packages":["./..."]}]`
	code := run([]string{spec(t, body)}, &out, &errOut, nil, func(int) {})

	got := out.String() + errOut.String()
	if !strings.Contains(got, "appears to reach") {
		t.Errorf("nothing runs either line that changed, and the report said nothing about it:\n%s", got)
	}
	if code != exitSurvivor {
		t.Errorf("exit = %d, want the survivor status — the note is not an outcome", code)
	}
}

// A shorter replacement takes whole lines with it, and those lines can still show
// the change being run.
//
// They have no counterpart to compare against, so nothing marks where their code
// starts — and asked about at the left margin they fall outside every coverage
// block and answer nothing, every time. That is not a quiet failure: these are
// the lines that could have shown the tests running part of what changed and let
// the note fall silent. Without them the note appears over code that runs on
// every test.
func TestLinesARemovalTakesAreStillAsked(t *testing.T) {
	mod := t.TempDir()
	for name, body := range map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.24\n",
		"m.go": "package m\n\nfunc Tab(a int) int {\n\tn := 0\n\tif a > 0 {\n\t\tn = 1\n\t} else {\n" +
			"\t\tn = 2\n\t}\n\treturn n\n}\n",
		"m_test.go": "package m\n\nimport \"testing\"\n\nfunc TestTab(t *testing.T) {\n\t_ = Tab(-1)\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(mod, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(mod)

	var out, errOut bytes.Buffer
	// Four lines go and two come back. Line 8 is one of the four, and the test
	// runs it every time.
	body := `[{"name":"the else branch goes","file":"m.go",` +
		`"from":"\t\tn = 1\n\t} else {\n\t\tn = 2\n\t}","to":"\t\tn = 1\n\t}","packages":["./..."]}]`
	code := run([]string{spec(t, body)}, &out, &errOut, nil, func(int) {})

	got := out.String() + errOut.String()
	if strings.Contains(got, "appears to reach") {
		t.Errorf("the tests run one of the lines this removes, and it was written up as code "+
			"nothing reaches:\n%s", got)
	}
	if strings.Contains(got, "could not be measured") {
		t.Errorf("the lines this removes are ordinary statements; the reach was measurable:\n%s", got)
	}
	if code != exitSurvivor {
		t.Errorf("exit = %d, want the survivor status", code)
	}
}
