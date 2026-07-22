package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// A stopped runtime must not stall a mutating command: opossum starts it (once)
// and proceeds. The fake shim reports the system stopped until `system start` runs,
// then running — so this exercises the whole auto-start→proceed flow.
func TestUpAutoStartsStoppedRuntime(t *testing.T) {
	readLog := fakeShim(t)
	t.Setenv("SYSTEM_STOPPED", "1")
	t.Setenv("SYSTEM_START_FLAG", filepath.Join(t.TempDir(), "started"))
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: nginx\n")

	if _, err := run(t, "-f", compose, "up", "web"); err != nil {
		t.Fatalf("up should auto-start the runtime and proceed, got %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "system start") {
		t.Errorf("up on a stopped runtime must invoke `system start`, got:\n%s", joined)
	}
}

// OPOSSUM_NO_AUTO_START opts out: a mutating command on a stopped runtime errors
// (OPSM-405, with the why) instead of starting anything.
func TestNoAutoStartOptOut(t *testing.T) {
	readLog := fakeShim(t)
	t.Setenv("SYSTEM_STOPPED", "1")
	t.Setenv("SYSTEM_START_FLAG", filepath.Join(t.TempDir(), "started"))
	t.Setenv("OPOSSUM_NO_AUTO_START", "1")
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: nginx\n")

	_, err := run(t, "-f", compose, "up", "web")
	if err == nil {
		t.Fatal("with OPOSSUM_NO_AUTO_START, up on a stopped runtime must error, got nil")
	}
	for _, want := range []string{"OPSM-405", "container system start", "OPOSSUM_NO_AUTO_START"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("opt-out error %q missing %q", err.Error(), want)
		}
	}
	if strings.Contains(strings.Join(readLog(), "\n"), "system start") {
		t.Error("with OPOSSUM_NO_AUTO_START set, opossum must NOT invoke `system start`")
	}
}

// Read-only commands (ps/images) never auto-start — a read must not have a side
// effect. On a stopped runtime `ps` reports OPSM-405 and starts nothing.
func TestReadOnlyDoesNotAutoStart(t *testing.T) {
	readLog := fakeShim(t)
	t.Setenv("SYSTEM_STOPPED", "1")
	t.Setenv("SYSTEM_START_FLAG", filepath.Join(t.TempDir(), "started"))
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: nginx\n")

	_, err := run(t, "-f", compose, "ps")
	if err == nil || !strings.Contains(err.Error(), "OPSM-405") {
		t.Fatalf("ps on a stopped runtime should report OPSM-405, got %v", err)
	}
	if strings.Contains(strings.Join(readLog(), "\n"), "system start") {
		t.Error("a read-only command must not auto-start the runtime")
	}
}

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
