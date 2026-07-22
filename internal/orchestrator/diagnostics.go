package orchestrator

import "fmt"

// Error-message guideline (applies to EVERY user-facing error and warning opossum
// emits, not just coded ones): each should carry three things —
//  1. what happened (the fact; never dump a raw lower-level error alone),
//  2. why (the cause; if it's an Apple `container` constraint, state it in a line),
//  3. what to do next (a concrete, runnable command or config change).
//
// The good exemplars to copy: warnPostgresDatadir (OPSM-101), buildFailed (#170),
// the embedded-logs dependency failure (OPSM-401), and every `doctor` check. When
// adding an error, prefer naming the fix inline over leaving the user to guess.
//
// Diagnostic codes. opossum stamps each warning and recovery-relevant error with
// a stable `[OPSM-NNN]` code so an agent (or a human) can map it straight to a fix
// via AGENTS.md's "Diagnostic codes" / "Failure signatures" tables — no need to
// re-parse the prose. Codes are **add-only**: never renumber or reuse one, since
// their whole value is stability. Grouped loosely by area (1xx storage, 2xx
// network, 3xx build, 4xx lifecycle, 5xx compose). Keep allDiagCodes and AGENTS.md
// in sync — a test enforces it.
type diagCode string

const (
	codePGDATADatadir    diagCode = "OPSM-101" // named volume mounted directly at Postgres's data dir
	codeSharedVolume     diagCode = "OPSM-102" // a named volume shared by two running services
	codeVolumeAttachBusy diagCode = "OPSM-103" // a named volume is already attached to another running container
	codeBindDirCreate    diagCode = "OPSM-104" // couldn't create a bind mount's host source directory
	codeHostPortInUse    diagCode = "OPSM-201" // a published host port is already taken (pre-flight)
	codeDNSDomainAbsent  diagCode = "OPSM-202" // the DNS domain isn't registered (no bare-name discovery)
	codeInternalEgress   diagCode = "OPSM-203" // an internal network: no internet egress / no name resolution
	codeDockerSocket     diagCode = "OPSM-204" // a service mounts docker.sock (Apple container has none)
	codeBuildTmpContext  diagCode = "OPSM-301" // build context under /private/tmp (builder can't read it)
	codeBuildSymlink     diagCode = "OPSM-302" // build context is a symlink (builder may reject it)
	codeDepNotRunning    diagCode = "OPSM-401" // a dependency's container exited before becoming healthy
	codeOrphans          diagCode = "OPSM-402" // containers left by services no longer in the compose
	codeDepNoHealth      diagCode = "OPSM-403" // a service_healthy dependency defines no healthcheck
	codeRuntimeAbsent    diagCode = "OPSM-404" // the `container` CLI isn't installed / not on PATH
	codeRuntimeStopped   diagCode = "OPSM-405" // the `container` system (daemon) is installed but not running
	codeRuntimeAutoStart diagCode = "OPSM-406" // the runtime wasn't running; opossum is starting it
	codeIgnoredTopField  diagCode = "OPSM-501" // unsupported top-level compose field(s), ignored
	codeIgnoredField     diagCode = "OPSM-502" // unsupported service compose field(s), ignored
	codeWatchRebuild     diagCode = "OPSM-601" // a `watch` rebuild action failed
	codeWatchRestart     diagCode = "OPSM-602" // a `watch` restart action failed
	codeWatchSync        diagCode = "OPSM-603" // a `watch` file sync failed
	codeWatchSetup       diagCode = "OPSM-604" // `watch` couldn't start watching a path
	codeWatchError       diagCode = "OPSM-605" // the `watch` file watcher reported an error
)

