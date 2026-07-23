// Package workspace snapshots and restores a workspace directory using APFS file
// clones. A clone is copy-on-write, so a snapshot is near-instant and costs almost
// no extra disk until the workspace and the snapshot diverge — which makes
// "try something risky, then roll back" cheap for an agent's scratch directory.
//
// The box (VM) is already disposable; the only state worth saving is the
// workspace, so that's all this touches. On a filesystem without clone support
// (non-APFS, or a cross-device path) it falls back to a plain recursive copy and
// says so, rather than pretending the operation was free.
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// snapshotDirName is the directory, kept beside the workspace (not inside it, so
// it's never part of what gets cloned and never lands in a bind mount), where
// snapshots are stored.
const snapshotDirName = ".opossum-snapshots"

// autosavePrefix marks the snapshots Rollback takes automatically (of the state
// it's about to overwrite). Prune targets these by default, since they accumulate
// without anyone naming them.
const autosavePrefix = "before-rollback-"

// Manager snapshots and restores one workspace directory.
type Manager struct {
	Root string // the workspace directory (e.g. ./work)
	// clone clones srcDir to dstDir, which must not already exist. Overridable in
	// tests to exercise the non-clone fallback; defaults to cloneAPFS.
	clone func(srcDir, dstDir string) error
}

// New returns a Manager for the workspace at root. root is cleaned so a trailing
// slash (which shell completion loves to add) can't turn the sibling temp/old
// paths rollback builds by string-append into paths *inside* the workspace.
func New(root string) *Manager {
	return &Manager{Root: filepath.Clean(root), clone: cloneAPFS}
}

// Snapshot struct describes a stored snapshot.
type Snapshot struct {
	Name    string
	ModTime time.Time
}

// dir is where snapshots live: a sibling of the workspace, so cloning the
// workspace never recurses into it. Resolved to an absolute path so a workspace
// given as "." still gets a real parent.
func (m *Manager) dir() (string, error) {
	abs, err := filepath.Abs(m.Root)
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		// A filesystem root has no parent to hold a sibling snapshot dir — the
		// snapshot dir would land inside the workspace and get cloned into itself.
		return "", fmt.Errorf("workspace %q is a filesystem root; snapshot a subdirectory instead", m.Root)
	}
	return filepath.Join(parent, snapshotDirName), nil
}

// Snapshot saves the current workspace under name. fastClone is false when the
// filesystem couldn't clone and it fell back to a full (slower, space-using) copy.
//
// A user-facing snapshot may not use the autosave prefix: that namespace belongs
// to Rollback, and a bare `prune` deletes it — so letting a user name a snapshot
// there would quietly make it prunable, breaking "prune keeps the ones you named".
func (m *Manager) Snapshot(name string) (fastClone bool, err error) {
	if IsAutosave(name) {
		return false, fmt.Errorf("the %q prefix is reserved for automatic rollback snapshots; choose another name", autosavePrefix)
	}
	return m.snapshot(name)
}

// snapshot is the unguarded save used internally (Rollback's autosave legitimately
// uses the reserved prefix). External callers go through Snapshot.
func (m *Manager) snapshot(name string) (fastClone bool, err error) {
	if err := validateName(name); err != nil {
		return false, err
	}
	if info, err := os.Stat(m.Root); err != nil {
		return false, fmt.Errorf("workspace %q: %w", m.Root, err)
	} else if !info.IsDir() {
		return false, fmt.Errorf("workspace %q is not a directory", m.Root)
	}
	snapDir, err := m.dir()
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		return false, fmt.Errorf("couldn't create the snapshot directory %s: %w — check the parent directory's permissions and free space", snapDir, err)
	}
	dst := filepath.Join(snapDir, name)
	if _, err := os.Stat(dst); err == nil {
		return false, fmt.Errorf("snapshot %q already exists — choose another name, or remove it with `opossum ws rm %s`", name, name)
	}
	return m.cloneOrCopy(m.Root, dst)
}

// List returns the stored snapshots, oldest first. No snapshots (or no snapshot
// directory yet) is not an error — it returns an empty slice.
func (m *Manager) List() ([]Snapshot, error) {
	snapDir, err := m.dir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(snapDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("couldn't read the snapshot directory %s: %w — check its permissions", snapDir, err)
	}
	var snaps []Snapshot
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		snaps = append(snaps, Snapshot{Name: e.Name(), ModTime: info.ModTime()})
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].ModTime.Before(snaps[j].ModTime) })
	return snaps, nil
}

