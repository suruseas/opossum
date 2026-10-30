package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// aliasUp runs `up` of project demo on the fake shim, its CLI reporting
// version, with networks back and front declared, and returns the `run` line of
// each service and what `up` printed.
func aliasUp(t *testing.T, version, dnsDomain string, svcs map[string]*compose.Service) (map[string]string, string) {
	t.Helper()
	rt, log := fakeShim(t)
	rt.Env = append(rt.Env, "CLIENT_VERSION="+version)
	p := project("demo", svcs)
	p.Networks = map[string]compose.NetworkDecl{"back": {}, "front": {}}
	var out bytes.Buffer
	o := orchestrator.New(p, rt, dnsDomain, &out)
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v\n%s", err, out.String())
	}
	runs := map[string]string{}
	for _, l := range log() {
		if !strings.HasPrefix(l, "run ") {
			continue
		}
		for name := range svcs {
			if strings.Contains(l, " --name "+name+".") || strings.Contains(l, " --name "+name+" ") {
				runs[name] = l
			}
		}
	}
	return runs, out.String()
}

func aliased(networks []string, aliases map[string][]string) *compose.Service {
	return &compose.Service{Image: "redis:7", Networks: networks, NetworkAliases: aliases}
}

// An alias is passed under the project and the DNS domain, the form of the
// services' own names, on the network that writes it.
func TestUpPassesNetworkAliases(t *testing.T) {
	for _, tc := range []struct {
		name string
		svc  *compose.Service
		want []string // in the run line
		not  []string // not in it
	}{
		{"one alias", aliased([]string{"back"}, map[string][]string{"back": {"cache"}}),
			[]string{"--network=demo-back,alias=cache.demo.opossum "}, nil},
		{"two aliases on a network", aliased([]string{"back"}, map[string][]string{"back": {"cache", "kv"}}),
			[]string{"--network=demo-back,alias=cache.demo.opossum,alias=kv.demo.opossum "}, nil},
		{"aliases on two networks", aliased([]string{"back", "front"}, map[string][]string{"back": {"cache"}, "front": {"www"}}),
			[]string{"--network=demo-back,alias=cache.demo.opossum ", "--network=demo-front,alias=www.demo.opossum "}, nil},
		// The runtime refuses one alias on two networks, and answers for it on
		// every network: it is given once, on the first.
		{"the same alias on two networks", aliased([]string{"back", "front"}, map[string][]string{"back": {"cache"}, "front": {"cache"}}),
			[]string{"--network=demo-back,alias=cache.demo.opossum ", "--network=demo-front "}, nil},
		{"the same alias in other capitals on the second network", aliased([]string{"back", "front"}, map[string][]string{"back": {"cache"}, "front": {"Cache"}}),
			[]string{"--network=demo-back,alias=cache.demo.opossum ", "--network=demo-front "}, nil},
		{"the same alias twice on one network", aliased([]string{"back"}, map[string][]string{"back": {"cache", "cache"}}),
			[]string{"--network=demo-back,alias=cache.demo.opossum "}, nil},
		{"capitals and an underscore", aliased([]string{"back"}, map[string][]string{"back": {"Cache_1"}}),
			[]string{"--network=demo-back,alias=Cache_1.demo.opossum "}, nil},
		{"a 63-character label", aliased([]string{"back"}, map[string][]string{"back": {strings.Repeat("a", 63)}}),
			[]string{"--network=demo-back,alias=" + strings.Repeat("a", 63) + ".demo.opossum "}, nil},
		{"after the MAC address", &compose.Service{Image: "redis:7", Networks: []string{"back"}, MacAddress: "02:42:ac:11:00:77", NetworkAliases: map[string][]string{"back": {"cache"}}},
			[]string{"--network=demo-back,mac=02:42:ac:11:00:77,alias=cache.demo.opossum "}, nil},
		// An alias is read from the networks the service joins, in their order.
		{"an alias under a network the service does not join", aliased([]string{"back"}, map[string][]string{"front": {"www"}}),
			[]string{"--network=demo-back "}, []string{"alias="}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runs, out := aliasUp(t, "1.6.1", "opossum", map[string]*compose.Service{"db": tc.svc})
			for _, w := range tc.want {
				if !strings.Contains(runs["db"]+" ", w) {
					t.Errorf("want %q in the run, got %q", w, runs["db"])
				}
			}
			for _, n := range tc.not {
				if strings.Contains(runs["db"], n) {
					t.Errorf("want no %q in the run, got %q", n, runs["db"])
				}
			}
			if strings.Contains(out, "network alias") {
				t.Errorf("nothing was left out, so nothing should be said, got:\n%s", out)
			}
		})
	}
}

