package orchestrator_test

// #1093: a dependency cycle among ACTIVE services (no profile gates it) no
// longer stops the commands that only touch containers already there — they
// do not start anything in dependency order, so a cycle in the file must not
// leave a project they could otherwise act on stuck running with no way back
// down through opossum. docker compose does not refuse for its own read-only
// and teardown commands either, given `-p` and left to discover the file
// (measured on v5.5.1: `ps`, `images`, `logs`, `stop`, `kill`, `down`,
// `restart`, `stats` all go through). `up`, `run` and `config --services` —
// which start something or build the order itself — still refuse, docker
// compose included (measured, all 4 combinations of `-p`/`-f`).

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// cycledProject is a file with a real, active cycle (plain <-> plain2) beside
// a service the cycle does not touch (web) — the shape #1093 measured.
func cycledProject() *compose.Project {
	return project("demo", map[string]*compose.Service{
		"plain":  {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "plain2"}}},
		"plain2": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "plain"}}},
		"web":    {Image: "alpine:3.20"},
	})
}

func TestACycleAmongActiveServicesNoLongerStopsCommandsThatOnlyActOnWhatIsAlreadyThere(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(o *orchestrator.Orchestrator) error
	}{
		{"ps", func(o *orchestrator.Orchestrator) error { return o.Ps(orchestrator.PsOptions{}) }},
		{"images", func(o *orchestrator.Orchestrator) error { return o.Images(orchestrator.ImagesOptions{}) }},
		{"logs", func(o *orchestrator.Orchestrator) error { return o.Logs(nil, runtime.LogsOptions{}) }},
		{"stop", func(o *orchestrator.Orchestrator) error { return o.Stop(nil) }},
		{"kill", func(o *orchestrator.Orchestrator) error { return o.Kill(nil, "TERM") }},
		{"down", func(o *orchestrator.Orchestrator) error { return o.Down(false, "", false) }},
		{"restart", func(o *orchestrator.Orchestrator) error { return o.Restart(nil) }},
		{"stats", func(o *orchestrator.Orchestrator) error {
			return o.Stats(nil, orchestrator.StatsOptions{NoStream: true})
		}},
		{"destroy", func(o *orchestrator.Orchestrator) error {
			_, err := o.DestroyPlanFor(false, false, false)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			o := orchestrator.New(cycledProject(), rt, "opossum", &bytes.Buffer{})
			if err := tc.call(o); err != nil {
				t.Errorf("must not refuse a cycle among active services, got: %v", err)
			}
		})
	}
}

// The commands that start something still refuse: opossum does not start a
// project whose file it cannot make sense of, cycle included.
func TestACycleAmongActiveServicesStillStopsCommandsThatStartSomething(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(o *orchestrator.Orchestrator) error
	}{
		{"up", func(o *orchestrator.Orchestrator) error { return o.Up(true) }},
		{"run", func(o *orchestrator.Orchestrator) error {
			return o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{})
		}},
		{"config --services (StartupOrder)", func(o *orchestrator.Orchestrator) error {
			_, err := o.StartupOrder()
			return err
		}},
		// pull and build are measured on docker compose v5.5.1 too (given -p
		// and left to discover the file): unlike logs/stop/kill/restart/stats,
		// they still refuse — a build service's image is something they bring
		// into being, close enough to starting something that the tolerant
		// reasoning above does not carry over. import has no docker compose
		// equivalent to measure against; it is refused for the same reason.
		{"pull", func(o *orchestrator.Orchestrator) error { return o.Pull(nil) }},
		{"build", func(o *orchestrator.Orchestrator) error { return o.Build(nil) }},
		{"import", func(o *orchestrator.Orchestrator) error { return o.Import() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			o := orchestrator.New(cycledProject(), rt, "opossum", &bytes.Buffer{})
			err := tc.call(o)
			if err == nil || !strings.Contains(err.Error(), "dependency cycle detected") {
				t.Errorf("want the cycle refused, got: %v", err)
			}
		})
	}
}
