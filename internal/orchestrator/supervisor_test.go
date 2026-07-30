package orchestrator

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A pid file outlives the process that wrote it — a crash, a reboot, a kill -9 —
// and macOS recycles pids quickly. Since StopSupervisor escalates to SIGKILL,
// believing a bare number risks killing whatever inherited it.
func TestSupervisorPIDRejectsAReusedPid(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := filepath.Join(state, "opossum", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Our own pid, but recorded with a start time that isn't ours.
	me := os.Getpid()
	if err := os.WriteFile(filepath.Join(dir, "supervisor.pid"),
		[]byte(strconv.Itoa(me)+" Mon-Jan-1-00:00:00-1990\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := SupervisorPID("demo"); got != 0 {
		t.Errorf("a pid whose start time doesn't match must not be believed, got %d", got)
	}
	// Recorded correctly, it is believed.
	if err := os.WriteFile(filepath.Join(dir, "supervisor.pid"),
		[]byte(strconv.Itoa(me)+" "+processStartedAt(me)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := SupervisorPID("demo"); got != me {
		t.Errorf("a matching pid should be believed, got %d want %d", got, me)
	}
}

// A pid nobody is running is not a supervisor.
func TestSupervisorPIDRejectsADeadPid(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := filepath.Join(state, "opossum", "demo")
	os.MkdirAll(dir, 0o755)
	// 2^22 is above the default pid_max on macOS and Linux alike.
	os.WriteFile(filepath.Join(dir, "supervisor.pid"), []byte("4194304 x\n"), 0o644)
	if got := SupervisorPID("demo"); got != 0 {
		t.Errorf("a dead pid must not be believed, got %d", got)
	}
}

// The state directory is derived from a name that comes out of a compose file,
// and this code both writes and removes files there. A traversing name must not
// reach outside its own tree.
func TestSupervisorStateDirCannotEscape(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	for _, name := range []string{"../../evil", "a/b", "..", "/etc"} {
		dir, err := supervisorStateDir(name)
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(base, "opossum")
		if !strings.HasPrefix(filepath.Clean(dir), want) {
			t.Errorf("project %q produced %q, which is outside %q", name, dir, want)
		}
	}
}
