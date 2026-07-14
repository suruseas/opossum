package runtime

// Evals for InspectIP against the real `container inspect` JSON shape. The
// regression these guard: a service with a published port exposes a
// hostAddress of "0.0.0.0", which must never be reported as the container's IP.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeShimBin is the compiled fake `container` shim, built once for the package.
// A compiled binary spawns in ~1-2ms versus ~50-80ms for a /bin/sh script, and
// the suite spawns it many times — so this dominates the runtime tests' cost.
var fakeShimBin string

func TestMain(m *testing.M) {
	d, err := os.MkdirTemp("", "opossum-rt-test-")
	if err != nil {
		panic(err)
	}
	fakeShimBin = filepath.Join(d, "fakeshim")
	if out, berr := exec.Command("go", "build", "-o", fakeShimBin, "./testdata/fakeshim").CombinedOutput(); berr != nil {
		os.RemoveAll(d)
		panic(fmt.Sprintf("building fake shim: %v\n%s", berr, out))
	}
	code := m.Run()
	os.RemoveAll(d)
	os.Exit(code)
}

// recordWriter records each Write call separately, to verify FollowLogs emits one
// whole line per Write (so concurrent streams can't interleave mid-line).
type recordWriter struct{ writes []string }

func (rw *recordWriter) Write(p []byte) (int, error) {
	rw.writes = append(rw.writes, string(p))
	return len(p), nil
}

func TestFollowLogsWholeLineWrites(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "logs.txt")
	if err := os.WriteFile(outFile, []byte("a\nbb\nccc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{Bin: fakeShimBin, Env: []string{"SHIM_OUT=" + outFile}}
	rec := &recordWriter{}
	if err := r.FollowLogs(context.Background(), "x", LogsOptions{Follow: true}, rec, "P | "); err != nil {
		t.Fatalf("FollowLogs: %v", err)
	}
	// Each line is written once, whole, with the prefix — no torn writes.
	want := []string{"P | a\n", "P | bb\n", "P | ccc\n"}
	if !reflect.DeepEqual(rec.writes, want) {
		t.Errorf("each line should be one whole write, got %q want %q", rec.writes, want)
	}
}

// exitShim returns a Runtime whose `container` just exits 0 — for exercising the
// verbose command trace without caring about output.
func exitShim(t *testing.T) string {
	t.Helper()
	// The compiled shim with no SHIM_* env just exits 0.
	return fakeShimBin
}

func TestVerboseTracesCommands(t *testing.T) {
	shim := exitShim(t)
	var buf bytes.Buffer
	r := &Runtime{Bin: shim, Verbose: true, Trace: &buf}

	r.capture("inspect", "web.foo.opossum")
	if got := buf.String(); !strings.Contains(got, "+ "+shim+" inspect web.foo.opossum") {
		t.Errorf("verbose trace = %q, want the inspect command echoed", got)
	}

	// The stream path (used by `up`, so it carries the key `container run …` line)
	// must trace too.
	buf.Reset()
	if err := r.stream("run", "-d", "--name", "web.foo.opossum", "web:latest"); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "+ "+shim+" run -d --name web.foo.opossum web:latest") {
		t.Errorf("stream path should trace the run command, got %q", got)
	}

	// A multi-line arg (e.g. a PEM env value) is quoted so the trace stays on one
	// line instead of spilling raw newlines.
	buf.Reset()
	r.capture("run", "-e", "KEY=line1\nline2")
	got := buf.String()
	if !strings.Contains(got, `"KEY=line1\nline2"`) {
		t.Errorf("multi-line arg should be quoted onto one line, got %q", got)
	}
	if n := strings.Count(got, "\n"); n != 1 {
		t.Errorf("trace should be a single line (one trailing newline), got %d newlines: %q", n, got)
	}

	// Other control characters (e.g. ESC) are quoted too, so the trace can't be
	// pushed onto another line or inject terminal escapes.
	buf.Reset()
	r.capture("run", "-e", "T=a\x1bb")
	if got := buf.String(); !strings.Contains(got, `"T=a\x1bb"`) {
		t.Errorf("control char should be quoted, got %q", got)
	}
}

func TestVerboseOffIsSilent(t *testing.T) {
	shim := exitShim(t)
	var buf bytes.Buffer
	r := &Runtime{Bin: shim, Verbose: false, Trace: &buf}
	r.capture("inspect", "x")
	if err := r.stream("version"); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("verbose off must be silent, got %q", buf.String())
	}
}

