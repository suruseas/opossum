package orchestrator_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// `tty: true` reaches `container run` as `-t` when `up` starts the service —
// detached, and in the foreground too, where a run-to-completion dependency
// is run (container 1.4.1, measured 2026-09-19: `run -d -t` of an image whose
// command is a shell keeps running where `run -d` alone exits at once — the
// shape docker compose users start with `tty: true`; `run -t sh -c 'echo hi;
// exit 3'` with stdin not a terminal prints `hi` and hands back 3, so the
// runtime takes `-t` in the foreground; only `-i -t` together keep a
// container from starting there, and `-i` is not passed — what opossum reads
// from such a run's failure is TestAForegroundRunWithTTYStillReadsItsFailure's). Toggling it recreates the container, as any
// other change to what `run` is given does.
func TestTTYReachesUpAsDashT(t *testing.T) {
	for _, tc := range []struct {
		name string
		tty  bool
		// oneShot runs the service to completion in the foreground, as a
		// dependency that another service waits for.
		oneShot bool
	}{
		{"tty: true", true, false},
		{"tty left out", false, false},
		{"tty: true on a run-to-completion dependency", true, true},
		{"tty left out on a run-to-completion dependency", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			// Beside `init` and `read_only`, so that -t is the tty's and not a
			// neighbouring boolean's.
			svcs := map[string]*compose.Service{"app": {Image: "alpine:3.20", TTY: tc.tty, Init: true, ReadOnly: true}}
			if tc.oneShot {
				svcs["web"] = &compose.Service{Image: "web:latest", DependsOn: compose.DependsOn{{Name: "app", Condition: compose.ConditionCompleted}}}
			}
			o := orchestrator.New(project("demo", svcs), rt, "opossum", &bytes.Buffer{})
			if err := o.Up(true); err != nil {
				t.Fatal(err)
			}
			i := indexOf(log(), "--name app.demo.opossum")
			if i < 0 {
				t.Fatalf("no run of app:\n%s", strings.Join(log(), "\n"))
			}
			run := log()[i]
			if !strings.HasPrefix(run, "run ") {
				t.Fatalf("not a run: %s", run)
			}
			if got := strings.HasPrefix(run, "run -d"); got == tc.oneShot {
				t.Errorf("detached: %v, want %v (a run-to-completion dependency runs in the foreground)\n%s", got, !tc.oneShot, run)
			}
			if got := strings.Contains(" "+run+" ", " -t "); got != tc.tty {
				t.Errorf("-t in the run: %v, want %v\n%s", got, tc.tty, run)
			}
		})
	}
}

func TestTogglingTTYRecreatesTheContainer(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{"app": {Image: "alpine:3.20"}})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatal(err)
	}
	// The same file with `tty: true` added: not up to date any more.
	p = project("demo", map[string]*compose.Service{"app": {Image: "alpine:3.20", TTY: true}})
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatal(err)
	}
	// Counted by name, not by the exact prefix: the second run carries `-t`
	// between `-d` and `--name`.
	runs := func() (n int) {
		for _, l := range log() {
			if strings.HasPrefix(l, "run ") && strings.Contains(l, "--name app.demo.opossum") {
				n++
			}
		}
		return n
	}
	if n := runs(); n != 2 {
		t.Errorf("adding tty: true should recreate the container, got %d runs\n%s", n, strings.Join(log(), "\n"))
	}
	if i := indexOf(log(), "run -d -t --name app.demo.opossum"); i < 0 {
		t.Errorf("the recreated container should carry -t:\n%s", strings.Join(log(), "\n"))
	}
	if strings.Contains(out.String(), "app is up to date") {
		t.Errorf("the second up called the service up to date:\n%s", out.String())
	}
	// And the same file again: up to date.
	out.Reset()
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "app is up to date") {
		t.Errorf("the third up, with nothing changed, should leave the service alone:\n%s", out.String())
	}
	if n := runs(); n != 2 {
		t.Errorf("the third up should not have run again, got %d runs", n)
	}
}

