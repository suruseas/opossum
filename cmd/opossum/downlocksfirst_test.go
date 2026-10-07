package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// `down` takes the project's lock before it asks the restart supervisor to stop, and keeps it until it is done: an `up` that has not let go of the lock
// yet is still watching its new supervisor claim, and a `down` that killed that supervisor and was then refused for the lock left a project running with no
// supervisor (#1822). A lock another command holds, here held through a second open file (a flock of a file a process has open twice conflicts with itself),
// refuses `down` before anything is stopped, as `Down` refuses a project another command is changing. Every stop that is asked is asked with the lock held:
// the stop stands in for the supervisor's own claim, and tries the lock itself. A project with no state directory has never been started: there is nothing to
// protect, and `down` leaves nothing behind for it.
func TestDownTakesTheProjectLockBeforeItStopsTheSupervisor(t *testing.T) {
	const plain = "services:\n  web:\n    image: alpine:3.20\n"
	for _, tc := range []struct {
		name      string
		args      []string // before `down`
		body      string
		existing  []string // the projects with a state directory (a `up` was here; "<dir>" for the directory's)
		lockedAs  string   // the project another command holds
		wantBusy  bool
		wantAsked []string
		byLabel   bool // the runtime holds a container of the project `held`, so a file that cannot be read still has something to take down (by its label)
	}{
		{name: "the project's lock is held elsewhere", args: []string{"-p", "held"}, body: plain, existing: []string{"held"}, lockedAs: "held", wantBusy: true},
		{name: "the file cannot be read and the project's lock is held elsewhere", args: []string{"-p", "held"}, body: "services:\n  web: [\n", existing: []string{"held"}, lockedAs: "held", wantBusy: true, byLabel: true},
		{name: "the lock of another project is held", args: []string{"-p", "held"}, body: plain, existing: []string{"held", "other"}, lockedAs: "other", wantAsked: []string{"held", "held"}},
		{name: "no lock is held", args: []string{"-p", "held"}, body: plain, existing: []string{"held"}, wantAsked: []string{"held", "held"}},
		{name: "the file cannot be read and no lock is held", args: []string{"-p", "held"}, body: "services:\n  web: [\n", existing: []string{"held"}, wantAsked: []string{"held", "held"}, byLabel: true},
		{name: "the directory's name is held, and the file names another project", body: "name: filenamed\n" + plain, existing: []string{"<dir>"}, lockedAs: "<dir>", wantBusy: true},
		{name: "the name the file gives is held, the directory's is not", body: "name: filenamed\n" + plain, existing: []string{"<dir>", "filenamed"}, lockedAs: "filenamed", wantBusy: true, wantAsked: []string{"<dir>"}},
		{name: "the project has no state directory", args: []string{"-p", "held"}, body: plain, wantAsked: []string{"held", "held"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeShim(t)
			state := t.TempDir()
			t.Setenv("XDG_STATE_HOME", state)
			file := writeCompose(t, tc.body)
			dir := compose.SanitizeName(filepath.Base(filepath.Dir(file)))
			named := func(n string) string { return strings.ReplaceAll(n, "<dir>", dir) }
			for _, n := range tc.existing {
				if err := os.MkdirAll(filepath.Join(state, "opossum", named(n)), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if tc.lockedAs != "" {
				held, err := orchestrator.HoldProjectLock(named(tc.lockedAs))
				if err != nil || held == nil {
					t.Fatalf("holding the lock of %s: %v", tc.lockedAs, err)
				}
				t.Cleanup(held.Release)
			}
			if tc.byLabel {
				t.Setenv("CONTAINER_LS", `[{"status":{"state":"running"},"configuration":{"id":"web.held.opossum","labels":{"opossum.project":"held"}}}]`)
			}
			var asked, unlocked []string
			saved := stopSupervisorFn
			stopSupervisorFn = func(project string) (bool, bool) {
				asked = append(asked, project)
				// Asked with the project's lock held: another command cannot take it now, so this cannot either.
				if probe, err := orchestrator.HoldProjectLock(project); err == nil && probe != nil {
					probe.Release()
					unlocked = append(unlocked, project)
				}
				return false, false
			}
			t.Cleanup(func() { stopSupervisorFn = saved })

			args := append([]string{"-f", file}, tc.args...)
			out, err := run(t, append(args, "down")...)
			if busy := err != nil && strings.Contains(err.Error()+out, "OPSM-208"); busy != tc.wantBusy {
				t.Errorf("down refused for the project's lock = %v (err %v), want %v\n%s", busy, err, tc.wantBusy, out)
			}
			if !tc.wantBusy && err != nil {
				t.Errorf("down: %v\n%s", err, out)
			}
			want := make([]string, len(tc.wantAsked))
			for i, n := range tc.wantAsked {
				want[i] = named(n)
			}
			if strings.Join(asked, ",") != strings.Join(want, ",") {
				t.Errorf("the supervisors asked to stop = %v, want %v", asked, want)
			}
			// A project that had a state directory is asked with its lock held, whichever of the stops it is; one that had none was never started, and the
			// first stop has nothing to protect (`Down` takes the lock, and makes the directory, for the second).
			if len(tc.existing) > 0 && len(unlocked) > 0 {
				t.Errorf("stops asked without the project's lock held: %v", unlocked)
			}
			// A `down` that was let through lets go of the lock when it is done: the next one is let through as well.
			if !tc.wantBusy {
				if out, err := run(t, append(args, "down")...); err != nil {
					t.Errorf("a second down: %v\n%s", err, out)
				}
			}
		})
	}
}

// A `down` in a directory with no compose file, given no project name, has nothing to take down, and leaves no state directory for the name the directory
// gives: the lock it takes before stopping a supervisor is for a project that was started (#1822).
func TestDownInADirectoryWithNoComposeFileLeavesNoStateDirectory(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	for _, n := range []string{"COMPOSE_PROJECT_NAME", "COMPOSE_FILE", "COMPOSE_PATH_SEPARATOR", "COMPOSE_PROFILES"} {
		t.Setenv(n, "")
		os.Unsetenv(n)
	}
	dir := filepath.Join(t.TempDir(), "nofile")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	out, err := run(t, "down")
	if err == nil {
		t.Fatalf("down with no compose file went through:\n%s", out)
	}
	if entries, _ := os.ReadDir(filepath.Join(state, "opossum")); len(entries) > 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("down left state directories for a project that was never started: %v", names)
	}
}
