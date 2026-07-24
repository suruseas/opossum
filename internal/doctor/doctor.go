// Package doctor runs environment checks for `opossum doctor`. It bundles the
// manual triage steps for the environment snags opossum users hit repeatedly —
// the runtime not running, the DNS domain not registered, a long-running default
// network that has wedged (containers can't reach the internet), an
// under-resourced builder, and a memory over-commit — into one command, each with
// a one-line fix.
package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

// perVMBaseMB is the typical host memory a single idle container VM uses
// (measured ~250-400 MB; see docs/vs-docker-desktop.md). Used for the estimate.
const perVMBaseMB = 290

const (
	probeImage  = "docker.io/library/alpine:3.20"
	probeScript = "nslookup github.com >/dev/null 2>&1 && echo DNS-OK || echo DNS-FAIL; " +
		"wget -T5 -qO- http://1.1.1.1/ >/dev/null 2>&1 && echo IP-OK || echo IP-FAIL"
)

type status int

const (
	ok status = iota
	warn
	fail
)

func (s status) icon() string {
	switch s {
	case ok:
		return "✅"
	case warn:
		return "⚠️ "
	default:
		return "❌"
	}
}

// String is the machine-readable status vocabulary used by `--format json`:
// "ok" / "warn" / "fail".
func (s status) String() string {
	switch s {
	case ok:
		return "ok"
	case warn:
		return "warn"
	default:
		return "fail"
	}
}

type check struct {
	name        string
	status      status
	detail, fix string
}

// Runner is the runtime capability doctor needs (satisfied by *runtime.Runtime),
// kept small so checks are easy to test.
type Runner interface {
	Output(args ...string) (string, error)
	DNSDomainExists(domain string) bool
}

var _ Runner = (*runtime.Runtime)(nil)

// CheckResult is one environment check in machine-readable form (`--format json`).
// ID is a stable slug (matching the human report's name column), Status is one of
// "ok"/"warn"/"fail", Detail is the human explanation, and Fix is the remediation
// hint (empty when Status is "ok").
type CheckResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

// Report is the machine-readable result of the environment checks. Healthy is
// false when any check failed — the same condition that makes the process exit
// non-zero — so an agent can decide from this one field.
type Report struct {
	Healthy bool          `json:"healthy"`
	Checks  []CheckResult `json:"checks"`
}

// runChecks executes the environment checks against rt. dnsDomain is the domain
// to verify; project (nil if none was found) drives the memory estimate;
// hostMemMB is the machine's RAM in MB (0 = unknown).
func runChecks(rt Runner, dnsDomain string, project *compose.Project, hostMemMB int) []check {
	checks := []check{checkRuntime(rt)}
	if checks[0].status != fail { // pointless to probe further if the runtime is down
		checks = append(checks, checkDNS(rt, dnsDomain), checkNetwork(rt), checkBuilder(rt), checkStorage(rt))
	}
	if project != nil {
		checks = append(checks, checkMemory(project, hostMemMB))
	}
	return checks
}

// Run executes the checks against rt and writes a human report to w. It returns
// false if any check failed. See runChecks for the parameters.
func Run(w io.Writer, rt Runner, dnsDomain string, project *compose.Project, hostMemMB int) bool {
	checks := runChecks(rt, dnsDomain, project, hostMemMB)

	allOK := true
	for _, c := range checks {
		fmt.Fprintf(w, "%s %-8s %s\n", c.status.icon(), c.name, c.detail)
		if c.fix != "" {
			fmt.Fprintf(w, "            ↳ %s\n", c.fix)
		}
		if c.status == fail {
			allOK = false
		}
	}
	return allOK
}

