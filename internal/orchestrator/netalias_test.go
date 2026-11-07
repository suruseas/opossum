package orchestrator_test

import (
	"bytes"
	"strconv"
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
	runs, _, out, err := aliasUpIn(t, "demo", version, dnsDomain, svcs)
	if err != nil {
		t.Fatalf("Up: %v\n%s", err, out)
	}
	return runs, out
}

// aliasUpIn is aliasUp for a project of any name, with more of the fake's knobs
// (env), returning every `run` line in order and the error `up` returned.
func aliasUpIn(t *testing.T, name, version, dnsDomain string, svcs map[string]*compose.Service, env ...string) (map[string]string, []string, string, error) {
	t.Helper()
	rt, log := fakeShim(t)
	rt.Env = append(append(rt.Env, "CLIENT_VERSION="+version), env...)
	p := project(name, svcs)
	p.Networks = map[string]compose.NetworkDecl{"back": {}, "front": {}}
	var out bytes.Buffer
	o := orchestrator.New(p, rt, dnsDomain, &out)
	err := o.Up(true)
	runs := map[string]string{}
	var all []string
	for _, l := range log() {
		if !strings.HasPrefix(l, "run ") {
			continue
		}
		all = append(all, l)
		for svc := range svcs {
			if strings.Contains(l, " --name "+svc+".") || strings.Contains(l, " --name "+svc+" ") {
				runs[svc] = l
			}
		}
	}
	return runs, all, out.String(), err
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
		{"the same alias in capitals on the first network", aliased([]string{"back", "front"}, map[string][]string{"back": {"Cache"}, "front": {"cache"}}),
			[]string{"--network=demo-back,alias=Cache.demo.opossum ", "--network=demo-front "}, nil},
		// The default network is the one a service with no list joins, or one that
		// lists `default`; its name is the project's own.
		{"on the default network", aliased([]string{"default"}, map[string][]string{"default": {"cache"}}),
			[]string{"--network=demo-net,alias=cache.demo.opossum "}, nil},
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
		{"a CLI before 1.6.0", "1.5.0", "opossum", one("cache"), "container 1.5.0 does not take network aliases"},
		{"a CLI whose version is not read", "unknown", "opossum", one("cache"), "did not say its version"},
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

// The name the alias is given under is the project's and the DNS domain's, not
// a fixed one.
func TestUpPassesAnAliasUnderTheProjectAndDomain(t *testing.T) {
	runs, _, out, err := aliasUpIn(t, "shop", "1.6.1", "test", map[string]*compose.Service{
		"db": aliased([]string{"back"}, map[string][]string{"back": {"cache"}}),
	})
	if err != nil {
		t.Fatalf("Up: %v\n%s", err, out)
	}
	if !strings.Contains(runs["db"], "--network=shop-back,alias=cache.shop.test ") {
		t.Errorf("want the alias under shop.test, got %q", runs["db"])
	}
}

// The runtime takes 32 aliases on a network (its source); the 33rd is left out
// and said.
func TestUpPassesAtMost32AliasesOnANetwork(t *testing.T) {
	var names []string
	for i := 0; i < 33; i++ {
		names = append(names, "a"+strconv.Itoa(i))
	}
	runs, out := aliasUp(t, "1.6.1", "opossum", map[string]*compose.Service{
		"db": aliased([]string{"back"}, map[string][]string{"back": names}),
	})
	if n := strings.Count(runs["db"], ",alias="); n != 32 {
		t.Errorf("want 32 aliases passed, got %d in %q", n, runs["db"])
	}
	if !strings.Contains(out, `network alias "a32" is not passed: the runtime takes at most 32`) {
		t.Errorf("want the 33rd said, got:\n%s", out)
	}
}

// An alias the 32 on one network left out is still given on a later network
// that writes it: being left out on the first is not being given there.
func TestUpGivesThe33rdAliasOnALaterNetwork(t *testing.T) {
	var names []string
	for i := 0; i < 33; i++ {
		names = append(names, "a"+strconv.Itoa(i))
	}
	runs, _ := aliasUp(t, "1.6.1", "opossum", map[string]*compose.Service{
		"db": aliased([]string{"back", "front"}, map[string][]string{"back": names, "front": {"a32"}}),
	})
	if !strings.Contains(runs["db"], "--network=demo-front,alias=a32.demo.opossum ") {
		t.Errorf("want a32 given on front, got %q", runs["db"])
	}
}

// A one-off `run` passes no aliases, as `docker compose run` passes none without
// `--use-aliases`; one that did would be refused by the runtime while the
// service's own container holds them.
func TestRunOneOffPassesNoAliases(t *testing.T) {
	rt, log := fakeShim(t)
	rt.Env = append(rt.Env, "CLIENT_VERSION=1.6.1")
	p := project("demo", map[string]*compose.Service{
		"db": aliased([]string{"back"}, map[string][]string{"back": {"cache"}}),
	})
	p.Networks = map[string]compose.NetworkDecl{"back": {}}
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.RunOneOff("db", []string{"true"}, orchestrator.RunOneOffOptions{Rm: true}); err != nil {
		t.Fatalf("RunOneOff: %v", err)
	}
	ran := false
	for _, l := range log() {
		if strings.HasPrefix(l, "run ") {
			ran = true
			if strings.Contains(l, "alias=") {
				t.Errorf("a one-off should pass no alias, got %q", l)
			}
		}
	}
	if !ran {
		t.Fatalf("no run was made: %v", log())
	}
}

// A container outside the file — one an earlier `up` left, running or stopped —
// that holds an alias makes the runtime refuse the run: it is made again without
// the aliases, said, and the service starts. Both places `up` runs a service do
// this: a detached start and a run-to-completion dependency.
func TestUpRunsAgainWithoutAliasesAnotherContainerHolds(t *testing.T) {
	for _, tc := range []struct {
		name string
		svcs map[string]*compose.Service
		svc  string
	}{
		{"a detached start", map[string]*compose.Service{
			"db": aliased([]string{"back"}, map[string][]string{"back": {"cache"}}),
		}, "db"},
		{"a run-to-completion dependency", map[string]*compose.Service{
			"db":  aliased([]string{"back"}, map[string][]string{"back": {"cache"}}),
			"web": {Image: "nginx", Networks: []string{"back"}, DependsOn: compose.DependsOn{{Name: "db", Condition: "service_completed_successfully"}}},
		}, "db"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, all, out, err := aliasUpIn(t, "demo", "1.6.1", "opossum", tc.svcs, "RUN_ALIAS_HELD=cache.demo.opossum")
			if err != nil {
				t.Fatalf("up should go on without the aliases, got %v\n%s", err, out)
			}
			var mine []string
			for _, l := range all {
				if strings.Contains(l, " --name "+tc.svc+".demo.opossum ") {
					mine = append(mine, l)
				}
			}
			if len(mine) != 2 || !strings.Contains(mine[0], "alias=cache.demo.opossum") || strings.Contains(mine[1], "alias=") {
				t.Fatalf("want a run with the alias, then one without, got %q", mine)
			}
			if !strings.Contains(out, `[OPSM-502] service "db": network alias(es) "cache" not passed: another container already holds them`) {
				t.Errorf("want it said, got:\n%s", out)
			}
		})
	}
}

