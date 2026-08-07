// Package repohygiene decides whether a tracked file has any business being in
// the repository. It exists because a build artifact once reached main: a
// `go build ./internal/orchestrator/testdata/fakeshim` with no `-o` dropped a
// 2.7MB Mach-O binary in the working directory and `git add -A` swept it in.
// Nothing in CI noticed — a binary is just bytes, and the tests still passed.
//
// The rule is deliberately blunt (no binary blobs, nothing large) rather than
// clever (detect executables). A stripped binary, a tarball, a stray core dump
// and a checked-in database all fail for the same reason, and the way past the
// gate is to add the path to the allow-list on purpose, in a diff someone reads.
package repohygiene

import (
	"bytes"
	"fmt"
	"path"
	"strings"
)

// MaxTrackedBytes caps a tracked file's size. The largest file in the repo today
// is a ~233KB banner image, so 512KB leaves room to add artwork without anyone
// having to touch this constant, while still catching artifacts (the binary that
// prompted this was 2.7MB). Raise it only alongside a file that deserves it.
const MaxTrackedBytes = 512 << 10

// SniffBytes is how much of a file is examined for the NUL byte that marks it as
// binary. It matches git's own heuristic, so "opossum thinks this is binary" and
// "git thinks this is binary" don't disagree.
const SniffBytes = 8000

// binaryDirs are the only places a binary file may live, and imageExts the only
// forms it may take. Scoping by directory as well as extension means a .png that
// shows up in, say, internal/ is still a finding — art belongs in docs/assets.
var binaryDirs = []string{"docs/assets"}

var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".ico": true, ".webp": true,
}

// allowedBinary reports whether path is a location where binary content is
// expected. Paths are slash-separated and repo-relative (as `git ls-files` emits
// them), so this does no filesystem access and is safe to unit-test directly.
func allowedBinary(p string) bool {
	if !imageExts[strings.ToLower(path.Ext(p))] {
		return false
	}
	dir := path.Dir(p)
	for _, d := range binaryDirs {
		if dir == d || strings.HasPrefix(dir, d+"/") {
			return true
		}
	}
	return false
}

// scratchNames are the shapes a throwaway file takes: a probe written to answer
// one question, a temporary copy, a scratch script. They reach the index the same
// way a build artifact does — `git add -A` sweeping up whatever happened to be in
// the tree — and a test-shaped one is worse than a binary, because it compiles,
// asserts nothing, and passes CI forever. One arrived this way in a review, whose
// own first line read "TEMPORARY reviewer probe".
var scratchNames = []string{"zz_", "tmp_", "temp_", "scratch", "probe_", "_probe", "dbg_", "_dbg"}

// looksLikeScratch reports whether this path's base name announces itself as
// throwaway.
func looksLikeScratch(p string) bool {
	base := strings.ToLower(path.Base(p))
	for _, frag := range scratchNames {
		if strings.Contains(base, frag) {
			return true
		}
	}
	return false
}

// maintainerDirs hold tooling that runs the project rather than being part of it
// — the release sync, and whatever joins it. What reaches the public repository
// should be the product and what someone using it needs; a script that pushes
// releases is neither, and shipping it puts internal process into the
// distribution. They stay on the maintainer's disk, ignored rather than tracked,
// so this catches the one that gets `git add -f`'d back in by habit.
var maintainerDirs = []string{"scripts"}

// underMaintainerDir reports whether p lives in one of them.
func underMaintainerDir(p string) bool {
	for _, d := range maintainerDirs {
		if p == d || strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

// Offense returns a human-readable reason the file should not be tracked, or ""
// if it's fine. head is the first SniffBytes of the file (or all of it, if
// shorter); size is the full size, which is why a large file is caught even
// though only its head is read.
//
// The message names the likeliest cause, because the point of a gate like this
// is to be understood at 2am by whoever tripped it, not merely to be correct.
func Offense(p string, size int64, head []byte) string {
	if bytes.IndexByte(head, 0) >= 0 && !allowedBinary(p) {
		return fmt.Sprintf("%s looks like a binary file (a NUL byte in the first %d bytes).\n"+
			"  The usual cause is `go build ./some/pkg` without `-o`: it writes the binary into the "+
			"current directory, and `git add -A` picks it up. Build to a temp path instead "+
			"(`go build -o \"$(mktemp -d)/x\" ./some/pkg`), then `git rm --cached %s`.\n"+
			"  If this file genuinely belongs in the repo, put it under %s and give it an image "+
			"extension, or widen the allow-list in internal/repohygiene with a reason.",
			p, SniffBytes, p, strings.Join(binaryDirs, ", "))
	}
	if looksLikeScratch(p) {
		return fmt.Sprintf("%s is named like a throwaway file, and throwaway files are not "+
			"committed.\n"+
			"  These reach the index the way build artifacts do — `git add -A` sweeping up "+
			"whatever was in the tree at the time. A test-shaped one is the worst case: it "+
			"compiles, asserts nothing, and passes CI indefinitely.\n"+
			"  Remove it (`git rm --cached %s`). If the name is a false alarm, rename the file "+
			"to say what it is, or widen the allow-list in internal/repohygiene with a reason.", p, p)
	}
	if underMaintainerDir(p) {
		return fmt.Sprintf("%s is maintainer-only tooling, and maintainer-only tooling is not tracked.\n"+
			"  %s holds what runs the project — the release sync and its like — rather than what the "+
			"project is. Tracking it publishes internal process alongside the product.\n"+
			"  Remove it from the index (`git rm --cached %s`); it stays on disk, ignored. If this is "+
			"something a user of opossum runs, it belongs somewhere they would look for it, not here.",
			p, strings.Join(maintainerDirs, ", "), p)
	}
	if size > MaxTrackedBytes {
		return fmt.Sprintf("%s is %d bytes, over the %d-byte limit for a tracked file.\n"+
			"  Large files make every clone slower forever — git keeps them even after a later "+
			"deletion. If it's a build artifact or a captured fixture, generate it instead of "+
			"committing it; if it truly belongs here, raise MaxTrackedBytes in "+
			"internal/repohygiene in the same change, so the cost is visible in review.",
			p, size, MaxTrackedBytes)
	}
	return ""
}
