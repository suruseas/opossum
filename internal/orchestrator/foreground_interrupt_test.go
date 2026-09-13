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

// A foreground `up` stays attached to its service until the container exits, so
// the person who presses Ctrl-C sees the run come back as a failure. Both the
// interrupted case and a real start failure are here, side by side: a verdict
// that sent every run failure to "interrupted" would pass the first alone.
func TestForegroundUpInterruptedIsNotReportedAsAStartFailure(t *testing.T) {
	const startFailureHint = "check why with `opossum logs web`"
	// The exact sentence a Ctrl-C between services already produces (the loop
	// head's interrupted()). The attached case must say the same thing, not
	// merely something with "interrupted" in it.
	const interruptedVerdict = "interrupted — rolling back"

	// upForeground runs `up --foreground` for a single service against the shim
	// steered by env, and returns the error, the output, and the shim's log.
	upForeground := func(t *testing.T, env []string, cancelOnceRunning bool) (string, []string, error) {
		t.Helper()
		rt, log := fakeShim(t)
		setShimEnv(rt, env...)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
		var out bytes.Buffer
		o := orchestrator.New(p, rt, "opossum", &out)
		o.OnSignal(ctx)
		done := make(chan error, 1)
		go func() { done <- o.Up(false) }()
		if cancelOnceRunning {
			// Interrupt only once the foreground run is under way — the case is the
			// run's failure after a cancel, not a cancel seen at the loop head.
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) && indexOf(log(), "run --name web.demo.opossum") < 0 {
				time.Sleep(2 * time.Millisecond)
			}
			if indexOf(log(), "run --name web.demo.opossum") < 0 {
				t.Fatalf("the foreground run never started, got %v", log())
			}
			cancel()
		}
		select {
		case err := <-done:
			return out.String(), log(), err
		case <-time.After(5 * time.Second):
			t.Fatal("up did not return")
			return "", nil, nil
		}
	}

	t.Run("Ctrl-C during the attached run says interrupted, not start failure", func(t *testing.T) {
		out, lines, err := upForeground(t, []string{"RUN_HANG=web.demo.opossum"}, true)
		if err == nil {
			t.Fatal("an interrupted up must fail")
		}
		if err.Error() != interruptedVerdict {
			t.Errorf("the error should be the same verdict as a Ctrl-C between services, %q, got: %v", interruptedVerdict, err)
		}
		if strings.Contains(err.Error(), startFailureHint) || strings.Contains(err.Error(), "starting service") {
			t.Errorf("Ctrl-C is not a start failure and there is nothing in the logs to check, got: %v", err)
		}
		// The verdict changed, the rollback did not: the container is still torn down.
		if indexOf(lines, "stop web.demo.opossum") < 0 {
			t.Errorf("the interrupted run should still be rolled back, got %v", lines)
		}
		if !strings.Contains(out, "Rolled back web") {
			t.Errorf("the rollback should still be reported, got:\n%s", out)
		}
	})

	t.Run("a real start failure keeps the start-failure wording", func(t *testing.T) {
		_, _, err := upForeground(t, []string{"RUN_FAIL=web.demo.opossum"}, false)
		if err == nil {
			t.Fatal("a failed run must fail the up")
		}
		if !strings.Contains(err.Error(), `starting service "web"`) || !strings.Contains(err.Error(), startFailureHint) {
			t.Errorf("a run that failed on its own is a start failure, with the hint, got: %v", err)
		}
		if strings.Contains(err.Error(), "interrupted") {
			t.Errorf("nothing was interrupted, got: %v", err)
		}
	})

	// One level deeper: a run-to-completion dependency also runs attached, and
	// its own failure wording ("did not complete successfully") is just as wrong
	// for a Ctrl-C.
	t.Run("Ctrl-C during a run-to-completion dependency says interrupted too", func(t *testing.T) {
		rt, log := fakeShim(t)
		setShimEnv(rt, "RUN_HANG=migrate.demo.opossum")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		p := project("demo", map[string]*compose.Service{
			"migrate": {Image: "migrate:latest"},
			"web": {Image: "web:latest",
				DependsOn: compose.DependsOn{{Name: "migrate", Condition: compose.ConditionCompleted}}},
		})
		var out bytes.Buffer
		o := orchestrator.New(p, rt, "opossum", &out)
		o.OnSignal(ctx)
		done := make(chan error, 1)
		go func() { done <- o.Up(true) }()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && indexOf(log(), "run --name migrate.demo.opossum") < 0 {
			time.Sleep(2 * time.Millisecond)
		}
		if indexOf(log(), "run --name migrate.demo.opossum") < 0 {
			t.Fatalf("the run-to-completion dependency never started, got %v", log())
		}
		cancel()
		var err error
		select {
		case err = <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("up did not return")
		}
		if err == nil || err.Error() != interruptedVerdict {
			t.Errorf("the error should be the same verdict as a Ctrl-C between services, %q, got: %v", interruptedVerdict, err)
		}
	})

	// The shim knob these cases lean on has to mean what its name says: a
	// foreground run of that one service, and nothing else. A detached run of
	// the same name and a foreground run of another name both come back on
	// their own (within the helper's deadline; the hang is far longer) —
	// otherwise a knob that held every run would let the cases above pass for
	// the wrong reason, with the cancel landing on whatever happened to hang.
	t.Run("RUN_HANG holds only a foreground run of the named service", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "RUN_HANG=web.demo.opossum")
		p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
		done := make(chan error, 1)
		go func() { done <- orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true) }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("a detached up of the named service must not be held, got: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a detached run of the named service was held by RUN_HANG")
		}
		if _, _, err := upForeground(t, []string{"RUN_HANG=other.demo.opossum"}, false); err != nil {
			t.Errorf("a foreground run of another service must not be held, got: %v", err)
		}
	})
}
