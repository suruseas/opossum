package orchestrator_test

// These evals verify the *command sequence* opossum emits against the container
// runtime — the argument-assembly logic that §5 of the project brief designates
// for the "fake layer". They run a fake `container` shim, capture every
// invocation it receives, and assert on the exact arguments and ordering.
// No real runtime is involved.

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// fakeShimInspect returns a Runtime whose `inspect` prints out and exits with
// code (other subcommands succeed silently) — for exercising Ps against a
// missing container.
func fakeShimInspect(t *testing.T, out string, code int) *runtime.Runtime {
	t.Helper()
	dir := t.TempDir()
	shim := filepath.Join(dir, "c.sh")
	// `system status` answers "running" so Ps/Images' liveness probe passes; the
	// fixed inspect output/code is what the test is actually exercising.
	script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n  inspect) echo %q; exit %d ;;\n  system) echo 'status running' ;;\nesac\nexit 0\n", out, code)
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &runtime.Runtime{Bin: shim}
}

// countLines returns how many captured invocations contain sub.
func countLines(lines []string, sub string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// fakeShim writes a small `container` stand-in that logs each invocation's
// arguments (one per line) to $FAKE_LOG and returns plausible output. It returns
// a Runtime pointed at the shim and a reader for the captured invocation lines.
func fakeShim(t *testing.T) (*runtime.Runtime, func() []string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "invocations.log")
	// Steer the compiled shim through the Runtime's per-child Env (not the process
	// environment), so tests need no t.Setenv and stay isolated. `exec` simulates a
	// healthcheck that fails until the HEALTH_OK_AT-th call (default 1 = healthy now).
	rt := &runtime.Runtime{Bin: fakeShimBin, Env: []string{
		"FAKE_LOG=" + logPath,
		"HEALTH_COUNTER=" + filepath.Join(dir, "health.count"),
		"STATE_DIR=" + dir, // remembers each run's config-hash for idempotency evals
	}}
	read := func() []string {
		b, err := os.ReadFile(logPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
		// The config-hash label is an implementation detail of change detection;
		// strip it so command-shape assertions stay stable. The dedicated skip
		// tests verify its effect (a second up doesn't recreate an unchanged one).
		for i, l := range lines {
			lines[i] = stripConfigHash(l)
		}
		return lines
	}
	return rt, read
}

// setShimEnv steers the fake shim by appending KEY=value entries to the
// Runtime's per-child environment — the process-env-free replacement for
// t.Setenv, so one test's shim settings never leak into another's.
func setShimEnv(rt *runtime.Runtime, kv ...string) {
	rt.Env = append(rt.Env, kv...)
}

// project builds a Project literal directly so evals control every field without
// YAML/path resolution noise.
// testBaseDir is a throwaway compose base directory shared by the tests, so that
// bind-mount resolution and `ensureBindDirs` (which creates missing host dirs)
// write under a temp dir instead of polluting the real /tmp (#132).
var testBaseDir string

// fakeShimBin is the compiled fake `container` shim, built once for the whole
// package. A compiled binary spawns in ~1-2ms versus ~50-80ms for a /bin/sh
// script, and the suite spawns it thousands of times — so this dominates runtime.
var fakeShimBin string

func TestMain(m *testing.M) {
	d, err := os.MkdirTemp("", "opossum-orch-test-")
	if err != nil {
		panic(err)
	}
	testBaseDir = d
	fakeShimBin = filepath.Join(d, "fakeshim")
	if out, err := exec.Command("go", "build", "-o", fakeShimBin, "./testdata/fakeshim").CombinedOutput(); err != nil {
		os.RemoveAll(d)
		panic(fmt.Sprintf("building fake shim: %v\n%s", err, out))
	}
	code := m.Run()
	os.RemoveAll(d)
	os.Exit(code)
}

func project(name string, svcs map[string]*compose.Service) *compose.Project {
	for n, s := range svcs {
		s.Name = n
	}
	return &compose.Project{Name: name, BaseDir: testBaseDir, Services: svcs}
}

// stripConfigHash removes the " -l opossum.config-hash=<hex>" token from a logged
// command so command-shape assertions don't depend on the hash value.
func stripConfigHash(line string) string {
	const tok = " -l opossum.config-hash="
	i := strings.Index(line, tok)
	if i < 0 {
		return line
	}
	j := i + len(tok)
	for j < len(line) && line[j] != ' ' {
		j++
	}
	return line[:i] + line[j:]
}

func hasLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

// indexOf returns the position of the first line containing sub, or -1.
func indexOf(lines []string, sub string) int {
	for i, l := range lines {
		if strings.Contains(l, sub) {
			return i
		}
	}
	return -1
}

// A service with `ssh: true` forwards the host SSH agent (--ssh); others don't.
func TestUpForwardsSSHWhenServiceOptsIn(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"builder": {Image: "ci:latest", SSH: true},
		"plain":   {Image: "app:latest"},
	})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	lines := log()
	if i := indexOf(lines, "--name builder.demo.opossum"); i < 0 || !strings.Contains(lines[i], "--ssh") {
		t.Errorf("builder run should include --ssh, got: %v", lines)
	}
	if i := indexOf(lines, "--name plain.demo.opossum"); i < 0 || strings.Contains(lines[i], "--ssh") {
		t.Errorf("plain run should not include --ssh, got: %v", lines)
	}
}

func TestUpEmitsOrderedCommands(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"db":    {Image: "postgres:16", Environment: compose.Environment{"POSTGRES_PASSWORD=secret"}},
		"cache": {Image: "redis:7"},
		"web": {
			Image:     "web:latest",
			Ports:     []string{"8080:8080"},
			DependsOn: compose.DependsOn{{Name: "db"}, {Name: "cache"}},
		},
	})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	lines := log()

	// Network is created before any container is touched (a foreign-owner
	// pre-flight may inspect first, but no delete/run precedes the network).
	netIdx := indexOf(lines, "network create demo-net")
	firstMutation := indexOf(lines, "delete --force")
	if netIdx < 0 || firstMutation < 0 || netIdx > firstMutation {
		t.Fatalf("network create should precede any container mutation, got net=%d firstDelete=%d in %v", netIdx, firstMutation, lines)
	}

	// Each service is force-deleted (stale cleanup) then run, with the DNS flags,
	// on the shared network, named "<svc>.<domain>".
	wantRun := map[string]string{
		"cache": "run -d --name cache.demo.opossum --network demo-net --dns-domain opossum --dns-search demo.opossum -l opossum.project=demo redis:7",
		"db":    "run -d --name db.demo.opossum --network demo-net --dns-domain opossum --dns-search demo.opossum -e POSTGRES_PASSWORD=secret -l opossum.project=demo postgres:16",
		"web":   "run -d --name web.demo.opossum --network demo-net --dns-domain opossum --dns-search demo.opossum -p 8080:8080 -l opossum.project=demo web:latest",
	}
	for svc, want := range wantRun {
		if !hasLine(lines, want) {
			t.Errorf("missing run for %s.\n want: %q\n got:  %v", svc, want, lines)
		}
		if !hasLine(lines, "delete --force "+svc+".demo.opossum") {
			t.Errorf("missing stale-delete for %s", svc)
		}
	}

	// web depends on db and cache, so both must be run before web.
	if r := indexOf(lines, "run -d --name web.demo.opossum"); r >= 0 {
		if d := indexOf(lines, "run -d --name db.demo.opossum"); d < 0 || d > r {
			t.Errorf("db must run before web (db=%d web=%d)", d, r)
		}
		if c := indexOf(lines, "run -d --name cache.demo.opossum"); c < 0 || c > r {
			t.Errorf("cache must run before web (cache=%d web=%d)", c, r)
		}
	}
}

func TestUpForegroundRejectsMultipleLongRunning(t *testing.T) {
	// Foreground can attach to only one long-running container, so `up --foreground`
	// of multiple services is rejected early rather than hanging on the first.
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web":   {Image: "web:latest"},
		"cache": {Image: "redis:7"},
	})
	err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(false)
	if err == nil || !strings.Contains(err.Error(), "foreground") {
		t.Errorf("foreground up of multiple services should be rejected, got err=%v", err)
	}
}

func TestUpForegroundAllowsSingleService(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(false); err != nil {
		t.Errorf("foreground up of a single service should be allowed, got %v", err)
	}
	// Foreground means attached: the container must run WITHOUT -d, else "up to
	// date"/logs behaviour differs from what the user asked for.
	var runLine string
	for _, l := range log() {
		if strings.Contains(l, "--name web.demo.opossum") && strings.HasPrefix(l, "run") {
			runLine = l
		}
	}
	if runLine == "" {
		t.Fatalf("web was never run, got %v", log())
	}
	if strings.HasPrefix(runLine, "run -d") {
		t.Errorf("foreground up must not detach, got: %s", runLine)
	}
}

func TestUpForegroundIgnoresOneShotDeps(t *testing.T) {
	// A one-shot (completed) dependency runs to completion and doesn't block, so a
	// single long-running service plus a one-shot dep is still a valid foreground up.
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"migrate": {Image: "alpine:3.20"},
		"web": {
			Image:     "web:latest",
			DependsOn: compose.DependsOn{{Name: "migrate", Condition: compose.ConditionCompleted}},
		},
	})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(false); err != nil {
		t.Errorf("single long-running service + one-shot dep should be allowed in foreground, got %v", err)
	}
}

func TestUpFailsWhenHostPortInUse(t *testing.T) {
	// Occupy a host port, then a project that publishes it must fail pre-flight
	// with a clear message (not the runtime's raw bind error mid-startup).
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Ports: []string{fmt.Sprintf("127.0.0.1:%d:80", port)}},
	})
	err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
	if err == nil || !strings.Contains(err.Error(), "already in use") || !strings.Contains(err.Error(), "[OPSM-201]") {
		t.Errorf("up should fail with code OPSM-201 when a published host port is in use, got %v", err)
	}
}

// A service that declares MCP tools gets a generated .mcp.json bind-mounted at the
// documented in-container path (and the file holds the resolved URL). Guards the
// whole wiring: parse → generate → mount (#258).
func TestUpMountsGeneratedMCPConfig(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"agent": {Image: "agent:latest", MCPTools: []string{"tf"}},
		"tf":    {Image: "tf:latest", Ports: compose.Ports{"8080:8080"}},
	})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true, "agent"); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if joined := strings.Join(log(), "\n"); !strings.Contains(joined, ":"+"/run/opossum/mcp.json:ro") {
		t.Errorf("agent run must mount the generated MCP config read-only, got:\n%s", joined)
	}
	data, err := os.ReadFile(filepath.Join(p.BaseDir, ".opossum", "mcp", "agent.json"))
	if err != nil {
		t.Fatalf("generated MCP config not written: %v", err)
	}
	if !strings.Contains(string(data), `"http://tf:8080/mcp"`) {
		t.Errorf("generated MCP config has the wrong URL:\n%s", data)
	}
}

// `up --dry-run` shows the MCP mount in the plan but writes no host file — a dry
// run must touch nothing on disk.
func TestUpDryRunDoesNotWriteMCPConfig(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"agent": {Image: "agent:latest", MCPTools: []string{"tf"}},
		"tf":    {Image: "tf:latest", Ports: compose.Ports{"8080:8080"}},
	})
	p.BaseDir = t.TempDir() // isolated, so we can assert nothing was written
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	o.SetDryRun(true)
	if err := o.Up(true, "agent"); err != nil {
		t.Fatalf("Up (dry-run): %v", err)
	}
	if !strings.Contains(out.String(), "/run/opossum/mcp.json:ro") {
		t.Errorf("the plan should still show the MCP mount, got:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(p.BaseDir, ".opossum", "mcp", "agent.json")); !os.IsNotExist(err) {
		t.Errorf("dry-run must not write the MCP config file (stat err = %v)", err)
	}
}

// RunAudited assembles a report even when a dimension can't be observed: a service
// with no working_dir bind mount and no proxy must report files AND egress as
// unobserved WITH a reason (never a blank that reads as "nothing happened"), and
// still carry the exit code. Guards the audit orchestration skeleton (#261).
func TestRunAuditedMarksUnobservedDimensions(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest"}, // no working_dir bind, no proxy env
	})
	report, err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).
		RunAudited("web", []string{"echo", "hi"}, orchestrator.RunOneOffOptions{Rm: true})
	if err != nil {
		t.Fatalf("RunAudited: %v", err)
	}
	if report.ExitCode != 0 {
		t.Errorf("a passing fake run should report exit 0, got %d", report.ExitCode)
	}
	if report.Files.Observed || report.Files.Reason == "" {
		t.Errorf("no working_dir bind → files must be unobserved with a reason, got %+v", report.Files)
	}
	if report.Egress.Observed || report.Egress.Reason == "" {
		t.Errorf("no proxy → egress must be unobserved with a reason, got %+v", report.Egress)
	}
}

func TestUpSkipsOwnRunningContainerPort(t *testing.T) {
	// A re-up must not flag its OWN already-running container's published port as a
	// conflict: up will delete and recreate that container, freeing the port. The
	// port stays bound here, but because inspect reports the container as running
	// and owned by this project, checkHostPorts skips it and up proceeds — the
	// distinction that keeps a plain re-up from failing on ports it already holds.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	rt, _ := fakeShim(t)
	setShimEnv(rt, "INSPECT_PROJECT=demo") // web's container is running (shim default) and ours
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Ports: []string{fmt.Sprintf("127.0.0.1:%d:80", port)}},
	})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Errorf("re-up must skip its own running container's published port, got %v", err)
	}
}

func TestUpAllowsFreeHostPort(t *testing.T) {
	// Grab a port then release it, so it's (almost certainly) free for the up.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Ports: []string{fmt.Sprintf("127.0.0.1:%d:80", port)}},
	})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Errorf("up should succeed when the host port is free, got %v", err)
	}
}

func TestUpPassesPlatform(t *testing.T) {
	// An amd64 platform reaches `container run --platform` and adds `--rosetta`
	// (x86-64 emulation on Apple silicon); an arm64 platform adds only --platform.
	run := func(platform string) string {
		rt, log := fakeShim(t)
		p := project("demo", map[string]*compose.Service{
			"cache": {Image: "img", Platform: platform},
		})
		if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		for _, l := range log() {
			if strings.HasPrefix(l, "run -d") {
				return l
			}
		}
		return ""
	}
	if l := run("linux/amd64"); !strings.Contains(l, "--platform linux/amd64") || !strings.Contains(l, "--rosetta") {
		t.Errorf("amd64 should add --platform and --rosetta, got %q", l)
	}
	if l := run("linux/arm64"); !strings.Contains(l, "--platform linux/arm64") || strings.Contains(l, "--rosetta") {
		t.Errorf("arm64 should add --platform without --rosetta, got %q", l)
	}
	if l := run(""); strings.Contains(l, "--platform") || strings.Contains(l, "--rosetta") {
		t.Errorf("no platform should add neither flag, got %q", l)
	}
}

func TestUpPassesEntrypoint(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {
			Image:      "web:latest",
			Entrypoint: compose.Command{"/app/run", "--serve"},
			Command:    compose.Command{"-c", "cfg"},
		},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	// --entrypoint takes the executable; the rest goes positional before command.
	want := "run -d --name web.demo.opossum --network demo-net --dns-domain opossum --dns-search demo.opossum " +
		"-l opossum.project=demo --entrypoint /app/run web:latest --serve -c cfg"
	if !hasLine(log(), want) {
		t.Errorf("expected entrypoint to be assembled, got %v", log())
	}
}

// Ignored service fields don't affect startup, so `up` never warns per field by
// default — but silence let agents cargo-cult invalid config, so a one-line note
// points at `opossum config`. --verbose still gives the full per-field warning.
func TestUpIgnoredServiceFieldsNoteAndVerbose(t *testing.T) {
	upOutput := func(verbose bool) string {
		rt, _ := fakeShim(t)
		rt.Verbose = verbose
		p := project("demo", map[string]*compose.Service{
			"web": {Image: "web:latest", Unsupported: []string{"container_name", "restart"}},
		})
		var out bytes.Buffer
		if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		return out.String()
	}
	// Default: one low-key note naming a representative field + pointing to config —
	// not a per-field warning.
	def := upOutput(false)
	if !strings.Contains(def, "note:") || !strings.Contains(def, "2 compose fields are ignored") ||
		!strings.Contains(def, "web: container_name") || !strings.Contains(def, "opossum config") {
		t.Errorf("default up should print a one-line ignored-fields note, got:\n%s", def)
	}
	if strings.Contains(def, "unsupported field") {
		t.Errorf("default up should note, not warn per field, got:\n%s", def)
	}
	// Verbose: the full per-field warning, and no summary note (no double-report).
	v := upOutput(true)
	if !strings.Contains(v, "unsupported field") || !strings.Contains(v, "container_name") || !strings.Contains(v, "restart") {
		t.Errorf("--verbose should name every ignored field, got:\n%s", v)
	}
	if strings.Contains(v, "note:") {
		t.Errorf("--verbose should not also print the summary note, got:\n%s", v)
	}
}

func TestUpTopLevelIgnoredFieldsNoteAndVerbose(t *testing.T) {
	upOutput := func(verbose bool) string {
		rt, _ := fakeShim(t)
		rt.Verbose = verbose
		p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
		p.Unsupported = []string{"networks", "volumes"}
		var out bytes.Buffer
		if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		return out.String()
	}
	if got := upOutput(false); !strings.Contains(got, "note:") || !strings.Contains(got, "top-level: networks") {
		t.Errorf("default up should note the ignored top-level fields, got:\n%s", got)
	}
	if got := upOutput(true); !strings.Contains(got, "unsupported top-level field(s): networks, volumes") {
		t.Errorf("--verbose should show the top-level ignored fields, got:\n%s", got)
	}
}

// A clean compose (nothing ignored) prints no note at all — the note only appears
// when there's actually a dropped field to surface.
func TestUpNoNoteWhenNothingIgnored(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if got := out.String(); strings.Contains(got, "note:") || strings.Contains(got, "compose field") {
		t.Errorf("no ignored-fields note when nothing is ignored, got:\n%s", got)
	}
}

