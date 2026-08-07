package main

import (
	"bytes"
	"encoding/json"
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
func TestAnInterruptRestoresTheFileAndExitsAsInterrupted(t *testing.T) {
	mod := throwawayModule(t)
	sigs := make(chan os.Signal, 1)
	exited := make(chan int, 4)
	var out bytes.Buffer
	errOut := &lockedBuf{}

	// Fire while the sweep is running: the test in the throwaway module sleeps.
	go func() {
		time.Sleep(300 * time.Millisecond)
		sigs <- os.Interrupt
	}()
	run([]string{spec(t, sweepFor("func Answer() int { return 43 }"))}, &out, errOut, sigs,
		func(c int) { exited <- c })

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
