package orchestrator_test

// Down and Destroy both delegate to a shared, already-tested helper
// (stopSupervisorAndReport, pinned directly in the white-box
// stopsupervisorreport_test.go). What that white-box test cannot prove is that
// Down and Destroy actually still CALL it — a mutation replacing either call
// site with a bare `StopSupervisor(o.Project.Name)` (the exact #1401 bug shape)
// compiles fine and left every other test in this repository green when tried
// independently. These tests drive the real entry points instead.

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

func emptyProject(name string) *compose.Project {
	return project(name, map[string]*compose.Service{})
}

// spawnStuckSupervisor starts a real process that ignores SIGTERM (so only a
// real SIGKILL can end it — see stopsupervisorreport_test.go for why a plain
// `sleep` would not exercise the same thing), claims it as project's
// supervisor, and returns a cleanup that waits for it to actually be gone.
func spawnStuckSupervisor(t *testing.T, project string) {
	t.Helper()
	cmd := exec.Command("sh", "-c", `trap "" TERM; exec sleep 30`)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting a throwaway process: %v", err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	if err := orchestrator.WriteSupervisorPidFileForTest(project, cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the spawned process was never reaped")
		}
	})
}

// spawnLiveSupervisor starts a real process that dies quietly on SIGTERM,
// claims it as project's supervisor, and returns a cleanup that waits for it to
// be reaped.
func spawnLiveSupervisor(t *testing.T, project string) {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting a throwaway process: %v", err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	if err := orchestrator.WriteSupervisorPidFileForTest(project, cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the spawned process was never reaped")
		}
	})
}

// Down calls stopSupervisorAndReport before it touches anything else. This
// proves it — a mutation swapping that call for a bare, unchecked
// StopSupervisor(...) leaves this red.
func TestDownWarnsWhenItCannotConfirmTheSupervisorStopped(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	restoreBudgets := orchestrator.ShrinkStopSupervisorBudgetsForTest(5*time.Millisecond, 5*time.Millisecond, time.Millisecond)
	defer restoreBudgets()
	restoreAlive := orchestrator.SetProcessAliveForTest(func(int) bool { return true })
	defer restoreAlive()
	spawnStuckSupervisor(t, "down-stuck")

	rt, _ := fakeShim(t)
	var out bytes.Buffer
	o := orchestrator.New(emptyProject("down-stuck"), rt, "opossum", &out)
	if err := o.Down(false, "", false); err != nil {
		t.Fatalf("down: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "OPSM-414") {
		t.Errorf("down must warn with OPSM-414 when it cannot confirm the supervisor stopped, got: %q", got)
	}
	if !orchestrator.SupervisorPidFileExistsForTest("down-stuck") {
		t.Errorf("a stop that could not be confirmed must keep the pid file")
	}
}

// The confirmed-stop sibling of the test above, so a mutation that made Down
// always print the OPSM-414 notice (or never print the plain line) is caught
// too, at this same call site.
func TestDownReportsAConfirmedSupervisorStop(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	restoreBudgets := orchestrator.ShrinkStopSupervisorBudgetsForTest(time.Second, time.Second, time.Millisecond)
	defer restoreBudgets()
	spawnLiveSupervisor(t, "down-live")

	rt, _ := fakeShim(t)
	var out bytes.Buffer
	o := orchestrator.New(emptyProject("down-live"), rt, "opossum", &out)
	if err := o.Down(false, "", false); err != nil {
		t.Fatalf("down: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "Stopped the restart supervisor") || strings.Contains(got, "OPSM-414") {
		t.Errorf("down must report a confirmed stop plainly, got: %q", got)
	}
	if orchestrator.SupervisorPidFileExistsForTest("down-live") {
		t.Errorf("a confirmed stop must clear the pid file")
	}
}

// Destroy's sibling of TestDownWarnsWhenItCannotConfirmTheSupervisorStopped —
// its own call site, guarded on its own (p.SupervisorRunning) instead of
// Down's unconditional one.
func TestDestroyWarnsWhenItCannotConfirmTheSupervisorStopped(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	restoreBudgets := orchestrator.ShrinkStopSupervisorBudgetsForTest(5*time.Millisecond, 5*time.Millisecond, time.Millisecond)
	defer restoreBudgets()
	restoreAlive := orchestrator.SetProcessAliveForTest(func(int) bool { return true })
	defer restoreAlive()
	spawnStuckSupervisor(t, "destroy-stuck")

	rt, _ := fakeShim(t)
	var out bytes.Buffer
	o := orchestrator.New(emptyProject("destroy-stuck"), rt, "opossum", &out)
	if err := o.Destroy(orchestrator.DestroyPlan{SupervisorRunning: true}); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "OPSM-414") {
		t.Errorf("destroy must warn with OPSM-414 when it cannot confirm the supervisor stopped, got: %q", got)
	}
	if !orchestrator.SupervisorPidFileExistsForTest("destroy-stuck") {
		t.Errorf("a stop that could not be confirmed must keep the pid file")
	}
}
