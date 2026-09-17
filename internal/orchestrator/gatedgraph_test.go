package orchestrator_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"

	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// What a command reads out of a compose file when it puts its services in
// order: the ones the profiles leave active, and only those. A dependency
// cycle behind a profile nothing turned on, and a dependency on a service
// behind another profile, are nobody's business until something enables them.
// Naming a service enables it and what it depends on behind its own profiles;
// `--profile`, `COMPOSE_PROFILES` and `*` open whole profiles. A depends_on
// naming a service the file does not define is the exception: reading the file
// refuses that whatever the profiles say, which is a difference from docker
// compose of its own and tracked separately — the rows for it are here to sit
// beside the cycle rows, so that a change to either is seen.
//
// The table is docker compose v5.5.1, measured over the same three skeletons
// (issue #1088): `a` behind profile `x` depends on `b` (also `x`), `plain` is
// gated by nothing, and the fault sits in one of three places — in the chain
// (on `b`), on a service of the same profile the chain never reaches (`un`),
// or on `plain`.
func TestWhatAFaultBehindAProfileIsReadBy(t *testing.T) {
	const skeleton = "  a:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [b]\n  b:\n    image: alpine:3.20\n    profiles: [x]\n"
	// The fault, spelled three times over: what it does to the chain, to an
	// unrelated service of the same profile, and to an ungated one. Each body
	// keeps the skeleton's shape so the only thing a row changes is where the
	// fault sits.
	const (
		nameChain = "  a:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [b]\n  b:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [nosuch]\n  plain:\n    image: alpine:3.20\n"
		nameOther = skeleton + "  un:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [nosuch]\n  plain:\n    image: alpine:3.20\n"
		namePlain = skeleton + "  plain:\n    image: alpine:3.20\n    depends_on: [nosuch]\n"

		gatedChain = "  a:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [b]\n  b:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [gy]\n  gy:\n    image: alpine:3.20\n    profiles: [y]\n  plain:\n    image: alpine:3.20\n"
		gatedOther = skeleton + "  un:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [gy]\n  gy:\n    image: alpine:3.20\n    profiles: [y]\n  plain:\n    image: alpine:3.20\n"
		gatedPlain = skeleton + "  plain:\n    image: alpine:3.20\n    depends_on: [gy]\n  gy:\n    image: alpine:3.20\n    profiles: [y]\n"

		cycleChain = "  a:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [b]\n  b:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [b2]\n  b2:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [b]\n  plain:\n    image: alpine:3.20\n"
		cycleOther = skeleton + "  un:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [un2]\n  un2:\n    image: alpine:3.20\n    profiles: [x]\n    depends_on: [un]\n  plain:\n    image: alpine:3.20\n"
		cyclePlain = skeleton + "  plain:\n    image: alpine:3.20\n    depends_on: [plain2]\n  plain2:\n    image: alpine:3.20\n    depends_on: [plain]\n"
	)
	// Which kind each way of asking reaches: naming `a`, opening `x`, opening
	// every profile, and naming nothing. docker spells an undefined dependency
	// and a dependency behind an inactive profile the same way ("depends on
	// undefined service"); opossum keeps them apart (U and P), and the column
	// says which of its refusals stands where docker refuses.
	for _, tc := range []struct {
		name                       string
		body                       string
		named, profile, all, plain string
	}{
		// Reading the file refuses a depends_on it does not define, wherever it
		// sits and whatever the profiles say — before any command runs, so
		// every column is the same. docker compose reads a gated service's
		// depends_on only once something activates it: a difference of its
		// own, measured and tracked separately. These rows are here so that a
		// change to it is seen beside the cycle rows.
		{name: "an undefined dependency in the chain", body: nameChain,
			named: "U", profile: "U", all: "U", plain: "U"},
		{name: "an undefined dependency on a service of the same profile", body: nameOther,
			named: "U", profile: "U", all: "U", plain: "U"},
		{name: "an undefined dependency on an ungated service", body: namePlain,
			named: "U", profile: "U", all: "U", plain: "U"},
		// `*` opens `y` as well, so the dependency is active and there is no
		// fault left to find — which is why the way of opening a profile is a
		// column of its own and not a synonym for `--profile x`.
		{name: "a dependency behind another profile in the chain", body: gatedChain,
			named: "P", profile: "P", all: "ok", plain: "ok"},
		{name: "a dependency behind another profile on a service of the same profile", body: gatedOther,
			named: "ok", profile: "P", all: "ok", plain: "ok"},
		{name: "a dependency behind another profile on an ungated service", body: gatedPlain,
			named: "P", profile: "P", all: "ok", plain: "P"},
		{name: "a cycle in the chain", body: cycleChain,
			named: "C", profile: "C", all: "C", plain: "ok"},
		{name: "a cycle among services of the same profile", body: cycleOther,
			named: "ok", profile: "C", all: "C", plain: "ok"},
		{name: "a cycle among ungated services", body: cyclePlain,
			named: "C", profile: "C", all: "C", plain: "C"},
	} {
		for _, form := range []string{"up a", "--profile x up a", "--profile * up a", "up"} {
			t.Run(tc.name+"/"+form, func(t *testing.T) {
				rt, log := fakeShim(t)
				var err error
				want := tc.named
				proj, loadErr := loadProject(t, "services:\n"+tc.body)
				switch form {
				case "--profile x up a":
					want = tc.profile
				case "--profile * up a":
					want = tc.all
				case "up":
					want = tc.plain
				}
				// Reading the file refuses an ungated service's undefined
				// dependency, before any command runs: docker compose refuses
				// it while loading too, and a project that was never read
				// created nothing by construction.
				if loadErr != nil {
					if got := loadFault(loadErr); got != want {
						t.Errorf("want %s, got %s while reading the file (%v)", want, got, loadErr)
					}
					return
				}
				o := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})
				switch form {
				case "--profile x up a":
					o.EnableProfiles([]string{"x"})
				case "--profile * up a":
					o.EnableProfiles([]string{"*"})
				}
				if form == "up" {
					err = o.Up(true)
				} else {
					err = o.Up(true, "a")
				}
				if got := loadFault(err); got != want {
					t.Errorf("want %s, got %s (%v)", want, got, err)
				}
				if want == "ok" {
					// A row that passes has to have started something: a cell
					// that says "read nothing, refused nothing" and a cell that
					// says "read it and found it fine" both show up as a nil
					// error, and only one of them is what was measured.
					if started(log()) == "" {
						t.Errorf("want the services this way of asking starts to be started, got\n%s", strings.Join(log(), "\n"))
					}
					return
				}
				if l := createdSomething(log()); l != "" {
					t.Errorf("want nothing created before the refusal, got %q", l)
				}
			})
		}
	}
}