func TestUpBuildsAndTags(t *testing.T) {
	rt, log := fakeShim(t)
	setShimEnv(rt, "IMAGE_ABSENT=demo-api:latest") // a fresh build: the image isn't present yet
	p := project("demo", map[string]*compose.Service{
		"api": {Build: &compose.Build{Context: "/ctx"}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	lines := log()
	if !hasLine(lines, "build --progress plain -t demo-api:latest /ctx") {
		t.Errorf("expected build with project-scoped tag, got %v", lines)
	}
	// The built image tag is what gets run.
	if indexOf(lines, "--name api.demo.opossum --network demo-net --dns-domain opossum --dns-search demo.opossum -l opossum.project=demo demo-api:latest") < 0 {
		t.Errorf("expected api to run the built image demo-api:latest, got %v", lines)
	}
}

func TestUpBuildTargetFlag(t *testing.T) {
	// A multi-stage build target must reach `container build` as --target, so a
	// service that pins a stage builds that stage rather than the final one (#75).
	rt, log := fakeShim(t)
	setShimEnv(rt, "IMAGE_ABSENT=demo-api:latest")
	p := project("demo", map[string]*compose.Service{
		"api": {Build: &compose.Build{Context: "/ctx", Target: "builder"}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !hasLine(log(), "build --progress plain -t demo-api:latest --target builder /ctx") {
		t.Errorf("expected build to pass --target builder, got %v", log())
	}
}

func TestBuildContextUnreadableWarns(t *testing.T) {
	// A build context the container builder can't read gets a hint, not a silent
	// failure at COPY time (#83): under /private/tmp, or a symlinked directory.
	t.Run("under /private/tmp", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "IMAGE_ABSENT=demo-api:latest")
		var out bytes.Buffer
		p := project("demo", map[string]*compose.Service{
			"api": {Build: &compose.Build{Context: "/private/tmp/ctx"}},
		})
		o := orchestrator.New(p, rt, "opossum", &out)
		if err := o.Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if !strings.Contains(out.String(), "under /private/tmp") {
			t.Errorf("expected a /private/tmp build-context warning, got:\n%s", out.String())
		}
	})

	t.Run("symlinked context", func(t *testing.T) {
		dir := t.TempDir()
		real := filepath.Join(dir, "real")
		if err := os.Mkdir(real, 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		rt, _ := fakeShim(t)
		setShimEnv(rt, "IMAGE_ABSENT=demo-api:latest")
		var out bytes.Buffer
		p := project("demo", map[string]*compose.Service{
			"api": {Build: &compose.Build{Context: link}},
		})
		o := orchestrator.New(p, rt, "opossum", &out)
		if err := o.Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if !strings.Contains(out.String(), "is a symlink") {
			t.Errorf("expected a symlink build-context warning, got:\n%s", out.String())
		}
	})

	t.Run("normal context: no warning", func(t *testing.T) {
		dir := t.TempDir()
		ctx := filepath.Join(dir, "app") // a real, non-symlink dir (not under /private/tmp)
		if err := os.Mkdir(ctx, 0o755); err != nil {
			t.Fatal(err)
		}
		rt, _ := fakeShim(t)
		var out bytes.Buffer
		p := project("demo", map[string]*compose.Service{
			"api": {Build: &compose.Build{Context: ctx}},
		})
		o := orchestrator.New(p, rt, "opossum", &out)
		if err := o.Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if strings.Contains(out.String(), "warning: build context") {
			t.Errorf("a normal build context must not warn, got:\n%s", out.String())
		}
	})
}

func TestUpMountsFileSecrets(t *testing.T) {
	// A file-based secret is mounted read-only at /run/secrets/<target>, where
	// official images read it via their *_FILE env vars (#76). The short ref
	// uses the secret name; the long ref sets a distinct target.
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"db": {Image: "postgres:16", Secrets: compose.SecretRefs{
			{Source: "db-password", Target: "db-password"},
			{Source: "api-key", Target: "api_key"},
		}},
	})
	p.Secrets = map[string]compose.Secret{
		"db-password": {File: "/secrets/pw.txt"},
		"api-key":     {File: "/secrets/api.txt"},
	}
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if indexOf(log(), "-v /secrets/pw.txt:/run/secrets/db-password:ro") < 0 {
		t.Errorf("expected db-password secret mounted read-only, got %v", log())
	}
	if indexOf(log(), "-v /secrets/api.txt:/run/secrets/api_key:ro") < 0 {
		t.Errorf("expected api-key secret mounted at its target, got %v", log())
	}
}

func TestUpMountsTmpfs(t *testing.T) {
	// tmpfs targets are passed as `--tmpfs <path>` (not `-v`), so a service can
	// mount an in-memory filesystem (#79).
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "nginx", Tmpfs: []string{"/tmp", "/run"}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if indexOf(log(), "--tmpfs /tmp") < 0 || indexOf(log(), "--tmpfs /run") < 0 {
		t.Errorf("expected --tmpfs mounts, got %v", log())
	}
}

func TestUpWithoutDNSDomainUsesBareNames(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"solo": {Image: "busybox"},
	})
	o := orchestrator.New(p, rt, "", &bytes.Buffer{}) // no DNS domain
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	lines := log()
	if !hasLine(lines, "run -d --name solo --network demo-net -l opossum.project=demo busybox") {
		t.Errorf("without a DNS domain, expected bare container name and no --dns-* flags, got %v", lines)
	}
	for _, l := range lines {
		if strings.Contains(l, "--dns-domain") || strings.Contains(l, "--dns-search") {
			t.Errorf("unexpected DNS flag with empty domain: %q", l)
		}
	}
}

// network_mode: none isolates a service — it must reach `--network none` (not the
// project network) and, being networkless, carry no DNS flags. A sibling on the
// default network still joins the project net and resolves peers by name, so the
// isolation is per-service.
func TestUpNetworkModeNoneIsolatesService(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"agent": {Image: "agent:latest", NetworkMode: compose.NetworkModeNone},
		"peer":  {Image: "peer:latest"},
	})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	lines := log()
	if !hasLine(lines, "run -d --name agent.demo.opossum --network none -l opossum.project=demo agent:latest") {
		t.Errorf("isolated service should get --network none and no DNS flags, got %v", lines)
	}
	for _, l := range lines {
		if strings.Contains(l, "agent.demo.opossum") && (strings.Contains(l, "--dns-domain") || strings.Contains(l, "--dns-search") || strings.Contains(l, "demo-net")) {
			t.Errorf("isolated service must not join the project net or get DNS flags: %q", l)
		}
	}
	// The sibling still joins the project network with DNS for name resolution.
	if !hasLine(lines, "run -d --name peer.demo.opossum --network demo-net --dns-domain opossum --dns-search demo.opossum -l opossum.project=demo peer:latest") {
		t.Errorf("default-network sibling should keep project net + DNS, got %v", lines)
	}
}

// A service on a declared internal network joins the namespaced host-only network
// (created with --internal), while a sibling with no `networks:` stays on the
// default project net. `up` also warns that the internal network blocks egress.
func TestUpInternalNetworkCreatesAndAttaches(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"agent": {Image: "agent:latest", Networks: compose.ServiceNetworks{"caged"}},
		"peer":  {Image: "peer:latest"},
	})
	p.Networks = map[string]compose.NetworkDecl{"caged": {Internal: true}}
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	lines := log()
	// The internal network is created host-only, namespaced <project>-<name>.
	if !hasLine(lines, "network create --internal demo-caged") {
		t.Errorf("internal network should be created with --internal, got %v", lines)
	}
	// The default project net is still created for the sibling.
	if !hasLine(lines, "network create demo-net") {
		t.Errorf("default project net should still be created, got %v", lines)
	}
	// agent joins the internal net; peer stays on the default net.
	if !hasLine(lines, "run -d --name agent.demo.opossum --network demo-caged --dns-domain opossum --dns-search demo.opossum -l opossum.project=demo agent:latest") {
		t.Errorf("agent should join the namespaced internal net, got %v", lines)
	}
	if !hasLine(lines, "run -d --name peer.demo.opossum --network demo-net --dns-domain opossum --dns-search demo.opossum -l opossum.project=demo peer:latest") {
		t.Errorf("peer should stay on the default net, got %v", lines)
	}
	// The internal-network egress caveat is surfaced to the user.
	if !strings.Contains(out.String(), "internal (host-only)") || !strings.Contains(out.String(), "no internet egress") {
		t.Errorf("up should warn that the internal network blocks egress, got:\n%s", out.String())
	}
}

// A service may join several declared networks: each becomes a `--network` (in
// declaration order), and every non-external one is created.
func TestUpAttachesMultipleNetworks(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"app": {Image: "app:latest", Networks: compose.ServiceNetworks{"front", "back"}},
	})
	p.Networks = map[string]compose.NetworkDecl{"front": {}, "back": {Internal: true}}
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	lines := log()
	// Both networks created (front plain, back internal).
	if !hasLine(lines, "network create demo-front") {
		t.Errorf("expected plain network demo-front created, got %v", lines)
	}
	if !hasLine(lines, "network create --internal demo-back") {
		t.Errorf("expected internal network demo-back created, got %v", lines)
	}
	// The service joins both, in declaration order (front then back).
	if !hasLine(lines, "run -d --name app.demo.opossum --network demo-front --network demo-back --dns-domain opossum --dns-search demo.opossum -l opossum.project=demo app:latest") {
		t.Errorf("service should get one --network per declared net in order, got %v", lines)
	}
}

// Swapping a service's network set recreates it (the config hash covers the set).
func TestUpRecreatesOnNetworkSetChange(t *testing.T) {
	rt, log := fakeShim(t)
	svc := &compose.Service{Image: "app:latest", Networks: compose.ServiceNetworks{"a"}}
	p := project("demo", map[string]*compose.Service{"app": svc})
	p.Networks = map[string]compose.NetworkDecl{"a": {}, "b": {}}
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}
	svc.Networks = compose.ServiceNetworks{"a", "b"} // joined an extra network
	if err := o.Up(true); err != nil {
		t.Fatalf("second up: %v", err)
	}
	if n := countLines(log(), "--name app.demo.opossum"); n != 2 {
		t.Errorf("adding a network should recreate the container, want 2 runs got %d", n)
	}
}

// Reordering a service's networks changes which one becomes eth0, so the emitted
// --network order (and thus the container) must change — the config hash tracks
// declaration order, not just set membership.
func TestUpRecreatesOnNetworkReorder(t *testing.T) {
	rt, log := fakeShim(t)
	svc := &compose.Service{Image: "app:latest", Networks: compose.ServiceNetworks{"a", "b"}}
	p := project("demo", map[string]*compose.Service{"app": svc})
	p.Networks = map[string]compose.NetworkDecl{"a": {}, "b": {}}
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}
	svc.Networks = compose.ServiceNetworks{"b", "a"} // same set, different order
	if err := o.Up(true); err != nil {
		t.Fatalf("second up: %v", err)
	}
	if n := countLines(log(), "--name app.demo.opossum"); n != 2 {
		t.Errorf("reordering networks should recreate the container, want 2 runs got %d", n)
	}
}

// An external network is used by its real name and never created or deleted by
// opossum (it's owned outside the project).
func TestExternalNetworkNotManaged(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"app": {Image: "app:latest", Networks: compose.ServiceNetworks{"shared"}},
	})
	p.Networks = map[string]compose.NetworkDecl{"shared": {External: true, Name: "prod-shared"}}
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	lines := log()
	if !hasLine(lines, "run -d --name app.demo.opossum --network prod-shared --dns-domain opossum --dns-search demo.opossum -l opossum.project=demo app:latest") {
		t.Errorf("service should join the external net by its real name, got %v", lines)
	}
	for _, l := range lines {
		if strings.Contains(l, "network create") && strings.Contains(l, "prod-shared") {
			t.Errorf("opossum must not create an external network: %q", l)
		}
	}
	// down must not delete the external network either.
	rt2, log2 := fakeShim(t)
	o2 := orchestrator.New(p, rt2, "opossum", &bytes.Buffer{})
	if err := o2.Down(false, "", false); err != nil {
		t.Fatalf("Down: %v", err)
	}
	for _, l := range log2() {
		if strings.Contains(l, "network delete") && strings.Contains(l, "prod-shared") {
			t.Errorf("opossum must not delete an external network: %q", l)
		}
	}
}

// down removes the default project net and every declared non-external network
// opossum namespaces, so a re-`up` starts clean.
func TestDownDeletesDeclaredNetworks(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"agent": {Image: "agent:latest", Networks: compose.ServiceNetworks{"caged"}},
	})
	p.Networks = map[string]compose.NetworkDecl{"caged": {Internal: true}}
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Down(false, "", false); err != nil {
		t.Fatalf("Down: %v", err)
	}
	lines := log()
	if !hasLine(lines, "network delete demo-net") {
		t.Errorf("down should delete the default project net, got %v", lines)
	}
	if !hasLine(lines, "network delete demo-caged") {
		t.Errorf("down should delete the declared namespaced net, got %v", lines)
	}
}

func TestDownTearsDownInReverse(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"db":  {Image: "postgres:16"},
		"web": {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "db"}}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Down(false, "", false); err != nil {
		t.Fatalf("Down: %v", err)
	}
	lines := log()

	// web (dependent) is stopped before db (dependency); network deleted last.
	sWeb := indexOf(lines, "stop web.demo.opossum")
	sDB := indexOf(lines, "stop db.demo.opossum")
	if sWeb < 0 || sDB < 0 || sWeb > sDB {
		t.Errorf("web should stop before db (web=%d db=%d) in %v", sWeb, sDB, lines)
	}
	if net := indexOf(lines, "network delete demo-net"); net != len(lines)-1 {
		t.Errorf("network delete should be last, got index %d of %d", net, len(lines))
	}
	if !hasLine(lines, "delete --force web.demo.opossum") || !hasLine(lines, "delete --force db.demo.opossum") {
		t.Errorf("expected force-delete of both containers, got %v", lines)
	}
	// down also clears any leftover one-off (`run` without --rm) containers.
	if !hasLine(lines, "delete --force web-run.demo.opossum") || !hasLine(lines, "delete --force db-run.demo.opossum") {
		t.Errorf("expected down to also delete one-off containers, got %v", lines)
	}
}

func TestBuildAndPullSelectByServiceKind(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"db":  {Image: "postgres:16"},
		"api": {Build: &compose.Build{Context: "/ctx"}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})

	if err := o.Build(nil); err != nil {
		t.Fatalf("Build: %v", err)
	}
	lines := log()
	// Only the build service is built; the image-only service is skipped.
	if !hasLine(lines, "build --progress plain -t demo-api:latest /ctx") {
		t.Errorf("expected api to be built, got %v", lines)
	}
	if countLines(lines, "build ") != 1 {
		t.Errorf("only one build expected (api), got %v", lines)
	}

	rt2, log2 := fakeShim(t)
	o2 := orchestrator.New(p, rt2, "opossum", &bytes.Buffer{})
	if err := o2.Pull(nil); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	// Only the image service is pulled; the build-only service is skipped.
	if !hasLine(log2(), "image pull postgres:16") {
		t.Errorf("expected db image to be pulled, got %v", log2())
	}
	if countLines(log2(), "image pull") != 1 {
		t.Errorf("only one pull expected (db), got %v", log2())
	}
}

func TestStartInOrderAndKillInReverse(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"db":  {Image: "postgres:16"},
		"web": {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "db"}}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})

	if err := o.Start(nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	lines := log()
	if d, w := indexOf(lines, "start db.demo.opossum"), indexOf(lines, "start web.demo.opossum"); d < 0 || d > w {
		t.Errorf("db should start before web (db=%d web=%d)", d, w)
	}

	rt2, log2 := fakeShim(t)
	o2 := orchestrator.New(p, rt2, "opossum", &bytes.Buffer{})
	if err := o2.Kill(nil, "TERM"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	kl := log2()
	// Reverse order (dependents first) and the signal is applied.
	sw, sd := indexOf(kl, "kill -s TERM web.demo.opossum"), indexOf(kl, "kill -s TERM db.demo.opossum")
	if sw < 0 || sd < 0 || sw > sd {
		t.Errorf("web should be killed before db with -s TERM (web=%d db=%d) in %v", sw, sd, kl)
	}
}

func runOneOffProject() *compose.Project {
	return project("demo", map[string]*compose.Service{
		"db":  {Image: "postgres:16"},
		"web": {Image: "web:latest", Command: compose.Command{"serve"}, DependsOn: compose.DependsOn{{Name: "db"}}},
	})
}

func TestRunOneOffStartsDepsAndOverridesCommand(t *testing.T) {
	rt, log := fakeShim(t)
	o := orchestrator.New(runOneOffProject(), rt, "opossum", &bytes.Buffer{})
	if err := o.RunOneOff("web", []string{"echo", "hi"}, orchestrator.RunOneOffOptions{}); err != nil {
		t.Fatalf("RunOneOff: %v", err)
	}
	lines := log()
	// Dependency db is started first (detached), then the one-off runs foreground
	// under a distinct name, with the overridden command and no published ports.
	// The one-off keeps stdin connected (-i) so piped input reaches the process,
	// but gets no TTY unless asked (a pty would echo into piped streams).
	dbRun := indexOf(lines, "run -d --name db.demo.opossum")
	oneOff := indexOf(lines, "run -i --name web-run.demo.opossum")
	if dbRun < 0 || oneOff < 0 || dbRun > oneOff {
		t.Fatalf("db should start before the one-off (db=%d one-off=%d) in %v", dbRun, oneOff, lines)
	}
	if !hasLine(lines, "run -i --name web-run.demo.opossum --network demo-net --dns-domain opossum --dns-search demo.opossum -l opossum.project=demo web:latest echo hi") {
		t.Errorf("one-off run mismatch, got %v", lines)
	}
	// The one-off is foreground (no -d) and publishes no ports.
	if indexOf(lines, "run -d --name web-run.demo.opossum") >= 0 {
		t.Error("one-off must run in the foreground (no -d)")
	}
	// The dependency (a service, not a one-off) must NOT get -i.
	if indexOf(lines, "run -d -i") >= 0 {
		t.Errorf("detached services must not attach stdin, got %v", lines)
	}
}

func TestRunOneOffTTY(t *testing.T) {
	// With TTY requested (CLI stdin is a terminal), the one-off gets -i AND -t.
	rt, log := fakeShim(t)
	o := orchestrator.New(runOneOffProject(), rt, "opossum", &bytes.Buffer{})
	if err := o.RunOneOff("web", nil, orchestrator.RunOneOffOptions{NoDeps: true, TTY: true}); err != nil {
		t.Fatalf("RunOneOff: %v", err)
	}
	if indexOf(log(), "run -i -t --name web-run.demo.opossum") < 0 {
		t.Errorf("TTY one-off should pass -i -t, got %v", log())
	}
}

func TestRunOneOffForwardsSSH(t *testing.T) {
	// The --ssh flag forwards the agent for a one-off, even when the service
	// itself didn't opt in.
	rt, log := fakeShim(t)
	o := orchestrator.New(runOneOffProject(), rt, "opossum", &bytes.Buffer{})
	if err := o.RunOneOff("web", nil, orchestrator.RunOneOffOptions{NoDeps: true, SSH: true}); err != nil {
		t.Fatalf("RunOneOff: %v", err)
	}
	if lines := log(); indexOf(lines, "--name web-run.demo.opossum") < 0 || indexOf(lines, "--ssh") < 0 {
		t.Errorf("--ssh flag should forward the agent for a one-off, got %v", lines)
	}

	// A service with ssh: true forwards without the flag; a plain one never does.
	p := project("demo", map[string]*compose.Service{
		"sshsvc": {Image: "ci:latest", SSH: true},
		"plain":  {Image: "app:latest"},
	})
	rt2, log2 := fakeShim(t)
	o2 := orchestrator.New(p, rt2, "opossum", &bytes.Buffer{})
	if err := o2.RunOneOff("sshsvc", nil, orchestrator.RunOneOffOptions{NoDeps: true}); err != nil {
		t.Fatalf("RunOneOff: %v", err)
	}
	if lines := log2(); indexOf(lines, "--name sshsvc-run.demo.opossum") < 0 || indexOf(lines, "--ssh") < 0 {
		t.Errorf("service ssh:true should forward for a one-off, got %v", lines)
	}
	rt3, log3 := fakeShim(t)
	o3 := orchestrator.New(p, rt3, "opossum", &bytes.Buffer{})
	if err := o3.RunOneOff("plain", nil, orchestrator.RunOneOffOptions{NoDeps: true}); err != nil {
		t.Fatalf("RunOneOff: %v", err)
	}
	if i := indexOf(log3(), "--name plain-run.demo.opossum"); i < 0 || strings.Contains(log3()[i], "--ssh") {
		t.Errorf("plain one-off should not forward ssh, got %v", log3())
	}
}

// A one-off of a network_mode: none service is isolated the same way `up` is:
// `--network none` and no DNS flags (the docs promise up/run parity).
func TestRunOneOffNetworkModeNone(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"agent": {Image: "agent:latest", NetworkMode: compose.NetworkModeNone},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.RunOneOff("agent", nil, orchestrator.RunOneOffOptions{NoDeps: true}); err != nil {
		t.Fatalf("RunOneOff: %v", err)
	}
	lines := log()
	if !hasLine(lines, "run -i --name agent-run.demo.opossum --network none -l opossum.project=demo agent:latest") {
		t.Errorf("isolated one-off should get --network none and no DNS flags, got %v", lines)
	}
	if i := indexOf(lines, "--name agent-run.demo.opossum"); i >= 0 {
		if l := lines[i]; strings.Contains(l, "demo-net") || strings.Contains(l, "--dns-domain") || strings.Contains(l, "--dns-search") {
			t.Errorf("isolated one-off must not join the project net or get DNS flags: %q", l)
		}
	}
}

// A one-off on a declared internal network joins the namespaced host-only net and
// gets the same egress/name-resolution warning `up` emits (up/run parity).
func TestRunOneOffInternalNetworkWarnsAndAttaches(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"agent": {Image: "agent:latest", Networks: compose.ServiceNetworks{"caged"}},
	})
	p.Networks = map[string]compose.NetworkDecl{"caged": {Internal: true}}
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	if err := o.RunOneOff("agent", nil, orchestrator.RunOneOffOptions{NoDeps: true}); err != nil {
		t.Fatalf("RunOneOff: %v", err)
	}
	lines := log()
	if !hasLine(lines, "network create --internal demo-caged") {
		t.Errorf("one-off should create the internal net with --internal, got %v", lines)
	}
	if i := indexOf(lines, "--name agent-run.demo.opossum"); i < 0 || !strings.Contains(lines[i], "--network demo-caged") {
		t.Errorf("one-off should join the namespaced internal net, got %v", lines)
	}
	if !strings.Contains(out.String(), "internal (host-only)") || !strings.Contains(out.String(), "no internet egress") {
		t.Errorf("one-off on an internal net should warn about egress, got:\n%s", out.String())
	}
}