// allDiagCodes lists every code opossum can emit. A test asserts each appears in
// AGENTS.md, so adding a code forces documenting it.
var allDiagCodes = []diagCode{
	codePGDATADatadir, codeSharedVolume, codeVolumeAttachBusy, codeBindDirCreate,
	codeHostPortInUse, codeDNSDomainAbsent, codeInternalEgress, codeDockerSocket,
	codeBuildTmpContext, codeBuildSymlink,
	codeDepNotRunning, codeOrphans, codeDepNoHealth,
	codeIgnoredTopField, codeIgnoredField, codeRuntimeAbsent, codeRuntimeStopped, codeRuntimeAutoStart,
	codeWatchRebuild, codeWatchRestart, codeWatchSync, codeWatchSetup, codeWatchError,
}

// ErrRuntimeAbsent is the unified error every runtime-touching command returns
// when Apple's `container` CLI isn't installed / not on PATH. Returning it (and a
// non-zero exit) instead of an empty `ps` table or a raw exec error means an
// agent reads a clear, actionable signal rather than "nothing is running".
func ErrRuntimeAbsent() error {
	return fmt.Errorf("[%s] the `container` CLI was not found on PATH — install it first: "+
		"`brew install container` (or the .pkg from github.com/apple/container/releases), "+
		"then run `container system start`", codeRuntimeAbsent)
}

// runtimeWhy explains, in one breath, why the runtime needs starting — so the
// error teaches the cause, not just the command (an agent that understands "the
// service doesn't start on demand" won't loop). Shared by the stopped error and
// the auto-start notice.
const runtimeWhy = "opossum drives Apple's `container` CLI, which manages the VM through a " +
	"background service (apiserver) that doesn't start on demand, so it needs starting after a " +
	"reboot or a `container system stop`."

// ErrRuntimeStopped is the unified error a runtime-touching command returns when
// the `container` CLI is installed but its system (daemon) isn't running. It's
// the sibling of ErrRuntimeAbsent for a subtler failure: without it, a read-only
// command like `ps` would render an empty table — indistinguishable from "nothing
// is running" — when in truth it just couldn't reach the daemon.
func ErrRuntimeStopped() error {
	return fmt.Errorf("[%s] the `container` system isn't running. %s Start it with "+
		"`container system start` (or run `opossum doctor`); opossum starts it for you on "+
		"a mutating command unless OPOSSUM_NO_AUTO_START is set", codeRuntimeStopped, runtimeWhy)
}

// NoticeRuntimeAutoStart is the one-line notice printed before opossum starts a
// stopped runtime for a mutating command.
func NoticeRuntimeAutoStart() string {
	return fmt.Sprintf("[%s] the container runtime isn't running — starting it now "+
		"(`container system start`). %s", codeRuntimeAutoStart, runtimeWhy)
}

// ErrRuntimeAutoStartFailed is returned when opossum tried to auto-start a stopped
// runtime for a mutating command and the start itself failed — so it says opossum
// tried and couldn't, not (as ErrRuntimeStopped would) that it will.
func ErrRuntimeAutoStartFailed(cause error) error {
	return fmt.Errorf("[%s] the `container` system isn't running and opossum couldn't start it "+
		"(`container system start` failed: %v). %s Try starting it yourself, or run `opossum doctor`",
		codeRuntimeStopped, cause, runtimeWhy)
}

// warnf prints a coded warning: `warning: [OPSM-NNN] <message>`. Every warning the
// orchestrator emits (during plan/up/watch) goes through here — a test forbids a
// bare `"warning:"` string literal anywhere else in the package, so no warning
// ships without a code. (One best-effort teardown notice in the runtime layer,
// `container network delete`, is intentionally uncoded — it isn't a diagnostic a
// caller recovers from.)
//
// Note: warnf builds its format by concatenation, which stops `go vet` from
// checking printf args at call sites. The 16 call sites are content-tested, so a
// format mistake still surfaces; a new call site should assert its output.
func (o *Orchestrator) warnf(code diagCode, format string, a ...interface{}) {
	o.logf("warning: [%s] "+format, append([]interface{}{code}, a...)...)
}