// RunJSON executes the checks against rt and writes a machine-readable Report to
// w as indented JSON. It returns false if any check failed (mirroring Run's exit
// signal) plus any encoding error. See runChecks for the parameters.
func RunJSON(w io.Writer, rt Runner, dnsDomain string, project *compose.Project, hostMemMB int) (bool, error) {
	checks := runChecks(rt, dnsDomain, project, hostMemMB)

	rep := Report{Healthy: true, Checks: make([]CheckResult, len(checks))}
	for i, c := range checks {
		rep.Checks[i] = CheckResult{ID: c.name, Status: c.status.String(), Detail: c.detail, Fix: c.fix}
		if c.status == fail {
			rep.Healthy = false
		}
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return rep.Healthy, err
	}
	fmt.Fprintln(w, string(b))
	return rep.Healthy, nil
}

func checkRuntime(rt Runner) check {
	out, err := rt.Output("system", "status")
	if err != nil {
		return check{"runtime", fail, "Apple `container` isn't available or its system isn't running",
			"install Apple container and run `container system start`"}
	}
	if systemRunning(out) {
		return check{"runtime", ok, "Apple container system is running", ""}
	}
	return check{"runtime", fail, "the container system is not running", "run `container system start`"}
}

func systemRunning(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "status" && strings.EqualFold(f[1], "running") {
			return true
		}
	}
	return false
}

func checkDNS(rt Runner, domain string) check {
	if rt.DNSDomainExists(domain) {
		return check{"dns", ok, fmt.Sprintf("DNS domain %q is registered", domain), ""}
	}
	return check{"dns", fail,
		fmt.Sprintf("DNS domain %q isn't registered — services can't resolve each other by name", domain),
		fmt.Sprintf("sudo container system dns create %s", domain)}
}

// checkNetwork probes outbound connectivity and DNS from a throwaway container —
// the classic "default network wedged after long uptime" failure.
func checkNetwork(rt Runner) check {
	out, _ := rt.Output("run", "--rm", probeImage, "sh", "-c", probeScript)
	ipOK := strings.Contains(out, "IP-OK")
	dnsOK := strings.Contains(out, "DNS-OK")
	switch {
	case ipOK && dnsOK:
		return check{"network", ok, "containers can reach the internet and resolve DNS", ""}
	case !ipOK:
		// Either the probe couldn't reach anything, or it couldn't even run/pull —
		// both point at the default network having wedged.
		return check{"network", fail, "containers can't reach the internet (a long-running default network can wedge)",
			"restart the runtime: container system stop && container system start"}
	default:
		return check{"network", fail, "containers can reach the internet but DNS resolution is failing",
			"restart the runtime: container system stop && container system start"}
	}
}

func checkBuilder(rt Runner) check {
	out, err := rt.Output("builder", "status")
	memMB, state := parseBuilder(out)
	if err != nil || state == "" {
		return check{"builder", ok, "no build VM yet (one starts on first build)", ""}
	}
	if memMB > 0 && memMB <= 2048 {
		return check{"builder", warn,
			fmt.Sprintf("the build VM has only %d MB — heavy builds (large multi-stage, big apt-get) can be slow or fail", memMB),
			"give it more: container builder delete --force && container builder start --cpus 4 --memory 8g"}
	}
	return check{"builder", ok, fmt.Sprintf("build VM is provisioned (%d MB)", memMB), ""}
}

// parseBuilder pulls the memory (MB) and state out of `container builder status`.
func parseBuilder(out string) (memMB int, state string) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] == "ID" {
			continue
		}
		for i, t := range f {
			if t == "running" || t == "stopped" {
				state = t
			}
			if (t == "MB" || t == "GB") && i > 0 {
				if n, err := strconv.Atoi(f[i-1]); err == nil {
					memMB = n
					if t == "GB" {
						memMB *= 1024
					}
				}
			}
		}
	}
	return memMB, state
}

// reclaimableWarnBytes is the point past which "reclaimable" storage reads as
// accumulated cruft rather than a working image cache. It's deliberately high:
// when nothing is running, every cached image counts as reclaimable (no container
// references it), so a low threshold would nag users who keep base images around
// on purpose. 20 GB is well beyond a normal cache but far below the disk-filling
// accumulation (100+ GB of untagged build layers) this check exists to catch.
const reclaimableWarnBytes = 20_000_000_000

