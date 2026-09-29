package orchestrator

import (
	"os"
	"strconv"
	"time"

	"github.com/suruseas/opossum/internal/runtime"
)

// DecodeStartErrorForTest is decodeStartError for the tests in this package's
// external test file: what a failed start is told to the reader as.
func (o *Orchestrator) DecodeStartErrorForTest(service string, err error) string {
	return o.decodeStartError(service, nil, err).Error()
}

// RunErrorForTest is a failed run carrying the output that was captured from
// it — the runtime's and, for a foreground `up`, the container's own.
func RunErrorForTest(err error, stderr string) error {
	return &runtime.RunError{Err: err, Stderr: stderr}
}

// RebuildServiceForTest is the rebuild `watch` does for one service: an `up` of
// that service alone, its dependencies left as they are.
func (o *Orchestrator) RebuildServiceForTest(name string) error { return o.rebuildService(name) }

// The exports below reach StopSupervisor's internal seams from black-box tests
// in this directory (package orchestrator_test) — which, unlike the white-box
// tests in this package, can drive the change through Down and Destroy
// themselves (via fakeShim/project in orchestrator_test.go) rather than by
// calling StopSupervisor or stopSupervisorAndReport directly. Test-only: these
// symbols exist only in the test binary, never in the built opossum.

// SetProcessAliveForTest replaces the seam StopSupervisor's confirmation loop
// polls, returning a func that restores it.
func SetProcessAliveForTest(fn func(int) bool) (restore func()) {
	saved := processAliveFn
	processAliveFn = fn
	return func() { processAliveFn = saved }
}

// ShrinkStopSupervisorBudgetsForTest replaces StopSupervisor's wait budgets,
// returning a func that restores them.
func ShrinkStopSupervisorBudgetsForTest(term, kill, poll time.Duration) (restore func()) {
	savedTerm, savedKill, savedPoll := stopSupervisorTermWait, stopSupervisorKillWait, stopSupervisorPoll
	stopSupervisorTermWait, stopSupervisorKillWait, stopSupervisorPoll = term, kill, poll
	return func() {
		stopSupervisorTermWait, stopSupervisorKillWait, stopSupervisorPoll = savedTerm, savedKill, savedPoll
	}
}

// WriteSupervisorPidFileForTest writes project's pid file as though pid were its
// claimed supervisor, so a test can point StopSupervisor at a real, signalable
// process without going through ClaimSupervisor/StartSupervisor (which claim for
// the calling process itself, not an arbitrary spawned one).
func WriteSupervisorPidFileForTest(project string, pid int) error {
	path, err := supervisorPidFile(project)
	if err != nil {
		return err
	}
	dir, err := supervisorStateDir(project)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	content := strconv.Itoa(pid) + " " + processStartedAt(pid) + "\n"
	return os.WriteFile(path, []byte(content), 0o644)
}

// SupervisorPidFileExistsForTest reports whether project's pid file is still
// there, so a test can tell a confirmed stop (which clears it) from a not
// -confirmed one (which deliberately keeps it — see StopSupervisor's own
// comment on why: reporting success there would hide an orphan).
func SupervisorPidFileExistsForTest(project string) bool {
	path, err := supervisorPidFile(project)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}
