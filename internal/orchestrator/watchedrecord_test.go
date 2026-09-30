package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The record of what a supervisor watches is replaced whole (#1496): a reader that
// looks while it is being written sees the set before or the set after, never an
// empty file. `os.WriteFile` truncates and then writes, and a reader between the two
// read "watching nothing" — which is what took a CI run down once, and what
// WatchedMatches would read as "the set changed" and answer by replacing a
// supervisor that was fine.
//
// The sets are long, so the write takes long enough to be caught in the middle by a
// reader that does nothing else.
func TestAReaderNeverSeesTheWatchedRecordHalfWritten(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stateDir(t, "race")
	names := func(prefix string) []string {
		out := make([]string, 4000)
		for i := range out {
			out[i] = fmt.Sprintf("%s-service-%04d", prefix, i)
		}
		return out
	}
	a, b := names("a"), names("b")
	if err := RecordWatched("race", a); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	torn := make(chan string, 8)
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				got := Watched("race")
				// The record is whole: all of one set or all of the other.
				if len(got) != len(a) || (!strings.HasPrefix(got[0], "a-") && !strings.HasPrefix(got[0], "b-")) {
					select {
					case torn <- fmt.Sprintf("read %d names (first %q)", len(got), first(got)):
					default:
					}
					return
				}
			}
		}()
	}
	for i := 0; i < 400; i++ {
		set := a
		if i%2 == 1 {
			set = b
		}
		if err := RecordWatched("race", set); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	select {
	case msg := <-torn:
		t.Errorf("a reader saw the record half-written: %s", msg)
	default:
	}
}

// Nothing is left beside the record: a temporary file that stays would be a file
// per write in the project's state directory.
func TestRecordingWhatIsWatchedLeavesNoTemporaryFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stateDir(t, "tidy")
	for i := 0; i < 5; i++ {
		if err := RecordWatched("tidy", []string{"web", "db"}); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := supervisorStateDir("tidy")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "supervised.tmp-") {
			t.Errorf("a temporary file was left in %s: %s", dir, e.Name())
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, "supervised"))
	if err != nil || string(got) != "db\nweb\n" {
		t.Errorf("want the sorted set in the record, got %q, err %v", got, err)
	}
	if info, err := os.Stat(filepath.Join(dir, "supervised")); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("want the record readable as before (0644), got %v, err %v", info.Mode().Perm(), err)
	}
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// stateDir makes the project's state directory, which `up` has made by the time
// anything is recorded in it.
func stateDir(t *testing.T, project string) {
	t.Helper()
	dir, err := supervisorStateDir(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

// A record that cannot be put in place says so and leaves nothing beside it: the
// place of the record holding a directory makes the rename fail, the way a full disk
// or a permission would, and the temporary file it was written to is removed.
func TestARecordThatCannotBePutInPlaceLeavesNoTemporaryFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stateDir(t, "blocked")
	dir, err := supervisorStateDir("blocked")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "supervised", "in-the-way"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RecordWatched("blocked", []string{"web"}); err == nil {
		t.Fatal("RecordWatched should fail when the record's place holds a directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "supervised.tmp-") {
			t.Errorf("a failed write left %s in the state directory", e.Name())
		}
	}
}

// The temporary file is made beside the record, not in the default temporary
// directory: a rename across volumes fails (EXDEV) and leaves the file behind. With
// TMPDIR pointed at a directory that is not there, a file made in the default one
// cannot be, and the record could not be written at all.
func TestTheTemporaryRecordIsMadeBesideTheRecord(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "not-there"))
	stateDir(t, "beside")
	if err := RecordWatched("beside", []string{"web", "db"}); err != nil {
		t.Fatalf("the record should be written beside itself, whatever TMPDIR is: %v", err)
	}
	if got := Watched("beside"); len(got) != 2 {
		t.Errorf("the record should be in place, got %v", got)
	}
}
