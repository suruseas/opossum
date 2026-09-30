package orchestrator

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Claims made at once have one winner (#1500). A claim used to create the pid file and then
// write it; a racer that came between saw an empty file, read it as "nobody behind it", removed
// it and claimed over the top, so two supervisors stood (7 to 11 of 400 rounds of 8 claims at
// once, measured before the claim went under one lock).
func TestClaimsMadeAtOnceHaveOneWinner(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const rounds = 300
	bad := 0
	for round := 0; round < rounds; round++ {
		project := "claim" + string(rune('a'+round%26)) + string(rune('a'+round/26))
		var wins int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if err := ClaimSupervisor(project); err == nil {
					atomic.AddInt32(&wins, 1)
				}
			}()
		}
		close(start)
		wg.Wait()
		if wins != 1 {
			bad++
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d rounds of 8 claims at once did not have exactly one winner", bad, rounds)
	}
}

// The lock is what keeps a claim out of another claim's middle: while one holds it, a claim
// waits, and goes on when it is let go. Read from outside, so that a claim that does not take
// the lock (and returns at once) is what fails here, without waiting for a race to turn up.
func TestAClaimWaitsForTheLockAnotherHolds(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const project = "lockwait"
	pidFile, err := supervisorPidFile(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(pidFile), 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(pidFile+".lock", os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- ClaimSupervisor(project) }()
	select {
	case err := <-done:
		t.Fatalf("the claim went on while another held the lock (err = %v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := os.Stat(pidFile); err == nil {
		t.Errorf("the pid file was made while another held the lock")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the claim, let in, failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the claim did not go on once the lock was let go")
	}
	if SupervisorPID(project) == 0 {
		t.Errorf("the claim that went on left no live claim behind")
	}
}

// A file a crash left, with no live process behind it, is replaced by a new claim — and what is
// written is whole: pid and start marker, ready for a reader that takes no lock.
func TestAClaimReplacesAFileNoProcessIsBehindAndWritesItWhole(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const project = "stalefile"
	pidFile, err := supervisorPidFile(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(pidFile), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, stale := range map[string]string{"empty": "", "a dead pid": "2147483646 x\n", "no marker": "1\n"} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(pidFile, []byte(stale), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := ClaimSupervisor(project); err != nil {
				t.Fatalf("a claim over a stale file: %v", err)
			}
			b, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, started, ok := parsePidFile(string(b))
			if !ok || pid != os.Getpid() || started == "" {
				t.Errorf("the claim wrote %q, want this process's pid and a start marker", b)
			}
			if fi, err := os.Stat(pidFile); err != nil || fi.Mode().Perm() != 0o644 {
				t.Errorf("the claim's file has mode %v (err %v), want 0644 as before", fi.Mode().Perm(), err)
			}
			os.Remove(pidFile)
		})
	}
	entries, _ := os.ReadDir(filepath.Dir(pidFile))
	for _, e := range entries {
		if matched, _ := filepath.Match("supervisor.pid.tmp-*", e.Name()); matched {
			t.Errorf("a temp file was left behind: %s", e.Name())
		}
	}
}

// A claim that cannot put its file in place says so, and leaves nothing of its own behind
// (#1500): an error dropped here would start a supervisor with no pid file, one `down` cannot
// find and cannot stop. A directory that holds something is where the pid file should be, so
// the temp file is made and the rename is what fails.
func TestAClaimThatCannotPlaceItsFileSaysSoAndLeavesNoTemp(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const project = "cannotplace"
	pidFile, err := supervisorPidFile(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pidFile, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ClaimSupervisor(project); err == nil {
		t.Fatalf("the claim reported success with a directory where its file goes")
	}
	entries, _ := os.ReadDir(filepath.Dir(pidFile))
	for _, e := range entries {
		if matched, _ := filepath.Match("supervisor.pid.tmp-*", e.Name()); matched {
			t.Errorf("a temp file was left behind after the failed claim: %s", e.Name())
		}
	}
}

// A reader that takes no lock never sees a claim half written (#1500): the file is there whole
// or not at all. Claims are made over and over — each over a stale file, as a crash leaves one —
// while another goroutine reads the file as fast as it can; whatever it reads must parse, with
// the start marker in it. A claim written in place (truncate, then write) shows an empty file to
// a reader in between, and `down` would read that as nobody behind it.
func TestAReaderNeverSeesAClaimHalfWritten(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const project = "neverhalf"
	pidFile, err := supervisorPidFile(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(pidFile), 0o755); err != nil {
		t.Fatal(err)
	}
	var stop int32
	var torn int32
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for atomic.LoadInt32(&stop) == 0 {
			b, err := os.ReadFile(pidFile)
			if err != nil {
				continue
			}
			if pid, started, ok := parsePidFile(string(b)); !ok || pid <= 0 || started == "" {
				atomic.AddInt32(&torn, 1)
			}
		}
	}()
	for i := 0; i < 1200; i++ {
		os.Remove(pidFile) // a stale file is what the claim replaces; none is what it creates
		if i%2 == 0 {
			// The stale file is put in place whole too: written in place, it is the test that
			// would show the reader an empty file.
			stale := pidFile + ".stale"
			os.WriteFile(stale, []byte("2147483646 x\n"), 0o644)
			os.Rename(stale, pidFile)
		}
		if err := ClaimSupervisor(project); err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
	}
	atomic.StoreInt32(&stop, 1)
	wg.Wait()
	if n := atomic.LoadInt32(&torn); n > 0 {
		t.Errorf("a reader saw a claim that was not whole %d times", n)
	}
}
