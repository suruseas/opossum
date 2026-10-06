package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// `default` is the one network a service with no `networks:` and a service that
// lists `default` are both on, whether or not the file declares it, and its
// declaration is that network's (docker compose v5.5.1: one `<project>_default`,
// or the `name:` it is given, `internal` and `labels` on it; an external one is
// used as it is).

// defaultProject has a service that lists no networks, one that lists `default`
// and one that lists `default` and another network, so an entry on the wrong
// network shows.
func defaultProject(decl *compose.NetworkDecl) *compose.Project {
	p := project("demo", map[string]*compose.Service{
		"a": {Image: "alpine:3.20"},
		"b": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"default"}},
		"c": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"default", "other"}},
	})
	p.Networks = map[string]compose.NetworkDecl{"other": {}}
	if decl != nil {
		p.Networks["default"] = *decl
	}
	return p
}

// runOf is the `run` line of a service's container, or "".
func runOf(lines []string, service string) string {
	for _, l := range lines {
		if strings.HasPrefix(l, "run ") && strings.Contains(l, " --name "+service+".demo.opossum ") {
			return l
		}
	}
	return ""
}

func TestServicesOnTheDefaultNetworkAreOnOneNetwork(t *testing.T) {
	for _, tc := range []struct {
		name string
		decl *compose.NetworkDecl
	}{
		{"the file does not declare it", nil},
		{"the file declares it with nothing in it", &compose.NetworkDecl{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			if err := orchestrator.New(defaultProject(tc.decl), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
				t.Fatalf("Up: %v", err)
			}
			if n := countLines(log(), "network create demo-net"); n != 1 {
				t.Errorf("want demo-net made once, got %d: %v", n, log())
			}
			if indexOf(log(), "demo-default") >= 0 {
				t.Errorf("the network was also made under its key, got %v", log())
			}
			for svc, want := range map[string]string{"a": " --network=demo-net ", "b": " --network=demo-net ", "c": " --network=demo-net --network=demo-other "} {
				if l := runOf(log(), svc); !strings.Contains(l, want) {
					t.Errorf("want %s run with%q, got %q", svc, want, l)
				}
			}
		})
	}
}

