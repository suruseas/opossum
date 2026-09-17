package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
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
// A variable, not a constant, so a test can drive the loop through many polls
// in milliseconds; nothing else assigns it.
var pollInterval = 3 * time.Second

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

	var w watch
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logf("[%s] stopping (asked to exit)", codeSupervisorStarted)
			return nil
		case <-ticker.C:
		}
		exists, unknown := o.superviseOnce(policies, state, logf)
		// An outage that lasts is told apart from a container the runtime
		// cannot answer about: when the unanswered polls reach the bound, the
		// runtime itself is asked (after decides when). Down, the watch stops —
		// a resident process with nothing it can reach is what the idle bound
		// exists to prevent — and says so truthfully; up, the watch goes on.
		line, stop := w.after(exists, unknown, o.rt.SystemRunning, services)
		if line != "" {
			logf("[%s] %s", codeSupervisorStarted, line)
		}
		if stop {
			return nil
		}
	}
}

// watch counts the polls that found nothing, and the polls the runtime could
// not answer, so that Supervise stops only when the project is known to be
// gone — not when the runtime happened to be unreachable for a minute.
type watch struct {
	idle, unanswered int
}

// after takes one poll's outcome and says what to log (if anything) and
// whether to stop. exists is whether any supervised container was found;
// unknown names the services the runtime could not be asked about. A poll that
// found a container resets both counts. A poll that found none and could not
// ask about some counts towards neither "idle" nor a stop: the project may be
// entirely there. It is said on the first such poll and on every
// idlePollsBeforeExit-th, naming only the services that went unanswered — and
// on exactly those polls runtimeUp is asked, once: if the runtime itself is
// down the watch stops and says that. Only polls that found none of the
// containers and were answered about all of them add up to "nothing left".
func (w *watch) after(exists bool, unknown []string, runtimeUp func() bool, services []string) (line string, stop bool) {
	switch {
	case exists:
		w.idle, w.unanswered = 0, 0
	case len(unknown) > 0:
		w.unanswered++
		if w.unanswered%idlePollsBeforeExit == 0 && !runtimeUp() {
			return fmt.Sprintf("the runtime is not running (%v could not be asked about for %d polls) — stopping; `opossum up` starts watching again", unknown, w.unanswered), true
		}
		if w.unanswered == 1 || w.unanswered%idlePollsBeforeExit == 0 {
			return fmt.Sprintf("the runtime could not be asked about %v (%d poll(s)) — still watching; `container ls -a` shows what is there", unknown, w.unanswered), false
		}
	default:
		w.unanswered = 0
		w.idle++
		if w.idle >= idlePollsBeforeExit {
			// "Not this project's" rather than "does not exist": a name can be
			// held by another project's container, which the watch leaves alone.
			return fmt.Sprintf("nothing left to watch (none of %v has had a container of this project's for %d polls) — stopping", services, w.idle), true
		}
	}
	return "", false
}

// superviseOnce is one poll: look at what's running, and act on what isn't.
// It reports whether any supervised container still exists, so the caller can
// stop watching a project that is no longer there — and which services the
// runtime could not be asked about, which is not the same thing (sorted).
func (o *Orchestrator) superviseOnce(policies map[string]compose.RestartPolicy, state map[string]serviceState, logf func(string, ...interface{})) (exists bool, unknown []string) {
	return o.superviseAt(time.Now(), policies, state, logf)
}

