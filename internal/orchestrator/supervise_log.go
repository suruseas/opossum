package orchestrator

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sync"
)

// The supervisor's log is the only record of what it did while nobody was
// looking, so it has to be kept — but a service that fails permanently is
// restarted for as long as its policy says, and each attempt writes a line. Left
// alone that is about 270KB a day, forever, for a project the user may have
// forgotten. A background process that holds an unbounded resource is a bug
// whatever the growth rate; this bounds it.
//
// The old half is what goes. What a supervisor did in the last few thousand lines
// answers "why is this service down?"; what it did last week does not, and if it
// did something interesting there, it did the same thing again since — a
// crash-loop repeats by definition.

// maxSupervisorLog is the cap. Big enough to hold days of a normal project (one
// line at start-up, then silence) and hours of a crash loop, small enough that
// nobody notices the file.
const maxSupervisorLog = 1 << 20 // 1MB

// cappedLog is an append-only writer that trims its file to the newest half when
// it outgrows max. It keeps a single inode across a trim (truncating the handle it
// already holds, never replacing the file) because the supervisor's own stdout is
// a second, inherited handle on the same file: a rotation by rename — or a
// re-created path — would leave that handle writing to a file nothing reads, and
// a panic, the one thing that goes to stdout rather than through here, would
// vanish.
//
// The size is read from the file rather than counted, so the bound holds
// unconditionally: it does not depend on this being the only writer, and it
// survives a supervisor that restarts against a file already at the cap. One
// stat per line, a few times a minute, is not worth optimising away for a
// weaker guarantee.
//
// It assumes a line much shorter than max/2 — the supervisor's longest is a few
// hundred bytes against a 1MB cap. A single write larger than that leaves nothing
// to keep, and the log starts again from the trim note.
type cappedLog struct {
	mu   sync.Mutex
	f    *os.File
	path string
	max  int64
}

// newCappedLog opens (or creates) the log for appending.
func newCappedLog(path string, max int64) (*cappedLog, error) {
	// O_RDWR, not O_WRONLY: a trim has to read the file it is about to rewrite, and
	// it must read *this* file — see trim.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &cappedLog{f: f, path: path, max: max}, nil
}

func (c *cappedLog) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// A failed stat means the size is unknown, and the answer to that is to append
	// rather than truncate: trimming a log we cannot measure would throw away
	// records to enforce a bound we can't even check. fstat on an open regular file
	// does not fail in practice, so the bound rests on that and nothing else.
	if fi, err := c.f.Stat(); err == nil && fi.Size()+int64(len(p)) > c.max {
		// Trimming is housekeeping; failing at it must not cost the caller the line
		// it was recording. Nothing is remembered about the failure either — the size
		// comes from the file, so the next line tries again rather than waiting for a
		// counter to refill.
		_ = c.trim()
	}
	return c.f.Write(p)
}

// trim rewrites the file with its newest half, preceded by a line saying so. A
// reader who finds the log starting mid-story should be able to see that it was
// cut rather than wonder what went wrong at the top.
func (c *cappedLog) trim() error {
	// Read through the handle, not the path. They are usually the same file, but if
	// the log has been replaced under us they are not — and reading the path would
	// then copy a stranger's bytes into this log, lose the real history, and blow
	// the cap (measured at 2.3x). Reading what we are about to truncate is the only
	// way the bound can be unconditional.
	fi, err := c.f.Stat()
	if err != nil {
		return err
	}
	data := make([]byte, fi.Size())
	if _, err := io.ReadFull(io.NewSectionReader(c.f, 0, fi.Size()), data); err != nil {
		return err
	}
	keep := data
	if len(data) > 0 {
		half := len(data) / 2
		// Start at a line boundary: half a line at the top reads like corruption.
		if i := bytes.IndexByte(data[half:], '\n'); i >= 0 {
			keep = data[half+i+1:]
		} else {
			keep = nil
		}
	}
	note := fmt.Sprintf("[%s] earlier lines dropped: this log reached %d bytes and was trimmed to its newest half\n",
		codeSupervisorLogTrimmed, c.max)
	// The handle we already hold, not the path: os.WriteFile would create a new
	// file if the old one had been deleted, and the supervisor's inherited stdout
	// would go on writing to the orphan while the path showed only this note. The
	// handle is O_APPEND, so a write after Truncate(0) lands at offset 0.
	if err := c.f.Truncate(0); err != nil {
		return err
	}
	if _, err := c.f.Write(append([]byte(note), keep...)); err != nil {
		return err
	}
	return nil
}

func (c *cappedLog) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.f.Close()
}

// NoticeSupervisorLogUncapped is what a supervisor says when it could not open a
// bounded log. It matters because the fallback is invisible otherwise: the
// unbounded stream goes to the same file, so the log looks identical while the
// only thing that changed is that nothing stops it growing.
func NoticeSupervisorLogUncapped(cause error) string {
	return fmt.Sprintf("[%s] couldn't open a size-capped log (%v) — writing without a cap, so this "+
		"file can grow without limit; remove it if it gets large, or restart the project",
		codeSupervisorLogUncapped, cause)
}

// OpenSupervisorLog opens this project's supervisor log for writing, bounded. The
// caller closes it.
func OpenSupervisorLog(project string) (io.WriteCloser, error) {
	path, err := SupervisorLogFile(project)
	if err != nil {
		return nil, err
	}
	return newCappedLog(path, maxSupervisorLog)
}
