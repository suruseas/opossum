package runtime

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// defaultHeartbeatIdle is how long a streamed command must be silent before the
// spinner appears, so quick commands never draw one.
const defaultHeartbeatIdle = 2 * time.Second

// heartbeat shows a "still working" spinner on an interactive terminal during
// long silent stretches of a streamed command (e.g. build context transfer or
// base-image pull), so `up` never looks frozen. It stays out of the way: it
// draws only after a stretch with no output and erases itself the instant real
// output resumes or the command ends. On a non-terminal (pipe/file/CI) it does
// nothing, so logs and tests stay byte-for-byte unchanged.
type heartbeat struct {
	out   io.Writer     // spinner sink (a terminal); nil = disabled (no-op)
	idle  time.Duration // draw once this much time passes with no output
	label string        // what we're waiting on, e.g. "building"

	mu    sync.Mutex
	last  time.Time // time of the last byte written by the command
	shown bool      // a spinner frame is currently on screen
	frame int

	stop    chan struct{}
	stopped chan struct{}
}

// newHeartbeat returns a heartbeat that draws to out, or a disabled one if out
// isn't an interactive terminal.
func newHeartbeat(out *os.File, idle time.Duration, label string) *heartbeat {
	if !isTerminal(out) {
		return &heartbeat{} // disabled: wrap is identity, run/close are no-ops
	}
	return &heartbeat{out: out, idle: idle, label: label, last: time.Now()}
}

// wrap returns a writer that forwards to w but first clears any spinner and
// records the activity, so command output and the spinner never interleave.
func (h *heartbeat) wrap(w io.Writer) io.Writer {
	if h.out == nil {
		return w
	}
	return &hbWriter{h: h, w: w}
}

type hbWriter struct {
	h *heartbeat
	w io.Writer
}

func (x *hbWriter) Write(p []byte) (int, error) {
	x.h.mu.Lock()
	defer x.h.mu.Unlock()
	x.h.clearLocked()
	x.h.last = time.Now()
	return x.w.Write(p)
}

// run starts the animation loop.
func (h *heartbeat) run() {
	if h.out == nil {
		return
	}
	h.stop = make(chan struct{})
	h.stopped = make(chan struct{})
	go func() {
		defer close(h.stopped)
		t := time.NewTicker(120 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-h.stop:
				return
			case <-t.C:
				h.tick()
			}
		}
	}()
}

// close stops the loop and erases any spinner still on screen.
func (h *heartbeat) close() {
	if h.out == nil {
		return
	}
	close(h.stop)
	<-h.stopped
	h.mu.Lock()
	h.clearLocked()
	h.mu.Unlock()
}

// tick draws a frame if the command has been silent long enough.
func (h *heartbeat) tick() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if time.Since(h.last) >= h.idle {
		h.drawLocked()
	}
}

var spinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

func (h *heartbeat) drawLocked() {
	f := spinnerFrames[h.frame%len(spinnerFrames)]
	h.frame++
	secs := int(time.Since(h.last).Seconds())
	// \r returns to column 0; \033[K clears to end of line, so the frame updates
	// in place instead of scrolling.
	fmt.Fprintf(h.out, "\r\033[K%c %s… (%ds)", f, h.label, secs)
	h.shown = true
}

func (h *heartbeat) clearLocked() {
	if h.shown {
		fmt.Fprint(h.out, "\r\033[K")
		h.shown = false
	}
}

// isTerminal reports whether f is an interactive terminal (a character device),
// using only the standard library so opossum keeps its minimal dependency set.
func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
