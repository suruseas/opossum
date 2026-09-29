package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The restart supervisor is a re-exec of opossum itself, which re-resolves
// the project from scratch — so it needs the same `--profile` this `up` was
// given, or it would decide a different set of services is in play
// (startSupervisorFor). Passing it is currently a low-risk redundancy rather
// than a user-visible fix (#1138: `CheckMounts`, the one path inside the
// child that reads profiles, is already run by `up`'s own pre-flight before
// anything starts, so a child that narrowed its profiles could only miss a
// conflict `up` had already refused) — but nothing pinned that the argv
// carries it at all, which is what #1137's independent review found
// (removing the passthrough left the whole suite green).
//
// COMPOSE_PROFILES is deliberately left unset here: it is the child's
// fallback, and if the test let it through, a broken passthrough could hide
// behind it.
func TestUpPassesItsProfileFlagToTheSupervisor(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	setOrUnset(t, "COMPOSE_PROFILES", nil)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: supprof\nservices:\n  web:\n    image: web\n    profiles: [x]\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	if _, err := run(t, "--profile", "x", "up", "--no-build"); err != nil {
		t.Fatalf("up: %v", err)
	}
	var pid int
	waitFor(t, "the supervisor to claim the project", func() bool {
		pid = supervisorPID(t, state, "supprof")
		return pid != 0
	})
	t.Cleanup(func() {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Signal(syscall.SIGKILL)
		}
	})

	pids, looked := processesMatching("__supervise.*--profile x")
	if !looked {
		t.Skip("pgrep unavailable, cannot inspect the supervisor's argv")
	}
	found := false
	for _, p := range pids {
		if p == pid {
			found = true
		}
	}
	if !found {
		t.Errorf("want pid %d's argv to carry --profile x, pgrep for it found %v", pid, pids)
	}

	if _, err := run(t, "down"); err != nil {
		t.Fatalf("down: %v", err)
	}
	waitFor(t, "the supervisor to exit", func() bool { return !processIsAlive(pid) })
}
