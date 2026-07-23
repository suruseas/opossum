package orchestrator

// Internal tests for the audit report's pure parts (#261): parsing, workspace
// detection, exit-code extraction, and rendering. The full snapshot→run→diff flow
// is covered by the real-runtime e2e in the PR.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
	"github.com/suruseas/opossum/internal/workspace"
)

func TestParseEgressDestinations(t *testing.T) {
	// Real tinyproxy request lines: an allowed CONNECT and a denied one both log a
	// "CONNECT host:port" request line, so both destinations are captured.
	lines := []string{
		`CONNECT   Jul 21 05:01:57 [1]: Request (file descriptor 4): CONNECT api.anthropic.com:443 HTTP/1.1`,
		`INFO      Jul 21 05:01:57 [1]: opensock: opening connection to api.anthropic.com:443`,
		`CONNECT   Jul 21 05:02:01 [2]: Request (file descriptor 6): CONNECT evil.example.com:443 HTTP/1.1`,
		`NOTICE    Jul 21 05:02:01 [2]: Proxying refused on filtered domain "evil.example.com"`,
		`CONNECT   Jul 21 05:02:05 [3]: Request (file descriptor 7): CONNECT api.anthropic.com:443 HTTP/1.1`, // dup
		// plain HTTP (proxied absolute URL) and an IPv6 literal must be captured too —
		// a CONNECT-only match would silently drop these (audit false-negative).
		`CONNECT   Jul 21 05:02:07 [4]: Request (file descriptor 8): GET http://plain.example.com/x HTTP/1.1`,
		`CONNECT   Jul 21 05:02:09 [5]: Request (file descriptor 9): CONNECT [2001:db8::1]:443 HTTP/1.1`,
	}
	got := parseEgressDestinations(lines)
	want := []string{"[2001:db8::1]:443", "api.anthropic.com:443", "evil.example.com:443", "plain.example.com"} // sorted, deduped
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("parseEgressDestinations = %v, want %v", got, want)
	}
}

func TestAuditWorkspaceFindsBindMountAtWorkdir(t *testing.T) {
	p := &compose.Project{Name: "demo", BaseDir: "/proj", Services: map[string]*compose.Service{
		"agent": {WorkingDir: "/work", Volumes: compose.Volumes{"./work:/work", "/etc/cfg:/cfg:ro"}},
		"nowd":  {Volumes: compose.Volumes{"./work:/work"}},                 // no working_dir
		"novol": {WorkingDir: "/work", Volumes: compose.Volumes{"data:/x"}}, // no bind at /work
	}}
	o := New(p, nil, "opossum", io.Discard)
	if got := o.auditWorkspace(p.Services["agent"]); got != "/proj/work" {
		t.Errorf("auditWorkspace(agent) = %q, want /proj/work", got)
	}
	if got := o.auditWorkspace(p.Services["nowd"]); got != "" {
		t.Errorf("a service with no working_dir has no audit workspace, got %q", got)
	}
	if got := o.auditWorkspace(p.Services["novol"]); got != "" {
		t.Errorf("a service with no bind mount at working_dir has no audit workspace, got %q", got)
	}
}

func TestEgressProxyDetection(t *testing.T) {
	withProxy := &compose.Project{Name: "d", Services: map[string]*compose.Service{
		"agent": {Environment: compose.Environment{"HTTPS_PROXY=http://gw:8080"}},
		"proxy": {},
	}}
	o := New(withProxy, nil, "opossum", io.Discard)
	if svc, _ := o.egressProxy(withProxy.Services["agent"]); svc != "proxy" {
		t.Errorf("a proxied run with a proxy service should observe egress via proxy, got %q", svc)
	}
	// No proxy env -> unobserved with a reason (never silently "no egress").
	noProxy := &compose.Project{Name: "d", Services: map[string]*compose.Service{"agent": {}, "proxy": {}}}
	o2 := New(noProxy, nil, "opossum", io.Discard)
	if svc, reason := o2.egressProxy(noProxy.Services["agent"]); svc != "" || reason == "" {
		t.Errorf("a non-proxied run must report egress unobserved with a reason, got (%q, %q)", svc, reason)
	}
}

func TestExitCode(t *testing.T) {
	if exitCode(nil) != 0 {
		t.Error("nil error should be exit 0")
	}
	// A real non-zero container exit.
	err := exec.Command("sh", "-c", "exit 7").Run()
	if got := exitCode(err); got != 7 {
		t.Errorf("exitCode(exit 7) = %d, want 7", got)
	}
	if exitCode(errors.New("setup failed")) != -1 {
		t.Error("a non-ExitError should be -1 (a setup failure, not a process exit)")
	}
	// A non-TTY foreground run now returns a *runtime.RunError wrapping the exec
	// error; exitCode must still extract the child's code through Unwrap so
	// `run --audit` reports it (a dropped Unwrap would silently regress to -1).
	if got := exitCode(&runtime.RunError{Err: err, Stderr: "boom"}); got != 7 {
		t.Errorf("exitCode through a RunError wrapper = %d, want 7", got)
	}
}

func TestAuditReportRendering(t *testing.T) {
	r := &AuditReport{
		Service: "agent", Command: []string{"claude", "-p", "x"}, ExitCode: 0,
		Files: AuditFiles{Observed: true, Workspace: "/proj/work", Changes: []workspace.FileChange{
			{Path: "out.txt", Kind: workspace.Added, Hash: "abc"},
			{Path: "src.go", Kind: workspace.Changed, Hash: "def"},
		}},
		Egress:    AuditEgress{Observed: true, Via: "proxy", Destinations: []string{"api.anthropic.com:443"}},
		Resources: AuditSection{Reason: "not captured"},
	}
	// JSON is machine-readable and round-trips.
	var jb bytes.Buffer
	if err := r.WriteJSON(&jb); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	var back AuditReport
	if err := json.Unmarshal(jb.Bytes(), &back); err != nil {
		t.Fatalf("audit JSON doesn't round-trip: %v\n%s", err, jb.String())
	}
	if back.ExitCode != 0 || len(back.Files.Changes) != 2 || !back.Egress.Observed {
		t.Errorf("round-tripped report lost data: %+v", back)
	}
	// Human summary names the changes and the destination.
	var sb bytes.Buffer
	r.WriteSummary(&sb)
	s := sb.String()
	for _, want := range []string{"exit 0", "added", "out.txt", "changed", "src.go", "api.anthropic.com:443", "via proxy"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary missing %q, got:\n%s", want, s)
		}
	}
	// An unobserved dimension says so (never blank).
	r2 := &AuditReport{Service: "agent", Egress: AuditEgress{Observed: false, Reason: "not routed through a proxy"}}
	var sb2 bytes.Buffer
	r2.WriteSummary(&sb2)
	if !strings.Contains(sb2.String(), "unobserved (not routed through a proxy)") {
		t.Errorf("summary must mark unobserved egress with its reason, got:\n%s", sb2.String())
	}
}