// `run` decides `-t` by the terminal it was typed at, not by the file — as
// docker compose does (v5.5.1, measured 2026-09-19: `run app sh -c tty` with
// `tty: true` in the file prints `not a tty` from a pipe and `/dev/pts/0`
// from a pty, exactly as without it).
func TestRunLeavesTTYToTheTerminal(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{"app": {Image: "alpine:3.20", TTY: true}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.RunOneOff("app", []string{"true"}, orchestrator.RunOneOffOptions{}); err != nil {
		t.Fatal(err)
	}
	i := indexOf(log(), "--name app-run.demo.opossum")
	if i < 0 {
		t.Fatalf("no run:\n%s", strings.Join(log(), "\n"))
	}
	if strings.Contains(" "+log()[i]+" ", " -t ") {
		t.Errorf("run passed -t from the file (the test has no terminal):\n%s", log()[i])
	}
}

// A `-t` of the service's own does not hand the caller's terminal over, so a
// failure of that run is read as any other run's: a refused name is
// recognised and the container left alone, a missing image named. Measured
// with the fake runtime before this held: with `-t` the run's failure came
// back bare, the refusal went unrecognised, and the rollback stopped and
// deleted a container this up had not created.
//
// Two foreground shapes (a run-to-completion dependency under `up -d`, and
// `up --foreground`) by three failures (a refused name, an image the registry
// does not have, a host port in use as the runtime reports it), each with and
// without `tty: true`: the reading has to be the same on both sides. What
// each shape reads is its own: a dependency run to completion recognises the
// refused name and reports every other failure as not having completed (the
// runtime's words having been streamed above), where `up --foreground` names
// the image and the port — as before `tty` was read, on both shapes.
func TestAForegroundRunWithTTYStillReadsItsFailure(t *testing.T) {
	const nameTaken = "something else now holds the container name"
	const image = "docker.io/nosuchorg-neko1165/nosuchimage:latest"
	type count struct {
		cmd  string
		want int
	}
	const notCompleted = `service "app" did not complete successfully`
	failures := []struct {
		name string
		env  []string
		// want is what `up --foreground` reads; wantOneShot what a dependency
		// run to completion reads.
		want, wantOneShot string
		// counts is what the rollback may and may not have done afterwards.
		counts []count
	}{
		{"a refused name", []string{"RUN_EXISTS_ANY=app.demo.opossum"}, nameTaken, nameTaken,
			// Not this up's container: neither stopped nor deleted by the
			// rollback; the pre-start delete is the one allowed.
			[]count{{"stop app.demo.opossum", 0}, {"delete --force app.demo.opossum", 1}}},
		{"a missing image", []string{"RUN_IMAGE_FETCH_FAIL=" + image, "RUN_IMAGE_FETCH_REASON=404 Not Found. Reason: Unknown", "RUN_IMAGE_FETCH_URL=https://registry-1.docker.io/v2/nosuchorg-neko1165/nosuchimage/manifests/latest"},
			"check the image name " + `"` + image + `"`, notCompleted, nil},
		{"a host port in use", []string{"RUN_FAIL=app.demo.opossum", "RUN_FAIL_STDERR=Error: failed to run container: Address already in use"},
			"a published host port is already in use", notCompleted, nil},
	}
	for _, shape := range []struct {
		name       string
		foreground bool // `up --foreground`, as against a dependency run to completion
	}{
		{"a dependency run to completion", false},
		{"up in the foreground", true},
	} {
		for _, f := range failures {
			for _, tty := range []bool{true, false} {
				name := shape.name + ", " + f.name
				if tty {
					name += ", tty: true"
				} else {
					name += ", tty left out"
				}
				t.Run(name, func(t *testing.T) {
					rt, log := fakeShim(t)
					setShimEnv(rt, f.env...)
					app := &compose.Service{Image: "alpine:3.20", TTY: tty}
					if f.name == "a missing image" {
						app.Image = image
					}
					svcs := map[string]*compose.Service{"app": app}
					if !shape.foreground {
						svcs["web"] = &compose.Service{Image: "web:latest", DependsOn: compose.DependsOn{{Name: "app", Condition: compose.ConditionCompleted}}}
					}
					err := orchestrator.New(project("demo", svcs), rt, "opossum", &bytes.Buffer{}).Up(!shape.foreground)
					want := f.want
					if !shape.foreground {
						want = f.wantOneShot
					}
					if err == nil || !strings.Contains(err.Error(), want) {
						t.Errorf("the failure has to be read as %q, got: %v", want, err)
					}
					for _, c := range f.counts {
						if n := countLines(log(), c.cmd); n != c.want {
							t.Errorf("%q: %d times, want %d:\n%s", c.cmd, n, c.want, strings.Join(log(), "\n"))
						}
					}
				})
			}
		}
	}
}

// `run` typed at a terminal hands that terminal to the container: the run is
// streamed with the real fds and nothing captured, so its failure comes back
// bare — not wrapped with a captured stderr, the way a service's own `-t`
// (and every other foreground run) is.
func TestRunWithATerminalHandsItOver(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tty      bool
		captured bool
	}{
		{"typed at a terminal", true, false},
		{"from a pipe", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			setShimEnv(rt, "RUN_FAIL=app-run.demo.opossum")
			p := project("demo", map[string]*compose.Service{"app": {Image: "alpine:3.20"}})
			err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).RunOneOff("app", []string{"true"}, orchestrator.RunOneOffOptions{TTY: tc.tty})
			if err == nil {
				t.Fatal("want the run to fail")
			}
			var re *runtime.RunError
			if got := errors.As(err, &re); got != tc.captured {
				t.Errorf("wrapped with a captured stderr: %v, want %v (%v)", got, tc.captured, err)
			}
		})
	}
}
