package runtime

import (
	"strings"
	"sync"
)

// buildErrorDetector scans streamed build output for known failure signatures so
// Build can turn an opaque buildkit error into an actionable hint. It's used as a
// sink alongside the real stdout/stderr (via io.MultiWriter): it never alters or
// withholds output, it only remembers what it saw.
type buildErrorDetector struct {
	mu                sync.Mutex
	tail              string // carry-over, so a signature split across writes still matches
	cacheCorrupt      bool
	resourceExhausted bool
	diskFull          bool
}

// Signatures Apple's `container` builder emits when its cache is in a bad state
// (typically after a build was interrupted), when it runs out of resources or the
// connection drops mid-build, and when the host volume runs out of disk.
var (
	cacheCorruptSignatures = []string{
		"unable to read root manifest",
		"read from underlying reader failed",
		"failed to load cache key",
	}
	resourceExhaustedSignatures = []string{
		"rpc error: code = Unavailable",
		"error reading from server: EOF",
	}
	// A real build pulls multi-GB base images and writes build layers onto the
	// host volume, so running out of disk is a common builder failure. Match the
	// kernel/buildkit wording and the Go errno name.
	diskFullSignatures = []string{
		"no space left on device",
		"No space left on device",
		"ENOSPC",
	}
)

func (d *buildErrorDetector) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.tail + string(p)
	if containsAny(s, cacheCorruptSignatures) {
		d.cacheCorrupt = true
	}
	if containsAny(s, resourceExhaustedSignatures) {
		d.resourceExhausted = true
	}
	if containsAny(s, diskFullSignatures) {
		d.diskFull = true
	}
	// Keep a short tail so a signature straddling two writes is still caught.
	if len(s) > 64 {
		s = s[len(s)-64:]
	}
	d.tail = s
	return len(p), nil
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// hint returns actionable guidance for the failure signature seen, or "" if the
// build failed for an ordinary reason (a Dockerfile/RUN error), which shouldn't
// be second-guessed. Disk exhaustion is checked first: it's usually the root
// cause when it co-occurs with a resource/connection error (a full volume makes
// the builder fail downstream), and its remedy — free disk — is the opposite of
// the resource remedy (grow the builder), which would only make ENOSPC worse.
func (d *buildErrorDetector) hint() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case d.diskFull:
		return "hint: the build ran out of disk space — Apple's builder pulls multi-GB base images and writes build layers onto the host volume. Free space and retry:\n" +
			"    container image prune -f          # remove unused images\n" +
			"    container builder delete --force  # clear the builder's cache (recreated automatically)\n" +
			"    df -h /                           # confirm there's room, then: opossum up"
	case d.resourceExhausted:
		return "hint: the builder ran out of resources or lost its connection — common for heavy builds. Give it more and retry:\n" +
			"    container builder delete --force\n" +
			"    container builder start --cpus 4 --memory 8g\n" +
			"    opossum up"
	case d.cacheCorrupt:
		return "hint: the builder cache looks corrupted (e.g. from a build interrupted with Ctrl-C). " +
			"Run `container builder delete --force` (a fresh builder is created automatically), then run `opossum up` again."
	}
	return ""
}
