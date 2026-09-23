package orchestrator_test

// Evals for a `run` whose one-off would carry a name the project already
// gives a service. The one-off's container is `<service>-run`, so a project
// with a service of that name cannot have both containers: starting the
// one-off makes a new container under that name and the service's is gone —
// what `--rm` removes afterwards is the one-off, and without it the one-off
// is what stays there, stopped (measured on container 1.4.1, 2026-09-21). The
// run is refused before anything starts.

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/orchestrator"
)

// oneOffNameTakenRefusal is the whole refusal, so a test says what a reader is
// told and not only that something was refused.
func oneOffNameTakenRefusal(service string) string {
	return "the one-off for service \"" + service + "\" is named \"" + service + "-run\", and the project has a service of that name — starting it would take the name: the container under it becomes the one-off's, whatever the service had there is gone, and a later `up` makes the service another container; rename the service \"" + service + "-run\""
}

// The run paths, with and without dependencies: the name is the one-off's own,
// so `--no-deps` does not make it free.
var oneOffPaths = []string{"run", "run --no-deps", "run --audit", "run --audit --no-deps"}

func runOneOffPath(o *orchestrator.Orchestrator, path, service string) error {
	opts := orchestrator.RunOneOffOptions{NoDeps: strings.HasSuffix(path, "--no-deps")}
	if strings.HasPrefix(path, "run --audit") {
		_, err := o.RunAudited(service, []string{"true"}, opts)
		return err
	}
	return o.RunOneOff(service, []string{"true"}, opts)
}

func TestARunIsRefusedWhenAServiceCarriesTheOneOffName(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		service string
		want    string // "" goes ahead
	}{{
		// The service of that name is one this run would start.
		name: "the service of that name is a dependency",
		body: "services:\n" +
			"  tool:\n    image: alpine:3.20\n    depends_on: [tool-run]\n" +
			"  tool-run:\n    image: alpine:3.20\n",
		service: "tool",
		want:    oneOffNameTakenRefusal("tool"),
	}, {
		// And one it leaves alone: the name is taken either way, and an `up`
		// while the run is going takes it back.
		name: "the service of that name is not started by this run",
		body: "services:\n" +
			"  tool:\n    image: alpine:3.20\n" +
			"  tool-run:\n    image: alpine:3.20\n",
		service: "tool",
		want:    oneOffNameTakenRefusal("tool"),
	}, {
		// A profile decides what starts, not what the file names. The
		// container of that name is made the moment the profile is active,
		// which can be while this one-off is running.
		name: "the service of that name is behind a profile that is not active",
		body: "services:\n" +
			"  tool:\n    image: alpine:3.20\n" +
			"  tool-run:\n    image: alpine:3.20\n    profiles: [debug]\n",
		service: "tool",
		want:    oneOffNameTakenRefusal("tool"),
	}, {
		// Contrast: `container_name:` is among the fields opossum does not
		// apply, so it cannot make a container of the one-off's name. The day
		// it is applied, this row moves to the refusing side — it is the one
		// that would go on passing while the collision came back.
		name: "another service writes container_name of that name",
		body: "services:\n" +
			"  tool:\n    image: alpine:3.20\n" +
			"  other:\n    image: alpine:3.20\n    container_name: tool-run\n",
		service: "tool",
	}, {
		// Contrast: a name that only begins the same way.
		name: "a service whose name starts with the one-off's",
		body: "services:\n" +
			"  tool:\n    image: alpine:3.20\n" +
			"  tool-runner:\n    image: alpine:3.20\n",
		service: "tool",
	}, {
		// Contrast: the collision belongs to the service being run, not to the
		// project. Running `other` is free while `tool-run` is defined.
		name: "the name is taken for another service than the one being run",
		body: "services:\n" +
			"  tool:\n    image: alpine:3.20\n" +
			"  tool-run:\n    image: alpine:3.20\n" +
			"  other:\n    image: alpine:3.20\n",
		service: "other",
	}, {
		// Contrast: running the service that carries a `-run` name is free
		// while nothing is called `tool-run-run`.
		name: "running the service whose own name ends in -run",
		body: "services:\n" +
			"  tool-run:\n    image: alpine:3.20\n",
		service: "tool-run",
	}, {
		// And refused when something is.
		name: "running the service whose own name ends in -run, with the next name taken",
		body: "services:\n" +
			"  tool-run:\n    image: alpine:3.20\n" +
			"  tool-run-run:\n    image: alpine:3.20\n",
		service: "tool-run",
		want:    oneOffNameTakenRefusal("tool-run"),
	}} {
		for _, path := range oneOffPaths {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				rt, log := fakeShim(t)
				p, err := loadProject(t, tc.body)
				if err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				o := orchestrator.New(p, rt, "opossum", &out)
				err = runOneOffPath(o, path, tc.service)
				if tc.want == "" {
					if err != nil || runLine(log()) < 0 {
						t.Errorf("want it run, got err %v and %v", err, log())
					}
					return
				}
				if err == nil || err.Error() != tc.want {
					t.Errorf("\n got %v\nwant %s", err, tc.want)
				}
				if indexOf(log(), "network create") >= 0 || runLine(log()) >= 0 {
					t.Errorf("want nothing created or started before the refusal, got %v", log())
				}
				// Nothing is said under that name either: the warnings about
				// names two containers could hold are not reached.
				if out.Len() > 0 {
					t.Errorf("want the refusal alone, got said:\n%s", out.String())
				}
			})
		}
	}
}