// Rollback restores the workspace to snapshot name. It first snapshots the current
// workspace (returning that autosave's name), so the rollback itself can be undone.
// The restore stages into a temp sibling and swaps, so a failure mid-restore leaves
// the original workspace intact.
func (m *Manager) Rollback(name string) (autosave string, err error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	snapDir, err := m.dir()
	if err != nil {
		return "", err
	}
	src := filepath.Join(snapDir, name)
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("snapshot %q not found — run `opossum ws ls` to see available snapshots", name)
	}

	// Save the current workspace first — rolling back must be reversible.
	autosave = autosavePrefix + timestamp()
	if _, err := m.snapshot(autosave); err != nil {
		return "", fmt.Errorf("saving the current workspace before rollback: %w", err)
	}

	// Stage the restore into a temp sibling, then swap it in, so a mid-restore
	// failure never leaves the workspace half-overwritten.
	tmp := m.Root + ".opossum-rollback-tmp"
	old := m.Root + ".opossum-rollback-old"
	os.RemoveAll(tmp)
	if _, err := m.cloneOrCopy(src, tmp); err != nil {
		os.RemoveAll(tmp)
		return "", fmt.Errorf("restoring snapshot %q: %w", name, err)
	}
	os.RemoveAll(old)
	if err := os.Rename(m.Root, old); err != nil {
		os.RemoveAll(tmp)
		return "", fmt.Errorf("couldn't move the current workspace aside to roll back: %w — check permissions on %s (nothing was changed)", err, m.Root)
	}
	if err := os.Rename(tmp, m.Root); err != nil {
		// Put the original back. If even that fails, the workspace's contents are
		// still safe in two places (the moved-aside `old` and the autosave snapshot)
		// — say where, so recovery is a copy away rather than a panic.
		if rerr := os.Rename(old, m.Root); rerr != nil {
			return "", fmt.Errorf("rollback failed mid-swap and the workspace couldn't be restored automatically; "+
				"your files are safe in snapshot %q and in %q: %w", autosave, old, err)
		}
		return "", fmt.Errorf("restoring snapshot %q (your previous state was saved as %q): %w", name, autosave, err)
	}
	os.RemoveAll(old)
	return autosave, nil
}

// IsAutosave reports whether a snapshot was taken automatically by Rollback (as
// opposed to one the user named).
func IsAutosave(name string) bool {
	return strings.HasPrefix(name, autosavePrefix)
}

// Remove deletes the named snapshot(s). It validates every name and confirms each
// exists before deleting anything, so a bad name in the list removes nothing.
func (m *Manager) Remove(names ...string) error {
	if len(names) == 0 {
		return errors.New("no snapshot name given")
	}
	snapDir, err := m.dir()
	if err != nil {
		return err
	}
	for _, n := range names {
		if err := validateName(n); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(snapDir, n)); err != nil {
			return fmt.Errorf("snapshot %q not found — run `opossum ws ls` to see available snapshots", n)
		}
	}
	for _, n := range names {
		if err := os.RemoveAll(filepath.Join(snapDir, n)); err != nil {
			return fmt.Errorf("removing snapshot %q: %w", n, err)
		}
	}
	return nil
}

// ChangeKind is how a file differs between a snapshot and the workspace now.
type ChangeKind string

const (
	Added   ChangeKind = "added"
	Changed ChangeKind = "changed"
	Deleted ChangeKind = "deleted"
)

// FileChange is one file's difference between a snapshot and the workspace now.
type FileChange struct {
	Path string     // path relative to the workspace root
	Kind ChangeKind // added / changed / deleted
	Hash string     // SHA-256 (hex) of the current content; "" for a deleted file
}

