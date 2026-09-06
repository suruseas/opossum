package orchestrator

// Evals for #298: decoding cryptic `container run` failures the pre-flight can't
// catch into actionable hints. Signatures are the real stderr captured while
// dogfooding Haxxnet (#296): an amd64-only image, a host-port conflict the host
// probe can't see (Apple `container`'s DNS holds 53), and a missing file bind mount.

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	rt "github.com/suruseas/opossum/internal/runtime"
)

func runErr(stderr string) error {
	return &rt.RunError{Err: fmt.Errorf("exit status 1"), Stderr: stderr}
}

// An image with no arm64 build is told so in whichever words the runtime uses,
// and an image with no amd64 build is not told to ask for amd64.
//
// The wordings are both measured, on the versions named beside them. The runtime
// changed how it says this between them, and the older text kept working while
// the newer one silently matched nothing: the guidance simply stopped appearing,
// which from the outside looks like guidance that was never written. Every
// wording here stays until the version that produces it is out of use.
//
// The last case is the one that makes the arm64 wording specific rather than
// loose. The 1.2.2 message names the platform that is *missing*, so asking an
// arm64-only image for amd64 says `platform linux/amd64` — and answering that
// with "add `platform: linux/amd64`" tells someone to do the thing that just
// failed.
func TestRunErrorHintPlatform(t *testing.T) {
	for _, tc := range []struct {
		name, platform, stderr string
		wantHint               bool
	}{
		// container 1.1.0, and 1.2.2 as well: measured on the corpus, an image
		// that is fetched before the mismatch is found still says this on 1.2.2
		// (Compose-Examples/examples/cs2-dedicated-server, atlas).
		{"the wording from both versions", "", "Error: image sha256:abc does not support required platforms", true},
		// container 1.2.2 said this for excalidraw/excalidraw-room:latest (image
		// index without arm64); 1.3.1 says it for an amd64-only local image run as
		// the default arm64 (platform-image-no-arm64-131.txt) — same wording,
		// different trigger.
		{"the wording both versions use for a missing arm64", "", "Error: platform linux/arm64", true},
		// container 1.2.2, measured by building an image for arm64 only and
		// running it with --platform linux/amd64. The message names what is
		// missing, so this is not an arm64 problem and amd64 is not the answer.
		{"an image with no amd64 build", "", "Error: platform linux/amd64", false},
		// The older wording does not say which platform was wanted, so the service
		// has to. Asked for amd64 and told to ask for amd64 is not advice.
		{"the older wording, having already asked for amd64", "linux/amd64",
			"Error: image sha256:abc does not support required platforms", false},
		// The same request, spelled the other way. The runtime treats both as
		// x86-64 and gives either one Rosetta; read differently here, a service
		// written this way is told to ask for what it already asked for.
		{"having asked for it as x86_64", "linux/x86_64",
			"Error: image sha256:abc does not support required platforms", false},
		{"having asked for it in capitals", "linux/AMD64",
			"Error: image sha256:abc does not support required platforms", false},
		// `platform linux/arm64` on its own is a substring of the flag a service
		// legitimately asks for. Nothing writes a command line into this stream
		// today — it carries the child's stderr and nothing else — so the anchor
		// guards against text arriving from somewhere that is not this failure,
		// rather than against a path anyone can point at now.
		{"a command line that names the platform", "linux/arm64",
			"run -d --platform linux/arm64 --name web web:latest: exit status 1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := runErrorHint(&compose.Service{Platform: tc.platform}, runErr(tc.stderr))
			if got := strings.Contains(h, "platform: linux/amd64"); got != tc.wantHint {
				if tc.wantHint {
					t.Errorf("this is an image with no arm64 build and nothing said so; the runtime "+
						"wording changed and the match did not, got: %q", h)
				} else {
					t.Errorf("telling anyone to add `platform: linux/amd64` here is telling them to "+
						"repeat what just failed, or to fix something that did not break, got: %q", h)
				}
			}
			switch {
			case tc.wantHint && !strings.Contains(h, "arm64"):
				t.Errorf("the hint should say which architecture has no build, got: %q", h)
			case !tc.wantHint && h != "":
				// Not merely "no amd64 advice": any hint here is a wrong answer, and
				// checking only for the amd64 wording would let a different wrong one
				// through.
				t.Errorf("nothing here is diagnosed, so nothing should be claimed, got: %q", h)
			}
		})
	}
}

func TestRunErrorHintPortConflict(t *testing.T) {
	svc := &compose.Service{Ports: []string{"8080:80/tcp", "53:53/udp"}}
	h := runErrorHint(svc, runErr("Error: failed to bootstrap container (cause: bind(descriptor:ptr:bytes:): Address already in use) (errno: 48)"))
	// Names the service's published ports (svc-derived, not just the static text)…
	if !strings.Contains(h, "this service publishes") || !strings.Contains(h, "localhost:8080") || !strings.Contains(h, "localhost:53") {
		t.Errorf("port hint should echo the service's published ports, got: %q", h)
	}
	// …and appends the per-service culprit note because 53 is actually published.
	if !strings.Contains(h, "on macOS, 53 is the runtime's built-in DNS") || !strings.Contains(h, "Remap") {
		t.Errorf("publishing 53 should append the runtime-DNS culprit note, got: %q", h)
	}
}

