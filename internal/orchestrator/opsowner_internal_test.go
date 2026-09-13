package orchestrator

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

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
