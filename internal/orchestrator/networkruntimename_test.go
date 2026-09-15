package orchestrator_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/workspace"
)

// A declared key the runtime refuses as written (`backEnd`) is folded to a name
// it creates, the same one on every path that names the network: the create
// and the `--network` of `up`, a one-off run, `down`, and what destroy lists.
func TestADeclaredNetworkKeyIsFoldedOnEveryPathThatNamesIt(t *testing.T) {
	newProject := func() *compose.Project {
		p := project("demo", map[string]*compose.Service{
			"web": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"backEnd"}},
		})
		p.Networks = map[string]compose.NetworkDecl{"backEnd": {}}
		return p
	}
	onlyFolded := func(t *testing.T, lines []string) {
		t.Helper()
		if i := indexOf(lines, "backEnd"); i >= 0 {
			t.Errorf("the key reached the runtime as written: %s", lines[i])
		}
	}
	t.Run("up", func(t *testing.T) {
		rt, log := fakeShim(t)
		if err := orchestrator.New(newProject(), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if !hasLine(log(), "network create demo-backend") {
			t.Errorf("want the network created as demo-backend, got %v", log())
		}
		if i := runLine(log()); i < 0 || !strings.Contains(log()[i], " --network demo-backend ") {
			t.Errorf("want the service run on demo-backend, got %v", log())
		}
		onlyFolded(t, log())
	})
	t.Run("run", func(t *testing.T) {
		rt, log := fakeShim(t)
		if err := orchestrator.New(newProject(), rt, "opossum", &bytes.Buffer{}).RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{}); err != nil {
			t.Fatalf("RunOneOff: %v", err)
		}
		if !hasLine(log(), "network create demo-backend") {
			t.Errorf("want the one-off run's network created as demo-backend, got %v", log())
		}
		onlyFolded(t, log())
	})
	t.Run("down", func(t *testing.T) {
		rt, log := fakeShim(t)
		if err := orchestrator.New(newProject(), rt, "opossum", &bytes.Buffer{}).Down(false, "", false); err != nil {
			t.Fatalf("Down: %v", err)
		}
		if !hasLine(log(), "network delete demo-backend") {
			t.Errorf("want down to delete demo-backend, got %v", log())
		}
		onlyFolded(t, log())
	})
	t.Run("destroy", func(t *testing.T) {
		rt, _ := fakeShim(t)
		plan, err := orchestrator.New(newProject(), rt, "opossum", &bytes.Buffer{}).DestroyPlanFor(false, true, false)
		if err != nil {
			t.Fatalf("DestroyPlanFor: %v", err)
		}
		if strings.Join(plan.Networks, " ") != "demo-backend demo-net" {
			t.Errorf("want destroy to list demo-backend and demo-net, got %v", plan.Networks)
		}
	})
}

// runLine is the position of the first `run` invocation, or -1.
func runLine(lines []string) int {
	for i, l := range lines {
		if strings.HasPrefix(l, "run ") {
			return i
		}
	}
	return -1
}

// invokePath runs one of the commands that create networks — the `up` of every
// service or of one, and a one-off run with and without its dependencies, audited
// or not — and returns its error.
func invokePath(o *orchestrator.Orchestrator, path string) error {
	switch path {
	case "up":
		return o.Up(true)
	case "up api":
		return o.Up(true, "api")
	case "up web":
		return o.Up(true, "web")
	case "run":
		return o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{})
	case "run --no-deps":
		return o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: true})
	case "run --audit":
		_, err := o.RunAudited("web", []string{"true"}, orchestrator.RunOneOffOptions{})
		return err
	case "run --audit --no-deps":
		_, err := o.RunAudited("web", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: true})
		return err
	}
	panic("unknown path " + path)
}

var runPaths = []string{"run", "run --no-deps", "run --audit", "run --audit --no-deps"}

