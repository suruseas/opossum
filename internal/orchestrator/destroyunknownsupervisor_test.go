package orchestrator

import (
	"bytes"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// `destroy` over a supervisor that `ps` would not vouch for (#1755): it counts the supervisor in the plan, says it
// could not confirm it stopped, and leaves the directory with its pid file — removed with the rest, the supervisor ran
// on with nothing that could find it (the same as `down` before, and in `destroy` also after the change that fixed `down`).
func TestDestroyKeepsThePidFileOfASupervisorItCouldNotCheck(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const project = "demo"
	pid := aLiveOtherProcess(t)
	useReadPs(t, pid, 1<<30, "")
	pidFile := pidFileOf(t, project, pid, readPsMarker)
	dir, err := supervisorStateDir(project)
	if err != nil {
		t.Fatal(err)
	}
	p := &compose.Project{Name: project, Services: map[string]*compose.Service{"a": {Image: "web:latest"}}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)

	t.Run("the plan counts it", func(t *testing.T) {
		plan, err := o.DestroyPlanFor(false, false, false)
		if err != nil {
			t.Fatalf("DestroyPlanFor: %v", err)
		}
		if !plan.SupervisorRunning {
			t.Errorf("the plan says no supervisor is running over a live process behind the pid file")
		}
	})
	t.Run("destroy leaves the directory and the process, and says so", func(t *testing.T) {
		if err := o.Destroy(DestroyPlan{SupervisorRunning: true, Paths: []string{dir}}); err != nil {
			t.Fatalf("Destroy: %v", err)
		}
		if _, err := os.Stat(pidFile); err != nil {
			t.Errorf("the pid file was removed with the directory: %v", err)
		}
		if err := syscall.Kill(pid, 0); err != nil {
			t.Errorf("the process was signalled: %v", err)
		}
		said := out.String()
		if !strings.Contains(said, "OPSM-414") {
			t.Errorf("destroy did not say it could not confirm the supervisor stopped:\n%s", said)
		}
		if !strings.Contains(said, "Keeping "+dir) {
			t.Errorf("destroy did not say it kept the directory:\n%s", said)
		}
	})
}

// With nothing left to find, the directory goes with the rest, as it did: a supervisor that stopped, and one that was
// never there.
func TestDestroyStillRemovesTheSupervisorDirectoryWhenNoneIsLeft(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const project = "demo"
	pidFile := pidFileOf(t, project, 2147483646, readPsMarker) // no such process
	dir, err := supervisorStateDir(project)
	if err != nil {
		t.Fatal(err)
	}
	p := &compose.Project{Name: project, Services: map[string]*compose.Service{"a": {Image: "web:latest"}}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	if err := o.Destroy(DestroyPlan{SupervisorRunning: false, Paths: []string{dir}}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Stat(pidFile); err == nil {
		t.Errorf("the directory of a supervisor that is not there was kept:\n%s", out.String())
	}
}
