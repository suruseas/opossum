package runtime

import (
	"errors"
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
	// line is the start of the line being written, kept so that a line can be
	// read from its first byte: whose line it is — the runtime's or a build
	// step's — shows only there. Held to maxHeldLine; longLine says the rest of
	// an over-long line is being let through unread.
	line         []byte
	longLine     bool
	imageRefused bool
	// refusedLine is the runtime's line for the refused request, whose URL
	// names the image the build asked for.
	refusedLine string
}

// maxHeldLine bounds how much of one line is kept. The runtime's refusal is a
// few hundred bytes (a registry URL and a reason); a step can write a line of
// any length, and none of that is the line looked for.
const maxHeldLine = 4096

// ErrBuildImageRefused is what a failed Build reports, through errors.Is, when
// the failure it explained is a registry refusing an image the build pulls. A
// caller with advice of its own for build failures uses it to tell this one
// apart: the image is not there, or cannot be seen, whoever builds it.
var ErrBuildImageRefused = errors.New("a registry refused an image the build pulls")

// buildImageRefused marks a build error as ErrBuildImageRefused without putting
// the sentinel's words into the message, which already carries the hint.
type buildImageRefused struct{ err error }

func (e *buildImageRefused) Error() string        { return e.err.Error() }
func (e *buildImageRefused) Unwrap() error        { return e.err }
func (e *buildImageRefused) Is(target error) bool { return target == ErrBuildImageRefused }

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

// isImageRefusedLine reports whether line — one whole line of build output — is
// the runtime's closing line when a registry would not hand over a manifest the
// build asked for: `Error: <kind>: "HTTP request to <url> failed with response:
// <status>…"`. The kind is not read: measured on 1.4.1 it is `unknown` for a tag
// that is not there and `internalError` for a repository that is not there or
// cannot be seen. A refused layer has not been produced through a build, so
// only the manifest read is matched.
//
// It has to be read from the line's first byte. A build step can print the same
// words, and the builder then shows them twice with its own prefix in front
// (the step's number and the seconds elapsed, as in `#5 0.045 …`, and the
// seconds alone in the closing summary), while the runtime's
// closing line for the failed step begins `Error: unknown: "failed to solve: …`
// and only quotes them further in (measured; the capture holds both). So:
// `Error: `, one word, and the request straight after it.
func isImageRefusedLine(line string) bool {
	if !strings.HasPrefix(line, "Error: ") {
		return false
	}
	rest := line[len("Error: "):]
	at := strings.Index(rest, `: "HTTP request to `)
	if at <= 0 {
		return false
	}
	kind, request := rest[:at], rest[at:]
	if strings.ContainsAny(kind, " \t\"") {
		return false
	}
	return strings.Contains(request, " failed with response: ") && strings.Contains(request, "/manifests/")
}

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
	d.readLines(p)
	return len(p), nil
}

// readLines feeds p through the line being held, reading each line as it ends.
// Lines end at a newline only: a carriage return is how a step redraws its own
// line, and what follows it is still that step's output.
func (d *buildErrorDetector) readLines(p []byte) {
	for _, b := range p {
		if b == '\n' {
			d.endLine()
			continue
		}
		if d.longLine {
			continue
		}
		if len(d.line) == maxHeldLine {
			d.line, d.longLine = d.line[:0], true
			continue
		}
		d.line = append(d.line, b)
	}
}