func TestRunOneOffNoDeps(t *testing.T) {
	rt, log := fakeShim(t)
	o := orchestrator.New(runOneOffProject(), rt, "opossum", &bytes.Buffer{})
	if err := o.RunOneOff("web", nil, orchestrator.RunOneOffOptions{NoDeps: true}); err != nil {
		t.Fatalf("RunOneOff: %v", err)
	}
	lines := log()
	if indexOf(lines, "run -d --name db.demo.opossum") >= 0 {
		t.Errorf("--no-deps must not start db, got %v", lines)
	}
	// Falls back to the service's own command when none is given.
	if !hasLine(lines, "run -i --name web-run.demo.opossum --network demo-net --dns-domain opossum --dns-search demo.opossum -l opossum.project=demo web:latest serve") {
		t.Errorf("expected the service command, got %v", lines)
	}
}

func TestRunOneOffMountsSecrets(t *testing.T) {
	// `run` mounts a service's secrets the same way `up` does, so a one-off of a
	// service that reads a *_FILE credential still finds it (#76 review).
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Secrets: compose.SecretRefs{{Source: "token", Target: "token"}}},
	})
	p.Secrets = map[string]compose.Secret{"token": {File: "/secrets/token.txt"}}
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.RunOneOff("web", nil, orchestrator.RunOneOffOptions{NoDeps: true}); err != nil {
		t.Fatalf("RunOneOff: %v", err)
	}
	if indexOf(log(), "-v /secrets/token.txt:/run/secrets/token:ro") < 0 {
		t.Errorf("run one-off should mount secrets like up, got %v", log())
	}
}

func TestRunOneOffRmDeletesAfter(t *testing.T) {
	rt, log := fakeShim(t)
	o := orchestrator.New(runOneOffProject(), rt, "opossum", &bytes.Buffer{})
	if err := o.RunOneOff("web", nil, orchestrator.RunOneOffOptions{Rm: true, NoDeps: true}); err != nil {
		t.Fatalf("RunOneOff: %v", err)
	}
	lines := log()
	oneOff := indexOf(lines, "run -i --name web-run.demo.opossum")
	del := -1
	for i := oneOff + 1; i < len(lines); i++ {
		if strings.Contains(lines[i], "delete --force web-run.demo.opossum") {
			del = i
			break
		}
	}
	if del < 0 {
		t.Errorf("--rm should delete the one-off after it runs, got %v", lines)
	}
}

func TestRunOneOffUnknownService(t *testing.T) {
	rt, _ := fakeShim(t)
	o := orchestrator.New(runOneOffProject(), rt, "opossum", &bytes.Buffer{})
	if err := o.RunOneOff("nope", nil, orchestrator.RunOneOffOptions{}); err == nil {
		t.Fatal("expected an error for an unknown service")
	}
}

func TestExecMapsServiceToContainer(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Exec("web", []string{"echo", "hi"}, runtime.ExecOptions{TTY: true}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !hasLine(log(), "exec -t web.demo.opossum echo hi") {
		t.Errorf("expected exec against the service's container, got %v", log())
	}
}

func TestExecRejectsUnknownServiceAndEmptyCommand(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Exec("nope", []string{"ls"}, runtime.ExecOptions{}); err == nil {
		t.Error("expected an error for an unknown service")
	}
	if err := o.Exec("web", nil, runtime.ExecOptions{}); err == nil {
		t.Error("expected an error when no command is given")
	}
}

func TestStopStopsInReverseWithoutRemoving(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"db":  {Image: "postgres:16"},
		"web": {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "db"}}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Stop(nil); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	lines := log()
	// Dependents stop before dependencies.
	sWeb, sDB := indexOf(lines, "stop web.demo.opossum"), indexOf(lines, "stop db.demo.opossum")
	if sWeb < 0 || sDB < 0 || sWeb > sDB {
		t.Errorf("web should stop before db (web=%d db=%d) in %v", sWeb, sDB, lines)
	}
	// Unlike down, stop removes nothing — no delete or network teardown.
	for _, l := range lines {
		if strings.HasPrefix(l, "delete --force") || strings.HasPrefix(l, "network delete") {
			t.Errorf("stop must not remove containers or the network, got %q", l)
		}
	}
}

func TestStopNamedOnly(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"db":  {Image: "postgres:16"},
		"web": {Image: "web:latest"},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Stop([]string{"db"}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	lines := log()
	if !hasLine(lines, "stop db.demo.opossum") || indexOf(lines, "stop web.demo.opossum") >= 0 {
		t.Errorf("only db should be stopped, got %v", lines)
	}
}

func TestRestartStopsThenStarts(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"db":  {Image: "postgres:16"},
		"web": {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "db"}}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Restart(nil); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	lines := log()
	// Everything is stopped before anything is started again.
	lastStop, firstStart := -1, -1
	for i, l := range lines {
		if strings.HasPrefix(l, "stop ") {
			lastStop = i
		}
		if strings.HasPrefix(l, "start ") && firstStart < 0 {
			firstStart = i
		}
	}
	if firstStart < 0 || lastStop < 0 || lastStop > firstStart {
		t.Errorf("all stops should precede starts (lastStop=%d firstStart=%d) in %v", lastStop, firstStart, lines)
	}
	// Start uses `container start` (in place), not a fresh run.
	if !hasLine(lines, "start db.demo.opossum") || !hasLine(lines, "start web.demo.opossum") {
		t.Errorf("expected in-place start of both services, got %v", lines)
	}
	if indexOf(lines, "run ") >= 0 {
		t.Errorf("restart must not re-run containers, got %v", lines)
	}
	// Dependencies start before dependents.
	if d, w := indexOf(lines, "start db.demo.opossum"), indexOf(lines, "start web.demo.opossum"); d > w {
		t.Errorf("db should start before web (db=%d web=%d)", d, w)
	}
	// Stops go in reverse dependency order (dependents first): web before db, so a
	// dependency isn't pulled out from under a still-running dependent.
	if w, d := indexOf(lines, "stop web.demo.opossum"), indexOf(lines, "stop db.demo.opossum"); w < 0 || d < 0 || w > d {
		t.Errorf("web should stop before db (web=%d db=%d) in %v", w, d, lines)
	}
}

func TestStopUnknownServiceRejected(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{"db": {Image: "postgres:16"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Stop([]string{"nope"}); err == nil {
		t.Fatal("expected an error for an unknown service")
	}
	if err := o.Restart([]string{"nope"}); err == nil {
		t.Fatal("expected an error for an unknown service on restart")
	}
}

func TestDownVolumesRemovesOnlyNamedVolumes(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"db": {Image: "postgres:16", Volumes: []string{
			"pgdata:/var/lib/postgresql/data", // named volume -> removed
			"./seed:/seed",                    // bind mount    -> not a volume
		}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})

	// Without -v, no volume is deleted.
	if err := o.Down(false, "", false); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if indexOf(log(), "volume delete") >= 0 {
		t.Errorf("down without --volumes must not delete volumes, got %v", log())
	}

	// With -v, the named volume is removed but the bind mount source is not.
	rt2, log2 := fakeShim(t)
	o2 := orchestrator.New(p, rt2, "opossum", &bytes.Buffer{})
	if err := o2.Down(true, "", false); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if !hasLine(log2(), "volume delete demo_pgdata") {
		t.Errorf("expected the project-namespaced named volume to be removed, got %v", log2())
	}
	if indexOf(log2(), "volume delete ./seed") >= 0 || countLines(log2(), "volume delete") != 1 {
		t.Errorf("only the named volume should be removed, got %v", log2())
	}
}

func TestUpNamespacesNamedVolumes(t *testing.T) {
	// A named volume is prefixed with the project name (docker compose's
	// <project>_<volume>), while a bind mount is resolved to a host path and
	// left un-namespaced. Two projects that share a volume name then get
	// distinct volumes and don't collide (#63).
	svcs := func() map[string]*compose.Service {
		return map[string]*compose.Service{
			"db": {Image: "postgres:16", Volumes: []string{
				"pgdata:/var/lib/postgresql/data", // named  -> namespaced
				"./seed:/seed",                    // bind   -> host path, untouched
				"/anon",                           // anonymous -> namespaced per service
			}},
		}
	}

	rt, log := fakeShim(t)
	o := orchestrator.New(project("demo", svcs()), rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if indexOf(log(), "-v demo_pgdata:/var/lib/postgresql/data") < 0 {
		t.Errorf("named volume should be project-namespaced, got %v", log())
	}
	if indexOf(log(), "-v pgdata:/var/lib/postgresql/data") >= 0 {
		t.Errorf("raw (un-namespaced) volume name must not be passed to the runtime, got %v", log())
	}
	if indexOf(log(), "-v "+filepath.Join(testBaseDir, "seed")+":/seed") < 0 {
		t.Errorf("bind mount should be resolved to a host path and left un-namespaced, got %v", log())
	}
	// An anonymous volume gets a stable per-service namespaced name (so `down -v`
	// can remove it and re-up reuses it), not a raw or empty-named passthrough.
	// The anonymous volume gets a project+service-namespaced name (with a path
	// hash suffix), mounted at its target; never a raw or empty-named passthrough.
	if indexOf(log(), "-v demo_db_anon_") < 0 || indexOf(log(), ":/anon") < 0 || indexOf(log(), "-v :/anon") >= 0 {
		t.Errorf("anonymous volume should be namespaced per service, got %v", log())
	}

	// A second project with the same volume name gets a distinct volume.
	rt2, log2 := fakeShim(t)
	o2 := orchestrator.New(project("prod", svcs()), rt2, "opossum", &bytes.Buffer{})
	if err := o2.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if indexOf(log2(), "-v prod_pgdata:/var/lib/postgresql/data") < 0 {
		t.Errorf("second project should get its own namespaced volume, got %v", log2())
	}
}

func TestExternalVolumeNotNamespacedOrRemoved(t *testing.T) {
	// An `external: true` volume is used by its real name (never namespaced) and
	// is never removed by `down -v` — the user manages it. A normal named volume
	// alongside it is still namespaced and removed (#64).
	newP := func() *compose.Project {
		p := project("demo", map[string]*compose.Service{
			"db": {Image: "postgres:16", Volumes: []string{
				"shared:/ext",  // external -> real name, protected
				"pgdata:/data", // normal   -> namespaced, removed
			}},
		})
		p.Volumes = map[string]compose.VolumeDecl{"shared": {External: true}}
		return p
	}

	rt, log := fakeShim(t)
	o := orchestrator.New(newP(), rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if indexOf(log(), "-v shared:/ext") < 0 || indexOf(log(), "-v demo_shared:/ext") >= 0 {
		t.Errorf("external volume should mount by its real name, not namespaced, got %v", log())
	}
	if indexOf(log(), "-v demo_pgdata:/data") < 0 {
		t.Errorf("normal named volume should still be namespaced, got %v", log())
	}

	// An external volume with a declared `name:` mounts that real name, not the key.
	rt3, log3 := fakeShim(t)
	pn := project("demo", map[string]*compose.Service{
		"db": {Image: "postgres:16", Volumes: []string{"alias:/ext"}},
	})
	pn.Volumes = map[string]compose.VolumeDecl{"alias": {External: true, Name: "real_vol"}}
	o3 := orchestrator.New(pn, rt3, "opossum", &bytes.Buffer{})
	if err := o3.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if indexOf(log3(), "-v real_vol:/ext") < 0 || indexOf(log3(), "-v alias:/ext") >= 0 {
		t.Errorf("external volume with a declared name should mount that real name, got %v", log3())
	}

	rt2, log2 := fakeShim(t)
	o2 := orchestrator.New(newP(), rt2, "opossum", &bytes.Buffer{})
	if err := o2.Down(true, "", false); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if !hasLine(log2(), "volume delete demo_pgdata") {
		t.Errorf("down -v should remove the normal named volume, got %v", log2())
	}
	if indexOf(log2(), "volume delete shared") >= 0 || indexOf(log2(), "volume delete demo_shared") >= 0 {
		t.Errorf("down -v must NOT remove an external volume, got %v", log2())
	}
}

// The seed copies from the path the compose file mounts the volume at, so the
// mount target reaches the runtime as the source to copy from. That one wire is
// the whole reason a target beginning with `-` mattered — it becomes an operand
// of `cp` inside the image — and nothing else pins it: the seed's argv carries
// the volume and the image, and the source only shows up inside the script.
func TestTheMountTargetIsWhatTheSeedCopiesFrom(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Volumes: []string{"data:/opt/a-distinctive-path"}},
	})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	var seed string
	for _, line := range log() {
		if strings.Contains(line, "demo_data:/__opossum_seed__") {
			seed = line
		}
	}
	if seed == "" {
		t.Fatalf("no seed ran: %v", log())
	}
	// The copy's source, not merely somewhere in the script: the target also
	// appears in the `[ -d … ]` that guards the copy, so looking for it anywhere
	// would pass for a version that tested one path and copied another.
	if !strings.Contains(seed, "cp -a -- '/opt/a-distinctive-path'/.") {
		t.Errorf("the seed should copy from the mount target, got: %q", seed)
	}
}

func TestUpSeedsFreshVolumesFromImage(t *testing.T) {
	// A fresh named or anonymous volume is seeded from the image's contents at the
	// mount path (a throwaway `run --rm --user 0 -v <vol>:/__opossum_seed__`), mirroring
	// Docker; a bind mount is not.
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Volumes: []string{
			"data:/var/data",    // named -> seeded
			"/app/node_modules", // anonymous -> seeded
			"./src:/app",        // bind -> NOT seeded
		}},
	})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	lines := log()
	if indexOf(lines, "run --rm --user 0 -v demo_data:/__opossum_seed__ web:latest") < 0 {
		t.Errorf("named volume should be seeded from the image, got %v", lines)
	}
	if indexOf(lines, "run --rm --user 0 -v demo_web_app_node_modules_") < 0 || indexOf(lines, ":/__opossum_seed__ web:latest") < 0 {
		t.Errorf("anonymous volume should be seeded from the image, got %v", lines)
	}
	// The bind mount's host path is never seeded.
	if indexOf(lines, "/src:/__opossum_seed__") >= 0 || indexOf(lines, "/app:/__opossum_seed__") >= 0 {
		t.Errorf("bind mounts must not be seeded, got %v", lines)
	}
}

// Recreating the containers must not touch the data. `up --force-recreate` is
// what someone runs to get a clean container — after changing an environment
// variable, or because something is wedged — and a database's contents are not
// what they are asking to lose.
//
// The two halves of that used to be tested apart: the shim was told a volume
// already existed, which says nothing about whether an `up` creates one, and
// nothing anywhere connected them. It has to be --force-recreate rather than a
// plain second `up`, or the run never reaches the volumes at all: an unchanged
// service is "up to date" and skipped several steps earlier, so the eval would
// pass on container idempotency while saying nothing about seeding. (Measured —
// written as a plain second `up` first, and a mutation that re-seeds every
// existing volume did not fail it.)
func TestRecreatingContainersLeavesTheVolumeTheFirstUpMade(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Volumes: []string{"data:/var/data"}},
	})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("first Up: %v", err)
	}
	if indexOf(log(), "demo_data:/__opossum_seed__") < 0 {
		t.Fatalf("the first up should have seeded a fresh volume, got %v", log())
	}
	first := len(log())

	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	o.SetUpOptions(true, false, false, false, false) // --force-recreate
	if err := o.Up(true); err != nil {
		t.Fatalf("second Up: %v", err)
	}
	recreated := false
	for _, line := range log()[first:] {
		if strings.Contains(line, "--name web.demo.opossum") {
			recreated = true
		}
		if strings.Contains(line, "demo_data:/__opossum_seed__") {
			t.Errorf("recreating the container re-seeded the volume, wiping what was in it: %q", line)
		}
	}
	// Without this, an `up` that did nothing at all would pass the assertion above.
	if !recreated {
		t.Errorf("the second up did not recreate the container, so it never reached the volumes: %v", log()[first:])
	}
}

