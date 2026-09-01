package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

// The default state directory — the one a real user gets — stopped being
// reached by the suite when the two TestMains that reach this function
// (internal/orchestrator and cmd/opossum) started pointing XDG_STATE_HOME at a
// temporary directory (#519). That was the right call: tests must not write
// into a developer's home. But it left the fallback unexecuted, and it had
// never been asserted even while it was executed — a mutation changing it to
// `$HOME/MUTATED-state` leaves the rest of the suite green.
//
// So this asks the question without answering it on disk: the returned path,
// with no directory created. $HOME moves to a temporary directory, which is
// what makes reading the assembled default safe — the value is checked, the
// filesystem is not touched (#522).
func TestTheDefaultStateDirIsUnderTheHomeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Empty rather than unset: the code reads it with os.Getenv, which cannot
	// tell the two apart, and a test that unset it would be relying on the
	// suite's TestMain not having set it in the first place.
	t.Setenv("XDG_STATE_HOME", "")

	got, err := supervisorStateDir("demo")
	if err != nil {
		t.Fatalf("supervisorStateDir: %v", err)
	}
	want := filepath.Join(home, ".local", "state", "opossum", "demo")
	if got != want {
		t.Errorf("with XDG_STATE_HOME empty the state dir should be the XDG default under "+
			"the home directory\n got: %s\nwant: %s", got, want)
	}
	// Nothing may have been created on the way to answering. This helper is
	// asked for a path, and a path is all it should cost — the reason the
	// suite stopped reaching this line was that executing it writes.
	if _, err := os.Stat(filepath.Join(home, ".local")); !os.IsNotExist(err) {
		t.Errorf("asking for the path created it: %v", err)
	}
}

// And XDG_STATE_HOME, when set, is what is used. The rest of the suite runs
// through this branch constantly and pins its shape only in passing — dropping
// the `opossum` element turns TestSupervisorPIDRejectsAReusedPid red, which is
// a test about pid files. Saying it here gives the branch something that fails
// for its own reason.
func TestAnExplicitStateHomeIsUsedAsGiven(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)

	got, err := supervisorStateDir("demo")
	if err != nil {
		t.Fatalf("supervisorStateDir: %v", err)
	}
	if want := filepath.Join(base, "opossum", "demo"); got != want {
		t.Errorf("state dir = %s, want %s", got, want)
	}
}

// The other half of the fallback: what happens when there is no home to fall
// back to. os.UserHomeDir reads $HOME on unix and errors when it is empty, and
// this helper passes that up rather than inventing a path. Nothing reached this
// line before — coverage read 0 for it while the branch above read 297 — so a
// mutation swallowing the error with `home, _ :=` survived the whole suite.
func TestNoHomeAndNoStateHomeIsAnError(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	got, err := supervisorStateDir("demo")
	if err == nil {
		t.Fatalf("with neither variable set there is no path to build, got %q", got)
	}
	if got != "" {
		t.Errorf("a failed lookup should not also return a path, got %q", got)
	}
}