// Diff compares the current workspace against a snapshot, returning the per-file
// changes (added / changed / deleted), sorted by path. It's how an audit reports
// exactly what a run touched. Regular files are compared by content hash and
// symlinks by target; directories aren't reported on their own.
func (m *Manager) Diff(snapshotName string) ([]FileChange, error) {
	if err := validateName(snapshotName); err != nil {
		return nil, err
	}
	snapDir, err := m.dir()
	if err != nil {
		return nil, err
	}
	snapRoot := filepath.Join(snapDir, snapshotName)
	if _, err := os.Stat(snapRoot); err != nil {
		return nil, fmt.Errorf("snapshot %q not found — run `opossum ws ls` to see available snapshots", snapshotName)
	}
	before, err := hashTree(snapRoot)
	if err != nil {
		return nil, err
	}
	after, err := hashTree(m.Root)
	if err != nil {
		return nil, err
	}
	var changes []FileChange
	for rel, h := range after {
		if old, ok := before[rel]; !ok {
			changes = append(changes, FileChange{rel, Added, h})
		} else if old != h {
			changes = append(changes, FileChange{rel, Changed, h})
		}
	}
	for rel := range before {
		if _, ok := after[rel]; !ok {
			changes = append(changes, FileChange{rel, Deleted, ""})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

// hashTree walks root, returning rel-path → content hash for every regular file
// and symlink (symlinks hash their target, tagged so they never collide with a
// file whose bytes equal the target string).
func hashTree(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			out[rel] = "symlink:" + target
		case info.Mode().IsRegular():
			h, err := hashFile(path)
			if err != nil {
				return err
			}
			// Fold the permission bits into the fingerprint so a mode-only change
			// (e.g. `chmod +x` on a script — a real thing an agent does) shows up.
			out[rel] = fmt.Sprintf("%04o|%s", info.Mode().Perm(), h)
		}
		return nil // other types (devices/sockets/fifos): skip
	})
	return out, err
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// Prune removes snapshots to reclaim space. By default it targets only the
// auto-save snapshots (the ones Rollback makes and no one names); all=true targets
// every snapshot. Of the target set it keeps the `keep` newest and removes the
// rest, returning the removed names (oldest first).
func (m *Manager) Prune(keep int, all bool) (removed []string, err error) {
	if keep < 0 {
		keep = 0
	}
	snaps, err := m.List() // oldest first
	if err != nil {
		return nil, err
	}
	var target []Snapshot
	for _, s := range snaps {
		if all || IsAutosave(s.Name) {
			target = append(target, s)
		}
	}
	drop := len(target) - keep // remove the oldest, keep the newest `keep`
	if drop <= 0 {
		return nil, nil
	}
	snapDir, err := m.dir()
	if err != nil {
		return nil, err
	}
	for _, s := range target[:drop] {
		if err := os.RemoveAll(filepath.Join(snapDir, s.Name)); err != nil {
			return removed, fmt.Errorf("removing snapshot %q: %w", s.Name, err)
		}
		removed = append(removed, s.Name)
	}
	return removed, nil
}

// cloneOrCopy clones src to dst, falling back to a plain recursive copy when the
// filesystem doesn't support cloning (non-APFS) or src and dst are on different
// devices. fastClone reports which path was taken.
func (m *Manager) cloneOrCopy(src, dst string) (fastClone bool, err error) {
	err = m.clone(src, dst)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EXDEV) {
		if cerr := copyTree(src, dst); cerr != nil {
			return false, cerr
		}
		return false, nil
	}
	return false, err
}

// cloneAPFS is defined per-platform: clonefile(2) on darwin, an unsupported-error
// stub elsewhere (so cloneOrCopy falls back to a plain copy). See clone_*.go.

// copyTree recursively copies src to dst (which must not exist), preserving file
// modes and symlinks. It's the fallback when cloning isn't available.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.Mode().IsRegular():
			return copyFile(path, target, info.Mode().Perm())
		default:
			return fmt.Errorf("cannot copy %q: unsupported file type %v", path, info.Mode().Type())
		}
	})
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// validateName keeps a snapshot name to a single, safe path component.
func validateName(name string) error {
	if name == "" {
		return errors.New("a snapshot name is required")
	}
	if name != filepath.Base(name) || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid snapshot name %q: use a simple name with no path separators", name)
	}
	return nil
}

// timestamp is a filename-safe, nanosecond-precision stamp used for default and
// autosave snapshot names. Nanosecond precision keeps rapid successive snapshots
// from colliding on the same name — two rollbacks in a row would otherwise clash
// (and Rollback's autosave must never fail on a name collision).
func timestamp() string {
	return time.Now().Format("20060102-150405.000000000")
}

// DefaultName is the snapshot name used when the user doesn't supply one.
func DefaultName() string {
	return "snapshot-" + timestamp()
}
