package orchestrator_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A named `stop`, `kill`, `logs` or `start` reads the dependencies of the dependencies the name carries, as docker compose does
// (v5.5.1, every row measured with the file read and no `-p`, the four commands alike: `refused` is its `no such service`): `a`
// (gated) -> `b` (gated) -> a service the file does not define refuses for `a`, and so does one more step, and one through a service
// with no `profiles:`. What the name alone enables has its optional undefined dependency left alone, and with the profile on it is
// refused; an optional dependency behind a profile that is not active is not followed (#1711).
func TestANamedTeardownReadsTheDependenciesOfTheDependenciesItCarries(t *testing.T) {
	type edge struct {
		to       string
		optional bool
	}
	type node struct {
		profile string
		deps    []edge
	}
	for _, shape := range []struct {
		name                       string
		nodes                      map[string]node
		refusedByName, refusedByOn bool
		profileOn                  string // the profile the "on" rows turn on; "g" where it is left out
	}{
		// A cycle among the services the name carries (docker compose refuses it: a known difference): the walk ends.
		{"a(g)->b(g)->a (a cycle)", map[string]node{"a": {"g", []edge{{"b", false}}}, "b": {"g", []edge{{"a", false}}}}, false, false, ""},
		{"a(g)->b(g)->undefined", map[string]node{"a": {"g", []edge{{"b", false}}}, "b": {"g", []edge{{"nosuch", false}}}}, true, true, ""},
		{"a(g)->b(g)->other(h)", map[string]node{"a": {"g", []edge{{"b", false}}}, "b": {"g", []edge{{"other", false}}}, "other": {"h", nil}}, true, true, ""},
		{"a(g)->b(g)->undefined, optional", map[string]node{"a": {"g", []edge{{"b", false}}}, "b": {"g", []edge{{"nosuch", true}}}}, false, true, ""},
		{"a(g)->b(g)->other(h), optional", map[string]node{"a": {"g", []edge{{"b", false}}}, "b": {"g", []edge{{"other", true}}}, "other": {"h", nil}}, false, false, ""},
		{"a(g)->b(g)->c(g)->undefined", map[string]node{"a": {"g", []edge{{"b", false}}}, "b": {"g", []edge{{"c", false}}}, "c": {"g", []edge{{"nosuch", false}}}}, true, true, ""},
		{"a(g)->b(g, optional)->undefined", map[string]node{"a": {"g", []edge{{"b", true}}}, "b": {"g", []edge{{"nosuch", false}}}}, true, true, ""},
		{"a(g)->b(g)->web", map[string]node{"a": {"g", []edge{{"b", false}}}, "b": {"g", []edge{{"web", false}}}}, false, false, ""},
		{"a(g)->b(none)->c(h)", map[string]node{"a": {"g", []edge{{"b", false}}}, "b": {"", []edge{{"c", false}}}, "c": {"h", nil}}, true, true, ""},
		{"a(none)->b(none)->c(h)", map[string]node{"a": {"", []edge{{"b", false}}}, "b": {"", []edge{{"c", false}}}, "c": {"h", nil}}, true, true, ""},
		{"a(g)->b(g)->c(none)->d(h)", map[string]node{"a": {"g", []edge{{"b", false}}}, "b": {"g", []edge{{"c", false}}}, "c": {"", []edge{{"d", false}}}, "d": {"h", nil}}, true, true, ""},
		{"a(g)->b(g)->c(g, optional)->undefined", map[string]node{"a": {"g", []edge{{"b", false}}}, "b": {"g", []edge{{"c", true}}}, "c": {"g", []edge{{"nosuch", false}}}}, true, true, ""},
		{"a(g)->b(h, optional)->undefined", map[string]node{"a": {"g", []edge{{"b", true}}}, "b": {"h", []edge{{"nosuch", false}}}}, false, false, ""},
		{"a(g)->b(h)->undefined, optional (the profile of b on)", map[string]node{"a": {"g", []edge{{"b", false}}}, "b": {"h", []edge{{"nosuch", true}}}}, true, true, "h"},
	} {
		for _, on := range []bool{false, true} {
			want := shape.refusedByName
			form := "by name"
			if on {
				want = shape.refusedByOn
				form = "with the profile on"
			}
			for _, tc := range namedCommands {
				t.Run(shape.name+": "+tc.name+" "+form, func(t *testing.T) {
					services := map[string]*compose.Service{"web": {Image: "alpine:3.20"}}
					for name, n := range shape.nodes {
						svc := &compose.Service{Image: "alpine:3.20"}
						if n.profile != "" {
							svc.Profiles = []string{n.profile}
						}
						for _, e := range n.deps {
							svc.DependsOn = append(svc.DependsOn, compose.Dependency{Name: e.to, Optional: e.optional})
						}
						services[name] = svc
					}
					rt, _ := fakeShim(t)
					setShimEnv(rt, "INSPECT_ABSENT=a.demo.opossum")
					o := orchestrator.New(project("demo", services), rt, "opossum", &bytes.Buffer{})
					if on {
						profile := shape.profileOn
						if profile == "" {
							profile = "g"
						}
						o.EnableProfiles([]string{profile})
					}
					// The walk ends: a cycle that was followed without a record of what was seen would not return.
					done := make(chan error, 1)
					go func() { done <- tc.call(o, []string{"a"}) }()
					var err error
					select {
					case err = <-done:
					case <-time.After(5 * time.Second):
						t.Fatalf("%s %s: the check of the dependencies does not return", tc.name, form)
					}
					if got := refusedOverADependency(err); got != want {
						t.Errorf("%s %s: refused over a dependency: %v, docker compose refuses it: %v (err %v)", tc.name, form, got, want, err)
					}
				})
			}
		}
	}
}
