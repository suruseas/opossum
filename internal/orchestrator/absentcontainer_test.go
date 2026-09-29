package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// A service the project defines and nobody has started yet — one behind a
// profile that was never turned on, or one an `up <service>` did not reach —
// has no container, and a command working over the whole project passes it
// by. It is not a failure and not something to report having done: docker
// compose v5.5.1 exits 0 over such a project and says nothing about that
// service (measured, with and without `-p`). Before this, `logs`, `start` and
// `restart` failed on it — `logs` printing nothing at all for the service that
// was running — and `stop`, `kill` and `down` said they were stopping a
// container that was not there.
func TestACommandOverTheWholeProjectPassesByAServiceWithNoContainer(t *testing.T) {
	const body = `services:
  web:
    image: alpine:3.20
  debug:
    image: alpine:3.20
    profiles: [x]
`
	for _, tc := range []struct {
		cmd string
		// What the runtime must have been asked about the service that is
		// there: the work itself, not the look that decided who owns it —
		// every one of these inspects web before it does anything, so a row
		// that accepted the inspect would pass over a command that did
		// nothing at all.
		asked string
	}{
		{cmd: "logs", asked: "logs"},
		{cmd: "start", asked: "start web.demo.opossum"},
		{cmd: "restart", asked: "start web.demo.opossum"},
		{cmd: "stop", asked: "stop web.demo.opossum"},
		{cmd: "kill", asked: "kill -s SIGKILL web.demo.opossum"},
		{cmd: "down", asked: "delete --force web.demo.opossum"},
	} {
		t.Run(tc.cmd, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, log := fakeShim(t)
			// Only web has a container: the fake answers `inspect` for it and
			// says the other is absent.
			setShimEnv(rt, "INSPECT_ABSENT=debug.demo.opossum")
			proj, err := loadProject(t, body)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			o := orchestrator.New(proj, rt, "opossum", &out)
			switch tc.cmd {
			case "logs":
				err = o.Logs(nil, runtime.LogsOptions{Tail: 1})
			case "start":
				err = o.Start(nil)
			case "restart":
				err = o.Restart(nil)
			case "stop":
				err = o.Stop(nil)
			case "kill":
				err = o.Kill(nil, "SIGKILL")
			case "down":
				err = o.Down(false, "", false)
			}
			if err != nil {
				t.Errorf("want %s to pass the service with no container by, got %v", tc.cmd, err)
			}
			// And it says nothing about it: a line naming a service it did not
			// touch reads as work it did.
			for _, line := range strings.Split(out.String(), "\n") {
				if strings.Contains(line, "debug") {
					t.Errorf("want nothing said about the service with no container, got %q", line)
				}
			}
			// The service that is there still gets the work done to it.
			asked := false
			for _, l := range log() {
				if strings.HasPrefix(l, tc.asked) && strings.Contains(l, "web") {
					asked = true
				}
			}
			if !asked {
				t.Errorf("want %q asked of the runtime for web, got\n%s", tc.asked, strings.Join(log(), "\n"))
			}
			// And for the one command whose whole job is output, the output.
			if tc.cmd == "logs" && !strings.Contains(out.String(), "web") {
				t.Errorf("want web's log line printed, got\n%s", out.String())
			}
		})
	}
}

// Naming a service by itself does not change the answer (#1098): docker
// compose v5.5.1 passes a service with no container by whether it is named or
// the whole project is asked about (measured, `-p` present or not made no
// difference). A name that does not resolve to a project service is a
// different mistake, and stays one — resolveServices/resolveServicesTolerant
// catch it before worksOn is ever asked, so nothing here is hidden by this.
func TestNamingAServiceWithNoContainerIsPassedByLikeTheWholeProject(t *testing.T) {
	const body = `services:
  web:
    image: alpine:3.20
  debug:
    image: alpine:3.20
    profiles: [x]
`
	for _, tc := range []struct {
		cmd string
		run func(o *orchestrator.Orchestrator) error
		// A verb the runtime must never see sent for debug's container: passed
		// by means nothing was attempted on it, not just that nothing failed.
		neverVerb string
	}{
		{"logs", func(o *orchestrator.Orchestrator) error {
			return o.Logs([]string{"debug"}, runtime.LogsOptions{Tail: 1})
		}, "logs"},
		{"start", func(o *orchestrator.Orchestrator) error { return o.Start([]string{"debug"}) }, "start"},
		{"restart", func(o *orchestrator.Orchestrator) error { return o.Restart([]string{"debug"}) }, "start"},
		{"stop", func(o *orchestrator.Orchestrator) error { return o.Stop([]string{"debug"}) }, "stop"},
		{"kill", func(o *orchestrator.Orchestrator) error { return o.Kill([]string{"debug"}, "SIGKILL") }, "kill"},
		{"stats", func(o *orchestrator.Orchestrator) error {
			return o.Stats([]string{"debug"}, orchestrator.StatsOptions{NoStream: true})
		}, "stats"},
	} {
		t.Run(tc.cmd, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, log := fakeShim(t)
			setShimEnv(rt, "INSPECT_ABSENT=debug.demo.opossum")
			proj, err := loadProject(t, body)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			o := orchestrator.New(proj, rt, "opossum", &out)
			if err := tc.run(o); err != nil {
				t.Errorf("want naming a service with no container passed by like the whole project does, got %v", err)
			}
			if s := out.String(); strings.Contains(s, "debug") {
				t.Errorf("want nothing said about the named service with no container, got %q", s)
			}
			// Passed by, not just quiet about it: the runtime was never asked
			// to do the verb this command is about, to debug's container.
			for _, l := range log() {
				if strings.HasPrefix(l, tc.neverVerb) && strings.Contains(l, "debug") {
					t.Errorf("want the runtime never asked %q of debug's container, got %q in\n%s", tc.neverVerb, l, strings.Join(log(), "\n"))
				}
			}
		})
	}
}

// The owner of a container and whether it is there come from one look. Two
// looks can disagree about a container that is starting or ending between
// them, which would leave a service stopped and not started again, or read a
// container that is there as one nobody started.
func TestWhoOwnsItAndWhetherItIsThereComeFromOneLook(t *testing.T) {
	const body = `services:
  web:
    image: alpine:3.20
  other:
    image: alpine:3.20
`
	for _, cmd := range []string{"stop", "kill", "logs"} {
		t.Run(cmd, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, log := fakeShim(t)
			proj, err := loadProject(t, body)
			if err != nil {
				t.Fatal(err)
			}
			o := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})
			switch cmd {
			case "stop":
				err = o.Stop(nil)
			case "kill":
				err = o.Kill(nil, "SIGKILL")
			case "logs":
				err = o.Logs(nil, runtime.LogsOptions{Tail: 1})
			}
			if err != nil {
				t.Fatalf("%s: %v", cmd, err)
			}
			for _, name := range []string{"web.demo.opossum", "other.demo.opossum"} {
				n := 0
				for _, l := range log() {
					if l == "inspect "+name {
						n++
					}
				}
				if n != 1 {
					t.Errorf("want %s looked at once, got %d times:\n%s", name, n, strings.Join(log(), "\n"))
				}
			}
		})
	}
}
