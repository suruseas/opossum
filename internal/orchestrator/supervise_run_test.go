package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		"  inspect) echo '[{\"status\":{\"state\":\"%s\"},\"configuration\":{\"labels\":{}}}]' ;;\n"+
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
		"  inspect) printf '[{\"status\":{\"state\":\"%%s\"},\"configuration\":{\"labels\":{}}}]' \"$(cat %s)\" ;;\n"+
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
		"  inspect) printf '[{\"status\":{\"state\":\"%%s\"},\"configuration\":{\"labels\":{}}}]' \"$(cat %s)\" ;;\n"+
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
		"  inspect) printf '[{\"status\":{\"state\":\"%%s\"},\"configuration\":{\"labels\":{}}}]' \"$(cat %s)\" ;;\n"+
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
	if o.superviseAt(time.Now(), pols, map[string]serviceState{}, func(string, ...interface{}) {}) {
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
		if !o.superviseAt(time.Now(), pols, map[string]serviceState{}, func(string, ...interface{}) {}) {
			t.Errorf("state %q: an existing container means there is still something to watch", state)
		}
	}
}
