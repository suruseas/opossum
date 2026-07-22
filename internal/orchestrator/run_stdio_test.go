package orchestrator_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// RunOneOff keeps the real stdout for the one-off body only: build (and dep)
// output is routed to stderr, so `opossum run` works as an MCP stdio bridge.
func TestRunOneOffStdoutIsBodyOnly(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "shim")
	// Echo a distinct marker to stdout for build vs the foreground body run.
	body := "#!/bin/sh\ncase \"$1\" in\n  build) echo BUILD-OUT ;;\n  run) echo BODY-OUT ;;\nesac\nexit 0\n"
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := &runtime.Runtime{Bin: shim}
	p := project("pj", map[string]*compose.Service{"app": {Build: &compose.Build{Context: "."}}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})

	// Capture the real stdout across the run.
	old := os.Stdout
	rp, wp, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = wp
	runErr := o.RunOneOff("app", nil, orchestrator.RunOneOffOptions{NoDeps: true})
	wp.Close()
	os.Stdout = old
	if runErr != nil {
		t.Fatalf("RunOneOff: %v", runErr)
	}
	var buf bytes.Buffer
	io.Copy(&buf, rp)
	got := buf.String()
	if !strings.Contains(got, "BODY-OUT") {
		t.Errorf("the one-off body's stdout should reach real stdout; got %q", got)
	}
	if strings.Contains(got, "BUILD-OUT") {
		t.Errorf("build output should go to stderr, not the one-off's stdout; got %q", got)
	}
}

// `opossum run` also surfaces the ignored-fields note for its target service, so a
// dropped field on a one-off isn't silently swallowed (#274).
func TestRunOneOffNotesIgnoredFields(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "shim")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := project("pj", map[string]*compose.Service{
		"app": {Image: "app:latest", Unsupported: []string{"dns_search"}},
	})
	var out bytes.Buffer
	o := orchestrator.New(p, &runtime.Runtime{Bin: shim}, "opossum", &out)
	if err := o.RunOneOff("app", nil, orchestrator.RunOneOffOptions{NoDeps: true}); err != nil {
		t.Fatalf("RunOneOff: %v", err)
	}
	if s := out.String(); !strings.Contains(s, "note:") || !strings.Contains(s, "app: dns_search") {
		t.Errorf("run should note the target's ignored field, got:\n%s", s)
	}
}

// With dependencies, `run` starts them via Up, which reports the project-wide
// top-level ignored fields — so the one-off must not report them a second time
// (no double count in a single command).
func TestRunOneOffDoesNotDoubleCountTopLevel(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "shim")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := project("pj", map[string]*compose.Service{
		"db":  {Image: "db:latest"},
		"web": {Image: "web:latest", Unsupported: []string{"dns_search"}, DependsOn: compose.DependsOn{{Name: "db"}}},
	})
	p.Unsupported = []string{"volumes"} // a project-wide ignored field
	var out bytes.Buffer
	o := orchestrator.New(p, &runtime.Runtime{Bin: shim}, "opossum", &out)
	if err := o.RunOneOff("web", nil, orchestrator.RunOneOffOptions{}); err != nil {
		t.Fatalf("RunOneOff: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "web: dns_search") {
		t.Errorf("target's ignored field should be noted, got:\n%s", s)
	}
	if n := strings.Count(s, "top-level"); n != 1 {
		t.Errorf("top-level ignored field should be reported exactly once, got %d:\n%s", n, s)
	}
}
