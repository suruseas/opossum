package main

import (
	"strings"
	"testing"
)

// Every command that reads the project without a more specific check of its
// own — everything but `up` and `run` (each enables the services it names
// first, then asks in its own words, via Orchestrator.checkProjectLoads) —
// now asks the same question `config` always has (loadOrchestratorChecked):
// an enabled service's depends_on naming no service anywhere, one behind
// another inactive profile, or (via volumes_from, #1156) a service or a
// container: entry outside the file, is a fault only once something makes
// the dependent itself active (#1094). Closed, a broken gated service is
// nobody's business; opened, it is refused before the runtime is asked
// anything. `down`, `destroy`, `stop` and `kill` are commands that take a
// project down and use a different, more lenient loader of their own
// (loadOrchestratorToTakeDown) — not covered here; see gatedgraph_test.go
// and docs/compatibility.md's `depends_on` row for how far that one goes.
func TestEveryPlainReaderRefusesAGatedServicesDependencyFault(t *testing.T) {
	const undefined = `
name: demo
services:
  web:
    image: alpine:3.20
  broken:
    image: alpine:3.20
    profiles: [g]
    depends_on: [nosuch]
`
	const inactiveProfile = `
name: demo
services:
  web:
    image: alpine:3.20
  broken:
    image: alpine:3.20
    profiles: [g]
    depends_on: [gy]
  gy:
    image: alpine:3.20
    profiles: [y]
`
	const undefinedVolumesFrom = `
name: demo
services:
  web:
    image: alpine:3.20
  broken:
    image: alpine:3.20
    profiles: [g]
    volumes_from: [nosuch]
`
	const containerVolumesFrom = `
name: demo
services:
  web:
    image: alpine:3.20
  broken:
    image: alpine:3.20
    profiles: [g]
    volumes_from: ['container:other']
`
	commands := []struct {
		name string
		args []string
	}{
		{"logs", []string{"logs", "web"}},
		{"ps", []string{"ps"}},
		{"images", []string{"images"}},
		{"port", []string{"port", "web", "80"}},
		{"volumes", []string{"volumes"}},
		{"exec", []string{"exec", "web", "true"}},
		{"restart", []string{"restart", "web"}},
		{"stats", []string{"stats", "--no-stream"}},
		{"cp", []string{"cp", "web:/x", "."}},
		{"build (via servicesCmd)", []string{"build", "web"}},
		{"watch", []string{"watch"}},
	}
	for _, fx := range []struct {
		name    string
		body    string
		refusal string
	}{
		{"an undefined dependency", undefined, `service "broken" depends on unknown service "nosuch"`},
		{"a dependency behind another inactive profile", inactiveProfile, `service "broken" depends on "gy", whose profile is not active`},
		{"an undefined volumes_from holder", undefinedVolumesFrom, `service "broken" depends on undefined service "nosuch"`},
		{"a volumes_from container: entry", containerVolumesFrom, `service "broken": volumes_from "container:other" names a container outside this compose file`},
	} {
		for _, tc := range commands {
			t.Run(fx.name+"/"+tc.name, func(t *testing.T) {
				fakeShim(t)
				c := writeCompose(t, fx.body)

				// Closed: `broken` is inactive, so its dependency fault is
				// nobody's business here — whatever this command does about
				// `web` (or fails to, against the fake runtime), it must not
				// be this refusal.
				out, err := run(t, append([]string{"-f", c}, tc.args...)...)
				if err != nil && strings.Contains(err.Error(), fx.refusal) {
					t.Errorf("profile closed: want no refusal over broken's dependency, got %v\n%s", err, out)
				}

				// Open: `broken` is now active, and its dependency fault is
				// refused before anything else runs. `--profile` comes
				// before the subcommand: exec's own flag parsing stops
				// interspersing after the service name, so a flag placed
				// after it would be handed to the executed command instead
				// of read as opossum's own.
				out, err = run(t, append([]string{"-f", c, "--profile", "g"}, tc.args...)...)
				if err == nil || !strings.Contains(err.Error(), fx.refusal) {
					t.Errorf("profile open: want %q refused, got %v\n%s", fx.refusal, err, out)
				}
			})
		}
	}
}