func TestTheDefaultNetworksDeclarationIsThatNetworks(t *testing.T) {
	t.Run("a name of its own", func(t *testing.T) {
		rt, log := fakeShim(t)
		if err := orchestrator.New(defaultProject(&compose.NetworkDecl{Name: "shared"}), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if !hasLine(log(), "network create --label=opossum.project=demo shared") {
			t.Errorf("want shared made with the project's label, got %v", log())
		}
		if indexOf(log(), "network create demo-net") >= 0 {
			t.Errorf("want no demo-net, got %v", log())
		}
		for _, svc := range []string{"a", "b", "c"} {
			if l := runOf(log(), svc); !strings.Contains(l, " --network=shared ") {
				t.Errorf("want %s on shared, got %q", svc, l)
			}
		}
		// down removes it because the project's label is on it.
		if err := orchestrator.New(defaultProject(&compose.NetworkDecl{Name: "shared"}), rt, "opossum", &bytes.Buffer{}).Down(false, "", false); err != nil {
			t.Fatalf("Down: %v", err)
		}
		if !hasLine(log(), "network delete shared") {
			t.Errorf("want down to remove shared, got %v", log())
		}
	})
	t.Run("a name of its own that is there and is not the project's is left", func(t *testing.T) {
		rt, log := fakeShim(t)
		setShimEnv(rt, "NETWORK_LABELS=shared=")
		if err := orchestrator.New(defaultProject(&compose.NetworkDecl{Name: "shared"}), rt, "opossum", &bytes.Buffer{}).Down(false, "", false); err != nil {
			t.Fatalf("Down: %v", err)
		}
		if hasLine(log(), "network delete shared") {
			t.Errorf("want shared left, got %v", log())
		}
	})
	t.Run("host-only", func(t *testing.T) {
		rt, log := fakeShim(t)
		var out bytes.Buffer
		if err := orchestrator.New(defaultProject(&compose.NetworkDecl{Internal: true}), rt, "opossum", &out).Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if !hasLine(log(), "network create --internal demo-net") {
			t.Errorf("want demo-net made host-only, got %v", log())
		}
		if !strings.Contains(out.String(), "network demo-net is internal") {
			t.Errorf("want the host-only warning, got:\n%s", out.String())
		}
	})
	t.Run("labels and a subnet", func(t *testing.T) {
		rt, log := fakeShim(t)
		d := compose.NetworkDecl{Labels: compose.Labels{"k=v"}}
		d.IPAM.Subnet = "10.7.0.0/24"
		if err := orchestrator.New(defaultProject(&d), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if !hasLine(log(), "network create --label=k=v --subnet 10.7.0.0/24 demo-net") {
			t.Errorf("want the labels and the subnet on demo-net, got %v", log())
		}
	})
	t.Run("external, by the name it gives", func(t *testing.T) {
		rt, log := fakeShim(t)
		if err := orchestrator.New(defaultProject(&compose.NetworkDecl{External: true, Name: "ext"}), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		// Only `other`, which c lists, is made: the external one is not ours to make.
		if n := countLines(log(), "network create"); n != 1 || !hasLine(log(), "network create demo-other") {
			t.Errorf("want demo-other made and nothing else, got %v", log())
		}
		for svc, want := range map[string]string{"a": " --network=ext ", "b": " --network=ext ", "c": " --network=ext --network=demo-other "} {
			if l := runOf(log(), svc); !strings.Contains(l, want) {
				t.Errorf("want %s run with%q, got %q", svc, want, l)
			}
		}
	})
	t.Run("external, by the name `default` when it gives none", func(t *testing.T) {
		rt, log := fakeShim(t)
		if err := orchestrator.New(defaultProject(&compose.NetworkDecl{External: true}), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if l := runOf(log(), "a"); !strings.Contains(l, " --network=default ") {
			t.Errorf("want a on `default`, got %q", l)
		}
	})
	t.Run("external and not there, asked of a service that lists no networks", func(t *testing.T) {
		rt, log := fakeShim(t)
		setShimEnv(rt, "NETWORK_ABSENT=ext")
		p := project("demo", map[string]*compose.Service{"a": {Image: "alpine:3.20"}})
		p.Networks = map[string]compose.NetworkDecl{"default": {External: true, Name: "ext"}}
		err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
		if err == nil || !strings.Contains(err.Error(), `network "ext" is declared `+"`external: true`"+` but doesn't exist`) {
			t.Fatalf("want the missing external network refused, got %v", err)
		}
		if runLine(log()) >= 0 {
			t.Errorf("want nothing started, got %v", log())
		}
	})
}

// A network an earlier version made for a declared `default` that services
// listed (`<project>-default`) is still removed, beside the default one.
func TestTheNetworkAnEarlierVersionMadeForAListedDefaultIsStillRemoved(t *testing.T) {
	t.Run("down", func(t *testing.T) {
		rt, log := fakeShim(t)
		if err := orchestrator.New(defaultProject(&compose.NetworkDecl{}), rt, "opossum", &bytes.Buffer{}).Down(false, "", false); err != nil {
			t.Fatalf("Down: %v", err)
		}
		if !hasLine(log(), "network delete demo-net") || !hasLine(log(), "network delete demo-default") {
			t.Errorf("want demo-net and demo-default removed, got %v", log())
		}
	})
	// A fresh runtime, as though this were the first command run against a
	// project an earlier version left demo-default on: `down`, above, having
	// just removed it is not this row's starting state.
	t.Run("destroy", func(t *testing.T) {
		rt, _ := fakeShim(t)
		plan, err := orchestrator.New(defaultProject(&compose.NetworkDecl{}), rt, "opossum", &bytes.Buffer{}).DestroyPlanFor(false, true, false)
		if err != nil {
			t.Fatalf("DestroyPlanFor: %v", err)
		}
		if got := strings.Join(plan.Networks, " "); got != "demo-default demo-net demo-other" {
			t.Errorf("plan.Networks: got %q", got)
		}
	})
}

// Only the default network's own declaration is its: another network that is
// host-only does not make it so.
func TestAnotherNetworksSettingsAreNotTheDefaultNetworks(t *testing.T) {
	rt, log := fakeShim(t)
	p := defaultProject(&compose.NetworkDecl{})
	p.Networks["other"] = compose.NetworkDecl{Internal: true, Labels: compose.Labels{"o=1"}}
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !hasLine(log(), "network create demo-net") || !hasLine(log(), "network create --internal --label=o=1 demo-other") {
		t.Errorf("want demo-net plain and demo-other host-only with its label, got %v", log())
	}
}

// A `name:` the runtime cannot create on the default network is refused before
// anything is made, as on any other network, whether or not a service lists it.
func TestTheDefaultNetworksNameIsHeldToWhatTheRuntimeTakes(t *testing.T) {
	for _, path := range []string{"up", "run"} {
		t.Run(path, func(t *testing.T) {
			rt, log := fakeShim(t)
			p := project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20"}})
			p.Networks = map[string]compose.NetworkDecl{"default": {Name: "Bad_Name+"}}
			err := invokePath(orchestrator.New(p, rt, "opossum", &bytes.Buffer{}), path)
			if err == nil || !strings.Contains(err.Error(), "network \"default\" has `name: Bad_Name+`, which the container runtime (1.4.1) cannot create") {
				t.Fatalf("want the name refused, got %v", err)
			}
			if indexOf(log(), "network create") >= 0 || runLine(log()) >= 0 {
				t.Errorf("want nothing made, got %v", log())
			}
		})
	}
}

// A network that is there was made in one mode, and a later `up` does not
// change it. A file that declares the other one is refused, with what to do,
// rather than run on a network it does not describe (a service that was
// host-only on egress it should not have) with a warning that says otherwise.
func TestAnExistingNetworkInAnotherModeIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name     string
		internal bool
		mode     string // $NETWORK_MODES for demo-net; "" = the runtime names none
		want     string // "" = goes ahead
	}{
		{"declared host-only, made host-only", true, "hostOnly", ""},
		{"declared plain, made plain", false, "nat", ""},
		{"declared host-only, made plain", true, "nat", `[OPSM-207] network "demo-net" exists as a not host-only network, and the compose file now declares it host-only (` + "`internal: true`" + `)`},
		{"declared plain, made host-only", false, "hostOnly", `[OPSM-207] network "demo-net" exists as a host-only network, and the compose file now declares it not host-only`},
		{"declared host-only, the runtime names no mode", true, "", ""},
		{"declared plain, a mode this does not know", false, "bridged", ""},
	} {
		for _, path := range []string{"up", "run"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				rt, log := fakeShim(t)
				env := []string{"NET_EXISTS=demo-net"}
				if tc.mode != "" {
					env = append(env, "NETWORK_MODES=demo-net="+tc.mode)
				}
				setShimEnv(rt, env...)
				p := project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20"}})
				p.Networks = map[string]compose.NetworkDecl{"default": {Internal: tc.internal}}
				err := invokePath(orchestrator.New(p, rt, "opossum", &bytes.Buffer{}), path)
				if tc.want == "" {
					if err != nil {
						t.Fatalf("%s: %v", path, err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "run `opossum down` (which removes it)") ||
					!strings.Contains(err.Error(), "match the existing network") {
					t.Fatalf("\n got %v\nwant %s, what to do, and the keep-it-as-is wording", err, tc.want)
				}
				// Not wrapped as a failure to create, and not carrying the subnet's
				// advice: the network is there, in a mode of its own, not a
				// leftover the reader should suspect and delete.
				if strings.Contains(err.Error(), "couldn't create network") || strings.Contains(err.Error(), "container network delete") || strings.Contains(err.Error(), "remove `ipam`") {
					t.Errorf("mode's OPSM-207 must not carry the stale-network or subnet advice, got: %v", err)
				}
				if runLine(log()) >= 0 {
					t.Errorf("want nothing started on a network the file does not describe, got %v", log())
				}
			})
		}
	}
	t.Run("a network under a name that is not the project's", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "NET_EXISTS=shared", "NETWORK_MODES=shared=nat", "NETWORK_LABELS=shared=")
		p := project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20"}})
		p.Networks = map[string]compose.NetworkDecl{"default": {Name: "shared", Internal: true}}
		err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
		if err == nil || !strings.Contains(err.Error(), "does not remove it: remove it yourself (`container network delete shared`)") {
			t.Fatalf("want the advice that works for a network down leaves, got %v", err)
		}
	})
}
