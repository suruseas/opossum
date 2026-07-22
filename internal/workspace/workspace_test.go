package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// makeWorkspace creates a workspace dir with a nested file and returns its path.
func makeWorkspace(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "a.txt"), "one")
	write(t, filepath.Join(root, "sub", "b.txt"), "two")
	return root
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSnapshotClonesContentAndLists(t *testing.T) {
	root := makeWorkspace(t)
	m := New(root)

	fast, err := m.Snapshot("v1")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// On macOS (APFS temp dir) the snapshot is a real clone; off darwin there's no
	// clonefile, so it falls back to a copy — both produce a correct snapshot.
	if runtime.GOOS == "darwin" && !fast {
		t.Error("expected a fast APFS clone on macOS (temp dir is APFS)")
	}
	// The snapshot has the workspace's content, nested files included.
	snapDir, _ := m.dir()
	if got := read(t, filepath.Join(snapDir, "v1", "sub", "b.txt")); got != "two" {
		t.Errorf("nested file not snapshotted, got %q", got)
	}
	// It lists.
	snaps, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snaps) != 1 || snaps[0].Name != "v1" {
		t.Errorf("List = %+v, want one snapshot named v1", snaps)
	}
	// The snapshot lives beside the workspace, not inside it (so it isn't cloned
	// into later snapshots or seen through a bind mount).
	if _, err := os.Stat(filepath.Join(root, ".opossum-snapshots")); !os.IsNotExist(err) {
		t.Error("snapshots must not live inside the workspace")
	}
}

func TestDiffReportsAddedChangedDeleted(t *testing.T) {
	root := makeWorkspace(t) // a.txt="one", sub/b.txt="two"
	m := New(root)
	if _, err := m.Snapshot("base"); err != nil {
		t.Fatal(err)
	}
	// The "run" changes a.txt, deletes sub/b.txt, and adds c.txt.
	write(t, filepath.Join(root, "a.txt"), "CHANGED")
	if err := os.Remove(filepath.Join(root, "sub", "b.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "c.txt"), "new")

	changes, err := m.Diff("base")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	got := map[string]ChangeKind{}
	for _, c := range changes {
		got[c.Path] = c.Kind
		if c.Kind != Deleted && c.Hash == "" {
			t.Errorf("%s (%s) should carry a content hash", c.Path, c.Kind)
		}
		if c.Kind == Deleted && c.Hash != "" {
			t.Errorf("deleted %s should have no hash, got %q", c.Path, c.Hash)
		}
	}
	want := map[string]ChangeKind{"a.txt": Changed, filepath.Join("sub", "b.txt"): Deleted, "c.txt": Added}
	if len(got) != len(want) {
		t.Fatalf("Diff = %v, want exactly %v", got, want)
	}
	for path, kind := range want {
		if got[path] != kind {
			t.Errorf("%s: got %q, want %q", path, got[path], kind)
		}
	}
}

func TestDiffDetectsModeOnlyChange(t *testing.T) {
	root := makeWorkspace(t)
	m := New(root)
	if _, err := m.Snapshot("base"); err != nil {
		t.Fatal(err)
	}
	// chmod +x with no content change — a real thing an agent does — must show up.
	if err := os.Chmod(filepath.Join(root, "a.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	changes, err := m.Diff("base")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(changes) != 1 || changes[0].Path != "a.txt" || changes[0].Kind != Changed {
		t.Errorf("a mode-only change should be reported as changed, got %v", changes)
	}
}

func TestDiffEmptyWhenUnchanged(t *testing.T) {
	root := makeWorkspace(t)
	m := New(root)
	if _, err := m.Snapshot("base"); err != nil {
		t.Fatal(err)
	}
	changes, err := m.Diff("base")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("an untouched workspace should diff empty, got %v", changes)
	}
}

func TestSnapshotIsIndependentOfLaterWorkspaceEdits(t *testing.T) {
	root := makeWorkspace(t)
	m := New(root)
	if _, err := m.Snapshot("v1"); err != nil {
		t.Fatal(err)
	}
	// Editing the workspace after the snapshot must not change the snapshot (CoW
	// gives each its own copy on write).
	write(t, filepath.Join(root, "a.txt"), "CHANGED")
	snapDir, _ := m.dir()
	if got := read(t, filepath.Join(snapDir, "v1", "a.txt")); got != "one" {
		t.Errorf("snapshot changed with the workspace, got %q — clone isn't copy-on-write", got)
	}
}

func TestRollbackRestoresAndAutosavesCurrent(t *testing.T) {
	root := makeWorkspace(t)
	m := New(root)
	if _, err := m.Snapshot("good"); err != nil {
		t.Fatal(err)
	}
	// The agent wrecks the workspace.
	write(t, filepath.Join(root, "a.txt"), "BROKEN")
	if err := os.Remove(filepath.Join(root, "sub", "b.txt")); err != nil {
		t.Fatal(err)
	}

	autosave, err := m.Rollback("good")
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	// The workspace is restored.
	if got := read(t, filepath.Join(root, "a.txt")); got != "one" {
		t.Errorf("rollback did not restore a.txt, got %q", got)
	}
	if got := read(t, filepath.Join(root, "sub", "b.txt")); got != "two" {
		t.Errorf("rollback did not restore the deleted nested file, got %q", got)
	}
	// The pre-rollback (broken) state was saved, so the rollback is reversible.
	if autosave == "" {
		t.Fatal("Rollback must return the autosave name")
	}
	snapDir, _ := m.dir()
	if got := read(t, filepath.Join(snapDir, autosave, "a.txt")); got != "BROKEN" {
		t.Errorf("autosave should hold the pre-rollback state, got %q", got)
	}
}

func TestSnapshotDuplicateNameErrors(t *testing.T) {
	m := New(makeWorkspace(t))
	if _, err := m.Snapshot("v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshot("v1"); err == nil {
		t.Error("a second snapshot with the same name must error, not overwrite")
	}
}

func TestRollbackMissingSnapshotErrors(t *testing.T) {
	m := New(makeWorkspace(t))
	if _, err := m.Rollback("nope"); err == nil {
		t.Error("rollback of a nonexistent snapshot must error")
	}
}

// Error-quality (#277): a "snapshot not found" — on rollback, rm, or diff — must
// point the user at how to list the real names, not just state the miss. The
// "already exists" collision likewise names the way out.
func TestSnapshotErrorsPointToDiscovery(t *testing.T) {
	m := New(makeWorkspace(t))
	_, rbErr := m.Rollback("nope")
	rmErr := m.Remove("nope")
	_, diffErr := m.Diff("nope")
	for name, err := range map[string]error{"Rollback": rbErr, "Remove": rmErr, "Diff": diffErr} {
		if err == nil {
			t.Fatalf("%s of a missing snapshot must error", name)
		}
		if !strings.Contains(err.Error(), "opossum ws ls") {
			t.Errorf("%s not-found error should point at `opossum ws ls`, got: %s", name, err)
		}
	}
	// A duplicate snapshot name names the way out (rm/rename).
	if _, err := m.Snapshot("dup"); err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	if _, err := m.Snapshot("dup"); err == nil || !strings.Contains(err.Error(), "opossum ws rm") {
		t.Errorf("already-exists error should point at `opossum ws rm`, got: %v", err)
	}
}

func TestSnapshotMissingWorkspaceErrors(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "does-not-exist"))
	if _, err := m.Snapshot("v1"); err == nil {
		t.Error("snapshot of a missing workspace must error")
	}
}

