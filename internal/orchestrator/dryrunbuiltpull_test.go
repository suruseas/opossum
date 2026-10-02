package orchestrator_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// withNoImageHere puts a wrapper in front of the fake: `image inspect` exits 1 as the real CLI does
// for a reference it does not have, and the rest is the fake's.
func withNoImageHere(t *testing.T, rt *runtime.Runtime) {
	t.Helper()
	wrapper := filepath.Join(t.TempDir(), "container")
	script := "#!/bin/sh\nif [ \"$1 $2\" = \"image inspect\" ]; then exit 1; fi\nexec " + fakeShimBin + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	rt.Bin = wrapper
}

// A service that writes `entrypoint: []` and no command runs its image's CMD, so the image has to
// be read, and one that is not here is pulled first. In a plan that pull is a line of its own,
// before the run; but the image of a service with `build:` is made by the build that comes
// before the run, and no registry has it, so the plan must not list a pull of it (#1634).
// (`up --dry-run` is the command that plans; `run` has no dry run.)
func TestAPlanPullsTheImageOfAClearedEntrypointOnlyWhenNoBuildMakesIt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		svc      compose.Service
		wantPull string // the image the plan pulls, empty when it pulls none
		wantTag  string // the image the plan's run uses
	}{
		{"an image to pull", compose.Service{Image: "reg.example/neko1634-notthere:1"}, "reg.example/neko1634-notthere:1", "reg.example/neko1634-notthere:1"},
		{"an image a build makes", compose.Service{Build: &compose.Build{Context: "."}}, "", "demo-w:latest"},
		{"an image a build makes under a name of its own", compose.Service{Image: "reg.example/neko1634-mine:2", Build: &compose.Build{Context: "."}}, "", "reg.example/neko1634-mine:2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := tc.svc
			svc.EntrypointCleared = true
			rt, _ := fakeShim(t)
			withNoImageHere(t, rt)
			o := orchestrator.New(project("demo", map[string]*compose.Service{"w": &svc}), rt, "opossum", &bytes.Buffer{})
			o.SetDryRun(true)
			if err := o.Up(true); err != nil {
				t.Fatalf("up (dry-run): %v", err)
			}
			pull, run := -1, -1
			for i, l := range rt.Plan {
				if strings.HasPrefix(l, "image pull ") {
					if pull >= 0 {
						t.Errorf("the plan pulls twice:\n%s", strings.Join(rt.Plan, "\n"))
					}
					pull = i
					if tc.wantPull == "" || l != "image pull "+tc.wantPull {
						t.Errorf("the plan has %q, want a pull of %q:\n%s", l, tc.wantPull, strings.Join(rt.Plan, "\n"))
					}
				}
				if strings.HasPrefix(l, "run ") && strings.HasSuffix(l, " "+tc.wantTag) {
					run = i
				}
			}
			if run < 0 {
				t.Fatalf("the plan has no run of %s:\n%s", tc.wantTag, strings.Join(rt.Plan, "\n"))
			}
			if tc.wantPull != "" && (pull < 0 || pull > run) {
				t.Errorf("the pull of %s should come before the run (pull at %d, run at %d):\n%s", tc.wantPull, pull, run, strings.Join(rt.Plan, "\n"))
			}
		})
	}
}

// Outside a plan the same holds: the image a build here made is never pulled to read its CMD (a
// registry may hold another image of that name), and one whose CMD cannot be read is told as it
// is. A registry image that is not here is pulled, as before. `up` and `run` both.
func TestTheImageOfAClearedEntrypointIsPulledToReadItOnlyWhenNoBuildMadeIt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		svc      compose.Service
		wantPull bool
	}{
		{"a registry image", compose.Service{Image: "reg.example/neko1634-notthere:1"}, true},
		{"an image a build makes", compose.Service{Build: &compose.Build{Context: "."}}, false},
		{"an image a build makes under a name of its own", compose.Service{Image: "reg.example/neko1634-mine:2", Build: &compose.Build{Context: "."}}, false},
	} {
		for _, via := range []string{"up", "run"} {
			t.Run(tc.name+"/"+via, func(t *testing.T) {
				svc := tc.svc
				svc.EntrypointCleared = true
				rt, log := fakeShim(t)
				withNoImageHere(t, rt)
				o := orchestrator.New(project("demo", map[string]*compose.Service{"w": &svc}), rt, "opossum", &bytes.Buffer{})
				var err error
				if via == "up" {
					err = o.Up(true)
				} else {
					err = o.RunOneOff("w", nil, orchestrator.RunOneOffOptions{Rm: true, NoDeps: true})
				}
				if err == nil || !strings.Contains(err.Error(), "has no command that can be read") {
					t.Errorf("%s with an image whose CMD cannot be read: want the refusal, got: %v", via, err)
				}
				pulled := false
				for _, l := range log() {
					if strings.HasPrefix(l, "image pull ") {
						pulled = true
					}
				}
				if pulled != tc.wantPull {
					t.Errorf("%s pulled the image = %v, want %v:\n%s", via, pulled, tc.wantPull, strings.Join(log(), "\n"))
				}
			})
		}
	}
}
