package orchestrator

import (
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

func TestAnonVolumeNameStableAndCollisionResistant(t *testing.T) {
	o := &Orchestrator{Project: &compose.Project{Name: "demo"}}

	// Deterministic: the same service+path yields the same name, so a re-up
	// reuses (rather than orphans) the volume.
	if a, b := o.anonVolumeName("web", "/app/node_modules"), o.anonVolumeName("web", "/app/node_modules"); a != b {
		t.Errorf("anonVolumeName must be deterministic: %q != %q", a, b)
	}
	// Paths that sanitize to the same string stay distinct via the hash suffix.
	if a, b := o.anonVolumeName("web", "/a/b"), o.anonVolumeName("web", "/a.b"); a == b {
		t.Errorf("/a/b and /a.b must not collide, both %q", a)
	}
	// Different services don't collide on the same path.
	if a, b := o.anonVolumeName("web", "/x"), o.anonVolumeName("api", "/x"); a == b {
		t.Errorf("different services must not collide, both %q", a)
	}
	// The name is project-namespaced (so `down -v` scoping and multi-project
	// isolation hold).
	if got := o.anonVolumeName("web", "/data"); !strings.HasPrefix(got, "demo_web_") {
		t.Errorf("anon volume name should be project+service namespaced, got %q", got)
	}
}