// checkStorage surfaces how much image/volume storage the runtime holds that no
// running container references. Apple's `container images ls` hides untagged
// (dangling) images, so a large cache from repeated builds/pulls can fill the
// disk unseen — `container system df` is the only place it shows. This warns only
// past reclaimableWarnBytes; below it, the amount is reported as informational.
func checkStorage(rt Runner) check {
	out, err := rt.Output("system", "df")
	if err != nil {
		return check{"storage", ok, "disk usage unavailable (`container system df` didn't run)", ""}
	}
	b, parsed := parseReclaimable(out)
	if !parsed {
		return check{"storage", ok, "disk usage looks fine", ""}
	}
	if b >= reclaimableWarnBytes {
		return check{"storage", warn,
			fmt.Sprintf("%s of images/volumes are reclaimable — not referenced by any running container, and untagged ones don't show in `container images ls`, so this can fill the disk unseen", humanBytes(b)),
			"if you're low on disk, reclaim it: container image prune -a && container volume prune"}
	}
	return check{"storage", ok, fmt.Sprintf("%s of images/volumes reclaimable", humanBytes(b)), ""}
}

// reclaimableRe matches the RECLAIMABLE column of `container system df` — a size
// followed by a "(NN%)" share, e.g. "188 GB (100%)". The trailing "(" is what
// distinguishes it from the SIZE column, which carries no percentage.
var reclaimableRe = regexp.MustCompile(`([0-9.]+)\s+(B|KB|MB|GB|TB|PB)\s+\(`)

// parseReclaimable sums the reclaimable bytes across every row of `container
// system df`. ok is false when no reclaimable column was found (unexpected output).
func parseReclaimable(out string) (bytes int64, ok bool) {
	ms := reclaimableRe.FindAllStringSubmatch(out, -1)
	if len(ms) == 0 {
		return 0, false
	}
	var total float64
	for _, m := range ms {
		n, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		total += n * unitBytes(m[2])
	}
	return int64(total), true
}

// unitBytes maps a `container system df` size unit to bytes, base-1000 to match
// how the CLI prints (GB, not GiB).
func unitBytes(u string) float64 {
	switch u {
	case "KB":
		return 1e3
	case "MB":
		return 1e6
	case "GB":
		return 1e9
	case "TB":
		return 1e12
	case "PB":
		return 1e15
	}
	return 1 // "B"
}

func humanBytes(b int64) string {
	f := float64(b)
	switch {
	case f >= 1e12:
		return fmt.Sprintf("%.1f TB", f/1e12)
	case f >= 1e9:
		return fmt.Sprintf("%.1f GB", f/1e9)
	case f >= 1e6:
		return fmt.Sprintf("%.0f MB", f/1e6)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// checkMemory estimates the stack's host memory use: each container is its own VM
// (~290 MB typical), more for a service with a higher mem_limit.
func checkMemory(p *compose.Project, hostMemMB int) check {
	total := 0
	for _, svc := range p.Services {
		per := perVMBaseMB
		if mem, _, _ := svc.Resources(); mem != "" {
			if mb := parseMiB(mem); mb > per {
				per = mb
			}
		}
		total += per
	}
	detail := fmt.Sprintf("%d services ≈ %s of RAM (each container is its own ~%d MB VM)",
		len(p.Services), humanMB(total), perVMBaseMB)
	if hostMemMB > 0 {
		detail += fmt.Sprintf("; this Mac has %s", humanMB(hostMemMB))
		if total > hostMemMB*7/10 {
			return check{"memory", warn, detail + " — a large share; expect swapping and slowdowns",
				"run fewer services at once, or lower mem_limit for the heavy ones"}
		}
	}
	return check{"memory", ok, detail, ""}
}

// parseMiB reads the leading integer of a "<N>M" memory arg (as Resources emits).
func parseMiB(mem string) int {
	n, _ := strconv.Atoi(strings.TrimRight(mem, "M"))
	return n
}

func humanMB(mb int) string {
	if mb >= 1024 {
		return fmt.Sprintf("%.1f GB", float64(mb)/1024)
	}
	return fmt.Sprintf("%d MB", mb)
}
