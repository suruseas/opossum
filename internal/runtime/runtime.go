// Package runtime is a thin wrapper around Apple's `container` CLI. opossum
// never re-implements the runtime; it only shells out and parses results.
package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runtime invokes the `container` binary.
type Runtime struct {
	Bin string
	// Verbose turns on extra diagnostics for bug reports: it echoes each
	// `container` invocation before running it (to Trace, or os.Stderr when Trace
	// is nil), and callers also use it to surface otherwise-hidden notices (e.g.
	// ignored compose fields).
	Verbose bool
	Trace   io.Writer
	// Ctx is the cancellation scope for child processes: when it's cancelled
	// (e.g. Ctrl-C during `up`), an in-flight build/run/exec is killed so the
	// caller can roll back promptly. nil means no cancellation (background).
	Ctx context.Context
	// DockerBin is the docker CLI used only by ImportFromDocker (empty = "docker").
	// It's a seam for tests; the normal path never shells out to docker.
	DockerBin string
	// Out redirects a streamed child's stdout (nil = os.Stdout). `run` sets it to
	// stderr while starting dependencies and building, so the one-off's own stdout
	// (e.g. an MCP server's JSON-RPC over stdio) stays clean.
	Out io.Writer
	// Env holds extra environment entries ("KEY=value") passed to every child
	// process, on top of the parent's environment. It's a test seam: the fake
	// shim is steered per-Runtime through Env instead of the process environment,
	// so a test needs no t.Setenv and its shim behaviour stays isolated from others.
	Env []string
	// DryRun makes the runtime plan instead of act: a mutating invocation (run,
	// build, create, delete, stop, …) is recorded to Plan and NOT executed, while
	// read-only queries (inspect, ls, image inspect, …) still run so a caller can
	// compute the plan from real runtime state. Set by `up --dry-run`.
	DryRun bool
	// Plan accumulates the argv of every mutating invocation suppressed under
	// DryRun (each an "container"-relative arg list, space-joined), in the order
	// they would have run — the "commands that would run" a dry-run prints.
	Plan []string
}

// mutating reports whether a `container` invocation changes runtime (or host)
// state, so a dry-run records it instead of executing. Only read-only queries
// (inspect, ls, logs, stats, exec, image inspect, volume ls, system dns list)
// return false — a dry-run still runs those to resolve its plan.
func mutating(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "run", "build", "stop", "start", "kill", "delete", "cp":
		return true
	case "network":
		return len(args) > 1 && (args[1] == "create" || args[1] == "delete")
	case "volume":
		return len(args) > 1 && args[1] == "delete"
	case "image":
		return len(args) > 1 && (args[1] == "delete" || args[1] == "pull" ||
			args[1] == "tag" || args[1] == "load" || args[1] == "import")
	}
	return false
}

// recordIfDryRun returns true (and appends the invocation to Plan) when a dry-run
// should suppress this mutating command; the exec helpers then skip spawning the
// child and return a benign success.
func (r *Runtime) recordIfDryRun(args []string) bool {
	if !r.DryRun || !mutating(args) {
		return false
	}
	r.Plan = append(r.Plan, strings.Join(args, " "))
	return true
}

// newCmd builds the exec.Cmd for a child `container` invocation, injecting r.Env
// on top of the inherited environment when set. Centralizing this keeps every
// exec site consistent and lets tests steer the fake shim without touching the
// process environment.
func (r *Runtime) newCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, r.Bin, args...)
	if len(r.Env) > 0 {
		cmd.Env = append(os.Environ(), r.Env...)
	}
	return cmd
}

// stdoutW is where a streamed child's stdout goes (Out when set, else os.Stdout).
func (r *Runtime) stdoutW() io.Writer {
	if r.Out != nil {
		return r.Out
	}
	return os.Stdout
}

// dockerBin returns the docker CLI to invoke for image import.
func (r *Runtime) dockerBin() string {
	if r.DockerBin != "" {
		return r.DockerBin
	}
	return "docker"
}

// baseCtx is the parent context for child processes — Ctx when set, else a
// never-cancelled background context.
func (r *Runtime) baseCtx() context.Context {
	if r.Ctx != nil {
		return r.Ctx
	}
	return context.Background()
}