// `down -v` says to throw the data away, so the next `up` has to start over. The
// pair with the eval above: one says opossum keeps a volume it made, this says it
// stops keeping it when told to, and neither passes for a shim that simply always
// answers the same way.
func TestUpSeedsAgainAfterDownTookTheVolumeAway(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Volumes: []string{"data:/var/data"}},
	})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("first Up: %v", err)
	}
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Down(true, "", false); err != nil {
		t.Fatalf("Down -v: %v", err)
	}
	mark := len(log())

	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("second Up: %v", err)
	}
	seeded := false
	for _, line := range log()[mark:] {
		if strings.Contains(line, "demo_data:/__opossum_seed__") {
			seeded = true
		}
	}
	if !seeded {
		t.Errorf("the volume was deleted, so the next up must seed a fresh one; got %v", log()[mark:])
	}
}

func TestUpSkipsSeedingWhenVolumeExists(t *testing.T) {
	// A volume that was already there — made by hand, or by another tool — is left
	// untouched. (That opossum also leaves alone the ones it made itself is
	// TestASecondUpLeavesTheVolumeTheFirstOneMade, which drives two real ups.)
	rt, log := fakeShim(t)
	setShimEnv(rt, "VOLUME_LS=demo_data") // pretend this volume already exists
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Volumes: []string{"data:/var/data"}},
	})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if indexOf(log(), "demo_data:/__opossum_seed__") >= 0 {
		t.Errorf("an existing volume must not be re-seeded, got %v", log())
	}
}

func TestUpSkipsSeedingWhenExistenceUnknown(t *testing.T) {
	// If `volume ls` errors, opossum can't tell whether the volume already exists,
	// so it fails SAFE and does not seed — never overwriting a volume that might be
	// there with real data.
	rt, log := fakeShim(t)
	setShimEnv(rt, "VOLUME_LS_FAIL=1")
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Volumes: []string{"data:/var/data"}},
	})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if indexOf(log(), "__opossum_seed__") >= 0 {
		t.Errorf("must not seed when volume existence can't be determined, got %v", log())
	}
}

func TestDownRemovesAnonVolume(t *testing.T) {
	// `down -v` removes anonymous volumes too (they're project-owned), not just
	// named ones.
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Volumes: []string{"/app/cache"}},
	})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Down(true, "", false); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if indexOf(log(), "volume delete demo_web_app_cache_") < 0 {
		t.Errorf("down -v should remove the anonymous volume, got %v", log())
	}
}

func imageProject() *compose.Project {
	return project("demo", map[string]*compose.Service{
		"web": {Build: &compose.Build{Context: "/ctx"}}, // built -> demo-web:latest
		"db":  {Image: "postgres:16"},                   // pulled
	})
}

func TestDownRmiLocalRemovesBuiltOnly(t *testing.T) {
	rt, log := fakeShim(t)
	if err := orchestrator.New(imageProject(), rt, "opossum", &bytes.Buffer{}).Down(false, "local", false); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if indexOf(log(), "image delete --force demo-web:latest") < 0 {
		t.Errorf("--rmi local should remove the built image, got %v", log())
	}
	if indexOf(log(), "image delete --force postgres:16") >= 0 {
		t.Errorf("--rmi local must NOT remove a pulled image, got %v", log())
	}
}

func TestDownRmiAllRemovesBuiltAndPulled(t *testing.T) {
	rt, log := fakeShim(t)
	if err := orchestrator.New(imageProject(), rt, "opossum", &bytes.Buffer{}).Down(false, "all", false); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if indexOf(log(), "image delete --force demo-web:latest") < 0 || indexOf(log(), "image delete --force postgres:16") < 0 {
		t.Errorf("--rmi all should remove built and pulled images, got %v", log())
	}
}

func TestDownWithoutRmiRemovesNoImages(t *testing.T) {
	rt, log := fakeShim(t)
	if err := orchestrator.New(imageProject(), rt, "opossum", &bytes.Buffer{}).Down(false, "", false); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if indexOf(log(), "image delete") >= 0 {
		t.Errorf("plain down must not remove images, got %v", log())
	}
}

func TestImagesListsBuiltAndPulled(t *testing.T) {
	rt, _ := fakeShim(t)
	setShimEnv(rt, "IMAGE_ABSENT=postgres:16") // the pulled image isn't present locally
	var out bytes.Buffer
	if err := orchestrator.New(imageProject(), rt, "opossum", &out).Images(); err != nil {
		t.Fatalf("Images: %v", err)
	}
	// Scan per line so PRESENT is tied to the right service.
	var web, db string
	for _, l := range strings.Split(out.String(), "\n") {
		switch {
		case strings.HasPrefix(l, "web"):
			web = l
		case strings.HasPrefix(l, "db"):
			db = l
		}
	}
	if !strings.Contains(web, "demo-web:latest") || !strings.Contains(web, "built") || !strings.Contains(web, "yes") {
		t.Errorf("built image present locally should show built + yes, got %q", web)
	}
	if !strings.Contains(db, "postgres:16") || !strings.Contains(db, "pulled") || !strings.Contains(db, "no") {
		t.Errorf("pulled image absent locally should show pulled + no, got %q", db)
	}
}

func TestUpDoesNotPredictThePostgresDatadirFailure(t *testing.T) {
	// A named volume mounted straight at Postgres's data directory used to earn a
	// warning before anything ran: the volume would arrive holding `lost+found`,
	// and initdb refuses a data directory that isn't empty. opossum now clears
	// `lost+found` out of the volumes it creates, so that mount is the one that
	// works — and a warning saying it "won't start as written" would be wrong on
	// the ordinary path, every single up.
	//
	// The case that remains real — a volume opossum did not create — is answered by
	// looking inside that volume (below) and by decoding initdb's own refusal if it
	// gets that far (lostfound_internal_test.go).
	rt, _ := fakeShim(t)
	var out bytes.Buffer
	p := project("demo", map[string]*compose.Service{
		"db": {Image: "postgres:16", Volumes: []string{"pgdata:/var/lib/postgresql/data"}},
	})
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if s := out.String(); strings.Contains(s, "OPSM-101") || strings.Contains(s, "won't start as written") {
		t.Errorf("nothing has failed, so there is nothing to predict:\n%s", s)
	}
}

// The shim answers `volume ls` one name per line, as the runtime does. That is
// load-bearing for the evals below: VolumeExists reads the first field of each
// line, so a shim that printed several names on one line would answer "exists"
// for the first only — and cases whose volume was silently missing would pass by
// never getting as far as the thing they mean to test.
func TestFakeVolumeListAnswersForEveryNameItWasGiven(t *testing.T) {
	rt, _ := fakeShim(t)
	setShimEnv(rt, "VOLUME_LS=demo_one demo_two")
	for _, name := range []string{"demo_one", "demo_two"} {
		if !rt.VolumeExists(name) {
			t.Errorf("the shim was given %q; it must be listed as existing", name)
		}
	}
	if rt.VolumeExists("demo_three") {
		t.Error("a volume nobody listed must not exist")
	}
}

// A volume opossum did not create still holds `lost+found`, and initdb will refuse
// it. That case can no longer be told from the compose file — the same mount line
// is the working one on a volume opossum made — so opossum looks inside the volume
// and speaks only about what it finds there.
func TestUpLooksInsideAnExistingPostgresVolume(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
		warn bool
	}{
		{"made elsewhere: lost+found and no cluster", []string{"VOLUME_LS=demo_pgdata", "LOOK_ENTRIES=lost+found"}, true},
		{"already initialised: initdb will not run again", []string{"VOLUME_LS=demo_pgdata", "LOOK_ENTRIES=lost+found PG_VERSION base global"}, false},
		{"prepared by opossum: nothing in it", []string{"VOLUME_LS=demo_pgdata", "LOOK_ENTRIES="}, false},
		{"no such volume yet: opossum makes it, empty", []string{"LOOK_ENTRIES=lost+found"}, false},
		{"the look failed: unknown is not evidence", []string{"VOLUME_LS=demo_pgdata", "LOOK_FAIL=1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			setShimEnv(rt, tc.env...)
			var out bytes.Buffer
			p := project("demo", map[string]*compose.Service{
				"db": {Image: "postgres:16", Volumes: []string{"pgdata:/var/lib/postgresql/data"}},
			})
			if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
				t.Fatalf("Up: %v", err)
			}
			if got := strings.Contains(out.String(), "OPSM-101"); got != tc.warn {
				t.Errorf("warned=%v, want %v; output:\n%s", got, tc.warn, out.String())
			}
		})
	}
}

// Which mounts the look applies to — the parsing half, kept from the eval of the
// warning this replaced. The volume in every case holds `lost+found` and no
// cluster, so the only thing deciding the answer is whether opossum reads that
// mount as its own volume at Postgres's data directory.
func TestUpLooksAtTheRightMounts(t *testing.T) {
	for _, tc := range []struct {
		name string
		svc  *compose.Service
		top  map[string]compose.VolumeDecl
		warn bool
		// canLook: whether a look into this mount is opossum's business at all.
		// False for the mounts it does not own, where silence must come from never
		// having read them, not from what was found.
		canLook bool
	}{
		{"the plain form", &compose.Service{Image: "postgres:16", Volumes: []string{"pgdata:/var/lib/postgresql/data"}}, nil, true, true},
		{"trailing slash and :ro", &compose.Service{Image: "postgres:16", Volumes: []string{"pgdata:/var/lib/postgresql/data/:ro"}}, nil, true, true},
		{"an explicit :rw", &compose.Service{Image: "postgres:16", Volumes: []string{"pgdata:/var/lib/postgresql/data:rw"}}, nil, true, true},
		{"a :cached mount", &compose.Service{Image: "postgres:16", Volumes: []string{"pgdata:/var/lib/postgresql/data:cached"}}, nil, true, true},
		// PGDATA below the mount point is the arrangement that sidesteps this, but
		// PGDATA set to the data directory ITSELF is not — initdb still looks at the
		// directory that holds `lost+found`.
		{"PGDATA is the data dir itself", &compose.Service{Image: "postgres:16", Environment: compose.Environment{"PGDATA=/var/lib/postgresql/data"}, Volumes: []string{"pgdata:/var/lib/postgresql/data"}}, nil, true, true},
		{"PGDATA is the data dir with a slash", &compose.Service{Image: "postgres:16", Environment: compose.Environment{"PGDATA=/var/lib/postgresql/data/"}, Volumes: []string{"pgdata:/var/lib/postgresql/data"}}, nil, true, true},
		{"PGDATA one level down", &compose.Service{Image: "postgres:16", Environment: compose.Environment{"PGDATA=/var/lib/postgresql/data/pgdata"}, Volumes: []string{"pgdata:/var/lib/postgresql/data"}}, nil, false, false},
		// A bind mount is a host directory: no ext4 of its own, so no `lost+found`,
		// and not opossum's to speak about either way.
		{"a bind mount at the data dir", &compose.Service{Image: "postgres:16", Volumes: []string{"./data:/var/lib/postgresql/data"}}, nil, false, false},
		// An external volume belongs to the user. opossum neither creates nor
		// removes it, so it must not be told to `down -v` it away.
		{"an external volume at the data dir", &compose.Service{Image: "postgres:16", Volumes: []string{"pgdata:/var/lib/postgresql/data"}}, map[string]compose.VolumeDecl{"pgdata": {External: true, Name: "real_pg_vol"}}, false, false},
		// MySQL tolerates a data directory that isn't empty.
		// Both of these DO exist and DO hold `lost+found` (see VOLUME_LS below), so
		// the only thing keeping them quiet is that neither sits at Postgres's data
		// directory. MySQL tolerates a non-empty data directory anyway.
		{"MySQL's data dir", &compose.Service{Image: "mysql:8", Volumes: []string{"dbdata:/var/lib/mysql"}}, nil, false, false},
		{"a volume somewhere else entirely", &compose.Service{Image: "postgres:16", Volumes: []string{"cache:/var/cache"}}, nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			// Every volume any of these cases could name already exists and holds
			// only `lost+found`, so the mount is the only thing left to decide the
			// answer. One name per line is what the runtime prints and what
			// VolumeExists reads — listing them on one line would answer "exists"
			// for the first name only, and most of these cases would then be
			// passing because their volume was missing.
			setShimEnv(rt, "VOLUME_LS=demo_pgdata\ndemo_dbdata\ndemo_cache\nreal_pg_vol", "LOOK_ENTRIES=lost+found")
			var out bytes.Buffer
			p := project("demo", map[string]*compose.Service{"db": tc.svc})
			p.Volumes = tc.top
			if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
				t.Fatalf("Up: %v", err)
			}
			if got := strings.Contains(out.String(), "OPSM-101"); got != tc.warn {
				t.Errorf("warned=%v, want %v; output:\n%s", got, tc.warn, out.String())
			}
			// Silence is not enough for the mounts opossum has no business reading:
			// a bind mount and an external volume must not even be looked into.
			if !tc.canLook {
				if indexOf(log(), "__opossum_look__") >= 0 {
					t.Errorf("nothing here is opossum's to read: %v", log())
				}
			}
		})
	}
}

// The clearing runs on volumes opossum creates and on nothing else. An
// `external: true` volume is the user's: opossum neither creates nor removes it,
// and must not run anything that touches its contents either.
func TestUpNeverTouchesAnExternalVolume(t *testing.T) {
	rt, log := fakeShim(t)
	var out bytes.Buffer
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Volumes: []string{"shared:/var/data"}},
	})
	p.Volumes = map[string]compose.VolumeDecl{"shared": {External: true}}
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	for _, l := range log() {
		if strings.Contains(l, "__opossum_seed__") || strings.Contains(l, "__opossum_look__") {
			t.Errorf("an external volume must be neither prepared nor read: %s", l)
		}
	}
}

// The look runs the service's image, and running an image that isn't here yet
// fetches it. On a real `up` the image has already been pulled or built by this
// point; under `--dry-run` it has not, and a dry-run that pulls gigabytes to
// answer a question is not a dry run. Not knowing reads the same as not being
// able to look: nothing is said.
func TestUpDoesNotFetchAnImageJustToLookInAVolume(t *testing.T) {
	rt, log := fakeShim(t)
	setShimEnv(rt, "VOLUME_LS=demo_pgdata", "LOOK_ENTRIES=lost+found", "IMAGE_ABSENT=postgres:16")
	var out bytes.Buffer
	p := project("demo", map[string]*compose.Service{
		"db": {Image: "postgres:16", Volumes: []string{"pgdata:/var/lib/postgresql/data"}},
	})
	o := orchestrator.New(p, rt, "opossum", &out)
	o.SetDryRun(true)
	if err := o.Up(true); err != nil {
		t.Fatalf("Up --dry-run: %v", err)
	}
	if indexOf(log(), "__opossum_look__") >= 0 {
		t.Errorf("the image is not here yet; looking would fetch it: %v", log())
	}
	if s := out.String(); strings.Contains(s, "OPSM-101") {
		t.Errorf("opossum never looked, so it has nothing to report:\n%s", s)
	}
}

// The other half of that: a real `up` looks even when the image is missing. It
// does not pre-pull a plain `image:` service — the container start does that — so
// declining here would go quiet on the first up after an image was pruned, which
// is exactly when the volume is old enough for the warning to be true. The pull is
// not a cost the look imposes; it was going to happen seconds later.
func TestUpStillLooksWhenTheImageHasYetToBeFetched(t *testing.T) {
	rt, log := fakeShim(t)
	setShimEnv(rt, "VOLUME_LS=demo_pgdata", "LOOK_ENTRIES=lost+found", "IMAGE_ABSENT=postgres:16")
	var out bytes.Buffer
	p := project("demo", map[string]*compose.Service{
		"db": {Image: "postgres:16", Volumes: []string{"pgdata:/var/lib/postgresql/data"}},
	})
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if indexOf(log(), "__opossum_look__") < 0 {
		t.Fatalf("a real up should still look: %v", log())
	}
	if s := out.String(); !strings.Contains(s, "OPSM-101") {
		t.Errorf("the volume holds lost+found and no cluster; that has to be said:\n%s", s)
	}
}

// A dry-run has to be able to say this — it is exactly the "what will happen"
// question — but the plan it prints is the list of commands a real `up` would
// run to change something. The look changes nothing, so it belongs in one and not
// the other.
func TestDryRunWarnsWithoutPuttingTheLookInThePlan(t *testing.T) {
	rt, _ := fakeShim(t)
	setShimEnv(rt, "VOLUME_LS=demo_pgdata", "LOOK_ENTRIES=lost+found")
	var out bytes.Buffer
	p := project("demo", map[string]*compose.Service{
		"db": {Image: "postgres:16", Volumes: []string{"pgdata:/var/lib/postgresql/data"}},
	})
	o := orchestrator.New(p, rt, "opossum", &out)
	o.SetDryRun(true)
	if err := o.Up(true); err != nil {
		t.Fatalf("Up --dry-run: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "OPSM-101") {
		t.Errorf("a dry-run should still say the service won't start as written:\n%s", s)
	}
	if strings.Contains(s, "__opossum_look__") {
		t.Errorf("the plan lists what a real up would change; the look changes nothing:\n%s", s)
	}
}

// PGDATA below the mount point is the end of it: initdb never looks at the
// directory holding `lost+found`, so there is nothing to warn about — and no
// reason to spend a container looking.
func TestUpDoesNotLookWhenPGDATAIsAlreadyASubdirectory(t *testing.T) {
	rt, log := fakeShim(t)
	setShimEnv(rt, "VOLUME_LS=demo_pgdata", "LOOK_ENTRIES=lost+found")
	var out bytes.Buffer
	p := project("demo", map[string]*compose.Service{
		"db": {
			Image:       "postgres:16",
			Environment: compose.Environment{"PGDATA=/var/lib/postgresql/data/pgdata"},
			Volumes:     []string{"pgdata:/var/lib/postgresql/data"},
		},
	})
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if s := out.String(); strings.Contains(s, "OPSM-101") {
		t.Errorf("the data already lives below the mount point:\n%s", s)
	}
	if indexOf(log(), "__opossum_look__") >= 0 {
		t.Errorf("nothing to decide, so nothing should have been started to look: %v", log())
	}
}

func TestStatsInvokesContainerStats(t *testing.T) {
	newP := func() *compose.Project {
		return project("demo", map[string]*compose.Service{
			"web": {Image: "web:latest"},
			"db":  {Image: "postgres:16"},
		})
	}

	// No services + --no-stream: one `stats --no-stream` over all project containers.
	rt, log := fakeShim(t)
	if err := orchestrator.New(newP(), rt, "opossum", &bytes.Buffer{}).Stats(nil, true); err != nil {
		t.Fatalf("Stats: %v", err)
	}
	line := ""
	for _, l := range log() {
		if strings.HasPrefix(l, "stats") {
			line = l
		}
	}
	if !strings.Contains(line, "--no-stream") || !strings.Contains(line, "web.demo.opossum") || !strings.Contains(line, "db.demo.opossum") {
		t.Errorf("expected `stats --no-stream` over both containers, got %q", line)
	}

	// A named service, streaming (default): no --no-stream, only that container.
	rt2, log2 := fakeShim(t)
	if err := orchestrator.New(newP(), rt2, "opossum", &bytes.Buffer{}).Stats([]string{"web"}, false); err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if !hasLine(log2(), "stats web.demo.opossum") {
		t.Errorf("expected streaming `stats web.demo.opossum`, got %v", log2())
	}

	// Unknown service is rejected.
	rt3, _ := fakeShim(t)
	if err := orchestrator.New(newP(), rt3, "opossum", &bytes.Buffer{}).Stats([]string{"nope"}, true); err == nil {
		t.Fatal("expected an error for an unknown service")
	}
}

func TestUpPrintsHostAddrForPublishedPorts(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Ports: []string{"4200:4200"}},
		"db":  {Image: "postgres:16"}, // no published ports
	})
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	s := out.String()
	// A published service gets a host-reachable address hint (not the container
	// DNS name, which the host can't open).
	if !strings.Contains(s, "web on the host: localhost:4200") {
		t.Errorf("expected host-address hint for web, got:\n%s", s)
	}
	// A service without published ports must not get a hint.
	if strings.Contains(s, "db on the host:") {
		t.Errorf("portless db should not get a host-address hint, got:\n%s", s)
	}
}

