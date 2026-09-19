package orchestrator_test

// Evals for #1005: a one-off whose dependencies it would start refuses one
// behind a profile that is not active — `run` did, and `run --audit` started it
// anyway, because the `up` it hands the dependencies to names them. docker
// compose v5.5.0 refuses such a `run` too ("depends on undefined service").

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/workspace"
)

// startedNames lists the --name of every `run` invocation, in order.
func startedNames(lines []string) []string {
	var names []string
	for _, l := range lines {
		if !strings.HasPrefix(l, "run ") {
			continue
		}
		f := strings.Fields(l)
		for i := range f[:len(f)-1] {
			if f[i] == "--name" {
				names = append(names, f[i+1])
			}
		}
	}
	return names
}

func TestAOneOffRefusesADependencyBehindAnInactiveProfile(t *testing.T) {
	const refusal = `service "web" depends on "db", whose profile is not active — enable its profile beside the ones this run has active: with another --profile in a run that has one, or in COMPOSE_PROFILES in a run with no --profile — where this run reads it, since a COMPOSE_PROFILES in the shell replaces one in the .env, as the flag replaces both (none of them add up)`
	for _, shape := range []struct {
		name     string
		services func() map[string]*compose.Service
		profiles []string
		refused  bool   // with dependencies started
		started  string // the `run` names when it goes ahead with dependencies
	}{
		{"the dependency behind an inactive profile", func() map[string]*compose.Service {
			return map[string]*compose.Service{
				"db":  {Image: "alpine:3.20", Profiles: []string{"debug"}},
				"web": {Image: "alpine:3.20", WorkingDir: "/work", Volumes: compose.Volumes{"./work:/work"}, DependsOn: compose.DependsOn{{Name: "db"}}},
			}
		}, nil, true, ""},
		{"the second of two dependencies behind an inactive profile", func() map[string]*compose.Service {
			return map[string]*compose.Service{
				"api": {Image: "alpine:3.20"},
				"db":  {Image: "alpine:3.20", Profiles: []string{"debug"}},
				"web": {Image: "alpine:3.20", WorkingDir: "/work", Volumes: compose.Volumes{"./work:/work"}, DependsOn: compose.DependsOn{{Name: "api"}, {Name: "db"}}},
			}
		}, nil, true, ""},
		{"the first of two dependencies behind an inactive profile", func() map[string]*compose.Service {
			return map[string]*compose.Service{
				"api": {Image: "alpine:3.20"},
				"db":  {Image: "alpine:3.20", Profiles: []string{"debug"}},
				"web": {Image: "alpine:3.20", WorkingDir: "/work", Volumes: compose.Volumes{"./work:/work"}, DependsOn: compose.DependsOn{{Name: "db"}, {Name: "api"}}},
			}
		}, nil, true, ""},
		{"the dependency's profile active", func() map[string]*compose.Service {
			return map[string]*compose.Service{
				"db":  {Image: "alpine:3.20", Profiles: []string{"debug"}},
				"web": {Image: "alpine:3.20", WorkingDir: "/work", Volumes: compose.Volumes{"./work:/work"}, DependsOn: compose.DependsOn{{Name: "db"}}},
			}
		}, []string{"debug"}, false, "db.demo.opossum web-run.demo.opossum"},
		{"no profiles", func() map[string]*compose.Service {
			return map[string]*compose.Service{
				"db":  {Image: "alpine:3.20"},
				"web": {Image: "alpine:3.20", WorkingDir: "/work", Volumes: compose.Volumes{"./work:/work"}, DependsOn: compose.DependsOn{{Name: "db"}}},
			}
		}, nil, false, "db.demo.opossum web-run.demo.opossum"},
	} {
		for _, path := range []string{"run", "run --audit", "run --no-deps", "run --audit --no-deps"} {
			t.Run(shape.name+"/"+path, func(t *testing.T) {
				base := t.TempDir()
				if err := os.Mkdir(filepath.Join(base, "work"), 0o755); err != nil {
					t.Fatal(err)
				}
				p := project("demo", shape.services())
				p.BaseDir = base
				rt, log := fakeShim(t)
				o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
				o.EnableProfiles(shape.profiles)
				var err error
				noDeps := strings.HasSuffix(path, "--no-deps")
				opts := orchestrator.RunOneOffOptions{NoDeps: noDeps}
				if strings.HasPrefix(path, "run --audit") {
					_, err = o.RunAudited("web", []string{"true"}, opts)
				} else {
					err = o.RunOneOff("web", []string{"true"}, opts)
				}
				started := strings.Join(startedNames(log()), " ")
				// Snapshots are kept beside the workspace, not inside it.
				_, statErr := os.Stat(filepath.Join(base, workspace.SnapshotDirName))
				snapshotted := statErr == nil
				switch {
				case noDeps && shape.refused:
					// --no-deps does not change how the project is read: docker
					// compose v5.5.1 refuses this one too (measured 2026-09-16:
					// `run --rm --no-deps web` on web -> db[profiles: x] gives
					// `service "web" depends on undefined service "db"`).
					if err == nil || err.Error() != refusal {
						t.Errorf("\n got %v\nwant %s", err, refusal)
					}
					if started != "" {
						t.Errorf("want nothing started before the refusal, got %q", started)
					}
				case noDeps:
					if err != nil || started != "web-run.demo.opossum" {
						t.Errorf("want only the one-off started, got err %v and %q", err, started)
					}
				case shape.refused:
					if err == nil || err.Error() != refusal {
						t.Errorf("\n got %v\nwant %s", err, refusal)
					}
					if started != "" || indexOf(log(), "network create") >= 0 {
						t.Errorf("want nothing created or started before the refusal, got %v", log())
					}
					if snapshotted {
						t.Errorf("want no workspace snapshot before the refusal")
					}
				default:
					if err != nil || started != shape.started {
						t.Errorf("want %q started, got err %v and %q", shape.started, err, started)
					}
				}
				// The audited run that goes ahead snapshots the workspace: the
				// fixture reaches the step the refusal has to come before.
				if strings.HasPrefix(path, "run --audit") && err == nil && !snapshotted {
					t.Errorf("want the audited run to have snapshotted the workspace, it did not")
				}
			})
		}
	}
}

