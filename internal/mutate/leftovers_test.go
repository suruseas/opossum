package mutate

// The cover profile is removed by a defer, and an interrupt that leaves by
// os.Exit runs no defers — so the runs that need their file reclaimed are
// exactly the ones that never got to their own cleanup. Recovery therefore
// lives at the start of the next run, in reapStrays, and these tests pin the
// two halves of that arrangement: the name carries the pid a later run needs,
// and the reaping touches the dead's files, nobody else's.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// deadPid hands back a pid that belonged to a real process and no longer
// does — made-up numbers can collide with something alive.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestTheProfileNameCarriesItsMakersPid(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	r := &Runner{}
	path, cleanup := r.profilePath()
	if path == "" {
		t.Fatal("no profile was made, so nothing was measured")
	}
	base := filepath.Base(path)
	want := fmt.Sprintf("opossum-mutate-%d-", os.Getpid())
	if !strings.HasPrefix(base, want) || !strings.HasSuffix(base, ".cover") {
		t.Errorf("the profile is named %q; a later run reads the maker's pid off %q, and a name "+
			"without it is one nobody may ever reclaim", base, want+"*.cover")
	}
	cleanup()
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("the cleanup left %s behind", path)
	}
}

// What the reaping touches, exhaustively: the dead's file goes, and the three
// shapes that are not ours to remove stay — a run still alive, the old
// pidless name (an older build's, or a hand-interrupted run from before the
// pid was carried), and another family entirely.
func TestOnlyTheDeadsProfilesAreReaped(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("mode: set\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	dead := write(fmt.Sprintf("opossum-mutate-%d-123.cover", deadPid(t)))
	alive := write(fmt.Sprintf("opossum-mutate-%d-123.cover", os.Getpid()))
	pidless := write("opossum-mutate-3762131923.cover")
	other := write("opossum-other-1-2.cover")
	// These two pin the glob's two anchors one at a time — review showed that
	// with both anchors knocked off, everything above still passed, because
	// `other` is refused by the family name and the pid rule at once and
	// cannot say which one held.
	foreignFamily := write(fmt.Sprintf("%d-foreign.cover", deadPid(t)))
	notACover := write(fmt.Sprintf("opossum-mutate-%d-1.tmp", deadPid(t)))

	reapStrays(dir)

	if _, err := os.Lstat(dead); err == nil {
		t.Errorf("the dead run's profile is still there — the whole point of carrying the pid "+
			"is that this file gets collected: %s", dead)
	}
	for what, p := range map[string]string{
		"a live run's profile":                    alive,
		"an old pidless name":                     pidless,
		"another family's file":                   other,
		"a foreign file that starts with digits":  foreignFamily,
		"our family's file that is not a profile": notACover,
	} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s was removed — that file was not ours to touch: %s", what, p)
		}
	}
}

// The reaping is wired to the run: making a profile collects the strays first,
// so recovery needs no step anybody has to remember.
func TestMakingAProfileCollectsTheStraysFirst(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	stray := filepath.Join(tmp, fmt.Sprintf("opossum-mutate-%d-9.cover", deadPid(t)))
	if err := os.WriteFile(stray, []byte("mode: set\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &Runner{}
	path, cleanup := r.profilePath()
	defer cleanup()
	if path == "" {
		t.Fatal("no profile was made, so nothing was measured")
	}
	if _, err := os.Lstat(stray); err == nil {
		t.Error("the stray was still there after a new run began — the beginning is the only " +
			"place a dead run's file can be collected, and it did not happen")
	}
}
