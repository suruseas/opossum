package orchestrator

// The "verify" face of the box (#261): after a one-off runs, `run --audit` returns
// a structured record of what it did — which files it touched, where it tried to
// connect — complementing the compose declaration of what it was *allowed* to do.
// Each dimension is either observed (with data) or explicitly unobserved (with a
// reason), so a blank never reads as "nothing happened": an audit's value is its
// honesty about what it couldn't see.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/workspace"
)

// AuditReport is the machine-readable result of `run --audit`.
type AuditReport struct {
	Service   string       `json:"service"`
	Command   []string     `json:"command"`
	ExitCode  int          `json:"exitCode"`
	Files     AuditFiles   `json:"files"`
	Egress    AuditEgress  `json:"egress"`
	Resources AuditSection `json:"resources"`
}

// AuditFiles is the workspace diff dimension.
type AuditFiles struct {
	Observed  bool                   `json:"observed"`
	Reason    string                 `json:"reason,omitempty"`
	Workspace string                 `json:"workspace,omitempty"`
	Changes   []workspace.FileChange `json:"changes"`
}

// AuditEgress is the outbound-connection dimension.
type AuditEgress struct {
	Observed     bool     `json:"observed"`
	Reason       string   `json:"reason,omitempty"`
	Via          string   `json:"via,omitempty"` // the service whose log was read
	Destinations []string `json:"destinations"`
}

// AuditSection is a dimension that's only observed/unobserved (no structured data
// in the MVP), e.g. resources.
type AuditSection struct {
	Observed bool   `json:"observed"`
	Reason   string `json:"reason,omitempty"`
}

// RunAudited runs a one-off and returns an AuditReport of what it did. It snapshots
// the workspace before the run and diffs it after, and reads the egress proxy's log
// (when the run routes through one). The run's exit status is captured either way.
func (o *Orchestrator) RunAudited(service string, command []string, opts RunOneOffOptions) (*AuditReport, error) {
	svc, ok := o.Project.Services[service]
	if !ok {
		return nil, o.unknownServiceErr(service)
	}
	report := &AuditReport{Service: service, Command: command}

	// Files: snapshot the workspace before the run so we can diff after.
	ws := o.auditWorkspace(svc)
	var wsm *workspace.Manager
	var snapName string
	if ws == "" {
		report.Files.Reason = "the service has no bind mount at its working_dir, so there's no host workspace to diff"
	} else {
		wsm = workspace.New(ws)
		snapName = "audit-" + time.Now().Format("20060102-150405.000000000")
		if _, err := wsm.Snapshot(snapName); err != nil {
			report.Files.Reason = fmt.Sprintf("could not snapshot the workspace before the run: %v", err)
			wsm = nil
		} else {
			defer wsm.Remove(snapName) // a transient audit snapshot
		}
	}

	// Start dependencies first, so the egress-proxy baseline is taken from the SAME
	// proxy container the run will use: starting deps inside RunOneOff could recreate
	// the proxy and reset its log, so a line-count delta would slice off the run's
	// own connections. Then run with NoDeps.
	if !opts.NoDeps {
		if deps := svc.DependsOn.Names(); len(deps) > 0 {
			if err := o.Up(true, deps...); err != nil {
				return nil, fmt.Errorf("starting dependencies: %w", err)
			}
		}
	}

	// Egress: the proxy's log up to now (deps already up) is the baseline; the run
	// adds to it.
	proxySvc, egressReason := o.egressProxy(svc)
	if proxySvc != "" && !o.rt.Inspect(o.containerName(proxySvc)).Exists {
		// The run routes through a proxy, but it isn't running — so its log can't be
		// read. Say unobserved rather than report an empty log as "no connections".
		proxySvc, egressReason = "", "the proxy the run routes through isn't running, so its egress log can't be read"
	}
	var proxyBaseline int
	if proxySvc != "" {
		proxyBaseline = o.countLines(o.rt.CaptureLogs(o.containerName(proxySvc), 0))
	}

	// Run (deps already up; container stdout stays on stderr so the report owns stdout).
	opts.Audit = true
	opts.NoDeps = true
	runErr := o.RunOneOff(service, command, opts)
	report.ExitCode = exitCode(runErr)

	// Files diff.
	if wsm != nil {
		if changes, err := wsm.Diff(snapName); err != nil {
			report.Files.Reason = fmt.Sprintf("diffing the workspace failed: %v", err)
		} else {
			report.Files.Observed = true
			report.Files.Workspace = ws
			report.Files.Changes = changes
		}
	}

	// Egress delta.
	if proxySvc == "" {
		report.Egress.Reason = egressReason
	} else {
		all := strings.Split(o.rt.CaptureLogs(o.containerName(proxySvc), 0), "\n")
		newLines := all
		if proxyBaseline < len(all) {
			newLines = all[proxyBaseline:]
		}
		report.Egress.Observed = true
		report.Egress.Via = proxySvc
		report.Egress.Destinations = parseEgressDestinations(newLines)
	}

	// Resources: not captured in the MVP (peak use needs mid-run sampling).
	report.Resources = AuditSection{Reason: "peak resource use isn't captured yet — mid-run sampling is future work"}

	return report, nil
}