// A network opossum would create under a name longer than the runtime's 63
// characters is refused before anything is created, by `up` and by a one-off
// run (audited or not, with or without its dependencies), for the default
// network and a declared one; 63 itself goes ahead, and so does a network
// opossum does not create.
func TestANetworkNameLongerThanTheRuntimeTakesIsRefusedBeforeCreating(t *testing.T) {
	onDefault := func(name string) *compose.Project {
		return project(name, map[string]*compose.Service{"web": {Image: "alpine:3.20"}})
	}
	onDeclared := func(key string) *compose.Project {
		p := project("demo", map[string]*compose.Service{
			"web": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{key}},
		})
		p.Networks = map[string]compose.NetworkDecl{key: {}}
		return p
	}
	onExternal := func(name string) *compose.Project {
		p := onDeclared("ext")
		p.Networks = map[string]compose.NetworkDecl{"ext": {External: true, Name: name}}
		return p
	}
	isolated := func(name string) *compose.Project {
		return project(name, map[string]*compose.Service{"web": {Image: "alpine:3.20", NetworkMode: compose.NetworkModeNone}})
	}
	name59 := strings.Repeat("p", 59) // + "-net" = 63
	key58 := strings.Repeat("k", 58)  // "demo-" + = 63
	// The long network second in line: after a short one on the same
	// service (a one-off run reads them in the file's order), and on the
	// second service to start (`up` reads them in name order).
	secondOfTwoNetworks := func() *compose.Project {
		p := project("demo", map[string]*compose.Service{
			"web": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"front", "z" + key58}},
		})
		p.Networks = map[string]compose.NetworkDecl{"front": {}, "z" + key58: {}}
		return p
	}
	secondOfTwoServices := func() *compose.Project {
		p := project("demo", map[string]*compose.Service{
			"api": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"front"}},
			"web": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"z" + key58}},
		})
		p.Networks = map[string]compose.NetworkDecl{"front": {}, "z" + key58: {}}
		return p
	}
	// The long network after an external one, which the check steps over.
	afterAnExternal := func() *compose.Project {
		p := project("demo", map[string]*compose.Service{
			"web": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"ext", "z" + key58}},
		})
		p.Networks = map[string]compose.NetworkDecl{"ext": {External: true, Name: "proxy"}, "z" + key58: {}}
		return p
	}
	fix := "shorten the project name (`-p`, or `name:` in the compose file)"
	longSecond := `network name "demo-z` + key58 + `" is 64 characters, and the container runtime (1.4.1) creates at most 63 — ` + fix + ` or the network key "z` + key58 + `"`
	for _, tc := range []struct {
		name    string
		project func() *compose.Project
		refusal string
		creates bool // whether opossum creates a network when it goes ahead
	}{
		{"a long network after a short one", secondOfTwoNetworks, longSecond, true},
		{"a long network on the second service", secondOfTwoServices, longSecond, true},
		{"a long network after an external one", afterAnExternal, longSecond, true},
		{"the default network at 63", func() *compose.Project { return onDefault(name59) }, "", true},
		{"the default network at 64", func() *compose.Project { return onDefault(name59 + "p") },
			`network name "` + name59 + `p-net" is 64 characters, and the container runtime (1.4.1) creates at most 63 — ` + fix, true},
		{"a declared network at 63", func() *compose.Project { return onDeclared(key58) }, "", true},
		{"a declared network at 64", func() *compose.Project { return onDeclared(key58 + "K") },
			`network name "demo-` + key58 + `k" is 64 characters, and the container runtime (1.4.1) creates at most 63 — ` + fix + ` or the network key "` + key58 + `K"`, true},
		// Not opossum's to create, so not its length to check: the file's
		// name is refused when it is read, and a literal like this one is
		// left to the runtime.
		{"an external network at 64", func() *compose.Project { return onExternal(strings.Repeat("x", 64)) }, "", false},
		// No network of the project's, so no name to be too long.
		{"an isolated service in a project of 60 characters", func() *compose.Project { return isolated(name59 + "p") }, "", false},
	} {
		for _, path := range append([]string{"up"}, runPaths...) {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				rt, log := fakeShim(t)
				// The rows that go ahead with a project name of 59 or 60 characters run
				// without a DNS domain: with one, their container names (71 characters
				// and more) are refused too (#1002). The rest keep the default domain,
				// and "the default network at 64" — refused for both — pins that the
				// network's refusal comes first.
				domain := "opossum"
				if tc.name == "the default network at 63" || tc.name == "an isolated service in a project of 60 characters" {
					domain = ""
				}
				proj := tc.project()
				setShimEnv(rt, "INSPECT_PROJECT="+proj.Name) // names without a DNS domain: the fake cannot read the project from them
				err := invokePath(orchestrator.New(proj, rt, domain, &bytes.Buffer{}), path)
				created := indexOf(log(), "network create") >= 0
				switch {
				case tc.refusal == "" && (err != nil || runLine(log()) < 0):
					t.Errorf("want it run, got err %v and %v", err, log())
				case tc.refusal == "" && created != tc.creates:
					t.Errorf("want a network created: %v, got %v", tc.creates, log())
				case tc.refusal != "" && (err == nil || err.Error() != tc.refusal):
					t.Errorf("\n got %v\nwant %s", err, tc.refusal)
				case tc.refusal != "" && (created || runLine(log()) >= 0):
					t.Errorf("want nothing created or run before the refusal, got %v", log())
				}
			})
		}
	}
}

