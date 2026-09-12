package orchestrator_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// holdProjectLock takes the project's lock the way another opossum process
// would — flock on the same file — and writes a pid into it. Two opens of
// one file are two file descriptions, so the lock conflicts even from one
// process. The returned func releases it.
func holdProjectLock(t *testing.T, project string, pid int) func() {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_STATE_HOME"), "opossum", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("could not take the lock to stand in for another process: %v", err)
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(pid)), 0)
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }
}

func lockProject() *compose.Project {
	return project("locked", map[string]*compose.Service{"web": {Image: "alpine:3.20"}})
}

// While another command holds the project's lock, `up` and `down` are
// refused at once with OPSM-208 naming the holder's pid, and nothing is
// started or removed; once it lets go, they proceed.
func TestUpAndDownWaitForNoOneButRefuseWhileAnotherHoldsTheProject(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	release := holdProjectLock(t, "locked", 4242)
	rt, log := fakeShim(t)
	o := orchestrator.New(lockProject(), rt, "opossum", &bytes.Buffer{})
	for name, run := range map[string]func() error{
		"up":      func() error { return o.Up(true) },
		"down":    func() error { return o.Down(false, "", false) },
		"destroy": func() error { return o.Destroy(orchestrator.DestroyPlan{}) },
	} {
		err := run()
		if err == nil || !strings.Contains(err.Error(), `[OPSM-208] another opossum command is changing project "locked" (pid 4242)`) {
			t.Fatalf("%s under another's lock: want OPSM-208 naming pid 4242, got: %v", name, err)
		}
		if !strings.Contains(err.Error(), "wait for it to finish (a foreground `up` ends with Ctrl-C), then retry") {
			t.Errorf("%s: the refusal must say what to do, got: %v", name, err)
		}
	}
	// Refused before the runtime is touched at all — not merely before a
	// container is started: a lock taken after the network is made would let
	// two `up`s race on that step.
	if got := log(); len(got) != 0 {
		t.Errorf("nothing may reach the runtime under another's lock, it saw %d command(s):\n%s", len(got), strings.Join(got, "\n"))
	}
	// A dry run neither takes the lock nor is stopped by it: it changes
	// nothing, and writes nothing under the state dir.
	o.SetDryRun(true)
	if err := o.Up(true); err != nil {
		t.Fatalf("a dry-run up under another's lock must proceed (it changes nothing), got: %v", err)
	}
	o.SetDryRun(false)
	release()
	if err := o.Up(true); err != nil {
		t.Fatalf("up after the lock was released: %v", err)
	}
	if indexOf(log(), "run -d") < 0 {
		t.Errorf("up must proceed once the lock is free, got %v", log())
	}
	if err := o.Down(false, "", false); err != nil {
		t.Fatalf("down after the lock was released: %v", err)
	}
}

// A finished command leaves the lock free (it holds it only while running),
// and writes its own pid into the file while it does — the note the refusal
// reads.
func TestAFinishedUpLeavesTheProjectFree(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, _ := fakeShim(t)
	o := orchestrator.New(lockProject(), rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	lockFile := filepath.Join(os.Getenv("XDG_STATE_HOME"), "opossum", "locked", "lock")
	if b, err := os.ReadFile(lockFile); err != nil || string(b) != strconv.Itoa(os.Getpid()) {
		t.Errorf("the lock file should carry this process's pid as a note, got %q (%v)", b, err)
	}
	// Free again: a stand-in for another process can take it.
	release := holdProjectLock(t, "locked", 1)
	release()
	if err := o.Up(true); err != nil {
		t.Fatalf("a second up after the first finished must proceed: %v", err)
	}
}

// The lock is let go on every way out — a failed `up` (rolled back) and a
// `destroy` alike — so the next command, from another process or this one
// (`watch` runs `up` again in-process after a failed rebuild), is not refused
// by a lock nobody holds.
func TestTheLockIsReleasedOnAFailedUpAndAfterDestroy(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, log := fakeShim(t)
	setShimEnv(rt, "RUN_FAIL=web.locked.opossum") // web's run fails, the up rolls back
	o := orchestrator.New(lockProject(), rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err == nil {
		t.Fatalf("this up is meant to fail")
	}
	release := holdProjectLock(t, "locked", 7)
	release() // another process could take it: the failed up let it go
	setShimEnv(rt, "RUN_FAIL=")
	if err := o.Destroy(orchestrator.DestroyPlan{}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	before := len(log())
	if err := o.Up(true); err != nil {
		t.Fatalf("up after destroy must proceed (the destroy let the lock go): %v", err)
	}
	if len(log()) == before {
		t.Errorf("the up after destroy must reach the runtime")
	}
}