// trace echoes the about-to-run command when Verbose is set. Args containing
// spaces or newlines (e.g. a multi-line env value) are quoted so each invocation
// stays on a single line.
func (r *Runtime) trace(args []string) {
	if !r.Verbose {
		return
	}
	w := r.Trace
	if w == nil {
		w = os.Stderr
	}
	parts := make([]string, len(args))
	for i, a := range args {
		if needsQuote(a) {
			parts[i] = strconv.Quote(a)
		} else {
			parts[i] = a
		}
	}
	fmt.Fprintf(w, "+ %s %s\n", r.Bin, strings.Join(parts, " "))
}

// needsQuote reports whether an argument must be quoted to keep the verbose trace
// on one readable line: empty, or containing whitespace/quotes/backslash or any
// control character (newlines, tabs, ESC, etc.).
func needsQuote(a string) bool {
	if a == "" {
		return true
	}
	for _, r := range a {
		if r < 0x20 || r == ' ' || r == '"' || r == '\'' || r == '\\' {
			return true
		}
	}
	return false
}

// New returns a Runtime. The binary can be overridden with OPOSSUM_CONTAINER_BIN
// (useful for tests or a fake shim).
func New() *Runtime {
	bin := os.Getenv("OPOSSUM_CONTAINER_BIN")
	if bin == "" {
		bin = "container"
	}
	return &Runtime{Bin: bin}
}

// Available reports whether the container binary can be found.
func (r *Runtime) Available() bool {
	_, err := exec.LookPath(r.Bin)
	return err == nil
}

// SystemRunning reports whether the container system (daemon) is actually up —
// not merely installed. Available only checks the CLI is on PATH; the system can
// be present but stopped, in which case every real call fails. A read-only
// command that silently returns "nothing" when the system is down (an empty `ps`
// table, `PRESENT=no`) would be lying, so those commands probe with this first.
// `container system status` is the canonical liveness signal — doctor uses the
// same one — printing a `status running` line when the daemon is up.
func (r *Runtime) SystemRunning() bool {
	out, err := r.capture("system", "status")
	if err != nil {
		return false
	}
	// Mirror doctor's parse: a `status running` field means the daemon is up.
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "status" && strings.EqualFold(f[1], "running") {
			return true
		}
	}
	return false
}

