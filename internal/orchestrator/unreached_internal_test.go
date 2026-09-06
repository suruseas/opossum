package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	rt "github.com/suruseas/opossum/internal/runtime"
)

// Lines a sweep found that no test reached.
//
// Exchanging two arguments of the same kind in each of these changed nothing any
// test could see — not because a check was weak, but because nothing ran the
// line at all. A message nobody has executed is a message nobody has read, and
// the ones here are the messages a person meets on their worst day: a service
// that died on startup, a dependency that will not come up, a volume that could
// not be prepared, a restart that failed.
//
// Six of the eight assert the whole sentence. The other two — the volume warning
// and the failed restart — are six lines and a quoted runtime error, and are
// checked by the parts that carry meaning rather than word for word: the volume
// and image named, what the reader is left holding, and the runtime's own words.
// The exchange that prompted all of this is one of the ways such a line can be
// wrong, and it is the way least visible to anything weaker.

// A watch rule whose action opossum has no automation for says so, and says
// which change, which action, and which service.
func TestAnUnautomatedWatchActionSaysWhatItCannotDo(t *testing.T) {
	rt, _ := watchShim(t)
	dir := t.TempDir()
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"app": {Name: "app", Image: "a", Develop: &compose.Develop{
			Watch: []compose.WatchRule{{Action: "sync+exec", Path: dir, Target: "/x"}},
		}},
	}}
	var out bytes.Buffer
	o := New(p, rt, "opossum", &out)

	svc, kind, matched := o.handleChange(dir + "/f.js")
	if !matched || svc != "" || kind != "" {
		t.Errorf("the rule matched but nothing was done: svc=%q kind=%q matched=%v", svc, kind, matched)
	}
	want := "watch: \"" + dir + "/f.js\" change needs action \"sync+exec\" for app, " +
		"which isn't automated yet — re-run `opossum up --build`\n"
	if out.String() != want {
		t.Errorf("got  %q\nwant %q", out.String(), want)
	}
}

// A dependency declared healthy without a healthcheck is warned about, naming
// which service wants which.
//
// The compose reader refuses this at load time — TestServiceHealthyRequiresHealthcheck
// over in internal/compose pins the refusal — so no compose file reaches this
// branch, which is why nothing had run it. The branch is still worth having and
// still worth reading: a Project can be built by something other than Load, and
// that is exactly how it is reached here.
func TestADependencyWithoutAHealthcheckIsNamedInBothDirections(t *testing.T) {
	rt, _ := watchShim(t)
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"web": {Name: "web", Image: "w"},
		"db":  {Name: "db", Image: "d"}, // no healthcheck at all
	}}
	var out bytes.Buffer
	o := New(p, rt, "opossum", &out)

	svc := &compose.Service{Name: "web", Image: "w", DependsOn: compose.DependsOn{
		{Name: "db", Condition: "service_healthy"},
	}}
	// All three spellings of "no healthcheck", because the branch is written as
	// three and the compose reader refuses all three. Only the first was covered
	// at first, and a version narrowed to `hc == nil` passed.
	//
	// The disabled one keeps its test on purpose. Written as an empty
	// Healthcheck it is also a healthcheck with no test, so the third clause
	// catches it and the second can be deleted with nothing going red — three
	// cases that look like three and are two. A healthcheck that keeps its test
	// while being switched off is the shape compose/load.go names as the day the
	// length check alone starts silently accepting this.
	for _, c := range []struct {
		name string
		hc   *compose.Healthcheck
	}{
		{"no healthcheck at all", nil},
		{"a healthcheck turned off, test and all", &compose.Healthcheck{Disabled: true, Test: []string{"CMD", "true"}}},
		{"a healthcheck with no test", &compose.Healthcheck{}},
	} {
		out.Reset()
		p.Services["db"].Healthcheck = c.hc
		if err := o.awaitHealthyDeps("web", svc); err != nil {
			t.Fatalf("%s: a dependency it will not wait for is not an error: %v", c.name, err)
		}
		// The two names are both plain strings on one format call: printed the
		// other way round this reads as db wanting web, which sends a reader to
		// the wrong service.
		if !strings.Contains(out.String(), "web wants db healthy but it has no healthcheck — not waiting") {
			t.Errorf("%s: got %q", c.name, out.String())
		}
	}
}

// A service that died on startup and left no logs still says what happened,
// naming the service and the state it ended in.
//
// The path with logs is covered elsewhere. This is the other one: nothing was
// captured, so the message is all the reader gets, and its two quoted strings —
// the service and the state — are the whole of what it tells them. Printed the
// other way round it reads as a service called "stopped" ending in a state
// called "web".
func TestAServiceThatDiedWithNoLogsStillNamesItselfAndItsState(t *testing.T) {
	var out bytes.Buffer
	o := New(webProject(), inspectShim(t, stoppedInspect, ""), "", &out)

	if err := o.verifyStarted([]string{"web"}, map[string]bool{}); err == nil {
		t.Fatal("a service that exited right after starting is a failure")
	}
	want := `service "web" exited right after starting (state "stopped") — check its command, image, and mounts`
	if !strings.Contains(out.String(), want) {
		t.Errorf("got\n%s\nwant it to contain\n%s", out.String(), want)
	}
}

