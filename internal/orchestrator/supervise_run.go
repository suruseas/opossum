package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/suruseas/opossum/internal/compose"
)

// idlePollsBeforeExit is how many consecutive polls may find NONE of the
// supervised containers before the supervisor stops. A watcher whose project has
// been taken apart by other means — `container delete`, a different tool, a
// `down` that couldn't reach it — has nothing left to do, and a resident process
// with nothing to watch is exactly what this feature must not leave behind. The
// count is generous so a project being recreated isn't abandoned mid-way.
const idlePollsBeforeExit = 20

// pollInterval is how often the supervisor asks the runtime what is running.
// Polling is the only option — Apple `container` has no event stream — so this
// trades responsiveness against a `container ls` every few seconds.
const pollInterval = 3 * time.Second

// Supervise watches this project's `restart:` services until ctx is cancelled.
// It runs in the background process started by `up`; the decisions it makes live
// in supervise_policy.go.
func (o *Orchestrator) Supervise(ctx context.Context, services []string, logw io.Writer) error {
	if len(services) == 0 {
		return nil
	}
	policies := map[string]compose.RestartPolicy{}
	for _, name := range services {
		svc := o.Project.Services[name]
		if svc == nil {
			continue
		}
		p, err := svc.RestartPolicy()
		if err != nil {
			return fmt.Errorf("service %q: %w", name, err)
		}
		policies[name] = p
	}
	state := map[string]serviceState{}
	logf := func(format string, a ...interface{}) {
		fmt.Fprintf(logw, "%s "+format+"\n", append([]interface{}{time.Now().Format(time.RFC3339)}, a...)...)
	}
	logf("[%s] supervising %v (poll %s)", codeSupervisorStarted, services, pollInterval)

	idle := 0
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logf("[%s] stopping (asked to exit)", codeSupervisorStarted)
			return nil
		case <-ticker.C:
		}
		if !o.superviseOnce(policies, state, logf) {
			idle++
			if idle >= idlePollsBeforeExit {
				logf("[%s] nothing left to watch (none of %v exists for %d polls) — stopping",
					codeSupervisorStarted, services, idle)
				return nil
			}
		} else {
			idle = 0
		}
	}
}

// superviseOnce is one poll: look at what's running, and act on what isn't.
// superviseOnce reports whether any supervised container still exists, so the
// caller can stop watching a project that is no longer there.
func (o *Orchestrator) superviseOnce(policies map[string]compose.RestartPolicy, state map[string]serviceState, logf func(string, ...interface{})) bool {
	return o.superviseAt(time.Now(), policies, state, logf)
}

// superviseAt is superviseOnce with the clock supplied, so a test can drive many
// polls — backoff, escalation and giving up only appear over several of them.
func (o *Orchestrator) superviseAt(now time.Time, policies map[string]compose.RestartPolicy, state map[string]serviceState, logf func(string, ...interface{})) bool {
	anyExists := false
	for name, policy := range policies {
		cname := o.containerName(name)
		info := o.rt.Inspect(cname)
		st := state[name]

		if info.Exists {
			anyExists = true
		}
		if info.Exists && info.State == "running" {
			// The marker is deliberately NOT cleared here. `container stop` isn't
			// instantaneous, so a poll landing between `Stop` writing the marker and
			// the container actually stopping would delete the marker that was just
			// written — and the next poll, seeing a stopped container with no marker,
			// would undo the stop the user asked for. The marker is cleared by the
			// commands that mean "bring this back": `up` and `start`.
			state[name] = observeRunning(st, now, policy)
			continue
		}
		// A container that no longer exists was removed, not crashed — `down` or a
		// manual delete. Recreating it here would resurrect a project the user took
		// apart, which is not what `restart:` asks for.
		if !info.Exists {
			continue
		}
		anyExists = true
		// The marker on disk is the truth: `stop` writes it, `up`/`start` remove it.
		// ORing with the previous poll's value would make the flag sticky, so a
		// service brought back by `start` would never be supervised again.
		st.stoppedByUs = o.wasStoppedByUs(name)
		what, wait := decide(policy, st, now)
		switch what {
		case leaveIt:
			state[name] = st
			continue
		case gaveUp:
			st.gaveUp = true
			state[name] = st
			logf("[%s] giving up on %q after %d restart(s): its `restart: %s` has no more retries. "+
				"Apple container doesn't report exit codes, so opossum can't tell a crash from a clean exit "+
				"and stops rather than looping a service that may have finished on purpose.",
				codeSupervisorAction, name, st.restarts, policy.Mode)
			continue
		}
		if wait > 0 {
			if now.Sub(st.lastAction) < wait {
				state[name] = st
				continue // still backing off
			}
		}
		if err := o.rt.Start(cname); err != nil {
			logf("[%s] couldn't restart %q: %v", codeSupervisorAction, name, err)
		} else {
			logf("[%s] restarted %q (attempt %d; `restart: %s`)", codeSupervisorAction, name, st.restarts+1, policy.Mode)
		}
		st.restarts++
		st.lastAction = now
		st.stoppedByUs = false
		state[name] = st
	}
	return anyExists
}

// stopMarkerPath records that opossum stopped a service on purpose, so a policy
// can tell a deliberate stop from a crash. The runtime doesn't record who stopped
// a container, so opossum has to remember for itself.
//
// The file name is a hash of the exact service name, not a sanitised version of
// it: `api.v2`, `api_v2` and `API-V2` are three legal, distinct compose services
// that all sanitise to `api-v2`, and they would then share one marker — stopping
// one would silence supervision for the others.
func (o *Orchestrator) stopMarkerPath(service string) (string, error) {
	dir, err := supervisorStateDir(o.Project.Name)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(service))
	return filepath.Join(dir, "stopped-"+hex.EncodeToString(sum[:8])), nil
}

// MarkStopped notes that `opossum stop` (or `down`) took this service down.
// A failed write is reported: silently losing the marker means the supervisor
// fights the stop the user just asked for, which is worse than a warning.
func (o *Orchestrator) MarkStopped(service string) {
	warn := func(err error) {
		o.warnf(codeSupervisorAction, "couldn't record that %q was stopped (%v) — "+
			"the supervisor may restart it\n", service, err)
	}
	path, err := o.stopMarkerPath(service)
	if err != nil {
		warn(err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		warn(err)
		return
	}
	if err := os.WriteFile(path, []byte("stopped\n"), 0o644); err != nil {
		warn(err)
	}
}

// ClearStopped forgets a recorded stop, so a later `start` is supervised again.
func (o *Orchestrator) ClearStopped(service string) {
	if path, err := o.stopMarkerPath(service); err == nil {
		os.Remove(path)
	}
}

func (o *Orchestrator) wasStoppedByUs(service string) bool {
	path, err := o.stopMarkerPath(service)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}