// `up` is not refused: it creates the service of that name as the file writes
// it, and no one-off is in the way.
func TestUpIsNotRefusedByTheOneOffName(t *testing.T) {
	rt, log := fakeShim(t)
	p, err := loadProject(t, "services:\n"+
		"  tool:\n    image: alpine:3.20\n"+
		"  tool-run:\n    image: alpine:3.20\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("up: %v", err)
	}
	if runLine(log()) < 0 {
		t.Errorf("want the services started, got %v", log())
	}
}

// The refusal names the service the file spells, both sides of it: the one
// being run and the one carrying the name. A reader edits the second.
func TestTheRefusalNamesBothServices(t *testing.T) {
	rt, _ := fakeShim(t)
	p, err := loadProject(t, "services:\n"+
		"  worker:\n    image: alpine:3.20\n"+
		"  worker-run:\n    image: alpine:3.20\n")
	if err != nil {
		t.Fatal(err)
	}
	err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).RunOneOff("worker", []string{"true"}, orchestrator.RunOneOffOptions{})
	want := oneOffNameTakenRefusal("worker")
	if err == nil || err.Error() != want {
		t.Errorf("\n got %v\nwant %s", err, want)
	}
	for _, name := range []string{`"worker"`, `"worker-run"`} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("want %s named in the refusal: %v", name, err)
		}
	}
}

// What the run refuses first: the file's own faults come before this, as they
// do for the rest of the one-off's pre-flight, and this comes before a
// container of another project carrying the name.
func TestTheNameTakenRefusalComesAfterTheFilesOwnFaults(t *testing.T) {
	rt, _ := fakeShim(t)
	// A cycle among the services this run reads, and the name taken.
	p, err := loadProject(t, "services:\n"+
		"  tool:\n    image: alpine:3.20\n    depends_on: [api]\n"+
		"  api:\n    image: alpine:3.20\n    depends_on: [tool]\n"+
		"  tool-run:\n    image: alpine:3.20\n")
	if err != nil {
		t.Fatal(err)
	}
	err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).RunOneOff("tool", []string{"true"}, orchestrator.RunOneOffOptions{})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("want the cycle refused first, got %v", err)
	}
	// A dependency behind a profile that is not active, which the file's own
	// reading refuses, comes first as well.
	rt, _ = fakeShim(t)
	pg, err := loadProject(t, "services:\n"+
		"  tool:\n    image: alpine:3.20\n    depends_on: [gated]\n"+
		"  gated:\n    image: alpine:3.20\n    profiles: [debug]\n"+
		"  tool-run:\n    image: alpine:3.20\n")
	if err != nil {
		t.Fatal(err)
	}
	err = orchestrator.New(pg, rt, "opossum", &bytes.Buffer{}).RunOneOff("tool", []string{"true"}, orchestrator.RunOneOffOptions{})
	if err == nil || !strings.Contains(err.Error(), "profile") {
		t.Errorf("want the inactive profile refused first, got %v", err)
	}
	// Without the cycle, the name is what refuses it.
	rt, _ = fakeShim(t)
	p2, err := loadProject(t, "services:\n"+
		"  tool:\n    image: alpine:3.20\n"+
		"  tool-run:\n    image: alpine:3.20\n")
	if err != nil {
		t.Fatal(err)
	}
	err = orchestrator.New(p2, rt, "opossum", &bytes.Buffer{}).RunOneOff("tool", []string{"true"}, orchestrator.RunOneOffOptions{})
	if err == nil || err.Error() != oneOffNameTakenRefusal("tool") {
		t.Errorf("want the name refused, got %v", err)
	}
}

