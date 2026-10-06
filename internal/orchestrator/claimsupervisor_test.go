package orchestrator

import (
	"os"
	"path/filepath"
	"strconv"
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
	var firstErr atomic.Value // the first error of a round that was not "already supervised"
	for round := 0; round < rounds; round++ {
		firstErr.Store("")
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
				} else if !ErrAlreadySupervised(err) {
					firstErr.CompareAndSwap("", err.Error())
				}
			}()
		}
		close(start)
		wg.Wait()
		if wins != 1 {
			bad++
			if bad == 1 {
				t.Logf("round %d (%s): %d claims won; the others said: %v", round, project, wins, firstErr.Load())
				if pidFile, err := supervisorPidFile(project); err == nil {
					b, rerr := os.ReadFile(pidFile)
					t.Logf("its pid file (%v): %q; its token now is %q; SupervisorPID = %d", rerr, b, processStartedAt(os.Getpid()), SupervisorPID(project))
				}
			}
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

// usePsReading puts a `ps` on PATH that answers with the next of the given start times on each
// call, and a /proc with no such process (so a token comes from `ps`), or, with procStat set,
// a /proc whose stat file for this process says so.
func usePsReading(t *testing.T, procStat string, readings ...string) {
	t.Helper()
	proc := t.TempDir()
	if procStat != "" {
		dir := filepath.Join(proc, strconv.Itoa(os.Getpid()))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(procStat), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := procRoot
	procRoot = proc
	t.Cleanup(func() { procRoot = old })
	bin := t.TempDir()
	count := filepath.Join(t.TempDir(), "count")
	script := "#!/bin/sh\nn=$(cat '" + count + "' 2>/dev/null || echo 0)\necho $((n+1)) > '" + count + "'\ncase $n in\n"
	for i, r := range readings {
		script += strconv.Itoa(i) + ") echo '" + r + "';;\n"
	}
	script += "*) echo '" + readings[len(readings)-1] + "';;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "ps"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// The start time `ps` gives for one process can move by a second between two calls (a
// clock being corrected: the boot time it is counted from moves), and a claim must not read
// its own holder as gone because of it (#1610). A /proc stat file does not move, and the
// token comes from it where it is. Each row makes two claims one after the other.
func TestAMovingStartTimeDoesNotLetASecondClaimTakeTheFirst(t *testing.T) {
	// a stat line whose command name holds spaces and parentheses; field 22 is 123456.
	stat := "4242 (a) b (c d) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 123456 24 25\n"
	cases := []struct {
		name     string
		procStat string
		ps       []string
	}{
		{"from /proc, whatever ps says", stat, []string{"Thu Oct 1 17:46:22 2026", "Thu Oct 1 17:46:23 2026", "Thu Oct 1 17:46:21 2026"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			usePsReading(t, c.procStat, c.ps...)
			if err := ClaimSupervisor("moving"); err != nil {
				t.Fatalf("the first claim: %v", err)
			}
			if err := ClaimSupervisor("moving"); !ErrAlreadySupervised(err) {
				t.Fatalf("the second claim = %v, want it refused as already supervised", err)
			}
		})
	}
}

// The token is field 22 counted from the last ")" of the line.
func TestTheProcTokenIsFieldTwentyTwo(t *testing.T) {
	usePsReading(t, "1 (x) y) z (w) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 777 24\n", "unused")
	if got, want := processStartedAt(os.Getpid()), "proc-"; got != want+"777" {
		t.Fatalf("processStartedAt = %q, want %q", got, want+"777")
	}
}

// A pid file a version that used `ps` wrote (no prefix) is still read with `ps`, so a running
// supervisor of that version is still found by this one.
func TestAPidFileFromBeforeTheProcTokenIsReadWithPs(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	usePsReading(t, "", "Thu Oct 1 17:46:22 2026")
	pidFile, err := supervisorPidFile("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(pidFile), 0o755); err != nil {
		t.Fatal(err)
	}
	line := strconv.Itoa(os.Getpid()) + " Thu-Oct-1-17:46:22-2026\n"
	if err := os.WriteFile(pidFile, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := SupervisorPID("legacy"); got != os.Getpid() {
		t.Fatalf("SupervisorPID = %d, want %d", got, os.Getpid())
	}
}

// A stat line too short to have a start tick, or with no ")", gives no /proc token (and is no
// panic): the token then comes from `ps`.
func TestAShortProcStatFallsBackToPs(t *testing.T) {
	for name, stat := range map[string]string{
		"too few fields": "1 (x) S 1 2 3\n",
		"no parenthesis": "1 x S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 777 24 25\n",
		"empty":          "",
	} {
		t.Run(name, func(t *testing.T) {
			usePsReading(t, stat, "Thu Oct 1 17:46:22 2026")
			if got, want := processStartedAt(os.Getpid()), "Thu-Oct-1-17:46:22-2026"; got != want {
				t.Fatalf("processStartedAt = %q, want %q", got, want)
			}
		})
	}
}

// A pid file whose /proc token is not the one the process has now is not that process: the
// pid was reused. Read as held, StopSupervisor would signal a process that is not ours. One
// row per way a token can wrongly match.
func TestAProcTokenThatIsNotTheProcessesIsAReusedPid(t *testing.T) {
	stat := "1 (x) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 123456 24 25\n"
	for name, token := range map[string]string{
		"another tick":                "proc-1",
		"a prefix of the tick":        "proc-12345",
		"the tick with a tail":        "proc-1234567",
		"no tick at all":              "proc-",
		"the same tick without a tag": "123456",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			usePsReading(t, stat, "Thu Oct 1 17:46:22 2026")
			writePidFile(t, "reused", strconv.Itoa(os.Getpid())+" "+token+"\n")
			if got := SupervisorPID("reused"); got != 0 {
				t.Fatalf("SupervisorPID = %d for the token %q, want 0 (not the process)", got, token)
			}
			// A reused pid is stale, not a process that could not be checked: left as unknown, a file that
			// means nothing would be kept for good and every claim after it refused.
			if _, unknown := lookSupervisor("reused"); unknown {
				t.Fatalf("lookSupervisor says the process could not be checked, for the token %q", token)
			}
		})
	}
	t.Run("the token it has", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		usePsReading(t, stat, "Thu Oct 1 17:46:22 2026")
		writePidFile(t, "same", strconv.Itoa(os.Getpid())+" proc-123456\n")
		if got := SupervisorPID("same"); got != os.Getpid() {
			t.Fatalf("SupervisorPID = %d, want %d", got, os.Getpid())
		}
	})
	t.Run("no stat for the process", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		usePsReading(t, "", "Thu Oct 1 17:46:22 2026")
		writePidFile(t, "gone", strconv.Itoa(os.Getpid())+" proc-\n")
		if got := SupervisorPID("gone"); got != 0 {
			t.Fatalf("SupervisorPID = %d for a proc token with no /proc entry, want 0", got)
		}
	})
}

func writePidFile(t *testing.T, project, content string) {
	t.Helper()
	pidFile, err := supervisorPidFile(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(pidFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