// What is not passed is said, once for each alias, and the service starts
// without it: each of these is a run the runtime would refuse, or a name a peer
// would not reach.
func TestUpLeavesOutAliasesItCannotPass(t *testing.T) {
	one := func(alias string) *compose.Service {
		return aliased([]string{"back"}, map[string][]string{"back": {alias}})
	}
	for _, tc := range []struct {
		name, version, domain string
		svc                   *compose.Service
		why                   string
	}{
		{"a CLI before 1.6.0", "1.5.0", "opossum", one("cache"), "from 1.6.0 on"},
		{"a CLI whose version is not read", "unknown", "opossum", one("cache"), "from 1.6.0 on"},
		{"no DNS domain", "1.6.1", "", one("cache"), "no DNS domain"},
		{"an alias with a dot", "1.6.1", "opossum", one("db.internal"), "with a dot"},
		{"a comma", "1.6.1", "opossum", one("a,b"), "one DNS label"},
		{"a space", "1.6.1", "opossum", one("a b"), "one DNS label"},
		{"a leading hyphen", "1.6.1", "opossum", one("-cache"), "one DNS label"},
		{"a trailing hyphen", "1.6.1", "opossum", one("cache-"), "one DNS label"},
		{"a 64-character label", "1.6.1", "opossum", one(strings.Repeat("a", 64)), "one DNS label"},
		{"the service's own name", "1.6.1", "opossum", one("db"), "name of a service"},
		{"its own name in capitals", "1.6.1", "opossum", one("DB"), "name of a service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runs, out := aliasUp(t, tc.version, tc.domain, map[string]*compose.Service{"db": tc.svc})
			if runs["db"] == "" {
				t.Fatalf("the service should still start, got no run:\n%s", out)
			}
			if strings.Contains(runs["db"], "alias=") {
				t.Errorf("want no alias passed, got %q", runs["db"])
			}
			if n := strings.Count(out, "is not passed"); n != 1 || !strings.Contains(out, "[OPSM-502]") || !strings.Contains(out, tc.why) {
				t.Errorf("want one OPSM-502 line saying %q, got %d in:\n%s", tc.why, n, out)
			}
		})
	}
}

