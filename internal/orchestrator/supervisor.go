package orchestrator

import (
	"errors"
	"fmt"
	"io"
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

// projectStateDir is where opossum keeps a project's own files on the host:
// the supervisor's pid and log, and the files it writes for `content:` and
// `environment:` configs. XDG_STATE_HOME is honoured so a test — or a user
// with opinions — can move it.
func projectStateDir(project string) (string, error) {
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

// supervisorStateDir is projectStateDir under its older name, for the
// supervisor's callers.
func supervisorStateDir(project string) (string, error) { return projectStateDir(project) }

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
	pid, unknown := lookSupervisor(project)
	if unknown {
		return 0
	}
	return pid
}

// lookSupervisor reads the project's pid file. pid is the one it names when that is a live process that has the start
// marker the file records; 0 when the file is stale (no file, nothing behind it, a pid that is another process now).
// unknown is set, with the pid, when a live process is behind the file and its start time cannot be read now
// (`ps` failed, even asked again): neither the project's supervisor nor a stale file, and whoever acts on it —
// signals it, removes the file, claims over it — acts on a guess. `down` removed the file of a live supervisor this
// way, and left it running with nothing that could find it (#1755).
func lookSupervisor(project string) (pid int, unknown bool) {
	path, err := supervisorPidFile(project)
	if err != nil {
		return 0, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, started, ok := parsePidFile(string(b))
	if !ok || !processAlive(pid) {
		return 0, false
	}
	// No token means the file can't be tied to a process, and StopSupervisor
	// escalates to SIGKILL — so it is treated as stale rather than acted on.
	if started == "" {
		return 0, false
	}
	if strings.HasPrefix(started, procTokenPrefix) {
		// No tick now (the process has no /proc entry) matches nothing, not a token that is only the prefix.
		ticks := procStartTicks(pid)
		if ticks != "" && procTokenPrefix+ticks == started {
			return pid, false
		}
		return 0, false
	}
	now := psStartedAt(pid)
	if now == "" {
		// `ps` has nothing to say of a process that was alive a moment ago and is gone now: stale, not unknown.
		if !processAlive(pid) {
			return 0, false
		}
		return pid, true
	}
	if now == started {
		return pid, false
	}
	return 0, false
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

// processStartedAt returns a stable per-process token, or "" when it can't be read. Two
// processes with the same pid at different times will not share one. Where /proc has the
// process it is its start time in clock ticks since boot, which does not move; otherwise it is
// the start time as `ps` reports it. `ps` computes that from the boot time plus the start
// tick, so on a machine whose clock is being corrected (WSL2) the same process reads one
// second apart on two calls, and a claim written with one reading did not match the next
// (#1610).
func processStartedAt(pid int) string {
	if ticks := procStartTicks(pid); ticks != "" {
		return procTokenPrefix + ticks
	}
	return psStartedAt(pid)
}

// procTokenPrefix marks a token taken from /proc, so that a pid file written by a version
// that used `ps` (no prefix) is still compared with `ps`.
const procTokenPrefix = "proc-"

// procRoot is where /proc is, behind a seam a test can point elsewhere.
var procRoot = "/proc"

// procStartTicks reads field 22 (starttime) of /proc/<pid>/stat, or "" when there is none.
// The command name (field 2) is in parentheses and may hold spaces and parentheses itself,
// so the fields are counted from the last ")".
func procStartTicks(pid int) string {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return ""
	}
	s := string(b)
	i := strings.LastIndex(s, ")")
	if i < 0 {
		return ""
	}
	rest := strings.Fields(s[i+1:]) // rest[0] is field 3 (state)
	if len(rest) < 20 {
		return ""
	}
	return rest[19]
}

// psStartedAt is the start time as `ps` reports it, or "" when `ps` would not say. A `ps` that fails
// (a fork that did not go through, on a machine that is busy) is asked again, psTries more times, psWait
// apart: what asks is the claim that writes the marker and the look that checks it, and an empty answer
// from either was taken as "no such process" (#1739, #1755).
func psStartedAt(pid int) string {
	for try := 0; ; try++ {
		out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
		if got := strings.Join(strings.Fields(string(out)), "-"); err == nil && got != "" {
			return got
		}
		if try >= psTries {
			return ""
		}
		time.Sleep(psWait)
	}
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

// processAliveFn is processAlive behind a seam a test can replace. No real
// process resists both SIGTERM and SIGKILL — SIGKILL cannot be caught — so the
// only way to exercise StopSupervisor's "waited the whole budget and still
// couldn't tell" branch without an unreproducible race is to make the check
// itself say "still alive" on command.
var processAliveFn = processAlive

// psTries and psWait are how many more times, and how far apart, `ps` is asked for a start time when the first
// answer is empty (`ps` can fail on a machine that is busy). Variables so a test can shorten the wait.
var (
	psTries = 5
	psWait  = 50 * time.Millisecond
)

// ClaimSupervisor is how a supervisor takes ownership of a project: it takes the
// project's claim lock and writes the pid file whole under it, so exactly one process
// can hold it. The CHILD claims,
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
	// The marker is asked for before the lock is taken, not under it: a `ps` that is slow or fails holds
	// every other claim up for as long as the asking goes on (psStartedAt asks again a few times).
	started := processStartedAt(me)
	// A claim with no marker is read as stale (see SupervisorPID): nobody would be behind it, a second
	// claim would succeed over it, and `down` would find no one to stop. It is not made.
	if started == "" {
		return errors.New("cannot read this process's start time (is `ps` available?), so the claim would not tell it from a stale one")
	}
	content := []byte(strconv.Itoa(me) + " " + started + "\n")
	// Every claim goes through one lock, held from the first look at the file to the
	// moment the claim is in place. Without it a racer that found the file just created
	// and not yet written read it as empty — nobody behind it — removed it, and claimed
	// over the top, so two supervisors stood (#1500: 7 to 11 of 400 rounds of 8 claims at
	// once had more than one winner).
	lock, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close() // closing the file releases the lock
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	if pid, unknown := lookSupervisor(project); unknown {
		return fmt.Errorf("cannot tell whether the process %d named by the pid file is this project's supervisor (`ps` could not be read), so the claim does not go over it", pid)
	} else if pid != 0 {
		return errAlreadySupervised
	}
	// Nothing is behind a file that is there now: a crash or a reboot left it. The claim
	// replaces it in one step, written whole into a file of its own first, so that a
	// reader (SupervisorPID takes no lock) never sees a half-written one.
	tmp, err := os.CreateTemp(dir, "supervisor.pid.tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(content)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), 0o644)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		os.Remove(tmp.Name())
	}
	return werr
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
	// Written beside the record and renamed over it, so a reader sees the old set
	// or the new one and never the moment between os.WriteFile's truncate and its
	// write — an empty record, which WatchedMatches reads as "the set changed" and
	// a test read as "watching nothing" (#1496).
	tmp, err := os.CreateTemp(filepath.Dir(path), "supervised.tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(strings.Join(sorted, "\n") + "\n"); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
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
// which is what stops a stale record from resurrecting a dismantled project. One
// the runtime could not be asked about is kept: unreachable is not gone, and
// dropping it would end its supervision for good over a passing outage.
func (o *Orchestrator) StillSupervised(names []string) []string {
	var out []string
	for _, name := range o.SupervisedServices(names) {
		if info := o.rt.Inspect(o.containerName(name)); info.Exists || info.Unknown {
			out = append(out, name)
		}
	}
	return out
}

// clearPidFile removes the pid file, so a later `up` doesn't see a stale one — unless a
// supervisor is behind it now. It goes through the lock a claim does, holds it until the file
// is gone, and looks again under it: `down` found nobody behind the file, or saw the one
// behind it go, some time before, and a supervisor may have claimed in between. Removing
// that file would leave a supervisor `down` could no longer find (#1550). With the clear
// started a little behind the claim, swept across the claim's whole length, the claim was
// gone in 133 of 400 rounds (the number of rounds that fell in the window, not how often it
// happens: with no delay it did not happen in 1200).
func clearPidFile(project string) {
	path, err := supervisorPidFile(project)
	if err != nil {
		return
	}
	// The lock file is made if it is not there, as a claim makes it: a clear that went on
	// without one would cross a claim that made it a moment later. Where it cannot be made
	// (no state dir, or one that cannot be written) nothing is removed: the pid file is not
	// there, or cannot be removed by this user either.
	lock, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	defer lock.Close() // closing the file releases the lock
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return
	}
	// pid is set both for a supervisor that is there and for a live process that could not be checked
	// (lookSupervisor): neither is a file to remove.
	if pid, _ := lookSupervisor(project); pid != 0 {
		return
	}
	os.Remove(path)
}

// stopSupervisorTermWait, stopSupervisorKillWait and stopSupervisorPoll are the
// budgets StopSupervisor waits before giving up on each signal, and how often it
// checks in between. Variables, not constants, so a test can shrink them instead
// of spending real seconds on the "never confirms" path.
var (
	stopSupervisorTermWait = 3 * time.Second
	stopSupervisorKillWait = 1 * time.Second
	stopSupervisorPoll     = 100 * time.Millisecond
)

// waitForDeath polls processAliveFn until pid is gone or timeout runs out.
func waitForDeath(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !processAliveFn(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(stopSupervisorPoll)
	}
}

// StopSupervisor asks this project's supervisor to exit and waits briefly for it.
// `down` calls this FIRST: a watcher that sees containers disappearing mid-teardown
// would try to bring them back, and the two would fight.
//
// The two results answer different questions. attempted says whether there was
// anything to stop at all (false when no supervisor was running — the ordinary,
// silent case). stopped, when attempted is true, says whether the exit was
// actually confirmed within budget. A caller that only reports success on stopped
// and says nothing otherwise treats "confirmed gone" and "asked, but still might
// be running" as the same silence — which is the bug this shape exists to rule
// out (#1401): the difference is exactly what a caller about to remove this
// project's containers and networks needs to know.
func StopSupervisor(project string) (stopped, attempted bool) {
	pid, unknown := lookSupervisor(project)
	if unknown {
		// A live process stands behind the pid file and `ps` will not say whether it is this project's
		// supervisor. Not signalled (it may be another process by now), and the file stays, so that the
		// next `down` can look again: reported as attempted and not confirmed, so that the caller tells
		// the reader to check by hand (#1755), and says, from SupervisorUncheckedPID read before, that
		// nothing was asked of it (#1759).
		attempted = true
		return
	}
	if pid == 0 {
		clearPidFile(project)
		return
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		clearPidFile(project)
		return
	}
	attempted = true
	_ = p.Signal(syscall.SIGTERM)
	// Give it a moment to go quietly, then insist.
	if waitForDeath(pid, stopSupervisorTermWait) {
		clearPidFile(project)
		stopped = true
		return
	}
	_ = p.Signal(syscall.SIGKILL)
	if waitForDeath(pid, stopSupervisorKillWait) {
		clearPidFile(project)
		stopped = true
		return
	}
	// Still there after SIGKILL: keep the pid file so the next `down` (or a human)
	// can still find it. Reporting success here would hide an orphan.
	return
}

// stopSupervisorAndReport stops this project's supervisor and logs what happened, and says whether one is left
// (asked and not confirmed gone, or one that could not be checked: its pid file is still there),
// including the case a bare `if StopSupervisor(...) { … }` would otherwise drop on
// the floor: asked, but not confirmed stopped (#1401). Shared by Down and Destroy,
// which do this identically before touching anything else.
func (o *Orchestrator) stopSupervisorAndReport() (left bool) {
	// A supervisor the command already asked to stop under this very name, and could not confirm
	// gone, is not asked again: its pid file is still there, so a second ask would wait out the whole
	// budget a second time and say the same thing twice (#1406). A name that differs — the file named
	// the project, where the command went by the directory's — is another supervisor's, and is asked.
	if o.supervisorHandled != "" && o.supervisorHandled == o.Project.Name {
		return false
	}
	stop := o.stopSupervisor
	if stop == nil {
		stop = StopSupervisor
	}
	unchecked := SupervisorUncheckedPID(o.Project.Name) // before the stop: it decides whether the supervisor is asked
	if stopped, attempted := stop(o.Project.Name); stopped {
		o.logf("Stopped the restart supervisor\n")
	} else if attempted {
		// "opossum: " matches the prefix cmd/opossum's own two call sites for
		// this same notice already use (the early stop in `down`,
		// and `up`'s replace path) — this one differs only in landing on
		// whatever writer the caller gave this Orchestrator (o.out), since it
		// has no writer of its own dedicated to warnings the way cmd's stderr
		// is (#1415: unifying the stream too would need Orchestrator to carry
		// a second writer, out of scope for a text-only inconsistency).
		o.logf("opossum: %s\n", NoticeSupervisorStopFailedFor(unchecked))
		return true
	}
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

// NoticeSupervisorStopFailed is the one line `down`, `destroy` and `up` (when
// replacing a supervisor) print when StopSupervisor asked but could not confirm
// the exit within its budget — so a caller about to remove this project's
// containers and networks doesn't do it believing every watcher is already gone.
func NoticeSupervisorStopFailed() string {
	return fmt.Sprintf("[%s] asked the restart supervisor to stop, but couldn't confirm it did — "+
		"it may still be watching this project's containers. Run `opossum ps` to check, and stop it "+
		"by hand (`kill`) if it's still there.", codeSupervisorStopFailed)
}

// SupervisorUncheckedPID is the pid of a live process behind this project's pid file that `ps` would not vouch for
// (lookSupervisor's unknown), and 0 when there is none. It is read before a stop is tried, because that is when it decides
// whether the supervisor will be asked at all: StopSupervisor does not signal it (#1759).
func SupervisorUncheckedPID(project string) int {
	if pid, unchecked := lookSupervisor(project); unchecked {
		return pid
	}
	return 0
}

// NoticeSupervisorUnchecked is the line for a supervisor that was not signalled because `ps` would not say whether the
// process behind the pid file is this project's (#1759): it says what happened (nothing was asked of it) and how to look.
func NoticeSupervisorUnchecked(pid int) string {
	return fmt.Sprintf("[%s] couldn't check whether process %d is this project's restart supervisor (`ps` did not answer), "+
		"so it was not asked to stop — it may still be watching this project's containers. Run `ps -p %d` to check, "+
		"and stop it by hand (`kill`) if it is the supervisor.", codeSupervisorStopFailed, pid, pid)
}

// NoticeSupervisorStopFailedFor is the notice for a stop that was attempted and not confirmed, worded for what happened to
// the supervisor: unchecked is the pid SupervisorUncheckedPID gave before the stop (nonzero: it was never asked, because it
// could not be checked), 0 when it was asked and is not confirmed gone.
func NoticeSupervisorStopFailedFor(unchecked int) string {
	if unchecked != 0 {
		return NoticeSupervisorUnchecked(unchecked)
	}
	return NoticeSupervisorStopFailed()
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
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	pid := cmd.Process.Pid
	return pid, awaitSupervisorClaim(project, pid, exited, logPath)
}

// supervisorStartWait is how long `up` waits for a watcher it started to take its claim, or to be gone.
// A variable so a test can shorten it.
var supervisorStartWait = 1500 * time.Millisecond

// awaitSupervisorClaim waits until the watcher this process started either holds the project's claim
// (it is going to stay), exits cleanly (another watcher holds the project: nothing is wrong), or exits with
// an error — a watcher that dies at once, as one does when it cannot read its own start time, was announced as
// running and watching nothing (#1739). The error carries the last line of the log the watcher wrote on the way
// out. A watcher still starting when the wait is over is taken as started, as it was before.
func awaitSupervisorClaim(project string, pid int, exited <-chan error, logPath string) error {
	deadline := time.Now().Add(supervisorStartWait)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			if err == nil {
				return nil
			}
			if last := lastLogLine(logPath); last != "" {
				return fmt.Errorf("it exited at once (%v): %s", err, last)
			}
			return fmt.Errorf("it exited at once (%v); see %s", err, logPath)
		case <-time.After(20 * time.Millisecond):
			if SupervisorPID(project) == pid {
				return nil
			}
		}
	}
	return nil
}

// lastLogLine is the last non-empty line of the file's last 4 KiB, or "".
func lastLogLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	size := st.Size()
	start := size - 4096
	if start < 0 {
		start = 0
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// supervisorLine is the line `ps` prints about the project's restart supervisor, or "" when there is none:
// running, or — a live process behind the pid file whose start time `ps` would not give — not checked. Said so, where
// it used to be left out as if there were no supervisor, which `down` and `destroy` read the same way (#1755).
func supervisorLine(project string) string {
	pid, unknown := lookSupervisor(project)
	if pid == 0 {
		return ""
	}
	if unknown {
		return fmt.Sprintf("restart supervisor: could not be checked (pid %d: `ps` would not say whether it is this project's) — look with `ps -p %d`", pid, pid)
	}
	msg := fmt.Sprintf("restart supervisor: running (pid %d)", pid)
	if log, err := SupervisorLogFile(project); err == nil {
		msg += " — " + log
	}
	return msg
}
