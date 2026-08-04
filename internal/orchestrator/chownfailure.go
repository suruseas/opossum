package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/suruseas/opossum/internal/compose"
)

// Failure-driven suggestions.
//
// The overlay is worth reading because everything in it is backed by something
// opossum knows. Applied entries come from a whitelist of images whose behaviour
// is documented; notes state facts. Suggestions used to be the exception: one of
// them proposed a named volume for any empty read-write bind directory, on the
// theory that an empty directory is where an app will put its data. Measured over
// a corpus of 156 real projects that predicate proposed 193 swaps, and half of
// them were wrong — `/config` files people edit, `/downloads` and `/media` they
// open in Finder, `/logs` they read. A suggestion that is a coin flip is worse
// than none: it teaches the reader to skip the section that also holds the good
// ones.
//
// So the evidence comes from the failure instead. When a container dies chowning
// a bind mount — the signature Apple `container` produces because bind mounts are
// host-owned and cannot be chowned from inside — that service, and that mount, are
// recorded here. The next `up --from-docker-compose` turns each record into a
// suggestion naming exactly that mount. Nothing is proposed for a directory that
// has not actually failed.
//
// The record lives in the project's own state directory, so `destroy` takes it
// with everything else and a fresh clone starts with no history.

// chownFailureFile is where observed failures are kept, under `.opossum/`.
const chownFailureFile = "chown-failures.json"

// chownFailure is one observation: this service died chowning this mount.
type chownFailure struct {
	Service string `json:"service"`
	Target  string `json:"target"` // the container path that could not be chowned
	Source  string `json:"source"` // the host path it was bound from
}

// chownPathRE pulls the path out of a chown failure line. Both spellings appear
// in the images that hit this: `chown: /var/lib/clickhouse/: Operation not
// permitted` and `chown: changing ownership of '/data/db': Operation not
// permitted`.
var chownPathRE = regexp.MustCompile(`chown:[^\n]*?'([^']+)'|chown:\s+([^:\n]+):`)

// chownedPath returns the path a chown failure names, or "" when the line does
// not carry a usable one — redis reports `chown: .:`, which says nothing about
// which mount died.
func chownedPath(logs string) string {
	m := chownPathRE.FindStringSubmatch(logs)
	if m == nil {
		return ""
	}
	p := m[1]
	if p == "" {
		p = m[2]
	}
	p = strings.TrimSpace(p)
	if !strings.HasPrefix(p, "/") {
		return "" // "." and friends: a path we cannot attribute
	}
	// Returned as written, trailing slash and all: the matching below absorbs one,
	// and trimming here would turn `chown: /:` into "" — which reads as "the log
	// said nothing", and the single-mount fallback would then blame a directory the
	// log never mentioned. The root is a real path; it is simply not any mount.
	return p
}

// blamedMount picks the bind mount a chown failure is about.
//
// Naming the wrong mount would be worse than naming none: the suggestion would
// propose detaching a directory that is working. So it answers only when the
// answer is forced — the failing path resolves to exactly one of the service's
// bind mounts, or the service has exactly one bind mount and so there is nothing
// else it could have been. Otherwise it declines, and the crash still gets its
// generic hint.
func blamedMount(svc *compose.Service, logs string) (src, target string, ok bool) {
	type mount struct{ src, target string }
	var binds []mount
	for _, v := range svc.Volumes {
		s, t, mode, ok := splitMount(v)
		if !ok || !isHostPath(s) || readOnlyMount(mode) || isHostDevicePath(s) {
			continue
		}
		binds = append(binds, mount{s, t})
	}
	if len(binds) == 0 {
		return "", "", false
	}
	if p := chownedPath(logs); p != "" {
		// The mount that holds the failing path. `chown /var/lib/mysql` names the
		// mount itself; `chown /var/lib/mysql/data` names something inside it.
		var holds []mount
		for _, b := range binds {
			if p == b.target || strings.HasPrefix(p, strings.TrimSuffix(b.target, "/")+"/") {
				holds = append(holds, b)
			}
		}
		if len(holds) == 1 {
			return holds[0].src, holds[0].target, true
		}
		if len(holds) > 1 {
			return "", "", false // nested mounts: which one owns the path is a guess
		}
		// Nothing holds it. A branch here that blamed a mount *underneath* the
		// failing path was tried and removed: it fired on `chown: /:` for every
		// absolute mount, and the reading behind it was wrong anyway. A log naming
		// a parent means the parent's own chown was refused — a rootfs permission
		// problem that swapping a child mount does not fix — and a recursive chown
		// that dies on a mounted child reports that child's path, which lands in
		// `holds` above.
		//
		// So: the log names a path this service does not mount at all — an image's own
		// directory, chowned by an entrypoint running as a non-root user. Falling
		// back to "well, there is only one bind mount" would name a directory that
		// is working and tell the reader it is the one that died.
		return "", "", false
	}
	// No usable path in the log at all (redis reports `chown: .:`). One bind mount
	// leaves nothing else it could have been.
	if len(binds) == 1 {
		return binds[0].src, binds[0].target, true
	}
	return "", "", false
}

// chownFailurePath is where this project's record lives.
func (o *Orchestrator) chownFailurePath() string {
	return filepath.Join(o.Project.BaseDir, ".opossum", chownFailureFile)
}

// recordChownFailure adds one observation, ignoring a repeat. Failing to write is
// silent on purpose: this is a note to a later command, and a project that cannot
// be written to has bigger problems to report than this one.
func (o *Orchestrator) recordChownFailure(f chownFailure) {
	if o.Project.BaseDir == "" {
		return
	}
	have := o.recordedChownFailures()
	for _, e := range have {
		if e == f {
			return
		}
	}
	have = append(have, f)
	sort.Slice(have, func(i, j int) bool {
		if have[i].Service != have[j].Service {
			return have[i].Service < have[j].Service
		}
		return have[i].Target < have[j].Target
	})
	body, err := json.MarshalIndent(have, "", "  ")
	if err != nil {
		return
	}
	p := o.chownFailurePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	// Written aside and renamed into place: a half-written file parses as nothing,
	// and reading nothing is indistinguishable from never having failed — the whole
	// history would vanish on one bad moment.
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
	}
}

// recordedChownFailures reads the observations back. A missing or unreadable file
// means none: the record is a convenience, never a precondition.
func (o *Orchestrator) recordedChownFailures() []chownFailure {
	if o.Project.BaseDir == "" {
		return nil
	}
	body, err := os.ReadFile(o.chownFailurePath())
	if err != nil {
		return nil
	}
	var out []chownFailure
	if err := json.Unmarshal(body, &out); err != nil {
		return nil
	}
	return out
}
