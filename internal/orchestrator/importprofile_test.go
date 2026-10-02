package orchestrator_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// Import reads the project's profiles the way Build and Pull do (#1433): a build
// service behind a profile that is not active is left alone unless it is named,
// and one whose profile is active (or `*`) is imported like any other. Before,
// Import took every build service `resolveServices` returned and tried to bring
// its image over, so a project's `debug` service the user never asked for was
// imported too — and an image Docker never built for it failed the whole command.
//
// `import` has no docker compose counterpart (docker has no such command), so
// the reference is opossum's own `build` and `pull`, which measured docker
// compose v5.5.1: only the active services, and naming one activates it.
func TestImportFollowsTheProfilesTheWayBuildAndPullDo(t *testing.T) {
	docker := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nprintf archive\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	newProject := func() *compose.Project {
		return project("pj", map[string]*compose.Service{
			"web":   {Build: &compose.Build{Context: "."}},
			"debug": {Build: &compose.Build{Context: "."}, Profiles: []string{"dbg"}},
			"other": {Build: &compose.Build{Context: "."}, Profiles: []string{"oth"}},
		})
	}
	for _, tc := range []struct {
		name     string
		profiles []string
		named    []string
		want     []string // services imported
		notWant  []string
	}{
		{"nothing named, no profile", nil, nil, []string{"web"}, []string{"debug", "other"}},
		{"a profile that names the service", []string{"dbg"}, nil, []string{"web", "debug"}, []string{"other"}},
		{"every profile", []string{"*"}, nil, []string{"web", "debug", "other"}, nil},
		// Naming a service reads only that service (resolveServices adds no
		// dependency to a named run), so `enabled` is true for it whatever its
		// profile: these two rows hold the named path, and the one below all
		// that the `named` map gives `enabled` — not the guard itself.
		{"naming the gated service enables it", nil, []string{"debug"}, []string{"debug"}, []string{"web", "other"}},
		{"naming an ungated service leaves the gated ones out", nil, []string{"web"}, []string{"web"}, []string{"debug", "other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, calls := fakeShim(t)
			rt.DockerBin = docker
			var out bytes.Buffer
			o := orchestrator.New(newProject(), rt, "opossum", &out)
			if tc.profiles != nil {
				o.EnableProfiles(tc.profiles)
			}
			if err := o.Import(tc.named...); err != nil {
				t.Fatalf("Import: %v", err)
			}
			// What was imported is what the runtime was asked to load, not only what
			// was printed: an image brought over without a line saying so would pass
			// the text checks below.
			loads := 0
			for _, c := range calls() {
				if strings.Contains(c, "image load") {
					loads++
				}
			}
			if loads != len(tc.want) {
				t.Errorf("want %d `image load` call(s) for %v, the runtime saw %d:\n%s", len(tc.want), tc.want, loads, strings.Join(calls(), "\n"))
			}
			s := out.String()
			for _, n := range tc.want {
				if !strings.Contains(s, "Importing "+n+" from Docker") {
					t.Errorf("%s should be imported; got: %s", n, s)
				}
			}
			for _, n := range tc.notWant {
				if strings.Contains(s, "Importing "+n+" ") {
					t.Errorf("%s should be left alone; got: %s", n, s)
				}
			}
		})
	}
}

// With every build service behind an inactive profile there is nothing to
// import, and it says so rather than staying silent.
func TestImportWithEveryBuildServiceGatedSaysThereIsNothingToImport(t *testing.T) {
	rt, calls := fakeShim(t)
	// The same fake docker as above, so that a guard that is gone fails on what the
	// runtime was asked to do here and not on whatever the host's docker holds.
	docker := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nprintf archive\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt.DockerBin = docker
	p := project("pj", map[string]*compose.Service{
		"debug": {Build: &compose.Build{Context: "."}, Profiles: []string{"dbg"}},
	})
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Import(); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !strings.Contains(out.String(), "No build services to import.") {
		t.Errorf("want the nothing-to-import line; got: %s", out.String())
	}
	for _, c := range calls() {
		if strings.Contains(c, "image load") {
			t.Errorf("nothing should be loaded, the runtime saw: %s", c)
		}
	}
}
