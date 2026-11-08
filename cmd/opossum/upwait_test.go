package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// The flags `up --wait` brings, read from the command line: what is refused before
// anything starts, what is only said, and that what is given reaches the wait.
func TestUpWaitFlags(t *testing.T) {
	const healthchecked = "name: demo\nservices:\n  web:\n    image: web:latest\n    healthcheck:\n      test: [\"CMD\", \"probe\"]\n      interval: 100ms\n      retries: 1000\n"
	probes := func(lines []string) int {
		n := 0
		for _, l := range lines {
			if strings.HasPrefix(l, "exec ") {
				n++
			}
		}
		return n
	}
	ran := func(lines []string) bool {
		for _, l := range lines {
			if strings.HasPrefix(l, "run ") {
				return true
			}
		}
		return false
	}
	start := func(t *testing.T, okAt string) func() []string {
		readLog := fakeShim(t)
		t.Setenv("HEALTH_COUNTER", filepath.Join(t.TempDir(), "health.count"))
		t.Setenv("HEALTH_OK_AT", okAt)
		return readLog
	}

	for _, tc := range []struct {
		name string
		args []string
		want string // part of the refusal
	}{
		{"--wait with --foreground", []string{"--foreground", "--wait"}, "--wait waits for a detached up; it cannot be used with --foreground"},
		{"-d with --foreground", []string{"--foreground", "-d"}, "--foreground and --detach are mutually exclusive"},
		{"a negative --wait-timeout", []string{"--wait", "--wait-timeout", "-1"}, "--wait-timeout must be 0 (no limit) or a number of seconds, not -1"},
	} {
		t.Run("refused: "+tc.name, func(t *testing.T) {
			readLog := start(t, "1")
			compose := writeCompose(t, healthchecked)
			_, err := run(t, append([]string{"-f", compose, "up", "--no-supervisor"}, tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a refusal saying %q, got %v", tc.want, err)
			}
			if ran(readLog()) {
				t.Errorf("something was started before the refusal: %v", readLog())
			}
		})
	}

	t.Run("-d --wait waits", func(t *testing.T) {
		readLog := start(t, "3")
		compose := writeCompose(t, healthchecked)
		if _, err := run(t, "-f", compose, "up", "--no-supervisor", "-d", "--wait"); err != nil {
			t.Fatalf("up -d --wait: %v", err)
		}
		if n := probes(readLog()); n != 3 {
			t.Errorf("want 3 probes, got %d", n)
		}
	})

	t.Run("--wait-timeout is in seconds", func(t *testing.T) {
		// Three probes 100ms apart: well inside a second, far outside a nanosecond.
		readLog := start(t, "3")
		compose := writeCompose(t, healthchecked)
		_, stderr, err := runSplit(t, "-f", compose, "up", "--no-supervisor", "--wait", "--wait-timeout", "5")
		if err != nil {
			t.Fatalf("up --wait --wait-timeout 5: %v", err)
		}
		if strings.Contains(stderr, "does nothing without --wait") {
			t.Errorf("the note was said with --wait given:\n%s", stderr)
		}
		if n := probes(readLog()); n != 3 {
			t.Errorf("want 3 probes, got %d", n)
		}
	})

	t.Run("--wait-timeout reaches the wait", func(t *testing.T) {
		start(t, "100000")
		compose := writeCompose(t, healthchecked)
		_, err := run(t, "-f", compose, "up", "--no-supervisor", "--wait", "--wait-timeout", "1")
		if err == nil || !strings.Contains(err.Error(), "within the --wait-timeout of 1s") {
			t.Fatalf("want the timeout, got %v", err)
		}
	})

	t.Run("--wait-timeout without --wait is said and does nothing", func(t *testing.T) {
		readLog := start(t, "100000")
		compose := writeCompose(t, healthchecked)
		_, stderr, err := runSplit(t, "-f", compose, "up", "--no-supervisor", "--wait-timeout", "1")
		if err != nil {
			t.Fatalf("up --wait-timeout 1: %v", err)
		}
		if n := strings.Count(stderr, "opossum: --wait-timeout does nothing without --wait"); n != 1 {
			t.Errorf("said %d times on stderr, want once:\n%s", n, stderr)
		}
		if n := probes(readLog()); n != 0 {
			t.Errorf("probed %d times without --wait", n)
		}
	})

	t.Run("-d alone starts and probes nothing", func(t *testing.T) {
		readLog := start(t, "100000")
		compose := writeCompose(t, healthchecked)
		if _, err := run(t, "-f", compose, "up", "--no-supervisor", "-d"); err != nil {
			t.Fatalf("up -d: %v", err)
		}
		if !ran(readLog()) {
			t.Errorf("up -d started nothing: %v", readLog())
		}
		if n := probes(readLog()); n != 0 {
			t.Errorf("up -d probed %d times", n)
		}
	})

	t.Run("without --wait nothing is probed", func(t *testing.T) {
		readLog := start(t, "100000")
		compose := writeCompose(t, healthchecked)
		_, stderr, err := runSplit(t, "-f", compose, "up", "--no-supervisor")
		if err != nil {
			t.Fatalf("up: %v", err)
		}
		if strings.Contains(stderr, "--wait-timeout") {
			t.Errorf("the note was said with no --wait-timeout given:\n%s", stderr)
		}
		if n := probes(readLog()); n != 0 {
			t.Errorf("probed %d times without --wait", n)
		}
	})
}
