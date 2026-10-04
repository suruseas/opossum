package orchestrator_test

// #1385 (the #1094 residual), widened by #1431: naming a service for `stop`/`kill`/`logs`/`start`
// takes the name as the whole answer and builds no order (#1094's own remap), so a gated service's
// undefined or gated-inactive dependency went unnoticed when it was named this way — unlike the
// bare (no name) case, which already goes through StartupOrderTolerant's undefined-dependency
// check.
//
// docker compose v5.5.1, measured with the file read (in its directory, with `-f`, or with `-p` and `-f`
// together; given a project name by COMPOSE_PROJECT_NAME, or by `-p` with no `-f`, it does not read the file
// at all and goes on for everything): `stop broken`, `kill broken`, `logs broken` and `start broken`
// (broken gated, depends_on an undefined or
// gated-inactive service) refuse with `no such service: <dep>` whether or not broken has a
// container, running or stopped; `restart`, `exec` and `port` do not read the dependency. The
// first version of this check ran only for a named service that already had a container, on a
// reading — made partly with a project name given by `-p` alone — that docker compose went on without one;
// that does not reproduce.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

func gatedBrokenTeardownProject(dep string) *compose.Project {
	return project("demo", map[string]*compose.Service{
		"web":   {Image: "alpine:3.20"},
		"other": {Image: "alpine:3.20", Profiles: []string{"h"}},
		"broken": {
			Image:     "alpine:3.20",
			Profiles:  []string{"g"},
			DependsOn: compose.DependsOn{{Name: dep}},
		},
	})
}

// namedCommands are the commands that read a named service's own dependency, each called with the
// names it was given.
var namedCommands = []struct {
	name string
	call func(o *orchestrator.Orchestrator, names []string) error
}{
	{"stop", func(o *orchestrator.Orchestrator, names []string) error { return o.Stop(names) }},
	{"kill", func(o *orchestrator.Orchestrator, names []string) error { return o.Kill(names, "TERM") }},
	{"logs", func(o *orchestrator.Orchestrator, names []string) error { return o.Logs(names, runtime.LogsOptions{}) }},
	{"start", func(o *orchestrator.Orchestrator, names []string) error { return o.Start(names) }},
}

// refusedOverADependency says whether err is the refusal of a service's dependency — an undefined
// one or one behind a profile that is not active — and not some other failure of the command.
func refusedOverADependency(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "depends on unknown service") || strings.Contains(err.Error(), "whose profile is not active"))
}

func TestNamingAGatedServiceMakesStopKillLogsStartSeeItsBadDependency(t *testing.T) {
	for _, dep := range []struct {
		name string
		dep  string
		want string
	}{
		{"undefined", "nosuch", `depends on unknown service "nosuch"`},
		// "name it explicitly" is the canName=true half of
		// gatedDependencyRefusal — these commands take service names, so it
		// belongs in the message the same way it does for up.
		{"gated-inactive", "other", `whose profile is not active — name it explicitly`},
	} {
		for _, container := range []string{"no container", "a container"} {
			for _, tc := range namedCommands {
				t.Run(dep.name+"/"+container+"/"+tc.name, func(t *testing.T) {
					rt, _ := fakeShim(t)
					if container == "no container" {
						// The fake shim answers Inspect of a name it has not heard of with a plausible running
						// container by default; this needs the opposite.
						setShimEnv(rt, "INSPECT_ABSENT=broken.demo.opossum")
					}
					o := orchestrator.New(gatedBrokenTeardownProject(dep.dep), rt, "opossum", &bytes.Buffer{})
					if container == "a container" {
						// "<service>.<project>.<dns domain>" — the project's own name is "demo" (project() in
						// orchestrator_test.go) and the dns domain passed to New above is "opossum".
						if err := rt.Run(runtime.RunOptions{Name: "broken.demo.opossum", Image: "alpine:3.20"}); err != nil {
							t.Fatalf("seeding broken's container: %v", err)
						}
					}
					err := tc.call(o, []string{"broken"})
					if err == nil || !strings.Contains(err.Error(), dep.want) {
						t.Errorf("want %q refused (%s for it), got: %v", dep.want, container, err)
					}
				})
			}
		}
	}
}

