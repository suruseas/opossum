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
// sameName compares one element of a path — a directory, a file name — the way
// the file systems this is developed and shipped on compare them, which is
// without case. `Scripts/` is the directory `scripts/`, and a rule that refuses
// one while letting the other through refuses nothing.
//
// Every rule below goes through this rather than choosing for itself. Three of
// them chose `==` in three separate sittings, the third of them hours after the
// second was corrected for exactly this, and one of the three was still wrong
// when this was written. A rule that can be written the strict way will be.
func sameName(a, b string) bool { return strings.EqualFold(a, b) }

// underDir reports whether p is inside dir, comparing each element by name.
func underDir(p, dir string) bool {
	if sameName(p, dir) {
		return true
	}
	segs := strings.Split(p, "/")
	for i := range segs {
		if sameName(strings.Join(segs[:i+1], "/"), dir) {
			return true
		}
	}
	return false
}

func allowedBinary(p string) bool {
	if !imageExts[strings.ToLower(path.Ext(p))] {
		return false
	}
	for _, d := range binaryDirs {
		if underDir(path.Dir(p), d) {
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

// rootComposeNames are the compose file names opossum itself looks for. One of
// them at the top of this repository is always a leftover: the examples live in
// examples/, and tests write theirs into a temp directory.
//
// This is the shape the last one took — four lines of YAML with an ordinary name,
// written while measuring a message by hand, swept in by `git add -A` when the
// shell had wandered back to the repository root. Nothing caught it: it is not
// binary, not large, and not named like a throwaway. It reached a pull request,
// where `opossum up` in a fresh clone would have read it and failed.
//
// The list is repeated here rather than imported, so that a package about what
// may be tracked does not depend on the compose reader; a test holds it to the
// reader's own list, so a name added there cannot quietly go unguarded here.
var rootComposeNames = []string{
	"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml",
	"compose.override.yaml", "compose.override.yml",
	"docker-compose.override.yaml", "docker-compose.override.yml",
	"compose.opossum.yaml", "compose.opossum.yml",
}

// isRootGo reports whether p is a Go file at the top of the tree.
//
// The module has no package at its root: everything it builds is under cmd/ or
// internal/, and `go list .` says so. A .go file here is therefore always
// something left behind — and the shape it takes is the same as the compose one
// above. A driver written to try a new tool out, the shell back in the repository
// root rather than a temp directory, `git add -A`.
//
// Worse than an ordinary stray, for the same reason the compose file was: it is
// not inert. A `package main` at the top of the module is what
// `go install github.com/suruseas/opossum@latest` installs, under the name
// `opossum`. The release build names ./cmd/opossum explicitly and is unaffected,
// so nothing goes red — the scratch simply becomes the product for anyone who
// installs it the ordinary way.
//
// Only the top level: a name with a directory in front of it cannot equal a bare
// one, which is what keeps cmd/opossum/main.go out of this.
//
// Compared without case, like the other rules here, but for a reason of its own
// and worth writing down rather than borrowing. Elsewhere the argument is that
// the file system does not tell the spellings apart, so whichever is tracked is
// the one that gets read. That does not hold here: the go tool matches `.go`
// exactly, and a tracked SWEEPGEN.GO builds nothing (`go list .` answers "no Go
// files"). What holds instead is that on a file system which does not
// distinguish them, the inert spelling is one keystroke from the live one, and
// the change of case is a rename this one will not force anybody to make.
func isRootGo(p string) bool {
	return strings.HasSuffix(strings.ToLower(p), ".go") && !strings.Contains(p, "/")
}

// isRootCompose reports whether p is one of them, at the top of the tree.
//
// Comparing the whole path is what keeps this to the top level: a name with a
// directory in front of it cannot equal a bare one, so examples/hello/compose.yaml
// is not this. An explicit test for a slash was here and did nothing.
//
// Compared without case, like the other rules here, and for a sharper reason than
// tidiness: the file system this is usually developed on does not distinguish
// them either, so a tracked `Compose.yaml` is the file `opossum up` opens when it
// asks for `compose.yaml`. Matching exactly would have let the one spelling
// through that behaves identically to the one refused.
func isRootCompose(p string) bool {
	for _, name := range rootComposeNames {
		if sameName(p, name) {
			return true
		}
	}
	return false
}

// runtimeStateDir is where opossum keeps what it writes about a project it is
// running — the MCP wiring it hands to a service, the record of a mount that
// failed, a workspace's snapshots.
//
// Tracking any of it is the same mistake as tracking a compose file at the top:
// these are read back at run time, so a copy committed here is not a leftover
// sitting quietly, it is state that a fresh clone will act on. Nothing of the
// sort is tracked today, which is the cheapest moment to say so.
const runtimeStateDir = ".opossum"

// underRuntimeState reports whether p is inside it, anywhere in the tree — a
// project can be a subdirectory (examples/ holds several), so this is not about
// the top level the way the compose rule is.
func underRuntimeState(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		// Without case, for the same reason as the compose rule directly below:
		// the file system this is developed on does not tell `.Opossum` from
		// `.opossum`, so a tracked one is what opossum reads back. Written
		// case-sensitively at first, hours after the other rule was corrected for
		// exactly this — a rule can be learnt and the next one still written the
		// old way.
		if sameName(seg, runtimeStateDir) {
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
		if underDir(p, d) {
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
	if underRuntimeState(p) {
		return fmt.Sprintf("%s is under %s/, where opossum keeps what it writes about a project "+
			"it is running.\n"+
			"  That is read back at run time — the wiring handed to a service, a record of a mount "+
			"that failed — so a copy committed here is not a file sitting quietly. A fresh clone "+
			"acts on it.\n"+
			"  Remove it (`git rm --cached %s`) and let opossum write its own. Add %s/ to "+
			".gitignore if it keeps coming back.", p, runtimeStateDir, p, runtimeStateDir)
	}
	if isRootCompose(p) {
		return fmt.Sprintf("%s is a compose file at the top of the repository, and there is no "+
			"reason for one to be here.\n"+
			"  These arrive from measuring a message by hand — writing a small compose file to "+
			"try something, with the shell in the repository root rather than a temp directory, "+
			"and `git add -A` sweeping it up. It is worse than an ordinary stray: `opossum up` in "+
			"a fresh clone reads exactly this name, so whatever was being tried becomes what a "+
			"reader runs.\n"+
			"  Remove it (`git rm --cached %s`). Examples belong in examples/; a file for trying "+
			"something belongs in a temp directory.", p, p)
	}
	if isRootGo(p) {
		return fmt.Sprintf("%s is a Go file at the top of the repository, and nothing is built "+
			"from here — every package this module has lives under cmd/ or internal/.\n"+
			"  These arrive from trying a tool out: a small driver written to see what something "+
			"prints, with the shell in the repository root rather than a temp directory, and "+
			"`git add -A` sweeping it up. It is worse than an ordinary stray, because a "+
			"`package main` here is what `go install <module>@latest` installs under the "+
			"product's name. The release build names ./cmd/opossum, so nothing turns red.\n"+
			"  Remove it (`git rm --cached %s`). A program worth keeping belongs under cmd/; "+
			"one written to try something belongs in a temp directory.", p, p)
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

// EmptiedMarker is the private-use character opossum writes where a reference
// produced nothing (internal/compose marks a substitution's empty result with
// it so a later pass can tell "expanded to nothing" from "was never there").
//
// It is written into values at run time, and it is never meant to be in the
// repository as a literal. The reason to guard that is the failure it produces:
// a stray one in a fixture makes the code that looks for it fire, so a test
// reaches the branch it meant to reach and passes — and only stops passing if
// the character is ever cleaned up. That happened during #436: a probe fixture
// carried one, the source read as plain "SECRETCANARY", and it took `od` to see
// the extra bytes. The sweep it was in was hollow until review found it.
//
// The escaped spelling is not a literal and is not looked for: `"\ue000"` in
// source is six ASCII characters. That is the distinction for this character:
// the visible spelling is allowed and the invisible one is not. It is not a
// rule about invisible characters in general — this looks for U+E000 and
// nothing else, because U+E000 is the one that makes a test pass by being
// there. Widen it when something else earns it.
const EmptiedMarker = '\ue000'

// InvisibleLiteral reports the first literal EmptiedMarker in content, naming
// the file and the byte offset. It returns "" both when there is none and when
// content is not text — a NUL in its head means the advice below has nothing to
// offer, so such a file is not read for this at all. That is a narrowing worth
// knowing at the call site: a sweep built on this reports nothing about the
// repository's images.
//
// The offset is there because the character is invisible: a reader told only
// which file has one has no way to look at it. `od` is named for the same
// reason — a grep for something you cannot type is not much help.
func InvisibleLiteral(p string, content []byte) string {
	// Text only. Three bytes can turn up anywhere in a compressed image by
	// chance — the tracked binaries here are around half a megabyte, which is
	// roughly a one-in-thirty-five shot already, and it grows with every one
	// added — and the advice below is meaningless for a PNG: there is no
	// escaped spelling to write instead, so the red would have no way out. The
	// same NUL sniff the rest of this package uses decides what is text.
	head := content
	if len(head) > SniffBytes {
		head = head[:SniffBytes]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return ""
	}
	i := bytes.IndexRune(content, EmptiedMarker)
	if i < 0 {
		return ""
	}
	line := 1 + bytes.Count(content[:i], []byte("\n"))
	return fmt.Sprintf("%s carries a literal U+E000 — the first at byte %d (line %d) — and nothing can see it.\n"+
		"  opossum writes that character at run time to mark where a reference produced "+
		"nothing, so code that looks for it fires on this file's contents. A fixture "+
		"carrying one reaches the branch it meant to test and passes for the wrong "+
		"reason — and goes red the day someone cleans the character out.\n"+
		"  Look at it with `LC_ALL=C od -v -c %s | sed -n '%d,%dp'` (it is the three bytes "+
		"356 200 200; the locale matters, because od prints multibyte characters as ** "+
		"under a UTF-8 one, and -v keeps it from folding repeated lines away). "+
		"If the character is meant to be there, write it as the escape \\ue000, which is "+
		"visible in a diff and is not what this looks for.",
		p, i, line, p, 1+i/16, 2+i/16)
}