// stream runs a command with stdio attached to the parent process.
func (r *Runtime) stream(args ...string) error {
	if r.recordIfDryRun(args) {
		return nil
	}
	r.trace(args)
	cmd := r.newCmd(r.baseCtx(), args...)
	cmd.WaitDelay = 2 * time.Second // don't hang on a lingering child after cancel
	cmd.Stdout = r.stdoutW()
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// streamHeartbeat is like stream but shows a "still working" spinner during long
// silent stretches. It's used only for build, whose output goes quiet during
// context transfer and base-image pull — and which already renders with
// --progress plain, so wrapping its stdout (which drops the child out of TTY
// mode) costs no terminal features. Interactive/TTY-sensitive streamed commands
// (exec, stats) keep their real terminal fds and get no spinner. The spinner is
// a no-op unless stderr is a terminal, so piped/redirected output is unchanged.
func (r *Runtime) streamHeartbeat(label string, tee io.Writer, args ...string) error {
	if r.recordIfDryRun(args) {
		return nil
	}
	r.trace(args)
	cmd := r.newCmd(r.baseCtx(), args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = os.Stdin
	out, errw := r.stdoutW(), io.Writer(os.Stderr)
	if tee != nil {
		// Feed a copy of the output to tee (a build-error detector) without
		// altering what the user sees.
		out = io.MultiWriter(out, tee)
		errw = io.MultiWriter(os.Stderr, tee)
	}
	hb := newHeartbeat(os.Stderr, defaultHeartbeatIdle, label)
	cmd.Stdout = hb.wrap(out)
	cmd.Stderr = hb.wrap(errw)
	hb.run()
	defer hb.close()
	return cmd.Run()
}

// capture runs a command and returns combined stdout+stderr.
func (r *Runtime) capture(args ...string) (string, error) {
	if r.recordIfDryRun(args) {
		return "", nil
	}
	r.trace(args)
	cmd := r.newCmd(r.baseCtx(), args...)
	cmd.WaitDelay = 2 * time.Second // don't hang on a lingering child after cancel
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// captureStdout runs a command and returns stdout only (stderr goes to the
// process's stderr). Used when the output is parsed (e.g. JSON), so a warning the
// child writes to stderr on an otherwise-successful run can't corrupt the parse.
func (r *Runtime) captureStdout(args ...string) (string, error) {
	if r.recordIfDryRun(args) {
		return "", nil
	}
	r.trace(args)
	cmd := r.newCmd(r.baseCtx(), args...)
	cmd.WaitDelay = 2 * time.Second
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	return buf.String(), err
}

// EnsureNetwork creates the network if it does not already exist. It reports
// whether it actually created it (false = it was already there), so callers can
// roll back only a network they themselves created. An internal network is
// created host-only (`--network create --internal`): no internet egress, though
// the host stays reachable — the enforcement point for allowlist egress.
func (r *Runtime) EnsureNetwork(name string, internal bool) (created bool, err error) {
	args := []string{"network", "create"}
	if internal {
		args = append(args, "--internal")
	}
	args = append(args, name)
	out, cerr := r.capture(args...)
	if cerr == nil {
		return true, nil
	}
	if strings.Contains(strings.ToLower(out), "exist") {
		return false, nil // already there — fine
	}
	return false, fmt.Errorf("creating network %q: %w\n%s", name, cerr, strings.TrimSpace(out))
}

// DeleteNetwork removes a network as part of best-effort teardown. It stays
// silent when the runtime reports the network as already gone and only warns on
// other, unexpected failures. The real CLI reports a missing network as
// "failed to delete one or more networks: [\"name\"]" (not "not found"), so a
// naive "not found" check warned spuriously on a clean re-`down` — see
// networkAlreadyGone.
func (r *Runtime) DeleteNetwork(name string) {
	out, err := r.capture("network", "delete", name)
	if err != nil && !networkAlreadyGone(out) {
		fmt.Fprintf(os.Stderr, "warning: could not delete network %q: %s\n", name, strings.TrimSpace(out))
	}
}

// networkAlreadyGone reports whether a `network delete` failure just means the
// network was already absent. Apple's `container` uses two shapes for this:
// "... not found" and "failed to delete one or more networks: [...]". (The
// latter is generic and can, in rare cases, also mean still-in-use; teardown is
// best-effort, so we accept that trade-off rather than warn on every clean
// re-run.)
func networkAlreadyGone(out string) bool {
	lo := strings.ToLower(out)
	return strings.Contains(lo, "not found") ||
		strings.Contains(lo, "failed to delete one or more networks")
}

// DeleteVolume removes a named volume, best-effort (used by `down --volumes`).
func (r *Runtime) DeleteVolume(name string) {
	r.capture("volume", "delete", name)
}

// ImageExists reports whether an image reference is present locally.
func (r *Runtime) ImageExists(ref string) bool {
	_, err := r.capture("image", "inspect", ref)
	return err == nil
}

// DeleteImage removes an image, best-effort (--force ignores a missing image),
// for `down --rmi`.
func (r *Runtime) DeleteImage(ref string) {
	r.capture("image", "delete", "--force", ref)
}

// Copy copies files between a container and the host via `container cp`. src and
// dst are each a host path or `<container>:<path>`.
func (r *Runtime) Copy(src, dst string) error {
	return r.stream("cp", src, dst)
}

// VolumeExists reports whether a named volume already exists. It gates seeding
// (which copies image contents into a volume), so on a query error it fails
// SAFE — reporting "exists" — rather than risk re-seeding a volume that's really
// there and overwriting its contents.
func (r *Runtime) VolumeExists(name string) bool {
	out, err := r.capture("volume", "ls")
	if err != nil {
		return true // can't tell → assume it exists, so we don't re-seed
	}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == name {
			return true
		}
	}
	return false
}

// SeedVolume copies the image's contents at srcPath into volume, by running a
// throwaway container (which also creates the volume). Best-effort: a missing
// path, missing shell, or copy error leaves the volume empty. This mirrors
// Docker seeding a fresh volume from the image at that path — Apple `container`
// mounts a fresh volume empty, which breaks the common "bind source + a volume
// to preserve the image's node_modules" dev pattern.
func (r *Runtime) SeedVolume(volume, image, srcPath string) {
	const dst = "/__opossum_seed__"
	script := fmt.Sprintf("[ -d %q ] && cp -a %q/. %q/ 2>/dev/null || true", srcPath, srcPath, dst)
	r.capture("run", "--rm", "-v", volume+":"+dst, image, "sh", "-c", script)
}

// Output runs a container subcommand and returns its combined output, for
// diagnostics (`doctor`) that interpret the CLI's output.
func (r *Runtime) Output(args ...string) (string, error) {
	return r.capture(args...)
}

// DNSDomainExists reports whether a local DNS domain has been created (via
// `sudo container system dns create <domain>`).
func (r *Runtime) DNSDomainExists(domain string) bool {
	out, err := r.capture("system", "dns", "list")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == domain {
			return true
		}
	}
	return false
}

// BuildOptions describes a `container build` invocation.
type BuildOptions struct {
	Tag        string
	Context    string
	Dockerfile string
	Args       []string
	Target     string // --target: multi-stage build stage
}

// Build builds an image. It always requests `--progress plain` so build output is
// steady per-line logging: a long step (e.g. transferring a large context) keeps
// advancing on screen instead of sitting on an in-place line that looks stuck.
func (r *Runtime) Build(o BuildOptions) error {
	args := []string{"build", "--progress", "plain", "-t", o.Tag}
	if o.Dockerfile != "" {
		args = append(args, "-f", o.Dockerfile)
	}
	if o.Target != "" {
		args = append(args, "--target", o.Target)
	}
	for _, a := range o.Args {
		args = append(args, "--build-arg", a)
	}
	ctx := o.Context
	if ctx == "" {
		ctx = "."
	}
	args = append(args, ctx)
	// Tee the output through a detector so an opaque builder failure (corrupted
	// cache, resource exhaustion) becomes an actionable hint.
	det := &buildErrorDetector{}
	err := r.streamHeartbeat("building", det, args...)
	if err != nil {
		if h := det.hint(); h != "" {
			return fmt.Errorf("%w\n%s", err, h)
		}
	}
	return err
}

// RunOptions describes a `container run` invocation.
type RunOptions struct {
	Name       string
	Image      string
	Platform   string   // --platform (e.g. linux/amd64); amd64 also enables --rosetta
	Networks   []string // one --network per entry (a service may join several); "none" for full isolation
	DNSDomain  string   // --dns-domain (the registered local domain)
	DNSSearch  string   // --dns-search (per-project subdomain for bare-name resolution)
	Env        []string
	Ports      []string
	Volumes    []string
	Tmpfs      []string // --tmpfs mount targets
	Command    []string
	Entrypoint []string // overrides the image ENTRYPOINT (--entrypoint + positional args)
	Labels     []string // key=value labels (-l)
	Memory     string   // -m memory limit (e.g. "512M")
	CPUs       string   // -c CPU count (integer)
	Detach     bool
	// Interactive (-i) keeps the container's stdin connected to ours. Foreground
	// one-off runs set it so piped input reaches the process — without it the
	// child sees an immediate EOF, which breaks stdin-driven tools (e.g. an MCP
	// server speaking JSON-RPC over stdio).
	Interactive bool
	TTY         bool // -t: allocate a pseudo-terminal (only when our stdin is one)
	// SSH forwards the host's SSH agent socket into the container (--ssh), so a
	// service can clone/push private git over SSH using the host's keys without
	// baking them into the image.
	SSH bool
	// Thin passthroughs of common compose run options to the matching
	// `container run` flags.
	User       string   // --user (name|uid[:gid])
	WorkingDir string   // --workdir
	Init       bool     // --init (reap zombies)
	ReadOnly   bool     // --read-only root filesystem
	CapAdd     []string // --cap-add
	CapDrop    []string // --cap-drop
}

// Run starts a container.
func (r *Runtime) Run(o RunOptions) error {
	args := []string{"run"}
	if o.Detach {
		args = append(args, "-d")
	}
	if o.Interactive {
		args = append(args, "-i")
	}
	if o.TTY {
		args = append(args, "-t")
	}
	if o.SSH {
		// Forward the host SSH agent so private git over SSH works in the container.
		args = append(args, "--ssh")
	}
	if o.Init {
		args = append(args, "--init")
	}
	if o.ReadOnly {
		args = append(args, "--read-only")
	}
	if o.User != "" {
		args = append(args, "--user", o.User)
	}
	if o.WorkingDir != "" {
		args = append(args, "--workdir", o.WorkingDir)
	}
	for _, c := range o.CapAdd {
		args = append(args, "--cap-add", c)
	}
	for _, c := range o.CapDrop {
		args = append(args, "--cap-drop", c)
	}
	if o.Name != "" {
		args = append(args, "--name", o.Name)
	}
	if o.Platform != "" {
		// Run the image for a specific platform. amd64 on Apple silicon needs
		// Rosetta to emulate x86-64 (the runtime is otherwise arm64-only).
		args = append(args, "--platform", o.Platform)
		if p := strings.ToLower(o.Platform); strings.Contains(p, "amd64") || strings.Contains(p, "x86_64") {
			args = append(args, "--rosetta")
		}
	}
	if o.Memory != "" {
		args = append(args, "-m", o.Memory)
	}
	if o.CPUs != "" {
		args = append(args, "-c", o.CPUs)
	}
	for _, n := range o.Networks {
		args = append(args, "--network", n)
	}
	if o.DNSDomain != "" {
		// Register the container under the local (registered) DNS domain.
		args = append(args, "--dns-domain", o.DNSDomain)
	}
	if o.DNSSearch != "" {
		// Add the per-project subdomain to the search list so peers resolve each
		// other by bare service name within the project.
		args = append(args, "--dns-search", o.DNSSearch)
	}
	for _, e := range o.Env {
		args = append(args, "-e", e)
	}
	for _, p := range o.Ports {
		args = append(args, "-p", p)
	}
	for _, v := range o.Volumes {
		args = append(args, "-v", v)
	}
	for _, t := range o.Tmpfs {
		args = append(args, "--tmpfs", t)
	}
	for _, l := range o.Labels {
		args = append(args, "-l", l)
	}
	// `container run --entrypoint` takes only the executable, so entrypoint args
	// past the first go positional (before the command) — the container then runs
	// entrypoint ++ command.
	if len(o.Entrypoint) > 0 {
		args = append(args, "--entrypoint", o.Entrypoint[0])
	}
	args = append(args, o.Image)
	if len(o.Entrypoint) > 1 {
		args = append(args, o.Entrypoint[1:]...)
	}
	args = append(args, o.Command...)
	return r.stream(args...)
}

// Exec runs a command inside a running container and returns an error if it
// exits non-zero. Output is captured (not streamed) so health probes stay quiet.
// A positive timeout bounds the call so a hung probe can't block `up` forever; a
// non-positive timeout means no limit.
func (r *Runtime) Exec(name string, args []string, timeout time.Duration) error {
	full := append([]string{"exec", name}, args...)
	r.trace(full)
	ctx := r.baseCtx()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := r.newCmd(ctx, full...)
	// After the deadline kills the process, don't wait indefinitely for a lingering
	// child to release the output pipes — force them closed so Run() actually returns.
	cmd.WaitDelay = timeout
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("probe did not return within %s", timeout)
	}
	return err
}