// The check looks at the dependencies the one-off names directly. A dependency
// of a dependency behind an inactive profile is refused by the `up` that starts
// the dependencies, before it starts anything — for `run --audit` that is after
// the workspace snapshot, as for the other refusals of that `up` (#660).
func TestADependencysDependencyBehindAnInactiveProfileIsRefusedByItsUp(t *testing.T) {
	// Refused by the run's own read of the project, before the dependencies
	// start — where it used to come back from their `up`, wearing its prefix.
	const refusal = `service "mid" depends on "db", whose profile is not active`
	for _, path := range []string{"run", "run --audit"} {
		t.Run(path, func(t *testing.T) {
			rt, log := fakeShim(t)
			p := project("demo", map[string]*compose.Service{
				"db":  {Image: "alpine:3.20", Profiles: []string{"debug"}},
				"mid": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "db"}}},
				"web": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "mid"}}},
			})
			o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
			var err error
			if path == "run" {
				err = o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{})
			} else {
				_, err = o.RunAudited("web", []string{"true"}, orchestrator.RunOneOffOptions{})
			}
			if err == nil || !strings.HasPrefix(err.Error(), refusal) {
				t.Errorf("\n got %v\nwant it to start %q", err, refusal)
			}
			if started := startedNames(log()); len(started) != 0 {
				t.Errorf("want nothing started, got %v", started)
			}
		})
	}
}

// A service that depends on itself is a cycle, refused by the `up` that starts
// the dependencies, whatever its profile: the one-off is the named service, so
// its own profile is not the reason given.
func TestASelfDependencyIsRefusedAsACycleNotForItsProfile(t *testing.T) {
	for _, path := range []string{"run", "run --audit"} {
		t.Run(path, func(t *testing.T) {
			rt, log := fakeShim(t)
			p := project("demo", map[string]*compose.Service{
				"web": {Image: "alpine:3.20", Profiles: []string{"debug"}, DependsOn: compose.DependsOn{{Name: "web"}}},
			})
			o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
			var err error
			if path == "run" {
				err = o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{})
			} else {
				_, err = o.RunAudited("web", []string{"true"}, orchestrator.RunOneOffOptions{})
			}
			if err == nil || !strings.Contains(err.Error(), "dependency cycle") || strings.Contains(err.Error(), "profile") {
				t.Errorf("want a cycle refusal that does not blame the profile, got %v", err)
			}
			if started := startedNames(log()); len(started) != 0 {
				t.Errorf("want nothing started, got %v", started)
			}
		})
	}
}
