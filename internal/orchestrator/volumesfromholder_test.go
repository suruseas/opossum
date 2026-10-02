package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A service that takes another's volumes needs that service started with it.
// docker compose refuses one whose holder is left out (`cannot share volume with
// service b: container missing`, v5.5.1, measured 2026-09-26): a holder behind a
// profile that is off, depended on with `required: false`, is dropped from what
// is started, and the borrower then had its volumes mounted from a service that
// was never there.
func TestAServiceIsNotStartedBesideAHolderThatIsNot(t *testing.T) {
	holderProject := func(gated bool, ref string, dep compose.DependsOn) *compose.Project {
		b := &compose.Service{Image: "alpine:3.20", Volumes: []string{"bv:/b"}}
		if gated {
			b.Profiles = []string{"x"}
		}
		return project("demo", map[string]*compose.Service{
			"a": {Image: "alpine:3.20", VolumesFrom: []string{ref}, DependsOn: dep, Volumes: []string{"av:/a"}},
			"b": b,
		})
	}
	optional := compose.DependsOn{{Name: "b", Condition: compose.ConditionStarted, Optional: true}}
	required := compose.DependsOn{{Name: "b", Condition: compose.ConditionStarted}}
	for _, tc := range []struct {
		name     string
		gated    bool
		ref      string
		dep      compose.DependsOn
		profiles []string
		// holderThere is whether the runtime has a container for the holder: an
		// earlier `up` made one. The shim answers "running" for a name nobody has
		// asked about, so the absent case says so.
		holderThere bool
		refused     bool
	}{
		{"a holder behind a profile that is off, with no container", true, "b", optional, nil, false, true},
		{"the same, borrowed read-only", true, "b:ro", optional, nil, false, true},
		{"the same with the profile on", true, "b", optional, []string{"x"}, false, false},
		// What docker compose asks is whether the holder has a container, in any
		// state: one an earlier `up` (with the profile on) made is shared with.
		{"the holder left out has a container from an earlier up", true, "b", optional, nil, true, false},
		// The controls: a holder that is not gated is started, and one that is
		// depended on as required is refused for that and not for this (the
		// gated-dependency refusal is another message).
		{"a holder that is not gated", false, "b", optional, nil, false, false},
		{"a holder that is not gated, depended on as required", false, "b", required, nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			if !tc.holderThere {
				strictContainers(t, rt)
				setShimEnv(rt, "INSPECT_ABSENT=b.demo.opossum a.demo.opossum")
			} else {
				// The holder's container is one an earlier up made; a's is not there yet.
				strictContainers(t, rt, "b.demo.opossum")
				setShimEnv(rt, "INSPECT_ABSENT=a.demo.opossum")
			}
			o := orchestrator.New(holderProject(tc.gated, tc.ref, tc.dep), rt, "opossum", &bytes.Buffer{})
			if tc.profiles != nil {
				o.EnableProfiles(tc.profiles)
			}
			err := o.Up(true)
			if tc.refused {
				if err == nil || !strings.Contains(err.Error(), `service "a" cannot share volume with service "b": container missing`) {
					t.Fatalf("want the borrower refused, got %v", err)
				}
				if indexOf(log(), "run -d --name a.demo.opossum") >= 0 {
					t.Errorf("the borrower was started beside a holder that is not:\n%s", strings.Join(log(), "\n"))
				}
				return
			}
			if err != nil {
				t.Fatalf("want it started, got %v", err)
			}
			if indexOf(log(), "run -d --name a.demo.opossum") < 0 {
				t.Errorf("the borrower was not started:\n%s", strings.Join(log(), "\n"))
			}
		})
	}
}

// The rebuild `watch` does — an `up` of the borrower alone, with its holder left
// as it is — is not refused for a holder that is running and not in that `up`:
// docker compose (`up -d --force-recreate --no-deps a`) goes through.
func TestARebuildOfABorrowerLeavesItsRunningHolderAlone(t *testing.T) {
	p := project("demo", map[string]*compose.Service{
		"a": {Image: "alpine:3.20", VolumesFrom: []string{"b"}, DependsOn: compose.DependsOn{{Name: "b", Condition: compose.ConditionStarted}}},
		"b": {Image: "alpine:3.20", Volumes: []string{"bv:/b"}},
	})
	rt, log := fakeShim(t)
	strictContainers(t, rt, "b.demo.opossum") // the running holder; a has none yet
	setShimEnv(rt, "INSPECT_ABSENT=a.demo.opossum")
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.RebuildServiceForTest("a"); err != nil {
		t.Fatalf("the rebuild of the borrower was refused for a holder that is running: %v\n%s", err, strings.Join(log(), "\n"))
	}
}
