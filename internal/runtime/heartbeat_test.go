package runtime

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// When a command goes silent, a spinner frame is drawn; when output resumes it
// is cleared first, so the command's bytes and the spinner never interleave.
func TestHeartbeatDrawsThenClearsOnOutput(t *testing.T) {
	var spinner bytes.Buffer
	h := &heartbeat{out: &spinner, idle: 0, label: "building", last: time.Now().Add(-time.Second)}

	h.tick() // idle >= 0, so it draws
	drawn := spinner.String()
	if !strings.Contains(drawn, "building") {
		t.Fatalf("spinner should name the activity, got %q", drawn)
	}
	if !strings.ContainsAny(drawn, string(spinnerFrames)) {
		t.Fatalf("spinner should show a frame, got %q", drawn)
	}

	// Command output arrives: it must clear the spinner (write an erase sequence)
	// and forward the bytes to the real sink.
	var stdout bytes.Buffer
	w := h.wrap(&stdout)
	spinner.Reset()
	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "hello\n" {
		t.Errorf("output not forwarded verbatim: %q", stdout.String())
	}
	if !strings.Contains(spinner.String(), "\r\033[K") {
		t.Errorf("spinner not cleared before output, got %q", spinner.String())
	}
}

// The spinner only shows after the idle threshold, so quick commands stay clean.
func TestHeartbeatSilentWhenActive(t *testing.T) {
	var spinner bytes.Buffer
	h := &heartbeat{out: &spinner, idle: time.Hour, label: "working", last: time.Now()}
	h.tick()
	if spinner.Len() != 0 {
		t.Errorf("no spinner expected before idle threshold, got %q", spinner.String())
	}
}

// On a non-terminal, the heartbeat is disabled: wrap is identity and run/close
// are no-ops, so piped/redirected output is byte-for-byte unchanged.
func TestHeartbeatDisabledOnNonTerminal(t *testing.T) {
	r, wpipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer wpipe.Close()

	h := newHeartbeat(wpipe, defaultHeartbeatIdle, "building") // a pipe is not a terminal
	if h.out != nil {
		t.Fatal("heartbeat should be disabled for a non-terminal")
	}
	var buf bytes.Buffer
	if got := h.wrap(&buf); got != &buf {
		t.Errorf("wrap should be identity when disabled")
	}
	h.run()   // no-op
	h.close() // no-op, must not panic or block
}

// Clearing a drawn spinner erases the line once and then does nothing until it's
// drawn again — so close() (which clears) and repeated output don't emit stray
// erase sequences. This guards the `shown = false` reset.
func TestHeartbeatClearResetsShown(t *testing.T) {
	var buf bytes.Buffer
	h := &heartbeat{out: &buf, idle: 0}

	h.mu.Lock()
	h.drawLocked() // shown = true
	h.mu.Unlock()

	buf.Reset()
	h.mu.Lock()
	h.clearLocked() // shown -> false, emits one erase
	h.mu.Unlock()
	if !strings.Contains(buf.String(), "\r\033[K") {
		t.Fatalf("first clear should erase, got %q", buf.String())
	}

	buf.Reset()
	h.mu.Lock()
	h.clearLocked() // nothing shown: must emit nothing
	h.mu.Unlock()
	if buf.Len() != 0 {
		t.Errorf("clear with nothing shown should emit nothing, got %q", buf.String())
	}
}

// The animation goroutine plus concurrent command output must be race-free, and
// close() must stop it and erase any spinner still on screen. Run under -race.
func TestHeartbeatConcurrentRunClose(t *testing.T) {
	var spinner, stdout bytes.Buffer
	h := &heartbeat{out: &spinner, idle: 0, last: time.Now()} // idle 0: the ticker draws eagerly
	h.run()
	w := h.wrap(&stdout)

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				w.Write([]byte("x"))
			}
		}()
	}
	wg.Wait()
	h.close() // must not panic/hang; erases a trailing spinner if shown

	if strings.Count(stdout.String(), "x") != 200 {
		t.Errorf("all output should pass through: got %d x's", strings.Count(stdout.String(), "x"))
	}
	if h.shown {
		t.Error("close() should leave no spinner shown")
	}
}
