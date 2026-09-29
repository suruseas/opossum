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

// TestRunOneOffExcludesItsOwnContainerNotTheServicesUpContainer guards the
// excludeContainer plumbing added for #1381: the one-off's own container name
// ("app-run") must never be named as a holder (it is the one-off's
// own, cleared and about to be replaced), while the service's separate `up`
// container ("app") — a real, distinct holder — must still be
// named. Passing the wrong exclude (the `up` name, as `specificStartError`'s
// other callers do) would swap these two outcomes.
func TestRunOneOffExcludesItsOwnContainerNotTheServicesUpContainer(t *testing.T) {
	failVZ := `echo 'Error Domain=VZErrorDomain Code=2 "The storage device attachment is invalid."' >&2; exit 1`
	for _, tc := range []struct {
		name   string
		holder string
		want   string
		wantNo string
	}{
		{name: "the service's own up container is a real, distinct holder",
			holder: "app", want: `running container "app"`},
		{name: "the one-off's own container is never a holder of itself",
			holder: "app-run", wantNo: "app-run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shim := runShim(t, oneRunning(tc.holder, "demo_data"), failVZ)
			err := New(volProject(), shim, "", &bytes.Buffer{}).RunOneOff("app", nil, RunOneOffOptions{NoDeps: true})
			if err == nil {
				t.Fatal("expected the one-off to fail")
			}
			s := err.Error()
			if tc.want != "" && !strings.Contains(s, tc.want) {
				t.Errorf("want the holder named, missing %q: %s", tc.want, s)
			}
			if tc.wantNo != "" && strings.Contains(s, tc.wantNo) {
				t.Errorf("the one-off's own container must not be named as its own holder: %s", s)
			}
		})
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

// TestRunOneOffNeverClaimsItRolledBackItsOwnContainer guards #1102's remaining
// branch: a one-off whose run is refused before the runtime makes a container
// (measured on 1.4.1 for a platform the image has no build for, and for the
// exclusive-attach volume conflict — neither leaves a container behind) must
// never say the container was "Rolled back" or "stopped and removed", the
// wording `up`'s rollback used to print regardless of whether one existed.
// RunOneOff has no rollback of its own container (unlike `up`, it passes the
// run's failure straight through, decoding only the exclusive-attach case), so
// this is a straight regression guard against that wording creeping back in.
func TestRunOneOffNeverClaimsItRolledBackItsOwnContainer(t *testing.T) {
	shim := runShim(t, "[]",
		`echo 'Error: unsupported platform Platform(osVersion: nil, osFeatures: nil, variant: nil, _rawOS: "linux", _rawArch: "arm64")' >&2; exit 1`)
	err := New(volProject(), shim, "", &bytes.Buffer{}).RunOneOff("app", nil, RunOneOffOptions{NoDeps: true})
	if err == nil {
		t.Fatal("expected the one-off to fail")
	}
	if s := err.Error(); strings.Contains(s, "Rolled back") || strings.Contains(s, "stopped and removed") {
		t.Errorf("a one-off that never made a container must not claim to have rolled it back, got: %s", s)
	}
}

// TestRunOneOffDoesNotDecodeVZErrorWhenAContainerWasMade guards the narrowing
// #1381 added: OPSM-103 used to decode unconditionally, but a run that made a
// container is never detached, so a job that happened to print the same VZError
// words and then exited must not be misread as the runtime's own exclusive-attach
// refusal — the same reason the coded runErrorHint failures are gated on
// madeNothing. On container 1.4.1 an exclusive-attach refusal itself never
// leaves a container (see runMadeNothing's doc), so this is a hypothetical the
// gate defends against, not a form measured to happen — this test pins the
// choice, made == not decoded, regardless.
func TestRunOneOffDoesNotDecodeVZErrorWhenAContainerWasMade(t *testing.T) {
	shim := scriptShim(t, ""+
		"  system) echo 'status running' ;;\n"+
		"  volume) echo 'demo_data' ;;\n"+
		"  ls) echo '"+oneRunning("otherapp", "demo_data")+"' ;;\n"+
		"  run) echo 'Error Domain=VZErrorDomain Code=2 \"The storage device attachment is invalid.\"' >&2; exit 1 ;;\n"+
		// The one-off's own container answers "there" — standing for a
		// container this run made and left behind.
		`  inspect) echo '[{"status":{"state":"running"},"configuration":{"id":"app-run","labels":{"opossum.project":"demo"}}}]' ;;`+"\n")
	err := New(volProject(), shim, "", &bytes.Buffer{}).RunOneOff("app", nil, RunOneOffOptions{NoDeps: true})
	if err == nil {
		t.Fatal("expected the one-off to fail")
	}
	if s := err.Error(); strings.Contains(s, "OPSM-103") {
		t.Errorf("a container was made, so OPSM-103 is not the runtime's to say, got: %s", s)
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