// The per-service culprit note ("on macOS, …") only appears for the ports the
// service actually publishes — no spurious 53/AirPlay note for an unrelated port.
func TestRunErrorHintPortNoSpuriousCulprit(t *testing.T) {
	h := runErrorHint(&compose.Service{Ports: []string{"8080:80"}},
		runErr("bind: Address already in use"))
	if strings.Contains(h, "AirPlay") || strings.Contains(h, "on macOS,") {
		t.Errorf("an 8080-only conflict must not append the 53/AirPlay culprit note, got: %q", h)
	}
	if !strings.Contains(h, "localhost:8080") {
		t.Errorf("should still name the published port, got: %q", h)
	}
}

func TestRunErrorHintFileBind(t *testing.T) {
	h := runErrorHint(&compose.Service{Volumes: []string{"./Caddyfile:/etc/caddy/Caddyfile"}},
		runErr("Error: failed to start process (cause: mount failed with errno 20: failed to resolve '/etc/caddy/Caddyfile' in rootfs)"))
	if !strings.Contains(h, "/etc/caddy/Caddyfile") || !strings.Contains(h, "config FILE") || !strings.Contains(h, "directory") {
		t.Errorf("a file bind-mount failure should name the path and explain the dir-vs-file gotcha, got: %q", h)
	}
}

func TestRunErrorHintUnknownAndNonRunError(t *testing.T) {
	if h := runErrorHint(&compose.Service{}, runErr("some other failure\nexit 1")); h != "" {
		t.Errorf("an unrecognized failure must yield no hint, got: %q", h)
	}
	// A plain (non-RunError) error carries no captured stderr — no hint.
	if h := runErrorHint(&compose.Service{}, fmt.Errorf("exit status 1")); h != "" {
		t.Errorf("a non-RunError must yield no hint, got: %q", h)
	}
	// nil service is safe (port branch guards it).
	if h := runErrorHint(nil, runErr("Address already in use")); !strings.Contains(h, "host port") {
		t.Errorf("nil service should still give the generic port hint, got: %q", h)
	}
}

// decodeStartError appends the decoded hint for a recognized run failure, and
// falls back to the generic start-failed message otherwise.
func TestDecodeStartErrorAppendsHint(t *testing.T) {
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{"web": {Name: "web", Image: "x"}}}
	o := New(p, &rt.Runtime{}, "", &bytes.Buffer{})
	if s := o.decodeStartError("web", runErr("Error: image does not support required platforms")).Error(); !strings.Contains(s, "platform: linux/amd64") {
		t.Errorf("a recognized run failure should get the decoded hint, got: %s", s)
	}
	// The service name opens the line and the hint closes it; both are strings,
	// and exchanged the hint would be quoted as the service's name (#559).
	if s := o.decodeStartError("web", runErr("Error: image does not support required platforms")).Error(); !strings.HasPrefix(s, `starting service "web": `) {
		t.Errorf("the decoded error should open with the service's name, got: %s", s)
	}
	if s := o.decodeStartError("web", runErr("random crash")).Error(); !strings.Contains(s, "opossum logs web") {
		t.Errorf("an unrecognized failure should fall back to startFailed, got: %s", s)
	}
}

// Every decoded hint carries a code, and it is the code that indexes its fix —
// this is the half the prose cannot do. Anything splitting "diagnosed" from
// "undiagnosed" has only the code to split on, so an accurate hint without one is
// counted as undiagnosed.
//
// The expected codes are written out rather than derived, so that pointing two
// signatures at one code — or at the wrong one — is a failure and not a rename.
func TestEachDecodedHintCarriesItsCode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		svc    *compose.Service
		stderr string
		want   diagCode
	}{
		{"an amd64-only image", &compose.Service{},
			"Error: image sha256:abc does not support required platforms", codeImageNoArm64},
		// The same failure in the words 1.2.2 also uses. Listed separately because
		// a wording that decodes without carrying its code is a hint nobody can
		// look up, and matching on new text is exactly where that gets forgotten.
		{"an amd64-only image, in the newer wording", &compose.Service{},
			"Error: platform linux/arm64", codeImageNoArm64},
		// Reused, not minted: the pre-flight names this same conflict, and the
		// only difference here is that it could not see it in advance.
		{"a port the pre-flight could not see", &compose.Service{Ports: []string{"53:53/udp"}},
			"Error: bind(descriptor:ptr:bytes:): Address already in use", codeHostPortInUse},
		// Likewise: this is the placeholder directory OPSM-107 warns about,
		// arriving as a refusal instead of a warning.
		{"a bind mount that will not resolve", &compose.Service{},
			"Error: mount failed with errno 20: failed to resolve '/etc/caddy/Caddyfile' in rootfs", codeBindFilePlaceholder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := runErrorHint(tc.svc, runErr(tc.stderr))
			if h == "" {
				t.Fatalf("this signature should still decode at all, got no hint")
			}
			if want := "[" + string(tc.want) + "]"; !strings.Contains(h, want) {
				t.Errorf("the hint should carry %s so it can be looked up, got: %q", want, h)
			}
		})
	}
}

// The generic start failure stays uncoded, and that is a decision rather than an
// omission: opossum has nothing specific to say there, so there is no fix for a
// code to index. Coding it would make every start failure look diagnosed and
// erase the distinction the decoding above exists to draw.
func TestAnUndecodedStartFailureCarriesNoCode(t *testing.T) {
	err := startFailed("web", runErr("Error: something nobody has decoded yet"))
	if strings.Contains(err.Error(), "OPSM-") {
		t.Errorf("an undiagnosed failure must not look diagnosed, got: %v", err)
	}
	if runErrorHint(&compose.Service{}, runErr("Error: something nobody has decoded yet")) != "" {
		t.Error("an unknown signature should decode to nothing at all")
	}
}