// Asked to import a named service that has no build, opossum says so and says
// which image it will use instead.
//
// Only when the service was named: importing everything skips these quietly,
// because "you asked for this one and it has nothing to import" is news and
// "one of the twelve has nothing to import" is noise. The message carries the
// service and the image, both bare strings — the other way round it names an
// image that does not exist and an image that is really a service.
func TestImportSaysWhichNamedServiceHasNothingToImport(t *testing.T) {
	rt, _ := watchShim(t)
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"web": {Name: "web", Image: "nginx:1.27"}, // an image, not a build
	}}
	var out bytes.Buffer
	o := New(p, rt, "opossum", &out)

	if err := o.Import("web"); err != nil {
		t.Fatalf("nothing to import is not a failure: %v", err)
	}
	if want := "Skipping web: no build to import (uses image nginx:1.27)"; !strings.Contains(out.String(), want) {
		t.Errorf("got %q, want it to contain %q", out.String(), want)
	}

	// Unnamed, the same project says nothing about it.
	out.Reset()
	if err := o.Import(); err != nil {
		t.Fatalf("importing everything with nothing to import is not a failure: %v", err)
	}
	if strings.Contains(out.String(), "Skipping") {
		t.Errorf("nothing was asked for by name, so nothing is worth saying: %q", out.String())
	}
}

// A dependency whose container has stopped fails fast, naming the diagnostic
// code and the state it is in.
//
// The two are both strings on one format call and they sit in adjacent slots.
// Exchanged, the state lands where the code belongs and the code lands inside
// the parentheses — which is not merely confusing: the code is the thing a
// person searches for, and putting a state where it belongs takes the search
// away.
//
// The code is written here as the constant rather than as its number, for the
// reason the sentence above gives. A draft of this comment quoted the number of
// a different code — one that really exists, belonging to something else
// entirely — so a paragraph about codes being what people search for sent its
// reader to the wrong symptom. Prose holding a number drifts; a constant does
// not.
func TestAStoppedDependencyNamesTheCodeAndTheState(t *testing.T) {
	// The probe fails (so the wait does not succeed), the container reports as
	// exited (so the wait gives up rather than retrying), and there are no logs
	// (so the message is the short form, with nothing else standing in for it).
	rt := scriptShim(t, ""+
		"  inspect) echo '[{\"status\":{\"state\":\"exited\"},\"configuration\":{\"id\":\"db\"}}]' ;;\n"+
		"  logs) : ;;\n"+
		"  exec) exit 1 ;;\n")
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"db": {Name: "db", Image: "d"},
	}}
	o := New(p, rt, "opossum", &bytes.Buffer{})
	o.sleep = func(time.Duration) {}

	err := o.waitHealthy("db", &compose.Healthcheck{Test: []string{"CMD", "true"}, Retries: 1})
	if err == nil {
		t.Fatal("a dependency that has exited will not become healthy")
	}
	want := fmt.Sprintf("[%s] container is not running (state %q)", codeDepNotRunning, "exited")
	if err.Error() != want {
		t.Errorf("err  = %q\nwant = %q", err.Error(), want)
	}
}

