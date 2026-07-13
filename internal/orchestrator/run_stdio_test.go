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