// Naming the gated-inactive dependency too enables it, the same way naming
// enables broken itself elsewhere in the codebase (activeServices, via
// checkNamedDeps) — so stop, kill, logs and start go ahead instead of refusing.
func TestNamingBothTheGatedServiceAndItsDependencyEnablesBoth(t *testing.T) {
	for _, tc := range namedCommands {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			o := orchestrator.New(gatedBrokenTeardownProject("other"), rt, "opossum", &bytes.Buffer{})
			if err := rt.Run(runtime.RunOptions{Name: "broken.demo.opossum", Image: "alpine:3.20"}); err != nil {
				t.Fatalf("seeding broken's container: %v", err)
			}
			if err := tc.call(o, []string{"broken", "other"}); refusedOverADependency(err) {
				t.Errorf("must not refuse once other is named alongside broken, got: %v", err)
			}
		})
	}
}

// The check must not stop at the first name, the last name, or bail out of
// the loop entirely the moment one named service turns out to have no
// container (independent review of #1385: a mutation checking only the first
// or the last name, or turning the "no container, skip this one" continue
// into a break, all stayed green without this).
func TestNamingMultipleServicesStillChecksEachOne(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stop    []string
		kill    []string
		seedGap bool // true: web (no container) sits before broken in the list
	}{
		{"broken named second, healthy web first (web has no container)", nil, nil, true},
		{"broken named first, healthy web second", nil, nil, false},
	} {
		names := []string{"broken", "web"}
		if tc.seedGap {
			names = []string{"web", "broken"}
		}
		for _, call := range namedCommands {
			t.Run(tc.name+"/"+call.name, func(t *testing.T) {
				rt, _ := fakeShim(t)
				setShimEnv(rt, "INSPECT_ABSENT=web.demo.opossum")
				o := orchestrator.New(gatedBrokenTeardownProject("nosuch"), rt, "opossum", &bytes.Buffer{})
				if err := rt.Run(runtime.RunOptions{Name: "broken.demo.opossum", Image: "alpine:3.20"}); err != nil {
					t.Fatalf("seeding broken's container: %v", err)
				}
				err := call.call(o, names)
				if err == nil || !strings.Contains(err.Error(), `depends on unknown service "nosuch"`) {
					t.Errorf("want broken's dependency refused regardless of its position among %v, got: %v", names, err)
				}
			})
		}
	}
}

// An optional (`required: false`) dependency behind a profile that is not
// active is not a fault for `up` either (validateProfileDeps,
// checkProjectLoads drop it via dropOptionalGatedDeps first) — found missing
// here by independent review of #1385, which noticed checkNamedDeps
// ignored DependsOn's Optional field, so a perfectly healthy service with an
// optional gated dependency could no longer be stopped or killed by name.
func TestNamingAServiceWithAnOptionalGatedInactiveDependencyDoesNotRefuse(t *testing.T) {
	optionalDepProject := func() *compose.Project {
		return project("demo", map[string]*compose.Service{
			"web": {
				Image:     "alpine:3.20",
				DependsOn: compose.DependsOn{{Name: "dbg", Optional: true}},
			},
			"dbg": {Image: "alpine:3.20", Profiles: []string{"h"}},
		})
	}
	for _, tc := range namedCommands {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			o := orchestrator.New(optionalDepProject(), rt, "opossum", &bytes.Buffer{})
			if err := rt.Run(runtime.RunOptions{Name: "web.demo.opossum", Image: "alpine:3.20"}); err != nil {
				t.Fatalf("seeding web's container: %v", err)
			}
			if err := tc.call(o, []string{"web"}); refusedOverADependency(err) {
				t.Errorf("must not refuse over an optional gated-inactive dependency, got: %v", err)
			}
		})
	}
}