// The supervisor says what it is watching and how often, when it starts.
//
// This is the first line in the log a person opens when they want to know
// whether the supervisor is alive at all. It carries the code, the services and
// the interval — three values of which two are printed with %v and %s, so
// exchanging them produces a line that still looks like a log line.
func TestTheSupervisorOpensItsLogBySayingWhatItWatches(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	shim, _ := superviseShim(t, "running")
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"web":    {Name: "web", Image: "w", Restart: "always"},
		"worker": {Name: "worker", Image: "k", Restart: "always"},
	}}
	o := New(p, shim, "opossum", &bytes.Buffer{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // stop at the first turn of the loop; the opening line is already out
	var log bytes.Buffer
	// Two services, not one — and it is this argument the line prints, not the
	// project. With one name, a version that printed only the first of them
	// reads exactly the same: the list is the whole point of the line, and a
	// fixture of one cannot show it being cut.
	if err := o.Supervise(ctx, []string{"web", "worker"}, &log); err != nil {
		t.Fatalf("a supervisor asked to stop is not a failure: %v", err)
	}
	want := fmt.Sprintf("[%s] supervising [web worker] (poll %s)", codeSupervisorStarted, pollInterval)
	if !strings.Contains(log.String(), want) {
		t.Errorf("log:\n%s\nwant a line containing %q", log.String(), want)
	}
}

// A restart that failed says so, naming the service and the reason.
//
// The success next door is covered; this is the branch where the runtime refused.
// The service name and the error are both printed on one call, and a reader who
// gets them the other way round is told a service called "exit status 1" could
// not be restarted.
func TestARestartThatFailedNamesTheServiceAndTheReason(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, _ := superviseShimStartFails(t, "stopped")
	o := New(superviseProject(t, "always"), rt, "opossum", &bytes.Buffer{})

	var log strings.Builder
	pols := map[string]compose.RestartPolicy{"web": pol(t, "always")}
	o.superviseOnce(pols, map[string]serviceState{}, func(f string, a ...interface{}) {
		fmt.Fprintf(&log, f+"\n", a...)
	})
	if want := fmt.Sprintf("[%s] couldn't restart %q: ", codeSupervisorAction, "web"); !strings.Contains(log.String(), want) {
		t.Errorf("log:\n%s\nwant the code, the service and then the reason", log.String())
	}
	// And the reason the runtime gave, not the fact that there was one. Stopping
	// at the colon leaves the %v free: the error can be dropped entirely and this
	// still passes, which is the same half-a-check the volume warning below is
	// careful not to make.
	if !strings.Contains(log.String(), "no such container") {
		t.Errorf("log:\n%s\nwant the runtime's own words in it", log.String())
	}
	if strings.Contains(log.String(), "restarted") {
		t.Errorf("nothing was restarted:\n%s", log.String())
	}
}

// superviseShimStartFails is superviseShim with a runtime that refuses to start
// anything, which is the branch the success case cannot reach.
func superviseShimStartFails(t *testing.T, state string) (*rt.Runtime, func() string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	shim := filepath.Join(dir, "c.sh")
	body := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %s\ncase \"$1\" in\n"+
		"  inspect) echo '[{\"status\":{\"state\":\"%s\"},\"configuration\":{\"labels\":{}}}]' ;;\n"+
		"  system) echo 'status running' ;;\n"+
		"  start) echo 'Error: no such container' >&2; exit 1 ;;\nesac\nexit 0\n", log, state)
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return &rt.Runtime{Bin: shim}, func() string {
		b, _ := os.ReadFile(log)
		return string(b)
	}
}

// A `nocopy` volume that could not be prepared says which volume, which image,
// and why — and says what the reader is left holding.
//
// `nocopy` asks for an empty volume, so nothing is copied into it. It is still a
// volume opossum creates, so `lost+found` still has to come out, and that needs
// `sh` inside the image. When the image has none — distroless and scratch builds
// — this is the only warning the reader gets, and it has to be enough to act on:
// Postgres's initdb refuses a data directory that is not empty.
//
// The volume name and the image are adjacent strings on one call. Exchanged,
// the line blames a volume named for an image and an image named for a volume.
//
// The name checked is the runtime one, project prefix and all. That is what a
// reader types into `container volume`; the compose file's bare `deps` would
// send them looking for something that does not exist.
func TestANoCopyVolumeThatWouldNotPrepareSaysWhichAndWhy(t *testing.T) {
	rt := scriptShim(t, ""+
		"  system) echo 'status running' ;;\n"+
		"  volume) if [ \"$2\" = ls ]; then echo 'NAME'; fi ;;\n"+ // the volume does not exist yet
		"  run) echo 'exec: \"sh\": executable file not found in $PATH' >&2; exit 1 ;;\n")
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"web": {Name: "web", Image: "web:latest",
			Volumes: []string{"deps:/app/node_modules"},
			NoCopy:  []string{"/app/node_modules"},
		},
	}}
	var out bytes.Buffer
	o := New(p, rt, "opossum", &out)

	o.seedVolumes("web", p.Services["web"], "web:latest")

	s := out.String()
	for _, want := range []string{
		`couldn't prepare the new volume "demo_deps" with web:latest`,
		"nothing was copied into it",
		"lost+found",
		"initdb",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("warning is missing %q:\n%s", want, s)
		}
	}
	// The reason the runtime gave, not a summary of it.
	if !strings.Contains(s, "executable file not found") {
		t.Errorf("the runtime's own words belong in it:\n%s", s)
	}
}

// The same for a volume opossum meant to fill: the warning names the volume, the
// image it copied from and the mount it copied for, in that order — three
// strings on one call, and exchanged the line would blame an image called after
// a volume (#559).
func TestAVolumeThatWouldNotFillSaysWhichImageAndWhere(t *testing.T) {
	rt := scriptShim(t, ""+
		"  system) echo 'status running' ;;\n"+
		"  volume) if [ \"$2\" = ls ]; then echo 'NAME'; fi ;;\n"+ // the volume does not exist yet
		"  run) echo 'exec: \"sh\": executable file not found in $PATH' >&2; exit 1 ;;\n")
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"web": {Name: "web", Image: "web:latest", Volumes: []string{"deps:/app/node_modules"}},
	}}
	var out bytes.Buffer
	o := New(p, rt, "opossum", &out)

	o.seedVolumes("web", p.Services["web"], "web:latest")

	if want := `couldn't fill the new volume "demo_deps" from web:latest at /app/node_modules:`; !strings.Contains(out.String(), want) {
		t.Errorf("the warning should open with %q, got:\n%s", want, out.String())
	}
}
