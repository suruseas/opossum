package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// Apple container attaches a named volume to only one running container, so `up`
// warns when two services share one (but not for bind mounts or single use).
func TestWarnsSharedNamedVolume(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("pj", map[string]*compose.Service{
		"app":   {Image: "app:latest", Volumes: []string{"shared:/data", "apponly:/x"}},
		"nginx": {Image: "nginx:latest", Volumes: []string{"shared:/data", "./local:/y"}},
		"db":    {Image: "db:latest", Volumes: []string{"dbdata:/var/lib"}},
	})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, `share named volume "shared"`) {
		t.Errorf("expected a shared-named-volume warning; got: %s", s)
	}
	if !strings.Contains(s, `"app"`) || !strings.Contains(s, `"nginx"`) {
		t.Errorf("warning should name both sharers; got: %s", s)
	}
	// Exactly one warning: single-use named volumes (apponly, dbdata) and the bind
	// mount must not trigger one.
	if n := strings.Count(s, "share named volume"); n != 1 {
		t.Errorf("expected exactly one shared-volume warning, got %d; output: %s", n, s)
	}
}

// An init one-shot runs to completion and frees the volume before its dependent
// starts, so sharing a named volume with it is fine — no warning.
func TestNoWarnSharingWithOneShot(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("pj", map[string]*compose.Service{
		"init": {Image: "init:latest", Volumes: []string{"shared:/data"}},
		"app": {Image: "app:latest", Volumes: []string{"shared:/data"},
			DependsOn: compose.DependsOn{{Name: "init", Condition: compose.ConditionCompleted}}},
	})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if strings.Contains(out.String(), "share named volume") {
		t.Errorf("a one-shot sharing a named volume should not warn; got: %s", out.String())
	}
}
