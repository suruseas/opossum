package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A network declared with a `name:` of its own is the network of that name, as
// docker compose creates it — not `<project>-<key>`. The project's label goes on
// it when it is made, and that label is what lets `down` and `destroy` remove it:
// a network of that name that is already there may be anyone's.

// ownNameProject has a service on a network `n` that carries `name: shared` and a
// second service on the default network, so a name given to the wrong one of the
// two shows.
func ownNameProject() *compose.Project {
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"n"}},
		"db":  {Image: "alpine:3.20"},
	})
	p.Networks = map[string]compose.NetworkDecl{"n": {Name: "shared"}}
	return p
}

func TestANetworkWithANameOfItsOwnIsMadeAndJoinedUnderThatName(t *testing.T) {
	for _, path := range []string{"up", "run"} {
		t.Run(path, func(t *testing.T) {
			rt, log := fakeShim(t)
			if err := invokePath(orchestrator.New(ownNameProject(), rt, "opossum", &bytes.Buffer{}), path); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			if !hasLine(log(), "network create --label opossum.project=demo shared") {
				t.Errorf("want the network made as `shared` with the project's label, got %v", log())
			}
			if i := indexOf(log(), "--name web"); i < 0 || !strings.Contains(log()[i], " --network shared ") {
				t.Errorf("want web run on `shared`, got %v", log())
			}
			if i := indexOf(log(), "--name db."); path == "up" && (i < 0 || !strings.Contains(log()[i], " --network demo-net ")) {
				t.Errorf("want db left on the default network, got %v", log())
			}
			if hasLine(log(), "network create demo-n") || hasLine(log(), "network create --label opossum.project=demo demo-n") {
				t.Errorf("the network was also made under its key's name, got %v", log())
			}
		})
	}
	t.Run("the declaration's labels come before the project's", func(t *testing.T) {
		rt, log := fakeShim(t)
		p := ownNameProject()
		p.Networks["n"] = compose.NetworkDecl{Name: "shared", Internal: true, Labels: compose.Labels{"tier=back"}}
		if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if !hasLine(log(), "network create --internal --label tier=back --label opossum.project=demo shared") {
			t.Errorf("want the declaration's flags and labels kept, and the project's added, got %v", log())
		}
	})
	t.Run("a network with no name of its own is made as it was", func(t *testing.T) {
		rt, log := fakeShim(t)
		p := ownNameProject()
		p.Networks["n"] = compose.NetworkDecl{}
		if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if !hasLine(log(), "network create demo-n") || !hasLine(log(), "network create demo-net") {
			t.Errorf("want demo-n and demo-net made with no label, got %v", log())
		}
	})
	t.Run("an external network's name is still the network it uses, and it is not made", func(t *testing.T) {
		rt, log := fakeShim(t)
		p := ownNameProject()
		p.Networks["n"] = compose.NetworkDecl{Name: "shared", External: true}
		if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if indexOf(log(), "network create --label") >= 0 || indexOf(log(), "network create shared") >= 0 {
			t.Errorf("want nothing made for an external network, got %v", log())
		}
	})
}

// Two keys with the same `name:` and the same declaration are one network, made
// once.
func TestTwoKeysWithOneNameAndOneDeclarationAreOneNetwork(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"x"}},
		"db":  {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"y"}},
	})
	p.Networks = map[string]compose.NetworkDecl{"x": {Name: "shared"}, "y": {Name: "shared"}}
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if n := countLines(log(), "network create"); n != 1 {
		t.Errorf("want one network made, got %d: %v", n, log())
	}
	var down []string
	rt2, log2 := fakeShim(t)
	if err := orchestrator.New(p, rt2, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if err := orchestrator.New(p, rt2, "opossum", &bytes.Buffer{}).Down(false, "", false); err != nil {
		t.Fatalf("Down: %v", err)
	}
	for _, l := range log2() {
		if strings.HasPrefix(l, "network delete ") {
			down = append(down, l)
		}
	}
	// demo-x and demo-y are what an earlier version made of the two keys.
	if strings.Join(down, "|") != "network delete demo-net|network delete demo-x|network delete demo-y|network delete shared" {
		t.Errorf("want the shared network deleted once (after the default one and the earlier version's), got %v", down)
	}
}

