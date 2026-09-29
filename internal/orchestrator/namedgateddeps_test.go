package orchestrator_test

// #1419: naming a gated-inactive service is a way to enable it for `up` and
// `run` — and, since this fix, for `build`, `pull` and `import` too — so its
// own undefined or gated-inactive dependency becomes this run's business the
// same way an `up` naming it would refuse it. Before this fix, these three
// went through loadOrchestratorChecked, which never learned which services
// were named, so a gated service's bad dependency went unnoticed until (for
// build and pull) it happened to also be unreachable some other way.
//
// Measured on docker compose v5.5.1: naming `broken` (profiles: [g], not
// active) for `pull`/`build` refuses "no such service: nosuch" whether or not
// a container already exists for it — state-independent, unlike `restart`,
// `logs`, `stop` and `kill`, which behave differently once a container exists
// (a separate, larger question left to a follow-up, #1431).
//
// checkProjectLoads also refuses a dependency cycle among the services it
// reads — and, called with the named service, that now includes a cycle
// among services this run always read anyway (they carry no `profiles:`),
// even when the named service itself sits outside it: naming used to skip
// the cycle check entirely (resolveServiceNames, which the unnamed case did
// not go through), and now it does not (independent review, PR #1432).

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

func gatedUndefinedDepProject() *compose.Project {
	return project("demo", map[string]*compose.Service{
		"web": {Image: "alpine:3.20"},
		"broken": {
			Image:     "alpine:3.20",
			Profiles:  []string{"g"},
			Build:     &compose.Build{Context: "."},
			DependsOn: compose.DependsOn{{Name: "nosuch"}},
		},
	})
}

func gatedInactiveDepProject() *compose.Project {
	return project("demo", map[string]*compose.Service{
		"web":   {Image: "alpine:3.20"},
		"other": {Image: "alpine:3.20", Profiles: []string{"h"}},
		"broken": {
			Image:     "alpine:3.20",
			Profiles:  []string{"g"},
			DependsOn: compose.DependsOn{{Name: "other"}},
		},
	})
}

func TestNamingAGatedServiceMakesBuildPullImportSeeItsUndefinedDependency(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(o *orchestrator.Orchestrator) error
	}{
		{"build", func(o *orchestrator.Orchestrator) error { return o.Build([]string{"broken"}) }},
		{"pull", func(o *orchestrator.Orchestrator) error { return o.Pull([]string{"broken"}) }},
		{"import", func(o *orchestrator.Orchestrator) error { return o.Import("broken") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			o := orchestrator.New(gatedUndefinedDepProject(), rt, "opossum", &bytes.Buffer{})
			err := tc.call(o)
			if err == nil || !strings.Contains(err.Error(), `depends on unknown service "nosuch"`) {
				t.Errorf("want the undefined dependency refused once broken is named, got: %v", err)
			}
			// The refusal is checked before anything runs — build/pull must not
			// have reached the runtime for broken's image over it (a moved check
			// that refuses only after acting would still pass on the error alone).
			for _, l := range log() {
				if strings.HasPrefix(l, "build ") || strings.HasPrefix(l, "image pull") {
					t.Errorf("must not act on broken before refusing its dependency, got runtime call: %q", l)
				}
			}
		})
	}
}

// The same dependency, gated behind a profile that stays inactive instead of
// undefined, is refused the same way once broken is named — and the message
// mentions naming as a way out (canName=true), since these three commands
// take service names.
func TestNamingAGatedServiceMakesBuildPullImportSeeItsGatedInactiveDependency(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(o *orchestrator.Orchestrator) error
	}{
		{"build", func(o *orchestrator.Orchestrator) error { return o.Build([]string{"broken"}) }},
		{"pull", func(o *orchestrator.Orchestrator) error { return o.Pull([]string{"broken"}) }},
		{"import", func(o *orchestrator.Orchestrator) error { return o.Import("broken") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			o := orchestrator.New(gatedInactiveDepProject(), rt, "opossum", &bytes.Buffer{})
			err := tc.call(o)
			if err == nil || !strings.Contains(err.Error(), `whose profile is not active`) || !strings.Contains(err.Error(), "name it explicitly") {
				t.Errorf("want the gated-inactive dependency refused (naming offered as a way out) once broken is named, got: %v", err)
			}
		})
	}
}

// Left unnamed, broken's profile stays inactive and its bad dependency is not
// this run's business — the same three commands go on. This fixture's broken
// has no `build:` (Import's own, pre-existing and separate gap — it does not
// call o.enabled() in its loop the way Build and Pull do — is not this
// test's business either, so it is kept out of the way here rather than
// exercised by accident).
func TestNotNamingTheGatedServiceLeavesBuildPullImportAlone(t *testing.T) {
	noBuildProject := func() *compose.Project {
		return project("demo", map[string]*compose.Service{
			"web": {Image: "alpine:3.20"},
			"broken": {
				Image:     "alpine:3.20",
				Profiles:  []string{"g"},
				DependsOn: compose.DependsOn{{Name: "nosuch"}},
			},
		})
	}
	for _, tc := range []struct {
		name string
		call func(o *orchestrator.Orchestrator) error
	}{
		{"build", func(o *orchestrator.Orchestrator) error { return o.Build(nil) }},
		{"pull", func(o *orchestrator.Orchestrator) error { return o.Pull(nil) }},
		{"import", func(o *orchestrator.Orchestrator) error { return o.Import() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			o := orchestrator.New(noBuildProject(), rt, "opossum", &bytes.Buffer{})
			if err := tc.call(o); err != nil {
				t.Errorf("must not refuse over broken's dependency while its profile is off and it is not named, got: %v", err)
			}
		})
	}
}

// Naming a service outside an active (ungated) dependency cycle used to skip
// the cycle check entirely for build/pull/import — resolveServiceNames, which
// the named path went through, never looked for one. checkProjectLoads does,
// so naming now refuses the same cycle these commands already refused when
// nothing was named (#1093 keeps them strict; independent review of #1419
// found this case untested).
func TestNamingAServiceOutsideAnActiveCycleStillRefusesBuildPullImport(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(o *orchestrator.Orchestrator) error
	}{
		{"build", func(o *orchestrator.Orchestrator) error { return o.Build([]string{"web"}) }},
		{"pull", func(o *orchestrator.Orchestrator) error { return o.Pull([]string{"web"}) }},
		{"import", func(o *orchestrator.Orchestrator) error { return o.Import("web") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			o := orchestrator.New(cycledProject(), rt, "opossum", &bytes.Buffer{})
			err := tc.call(o)
			if err == nil || !strings.Contains(err.Error(), "dependency cycle detected") {
				t.Errorf("want the cycle refused even though web is named and outside it, got: %v", err)
			}
		})
	}
}