// endLine reads the line being held and starts the next one.
func (d *buildErrorDetector) endLine() {
	if !d.longLine && isImageRefusedLine(string(d.line)) {
		d.imageRefused = true
		d.refusedLine = string(d.line)
	}
	d.line, d.longLine = d.line[:0], false
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
//
// redo is the opossum command the reader typed and should type again once the
// remedy is applied — builds are reached from `up`, `run`, and `build`, and
// advice that names a command the reader didn't type sends them somewhere else.
// Empty means the caller didn't say: the hint then names no command, because
// naming none beats naming a wrong one.
func (d *buildErrorDetector) hint(redo string) string {
	return hintFor(d.diagnosed(), redo)
}

// hintFor is the guidance for one diagnosis. Build asks for the diagnosis once
// and uses it for both the hint and what it reports to its caller, so the two
// are about the same failure.
func hintFor(failure buildFailure, redo string) string {
	switch failure {
	case failedDiskFull:
		then := "then rerun the opossum command that failed"
		if redo != "" {
			then = "then: " + redo
		}
		return "hint: the build ran out of disk space — Apple's builder pulls multi-GB base images and writes build layers onto the host volume. Free space and retry:\n" +
			"    container image prune -f          # remove unused images\n" +
			"    container builder delete --force  # clear the builder's cache (recreated automatically)\n" +
			"    df -h /                           # confirm there's room, " + then
	case failedResources:
		retry := "    # then rerun the opossum command that failed"
		if redo != "" {
			retry = "    " + redo
		}
		return "hint: the builder ran out of resources or lost its connection — common for heavy builds. Give it more and retry:\n" +
			"    container builder delete --force\n" +
			"    container builder start --cpus 4 --memory 8g\n" +
			retry
	case failedCache:
		again := "then rerun the opossum command that failed."
		if redo != "" {
			again = "then run `" + redo + "` again."
		}
		return "hint: the builder cache looks corrupted (e.g. from a build interrupted with Ctrl-C). " +
			"Run `container builder delete --force` (a fresh builder is created automatically), " + again
	case failedImageRefused:
		// After the three above, which keep answering as they did: what a full
		// disk or a dropped connection looks like alongside a refusal has not
		// been measured. The image is not named here — the request in the
		// runtime's line above names it, and rebuilding a name from a registry
		// URL (`registry-1.docker.io`, `library/`) would be a guess.
		again := "then rerun the opossum command that failed."
		if redo != "" {
			again = "then run `" + redo + "` again."
		}
		return "hint: a registry refused an image this build pulls — the request is in the error above. " +
			"Check that image's name and tag where the Dockerfile names it (a `FROM` or a `COPY --from`, for instance) " +
			"and that it's reachable (registry auth / network), " + again
	}
	return ""
}

// refusedContext is the additional context whose name the refused request
// asked a registry for as an image, or "" when the request is for none of
// them. The builder, given a name it has no context for, asks a registry for
// it with no tag (contextManifestURL) — `https://registry-1.docker.io/v2/library/<name>/manifests/latest`
// for a bare name (measured on container 1.4.1 with `COPY --from=sharedlib`,
// on the machine and not in the capture the tests replay: `…/v2/library/sharedlib/manifests/latest
// failed with response: 401`) — so that is the request matched, tag and all.
// A request with another tag or a digest is for an image the Dockerfile
// named with it (`FROM alpine:3.20`), which a context of the same name does
// not stand in for (docker 29.8.0 buildx, measured 2026-09-20: a context
// `alpine` is not used for `FROM alpine:nosuchtag`), and the ordinary hint —
// check that image's name and tag — is the true one there.
func (d *buildErrorDetector) refusedContext(contexts []string) string {
	d.mu.Lock()
	line := d.refusedLine
	d.mu.Unlock()
	for _, name := range contexts {
		if url := contextManifestURL(name); url != "" && strings.Contains(line, `"HTTP request to `+url+" failed with response: ") {
			return name
		}
	}
	return ""
}

// contextManifestURL is the request the builder makes for name when no
// context stands in for it, or "" for a name that is not matched. Measured on
// container 1.4.1 with `COPY --from=<name>` (2026-09-20): a bare name goes to
// Docker Hub's library (`sharedlib` → `registry-1.docker.io/v2/library/sharedlib`);
// a name with a `/` goes to Docker Hub as written (`org/lib` →
// `registry-1.docker.io/v2/org/lib`, no `library/`), unless its first part
// looks like a host — a `.` or a `:` in it, or `localhost` — which the request
// goes to with the rest as the path (`example.com/org/lib` →
// `example.com/v2/org/lib`).
//
// Docker compose does not use a context in place of a name that is a Docker
// Hub reference spelled out — `library/sharedlib`, `docker.io/org/lib`,
// `docker.io/library/sharedlib` all go to the registry there (docker 29.8.0,
// measured 2026-09-20) — so such a name is not matched, and the ordinary hint
// stays: one under `library/` is left out here, and one naming `docker.io`
// is asked of `registry-1.docker.io`, not of the host it names, so its URL
// never matches. One naming `registry-1.docker.io` is left out too: docker
// compose does use it as a context, but the builder's request for it has not
// been measured, and the ordinary hint is what it had before. A name with a
// tag (`org/lib:v1`) is asked for with that tag, not `latest`, and is not
// matched either.
func contextManifestURL(name string) string {
	host, path := "registry-1.docker.io", "library/"+name
	if first, rest, ok := strings.Cut(name, "/"); ok {
		switch {
		case first == "library" || first == "registry-1.docker.io":
			return ""
		case strings.ContainsAny(first, ".:") || first == "localhost":
			host, path = first, rest
		default:
			path = name
		}
	}
	return "https://" + host + "/v2/" + path + "/manifests/latest"
}

// additionalContextHint is the refused-image hint when the image asked for is
// the name of an additional build context: the name was never an image, and
// checking its tag would send the reader the wrong way.
func additionalContextHint(name string) string {
	return "hint: the build asked a registry for `" + name + "` as an image, and `" + name + "` is one of this service's additional build contexts (`build.additional_contexts`), which opossum does not pass: `container build` takes one context directory and has no way to add another. " +
		"Copy what the Dockerfile takes from `" + name + "` into the build context, or build the image with docker and bring it over with `opossum up --from-docker-compose`."
}

// buildFailure is which known failure a build's output is read as.
type buildFailure int

const (
	noKnownFailure buildFailure = iota
	failedDiskFull
	failedResources
	failedCache
	failedImageRefused
)

// diagnosed says which failure answers, and is the one place the order is
// written. Disk first (see
// hint); a refused image last, after the three that were here before it — what
// a full disk or a dropped connection looks like alongside a refusal has not
// been measured, so those keep answering as they did.
func (d *buildErrorDetector) diagnosed() buildFailure {
	d.mu.Lock()
	defer d.mu.Unlock()
	// The runtime's closing line is the last thing it writes and need not end in
	// a newline, so whatever is still held is a line too.
	if len(d.line) > 0 || d.longLine {
		d.endLine()
	}
	switch {
	case d.diskFull:
		return failedDiskFull
	case d.resourceExhausted:
		return failedResources
	case d.cacheCorrupt:
		return failedCache
	case d.imageRefused:
		return failedImageRefused
	}
	return noKnownFailure
}