// Naming a service carries a dependency that shares its own profile (#1072,
// measured against docker compose: `run web` on web[x] -> db[x] runs) — the
// same set `up`/`checkProjectLoads` use via activeServices. `enabled` alone
// does not see this (independent review of #1385: `up web` on this exact
// layout starts db, but `stop web` afterwards refused over db "whose profile
// is not active" until checkNamedDeps used activeServices too).
func TestNamingCarriesADependencyThatSharesItsProfile(t *testing.T) {
	sharedProfileProject := func() *compose.Project {
		return project("demo", map[string]*compose.Service{
			"web": {
				Image:     "alpine:3.20",
				Profiles:  []string{"x"},
				DependsOn: compose.DependsOn{{Name: "db"}},
			},
			"db": {Image: "alpine:3.20", Profiles: []string{"x"}},
		})
	}
	for _, tc := range []struct {
		name string
		call func(o *orchestrator.Orchestrator) error
	}{
		{"stop", func(o *orchestrator.Orchestrator) error { return o.Stop([]string{"web"}) }},
		{"kill", func(o *orchestrator.Orchestrator) error { return o.Kill([]string{"web"}, "TERM") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			proj := sharedProfileProject()
			o := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})
			// Confirms the layout: `up web` genuinely starts db too, the way
			// #1072 measured docker compose doing it, before checking stop/kill.
			if err := o.Up(true, "web"); err != nil {
				t.Fatalf("up web: %v", err)
			}
			if err := tc.call(o); err != nil {
				t.Errorf("must not refuse over db, carried in under web's own profile by naming web, got: %v", err)
			}
		})
	}
}

// Naming two services at once carries each one's own profile-mate
// independently — activeServices walks each named service's dependency
// chain separately, keyed by that service's own profiles, and unions the
// results (independent review round 3 of #1385: no existing test named two
// services that each carry a dependency under a DIFFERENT shared profile at
// the same time — this checks both orders, since activeServices sorts the
// names it is given and a walk keyed to the wrong one would still pass a
// single-name test).
func TestNamingTwoServicesEachCarriesItsOwnSharedProfileDependency(t *testing.T) {
	twoCarriesProject := func() *compose.Project {
		return project("demo", map[string]*compose.Service{
			"web":   {Image: "alpine:3.20", Profiles: []string{"x"}, DependsOn: compose.DependsOn{{Name: "db"}}},
			"db":    {Image: "alpine:3.20", Profiles: []string{"x"}},
			"other": {Image: "alpine:3.20", Profiles: []string{"y"}, DependsOn: compose.DependsOn{{Name: "db2"}}},
			"db2":   {Image: "alpine:3.20", Profiles: []string{"y"}},
		})
	}
	for _, order := range [][]string{{"web", "other"}, {"other", "web"}} {
		for _, tc := range []struct {
			name string
			call func(o *orchestrator.Orchestrator, names []string) error
		}{
			{"stop", func(o *orchestrator.Orchestrator, names []string) error { return o.Stop(names) }},
			{"kill", func(o *orchestrator.Orchestrator, names []string) error { return o.Kill(names, "TERM") }},
		} {
			t.Run(strings.Join(order, "-")+"/"+tc.name, func(t *testing.T) {
				rt, _ := fakeShim(t)
				o := orchestrator.New(twoCarriesProject(), rt, "opossum", &bytes.Buffer{})
				if err := o.Up(true, order...); err != nil {
					t.Fatalf("up %v: %v", order, err)
				}
				if err := tc.call(o, order); err != nil {
					t.Errorf("must not refuse: each of %v carries its own dependency under its own shared profile, got: %v", order, err)
				}
			})
		}
	}
}

