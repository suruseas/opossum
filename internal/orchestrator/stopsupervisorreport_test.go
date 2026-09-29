package orchestrator

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
)

// StopSupervisor's second return value is what #1401 adds: whether stopping was
// even attempted, independent of whether it was confirmed. Down and Destroy used
// to check only the first value, so "nothing was running" (ordinary, silent) and
// "asked, but couldn't confirm within budget" (worth telling the user about) were
// the same silence. This test pins the three shapes a caller can see, plus
// whether the pid file — the other thing a caller downstream (a later `down`, or
// a human) relies on — is cleared exactly when the stop was confirmed.
func TestStopSupervisorDistinguishesNotRunningFromNotConfirmed(t *testing.T) {
	t.Run("nothing running", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		stopped, attempted := StopSupervisor("demo-none")
		if stopped || attempted {
			t.Errorf("got stopped=%v attempted=%v, want false, false", stopped, attempted)
		}
	})

	t.Run("stops and confirms", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		shrinkStopBudgets(t, time.Second, time.Second)
		_, reaped := spawnAsSupervisor(t, "demo-stops", false)
		stopped, attempted := StopSupervisor("demo-stops")
		if !stopped || !attempted {
			t.Errorf("got stopped=%v attempted=%v, want true, true", stopped, attempted)
		}
		if pidFileExists(t, "demo-stops") {
			t.Errorf("a confirmed stop must clear the pid file")
		}
		waitReaped(t, reaped)
	})

	t.Run("escalates to SIGKILL for a process that ignores SIGTERM", func(t *testing.T) {
		// Proves SIGKILL is actually sent and actually needed, not merely
		// harmless against an already-dead process: `sleep` alone dies on
		// SIGTERM, so a mutation that deleted the SIGKILL call, or the
		// kill-branch's `stopped = true`, would still pass a test built on it.
		// A process that traps SIGTERM away can only die from SIGKILL (which
		// cannot be caught), so its death is real evidence the kill path ran.
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		shrinkStopBudgets(t, 20*time.Millisecond, time.Second)
		_, reaped := spawnAsSupervisor(t, "demo-escalates", true)
		stopped, attempted := StopSupervisor("demo-escalates")
		if !stopped || !attempted {
			t.Errorf("got stopped=%v attempted=%v, want true, true", stopped, attempted)
		}
		if pidFileExists(t, "demo-escalates") {
			t.Errorf("a confirmed stop must clear the pid file")
		}
		waitReaped(t, reaped)
	})

	t.Run("attempted but not confirmed", func(t *testing.T) {
		// No real process resists both SIGTERM and SIGKILL — SIGKILL cannot be
		// caught — so the only way to reach this branch is to make the
		// confirmation check itself lie and say "still alive" throughout the
		// wait, exactly as a CI runner too loaded to reap a zombie in time would
		// look from here (see #1312, which is where this branch was found). The
		// spawned process traps SIGTERM away, same as the escalation test above,
		// so it is genuinely still alive when the (stubbed) confirmation gives up
		// — the real SIGKILL that follows is what actually ends it.
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		shrinkStopBudgets(t, 5*time.Millisecond, 5*time.Millisecond)
		pid, reaped := spawnAsSupervisor(t, "demo-stuck", true)
		saved := processAliveFn
		processAliveFn = func(int) bool { return true }
		t.Cleanup(func() { processAliveFn = saved })

		stopped, attempted := StopSupervisor("demo-stuck")
		if stopped || !attempted {
			t.Errorf("got stopped=%v attempted=%v, want false, true", stopped, attempted)
		}
		if !pidFileExists(t, "demo-stuck") {
			t.Errorf("a stop that could not be confirmed must keep the pid file (reporting success would hide an orphan)")
		}
		// The real process is actually dead by now — StopSupervisor really did
		// send SIGKILL, processAliveFn only lied about seeing it happen. Confirmed
		// with the real check once reaped, not the stubbed one, so this test does
		// not itself leak a process (the exact failure #1312 is about).
		waitReaped(t, reaped)
		if processAlive(pid) {
			t.Fatalf("the spawned process is still alive — this test would leak it")
		}
	})
}