// What `down` does with the network under the name depends on whose it is, and
// the only proof is the label an `up` of this project put on it.
func TestDownAndDestroyRemoveANamedNetworkOnlyWhenTheProjectMadeIt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels string // $NETWORK_LABELS: how the network under the name is already there
		remove bool
	}{
		{"made by an up of this project", "shared=opossum.project=demo", true},
		{"made by another project", "shared=opossum.project=other", false},
		{"made by hand, with no label", "shared=", false},
		{"made by another tool, with a label of its own", "shared=com.docker.compose.project=demo", false},
		{"the runtime does not say", "", false},
		{"this project's label among others", "shared=tier=back,opossum.project=demo", true},
	} {
		t.Run("down/"+tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, "NETWORK_LABELS="+tc.labels)
			if err := orchestrator.New(ownNameProject(), rt, "opossum", &bytes.Buffer{}).Down(false, "", false); err != nil {
				t.Fatalf("Down: %v", err)
			}
			if got := hasLine(log(), "network delete shared"); got != tc.remove {
				t.Errorf("network delete shared: got %v, want %v (%v)", got, tc.remove, log())
			}
			if !hasLine(log(), "network delete demo-net") {
				t.Errorf("the default network is deleted whatever the named one is, got %v", log())
			}
		})
		t.Run("destroy/"+tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			setShimEnv(rt, "NETWORK_LABELS="+tc.labels)
			plan, err := orchestrator.New(ownNameProject(), rt, "opossum", &bytes.Buffer{}).DestroyPlanFor(false, true, false)
			if err != nil {
				t.Fatalf("DestroyPlanFor: %v", err)
			}
			// demo-n is what an earlier version made of the key; the shim answers
			// that it is there.
			want := "demo-n demo-net"
			if tc.remove {
				want = "demo-n demo-net shared"
			}
			if got := strings.Join(plan.Networks, " "); got != want {
				t.Errorf("plan.Networks: got %q, want %q", got, want)
			}
		})
	}
	// An external network is somebody else's whatever label it carries.
	t.Run("an external network carrying the project's label", func(t *testing.T) {
		newProject := func() *compose.Project {
			p := ownNameProject()
			p.Networks["n"] = compose.NetworkDecl{Name: "shared", External: true}
			return p
		}
		rt, log := fakeShim(t)
		setShimEnv(rt, "NETWORK_LABELS=shared=opossum.project=demo")
		if err := orchestrator.New(newProject(), rt, "opossum", &bytes.Buffer{}).Down(false, "", false); err != nil {
			t.Fatalf("Down: %v", err)
		}
		if hasLine(log(), "network delete shared") {
			t.Errorf("want an external network left alone, got %v", log())
		}
		plan, err := orchestrator.New(newProject(), rt, "opossum", &bytes.Buffer{}).DestroyPlanFor(false, true, false)
		if err != nil {
			t.Fatalf("DestroyPlanFor: %v", err)
		}
		if strings.Contains(" "+strings.Join(plan.Networks, " ")+" ", " shared ") {
			t.Errorf("want an external network out of the plan, got %v", plan.Networks)
		}
	})
	t.Run("a network that is not there is not listed", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "NETWORK_LABELS=shared=opossum.project=demo", "NETWORK_ABSENT=shared demo-n")
		plan, err := orchestrator.New(ownNameProject(), rt, "opossum", &bytes.Buffer{}).DestroyPlanFor(false, true, false)
		if err != nil {
			t.Fatalf("DestroyPlanFor: %v", err)
		}
		if got := strings.Join(plan.Networks, " "); got != "demo-net" {
			t.Errorf("plan.Networks: got %q, want the default network alone", got)
		}
	})
}