// A one-off run refuses a network name too long for the runtime before it
// starts the service's dependencies, audited or not: a refusal after them
// would leave them running, and nothing takes them back — and an audited run
// would spend the reason on a report reading `exit -1`.
func TestRunRefusesALongNetworkNameBeforeStartingDependencies(t *testing.T) {
	key := strings.Repeat("k", 59) // "demo-" + = 64
	for _, path := range []string{"run", "run --audit"} {
		t.Run(path, func(t *testing.T) {
			rt, log := fakeShim(t)
			p := project("demo", map[string]*compose.Service{
				"db":  {Image: "alpine:3.20"},
				"web": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{key}, DependsOn: compose.DependsOn{{Name: "db"}}},
			})
			p.Networks = map[string]compose.NetworkDecl{key: {}}
			err := invokePath(orchestrator.New(p, rt, "opossum", &bytes.Buffer{}), path)
			if err == nil || !strings.HasPrefix(err.Error(), `network name "demo-`+key+`" is 64 characters`) {
				t.Fatalf("want the long name refused, got %v", err)
			}
			if indexOf(log(), "network create") >= 0 || runLine(log()) >= 0 {
				t.Errorf("want nothing created or started before the refusal, got %v", log())
			}
		})
	}
}

// Keys folding to one runtime network are refused by the commands that create
// networks, before anything is created or started, and not by the ones that
// clean up: a key `net` beside the default network ran before (the services
// shared one network), and that project has to come down. The check reads the
// whole project — a service `up` was not asked for, or one behind a profile
// that is not active, counts — since starting it later would put it on the
// same network.
func TestNetworksFoldingToOneNameAreRefusedByUpAndRunButNotByDownOrDestroy(t *testing.T) {
	for _, shape := range []struct {
		name    string
		project func() *compose.Project
	}{
		{"web depends on api", func() *compose.Project {
			p := project("demo", map[string]*compose.Service{
				"api": {Image: "alpine:3.20"},
				"web": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"net"}, DependsOn: compose.DependsOn{{Name: "api"}}},
			})
			p.Networks = map[string]compose.NetworkDecl{"net": {}}
			return p
		}},
		{"no dependencies", func() *compose.Project {
			p := project("demo", map[string]*compose.Service{
				"api": {Image: "alpine:3.20"},
				"web": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"net"}},
			})
			p.Networks = map[string]compose.NetworkDecl{"net": {}}
			return p
		}},
		// The service run or brought up creates no network at all, and the
		// collision is between two other services.
		{"web isolated, api on the default network and db on NET", func() *compose.Project {
			p := project("demo", map[string]*compose.Service{
				"api": {Image: "alpine:3.20"},
				"db":  {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"NET"}},
				"web": {Image: "alpine:3.20", NetworkMode: compose.NetworkModeNone},
			})
			p.Networks = map[string]compose.NetworkDecl{"NET": {}}
			return p
		}},
		{"api behind a profile that is not active", func() *compose.Project {
			p := project("demo", map[string]*compose.Service{
				"api": {Image: "alpine:3.20", Profiles: []string{"debug"}},
				"web": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"net"}},
			})
			p.Networks = map[string]compose.NetworkDecl{"net": {}}
			return p
		}},
	} {
		refusal := "network \"net\" becomes the runtime network `<project>-net`, which is the network services without `networks:` join — rename the key"
		if strings.HasPrefix(shape.name, "web isolated") {
			refusal = strings.Replace(refusal, `"net"`, `"NET"`, 1)
		}
		paths := append([]string{"up", "up web"}, runPaths...)
		if shape.name != "api behind a profile that is not active" {
			paths = append(paths, "up api")
		}
		for _, path := range paths {
			t.Run(shape.name+"/"+path, func(t *testing.T) {
				rt, log := fakeShim(t)
				err := invokePath(orchestrator.New(shape.project(), rt, "opossum", &bytes.Buffer{}), path)
				if err == nil || err.Error() != refusal {
					t.Errorf("\n got %v\nwant %s", err, refusal)
				}
				if indexOf(log(), "network create") >= 0 || runLine(log()) >= 0 {
					t.Errorf("want nothing created or started before the refusal, got %v", log())
				}
			})
		}
		t.Run(shape.name+"/down", func(t *testing.T) {
			rt, log := fakeShim(t)
			if err := orchestrator.New(shape.project(), rt, "opossum", &bytes.Buffer{}).Down(false, "", false); err != nil {
				t.Fatalf("Down: %v", err)
			}
			if !hasLine(log(), "network delete demo-net") {
				t.Errorf("want down to delete demo-net, got %v", log())
			}
		})
		t.Run(shape.name+"/destroy", func(t *testing.T) {
			rt, _ := fakeShim(t)
			plan, err := orchestrator.New(shape.project(), rt, "opossum", &bytes.Buffer{}).DestroyPlanFor(false, true, false)
			if err != nil {
				t.Fatalf("DestroyPlanFor: %v", err)
			}
			if strings.Join(plan.Networks, " ") != "demo-net" {
				t.Errorf("want destroy to list demo-net once, got %v", plan.Networks)
			}
		})
	}
}