// The carrying above is not accidentally permissive about everything: a
// dependency that shares neither named service's own profile is still
// refused, so naming two services does not launder an unrelated gated
// service in by accident.
func TestNamingTwoServicesDoesNotCarryAThirdUnrelatedProfile(t *testing.T) {
	proj := project("demo", map[string]*compose.Service{
		"web":   {Image: "alpine:3.20", Profiles: []string{"x"}, DependsOn: compose.DependsOn{{Name: "db"}, {Name: "gz"}}},
		"db":    {Image: "alpine:3.20", Profiles: []string{"x"}},
		"gz":    {Image: "alpine:3.20", Profiles: []string{"z"}},
		"other": {Image: "alpine:3.20", Profiles: []string{"y"}, DependsOn: compose.DependsOn{{Name: "db2"}}},
		"db2":   {Image: "alpine:3.20", Profiles: []string{"y"}},
	})
	for _, tc := range []struct {
		name string
		call func(o *orchestrator.Orchestrator) error
	}{
		{"stop", func(o *orchestrator.Orchestrator) error { return o.Stop([]string{"web", "other"}) }},
		{"kill", func(o *orchestrator.Orchestrator) error { return o.Kill([]string{"web", "other"}, "TERM") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			// gz's inactive profile would refuse `up`, so its container (and
			// web's) is seeded directly, the way the single-name fixtures
			// above do — the point here is what naming web and other checks,
			// not whether up would have started this file.
			o := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})
			if err := rt.Run(runtime.RunOptions{Name: "web.demo.opossum", Image: "alpine:3.20"}); err != nil {
				t.Fatalf("seeding web's container: %v", err)
			}
			err := tc.call(o)
			if err == nil || !strings.Contains(err.Error(), `"gz", whose profile is not active`) {
				t.Errorf("want gz refused (not carried by naming web and other, which share neither of gz's profile), got: %v", err)
			}
		})
	}
}

// The check must not stop after a named service's first dependency: a second,
// later dependency that is the actually-broken one must still be reached
// (independent review of #1385: a mutation that broke out of the inner loop
// after one iteration stayed green without a fixture naming more than one
// dependency).
func TestNamingAGatedServiceWithTwoDependenciesChecksBothOfThem(t *testing.T) {
	twoDepsProject := func() *compose.Project {
		return project("demo", map[string]*compose.Service{
			"web": {Image: "alpine:3.20"},
			"broken": {
				Image:     "alpine:3.20",
				Profiles:  []string{"g"},
				DependsOn: compose.DependsOn{{Name: "web"}, {Name: "nosuch"}},
			},
		})
	}
	for _, tc := range namedCommands {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			o := orchestrator.New(twoDepsProject(), rt, "opossum", &bytes.Buffer{})
			if err := rt.Run(runtime.RunOptions{Name: "broken.demo.opossum", Image: "alpine:3.20"}); err != nil {
				t.Fatalf("seeding broken's container: %v", err)
			}
			err := tc.call(o, []string{"broken"})
			if err == nil || !strings.Contains(err.Error(), `depends on unknown service "nosuch"`) {
				t.Errorf("want the second dependency (nosuch) refused even though the first (web) is fine, got: %v", err)
			}
		})
	}
}

// An optional dependency that does not exist at all is still refused as
// unknown for a service the run reads without its being named (here, one with no `profiles:`) —
// Optional excuses a gated-inactive dependency, and a missing one only for a gated service that
// the name alone enables (TestNamingAGatedServiceWithAnOptionalUndefinedDependency); docker compose
// refuses the file as it reads it for every command (measured, v5.5.1).
func TestNamingAServiceWithAnOptionalButUndefinedDependencyStillRefuses(t *testing.T) {
	proj := func() *compose.Project {
		return project("demo", map[string]*compose.Service{
			"web": {
				Image:     "alpine:3.20",
				DependsOn: compose.DependsOn{{Name: "nosuch", Optional: true}},
			},
		})
	}
	for _, tc := range []struct {
		name string
		call func(o *orchestrator.Orchestrator) error
	}{
		{"stop", func(o *orchestrator.Orchestrator) error { return o.Stop([]string{"web"}) }},
		{"kill", func(o *orchestrator.Orchestrator) error { return o.Kill([]string{"web"}, "TERM") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			o := orchestrator.New(proj(), rt, "opossum", &bytes.Buffer{})
			if err := rt.Run(runtime.RunOptions{Name: "web.demo.opossum", Image: "alpine:3.20"}); err != nil {
				t.Fatalf("seeding web's container: %v", err)
			}
			err := tc.call(o)
			if err == nil || !strings.Contains(err.Error(), `depends on unknown service "nosuch"`) {
				t.Errorf("want an optional but undefined dependency refused (Optional excuses gated-inactive, not missing), got: %v", err)
			}
		})
	}
}