// The label `up` writes is the label `down` reads: end to end, on the fake
// runtime that remembers what a create was given.
func TestTheLabelUpPutsOnANamedNetworkIsTheOneDownReads(t *testing.T) {
	rt, log := fakeShim(t)
	if err := orchestrator.New(ownNameProject(), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if err := orchestrator.New(ownNameProject(), rt, "opossum", &bytes.Buffer{}).Down(false, "", false); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if !hasLine(log(), "network delete shared") {
		t.Errorf("want down to remove the network its up made, got %v", log())
	}
	// Under another project's name, the same file leaves it.
	rt, log = fakeShim(t)
	if err := orchestrator.New(ownNameProject(), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	other := ownNameProject()
	other.Name = "other"
	if err := orchestrator.New(other, rt, "opossum", &bytes.Buffer{}).Down(false, "", false); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if hasLine(log(), "network delete shared") {
		t.Errorf("want another project's down to leave it, got %v", log())
	}
}

// A network made by a version that read `name:` past is `<project>-<key>`, and
// that name is opossum's alone: `down` and `destroy` remove it, with or without
// a label on it, and whether or not the file still names the network.
func TestTheNetworkAnEarlierVersionMadeForANamedKeyIsStillRemoved(t *testing.T) {
	t.Run("down", func(t *testing.T) {
		rt, log := fakeShim(t)
		setShimEnv(rt, "NETWORK_LABELS=shared=")
		if err := orchestrator.New(ownNameProject(), rt, "opossum", &bytes.Buffer{}).Down(false, "", false); err != nil {
			t.Fatalf("Down: %v", err)
		}
		if !hasLine(log(), "network delete demo-n") {
			t.Errorf("want the earlier version's demo-n removed, got %v", log())
		}
		if hasLine(log(), "network delete shared") {
			t.Errorf("the unlabelled network `shared` is not this version's to remove, got %v", log())
		}
	})
	t.Run("destroy", func(t *testing.T) {
		rt, _ := fakeShim(t)
		plan, err := orchestrator.New(ownNameProject(), rt, "opossum", &bytes.Buffer{}).DestroyPlanFor(false, true, false)
		if err != nil {
			t.Fatalf("DestroyPlanFor: %v", err)
		}
		if got := strings.Join(plan.Networks, " "); got != "demo-n demo-net" {
			t.Errorf("plan.Networks: got %q, want the earlier version's network listed", got)
		}
	})
}

// A network that was already there is used, and a failed `up` does not remove
// it; one this `up` made is removed with the rest.
func TestAFailedUpRemovesANamedNetworkOnlyIfItMadeIt(t *testing.T) {
	for _, tc := range []struct {
		name, exists string
		removed      bool
	}{
		{"it made the network", "", true},
		{"the network was there", "shared", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, "RUN_FAIL=web.demo.opossum", "NET_EXISTS="+tc.exists)
			if err := orchestrator.New(ownNameProject(), rt, "opossum", &bytes.Buffer{}).Up(true); err == nil {
				t.Fatal("want the up to fail")
			}
			if got := hasLine(log(), "network delete shared"); got != tc.removed {
				t.Errorf("network delete shared: got %v, want %v (%v)", got, tc.removed, log())
			}
		})
	}
}

// A name the runtime cannot create is refused before anything is made, on every
// path that makes networks, with what it takes; a name it takes is not, and
// `down` and `destroy` read such a file to clean up.
func TestANameTheRuntimeCannotCreateIsRefusedBeforeAnythingIsMade(t *testing.T) {
	for _, name := range []string{"Shared", "sha+red", ".shared", "shared-", strings.Repeat("k", 64)} {
		for _, path := range append([]string{"up", "up web"}, runPaths...) {
			t.Run(path+"/"+name, func(t *testing.T) {
				rt, log := fakeShim(t)
				p := ownNameProject()
				p.Networks["n"] = compose.NetworkDecl{Name: name}
				err := invokePath(orchestrator.New(p, rt, "opossum", &bytes.Buffer{}), path)
				if err == nil || !strings.Contains(err.Error(), "`name: "+name+"`, which the container runtime (1.4.1) cannot create") {
					t.Fatalf("want the name refused, got %v", err)
				}
				if indexOf(log(), "network create") >= 0 || runLine(log()) >= 0 {
					t.Errorf("want nothing made before the refusal, got %v", log())
				}
			})
		}
	}
	t.Run("63 characters is taken", func(t *testing.T) {
		rt, log := fakeShim(t)
		p := ownNameProject()
		long := strings.Repeat("k", 63)
		p.Networks["n"] = compose.NetworkDecl{Name: long}
		if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if !hasLine(log(), "network create --label opossum.project=demo "+long) {
			t.Errorf("want the network made, got %v", log())
		}
	})
	t.Run("down and destroy read the file", func(t *testing.T) {
		p := ownNameProject()
		p.Networks["n"] = compose.NetworkDecl{Name: "Shared"}
		rt, _ := fakeShim(t)
		if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Down(false, "", false); err != nil {
			t.Errorf("Down: %v", err)
		}
		if _, err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).DestroyPlanFor(false, true, false); err != nil {
			t.Errorf("DestroyPlanFor: %v", err)
		}
	})
	// The name is asked of the networks about to be made: a service the command
	// does not start does not refuse it (docker compose creates it when that
	// service is started, and this refuses it then).
	t.Run("a service that is not started", func(t *testing.T) {
		newProject := func(profiles []string) *compose.Project {
			p := project("demo", map[string]*compose.Service{
				"api": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "db"}}},
				"db":  {Image: "alpine:3.20"},
				"web": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"n"}, Profiles: profiles},
			})
			p.Networks = map[string]compose.NetworkDecl{"n": {Name: "Shared"}}
			return p
		}
		for _, tc := range []struct {
			name string
			call func(o *orchestrator.Orchestrator) error
			prof []string
		}{
			{"up api", func(o *orchestrator.Orchestrator) error { return o.Up(true, "api") }, nil},
			{"run api", func(o *orchestrator.Orchestrator) error {
				return o.RunOneOff("api", []string{"true"}, orchestrator.RunOneOffOptions{})
			}, nil},
			{"run --audit api", func(o *orchestrator.Orchestrator) error {
				_, err := o.RunAudited("api", []string{"true"}, orchestrator.RunOneOffOptions{})
				return err
			}, nil},
			{"up with web behind a profile that is not active", func(o *orchestrator.Orchestrator) error { return o.Up(true) }, []string{"debug"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				rt, log := fakeShim(t)
				if err := tc.call(orchestrator.New(newProject(tc.prof), rt, "opossum", &bytes.Buffer{})); err != nil || runLine(log()) < 0 {
					t.Errorf("want it run, got err %v and %v", err, log())
				}
			})
		}
	})
	t.Run("a network no service joins is not refused", func(t *testing.T) {
		rt, _ := fakeShim(t)
		p := ownNameProject()
		p.Networks["unused"] = compose.NetworkDecl{Name: "Unused+"}
		if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Errorf("Up: %v", err)
		}
	})
}

