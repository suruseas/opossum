package orchestrator

// Diagnostic codes. opossum stamps each warning and recovery-relevant error with
// a stable `[OPSM-NNN]` code so an agent (or a human) can map it straight to a fix
// via AGENTS.md's "Diagnostic codes" / "Failure signatures" tables — no need to
// re-parse the prose. Codes are **add-only**: never renumber or reuse one, since
// their whole value is stability. Grouped loosely by area (1xx storage, 2xx
// network, 3xx build, 4xx lifecycle, 5xx compose). Keep allDiagCodes and AGENTS.md
// in sync — a test enforces it.
type diagCode string

const (
	codePGDATADatadir   diagCode = "OPSM-101" // named volume mounted directly at Postgres's data dir
	codeSharedVolume    diagCode = "OPSM-102" // a named volume shared by two running services
	codeHostPortInUse   diagCode = "OPSM-201" // a published host port is already taken (pre-flight)
	codeDNSDomainAbsent diagCode = "OPSM-202" // the DNS domain isn't registered (no bare-name discovery)
	codeInternalEgress  diagCode = "OPSM-203" // an internal network: no internet egress / no name resolution
	codeDockerSocket    diagCode = "OPSM-204" // a service mounts docker.sock (Apple container has none)
	codeBuildTmpContext diagCode = "OPSM-301" // build context under /private/tmp (builder can't read it)
	codeBuildSymlink    diagCode = "OPSM-302" // build context is a symlink (builder may reject it)
	codeDepNotRunning   diagCode = "OPSM-401" // a dependency's container exited before becoming healthy
	codeOrphans         diagCode = "OPSM-402" // containers left by services no longer in the compose
	codeDepNoHealth     diagCode = "OPSM-403" // a service_healthy dependency defines no healthcheck
	codeIgnoredTopField diagCode = "OPSM-501" // unsupported top-level compose field(s), ignored
	codeIgnoredField    diagCode = "OPSM-502" // unsupported service compose field(s), ignored
	codeWatchRebuild    diagCode = "OPSM-601" // a `watch` rebuild action failed
	codeWatchRestart    diagCode = "OPSM-602" // a `watch` restart action failed
	codeWatchSync       diagCode = "OPSM-603" // a `watch` file sync failed
	codeWatchSetup      diagCode = "OPSM-604" // `watch` couldn't start watching a path
	codeWatchError      diagCode = "OPSM-605" // the `watch` file watcher reported an error
)

// allDiagCodes lists every code opossum can emit. A test asserts each appears in
// AGENTS.md, so adding a code forces documenting it.
var allDiagCodes = []diagCode{
	codePGDATADatadir, codeSharedVolume,
	codeHostPortInUse, codeDNSDomainAbsent, codeInternalEgress, codeDockerSocket,
	codeBuildTmpContext, codeBuildSymlink,
	codeDepNotRunning, codeOrphans, codeDepNoHealth,
	codeIgnoredTopField, codeIgnoredField,
	codeWatchRebuild, codeWatchRestart, codeWatchSync, codeWatchSetup, codeWatchError,
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
