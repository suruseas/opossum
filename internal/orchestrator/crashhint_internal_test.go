package orchestrator

// Evals for #293: a DB image that chowns its data directory fails on Apple
// `container` when that directory is a bind mount (host-owned, not chownable). The
// crash report (OPSM-401/407) decodes the "chown … Operation not permitted"
// signature into a hint to use a named volume — but only when the service actually
// bind-mounts a host path.

import (
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

const chownLog = "[Entrypoint] started.\nchown: /var/lib/mysql: Operation not permitted"

func TestCrashHintBindMountChown(t *testing.T) {
	svc := &compose.Service{Volumes: []string{"/host/mysql:/var/lib/mysql"}}
	h := crashHint(chownLog, svc)
	if !strings.Contains(h, "bind mount") || !strings.Contains(h, "named volume") {
		t.Errorf("a chown failure on a bind mount should hint at a named volume, got: %q", h)
	}
}

func TestCrashHintNamedVolumeNoHint(t *testing.T) {
	// Same chown log, but the data dir is a named volume (not a host path) — that's
	// chownable, so this isn't the bind-mount gotcha; no hint.
	svc := &compose.Service{Volumes: []string{"dbdata:/var/lib/mysql"}}
	if h := crashHint(chownLog, svc); h != "" {
		t.Errorf("a named-volume mount must not get the bind-mount hint, got: %q", h)
	}
}

func TestCrashHintUnrelatedCrashNoHint(t *testing.T) {
	svc := &compose.Service{Volumes: []string{"/host/mysql:/var/lib/mysql"}}
	if h := crashHint("panic: config invalid\nexit 1", svc); h != "" {
		t.Errorf("an unrelated crash must not get the chown hint, got: %q", h)
	}
}

func TestCrashHintNilService(t *testing.T) {
	if h := crashHint(chownLog, nil); h != "" {
		t.Errorf("a nil service must yield no hint, got: %q", h)
	}
}

func TestCrashHintNoVolumes(t *testing.T) {
	// A chown crash on a service with no mounts at all — nothing to point at.
	if h := crashHint(chownLog, &compose.Service{}); h != "" {
		t.Errorf("a service with no volumes must yield no hint, got: %q", h)
	}
}
