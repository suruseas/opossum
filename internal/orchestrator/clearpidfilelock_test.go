package orchestrator

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"
)

// stalePidFile makes the project's state dir with a pid file no live process is behind, and
// returns its path.
func stalePidFile(t *testing.T, project string) string {
	t.Helper()
	pidFile, err := supervisorPidFile(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(pidFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidFile, []byte("2147483646 x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return pidFile
}

// What `down` clears must not be a claim another supervisor made while it was looking (#1550).
// clearPidFile read the file, found nobody behind it, and removed it with no lock — and a claim
// put in place between the two was removed with it, a supervisor `down` could no longer find.
// Measured as rounds in which a claim that succeeded left no pid file: a claim and a clear of a
// stale file started at once, and what is left must be the claim.
func TestAClearRacingAClaimLeavesTheClaim(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const rounds = 400
	lost := 0
	for round := 0; round < rounds; round++ {
		project := "clr" + strconv.Itoa(round)
		stalePidFile(t, project)
		var wg sync.WaitGroup
		start := make(chan struct{})
		var claimErr error
		wg.Add(2)
		go func() { defer wg.Done(); <-start; claimErr = ClaimSupervisor(project) }()
		wait := time.Duration(round%36) * 100 * time.Microsecond // sweeps the clear across the claim's whole length
		go func() { defer wg.Done(); <-start; time.Sleep(wait); clearPidFile(project) }()
		close(start)
		wg.Wait()
		if claimErr != nil {
			t.Fatalf("round %d: the claim over a stale file failed: %v", round, claimErr)
		}
		if SupervisorPID(project) != os.Getpid() {
			lost++
		}
	}
	if lost > 0 {
		t.Errorf("%d of %d rounds: the claim succeeded and its pid file was cleared", lost, rounds)
	}
}

// The lock is what keeps a clear out of a claim's middle: while another holds it, the clear waits,
// and goes on when it is let go. Read from outside, so that a clear that does not take the lock (and
// returns at once) is what fails here, without waiting for a race to turn up.
func TestAClearWaitsForTheLockAnotherHolds(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const project = "clrwait"
	pidFile := stalePidFile(t, project)
	lock, err := os.OpenFile(pidFile+".lock", os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { clearPidFile(project); close(done) }()
	select {
	case <-done:
		t.Fatalf("the clear went on while another held the lock")
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := os.Stat(pidFile); err != nil {
		t.Errorf("the pid file was removed while another held the lock: %v", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the clear did not go on once the lock was let go")
	}
	if _, err := os.Stat(pidFile); err == nil {
		t.Errorf("the stale pid file was not removed once the clear went on")
	}
}

// What the clear finds when it gets the lock is what it acts on, not what it saw before: a claim made
// while it waited is a claim, and stays. Held by the test the way a claim holds it, with the claim
// put in place before it is let go.
func TestAClearThatWaitedLeavesAClaimMadeMeanwhile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const project = "clrmeanwhile"
	pidFile := stalePidFile(t, project)
	lock, err := os.OpenFile(pidFile+".lock", os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { clearPidFile(project); close(done) }()
	time.Sleep(100 * time.Millisecond)
	me := os.Getpid()
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(me)+" "+processStartedAt(me)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the clear did not go on once the lock was let go")
	}
	if SupervisorPID(project) != me {
		t.Errorf("the claim made while the clear waited was removed")
	}
}

// What is no claim goes, whatever way it is not one: empty, a pid nothing runs under, a pid with no
// start marker to tie it to a process, a pid a process runs under now that is not the one that wrote it
// (a number macOS gave out again). And nothing is made where nothing was: a clear in a project that
// never had a state dir leaves none (the lock file is made only where the pid file could be).
func TestAClearRemovesWhatIsNoClaimAndMakesNothingElse(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for name, stale := range map[string]string{"empty": "", "a dead pid": "2147483646 x\n", "no marker": "1\n", "a live pid of another start": strconv.Itoa(os.Getpid()) + " not-me\n"} {
		t.Run(name, func(t *testing.T) {
			project := "clrstale" + strconv.Itoa(len(name))
			pidFile := stalePidFile(t, project)
			if err := os.WriteFile(pidFile, []byte(stale), 0o644); err != nil {
				t.Fatal(err)
			}
			clearPidFile(project)
			if _, err := os.Stat(pidFile); err == nil {
				t.Errorf("the pid file (%q) was not removed", stale)
			}
		})
	}
	t.Run("no state dir", func(t *testing.T) {
		const project = "clrnodir"
		pidFile, err := supervisorPidFile(project)
		if err != nil {
			t.Fatal(err)
		}
		clearPidFile(project)
		if _, err := os.Stat(filepath.Dir(pidFile)); err == nil {
			t.Errorf("the state dir was made by a clear")
		}
	})
}

// A claim in place, with a process behind it, is not removed by a clear that finds the lock free.
func TestAClearLeavesALiveClaim(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const project = "clrlive"
	pidFile := stalePidFile(t, project)
	me := os.Getpid()
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(me)+" "+processStartedAt(me)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clearPidFile(project)
	if SupervisorPID(project) != me {
		t.Errorf("a live claim was removed")
	}
}

// The lock is held until the file is gone: a clear that let it go after looking and before removing would
// let a claim in between, which is the window this closes. Read from outside — the test takes the lock the
// moment the clear lets it go, and the file must be gone by then. The file names a live pid with another
// start marker, so that the look runs `ps` and the stretch between the look and the remove is long enough
// to be caught (with a dead pid it is a few microseconds, and a clear that let go early passed this too).
func TestAClearHoldsTheLockUntilTheFileIsGone(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const rounds = 20
	sawHeld := 0
	for round := 0; round < rounds; round++ {
		project := "clrhold" + strconv.Itoa(round)
		pidFile := stalePidFile(t, project)
		if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())+" not-me\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		lock, err := os.OpenFile(pidFile+".lock", os.O_RDWR|os.O_CREATE, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { clearPidFile(project); close(done) }()
		held := false
	poll:
		for {
			if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				held = true
				continue
			}
			if held {
				if _, err := os.Stat(pidFile); err == nil {
					t.Errorf("round %d: the clear let go of the lock while the pid file was still there", round)
				}
				break
			}
			// The clear has not taken it yet (or is already done): let it go and look again.
			syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			select {
			case <-done:
				break poll
			default:
			}
		}
		if held {
			sawHeld++
		}
		syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		lock.Close()
		<-done
	}
	if sawHeld == 0 {
		t.Errorf("in %d rounds the clear was never seen holding the lock, so nothing here was checked", rounds)
	}
}