// loadProject reads a compose file the way a command does, handing back what
// reading it refused rather than ending the test: which of the refusals comes
// from reading the file and which from the command is part of what the table
// says.
func loadProject(t *testing.T, body string) (*compose.Project, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(path, []byte("name: demo\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	return compose.Load(path)
}

// loadFault names which of the refusals that belong to reading the project an
// error came from: a dependency on a service the file does not define (U), one
// behind a profile that is not active (P), a dependency cycle (C).
func loadFault(err error) string {
	if err == nil {
		return "ok"
	}
	switch msg := err.Error(); {
	case strings.Contains(msg, "depends on unknown service"):
		return "U"
	case strings.Contains(msg, "whose profile is not active"):
		return "P"
	case strings.Contains(msg, "dependency cycle detected"):
		return "C"
	default:
		return "other: " + orchestrator.OneLine(msg)
	}
}

// started reports the first container the runtime was asked to make, so a row
// that expects a command to get on with its work can say it did.
func started(lines []string) string {
	for _, l := range lines {
		if strings.HasPrefix(l, "run ") || strings.HasPrefix(l, "create ") {
			return l
		}
	}
	return ""
}

// The commands that work from the containers a project already has read the
// project the same way, which is what keeps a running project takeable down:
// a cycle added behind a profile nobody turned on must not be what stops
// `down`. docker compose v5.5.1 takes such a project down without a word
// (measured, #1088). A fault among the services these commands do read still
// stops them, exactly as it did before this — the ungated rows are here to
// pin that this change adds and removes nothing there. A dependency no
// service defines reads the same way: refused while the file is read when
// nothing gates it, unread behind a profile that is off.
func TestAGatedFaultDoesNotStopTheCommandsThatReadOrTakeDown(t *testing.T) {
	const gated = `services:
  web:
    image: alpine:3.20
  other:
    image: alpine:3.20
    profiles: [g]
    depends_on: [zed]
  zed:
    image: alpine:3.20
    profiles: [g]
    depends_on: [other]
`
	const ungated = `services:
  web:
    image: alpine:3.20
  other:
    image: alpine:3.20
    depends_on: [zed]
  zed:
    image: alpine:3.20
    depends_on: [other]
`
	const gatedUndefined = `services:
  web:
    image: alpine:3.20
  other:
    image: alpine:3.20
    profiles: [g]
    depends_on: [nosuch]
`
	const ungatedUndefined = `services:
  web:
    image: alpine:3.20
  other:
    image: alpine:3.20
    depends_on: [nosuch]
`
	// Half in and half out: the cycle runs through a service the profile
	// gates, and the service the walk comes back to is one being read — the
	// names put the ungated one first so that it is, since the walk starts
	// from the first name and the cycle is judged by all of it, not by the
	// service it came back to. It counts as a cycle only once the whole of it
	// is read. docker compose refuses this file either way, for the
	// dependency on a gated service rather than for the cycle: a known
	// difference of its own (the commands here do not read a dependency on a
	// service behind a profile that is not active), unchanged by this and
	// tracked separately.
	const halfGated = `services:
  web:
    image: alpine:3.20
    depends_on: [zother]
  zother:
    image: alpine:3.20
    profiles: [g]
    depends_on: [web]
`
	// Naming a service is an axis of its own: some of these put their
	// services in order whether or not they were given names, and some take
	// the names as the whole answer and build no order — which is what
	// decides whether a cycle is read. The rows are both sides of that.
	readsNoOrderWhenNamed := map[string]bool{"stop web": true, "logs web": true, "restart web": true}
	for _, cmd := range []string{"ps", "images", "stop", "kill", "down", "destroy plan", "logs", "start", "restart", "stop web", "logs web", "restart web", "ps web", "start web"} {
		// Both halves of the claim: gated, the fault is nobody's business;
		// with the profile on, the same file is refused, as it was before this
		// change and as docker compose v5.5.1 refuses it — with the caveat
		// that docker answers these commands from the containers, without
		// reading the file at all, when it is given a project name with `-p`
		// and left to find the file itself (measured, #1093). What this table
		// pins is the gated half, where the two agree in every form; the
		// active half is here because it is the half that was refused before
		// and must go on being refused.
		for _, tc := range []struct {
			name, body string
			off, on    string // what the fault is with the profile off, and on
		}{
			{name: "a cycle", body: gated, off: "ok", on: "C"},
			{name: "an ungated cycle", body: ungated, off: "C", on: "C"},
			// Reading the file refuses these, gated or not, before the command
			// runs: they are here so that a change to that is seen.
			{name: "an undefined dependency", body: gatedUndefined, off: "U", on: "U"},
			{name: "an ungated undefined dependency", body: ungatedUndefined, off: "U", on: "U"},
			{name: "a cycle through a gated service", body: halfGated, off: "ok", on: "C"},
		} {
			for _, profile := range []string{"gate on it", "profile g turned on"} {
				tc, profile := tc, profile
				on := profile == "profile g turned on"
				want := map[bool]string{false: tc.off, true: tc.on}[on]
				// `stop`, `logs` and `restart` take the names as the whole
				// answer and build no order, so they read no cycle — as they
				// did before this. `ps` and `start` put even the services they
				// were given in order, so they do read it. docker compose
				// refuses both kinds: the first is a difference of its own,
				// measured and tracked separately, pinned here so that a
				// change to it shows up beside the rows it sits with.
				if readsNoOrderWhenNamed[cmd] && want == "C" {
					want = "ok"
				}
				t.Run(cmd+"/"+tc.name+"/"+profile, func(t *testing.T) {
					runReadOrTakeDown(t, cmd, tc.body, on, want)
				})
			}
		}
	}
}

// runReadOrTakeDown asks one of the commands that work from the containers a
// project already has, and says which of the refusals that belong to reading
// the project it came back with.
func runReadOrTakeDown(t *testing.T, cmd, body string, profileOn bool, want string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, log := fakeShim(t)
	proj, loadErr := loadProject(t, body)
	if loadErr != nil {
		// Reading the file is where an ungated undefined dependency
		// is refused, so no command of this table runs at all.
		if got := loadFault(loadErr); got != want {
			t.Fatalf("want %s, got %s while reading the file (%v)", want, got, loadErr)
		}
		return
	}
	o := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})
	if profileOn {
		o.EnableProfiles([]string{"g"})
	}
	var err error
	// Naming a service says which ones to work on, not which parts of the
	// file to read, so the answer is the same named or not.
	switch cmd {
	case "ps":
		err = o.Ps(orchestrator.PsOptions{})
	case "images":
		err = o.Images(orchestrator.ImagesOptions{})
	case "stop":
		err = o.Stop(nil)
	case "stop web":
		err = o.Stop([]string{"web"})
	case "kill":
		err = o.Kill(nil, "SIGKILL")
	case "down":
		err = o.Down(false, "", false)
	case "destroy plan":
		_, err = o.DestroyPlanFor(true, true, true)
	case "logs":
		err = o.Logs(nil, runtime.LogsOptions{Tail: 1})
	case "logs web":
		err = o.Logs([]string{"web"}, runtime.LogsOptions{Tail: 1})
	case "ps web":
		err = o.Ps(orchestrator.PsOptions{})
	case "start web":
		err = o.Start([]string{"web"})
	case "start":
		err = o.Start(nil)
	case "restart":
		err = o.Restart(nil)
	case "restart web":
		err = o.Restart([]string{"web"})
	}
	if got := loadFault(err); got != want {
		t.Errorf("want %s to be %s, got %s (%v)", cmd, want, got, err)
	}
	if want == "ok" {
		// "read nothing and refused nothing" and "read it and got on with the
		// work" are the same nil error; only one of them is what was measured.
		// The probe every one of these makes before it starts (`system
		// status`) is not the work, so it does not count as having done any.
		var work []string
		for _, l := range log() {
			if !strings.HasPrefix(l, "system status") {
				work = append(work, l)
			}
		}
		if len(work) == 0 {
			t.Errorf("want %s to ask the runtime about the project, it only probed the runtime: %v", cmd, log())
		}
	}
}