// demoRunsOf is the `run` lines of one service of project demo.
func demoRunsOf(all []string, svc string) []string {
	var mine []string
	for _, l := range all {
		if strings.Contains(l, " --name "+svc+".demo.opossum ") {
			mine = append(mine, l)
		}
	}
	return mine
}

// Only the aliases the refusal names are left out of the run made again; the
// others are still passed, so a service that holds one of them goes on holding
// it. The warning names those left out.
func TestUpRunsAgainWithoutOnlyTheHeldAliases(t *testing.T) {
	_, all, out, err := aliasUpIn(t, "demo", "1.6.1", "opossum", map[string]*compose.Service{
		"db": aliased([]string{"back", "front"}, map[string][]string{"back": {"cache", "kv"}, "front": {"www"}}),
	}, "RUN_ALIAS_HELD=kv.demo.opossum")
	if err != nil {
		t.Fatalf("up should go on, got %v\n%s", err, out)
	}
	mine := demoRunsOf(all, "db")
	if len(mine) != 2 {
		t.Fatalf("want two runs, got %q", mine)
	}
	for _, want := range []string{"--network=demo-back,alias=cache.demo.opossum ", "--network=demo-front,alias=www.demo.opossum "} {
		if !strings.Contains(mine[1], want) {
			t.Errorf("want %q kept in the run made again, got %q", want, mine[1])
		}
	}
	if strings.Contains(mine[1], "alias=kv.") {
		t.Errorf("want the held alias left out, got %q", mine[1])
	}
	if !strings.Contains(out, `network alias(es) "kv" not passed`) {
		t.Errorf("want the held alias named, got:\n%s", out)
	}
}

// A refusal whose list of aliases does not read as one names none of them, and
// the run is made again without any alias: what is held cannot be told apart.
func TestUpRunsAgainWithoutEveryAliasWhenTheListDoesNotRead(t *testing.T) {
	_, all, out, err := aliasUpIn(t, "demo", "1.6.1", "opossum", map[string]*compose.Service{
		"db": aliased([]string{"back"}, map[string][]string{"back": {"cache", "kv"}}),
	}, "RUN_ALIAS_HELD=kv.demo.opossum", "RUN_ALIAS_HELD_UNQUOTED=1")
	if err != nil {
		t.Fatalf("up should go on, got %v\n%s", err, out)
	}
	mine := demoRunsOf(all, "db")
	if len(mine) != 2 || strings.Contains(mine[1], "alias=") {
		t.Fatalf("want a run made again with no alias, got %q", mine)
	}
	if !strings.Contains(out, `network alias(es) "cache", "kv" not passed`) {
		t.Errorf("want every alias named, got:\n%s", out)
	}
}

