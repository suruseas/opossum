package runtime

import (
	"errors"
	"strings"
	"testing"
)

// When the registry refuses, as an image, the name of one of the build's
// additional contexts, the hint says the name was meant as a context — which
// `container build` cannot be given — instead of asking the reader to check an
// image's tag. Replayed from the capture: shape b is the builder asking
// `registry-1.docker.io/v2/library/nosuchimage-neko1104` for a bare name it
// could not find, the shape a context name takes (measured on container 1.4.1
// with `COPY --from=sharedlib`). The build still fails as before, and is
// still told as a refused image to a caller that branches on it.
func TestARefusedImageThatIsAnAdditionalContextIsToldAsOne(t *testing.T) {
	const contextHint = "is one of this service's additional build contexts (`build.additional_contexts`)"
	// Synthetic lines, one condition each changed from shape b's.
	const b = `Error: internalError: "HTTP request to https://registry-1.docker.io/v2/library/nosuchimage-neko1104/manifests/latest failed with response: 401 Unauthorized. Reason: Unknown, no credentials found for host registry-1.docker.io"`
	if !strings.Contains(captured(t, "b"), b) {
		t.Fatal("the line these rows vary is no longer the one in the capture")
	}
	for _, tc := range []struct {
		name, shape string
		contexts    []string
		contextSaid bool
		refusedSaid bool
		// output, when set, is replayed instead of the capture's shape.
		output string
	}{
		// A context named like an image the Dockerfile takes with a tag of its
		// own: the request carries that tag, the context does not stand in for
		// it (docker buildx: a context `alpine` is not used for `FROM
		// alpine:nosuchtag`), and the ordinary hint is the true one. Shape a is
		// that tag missing; shape d the same in the second stage.
		{name: "a context named like an image with a tag that is not there", shape: "a", contexts: []string{"alpine"}, contextSaid: false, refusedSaid: true},
		{name: "the same in the second stage", shape: "d", contexts: []string{"alpine"}, contextSaid: false, refusedSaid: true},
		{name: "a request by digest", contexts: []string{"nosuchimage-neko1104"}, contextSaid: false, refusedSaid: true,
			output: strings.Replace(b, "/manifests/latest", "/manifests/sha256:0123", 1) + "\n"},
		{name: "another registry's library namespace", contexts: []string{"nosuchimage-neko1104"}, contextSaid: false, refusedSaid: true,
			output: strings.Replace(b, "registry-1.docker.io", "ghcr.io", 1) + "\n"},
		// A tag that merely begins with `latest` is another tag.
		{name: "a tag that begins with latest", contexts: []string{"nosuchimage-neko1104"}, contextSaid: false, refusedSaid: true,
			output: strings.Replace(b, "/manifests/latest", "/manifests/latest-dev", 1) + "\n"},
		// A library repository with no `latest` answers 404, wrapped as
		// `unknown` (shape a's wrapper): the context is still what it was.
		{name: "a 404 wrapped as unknown", contexts: []string{"nosuchimage-neko1104"}, contextSaid: true, refusedSaid: false,
			output: strings.NewReplacer("internalError", "unknown", "401 Unauthorized. Reason: Unknown, no credentials found for host registry-1.docker.io", "404 Not Found. Reason: Unknown").Replace(b) + "\n"},
		// The runtime's closing line is the one read: of two refusals, the last.
		{name: "two refusals, the context's last", contexts: []string{"nosuchimage-neko1104"}, contextSaid: true, refusedSaid: false,
			output: strings.Replace(b, "nosuchimage-neko1104", "other", 1) + "\n" + b + "\n"},
		{name: "two refusals, the context's first", contexts: []string{"nosuchimage-neko1104"}, contextSaid: false, refusedSaid: true,
			output: b + "\n" + strings.Replace(b, "nosuchimage-neko1104", "other", 1) + "\n"},
		{name: "the refused name is a context", shape: "b", contexts: []string{"nosuchimage-neko1104"}, contextSaid: true},
		{name: "the refused name is the second context", shape: "b", contexts: []string{"lib", "nosuchimage-neko1104"}, contextSaid: true},
		{name: "no context has the refused name", shape: "b", contexts: []string{"lib"}, refusedSaid: true},
		{name: "no contexts at all", shape: "b", refusedSaid: true},
		// A name that is part of the refused one is not it.
		{name: "a context whose name is part of the refused one", shape: "b", contexts: []string{"nosuchimage"}, refusedSaid: true},
		// A refusal in another registry's namespace names no bare image.
		{name: "a refusal outside the library namespace", shape: "c", contexts: []string{"private-nosuch-neko1104"}, refusedSaid: true},
		// Not a refusal at all: an ordinary failing step says nothing of either.
		{name: "an ordinary failing step", shape: "f", contexts: []string{"nosuchimage-neko1104"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := tc.output
			if out == "" {
				out = captured(t, tc.shape)
			}
			r := replayBuild(t, out)
			err := r.Build(BuildOptions{Tag: "x:1", Context: t.TempDir(), Redo: "opossum up", AdditionalContexts: tc.contexts})
			if err == nil {
				t.Fatal("expected a build error")
			}
			msg := err.Error()
			if got := strings.Contains(msg, contextHint); got != tc.contextSaid {
				t.Errorf("the context named: %v, want %v\n%s", got, tc.contextSaid, msg)
			}
			if tc.contextSaid && !strings.Contains(msg, "`nosuchimage-neko1104` as an image") {
				t.Errorf("the hint should name the context it was:\n%s", msg)
			}
			if got := strings.Contains(msg, refusedHint); got != tc.refusedSaid {
				t.Errorf("the ordinary refused-image hint: %v, want %v\n%s", got, tc.refusedSaid, msg)
			}
			// Still a refused image to a caller that branches on it.
			if want := tc.contextSaid || tc.refusedSaid; errors.Is(err, ErrBuildImageRefused) != want {
				t.Errorf("errors.Is(err, ErrBuildImageRefused) = %v, want %v", !want, want)
			}
		})
	}
}