// ExecOptions configures an interactive-capable exec.
type ExecOptions struct {
	Interactive bool // -i: keep stdin open
	TTY         bool // -t: allocate a TTY
}

// ExecStream runs a command in a running container with the parent's stdio
// attached — for `opossum exec`, where the user sees output and can interact
// (unlike Exec, which captures for silent health probes).
func (r *Runtime) ExecStream(name string, command []string, o ExecOptions) error {
	args := []string{"exec"}
	if o.Interactive {
		args = append(args, "-i")
	}
	if o.TTY {
		args = append(args, "-t")
	}
	args = append(args, name)
	args = append(args, command...)
	return r.stream(args...)
}

// LogsOptions describes a `container logs` invocation.
type LogsOptions struct {
	Follow bool // stream new output as it arrives (-f)
	Tail   int  // show only the last N lines (-n); <= 0 means all
}

// Logs streams a container's logs to the parent's stdout/stderr.
func (r *Runtime) Logs(name string, o LogsOptions) error {
	args := []string{"logs"}
	if o.Follow {
		args = append(args, "-f")
	}
	if o.Tail > 0 {
		args = append(args, "-n", strconv.Itoa(o.Tail))
	}
	args = append(args, name)
	return r.stream(args...)
}

// CaptureLogs returns the last `tail` lines of a container's logs, captured (not
// streamed). Best-effort: it returns "" on any error. Used to show why a
// container died directly in an error message, before teardown removes it.
func (r *Runtime) CaptureLogs(name string, tail int) string {
	args := []string{"logs"}
	if tail > 0 {
		args = append(args, "-n", strconv.Itoa(tail))
	}
	args = append(args, name)
	out, err := r.capture(args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (r *Runtime) logsArgs(name string, o LogsOptions) []string {
	args := []string{"logs"}
	if o.Follow {
		args = append(args, "-f")
	}
	if o.Tail > 0 {
		args = append(args, "-n", strconv.Itoa(o.Tail))
	}
	return append(args, name)
}

// FollowLogs streams a container's logs, writing each line to w prefixed with
// prefix, until the stream ends or ctx is cancelled. Used to multiplex several
// services' logs onto one output; w must be safe for concurrent Write. A whole
// line is written in one call so concurrent streams don't interleave mid-line.
// A real failure (not a Ctrl-C cancel) is surfaced as a prefixed diagnostic line
// and returned, so a failing stream isn't silent.
func (r *Runtime) FollowLogs(ctx context.Context, name string, o LogsOptions, w io.Writer, prefix string) error {
	args := r.logsArgs(name, o)
	r.trace(args)
	cmd := r.newCmd(ctx, args...)
	// A real OS pipe (not an io.Writer) so the child writes directly and the reader
	// sees EOF when it exits — stdout and stderr share it (logs may use either).
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return err
	}
	pw.Close() // parent's copy; the child holds the write end, so pr EOFs on exit

	// Unblock a stuck read on cancel, in case a lingering grandchild holds the write
	// end past the child's death (so Ctrl-C always returns).
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			pr.Close()
		case <-done:
		}
	}()

	// ReadString (not bufio.Scanner) has no line-length cap, so a very long log line
	// can't silently truncate or stop the stream.
	br := bufio.NewReader(pr)
	for {
		line, rerr := br.ReadString('\n')
		if len(line) > 0 {
			w.Write([]byte(prefix + strings.TrimRight(line, "\r\n") + "\n"))
		}
		if rerr != nil {
			break
		}
	}
	pr.Close()

	err = cmd.Wait()
	if err != nil && ctx.Err() == nil { // a genuine failure, not a Ctrl-C cancel
		fmt.Fprintf(w, "%s[logs error: %v]\n", prefix, err)
		return err
	}
	return nil
}