func TestInvalidNamesRejected(t *testing.T) {
	m := New(makeWorkspace(t))
	for _, bad := range []string{"", ".", "..", "a/b", "../escape", `a\b`} {
		if _, err := m.Snapshot(bad); err == nil {
			t.Errorf("snapshot name %q should be rejected", bad)
		}
	}
}

// A user may not name a snapshot in the autosave namespace: a bare `prune` deletes
// those, so allowing it would quietly make a user's snapshot prunable.
func TestSnapshotRejectsReservedAutosavePrefix(t *testing.T) {
	m := New(makeWorkspace(t))
	if _, err := m.Snapshot(autosavePrefix + "mine"); err == nil {
		t.Errorf("Snapshot must reject the reserved %q prefix", autosavePrefix)
	}
	// But a name that merely contains it later is fine.
	if _, err := m.Snapshot("my-before-rollback-notes"); err != nil {
		t.Errorf("a non-prefix use of the words should be allowed, got %v", err)
	}
}

// When the filesystem can't clone (non-APFS / cross-device), Snapshot must fall
// back to a full copy, report fastClone=false, and still produce correct content.
func TestFallbackToCopyWhenCloneUnsupported(t *testing.T) {
	root := makeWorkspace(t)
	// A symlink to prove the fallback copy preserves link entries, not just files.
	if err := os.Symlink("a.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	m := New(root)
	m.clone = func(src, dst string) error { return unix.ENOTSUP } // simulate non-APFS

	fast, err := m.Snapshot("v1")
	if err != nil {
		t.Fatalf("Snapshot (fallback): %v", err)
	}
	if fast {
		t.Error("fastClone must be false when the clone fell back to a copy")
	}
	snapDir, _ := m.dir()
	if got := read(t, filepath.Join(snapDir, "v1", "sub", "b.txt")); got != "two" {
		t.Errorf("fallback copy lost nested content, got %q", got)
	}
	if target, err := os.Readlink(filepath.Join(snapDir, "v1", "link")); err != nil || target != "a.txt" {
		t.Errorf("fallback copy did not preserve the symlink (target %q, err %v)", target, err)
	}
}

// A trailing slash on the path (shell completion adds it readily) must not break
// rollback: the temp/old sibling paths are built by string-append, so without
// cleaning, "work/" would make them land inside the workspace and the swap would
// fail. New cleans the root; this guards that.
func TestRollbackToleratesTrailingSlashPath(t *testing.T) {
	root := makeWorkspace(t)
	m := New(root + "/") // <- trailing slash
	if _, err := m.Snapshot("good"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "a.txt"), "BROKEN")
	if _, err := m.Rollback("good"); err != nil {
		t.Fatalf("rollback with a trailing-slash path must work, got %v", err)
	}
	if got := read(t, filepath.Join(root, "a.txt")); got != "one" {
		t.Errorf("rollback did not restore with a trailing-slash path, got %q", got)
	}
}