// A project comes down in the order it went up, reversed, even when what it
// takes down is no longer what the profiles leave active. opossum takes the
// whole project down where docker compose leaves a gated container running (a
// known difference), so the gated services are still stopped here — and a
// service is stopped before the one it depends on, as `down` promises,
// whichever of them a profile gates. The names are chosen so that the
// dependency sorts after the service that needs it: placing the two by name
// alone gives the opposite order, which is what this pins.
func TestTakingDownAProjectTheProfilesNoLongerCoverKeepsDependencyOrder(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"an ungated service that depends on a gated one", `services:
  keep:
    image: alpine:3.20
  aa:
    image: alpine:3.20
    depends_on: [zz]
  zz:
    image: alpine:3.20
    profiles: [x]
`},
		{"a chain of gated services", `services:
  keep:
    image: alpine:3.20
  aa:
    image: alpine:3.20
    profiles: [x]
    depends_on: [zz]
  zz:
    image: alpine:3.20
    profiles: [x]
`},
		// A cycle in the file the profiles leave gated changes nothing about
		// the order of everything else: the edge it is broken at is its own.
		// docker compose v5.5.1 is the same wherever it can read the file —
		// it stops a service before the one it depends on, and only where the
		// file cannot be read does it stop them all at once (measured).
		{"a gated cycle beside them", `services:
  keep:
    image: alpine:3.20
  aa:
    image: alpine:3.20
    depends_on: [zz]
  zz:
    image: alpine:3.20
    profiles: [x]
  gc1:
    image: alpine:3.20
    profiles: [g]
    depends_on: [gc2]
  gc2:
    image: alpine:3.20
    profiles: [g]
    depends_on: [gc1]
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, log := fakeShim(t)
			proj, err := loadProject(t, tc.body)
			if err != nil {
				t.Fatal(err)
			}
			up := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})
			up.EnableProfiles([]string{"x"})
			if err := up.Up(true); err != nil {
				t.Fatalf("want the project started with the profile on, got %v", err)
			}
			if got := namesIn(log(), "run "); !before(got, "zz.demo.opossum", "aa.demo.opossum") {
				t.Errorf("want zz started before aa, got %v", got)
			}
			// The profile is off now: the file is read by a command that was
			// given no profile at all, which is how a project outlives the
			// `--profile` that started it.
			rt2, log2 := fakeShim(t)
			down := orchestrator.New(proj, rt2, "opossum", &bytes.Buffer{})
			if err := down.Down(false, "", false); err != nil {
				t.Fatalf("want the project taken down, got %v", err)
			}
			stopped := namesIn(log2(), "stop ")
			for _, name := range []string{"aa.demo.opossum", "zz.demo.opossum", "keep.demo.opossum"} {
				if !slices.Contains(stopped, name) {
					t.Errorf("want %s stopped by a `down` the profile no longer covers, got %v", name, stopped)
				}
			}
			if !before(stopped, "aa.demo.opossum", "zz.demo.opossum") {
				t.Errorf("want aa stopped before the zz it depends on, got %v", stopped)
			}
		})
	}
}

// namesIn is the container names the runtime was asked about by the lines that
// start with verb, in the order it was asked.
func namesIn(lines []string, verb string) []string {
	var names []string
	for _, l := range lines {
		if !strings.HasPrefix(l, verb) {
			continue
		}
		f := strings.Fields(l)
		if verb == "run " {
			for i := range f[:len(f)-1] {
				if f[i] == "--name" {
					names = append(names, f[i+1])
				}
			}
			continue
		}
		names = append(names, f[len(f)-1])
	}
	return names
}

// before reports whether both names are there and first comes before second.
func before(names []string, first, second string) bool {
	i, j := slices.Index(names, first), slices.Index(names, second)
	return i >= 0 && j >= 0 && i < j
}

// `watch` puts the services in order like the commands that start them, so a
// dependency cycle among the ones it reads stops it — and it says so, rather
// than saying the file has no rules in it, which sends a reader looking for
// rules they can see they wrote. (What it does not look at is what no order
// needs: a dependency on a service behind a profile that is not active leaves
// it watching, where `up` and `config` refuse the file.)
func TestWatchSaysWhatIsWrongWithTheFileRatherThanThatItHasNoRules(t *testing.T) {
	const body = `services:
  web:
    image: alpine:3.20
    develop:
      watch:
        - path: ./src
          action: sync
          target: /app
  other:
    image: alpine:3.20
    profiles: [g]
    depends_on: [zed]
  zed:
    image: alpine:3.20
    profiles: [g]
    depends_on: [other]
`
	for _, tc := range []struct {
		name    string
		profile bool
		says    string // what the refusal has to contain, "" for no refusal
	}{
		// Gated, the cycle is nobody's business and the rules are watched; the
		// ctx is already over, so the watch ends at once.
		{name: "the fault behind its profile", says: ""},
		{name: "the profile turned on", profile: true, says: "dependency cycle detected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			proj, err := loadProject(t, body)
			if err != nil {
				t.Fatal(err)
			}
			o := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})
			if tc.profile {
				o.EnableProfiles([]string{"g"})
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err = o.Watch(ctx)
			if tc.says == "" {
				if err != nil {
					t.Errorf("want the rules watched, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Errorf("want %q said, got %v", tc.says, err)
			}
			if err != nil && strings.Contains(err.Error(), "no develop.watch rules") {
				t.Errorf("want the fault named, got the answer for a file with no rules: %v", err)
			}
		})
	}
}