// superviseAt is superviseOnce with the clock supplied, so a test can drive many
// polls — backoff, escalation and giving up only appear over several of them.
func (o *Orchestrator) superviseAt(now time.Time, policies map[string]compose.RestartPolicy, state map[string]serviceState, logf func(string, ...interface{})) (exists bool, unknown []string) {
	anyExists := false
	for name, policy := range policies {
		cname := o.containerName(name)
		info := o.rt.Inspect(cname)
		st := state[name]

		// The runtime could not be asked: nothing is known about this service,
		// so nothing is done to it this poll — not "gone", not "crashed" — and its
		// state (backoff, giving up) is left as it was. The other services are
		// still looked at.
		if info.Unknown {
			unknown = append(unknown, name)
			continue
		}
		// The name is another project's container now: this project's was removed
		// and another put in its place (with `--dns-domain ""` names are bare
		// service names). It is not this project's to restart, and it does not
		// keep this project's watch going. Said when the name is first seen on that
		// project — again if it moves on to another project, or comes back after
		// being this project's, unlabeled or gone; not again for an inspect that
		// went unanswered in between. Read from the same inspect as the state, so
		// the owner and the state agree.
		// A container of the name with no project label at all was made outside
		// opossum, and is left alone the same way.
		if owner := info.Labels[projectLabel]; info.Exists && owner != o.Project.Name {
			seen := "project " + owner
			if owner == "" {
				seen = "no label"
			}
			if st.foreign != seen {
				if owner == "" {
					logf("[%s] leaving %q alone: its container %s carries no %s label, so it was not made by this project", codeSupervisorAction, name, cname, projectLabel)
				} else {
					logf("[%s] leaving %q alone: its container %s belongs to project %q, not this one", codeSupervisorAction, name, cname, owner)
				}
			}
			st.foreign = seen
			state[name] = st
			continue
		}
		if st.foreign != "" {
			st.foreign = ""
			state[name] = st
		}
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
	sort.Strings(unknown)
	return anyExists, unknown
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

// MarkStopped notes that `opossum stop` or `kill` took this service down.
// A failed write is reported: silently losing the marker means the supervisor
// fights the stop the user just asked for, which is worse than a warning.
func (o *Orchestrator) MarkStopped(service string) {
	warn := func(err error) {
		o.warnf(codeSupervisorAction, "couldn't record that %q was stopped or killed on purpose (%v) — "+
			"the supervisor may restart it when it exits\n", service, err)
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

// restartMarkerPath is where `restart` notes that it is taking this service
// down to bring it straight back, beside the stop marker.
func (o *Orchestrator) restartMarkerPath(service string) (string, error) {
	path, err := o.stopMarkerPath(service)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "restarting-"+strings.TrimPrefix(filepath.Base(path), "stopped-")), nil
}

// markRestarting notes that `restart` is stopping this service to start it
// again, so a `logs --follow` does not read the gap as the container's end.
// The note is a lock held on the marker file, not the file alone: the lock
// goes with the process, so a restart that dies part way (a Ctrl-C) leaves
// nothing that keeps a follow going for good. The returned func releases it.
func (o *Orchestrator) markRestarting(service string) func() {
	path, err := o.restartMarkerPath(service)
	if err != nil || os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return func() {}
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return func() {}
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX) != nil {
		f.Close()
		return func() {}
	}
	return func() {
		// The file stays: another restart of the same service may already be
		// waiting on this lock, and removing it would leave that one holding a
		// file nobody can open, which a follow reads as no restart at all. An
		// unlocked file left behind is read as no restart either way, and
		// `destroy` takes the directory.
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
}

// markRestartingAll marks every service of one `restart`, in the order of their
// names whatever order they were asked in, so two restarts that name the same
// services in different orders cannot each hold one the other waits for. It
// returns those names, sorted and without repeats (a name whose mark could not
// be taken is among them all the same), and the release for all of them. The
// services given are left in the order the caller had them: `restart` stops and
// starts them in dependency order, which is not the order the marks are taken
// in.
func (o *Orchestrator) markRestartingAll(services []string) ([]string, func()) {
	taken := append([]string(nil), services...)
	sort.Strings(taken)
	// A name asked for twice would wait for its own mark: one file, two locks
	// of the same process, and flock counts them apart.
	taken = slices.Compact(taken)
	releases := make([]func(), 0, len(taken))
	for _, name := range taken {
		releases = append(releases, o.markRestarting(name))
	}
	// Released in any order: each mark is its own lock, and one waiting for it
	// takes it as soon as it is free.
	return taken, func() {
		for _, release := range releases {
			release()
		}
	}
}

// isRestarting reports whether a `restart` holds this service's marker: a
// marker file nobody holds (left by a restart that died) is not one.
func (o *Orchestrator) isRestarting(service string) bool {
	path, err := o.restartMarkerPath(service)
	if err != nil {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

func (o *Orchestrator) wasStoppedByUs(service string) bool {
	path, err := o.stopMarkerPath(service)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}
