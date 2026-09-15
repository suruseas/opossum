package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

// superviseShim returns a runtime whose `inspect` reports the given state and
// records every command, so a poll's decision can be observed as an action.
func superviseShim(t *testing.T, state string) (*runtime.Runtime, func() string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	shim := filepath.Join(dir, "c.sh")
	body := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %s\ncase \"$1\" in\n"+
		"  inspect) echo '[{\"status\":{\"state\":\"%s\"},\"configuration\":{\"labels\":{\"opossum.project\":\"demo\"}}}]' ;;\n"+
		"  system) echo 'status running' ;;\nesac\nexit 0\n", log, state)
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return &runtime.Runtime{Bin: shim}, func() string {
		b, _ := os.ReadFile(log)
		return string(b)
	}
}

func superviseProject(t *testing.T, restart string) *compose.Project {
	t.Helper()
	return &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"web": {Name: "web", Image: "w", Restart: restart},
	}}
}

// The whole point: a stopped service with `restart: always` is brought back.
func TestSuperviseRestartsAStoppedService(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, calls := superviseShim(t, "stopped")
	o := New(superviseProject(t, "always"), rt, "opossum", os.Stderr)

	var logbuf strings.Builder
	pols := map[string]compose.RestartPolicy{"web": pol(t, "always")}
	o.superviseOnce(pols, map[string]serviceState{}, func(f string, a ...interface{}) {
		fmt.Fprintf(&logbuf, f+"\n", a...)
	})
	if !strings.Contains(calls(), "start web.demo.opossum") {
		t.Errorf("a stopped `restart: always` service should be started, calls:\n%s", calls())
	}
	if !strings.Contains(logbuf.String(), "[OPSM-409]") || !strings.Contains(logbuf.String(), "restarted") {
		t.Errorf("the restart should be logged with its code, got:\n%s", logbuf.String())
	}
}

// A running service is left alone — the supervisor must not churn what works.
func TestSuperviseLeavesRunningServiceAlone(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, calls := superviseShim(t, "running")
	o := New(superviseProject(t, "always"), rt, "opossum", os.Stderr)
	o.superviseOnce(map[string]compose.RestartPolicy{"web": pol(t, "always")},
		map[string]serviceState{}, func(string, ...interface{}) {})
	if strings.Contains(calls(), "start ") {
		t.Errorf("a running service must not be restarted, calls:\n%s", calls())
	}
}

// A container that no longer exists was removed, not crashed. Recreating it would
// resurrect a project the user took apart — `restart:` does not ask for that.
func TestSuperviseIgnoresAMissingContainer(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	shim := filepath.Join(dir, "c.sh")
	// inspect fails => the container doesn't exist.
	body := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %s\n[ \"$1\" = inspect ] && exit 1\nexit 0\n", log)
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	o := New(superviseProject(t, "always"), &runtime.Runtime{Bin: shim}, "opossum", os.Stderr)
	o.superviseOnce(map[string]compose.RestartPolicy{"web": pol(t, "always")},
		map[string]serviceState{}, func(string, ...interface{}) {})
	b, _ := os.ReadFile(log)
	if strings.Contains(string(b), "start ") {
		t.Errorf("a removed container must not be recreated, calls:\n%s", b)
	}
}

// `unless-stopped` must honour a stop opossum performed. The runtime doesn't
// record who stopped a container, so opossum leaves itself a note.
func TestSuperviseHonoursOurOwnStop(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, calls := superviseShim(t, "stopped")
	o := New(superviseProject(t, "unless-stopped"), rt, "opossum", os.Stderr)
	o.MarkStopped("web")

	o.superviseOnce(map[string]compose.RestartPolicy{"web": pol(t, "unless-stopped")},
		map[string]serviceState{}, func(string, ...interface{}) {})
	if strings.Contains(calls(), "start ") {
		t.Errorf("`unless-stopped` must not undo an explicit stop, calls:\n%s", calls())
	}
	// And once the note is cleared (a later `start`), it is supervised again.
	o.ClearStopped("web")
	o.superviseOnce(map[string]compose.RestartPolicy{"web": pol(t, "unless-stopped")},
		map[string]serviceState{}, func(string, ...interface{}) {})
	if !strings.Contains(calls(), "start web.demo.opossum") {
		t.Errorf("after clearing the stop it should be supervised again, calls:\n%s", calls())
	}
}