func TestPsReportsInspectedIP(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"db": {Image: "postgres:16"},
	})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	if err := o.Ps(); err != nil {
		t.Fatalf("Ps: %v", err)
	}
	got := out.String()
	// PORTS and STATUS columns are present in the header.
	if !strings.Contains(got, "PORTS") || !strings.Contains(got, "STATUS") {
		t.Errorf("ps header should include PORTS and STATUS, got:\n%s", got)
	}
	if !strings.Contains(got, "192.168.64.10") {
		t.Errorf("ps should show the inspected IP, got:\n%s", got)
	}
	// PORTS is rendered docker-ps style from inspect's publishedPorts.
	if !strings.Contains(got, "0.0.0.0:8080->8080/tcp") {
		t.Errorf("ps should render published ports, got:\n%s", got)
	}
	// STATUS comes from status.state, not from IP inference.
	if !strings.Contains(got, "db.demo.opossum") || !strings.Contains(got, "running") {
		t.Errorf("ps should show container name and running status, got:\n%s", got)
	}
}

func TestPsHidesMissingContainers(t *testing.T) {
	// A shim whose inspect reports every container missing -> ps lists no rows
	// (just the header), so after `down` (or before `up`) ps is empty, matching
	// docker compose, instead of a wall of dead rows.
	rt := fakeShimInspect(t, "Error: container not found", 1)
	p := project("demo", map[string]*compose.Service{
		"db":  {Image: "postgres:16"},
		"web": {Image: "web:latest"},
	})
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Ps(); err != nil {
		t.Fatalf("Ps: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "db") || strings.Contains(got, "web") {
		t.Errorf("missing containers must not be listed, got:\n%s", got)
	}
	// The header is still printed (an empty table, like docker compose).
	if !strings.Contains(got, "SERVICE") {
		t.Errorf("expected the header even when empty, got:\n%s", got)
	}
}

func TestPsErrorsWhenSystemStopped(t *testing.T) {
	// The CLI is installed but its daemon is stopped, so every inspect fails. Ps
	// must NOT render an empty table (which reads as "nothing is running") — it must
	// fail loudly with the coded [OPSM-405] error. This is the mutation guard for
	// the SystemRunning probe: drop it and Ps returns an empty table with nil error.
	rt, _ := fakeShim(t)
	setShimEnv(rt, "SYSTEM_STOPPED=1")
	p := project("demo", map[string]*compose.Service{"db": {Image: "postgres:16"}})
	var out bytes.Buffer
	err := orchestrator.New(p, rt, "opossum", &out).Ps()
	if err == nil {
		t.Fatalf("Ps must error when the container system is stopped, got nil (output: %q)", out.String())
	}
	if !strings.Contains(err.Error(), "OPSM-405") {
		t.Errorf("Ps error should carry the OPSM-405 code, got %q", err.Error())
	}
	if strings.Contains(out.String(), "db") {
		t.Errorf("Ps must not list containers when it couldn't reach the runtime, got:\n%s", out.String())
	}
}

func TestImagesErrorsWhenSystemStopped(t *testing.T) {
	// Same contract as Ps: a stopped daemon makes `image inspect` unanswerable, so
	// Images must fail with [OPSM-405] rather than print a confident PRESENT=no.
	rt, _ := fakeShim(t)
	setShimEnv(rt, "SYSTEM_STOPPED=1")
	var out bytes.Buffer
	err := orchestrator.New(imageProject(), rt, "opossum", &out).Images()
	if err == nil {
		t.Fatalf("Images must error when the container system is stopped, got nil (output: %q)", out.String())
	}
	if !strings.Contains(err.Error(), "OPSM-405") {
		t.Errorf("Images error should carry the OPSM-405 code, got %q", err.Error())
	}
	if strings.Contains(out.String(), "PRESENT") {
		t.Errorf("Images must not render its table when it couldn't reach the runtime, got:\n%s", out.String())
	}
}

func TestPsShowsStoppedWhenExistsButNotRunning(t *testing.T) {
	// A container that exists but whose state is "stopped" must read "stopped",
	// not "absent" — the two are different situations.
	rt, _ := fakeShim(t)
	setShimEnv(rt, "INSPECT_STATE=stopped")
	p := project("demo", map[string]*compose.Service{"db": {Image: "postgres:16"}})
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Ps(); err != nil {
		t.Fatalf("Ps: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "stopped") || strings.Contains(got, "absent") {
		t.Errorf("an existing stopped container should read 'stopped' (not 'absent'), got:\n%s", got)
	}
}

func TestPsFallsBackToStoppedWhenExistsWithEmptyState(t *testing.T) {
	// A container that exists but reports no state must fall back to "stopped",
	// not "absent" — guards the exists-but-empty-state branch (which a shim with a
	// non-empty INSPECT_STATE never exercises).
	rt := fakeShimInspect(t, `[{"status":{"state":""},"configuration":{}}]`, 0)
	p := project("demo", map[string]*compose.Service{"db": {Image: "postgres:16"}})
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Ps(); err != nil {
		t.Fatalf("Ps: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "stopped") || strings.Contains(got, "absent") {
		t.Errorf("exists-but-empty-state should read 'stopped' (not 'absent'), got:\n%s", got)
	}
}

// healthyDepsProject: `db` has a healthcheck; `web` waits for it to be healthy.
func healthyDepsProject() *compose.Project {
	return project("demo", map[string]*compose.Service{
		"db": {
			Image: "postgres:16",
			Healthcheck: &compose.Healthcheck{
				Test:     []string{"pg_isready"},
				Interval: time.Millisecond, // keep the eval fast
				Retries:  5,
			},
		},
		"web": {
			Image:     "web:latest",
			DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}},
		},
	})
}

func TestUpWaitsForHealthyDependency(t *testing.T) {
	rt, log := fakeShim(t)
	setShimEnv(rt, "HEALTH_OK_AT=3") // db reports healthy only on the 3rd probe
	var out bytes.Buffer
	o := orchestrator.New(healthyDepsProject(), rt, "opossum", &out)
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	lines := log()

	// db is probed via exec until healthy — exactly 3 attempts here.
	if n := countLines(lines, "exec db.demo.opossum pg_isready"); n != 3 {
		t.Errorf("expected 3 healthcheck probes, got %d in %v", n, lines)
	}
	// web must not start until db is healthy: its run comes after every probe.
	webRun := indexOf(lines, "run -d --name web.demo.opossum")
	dbRun := indexOf(lines, "run -d --name db.demo.opossum")
	lastProbe := -1
	for i, l := range lines {
		if strings.Contains(l, "exec db.demo.opossum") {
			lastProbe = i
		}
	}
	if webRun < 0 || dbRun < 0 || !(dbRun < lastProbe && lastProbe < webRun) {
		t.Errorf("expected db run(%d) < probes(last=%d) < web run(%d) in %v", dbRun, lastProbe, webRun, lines)
	}
	if !strings.Contains(out.String(), "Waiting for db to be healthy") {
		t.Errorf("expected a wait message, got:\n%s", out.String())
	}
}

// A health probe that never returns must not block `up` forever: each attempt is
// bounded by the healthcheck's timeout, so up fails (after retries) instead (#139).
func TestUpHealthProbeTimeoutDoesNotHang(t *testing.T) {
	rt, _ := fakeShim(t)
	setShimEnv(rt, "HEALTH_HANG=1") // the healthcheck exec never returns
	p := project("demo", map[string]*compose.Service{
		"db": {
			Image: "postgres:16",
			Healthcheck: &compose.Healthcheck{
				Test:    []string{"pg_isready"},
				Timeout: 150 * time.Millisecond, // per-attempt bound
				Retries: 2,
			},
		},
		"web": {
			Image:     "web:latest",
			DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}},
		},
	})
	done := make(chan error, 1)
	go func() { done <- orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected up to fail when the health probe hangs, got nil")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("up hung on a stuck health probe — per-attempt timeout not enforced")
	}
}

// Interrupting `up` (Ctrl-C, modelled by cancelling the signal context) while it
// waits on a dependency's health must roll back what it already started — the
// started container and the network — rather than leaving residue (#140).
func TestUpRollsBackOnInterrupt(t *testing.T) {
	rt, log := fakeShim(t)
	setShimEnv(rt, "HEALTH_OK_AT=100000") // db never reports healthy, so up stays in the probe loop
	ctx, cancel := context.WithCancel(context.Background())
	p := project("demo", map[string]*compose.Service{
		"db": {
			Image: "postgres:16",
			Healthcheck: &compose.Healthcheck{
				Test:     []string{"pg_isready"},
				Interval: 5 * time.Millisecond,
				Retries:  1_000_000,
				Timeout:  time.Second,
			},
		},
		"web": {
			Image:     "web:latest",
			DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}},
		},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	o.OnSignal(ctx)

	done := make(chan error, 1)
	go func() { done <- o.Up(true) }()

	// Interrupt only once db has actually started (so there's something to roll back).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && indexOf(log(), "run -d --name db.demo.opossum") < 0 {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an interrupt error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("up did not return after interrupt")
	}
	lines := log()
	if indexOf(lines, "run -d --name db.demo.opossum") < 0 {
		t.Fatalf("db should have started before the interrupt, got %v", lines)
	}
	// Rollback: db is stopped (Stop is used nowhere else in up) and the network removed.
	if indexOf(lines, "stop db.demo.opossum") < 0 {
		t.Errorf("interrupt should stop the started container, got %v", lines)
	}
	if indexOf(lines, "network delete demo-net") < 0 {
		t.Errorf("interrupt should remove the created network, got %v", lines)
	}
	if indexOf(lines, "run -d --name web.demo.opossum") >= 0 {
		t.Errorf("web must not start after the interrupt, got %v", lines)
	}
}

// A second `up` leaves a running, unchanged service alone instead of recreating
// it (docker compose parity) — so it keeps its state and logs (#144).
func TestUpSkipsUnchangedRunningService(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	if err := o.Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}
	if err := o.Up(true); err != nil {
		t.Fatalf("second up: %v", err)
	}
	if n := countLines(log(), "run -d --name web.demo.opossum"); n != 1 {
		t.Errorf("an unchanged running service should be created once, got %d runs", n)
	}
	if !strings.Contains(out.String(), "web is up to date") {
		t.Errorf("expected 'web is up to date' on the second up, got:\n%s", out.String())
	}
}

// `up --foreground` must recreate even an unchanged running service: attaching to
// stream its output requires a fresh container, so the skip is bypassed.
func TestUpForegroundRecreatesEvenIfUnchanged(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil { // detached
		t.Fatalf("first up: %v", err)
	}
	if err := o.Up(false); err != nil { // --foreground
		t.Fatalf("foreground up: %v", err)
	}
	if n := countLines(log(), "--name web.demo.opossum"); n != 2 {
		t.Errorf("foreground up should recreate to attach, want 2 runs got %d", n)
	}
}

// --force-recreate recreates even when nothing changed.
func TestUpForceRecreateRecreates(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}
	o.SetUpOptions(true, false, false, false, false) // --force-recreate
	if err := o.Up(true); err != nil {
		t.Fatalf("second up: %v", err)
	}
	if n := countLines(log(), "run -d --name web.demo.opossum"); n != 2 {
		t.Errorf("--force-recreate should recreate, want 2 runs got %d", n)
	}
}

// A configuration change (here: environment) recreates the service.
// A change to ANY field that feeds the config-hash must recreate the container
// on the next up (idempotency correctness). Guards against a refactor silently
// dropping a field from configHash — previously only the env case was tested.
func TestUpRecreatesOnConfigChange(t *testing.T) {
	cases := []struct {
		field  string
		change func(*compose.Service)
	}{
		{"environment", func(s *compose.Service) { s.Environment = compose.Environment{"NEW=1"} }},
		{"ports", func(s *compose.Service) { s.Ports = []string{"8080:8080"} }},
		{"volumes", func(s *compose.Service) { s.Volumes = []string{"data:/data"} }},
		{"tmpfs", func(s *compose.Service) { s.Tmpfs = []string{"/tmp"} }},
		{"command", func(s *compose.Service) { s.Command = compose.Command{"serve"} }},
		{"entrypoint", func(s *compose.Service) { s.Entrypoint = compose.Command{"/app/run"} }},
		{"platform", func(s *compose.Service) { s.Platform = "linux/amd64" }},
		{"ssh", func(s *compose.Service) { s.SSH = true }},
		{"user", func(s *compose.Service) { s.User = "1000" }},
		{"working_dir", func(s *compose.Service) { s.WorkingDir = "/app" }},
		{"init", func(s *compose.Service) { s.Init = true }},
		{"read_only", func(s *compose.Service) { s.ReadOnly = true }},
		{"cap_add", func(s *compose.Service) { s.CapAdd = compose.StringOrSlice{"NET_ADMIN"} }},
		{"cap_drop", func(s *compose.Service) { s.CapDrop = compose.StringOrSlice{"ALL"} }},
		{"network_mode", func(s *compose.Service) { s.NetworkMode = compose.NetworkModeNone }},
		{"networks", func(s *compose.Service) { s.Networks = compose.ServiceNetworks{"other"} }},
	}
	for _, c := range cases {
		t.Run(c.field, func(t *testing.T) {
			rt, log := fakeShim(t)
			svc := &compose.Service{Image: "web:latest"}
			p := project("demo", map[string]*compose.Service{"web": svc})
			o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
			if err := o.Up(true); err != nil {
				t.Fatalf("first up: %v", err)
			}
			c.change(svc) // config changed
			if err := o.Up(true); err != nil {
				t.Fatalf("second up: %v", err)
			}
			// Count run lines by "--name web.demo.opossum" so intervening flags
			// (e.g. --ssh inserted before --name) don't hide a recreation.
			if n := countLines(log(), "--name web.demo.opossum"); n != 2 {
				t.Errorf("changing %s should recreate the container, want 2 runs got %d", c.field, n)
			}
		})
	}
}

// mem_limit / cpus (and deploy.resources) must reach `container run` as -m / -c
// and not be dropped or swapped. Loaded from YAML since the resource fields are
// unexported scalars.
func TestUpPassesResourceLimits(t *testing.T) {
	rt, log := fakeShim(t)
	dir := t.TempDir()
	f := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(f, []byte("name: demo\nservices:\n  web:\n    image: web:latest\n    mem_limit: 512m\n    cpus: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := compose.Load(f)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("up: %v", err)
	}
	line, ok := "", false
	for _, l := range log() {
		if strings.Contains(l, "run -d --name web.demo.opossum") {
			line, ok = l, true
		}
	}
	if !ok {
		t.Fatalf("no run line, got %v", log())
	}
	// 512m → 512 MiB → "512M"; cpus 2 → "2". Assert both flags with the right values.
	if !strings.Contains(line, "-m 512M") || !strings.Contains(line, "-c 2") {
		t.Errorf("run line should carry -m 512M and -c 2, got: %s", line)
	}
}

// A build service builds only when its image is missing (or --build); --no-build
// refuses to build a missing image.
func TestUpBuildsOnlyWhenNeeded(t *testing.T) {
	t.Run("present image is not rebuilt", func(t *testing.T) {
		rt, log := fakeShim(t) // image inspect returns present by default
		p := project("demo", map[string]*compose.Service{"api": {Build: &compose.Build{Context: "/ctx"}}})
		if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if n := countLines(log(), "build "); n != 0 {
			t.Errorf("a present image should not be rebuilt, got %d builds", n)
		}
	})
	t.Run("no-build errors on a missing image", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "IMAGE_ABSENT=demo-api:latest")
		p := project("demo", map[string]*compose.Service{"api": {Build: &compose.Build{Context: "/ctx"}}})
		o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
		o.SetUpOptions(false, false, true, false, false) // --no-build
		if err := o.Up(true); err == nil || !strings.Contains(err.Error(), "no-build") {
			t.Fatalf("expected a --no-build error for a missing image, got %v", err)
		}
	})
}

// orphanProject: current compose has only `web`; the runtime still holds an
// `old` container from a since-removed service.
func orphanProject(t *testing.T, rt *runtime.Runtime) *compose.Project {
	t.Helper()
	setShimEnv(rt, "LS_CONTAINERS=web.demo.opossum old.demo.opossum")
	setShimEnv(rt, "LS_PROJECT=demo")
	return project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
}

func TestUpWarnsAboutOrphans(t *testing.T) {
	rt, _ := fakeShim(t)
	p := orphanProject(t, rt)
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !strings.Contains(out.String(), "orphan") || !strings.Contains(out.String(), "old.demo.opossum") {
		t.Errorf("expected an orphan warning naming old.demo.opossum, got:\n%s", out.String())
	}
}

func TestUpRemovesOrphans(t *testing.T) {
	rt, log := fakeShim(t)
	p := orphanProject(t, rt)
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	o.SetUpOptions(false, false, false, true, false) // --remove-orphans
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	// The orphan is stopped+deleted (stop is unique to orphan removal here); the
	// current service `web` is not treated as an orphan.
	if indexOf(log(), "stop old.demo.opossum") < 0 || indexOf(log(), "delete --force old.demo.opossum") < 0 {
		t.Errorf("--remove-orphans should stop+delete the orphan, got %v", log())
	}
}

func TestDownRemovesOrphans(t *testing.T) {
	rt, log := fakeShim(t)
	p := orphanProject(t, rt)
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Down(false, "", true); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if indexOf(log(), "delete --force old.demo.opossum") < 0 {
		t.Errorf("down --remove-orphans should delete the orphan, got %v", log())
	}
}

func TestDownWithoutFlagLeavesOrphans(t *testing.T) {
	rt, log := fakeShim(t)
	p := orphanProject(t, rt)
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Down(false, "", false); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if indexOf(log(), "old.demo.opossum") >= 0 {
		t.Errorf("down without --remove-orphans must not touch orphans, got %v", log())
	}
}