// auditWorkspace returns the host directory bind-mounted at the service's
// working_dir — the agent's writable workspace — or "" when there isn't one.
func (o *Orchestrator) auditWorkspace(svc *compose.Service) string {
	if svc.WorkingDir == "" {
		return ""
	}
	for _, v := range svc.Volumes {
		parts := strings.SplitN(v, ":", 2)
		if len(parts) < 2 || !isHostPath(parts[0]) {
			continue
		}
		if target := strings.SplitN(parts[1], ":", 2)[0]; target == svc.WorkingDir {
			return o.resolvePath(parts[0])
		}
	}
	return ""
}

// egressProxy returns the service whose log records this run's egress, or ""
// with a reason when egress isn't observable. Egress is only observable when the
// run routes through a proxy service we can read — on a plain NAT network the
// runtime doesn't log where a container connected.
func (o *Orchestrator) egressProxy(svc *compose.Service) (proxySvc, reason string) {
	if !envHasKey(svc.Environment, "HTTPS_PROXY") && !envHasKey(svc.Environment, "HTTP_PROXY") {
		return "", "the run isn't routed through a proxy, and a NAT network's egress isn't logged — use the caged variant to observe egress"
	}
	if _, ok := o.Project.Services["proxy"]; !ok {
		return "", "the run sets a proxy but the project has no `proxy` service whose log opossum can read"
	}
	return "proxy", ""
}

// requestRE pulls the method and target out of a tinyproxy request line, e.g.
// "Request (file descriptor 4): CONNECT api.anthropic.com:443 HTTP/1.1" or
// "Request (file descriptor 4): GET http://host/x HTTP/1.1". tinyproxy logs this
// line for every request BEFORE allowing or denying it, so parsing it captures
// every attempted destination — allowed and refused alike — over HTTPS (CONNECT),
// plain HTTP, and IPv6 literals, none of which a CONNECT-only match would catch.
var requestRE = regexp.MustCompile(`Request \(file descriptor \d+\): (\S+) (\S+)`)

func parseEgressDestinations(lines []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	for _, l := range lines {
		m := requestRE.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		method, target := m[1], m[2]
		if strings.EqualFold(method, "CONNECT") {
			add(target) // host:port, including [ipv6]:port
		} else if u, err := url.Parse(target); err == nil && u.Host != "" {
			add(u.Host) // GET/POST/… proxied absolute URL — take its host[:port]
		}
	}
	sort.Strings(out)
	return out
}

func (o *Orchestrator) countLines(s string) int {
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

// envHasKey reports whether a normalized KEY=value environment declares key.
func envHasKey(env compose.Environment, key string) bool {
	for _, e := range env {
		if e == key || strings.HasPrefix(e, key+"=") {
			return true
		}
	}
	return false
}

// exitCode extracts a numeric exit code from a run error: 0 for success, the
// process's code for a normal non-zero exit, -1 for anything else (a setup error).
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// WriteJSON renders the report as indented JSON.
func (r *AuditReport) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteSummary renders a short human-readable summary.
func (r *AuditReport) WriteSummary(w io.Writer) {
	fmt.Fprintf(w, "Audit of `%s`%s — exit %d\n", r.Service, cmdSuffix(r.Command), r.ExitCode)

	fmt.Fprint(w, "  files:  ")
	if !r.Files.Observed {
		fmt.Fprintf(w, "unobserved (%s)\n", r.Files.Reason)
	} else if len(r.Files.Changes) == 0 {
		fmt.Fprintf(w, "no changes under %s\n", r.Files.Workspace)
	} else {
		var a, c, d int
		for _, ch := range r.Files.Changes {
			switch ch.Kind {
			case workspace.Added:
				a++
			case workspace.Changed:
				c++
			case workspace.Deleted:
				d++
			}
		}
		fmt.Fprintf(w, "%d changed, %d added, %d deleted under %s\n", c, a, d, r.Files.Workspace)
		for _, ch := range r.Files.Changes {
			fmt.Fprintf(w, "    %-8s %s\n", ch.Kind, ch.Path)
		}
	}

	fmt.Fprint(w, "  egress: ")
	if !r.Egress.Observed {
		fmt.Fprintf(w, "unobserved (%s)\n", r.Egress.Reason)
	} else if len(r.Egress.Destinations) == 0 {
		fmt.Fprintf(w, "no outbound connections (via %s)\n", r.Egress.Via)
	} else {
		fmt.Fprintf(w, "%d destination(s) (via %s): %s\n", len(r.Egress.Destinations), r.Egress.Via, strings.Join(r.Egress.Destinations, ", "))
	}

	fmt.Fprintf(w, "  resources: unobserved (%s)\n", r.Resources.Reason)
}

func cmdSuffix(cmd []string) string {
	if len(cmd) == 0 {
		return ""
	}
	return " " + strings.Join(cmd, " ")
}