// A service another service waits on with `service_completed_successfully` is
// meant to exit. Watching it would turn a finished job into a loop.
func TestSupervisedServicesExcludesOneShots(t *testing.T) {
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"migrate": {Name: "migrate", Image: "m", Restart: "always"},
		"app": {Name: "app", Image: "a", Restart: "always", DependsOn: compose.DependsOn{
			{Name: "migrate", Condition: compose.ConditionCompleted},
		}},
	}}
	o := New(p, nil, "opossum", os.Stderr)
	got := o.SupervisedServices([]string{"migrate", "app"})
	if len(got) != 1 || got[0] != "app" {
		t.Errorf("a run-to-completion dependency must not be supervised, got %v", got)
	}
}

// Only services that asked for it are watched, so a project without `restart:`
// never grows a supervisor.
func TestSupervisedServicesSkipsNoPolicy(t *testing.T) {
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Name: "a", Image: "x"},
		"b": {Name: "b", Image: "x", Restart: "no"},
		"c": {Name: "c", Image: "x", Restart: "always"},
	}}
	o := New(p, nil, "opossum", os.Stderr)
	got := o.SupervisedServices([]string{"a", "b", "c"})
	if len(got) != 1 || got[0] != "c" {
		t.Errorf("only `restart:` services should be watched, got %v", got)
	}
}

// countingShim reports a state that a test can change between polls, and counts
// `start` calls — so a sequence of polls can be checked, not just one.
func countingShim(t *testing.T, state *string) (*runtime.Runtime, func() int) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	statef := filepath.Join(dir, "state")
	shim := filepath.Join(dir, "c.sh")
	body := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %s\ncase \"$1\" in\n"+
		"  inspect) printf '[{\"status\":{\"state\":\"%%s\"},\"configuration\":{\"labels\":{\"opossum.project\":\"demo\"}}}]' \"$(cat %s)\" ;;\n"+
		"  system) echo 'status running' ;;\nesac\nexit 0\n", log, statef)
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statef, []byte(*state), 0o644); err != nil {
		t.Fatal(err)
	}
	set := func(s string) { _ = os.WriteFile(statef, []byte(s), 0o644) }
	_ = set
	return &runtime.Runtime{Bin: shim}, func() int {
		b, _ := os.ReadFile(log)
		return strings.Count(string(b), "start ")
	}
}

// Backoff has to actually delay: without it a crash-looping service is restarted
// on every poll, hammering the runtime. Driving the clock shows the escalation.
func TestSuperviseBacksOffBetweenRestarts(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	st := "stopped"
	rt, starts := countingShim(t, &st)
	o := New(superviseProject(t, "always"), rt, "opossum", os.Stderr)
	pols := map[string]compose.RestartPolicy{"web": pol(t, "always")}
	state := map[string]serviceState{}
	quiet := func(string, ...interface{}) {}

	base := time.Now()
	o.superviseAt(base, pols, state, quiet) // 1st: immediate
	if starts() != 1 {
		t.Fatalf("the first restart should be immediate, got %d start(s)", starts())
	}
	// Straight away again: the backoff for the 2nd restart must hold it back.
	o.superviseAt(base.Add(100*time.Millisecond), pols, state, quiet)
	if starts() != 1 {
		t.Errorf("a second restart must wait for the backoff, got %d start(s)", starts())
	}
	// Once the backoff has elapsed, it restarts again.
	o.superviseAt(base.Add(backoffFor(1)+time.Second), pols, state, quiet)
	if starts() != 2 {
		t.Errorf("after the backoff it should restart, got %d start(s)", starts())
	}
}

// `on-failure` must actually stop trying — the log line is the only signal a user
// gets that opossum decided the service had finished rather than crashed.
func TestSuperviseGivesUpOnFailureAfterTheBound(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	st := "stopped"
	rt, starts := countingShim(t, &st)
	o := New(superviseProject(t, "on-failure"), rt, "opossum", os.Stderr)
	pols := map[string]compose.RestartPolicy{"web": pol(t, "on-failure")}
	state := map[string]serviceState{}
	var log strings.Builder
	logf := func(f string, a ...interface{}) { fmt.Fprintf(&log, f+"\n", a...) }

	now := time.Now()
	for i := 0; i < 10; i++ {
		o.superviseAt(now, pols, state, logf)
		now = now.Add(time.Minute) // well past any backoff
	}
	if starts() > 3 {
		t.Errorf("on-failure should stop after its bound, got %d start(s)", starts())
	}
	if !strings.Contains(log.String(), "giving up") {
		t.Errorf("giving up should be logged, got:\n%s", log.String())
	}
	if strings.Count(log.String(), "giving up") != 1 {
		t.Errorf("it should say so once, not every poll:\n%s", log.String())
	}
}

