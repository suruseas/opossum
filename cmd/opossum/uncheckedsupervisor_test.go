package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A live process stands behind the project's pid file and `ps` will not say whether it is the supervisor (#1759). `down` has not
// signalled it, so it does not say it asked; `up` has not started a new supervisor over it, so it says that, and does not say that
// it could not start one (the old one may be watching).
func TestUpAndDownSayWhatWasDoneWhenPsWillNotVouchForTheSupervisor(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	setOrUnset(t, "COMPOSE_PROFILES", nil)
	pid := anUncheckedSupervisor(t, state, "unchecked")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: unchecked\nservices:\n  web:\n    image: web\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	t.Run("up starts none over it and says so", func(t *testing.T) {
		out, err := run(t, "up", "--no-build")
		if err != nil {
			t.Fatalf("up: %v\n%s", err, out)
		}
		for _, want := range []string{"so no new one is started", fmt.Sprintf("process %d", pid), fmt.Sprintf("ps -p %d", pid)} {
			if !strings.Contains(out, want) {
				t.Errorf("up did not say %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "couldn't start the restart supervisor") {
			t.Errorf("up says it could not start a supervisor, when it chose not to:\n%s", out)
		}
		if got := supervisorPID(t, state, "unchecked"); got != pid {
			t.Errorf("the pid file names %d after up, want %d (nothing was claimed over it)", got, pid)
		}
	})

	t.Run("the early stop in down, which goes by the name given with -p, says it did not ask", func(t *testing.T) {
		out, err := run(t, "-p", "unchecked", "down")
		if err != nil {
			t.Fatalf("down: %v\n%s", err, out)
		}
		for _, want := range []string{"was not asked to stop", fmt.Sprintf("ps -p %d", pid)} {
			if !strings.Contains(out, want) {
				t.Errorf("the early stop did not say %q:\n%s", want, out)
			}
		}
		if strings.Count(out, "OPSM-414") != 1 {
			t.Errorf("the notice is not said once:\n%s", out)
		}
	})

	t.Run("down says it did not ask", func(t *testing.T) {
		out, err := run(t, "down")
		if err != nil {
			t.Fatalf("down: %v\n%s", err, out)
		}
		for _, want := range []string{"OPSM-414", "was not asked to stop", fmt.Sprintf("ps -p %d", pid)} {
			if !strings.Contains(out, want) {
				t.Errorf("down did not say %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "asked the restart supervisor to stop") {
			t.Errorf("down says it asked the supervisor, and it did not:\n%s", out)
		}
		if err := syscall.Kill(pid, 0); err != nil {
			t.Errorf("the process was signalled: %v", err)
		}
	})
}

// anUncheckedSupervisor puts a live process behind the project's pid file and a `ps` on PATH that answers nothing, and returns the pid.
func anUncheckedSupervisor(t *testing.T, state, project string) int {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ps"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait()
	t.Cleanup(func() { cmd.Process.Kill() })
	pid := cmd.Process.Pid
	pidFile := filepath.Join(state, "opossum", project, "supervisor.pid")
	if err := os.MkdirAll(filepath.Dir(pidFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d Thu-Oct-1-17:46:22-2026\n", pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	return pid
}

// reportSupervisorStop is what the early stop in `down` and the replace path of `up` print: for a supervisor that was not signalled
// (the pid read before the stop) it says it was not asked, and for one that was asked (0) it says it was (#1759).
func TestReportSupervisorStopSaysWhetherTheSupervisorWasAsked(t *testing.T) {
	for _, tc := range []struct {
		name      string
		unchecked int
		want      []string
		wantNot   string
	}{
		{"not checked", 4242, []string{"was not asked to stop", "ps -p 4242"}, "asked the restart supervisor to stop"},
		{"asked and not confirmed", 0, []string{"asked the restart supervisor to stop"}, "was not asked to stop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			reportSupervisorStop(&out, tc.unchecked, false, true, "")
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("did not say %q:\n%s", want, out.String())
				}
			}
			if strings.Contains(out.String(), tc.wantNot) {
				t.Errorf("said %q:\n%s", tc.wantNot, out.String())
			}
		})
	}
}
