package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A one-off's stdout is the container's — a service that speaks a protocol
// over stdio (an MCP server) is heard by whatever `opossum run` is piped
// into — and under --audit the report owns stdout, so the container's
// output moves to stderr with the rest of the progress. Both are decided
// in the orchestrator with the process's own streams, past what cobra's
// SetOut/SetErr (and so runSplit) can see, so this drives the built binary
// with two pipes. Until now neither was guarded: the tests read what a run
// said, not where (#511).
func TestARunsStdoutIsTheContainersAndUnderAuditTheReports(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: streams\nservices:\n  web:\n    image: web:latest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const says = "hello from the container"
	env := append(os.Environ(),
		"OPOSSUM_CONTAINER_BIN="+fakeShimBin,
		"FAKE_LOG="+filepath.Join(dir, "invocations.log"),
		"OPOSSUM_CRASH_GRACE=0",
		"FAKE_RUN_SAYS="+says)
	invoke := func(args ...string) (stdout, stderr string) {
		t.Helper()
		cmd := exec.Command(opossumBin, args...)
		cmd.Dir = dir
		cmd.Env = env
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		if err := cmd.Run(); err != nil {
			t.Fatalf("opossum %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, out.String(), errOut.String())
		}
		return out.String(), errOut.String()
	}

	// Plain: stdout is the container's line and nothing else; the progress
	// line is on stderr.
	stdout, stderr := invoke("run", "--no-deps", "web", "echo", "hi")
	if stdout != says+"\n" {
		t.Errorf("a one-off's stdout should be the container's output alone, got:\n%q", stdout)
	}
	if !strings.Contains(stderr, "Running one-off web") {
		t.Errorf("the progress line belongs on stderr, got:\n%s", stderr)
	}
	if strings.Contains(stderr, says) {
		t.Errorf("the container's output reached stderr as well:\n%s", stderr)
	}

	// Audited: stdout is the report alone — parsed whole, so a stray line
	// anywhere in it fails — and the container's line is on stderr.
	stdout, stderr = invoke("run", "--audit", "--audit-format", "json", "--no-deps", "web", "echo", "hi")
	var report struct {
		Service  string `json:"service"`
		ExitCode int    `json:"exitCode"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("under --audit, stdout should be the JSON report and nothing else: %v\n%s", err, stdout)
	}
	if report.Service != "web" || report.ExitCode != 0 {
		t.Errorf("the report should be web's, exit 0, got %+v", report)
	}
	if !strings.Contains(stderr, says) {
		t.Errorf("under --audit the container's output should be on stderr, got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "Running one-off web") {
		t.Errorf("the progress line belongs on stderr under --audit too, got:\n%s", stderr)
	}
}
