package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const flakyPsAnswer = "Thu-Oct-1-17:46:22-2026"

// useFlakyPs puts a `ps` on PATH that fails (exit 1, nothing printed) for its first `failures` calls and
// answers with a start time after that — and only when it is asked about this process (`-p <pid>`): a claim that
// asked for another process's start time would otherwise be told this one's. A /proc with no such process is put
// in procRoot, so the token comes from `ps`. It returns the file `ps` counts its calls in.
func useFlakyPs(t *testing.T, failures int) (count string) {
	t.Helper()
	old := procRoot
	procRoot = t.TempDir()
	t.Cleanup(func() { procRoot = old })
	bin := t.TempDir()
	count = filepath.Join(t.TempDir(), "count")
	script := "#!/bin/sh\nn=$(cat '" + count + "' 2>/dev/null || echo 0)\necho $((n+1)) > '" + count + "'\n" +
		"[ \"$4\" = " + strconv.Itoa(os.Getpid()) + " ] || exit 1\n" +
		"if [ $n -lt " + strconv.Itoa(failures) + " ]; then exit 1; fi\necho 'Thu Oct 1 17:46:22 2026'\n"
	if err := os.WriteFile(filepath.Join(bin, "ps"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldWait := psWait
	psWait = time.Millisecond
	t.Cleanup(func() { psWait = oldWait })
	return count
}

// psCalls is how many times the fake `ps` was run.
func psCalls(t *testing.T, count string) int {
	t.Helper()
	b, err := os.ReadFile(count)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

// A claim is made with a start marker, or not at all. The marker is what ties the pid file to its process, and a
// file with none is read as stale (a pid file an older version wrote, or a crash left): a claim written without one
// was taken by nobody, a second claim succeeded over it, and `down` found nobody to stop. With `ps` failing — it is the
// only source of the marker where there is no /proc, and it can fail when a machine is loaded — both claims of
// the pair succeeded and the file held `<pid> ` and nothing after it (measured, with a `ps` that exits 1).
func TestAClaimIsRefusedWhenItsStartMarkerCannotBeRead(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	count := useFlakyPs(t, 1<<30)
	const project = "nomarker"
	for i := 0; i < 2; i++ {
		err := ClaimSupervisor(project)
		if err == nil {
			t.Fatalf("claim %d: made with no start marker", i)
		}
		if ErrAlreadySupervised(err) {
			t.Fatalf("claim %d: %v — no claim stands, so this is not another supervisor's", i, err)
		}
		if !strings.Contains(err.Error(), "ps") {
			t.Errorf("claim %d: %q does not say what could not be read, or where it comes from", i, err)
		}
	}
	pidFile, err := supervisorPidFile(project)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(pidFile); err == nil {
		t.Errorf("a pid file was left behind by a claim that was refused: %q", b)
	}
	// 1 + psTries readings for each of the two claims: asked again as many times as is meant, no more.
	if got, want := psCalls(t, count), 2*(1+psTries); got != want {
		t.Errorf("`ps` was run %d times for two refused claims, want %d (one reading and %d more each)", got, want, psTries)
	}
}

// A `ps` that fails as many times as the marker is asked for again, and then answers (a fork that did not go
// through, on a machine that is busy), does not keep a supervisor from starting; one that fails once more does.
func TestAClaimAsksForItsStartMarkerAgainAsManyTimesAsItsBudgetSays(t *testing.T) {
	for name, tc := range map[string]struct {
		failures int
		ok       bool
	}{
		"fails once":                          {1, true},
		"fails as often as it is asked again": {psTries, true},
		"fails once more than that":           {psTries + 1, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			useFlakyPs(t, tc.failures)
			const project = "psretry"
			err := ClaimSupervisor(project)
			if tc.ok {
				if err != nil {
					t.Fatalf("claim after %d failed readings of the marker: %v", tc.failures, err)
				}
				pidFile, perr := supervisorPidFile(project)
				if perr != nil {
					t.Fatal(perr)
				}
				b, rerr := os.ReadFile(pidFile)
				if rerr != nil {
					t.Fatal(rerr)
				}
				if _, started, ok := parsePidFile(string(b)); !ok || started != flakyPsAnswer {
					t.Errorf("the claim has no start marker from the reading that worked: %q", b)
				}
				if got := SupervisorPID(project); got != os.Getpid() {
					t.Errorf("SupervisorPID = %d after the claim, want this process (%d): the marker is not this process's own", got, os.Getpid())
				}
			} else if err == nil {
				t.Fatalf("claim made after %d failed readings, which is more than it asks for again", tc.failures)
			}
		})
	}
}

// The asks for the marker are spaced by psWait: a `ps` that fails because the machine is busy is not
// asked again at once.
func TestAClaimWaitsBetweenAsksForItsStartMarker(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	useFlakyPs(t, 1<<30)
	psWait = 100 * time.Millisecond // far more than the runs of `ps` take by themselves; useFlakyPs's cleanup puts the old value back
	start := time.Now()
	if err := ClaimSupervisor("psspaced"); err == nil {
		t.Fatal("claim made with no start marker")
	}
	if took, least := time.Since(start), time.Duration(psTries)*psWait; took < least {
		t.Errorf("the claim gave up after %v, want at least %d waits of %v (%v)", took, psTries, psWait, least)
	}
}

// What `up` is told when the watcher it started dies at once (#1739). The watcher is a hidden re-run of the binary;
// here it is a script. One that exits with an error is reported, with the last line it wrote to the log; one that
// exits cleanly lost the claim to another watcher and is not; one that is still starting is taken as started.
func TestUpIsToldWhenTheWatcherItStartedDiesAtOnce(t *testing.T) {
	for name, tc := range map[string]struct {
		script  string
		wantErr string // "" for no error
	}{
		"exits with an error": {"echo 'opossum: cannot read this process start time' >&2\nexit 1\n", "cannot read this process start time"},
		"exits cleanly":       {"exit 0\n", ""},
		"is still starting":   {"sleep 5\n", ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			self := filepath.Join(t.TempDir(), "self")
			if err := os.WriteFile(self, []byte("#!/bin/sh\n"+tc.script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("OPOSSUM_SELF_BIN", self)
			old := supervisorStartWait
			supervisorStartWait = 400 * time.Millisecond
			t.Cleanup(func() { supervisorStartWait = old })
			pid, err := StartSupervisor("diesatonce", t.TempDir(), nil)
			t.Cleanup(func() {
				if pid > 0 {
					syscall.Kill(pid, syscall.SIGKILL)
				}
			})
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("StartSupervisor = %v, want no error", err)
			case tc.wantErr != "" && err == nil:
				t.Errorf("StartSupervisor said nothing of a watcher that died at once")
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Errorf("StartSupervisor = %q, want it to carry the last line the watcher wrote (%q)", err, tc.wantErr)
			}
		})
	}
}

// A watcher that takes its claim is not waited for any longer: the wait ends when the claim is seen, not when the
// time is up. The watcher here is this test binary, run again as a helper that claims the project and stays.
func TestUpDoesNotWaitOutTheWholeTimeForAWatcherThatHasClaimed(t *testing.T) {
	if os.Getenv("OPOSSUM_TEST_CLAIMING_WATCHER") != "" {
		// TestMain gives every process of this package a state directory of its own; the parent's is handed over.
		os.Setenv("XDG_STATE_HOME", os.Getenv("OPOSSUM_TEST_CLAIMING_WATCHER_STATE"))
		if err := ClaimSupervisor(os.Getenv("OPOSSUM_TEST_CLAIMING_WATCHER")); err != nil {
			fmt.Fprintln(os.Stderr, "helper claim:", err)
			os.Exit(3)
		}
		time.Sleep(30 * time.Second)
		return
	}
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	const project = "claimsatonce"
	t.Setenv("OPOSSUM_TEST_CLAIMING_WATCHER_STATE", state)
	t.Setenv("OPOSSUM_SELF_BIN", os.Args[0])
	t.Setenv("OPOSSUM_TEST_CLAIMING_WATCHER", project)
	old := supervisorStartWait
	supervisorStartWait = 20 * time.Second
	t.Cleanup(func() { supervisorStartWait = old })
	start := time.Now()
	wd, err := os.Getwd() // the helper is this package's test binary, whose TestMain builds a shim from its working directory
	if err != nil {
		t.Fatal(err)
	}
	pid, err := StartSupervisor(project, wd, []string{"-test.run=^TestUpDoesNotWaitOutTheWholeTimeForAWatcherThatHasClaimed$"})
	t.Cleanup(func() {
		if pid > 0 {
			syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	if err != nil {
		t.Fatalf("StartSupervisor: %v", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("StartSupervisor took %v for a watcher that claimed at once; the wait is meant to end when the claim is seen", took)
	}
	if got := SupervisorPID(project); got != pid {
		t.Errorf("SupervisorPID = %d, want the watcher that was started (%d)", got, pid)
	}
}

// How far a claim goes to read its own start marker is a decision, not an accident of the code: five more readings,
// 50 ms apart (a quarter of a second, with the readings themselves). The other tests are written against the numbers
// the code holds, so a change to them has to be made here too, on purpose.
func TestTheStartMarkerIsAskedForFiveMoreTimesFiftyMillisecondsApart(t *testing.T) {
	if psTries != 5 || psWait != 50*time.Millisecond {
		t.Errorf("psTries = %d, psWait = %v; want 5 and 50ms", psTries, psWait)
	}
}
