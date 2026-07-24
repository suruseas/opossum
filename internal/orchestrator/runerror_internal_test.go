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

func TestRunErrorHintPlatform(t *testing.T) {
	h := runErrorHint(&compose.Service{}, runErr("Error: image sha256:abc does not support required platforms"))
	if !strings.Contains(h, "arm64") || !strings.Contains(h, "platform: linux/amd64") {
		t.Errorf("amd64-only image should hint at `platform: linux/amd64`, got: %q", h)
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
	if s := o.decodeStartError("web", runErr("random crash")).Error(); !strings.Contains(s, "opossum logs web") {
		t.Errorf("an unrecognized failure should fall back to startFailed, got: %s", s)
	}
}
