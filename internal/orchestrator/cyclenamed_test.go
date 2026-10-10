package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A dependency cycle among the services the run reads without a name — no `profiles:`, or a profile turned on — is refused by `stop`, `kill`, `logs` and `start` that name a
// service, whichever one they name, even one the cycle does not touch (docker compose v5.5.1, measured on `stop`/`kill`/`logs`/`start` of every row, #1871). One behind a profile
// that is not on is not read, and a service unrelated to it goes on; naming a gated service that carries the cycle is where `stop` refuses and `kill` and `logs` go on, which is
// not asked here (those rows are left out).
func TestACycleAmongTheServicesReadWithoutANameRefusesTheNamingCommands(t *testing.T) {
	for _, tc := range []struct {
		cycleGated bool
		dep        string // "" none, "ungated", "gated": a service that depends on the cycle
		profileOn  bool
		target     string
		verb       string
		refused    bool
	}{
		{false, "ungated", false, "c1", "stop", true},
		{false, "ungated", false, "c1", "kill", true},
		{false, "ungated", false, "c1", "logs", true},
		{false, "ungated", false, "c1", "start", true},
		{false, "ungated", false, "dep", "stop", true},
		{false, "ungated", false, "dep", "kill", true},
		{false, "ungated", false, "dep", "logs", true},
		{false, "ungated", false, "dep", "start", true},
		{false, "ungated", false, "free", "stop", true},
		{false, "ungated", false, "free", "kill", true},
		{false, "ungated", false, "free", "logs", true},
		{false, "ungated", false, "free", "start", true},
		{false, "ungated", false, "gfree", "stop", true},
		{false, "ungated", false, "gfree", "kill", true},
		{false, "ungated", false, "gfree", "logs", true},
		{false, "ungated", false, "gfree", "start", true},
		{false, "ungated", true, "c1", "stop", true},
		{false, "ungated", true, "c1", "kill", true},
		{false, "ungated", true, "c1", "logs", true},
		{false, "ungated", true, "c1", "start", true},
		{false, "ungated", true, "dep", "stop", true},
		{false, "ungated", true, "dep", "kill", true},
		{false, "ungated", true, "dep", "logs", true},
		{false, "ungated", true, "dep", "start", true},
		{false, "ungated", true, "free", "stop", true},
		{false, "ungated", true, "free", "kill", true},
		{false, "ungated", true, "free", "logs", true},
		{false, "ungated", true, "free", "start", true},
		{false, "ungated", true, "gfree", "stop", true},
		{false, "ungated", true, "gfree", "kill", true},
		{false, "ungated", true, "gfree", "logs", true},
		{false, "ungated", true, "gfree", "start", true},
		{false, "", false, "c1", "stop", true},
		{false, "", false, "c1", "kill", true},
		{false, "", false, "c1", "logs", true},
		{false, "", false, "c1", "start", true},
		{false, "", false, "free", "stop", true},
		{false, "", false, "free", "kill", true},
		{false, "", false, "free", "logs", true},
		{false, "", false, "free", "start", true},
		{false, "", false, "gfree", "stop", true},
		{false, "", false, "gfree", "kill", true},
		{false, "", false, "gfree", "logs", true},
		{false, "", false, "gfree", "start", true},
		{false, "", true, "c1", "stop", true},
		{false, "", true, "c1", "kill", true},
		{false, "", true, "c1", "logs", true},
		{false, "", true, "c1", "start", true},
		{false, "", true, "free", "stop", true},
		{false, "", true, "free", "kill", true},
		{false, "", true, "free", "logs", true},
		{false, "", true, "free", "start", true},
		{false, "", true, "gfree", "stop", true},
		{false, "", true, "gfree", "kill", true},
		{false, "", true, "gfree", "logs", true},
		{false, "", true, "gfree", "start", true},
		{false, "gated", false, "c1", "stop", true},
		{false, "gated", false, "c1", "kill", true},
		{false, "gated", false, "c1", "logs", true},
		{false, "gated", false, "c1", "start", true},
		{false, "gated", false, "dep", "stop", true},
		{false, "gated", false, "dep", "kill", true},
		{false, "gated", false, "dep", "logs", true},
		{false, "gated", false, "dep", "start", true},
		{false, "gated", false, "free", "stop", true},
		{false, "gated", false, "free", "kill", true},
		{false, "gated", false, "free", "logs", true},
		{false, "gated", false, "free", "start", true},
		{false, "gated", false, "gfree", "stop", true},
		{false, "gated", false, "gfree", "kill", true},
		{false, "gated", false, "gfree", "logs", true},
		{false, "gated", false, "gfree", "start", true},
		{false, "gated", true, "c1", "stop", true},
		{false, "gated", true, "c1", "kill", true},
		{false, "gated", true, "c1", "logs", true},
		{false, "gated", true, "c1", "start", true},
		{false, "gated", true, "dep", "stop", true},
		{false, "gated", true, "dep", "kill", true},
		{false, "gated", true, "dep", "logs", true},
		{false, "gated", true, "dep", "start", true},
		{false, "gated", true, "free", "stop", true},
		{false, "gated", true, "free", "kill", true},
		{false, "gated", true, "free", "logs", true},
		{false, "gated", true, "free", "start", true},
		{false, "gated", true, "gfree", "stop", true},
		{false, "gated", true, "gfree", "kill", true},
		{false, "gated", true, "gfree", "logs", true},
		{false, "gated", true, "gfree", "start", true},
		{true, "ungated", false, "c1", "stop", true},
		{true, "ungated", false, "c1", "kill", true},
		{true, "ungated", false, "c1", "logs", true},
		{true, "ungated", false, "c1", "start", true},
		{true, "ungated", false, "dep", "stop", true},
		{true, "ungated", false, "dep", "kill", true},
		{true, "ungated", false, "dep", "logs", true},
		{true, "ungated", false, "dep", "start", true},
		{true, "ungated", false, "free", "stop", true},
		{true, "ungated", false, "free", "kill", true},
		{true, "ungated", false, "free", "logs", true},
		{true, "ungated", false, "free", "start", true},
		{true, "ungated", false, "gfree", "stop", true},
		{true, "ungated", false, "gfree", "kill", true},
		{true, "ungated", false, "gfree", "logs", true},
		{true, "ungated", false, "gfree", "start", true},
		{true, "ungated", true, "c1", "stop", true},
		{true, "ungated", true, "c1", "kill", true},
		{true, "ungated", true, "c1", "logs", true},
		{true, "ungated", true, "c1", "start", true},
		{true, "ungated", true, "dep", "stop", true},
		{true, "ungated", true, "dep", "kill", true},
		{true, "ungated", true, "dep", "logs", true},
		{true, "ungated", true, "dep", "start", true},
		{true, "ungated", true, "free", "stop", true},
		{true, "ungated", true, "free", "kill", true},
		{true, "ungated", true, "free", "logs", true},
		{true, "ungated", true, "free", "start", true},
		{true, "ungated", true, "gfree", "stop", true},
		{true, "ungated", true, "gfree", "kill", true},
		{true, "ungated", true, "gfree", "logs", true},
		{true, "ungated", true, "gfree", "start", true},
		{true, "", false, "free", "stop", false},
		{true, "", false, "free", "kill", false},
		{true, "", false, "free", "logs", false},
		{true, "", false, "gfree", "stop", false},
		{true, "", false, "gfree", "kill", false},
		{true, "", false, "gfree", "logs", false},
		{true, "", true, "c1", "stop", true},
		{true, "", true, "c1", "kill", true},
		{true, "", true, "c1", "logs", true},
		{true, "", true, "c1", "start", true},
		{true, "", true, "free", "stop", true},
		{true, "", true, "free", "kill", true},
		{true, "", true, "free", "logs", true},
		{true, "", true, "free", "start", true},
		{true, "", true, "gfree", "stop", true},
		{true, "", true, "gfree", "kill", true},
		{true, "", true, "gfree", "logs", true},
		{true, "", true, "gfree", "start", true},
		{true, "gated", false, "free", "stop", false},
		{true, "gated", false, "free", "kill", false},
		{true, "gated", false, "free", "logs", false},
		{true, "gated", false, "gfree", "stop", false},
		{true, "gated", false, "gfree", "kill", false},
		{true, "gated", false, "gfree", "logs", false},
		{true, "gated", true, "c1", "stop", true},
		{true, "gated", true, "c1", "kill", true},
		{true, "gated", true, "c1", "logs", true},
		{true, "gated", true, "c1", "start", true},
		{true, "gated", true, "dep", "stop", true},
		{true, "gated", true, "dep", "kill", true},
		{true, "gated", true, "dep", "logs", true},
		{true, "gated", true, "dep", "start", true},
		{true, "gated", true, "free", "stop", true},
		{true, "gated", true, "free", "kill", true},
		{true, "gated", true, "free", "logs", true},
		{true, "gated", true, "free", "start", true},
		{true, "gated", true, "gfree", "stop", true},
		{true, "gated", true, "gfree", "kill", true},
		{true, "gated", true, "gfree", "logs", true},
		{true, "gated", true, "gfree", "start", true},
	} {
		name := "cycle gated=" + map[bool]string{false: "no", true: "yes"}[tc.cycleGated] + "/dep=" + tc.dep + "/profile on=" + map[bool]string{false: "no", true: "yes"}[tc.profileOn] + "/" + tc.verb + " " + tc.target
		t.Run(name, func(t *testing.T) {
			var profiles []string
			if tc.cycleGated {
				profiles = []string{"g"}
			}
			services := map[string]*compose.Service{
				"c1":    {Image: "alpine:3.20", Profiles: profiles, DependsOn: compose.DependsOn{{Name: "c2"}}},
				"c2":    {Image: "alpine:3.20", Profiles: profiles, DependsOn: compose.DependsOn{{Name: "c1"}}},
				"free":  {Image: "alpine:3.20"},
				"gfree": {Image: "alpine:3.20", Profiles: []string{"g"}},
			}
			switch tc.dep {
			case "ungated":
				services["dep"] = &compose.Service{Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "c1"}}}
			case "gated":
				services["dep"] = &compose.Service{Image: "alpine:3.20", Profiles: []string{"g"}, DependsOn: compose.DependsOn{{Name: "c1"}}}
			}
			rt, _ := fakeShim(t)
			o := orchestrator.New(project("demo", services), rt, "opossum", &bytes.Buffer{})
			if tc.profileOn {
				o.EnableProfiles([]string{"g"})
			}
			var call func(*orchestrator.Orchestrator, []string) error
			for _, c := range namedCommands {
				if c.name == tc.verb {
					call = c.call
				}
			}
			err := call(o, []string{tc.target})
			if (err != nil) != tc.refused {
				t.Fatalf("%s %s: refused = %v, docker compose refuses it: %v (err %v)", tc.verb, tc.target, err != nil, tc.refused, err)
			}
			// A dependency of an ungated service on a cycle behind a profile that is off is refused for the dependency (docker compose too); every other refusal here is the cycle.
			if err != nil && !(tc.cycleGated && tc.dep == "ungated" && !tc.profileOn) && !strings.Contains(err.Error(), "dependency cycle detected") {
				t.Errorf("the refusal does not say what it is: %v", err)
			}
		})
	}
}

