package runtime

// Evals for #276: a foreground (one-off) run captures its stderr into a bounded
// buffer so the same bootstrap VZError `up` decodes is decodable here too — but
// only when NOT on a TTY (a TTY run keeps its real terminal fds), and the buffer
// is capped so a long-running one-off can't balloon memory.

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestCappedBuffer(t *testing.T) {
	c := &cappedBuffer{cap: 4}
	if n, _ := c.Write([]byte("ab")); n != 2 {
		t.Errorf("write should report 2, got %d", n)
	}
	// Overshoots the cap: only the head fits, but the write still reports full length
	// (so the child's pipe never blocks on a short write).
	if n, _ := c.Write([]byte("cdef")); n != 4 {
		t.Errorf("write should report the full 4 even when capped, got %d", n)
	}
	if got := c.String(); got != "abcd" {
		t.Errorf("capped buffer should hold only the first 4 bytes, got %q", got)
	}
}

func TestForegroundRunCapturesStderrForDecode(t *testing.T) {
	// A non-TTY foreground run that fails to bootstrap must surface a *RunError with
	// the stderr, so the orchestrator can decode the VZError.
	shim := filepath.Join(t.TempDir(), "c")
	writeShimFile(t, shim, "#!/bin/sh\necho 'Error Domain=VZErrorDomain Code=2 \"The storage device attachment is invalid.\"' >&2\nexit 1\n")
	r := &Runtime{Bin: shim}
	err := r.Run(RunOptions{Image: "x", Detach: false, TTY: false})
	var re *RunError
	if !errors.As(err, &re) {
		t.Fatalf("a non-TTY foreground failure should be a *RunError, got %T: %v", err, err)
	}
	if !strings.Contains(re.Stderr, "storage device attachment is invalid") {
		t.Errorf("RunError should carry the captured stderr, got %q", re.Stderr)
	}
}

func TestTTYForegroundRunDoesNotCapture(t *testing.T) {
	// A run with the caller's terminal attached keeps its real terminal fds
	// untouched — no capture, so a plain error. A -t of the service's own
	// (`tty: true` under `up`) has no terminal behind it and is captured like
	// any foreground run, so its failure can be read.
	for _, tc := range []struct {
		name     string
		opts     RunOptions
		captured bool
	}{
		{"the caller's terminal attached", RunOptions{Image: "x", Detach: false, TTY: true, Attached: true}, false},
		{"-t without a terminal behind it", RunOptions{Image: "x", Detach: false, TTY: true}, true},
		{"no -t", RunOptions{Image: "x", Detach: false}, true},
		// Attached without -t is a pair no caller makes; read as the name
		// says, it is nothing, and the run is captured as any other.
		{"attached but no -t", RunOptions{Image: "x", Detach: false, Attached: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shim := filepath.Join(t.TempDir(), "c")
			writeShimFile(t, shim, "#!/bin/sh\necho refused >&2\nexit 1\n")
			r := &Runtime{Bin: shim}
			err := r.Run(tc.opts)
			if err == nil {
				t.Fatal("expected the run to fail")
			}
			var re *RunError
			if got := errors.As(err, &re); got != tc.captured {
				t.Fatalf("wrapped in a *RunError: %v, want %v (%v)", got, tc.captured, err)
			}
			if tc.captured && !strings.Contains(re.Stderr, "refused") {
				t.Errorf("the captured stderr should carry what the runtime said, got %q", re.Stderr)
			}
		})
	}
}