// `container stop` is not instantaneous, so a poll can land while the container is
// still running after `opossum stop` wrote its marker. Clearing the marker there
// would delete the record of the stop, and the next poll — seeing a stopped
// container with no marker — would undo what the user just asked for. The marker
// therefore survives being seen running; only `up` and `start` clear it.
func TestSuperviseDoesNotClearTheStopMarkerWhileRunning(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, calls := superviseShim(t, "running")
	o := New(superviseProject(t, "always"), rt, "opossum", os.Stderr)
	o.MarkStopped("web")

	pols := map[string]compose.RestartPolicy{"web": pol(t, "always")}
	state := map[string]serviceState{}
	// A poll that catches the container still running must not erase the record.
	o.superviseAt(time.Now(), pols, state, func(string, ...interface{}) {})
	if !o.wasStoppedByUs("web") {
		t.Fatal("a poll during the stop must not erase the marker — the stop would then be undone")
	}
	_ = calls
}

// …and once it is stopped, the recorded stop is honoured rather than undone.
func TestSuperviseHonoursAStopThatIsStillSettling(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	statef := filepath.Join(dir, "state")
	log := filepath.Join(dir, "calls.log")
	shim := filepath.Join(dir, "c.sh")
	body := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %s\ncase \"$1\" in\n"+
		"  inspect) printf '[{\"status\":{\"state\":\"%%s\"},\"configuration\":{\"labels\":{\"opossum.project\":\"demo\"}}}]' \"$(cat %s)\" ;;\n"+
		"esac\nexit 0\n", log, statef)
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(statef, []byte("running"), 0o644)

	o := New(superviseProject(t, "always"), &runtime.Runtime{Bin: shim}, "opossum", os.Stderr)
	o.MarkStopped("web")
	pols := map[string]compose.RestartPolicy{"web": pol(t, "always")}
	state := map[string]serviceState{}

	now := time.Now()
	o.superviseAt(now, pols, state, func(string, ...interface{}) {}) // still running
	os.WriteFile(statef, []byte("stopped"), 0o644)
	o.superviseAt(now.Add(pollInterval), pols, state, func(string, ...interface{}) {}) // now stopped

	b, _ := os.ReadFile(log)
	if strings.Contains(string(b), "start web.demo.opossum") {
		t.Errorf("a stop the user asked for must not be undone, calls:\n%s", b)
	}
	// `start` is what says "bring it back" — after that, supervision resumes.
	o.ClearStopped("web")
	o.superviseAt(now.Add(2*pollInterval), pols, state, func(string, ...interface{}) {})
	b, _ = os.ReadFile(log)
	if !strings.Contains(string(b), "start web.demo.opossum") {
		t.Errorf("after the stop is cleared it should be supervised again, calls:\n%s", b)
	}
}

// A service `kill` stopped is not brought back by the supervisor, whatever the
// signal, as docker compose v5.5.0 does not restart a container `kill` stopped
// under `restart: always` (measured: SIGKILL, and TERM to a container that
// exits on it, both stay exited; one that exits from inside is restarted). The
// record is on disk before the signal is sent, and `start` resumes supervision.
func TestSuperviseLeavesAKilledServiceStopped(t *testing.T) {
	for _, signal := range []string{"", "TERM", "HUP"} {
		t.Run("signal "+signal, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			dir := t.TempDir()
			statef := filepath.Join(dir, "state")
			log := filepath.Join(dir, "calls.log")
			shim := filepath.Join(dir, "c.sh")
			o := New(superviseProject(t, "always"), nil, "opossum", os.Stderr)
			marker, err := o.stopMarkerPath("web")
			if err != nil {
				t.Fatal(err)
			}
			// On `kill`, the shim logs whether the record was already on disk.
			body := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %s\ncase \"$1\" in\n"+
				"  kill) if [ -e %s ]; then echo recorded-before-signal >> %s; fi ;;\n"+
				"  inspect) printf '[{\"status\":{\"state\":\"%%s\"},\"configuration\":{\"labels\":{\"opossum.project\":\"demo\"}}}]' \"$(cat %s)\" ;;\n"+
				"  system) echo 'status running' ;;\n"+
				"esac\nexit 0\n", log, marker, log, statef)
			if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(statef, []byte("running"), 0o644)
			o.rt = &runtime.Runtime{Bin: shim}

			if err := o.Kill(nil, signal); err != nil {
				t.Fatalf("kill: %v", err)
			}
			b, _ := os.ReadFile(log)
			if !strings.Contains(string(b), "recorded-before-signal") {
				t.Fatalf("the stop must be recorded before the signal is sent, calls:\n%s", b)
			}
			// The container ends — at once, or later on its own after a signal
			// it did not end on — and the supervisor polls.
			os.WriteFile(statef, []byte("stopped"), 0o644)
			pols := map[string]compose.RestartPolicy{"web": pol(t, "always")}
			state := map[string]serviceState{}
			now := time.Now()
			o.superviseAt(now, pols, state, func(string, ...interface{}) {})
			o.superviseAt(now.Add(10*pollInterval), pols, state, func(string, ...interface{}) {})
			b, _ = os.ReadFile(log)
			if strings.Contains(string(b), "start web.demo.opossum") {
				t.Errorf("a killed service must not be restarted, calls:\n%s", b)
			}
			// `start` says bring it back: supervision resumes.
			if err := o.Start(nil); err != nil {
				t.Fatalf("start: %v", err)
			}
			if o.wasStoppedByUs("web") {
				t.Error("start should clear the record of the kill")
			}
		})
	}
}

