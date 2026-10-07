package orchestrator_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// `up a` starts what `a` links to first, and brings it along, as docker compose does (`links:` and `network_mode: service:` are dependencies, #1802).
func TestUpStartsWhatAServiceLinksToFirst(t *testing.T) {
	for _, tc := range []struct{ name, extra string }{
		{"links", "    links: [\"b:alias\"]\n"},
		{"network_mode service", "    network_mode: service:b\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, _ := fakeShim(t)
			path := filepath.Join(t.TempDir(), "c.yaml")
			body := "name: lk\nservices:\n  a:\n    image: x\n" + tc.extra + "  b:\n    image: y\n"
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			project, err := compose.LoadFiles([]string{path}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := orchestrator.New(project, rt, "opossum", &out).Up(true, "a"); err != nil {
				t.Fatalf("Up: %v\n%s", err, out.String())
			}
			ib, ia := strings.Index(out.String(), "Starting b"), strings.Index(out.String(), "Starting a")
			if ib < 0 || ia < 0 || ib > ia {
				t.Errorf("want b started, and before a, got:\n%s", out.String())
			}
		})
	}
}

// A host port of 0 (docker compose: the engine picks a free one) is made: the runtime refuses it (`invalid publish host port range: 0`, the fake does as it
// does), so it is the container port, as a host port left out is (#1820).
func TestAServicePublishingHostPortZeroIsStarted(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, read := fakeShim(t)
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte("name: hp0\nservices:\n  a:\n    image: x\n    ports: [\"0:47121\", \"127.0.0.1:0:47122/udp\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project, err := compose.LoadFiles([]string{path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := orchestrator.New(project, rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("Up: %v\n%s", err, out.String())
	}
	var run string
	for _, l := range read() {
		if strings.HasPrefix(l, "run ") {
			run = l
		}
	}
	for _, want := range []string{"-p 47121:47121", "-p 127.0.0.1:47122:47122/udp"} {
		if !strings.Contains(run, want) {
			t.Errorf("the container was run with %q, want %q in it", run, want)
		}
	}
}
