package orchestrator

import (
	"time"

	"github.com/suruseas/opossum/internal/compose"
)

// Deciding when to restart
//
// Docker's engine is always there to notice a container exited and start it
// again. Apple `container` has no such resident, so `up` leaves a small watcher
// behind instead (see supervisor.go). This file is the part that decides —
// separated out because it is pure, and because it is where the judgement lives.
//
// One runtime limitation shapes everything here: **a container's exit code is not
// observable**. Neither `container inspect` nor `container ls` reports it (checked
// on the real runtime), so a service that exited 0 and one that crashed look
// identical from outside. That makes `on-failure` impossible to honour exactly,
// and the compromise is written down in decide() rather than hidden.

// restartDecision is what the watcher should do about one stopped container.
type restartDecision int

const (
	leaveIt   restartDecision = iota // policy says no, or we stopped it ourselves
	restartIt                        // bring it back
	gaveUp                           // it kept failing; stop trying and say so
)

// serviceState is what the watcher remembers between polls.
type serviceState struct {
	restarts    int       // consecutive restarts without a stable run
	lastAction  time.Time // when we last restarted it
	stoppedByUs bool      // `opossum stop` asked for this; unless-stopped must honour it
	gaveUp      bool
}

// backoff limits how fast a crash loop can spin. The first restart is immediate —
// the case this feature exists for is a service that lost a startup race and will
// succeed on the next try, and making that wait would be pure latency.
var backoffSteps = []time.Duration{
	0, 1 * time.Second, 2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second,
}

// maxOnFailureRetries bounds `on-failure` when the compose file didn't say. With
// no exit code to read, a service that exits cleanly looks like one that crashed,
// so an unbounded policy would restart a finished job forever. A small bound keeps
// the startup-race case working (it converges in one or two tries) while a service
// that simply runs to completion is left alone after a few attempts.
const maxOnFailureRetries = 3

// stableRun is how long a container must stay up before its restart count resets.
// Without it a service that crashes every few minutes would eventually exhaust the
// backoff and be given up on, even though each individual recovery worked.
const stableRun = 60 * time.Second

// decide answers what to do about a service the watcher found stopped.
func decide(p compose.RestartPolicy, st serviceState, now time.Time) (restartDecision, time.Duration) {
	if st.gaveUp || !p.Wants() {
		return leaveIt, 0
	}
	// A stop the user asked for wins, for BOTH policies. Docker documents `always`
	// as "if it is manually stopped, it is restarted only when the Docker daemon
	// restarts or the container itself is manually restarted" — so within one daemon
	// lifetime a manual stop stands, and the same is true of `unless-stopped`. The
	// two differ only across a daemon restart, which for opossum maps to the next
	// `up` (the supervisor's lifetime is exactly up…down). `up` starts services
	// regardless and clears the marker, so that distinction resolves itself.
	//
	// Undoing `opossum stop` within one poll would make the command useless for the
	// most common policy there is.
	//
	// (Stated from Docker's documented behaviour: the daemon wasn't running here to
	// confirm it by experiment, unlike the exit-code limitation above.)
	if st.stoppedByUs {
		return leaveIt, 0
	}
	if p.Mode == compose.RestartOnFailure {
		limit := p.MaxRetry
		if limit == 0 {
			// The compose file said `on-failure` with no count. Docker would retry
			// forever, relying on the exit code to know a clean exit isn't a failure.
			// Without that signal, retrying forever would loop a finished job, so
			// opossum bounds it and records the assumption in the log.
			limit = maxOnFailureRetries
		}
		if st.restarts >= limit {
			return gaveUp, 0
		}
	}
	return restartIt, backoffFor(st.restarts)
}

// backoffFor is how long to wait before the nth consecutive restart.
func backoffFor(n int) time.Duration {
	if n < 0 {
		n = 0
	}
	if n >= len(backoffSteps) {
		return backoffSteps[len(backoffSteps)-1]
	}
	return backoffSteps[n]
}

// observeRunning resets the restart count once a container has been up long
// enough to call the recovery successful.
//
// It deliberately does NOT reset the count for `on-failure`. That bound exists
// because a clean exit is indistinguishable from a crash, and a job that runs for
// longer than stableRun before finishing would otherwise reset the counter every
// cycle and be restarted forever — exactly the "restarting a service that may have
// finished on purpose" this is supposed to prevent.
func observeRunning(st serviceState, now time.Time, p compose.RestartPolicy) serviceState {
	if p.Mode != compose.RestartOnFailure &&
		st.restarts > 0 && !st.lastAction.IsZero() && now.Sub(st.lastAction) >= stableRun {
		st.restarts = 0
		st.gaveUp = false
	}
	// Coming back up on its own clears a stop we recorded: whatever the container
	// is doing now, it isn't the state `opossum stop` left behind.
	st.stoppedByUs = false
	return st
}
