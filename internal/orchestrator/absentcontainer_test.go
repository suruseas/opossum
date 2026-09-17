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

// Asking for a service by name is a different question, and this change does
// not answer it: a service that is not running still says so when someone asks
// for it, so that a name typed wrong is not passed over in silence. What that
// answer should be is issue #1098; this row is here to say it did not change.
func TestNamingAServiceWithNoContainerStillAnswersForIt(t *testing.T) {
	const body = `services:
  web:
    image: alpine:3.20
  debug:
    image: alpine:3.20
    profiles: [x]
`
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, _ := fakeShim(t)
	setShimEnv(rt, "INSPECT_ABSENT=debug.demo.opossum")
	proj, err := loadProject(t, body)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	o := orchestrator.New(proj, rt, "opossum", &out)
	if err := o.Logs([]string{"debug"}, runtime.LogsOptions{Tail: 1}); err == nil {
		t.Error("want `logs debug` to answer for the service it was given, got no error")
	}
	if err := o.Start([]string{"debug"}); err == nil {
		t.Error("want `start debug` to answer for the service it was given, got no error")
	}
	// The other four take a name that is not running as something to do
	// nothing about, and say so — which is where they were before this, and
	// what #1098 is about. Pinned so that a change to it is seen.
	var said bytes.Buffer
	o2 := orchestrator.New(proj, rt, "opossum", &said)
	if err := o2.Stop([]string{"debug"}); err != nil {
		t.Errorf("want `stop debug` to stay as it was, got %v", err)
	}
	if err := o2.Kill([]string{"debug"}, "SIGKILL"); err != nil {
		t.Errorf("want `kill debug` to stay as it was, got %v", err)
	}
	if s := said.String(); !strings.Contains(s, "Stopping debug") || !strings.Contains(s, "Killing debug") {
		t.Errorf("want both to name the service they were given, got\n%s", s)
	}
	// And it says which service it is about, so that the answer is about the
	// name that was asked for.
	if s := out.String(); !strings.Contains(s, "debug") {
		t.Errorf("want the service named in what it says, got\n%s", s)
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
