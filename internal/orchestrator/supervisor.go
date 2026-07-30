package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/suruseas/opossum/internal/compose"
)

// The per-project supervisor
//
// `restart:` is the most common field in real compose files, and until now
// opossum ignored it: Docker leans on its always-running engine to notice a
// container exited, and Apple `container` has no equivalent resident. So `up`
// leaves a small watcher of its own behind, and `down` takes it away again.
//
// "No daemon" is one of opossum's selling points, so it is worth being precise
// about what this is: a few MB of Go polling `container ls`, scoped to one
// project, started and stopped with that project. What opossum avoids is a
// multi-gigabyte VM sitting idle — not a process that watches the containers you
// just asked it to keep running.
//
// State lives outside the user's project directory on purpose: a repository
// should not grow pid files because someone ran `up` in it.

// supervisorStateDir is where a project's supervisor keeps its pid and log.
// XDG_STATE_HOME is honoured so a test — or a user with opinions — can move it.
func supervisorStateDir(project string) (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	// The project name reaches this from a compose file, and this helper both
	// writes and removes files. A name of `../../x` would put those somewhere in the
	// user's home, so it is reduced to a single safe path element here rather than
	// trusting every present and future caller to have sanitised it.
	return filepath.Join(base, "opossum", compose.SanitizeName(project)), nil
}

func supervisorPidFile(project string) (string, error) {
	dir, err := supervisorStateDir(project)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "supervisor.pid"), nil
}

// SupervisorLogFile is where the supervisor records what it restarted and why, so
// a restart that happened while nobody was looking can still be accounted for.
func SupervisorLogFile(project string) (string, error) {
	dir, err := supervisorStateDir(project)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "supervisor.log"), nil
}

// SupervisorPID returns the pid of this project's running supervisor, or 0.
//
// A pid alone is not enough to act on. A pid file survives a crash or a reboot,
// macOS recycles pids quickly, and StopSupervisor escalates to SIGKILL — so
// trusting a bare number risks killing an unrelated process that happens to have
// inherited the number. The file therefore records the process's start time as
// well, and the pid is believed only if a live process with that start time is
// still there.
func SupervisorPID(project string) int {
	path, err := supervisorPidFile(project)
	if err != nil {
		return 0
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, started, ok := parsePidFile(string(b))
	if !ok || !processAlive(pid) {
		return 0
	}
	// No token means the file can't be tied to a process, and StopSupervisor
	// escalates to SIGKILL — so it is treated as stale rather than acted on.
	if started == "" || processStartedAt(pid) != started {
		return 0
	}
	return pid
}

// parsePidFile reads "<pid> <start-marker>"; the marker may be absent in a file
// written by an older version.
func parsePidFile(s string) (pid int, started string, ok bool) {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) == 0 {
		return 0, "", false
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil || n <= 0 {
		return 0, "", false
	}
	if len(fields) > 1 {
		started = fields[1]
	}
	return n, started, true
}

// processStartedAt returns a stable per-process token (its start time as `ps`
// reports it), or "" when it can't be read. Two processes with the same pid at
// different times will not share one.
func processStartedAt(pid int) string {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(string(out)), "-")
}

// processAlive reports whether a pid is a live process. Signal 0 performs the
// permission and existence checks without delivering anything.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// ClaimSupervisor is how a supervisor takes ownership of a project: it creates the
// pid file exclusively, so exactly one process can hold it. The CHILD claims,
// not the parent — a parent that checked first and wrote after would leave a
// window in which two `up`s both see "nobody is watching" and both spawn, and the
// loser of that race becomes an orphan nothing can stop.
//
// A file left by a supervisor that is no longer running is not a claim; it is
// replaced.
func ClaimSupervisor(project string) error {
	dir, err := supervisorStateDir(project)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "supervisor.pid")
	me := os.Getpid()
	content := []byte(strconv.Itoa(me) + " " + processStartedAt(me) + "\n")
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			_, werr := f.Write(content)
			if cerr := f.Close(); werr == nil {
				werr = cerr
			}
			return werr
		}
		if !os.IsExist(err) {
			return err
		}
		if SupervisorPID(project) != 0 {
			return errAlreadySupervised
		}
		// The file is there but nobody is behind it: a crash or a reboot left it.
		if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
			return rmErr
		}
	}
	return errAlreadySupervised
}

// errAlreadySupervised means another process holds this project's claim.
var errAlreadySupervised = errors.New("another supervisor is already watching this project")

// ErrAlreadySupervised reports whether err is the "someone else has it" case, so
// a losing racer can exit quietly rather than treat it as a failure.
func ErrAlreadySupervised(err error) bool { return errors.Is(err, errAlreadySupervised) }

// watchedFile records which services a running supervisor is actually watching.
// A supervisor started before the compose file changed would otherwise keep
// enforcing the old policies while `up` announced the new ones — the notice would
// name services nothing is watching, and a policy the user deleted would still be
// in force.
func watchedFile(project string) (string, error) {
	dir, err := supervisorStateDir(project)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "supervised"), nil
}

// RecordWatched notes the set a supervisor has taken on.
func RecordWatched(project string, services []string) error {
	path, err := watchedFile(project)
	if err != nil {
		return err
	}
	sorted := append([]string(nil), services...)
	sort.Strings(sorted)
	return os.WriteFile(path, []byte(strings.Join(sorted, "\n")+"\n"), 0o644)
}

