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

// The one line `up` prints when it leaves a watcher behind, word for word.
//
// Nothing read this line at all. A mutation sweep exchanged the three strings it
// is built from — the code, the services, the log path — and all three exchanges
// left this repository green, which would have printed `[web, worker] watching
// OPSM-408 for `restart:“ to everyone who ran `up`.
//
// It is the only notice a user gets about a background process they did not ask
// for by name, so every clause in it is load-bearing: what is running, how to
// stop it, where to read it, and how to have refused it.
func TestTheSupervisorNoticeIsWordForWordWhatWeMeanToSay(t *testing.T) {
	const rest = " for `restart:` — a small supervisor is now running for this project. " +
		"`opossum down` stops it, `opossum ps` shows it, and it logs to /tmp/x.log. " +
		"Start with --no-supervisor (or OPOSSUM_NO_SUPERVISOR=1) to skip it."
	// One service as well as two. A width on the services verb — `%-8s`, which
	// audit.go uses two files away — pads a short name and leaves a long one
	// alone, so a list that is always long is a golden that cannot see it.
	for _, c := range []struct {
		services []string
		want     string
	}{
		{[]string{"web", "worker"}, "[OPSM-408] watching web, worker" + rest},
		{[]string{"web"}, "[OPSM-408] watching web" + rest},
	} {
		// A project name that cannot appear in the notice by accident: named
		// "demo", the check below would also fire on a log path like
		// /tmp/demo.log, and report a notice naming the project when nothing had
		// changed but the fixture.
		got := NoticeSupervisorStarted("zzz-never-in-this-line", c.services, "/tmp/x.log")
		if got != c.want {
			t.Errorf("the supervisor notice is not what this file says it should be\n got: %s\nwant: %s", got, c.want)
		}
		// The project name is taken and not used. Said here rather than left for
		// the next reader to work out from the format string: callers compute it
		// (`up` passes o.Project.Name) and the line says "this project" without
		// naming it. Pinning the text first makes changing that a decision rather
		// than a slip.
		if strings.Contains(got, "zzz-never-in-this-line") {
			t.Errorf("the notice does not name the project today; if that changed, this file "+
				"has to say so first:\n%s", got)
		}
	}
}
