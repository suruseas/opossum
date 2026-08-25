// Package suitedir gives a suite a temporary directory that a later run can
// clean up, for the runs that never clean up after themselves.
//
// A suite builds its binaries under $TMPDIR and removes them when it finishes.
// "When it finishes" is the catch: a panic — and a -timeout firing is a panic —
// ends the test binary where it stands, so the removal never happens. Nothing
// inside the process can fix that, because the process is what stopped.
//
// So the fix is outside it in time rather than in place: the directory carries
// the pid of the run that made it, and every later run removes the ones whose
// maker is gone. A directory belonging to a suite running right now has a live
// pid and is left alone, which is what makes this safe to do from a machine that
// runs several at once.
package suitedir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Make makes a temporary directory named after this process, after removing any
// left behind by a process that is no longer alive.
//
// prefix is the family name — "opossum-cmd-test-" and its siblings. What comes
// back is <prefix><pid>-<random>, and the sweep only ever touches names of that
// shape: a directory from before this was introduced has no pid where one is
// expected, and is left where it is rather than guessed about.
func Make(prefix string) (string, error) {
	sweep(prefix)
	return os.MkdirTemp("", fmt.Sprintf("%s%d-*", prefix, os.Getpid()))
}

func sweep(prefix string) {
	names, err := filepath.Glob(filepath.Join(os.TempDir(), prefix+"*"))
	if err != nil {
		return
	}
	for _, name := range names {
		if pid, ok := makerOf(filepath.Base(name), prefix); ok && !Alive(pid) {
			os.RemoveAll(name)
		}
	}
}

// makerOf reads the pid out of a directory name, and says so when there is not
// one to read — an older name, or something else that happens to start the same
// way. Neither is ours to remove.
func makerOf(base, prefix string) (int, bool) {
	rest, found := strings.CutPrefix(base, prefix)
	if !found {
		return 0, false
	}
	return PidLeading(rest)
}

// PidLeading reads a pid off the front of a name, and says so when there is not
// one to read.
//
// Exported for cmd/opossum's own orphan sweep, which reads the same shape off
// its probes. The question those two share is not "is this pid alive" but "is
// this string a number that may be asked about at all" — and that is where they
// were most at risk of drifting, because the bounds are easy to leave out.
//
// A separator after the digits is required, which the probe sweep did not ask
// for before. Every probe name has one; if one ever loses it, this answers no
// and the orphan waits rather than being signalled. That is the safe direction,
// but it is a change, and a silent one.
func PidLeading(rest string) (int, bool) {
	digits, _, found := strings.Cut(rest, "-")
	if !found {
		return 0, false
	}
	pid, err := strconv.Atoi(digits)
	// Zero and negatives are not pids: to kill(2) they mean this process group
	// and everything reachable. Above the maximum is not a pid either, and
	// matters more than it looks — kill(2) takes a pid_t, so a number too large
	// silently becomes a different one (4294967296 arrives as 0). The signal
	// here is 0, so today that is harmless; the day somebody sends a real one it
	// would not be.
	if err != nil || pid <= 0 || pid > pidCeiling {
		return 0, false
	}
	return pid, true
}

// pidCeiling is above every pid either kernel hands out — macOS caps at 99999,
// Linux's PID_MAX_LIMIT is 2^22 — and below where pid_t truncation begins.
const pidCeiling = 1 << 23

// Alive says whether a process with this pid exists. Signal 0 asks without
// sending anything: ESRCH is the one answer that means gone. Anything else —
// including EPERM, which is somebody else's process — is a live pid, and a live
// pid is a directory somebody may still be using.
//
// A pid can be reused, so a long-dead run whose number has come round again
// keeps its directory. That is the direction to err in: this removes less than
// it could rather than removing something in use.
//
// One thing it does remove that is still in use: a suite's own children can
// outlive the suite. A supervisor a test started and never stopped goes on
// running out of a directory whose maker is gone, and this takes the directory
// out from under it. The process survives — Unix keeps the file open — but its
// path now reads as deleted. Measured and left alone deliberately: asking
// whether anything is running out of a directory means asking the process table
// from a package every suite imports, and the process that is still there is a
// leak somebody already has to deal with. See cmd/noleftovers, which reports it.
// It is exported because cmd/opossum's own orphan sweep asks the same question
// about the probes it leaves, and two answers to one question drift apart: the
// two implementations had already diverged on errors.Is before this was shared.
func Alive(pid int) bool {
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
