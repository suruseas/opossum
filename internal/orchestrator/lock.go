package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// projectLock keeps two opossum processes from changing one project at the
// same time. Two `up`s a second apart broke a project (measured on container
// 1.4.1): each decided what it owned on its own — the first created `db`, the
// second found `db` up to date — so when the first lost the race for `web`
// and rolled back, it removed the `db` the second had just reported, and the
// second exited 0 over a project missing a service. docker compose folds a
// second `up` into "already running"; here the two never overlap.
//
// Which commands take it, and why: `up`, `down` and `destroy` — the ones
// that create or remove the project's containers and network, where two
// owners at once is the hazard above — and, through them, a one-off `run`
// while it starts the service's dependencies (it calls `up` for those; with
// `--no-deps` it takes no lock) and `watch` while it rebuilds a service. A
// foreground `up` holds it until the service exits or Ctrl-C: attached, the
// call has not finished. Not taken by the readers (`ps`, `logs`, `config`,
// `port`, `ls`, `volumes`): an `up` in progress shows a half-started
// project to `ps` under docker compose too. Not taken for the one-off's own
// container (it removes only that; an `up` beside it is not deprived of
// anything), by `stop`/`start`/`restart`/`kill` (they act on containers
// that exist and roll nothing back), `import` (images only) or `ws` (files,
// not the project).
//
// The lock is a file under the project's state directory held with flock:
// the OS releases it when the holder exits, however it exits, so a process
// killed mid-`up` leaves no lock to steal. The pid in the file is a note for
// the refusal, not the lock — written after the lock is held, so a reader
// racing that write may still see the previous holder's pid for an instant.
// `down` leaves the file; `destroy` removes the state directory with it.
type projectLock struct{ f *os.File }

// lockProject takes the project's lock without waiting. A lock held elsewhere
// is refused with OPSM-208 naming the holder's pid; nothing waits for the
// other command, since what it will leave is not known.
func lockProject(project string) (*projectLock, error) {
	dir, err := projectStateDir(project)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder := ""
		if b, rerr := os.ReadFile(f.Name()); rerr == nil {
			if pid, perr := strconv.Atoi(string(b)); perr == nil && pid > 0 {
				holder = fmt.Sprintf(" (pid %d)", pid)
			}
		}
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("[%s] another opossum command is changing project %q%s — wait for it to finish (a foreground `up` ends with Ctrl-C), then retry", codeProjectBusy, project, holder)
		}
		return nil, fmt.Errorf("locking project %q: %w", project, err)
	}
	// Written after the lock is held; a failed write costs only the note.
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0)
	return &projectLock{f: f}, nil
}

// release lets the lock go. Closing the file releases the flock; the file
// stays, so the next holder's pid overwrites it.
func (l *projectLock) release() {
	if l != nil && l.f != nil {
		_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
		_ = l.f.Close()
	}
}
