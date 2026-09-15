package orchestrator

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

// A kill the runtime refuses leaves no record of its own: the service is not
// recorded as stopped on purpose, so a later exit is still restarted — and a
// record an earlier `stop` wrote stays. A running container that refused the
// signal makes the exit non-zero, as docker compose v5.5.0 exits 1 for
// `kill -s BOGUS` (`invalid signal`); a container that is not running is not
// killed and is no error, as docker compose kills only running containers.
func TestARefusedKillLeavesNoRecordOfItsOwn(t *testing.T) {
	shim := func(t *testing.T, state string) *runtime.Runtime {
		dir := t.TempDir()
		path := filepath.Join(dir, "c.sh")
		body := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n"+
			"  inspect) echo '[{\"status\":{\"state\":\"%s\"},\"configuration\":{\"labels\":{\"opossum.project\":\"demo\"}}}]' ;;\n"+
			"  kill) echo 'Error: invalid signal: BOGUS' >&2; exit 1 ;;\n"+
			"  system) echo 'status running' ;;\nesac\nexit 0\n", state)
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return &runtime.Runtime{Bin: path}
	}
	proj := func() *compose.Project {
		return &compose.Project{Name: "demo", Services: map[string]*compose.Service{"web": {Name: "web", Image: "w"}}}
	}
	t.Run("running, refused: no record, exit non-zero", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		o := New(proj(), shim(t, "running"), "opossum", &bytes.Buffer{})
		err := o.Kill(nil, "BOGUS")
		if err == nil || !strings.Contains(err.Error(), `killing service "web"`) || !strings.Contains(err.Error(), "invalid signal: BOGUS") {
			t.Errorf("want the refusal named, got %v", err)
		}
		if o.wasStoppedByUs("web") {
			t.Error("a refused kill must not leave web recorded as stopped: its later exit would not be restarted")
		}
	})
	t.Run("running, refused, stopped before: the earlier record stays", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		o := New(proj(), shim(t, "running"), "opossum", &bytes.Buffer{})
		o.MarkStopped("web")
		_ = o.Kill(nil, "BOGUS")
		if !o.wasStoppedByUs("web") {
			t.Error("the record an earlier stop wrote was taken back by a refused kill")
		}
	})
	t.Run("not running: not killed, no record, no error", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		o := New(proj(), shim(t, "stopped"), "opossum", &bytes.Buffer{})
		if err := o.Kill(nil, ""); err != nil {
			t.Errorf("a container that is not running is no error, got %v", err)
		}
		if o.wasStoppedByUs("web") {
			t.Error("a kill that did not reach a running container must not record it as stopped")
		}
	})
}

// The supervisor's record of a stop is this project's word about its own
// service. `stop` writes it only for the containers it stops, and `restart`
// clears it only for the ones it brings back: a container left because it is
// another project's keeps whatever record the service had, in both directions.
func TestAStopRecordFollowsOnlyTheContainersActedOn(t *testing.T) {
	shim := func(t *testing.T) *runtime.Runtime {
		dir := t.TempDir()
		path := filepath.Join(dir, "c.sh")
		body := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n" +
			"  inspect) if [ \"$2\" = web.demo.opossum ]; then p=otherproj; else p=demo; fi\n" +
			"    echo \"[{\\\"status\\\":{\\\"state\\\":\\\"running\\\"},\\\"configuration\\\":{\\\"labels\\\":{\\\"opossum.project\\\":\\\"$p\\\"}}}]\" ;;\n" +
			"  system) echo 'status running' ;;\nesac\nexit 0\n")
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return &runtime.Runtime{Bin: path}
	}
	proj := func() *compose.Project {
		return &compose.Project{Name: "demo", Services: map[string]*compose.Service{
			"web": {Name: "web", Image: "w"},
			"db":  {Name: "db", Image: "d"},
		}}
	}
	t.Run("stop records only what it stopped", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		o := New(proj(), shim(t), "opossum", &bytes.Buffer{})
		if err := o.Stop(nil); err != nil {
			t.Fatalf("stop: %v", err)
		}
		if o.wasStoppedByUs("web") {
			t.Error("web's container is another project's and was not stopped, yet the service is recorded as stopped")
		}
		if !o.wasStoppedByUs("db") {
			t.Error("db was stopped and should be recorded as stopped")
		}
	})
	t.Run("kill records only what it killed", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		// Two of this project's services on either side of another project's,
		// so a record written for one position alone shows.
		p := proj()
		p.Services["cache"] = &compose.Service{Name: "cache", Image: "c"}
		p.Services["zeta"] = &compose.Service{Name: "zeta", Image: "z"}
		o := New(p, shim(t), "opossum", &bytes.Buffer{})
		if err := o.Kill(nil, ""); err != nil {
			t.Fatalf("kill: %v", err)
		}
		if o.wasStoppedByUs("web") {
			t.Error("web's container is another project's and was not killed, yet the service is recorded as stopped")
		}
		for _, name := range []string{"cache", "db", "zeta"} {
			if !o.wasStoppedByUs(name) {
				t.Errorf("%s was killed and should be recorded as stopped", name)
			}
		}
	})
	t.Run("restart clears only what it brought back", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		o := New(proj(), shim(t), "opossum", &bytes.Buffer{})
		o.MarkStopped("web")
		o.MarkStopped("db")
		if err := o.Restart(nil); err != nil {
			t.Fatalf("restart: %v", err)
		}
		if !o.wasStoppedByUs("web") {
			t.Error("web's container is another project's and was not restarted, yet its stop record was cleared")
		}
		if o.wasStoppedByUs("db") {
			t.Error("db was restarted and its stop record should be cleared")
		}
	})
	t.Run("start clears only what it started", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		o := New(proj(), shim(t), "opossum", &bytes.Buffer{})
		o.MarkStopped("web")
		o.MarkStopped("db")
		if err := o.Start(nil); err != nil {
			t.Fatalf("start: %v", err)
		}
		if !o.wasStoppedByUs("web") {
			t.Error("web's container is another project's and was not started, yet its stop record was cleared")
		}
		if o.wasStoppedByUs("db") {
			t.Error("db was started and its stop record should be cleared")
		}
	})
}