// Watched returns the services a supervisor last recorded itself as watching, or
// nil if there is no record. The file outlives the supervisor that wrote it, so a
// caller has to decide for itself whether those services still mean anything —
// see StillSupervised.
func Watched(project string) []string {
	path, err := watchedFile(project)
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// WatchedMatches reports whether a running supervisor is watching exactly these
// services. A mismatch means the compose file changed since it started.
func WatchedMatches(project string, services []string) bool {
	path, err := watchedFile(project)
	if err != nil {
		return false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	sorted := append([]string(nil), services...)
	sort.Strings(sorted)
	return strings.TrimSpace(string(b)) == strings.Join(sorted, "\n")
}

// ClearWatched forgets what was being watched. `down` calls it: the project is
// being taken apart, so a later `up <service>` must not carry over services from
// the stack that no longer exists. Stopping a supervisor in order to replace it
// deliberately does NOT clear the record — that record is what the replacement
// carries over.
func ClearWatched(project string) {
	if path, err := watchedFile(project); err == nil {
		_ = os.Remove(path)
	}
}

// StillSupervised narrows names to those this project would still supervise and
// whose container is still there. It exists so a partial `up` can carry over the
// services it didn't touch: `up web` says nothing about `db`, which may well be
// running under a `restart:` policy the user is entitled to keep.
//
// Presence, not liveness, is the test. A container stopped by `opossum stop` is
// still supervised — the stop marker, not the watch list, is what keeps it down —
// and one that crashed while nobody was watching is exactly what should be picked
// back up. A container that is simply gone (a `down`, a manual delete) is dropped,
// which is what stops a stale record from resurrecting a dismantled project.
func (o *Orchestrator) StillSupervised(names []string) []string {
	var out []string
	for _, name := range o.SupervisedServices(names) {
		if o.rt.Inspect(o.containerName(name)).Exists {
			out = append(out, name)
		}
	}
	return out
}

// clearPidFile removes the pid file, so a later `up` doesn't see a stale one.
func clearPidFile(project string) {
	if path, err := supervisorPidFile(project); err == nil {
		os.Remove(path)
	}
}

// StopSupervisor asks this project's supervisor to exit and waits briefly for it.
// `down` calls this FIRST: a watcher that sees containers disappearing mid-teardown
// would try to bring them back, and the two would fight.
func StopSupervisor(project string) (stopped bool) {
	pid := SupervisorPID(project)
	if pid == 0 {
		clearPidFile(project)
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		clearPidFile(project)
		return false
	}
	_ = p.Signal(syscall.SIGTERM)
	// Give it a moment to go quietly, then insist.
	for i := 0; i < 30; i++ {
		if !processAlive(pid) {
			clearPidFile(project)
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = p.Signal(syscall.SIGKILL)
	for i := 0; i < 10; i++ {
		if !processAlive(pid) {
			clearPidFile(project)
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Still there after SIGKILL: keep the pid file so the next `down` (or a human)
	// can still find it. Reporting success here would hide an orphan.
	return false
}

// SupervisedServices returns the services whose `restart:` asks to be kept up,
// excluding the ones that are meant to exit. A service another service waits on
// with `service_completed_successfully` runs to completion by design; restarting
// it would turn a finished job into a loop.
func (o *Orchestrator) SupervisedServices(order []string) []string {
	oneShot := map[string]bool{}
	for _, svc := range o.Project.Services {
		for _, dep := range svc.DependsOn {
			if dep.Condition == compose.ConditionCompleted {
				oneShot[dep.Name] = true
			}
		}
	}
	var out []string
	for _, name := range order {
		svc := o.Project.Services[name]
		if svc == nil || oneShot[name] {
			continue
		}
		p, err := svc.RestartPolicy()
		if err != nil || !p.Wants() {
			continue
		}
		out = append(out, name)
	}
	return out
}

// NoticeSupervisorStarted is the one line `up` prints when it leaves a watcher
// behind. It says what is running and how to be rid of it, because a background
// process the user didn't ask for by name should never be a surprise.
func NoticeSupervisorStarted(project string, services []string, logPath string) string {
	return fmt.Sprintf("[%s] watching %s for `restart:` — a small supervisor is now running for this project. "+
		"`opossum down` stops it, `opossum ps` shows it, and it logs to %s. "+
		"Start with --no-supervisor (or OPOSSUM_NO_SUPERVISOR=1) to skip it.",
		codeSupervisorStarted, strings.Join(services, ", "), logPath)
}

// StartSupervisor launches the watcher for this project in the background and
// records its pid. It is a no-op when one is already running — a second `up` in
// the same project must not leave two watchers racing to restart the same
// container.
//
// The watcher is this same binary re-invoked with a hidden subcommand: one
// binary to ship, and it already knows how to read the compose file.
func StartSupervisor(project, workdir string, args []string) (int, error) {
	if pid := SupervisorPID(project); pid != 0 {
		return pid, nil // already watching
	}

	// Which binary to re-invoke. OPOSSUM_SELF_BIN overrides it, in the same spirit
	// as OPOSSUM_CONTAINER_BIN: without a seam here nothing can test that a
	// supervisor is actually spawned, because under `go test` os.Executable() is
	// the test binary — which is precisely how this code shipped untested.
	self := os.Getenv("OPOSSUM_SELF_BIN")
	if self == "" {
		var err error
		if self, err = os.Executable(); err != nil {
			return 0, err
		}
	}
	logPath, err := SupervisorLogFile(project)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	defer logFile.Close()

	cmd := exec.Command(self, args...)
	cmd.Dir = workdir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	// Its own session, so it survives the shell that ran `up` (and a Ctrl-C there
	// doesn't take the watcher with it).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	// The child claims the pid file itself; this process doesn't write it.
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
}
