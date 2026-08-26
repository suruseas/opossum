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
	// The human summary, word for word.
	//
	// Asking whether each name appears says nothing about where it appears. A
	// mutation sweep exchanged the service with the command on the first line,
	// and the kind of a change with its path on every line under "files:", and
	// both left this repository green — printing "Audit of ` claude -p x`agent"
	// and "out.txt  added" to someone reading what an agent did to their files.
	var sb bytes.Buffer
	r.WriteSummary(&sb)
	wantSummary := strings.Join([]string{
		"Audit of `agent` claude -p x — exit 0",
		"  files:  1 changed, 1 added, 0 deleted under /proj/work",
		"    added    out.txt",
		"    changed  src.go",
		"  egress: 1 destination(s) (via proxy): api.anthropic.com:443",
		"  resources: unobserved (not captured)",
		"",
	}, "\n")
	if got := sb.String(); got != wantSummary {
		t.Errorf("the summary is not what this file says it should be\n got:\n%s\nwant:\n%s", got, wantSummary)
	}
	// The counts, in their own words, with three different numbers. Three ints of
	// the same type sit next to each other on one format call, and asking whether
	// "added" and "changed" appear says nothing about which number went with
	// which. The numbers have to differ from each other or the check cannot see
	// the swap: with one changed and one added, exchanging them reads the same.
	counted := &AuditReport{Files: AuditFiles{
		Observed:  true,
		Workspace: "/w",
		Changes: []workspace.FileChange{
			{Kind: workspace.Changed, Path: "a"},
			{Kind: workspace.Changed, Path: "b"},
			{Kind: workspace.Changed, Path: "c"},
			{Kind: workspace.Added, Path: "d"},
			{Kind: workspace.Added, Path: "e"},
			{Kind: workspace.Deleted, Path: "f"},
		},
	}}
	var cb bytes.Buffer
	counted.WriteSummary(&cb)
	if want := "3 changed, 2 added, 1 deleted under /w"; !strings.Contains(cb.String(), want) {
		t.Errorf("summary should say %q, got:\n%s", want, cb.String())
	}
	// The other two ways each line comes out, also whole.
	//
	// Three reports cover what WriteSummary can print: something happened,
	// nothing happened, nothing was watched. Pinning only the first left the
	// other two saying whatever they liked — an unobserved `files:` line printed
	// the reason belonging to a different dimension and nothing here noticed,
	// which is the same defect this test was just extended to catch one line up.
	for name, c := range map[string]struct {
		r    *AuditReport
		want []string
	}{
		"nothing was watched": {
			&AuditReport{
				Service: "agent", Command: nil, ExitCode: 0,
				Files:     AuditFiles{Observed: false, Reason: "no workspace bind mount"},
				Egress:    AuditEgress{Observed: false, Reason: "not routed through a proxy"},
				Resources: AuditSection{Reason: "not captured"},
			},
			[]string{
				"Audit of `agent` — exit 0",
				"  files:  unobserved (no workspace bind mount)",
				"  egress: unobserved (not routed through a proxy)",
				"  resources: unobserved (not captured)",
				"",
			},
		},
		"nothing happened": {
			&AuditReport{
				Service: "agent", Command: []string{"sh"}, ExitCode: 1,
				Files:     AuditFiles{Observed: true, Workspace: "/w"},
				Egress:    AuditEgress{Observed: true, Via: "proxy"},
				Resources: AuditSection{Reason: "not captured"},
			},
			[]string{
				"Audit of `agent` sh — exit 1",
				"  files:  no changes under /w",
				"  egress: no outbound connections (via proxy)",
				"  resources: unobserved (not captured)",
				"",
			},
		},
	} {
		var b bytes.Buffer
		c.r.WriteSummary(&b)
		if want := strings.Join(c.want, "\n"); b.String() != want {
			t.Errorf("%s: the summary is not what this file says it should be\n got:\n%s\nwant:\n%s",
				name, b.String(), want)
		}
	}
}
