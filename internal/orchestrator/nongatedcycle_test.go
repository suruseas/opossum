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
		// `stats --host` has no docker compose counterpart. Its grounds are `docker compose
		// stats` (goes on through such a cycle, measured on v5.5.1) and opossum's rule that a
		// command that only reads what is already there goes on (#1430).
		{"stats --host", func(o *orchestrator.Orchestrator) error { return o.StatsHost(nil) }},
		// Named, `stats --host` never read the cycle (both resolvers go the same way with a
		// name): this row pins what already held, and is not what the change is about.
		{"stats --host, a service named", func(o *orchestrator.Orchestrator) error {
			return o.StatsHost([]string{"web"})
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

// `stats --host` goes on through a cycle AND prints the table (#1430): every service once, in
// a fixed order. A run that went on and printed nothing would pass the row above (which reads
// only the error), so this one reads what was printed.
func TestStatsHostPrintsEveryServiceOnceThroughACycle(t *testing.T) {
	rt, _ := fakeShim(t)
	var out bytes.Buffer
	o := orchestrator.New(cycledProject(), rt, "opossum", &out)
	if err := o.StatsHost(nil); err != nil {
		t.Fatalf("stats --host through a cycle: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "SERVICE") || !strings.Contains(got, "HOST FOOTPRINT") {
		t.Errorf("want the table's header, got:\n%s", got)
	}
	for _, name := range []string{"plain", "plain2", "web"} {
		rows := 0
		for _, line := range strings.Split(got, "\n") {
			if f := strings.Fields(line); len(f) > 0 && f[0] == name {
				rows++
			}
		}
		if rows != 1 {
			t.Errorf("service %s has %d rows, want 1:\n%s", name, rows, got)
		}
	}
}