// Stats streams `container stats` for the named containers (CPU %, memory, net,
// block I/O, pids), attached to the terminal. With noStream it prints a single
// snapshot and returns instead of streaming live.
func (r *Runtime) Stats(names []string, noStream bool) error {
	args := []string{"stats"}
	if noStream {
		args = append(args, "--no-stream")
	}
	args = append(args, names...)
	return r.stream(args...)
}

// ContainerStat is one container's guest-view resource snapshot, as reported by
// `container stats --no-stream --format json`. Only the fields opossum uses are
// decoded; the guest sees its own memory usage against its RAM limit (not the
// host memory the container's VM actually occupies — see host footprint).
type ContainerStat struct {
	ID               string `json:"id"`
	MemoryUsageBytes int64  `json:"memoryUsageBytes"`
	MemoryLimitBytes int64  `json:"memoryLimitBytes"`
}

// StatsSnapshot captures a single (non-streaming) guest-view stats reading for
// the named containers, parsed for further processing (e.g. rendering alongside
// host footprint). An empty names list asks the runtime for all running ones.
func (r *Runtime) StatsSnapshot(names []string) ([]ContainerStat, error) {
	args := append([]string{"stats", "--no-stream", "--format", "json"}, names...)
	// stdout-only: the output is JSON, so a stderr warning must not corrupt it.
	out, err := r.captureStdout(args...)
	if err != nil {
		return nil, fmt.Errorf("container stats: %w", err)
	}
	var stats []ContainerStat
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &stats); err != nil {
		return nil, fmt.Errorf("parsing container stats: %w", err)
	}
	return stats, nil
}