// The safety invariant: --remove-orphans must never warn about or remove another
// project's container (only this project's label is considered).
func TestRemoveOrphansSparesOtherProjects(t *testing.T) {
	rt, log := fakeShim(t)
	newProj := func(t *testing.T) *compose.Project {
		setShimEnv(rt, "LS_CONTAINERS=web.demo.opossum")          // this project, current service
		setShimEnv(rt, "LS_PROJECT=demo")                         // its label
		setShimEnv(rt, "LS_FOREIGN=db.other.opossum otherproj-x") // a different project's containers
		return project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
	}

	var out bytes.Buffer
	o := orchestrator.New(newProj(t), rt, "opossum", &out)
	o.SetUpOptions(false, false, false, true, false) // --remove-orphans
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if strings.Contains(out.String(), "orphan") {
		t.Errorf("must not report another project's containers as orphans, got:\n%s", out.String())
	}
	for _, foreign := range []string{"db.other.opossum", "otherproj-x"} {
		if indexOf(log(), foreign) >= 0 {
			t.Errorf("--remove-orphans must not touch another project's container %q, got %v", foreign, log())
		}
	}

	rt2, log2 := fakeShim(t)
	if err := orchestrator.New(newProj(t), rt2, "opossum", &bytes.Buffer{}).Down(false, "", true); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if indexOf(log2(), "db.other.opossum") >= 0 {
		t.Errorf("down --remove-orphans must not touch another project's container, got %v", log2())
	}
}

// profilesProject: web always runs; debug is gated behind the "debug" profile.
func profilesProject() *compose.Project {
	return project("demo", map[string]*compose.Service{
		"web":   {Image: "web:latest"},
		"debug": {Image: "debug:latest", Profiles: []string{"debug"}},
	})
}

func startedDebug(t *testing.T, o *orchestrator.Orchestrator, log func() []string, args ...string) bool {
	t.Helper()
	if err := o.Up(true, args...); err != nil {
		t.Fatalf("Up %v: %v", args, err)
	}
	return indexOf(log(), "run -d --name debug.demo.opossum") >= 0
}

func TestUpProfilesGatedByDefault(t *testing.T) {
	rt, log := fakeShim(t)
	o := orchestrator.New(profilesProject(), rt, "opossum", &bytes.Buffer{})
	if startedDebug(t, o, log) {
		t.Error("a profiled service must not start by default")
	}
	if indexOf(log(), "run -d --name web.demo.opossum") < 0 {
		t.Error("a non-profiled service should always start")
	}
}

// A network used only by a profile-gated service isn't created when that profile
// is inactive — so an example can carry a "caged" (internal-net) variant behind a
// profile without a plain `up` creating a stray host-only network. Activating the
// profile does create it.
func TestUpSkipsNetworkForInactiveProfile(t *testing.T) {
	newProj := func() *compose.Project {
		p := project("demo", map[string]*compose.Service{
			"web":   {Image: "web:latest"},
			"caged": {Image: "caged:latest", Networks: compose.ServiceNetworks{"cage"}, Profiles: []string{"cage"}},
		})
		p.Networks = map[string]compose.NetworkDecl{"cage": {Internal: true}}
		return p
	}
	// Inactive profile: the internal net is never created.
	rt, log := fakeShim(t)
	if err := orchestrator.New(newProj(), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	for _, l := range log() {
		if strings.Contains(l, "network create") && strings.Contains(l, "demo-cage") {
			t.Errorf("inactive-profile internal net must not be created: %q", l)
		}
	}
	// Active profile: now it is.
	rt2, log2 := fakeShim(t)
	o2 := orchestrator.New(newProj(), rt2, "opossum", &bytes.Buffer{})
	o2.EnableProfiles([]string{"cage"})
	if err := o2.Up(true); err != nil {
		t.Fatalf("Up (profile active): %v", err)
	}
	if !hasLine(log2(), "network create --internal demo-cage") {
		t.Errorf("active-profile internal net should be created, got %v", log2())
	}
}

func TestUpProfilesActivatedStart(t *testing.T) {
	rt, log := fakeShim(t)
	o := orchestrator.New(profilesProject(), rt, "opossum", &bytes.Buffer{})
	o.EnableProfiles([]string{"debug"})
	if !startedDebug(t, o, log) {
		t.Error("a profiled service should start when its profile is active")
	}
}

func TestUpProfilesNamedServiceEnables(t *testing.T) {
	rt, log := fakeShim(t)
	o := orchestrator.New(profilesProject(), rt, "opossum", &bytes.Buffer{})
	// Naming a gated service on the command line enables it (docker compose parity).
	if !startedDebug(t, o, log, "debug") {
		t.Error("naming a profiled service should start it")
	}
}

// A started service that depends on a profile-gated, inactive service is an
// error — docker compose treats the gated dependency as undefined.
func TestUpProfilesDependencyOnDisabledErrors(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web":    {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "helper"}}},
		"helper": {Image: "helper:latest", Profiles: []string{"opt"}},
	})
	err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
	if err == nil || !strings.Contains(err.Error(), "profile is not active") {
		t.Fatalf("expected a disabled-dependency error, got %v", err)
	}
}

// A gated dependency whose profile IS active starts normally (no error) and the
// dependent runs too.
func TestUpProfilesActiveDependencyStarts(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web":    {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "helper"}}},
		"helper": {Image: "helper:latest", Profiles: []string{"opt"}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	o.EnableProfiles([]string{"opt"})
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	lines := log()
	if indexOf(lines, "run -d --name helper.demo.opossum") < 0 || indexOf(lines, "run -d --name web.demo.opossum") < 0 {
		t.Errorf("both helper (active profile) and web should start, got %v", lines)
	}
}

// A service listing several profiles is enabled if ANY of them is active.
func TestUpProfilesMultipleAnyActive(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"svc": {Image: "svc:latest", Profiles: []string{"a", "b"}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	o.EnableProfiles([]string{"b"}) // second profile active
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if indexOf(log(), "run -d --name svc.demo.opossum") < 0 {
		t.Errorf("service should start when any of its profiles is active, got %v", log())
	}
}

// `run` is consistent with `up`: a gated-inactive dependency is an error, not a
// silent force-start.
func TestRunProfilesDependencyOnDisabledErrors(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web":    {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "helper"}}},
		"helper": {Image: "helper:latest", Profiles: []string{"opt"}},
	})
	err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).RunOneOff("web", nil, orchestrator.RunOneOffOptions{})
	if err == nil || !strings.Contains(err.Error(), "profile is not active") {
		t.Fatalf("run should error on a gated-inactive dependency, got %v", err)
	}
}

func TestUpReportsExitedDependencyClearly(t *testing.T) {
	rt, log := fakeShim(t)
	setShimEnv(rt, "HEALTH_OK_AT=999")      // probe never passes
	setShimEnv(rt, "INSPECT_STATE=stopped") // the dependency container has exited
	p := project("demo", map[string]*compose.Service{
		"db": {
			Image:       "postgres:16",
			Healthcheck: &compose.Healthcheck{Test: []string{"pg_isready"}, Interval: time.Millisecond, Retries: 15},
		},
		"web": {
			Image:     "web:latest",
			DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}},
		},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	err := o.Up(true)
	if err == nil {
		t.Fatal("expected Up to fail when the dependency has exited")
	}
	// The error names the real cause and embeds the container's last logs (rather
	// than an opaque "healthcheck" or a stale `opossum logs` hint the rollback
	// breaks — the container is removed on failed up).
	msg := err.Error()
	if !strings.Contains(msg, "[OPSM-401]") || !strings.Contains(msg, "not running") || !strings.Contains(msg, "last log lines") || !strings.Contains(msg, "log-line db.demo.opossum") {
		t.Errorf("error should carry code OPSM-401 and embed the exited container's captured logs, got: %v", err)
	}
	if strings.Contains(msg, "opossum logs") {
		t.Errorf("error should not suggest `opossum logs` (rollback removes the container): %v", err)
	}
	// Fails fast: it bails after the first failed probe, not all 15.
	if n := countLines(log(), "exec db.demo.opossum"); n != 1 {
		t.Errorf("expected to bail after the first probe, got %d probes", n)
	}
}

func TestUpFailsWhenDependencyNeverHealthy(t *testing.T) {
	rt, log := fakeShim(t)
	setShimEnv(rt, "HEALTH_OK_AT=999") // never healthy within the retry budget
	p := project("demo", map[string]*compose.Service{
		"db": {
			Image: "postgres:16",
			Healthcheck: &compose.Healthcheck{
				Test:     []string{"pg_isready"},
				Interval: time.Millisecond,
				Retries:  2,
			},
		},
		"web": {
			Image:     "web:latest",
			DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}},
		},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	err := o.Up(true)
	if err == nil {
		t.Fatal("expected Up to fail when the dependency never becomes healthy")
	}
	if !strings.Contains(err.Error(), "db") || !strings.Contains(err.Error(), "healthcheck") {
		t.Errorf("error should name the unhealthy dependency and healthcheck, got: %v", err)
	}
	lines := log()
	// Retries were honored (exactly 2 attempts) and web never started.
	if n := countLines(lines, "exec db.demo.opossum"); n != 2 {
		t.Errorf("expected 2 probe attempts (Retries), got %d", n)
	}
	if indexOf(lines, "run -d --name web.demo.opossum") >= 0 {
		t.Errorf("web must NOT start when its dependency is unhealthy, got %v", lines)
	}
}

func TestUpRollsBackOnFailure(t *testing.T) {
	rt, log := fakeShim(t)
	setShimEnv(rt, "RUN_FAIL=web.demo.opossum") // web's run fails after db is up
	p := project("demo", map[string]*compose.Service{
		"db":  {Image: "postgres:16"},
		"web": {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "db"}}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err == nil {
		t.Fatal("expected Up to fail when a service run fails")
	}
	lines := log()
	// The network we created is removed, and the already-started db is torn down —
	// a failed up leaves no residue.
	if !hasLine(lines, "network delete demo-net") {
		t.Errorf("expected the created network to be rolled back, got %v", lines)
	}
	if !hasLine(lines, "stop db.demo.opossum") || !hasLine(lines, "delete --force db.demo.opossum") {
		t.Errorf("expected the started db to be torn down on rollback, got %v", lines)
	}
}

func TestUpDoesNotDeletePreexistingNetworkOnFailure(t *testing.T) {
	rt, log := fakeShim(t)
	setShimEnv(rt, "NET_EXISTS=1")              // network was already there (not ours)
	setShimEnv(rt, "RUN_FAIL=web.demo.opossum") // and the up fails partway
	p := project("demo", map[string]*compose.Service{
		"db":  {Image: "postgres:16"},
		"web": {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "db"}}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err == nil {
		t.Fatal("expected Up to fail")
	}
	lines := log()
	// Containers are still cleaned up, but a network we didn't create is left alone.
	if !hasLine(lines, "delete --force db.demo.opossum") {
		t.Errorf("expected started containers to be cleaned up, got %v", lines)
	}
	if hasLine(lines, "network delete demo-net") {
		t.Errorf("must NOT delete a network opossum did not create, got %v", lines)
	}
}

func TestUpRefusesForeignProjectContainer(t *testing.T) {
	rt, log := fakeShim(t)
	setShimEnv(rt, "INSPECT_PROJECT=otherproj") // db.demo.opossum is owned by another project
	p := project("demo", map[string]*compose.Service{
		"db": {Image: "postgres:16"},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	err := o.Up(true)
	if err == nil {
		t.Fatal("expected Up to refuse a container owned by another project")
	}
	if !strings.Contains(err.Error(), "otherproj") || !strings.Contains(err.Error(), "--dns-domain") {
		t.Errorf("error should name the owning project and suggest --dns-domain, got: %v", err)
	}
	// Crucially, opossum must NOT have force-deleted the other project's container.
	for _, l := range log() {
		if strings.HasPrefix(l, "delete --force") || strings.HasPrefix(l, "run ") {
			t.Errorf("no delete/run should happen for a foreign container, got %q", l)
		}
	}
}

func TestUpProceedsForSameProjectContainer(t *testing.T) {
	rt, log := fakeShim(t)
	setShimEnv(rt, "INSPECT_PROJECT=demo") // existing db.demo.opossum belongs to THIS project
	p := project("demo", map[string]*compose.Service{
		"db": {Image: "postgres:16"},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("re-up of the same project should proceed: %v", err)
	}
	lines := log()
	// Same project: stale cleanup + fresh run, tagged with our project label.
	if !hasLine(lines, "delete --force db.demo.opossum") {
		t.Errorf("expected stale-delete of our own container, got %v", lines)
	}
	if indexOf(lines, "run -d --name db.demo.opossum") < 0 || indexOf(lines, "-l opossum.project=demo") < 0 {
		t.Errorf("expected db to run with the project label, got %v", lines)
	}
}

func TestUpPartialStartsOnlyRequestedAndDeps(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"db":     {Image: "postgres:16"},
		"cache":  {Image: "redis:7"},
		"web":    {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "db"}, {Name: "cache"}}},
		"worker": {Image: "worker:latest", DependsOn: compose.DependsOn{{Name: "db"}}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true, "web"); err != nil {
		t.Fatalf("Up: %v", err)
	}
	lines := log()

	// web plus its transitive deps (db, cache) start; the unrelated worker does not.
	for _, svc := range []string{"db", "cache", "web"} {
		if indexOf(lines, "run -d --name "+svc+".demo.opossum") < 0 {
			t.Errorf("expected %s to start for `up web`, got %v", svc, lines)
		}
	}
	if indexOf(lines, "run -d --name worker.demo.opossum") >= 0 {
		t.Errorf("worker is unrelated to web and must NOT start, got %v", lines)
	}
	// Dependencies still precede the requested service.
	if d, w := indexOf(lines, "run -d --name db.demo.opossum"), indexOf(lines, "run -d --name web.demo.opossum"); d < 0 || d > w {
		t.Errorf("db must start before web (db=%d web=%d)", d, w)
	}
}

func TestUpPartialUnknownServiceRejected(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{"db": {Image: "postgres:16"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true, "nope"); err == nil {
		t.Fatal("expected an error for an unknown service")
	}
	// Nothing should have been started (the network create may run first, but no
	// service run should appear).
	for _, l := range log() {
		if strings.HasPrefix(l, "run ") {
			t.Errorf("no service should start for an unknown request, got %q", l)
		}
	}
}

func TestUpNoArgsStartsAll(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"db":  {Image: "postgres:16"},
		"web": {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "db"}}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil { // no service args = whole project
		t.Fatalf("Up: %v", err)
	}
	lines := log()
	if indexOf(lines, "run -d --name db.demo.opossum") < 0 || indexOf(lines, "run -d --name web.demo.opossum") < 0 {
		t.Errorf("bare `up` should start every service, got %v", lines)
	}
}

func TestLogsAllServicesInOrder(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"db":  {Image: "postgres:16"},
		"web": {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "db"}}},
	})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	if err := o.Logs(nil, runtime.LogsOptions{}); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	lines := log()
	// With no service named, every service is shown, mapped to its container
	// name, in dependency order (db before web).
	if !hasLine(lines, "logs db.demo.opossum") || !hasLine(lines, "logs web.demo.opossum") {
		t.Errorf("expected logs for both services, got %v", lines)
	}
	if d, w := indexOf(lines, "logs db.demo.opossum"), indexOf(lines, "logs web.demo.opossum"); d < 0 || w < 0 || d > w {
		t.Errorf("db logs should come before web (db=%d web=%d)", d, w)
	}
	// Multiple services get a per-service header on stdout.
	if !strings.Contains(out.String(), "==> db <==") {
		t.Errorf("expected a per-service header, got:\n%s", out.String())
	}
}

func TestLogsSelectedServiceWithFollow(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"db":  {Image: "postgres:16"},
		"web": {Image: "web:latest"},
	})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	if err := o.Logs([]string{"web"}, runtime.LogsOptions{Follow: true}); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	lines := log()
	if !hasLine(lines, "logs -f web.demo.opossum") {
		t.Errorf("expected followed logs for web only, got %v", lines)
	}
	// Only the named service is shown; a single stream gets no header.
	if hasLine(lines, "logs -f db.demo.opossum") || strings.Contains(out.String(), "==>") {
		t.Errorf("only web should be followed, with no header; got %v / %q", lines, out.String())
	}
}

// `logs -f` across several services multiplexes their streams into one output,
// each line prefixed with the service name (#148).
func TestLogsFollowMultipleMultiplexed(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest"},
		"api": {Image: "api:latest"}, // same length as web → no prefix padding
	})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	if err := o.Logs(nil, runtime.LogsOptions{Follow: true}); err != nil { // all services + follow
		t.Fatalf("Logs: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "web | log-line web.demo.opossum") {
		t.Errorf("web logs should be multiplexed with a service prefix, got:\n%s", s)
	}
	if !strings.Contains(s, "api | log-line api.demo.opossum") {
		t.Errorf("api logs should be multiplexed with a service prefix, got:\n%s", s)
	}
}