// `api.v2`, `api_v2` and `API-V2` are three legal, distinct compose services that
// all sanitise to the same string. Keying the stop marker on that would make
// stopping one silence supervision for the others.
func TestStopMarkersDoNotCollideAcrossServiceNames(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{}}
	o := New(p, nil, "opossum", os.Stderr)
	seen := map[string]string{}
	for _, name := range []string{"api.v2", "api_v2", "API-V2", "api-v2"} {
		path, err := o.stopMarkerPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if other, dup := seen[path]; dup {
			t.Errorf("services %q and %q share the marker %q", other, name, path)
		}
		seen[path] = name
	}
	// And marking one must not mark another.
	o.MarkStopped("api.v2")
	if o.wasStoppedByUs("api_v2") {
		t.Error("stopping api.v2 must not mark api_v2 as stopped")
	}
	if !o.wasStoppedByUs("api.v2") {
		t.Error("the service that was stopped should be marked")
	}
}

// The startup race this feature exists for: a service loses to its database on
// the first try and would succeed on the second. Under supervision the project
// reaches "everything running" without the user doing anything — which is why the
// first restart is immediate rather than backed off.
func TestSuperviseAbsorbsAStartupRace(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	statef := filepath.Join(dir, "state")
	log := filepath.Join(dir, "calls.log")
	shim := filepath.Join(dir, "c.sh")
	// The container is stopped (it lost the race). A `start` flips it to running,
	// the way a real second attempt would once the database is up.
	body := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %s\ncase \"$1\" in\n"+
		"  inspect) printf '[{\"status\":{\"state\":\"%%s\"},\"configuration\":{\"labels\":{\"opossum.project\":\"demo\"}}}]' \"$(cat %s)\" ;;\n"+
		"  start) echo running > %s ;;\nesac\nexit 0\n", log, statef, statef)
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(statef, []byte("stopped"), 0o644)

	o := New(superviseProject(t, "always"), &runtime.Runtime{Bin: shim}, "opossum", os.Stderr)
	pols := map[string]compose.RestartPolicy{"web": pol(t, "always")}
	state := map[string]serviceState{}

	now := time.Now()
	o.superviseAt(now, pols, state, func(string, ...interface{}) {})
	if got, _ := os.ReadFile(statef); strings.TrimSpace(string(got)) != "running" {
		t.Fatalf("the first poll should have brought it back, state=%q", got)
	}
	// The recovery holds: a later poll leaves the now-running service alone.
	o.superviseAt(now.Add(pollInterval), pols, state, func(string, ...interface{}) {})
	b, _ := os.ReadFile(log)
	if n := strings.Count(string(b), "start "); n != 1 {
		t.Errorf("a recovered service should be started once, got %d", n)
	}
}