// A filesystem root has no parent to hold the sibling snapshot dir, so it must be
// refused rather than nesting the snapshot dir inside the workspace.
func TestSnapshotFilesystemRootRefused(t *testing.T) {
	if _, err := New("/").Snapshot("v1"); err == nil {
		t.Error("snapshotting a filesystem root must be refused")
	}
}

// names lists the snapshot names currently stored (order-independent helper).
func names(t *testing.T, m *Manager) []string {
	t.Helper()
	snaps, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range snaps {
		out = append(out, s.Name)
	}
	return out
}

func has(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// save creates a snapshot for tests, routing autosave-prefixed names through the
// internal snapshot (Snapshot rejects that reserved prefix for user input).
func save(t *testing.T, m *Manager, name string) {
	t.Helper()
	var err error
	if IsAutosave(name) {
		_, err = m.snapshot(name)
	} else {
		_, err = m.Snapshot(name)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestRemoveDeletesNamedAndValidatesFirst(t *testing.T) {
	m := New(makeWorkspace(t))
	for _, n := range []string{"a", "b", "c"} {
		if _, err := m.Snapshot(n); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Remove("b"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := names(t, m); has(got, "b") || !has(got, "a") || !has(got, "c") {
		t.Errorf("after removing b, expected a and c to remain, got %v", got)
	}
	// A missing name in the batch removes nothing (validate-all-first).
	if err := m.Remove("a", "missing"); err == nil {
		t.Error("Remove with a missing name should error")
	}
	if got := names(t, m); !has(got, "a") {
		t.Errorf("a must survive a failed batch remove, got %v", got)
	}
}

func TestRemoveRejectsInvalidName(t *testing.T) {
	m := New(makeWorkspace(t))
	if err := m.Remove("../escape"); err == nil {
		t.Error("Remove must reject a name with path separators")
	}
}

func TestPruneRemovesAutosavesKeepsNamed(t *testing.T) {
	m := New(makeWorkspace(t))
	for _, n := range []string{"mine", autosavePrefix + "1", autosavePrefix + "2"} {
		save(t, m, n)
	}
	removed, err := m.Prune(0, false)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(removed) != 2 {
		t.Errorf("prune should remove both auto-saves, removed %v", removed)
	}
	if got := names(t, m); !has(got, "mine") || len(got) != 1 {
		t.Errorf("prune must keep the named snapshot and drop auto-saves, got %v", got)
	}
}

func TestPruneKeepsNewestN(t *testing.T) {
	m := New(makeWorkspace(t))
	snapDir, _ := m.dir()
	base := time.Now()
	// Three auto-saves with distinct, increasing modtimes so "newest" is unambiguous.
	for i, spec := range []struct {
		name string
		age  time.Duration
	}{{autosavePrefix + "old", 2 * time.Hour}, {autosavePrefix + "mid", time.Hour}, {autosavePrefix + "new", 0}} {
		save(t, m, spec.name)
		mt := base.Add(-spec.age)
		if err := os.Chtimes(filepath.Join(snapDir, spec.name), mt, mt); err != nil {
			t.Fatal(err)
		}
		_ = i
	}
	removed, err := m.Prune(1, false) // keep the 1 newest auto-save
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	// The two oldest are removed; the newest stays.
	if len(removed) != 2 || !has(removed, autosavePrefix+"old") || !has(removed, autosavePrefix+"mid") {
		t.Errorf("prune --keep 1 should drop the two oldest, removed %v", removed)
	}
	if got := names(t, m); len(got) != 1 || got[0] != autosavePrefix+"new" {
		t.Errorf("only the newest auto-save should remain, got %v", got)
	}
}

func TestPruneAllIncludesNamed(t *testing.T) {
	m := New(makeWorkspace(t))
	for _, n := range []string{"mine", autosavePrefix + "1"} {
		save(t, m, n)
	}
	removed, err := m.Prune(0, true)
	if err != nil {
		t.Fatalf("Prune all: %v", err)
	}
	if len(removed) != 2 {
		t.Errorf("prune --all should remove every snapshot, removed %v", removed)
	}
	if got := names(t, m); len(got) != 0 {
		t.Errorf("prune --all should leave nothing, got %v", got)
	}
}

func TestPruneNothingToDo(t *testing.T) {
	m := New(makeWorkspace(t))
	if _, err := m.Snapshot("mine"); err != nil {
		t.Fatal(err)
	}
	removed, err := m.Prune(0, false) // only a named snapshot exists; default targets auto-saves
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("prune should remove nothing when there are no auto-saves, removed %v", removed)
	}
	if got := names(t, m); !has(got, "mine") {
		t.Errorf("the named snapshot must survive, got %v", got)
	}
}

func TestListEmptyWhenNoSnapshots(t *testing.T) {
	m := New(makeWorkspace(t))
	snaps, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snaps) != 0 {
		t.Errorf("expected no snapshots, got %+v", snaps)
	}
}