// More than one name is read the same: any of them naming a service the cycle does not touch, or none of them in it (docker compose v5.5.1, measured: `stop c1 free`, `stop free dep`).
func TestSeveralNamesAreReadForACycleAsOneDoes(t *testing.T) {
	for _, names := range [][]string{{"c1", "free"}, {"free", "dep"}, {"free", "c1", "dep"}} {
		for _, tc := range namedCommands {
			if tc.name == "start" {
				continue
			}
			t.Run(strings.Join(names, "+")+"/"+tc.name, func(t *testing.T) {
				rt, _ := fakeShim(t)
				o := orchestrator.New(project("demo", map[string]*compose.Service{
					"c1":   {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "c2"}}},
					"c2":   {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "c1"}}},
					"free": {Image: "alpine:3.20"},
					"dep":  {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "c1"}}},
				}), rt, "opossum", &bytes.Buffer{})
				if err := tc.call(o, names); err == nil || !strings.Contains(err.Error(), "dependency cycle detected") {
					t.Errorf("%s %v: want the cycle refused, got %v", tc.name, names, err)
				}
			})
		}
	}
}

// Left as it is, and not asked: a service behind a profile that is not on, named, with the cycle it carries. docker compose v5.5.1 refuses `stop c1` and `start c1` there and goes on for
// `kill c1` and `logs c1` (measured); this goes on for all four, so the rows pin that the check reads the services read without a name and not the ones a name brings in (#1871).
func TestANamedGatedServiceThatCarriesACycleIsNotAskedForIt(t *testing.T) {
	for _, tc := range namedCommands {
		if tc.name == "start" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			o := orchestrator.New(project("demo", map[string]*compose.Service{
				"c1":   {Image: "alpine:3.20", Profiles: []string{"g"}, DependsOn: compose.DependsOn{{Name: "c2"}}},
				"c2":   {Image: "alpine:3.20", Profiles: []string{"g"}, DependsOn: compose.DependsOn{{Name: "c1"}}},
				"free": {Image: "alpine:3.20"},
			}), rt, "opossum", &bytes.Buffer{})
			if err := tc.call(o, []string{"c1"}); err != nil && strings.Contains(err.Error(), "dependency cycle detected") {
				t.Errorf("%s c1: the cycle behind a profile that is off is read through the name: %v", tc.name, err)
			}
		})
	}
}