// A watcher whose project has been taken apart by other means has nothing left to
// do, and a resident process with nothing to watch is exactly what this feature
// must not leave behind. It reports "nothing exists" so the loop can stop.
func TestSuperviseReportsWhenNothingExists(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	shim := filepath.Join(dir, "c.sh")
	// inspect fails => no such container.
	if err := os.WriteFile(shim, []byte("#!/bin/sh\n[ \"$1\" = inspect ] && exit 1\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	o := New(superviseProject(t, "always"), &runtime.Runtime{Bin: shim}, "opossum", os.Stderr)
	pols := map[string]compose.RestartPolicy{"web": pol(t, "always")}
	if exists, _ := o.superviseAt(time.Now(), pols, map[string]serviceState{}, func(string, ...interface{}) {}); exists {
		t.Error("a project whose containers are gone should report nothing to watch")
	}
}

// …and while a container is there — running or not — it keeps watching.
func TestSuperviseKeepsWatchingWhileAContainerExists(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, state := range []string{"running", "stopped"} {
		rt, _ := superviseShim(t, state)
		o := New(superviseProject(t, "always"), rt, "opossum", os.Stderr)
		pols := map[string]compose.RestartPolicy{"web": pol(t, "always")}
		if exists, _ := o.superviseAt(time.Now(), pols, map[string]serviceState{}, func(string, ...interface{}) {}); !exists {
			t.Errorf("state %q: an existing container means there is still something to watch", state)
		}
	}
}

// The giving-up line names the code, then the service, then how many restarts
// it took and which policy ran out. The code and the service are both strings on
// one format call: exchanged, the line would open with the service in brackets
// and give up on the code (#559).
func TestTheGivingUpLineNamesTheCodeThenTheService(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	st := "stopped"
	rt, _ := countingShim(t, &st)
	o := New(superviseProject(t, "on-failure:1"), rt, "opossum", os.Stderr)
	pols := map[string]compose.RestartPolicy{"web": pol(t, "on-failure:1")}
	state := map[string]serviceState{"web": {restarts: 1}}
	var logged strings.Builder
	logf := func(format string, args ...interface{}) { fmt.Fprintf(&logged, format+"\n", args...) }

	o.superviseAt(time.Now(), pols, state, logf)

	if want := "[" + string(codeSupervisorAction) + `] giving up on "web" after 1 restart(s): its ` + "`restart: on-failure`" + " has no more retries."; !strings.Contains(logged.String(), want) {
		t.Errorf("the line should read %q, got:\n%s", want, logged.String())
	}
}

// The restart line names the code, the service, the attempt and the policy
// that asked for it. The service and the policy's mode are both strings on one
// format call: exchanged, the line would restart "always" under a policy called
// web (#559).
func TestTheRestartLineNamesTheServiceThenThePolicy(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	st := "stopped"
	rt, _ := countingShim(t, &st)
	o := New(superviseProject(t, "always"), rt, "opossum", os.Stderr)
	pols := map[string]compose.RestartPolicy{"web": pol(t, "always")}
	state := map[string]serviceState{}
	var logged strings.Builder
	logf := func(format string, args ...interface{}) { fmt.Fprintf(&logged, format+"\n", args...) }

	o.superviseAt(time.Now(), pols, state, logf)

	if want := "[" + string(codeSupervisorAction) + `] restarted "web" (attempt 1; ` + "`restart: always`)"; !strings.Contains(logged.String(), want) {
		t.Errorf("the line should read %q, got:\n%s", want, logged.String())
	}
}

// multiShim is a shell stand-in for the runtime whose `inspect` answers per
// container: a name listed in <dir>/unknown-<name> fails the way a stopped
// apiserver fails (not "not found"); otherwise the container is reported in
// the state written to <dir>/state-<name> ("stopped" if none). Every call is
// logged; `system status --format json` answers as container 1.4.1 does —
// `{"status":"running"}`, or exit 1 with `{"status":"unregistered"}` when
// <dir>/runtime-down exists (testdata/real-cli-output.md) — so one probe is
// one call, appended to <dir>/system.log for a test to count.
func multiShim(t *testing.T) (bin, dir string, calls func() string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "c.sh")
	body := fmt.Sprintf(`#!/bin/sh
d=%s
echo "$@" >> "$d/calls.log"
case "$1" in
  inspect)
    n=$(echo "$2" | cut -d. -f1)
    if [ -e "$d/unknown-$n" ]; then echo 'Error: apiserver is not running and not registered with launchd' >&2; exit 1; fi
    st=stopped; [ -e "$d/state-$n" ] && st=$(cat "$d/state-$n")
    echo "[{\"status\":{\"state\":\"$st\"},\"configuration\":{\"labels\":{\"opossum.project\":\"demo\"}}}]" ;;
  system)
    echo x >> "$d/system.log"
    if [ -e "$d/runtime-down" ]; then echo '{"status":"unregistered"}'; exit 1; fi
    echo '{"status":"running"}' ;;
esac
exit 0
`, dir)
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir, func() string { b, _ := os.ReadFile(filepath.Join(dir, "calls.log")); return string(b) }
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A poll the runtime cannot answer about a service is not a poll that found it
// gone: that service is neither restarted (nothing is known) nor written off,
// its state (backoff, giving up) is kept, and the other services are still
// looked after in the same poll. Two services — one unanswered, one stopped —
// so an unanswered service that ended the poll, or reset what was known about
// it, shows. Before this, an unanswered poll read as "gone", and enough of them
// in a row ended the supervisor with a line claiming none existed.
func TestSuperviseNeitherActsNorGivesUpWhenTheRuntimeCannotBeAsked(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	project := func() *compose.Project {
		return &compose.Project{Name: "demo", Services: map[string]*compose.Service{
			"web": {Name: "web", Image: "w", Restart: "always"},
			"db":  {Name: "db", Image: "d", Restart: "always"},
		}}
	}
	pols := map[string]compose.RestartPolicy{"web": pol(t, "always"), "db": pol(t, "always")}
	quiet := func(string, ...interface{}) {}

	t.Run("the unanswered one is left alone, the other is still restarted", func(t *testing.T) {
		// Map order is random: ten rounds, so an unanswered service that ended
		// the poll for whatever came after it shows (a miss is 1 in 1024).
		for round := 0; round < 10; round++ {
			bin, dir, calls := multiShim(t)
			touch(t, filepath.Join(dir, "unknown-web"))
			o := New(project(), &runtime.Runtime{Bin: bin}, "opossum", os.Stderr)
			exists, unknown := o.superviseAt(time.Now(), pols, map[string]serviceState{}, quiet)
			if !exists || strings.Join(unknown, ",") != "web" {
				t.Fatalf("round %d: db is there and web could not be asked about: exists=%v unknown=%v", round, exists, unknown)
			}
			if strings.Contains(calls(), "start web.demo.opossum") {
				t.Fatalf("round %d: nothing is known about web, so it is not restarted, got %q", round, calls())
			}
			if !strings.Contains(calls(), "start db.demo.opossum") {
				t.Fatalf("round %d: db is stopped and answered about — it is restarted in the same poll, got %q", round, calls())
			}
		}
	})
	// Every service the runtime could not be asked about is named, in order —
	// in a real outage that is all of them. Three services, two unanswered: the
	// order comes from a map walk, not from the input, so rounds (not testpair's
	// two input orders) are what make an unsorted or truncated list show.
	t.Run("all the unanswered are named, sorted", func(t *testing.T) {
		three := func() *compose.Project {
			return &compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"web": {Name: "web", Image: "w", Restart: "always"}, "db": {Name: "db", Image: "d", Restart: "always"},
				"cache": {Name: "cache", Image: "c", Restart: "always"},
			}}
		}
		pols3 := map[string]compose.RestartPolicy{"web": pol(t, "always"), "db": pol(t, "always"), "cache": pol(t, "always")}
		for round := 0; round < 10; round++ {
			bin, dir, _ := multiShim(t)
			touch(t, filepath.Join(dir, "unknown-web"))
			touch(t, filepath.Join(dir, "unknown-db"))
			o := New(three(), &runtime.Runtime{Bin: bin}, "opossum", os.Stderr)
			if _, unknown := o.superviseAt(time.Now(), pols3, map[string]serviceState{}, quiet); strings.Join(unknown, ",") != "db,web" {
				t.Fatalf("round %d: want [db web], got %v", round, unknown)
			}
		}
	})
	t.Run("once answered again, a stopped service is picked back up", func(t *testing.T) {
		bin, dir, calls := multiShim(t)
		touch(t, filepath.Join(dir, "unknown-web"))
		o := New(project(), &runtime.Runtime{Bin: bin}, "opossum", os.Stderr)
		state := map[string]serviceState{}
		o.superviseAt(time.Now(), pols, state, quiet)
		os.Remove(filepath.Join(dir, "unknown-web"))
		if _, unknown := o.superviseAt(time.Now().Add(pollInterval), pols, state, quiet); len(unknown) != 0 {
			t.Errorf("answered now, got unknown=%v", unknown)
		}
		if !strings.Contains(calls(), "start web.demo.opossum") {
			t.Errorf("web is restarted on the next answered poll, got %q", calls())
		}
	})
	t.Run("an outage does not undo giving up", func(t *testing.T) {
		bin, dir, calls := multiShim(t)
		touch(t, filepath.Join(dir, "unknown-web"))
		o := New(project(), &runtime.Runtime{Bin: bin}, "opossum", os.Stderr)
		state := map[string]serviceState{"web": {gaveUp: true, restarts: 3}}
		o.superviseAt(time.Now(), pols, state, quiet)
		if st := state["web"]; !st.gaveUp || st.restarts != 3 {
			t.Errorf("an unanswered poll must leave what was known about web as it was, got %+v", st)
		}
		os.Remove(filepath.Join(dir, "unknown-web"))
		o.superviseAt(time.Now().Add(pollInterval), pols, state, quiet)
		if strings.Contains(calls(), "start web.demo.opossum") {
			t.Errorf("web was given up on before the outage and stays given up on, got %q", calls())
		}
	})
}