// Pull fetches an image, streaming progress.
func (r *Runtime) Pull(ref string) error {
	return r.stream("image", "pull", ref)
}

// Stop stops a running container, ignoring errors for already-stopped ones.
func (r *Runtime) Stop(name string) {
	r.capture("stop", name)
}

// Kill sends a signal (default KILL when signal is empty) to a running
// container, best-effort like Stop.
func (r *Runtime) Kill(name, signal string) {
	args := []string{"kill"}
	if signal != "" {
		args = append(args, "-s", signal)
	}
	r.capture(append(args, name)...)
}

// Start starts an existing (stopped) container in place, keeping its config. It
// returns an error (e.g. the container was never created).
func (r *Runtime) Start(name string) error {
	if out, err := r.capture("start", name); err != nil {
		return fmt.Errorf("starting %q: %w\n%s", name, err, strings.TrimSpace(out))
	}
	return nil
}

// Delete force-removes a container, ignoring "not found".
func (r *Runtime) Delete(name string) {
	r.capture("delete", "--force", name)
}

// inspectResult captures the fields of `container inspect` output that opossum
// reads. The container's own interface address lives under
// status.networks[].ipv4Address (e.g. "192.168.66.4/24"); other IPv4-shaped
// values in the document — notably configuration.publishedPorts[].hostAddress
// ("0.0.0.0") and ipv4Gateway — must NOT be mistaken for it.
type inspectResult struct {
	Status struct {
		State    string `json:"state"`
		Networks []struct {
			IPv4Address string `json:"ipv4Address"`
			IPv6Address string `json:"ipv6Address"`
		} `json:"networks"`
	} `json:"status"`
	Configuration struct {
		ID             string            `json:"id"` // the container's name (Apple `container` uses name as id)
		Labels         map[string]string `json:"labels"`
		PublishedPorts []struct {
			ContainerPort int    `json:"containerPort"`
			HostAddress   string `json:"hostAddress"`
			HostPort      int    `json:"hostPort"`
			Proto         string `json:"proto"`
		} `json:"publishedPorts"`
	} `json:"configuration"`
}

