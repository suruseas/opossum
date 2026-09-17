package orchestrator

import (
	"io"
	"testing"
	"time"
)

// heldWriter holds each write until release is closed, as a reader that has
// stopped taking lines holds the one being written.
type heldWriter struct {
	entered chan struct{}
	release chan struct{}
}

func (h *heldWriter) Write(p []byte) (int, error) {
	h.entered <- struct{}{}
	<-h.release
	return len(p), nil
}

// A followed stream is ended only once nothing has been written for the drain
// time: a write the reader holds is not that, nor is the time the reader held
// it, which is counted from when the write returned.
func TestLastWriteIsIdleOnlyAfterTheLastWriteReturned(t *testing.T) {
	const d = 150 * time.Millisecond
	hold := func(t *testing.T, l *lastWrite, h *heldWriter, held time.Duration) {
		t.Helper()
		done := make(chan struct{})
		go func() { _, _ = l.Write([]byte("line\n")); close(done) }()
		<-h.entered
		time.Sleep(held)
		close(h.release)
		<-done
	}
	t.Run("a write the reader holds", func(t *testing.T) {
		h := &heldWriter{entered: make(chan struct{}, 1), release: make(chan struct{})}
		l := &lastWrite{w: h}
		done := make(chan struct{})
		go func() { _, _ = l.Write([]byte("line\n")); close(done) }()
		<-h.entered
		time.Sleep(2 * d)
		if l.idle(time.Now(), d) {
			t.Error("want not idle while a write is held")
		}
		close(h.release)
		<-done
	})
	t.Run("just after a write held longer than the drain time returned", func(t *testing.T) {
		h := &heldWriter{entered: make(chan struct{}, 1), release: make(chan struct{})}
		l := &lastWrite{w: h}
		hold(t, l, h, 2*d)
		if l.idle(time.Now(), d) {
			t.Error("want not idle just after the held write returned")
		}
	})
	t.Run("the drain time after that write, the control", func(t *testing.T) {
		h := &heldWriter{entered: make(chan struct{}, 1), release: make(chan struct{})}
		l := &lastWrite{w: h}
		hold(t, l, h, 2*d)
		time.Sleep(d)
		if !l.idle(time.Now(), d) {
			t.Error("want idle once the drain time has passed with nothing written")
		}
	})
	t.Run("nothing written yet, the control", func(t *testing.T) {
		l := &lastWrite{w: io.Discard}
		if !l.idle(time.Now(), d) {
			t.Error("want idle with nothing ever written")
		}
	})
}