// The watch stops only on answered polls that found nothing. Unanswered polls
// are said — on the first and every idlePollsBeforeExit-th, naming only the
// services that went unanswered — and never add up to a "nothing left" stop;
// the runtime itself is asked on exactly those bound polls, once each. Both
// orders (empty then unanswered, unanswered then empty) sit here so that a
// counter that reset on the wrong event shows on one of them.
func TestTheWatchStopsOnlyOnAnsweredEmptyPolls(t *testing.T) {
	services := []string{"db", "web"}
	web := []string{"web"}
	up := func() bool { return true }
	t.Run("unanswered polls never stop the watch, are said sparingly, and name only the unanswered", func(t *testing.T) {
		var w watch
		var said []string
		asked := 0
		for i := 0; i < 3*idlePollsBeforeExit; i++ {
			line, stop := w.after(false, web, func() bool { asked++; return true }, services)
			if stop {
				t.Fatalf("poll %d: an unanswered poll must not stop the watch", i+1)
			}
			if line != "" {
				said = append(said, line)
			}
		}
		if len(said) != 4 || !strings.Contains(said[0], "could not be asked about [web] (1 poll(s))") || !strings.Contains(said[3], fmt.Sprintf("(%d poll(s))", 3*idlePollsBeforeExit)) || strings.Contains(strings.Join(said, "\n"), "db") {
			t.Errorf("want the outage said on poll 1 and every %d, naming web only, got %q", idlePollsBeforeExit, said)
		}
		if asked != 3 {
			t.Errorf("the runtime is asked on each bound poll and no other, want 3 over %d polls, got %d", 3*idlePollsBeforeExit, asked)
		}
	})
	t.Run("a dead runtime stops the watch at the bound, with the true reason", func(t *testing.T) {
		var w watch
		down := func() bool { return false }
		for i := 1; i < idlePollsBeforeExit; i++ {
			if _, stop := w.after(false, web, down, services); stop {
				t.Fatalf("poll %d: the runtime is not asked before the bound, so the watch does not stop", i)
			}
		}
		line, stop := w.after(false, web, down, services)
		if !stop || !strings.Contains(line, fmt.Sprintf("the runtime is not running ([web] could not be asked about for %d polls)", idlePollsBeforeExit)) || !strings.Contains(line, "`opossum up` starts watching again") || strings.Contains(line, "nothing left to watch") {
			t.Errorf("want the stop at poll %d saying the runtime is down, got stop=%v %q", idlePollsBeforeExit, stop, line)
		}
	})
	// One step later: the empty polls before an outage are not counted towards
	// it — the runtime is asked on the 20th unanswered poll, not sooner.
	t.Run("empty polls before an outage do not bring the probe forward", func(t *testing.T) {
		var w watch
		down := func() bool { return false }
		for i := 0; i < 5; i++ {
			w.after(false, nil, up, services)
		}
		for i := 1; i < idlePollsBeforeExit; i++ {
			if _, stop := w.after(false, web, down, services); stop {
				t.Fatalf("unanswered poll %d: the runtime is not asked before the 20th unanswered poll", i)
			}
		}
		if line, stop := w.after(false, web, down, services); !stop || !strings.Contains(line, fmt.Sprintf("for %d polls", idlePollsBeforeExit)) {
			t.Errorf("want the stop on the 20th unanswered poll, got stop=%v %q", stop, line)
		}
	})
	// A runtime that answered at the first bound and is down at the second: the
	// stop names the polls actually unanswered, not the bound.
	t.Run("a runtime that goes down later is reported with the real count", func(t *testing.T) {
		var w watch
		asked := 0
		thenDown := func() bool { asked++; return asked == 1 }
		var line string
		var stop bool
		for i := 1; i <= 2*idlePollsBeforeExit && !stop; i++ {
			line, stop = w.after(false, web, thenDown, services)
		}
		if !stop || !strings.Contains(line, fmt.Sprintf("could not be asked about for %d polls", 2*idlePollsBeforeExit)) {
			t.Errorf("want the stop at poll %d with that count, got stop=%v %q", 2*idlePollsBeforeExit, stop, line)
		}
	})
	t.Run("answered empty polls stop the watch after the bound, and name what had no container of this project's", func(t *testing.T) {
		var w watch
		for i := 1; i < idlePollsBeforeExit; i++ {
			if _, stop := w.after(false, nil, up, services); stop {
				t.Fatalf("poll %d: too early to stop", i)
			}
		}
		line, stop := w.after(false, nil, up, services)
		if !stop || !strings.Contains(line, "none of [db web] has had a container of this project's for") {
			t.Errorf("want the stop with its reason, got stop=%v %q", stop, line)
		}
	})
	t.Run("an unanswered poll between empty ones neither counts nor resets", func(t *testing.T) {
		var w watch
		for i := 1; i < idlePollsBeforeExit; i++ {
			w.after(false, nil, up, services)
		}
		if _, stop := w.after(false, web, up, services); stop {
			t.Fatal("an unanswered poll must not be the one that stops the watch")
		}
		if _, stop := w.after(false, nil, up, services); !stop {
			t.Error("the next answered empty poll completes the bound: the unanswered one did not reset it")
		}
	})
	t.Run("unanswered polls do not add up with empty ones", func(t *testing.T) {
		var w watch
		for i := 0; i < idlePollsBeforeExit/2; i++ {
			w.after(false, nil, up, services)
		}
		for i := 0; i < idlePollsBeforeExit; i++ {
			w.after(false, web, up, services)
		}
		if _, stop := w.after(false, nil, up, services); stop {
			t.Error("ten empty polls, twenty unanswered and one more empty are eleven empty polls, not thirty-one")
		}
	})
	// An outage interrupted by an answer starts counting from one again —
	// whether the answer found a container or found none.
	for _, between := range []struct {
		name   string
		exists bool
	}{{"a poll that found a container", true}, {"an answered poll that found none", false}} {
		t.Run(between.name+" ends an outage: the next one starts from one", func(t *testing.T) {
			var w watch
			for i := 1; i < idlePollsBeforeExit; i++ {
				w.after(false, web, up, services)
			}
			w.after(between.exists, nil, up, services)
			line, _ := w.after(false, web, up, services)
			if !strings.Contains(line, "(1 poll(s))") {
				t.Errorf("want the outage counted from 1 again, got %q", line)
			}
		})
	}
	t.Run("a poll that found a container resets the empty count", func(t *testing.T) {
		var w watch
		for i := 1; i < idlePollsBeforeExit; i++ {
			w.after(false, nil, up, services)
		}
		w.after(true, nil, up, services)
		if _, stop := w.after(false, nil, up, services); stop {
			t.Error("after a container was seen the count starts over")
		}
	})
}