// A build that also ran out of disk is told as that, as before: the disk is
// read first, and its remedy is what the reader needs, whatever else the
// output shows.
func TestARunOutOfDiskIsToldFirstEvenWithAContextRefused(t *testing.T) {
	r := replayBuild(t, "error: write /var/lib/buildkit: no space left on device\n"+captured(t, "b"))
	err := r.Build(BuildOptions{Tag: "x:1", Context: t.TempDir(), Redo: "opossum up", AdditionalContexts: []string{"nosuchimage-neko1104"}})
	if err == nil {
		t.Fatal("expected a build error")
	}
	if !strings.Contains(err.Error(), "ran out of disk space") {
		t.Errorf("want the disk told first, got:\n%v", err)
	}
	if strings.Contains(err.Error(), "additional build contexts") {
		t.Errorf("the context hint should not stand in for the disk's:\n%v", err)
	}
}

// A context name with a `/` in it is asked for as written, not under Docker
// Hub's library, and one whose first part is a host is asked of that host.
// The lines are the builder's own, taken on container 1.4.1 with
// `additional_contexts: {<name>: ./shared}` and `COPY --from=<name>`
// (2026-09-20). A name that is a Docker Hub reference spelled out
// (`library/…`, `docker.io/…`) is one docker compose does not use a context
// for, so the ordinary hint stays; so does a failure that is not a refusal
// (nothing listening, a host that does not resolve).
func TestAContextNameWithASlashIsMatchedAsTheBuilderAsksForIt(t *testing.T) {
	const contextHint = "is one of this service's additional build contexts (`build.additional_contexts`)"
	const (
		orgLib   = `Error: internalError: "HTTP request to https://registry-1.docker.io/v2/org/lib/manifests/latest failed with response: 401 Unauthorized. Reason: Unknown, no credentials found for host registry-1.docker.io"`
		abc      = `Error: internalError: "HTTP request to https://registry-1.docker.io/v2/a/b/c/manifests/latest failed with response: 401 Unauthorized. Reason: Unknown, no credentials found for host registry-1.docker.io"`
		hosted   = `Error: unknown: "HTTP request to https://example.com/v2/org/lib/manifests/latest failed with response: 404 Not Found. Reason: Unknown"`
		library  = `Error: internalError: "HTTP request to https://registry-1.docker.io/v2/library/sharedlib/manifests/latest failed with response: 401 Unauthorized. Reason: Unknown, no credentials found for host registry-1.docker.io"`
		refused  = `Error: unknown: "POSIXErrorCode(rawValue: 61): Connection refused"`
		resolved = `Error: internalError: "failed to resolve either repository hostname nosuchhost.invalid"`
	)
	for _, tc := range []struct {
		name, line string
		contexts   []string
		said       string // the context the hint names; "" for none
	}{
		{"a name with one slash", orgLib, []string{"org/lib"}, "org/lib"},
		{"a name with two", abc, []string{"a/b/c"}, "a/b/c"},
		{"a name whose first part is a host", hosted, []string{"example.com/org/lib"}, "example.com/org/lib"},
		{"a bare name, as before", library, []string{"sharedlib"}, "sharedlib"},
		{"the second of two contexts", orgLib, []string{"sharedlib", "org/lib"}, "org/lib"},
		// Synthetic: the hosted line with the host changed to one with a port,
		// and to `localhost` alone — a registry that answers there.
		{"a host with a port", strings.Replace(hosted, "example.com", "localhost:5000", 1), []string{"localhost:5000/org/lib"}, "localhost:5000/org/lib"},
		{"localhost alone", strings.Replace(hosted, "example.com", "localhost", 1), []string{"localhost/org/lib"}, "localhost/org/lib"},
		// Synthetic: only the first part says host. A `.` further on is part
		// of the path (docker compose uses `org/my.lib` as a context), and a
		// first part that merely begins with `localhost` is a Docker Hub
		// namespace (docker compose uses `localhostfoo/lib` as one).
		{"a dot after the first part", strings.Replace(orgLib, "/v2/org/lib/", "/v2/org/my.lib/", 1), []string{"org/my.lib"}, "org/my.lib"},
		{"a first part that begins with localhost", strings.Replace(orgLib, "/v2/org/lib/", "/v2/localhostfoo/lib/", 1), []string{"localhostfoo/lib"}, "localhostfoo/lib"},
		// A context name with a tag: the builder asks for that tag, which is
		// not the request matched; the ordinary hint stays, as before.
		{"a name with a tag", strings.Replace(orgLib, "/manifests/latest", "/manifests/v1", 1), []string{"org/lib:v1"}, ""},
		// Not the name asked for.
		// Docker Hub references spelled out, which docker compose does not
		// use a context for (docker 29.8.0, measured): the ordinary hint.
		{"a name under library/", library, []string{"library/sharedlib"}, ""},
		{"a name under docker.io/", orgLib, []string{"docker.io/org/lib"}, ""},
		{"a bare name is not a slashed request", orgLib, []string{"lib"}, ""},
		{"the host is not dropped", hosted, []string{"org/lib"}, ""},
		{"the host is not Docker Hub", orgLib, []string{"example.com/org/lib"}, ""},
		{"a part of the path", abc, []string{"a/b"}, ""},
		{"a host spelled as Docker Hub", library, []string{"docker.io/library/sharedlib"}, ""},
		{"Docker Hub's index host", library, []string{"index.docker.io/library/sharedlib"}, ""},
		// Docker compose uses it as a context, but what the builder asks for
		// has not been measured: the ordinary hint, as before.
		{"Docker Hub's registry host", orgLib, []string{"registry-1.docker.io/org/lib"}, ""},
		// Not a refusal: no request line to match.
		{"nothing listening", refused, []string{"localhost:59999/lib"}, ""},
		{"a host that does not resolve", resolved, []string{"nosuchhost.invalid/lib"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := replayBuild(t, tc.line+"\n")
			err := r.Build(BuildOptions{Tag: "x:1", Context: t.TempDir(), Redo: "opossum up", AdditionalContexts: tc.contexts})
			if err == nil {
				t.Fatal("expected a build error")
			}
			msg := err.Error()
			if got := strings.Contains(msg, contextHint); got != (tc.said != "") {
				t.Fatalf("the context named: %v, want %v\n%s", got, tc.said != "", msg)
			}
			if tc.said != "" && !strings.Contains(msg, "`"+tc.said+"` as an image") {
				t.Errorf("the hint should name %q:\n%s", tc.said, msg)
			}
		})
	}
}
