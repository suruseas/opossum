package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// A container-CLI-absent environment must fail every runtime-touching command the
// same way: the coded, actionable OPSM-404 error and a non-zero exit — never a
// misleading empty `ps` table, a confident `PRESENT=no`, or a bare exec error
// reported as success. These per-command assertions also double as the mutation
// guard for the root preflight: drop the PersistentPreRunE and `ps`/`images` go
// back to exit 0 with empty/"no" output, failing here.
func TestRuntimeAbsentPreflight(t *testing.T) {
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: nginx\n")

	// Representative runtime-touching commands from the #252 report plus a few
	// neighbours. Each is invoked with args that pass cobra's own arg validation,
	// so the only thing that can fail is the preflight.
	gated := [][]string{
		{"up"},
		{"up", "--dry-run"},
		{"ps"},
		{"images"},
		{"logs", "web"},
		{"stats", "--no-stream"},
		{"down"},
	}
	for _, args := range gated {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			// A path that doesn't exist ⇒ exec.LookPath fails ⇒ Runtime.Available()==false.
			t.Setenv("OPOSSUM_CONTAINER_BIN", filepath.Join(t.TempDir(), "no-such-container"))
			_, err := run(t, append([]string{"-f", compose}, args...)...)
			if err == nil {
				t.Fatalf("%v: want a non-nil error (non-zero exit) when the container CLI is absent, got nil", args)
			}
			msg := err.Error()
			// The unified, actionable signal: a stable code an agent can map, plus the
			// concrete install steps a first-time human needs.
			for _, want := range []string{"OPSM-404", "not found on PATH", "brew install container", "container system start"} {
				if !strings.Contains(msg, want) {
					t.Errorf("%v: error %q missing %q", args, msg, want)
				}
			}
		})
	}
}

// config only parses/interpolates/merges compose — it never touches the runtime —
// so it must keep working with no container CLI installed. This pins the preflight
// exemption: over-broaden the preflight to gate config and this fails.
func TestConfigUnaffectedByRuntimeAbsence(t *testing.T) {
	t.Setenv("OPOSSUM_CONTAINER_BIN", filepath.Join(t.TempDir(), "no-such-container"))
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: nginx\n")
	out, err := run(t, "-f", compose, "config")
	if err != nil {
		t.Fatalf("config must work without the container CLI (it only parses compose), got %v", err)
	}
	if !strings.Contains(out, "web") || !strings.Contains(out, "nginx") {
		t.Errorf("config output missing the rendered service; got:\n%s", out)
	}
}