// The loop, not just the counter: with the runtime answering nothing about the
// container, Supervise keeps running while `system status` answers, and stops
// — with the true line, at the bound, having asked once — once that fails too.
// Both worlds come from one shim, told apart by a flag file, and the first is
// cancelled by count (two probes = forty polls), not by time, so a slow machine
// cannot end it before the bound it is meant to pass.
func TestSuperviseStopsOnlyWhenTheRuntimeItselfIsDown(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	saved := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = saved })
	probes := func(dir string) int {
		b, _ := os.ReadFile(filepath.Join(dir, "system.log"))
		return strings.Count(string(b), "x")
	}
	run := func(t *testing.T, bin string, ctx context.Context) string {
		t.Helper()
		var logw safeBuffer
		o := New(superviseProject(t, "always"), &runtime.Runtime{Bin: bin}, "opossum", os.Stderr)
		done := make(chan error, 1)
		go func() { done <- o.Supervise(ctx, []string{"web"}, &logw) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("supervise: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("supervise did not return")
		}
		return logw.String()
	}
	t.Run("the runtime answers: the watch goes on past the bound", func(t *testing.T) {
		bin, dir, _ := multiShim(t)
		touch(t, filepath.Join(dir, "unknown-web"))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			for ctx.Err() == nil && probes(dir) < 2 {
				time.Sleep(time.Millisecond)
			}
			cancel()
		}()
		got := run(t, bin, ctx)
		for _, want := range []string{"could not be asked about [web] (1 poll(s))", fmt.Sprintf("(%d poll(s)) — still watching", idlePollsBeforeExit), fmt.Sprintf("(%d poll(s)) — still watching", 2*idlePollsBeforeExit), "stopping (asked to exit)"} {
			if !strings.Contains(got, want) {
				t.Errorf("want %q in the log, got:\n%s", want, got)
			}
		}
		if strings.Contains(got, "the runtime is not running") || strings.Contains(got, "nothing left to watch") {
			t.Errorf("a runtime that answers must not stop the watch, got:\n%s", got)
		}
	})
	t.Run("the runtime is down: the watch stops at the bound and says so", func(t *testing.T) {
		bin, dir, _ := multiShim(t)
		touch(t, filepath.Join(dir, "unknown-web"))
		touch(t, filepath.Join(dir, "runtime-down"))
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		got := run(t, bin, ctx)
		if want := fmt.Sprintf("the runtime is not running ([web] could not be asked about for %d polls) — stopping; `opossum up` starts watching again", idlePollsBeforeExit); !strings.Contains(got, want) {
			t.Errorf("want %q, got:\n%s", want, got)
		}
		if n := probes(dir); n != 1 {
			t.Errorf("the runtime is asked once, at the bound, got %d", n)
		}
		if strings.Contains(got, "nothing left to watch") || strings.Contains(got, "asked to exit") {
			t.Errorf("must stop on its own and not claim there is nothing left to watch, got:\n%s", got)
		}
	})
}

// safeBuffer is a bytes.Buffer the supervisor's goroutine can write to while
// the test reads it afterwards.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *safeBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }
