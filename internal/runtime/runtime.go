// Package runtime is a thin wrapper around Apple's `container` CLI. opossum
// never re-implements the runtime; it only shells out and parses results.
package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Runtime invokes the `container` binary.
type Runtime struct {
	Bin string
	// Verbose echoes each `container` invocation before running it (useful for
	// bug reports). The echo goes to Trace, or os.Stderr when Trace is nil.
	Verbose bool
	Trace   io.Writer
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

// stream runs a command with stdio attached to the parent process.
func (r *Runtime) stream(args ...string) error {
	r.trace(args)
	cmd := exec.Command(r.Bin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// capture runs a command and returns combined stdout+stderr.
func (r *Runtime) capture(args ...string) (string, error) {
	r.trace(args)
	cmd := exec.Command(r.Bin, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// EnsureNetwork creates the network if it does not already exist. It reports
// whether it actually created it (false = it was already there), so callers can
// roll back only a network they themselves created.
func (r *Runtime) EnsureNetwork(name string) (created bool, err error) {
	out, cerr := r.capture("network", "create", name)
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

// Build builds an image.
func (r *Runtime) Build(o BuildOptions) error {
	args := []string{"build", "-t", o.Tag}
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
	return r.stream(args...)
}

// RunOptions describes a `container run` invocation.
type RunOptions struct {
	Name       string
	Image      string
	Platform   string // --platform (e.g. linux/amd64); amd64 also enables --rosetta
	Network    string
	DNSDomain  string // --dns-domain (the registered local domain)
	DNSSearch  string // --dns-search (per-project subdomain for bare-name resolution)
	Env        []string
	Ports      []string
	Volumes    []string
	Tmpfs      []string // --tmpfs mount targets
	Command    []string
	Entrypoint []string // overrides the image ENTRYPOINT (--entrypoint + positional args)
	Labels     []string // key=value labels (-l)
	Detach     bool
}

// Run starts a container.
func (r *Runtime) Run(o RunOptions) error {
	args := []string{"run"}
	if o.Detach {
		args = append(args, "-d")
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
	if o.Network != "" {
		args = append(args, "--network", o.Network)
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
func (r *Runtime) Exec(name string, args []string) error {
	_, err := r.capture(append([]string{"exec", name}, args...)...)
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
		Labels         map[string]string `json:"labels"`
		PublishedPorts []struct {
			ContainerPort int    `json:"containerPort"`
			HostAddress   string `json:"hostAddress"`
			HostPort      int    `json:"hostPort"`
			Proto         string `json:"proto"`
		} `json:"publishedPorts"`
	} `json:"configuration"`
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
