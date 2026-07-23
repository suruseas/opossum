package orchestrator

// Evals for #276: `opossum run` (a foreground one-off) now gets the same
// exclusive-attach (VZError/OPSM-103) treatment as `up` — a pre-flight warning
// before it runs and a decode of the failure — WITHOUT disturbing the normal
// exit-code passthrough of a one-off whose command simply exits non-zero.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	rt "github.com/suruseas/opossum/internal/runtime"
)

func volProject() *compose.Project {
	return &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"app": {Name: "app", Image: "app:latest", Volumes: []string{"data:/x"}},
	}}
}

// runShim builds a one-off shim: runtime running, the target's volume already
// exists (skip seeding), `ls` reports holders, and `run` behaves as runBody says.
func runShim(t *testing.T, lsJSON, runBody string) *rt.Runtime {
	t.Helper()
	return scriptShim(t, ""+
		"  system) echo 'status running' ;;\n"+
		"  volume) echo 'demo_data' ;;\n"+
		"  ls) echo '"+lsJSON+"' ;;\n"+
		"  run) "+runBody+" ;;\n")
}

func TestRunOneOffDecodesVZError(t *testing.T) {
	shim := runShim(t, oneRunning("otherapp", "demo_data"),
		`echo 'Error Domain=VZErrorDomain Code=2 "The storage device attachment is invalid."' >&2; exit 1`)
	err := New(volProject(), shim, "", &bytes.Buffer{}).RunOneOff("app", nil, RunOneOffOptions{NoDeps: true})
	if err == nil {
		t.Fatal("expected the one-off to fail")
	}
	s := err.Error()
	for _, want := range []string{"[OPSM-103]", `"demo_data"`, `"otherapp"`} {
		if !strings.Contains(s, want) {
			t.Errorf("run one-off VZError should decode to OPSM-103 naming volume+holder, missing %q: %s", want, s)
		}
	}
}

func TestRunOneOffPreflightWarnsBusyVolume(t *testing.T) {
	// A holder exists but the run (contrived) succeeds — isolates the pre-flight
	// warning, which must fire before the run.
	shim := runShim(t, oneRunning("otherapp", "demo_data"), "exit 0")
	var out bytes.Buffer
	if err := New(volProject(), shim, "", &out).RunOneOff("app", nil, RunOneOffOptions{NoDeps: true}); err != nil {
		t.Fatalf("run should succeed: %v", err)
	}
	if s := out.String(); !strings.Contains(s, "[OPSM-103]") || !strings.Contains(s, `"otherapp"`) {
		t.Errorf("one-off pre-flight should warn about the busy volume, got: %s", s)
	}
}

func TestRunOneOffPassesThroughExitCode(t *testing.T) {
	// A one-off whose command exits non-zero (no VZError) must pass through raw —
	// not decoded to OPSM-103, not wrapped with the generic start-failed hint — so
	// `run` keeps propagating exit codes. Use a service WITH a named volume (and even
	// a live holder), so a too-loose storage matcher would misdecode a plain exit.
	shim := runShim(t, oneRunning("otherapp", "demo_data"),
		"echo 'command failed with code 3' >&2; exit 3")
	err := New(volProject(), shim, "", &bytes.Buffer{}).RunOneOff("app", nil, RunOneOffOptions{NoDeps: true})
	if err == nil {
		t.Fatal("expected the one-off to fail")
	}
	if s := err.Error(); strings.Contains(s, "OPSM-103") || strings.Contains(s, "opossum logs") {
		t.Errorf("a normal one-off failure must pass through untouched, got: %s", s)
	}
	// The child's exit code must survive (exit-code propagation is the whole point).
	if code := exitCode(err); code != 3 {
		t.Errorf("the one-off's exit code should propagate, exitCode = %d, want 3", code)
	}
}
