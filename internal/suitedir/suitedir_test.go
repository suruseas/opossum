package suitedir

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// What these check, and what they do not.
//
// The property is that a directory outlives only a live maker. Both directions
// are here: one whose maker is gone is removed, one whose maker is running is
// not — the second is what makes this safe on a machine running two suites at
// once, and without it "removes leftovers" and "removes everything" look the
// same.
//
// **Not checked:**
//
//   - That a suite dying to a -timeout actually leaves a directory for this to
//     find. That is measured from outside, against the real suites, and written
//     down in the pull request; nothing in this package can make a Go test
//     binary panic on its own behalf.
//   - Pid reuse, and a process that has exited but not been reaped. Both read as
//     alive and keep a directory nobody is using. Deliberate: this removes less
//     than it could rather than removing something in use.
//   - Anything about $TMPDIR being shared with other users. Directories are
//     matched by name, and a name is not proof of ownership.
//   - cmd/opossum's own `opossum-dryrun` directory, which the product makes at
//     run time and removes with a defer. It leaks down the same path, is in the
//     same `opossum-*` family that cmd/noleftovers reports, and is out of reach
//     here twice over: it is not a suite's build directory, and the wiring check
//     only walks _test.go files. Its name has no pid and no separator either, so
//     bringing it in later means renaming it first — the same bind the leftovers
//     from before this change are in. That the check cannot see it is the same
//     reason it cannot see suitedir.go: it walks _test.go files only.
//   - Whether a removal succeeded. sweep drops the error, so a directory it
//     could only partly take apart is left half there, silently.
//   - A directory that is gone but still in use. A supervisor a test left
//     running outlives its maker, and the sweep takes its binary's directory
//     with it — the process keeps going, its path reads as deleted. Measured
//     while writing this; see the package comment for why it is left that way.

func TestADirectoryOutlivesOnlyALiveMaker(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if os.TempDir() != tmp {
		t.Fatalf("os.TempDir() does not follow TMPDIR here (%s), so this would sweep the real one", os.TempDir())
	}
	const prefix = "opossum-testtmp-fixture-"

	dead := filepath.Join(tmp, prefix+strconv.Itoa(deadPid(t))+"-gone")
	mine := filepath.Join(tmp, prefix+strconv.Itoa(os.Getpid())+"-mine")
	// The shape used before pids were in the name — and the shape that is
	// actually lying around on this machine, which is a random number with no
	// separator after it. Nothing can be concluded about who made it, so it
	// stays. Written with digits on purpose: every pid-less fixture here used to
	// be letters, which Atoi refuses anyway, so the guard that reads "is there a
	// pid-shaped field at all" was never once exercised — and deleting it left
	// the suite green while the real leftovers were removed.
	old := filepath.Join(tmp, prefix+"1065077236")
	// And a short one. MkdirTemp's random tail is usually ten digits, which the
	// pid ceiling refuses on its own — so the long name above does not exercise
	// the "is there a pid-shaped field at all" guard at all. A short tail does:
	// it is a plausible pid, and only the missing separator says it is not one.
	oldShort := filepath.Join(tmp, prefix+"12345")
	// Another family entirely.
	stranger := filepath.Join(tmp, "some-other-tool-1-x")
	for _, d := range []string{dead, mine, old, oldShort, stranger} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		// Not empty: a real leftover holds a built binary and a state directory,
		// and os.Remove would fail on it where it succeeds on an empty one. With
		// empty fixtures, a sweep that only unlinked the top passed.
		if err := os.WriteFile(filepath.Join(d, "opossum"), []byte("not really"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got, err := Make(prefix)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(got)

	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("a directory whose maker is gone should have been removed, stat said: %v", err)
	}
	for _, d := range []string{mine, old, oldShort, stranger} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s should have been left alone: %v", filepath.Base(d), err)
		}
	}

	// And what it hands back has to carry this process, or no later run can tell
	// whether it may remove it.
	if !strings.HasPrefix(filepath.Base(got), prefix+strconv.Itoa(os.Getpid())+"-") {
		t.Errorf("made %s, want a name carrying pid %d", filepath.Base(got), os.Getpid())
	}
}

// The sweep runs before the directory is made, so a run that finds a hundred
// leftovers still gets its own.
func TestItStillGetsADirectoryWhenThereWasAMess(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if os.TempDir() != tmp {
		t.Fatalf("os.TempDir() does not follow TMPDIR here (%s), so this would sweep the real one", os.TempDir())
	}
	const prefix = "opossum-testtmp-mess-"
	gone := strconv.Itoa(deadPid(t))
	for i := 0; i < 20; i++ {
		if err := os.Mkdir(filepath.Join(tmp, prefix+gone+"-"+strconv.Itoa(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Make(prefix)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(got)
	left, _ := filepath.Glob(filepath.Join(tmp, prefix+"*"))
	if len(left) != 1 {
		t.Errorf("left %d directories, want only the new one: %v", len(left), left)
	}
}

func TestWhatCountsAsAMakerName(t *testing.T) {
	const prefix = "p-"
	for _, tc := range []struct {
		base string
		pid  int
		ok   bool
	}{
		{"p-123-abc", 123, true},
		{"p-1-x", 1, true},
		// No pid to read: an older name. Not ours to guess about.
		{"p-abc", 0, false},
		{"p-nopidhere", 0, false},
		// The shape lying around on this machine right now: digits, and no
		// separator after them. Nothing here says which run made it.
		{"p-1065077236", 0, false},
		// Short enough to be a pid. Only the missing separator says otherwise,
		// which makes this the one row that tests that guard.
		{"p-12345", 0, false},
		// A pid must be a pid.
		{"p--1-x", 0, false},
		{"p-0-x", 0, false},
		{"p-12x-y", 0, false},
		// Past what any kernel hands out. kill(2) truncates to pid_t, so this
		// would arrive as some other process entirely.
		{"p-4294967296-x", 0, false},
		// Another family.
		{"q-123-abc", 0, false},
	} {
		t.Run(tc.base, func(t *testing.T) {
			pid, ok := makerOf(tc.base, prefix)
			if ok != tc.ok || pid != tc.pid {
				t.Errorf("makerOf(%q) = %d, %v, want %d, %v", tc.base, pid, ok, tc.pid, tc.ok)
			}
		})
	}
}

func TestALivePidReadsAsAlive(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Error("this process reads as gone")
	}
	if Alive(deadPid(t)) {
		t.Error("a process that has exited and been reaped reads as alive")
	}
	// pid 1 is somebody else's, and signalling it is refused rather than
	// answered — the branch that treats "not allowed to ask" as alive. Without
	// this the branch is never taken, and a version that only accepted a nil
	// error would go looking for other people's live directories.
	//
	// Running as root, or in a container where this process owns pid 1, the
	// error is nil instead and the case is still true but for the other reason.
	if !Alive(1) {
		t.Error("a pid this process may not signal reads as gone")
	}
}

// deadPid gives back the pid of a process that has run, exited, and been
// reaped. A made-up number will not do any more: the parser now refuses
// anything above what a kernel hands out, so a sentinel like 2147483646 reads
// as "not a pid" and is never swept — the test would pass for the wrong reason.
//
// The pid could be handed out again before the test finishes. That is the same
// risk the sweep itself carries, and errs the same way: a reused pid reads as
// alive, and the directory stays.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("could not make a dead process: %v", err)
	}
	return cmd.ProcessState.Pid()
}