// spawnAsSupervisor starts a real, harmless child process, reaps it itself in the
// background (as StartSupervisor's real spawn does — see supervisor.go), and
// writes it into project's pid file as though it were that project's supervisor.
// Without the background reap, a process StopSupervisor kills is a zombie until
// something calls Wait on it, and processAlive's signal-0 check reads a zombie as
// still alive (the false positive #1312 traced this whole area to) — which would
// make "stops and confirms" indistinguishable from "not confirmed" for reasons
// that have nothing to do with StopSupervisor itself.
//
// termIgnoring spawns a shell that traps SIGTERM away, so only SIGKILL can end
// it — for a test that needs the SIGKILL path to actually run and actually
// matter, not merely be harmless against an already-dead `sleep`. It blocks
// until the child has actually installed the trap before returning: without that
// handshake, a SIGTERM sent to the shell before its `trap` statement runs would
// kill it via the ordinary default action, which would make the death look like
// evidence for the SIGKILL path when it was really nothing of the kind.
func spawnAsSupervisor(t *testing.T, project string, termIgnoring bool) (pid int, reaped <-chan struct{}) {
	t.Helper()
	var cmd *exec.Cmd
	if termIgnoring {
		readyR, readyW, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer readyR.Close()
		cmd = exec.Command("sh", "-c", `trap "" TERM; echo ready; exec sleep 30`)
		cmd.Stdout = readyW
		if err := cmd.Start(); err != nil {
			readyW.Close()
			t.Fatalf("starting a throwaway process: %v", err)
		}
		// The parent's own copy has to close right away, not deferred to this
		// function's return: the child holds its own duplicate for the write, and
		// the Read below blocks until every write end is closed. Keeping this one
		// open past Start would mean a child that died before writing left Read
		// hanging (on nothing, forever) instead of failing fast on EOF.
		readyW.Close()
		buf := make([]byte, 1)
		if _, err := readyR.Read(buf); err != nil {
			t.Fatalf("waiting for the spawned shell to install its TERM trap: %v", err)
		}
	} else {
		cmd = exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			t.Fatalf("starting a throwaway process: %v", err)
		}
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	pid = cmd.Process.Pid
	dir, err := supervisorStateDir(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := strconv.Itoa(pid) + " " + processStartedAt(pid) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "supervisor.pid"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return pid, done
}

// pidFileExists reports whether project's supervisor pid file is still there.
func pidFileExists(t *testing.T, project string) bool {
	t.Helper()
	path, err := supervisorPidFile(project)
	if err != nil {
		t.Fatal(err)
	}
	_, err = os.Stat(path)
	return err == nil
}

// waitReaped blocks until spawnAsSupervisor's background Wait has reaped the
// child, so a test doesn't check liveness (or end) before that has happened.
func waitReaped(t *testing.T, reaped <-chan struct{}) {
	t.Helper()
	select {
	case <-reaped:
	case <-time.After(5 * time.Second):
		t.Fatal("the spawned process was never reaped")
	}
}

// shrinkStopBudgets replaces StopSupervisor's wait budgets for the duration of a
// test, so exercising the "waited the whole budget" path costs milliseconds
// instead of the real four seconds.
func shrinkStopBudgets(t *testing.T, term, kill time.Duration) {
	t.Helper()
	savedTerm, savedKill, savedPoll := stopSupervisorTermWait, stopSupervisorKillWait, stopSupervisorPoll
	stopSupervisorTermWait, stopSupervisorKillWait, stopSupervisorPoll = term, kill, time.Millisecond
	t.Cleanup(func() {
		stopSupervisorTermWait, stopSupervisorKillWait, stopSupervisorPoll = savedTerm, savedKill, savedPoll
	})
}

// stopSupervisorAndReport is what Down and Destroy both call; this pins what each
// of StopSupervisor's three outcomes prints through it, so a future edit that
// collapses the not-confirmed branch back into the confirmed one (restoring the
// #1401 silence) fails here rather than only in a caller nobody happens to run.
// (Down and Destroy's OWN wiring to this method is pinned separately, at the
// black-box level, in stopsupervisorcallsites_test.go — this test only proves
// the shared helper's own dispatch is right.)
func TestStopSupervisorAndReportPrintsPerOutcome(t *testing.T) {
	newOrch := func(t *testing.T, project string, out *bytes.Buffer) *Orchestrator {
		t.Helper()
		return New(&compose.Project{Name: project}, nil, "opossum", out)
	}

	t.Run("nothing running: silent", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		var out bytes.Buffer
		newOrch(t, "demo-report-none", &out).stopSupervisorAndReport()
		if out.Len() != 0 {
			t.Errorf("got %q, want no output for a project with no supervisor running", out.String())
		}
	})

	t.Run("stops and confirms: says so", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		shrinkStopBudgets(t, time.Second, time.Second)
		_, reaped := spawnAsSupervisor(t, "demo-report-stops", false)
		var out bytes.Buffer
		newOrch(t, "demo-report-stops", &out).stopSupervisorAndReport()
		waitReaped(t, reaped)
		if got := out.String(); got != "Stopped the restart supervisor\n" {
			t.Errorf("got %q, want the plain stopped line", got)
		}
	})

	t.Run("attempted but not confirmed: warns with OPSM-414", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		shrinkStopBudgets(t, 5*time.Millisecond, 5*time.Millisecond)
		_, reaped := spawnAsSupervisor(t, "demo-report-stuck", true)
		saved := processAliveFn
		processAliveFn = func(int) bool { return true }
		t.Cleanup(func() { processAliveFn = saved })
		var out bytes.Buffer
		newOrch(t, "demo-report-stuck", &out).stopSupervisorAndReport()
		waitReaped(t, reaped)
		if got := out.String(); !strings.Contains(got, "OPSM-414") || strings.Contains(got, "Stopped the restart supervisor") {
			t.Errorf("got %q, want the OPSM-414 notice and not the plain stopped line", got)
		}
		// #1415: this notice reaches the user through three call sites — this
		// one (Down/Destroy) and two in cmd/opossum's own stderr prints (the
		// early stop, and up's replace path) — and all three must read the
		// same way; the other two already write "opossum: [OPSM-414] ...".
		// HasPrefix, not Contains: this is the only line the outcome prints, so
		// a double prefix ("opossum: opossum: [OPSM-414]") would still contain
		// the substring — HasPrefix catches that too (independent review).
		if got := out.String(); !strings.HasPrefix(got, "opossum: [OPSM-414]") {
			t.Errorf("got %q, want the same \"opossum: [OPSM-414]\" prefix cmd/opossum's two other call sites for this notice use", got)
		}
	})
}

// The one line printed when StopSupervisor could not confirm the exit, word for
// word — the line the whole change exists to make reachable at all.
func TestTheSupervisorStopFailedNoticeIsWordForWordWhatWeMeanToSay(t *testing.T) {
	want := "[OPSM-414] asked the restart supervisor to stop, but couldn't confirm it did — " +
		"it may still be watching this project's containers. Run `opossum ps` to check, and stop it " +
		"by hand (`kill`) if it's still there."
	if got := NoticeSupervisorStopFailed(); got != want {
		t.Errorf("the stop-failed notice is not what this file says it should be\n got: %s\nwant: %s", got, want)
	}
}
