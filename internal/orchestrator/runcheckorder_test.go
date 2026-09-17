package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/orchestrator"
)

// What `run`, `run --no-deps` and `up` refuse first, when more than one thing
// is wrong: the order docker compose v5.5.1 refuses them in (measured, issue
// #1072). Two of the checks belong to reading the project at all and come
// first wherever the fault sits — a dependency on a service behind an inactive
// profile (P), then a dependency cycle (C) — and the rest are about the
// one-off and the services it depends on, in the order the runtime would need
// them: an absent external network (N), an absent external volume (V), a
// volume name the runtime cannot create (L), a tmpfs option the engine refuses
// (T). `--no-deps` changes only what is created: a dependency's named volume
// and the external things it names are still checked, its container and its
// anonymous volumes are not.
func TestWhatRunRefusesFirst(t *testing.T) {
	const chain = "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    depends_on: [db]\n  db:\n    image: alpine:3.20\n    depends_on: [cache]\n  cache:\n    image: alpine:3.20\n"
	long := strings.Repeat("k", 251)
	longSvc := "d" + strings.Repeat("b", 249) // demo_<svc>_<hash> is over 255
	for _, tc := range []struct {
		name     string
		body     string
		run      string   // the kind `run` refuses first
		noDeps   string   // ... with --no-deps
		up       string   // ... and what `up web` refuses
		nets     string   // networks the fake says are absent
		says     string   // the whole refusal, where which one it is is not enough
		profiles []string // profiles this run activates
	}{
		{name: "a cycle outside the one-off's chain", run: "C", noDeps: "C", up: "C",
			body: chain + "  other:\n    image: alpine:3.20\n    depends_on: [zed]\n  zed:\n    image: alpine:3.20\n    depends_on: [other]\n"},
		{name: "that cycle beside an absent external network on the one-off", run: "C", noDeps: "C", up: "C", nets: "demo-xnet",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    depends_on: [db]\n    networks: [xnet]\n  db:\n    image: alpine:3.20\n  other:\n    image: alpine:3.20\n    depends_on: [zed]\n  zed:\n    image: alpine:3.20\n    depends_on: [other]\nnetworks:\n  xnet:\n    external: true\n    name: demo-xnet\n"},
		{name: "a dependency on an inactive profile outside the chain", run: "P", noDeps: "P", up: "P",
			body: chain + "  other:\n    image: alpine:3.20\n    depends_on: [off]\n  off:\n    image: alpine:3.20\n    profiles: [x]\n"},
		{name: "that profile beside an absent external network on the one-off", run: "P", noDeps: "P", up: "P", nets: "demo-xnet",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    depends_on: [db]\n    networks: [xnet]\n  db:\n    image: alpine:3.20\n  other:\n    image: alpine:3.20\n    depends_on: [off]\n  off:\n    image: alpine:3.20\n    profiles: [x]\nnetworks:\n  xnet:\n    external: true\n    name: demo-xnet\n"},
		{name: "a cycle and an inactive profile, both outside the chain", run: "P", noDeps: "P", up: "P",
			body: chain + "  other:\n    image: alpine:3.20\n    depends_on: [zed, off]\n  zed:\n    image: alpine:3.20\n    depends_on: [other]\n  off:\n    image: alpine:3.20\n    profiles: [x]\n"},
		// Which services the cycle names, and in which order: the walk takes
		// the dependencies of each service by name, as the startup order does,
		// so the two name the same cycle for the same file — docker compose
		// names the same chain, spelled `ay -> web -> ay` (measured).
		{name: "a cycle reached by two dependencies of the one-off", run: "C", noDeps: "C", up: "C",
			says: "dependency cycle detected: [ay web] -> ay",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    depends_on: [zed, ay]\n  zed:\n    image: alpine:3.20\n    depends_on: [web]\n  ay:\n    image: alpine:3.20\n    depends_on: [web]\n"},
		{name: "a cycle between two services in the chain", run: "C", noDeps: "C", up: "C",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    depends_on: [db]\n  db:\n    image: alpine:3.20\n    depends_on: [cache]\n  cache:\n    image: alpine:3.20\n    depends_on: [db]\n"},
		// The dependency's container is not created without it, so its tmpfs is
		// never handed to the engine.
		{name: "a tmpfs option on a dependency of two paths", run: "T", noDeps: "ok", up: "T",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    depends_on: [db, cache]\n  db:\n    image: alpine:3.20\n    depends_on: [cache]\n  cache:\n    image: alpine:3.20\n    tmpfs: [\"/t:exec,\"]\n"},
		{name: "a tmpfs option on the dependency", run: "T", noDeps: "ok", up: "T",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    depends_on: [db]\n  db:\n    image: alpine:3.20\n    tmpfs: [\"/t:exec,\"]\n"},
		// An anonymous volume is made with its container, so --no-deps, which
		// makes no container for the dependency, makes none of these either —
		// and a name too long for the runtime is nobody's business there.
		// docker compose runs this one (measured: rc 0 in all three) — the
		// limits are the runtime's, and both refusals are known differences
		// already: 255 characters for a volume name, 63 for a container's,
		// which `up` reaches first for a service named this long.
		{name: "an anonymous volume the dependency's name makes too long", run: "L", noDeps: "ok", up: "K",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    depends_on: [" + longSvc + "]\n  " + longSvc + ":\n    image: alpine:3.20\n    volumes: [\"/data\"]\n"},
		{name: "an anonymous volume on the dependency", run: "ok", noDeps: "ok", up: "ok",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    depends_on: [db]\n  db:\n    image: alpine:3.20\n    volumes: [\"/data\"]\n"},
		// Both absent at once: what the runtime must already have is asked for
		// network first.
		{name: "an absent external network and an absent external volume on the dependency", run: "N", noDeps: "N", up: "N", nets: "demo-xnet",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    depends_on: [db]\n  db:\n    image: alpine:3.20\n    networks: [xnet]\n    volumes: [\"xvol:/v\"]\nnetworks:\n  xnet:\n    external: true\n    name: demo-xnet\nvolumes:\n  xvol:\n    external: true\n    name: no-such-vol\n"},
		// Naming a service turns its own profiles on, so a dependency behind one
		// of them comes with it; one behind another profile does not.
		{name: "a gated one-off whose dependency sits behind the same profile", run: "ok", noDeps: "ok", up: "ok",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    profiles: [x]\n    depends_on: [db]\n  db:\n    image: alpine:3.20\n    profiles: [x]\n"},
		{name: "a gated one-off whose dependency sits behind another profile", run: "P", noDeps: "P", up: "P",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    profiles: [x]\n    depends_on: [db]\n  db:\n    image: alpine:3.20\n    profiles: [y]\n"},
		// Naming carries only what the named service depends on: another
		// service of the same profile keeps its gate. `--profile x` opens the
		// whole profile, so the fault under that service is read.
		// Two deep: what the named service carries carries its own in turn.
		{name: "a gated one-off whose dependency's dependency sits behind the same profile", run: "ok", noDeps: "ok", up: "ok",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    profiles: [x]\n    depends_on: [db]\n  db:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [cache]\n  cache:\n    image: alpine:3.20\n    profiles: [x]\n"},
		{name: "a gated service of the one-off's profile with a fault under it", run: "ok", noDeps: "ok", up: "ok",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    profiles: [x]\n  other:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [off]\n  off:\n    image: alpine:3.20\n    profiles: [y]\n"},
		{name: "that service once --profile names its profile", run: "P", noDeps: "P", up: "P", profiles: []string{"x"},
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    profiles: [x]\n  other:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [off]\n  off:\n    image: alpine:3.20\n    profiles: [y]\n"},
		// A named volume is created either way, so its name is checked either way.
		{name: "a volume name the runtime cannot create, on the dependency", run: "L", noDeps: "L", up: "L",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    depends_on: [db]\n  db:\n    image: alpine:3.20\n    volumes: [" + long + ":/l]\nvolumes:\n  " + long + ": {}\n"},
		{name: "an external network that is there, and that volume name", run: "L", noDeps: "L", up: "L",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    depends_on: [db]\n    networks: [there]\n  db:\n    image: alpine:3.20\n    networks: [there]\n    volumes: [" + long + ":/l]\nnetworks:\n  there:\n    external: true\n    name: demo-there\nvolumes:\n  " + long + ": {}\n"},
		{name: "an absent external network on the dependency", run: "N", noDeps: "N", up: "N", nets: "demo-xnet",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    depends_on: [db]\n  db:\n    image: alpine:3.20\n    networks: [xnet]\nnetworks:\n  xnet:\n    external: true\n    name: demo-xnet\n"},
		// Not a dependency: docker compose does not look at it for this run.
		{name: "an absent external network on a service outside the chain", run: "ok", noDeps: "ok", up: "ok", nets: "demo-xnet",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    depends_on: [db]\n  db:\n    image: alpine:3.20\n  other:\n    image: alpine:3.20\n    networks: [xnet]\nnetworks:\n  xnet:\n    external: true\n    name: demo-xnet\n"},
		// Both at once: what must already be there is asked for before the
		// names this would create.
		{name: "an absent external volume and a long volume name on the dependency", run: "V", noDeps: "V", up: "V",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    depends_on: [db]\n  db:\n    image: alpine:3.20\n    volumes: [\"xvol:/v\", " + long + ":/l]\nvolumes:\n  xvol:\n    external: true\n    name: no-such-vol\n  " + long + ": {}\n"},
		{name: "an absent external volume and a long volume name on the one-off", run: "V", noDeps: "V", up: "V",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work, \"xvol:/v\", " + long + ":/l]\n    depends_on: [db]\n  db:\n    image: alpine:3.20\nvolumes:\n  xvol:\n    external: true\n    name: no-such-vol\n  " + long + ": {}\n"},
		// A gated service is not read at all until something enables it, so what
		// it depends on is nobody's business yet. A cycle among gated services
		// is none of docker's business either (measured: `run` and `up -d web`
		// are both rc 0), and none of opossum's since #1088: the startup order
		// is built over the services being read, so neither `up` nor the `up`
		// a `run` does for its dependencies finds this cycle. Pinned in both
		// shapes so a change to it is seen.
		{name: "a cycle between two gated services, the one-off having dependencies", run: "ok", noDeps: "ok", up: "ok",
			body: chain + "  a:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [b]\n  b:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [a]\n"},
		{name: "a cycle between two gated services, the one-off having none", run: "ok", noDeps: "ok", up: "ok",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n  a:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [b]\n  b:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [a]\n"},
		{name: "a gated service that depends on a gated service", run: "ok", noDeps: "ok", up: "ok",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n  other:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [off]\n  off:\n    image: alpine:3.20\n    profiles: [x]\n"},
		{name: "an absent external volume on the dependency", run: "V", noDeps: "V", up: "V",
			body: "  web:\n    image: alpine:3.20\n    working_dir: /work\n    volumes: [./work:/work]\n    depends_on: [db]\n  db:\n    image: alpine:3.20\n    volumes: [\"xvol:/v\"]\nvolumes:\n  xvol:\n    external: true\n    name: no-such-vol\n",
		},
	} {
		for _, form := range []string{"run", "run --audit", "run --no-deps", "run --audit --no-deps", "up"} {
			t.Run(tc.name+"/"+form, func(t *testing.T) {
				rt, log := fakeShim(t)
				if tc.nets != "" {
					setShimEnv(rt, "NETWORK_ABSENT="+tc.nets)
				}
				proj := loadTmpfsProject(t, "services:\n"+tc.body)
				o := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})
				o.EnableProfiles(tc.profiles)
				var err error
				switch form {
				case "run":
					err = o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{})
				case "run --audit":
					_, err = o.RunAudited("web", []string{"true"}, orchestrator.RunOneOffOptions{})
				case "run --no-deps":
					err = o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: true})
				case "run --audit --no-deps":
					_, err = o.RunAudited("web", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: true})
				case "up":
					err = o.Up(true, "web")
				}
				want := tc.run
				switch form {
				case "run --no-deps", "run --audit --no-deps":
					want = tc.noDeps
				case "up":
					want = tc.up
				}
				if got := refusalKind(err); got != want {
					t.Errorf("want %s first, got %s (%v)", want, got, err)
				}
				if tc.says != "" && (err == nil || !strings.Contains(err.Error(), tc.says)) {
					t.Errorf("want %q said, got %v", tc.says, err)
				}
				// And the refusal comes before anything is made: a check the
				// command reaches only once the dependencies are up would give
				// the same words, having started them.
				if want != "ok" {
					if l := createdSomething(log()); l != "" {
						t.Errorf("want nothing created before the refusal, got %q", l)
					}
					return
				}
				// A row that passes has to have run the one-off, or started
				// the services `up` was asked for: "refused nothing" and "did
				// the work" are the same nil error otherwise, and a check that
				// stopped reading the file early would pass as the second.
				if createdSomething(log()) == "" {
					t.Errorf("want the container made, got\n%s", strings.Join(log(), "\n"))
				}
			})
		}
	}
}

// refusalKind names which of the checks an error came from, so a table can say
// which one a command reaches first: a dependency behind a profile that is not
// active (P) and a dependency cycle (C), which belong to reading the project,
// and then the ones about what a command is about to make.
func refusalKind(err error) string {
	if err == nil {
		return "ok"
	}
	switch msg := err.Error(); {
	case strings.Contains(msg, "whose profile is not active"):
		return "P"
	case strings.Contains(msg, "dependency cycle detected"):
		return "C"
	case strings.Contains(msg, "network ") && strings.Contains(msg, "is declared `external: true` but doesn't exist"):
		return "N"
	case strings.Contains(msg, "volume ") && strings.Contains(msg, "is declared `external: true` but doesn't exist"):
		return "V"
	case strings.Contains(msg, "mounts the volume"):
		return "L"
	case strings.Contains(msg, "mounts the tmpfs"):
		return "T"
	case strings.Contains(msg, "container name "):
		return "K"
	default:
		return "other: " + orchestrator.OneLine(msg)
	}
}

// Two services named at once each carry what they depend on behind their own
// profiles: a service both reach is read under both, so what lies beyond it is
// read for each, and naming one alone reads only what that one carries. docker
// compose v5.5.1 (measured): `up -d a b` and `up -d b a` are rc 0, `up -d a`
// alone refuses, `up -d b` alone is rc 0.
func TestNamingTwoServicesCarriesBothTheirProfiles(t *testing.T) {
	const body = `services:
  a:
    image: alpine:3.20
    profiles: [x]
    depends_on: [d]
  b:
    image: alpine:3.20
    profiles: [y]
    depends_on: [d]
  d:
    image: alpine:3.20
    profiles: [x, y]
    depends_on: [e]
  e:
    image: alpine:3.20
    profiles: [y]
`
	for _, tc := range []struct {
		name    string
		up      []string
		refused bool
	}{
		{name: "both at once", up: []string{"a", "b"}},
		{name: "b alone", up: []string{"b"}},
		// a alone reaches d under x, and d's own dependency sits behind y.
		{name: "a alone", up: []string{"a"}, refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			o := orchestrator.New(loadTmpfsProject(t, body), rt, "opossum", &bytes.Buffer{})
			err := o.Up(true, tc.up...)
			if tc.refused {
				if got := refusalKind(err); got != "P" {
					t.Errorf("want the profile refusal, got %s (%v)", got, err)
				}
				return
			}
			if err != nil {
				t.Errorf("want %v started, got %v", tc.up, err)
			}
		})
	}
}

// Where a fault is read from, by how the profile was turned on: naming a
// service reads it and what it carries — the services it depends on behind its
// own profiles — while `--profile` and COMPOSE_PROFILES open the whole profile
// and read every service in it. docker compose v5.5.1, measured (#1072): with
// the fault under an unrelated service of the same profile, `up -d a` is rc 0
// and `--profile x up -d a` refuses; with it under the named service's own
// chain, both refuse; a fault in another profile is read only once that profile
// is open too.
func TestWhatEachWayOfTurningAProfileOnReads(t *testing.T) {
	const gatedA = "  a:\n    image: alpine:3.20\n    profiles: [x]\n"
	for _, tc := range []struct {
		name  string
		body  string
		named string // what naming reads
		open  string // ... what --profile x (and COMPOSE_PROFILES=x, the same call) reads
		all   string // ... and --profile '*'
	}{
		{name: "the fault under the named service's own dependency", named: "P", open: "P", all: "ok",
			body: gatedA + "    depends_on: [d]\n  d:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [far]\n  far:\n    image: alpine:3.20\n    profiles: [y]\n"},
		{name: "the fault two deep under it", named: "P", open: "P", all: "ok",
			body: gatedA + "    depends_on: [d]\n  d:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [e]\n  e:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [far]\n  far:\n    image: alpine:3.20\n    profiles: [y]\n"},
		{name: "the fault under an unrelated service of the same profile", named: "ok", open: "P", all: "ok",
			body: gatedA + "  broken:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [far]\n  far:\n    image: alpine:3.20\n    profiles: [y]\n"},
		// Naming reads none of this (docker compose: rc 0, measured), and
		// opening the profile reads all of it. The startup order is built over
		// the services being read, so a cycle among the ones this way of asking
		// leaves gated is not found (#1088).
		{name: "a cycle among unrelated services of the same profile", named: "ok", open: "C", all: "C",
			body: gatedA + "  p:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [q]\n  q:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [p]\n"},
		// `*` turns on the profile the fault sits behind as well, so there is
		// no fault left to read (docker refuses the shape it was measured in,
		// where the dependency was a name no service has — refused as the file
		// is read here, whatever the profiles say).
		{name: "the fault under a service of another profile", named: "ok", open: "ok", all: "ok",
			body: gatedA + "  broken:\n    image: alpine:3.20\n    profiles: [y]\n    depends_on: [far]\n  far:\n    image: alpine:3.20\n    profiles: [z]\n"},
		{name: "the fault under a service no profile gates", named: "P", open: "P", all: "ok",
			body: gatedA + "  plain:\n    image: alpine:3.20\n    depends_on: [far]\n  far:\n    image: alpine:3.20\n    profiles: [y]\n"},
	} {
		// `--profile x` and COMPOSE_PROFILES=x are the same call here (the CLI
		// reads the variable into EnableProfiles), so the table has one column
		// for both; that they share the same call is pinned in cmd/opossum's own tests.
		for _, way := range []string{"naming", "--profile x", "--profile *"} {
			t.Run(tc.name+"/"+way, func(t *testing.T) {
				rt, _ := fakeShim(t)
				o := orchestrator.New(loadTmpfsProject(t, "services:\n"+tc.body), rt, "opossum", &bytes.Buffer{})
				switch way {
				case "--profile x":
					o.EnableProfiles([]string{"x"})
				case "--profile *":
					o.EnableProfiles([]string{"*"})
				}
				want := tc.named
				switch way {
				case "--profile x":
					want = tc.open
				case "--profile *":
					want = tc.all
				}
				if got := refusalKind(o.Up(true, "a")); got != want {
					t.Errorf("want %s, got %s", want, got)
				}
			})
		}
	}
}

// Naming a gated service starts the services it depends on behind its own
// profiles — the refusal going away is not the whole of it, the dependency has
// to come up. docker compose v5.5.1 creates the dependency's container for
// `up -d web` on `web[x] -> db[x]` (measured).
func TestNamingAGatedServiceStartsWhatItCarries(t *testing.T) {
	rt, log := fakeShim(t)
	body := "services:\n  app:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [zdb]\n  zdb:\n    image: alpine:3.20\n    profiles: [x]\n  other:\n    image: alpine:3.20\n    profiles: [x]\n"
	o := orchestrator.New(loadTmpfsProject(t, body), rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true, "app"); err != nil {
		t.Fatalf("want app and its dependency started, got %v", err)
	}
	var order []string
	started := map[string]bool{}
	for _, l := range log() {
		if !strings.HasPrefix(l, "run ") {
			continue
		}
		f := strings.Fields(l)
		for i := range f[:len(f)-1] {
			if f[i] == "--name" {
				started[f[i+1]] = true
				order = append(order, f[i+1])
			}
		}
	}
	if !started["app.demo.opossum"] || !started["zdb.demo.opossum"] {
		t.Errorf("want app and zdb started, got %v", started)
	}
	// And in that order: what naming carries is a dependency, so it starts
	// first. The order follows every depends_on there is, gated or not, so a
	// dependency decides where its dependent goes whether or not the profiles
	// leave it active — drop the edge and the two are placed by name alone,
	// which here would start the dependency second.
	if want := []string{"zdb.demo.opossum", "app.demo.opossum"}; len(order) != 2 || order[0] != want[0] || order[1] != want[1] {
		t.Errorf("want %v started in that order, got %v", want, order)
	}
	// Only what it carries: another service of the same profile is not named,
	// so this `up` does not start it (docker compose starts neither).
	if started["other.demo.opossum"] {
		t.Errorf("want other left alone, got %v", started)
	}
}