// The check reads the file, not the runtime: a project whose service of that
// name was never started is refused all the same, and one whose service is up
// is refused without looking. Every path the one-off has, because each asks
// the runtime for that name further down and none of them may get there
// first.
func TestTheNameTakenRefusalDoesNotAskTheRuntime(t *testing.T) {
	for _, started := range []string{"the service of that name is up", "it was never started"} {
		for _, path := range oneOffPaths {
			t.Run(started+"/"+path, func(t *testing.T) {
				rt, log := fakeShim(t)
				if started != "the service of that name is up" {
					// The fake reads this as a list of container names, so it
					// has to carry the name this project gives that service.
					setShimEnv(rt, "INSPECT_ABSENT=tool-run.demo.opossum")
				}
				p, err := loadProject(t, "services:\n"+
					"  tool:\n    image: alpine:3.20\n"+
					"  tool-run:\n    image: alpine:3.20\n")
				if err != nil {
					t.Fatal(err)
				}
				o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
				if got := o.Project.Name; got != "demo" {
					t.Fatalf("the fake is told about %q, but the project is %q", "demo", got)
				}
				err = runOneOffPath(o, path, "tool")
				if err == nil || err.Error() != oneOffNameTakenRefusal("tool") {
					t.Errorf("\n got %v\nwant %s", err, oneOffNameTakenRefusal("tool"))
				}
				if indexOf(log(), "inspect") >= 0 {
					t.Errorf("want the file alone to decide, got %v", log())
				}
			})
		}
	}
}

// And before the workspace is snapshotted, which `run --audit` does on its way
// in: a refusal afterwards would leave the directory holding it behind.
func TestTheNameTakenRefusalComesBeforeTheSnapshot(t *testing.T) {
	dir := t.TempDir()
	rt, log := fakeShim(t)
	p, err := loadProject(t, "services:\n"+
		"  tool:\n    image: alpine:3.20\n    working_dir: /w\n    volumes: ["+dir+":/w]\n"+
		"  tool-run:\n    image: alpine:3.20\n")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).RunAudited("tool", []string{"true"}, orchestrator.RunOneOffOptions{})
	if err == nil || err.Error() != oneOffNameTakenRefusal("tool") {
		t.Errorf("\n got %v\nwant %s", err, oneOffNameTakenRefusal("tool"))
	}
	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("want the workspace untouched, it went from %d entries to %d", len(before), len(after))
	}
	if indexOf(log(), "inspect") >= 0 {
		t.Errorf("want the file alone to decide, got %v", log())
	}
}

// A one-off of a service the file does not define is still an unknown service:
// this check does not come before that.
func TestAnUnknownServiceIsStillUnknown(t *testing.T) {
	rt, _ := fakeShim(t)
	p, err := loadProject(t, "services:\n  tool-run:\n    image: alpine:3.20\n")
	if err != nil {
		t.Fatal(err)
	}
	err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).RunOneOff("tool", []string{"true"}, orchestrator.RunOneOffOptions{})
	if err == nil || !strings.Contains(err.Error(), "tool") || strings.Contains(err.Error(), "rename") {
		t.Errorf("want the unknown service named, got %v", err)
	}
}
