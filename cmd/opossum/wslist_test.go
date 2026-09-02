package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/workspace"
)

// The snapshot listing prints names read off the disk, and a directory is a
// name anyone can choose: `opossum ws snapshot` used to accept a name with a
// newline in it, and List() enumerates whatever is there, CLI or not. A line
// break in a name handed the rest of it the start of a line — the position
// that tells opossum's words from quoted ones (#573). The listing flattens
// what it prints; the name check refuses control characters so opossum itself
// never mints such a directory.
func TestASnapshotNameCannotWriteItsOwnRow(t *testing.T) {
	forged := "FORGED opossum deleted your database"
	parent := t.TempDir()
	work := filepath.Join(parent, "ws")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	// Not through the CLI: the hostile name arrives as a directory on disk,
	// which is the way in that survives any input validation. Snapshots live
	// BESIDE the workspace, not inside it.
	snaps := filepath.Join(parent, workspace.SnapshotDirName, "web\n"+forged)
	if err := os.MkdirAll(snaps, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "ws", "ls", "--path", work)
	if err != nil {
		t.Fatalf("ws ls: %v", err)
	}
	if !strings.Contains(out, "web "+forged) {
		t.Fatalf("the listing should show the flattened name, got:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "FORGED") {
			t.Errorf("a snapshot name wrote a row of its own:\n%s", out)
		}
	}

	// `ws prune` walks the same directory and used to print the removed names
	// raw — the same way in, one command over (found by this PR's independent
	// review, measured before it was fixed). The hostile name above is not a
	// prune target (no `before-rollback-` prefix), so plant one that is.
	pruneTarget := filepath.Join(parent, workspace.SnapshotDirName, "before-rollback-x\n"+forged)
	if err := os.MkdirAll(pruneTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, "ws", "prune", "--path", work)
	if err != nil {
		t.Fatalf("ws prune: %v", err)
	}
	if !strings.Contains(out, "before-rollback-x "+forged) {
		t.Fatalf("prune should report the flattened name, got:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "FORGED") {
			t.Errorf("a pruned snapshot name wrote a line of its own:\n%s", out)
		}
	}
}

// And the front door refuses what the listing would have to flatten.
func TestASnapshotNameRefusesControlCharacters(t *testing.T) {
	work := t.TempDir()
	for _, name := range []string{"a\nb", "a\rb", "a\tb", "a\x1bb", "a\x7fb"} {
		if _, err := run(t, "ws", "snapshot", "--path", work, name); err == nil {
			t.Errorf("snapshot accepted the name %q", name)
		}
	}
	// The plain name stays accepted — the refusal is about control characters,
	// not about being strict for its own sake.
	if _, err := run(t, "ws", "snapshot", "--path", work, "release-1"); err != nil {
		t.Errorf("a plain name should be accepted, got: %v", err)
	}
}