// A dependency written `required: false` is not read as a fault for a gated service that only its
// name enables, whatever it points at: docker compose v5.5.1 goes on for `stop`, `kill` and `logs` over
// an undefined one (`start` then stops for there being no container, which is not about the
// dependency). With the profile turned on by `--profile` or COMPOSE_PROFILES the service is read like
// any other, and the undefined one is refused (measured, all four).
func TestNamingAGatedServiceWithAnOptionalUndefinedDependency(t *testing.T) {
	proj := func() *compose.Project {
		return project("demo", map[string]*compose.Service{
			"web": {Image: "alpine:3.20"},
			"broken": {
				Image:     "alpine:3.20",
				Profiles:  []string{"g"},
				DependsOn: compose.DependsOn{{Name: "nosuch", Optional: true}},
			},
		})
	}
	// An unrelated profile turned on does not read broken like an ordinary service: only its own
	// (or `*`) does (docker compose v5.5.1, measured: `--profile h stop broken` goes on).
	for _, pr := range []struct {
		name    string
		on      []string
		refused bool
	}{
		{"only the name enables it", nil, false},
		{"profile g turned on", []string{"g"}, true},
		{"an unrelated profile h turned on", []string{"h"}, false},
		{"every profile turned on (*)", []string{"*"}, true},
	} {
		for _, tc := range namedCommands {
			t.Run(tc.name+"/"+pr.name, func(t *testing.T) {
				rt, _ := fakeShim(t)
				o := orchestrator.New(proj(), rt, "opossum", &bytes.Buffer{})
				if pr.on != nil {
					o.EnableProfiles(pr.on)
				}
				if err := rt.Run(runtime.RunOptions{Name: "broken.demo.opossum", Image: "alpine:3.20"}); err != nil {
					t.Fatalf("seeding broken's container: %v", err)
				}
				err := tc.call(o, []string{"broken"})
				// Refused in any words counts as refused: a row that expects it let go must not pass
				// because a different refusal (a profile that is "not active" for a service the
				// file does not define) was worded differently.
				if refused := refusedOverADependency(err); refused != pr.refused {
					t.Errorf("refused over the optional undefined dependency = %v, want %v, got: %v", refused, pr.refused, err)
				}
				if pr.refused && !strings.Contains(err.Error(), `depends on unknown service "nosuch"`) {
					t.Errorf("refused, but not over the undefined service: %v", err)
				}
			})
		}
	}
}

// An optional dependency that is let go does not end the check: a later one that is a fault is still
// refused (docker compose refuses `broken` with [dbg optional, nosuch]).
func TestAnOptionalDependencyLetGoDoesNotEndTheCheck(t *testing.T) {
	proj := project("demo", map[string]*compose.Service{
		"web": {Image: "alpine:3.20"},
		"dbg": {Image: "alpine:3.20", Profiles: []string{"h"}},
		"broken": {
			Image:    "alpine:3.20",
			Profiles: []string{"g"},
			DependsOn: compose.DependsOn{
				{Name: "dbg", Optional: true},
				{Name: "nosuch"},
			},
		},
	})
	for _, tc := range namedCommands {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			o := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})
			err := tc.call(o, []string{"broken"})
			if err == nil || !strings.Contains(err.Error(), `depends on unknown service "nosuch"`) {
				t.Errorf("want nosuch refused after the optional dbg, got: %v", err)
			}
		})
	}
}