// Networks that would come to one runtime network are refused before anything
// is made, unless they are one declaration under one name.
func TestNetworksThatWouldBeOneRuntimeNetworkAreRefusedUnlessTheyAreOne(t *testing.T) {
	two := func(x, y compose.NetworkDecl) *compose.Project {
		p := project("demo", map[string]*compose.Service{
			"web": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"x"}},
			"db":  {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"y"}},
		})
		p.Networks = map[string]compose.NetworkDecl{"x": x, "y": y}
		return p
	}
	for _, tc := range []struct {
		name    string
		project func() *compose.Project
		want    string // "" = goes ahead
	}{
		{"same name, same declaration", func() *compose.Project {
			return two(compose.NetworkDecl{Name: "shared"}, compose.NetworkDecl{Name: "shared"})
		}, ""},
		{"same name, one host-only", func() *compose.Project {
			return two(compose.NetworkDecl{Name: "shared", Internal: true}, compose.NetworkDecl{Name: "shared"})
		}, `networks "x" and "y" both have ` + "`name: shared`" + ` and are declared differently`},
		{"same name, other labels", func() *compose.Project {
			return two(compose.NetworkDecl{Name: "shared", Labels: compose.Labels{"a=1"}}, compose.NetworkDecl{Name: "shared"})
		}, `networks "x" and "y" both have ` + "`name: shared`" + ` and are declared differently`},
		{"same name, other subnet", func() *compose.Project {
			a := compose.NetworkDecl{Name: "shared"}
			a.IPAM.Subnet = "10.9.0.0/24"
			return two(a, compose.NetworkDecl{Name: "shared"})
		}, `networks "x" and "y" both have ` + "`name: shared`" + ` and are declared differently`},
		{"a name that is another key's runtime name", func() *compose.Project {
			return two(compose.NetworkDecl{Name: "demo-y"}, compose.NetworkDecl{})
		}, "network \"x\" has `name: demo-y`, which is the runtime name of network \"y\" (`<project>-y`)"},
		{"the other key first", func() *compose.Project {
			return two(compose.NetworkDecl{}, compose.NetworkDecl{Name: "demo-x"})
		}, "network \"y\" has `name: demo-x`, which is the runtime name of network \"x\" (`<project>-x`)"},
	} {
		for _, path := range []string{"up", "run"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				rt, log := fakeShim(t)
				err := invokePath(orchestrator.New(tc.project(), rt, "opossum", &bytes.Buffer{}), path)
				if tc.want == "" {
					if err != nil {
						t.Fatalf("%s: %v", path, err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("\n got %v\nwant %s", err, tc.want)
				}
				if indexOf(log(), "network create") >= 0 || runLine(log()) >= 0 {
					t.Errorf("want nothing made before the refusal, got %v", log())
				}
			})
		}
	}
}

