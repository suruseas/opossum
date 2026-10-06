package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A gated service that only its name enables is not read for its dependencies docker compose reads when something else turns it on:
// an optional (`required: false`) dependency the file does not define is left alone by `up`, `run`, `build` and `pull`, as it is by
// `stop`, `kill`, `logs` and `start` (docker compose v5.5.1, every row measured against a real engine: `refused` is its rc 1). With
// the profile on by `--profile` or COMPOSE_PROFILES it is refused whatever `required` says, as is a dependency that is required, or
// written in the list form (#1712).
func TestAnOptionalUndefinedDependencyOfAServiceOnlyItsNameEnablesIsLeftAlone(t *testing.T) {
	gated := func(deps compose.DependsOn) *compose.Service {
		return &compose.Service{Image: "alpine:3.20", Profiles: []string{"g"}, DependsOn: deps}
	}
	optional := compose.DependsOn{{Name: "nosuch", Optional: true}}
	for _, shape := range []struct {
		name     string
		services func() map[string]*compose.Service
		named    []string
	}{
		{"an optional undefined dependency", func() map[string]*compose.Service {
			return map[string]*compose.Service{"web": {Image: "alpine:3.20"}, "broken": gated(optional)}
		}, []string{"broken"}},
		{"an undefined dependency that is required", func() map[string]*compose.Service {
			return map[string]*compose.Service{"web": {Image: "alpine:3.20"}, "broken": gated(compose.DependsOn{{Name: "nosuch"}})}
		}, []string{"broken"}},
		{"an optional undefined dependency of a gated dependency", func() map[string]*compose.Service {
			return map[string]*compose.Service{
				"web":    {Image: "alpine:3.20"},
				"broken": gated(compose.DependsOn{{Name: "helper"}}),
				"helper": gated(optional),
			}
		}, []string{"broken"}},
		{"an optional undefined dependency, with a second gated service named", func() map[string]*compose.Service {
			return map[string]*compose.Service{
				"web":    {Image: "alpine:3.20"},
				"broken": gated(optional),
				"other":  {Image: "alpine:3.20", Profiles: []string{"g"}},
			}
		}, []string{"broken", "other"}},
		{"an optional undefined dependency of a service with no profiles", func() map[string]*compose.Service {
			return map[string]*compose.Service{"web": {Image: "alpine:3.20"}, "broken": {Image: "alpine:3.20", DependsOn: optional}}
		}, []string{"broken"}},
	} {
		// Which of the two the name alone makes: what docker compose measured for the shape.
		refusedByName := shape.name == "an undefined dependency that is required" || shape.name == "an optional undefined dependency of a service with no profiles"
		for _, verb := range []struct {
			name string
			call func(o *orchestrator.Orchestrator, named []string) error
		}{
			{"up", func(o *orchestrator.Orchestrator, named []string) error { return o.Up(true, named...) }},
			{"run", func(o *orchestrator.Orchestrator, named []string) error {
				return o.RunOneOff(named[0], []string{"true"}, orchestrator.RunOneOffOptions{Rm: true})
			}},
			{"build", func(o *orchestrator.Orchestrator, named []string) error { return o.Build(named) }},
			{"pull", func(o *orchestrator.Orchestrator, named []string) error { return o.Pull(named) }},
			{"import", func(o *orchestrator.Orchestrator, named []string) error { return o.Import(named...) }},
		} {
			for _, on := range [][]string{nil, {"g"}, {"*"}} {
				profileOn := on != nil
				name := shape.name + ": " + verb.name + " by name"
				refused := refusedByName
				if profileOn {
					name = shape.name + ": " + verb.name + " with the profile on by " + on[0]
					refused = true
				}
				t.Run(name, func(t *testing.T) {
					rt, _ := fakeShim(t)
					o := orchestrator.New(project("demo", shape.services()), rt, "opossum", &bytes.Buffer{})
					if profileOn {
						o.EnableProfiles(on)
					}
					err := verb.call(o, shape.named)
					if refused {
						if err == nil || !strings.Contains(err.Error(), `depends on unknown service "nosuch"`) {
							t.Errorf("want the undefined dependency refused, got: %v", err)
						}
						return
					}
					if err != nil {
						t.Errorf("the optional undefined dependency was refused where only the name enables the service: %v", err)
					}
				})
			}
		}
	}
}