func TestLogsUnknownServiceRejected(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{"db": {Image: "postgres:16"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Logs([]string{"nope"}, runtime.LogsOptions{}); err == nil {
		t.Fatal("expected an error for an unknown service")
	}
	if len(log()) != 0 {
		t.Errorf("no logs command should be emitted for an unknown service, got %v", log())
	}
}

// completedDepsProject: `migrate` is a one-shot; `web` waits for it to finish
// successfully before starting.
func completedDepsProject() *compose.Project {
	return project("demo", map[string]*compose.Service{
		"migrate": {Image: "migrate:latest", Command: []string{"./migrate"}},
		"web": {
			Image:     "web:latest",
			DependsOn: compose.DependsOn{{Name: "migrate", Condition: compose.ConditionCompleted}},
		},
	})
}

func TestUpRunsCompletedDependencyToCompletion(t *testing.T) {
	rt, log := fakeShim(t)
	var out bytes.Buffer
	o := orchestrator.New(completedDepsProject(), rt, "opossum", &out)
	if err := o.Up(true); err != nil { // detached up …
		t.Fatalf("Up: %v", err)
	}
	lines := log()

	// … but the one-shot dependency runs in the FOREGROUND (no -d) so its exit
	// code is observable, while the long-running dependent keeps -d.
	if !hasLine(lines, "run --name migrate.demo.opossum --network demo-net --dns-domain opossum --dns-search demo.opossum -l opossum.project=demo migrate:latest ./migrate") {
		t.Errorf("migrate should run foreground (no -d) to completion, got %v", lines)
	}
	if !hasLine(lines, "run -d --name web.demo.opossum --network demo-net --dns-domain opossum --dns-search demo.opossum -l opossum.project=demo web:latest") {
		t.Errorf("web should run detached after migrate, got %v", lines)
	}
	// Ordering: migrate completes before web starts.
	mIdx := indexOf(lines, "run --name migrate.demo.opossum")
	wIdx := indexOf(lines, "run -d --name web.demo.opossum")
	if mIdx < 0 || wIdx < 0 || mIdx > wIdx {
		t.Errorf("migrate(%d) must run to completion before web(%d) in %v", mIdx, wIdx, lines)
	}
	if !strings.Contains(out.String(), "Running migrate to completion") {
		t.Errorf("expected a run-to-completion message, got:\n%s", out.String())
	}
}

func TestUpFailsWhenCompletedDependencyExitsNonZero(t *testing.T) {
	rt, log := fakeShim(t)
	setShimEnv(rt, "RUN_FAIL=migrate.demo.opossum") // migrate's process exits non-zero
	o := orchestrator.New(completedDepsProject(), rt, "opossum", &bytes.Buffer{})
	err := o.Up(true)
	if err == nil {
		t.Fatal("expected Up to fail when a completed-successfully dependency exits non-zero")
	}
	if !strings.Contains(err.Error(), "migrate") || !strings.Contains(err.Error(), "complete") {
		t.Errorf("error should name the failed dependency, got: %v", err)
	}
	lines := log()
	// web must never start once its one-shot dependency failed.
	if indexOf(lines, "run -d --name web.demo.opossum") >= 0 {
		t.Errorf("web must NOT start when migrate fails, got %v", lines)
	}
}

// fakeFootprinter is an injected HostFootprinter returning fixed host bytes.
type fakeFootprinter map[string]int64

func (f fakeFootprinter) Footprints() map[string]int64 { return f }

// stats --host pairs each service's guest-view usage (from the runtime) with the
// host memory its VM occupies (from the footprinter), sums the mapped ones, and
// renders an em dash for any it can't map — never failing.
func TestStatsHost(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web":   {Image: "web:latest"},
		"db":    {Image: "db:latest"},
		"cache": {Image: "cache:latest"}, // unmapped -> "—", excluded from total
	})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	o.HostFP = fakeFootprinter{
		"web.demo.opossum": 305 * 1024 * 1024,
		"db.demo.opossum":  340 * 1024 * 1024,
	}
	if err := o.StatsHost(nil); err != nil {
		t.Fatalf("StatsHost: %v", err)
	}
	s := out.String()
	for _, want := range []string{"305MiB", "340MiB"} { // each mapped host footprint
		if !strings.Contains(s, want) {
			t.Errorf("host footprint %s should render, got:\n%s", want, s)
		}
	}
	if !strings.Contains(s, "1GiB") { // guest limit from the stats snapshot
		t.Errorf("guest limit should render, got:\n%s", s)
	}
	if !strings.Contains(s, "—") { // cache couldn't be mapped
		t.Errorf("an unmapped service should render an em dash, got:\n%s", s)
	}
	// The total is the exact sum of the mapped footprints only (305+340=645), not a
	// double-count and not inflated by the unmapped cache.
	if !strings.Contains(s, "total") || !strings.Contains(s, "645MiB") {
		t.Errorf("total should be the exact sum of mapped footprints (645MiB), got:\n%s", s)
	}
}

// With nothing mappable (not on macOS, or all VMs gone), stats --host still
// renders — every host cell an em dash, no total — and returns no error.
func TestStatsHostAllUnmapped(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	o.HostFP = fakeFootprinter{} // maps nothing
	if err := o.StatsHost(nil); err != nil {
		t.Fatalf("StatsHost: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "—") {
		t.Errorf("host cell should be an em dash when unmapped, got:\n%s", s)
	}
	if strings.Contains(s, "total") {
		t.Errorf("no total line when nothing is mapped, got:\n%s", s)
	}
}

// A service mounting the Docker socket gets an up-front warning: Apple container
// has no Docker socket, so the mount fails and Docker-driving tools can't work.
func TestUpWarnsOnDockerSocketMount(t *testing.T) {
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		// A path that is not on this machine: the warning is about what the compose
		// file says, and has to read the same whether or not Docker is installed on
		// the host running the eval. (When the path really is a symlink to a socket,
		// the up is refused outright — TestUpRefusesASymlinkedSocketMount.)
		"portainer": {Image: "portainer/portainer-ce",
			Volumes: []string{filepath.Join(t.TempDir(), "absent", "docker.sock") + ":/var/run/docker.sock"}},
	})
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !strings.Contains(out.String(), "[OPSM-204]") || !strings.Contains(out.String(), "docker.sock") || !strings.Contains(out.String(), "no Docker daemon socket") {
		t.Errorf("mounting the Docker socket should warn with code OPSM-204, got:\n%s", out.String())
	}
}

// Where it stops matters, and the documentation now says so: this is not a
// pre-flight. Services earlier in the order are already running when the bad
// mount is reached, and the rollback takes them away again. AGENTS.md used to
// claim the up stopped "before starting anything", which measurement showed was
// false — so the true shape needs a test, or the next person will write the
// comfortable sentence again.
func TestABadMountStopsAtItsOwnServiceAndRollsBackTheRest(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a read-only directory")
	}
	parent := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o700) })

	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"aaa": {Image: "a:1"},
		"zzz": {Image: "z:1", Volumes: []string{filepath.Join(parent, "data") + ":/data"},
			DependsOn: []compose.Dependency{{Name: "aaa"}}},
	})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err == nil {
		t.Fatal("the bad mount must fail the up")
	}

	lines := log()
	started := indexOf(lines, "run -d --name aaa.demo.opossum")
	if started < 0 {
		t.Fatalf("the service before it should have started — that is what makes this not a pre-flight: %v", lines)
	}
	// And it does not stay up: the failure is a bring-up failure, so the rollback
	// removes what this up created.
	if stopped := indexOf(lines, "stop aaa.demo.opossum"); stopped < started {
		t.Errorf("the service started before the failure must be rolled back, got: %v", lines)
	}
	// The one with the bad mount never ran at all.
	if indexOf(lines, "run -d --name zzz.demo.opossum") >= 0 {
		t.Errorf("the service whose mount cannot work must not be started: %v", lines)
	}
}

// The helper refusing is not the same as the up refusing: nothing here used to
// check that `Up` passes it on, which is the half that actually protects a user.
// (The same shape as the seeding helper that was guarded while its caller was
// not.) The up must stop before it starts anything.
func TestUpStopsWhenABindSourceCannotBeCreated(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a read-only directory")
	}
	parent := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o700) })

	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Volumes: []string{filepath.Join(parent, "data") + ":/data"}},
	})
	err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
	if err == nil {
		t.Fatal("a bind source that cannot be created must fail the up")
	}
	if !strings.Contains(err.Error(), "[OPSM-104]") {
		t.Errorf("the error should carry the code, got: %v", err)
	}
	for _, line := range log() {
		if strings.HasPrefix(line, "run -d") {
			t.Errorf("the service was started even though its mount could not work: %q", line)
		}
	}
}

// socketAt makes a real unix socket, outside t.TempDir() because a socket path is
// capped near 104 bytes and the name this package's tests generate blows past it —
// a listen that fails turns the eval into a skip, and a skipped eval guards
// nothing.
func socketAt(t *testing.T) (path string, dir string) {
	t.Helper()
	d, err := os.MkdirTemp("", "sk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	p := filepath.Join(d, "s.sock")
	l, err := net.Listen("unix", p)
	if err != nil {
		t.Fatalf("could not make a unix socket at %s (%d bytes): %v", p, len(p), err)
	}
	t.Cleanup(func() { l.Close() })
	return p, d
}

// A symlink whose target is a socket cannot be mounted here, and that exact
// combination is the whole of it: measured against `container` 1.1.0, a socket by
// its own path mounts fine, a symlink to a file or a directory mounts fine, and
// only the two together fail with `mount failed with errno 95`.
//
// Where Docker Desktop is installed it links `/var/run/docker.sock` to a socket,
// which is how an earlier version of this read the failure as "a socket cannot be
// a bind source" and refused mounts that work.
func TestUpRefusesASymlinkedSocketMount(t *testing.T) {
	sock, dir := socketAt(t)
	link := filepath.Join(dir, "docker.sock")
	if err := os.Symlink(sock, link); err != nil {
		t.Fatal(err)
	}

	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"ci": {Image: "someci", Volumes: []string{link + ":/var/run/docker.sock"}},
	})
	var out bytes.Buffer
	err := orchestrator.New(p, rt, "opossum", &out).Up(true)
	if err == nil {
		t.Fatal("a mount that cannot work must fail the up rather than be attempted")
	}
	if !strings.Contains(err.Error(), "[OPSM-109]") || !strings.Contains(err.Error(), "errno 95") {
		t.Errorf("the error should name the code and what the runtime does, got: %v", err)
	}
	// The way out is in the message: the thing the link points at does mount.
	if !strings.Contains(err.Error(), sock) {
		t.Errorf("the error should name what the link resolves to, got: %v", err)
	}
	for _, line := range log() {
		if strings.HasPrefix(line, "run ") || strings.HasPrefix(line, "network create") {
			t.Errorf("the up should have stopped before touching the runtime, got: %q", line)
		}
	}
}

// The three shapes that DO work must keep working — this is where the previous
// attempt went wrong, refusing every socket and so breaking ups that were fine.
func TestUpDoesNotRefuseTheMountsThatWork(t *testing.T) {
	sock, dir := socketAt(t)
	fileLink := filepath.Join(dir, "to-file")
	dirLink := filepath.Join(dir, "to-dir")
	sub := filepath.Join(dir, "sub")
	plain := filepath.Join(dir, "plain.txt")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(plain, fileLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sub, dirLink); err != nil {
		t.Fatal(err)
	}

	for name, src := range map[string]string{
		"a socket by its own path": sock,
		"a symlink to a file":      fileLink,
		"a symlink to a directory": dirLink,
	} {
		t.Run(name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			p := project("demo", map[string]*compose.Service{
				"app": {Image: "app:1", Volumes: []string{src + ":/x"}},
			})
			if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
				t.Errorf("this mount works on the real runtime, so the up must not refuse it: %v", err)
			}
		})
	}
}

// Refusing the first volume of the first service is the easy half. These are the
// positions a check written as one nested loop quietly stops looking at, and each
// row is a mutation that survived the first version of this eval: examining only
// order[0], examining only Volumes[0], and reading the mount as written instead of
// resolving it. The last one is the least academic — a compose bind source is
// usually relative, so an unresolved path simply never matches anything on disk.
func TestUpFindsTheSymlinkedSocketWhereverItSits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		place func(link string) (svcs map[string]*compose.Service, mount string)
	}{
		{"not the first service", func(link string) (map[string]*compose.Service, string) {
			return map[string]*compose.Service{
				"aaa": {Image: "a:1"},
				"zzz": {Image: "z:1", Volumes: []string{link + ":/var/run/docker.sock"},
					DependsOn: compose.DependsOn{{Name: "aaa"}}},
			}, link
		}},
		{"not the first volume", func(link string) (map[string]*compose.Service, string) {
			return map[string]*compose.Service{
				"ci": {Image: "ci:1", Volumes: []string{
					"data:/data",
					link + ":/var/run/docker.sock",
				}},
			}, link
		}},
		{"written relative to the compose file", func(link string) (map[string]*compose.Service, string) {
			return map[string]*compose.Service{
				"ci": {Image: "ci:1", Volumes: []string{"./" + filepath.Base(link) + ":/var/run/docker.sock"}},
			}, link
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sock, dir := socketAt(t)
			link := filepath.Join(dir, "docker.sock")
			if err := os.Symlink(sock, link); err != nil {
				t.Fatal(err)
			}
			svcs, _ := tc.place(link)

			rt, log := fakeShim(t)
			p := project("demo", svcs)
			// The relative row needs the compose file to sit beside the link;
			// harmless for the others.
			p.BaseDir = dir
			err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
			if err == nil {
				t.Fatal("a mount that cannot work must fail the up wherever it appears")
			}
			if !strings.Contains(err.Error(), "[OPSM-109]") {
				t.Errorf("want the symlinked-socket refusal, got: %v", err)
			}
			// And it is still a pre-flight: nothing started, so there is nothing
			// to roll back.
			for _, line := range log() {
				if strings.HasPrefix(line, "run ") || strings.HasPrefix(line, "network create") {
					t.Errorf("the up should have stopped before touching the runtime, got: %q", line)
				}
			}
		})
	}
}

// A bind source that is a symlink pointing at nothing is a different problem with
// different words (OPSM-104), and it reaches the socket check first: Stat fails
// there rather than reporting a mode. Without this, dropping the error guard on
// that Stat leaves every eval green while `up` panics on the nil result.
func TestUpDoesNotReadADanglingSymlinkAsASocket(t *testing.T) {
	_, dir := socketAt(t)
	link := filepath.Join(dir, "docker.sock")
	if err := os.Symlink(filepath.Join(dir, "nothing-here"), link); err != nil {
		t.Fatal(err)
	}

	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"ci": {Image: "ci:1", Volumes: []string{link + ":/var/run/docker.sock"}},
	})
	err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
	if err == nil {
		t.Fatal("a bind source that points at nothing must fail the up")
	}
	if strings.Contains(err.Error(), "[OPSM-109]") || strings.Contains(err.Error(), "errno 95") {
		t.Errorf("a dangling link is not a socket mount; the message should say what it is, got: %v", err)
	}
}

// `opossum run` reaches the runtime by its own path, and a check wired into `up`
// alone is a hole — this is the second time a mount check has had to be carried
// across to the one-off, so it gets an eval of its own rather than a promise.
func TestRunRefusesASymlinkedSocketMount(t *testing.T) {
	sock, dir := socketAt(t)
	link := filepath.Join(dir, "docker.sock")
	if err := os.Symlink(sock, link); err != nil {
		t.Fatal(err)
	}

	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"ci": {Image: "ci:1", Volumes: []string{link + ":/var/run/docker.sock"}},
	})
	err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).
		RunOneOff("ci", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: true})
	if err == nil {
		t.Fatal("`run` must refuse the mount `up` refuses, not hand it to the runtime")
	}
	if !strings.Contains(err.Error(), "[OPSM-109]") {
		t.Errorf("want the symlinked-socket refusal, got: %v", err)
	}
	for _, line := range log() {
		if strings.HasPrefix(line, "run ") {
			t.Errorf("the one-off should have stopped before the mount was issued, got: %q", line)
		}
	}
}

// A dry run is exactly where you want to hear that the real one cannot work, so
// the refusal is not skipped there. It only reads the host — the checks skipped
// under --dry-run are the ones that create directories.
func TestUpDryRunStillRefusesASymlinkedSocketMount(t *testing.T) {
	sock, dir := socketAt(t)
	link := filepath.Join(dir, "docker.sock")
	if err := os.Symlink(sock, link); err != nil {
		t.Fatal(err)
	}

	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"ci": {Image: "ci:1", Volumes: []string{link + ":/var/run/docker.sock"}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	o.SetDryRun(true)
	if err := o.Up(true); err == nil || !strings.Contains(err.Error(), "[OPSM-109]") {
		t.Errorf("a dry run should report the mount that cannot work, got: %v", err)
	}
}

// mutatingVerbs are the container subcommands `up --dry-run` must never issue.
var mutatingVerbs = []string{"run ", "network create", "delete", "build ", "stop ", "kill ", "network delete", "volume delete"}

// A dry-run resolves the whole project but executes nothing: the fake shim
// records every invocation it actually receives, and none of the mutating verbs
// (run/create/delete/…) may appear. The read-only inspects that compute the plan
// are fine.
func TestUpDryRunExecutesNothing(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"db":  {Image: "postgres:16"},
		"web": {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "db"}}},
	})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	o.SetDryRun(true)
	if err := o.Up(true); err != nil {
		t.Fatalf("Up (dry-run): %v", err)
	}
	lines := log()
	for _, verb := range mutatingVerbs {
		if n := countLines(lines, verb); n > 0 {
			t.Errorf("dry-run must not invoke %q, but the runtime received it %d time(s): %v", verb, n, lines)
		}
	}
	// The plan is surfaced to the user: the startup order and the run commands.
	s := out.String()
	if !strings.Contains(s, "Dry run") || !strings.Contains(s, "Startup order: db, web") {
		t.Errorf("dry-run should print a header and the startup order, got:\n%s", s)
	}
	if !strings.Contains(s, "run -d --name db.demo.opossum") ||
		!strings.Contains(s, "run -d --name web.demo.opossum") {
		t.Errorf("dry-run should print the run commands it would issue, got:\n%s", s)
	}
}

// A dry-run's recorded plan is exactly what a real up would issue: every command
// the dry-run says it would run appears (verbatim, config-hash aside) in the
// invocation log of a real up over the same compose. This guards the (b) eval —
// dry-run output ⊆ real execution — so the plan can't drift from reality.
func TestUpDryRunPlanMatchesRealUp(t *testing.T) {
	svcs := func() map[string]*compose.Service {
		return map[string]*compose.Service{
			"db":    {Image: "postgres:16", Environment: compose.Environment{"POSTGRES_PASSWORD=secret"}},
			"cache": {Image: "redis:7"},
			"web": {
				Image:     "web:latest",
				Ports:     []string{"8080:8080"},
				DependsOn: compose.DependsOn{{Name: "db"}, {Name: "cache"}},
			},
		}
	}

	// Dry-run: capture the recorded plan (the argv it would issue).
	rtDry, _ := fakeShim(t)
	oDry := orchestrator.New(project("demo", svcs()), rtDry, "opossum", &bytes.Buffer{})
	oDry.SetDryRun(true)
	if err := oDry.Up(true); err != nil {
		t.Fatalf("Up (dry-run): %v", err)
	}
	if len(rtDry.Plan) == 0 {
		t.Fatal("dry-run recorded no planned commands")
	}

	// Real up over the same compose: the fake shim logs what actually ran.
	rtReal, logReal := fakeShim(t)
	if err := orchestrator.New(project("demo", svcs()), rtReal, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("Up (real): %v", err)
	}
	realLog := logReal() // already config-hash-stripped by fakeShim's reader

	for _, planned := range rtDry.Plan {
		planned = stripConfigHash(planned)
		if !hasLine(realLog, planned) {
			t.Errorf("planned command not issued by a real up:\n planned: %q\n log:     %v", planned, realLog)
		}
	}
	// Sanity: the plan actually covers the runs (not just the network).
	if countLines(rtDry.Plan, "run -d --name") != 3 {
		t.Errorf("dry-run plan should record all three service runs, got: %v", rtDry.Plan)
	}
}

// The --from-docker-compose import path issues `container image load`/`image tag` (and a
// `docker image save`) OUTSIDE the recording seam, so a dry-run must guard it
// explicitly: nothing may execute, and the plan records the import.
func TestUpDryRunFromDockerExecutesNothing(t *testing.T) {
	rt, log := fakeShim(t)
	setShimEnv(rt, "IMAGE_ABSENT=demo-api:latest") // the build image isn't present -> import path
	p := project("demo", map[string]*compose.Service{
		"api": {Build: &compose.Build{Context: "."}},
	})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	o.SetUpOptions(false, false, false, false, true) // --from-docker-compose (must precede SetDryRun)
	o.SetDryRun(true)
	if err := o.Up(true); err != nil {
		t.Fatalf("Up (dry-run --from-docker-compose): %v", err)
	}
	lines := log()
	for _, v := range []string{"image load", "image tag", "run -d"} {
		if n := countLines(lines, v); n > 0 {
			t.Errorf("dry-run --from-docker-compose must not execute %q, ran it %d time(s): %v", v, n, lines)
		}
	}
	if !strings.Contains(out.String(), "image load") {
		t.Errorf("dry-run --from-docker-compose should record the import in the plan, got:\n%s", out.String())
	}
}