// ContainerSummary is one entry from `container ls -a`.
type ContainerSummary struct {
	Name   string
	State  string
	Labels map[string]string
}

// List returns every container (running or not) with its name, state, and labels,
// so callers can find a project's containers (e.g. to detect orphans).
func (r *Runtime) List() []ContainerSummary {
	out, err := r.capture("ls", "-a", "--format", "json")
	if err != nil {
		return nil
	}
	var results []inspectResult
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		return nil
	}
	summaries := make([]ContainerSummary, 0, len(results))
	for _, res := range results {
		summaries = append(summaries, ContainerSummary{
			Name:   res.Configuration.ID,
			State:  res.Status.State,
			Labels: res.Configuration.Labels,
		})
	}
	return summaries
}

// PortMapping is one published-port entry from `container inspect`.
type PortMapping struct {
	HostAddress   string
	HostPort      int
	ContainerPort int
	Proto         string
}

// ContainerInfo is the subset of `container inspect` opossum surfaces. Exists is
// false when the container is not found.
type ContainerInfo struct {
	Exists bool
	State  string // e.g. "running", "stopped"
	IP     string // interface address, IPv4 preferred (IPv6 fallback)
	Ports  []PortMapping
	Labels map[string]string
}

// Inspect parses a container's inspect JSON once, extracting the fields opossum
// reports. A missing container yields ContainerInfo{Exists: false}.
func (r *Runtime) Inspect(name string) ContainerInfo {
	out, err := r.capture("inspect", name)
	if err != nil {
		return ContainerInfo{}
	}
	var results []inspectResult
	if err := json.Unmarshal([]byte(out), &results); err != nil || len(results) == 0 {
		return ContainerInfo{}
	}
	res := results[0]
	info := ContainerInfo{Exists: true, State: res.Status.State, Labels: res.Configuration.Labels}
	// Prefer the IPv4 interface address; fall back to IPv6 (IPv6-only networks).
	for _, n := range res.Status.Networks {
		if ip := trimMask(n.IPv4Address); ip != "" {
			info.IP = ip
			break
		}
	}
	if info.IP == "" {
		for _, n := range res.Status.Networks {
			if ip := trimMask(n.IPv6Address); ip != "" {
				info.IP = ip
				break
			}
		}
	}
	for _, p := range res.Configuration.PublishedPorts {
		info.Ports = append(info.Ports, PortMapping{p.HostAddress, p.HostPort, p.ContainerPort, p.Proto})
	}
	return info
}

// InspectLabel returns a container's label value and whether the container
// exists at all. A present-but-unlabeled container returns ("", true); a missing
// container returns ("", false).
func (r *Runtime) InspectLabel(name, key string) (value string, exists bool) {
	info := r.Inspect(name)
	if !info.Exists {
		return "", false
	}
	return info.Labels[key], true
}

// InspectIP returns a container's interface address, or "" when the container is
// not running or has no address.
func (r *Runtime) InspectIP(name string) string {
	return r.Inspect(name).IP
}

// trimMask drops a trailing CIDR suffix, turning "192.168.66.4/24" into
// "192.168.66.4".
func trimMask(addr string) string {
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		return addr[:i]
	}
	return addr
}