// A refusal whose list reads but names none of the aliases the run passed
// cannot say which is held, so the run is made again without any.
func TestUpRunsAgainWithoutEveryAliasWhenTheListNamesNoneOfThem(t *testing.T) {
	_, all, out, err := aliasUpIn(t, "demo", "1.6.1", "opossum", map[string]*compose.Service{
		"db": aliased([]string{"back"}, map[string][]string{"back": {"cache", "kv"}}),
	}, "RUN_ALIAS_HELD_OTHER=other.demo.opossum")
	if err != nil {
		t.Fatalf("up should go on, got %v\n%s", err, out)
	}
	mine := demoRunsOf(all, "db")
	if len(mine) != 2 || strings.Contains(mine[1], "alias=") {
		t.Fatalf("want a run made again with no alias, got %q", mine)
	}
	if !strings.Contains(out, `network alias(es) "cache", "kv" not passed`) {
		t.Errorf("want every alias named, got:\n%s", out)
	}
}

// The run made again is made once: when it fails too, up fails with its error,
// and nothing is run a third time.
func TestUpFailsWhenTheRunMadeAgainFails(t *testing.T) {
	_, all, out, err := aliasUpIn(t, "demo", "1.6.1", "opossum", map[string]*compose.Service{
		"db": aliased([]string{"back"}, map[string][]string{"back": {"cache"}}),
	}, "RUN_ALIAS_HELD=cache.demo.opossum", "RUN_REFUSE_AFTER_ALIASES=1")
	if err == nil {
		t.Fatalf("up should fail when the run made again fails\n%s", out)
	}
	if mine := demoRunsOf(all, "db"); len(mine) != 2 {
		t.Errorf("want two runs, got %q", mine)
	}
}

// A refusal line that came after the run made its container is not the
// runtime's refusal (which makes nothing), and the run is not made again.
func TestUpDoesNotRunAgainWhenTheRunMadeAContainer(t *testing.T) {
	_, all, out, err := aliasUpIn(t, "demo", "1.6.1", "opossum", map[string]*compose.Service{
		"db": aliased([]string{"back"}, map[string][]string{"back": {"cache"}}),
	}, "RUN_ALIAS_HELD=cache.demo.opossum", "RUN_ALIAS_HELD_MADE=1")
	if err == nil {
		t.Fatalf("up should fail\n%s", out)
	}
	if mine := demoRunsOf(all, "db"); len(mine) != 1 {
		t.Errorf("want one run, got %q", mine)
	}
}

// Any other failure is not the aliases', and the run is not made again.
func TestUpDoesNotRunAgainForAnotherFailure(t *testing.T) {
	_, all, out, err := aliasUpIn(t, "demo", "1.6.1", "opossum", map[string]*compose.Service{
		"db": aliased([]string{"back"}, map[string][]string{"back": {"cache"}}),
	}, "RUN_HOSTNAME_HELD=db.demo.opossum")
	if err == nil {
		t.Fatalf("up should fail\n%s", out)
	}
	if len(all) != 1 {
		t.Errorf("want one run, got %q", all)
	}
	if strings.Contains(out, "not passed: another container already holds them") {
		t.Errorf("not an alias failure, got:\n%s", out)
	}
}

// A service whose name another container holds as an alias is refused by the
// runtime; up says why and what to do (OPSM-217).
func TestUpSaysWhenAServiceNameIsHeldAsAnAlias(t *testing.T) {
	_, _, out, err := aliasUpIn(t, "demo", "1.6.1", "opossum", map[string]*compose.Service{
		"cache": {Image: "redis:7", Networks: []string{"back"}},
	}, "RUN_HOSTNAME_HELD=cache.demo.opossum")
	if err == nil {
		t.Fatalf("up should fail\n%s", out)
	}
	if !strings.Contains(err.Error()+out, "[OPSM-217] another container holds this service's name as a network alias") {
		t.Errorf("want OPSM-217, got %v\n%s", err, out)
	}
}

// The run made without the aliases carries the fingerprint of the one asked
// for, so the next `up` leaves the container as it is rather than meeting the
// same refusal on every `up`.
func TestARunWithoutHeldAliasesIsUpToDateNextTime(t *testing.T) {
	rt, log := fakeShim(t)
	rt.Env = append(rt.Env, "CLIENT_VERSION=1.6.1", "RUN_ALIAS_HELD=cache.demo.opossum")
	p := project("demo", map[string]*compose.Service{
		"db": aliased([]string{"back"}, map[string][]string{"back": {"cache"}}),
	})
	p.Networks = map[string]compose.NetworkDecl{"back": {}}
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatal(err)
	}
	runs := func() int {
		n := 0
		for _, l := range log() {
			if strings.HasPrefix(l, "run ") {
				n++
			}
		}
		return n
	}
	before := runs()
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatal(err)
	}
	if runs() != before || !strings.Contains(out.String(), "db is up to date") {
		t.Errorf("the second up should leave it as it is, got %d runs after %d:\n%s", runs(), before, out.String())
	}
}