// replayShim returns a Runtime whose `container` prints the given output (to
// stdout; capture merges stderr anyway) and exits with the given code — used to
// replay real CLI outputs through the parsers. See testdata/real-cli-output.md.
func replayShim(t *testing.T, output string, exit int) *Runtime {
	t.Helper()
	dir := t.TempDir()
	outFile := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(outFile, []byte(output), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Runtime{Bin: fakeShimBin, Env: []string{"SHIM_OUT=" + outFile, "SHIM_EXIT=" + strconv.Itoa(exit)}}
}

// inspectShim returns a Runtime whose `container` prints the given JSON for any
// invocation (enough to exercise InspectIP's `inspect` call).
func inspectShim(t *testing.T, json string) *Runtime {
	t.Helper()
	dir := t.TempDir()
	jsonFile := filepath.Join(dir, "inspect.json")
	if err := os.WriteFile(jsonFile, []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Runtime{Bin: fakeShimBin, Env: []string{"SHIM_OUT=" + jsonFile}}
}

func TestInspectIPPrefersInterfaceOverPublishedPort(t *testing.T) {
	// Real shape: the interface IPv4 is under status.networks[].ipv4Address;
	// configuration.publishedPorts[].hostAddress is the 0.0.0.0 trap.
	js := `[{"status":{"state":"running","networks":[
		{"network":"demo-net","ipv4Address":"192.168.66.4/24","ipv6Address":"fdee::4/64","ipv4Gateway":"192.168.66.1"}]},
		"configuration":{"publishedPorts":[{"hostAddress":"0.0.0.0","hostPort":8080}]}}]`
	if got := inspectShim(t, js).InspectIP("web"); got != "192.168.66.4" {
		t.Errorf("InspectIP = %q, want 192.168.66.4 (not the published 0.0.0.0 or the gateway)", got)
	}
}

func TestInspectIPFallsBackToIPv6WhenNoIPv4(t *testing.T) {
	// This is the exact bug scenario: an IPv6-only interface plus a published
	// port. The old heuristic reported 0.0.0.0; we now report the IPv6 address.
	js := `[{"status":{"networks":[
		{"network":"demo-net","ipv4Address":"","ipv6Address":"fd48:2e4c::abcd/64"}]},
		"configuration":{"publishedPorts":[{"hostAddress":"0.0.0.0"}]}}]`
	got := inspectShim(t, js).InspectIP("web")
	if got == "0.0.0.0" {
		t.Fatalf("InspectIP returned the published 0.0.0.0 — the bug is not fixed")
	}
	if got != "fd48:2e4c::abcd" {
		t.Errorf("InspectIP = %q, want the IPv6 interface address", got)
	}
}

func TestInspectIPEmptyWhenNoAddress(t *testing.T) {
	for name, js := range map[string]string{
		"no networks": `[{"status":{"networks":[]}}]`,
		"empty array": `[]`,
		"not json":    `not json at all`,
	} {
		if got := inspectShim(t, js).InspectIP("x"); got != "" {
			t.Errorf("%s: InspectIP = %q, want empty", name, got)
		}
	}
}

// --- Fidelity evals: real CLI outputs (testdata/real-cli-output.md) must be
// handled correctly by the parsers. These guard against the fake shim drifting
// from the real `container` CLI, which previously hid a bug.

func TestFidelityDNSDomainExists(t *testing.T) {
	const realList = "DOMAIN\nopossum\n" // container system dns list, exit 0
	rt := replayShim(t, realList, 0)
	if !rt.DNSDomainExists("opossum") {
		t.Error("DNSDomainExists should find 'opossum' in the real dns-list output")
	}
	if rt.DNSDomainExists("nope") {
		t.Error("DNSDomainExists should not find an absent domain")
	}
	if replayShim(t, "", 1).DNSDomainExists("opossum") {
		t.Error("DNSDomainExists should be false when the command errors")
	}
}

func TestFidelityEnsureNetwork(t *testing.T) {
	// Real "already exists" failure must be treated as success, and reported as
	// NOT created (so callers don't roll it back).
	if created, err := replayShim(t, "Error: network demo-net already exists", 1).EnsureNetwork("demo-net"); err != nil || created {
		t.Errorf("EnsureNetwork on existing should be (false, nil), got (%v, %v)", created, err)
	}
	// A fresh create (exit 0) succeeds and reports created.
	if created, err := replayShim(t, "demo-net", 0).EnsureNetwork("demo-net"); err != nil || !created {
		t.Errorf("EnsureNetwork on fresh create should be (true, nil), got (%v, %v)", created, err)
	}
	// An unexpected failure is surfaced.
	if _, err := replayShim(t, "Error: something went wrong", 1).EnsureNetwork("demo-net"); err == nil {
		t.Error("EnsureNetwork should surface an unexpected error")
	}
}

func TestFidelityNetworkAlreadyGone(t *testing.T) {
	// The real missing-network error (does NOT contain "not found").
	const realMissing = `Error: failed to delete one or more networks: ["demo-net"]`
	if !networkAlreadyGone(realMissing) {
		t.Error("networkAlreadyGone should recognize the real missing-network error (no spurious warning on clean re-down)")
	}
	if !networkAlreadyGone("Error: network demo-net not found") {
		t.Error("networkAlreadyGone should also recognize a 'not found' phrasing")
	}
	if networkAlreadyGone("Error: network is still in use by container x") {
		t.Error("networkAlreadyGone must NOT swallow an unrelated failure")
	}
}

func TestFidelityInspectIPContainerNotFound(t *testing.T) {
	// Real inspect error for a missing/stopped container.
	if got := replayShim(t, "Error: container not found: web.opossum", 1).InspectIP("web.opossum"); got != "" {
		t.Errorf("InspectIP on 'container not found' = %q, want empty", got)
	}
}

// --- Direct argument-assembly evals for the runtime wrapper. Until now these
// methods were only exercised indirectly through the orchestrator; here we
// assert the exact `container` argv each one emits, at the runtime layer.

// loggingShim records each invocation's arguments (one space-joined line) and
// exits 0.
func loggingShim(t *testing.T) (*Runtime, func() []string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "args.log")
	rt := &Runtime{Bin: fakeShimBin, Env: []string{"SHIM_LOG=" + logPath}}
	read := func() []string {
		b, err := os.ReadFile(logPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			t.Fatal(err)
		}
		return splitLines(string(b))
	}
	return rt, read
}

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func lastLine(t *testing.T, read func() []string) string {
	t.Helper()
	lines := read()
	if len(lines) == 0 {
		t.Fatal("no invocation was recorded")
	}
	return lines[len(lines)-1]
}

func TestRunAssemblesFullArgv(t *testing.T) {
	rt, read := loggingShim(t)
	err := rt.Run(RunOptions{
		Name:      "db.demo.opossum",
		Image:     "postgres:16",
		Network:   "demo-net",
		DNSDomain: "opossum",
		DNSSearch: "demo.opossum",
		Env:       []string{"A=1", "B=2"},
		Ports:     []string{"5432:5432"},
		Volumes:   []string{"/host:/data"},
		Command:   []string{"postgres", "-c", "log=all"},
		Labels:    []string{"opossum.project=demo"},
		Detach:    true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := lastLine(t, read)
	want := "run -d --name db.demo.opossum --network demo-net --dns-domain opossum --dns-search demo.opossum " +
		"-e A=1 -e B=2 -p 5432:5432 -v /host:/data -l opossum.project=demo postgres:16 postgres -c log=all"
	if got != want {
		t.Errorf("Run argv mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestInspect(t *testing.T) {
	// Real shape: state under status.state, interface IP under status.networks,
	// published ports under configuration.publishedPorts, labels under
	// configuration.labels.
	js := `[{"status":{"state":"running","networks":[
		{"network":"demo-net","ipv4Address":"192.168.66.4/24","ipv4Gateway":"192.168.66.1"}]},
		"configuration":{"labels":{"opossum.project":"demo"},
		"publishedPorts":[{"containerPort":8080,"hostAddress":"0.0.0.0","hostPort":8080,"proto":"tcp"}]}}]`
	info := inspectShim(t, js).Inspect("web")
	if !info.Exists || info.State != "running" {
		t.Errorf("Exists/State = %v/%q, want true/running", info.Exists, info.State)
	}
	if info.IP != "192.168.66.4" {
		t.Errorf("IP = %q, want 192.168.66.4", info.IP)
	}
	if info.Labels["opossum.project"] != "demo" {
		t.Errorf("Labels = %v", info.Labels)
	}
	if len(info.Ports) != 1 || info.Ports[0] != (PortMapping{HostAddress: "0.0.0.0", HostPort: 8080, ContainerPort: 8080, Proto: "tcp"}) {
		t.Errorf("Ports = %#v", info.Ports)
	}

	// A missing container is not found (inspect exits non-zero).
	if info := replayShim(t, "Error: container not found: ghost", 1).Inspect("ghost"); info.Exists {
		t.Errorf("missing container should be Exists=false, got %#v", info)
	}
}

func TestInspectLabel(t *testing.T) {
	// Real inspect shape: labels live under configuration.labels (see
	// testdata/real-cli-output.md).
	js := `[{"status":{"state":"running","networks":[]},"configuration":{"labels":{"opossum.project":"demo"}}}]`
	if v, ok := inspectShim(t, js).InspectLabel("db.opossum", "opossum.project"); !ok || v != "demo" {
		t.Errorf("InspectLabel = (%q, %v), want (\"demo\", true)", v, ok)
	}
	// Present but unlabeled: exists true, value empty.
	if v, ok := inspectShim(t, `[{"configuration":{"labels":{}}}]`).InspectLabel("x", "opossum.project"); !ok || v != "" {
		t.Errorf("unlabeled InspectLabel = (%q, %v), want (\"\", true)", v, ok)
	}
	// Missing container: inspect exits non-zero -> not exists.
	if v, ok := replayShim(t, "Error: container not found: ghost", 1).InspectLabel("ghost", "opossum.project"); ok || v != "" {
		t.Errorf("missing-container InspectLabel = (%q, %v), want (\"\", false)", v, ok)
	}
}

func TestRunWithEntrypoint(t *testing.T) {
	rt, read := loggingShim(t)
	// `container run --entrypoint` takes only the executable; the rest of the
	// entrypoint goes positional (before the command) so the container runs
	// entrypoint ++ command = /app/run --serve web -c cfg.
	err := rt.Run(RunOptions{
		Name:       "web.demo.opossum",
		Image:      "web:latest",
		Entrypoint: []string{"/app/run", "--serve", "web"},
		Command:    []string{"-c", "cfg"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := lastLine(t, read)
	want := "run --name web.demo.opossum --entrypoint /app/run web:latest --serve web -c cfg"
	if got != want {
		t.Errorf("entrypoint argv mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestRunForwardsSSHAgent(t *testing.T) {
	rt, read := loggingShim(t)
	if err := rt.Run(RunOptions{Name: "agent", Image: "alpine", SSH: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := lastLine(t, read), "run --ssh --name agent alpine"; got != want {
		t.Errorf("SSH argv mismatch\n got: %s\nwant: %s", got, want)
	}
	// Unset SSH must not emit the flag.
	rt2, read2 := loggingShim(t)
	if err := rt2.Run(RunOptions{Name: "plain", Image: "alpine"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := lastLine(t, read2); strings.Contains(got, "--ssh") {
		t.Errorf("--ssh should not appear when SSH is unset: %s", got)
	}
}

func TestRunOmitsDetachAndDNSWhenUnset(t *testing.T) {
	rt, read := loggingShim(t)
	if err := rt.Run(RunOptions{Name: "solo", Image: "busybox", Network: "demo-net", Detach: false}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := lastLine(t, read)
	if want := "run --name solo --network demo-net busybox"; got != want {
		t.Errorf("Run (no detach/dns) mismatch\n got: %s\nwant: %s", got, want)
	}
	if strings.Contains(got, "-d ") || strings.Contains(got, "--dns") {
		t.Errorf("unexpected -d/--dns flag: %s", got)
	}
}

func TestBuildAssemblesArgv(t *testing.T) {
	rt, read := loggingShim(t)
	if err := rt.Build(BuildOptions{
		Tag:        "demo-api:latest",
		Context:    "/ctx",
		Dockerfile: "Dockerfile.api",
		Args:       []string{"VERSION=1", "MODE=prod"},
	}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := lastLine(t, read)
	want := "build --progress plain -t demo-api:latest -f Dockerfile.api --build-arg VERSION=1 --build-arg MODE=prod /ctx"
	if got != want {
		t.Errorf("Build argv mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestBuildTargetArgv(t *testing.T) {
	rt, read := loggingShim(t)
	if err := rt.Build(BuildOptions{Tag: "app:latest", Context: "/ctx", Target: "builder"}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := lastLine(t, read); got != "build --progress plain -t app:latest --target builder /ctx" {
		t.Errorf("Build --target argv = %q", got)
	}
}

func TestBuildDefaultsContextToDot(t *testing.T) {
	rt, read := loggingShim(t)
	if err := rt.Build(BuildOptions{Tag: "x:latest"}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := lastLine(t, read); got != "build --progress plain -t x:latest ." {
		t.Errorf("Build with empty context = %q, want trailing '.'", got)
	}
}

// A hung probe must not block forever: Exec with a timeout returns an error
// promptly instead of waiting for the (here, 10s-sleeping) command.
func TestExecTimesOut(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "container.sh")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nsleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{Bin: shim}
	start := time.Now()
	err := r.Exec("web", []string{"true"}, 200*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a timeout error for a hung probe")
	}
	if elapsed > 3*time.Second {
		t.Errorf("Exec should return near the timeout, took %s", elapsed)
	}
}

func TestExecStopDeleteArgv(t *testing.T) {
	rt, read := loggingShim(t)
	if err := rt.Exec("db.opossum", []string{"pg_isready", "-U", "postgres"}, 0); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if got := lastLine(t, read); got != "exec db.opossum pg_isready -U postgres" {
		t.Errorf("Exec argv = %q", got)
	}

	rt.Stop("db.opossum")
	if got := lastLine(t, read); got != "stop db.opossum" {
		t.Errorf("Stop argv = %q", got)
	}

	rt.Delete("db.opossum")
	if got := lastLine(t, read); got != "delete --force db.opossum" {
		t.Errorf("Delete argv = %q", got)
	}

	if err := rt.Start("db.opossum"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := lastLine(t, read); got != "start db.opossum" {
		t.Errorf("Start argv = %q", got)
	}
}

func TestExecStreamArgv(t *testing.T) {
	rt, read := loggingShim(t)

	if err := rt.ExecStream("web.opossum", []string{"ls", "-la"}, ExecOptions{}); err != nil {
		t.Fatalf("ExecStream: %v", err)
	}
	if got := lastLine(t, read); got != "exec web.opossum ls -la" {
		t.Errorf("plain ExecStream argv = %q", got)
	}

	if err := rt.ExecStream("web.opossum", []string{"sh"}, ExecOptions{Interactive: true, TTY: true}); err != nil {
		t.Fatalf("ExecStream: %v", err)
	}
	if got := lastLine(t, read); got != "exec -i -t web.opossum sh" {
		t.Errorf("interactive ExecStream argv = %q", got)
	}
}

func TestStartSurfacesError(t *testing.T) {
	// A non-existent container makes `container start` fail; Start returns it.
	if err := replayShim(t, "Error: container not found: ghost", 1).Start("ghost"); err == nil {
		t.Error("Start should surface an error when the container is missing")
	}
}

func TestLogsArgv(t *testing.T) {
	rt, read := loggingShim(t)

	// Plain: just the container id.
	if err := rt.Logs("web.opossum", LogsOptions{}); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if got := lastLine(t, read); got != "logs web.opossum" {
		t.Errorf("plain Logs argv = %q", got)
	}

	// Follow + tail assemble -f and -n before the id, matching the real CLI.
	if err := rt.Logs("web.opossum", LogsOptions{Follow: true, Tail: 20}); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if got := lastLine(t, read); got != "logs -f -n 20 web.opossum" {
		t.Errorf("follow+tail Logs argv = %q", got)
	}

	// Tail alone (no follow).
	if err := rt.Logs("db.opossum", LogsOptions{Tail: 5}); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if got := lastLine(t, read); got != "logs -n 5 db.opossum" {
		t.Errorf("tail-only Logs argv = %q", got)
	}
}

func TestPullAndKillArgv(t *testing.T) {
	rt, read := loggingShim(t)

	if err := rt.Pull("postgres:16"); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if got := lastLine(t, read); got != "image pull postgres:16" {
		t.Errorf("Pull argv = %q", got)
	}

	rt.Kill("web.opossum", "") // default signal -> no -s
	if got := lastLine(t, read); got != "kill web.opossum" {
		t.Errorf("Kill (default) argv = %q", got)
	}

	rt.Kill("web.opossum", "TERM")
	if got := lastLine(t, read); got != "kill -s TERM web.opossum" {
		t.Errorf("Kill (signal) argv = %q", got)
	}
}

func TestDeleteVolumeArgv(t *testing.T) {
	rt, read := loggingShim(t)
	rt.DeleteVolume("pgdata")
	if got := lastLine(t, read); got != "volume delete pgdata" {
		t.Errorf("DeleteVolume argv = %q", got)
	}
}

func TestEnsureAndDeleteNetworkArgv(t *testing.T) {
	rt, read := loggingShim(t)
	if _, err := rt.EnsureNetwork("demo-net"); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	if got := lastLine(t, read); got != "network create demo-net" {
		t.Errorf("EnsureNetwork argv = %q", got)
	}
	rt.DeleteNetwork("demo-net") // shim exits 0, so no warning path
	if got := lastLine(t, read); got != "network delete demo-net" {
		t.Errorf("DeleteNetwork argv = %q", got)
	}
}
