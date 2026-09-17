package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// The half of the generic start-failure guidance that holds whichever way it is
// worded: where to read depends on whether a container is left (#1103), what to
// check in the file does not. These rows are about which failures get the
// generic guidance at all, so they mark it by the half that does not move.
const startFailureGuidance = "verify the image, command, and mounts in the compose file"

// `up` frees a service's name and then starts it. When the runtime still says
// the name is taken, something the project lock could not see made a container
// in between — and that container is not this up's to roll back (#912; the
// shape #909 measured, with the lock unable to help). Docker compose, finding
// the service already there, reports it Running when it is the right one.
//
// The refusal cases go through `--force-recreate`, so the start is reached
// even though a container of that name exists: the up-to-date skip before the
// start would otherwise hide the situation entirely (without it the shim log
// shows no delete and no run at all). The two control cases at the end start
// from nothing and need no such step.
func TestUpLeavesAContainerItDidNotCreateAlone(t *testing.T) {
	const web = "web.demo.opossum"
	const nameTaken = "something else now holds the container name"
	one := func() *compose.Project {
		return project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
	}
	// firstUp brings the project up once so the shim holds each service's real
	// config hash, then arms the refusal for every later start of `taken`. The
	// log it returns starts after that first up, so a count of deletes is a
	// count of the second up's deletes.
	firstUp := func(t *testing.T, p *compose.Project, taken string) (*runtime.Runtime, func() []string) {
		t.Helper()
		rt, log := fakeShim(t)
		if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Fatalf("first up: %v", err)
		}
		setShimEnv(rt, "RUN_EXISTS="+taken)
		before := len(log())
		return rt, func() []string { return log()[before:] }
	}
	secondUp := func(t *testing.T, p *compose.Project, rt *runtime.Runtime) (string, error) {
		t.Helper()
		var out bytes.Buffer
		o := orchestrator.New(p, rt, "opossum", &out)
		o.SetUpOptions(true, false, false, false, false) // --force-recreate
		err := o.Up(true)
		return out.String(), err
	}
	// leftAlone checks the second up freed the name once and never touched the
	// holder again: no stop, and no second delete (which would be the rollback).
	leftAlone := func(t *testing.T, lines []string, cname string) {
		t.Helper()
		if n := countLines(lines, "delete --force "+cname); n != 1 {
			t.Errorf("the container that appeared is not this up's to remove; want 1 delete (freeing the name), got %d in %v", n, lines)
		}
		if indexOf(lines, "stop "+cname) >= 0 {
			t.Errorf("the container that appeared must not be stopped, got %v", lines)
		}
	}
	refused := func(t *testing.T, err error, out string) {
		t.Helper()
		if err == nil {
			t.Fatal("a container that is not the one the file describes must not be reported as up")
		}
		if !strings.Contains(err.Error(), nameTaken) || !strings.Contains(err.Error(), "container ls -a") {
			t.Errorf("the refusal should say the name is held and where to look, got: %v", err)
		}
		if strings.Contains(err.Error(), startFailureGuidance) {
			t.Errorf("this is not a generic start failure, got: %v", err)
		}
		if strings.Contains(out, "up to date") {
			t.Errorf("a refused service must not be reported up to date, got:\n%s", out)
		}
	}

	t.Run("running with this compose file's config: reported up to date, not rolled back", func(t *testing.T) {
		rt, log := firstUp(t, one(), web)
		out, err := secondUp(t, one(), rt)
		if err != nil {
			t.Fatalf("the container is the one the file describes; up should accept it, got: %v", err)
		}
		if !strings.Contains(out, "web is up to date") {
			t.Errorf("expected the docker-style 'up to date' report, got:\n%s", out)
		}
		leftAlone(t, log(), web)
	})

	// The runtime's other spelling of the same refusal — the one a real race
	// produces, when the holder is still being created — is the same situation.
	t.Run("the mid-creation spelling is the same refusal", func(t *testing.T) {
		rt, log := firstUp(t, one(), web)
		setShimEnv(rt, "RUN_EXISTS_WORDING=race")
		out, err := secondUp(t, one(), rt)
		if err != nil {
			t.Fatalf("the racing spelling is the same refusal; up should accept the container, got: %v", err)
		}
		if !strings.Contains(out, "web is up to date") {
			t.Errorf("expected 'up to date', got:\n%s", out)
		}
		leftAlone(t, log(), web)
	})

	// The holder was made with another configuration. Its hash is written by
	// the shim at the moment of the refusal — so an inspect taken before the
	// name was freed (this up's own container, same hash) would still say the
	// wrong thing; only an inspect of what is there now can decide.
	t.Run("a different config: refused, and still not rolled back", func(t *testing.T) {
		rt, log := firstUp(t, one(), web)
		setShimEnv(rt, "RUN_EXISTS_HASH=someone-elses-config")
		out, err := secondUp(t, one(), rt)
		refused(t, err, out)
		leftAlone(t, log(), web)
	})

	// A container made by hand (`container run --name …`) carries no opossum
	// labels at all: no hash is not the same hash.
	t.Run("no config label at all: refused", func(t *testing.T) {
		rt, log := firstUp(t, one(), web)
		setShimEnv(rt, "RUN_EXISTS_HASH=none")
		out, err := secondUp(t, one(), rt)
		refused(t, err, out)
		leftAlone(t, log(), web)
	})

	t.Run("right config but not running: refused, not reported up to date", func(t *testing.T) {
		rt, log := firstUp(t, one(), web)
		setShimEnv(rt, "INSPECT_STOPPED="+web)
		out, err := secondUp(t, one(), rt)
		refused(t, err, out)
		leftAlone(t, log(), web)
	})

	// Two services, the second refused: the rollback still removes what this up
	// did create (db) and only that — a rollback list one entry out of step
	// would stop the holder and leave db standing, the exact harm this guards
	// against — and the report does not claim the holder as this up's.
	t.Run("the second of two services refused: only the first is rolled back", func(t *testing.T) {
		two := func() *compose.Project {
			return project("demo", map[string]*compose.Service{
				"db":  {Image: "db:latest"},
				"web": {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "db"}}},
			})
		}
		rt, log := firstUp(t, two(), web)
		setShimEnv(rt, "RUN_EXISTS_HASH=someone-elses-config")
		out, err := secondUp(t, two(), rt)
		refused(t, err, out)
		lines := log()
		leftAlone(t, lines, web)
		if indexOf(lines, "stop db.demo.opossum") < 0 || countLines(lines, "delete --force db.demo.opossum") != 2 {
			t.Errorf("db was this up's, and is rolled back; got %v", lines)
		}
		// Whatever wording the rollback report uses, web — a container this up did
		// not create — is not in it: neither as rolled back nor as left behind.
		if report := rollbackLine(out); !strings.HasPrefix(report, "Rolled back db") || strings.Contains(report, "web") {
			t.Errorf("the rollback report covers db and does not claim web, got:\n%s", out)
		}
	})

	// A run-to-completion dependency runs attached, in its own branch of the
	// start loop, and the same refusal there is the same situation — with no
	// "up to date" to offer, since only running it tells whether it completed.
	t.Run("a run-to-completion dependency whose name is taken: refused, not rolled back, not 'exited non-zero'", func(t *testing.T) {
		const init = "init.demo.opossum"
		withInit := func() *compose.Project {
			return project("demo", map[string]*compose.Service{
				"init": {Image: "init:latest"},
				"web": {Image: "web:latest",
					DependsOn: compose.DependsOn{{Name: "init", Condition: compose.ConditionCompleted}}},
			})
		}
		rt, log := firstUp(t, withInit(), init)
		out, err := secondUp(t, withInit(), rt)
		refused(t, err, out)
		if strings.Contains(err.Error(), "did not complete successfully") || !strings.Contains(err.Error(), "run-to-completion") {
			t.Errorf("it never ran, so it did not 'exit non-zero'; got: %v", err)
		}
		leftAlone(t, log(), init)
	})

	// The same, after a service this up did create: only that one is rolled
	// back, and the report does not claim the holder — a single-service fixture
	// cannot tell "drop the last rollback entry" from "drop them all".
	t.Run("a run-to-completion dependency refused after a service this up created: only that service is rolled back", func(t *testing.T) {
		const db, init = "db.demo.opossum", "init.demo.opossum"
		three := func() *compose.Project {
			return project("demo", map[string]*compose.Service{
				"db":   {Image: "db:latest"},
				"init": {Image: "init:latest", DependsOn: compose.DependsOn{{Name: "db"}}},
				"web": {Image: "web:latest",
					DependsOn: compose.DependsOn{{Name: "init", Condition: compose.ConditionCompleted}}},
			})
		}
		rt, log := firstUp(t, three(), init)
		out, err := secondUp(t, three(), rt)
		refused(t, err, out)
		lines := log()
		leftAlone(t, lines, init)
		if indexOf(lines, "stop "+db) < 0 || countLines(lines, "delete --force "+db) != 2 {
			t.Errorf("db was this up's, and is rolled back; got %v", lines)
		}
		if report := rollbackLine(out); !strings.HasPrefix(report, "Rolled back db") || strings.Contains(report, "init") {
			t.Errorf("the rollback report covers db and does not claim init, got:\n%s", out)
		}
	})

	// Control: a start that fails for any other reason is still this up's own
	// failure, with its own wording, and its container is rolled back — the
	// guard above must not widen into "never roll back a failed start".
	t.Run("a start that fails for another reason is still rolled back", func(t *testing.T) {
		rt, log := fakeShim(t)
		setShimEnv(rt, "RUN_FAIL="+web)
		err := orchestrator.New(one(), rt, "opossum", &bytes.Buffer{}).Up(true)
		if err == nil || !strings.Contains(err.Error(), `starting service "web"`) || !strings.Contains(err.Error(), startFailureGuidance) {
			t.Errorf("an ordinary start failure keeps its wording, got: %v", err)
		}
		// The pre-start delete, then the rollback's.
		if n := countLines(log(), "delete --force "+web); n != 2 {
			t.Errorf("an ordinary failed start is rolled back; want 2 deletes, got %d in %v", n, log())
		}
	})

	// The refusal about some other container is not about this name: the
	// runtime's sentence names the container, and so does the check — so web's
	// start refused with db's sentence is an ordinary failure, rolled back.
	t.Run("a refusal naming another container is an ordinary failure", func(t *testing.T) {
		rt, log := fakeShim(t)
		setShimEnv(rt, "RUN_EXISTS_ANY=db.demo.opossum")
		err := orchestrator.New(one(), rt, "opossum", &bytes.Buffer{}).Up(true)
		if err == nil || !strings.Contains(err.Error(), startFailureGuidance) {
			t.Errorf("a refusal about another name is an ordinary start failure for this one, got: %v", err)
		}
		if err != nil && strings.Contains(err.Error(), nameTaken) {
			t.Errorf("db's name being taken says nothing about web, got: %v", err)
		}
		if n := countLines(log(), "delete --force "+web); n != 2 {
			t.Errorf("an ordinary failed start is rolled back; want 2 deletes, got %d in %v", n, log())
		}
	})
}

// rollbackLine is the one line of `up`'s output that reports the rollback, or
// "" when there is none — so a check on what it names is not satisfied by the
// same words elsewhere, nor escaped by a change of wording.
func rollbackLine(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "Rolled back ") || strings.HasPrefix(l, "Tried to roll back ") {
			return l
		}
	}
	return ""
}
