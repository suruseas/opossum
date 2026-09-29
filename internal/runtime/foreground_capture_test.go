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
	c := &cappedBuffer{headCap: 4, tailCap: 4}
	if n, _ := c.Write([]byte("ab")); n != 2 {
		t.Errorf("write should report 2, got %d", n)
	}
	// Still within head+tail (2 of 8): nothing dropped yet, so this reads as
	// plain, uncapped content.
	if got := c.String(); got != "ab" {
		t.Errorf("under head+tail, capped buffer should read as written, got %q", got)
	}
	// Overshoots headCap but not headCap+tailCap: the head is full (4), and
	// the rest ("ef", 2 bytes) fits entirely in the tail — nothing dropped,
	// so the two together still reconstruct everything written.
	if n, _ := c.Write([]byte("cdef")); n != 4 {
		t.Errorf("write should report the full 4 even when capped, got %d", n)
	}
	if got := c.String(); got != "abcdef" {
		t.Errorf("head+tail together should cover everything written when nothing overflows tailCap, got %q", got)
	}
	// Exactly headCap+tailCap (8 of 8): still nothing dropped — the boundary
	// itself (over == 0, not over > 0) must not be misread as an overflow, or
	// a failure line landing right on it would gain a spurious "\n...\n".
	if n, _ := c.Write([]byte("gh")); n != 2 {
		t.Errorf("write should report the full 2, got %d", n)
	}
	if got := c.String(); got != "abcdefgh" {
		t.Errorf("exactly headCap+tailCap should still read as written, nothing dropped, got %q", got)
	}
	// Now overshoots headCap+tailCap (12 written, cap 8): the middle ("efgh")
	// is what the tail's window slides past and drops, keeping only the head
	// and the most recent tailCap bytes.
	if n, _ := c.Write([]byte("ijkl")); n != 4 {
		t.Errorf("write should report the full 4 even when capped, got %d", n)
	}
	if got := c.String(); got != "abcd\n...\nijkl" {
		t.Errorf("capped buffer should hold the first 4 and the last 4 bytes once it overflows both, got %q", got)
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