// The host-port pre-flight has to see an IPv4-only listener. Asking the OS for a
// plain "tcp" wildcard bind gets a dual-stack IPv6 socket, which on macOS binds
// alongside an existing IPv4 listener and reports the port free — and the daemons
// that actually squat ports (AirPlay's receiver on 5000/7000) are IPv4-only, so
// that blind spot covers the case the check exists for.
func TestUpDetectsIPv4OnlyHostPortConflict(t *testing.T) {
	l, err := net.Listen("tcp4", "0.0.0.0:0") // IPv4 only, all interfaces
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:80", port)}},
	})
	err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
	if err == nil || !strings.Contains(err.Error(), "[OPSM-201]") {
		t.Errorf("an IPv4-only listener must be detected as a host-port conflict, got %v", err)
	}
}

// …and a port nobody holds still reads as free, so the wider probe doesn't start
// inventing conflicts.
func TestUpAllowsPortAfterListenerClosed(t *testing.T) {
	l, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:80", port)}},
	})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Errorf("a free port must not be reported as in use, got %v", err)
	}
}

// A container-only port entry (`ports: ["3000"]`) leaves the host port to the
// engine, so when the port opossum mirrored is taken it publishes on a free one
// instead of failing — docker compose does the same. The run command must carry
// the new port, and the notice must say where it went.
func TestUpRemapsBareHostPortWhenTaken(t *testing.T) {
	// Occupy the port. The probe binds an IPv4 wildcard, which conflicts with this
	// listener — the same way a real squatter (AirPlay's receiver) would.
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	spec := fmt.Sprintf("%d:%d", port, port)

	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {
			Image:        "web:latest",
			Ports:        []string{spec},
			AutoHostPort: map[string]bool{spec: true}, // as Load marks a bare "3000"
		},
	})
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("a container-only port should fall back to a free host port, got %v", err)
	}
	calls := strings.Join(log(), "\n")
	if strings.Contains(calls, fmt.Sprintf("-p %s", spec)) {
		t.Errorf("the taken host port should not have been published, calls:\n%s", calls)
	}
	if !strings.Contains(calls, fmt.Sprintf(":%d", port)) {
		t.Errorf("the container port should still be published somewhere, calls:\n%s", calls)
	}
	if !strings.Contains(out.String(), "[OPSM-206]") || !strings.Contains(out.String(), "opossum ps") {
		t.Errorf("the remap should be announced and point at `opossum ps`, got:\n%s", out.String())
	}
}

// An explicit mapping is a contract the user wrote down: it must still fail
// loudly rather than quietly listening on some other port.
func TestUpDoesNotRemapExplicitHostPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Ports: []string{fmt.Sprintf("127.0.0.1:%d:80", port)}},
	})
	err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
	if err == nil || !strings.Contains(err.Error(), "[OPSM-201]") {
		t.Errorf("an explicit host port must still fail when taken, got %v", err)
	}
}

// A free mirrored port is left exactly as it is — the fallback must not fire, and
// must not perturb the predictable same-number mapping when nothing is wrong.
func TestUpKeepsBareHostPortWhenFree(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close() // free it again

	spec := fmt.Sprintf("%d:%d", port, port)
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Ports: []string{spec}, AutoHostPort: map[string]bool{spec: true}},
	})
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("up: %v", err)
	}
	if !strings.Contains(strings.Join(log(), "\n"), spec) {
		t.Errorf("a free mirrored port should be published unchanged, calls:\n%s", strings.Join(log(), "\n"))
	}
	if strings.Contains(out.String(), "[OPSM-206]") {
		t.Errorf("no remap notice should appear when the port was free, got:\n%s", out.String())
	}
}

// A service with BOTH kinds of entry is where the distinction actually bites: the
// container-only one moves, the explicit one still fails. Without a per-spec
// check, having any movable port on the service would make every port movable.
func TestUpRemapsOnlyBarePortOnMixedService(t *testing.T) {
	bare, err := net.Listen("tcp", ":0") // occupied on every interface, like AirPlay
	if err != nil {
		t.Fatal(err)
	}
	defer bare.Close()
	barePort := bare.Addr().(*net.TCPAddr).Port

	explicit, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer explicit.Close()
	explicitPort := explicit.Addr().(*net.TCPAddr).Port

	bareSpec := fmt.Sprintf("%d:%d", barePort, barePort)
	explicitSpec := fmt.Sprintf("127.0.0.1:%d:80", explicitPort)

	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {
			Image:        "web:latest",
			Ports:        []string{bareSpec, explicitSpec},
			AutoHostPort: map[string]bool{bareSpec: true}, // only the bare one
		},
	})
	err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
	if err == nil || !strings.Contains(err.Error(), "[OPSM-201]") {
		t.Fatalf("the explicit mapping must still fail even alongside a movable port, got %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d/tcp", explicitPort)) {
		t.Errorf("the conflict should name the explicit port %d, got %v", explicitPort, err)
	}
	// The bare one was moved out of the way rather than reported as a conflict.
	if strings.Contains(err.Error(), fmt.Sprintf("%d/tcp", barePort)) {
		t.Errorf("the container-only port should have been remapped, not reported, got %v", err)
	}
}

// A remapped port must survive the next `up`: reusing what this service's own
// running container publishes is what keeps the config hash — and so the
// "unchanged, leave it alone" decision — stable. Without it a remapped service
// would look different every run and be recreated forever.
func TestUpReusesOwnPublishedPortOnRerun(t *testing.T) {
	l, err := net.Listen("tcp4", "0.0.0.0:0") // hold the mirror for both runs
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	spec := fmt.Sprintf("%d:%d", port, port)

	rt, log := fakeShim(t)
	setShimEnv(rt, "INSPECT_PROJECT=demo")
	newProj := func() *compose.Project {
		return project("demo", map[string]*compose.Service{
			"web": {Image: "web:latest", Ports: []string{spec}, AutoHostPort: map[string]bool{spec: true}},
		})
	}
	if err := orchestrator.New(newProj(), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}
	first := publishedHostPort(t, strings.Join(log(), "\n"), port)

	if err := orchestrator.New(newProj(), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("second up: %v", err)
	}
	second := publishedHostPort(t, strings.Join(log(), "\n"), port)
	if first != second {
		t.Errorf("a remapped port should be reused on the next up: first=%d second=%d", first, second)
	}
}

// publishedHostPort returns the host port of the LAST `-p <host>:<container>`
// published for container port want.
func publishedHostPort(t *testing.T, calls string, want int) int {
	t.Helper()
	re := regexp.MustCompile(fmt.Sprintf(`-p (\d+):%d\b`, want))
	m := re.FindAllStringSubmatch(calls, -1)
	if len(m) == 0 {
		t.Fatalf("no published mapping for container port %d in:\n%s", want, calls)
	}
	n, err := strconv.Atoi(m[len(m)-1][1])
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Two services that both leave the host port to the engine must not be handed the
// same one — probing the OS alone can't see a port this very run already gave out.
func TestUpGivesDistinctPortsToTwoBareServices(t *testing.T) {
	l, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	l.Close() // a FREE port both services would mirror
	port := l.Addr().(*net.TCPAddr).Port
	spec := fmt.Sprintf("%d:%d", port, port)

	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Ports: []string{spec}, AutoHostPort: map[string]bool{spec: true}},
		"api": {Image: "api:latest", Ports: []string{spec}, AutoHostPort: map[string]bool{spec: true}},
	})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("up: %v", err)
	}
	re := regexp.MustCompile(fmt.Sprintf(`-p (\d+):%d\b`, port))
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(strings.Join(log(), "\n"), -1) {
		if seen[m[1]] {
			t.Errorf("both services were published on host port %s", m[1])
		}
		seen[m[1]] = true
	}
	if len(seen) != 2 {
		t.Errorf("expected two distinct host ports, got %v", seen)
	}
}

// Started() is what decides which services get a restart supervisor, so it has
// to mean "still running" — not "we got as far as starting it". When the
// bring-up fails, Up rolls the stack back and deletes what it made, and a
// caller acting on a stale list would announce it was watching containers that
// no longer exist.
func TestStartedIsEmptyAfterARolledBackUp(t *testing.T) {
	rt, _ := fakeShim(t)
	// web is declared absent because the bring-up never reaches it, and the shim's
	// default is that every container exists — without this it would be reported as
	// a survivor of a call that never created it. (db needs no such declaration: it
	// is created by this call, so it is excluded whatever the runtime says.)
	setShimEnv(rt, "HEALTH_HANG=1", "INSPECT_ABSENT=web.demo.opossum") // db never becomes healthy
	p := project("demo", map[string]*compose.Service{
		"db": {
			Image:   "postgres:16",
			Restart: "always",
			Healthcheck: &compose.Healthcheck{
				Test:    []string{"pg_isready"},
				Timeout: 100 * time.Millisecond,
				Retries: 1,
			},
		},
		"web": {
			Image:     "web:latest",
			Restart:   "always",
			DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}},
		},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err == nil {
		t.Fatal("up should fail when a dependency never becomes healthy")
	}
	if got := o.Started(); len(got) != 0 {
		t.Errorf("nothing survives a rolled-back up, so Started() should be empty, got %v", got)
	}
}

// The other side of the same coin: a service that exits right after starting
// fails the up, but is NOT rolled back (that's a post-start health report, not a
// failed bring-up). Those containers are still there, still carrying a
// `restart:` policy, so Started() must still list them.
func TestStartedSurvivesAPostStartCrash(t *testing.T) {
	rt, _ := fakeShim(t)
	setShimEnv(rt, "INSPECT_STATE=stopped") // the container exits right after `run`
	p := project("demo", map[string]*compose.Service{"web": {Image: "web", Restart: "always"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	err := o.Up(true)
	if err == nil || !strings.Contains(err.Error(), "OPSM-407") {
		t.Fatalf("expected the post-start crash to fail the up, got %v", err)
	}
	if got := o.Started(); len(got) != 1 || got[0] != "web" {
		t.Errorf("a crashed-but-present service still needs watching; Started() = %v", got)
	}
}

// StillSupervised decides what a partial `up` carries over, so it has to answer
// two questions at once: would this project supervise the service at all, and is
// its container still there. Neither had a unit test.
func TestStillSupervised(t *testing.T) {
	rt, _ := fakeShim(t)
	setShimEnv(rt, "INSPECT_ABSENT=gone.demo.opossum")
	p := project("demo", map[string]*compose.Service{
		"web":     {Image: "web", Restart: "always"},
		"gone":    {Image: "g", Restart: "always"},
		"plain":   {Image: "p"},
		"migrate": {Image: "m", Restart: "always"},
		"app": {Image: "a", Restart: "always",
			DependsOn: compose.DependsOn{{Name: "migrate", Condition: compose.ConditionCompleted}}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	got := o.StillSupervised([]string{"web", "gone", "plain", "migrate", "absent-from-compose"})
	want := []string{"web"}
	if len(got) != len(want) || (len(got) > 0 && got[0] != want[0]) {
		t.Errorf("StillSupervised = %v, want %v — gone has no container, plain has no policy, "+
			"migrate runs to completion, and absent-from-compose isn't a service", got, want)
	}
}

// `nocopy` is the compose file saying "mount this empty" — the image's copy is
// stale, or huge, or the real content arrives another way. Seeding it anyway
// overrides an explicit instruction. Measured against docker compose v5.1.4,
// which leaves such a volume empty while seeding its neighbour
// (~/opossum-dogfood/results/df366-golden.md).
//
// The other mount in this fixture is what makes the test say something: with
// nocopy ignored, both are seeded and nothing distinguishes "honoured" from
// "seeding is off".
func TestUpDoesNotSeedANoCopyVolume(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {
			Image: "web:latest",
			// The trailing slash is deliberate: the target is normalised in one place
			// when NoCopy is recorded and in another when seeding checks it, and the
			// two have to agree. Written without it, either normalisation could be
			// dropped and nothing here would notice.
			Volumes: []string{"deps:/app/node_modules/", "data:/var/data"},
			// Both carry a trailing slash the mount target does not have to match on:
			// each side is normalised in a different place, so dropping either
			// normalisation alone fails here. (Dropping both at once would agree
			// again — but the load path always trims the recorded side, so only the
			// hand-built Service in this test can put a slash there at all.)
			NoCopy: []string{"/app/node_modules/"},
		},
	})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	lines := log()
	// A container still runs for it — the volume is one opossum is creating, and
	// `lost+found` has to come out of it like any other, or `nocopy` would be the
	// one way to ask for an empty volume and get a non-empty one. What must not
	// happen is the copy.
	nocopyRun := ""
	for _, l := range lines {
		if strings.Contains(l, "demo_deps:/__opossum_seed__") {
			nocopyRun = l
		}
	}
	if nocopyRun == "" {
		t.Fatalf("the new volume should still be prepared, got %v", lines)
	}
	if strings.Contains(nocopyRun, "cp -a") {
		t.Errorf("the compose file asked for this volume not to be filled: %s", nocopyRun)
	}
	if !strings.Contains(nocopyRun, "rmdir") {
		t.Errorf("an empty volume means empty, lost+found included: %s", nocopyRun)
	}
	// Its neighbour has no such request and is copied into as well.
	filled := ""
	for _, l := range lines {
		if strings.Contains(l, "demo_data:/__opossum_seed__") {
			filled = l
		}
	}
	if filled == "" || !strings.Contains(filled, "cp -a") {
		t.Errorf("a volume without the option should still be seeded, got %q of %v", filled, lines)
	}
	// The option is not a mount mode: it must not reach the runtime as one.
	for _, l := range lines {
		if strings.Contains(l, "nocopy") {
			t.Errorf("nocopy leaked into a runtime argument: %s", l)
		}
	}
}

// opossum fills a fresh volume by running `cp -a` inside a throwaway container
// built from the service's image. An image with no shell — most distroless and
// scratch builds — has nothing to run that with, so the volume mounts empty.
//
// That is indistinguishable from a service that lost its data, and until now
// opossum said nothing: the copy's exit status was discarded. The failure has to
// be visible at the moment it happens, because nothing later can tell the two
// apart. Verified on the real runtime too: `container run … distroless … sh -c`
// reports `failed to find target executable sh`.
func TestUpSaysWhenAVolumeCouldNotBeSeeded(t *testing.T) {
	rt, _ := fakeShim(t)
	setShimEnv(rt, "SEED_FAIL=1")
	var out bytes.Buffer
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "distroless:latest", Volumes: []string{"data:/var/data"}},
	})
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "OPSM-108") {
		t.Errorf("a volume that could not be filled should say so, got:\n%s", s)
	}
	// The three things a reader needs: which volume, why, and what to do instead.
	// The "why" is the runtime's own words, dug out of the nesting it arrives in —
	// asserting only that *something* was said would leave the digging deletable.
	for _, want := range []string{"demo_data", "failed to find target executable sh", "mount empty", "nocopy"} {
		if !strings.Contains(s, want) {
			t.Errorf("the warning should mention %q, got:\n%s", want, s)
		}
	}
	// The reason is the runtime's own words, passed through as written. opossum
	// does not unwrap the `internalError:` nesting Apple `container` puts around it:
	// parsing someone else's error format to shorten a warning is a dependency that
	// readability does not pay for, and the raw text is better evidence. So the
	// wrapper comes through too, and that is the intended shape.
	if !strings.Contains(s, "internalError:") {
		t.Errorf("the runtime's message should reach the reader as it was written:\n%s", s)
	}
	// The explanation still has to follow it — the warning is the reason AND what to
	// do about it, and a long first line must not have swallowed the rest.
	if i, j := strings.Index(s, "OPSM-108"), strings.Index(s, "nocopy"); i < 0 || j < i {
		t.Errorf("the guidance should follow the reason, not be lost before it:\n%s", s)
	}
	// A failure to seed is not a failure to start — the `Up` above returning nil is
	// what says so, and the t.Fatalf on it is the assertion. (An earlier version
	// looked for "starting service" in the output; that text only ever appears in
	// the returned error, so it asserted nothing.)
}

// Seeding an image whose default user is not root — node:*, most database images,
// anything with a `USER` line. A fresh volume's root belongs to 0:0 and is mode
// 755, so that user cannot create anything in it: measured on the real runtime,
// the copy moved nothing and the volume came up holding only ext4's `lost+found`.
// docker does not have the problem because the engine, which is root, does the
// copying; opossum asks for the same privilege with `--user 0`.
//
// The eval is the outcome, not the flag: the shim refuses the write unless root
// was asked for, so dropping the flag turns the seed into a failure the warning
// reports — which is what a user would have seen, minus the warning, before this.
func TestSeedsWithRootSoANonRootImageCanFillTheVolume(t *testing.T) {
	rt, log := fakeShim(t)
	setShimEnv(rt, "SEED_NONROOT=1")
	var out bytes.Buffer
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "node:20-alpine", Volumes: []string{"data:/var/data"}},
	})
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if indexOf(log(), "demo_data:/__opossum_seed__") < 0 {
		t.Fatalf("the volume was never seeded at all, so this asserts nothing: %v", log())
	}
	if s := out.String(); strings.Contains(s, "OPSM-108") {
		t.Errorf("the copy could not write into the fresh volume:\n%s", s)
	}
}

// The mirror of the above: a copy that starts and then fails is reported, not
// counted as a filled volume — the case OPSM-108 could not see when it only ever
// heard about containers that failed to start.
//
// What this does NOT guard is the seed script's own error handling: the shim never
// runs the script, it decides an exit code from the argv, so putting the old
// `2>/dev/null || true` back leaves this test green. That regression is held by
// TestSeedScriptReportsACopyItCannotMake in the runtime package, which runs the
// script under a real `sh`. Two halves of one behaviour, in the two places that
// can each see their half.
func TestUpSaysWhenTheSeedingCopyCouldNotWrite(t *testing.T) {
	rt, _ := fakeShim(t)
	setShimEnv(rt, "SEED_COPY_FAIL=1")
	var out bytes.Buffer
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "node:20-alpine", Volumes: []string{"data:/var/data"}},
	})
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "OPSM-108") {
		t.Errorf("a copy that wrote nothing must not pass for a filled volume:\n%s", s)
	}
	// The copy's own words, not a generic "seeding failed" — a permission failure
	// and a missing shell are different problems with different fixes.
	if !strings.Contains(s, "Permission denied") {
		t.Errorf("the warning should carry what the copy said:\n%s", s)
	}
}

// The warning is about a copy that could not run. A seeding container that works
// says nothing — a message on every fresh volume would be noise on the ordinary
// path, and noise is what stops the real one from being read.
func TestUpIsQuietWhenSeedingWorks(t *testing.T) {
	rt, _ := fakeShim(t)
	var out bytes.Buffer
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest", Volumes: []string{"data:/var/data"}},
	})
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if s := out.String(); strings.Contains(s, "OPSM-108") {
		t.Errorf("seeding succeeded, so there is nothing to report:\n%s", s)
	}
}