// A network under a name that is there with another subnet than `ipam` declares
// is kept, and `up` says what to do. `down` removes it only if it is this
// project's, so the advice to run `down` is given only then.
func TestTheAdviceForANamedNetworkWithAnotherSubnetIsOneThatWorks(t *testing.T) {
	newProject := func() *compose.Project {
		p := ownNameProject()
		d := compose.NetworkDecl{Name: "shared"}
		d.IPAM.Subnet = "10.7.0.0/24"
		p.Networks["n"] = d
		return p
	}
	for _, tc := range []struct {
		name, labels  string
		want, notWant string
	}{
		{"made by this project", "shared=opossum.project=demo", "run `opossum down` (which removes it)", "label is not on it"},
		{"made by another project", "shared=opossum.project=other", "this project's label is not on it (or could not be read), so `opossum down` does not remove it: remove it yourself (`container network delete shared`)", "run `opossum down`"},
		{"made by hand", "shared=", "label is not on it", "run `opossum down`"},
		{"the runtime does not say whose", "", "label is not on it", "run `opossum down`"},
		// Its own name from before: `down` removes it whatever label it has, so
		// the advice to run `down` works.
		{"its own name from an earlier version", "", "run `opossum down` (which removes it)", "label is not on it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			p, exists := newProject(), "shared"
			if strings.HasPrefix(tc.name, "its own") {
				d := p.Networks["n"]
				d.Name = "demo-n"
				p.Networks["n"] = d
				exists = "demo-n"
				tc.labels = "demo-n="
			}
			// The subnets and the labels of one network are one answer.
			setShimEnv(rt, "NET_EXISTS="+exists, "NETWORK_SUBNETS="+exists+"=10.9.0.0/24", "NETWORK_LABELS="+tc.labels)
			err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
			if err == nil || !strings.Contains(err.Error(), "[OPSM-207]") || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), tc.notWant) {
				t.Fatalf("\n got %v\nwant it to hold %q and not %q", err, tc.want, tc.notWant)
			}
			if runLine(log()) >= 0 {
				t.Errorf("want nothing started, got %v", log())
			}
		})
	}
}

// Networks under a name are removed in name order after the default one, so
// what `down` does is the same on every run.
func TestDownRemovesNamedNetworksInNameOrder(t *testing.T) {
	for i := 0; i < 5; i++ {
		rt, log := fakeShim(t)
		p := project("demo", map[string]*compose.Service{
			"web": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"x", "y", "z"}},
		})
		p.Networks = map[string]compose.NetworkDecl{"x": {Name: "zeta"}, "y": {Name: "alpha"}, "z": {Name: "mid"}}
		setShimEnv(rt, "NETWORK_LABELS=zeta=opossum.project=demo alpha=opossum.project=demo mid=opossum.project=demo", "NETWORK_ABSENT=demo-x demo-y demo-z")
		if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Down(false, "", false); err != nil {
			t.Fatalf("Down: %v", err)
		}
		var del []string
		for _, l := range log() {
			if strings.HasPrefix(l, "network delete ") {
				del = append(del, strings.TrimPrefix(l, "network delete "))
			}
		}
		if got := strings.Join(del, " "); got != "demo-net demo-x demo-y demo-z alpha mid zeta" {
			t.Fatalf("run %d: got %s", i, got)
		}
	}
}

// `run --audit` starts a dependency through an `up` that is after the snapshot,
// so a dependency's network whose `name:` the runtime cannot create is refused
// there: the snapshot is made first, and nothing is started.
func TestAnAuditedRunRefusesADependencysBadNetworkNameWhenTheDependenciesStart(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"api": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "db"}}},
		"db":  {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"n"}},
	})
	p.Networks = map[string]compose.NetworkDecl{"n": {Name: "Shared"}}
	_, err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).RunAudited("api", []string{"true"}, orchestrator.RunOneOffOptions{})
	if err == nil || !strings.Contains(err.Error(), "`name: Shared`, which the container runtime (1.4.1) cannot create") {
		t.Fatalf("want the dependency's network refused, got %v", err)
	}
	if !strings.Contains(err.Error(), "starting dependencies") {
		t.Errorf("want it refused where the dependencies start, got %v", err)
	}
	if indexOf(log(), "network create") >= 0 || runLine(log()) >= 0 {
		t.Errorf("want nothing made, got %v", log())
	}
}
