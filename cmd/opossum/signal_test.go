package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Every Ctrl-C behaviour of `up` and `run` — the interrupted verdicts, the
// rollback, the one-off stopped — rests on one piece of wiring in this
// package: the signal handler that cancels the context the orchestrator is
// given. The orchestrator's own evals cancel that context by hand, so nothing
// there notices if the handler is never installed or never handed over. These
// two drive the real command in-process and send the process a SIGINT once
// the attached run is under way, which is the only way to see the wiring end
// to end. They are deliberately not parallel: a signal is process-wide.
//
// A SIGINT lands here only while the command's handler is installed (it is
// sent after the run started, and the handler is installed before the run);
// were it sent earlier, the test binary itself would die of it — which is
// also why exactly one is sent (a second one is the command's forced exit).
func TestCtrlCReachesTheOrchestratorThroughTheSignalWiring(t *testing.T) {
	// interruptedRun runs the command with the named container's foreground
	// run held by the shim, sends SIGINT once that run is in the shim log, and
	// returns the exit code, stderr, and the log. A wiring that never cancels
	// lets the held run go on: the command returns late and with the wrong
	// answer, which the deadline turns into a failure rather than a hang.
	interruptedRun := func(t *testing.T, args []string, held string) (int, string, []string) {
		t.Helper()
		log := fakeShim(t)
		t.Setenv("RUN_HANG", held)
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(
			"name: sig\nservices:\n  web:\n    image: alpine:3\n    command: [\"sleep\", \"300\"]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		var out, errOut strings.Builder
		done := make(chan int, 1)
		go func() { done <- runCLI(args, &out, &errOut) }()
		running := func() bool {
			for _, l := range log() {
				if strings.HasPrefix(l, "run ") && strings.Contains(l, "--name "+held+" ") && !strings.Contains(l, " -d ") {
					return true
				}
			}
			return false
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) && !running() {
			time.Sleep(5 * time.Millisecond)
		}
		if !running() {
			t.Fatalf("the attached run never started, so there is nothing to interrupt:\n%s%s\nlog: %v", out.String(), errOut.String(), log())
		}
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
		select {
		case code := <-done:
			return code, errOut.String(), log()
		case <-time.After(8 * time.Second):
			t.Fatalf("the command did not return after SIGINT: the signal never reached the orchestrator's context\n%s%s", out.String(), errOut.String())
			return 0, "", nil
		}
	}

	cases := []struct {
		name, held, verdict string
		args                []string
	}{
		{"up --foreground: interrupted and rolled back", "web.sig.opossum", "interrupted — rolling back",
			[]string{"up", "--foreground", "--no-supervisor"}},
		{"run: interrupted, the one-off stopped", "web-run.sig.opossum", "interrupted — stopped web-run.sig.opossum",
			[]string{"run", "web", "sleep", "100"}},
	}
	for _, c := range cases {
		// A case whose wiring is dead leaves its command — and its signal
		// handler — running past the deadline; a second SIGINT would then be
		// that handler's forced exit and take the whole test binary with it.
		// So after one failure the rest is not attempted.
		if t.Failed() {
			t.Logf("skipping %q: an earlier case left a command running", c.name)
			break
		}
		t.Run(c.name, func(t *testing.T) {
			code, errOut, log := interruptedRun(t, c.args, c.held)
			if code != 1 || !strings.Contains(errOut, c.verdict) {
				t.Errorf("want exit 1 with %q, got %d:\n%s", c.verdict, code, errOut)
			}
			if indexOfLine(log, "stop "+c.held) < 0 {
				t.Errorf("the interrupted container should be stopped, got %v", log)
			}
		})
	}
}

func indexOfLine(lines []string, sub string) int {
	for i, l := range lines {
		if strings.Contains(l, sub) {
			return i
		}
	}
	return -1
}
