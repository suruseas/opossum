package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A `down` that comes while `up` is starting the restart supervisor is refused, because `up` still holds the project's lock until the
// watcher has claimed it (#1740). The supervisor used to be started after `up` let the lock go: a `down` in between took the project
// down and returned, and the watcher then claimed a project nothing was left in. The watcher here is slow to claim (it waits before
// it runs), so the `down` lands in the time `up` is waiting for it.
func TestADownWhileUpStartsTheSupervisorIsRefusedUntilItIsClaimed(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	setOrUnset(t, "COMPOSE_PROFILES", nil)
	dir := t.TempDir()
	started := filepath.Join(dir, "supervisor-started")
	slow := filepath.Join(dir, "slow-supervisor.sh")
	script := "#!/bin/sh\n: > '" + started + "'\nsleep 0.8\nexec '" + opossumBin + "' \"$@\"\n"
	if err := os.WriteFile(slow, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPOSSUM_SELF_BIN", slow)
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: downrace\nservices:\n  web:\n    image: web\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	upDone := make(chan error, 1)
	go func() {
		_, err := run(t, "up", "--no-build")
		upDone <- err
	}()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("up never started the supervisor")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The watcher is on its way and has not claimed: this is the time the lock has to cover.
	down := exec.Command(opossumBin, "-p", "downrace", "down")
	out, derr := down.CombinedOutput()
	if derr == nil || !strings.Contains(string(out), "OPSM-208") {
		t.Errorf("a down while the supervisor is being started: want it refused (OPSM-208), got %v\n%s", derr, out)
	}
	if err := <-upDone; err != nil {
		t.Fatalf("up: %v", err)
	}
	if out, err := exec.Command(opossumBin, "-p", "downrace", "down").CombinedOutput(); err != nil {
		t.Errorf("down after up has finished: %v\n%s", err, out)
	}
}
