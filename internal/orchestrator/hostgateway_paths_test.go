package orchestrator_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// The note is printed by the commands that start services, each of which reaches
// it with its own arguments: `up`, and `run` with and without dependencies.
const hostGatewayLine = "extra_hosts maps "

func hostGatewayShim(t *testing.T) *runtime.Runtime {
	t.Helper()
	shim := filepath.Join(t.TempDir(), "shim")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\n[ \"$1\" = inspect ] && { echo \"Error: container not found: $2\" >&2; exit 1; }\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &runtime.Runtime{Bin: shim}
}

func TestUpPrintsTheHostGatewayNoteOnce(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest", HostGatewayNames: []string{"gw"}}})
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if n := strings.Count(out.String(), hostGatewayLine+"gw "); n != 1 {
		t.Errorf("want the note once, got %d:\n%s", n, out.String())
	}
}

func TestRunPrintsTheHostGatewayNoteOnce(t *testing.T) {
	services := func() map[string]*compose.Service {
		return map[string]*compose.Service{
			"db":  {Image: "db:latest", HostGatewayNames: []string{"dbgw"}},
			"web": {Image: "web:latest", HostGatewayNames: []string{"webgw"}, DependsOn: compose.DependsOn{{Name: "db"}}},
		}
	}
	t.Run("no dependencies", func(t *testing.T) {
		p := project("pj", map[string]*compose.Service{"web": {Image: "web:latest", HostGatewayNames: []string{"webgw"}}})
		var out bytes.Buffer
		if err := orchestrator.New(p, hostGatewayShim(t), "opossum", &out).RunOneOff("web", nil, orchestrator.RunOneOffOptions{}); err != nil {
			t.Fatalf("RunOneOff: %v", err)
		}
		if n := strings.Count(out.String(), hostGatewayLine+"webgw "); n != 1 {
			t.Errorf("want the note once, got %d:\n%s", n, out.String())
		}
	})
	t.Run("with dependencies, for the target and for the dependency", func(t *testing.T) {
		var out bytes.Buffer
		if err := orchestrator.New(project("pj", services()), hostGatewayShim(t), "opossum", &out).RunOneOff("web", nil, orchestrator.RunOneOffOptions{}); err != nil {
			t.Fatalf("RunOneOff: %v", err)
		}
		for _, name := range []string{"webgw", "dbgw"} {
			if n := strings.Count(out.String(), hostGatewayLine+name+" "); n != 1 {
				t.Errorf("want the note for %s once, got %d:\n%s", name, n, out.String())
			}
		}
	})
	t.Run("no-deps leaves the dependency out", func(t *testing.T) {
		var out bytes.Buffer
		if err := orchestrator.New(project("pj", services()), hostGatewayShim(t), "opossum", &out).RunOneOff("web", nil, orchestrator.RunOneOffOptions{NoDeps: true}); err != nil {
			t.Fatalf("RunOneOff: %v", err)
		}
		if n := strings.Count(out.String(), hostGatewayLine+"webgw "); n != 1 {
			t.Errorf("want the target's note once, got %d:\n%s", n, out.String())
		}
		if strings.Contains(out.String(), "dbgw") {
			t.Errorf("--no-deps does not start the dependency, but it was named:\n%s", out.String())
		}
	})
}