// With --remove-orphans, the refusal comes before an orphan is removed: the
// check asks nothing of the runtime, so nothing needs to go first.
func TestNetworksFoldingToOneNameAreRefusedBeforeOrphansAreRemoved(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"api": {Image: "alpine:3.20"},
		"web": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"net"}},
	})
	p.Networks = map[string]compose.NetworkDecl{"net": {}}
	setShimEnv(rt, "LS_CONTAINERS=web.demo.opossum old.demo.opossum", "LS_PROJECT=demo") // as orphanProject
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	o.SetUpOptions(false, false, false, true, false) // --remove-orphans
	if err := o.Up(true); err == nil || !strings.HasPrefix(err.Error(), `network "net" becomes`) {
		t.Fatalf("want the collision refused, got %v", err)
	}
	for _, l := range log() {
		if strings.HasPrefix(l, "stop ") || strings.HasPrefix(l, "delete ") {
			t.Errorf("want no orphan touched before the refusal, got %v", log())
			break
		}
	}
}

// Declarations no service joins are not refused for folding to one name, to
// the default network's or to nothing, so down and destroy name each network
// once and never the project name with nothing after it. Down deletes them in
// name order after the default network, the same on every run; with five names
// to order, the ten runs below would not all come out sorted by chance.
func TestDownAndDestroyNameEachDeclaredNetworkOnce(t *testing.T) {
	newProject := func() *compose.Project {
		p := project("demo", map[string]*compose.Service{
			"api": {Image: "alpine:3.20"},
			"web": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"backend"}},
		})
		p.Networks = map[string]compose.NetworkDecl{"backend": {}, "backEnd": {}, "NET": {}, "+": {}, "front": {}, "zeta": {}, "alpha": {}, "mid": {}}
		return p
	}
	t.Run("down", func(t *testing.T) {
		for i := 0; i < 10; i++ {
			rt, log := fakeShim(t)
			if err := orchestrator.New(newProject(), rt, "opossum", &bytes.Buffer{}).Down(false, "", false); err != nil {
				t.Fatalf("Down: %v", err)
			}
			var deletes []string
			for _, l := range log() {
				if strings.HasPrefix(l, "network delete ") {
					deletes = append(deletes, strings.TrimPrefix(l, "network delete "))
				}
			}
			if got := strings.Join(deletes, " "); got != "demo-net demo-alpha demo-backend demo-front demo-mid demo-zeta" {
				t.Fatalf("run %d: want each network deleted once, the default first and the rest in name order, got %s", i, got)
			}
		}
	})
	t.Run("destroy", func(t *testing.T) {
		rt, _ := fakeShim(t)
		plan, err := orchestrator.New(newProject(), rt, "opossum", &bytes.Buffer{}).DestroyPlanFor(false, true, false)
		if err != nil {
			t.Fatalf("DestroyPlanFor: %v", err)
		}
		if strings.Join(plan.Networks, " ") != "demo-alpha demo-backend demo-front demo-mid demo-net demo-zeta" {
			t.Errorf("want each network listed once, got %v", plan.Networks)
		}
	})
}