// An alias that is another service's name, or that two services write, would
// be refused by the runtime once the other container exists: the name goes to
// the service it names, and a shared alias to the first service by name.
func TestUpGivesAnAliasToOneService(t *testing.T) {
	t.Run("another service's name", func(t *testing.T) {
		runs, out := aliasUp(t, "1.6.1", "opossum", map[string]*compose.Service{
			"db":  aliased([]string{"back"}, map[string][]string{"back": {"web"}}),
			"web": {Image: "nginx", Networks: []string{"back"}},
		})
		if strings.Contains(runs["db"], "alias=") || !strings.Contains(out, "name of a service") {
			t.Errorf("want the alias left out and said, got %q\n%s", runs["db"], out)
		}
	})
	// DNS compares names without regard to case, so a service written `Web`
	// holds the name `web` too.
	t.Run("another service's name in other capitals", func(t *testing.T) {
		runs, out := aliasUp(t, "1.6.1", "opossum", map[string]*compose.Service{
			"db":  aliased([]string{"back"}, map[string][]string{"back": {"web"}}),
			"Web": {Image: "nginx", Networks: []string{"back"}},
		})
		if strings.Contains(runs["db"], "alias=") || !strings.Contains(out, "name of a service") {
			t.Errorf("want the alias left out and said, got %q\n%s", runs["db"], out)
		}
	})
	t.Run("two services write it", func(t *testing.T) {
		runs, out := aliasUp(t, "1.6.1", "opossum", map[string]*compose.Service{
			"b": aliased([]string{"back"}, map[string][]string{"back": {"cache"}}),
			"a": aliased([]string{"front"}, map[string][]string{"front": {"Cache"}}),
		})
		if !strings.Contains(runs["a"], "alias=Cache.demo.opossum") {
			t.Errorf("the first service by name should take it, got %q", runs["a"])
		}
		if strings.Contains(runs["b"], "alias=") || !strings.Contains(out, `service "b": network alias "cache" is not passed: service "a" takes it`) {
			t.Errorf("the second should be told, got %q\n%s", runs["b"], out)
		}
	})
	t.Run("the first by name has no network", func(t *testing.T) {
		runs, _ := aliasUp(t, "1.6.1", "opossum", map[string]*compose.Service{
			"b": aliased([]string{"back"}, map[string][]string{"back": {"cache"}}),
			"a": {Image: "redis:7", NetworkMode: compose.NetworkModeNone, NetworkAliases: map[string][]string{"back": {"cache"}}},
		})
		if !strings.Contains(runs["b"], "alias=cache.demo.opossum") {
			t.Errorf("a service on no network takes no alias, so b should, got %q", runs["b"])
		}
	})
}

// A service on no network passes nothing and says nothing.
func TestUpPassesNoAliasWithNetworkModeNone(t *testing.T) {
	runs, out := aliasUp(t, "1.6.1", "opossum", map[string]*compose.Service{
		"db": {Image: "redis:7", NetworkMode: compose.NetworkModeNone, NetworkAliases: map[string][]string{"back": {"cache"}}},
	})
	if strings.Contains(runs["db"], "alias=") || strings.Contains(out, "network alias") {
		t.Errorf("want nothing passed or said, got %q\n%s", runs["db"], out)
	}
}

// The runtime is asked its version once for an `up`, however many services
// write aliases.
func TestUpAsksTheVersionOnce(t *testing.T) {
	rt, log := fakeShim(t)
	rt.Env = append(rt.Env, "CLIENT_VERSION=1.6.1")
	p := project("demo", map[string]*compose.Service{
		"a": aliased([]string{"back"}, map[string][]string{"back": {"x"}}),
		"b": aliased([]string{"back"}, map[string][]string{"back": {"y"}}),
	})
	p.Networks = map[string]compose.NetworkDecl{"back": {}}
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	before := 0
	if err := o.Up(true); err != nil {
		t.Fatal(err)
	}
	for _, l := range log() {
		if l == "system status --format json" {
			before++
		}
	}
	// One for the aliases; `up` may ask for other reasons, so compare with the
	// same project written without aliases.
	rt2, log2 := fakeShim(t)
	rt2.Env = append(rt2.Env, "CLIENT_VERSION=1.6.1")
	p2 := project("demo", map[string]*compose.Service{
		"a": {Image: "redis:7", Networks: []string{"back"}},
		"b": {Image: "redis:7", Networks: []string{"back"}},
	})
	p2.Networks = map[string]compose.NetworkDecl{"back": {}}
	if err := orchestrator.New(p2, rt2, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatal(err)
	}
	without := 0
	for _, l := range log2() {
		if l == "system status --format json" {
			without++
		}
	}
	if before != without+1 {
		t.Errorf("want one more status query than without aliases, got %d and %d", before, without)
	}
}
