package orchestrator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// useReadPs puts a `ps` on PATH that answers for one pid only: it fails for its first `failures` calls about that pid
// (exit 1, nothing printed) and gives `answer` after that. Any other pid it knows nothing of (exit 1). A /proc with
// no such process is put in procRoot, so a token comes from `ps`. It returns the file `ps` counts its calls in.
func useReadPs(t *testing.T, pid int, failures int, answer string) (count string) {
	t.Helper()
	old := procRoot
	procRoot = t.TempDir()
	t.Cleanup(func() { procRoot = old })
	oldWait := psWait
	psWait = time.Millisecond
	t.Cleanup(func() { psWait = oldWait })
	bin := t.TempDir()
	count = filepath.Join(t.TempDir(), "count")
	// This process's own start time is always answered (a claim asks for it first); the other pid's is the one
	// that fails or answers as given; any other pid is not known.
	script := "#!/bin/sh\nn=$(cat '" + count + "' 2>/dev/null || echo 0)\n" +
		"if [ \"$4\" = " + strconv.Itoa(os.Getpid()) + " ]; then echo 'Thu Oct 1 17:46:22 2026'; exit 0; fi\n" +
		"[ \"$4\" = " + strconv.Itoa(pid) + " ] || exit 1\n" +
		"echo $((n+1)) > '" + count + "'\n" +
		"if [ $n -lt " + strconv.Itoa(failures) + " ]; then exit 1; fi\necho '" + answer + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "ps"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return count
}