// An audited run refuses a name too long for the runtime on a network the run
// itself joins before it snapshots the workspace, whose directory would
// outlive the refusal; a run that goes ahead does snapshot it (the fixture
// reaches that step). Snapshots are kept beside the workspace, not inside it.
func TestAnAuditedRunRefusesItsOwnLongNetworkBeforeTheSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name, key, refusal string
	}{
		{"a long network", strings.Repeat("k", 60), `network name "demo-` + strings.Repeat("k", 60) + `" is 65 characters`},
		{"a short network", "back", ""},
	} {
		for _, path := range []string{"run --audit", "run --audit --no-deps"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				base := t.TempDir()
				if err := os.Mkdir(filepath.Join(base, "work"), 0o755); err != nil {
					t.Fatal(err)
				}
				p := project("demo", map[string]*compose.Service{
					"db":  {Image: "alpine:3.20"},
					"web": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{tc.key}, WorkingDir: "/work", Volumes: compose.Volumes{"./work:/work"}, DependsOn: compose.DependsOn{{Name: "db"}}},
				})
				p.Networks = map[string]compose.NetworkDecl{tc.key: {}}
				p.BaseDir = base
				rt, log := fakeShim(t)
				err := invokePath(orchestrator.New(p, rt, "opossum", &bytes.Buffer{}), path)
				_, statErr := os.Stat(filepath.Join(base, workspace.SnapshotDirName))
				snapshotted := statErr == nil
				if tc.refusal == "" {
					if err != nil || runLine(log()) < 0 || !snapshotted {
						t.Errorf("want the run to go ahead and snapshot the workspace, got err %v, snapshotted %v, %v", err, snapshotted, log())
					}
					return
				}
				if err == nil || !strings.HasPrefix(err.Error(), tc.refusal) {
					t.Errorf("\n got %v\nwant it to start %q", err, tc.refusal)
				}
				if snapshotted || indexOf(log(), "network create") >= 0 || runLine(log()) >= 0 {
					t.Errorf("want no snapshot and nothing created or started before the refusal, got snapshotted %v, %v", snapshotted, log())
				}
			})
		}
	}
}

