package orchestrator_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// `opossum run` attaches to its one-off container until it exits. A Ctrl-C
// used to kill the process with nothing said, and the container kept running
// (measured on container 1.4.1). Now the run is cancelled through the same
// context `up` uses, the container is stopped — removed with --rm — and the
// interruption is reported. The wording and the teardown are separate guards:
// a fix that only changed the sentence would leave the container running.
func TestRunOneOffInterruptedStopsItsContainerAndSaysSo(t *testing.T) {
	const cname = "web-run.demo.opossum"
	// runOf finds the one-off's own run in the shim log: a `run` line naming the
	// container (the flags between differ — `-i`, `-t` — so the name is what is
	// matched, not a fixed spelling of the whole line).
	runOf := func(lines []string) int {
		for i, l := range lines {
			if strings.HasPrefix(l, "run ") && strings.Contains(l, "--name "+cname+" ") {
				return i
			}
		}
		return -1
	}

	// runInterrupted runs the one-off against the shim, cancels once the
	// attached run is under way (unless told not to), and returns the shim's
	// full log and the error.
	runInterrupted := func(t *testing.T, env []string, opts orchestrator.RunOneOffOptions, cancelOnceRunning bool) ([]string, error) {
		t.Helper()
		rt, log := fakeShim(t)
		setShimEnv(rt, env...)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
		o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
		o.OnSignal(ctx)
		done := make(chan error, 1)
		go func() { done <- o.RunOneOff("web", []string{"sleep", "100"}, opts) }()
		if cancelOnceRunning {
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) && runOf(log()) < 0 {
				time.Sleep(2 * time.Millisecond)
			}
			if runOf(log()) < 0 {
				t.Fatalf("the one-off never started, got %v", log())
			}
			cancel()
		}
		select {
		case err := <-done:
			return log(), err
		case <-time.After(5 * time.Second):
			t.Fatal("run did not return")
			return nil, nil
		}
	}
	// afterRun is the log from the one-off's own run onward: what the teardown
	// did, and nothing the dependency start-up did before.
	afterRun := func(t *testing.T, lines []string) []string {
		t.Helper()
		i := runOf(lines)
		if i < 0 {
			t.Fatalf("no run of %s in %v", cname, lines)
		}
		return lines[i:]
	}

	t.Run("says it was interrupted, not that the command failed", func(t *testing.T) {
		_, err := runInterrupted(t, []string{"RUN_HANG=" + cname}, orchestrator.RunOneOffOptions{}, true)
		if err == nil || !strings.HasPrefix(err.Error(), "interrupted — stopped "+cname) {
			t.Errorf("want the interruption reported with the container's fate, got: %v", err)
		}
		if err != nil && strings.Contains(err.Error(), "signal: killed") {
			t.Errorf("the kill is not the command's result, got: %v", err)
		}
	})

	t.Run("stops the container it started, and keeps it as without --rm", func(t *testing.T) {
		lines, _ := runInterrupted(t, []string{"RUN_HANG=" + cname}, orchestrator.RunOneOffOptions{}, true)
		after := afterRun(t, lines)
		if indexOf(after, "stop "+cname) < 0 {
			t.Errorf("the one-off container must be stopped after the interrupt, got %v", after)
		}
		if indexOf(after, "delete --force "+cname) >= 0 {
			t.Errorf("without --rm the stopped container is kept, got %v", after)
		}
	})

	// "Stopped" is checked, not assumed: `stop`'s exit code says only whether the
	// name exists (measured on 1.4.1), so the runtime is asked afterwards. A stop
	// that did not take must not be reported as one that did — the user would
	// never look for the container. (No real form of a silently failing stop was
	// found on 1.4.1; the knob is a defence, and this guards the caller's honesty,
	// not the runtime.)
	t.Run("says stopped only when the runtime agrees", func(t *testing.T) {
		lines, err := runInterrupted(t, []string{"RUN_HANG=" + cname, "STOP_FAIL=" + cname}, orchestrator.RunOneOffOptions{}, true)
		if err == nil || !strings.Contains(err.Error(), "tried to stop "+cname+", but it is still running") || !strings.Contains(err.Error(), "`container stop "+cname+"`") {
			t.Errorf("a stop that did not take must be reported as such, with the command that stops it, got: %v", err)
		}
		if err != nil && strings.HasPrefix(err.Error(), "interrupted — stopped") {
			t.Errorf("must not claim the container is stopped, got: %v", err)
		}
		after := afterRun(t, lines)
		if i, j := indexOf(after, "stop "+cname), indexOf(after, "inspect "+cname); i < 0 || j < i {
			t.Errorf("the check is an inspect after the stop, got %v", after)
		}
	})

	// A container that is gone by the time it is asked about (something else
	// removed it) is not running either: that is the outcome wanted, and reading
	// the missing container as a failed stop would refuse to say so. But without
	// --rm this run did not remove it, so it says who did not — "kept" would be
	// false, and silence would pass off someone else's removal as its own.
	t.Run("a container gone after the stop counts as stopped, and is not called kept", func(t *testing.T) {
		_, err := runInterrupted(t, []string{"RUN_HANG=" + cname, "INSPECT_ABSENT=" + cname}, orchestrator.RunOneOffOptions{}, true)
		if err == nil || !strings.HasPrefix(err.Error(), "interrupted — stopped "+cname+"; it is no longer there (something else removed it)") {
			t.Errorf("gone is stopped, and said to be gone, got: %v", err)
		}
		if err != nil && strings.Contains(err.Error(), "kept") {
			t.Errorf("a container that is gone must not be called kept, got: %v", err)
		}
	})

	// A runtime that cannot be asked is not a gone container: the stop reached
	// the same runtime, so nothing is known — and "stopped" would be a guess.
	t.Run("says it could not confirm when the runtime cannot be asked", func(t *testing.T) {
		_, err := runInterrupted(t, []string{"RUN_HANG=" + cname, "INSPECT_FAIL=" + cname}, orchestrator.RunOneOffOptions{}, true)
		if err == nil || !strings.Contains(err.Error(), "tried to stop "+cname+", but the runtime could not be asked whether it stopped") || !strings.Contains(err.Error(), "`container ls -a` shows it, `container stop "+cname+"` stops it") {
			t.Errorf("an unanswered inspect must be reported as such, with the commands that show and stop it, got: %v", err)
		}
		if err != nil && strings.HasPrefix(err.Error(), "interrupted — stopped") {
			t.Errorf("must not claim the container is stopped, got: %v", err)
		}
	})
	t.Run("with --rm, says it could not confirm when the runtime cannot be asked", func(t *testing.T) {
		_, err := runInterrupted(t, []string{"RUN_HANG=" + cname, "INSPECT_FAIL=" + cname}, orchestrator.RunOneOffOptions{Rm: true}, true)
		if err == nil || !strings.Contains(err.Error(), "tried to stop and remove "+cname+", but the runtime could not be asked whether it is gone") || !strings.Contains(err.Error(), "`container ls -a` shows it, `container delete --force "+cname+"` removes it") {
			t.Errorf("an unanswered inspect must be reported as such, with the commands that show and remove it, got: %v", err)
		}
		if err != nil && strings.Contains(err.Error(), "stopped and removed") {
			t.Errorf("must not claim the container is removed, got: %v", err)
		}
	})

	t.Run("with --rm, says removed only when the runtime agrees", func(t *testing.T) {
		_, err := runInterrupted(t, []string{"RUN_HANG=" + cname, "DELETE_STICKY=" + cname}, orchestrator.RunOneOffOptions{Rm: true}, true)
		if err == nil || !strings.Contains(err.Error(), "tried to stop and remove "+cname+", but it is still there") || !strings.Contains(err.Error(), "`container delete --force "+cname+"`") {
			t.Errorf("a removal that did not take must be reported as such, with the command that removes it, got: %v", err)
		}
		if err != nil && strings.Contains(err.Error(), "stopped and removed") {
			t.Errorf("must not claim the container is removed, got: %v", err)
		}
	})

	t.Run("with --rm, removes it as well", func(t *testing.T) {
		lines, err := runInterrupted(t, []string{"RUN_HANG=" + cname}, orchestrator.RunOneOffOptions{Rm: true}, true)
		after := afterRun(t, lines)
		if indexOf(after, "stop "+cname) < 0 || indexOf(after, "delete --force "+cname) < 0 {
			t.Errorf("--rm: the interrupted one-off is stopped and removed, got %v", after)
		}
		if err == nil || !strings.Contains(err.Error(), "stopped and removed") {
			t.Errorf("the report should say it was removed, got: %v", err)
		}
	})

	// A run that dies of a signal with nothing cancelled — killed from outside,
	// not by Ctrl-C — is not an interruption: the verdict comes from the
	// context, not from the shape of the error. Such a run has already died,
	// so nothing is stopped either.
	t.Run("a run killed by some other signal is not an interruption", func(t *testing.T) {
		lines, err := runInterrupted(t, []string{"RUN_DIE_SIGNAL=" + cname}, orchestrator.RunOneOffOptions{}, false)
		if err == nil || !strings.Contains(err.Error(), "signal: killed") || strings.Contains(err.Error(), "interrupted") {
			t.Errorf("a signal death with nothing cancelled passes through as the run's own error, got: %v", err)
		}
		if indexOf(afterRun(t, lines), "stop "+cname) >= 0 {
			t.Errorf("nothing was interrupted, so nothing is stopped, got %v", lines)
		}
	})

	// `run --audit` goes through the same RunOneOff; an interruption there is
	// handed up as such, not folded into a report that reads "exit -1".
	t.Run("--audit: the interruption is reported, not audited as exit -1", func(t *testing.T) {
		rt, log := fakeShim(t)
		setShimEnv(rt, "RUN_HANG="+cname)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
		o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
		o.OnSignal(ctx)
		type result struct {
			report *orchestrator.AuditReport
			err    error
		}
		done := make(chan result, 1)
		go func() {
			r, err := o.RunAudited("web", []string{"sleep", "100"}, orchestrator.RunOneOffOptions{})
			done <- result{r, err}
		}()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && runOf(log()) < 0 {
			time.Sleep(2 * time.Millisecond)
		}
		if runOf(log()) < 0 {
			t.Fatalf("the audited one-off never started, got %v", log())
		}
		cancel()
		select {
		case r := <-done:
			if r.err == nil || !strings.HasPrefix(r.err.Error(), "interrupted — stopped "+cname) {
				t.Errorf("want the interruption handed up, got err=%v report=%+v", r.err, r.report)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("audited run did not return")
		}
	})

	// Control: a one-off that exits non-zero on its own is its own result —
	// propagated untouched, with no stop (it already exited) and no talk of an
	// interruption.
	t.Run("a command that fails on its own keeps its exit as the result", func(t *testing.T) {
		lines, err := runInterrupted(t, []string{"RUN_FAIL=" + cname}, orchestrator.RunOneOffOptions{}, false)
		if err == nil || strings.Contains(err.Error(), "interrupted") {
			t.Errorf("nothing was interrupted; the command's own failure is the result, got: %v", err)
		}
		if indexOf(afterRun(t, lines), "stop "+cname) >= 0 {
			t.Errorf("a command that exited is not stopped, got %v", lines)
		}
	})
}