// aLiveOtherProcess is a process of its own that is there for the length of the test: what a supervisor is, as far as a
// pid file is concerned.
func aLiveOtherProcess(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Reaped as soon as it exits, so that a signal that killed it shows as a process that is gone (a zombie
	// answers signal 0 as if it were there).
	go cmd.Wait()
	t.Cleanup(func() { cmd.Process.Kill() })
	return cmd.Process.Pid
}

// pidFileOf writes the project's pid file naming pid with the start marker given, and returns its path.
func pidFileOf(t *testing.T, project string, pid int, marker string) string {
	t.Helper()
	path, err := supervisorPidFile(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)+" "+marker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const readPsMarker = "Thu-Oct-1-17:46:22-2026"

// A `ps` that fails once when the pid file is read is asked again, and the claim in the file is still the claim:
// read as stale, it let a second claim go over a live supervisor, and `down` leave it running with no file (#1755).
func TestAPidFileIsReadAgainWhenPsFailsOnce(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	pid := aLiveOtherProcess(t)
	useReadPs(t, pid, 1, strings.ReplaceAll(readPsMarker, "-", " "))
	pidFileOf(t, "readagain", pid, readPsMarker)
	if got := SupervisorPID("readagain"); got != pid {
		t.Errorf("SupervisorPID = %d after one failed reading, want %d", got, pid)
	}
}

// With `ps` failing every time, a live process behind the file is neither this project's supervisor nor a stale
// file, and nothing is done to it or to the file: `down` does not remove the file of a supervisor it cannot find again
// (it removed it, and the supervisor ran on), and it does not signal what may be another process by now. It reports
// the stop as asked and not confirmed, which is what makes the caller tell the reader to look.
func TestAPidFileWhosePidPsWillNotAnswerForIsLeftAlone(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	pid := aLiveOtherProcess(t)
	useReadPs(t, pid, 1<<30, "")
	const project = "unknown"
	pidFile := pidFileOf(t, project, pid, readPsMarker)

	t.Run("StopSupervisor leaves the file and the process, and says it could not confirm", func(t *testing.T) {
		stopped, attempted := StopSupervisor(project)
		if stopped || !attempted {
			t.Errorf("StopSupervisor = (stopped %v, attempted %v), want (false, true)", stopped, attempted)
		}
		if _, err := os.Stat(pidFile); err != nil {
			t.Errorf("the pid file of a process that could not be checked was removed: %v", err)
		}
		if err := syscall.Kill(pid, 0); err != nil {
			t.Errorf("the process was signalled (it is gone: %v)", err)
		}
	})
	t.Run("clearPidFile leaves it", func(t *testing.T) {
		clearPidFile(project)
		if _, err := os.Stat(pidFile); err != nil {
			t.Errorf("clearPidFile removed it: %v", err)
		}
	})
	t.Run("a claim does not go over it", func(t *testing.T) {
		err := ClaimSupervisor(project)
		if err == nil {
			t.Fatal("a claim went over a pid file whose process could not be checked")
		}
		if ErrAlreadySupervised(err) {
			t.Errorf("%v: it is not known that another supervisor holds the project, and the message should not say so", err)
		}
		if b, _ := os.ReadFile(pidFile); !strings.HasPrefix(string(b), strconv.Itoa(pid)+" ") {
			t.Errorf("the pid file was replaced: %q", b)
		}
	})
	t.Run("SupervisorPID does not name it", func(t *testing.T) {
		if got := SupervisorPID(project); got != 0 {
			t.Errorf("SupervisorPID = %d for a process that could not be checked, want 0 (nothing is known to be this project's)", got)
		}
	})
}

// What is not in doubt is as it was. A pid that is another process now (`ps` answers, with another start time) is a
// stale file: removed, and a claim goes over it. A pid no process has is stale. And a process that is the supervisor
// (`ps` answers with the marker the file has) is found, and stopped.
func TestAPidFileThatPsAnswersForIsJudgedAsItWas(t *testing.T) {
	for name, tc := range map[string]struct {
		answer    string // what `ps` says of the live process (spaces: `ps` prints them)
		dead      bool   // the pid is no process at all
		wantStale bool
	}{
		"another start time (the pid is reused)": {"Fri Oct 2 08:00:00 2026", false, true},
		"no such process":                        {"", true, true},
		"the marker the file has":                {"Thu Oct 1 17:46:22 2026", false, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			pid := aLiveOtherProcess(t)
			if tc.dead {
				pid = 2147483646
			}
			useReadPs(t, pid, 0, tc.answer)
			const project = "judged"
			pidFile := pidFileOf(t, project, pid, readPsMarker)
			got := SupervisorPID(project)
			if tc.wantStale && got != 0 {
				t.Errorf("SupervisorPID = %d, want a stale file (0)", got)
			}
			if !tc.wantStale && got != pid {
				t.Errorf("SupervisorPID = %d, want %d", got, pid)
			}
			clearPidFile(project)
			_, err := os.Stat(pidFile)
			if tc.wantStale && err == nil {
				t.Error("a stale file was not removed")
			}
			if !tc.wantStale && err != nil {
				t.Errorf("a live supervisor's file was removed: %v", err)
			}
		})
	}
}

// How far a read of a start time goes is a decision: five more asks, 50 ms apart.
func TestPsIsAskedForFiveMoreTimesFiftyMillisecondsApart(t *testing.T) {
	if psTries != 5 || psWait != 50*time.Millisecond {
		t.Errorf("psTries = %d, psWait = %v; want 5 and 50ms", psTries, psWait)
	}
}

// Asked as often as the budget says, and no more: a `ps` that never answers is run 1+psTries times for one look.
func TestALookAsksPsAsManyTimesAsItsBudgetSays(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	pid := aLiveOtherProcess(t)
	count := useReadPs(t, pid, 1<<30, "")
	pidFileOf(t, "budget", pid, readPsMarker)
	SupervisorPID("budget")
	if got, want := psCalls(t, count), 1+psTries; got != want {
		t.Errorf("`ps` was run %d times for one look, want %d", got, want)
	}
}

// `ps` that prints nothing and exits 0 (rather than failing) says nothing as well, and is asked again.
func TestAPsThatAnswersWithNothingIsAskedAgain(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	pid := aLiveOtherProcess(t)
	old := procRoot
	procRoot = t.TempDir()
	t.Cleanup(func() { procRoot = old })
	oldWait := psWait
	psWait = time.Millisecond
	t.Cleanup(func() { psWait = oldWait })
	bin := t.TempDir()
	count := filepath.Join(t.TempDir(), "count")
	script := "#!/bin/sh\nn=$(cat '" + count + "' 2>/dev/null || echo 0)\necho $((n+1)) > '" + count + "'\n" +
		"if [ $n -lt 2 ]; then exit 0; fi\necho 'Thu Oct 1 17:46:22 2026'\n"
	if err := os.WriteFile(filepath.Join(bin, "ps"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	pidFileOf(t, "emptyout", pid, readPsMarker)
	if got := SupervisorPID("emptyout"); got != pid {
		t.Errorf("SupervisorPID = %d after two empty answers, want %d", got, pid)
	}
}

// A process that is there when the file is read and gone when `ps` is asked about it is a stale file, not one that
// could not be checked: `ps` has nothing to say of it because it is not there.
func TestAProcessThatDiesWhilePsIsBeingAskedIsStale(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	pid := aLiveOtherProcess(t)
	old := procRoot
	procRoot = t.TempDir()
	t.Cleanup(func() { procRoot = old })
	bin := t.TempDir()
	script := "#!/bin/sh\nkill -9 " + strconv.Itoa(pid) + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "ps"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldWait := psWait
	psWait = 10 * time.Millisecond
	t.Cleanup(func() { psWait = oldWait })
	pidFileOf(t, "diesduring", pid, readPsMarker)
	if got, unknown := lookSupervisor("diesduring"); got != 0 || unknown {
		t.Errorf("lookSupervisor = (%d, %v) for a process that is gone, want (0, false): stale", got, unknown)
	}
}

// A file with no start marker is stale whatever process the pid is now: the marker is what ties it to one, and
// StopSupervisor signals what the pid names. The pid here is a live process that is not anyone's supervisor — pid 1,
// which an ordinary user cannot signal at all, ended the check at `processAlive` and never reached the marker.
func TestAPidFileWithNoMarkerIsStaleEvenWhereTheProcessIsAlive(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	pid := aLiveOtherProcess(t)
	useReadPs(t, pid, 0, "Thu Oct 1 17:46:22 2026")
	const project = "nomarkerlive"
	path, err := supervisorPidFile(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, unknown := lookSupervisor(project); got != 0 || unknown {
		t.Errorf("lookSupervisor = (%d, %v), want (0, false): a file with no marker is stale", got, unknown)
	}
	if stopped, attempted := StopSupervisor(project); stopped || attempted {
		t.Errorf("StopSupervisor = (%v, %v), want (false, false): nothing was asked of a process that is not the supervisor", stopped, attempted)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Errorf("the process was signalled: %v", err)
	}
}

// What `ps` says of the supervisor, where one stands behind the pid file. Running, with its pid and log; not
// checked, said so and told how to look, where it used to be no line at all (and read as "no supervisor"); none.
func TestPsSaysWhenTheSupervisorCouldNotBeChecked(t *testing.T) {
	t.Run("running", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		pid := aLiveOtherProcess(t)
		useReadPs(t, pid, 0, "Thu Oct 1 17:46:22 2026")
		pidFileOf(t, "psrun", pid, readPsMarker)
		if got := supervisorLine("psrun"); !strings.HasPrefix(got, "restart supervisor: running (pid "+strconv.Itoa(pid)+")") {
			t.Errorf("supervisorLine = %q", got)
		}
	})
	t.Run("could not be checked", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		pid := aLiveOtherProcess(t)
		useReadPs(t, pid, 1<<30, "")
		pidFileOf(t, "psunk", pid, readPsMarker)
		got := supervisorLine("psunk")
		if !strings.Contains(got, "could not be checked") || !strings.Contains(got, "ps -p "+strconv.Itoa(pid)) {
			t.Errorf("supervisorLine = %q, want it to say the supervisor could not be checked and how to look", got)
		}
		if strings.Contains(got, "running") {
			t.Errorf("supervisorLine = %q says running of a process that could not be checked", got)
		}
	})
	t.Run("none", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		if got := supervisorLine("psnone"); got != "" {
			t.Errorf("supervisorLine = %q for a project with no pid file, want none", got)
		}
	})
}