// A dependency's network too long for the runtime is refused by the `up` that
// starts the dependencies, before it creates or starts anything — whichever
// dependency joins it, however deep. With --no-deps that network is not the
// run's to create, and it goes ahead. A dependency behind a profile that is not
// active is refused for that, as before: its network would not be created.
func TestADependencysLongNetworkIsRefusedBeforeAnythingIsCreated(t *testing.T) {
	key := strings.Repeat("k", 60) // "demo-" + = 65
	name60 := strings.Repeat("p", 60)
	long := `starting dependencies: network name "demo-` + key + `" is 65 characters`
	onLong := func(svc *compose.Service) *compose.Service {
		svc.Networks = compose.ServiceNetworks{key}
		return svc
	}
	withKey := func(p *compose.Project) *compose.Project {
		p.Networks = map[string]compose.NetworkDecl{key: {}}
		return p
	}
	for _, shape := range []struct {
		name    string
		project func() *compose.Project
		refusal map[string]string // by path; "" goes ahead
	}{
		{"the dependency", func() *compose.Project {
			return withKey(project("demo", map[string]*compose.Service{
				"db":  onLong(&compose.Service{Image: "alpine:3.20"}),
				"web": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "db"}}},
			}))
		}, map[string]string{"run": long, "run --audit": long}},
		{"the dependency's dependency", func() *compose.Project {
			return withKey(project("demo", map[string]*compose.Service{
				"db":  onLong(&compose.Service{Image: "alpine:3.20"}),
				"mid": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "db"}}},
				"web": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "mid"}}},
			}))
		}, map[string]string{"run": long, "run --audit": long}},
		{"the second of two dependencies", func() *compose.Project {
			return withKey(project("demo", map[string]*compose.Service{
				"api": {Image: "alpine:3.20"},
				"db":  onLong(&compose.Service{Image: "alpine:3.20"}),
				"web": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "api"}, {Name: "db"}}},
			}))
		}, map[string]string{"run": long, "run --audit": long}},
		{"the first of two dependencies", func() *compose.Project {
			return withKey(project("demo", map[string]*compose.Service{
				"api": onLong(&compose.Service{Image: "alpine:3.20"}),
				"db":  {Image: "alpine:3.20"},
				"web": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "api"}, {Name: "db"}}},
			}))
		}, map[string]string{"run": `starting dependencies: network name "demo-` + key, "run --audit": `starting dependencies: network name "demo-` + key}},
		{"an isolated run's dependency on a long default network", func() *compose.Project {
			return project(name60, map[string]*compose.Service{
				"db":  {Image: "alpine:3.20"},
				"web": {Image: "alpine:3.20", NetworkMode: compose.NetworkModeNone, DependsOn: compose.DependsOn{{Name: "db"}}},
			})
		}, map[string]string{"run": `starting dependencies: network name "` + name60 + `-net" is 64 characters`, "run --audit": `starting dependencies: network name "` + name60 + `-net" is 64 characters`}},
		{"a dependency behind a profile that is not active", func() *compose.Project {
			return withKey(project("demo", map[string]*compose.Service{
				"db":  onLong(&compose.Service{Image: "alpine:3.20", Profiles: []string{"debug"}}),
				"web": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "db"}}},
			}))
		}, map[string]string{"run": `service "web" depends on "db", whose profile is not active`, "run --audit": `service "web" depends on "db", whose profile is not active`}},
	} {
		for _, path := range runPaths {
			want, refused := shape.refusal[path]
			t.Run(shape.name+"/"+path, func(t *testing.T) {
				rt, log := fakeShim(t)
				// The isolated run's project name is 60 characters: with a DNS domain its
				// one-off's container name is refused before the dependency's network
				// is reached (#1002), so that shape runs without one.
				domain := "opossum"
				if shape.name == "an isolated run's dependency on a long default network" {
					domain = ""
				}
				proj := shape.project()
				setShimEnv(rt, "INSPECT_PROJECT="+proj.Name) // names without a DNS domain: the fake cannot read the project from them
				err := invokePath(orchestrator.New(proj, rt, domain, &bytes.Buffer{}), path)
				if !refused {
					if err != nil || runLine(log()) < 0 {
						t.Errorf("want the run to go ahead without its dependencies, got err %v and %v", err, log())
					}
					return
				}
				if err == nil || !strings.HasPrefix(err.Error(), want) {
					t.Errorf("\n got %v\nwant it to start %q", err, want)
				}
				if indexOf(log(), "network create") >= 0 || runLine(log()) >= 0 {
					t.Errorf("want nothing created or started before the refusal, got %v", log())
				}
			})
		}
	}
}

// A long network only a service that is not being started joins is not refused:
// the length is checked for the networks the command creates, not the file's —
// for a run with dependencies, the networks of those dependencies (api's db),
// not of every service.
func TestALongNetworkOfAServiceNotStartedIsNotRefused(t *testing.T) {
	key := strings.Repeat("k", 60) // "demo-" + = 65
	newProject := func(profiles []string) *compose.Project {
		p := project("demo", map[string]*compose.Service{
			"api": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "db"}}},
			"db":  {Image: "alpine:3.20"},
			"web": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{key}, Profiles: profiles},
		})
		p.Networks = map[string]compose.NetworkDecl{key: {}}
		return p
	}
	for _, tc := range []struct {
		name string
		call func(o *orchestrator.Orchestrator) error
	}{
		{"up api", func(o *orchestrator.Orchestrator) error { return o.Up(true, "api") }},
		{"run api", func(o *orchestrator.Orchestrator) error {
			return o.RunOneOff("api", []string{"true"}, orchestrator.RunOneOffOptions{})
		}},
		{"run --audit api", func(o *orchestrator.Orchestrator) error {
			_, err := o.RunAudited("api", []string{"true"}, orchestrator.RunOneOffOptions{})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			if err := tc.call(orchestrator.New(newProject(nil), rt, "opossum", &bytes.Buffer{})); err != nil || runLine(log()) < 0 {
				t.Errorf("want it run, got err %v and %v", err, log())
			}
		})
	}
	t.Run("up with web behind a profile that is not active", func(t *testing.T) {
		rt, log := fakeShim(t)
		if err := orchestrator.New(newProject([]string{"debug"}), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil || runLine(log()) < 0 {
			t.Errorf("want it run, got err %v and %v", err, log())
		}
	})
}
