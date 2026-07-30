package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// doctor's ❌→non-zero-exit contract (which CI and `opossum doctor && …` depend
// on) must return an error, not silently succeed. Pointing at a missing runtime
// makes the environment check fail.
func TestDoctorExitsNonZeroWhenUnhealthy(t *testing.T) {
	t.Setenv("OPOSSUM_CONTAINER_BIN", filepath.Join(t.TempDir(), "no-such-container"))
	root := newRootCmd()
	root.SetArgs([]string{"doctor"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	if err := root.Execute(); !errors.Is(err, errEnvUnhealthy) {
		t.Errorf("doctor should return errEnvUnhealthy (exit 1) when unhealthy, got %v", err)
	}
}

// doctor --format json must (a) emit valid JSON to stdout and (b) preserve the
// ❌→non-zero-exit contract: an unhealthy environment still returns errEnvUnhealthy,
// with the JSON reporting healthy:false and the failing check as status:"fail".
func TestDoctorJSONExitsNonZeroWhenUnhealthy(t *testing.T) {
	t.Setenv("OPOSSUM_CONTAINER_BIN", filepath.Join(t.TempDir(), "no-such-container"))
	root := newRootCmd()
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"doctor", "--format", "json"})
	if err := root.Execute(); !errors.Is(err, errEnvUnhealthy) {
		t.Errorf("doctor --format json should return errEnvUnhealthy when unhealthy, got %v", err)
	}

	var rep struct {
		Healthy bool `json:"healthy"`
		Checks  []struct {
			ID, Status, Detail, Fix string
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(out.String()), &rep); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, out.String())
	}
	if rep.Healthy {
		t.Error("healthy should be false when the runtime is unavailable")
	}
	if len(rep.Checks) == 0 || rep.Checks[0].ID != "runtime" || rep.Checks[0].Status != "fail" {
		t.Errorf("expected a runtime check with status fail; got %+v", rep.Checks)
	}
	if rep.Checks[0].Fix == "" {
		t.Error("a failing check should carry a non-empty fix hint")
	}
}

// fakeShimBin is the compiled fake `container` shim, built once for the package.
// A compiled binary spawns in ~1-2ms versus ~50-80ms for a /bin/sh script.
var fakeShimBin string

// opossumBin is a real build of the CLI. The supervisor works by re-invoking the
// binary, and under `go test` os.Executable() is the test binary — so without a
// genuine one no test can make a supervisor exist.
var opossumBin string

func TestMain(m *testing.M) {
	d, err := os.MkdirTemp("", "opossum-cmd-test-")
	if err != nil {
		panic(err)
	}
	opossumBin = filepath.Join(d, "opossum")
	if out, berr := exec.Command("go", "build", "-o", opossumBin, ".").CombinedOutput(); berr != nil {
		os.RemoveAll(d)
		panic(fmt.Sprintf("building opossum: %v\n%s", berr, out))
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

// fakeShim writes a `container` stand-in that logs each invocation to $FAKE_LOG
// and returns plausible output, then points OPOSSUM_CONTAINER_BIN at it.
func fakeShim(t *testing.T) func() []string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "invocations.log")
	t.Setenv("OPOSSUM_CONTAINER_BIN", fakeShimBin)
	t.Setenv("FAKE_LOG", logPath)
	return func() []string {
		b, err := os.ReadFile(logPath)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	}
}

// TestVerboseFlagAccepted checks the global --verbose flag parses and is wired
// through to a working run (the command trace itself goes to stderr; the
// runtime package owns that behavior).
func TestVerboseFlagAccepted(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, `
name: demo
services:
  web:
    image: web:latest
`)
	root := newRootCmd()
	root.SetArgs([]string{"-f", compose, "--verbose", "up"})
	if err := root.Execute(); err != nil {
		t.Fatalf("--verbose up: %v", err)
	}
}

func writeCompose(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// run executes the CLI with args and returns captured stdout plus any error.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// TestUpPartialCLI exercises the full CLI path: flag parsing, compose loading,
// and passing positional service args through to the orchestrator.
func TestUpPartialCLI(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, `
name: demo
services:
  db:
    image: postgres:16
  web:
    image: web:latest
    depends_on: [db]
  worker:
    image: worker:latest
`)
	root := newRootCmd()
	root.SetArgs([]string{"-f", compose, "up", "web"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	lines := readLog()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "run -d --name db.demo.opossum") ||
		!strings.Contains(joined, "run -d --name web.demo.opossum") {
		t.Errorf("`up web` should start web and its dep db, got:\n%s", joined)
	}
	if strings.Contains(joined, "worker.demo.opossum") {
		t.Errorf("unrelated worker must not start for `up web`, got:\n%s", joined)
	}
}

func TestRestartCLI(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, `
name: demo
services:
  db:
    image: postgres:16
`)
	root := newRootCmd()
	root.SetArgs([]string{"-f", compose, "restart", "db"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	joined := strings.Join(readLog(), "\n")
	if !strings.Contains(joined, "stop db.demo.opossum") || !strings.Contains(joined, "start db.demo.opossum") {
		t.Errorf("`restart db` should stop then start it, got:\n%s", joined)
	}
}

func TestUpUnknownServiceCLIErrors(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, `
name: demo
services:
  db:
    image: postgres:16
`)
	if _, err := run(t, "-f", compose, "up", "ghost"); err == nil {
		t.Fatal("expected a non-nil error for `up ghost`")
	}
}

func TestDownCLI(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, `
name: demo
services:
  db:
    image: postgres:16
  web:
    image: web:latest
    depends_on: [db]
`)
	if _, err := run(t, "-f", compose, "down"); err != nil {
		t.Fatalf("down: %v", err)
	}
	joined := strings.Join(readLog(), "\n")
	if !strings.Contains(joined, "stop web.demo.opossum") ||
		!strings.Contains(joined, "delete --force db.demo.opossum") ||
		!strings.Contains(joined, "network delete demo-net") {
		t.Errorf("down should stop, delete, and remove the network, got:\n%s", joined)
	}
}

func TestPsCLI(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, `
name: demo
services:
  web:
    image: web:latest
`)
	out, err := run(t, "-f", compose, "ps")
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	// The shim's inspect reports running with a published port.
	for _, want := range []string{"SERVICE", "PORTS", "web.demo.opossum", "192.168.66.9", "0.0.0.0:8080->80/tcp", "running"} {
		if !strings.Contains(out, want) {
			t.Errorf("ps output missing %q, got:\n%s", want, out)
		}
	}
}

func TestStopCLI(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, `
name: demo
services:
  db:
    image: postgres:16
`)
	if _, err := run(t, "-f", compose, "stop"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	joined := strings.Join(readLog(), "\n")
	if !strings.Contains(joined, "stop db.demo.opossum") {
		t.Errorf("stop should stop db, got:\n%s", joined)
	}
	if strings.Contains(joined, "delete --force") || strings.Contains(joined, "network delete") {
		t.Errorf("stop must not remove anything, got:\n%s", joined)
	}
}

func TestLogsCLI(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, `
name: demo
services:
  db:
    image: postgres:16
`)
	if _, err := run(t, "-f", compose, "logs", "-n", "5", "db"); err != nil {
		t.Fatalf("logs: %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "logs -n 5 db.demo.opossum") {
		t.Errorf("logs should tail db, got:\n%s", joined)
	}
}

func TestLogsFollowMultipleCLI(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, `
name: demo
services:
  db:
    image: postgres:16
  web:
    image: web:latest
`)
	// --follow across all services now multiplexes rather than erroring (#148).
	if _, err := run(t, "-f", compose, "logs", "--follow"); err != nil {
		t.Fatalf("logs --follow should multiplex multiple services, got: %v", err)
	}
}

func TestProjectNameDefaultsToDirectory(t *testing.T) {
	fakeShim(t)
	// No `name:` and no -p: the project name comes from the compose file's dir.
	dir := filepath.Join(t.TempDir(), "MyProj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	compose := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(compose, []byte("services:\n  db:\n    image: postgres:16\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "-f", compose, "ps")
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	// Directory "MyProj" is sanitized to "myproj".
	if !strings.Contains(out, "db.myproj.opossum") {
		t.Errorf("project name should default to the sanitized dir name, got:\n%s", out)
	}
}

func TestMissingComposeFileErrors(t *testing.T) {
	fakeShim(t)
	if _, err := run(t, "-f", filepath.Join(t.TempDir(), "nope.yaml"), "ps"); err == nil {
		t.Fatal("expected an error for a missing compose file")
	}
}

func TestDownVolumesCLI(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  db:\n    image: pg\n    volumes: [\"pgdata:/data\"]\n")
	if _, err := run(t, "-f", compose, "down", "-v"); err != nil {
		t.Fatalf("down -v: %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "volume delete demo_pgdata") {
		t.Errorf("down -v should remove the project-namespaced named volume, got:\n%s", joined)
	}
}

func TestImagesCLI(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web:latest\n")
	out, err := run(t, "-f", compose, "images")
	if err != nil {
		t.Fatalf("images: %v", err)
	}
	for _, want := range []string{"SERVICE", "IMAGE", "web", "web:latest", "pulled"} {
		if !strings.Contains(out, want) {
			t.Errorf("images output missing %q, got:\n%s", want, out)
		}
	}
}

// The `import` CLI path: importCmd → Import → ImportFromDocker, wired through
// runtime.New()'s OPOSSUM_CONTAINER_BIN / OPOSSUM_DOCKER_BIN seams. A build
// service is exported from Docker (save|load); the fakes prove the full chain ran
// (the runtime test covers the byte flow — this covers the CLI wiring).
func TestImportCLI(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	// docker: emits fake tar bytes on `image save`, logging every call.
	docker := filepath.Join(dir, "docker")
	writeExec(t, docker, fmt.Sprintf("#!/bin/sh\necho \"docker $*\" >> %s\n[ \"$1 $2\" = \"image save\" ] && echo TARDATA\nexit 0\n", logPath))
	// container: drains stdin on `image load` (mirroring the real load, which
	// consumes save's stream), logging. The runtime test covers the EPIPE/byte flow.
	container := filepath.Join(dir, "container")
	writeExec(t, container, fmt.Sprintf("#!/bin/sh\necho \"container $*\" >> %s\n[ \"$1 $2\" = \"image load\" ] && cat >/dev/null\nexit 0\n", logPath))
	t.Setenv("OPOSSUM_CONTAINER_BIN", container)
	t.Setenv("OPOSSUM_DOCKER_BIN", docker)

	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    build: .\n")
	out, err := run(t, "-f", compose, "import")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !strings.Contains(out, "Importing web from Docker (demo-web:latest)") {
		t.Errorf("import should report the build service and its docker ref, got:\n%s", out)
	}
	log, _ := os.ReadFile(logPath)
	if s := string(log); !strings.Contains(s, "docker image save demo-web:latest") || !strings.Contains(s, "container image load") {
		t.Errorf("import CLI did not drive save|load through the runtime, log:\n%s", s)
	}
}

// --from-docker is the old name for --from-docker-compose. It must keep working
// identically (published examples and agents that learned the old name still use
// it), and it must steer the caller to the new name. This drives a real `up` with
// each spelling through the same fakes and compares the commands the runtime
// actually issued — equivalence at the argv level, not just "both exit 0".
func TestFromDockerComposeLegacyAlias(t *testing.T) {
	upWith := func(flag string) (calls, stderr string) {
		t.Helper()
		dir := t.TempDir()
		logPath := filepath.Join(dir, "calls.log")
		docker := filepath.Join(dir, "docker")
		writeExec(t, docker, fmt.Sprintf("#!/bin/sh\necho \"docker $*\" >> %s\n[ \"$1 $2\" = \"image save\" ] && echo TARDATA\nexit 0\n", logPath))
		// `image inspect` fails so the built image looks missing — the real migration
		// case, and what makes `up` reach for the image at all.
		container := filepath.Join(dir, "container")
		writeExec(t, container, fmt.Sprintf("#!/bin/sh\necho \"container $*\" >> %s\n"+
			"[ \"$1 $2\" = \"image inspect\" ] && exit 1\n"+
			"[ \"$1 $2\" = \"image load\" ] && cat >/dev/null\nexit 0\n", logPath))
		t.Setenv("OPOSSUM_CONTAINER_BIN", container)
		t.Setenv("OPOSSUM_DOCKER_BIN", docker)

		compose := writeCompose(t, "name: demo\nservices:\n  web:\n    build: .\n")
		root := newRootCmd()
		var so, se strings.Builder
		root.SetOut(&so)
		root.SetErr(&se)
		root.SetArgs([]string{"-f", compose, "up", flag})
		if err := root.Execute(); err != nil {
			t.Fatalf("up %s: %v", flag, err)
		}
		log, _ := os.ReadFile(logPath)
		// The notice must go to stderr, never stdout (callers parse stdout).
		if strings.Contains(so.String(), "deprecated") {
			t.Errorf("the deprecation notice must not go to stdout, got:\n%s", so.String())
		}
		// Sorted: the import is a pipe (`docker image save | container image load`),
		// so those two processes append to the shared log concurrently and their
		// relative order isn't deterministic. Compare the commands as a multiset —
		// that still catches a missing, extra, or differently-argued command, which
		// is what "the two spellings do the same thing" means here.
		lines := strings.Split(strings.TrimRight(string(log), "\n"), "\n")
		sort.Strings(lines)
		return strings.Join(lines, "\n"), se.String()
	}

	newCalls, newErr := upWith("--from-docker-compose")
	oldCalls, oldErr := upWith("--from-docker")

	// The import actually ran (otherwise the comparison below is vacuous).
	if !strings.Contains(newCalls, "docker image save demo-web:latest") {
		t.Fatalf("--from-docker-compose should import from Docker, calls:\n%s", newCalls)
	}
	// The same commands, with the same arguments, for both spellings.
	if newCalls != oldCalls {
		t.Errorf("--from-docker must drive the identical commands as --from-docker-compose\nnew:\n%s\nold:\n%s", newCalls, oldCalls)
	}
	// Only the old spelling is called out, and it names the new flag.
	if !strings.Contains(oldErr, "--from-docker is deprecated") || !strings.Contains(oldErr, "--from-docker-compose") {
		t.Errorf("--from-docker should warn and name the new flag, stderr:\n%s", oldErr)
	}
	if strings.Contains(newErr, "deprecated") {
		t.Errorf("--from-docker-compose must not warn, stderr:\n%s", newErr)
	}
}

// The old name is hidden from `up --help` (the new name is the one to advertise),
// but still accepted — a hidden flag must not become an unknown flag.
func TestFromDockerComposeHelpAdvertisesNewNameOnly(t *testing.T) {
	out, err := run(t, "up", "--help")
	if err != nil {
		t.Fatalf("up --help: %v", err)
	}
	if !strings.Contains(out, "--from-docker-compose") {
		t.Errorf("up --help should advertise --from-docker-compose, got:\n%s", out)
	}
	// The old name appears nowhere on its own (only as the new name's prefix).
	if strings.Contains(strings.ReplaceAll(out, "--from-docker-compose", ""), "--from-docker") {
		t.Errorf("up --help should not advertise the deprecated --from-docker, got:\n%s", out)
	}
}

// The migration story end to end: a compose file that can't start as written on
// Apple `container` (Postgres data dir on a bind mount) reaches startup in ONE
// command, because --from-docker-compose writes the fixes into an overlay and
// re-resolves the project with it merged.
func TestFromDockerComposeGeneratesOverlayAndStarts(t *testing.T) {
	readLog := fakeShim(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(
		"name: demo\nservices:\n  db:\n    image: postgres:16\n    volumes:\n      - ./pgdata:/var/lib/postgresql/data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := run(t, "up", "--from-docker-compose", "--no-build")
	if err != nil {
		t.Fatalf("up --from-docker-compose: %v\n%s", err, out)
	}
	// The overlay was written, and the notice says what and where.
	body, rerr := os.ReadFile(filepath.Join(dir, "compose.opossum.yaml"))
	if rerr != nil {
		t.Fatalf("--from-docker-compose should write an overlay: %v", rerr)
	}
	if !strings.Contains(out, "wrote compose.opossum.yaml") {
		t.Errorf("generating an overlay should be announced, got:\n%s", out)
	}
	if !strings.Contains(out, "OPSM-101") || !strings.Contains(out, "OPSM-105") {
		t.Errorf("the notice should name the diagnostics it fixed, got:\n%s", out)
	}

	// The SAME run started the service with the adapted config: the bind mount is
	// gone, replaced by the named volume the overlay introduced.
	calls := strings.Join(readLog(), "\n")
	if !strings.Contains(calls, "db-data:/var/lib/postgresql/data") {
		t.Errorf("the run should mount the overlay's named volume, calls:\n%s", calls)
	}
	// The host bind mount must be GONE, not merely accompanied by the volume —
	// mounting both sources at one path is the bug #309 fixed.
	if strings.Contains(calls, filepath.Join(dir, "pgdata")+":/var/lib/postgresql/data") {
		t.Errorf("the run should not still use the host bind mount, calls:\n%s", calls)
	}
	if !strings.Contains(calls, "PGDATA=/var/lib/postgresql/data/pgdata") {
		t.Errorf("the run should carry the redirected PGDATA, calls:\n%s", calls)
	}
	if !strings.Contains(string(body), "[opossum --from-docker-compose]") {
		t.Errorf("the overlay should carry the stable marker, got:\n%s", body)
	}
}

// An existing compose.opossum.yaml is never overwritten — the user may have
// edited it, and clobbering that would destroy work.
func TestFromDockerComposeNeverOverwritesOverlay(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(
		"name: demo\nservices:\n  db:\n    image: postgres:16\n    volumes:\n      - ./pgdata:/var/lib/postgresql/data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mine := "# hand written\nservices:\n  db:\n    environment:\n      PGDATA: /var/lib/postgresql/data/mine\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.opossum.yaml"), []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	if _, err := run(t, "up", "--from-docker-compose", "--no-build"); err != nil {
		t.Fatalf("up: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "compose.opossum.yaml"))
	if string(got) != mine {
		t.Errorf("an existing overlay must be left alone, got:\n%s", got)
	}
}

// With an explicit -f, the overlay isn't auto-merged (same rule as the standard
// override), so writing one would leave a file that silently does nothing.
func TestFromDockerComposeNoGenerationWithExplicitFile(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	cf := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(cf, []byte(
		"name: demo\nservices:\n  db:\n    image: postgres:16\n    volumes:\n      - ./pgdata:/var/lib/postgresql/data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	if _, err := run(t, "-f", cf, "up", "--from-docker-compose", "--no-build"); err != nil {
		t.Fatalf("up: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.opossum.yaml")); err == nil {
		t.Error("an explicit -f should not generate an overlay (it wouldn't be merged)")
	}
}

// A project with nothing to adapt gets no file — the overlay is never written
// speculatively.
func TestFromDockerComposeNoOverlayWhenNothingToAdapt(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(
		"name: demo\nservices:\n  web:\n    image: nginx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := run(t, "up", "--from-docker-compose", "--no-build")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.opossum.yaml")); err == nil {
		t.Error("nothing to adapt should mean no overlay file")
	}
	if strings.Contains(out, "wrote compose.opossum.yaml") {
		t.Errorf("no overlay should be announced when none was written, got:\n%s", out)
	}
}

// writeExec writes an executable shim script.
func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The `ws` CLI path: snapshot → ls → rollback wired through newRootCmd().Execute().
// ws touches only the directory (never the runtime), so no fake container shim.
func TestWsCLI(t *testing.T) {
	work := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExec(t, filepath.Join(work, "f.txt"), "v1") // just writes the file (mode is harmless)

	if out, err := run(t, "ws", "snapshot", "s1", "--path", work); err != nil || !strings.Contains(out, `Saved workspace snapshot "s1"`) {
		t.Fatalf("ws snapshot: out=%q err=%v", out, err)
	}
	if out, err := run(t, "ws", "ls", "--path", work); err != nil || !strings.Contains(out, "s1") {
		t.Fatalf("ws ls should list s1: out=%q err=%v", out, err)
	}
	// Break the workspace, then roll back.
	if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("BROKEN"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, "ws", "rollback", "s1", "--path", work); err != nil || !strings.Contains(out, "before-rollback-") {
		t.Fatalf("ws rollback should restore and report the autosave: out=%q err=%v", out, err)
	}
	if b, _ := os.ReadFile(filepath.Join(work, "f.txt")); string(b) != "v1" {
		t.Errorf("rollback did not restore the file through the CLI, got %q", string(b))
	}
}

// The `ws rm` / `ws prune` CLI paths: create snapshots, prune the auto-saves,
// rm a named one. Directory-only, so no fake container shim.
func TestWsRmPruneCLI(t *testing.T) {
	work := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExec(t, filepath.Join(work, "f.txt"), "v1")
	if _, err := run(t, "ws", "snapshot", "keep-me", "--path", work); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// Two rollbacks each leave a before-rollback-* auto-save — the clutter prune clears.
	for i := 0; i < 2; i++ {
		if _, err := run(t, "ws", "rollback", "keep-me", "--path", work); err != nil {
			t.Fatalf("rollback: %v", err)
		}
	}
	// prune removes the auto-saves, leaves the named one.
	if out, err := run(t, "ws", "prune", "--path", work); err != nil || !strings.Contains(out, "before-rollback-") {
		t.Fatalf("ws prune: out=%q err=%v", out, err)
	}
	if out, _ := run(t, "ws", "ls", "--path", work); !strings.Contains(out, "keep-me") || strings.Contains(out, "before-rollback") {
		t.Errorf("prune should keep the named snapshot and drop auto-saves, ls:\n%s", out)
	}
	// rm removes the named one.
	if _, err := run(t, "ws", "rm", "keep-me", "--path", work); err != nil {
		t.Fatalf("ws rm: %v", err)
	}
	if out, _ := run(t, "ws", "ls", "--path", work); !strings.Contains(out, "No snapshots") {
		t.Errorf("after rm, expected no snapshots, ls:\n%s", out)
	}
}

func TestDownRmiCLI(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  db:\n    image: pg\n")
	if _, err := run(t, "-f", compose, "down", "--rmi", "all"); err != nil {
		t.Fatalf("down --rmi all: %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "image delete --force pg") {
		t.Errorf("down --rmi all should remove the pulled image, got:\n%s", joined)
	}
	// An invalid --rmi value is rejected.
	if _, err := run(t, "-f", compose, "down", "--rmi", "bogus"); err == nil {
		t.Error("down --rmi bogus should error")
	}
}

func TestConfigCLI(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, `
name: demo
services:
  db:
    image: postgres:${PG_TAG:-16}
    restart: always
  web:
    image: web
    depends_on: [db]
`)
	out, err := run(t, "-f", compose, "config")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if !strings.Contains(out, "image: postgres:16") { // interpolation resolved
		t.Errorf("config should show resolved image, got:\n%s", out)
	}
	// restart is acted on now (it drives the supervisor), so it appears in the
	// resolved config rather than in the ignored list.
	if !strings.Contains(out, "restart: always") {
		t.Errorf("config should show the restart policy, got:\n%s", out)
	}
}

func TestConfigServicesCLI(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  db:\n    image: pg\n  web:\n    image: web\n    depends_on: [db]\n")
	out, err := run(t, "-f", compose, "config", "--services")
	if err != nil {
		t.Fatalf("config --services: %v", err)
	}
	// Startup order: db before web, names only.
	if d, w := strings.Index(out, "db"), strings.Index(out, "web"); d < 0 || w < 0 || d > w {
		t.Errorf("--services should list names in startup order, got:\n%s", out)
	}
	if strings.Contains(out, "image:") {
		t.Errorf("--services should print names only, got:\n%s", out)
	}
}

// config mirrors what `up` would start: a profile-gated service is hidden unless
// its profile is active (docker compose parity) (#155).
func TestConfigProfileFilteredCLI(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web\n  debug:\n    image: dbg\n    profiles: [debug]\n")

	// Default: debug is hidden from --services and the full config.
	out, err := run(t, "-f", compose, "config", "--services")
	if err != nil {
		t.Fatalf("config --services: %v", err)
	}
	if strings.Contains(out, "debug") {
		t.Errorf("gated service should be hidden by default, got:\n%s", out)
	}
	full, err := run(t, "-f", compose, "config")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if strings.Contains(full, "debug:") {
		t.Errorf("gated service should be hidden from full config by default, got:\n%s", full)
	}

	// With --profile debug, it appears in both --services and the full config.
	out, err = run(t, "-f", compose, "config", "--profile", "debug", "--services")
	if err != nil {
		t.Fatalf("config --profile: %v", err)
	}
	if !strings.Contains(out, "debug") {
		t.Errorf("--profile debug should include the gated service, got:\n%s", out)
	}
	full, err = run(t, "-f", compose, "config", "--profile", "debug")
	if err != nil {
		t.Fatalf("config --profile (full): %v", err)
	}
	if !strings.Contains(full, "debug:") {
		t.Errorf("--profile debug should render the gated service in full config, got:\n%s", full)
	}
}

// config rejects the same projects `up` does: an enabled service depending on a
// gated-inactive one is an error, not a config with a dangling reference (#155).
func TestConfigRejectsGatedDependency(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web\n    depends_on: [helper]\n  helper:\n    image: h\n    profiles: [opt]\n")
	if _, err := run(t, "-f", compose, "config"); err == nil {
		t.Fatal("config should error when an enabled service depends on a gated-inactive one")
	}
}

// Multiple -f merge on the command line: a later file overrides an earlier one.
func TestMultipleComposeFilesCLI(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	if err := os.WriteFile(base, []byte("services:\n  web:\n    image: web:1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(over, []byte("services:\n  web:\n    image: web:2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "-f", base, "-f", over, "config")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if !strings.Contains(out, "image: web:2") {
		t.Errorf("a later -f should override an earlier one, got:\n%s", out)
	}
}

// `run --ssh` must forward the flag to the underlying `container run` (it was
// wired but never asserted at the CLI level).
func TestRunSSHCLI(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web:latest\n")
	if _, err := run(t, "-f", compose, "run", "--rm", "--ssh", "web", "true"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "--ssh") {
		t.Errorf("run --ssh should reach the container run, got:\n%s", joined)
	}
}

// --build and --no-build contradict each other and must error, not silently
// pick one.
func TestUpBuildNoBuildConflict(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web:latest\n")
	if _, err := run(t, "-f", compose, "up", "--build", "--no-build"); err == nil {
		t.Error("up --build --no-build should be rejected as contradictory")
	}
}

func TestRunCLIOneOff(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web:latest\n")
	// --rm before the service; `-la` after passes through to the command.
	if _, err := run(t, "-f", compose, "run", "--rm", "web", "ls", "-la"); err != nil {
		t.Fatalf("run: %v", err)
	}
	joined := strings.Join(readLog(), "\n")
	if !strings.Contains(joined, "run -i --name web-run.demo.opossum") || !strings.Contains(joined, "web:latest ls -la") {
		t.Errorf("one-off run should override the command, got:\n%s", joined)
	}
	if !strings.Contains(joined, "delete --force web-run.demo.opossum") {
		t.Errorf("--rm should remove the one-off, got:\n%s", joined)
	}
}

func TestRunCLIKeepsStdoutClean(t *testing.T) {
	// `run` is the CLI's stdio bridge: a piped caller (e.g. an MCP client
	// speaking JSON-RPC to a containerized server) reads the container's stdout.
	// opossum's own progress ("Running one-off …") must therefore go to stderr,
	// never stdout.
	fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web:latest\n")
	root := newRootCmd()
	var out, errBuf strings.Builder
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"-f", compose, "run", "--rm", "web", "true"})
	if err := root.Execute(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(out.String(), "Running one-off") {
		t.Errorf("progress leaked to stdout (pollutes piped stdio):\n%s", out.String())
	}
	if !strings.Contains(errBuf.String(), "Running one-off web") {
		t.Errorf("progress should still be visible on stderr, got:\n%s", errBuf.String())
	}
}

func TestBuildCLI(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  api:\n    build: /ctx\n")
	if _, err := run(t, "-f", compose, "build"); err != nil {
		t.Fatalf("build: %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "build --progress plain -t demo-api:latest /ctx") {
		t.Errorf("build should build api, got:\n%s", joined)
	}
}

func TestKillCLIWithSignal(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web:latest\n")
	if _, err := run(t, "-f", compose, "kill", "-s", "TERM"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "kill -s TERM web.demo.opossum") {
		t.Errorf("kill -s TERM should apply, got:\n%s", joined)
	}
}

func TestExecCLIPassesCommandFlags(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web:latest\n")
	// `-la` after the service must reach the exec'd command, not be parsed by opossum.
	if _, err := run(t, "-f", compose, "exec", "web", "ls", "-la"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "exec web.demo.opossum ls -la") {
		t.Errorf("expected the command flags to pass through, got:\n%s", joined)
	}
}

func TestExecCLIInteractiveFlags(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web:latest\n")
	// -it before the service are opossum's exec flags.
	if _, err := run(t, "-f", compose, "exec", "-it", "web", "sh"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "exec -i -t web.demo.opossum sh") {
		t.Errorf("expected -i -t to be applied, got:\n%s", joined)
	}
}

func TestDiscoversDockerComposeFileWithoutFlag(t *testing.T) {
	fakeShim(t)
	// A directory with only a docker-compose.yml — no -f given.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"),
		[]byte("name: demo\nservices:\n  db:\n    image: postgres:16\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir) // run as if invoked from that directory

	out, err := run(t, "ps") // no -f
	if err != nil {
		t.Fatalf("ps without -f should discover docker-compose.yml: %v", err)
	}
	if !strings.Contains(out, "db.demo.opossum") {
		t.Errorf("expected the discovered project to be used, got:\n%s", out)
	}
}

// An opossum overlay (compose.opossum.yaml) is auto-merged when no -f is given,
// at the HIGHEST precedence: its values win over both the base compose file and a
// standard compose.override.yaml. This is what lets opossum carry adjustments that
// make a project run on Apple `container` without editing the user's own files.
func TestOpossumOverlayAutoMerged(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	writeF := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Base sets FOO=base; a standard override sets FOO=override; the opossum
	// overlay sets FOO=overlay. Merge order is base -> override -> overlay, so the
	// overlay must win.
	writeF("compose.yaml", "name: demo\nservices:\n  web:\n    image: web\n    environment:\n      FOO: base\n")
	writeF("compose.override.yaml", "services:\n  web:\n    environment:\n      FOO: override\n")
	writeF("compose.opossum.yaml", "services:\n  web:\n    environment:\n      FOO: overlay\n")
	t.Chdir(dir)

	out, err := run(t, "config") // no -f
	if err != nil {
		t.Fatalf("config with an opossum overlay: %v", err)
	}
	if !strings.Contains(out, "FOO=overlay") {
		t.Errorf("the opossum overlay should win at the highest precedence, got:\n%s", out)
	}
	if strings.Contains(out, "FOO=base") || strings.Contains(out, "FOO=override") {
		t.Errorf("the overlay value should replace base/override, got:\n%s", out)
	}
}

// Merging an opossum overlay is announced by the commands that surface or start
// the resolved config (config, up) — the running config differs from the user's
// base compose file, and that must never be silent. It is NOT announced by
// read-only commands like `ps`, which would just be noise on a file meant to live
// in the repo. (run() captures cobra's stderr, where the notice is written.)
func TestOpossumOverlayNotice(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	writeF := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeF("compose.yaml", "name: demo\nservices:\n  web:\n    image: web\n")
	writeF("compose.opossum.yaml", "services:\n  web:\n    image: web2\n")
	t.Chdir(dir)

	// config announces the overlay.
	out, err := run(t, "config")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if !strings.Contains(out, "compose.opossum.yaml") {
		t.Errorf("config should announce a merged opossum overlay, got:\n%s", out)
	}

	// ps does not — it's scoped to config/up to avoid noise on every command.
	out, err = run(t, "ps")
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	if strings.Contains(out, "opossum overlay") {
		t.Errorf("ps should not announce the overlay (scoped to config/up), got:\n%s", out)
	}
}

func TestNoComposeFileWithoutFlagErrors(t *testing.T) {
	fakeShim(t)
	t.Chdir(t.TempDir()) // empty dir, no compose file
	if _, err := run(t, "ps"); err == nil {
		t.Fatal("expected an error when no compose file can be discovered")
	}
}

// COMPOSE_PROFILES activates profiles the same way --profile does, on every
// command that honors profiles (config here; up/run share the identical wiring).
// An unset/empty value must NOT activate anything (strings.Split("", ",") yields
// [""], which EnableProfiles must treat as no profile).
func TestComposeProfilesEnvCLI(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web\n  debug:\n    image: dbg\n    profiles: [debug]\n")

	// Empty/unset COMPOSE_PROFILES: the gated service stays hidden.
	t.Setenv("COMPOSE_PROFILES", "")
	out, err := run(t, "-f", compose, "config")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if strings.Contains(out, "debug:") {
		t.Errorf("empty COMPOSE_PROFILES must not activate a gated service, got:\n%s", out)
	}

	// COMPOSE_PROFILES=debug activates it, no --profile needed.
	t.Setenv("COMPOSE_PROFILES", "debug")
	out, err = run(t, "-f", compose, "config")
	if err != nil {
		t.Fatalf("config with COMPOSE_PROFILES: %v", err)
	}
	if !strings.Contains(out, "debug:") {
		t.Errorf("COMPOSE_PROFILES=debug should activate the gated service, got:\n%s", out)
	}
}

// COMPOSE_PROFILES also reaches `up` (the same EnableProfiles wiring), so a gated
// service starts without --profile.
func TestComposeProfilesEnvUpCLI(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web\n  debug:\n    image: dbg\n    profiles: [debug]\n")
	t.Setenv("COMPOSE_PROFILES", "debug")
	if _, err := run(t, "-f", compose, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "--name debug.demo.opossum") {
		t.Errorf("COMPOSE_PROFILES=debug should start the gated service, got:\n%s", joined)
	}
}

// `run` allocates a TTY (-t) only when our stdin is a terminal, and -T/--no-tty
// suppresses it even then. A test's stdin is never a real terminal, so we force
// the terminal case through the stdinIsTerminal seam.
func TestRunTTYAndNoTTYCLI(t *testing.T) {
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web:latest\n")
	orig := stdinIsTerminal
	stdinIsTerminal = func() bool { return true }
	defer func() { stdinIsTerminal = orig }()

	// Terminal stdin, no -T: the one-off gets -i -t.
	readLog := fakeShim(t)
	if _, err := run(t, "-f", compose, "run", "--rm", "web", "sh"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "run -i -t --name web-run.demo.opossum") {
		t.Errorf("a terminal stdin should allocate a TTY, got:\n%s", joined)
	}

	// -T suppresses the TTY even with a terminal stdin: -i but no -t.
	readLog = fakeShim(t)
	if _, err := run(t, "-f", compose, "run", "--rm", "-T", "web", "sh"); err != nil {
		t.Fatalf("run -T: %v", err)
	}
	joined := strings.Join(readLog(), "\n")
	if !strings.Contains(joined, "run -i --name web-run.demo.opossum") || strings.Contains(joined, "run -i -t --name web-run.demo.opossum") {
		t.Errorf("-T should suppress the TTY (-i, no -t), got:\n%s", joined)
	}
}

// Thin-CLI coverage: each of these commands parses and dispatches to the runtime.
// The fake shim logs the invocation and returns success.
func TestPullCLI(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web:latest\n")
	if _, err := run(t, "-f", compose, "pull"); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "pull web:latest") {
		t.Errorf("pull should reach `container pull <image>`, got:\n%s", joined)
	}
}

func TestStatsCLI(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web:latest\n")
	if _, err := run(t, "-f", compose, "stats", "--no-stream"); err != nil {
		t.Fatalf("stats: %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "stats") {
		t.Errorf("stats should reach `container stats`, got:\n%s", joined)
	}
}

func TestCpCLI(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web:latest\n")
	// A `service:path` argument is rewritten to the namespaced container name.
	if _, err := run(t, "-f", compose, "cp", "./local.txt", "web:/app/local.txt"); err != nil {
		t.Fatalf("cp: %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "cp ./local.txt web.demo.opossum:/app/local.txt") {
		t.Errorf("cp should rewrite service:path to the container name, got:\n%s", joined)
	}
}

// `watch` with no develop.watch rules fails fast (rather than blocking on an
// empty watcher), which also makes its CLI wiring observable.
func TestWatchNoRulesErrorsCLI(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web:latest\n")
	if _, err := run(t, "-f", compose, "watch"); err == nil {
		t.Fatal("watch with no develop.watch rules should error, not block")
	}
}

// --foreground refuses to attach when more than one long-running service would
// start (the runtime's foreground run blocks on the first). CLI-level wiring.
func TestUpForegroundMultipleRejectedCLI(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  a:\n    image: a\n  b:\n    image: b\n")
	if _, err := run(t, "-f", compose, "up", "--foreground"); err == nil {
		t.Fatal("--foreground with two long-running services should be rejected")
	}
}

// down --remove-orphans parses and runs (removing the project network); with no
// orphans present the scan is a no-op, but the flag path is exercised.
func TestDownRemoveOrphansCLI(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web:latest\n")
	if _, err := run(t, "-f", compose, "down", "--remove-orphans"); err != nil {
		t.Fatalf("down --remove-orphans: %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "network delete demo-net") {
		t.Errorf("down should tear down the project network, got:\n%s", joined)
	}
}

// `stats --host` dispatches to the host-footprint table (the header renders
// regardless of whether any VM can be mapped on the test machine).
func TestStatsHostCLI(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web:latest\n")
	out, err := run(t, "-f", compose, "stats", "--host")
	if err != nil {
		t.Fatalf("stats --host: %v", err)
	}
	if !strings.Contains(out, "HOST FOOTPRINT") || !strings.Contains(out, "GUEST MEM") {
		t.Errorf("stats --host should render the host-footprint table, got:\n%s", out)
	}
}

// TestUpDryRunCLI exercises the full CLI path for `up --dry-run`: it prints the
// plan (startup order and the run commands) and the fake runtime receives no
// mutating invocation (run/create/delete).
func TestUpDryRunCLI(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, `
name: demo
services:
  db:
    image: postgres:16
  web:
    image: web:latest
    depends_on: [db]
`)
	out, err := run(t, "-f", compose, "up", "--dry-run")
	if err != nil {
		t.Fatalf("up --dry-run: %v", err)
	}
	if !strings.Contains(out, "Dry run") ||
		!strings.Contains(out, "run -d --name web.demo.opossum") {
		t.Errorf("--dry-run should print the plan, got:\n%s", out)
	}
	joined := strings.Join(readLog(), "\n")
	for _, verb := range []string{"run -d", "network create", "delete --force"} {
		if strings.Contains(joined, verb) {
			t.Errorf("--dry-run must not issue %q to the runtime, got log:\n%s", verb, joined)
		}
	}
}

// The overlay is discovered under either spelling, so generation must respect a
// hand-written compose.opossum.YML too. Writing the .yaml next to it would take
// precedence and silently make the user's file inert — worse than overwriting,
// because nothing is lost visibly.
func TestFromDockerComposeRespectsYmlSpelling(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(
		"name: demo\nservices:\n  db:\n    image: postgres:16\n    volumes:\n      - ./pgdata:/var/lib/postgresql/data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mine := "services:\n  db:\n    environment:\n      MY_SETTING: keepme\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.opossum.yml"), []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := run(t, "up", "--from-docker-compose", "--no-build")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.opossum.yaml")); err == nil {
		t.Error("a hand-written compose.opossum.yml must not be shadowed by a generated .yaml")
	}
	// And the user is told fixes were available rather than left guessing.
	if !strings.Contains(out, "already exists") {
		t.Errorf("skipping because an overlay exists should be reported, got:\n%s", out)
	}
	// The user's setting still reaches the project.
	cfg, err := run(t, "config")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if !strings.Contains(cfg, "MY_SETTING=keepme") {
		t.Errorf("the hand-written overlay must still apply, got:\n%s", cfg)
	}
}

// --dry-run must show what a real run would do. It writes nothing, but it plans
// against the adapted project — otherwise the one command a cautious user runs to
// ask "what will this do?" is the one that answers wrongly.
func TestFromDockerComposeDryRunPlansAdapted(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(
		"name: demo\nservices:\n  db:\n    image: postgres:16\n    volumes:\n      - ./pgdata:/var/lib/postgresql/data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := run(t, "up", "--from-docker-compose", "--dry-run")
	if err != nil {
		t.Fatalf("up --dry-run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.opossum.yaml")); err == nil {
		t.Error("--dry-run must not write the overlay")
	}
	if !strings.Contains(out, "would write compose.opossum.yaml") {
		t.Errorf("--dry-run should say it would write the overlay, got:\n%s", out)
	}
	// The planned commands must match what a real run issues, not the unadapted file.
	if !strings.Contains(out, "db-data:/var/lib/postgresql/data") {
		t.Errorf("--dry-run should plan against the adapted project, got:\n%s", out)
	}
	if strings.Contains(out, filepath.Join(dir, "pgdata")+":/var/lib/postgresql/data") {
		t.Errorf("--dry-run should not plan the unadapted bind mount, got:\n%s", out)
	}
}

// A directory opossum can't write to must not turn an `up` that used to work into
// a failure: the overlay is a convenience, so it degrades to the existing warnings.
func TestFromDockerComposeUnwritableDirStillStarts(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a read-only directory")
	}
	fakeShim(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(
		"name: demo\nservices:\n  db:\n    image: postgres:16\n    volumes:\n      - ./pgdata:/var/lib/postgresql/data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	out, err := run(t, "up", "--from-docker-compose", "--no-build")
	if err != nil {
		t.Fatalf("an unwritable directory must not fail up: %v\n%s", err, out)
	}
	if !strings.Contains(out, "couldn't write compose.opossum.yaml") {
		t.Errorf("the failure to write should be reported, got:\n%s", out)
	}
}

// The overlay is self-checked before it lands: it's merged from a temp file in the
// same directory and only linked into place if it resolves. Since opossum never
// overwrites the result, an unloadable file would otherwise break every later
// command until a human deleted a file they never wrote. This asserts the file that
// does land always loads.
func TestFromDockerComposeWrittenOverlayAlwaysResolves(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	// Names and paths that stress the renderer: a reserved-word service, a `$` in
	// a host path, and a colliding sanitized name.
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(
		"name: demo\nservices:\n"+
			"  \"true\":\n    image: postgres:16\n    volumes:\n      - ./pg$$x:/var/lib/postgresql/data\n"+
			"  a_b:\n    image: mysql:8\n    volumes:\n      - ./one:/var/lib/mysql\n"+
			"  a-b:\n    image: mariadb:11\n    volumes:\n      - ./two:/var/lib/mysql\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	if _, err := run(t, "up", "--from-docker-compose", "--no-build"); err != nil {
		t.Fatalf("up: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.opossum.yaml")); err != nil {
		t.Fatalf("expected an overlay to be written: %v", err)
	}
	// Every later command must still work against the written file.
	if _, err := run(t, "config"); err != nil {
		t.Errorf("the written overlay must leave the project loadable: %v", err)
	}
	// No temp files left behind in the user's directory.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("a temp file was left in the project directory: %s", e.Name())
		}
	}
}

// End to end: a compose file with a container-only port (`ports: ["<p>"]`) whose
// mirrored host port is occupied still reaches startup, publishing on a free port
// — the whole point being that docker compose would have started here too.
func TestBarePortFallbackCLI(t *testing.T) {
	readLog := fakeShim(t)
	l, err := net.Listen("tcp", ":0") // occupy the port; the probe binds IPv4 wildcard, which this conflicts with
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(fmt.Sprintf(
		"name: demo\nservices:\n  web:\n    image: web\n    ports:\n      - \"%d\"\n", port)), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := run(t, "up")
	if err != nil {
		t.Fatalf("a container-only port on a taken host port should still start: %v\n%s", err, out)
	}
	if !strings.Contains(out, "[OPSM-206]") {
		t.Errorf("the fallback should be announced, got:\n%s", out)
	}
	calls := strings.Join(readLog(), "\n")
	if strings.Contains(calls, fmt.Sprintf("-p %d:%d", port, port)) {
		t.Errorf("the occupied host port must not be published, calls:\n%s", calls)
	}
	if !strings.Contains(calls, fmt.Sprintf(":%d ", port)) && !strings.Contains(calls, fmt.Sprintf(":%d\n", port)) {
		t.Errorf("the container port %d should still be published, calls:\n%s", port, calls)
	}
}

// A project whose only finding is a note gets NO overlay: the file is never
// overwritten once written, so a comment-only one would burn that single chance
// and block a real fix later. The finding is still reported.
func TestFromDockerComposeNotesOnlyWritesNoOverlay(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(
		"name: demo\nservices:\n  ci:\n    image: someci\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := run(t, "up", "--from-docker-compose", "--no-build")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, "compose.opossum.yaml")); serr == nil {
		t.Error("a notes-only finding must not write an overlay")
	}
	if !strings.Contains(out, "can't be fixed by a compose change") {
		t.Errorf("the note should still be reported, got:\n%s", out)
	}
}

// The report must not call a note or a suggestion a change opossum made.
func TestFromDockerComposeReportsClassesSeparately(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(
		"name: demo\nservices:\n"+
			"  db:\n    image: postgres:16\n    volumes:\n      - ./pg:/var/lib/postgresql/data\n"+
			"  web:\n    image: nginx\n    volumes:\n      - shared:/srv\n"+
			"  worker:\n    image: busybox\n    volumes:\n      - shared:/srv\n"+
			"volumes:\n  shared: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := run(t, "up", "--from-docker-compose", "--no-build")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if !strings.Contains(out, "change(s) so this project runs") {
		t.Errorf("applied changes should be reported as changes, got:\n%s", out)
	}
	if !strings.Contains(out, "suggestion(s) written but NOT applied") {
		t.Errorf("suggestions must be reported as not applied, got:\n%s", out)
	}
	// The applied count must not include the suggestions.
	if strings.Contains(out, "4 change(s)") {
		t.Errorf("suggestions must not be counted as changes, got:\n%s", out)
	}
}

// A project with no `restart:` must not grow a background process. "No daemon" is
// one of opossum's selling points, so the watcher has to be something you opted
// into by writing a policy.
func TestUpStartsNoSupervisorWithoutRestartPolicy(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: nosup\nservices:\n  web:\n    image: web\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := run(t, "up", "--no-build")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if strings.Contains(out, "OPSM-408") {
		t.Errorf("no restart policy means no supervisor notice, got:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(state, "opossum", "nosup", "supervisor.pid")); err == nil {
		t.Error("no restart policy should leave no pid file")
	}
}

// The opt-outs must actually prevent the process, not merely silence the notice —
// a background process outliving a CI job is exactly what they exist for.
func TestUpRespectsSupervisorOptOut(t *testing.T) {
	for _, how := range []string{"flag", "env"} {
		t.Run(how, func(t *testing.T) {
			fakeShim(t)
			state := t.TempDir()
			t.Setenv("XDG_STATE_HOME", state)
			if how == "env" {
				t.Setenv("OPOSSUM_NO_SUPERVISOR", "1")
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
				[]byte("name: optout\nservices:\n  web:\n    image: web\n    restart: always\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Chdir(dir)

			args := []string{"up", "--no-build"}
			if how == "flag" {
				args = append(args, "--no-supervisor")
			}
			out, err := run(t, args...)
			if err != nil {
				t.Fatalf("up: %v", err)
			}
			if strings.Contains(out, "OPSM-408") {
				t.Errorf("opting out should start no supervisor, got:\n%s", out)
			}
			if _, err := os.Stat(filepath.Join(state, "opossum", "optout", "supervisor.pid")); err == nil {
				t.Error("opting out should leave no pid file")
			}
		})
	}
}

// The hidden subcommand must stay hidden: it isn't something to run by hand, and
// two of them would race to restart the same container.
func TestSuperviseSubcommandIsHidden(t *testing.T) {
	out, err := run(t, "--help")
	if err != nil {
		t.Fatalf("--help: %v", err)
	}
	if strings.Contains(out, "__supervise") {
		t.Errorf("the internal supervisor command should not be advertised, got:\n%s", out)
	}
}

// supervisorPID reads the pid a claim recorded, or 0.
func supervisorPID(t *testing.T, state, project string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(state, "opossum", project, "supervisor.pid"))
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	n, err := strconv.Atoi(f[0])
	if err != nil {
		return 0
	}
	return n
}

// waitFor polls until cond holds or the deadline passes — the supervisor is a
// separate process, so its effects are not immediate.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The feature's whole claim: `up` on a project with `restart:` leaves a watcher
// running, and `down` takes it away. Every other supervisor test was negative
// ("it does NOT start"), which meant deleting the feature outright kept them all
// green.
func TestUpStartsASupervisorAndDownStopsIt(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: supd\nservices:\n  web:\n    image: web\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := run(t, "up", "--no-build")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if !strings.Contains(out, "OPSM-408") {
		t.Errorf("a restart policy should announce the supervisor, got:\n%s", out)
	}
	var pid int
	waitFor(t, "the supervisor to claim the project", func() bool {
		pid = supervisorPID(t, state, "supd")
		return pid != 0
	})
	t.Cleanup(func() {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Signal(syscall.SIGKILL)
		}
	})
	if !processIsAlive(pid) {
		t.Fatalf("the recorded pid %d is not running", pid)
	}

	if _, err := run(t, "down"); err != nil {
		t.Fatalf("down: %v", err)
	}
	waitFor(t, "the supervisor to exit", func() bool { return !processIsAlive(pid) })
	if got := supervisorPID(t, state, "supd"); got != 0 {
		t.Errorf("down should remove the pid file, still reports %d", got)
	}
}

// Two `up`s must leave exactly one watcher. The claim is made by the child, so a
// racing pair can't both end up running — the loser exits instead of becoming an
// orphan that nothing can stop.
func TestSecondUpDoesNotAddASecondSupervisor(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: supd2\nservices:\n  web:\n    image: web\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	// Registered before the first up: an assertion failing below would otherwise
	// skip the teardown and leave a supervisor running on the machine after the
	// test binary exits.
	t.Cleanup(func() { run(t, "down") })
	if _, err := run(t, "up", "--no-build"); err != nil {
		t.Fatalf("first up: %v", err)
	}
	var first int
	waitFor(t, "the first supervisor", func() bool {
		first = supervisorPID(t, state, "supd2")
		return first != 0
	})

	if _, err := run(t, "up", "--no-build"); err != nil {
		t.Fatalf("second up: %v", err)
	}
	time.Sleep(300 * time.Millisecond) // let a second child claim, if it would
	if got := supervisorPID(t, state, "supd2"); got != first {
		t.Errorf("a second up should keep the first supervisor, pid %d -> %d", first, got)
	}
	if !processIsAlive(first) {
		t.Error("the original supervisor should still be running")
	}
}

// The child runs from its own working directory, so a relative -f has to be made
// absolute before it is handed over — otherwise the watcher dies on startup while
// `up` has just announced that supervision is running.
func TestSupervisorSurvivesARelativeComposePath(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A NON-standard file name on purpose: with a discoverable `compose.yaml` the
	// child would find the project by itself, and the test would pass even if the
	// `-f` were never forwarded.
	if err := os.WriteFile(filepath.Join(dir, "deep", "stack.yaml"),
		[]byte("name: supd3\nservices:\n  web:\n    image: web\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	if _, err := run(t, "-f", "deep/stack.yaml", "up", "--no-build"); err != nil {
		t.Fatalf("up: %v", err)
	}
	var pid int
	waitFor(t, "the supervisor to claim the project", func() bool {
		pid = supervisorPID(t, state, "supd3")
		return pid != 0
	})
	t.Cleanup(func() {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Signal(syscall.SIGKILL)
		}
	})
	// It has to still be alive a moment later: a child that can't find its compose
	// file exits immediately, leaving a pid file behind that looks like success.
	time.Sleep(500 * time.Millisecond)
	if !processIsAlive(pid) {
		log, _ := os.ReadFile(filepath.Join(state, "opossum", "supd3", "supervisor.log"))
		t.Fatalf("the supervisor died on startup — the compose selection didn't reach it; log:\n%s", log)
	}
}

func processIsAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// `opossum stop` must stand. The supervisor polls, so a stop is only really
// honoured if it survives several polls — this is the wiring bug that let a
// stopped service come back within seconds.
func TestStopIsNotUndoneBySupervisor(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: stopd\nservices:\n  web:\n    image: web\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	// Registered before the first up: an assertion failing below would otherwise
	// skip the teardown and leave a supervisor running on the machine after the
	// test binary exits.
	t.Cleanup(func() { run(t, "down") })
	if _, err := run(t, "up", "--no-build"); err != nil {
		t.Fatalf("up: %v", err)
	}
	waitFor(t, "the supervisor", func() bool { return supervisorPID(t, state, "stopd") != 0 })

	if _, err := run(t, "stop"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// The marker records the stop, and nothing may erase it while the supervisor
	// is watching — that erasure is what made `stop` come undone.
	entries, _ := os.ReadDir(filepath.Join(state, "opossum", "stopd"))
	marked := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "stopped-") {
			marked = true
		}
	}
	if !marked {
		t.Fatal("stop should record that it stopped the service")
	}
	time.Sleep(1500 * time.Millisecond) // let polls happen
	entries, _ = os.ReadDir(filepath.Join(state, "opossum", "stopd"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "stopped-") {
			return // still recorded: the stop stands
		}
	}
	t.Error("the supervisor erased the record of an explicit stop — the stop would be undone")
}

// A foreground `up` means "run it here until it ends"; leaving a watcher to
// restart what the user just watched finish would contradict that.
func TestForegroundUpStartsNoSupervisor(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: fgd\nservices:\n  web:\n    image: web\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := run(t, "up", "--foreground", "--no-build")
	if err != nil {
		t.Fatalf("up --foreground: %v", err)
	}
	if strings.Contains(out, "OPSM-408") {
		t.Errorf("a foreground up should leave no supervisor, got:\n%s", out)
	}
	time.Sleep(300 * time.Millisecond)
	if pid := supervisorPID(t, state, "fgd"); pid != 0 {
		t.Errorf("a foreground up left a supervisor (pid %d)", pid)
	}
}

// `down` must be able to stop the supervisor even when the compose file is gone.
// A watcher is a resident process and `down` is the only thing that stops it, so
// making that reachable only while the file still parses would strand a process
// the user has no opossum command to remove.
func TestDownStopsSupervisorWithoutAComposeFile(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	dir := filepath.Join(t.TempDir(), "orphaned")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// No `name:`, so the project is named after the directory — which is what
	// `down` can still work out once the file is gone.
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("services:\n  web:\n    image: web\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	if _, err := run(t, "up", "--no-build"); err != nil {
		t.Fatalf("up: %v", err)
	}
	var pid int
	waitFor(t, "the supervisor", func() bool {
		pid = supervisorPID(t, state, "orphaned")
		return pid != 0
	})
	t.Cleanup(func() {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Signal(syscall.SIGKILL)
		}
	})

	// The compose file disappears — a rename, a branch switch, a deleted checkout.
	if err := os.Remove(filepath.Join(dir, "compose.yaml")); err != nil {
		t.Fatal(err)
	}
	out, _ := run(t, "down") // it will fail to load, and that's expected
	if !strings.Contains(out, "stopped the restart supervisor") {
		t.Errorf("down should still stop the supervisor, got:\n%s", out)
	}
	waitFor(t, "the supervisor to exit", func() bool { return !processIsAlive(pid) })
}

// Adding a `restart:` service and re-running `up` must actually put it under
// supervision. A watcher started before the change keeps enforcing the old set,
// so `up` would otherwise announce services nothing is watching — and a policy
// the user deleted would still be in force.
func TestUpReplacesASupervisorWhoseSetChanged(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	dir := t.TempDir()
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("name: chg\nservices:\n  web:\n    image: web\n    restart: always\n")
	t.Chdir(dir)

	// Registered before the first up: an assertion failing below would otherwise
	// skip the teardown and leave a supervisor running on the machine after the
	// test binary exits.
	t.Cleanup(func() { run(t, "down") })
	if _, err := run(t, "up", "--no-build"); err != nil {
		t.Fatalf("first up: %v", err)
	}
	var first int
	waitFor(t, "the first supervisor", func() bool {
		first = supervisorPID(t, state, "chg")
		return first != 0
	})
	watched := filepath.Join(state, "opossum", "chg", "supervised")
	waitFor(t, "the watched set to be recorded", func() bool {
		_, err := os.Stat(watched)
		return err == nil
	})

	// A second service gains a policy.
	write("name: chg\nservices:\n  web:\n    image: web\n    restart: always\n  cache:\n    image: c\n    restart: always\n")
	out, err := run(t, "up", "--no-build")
	if err != nil {
		t.Fatalf("second up: %v", err)
	}
	if !strings.Contains(out, "cache") {
		t.Errorf("the notice should mention the newly supervised service, got:\n%s", out)
	}
	waitFor(t, "the watched set to include cache", func() bool {
		b, err := os.ReadFile(watched)
		return err == nil && strings.Contains(string(b), "cache")
	})
	// Exactly one supervisor, and it is the new one.
	second := supervisorPID(t, state, "chg")
	if second == 0 {
		t.Fatal("no supervisor after the second up")
	}
	if second == first && processIsAlive(first) {
		b, _ := os.ReadFile(watched)
		t.Errorf("the stale supervisor was kept; watched set:\n%s", b)
	}
	t.Cleanup(func() {
		for _, p := range []int{first, second} {
			if pr, err := os.FindProcess(p); err == nil {
				_ = pr.Signal(syscall.SIGKILL)
			}
		}
	})
}

// `up web` must not leave a watcher polling for services nobody started.
func TestSupervisorWatchesOnlyWhatUpStarted(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(
		"name: partial\nservices:\n"+
			"  web:\n    image: web\n    restart: always\n"+
			"  other:\n    image: o\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	// Registered before the first up: an assertion failing below would otherwise
	// skip the teardown and leave a supervisor running on the machine after the
	// test binary exits.
	t.Cleanup(func() { run(t, "down") })
	out, err := run(t, "up", "web", "--no-build")
	if err != nil {
		t.Fatalf("up web: %v", err)
	}
	if strings.Contains(out, "other") {
		t.Errorf("`up web` should not announce watching `other`, got:\n%s", out)
	}
	waitFor(t, "the watched set", func() bool {
		_, err := os.Stat(filepath.Join(state, "opossum", "partial", "supervised"))
		return err == nil
	})
	b, _ := os.ReadFile(filepath.Join(state, "opossum", "partial", "supervised"))
	if strings.Contains(string(b), "other") {
		t.Errorf("the supervisor should watch only what was started, got:\n%s", b)
	}
}

// A service that exits right after starting makes `up` fail (OPSM-407) while the
// rest of the stack keeps running. Those survivors declare `restart:`, so they
// are exactly what needs watching — and until this was fixed, a failed `up`
// returned before the supervisor was ever started, leaving them unwatched in the
// one situation that most wants it.
func TestUpSupervisesTheSurvivorsOfAFailedUp(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	t.Setenv("INSPECT_STATE", "stopped") // every container inspects as exited
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: surv\nservices:\n  web:\n    image: web\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	// Registered before the up, not after the assertions: a t.Fatal below would
	// otherwise skip the teardown and leave a supervisor running on the machine
	// long after the test binary exits.
	t.Cleanup(func() { run(t, "down") })
	out, err := run(t, "up", "--no-build")
	if err == nil {
		t.Fatal("up should fail when a service exits right after starting")
	}
	if !strings.Contains(out, "OPSM-407") {
		t.Fatalf("expected the post-start crash report, got:\n%s", out)
	}
	waitFor(t, "a supervisor for the surviving services", func() bool {
		return supervisorPID(t, state, "surv") != 0
	})
}

// A failed partial `up` must not take supervision away from the services it
// didn't touch. `up web` that fails reports a Started() of just [web]; narrowing
// a supervisor watching [db web] down to [web] would leave `db` — running, and
// asking to be restarted — unwatched, by way of the very change that exists to
// stop that happening.
func TestFailedPartialUpDoesNotNarrowAnExistingSupervisor(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: narrow\nservices:\n  web:\n    image: web\n    restart: always\n"+
			"  db:\n    image: db\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Cleanup(func() { run(t, "down") })

	if _, err := run(t, "up", "--no-build"); err != nil {
		t.Fatalf("first up: %v", err)
	}
	watched := filepath.Join(state, "opossum", "narrow", "supervised")
	var first int
	waitFor(t, "the watched set", func() bool {
		first = supervisorPID(t, state, "narrow")
		_, err := os.Stat(watched)
		return first != 0 && err == nil
	})
	before, _ := os.ReadFile(watched)
	if !strings.Contains(string(before), "db") {
		t.Fatalf("the first up should watch db, got %q", before)
	}

	// Now `up web` fails, because web exits right after starting.
	t.Setenv("INSPECT_STATE", "stopped")
	if _, err := run(t, "up", "web", "--no-build"); err == nil {
		t.Fatal("up web should fail when web exits right after starting")
	}
	// The pid, not the file: the watched set is written by the supervisor itself,
	// so a replacement that has not got there yet would still read as the old set
	// and this would pass while the supervision it describes was already gone.
	// Replacing means stopping, which is synchronous, so the surviving pid is the
	// honest signal.
	var now int
	waitFor(t, "a supervisor to still be there", func() bool {
		now = supervisorPID(t, state, "narrow")
		return now != 0
	})
	if now != first {
		t.Errorf("the failed `up web` replaced the supervisor (pid %d -> %d); db is still "+
			"running and still asks to be restarted, but the watched set was %q", first, now, before)
	}
}

// The other half of the claim: a bring-up that fails is rolled back, so nothing
// survives — and no supervisor may be announced or started. Without this, making
// the supervisor fall back to "every service in the compose file" when Started()
// is empty would pass every other test.
func TestRolledBackUpStartsNoSupervisor(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	t.Setenv("IMAGE_ABSENT", "rolled-app:latest") // so --no-build has something to refuse
	dir := t.TempDir()
	// `build:` with --no-build fails inside the start loop, which is what triggers
	// the rollback (a pre-flight refusal would never reach it).
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: rolled\nservices:\n  app:\n    build: .\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Cleanup(func() { run(t, "down") })

	out, err := run(t, "up", "--no-build")
	if err == nil {
		t.Fatal("up should fail when a service must be built and --no-build was given")
	}
	if strings.Contains(out, "OPSM-408") {
		t.Errorf("a rolled-back up has nothing left to watch, but it announced a supervisor:\n%s", out)
	}
	if pid := supervisorPID(t, state, "rolled"); pid != 0 {
		t.Errorf("a supervisor (pid %d) was started for a stack that was rolled back", pid)
	}
}

// `up web` says nothing about `db`. If db is still there under its own
// `restart:` policy, narrowing the supervisor to [web] would drop it. The test
// that matters is what the supervisor itself records: the notice and the
// comparison can both say "db, web" while the process only ever watches [web].
// So the first supervisor is killed, forcing `up web` to start a real one.
func TestPartialUpKeepsWatchingWhatItDidNotTouch(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: keep\nservices:\n  web:\n    image: web\n    restart: always\n"+
			"  db:\n    image: db\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Cleanup(func() { run(t, "down") })

	if _, err := run(t, "up", "--no-build"); err != nil {
		t.Fatalf("first up: %v", err)
	}
	watched := filepath.Join(state, "opossum", "keep", "supervised")
	var first int
	waitFor(t, "the first supervisor", func() bool {
		first = supervisorPID(t, state, "keep")
		_, err := os.Stat(watched)
		return first != 0 && err == nil
	})

	// Kill it, so `up web` has to start a supervisor rather than keep this one —
	// the only path on which the set handed to the child matters. The record stays:
	// it is the carry-over source.
	before := len(supervisingLines(t, state, "keep"))
	killSupervisor(t, first)

	out, err := run(t, "up", "web", "--no-build")
	if err != nil {
		t.Fatalf("up web: %v", err)
	}
	if !strings.Contains(out, "db") {
		t.Errorf("db is still running under `restart: always`, so `up web` should say it is "+
			"still watched, got:\n%s", out)
	}
	if got := waitForNewSupervising(t, state, "keep", before); got != "db web" {
		t.Errorf("the new supervisor should watch the union of what `up web` started and what "+
			"was already watched, it watches [%s]", got)
	}
	if got := readWatched(t, watched); got != "db\nweb" {
		t.Errorf("the recorded set should be the union too, got %q", got)
	}
}

// Presence, not liveness: a service stopped with `opossum stop` still has a
// container, and the stop marker — not the watch list — is what keeps it down.
// Dropping it here would mean `opossum start db` after a partial up left db
// running with nobody watching it.
func TestPartialUpCarriesOverAStoppedService(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: presence\nservices:\n  web:\n    image: web\n    restart: always\n"+
			"  db:\n    image: db\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Cleanup(func() { run(t, "down") })

	if _, err := run(t, "up", "--no-build"); err != nil {
		t.Fatalf("first up: %v", err)
	}
	watched := filepath.Join(state, "opossum", "presence", "supervised")
	var first int
	waitFor(t, "the first supervisor", func() bool {
		first = supervisorPID(t, state, "presence")
		_, err := os.Stat(watched)
		return first != 0 && err == nil
	})
	before := len(supervisingLines(t, state, "presence"))
	killSupervisor(t, first)

	// db exists but is not running; web is untouched.
	t.Setenv("INSPECT_STOPPED", "db.presence.opossum")
	// Assert the premise. Without this the test still passes when the knob stops
	// working — db reads as running, and it quietly becomes a duplicate of the
	// test above instead of the one thing that pins "presence, not liveness".
	if out, _ := run(t, "ps"); !regexp.MustCompile(`(?m)^db\s.*\sstopped$`).MatchString(out) {
		t.Fatalf("this test needs db to exist and be stopped, `ps` says:\n%s", out)
	}
	if _, err := run(t, "up", "web", "--no-build"); err != nil {
		t.Fatalf("up web: %v", err)
	}
	if got := waitForNewSupervising(t, state, "presence", before); got != "db web" {
		t.Errorf("a stopped-but-present service is still supervised (the stop marker keeps it "+
			"down, not the watch list); the supervisor watches [%s]", got)
	}
	if got := readWatched(t, watched); got != "db\nweb" {
		t.Errorf("the recorded set should carry db over too, got %q", got)
	}
}

// A record can outlive the compose file that produced it. Anything in it that
// this project would no longer supervise — a `restart:` that was removed, a
// run-to-completion service — must not come back through the union.
func TestPartialUpDoesNotCarryOverServicesItWouldNotSupervise(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: filt\nservices:\n  web:\n    image: web\n    restart: always\n"+
			"  plain:\n    image: p\n"+ // no restart: at all
			"  migrate:\n    image: m\n    restart: always\n"+
			"  app:\n    image: a\n    restart: always\n    depends_on:\n      migrate:\n"+
			"        condition: service_completed_successfully\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Cleanup(func() { run(t, "down") })

	// Stage a record naming both — as an older compose file might have.
	if err := os.MkdirAll(filepath.Join(state, "opossum", "filt"), 0o755); err != nil {
		t.Fatal(err)
	}
	watched := filepath.Join(state, "opossum", "filt", "supervised")
	if err := os.WriteFile(watched, []byte("migrate\nplain\nweb\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, "up", "web", "--no-build")
	if err != nil {
		t.Fatalf("up web: %v", err)
	}
	for _, unwanted := range []string{"plain", "migrate"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("%s is not something this project supervises, so it must not be carried "+
				"over, got:\n%s", unwanted, out)
		}
	}
	if got := waitForNewSupervising(t, state, "filt", 0); got != "web" {
		t.Errorf("only web should be watched, the supervisor watches [%s]", got)
	}
	if got := readWatched(t, watched); got != "web" {
		t.Errorf("the recorded set should be web alone, got %q", got)
	}
}

// Repeating the same partial up must not churn the supervisor: same set, same
// process. A pid that changes on every `up` is its own kind of instability.
func TestRepeatedPartialUpKeepsTheSameSupervisor(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: churn\nservices:\n  web:\n    image: web\n    restart: always\n"+
			"  db:\n    image: db\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Cleanup(func() { run(t, "down") })

	if _, err := run(t, "up", "--no-build"); err != nil {
		t.Fatalf("first up: %v", err)
	}
	var first int
	waitFor(t, "the first supervisor", func() bool {
		first = supervisorPID(t, state, "churn")
		return first != 0
	})
	for i := 0; i < 3; i++ {
		if _, err := run(t, "up", "web", "--no-build"); err != nil {
			t.Fatalf("up web #%d: %v", i, err)
		}
		if got := supervisorPID(t, state, "churn"); got != first {
			t.Fatalf("`up web` #%d replaced the supervisor (%d -> %d)", i, first, got)
		}
	}
}

// The union may only carry over services that are actually there. A service that
// `down` removed, or one the compose file never started, must not come back —
// that was the original defect: announcing supervision of containers that had
// never been created.
func TestPartialUpDoesNotCarryOverAbsentServices(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: absent\nservices:\n  web:\n    image: web\n    restart: always\n"+
			"  gone:\n    image: g\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Cleanup(func() { run(t, "down") })

	// Stage a record naming a service whose container does not exist.
	if err := os.MkdirAll(filepath.Join(state, "opossum", "absent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "opossum", "absent", "supervised"),
		[]byte("gone\nweb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INSPECT_ABSENT", "gone.absent.opossum")

	out, err := run(t, "up", "web", "--no-build")
	if err != nil {
		t.Fatalf("up web: %v", err)
	}
	if strings.Contains(out, "gone") {
		t.Errorf("`gone` has no container, so it must not be announced as watched, got:\n%s", out)
	}
	// The supervisor claims its pid file itself, a moment after `up` returns. Ending
	// the test before that would run the cleanup `down` against an empty state dir
	// and leave the process behind.
	waitFor(t, "the supervisor to claim its pid file", func() bool {
		return supervisorPID(t, state, "absent") != 0
	})
}

// killSupervisor ends a supervisor the hard way and waits for it to be gone, so
// a following `up` has to start a fresh one. That is the only path on which the
// set handed to the child matters — while a matching supervisor is still alive,
// `up` leaves it be and nothing is re-derived.
func killSupervisor(t *testing.T, pid int) {
	t.Helper()
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("killing supervisor %d: %v", pid, err)
	}
	waitFor(t, "the supervisor to be gone", func() bool {
		return syscall.Kill(pid, 0) != nil
	})
}

// readWatched returns the recorded watch set as a single comparable string, so a
// test can assert the exact set rather than that it contains a name.
func readWatched(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return strings.TrimSpace(string(b))
}

// supervisingLines returns the service lists from every "[OPSM-408] supervising
// [...]" line a supervisor has written. The log is the only place the set that
// actually reached the child is visible: the notice and the recorded file can
// both be right while the process watches something narrower.
func supervisingLines(t *testing.T, state, project string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(state, "opossum", project, "supervisor.log"))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if i := strings.Index(line, "supervising ["); i >= 0 {
			rest := line[i+len("supervising ["):]
			if j := strings.Index(rest, "]"); j >= 0 {
				out = append(out, rest[:j])
			}
		}
	}
	return out
}

// waitForNewSupervising waits for a supervisor started after `before` lines were
// present, and returns the set it says it is watching.
func waitForNewSupervising(t *testing.T, state, project string, before int) string {
	t.Helper()
	var got []string
	waitFor(t, "a newly started supervisor to say what it watches", func() bool {
		got = supervisingLines(t, state, project)
		return len(got) > before
	})
	return got[len(got)-1]
}

// `down` takes the project apart, so a later `up <service>` must not carry over
// services from the stack that no longer exists. Stopping a supervisor in order
// to replace it is a different thing and must keep the record.
func TestDownForgetsTheWatchedSet(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: forget\nservices:\n  web:\n    image: web\n    restart: always\n"+
			"  db:\n    image: db\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Cleanup(func() { run(t, "down") })

	if _, err := run(t, "up", "--no-build"); err != nil {
		t.Fatalf("up: %v", err)
	}
	watched := filepath.Join(state, "opossum", "forget", "supervised")
	waitFor(t, "the watched set", func() bool { _, err := os.Stat(watched); return err == nil })

	if _, err := run(t, "down"); err != nil {
		t.Fatalf("down: %v", err)
	}
	if _, err := os.Stat(watched); !os.IsNotExist(err) {
		got, _ := os.ReadFile(watched)
		t.Errorf("down should forget what was watched, the record still says %q", got)
	}
}

// destroyProject lays out a project the way a trial run leaves one: a compose
// file and a .env the user wrote, plus the things opossum generates beside them.
// It returns the directory.
func destroyProject(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("compose.yaml", body)
	write(".env", "SECRET=keep-me\n")
	write("app/main.go", "package main\n")
	// The header matters: destroy removes an overlay opossum generated and keeps one
	// it did not, so a fixture without it would be testing the other case.
	write("compose.opossum.yaml", "# Generated by `opossum up --from-docker-compose`.\n"+
		"services:\n  web:\n    environment:\n      GENERATED: \"1\"\n")
	write(".opossum/mcp/agent.json", "{}\n")
	return dir
}

// The core promise: everything opossum made is gone, and everything the user
// wrote is still there. Both halves are asserted, because a destroy that removed
// too much would pass a test that only looked for absence.
func TestDestroyRemovesOpossumsThingsAndNothingElse(t *testing.T) {
	readLog := fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "dstry")
	t.Setenv("VOLUME_LS", "NAME\ndstry_data")
	dir := destroyProject(t, "name: dstry\nservices:\n"+
		"  web:\n    build: .\n    volumes:\n      - data:/var/lib/data\n"+
		"  db:\n    image: postgres:16\nvolumes:\n  data: {}\n")
	t.Chdir(dir)

	out, err := run(t, "destroy", "--force")
	if err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}

	joined := strings.Join(readLog(), "\n")
	for _, want := range []string{
		"delete --force web.dstry.opossum",
		"delete --force db.dstry.opossum",
		"network delete dstry-net",
		"volume delete dstry_data",
		"image delete --force dstry-web:latest", // built
		"image delete --force postgres:16",      // pulled
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("destroy should have run %q, the runtime saw:\n%s", want, joined)
		}
	}

	// Gone: what opossum generated.
	for _, rel := range []string{"compose.opossum.yaml", ".opossum"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); !os.IsNotExist(err) {
			t.Errorf("%s is generated by opossum and should be gone, stat err = %v", rel, err)
		}
	}
	// Untouched: what the user wrote. This is the half that makes the command safe
	// to reach for at all.
	for _, rel := range []string{"compose.yaml", ".env", "app/main.go"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("destroy must not touch %s, but: %v", rel, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".env")); !strings.Contains(string(b), "keep-me") {
		t.Error("the user's .env was modified")
	}
	if !strings.Contains(out, "untouched") {
		t.Errorf("destroy should say the user's files were left alone, got:\n%s", out)
	}
}

// --dry-run is the command's safety valve: it must be possible to ask "what
// would this take?" without it taking anything.
func TestDestroyDryRunRemovesNothing(t *testing.T) {
	readLog := fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "dry")
	dir := destroyProject(t, "name: dry\nservices:\n  web:\n    image: web\n")
	t.Chdir(dir)

	out, err := run(t, "destroy", "--dry-run")
	if err != nil {
		t.Fatalf("destroy --dry-run: %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); strings.Contains(joined, "delete") {
		t.Errorf("--dry-run must not delete anything, the runtime saw:\n%s", joined)
	}
	for _, rel := range []string{"compose.opossum.yaml", ".opossum", "compose.yaml", ".env"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("--dry-run must leave %s alone, but: %v", rel, err)
		}
	}
	if !strings.Contains(out, "would remove") {
		t.Errorf("--dry-run should say what it would remove, got:\n%s", out)
	}
}

// With no terminal there is nobody to answer the question. Assuming yes would
// destroy data in a script that only meant to inspect; assuming no would make a
// script that meant it do nothing, silently. So it refuses and names the flag.
func TestDestroyWithoutForceRefusesWhenNobodyCanAnswer(t *testing.T) {
	readLog := fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "nott")
	dir := destroyProject(t, "name: nott\nservices:\n  web:\n    image: web\n")
	t.Chdir(dir)

	out, err := run(t, "destroy")
	if err == nil {
		t.Fatalf("destroy without a terminal and without --force should fail, got:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("the refusal should name the flag that resolves it, got: %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); strings.Contains(joined, "delete") {
		t.Errorf("a refused destroy must not delete anything, the runtime saw:\n%s", joined)
	}
	if _, err := os.Stat(filepath.Join(dir, ".opossum")); err != nil {
		t.Errorf("a refused destroy must leave generated state alone, but: %v", err)
	}
}

// Answering anything but yes at the prompt leaves the project alone. The prompt
// is the only thing standing between a mistyped command and lost volumes.
func TestDestroyPromptDeclinedLeavesEverything(t *testing.T) {
	readLog := fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "nope")
	dir := destroyProject(t, "name: nope\nservices:\n  web:\n    image: web\n")
	t.Chdir(dir)
	orig := stdinIsTerminal
	stdinIsTerminal = func() bool { return true }
	defer func() { stdinIsTerminal = orig }()

	root := newRootCmd()
	var buf strings.Builder
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetIn(strings.NewReader("n\n"))
	root.SetArgs([]string{"destroy"})
	if err := root.Execute(); err != nil {
		t.Fatalf("declining should not be an error: %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); strings.Contains(joined, "delete") {
		t.Errorf("answering no must delete nothing, the runtime saw:\n%s", joined)
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.opossum.yaml")); err != nil {
		t.Errorf("answering no must leave the overlay alone, but: %v", err)
	}
	if !strings.Contains(buf.String(), "Left everything as it was") {
		t.Errorf("declining should say so, got:\n%s", buf.String())
	}
}

// Confirming at the prompt goes through. Without this, the test above would pass
// just as well against a destroy that never removed anything.
func TestDestroyPromptConfirmedRemoves(t *testing.T) {
	readLog := fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "yep")
	dir := destroyProject(t, "name: yep\nservices:\n  web:\n    image: web\n")
	t.Chdir(dir)
	orig := stdinIsTerminal
	stdinIsTerminal = func() bool { return true }
	defer func() { stdinIsTerminal = orig }()

	root := newRootCmd()
	var buf strings.Builder
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetIn(strings.NewReader("y\n"))
	root.SetArgs([]string{"destroy"})
	if err := root.Execute(); err != nil {
		t.Fatalf("destroy: %v\n%s", err, buf.String())
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "delete --force web.yep.opossum") {
		t.Errorf("answering yes should remove the container, the runtime saw:\n%s", joined)
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.opossum.yaml")); !os.IsNotExist(err) {
		t.Errorf("answering yes should remove the overlay, stat err = %v", err)
	}
}

// The overlay is generated, but a user may have edited it, so there is a way to
// keep it — and keeping it must not keep anything else.
func TestDestroyKeepOverlay(t *testing.T) {
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "keepov")
	dir := destroyProject(t, "name: keepov\nservices:\n  web:\n    image: web\n")
	t.Chdir(dir)

	if _, err := run(t, "destroy", "--force", "--keep-overlay"); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.opossum.yaml")); err != nil {
		t.Errorf("--keep-overlay should keep the overlay, but: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".opossum")); !os.IsNotExist(err) {
		t.Errorf("--keep-overlay is about the overlay alone; .opossum should be gone, stat err = %v", err)
	}
}

// Destroy is scoped to one project. A volume the compose file declares
// `external: true` belongs to whoever made it, and another project's containers
// are none of this command's business — the sweep is filtered by label.
func TestDestroyLeavesSharedAndForeignThingsAlone(t *testing.T) {
	readLog := fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// No LS_* here: this shim has no `ls`, so a foreign container cannot be staged
	// at this level. The orphan sweep's label scoping is pinned in
	// TestDestroyPlanOrphansAreScopedByLabel, against the shim that does.
	t.Setenv("INSPECT_PROJECT", "scope")
	t.Setenv("VOLUME_LS", "NAME\nscope_mine\nshared")
	dir := destroyProject(t, "name: scope\nservices:\n"+
		"  web:\n    image: web\n    volumes:\n      - shared:/data\n      - mine:/var/lib/mine\n"+
		"    networks:\n      - outside\n      - inside\n"+
		"volumes:\n  shared:\n    external: true\n  mine: {}\n"+
		"networks:\n  outside:\n    external: true\n  inside: {}\n")
	t.Chdir(dir)

	if _, err := run(t, "destroy", "--force"); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	joined := strings.Join(readLog(), "\n")
	// Matched by pattern, not by exact command: a version that removed the wrong
	// thing would most likely reach for a decorated name (`scope_shared`,
	// `scope-outside`) rather than the bare one, and an exact string would let that
	// straight through. The rule is that these names must not appear in a deletion
	// at all.
	for _, forbidden := range []struct {
		what string
		re   string
	}{
		{"the external volume", `volume delete \S*shared`},    // someone else's data
		{"the external network", `network delete \S*outside`}, // someone else's network
	} {
		if regexp.MustCompile(forbidden.re).MatchString(joined) {
			t.Errorf("destroy must not touch %s (/%s/), the runtime saw:\n%s",
				forbidden.what, forbidden.re, joined)
		}
	}
	// …while still removing this project's own.
	for _, want := range []string{"volume delete scope_mine", "network delete scope-inside"} {
		if !strings.Contains(joined, want) {
			t.Errorf("destroy should have run %q, the runtime saw:\n%s", want, joined)
		}
	}
}

// #314's supervisor is a resident process, which makes it the one piece of a
// project that a teardown can leave running on the machine. Destroy has to end
// it and remove its state, not just the containers it was watching.
func TestDestroyStopsTheSupervisorAndLeavesNoState(t *testing.T) {
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	t.Setenv("INSPECT_PROJECT", "supd")
	dir := destroyProject(t, "name: supd\nservices:\n  web:\n    image: web\n    restart: always\n")
	t.Chdir(dir)

	if _, err := run(t, "up", "--no-build"); err != nil {
		t.Fatalf("up: %v", err)
	}
	var pid int
	waitFor(t, "the supervisor", func() bool {
		pid = supervisorPID(t, state, "supd")
		return pid != 0
	})

	out, err := run(t, "destroy", "--force")
	if err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	waitFor(t, "the supervisor to be gone", func() bool { return syscall.Kill(pid, 0) != nil })
	// Not just stopped — no trace: the pid file, the watch record and the log all
	// live in the project's state directory, and "destroy" means that goes too.
	if _, err := os.Stat(filepath.Join(state, "opossum", "supd")); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(filepath.Join(state, "opossum", "supd"))
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("destroy should leave no supervisor state, found %v (stat err = %v)", names, err)
	}
	if !strings.Contains(out, "supervisor") {
		t.Errorf("destroy should say it stopped the supervisor, got:\n%s", out)
	}
}

// The overlay is a documented place for people to put their own adjustments —
// the generated file even says "edit or delete anything here". So an overlay
// without opossum's header was written by a person, and destroy has no business
// removing it, whatever the file is called. Saying "your sources are untouched"
// while deleting it would be the worst kind of wrong.
func TestDestroyKeepsAnOverlayItDidNotGenerate(t *testing.T) {
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "hand")
	dir := destroyProject(t, "name: hand\nservices:\n  web:\n    image: web\n")
	// Replace the generated overlay with one a person wrote.
	handwritten := "# My own Apple-container tweaks. I wrote this by hand.\nservices:\n  web:\n    memory: 512m\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.opossum.yaml"), []byte(handwritten), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := run(t, "destroy", "--force")
	if err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	b, readErr := os.ReadFile(filepath.Join(dir, "compose.opossum.yaml"))
	if readErr != nil {
		t.Fatalf("destroy deleted a hand-written overlay: %v", readErr)
	}
	if string(b) != handwritten {
		t.Errorf("the hand-written overlay was modified:\n%s", b)
	}
	if !strings.Contains(out, "compose.opossum.yaml") {
		t.Errorf("destroy should say it kept the overlay rather than leave the user to notice, got:\n%s", out)
	}
	// …and everything else still went.
	if _, err := os.Stat(filepath.Join(dir, ".opossum")); !os.IsNotExist(err) {
		t.Errorf("keeping the overlay must not keep .opossum, stat err = %v", err)
	}
}

// The plan is the whole of the command's safety: what it lists is what it
// removes, and a person approves it on that basis. A plan that quietly omitted a
// group would still "work" — which is why this asserts each group by name against
// what the removal actually did.
func TestDestroyPlanListsEverythingItWillRemove(t *testing.T) {
	readLog := fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "plan")
	t.Setenv("VOLUME_LS", "NAME\nplan_data")
	dir := destroyProject(t, "name: plan\nservices:\n"+
		"  web:\n    build: .\n    volumes:\n      - data:/d\n"+
		"  db:\n    image: postgres:16\nvolumes:\n  data: {}\n")
	t.Chdir(dir)

	planned, err := run(t, "destroy", "--dry-run")
	if err != nil {
		t.Fatalf("destroy --dry-run: %v", err)
	}
	for _, name := range []string{
		"web.plan.opossum", "db.plan.opossum", // containers
		"plan-net",                         // network
		"plan_data",                        // volume
		"plan-web:latest",                  // built image
		"postgres:16",                      // pulled image
		"compose.opossum.yaml", ".opossum", // generated files
	} {
		if !strings.Contains(planned, name) {
			t.Errorf("the plan must name %s — it is about to be removed, and the user is "+
				"approving this list. Plan was:\n%s", name, planned)
		}
	}

	if _, err := run(t, "destroy", "--force"); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	// Nothing may be removed that the plan didn't mention: the other direction of
	// the same promise. Membership is by exact name against the plan's own list
	// lines — a substring test against the whole plan would accept removing plain
	// `web` on the strength of `web.plan.opossum` appearing somewhere in it, which
	// is precisely the namesake bug the label check exists to prevent.
	listed := map[string]bool{}
	for _, line := range strings.Split(planned, "\n") {
		if name := strings.TrimSpace(line); strings.HasPrefix(name, "- ") {
			listed[strings.TrimPrefix(name, "- ")] = true
		}
	}
	if len(listed) == 0 {
		t.Fatalf("no plan entries were parsed — the assertion below would prove nothing. Plan:\n%s", planned)
	}
	for _, line := range readLog() {
		if !strings.HasPrefix(line, "delete") && !strings.HasPrefix(line, "volume delete") &&
			!strings.HasPrefix(line, "image delete") && !strings.HasPrefix(line, "network delete") {
			continue
		}
		target := line[strings.LastIndex(line, " ")+1:]
		if target != "" && !listed[target] {
			t.Errorf("destroy removed %q, which the plan never listed (plan entries: %v)", target, listed)
		}
	}
}

// [y/N] means N by default. Pressing Enter, or closing stdin, must not destroy a
// project — the whole point of a default is that the careless answer is the safe
// one.
func TestDestroyPromptDefaultsToNo(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		destroys    bool
	}{
		{"a bare newline", "\n", false},
		{"end of input", "", false},
		{"something else entirely", "maybe\n", false},
		{"an explicit no", "n\n", false},
		{"yes", "y\n", true},
		{"the word yes", "yes\n", true},
		{"a capital Y", "Y\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readLog := fakeShim(t)
			t.Setenv("STATE_DIR", t.TempDir()) // so the shim remembers what was deleted
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("INSPECT_PROJECT", "ask")
			dir := destroyProject(t, "name: ask\nservices:\n  web:\n    image: web\n")
			t.Chdir(dir)
			orig := stdinIsTerminal
			stdinIsTerminal = func() bool { return true }
			defer func() { stdinIsTerminal = orig }()

			root := newRootCmd()
			var buf strings.Builder
			root.SetOut(&buf)
			root.SetErr(&buf)
			root.SetIn(strings.NewReader(tc.input))
			root.SetArgs([]string{"destroy"})
			if err := root.Execute(); err != nil {
				t.Fatalf("destroy: %v\n%s", err, buf.String())
			}
			destroyed := strings.Contains(strings.Join(readLog(), "\n"), "delete --force web.ask.opossum")
			if destroyed != tc.destroys {
				t.Errorf("input %q: destroyed = %v, want %v (output:\n%s)", tc.input, destroyed, tc.destroys, buf.String())
			}
		})
	}
}

// Destroy is not the whole story: two things are shared with every other project
// on the machine, and a teardown that didn't mention them would leave the user
// believing the disk was clean.
func TestDestroyReportsWhatItLeavesToTheMachine(t *testing.T) {
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "shared")
	dir := destroyProject(t, "name: shared\nservices:\n  web:\n    image: web\n")
	t.Chdir(dir)

	out, err := run(t, "destroy", "--force")
	if err != nil {
		t.Fatalf("destroy: %v", err)
	}
	for _, want := range []string{"dns delete", "image prune"} {
		if !strings.Contains(out, want) {
			t.Errorf("destroy should say how to remove the shared %q, got:\n%s", want, out)
		}
	}
}

// A `run --rm`-less one-off leaves a `<service>-run` container behind. It is
// opossum's, it is invisible in `ps`, and a teardown that misses it is exactly
// the kind of leftover destroy exists to prevent.
func TestDestroyRemovesRunLeftovers(t *testing.T) {
	readLog := fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "leftover")
	dir := destroyProject(t, "name: leftover\nservices:\n  web:\n    image: web\n")
	t.Chdir(dir)

	if _, err := run(t, "destroy", "--force"); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "delete --force web-run.leftover.opossum") {
		t.Errorf("destroy should remove the `run` leftover too, the runtime saw:\n%s", joined)
	}
}

// A container that merely shares a name is not this project's. It matters most
// with `--dns-domain ""`, where opossum's own containers are called plain `web`
// and so is half the world.
func TestDestroyOnlyRemovesContainersLabelledForThisProject(t *testing.T) {
	readLog := fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "someone-else") // every container answers with a foreign owner
	dir := destroyProject(t, "name: mine\nservices:\n  web:\n    image: web\n")
	t.Chdir(dir)

	out, err := run(t, "destroy", "--force")
	if err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	// Anchored on the container name: `image delete --force web` is a legitimate
	// line here (the image really is called web), and a looser match would fail on it.
	joined := strings.Join(readLog(), "\n")
	for _, cname := range []string{"web.mine.opossum", "web-run.mine.opossum"} {
		if strings.Contains(joined, "delete --force "+cname) {
			t.Errorf("a container labelled for another project must not be removed, the runtime saw:\n%s", joined)
		}
	}
}

// Asking what a destroy would remove must not start the container runtime — `ps`
// and `images` are exempt for the same reason, and this one matters more: someone
// runs it to decide whether to keep opossum at all. But not starting it is only
// half the answer. Every check the plan makes reads a failed query as "not there",
// so an unreachable runtime would produce an empty plan and destroy would report
// nothing to remove over a project that is entirely present. It has to say the
// runtime is down instead.
func TestDestroyDryRunNeitherStartsNorLiesAboutAStoppedRuntime(t *testing.T) {
	readLog := fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "quiet")
	t.Setenv("SYSTEM_STOPPED", "1") // `system status` reports stopped…
	t.Setenv("APISERVER_DOWN", "1") // …and every other query fails, as it would
	dir := destroyProject(t, "name: quiet\nservices:\n  web:\n    image: web\n")
	t.Chdir(dir)

	out, err := run(t, "destroy", "--dry-run")
	if err == nil {
		t.Fatalf("a dry-run against an unreachable runtime must not answer as if it had "+
			"looked, got:\n%s", out)
	}
	if !strings.Contains(err.Error(), "OPSM-405") {
		t.Errorf("the error should be the shared runtime-stopped signal, got: %v", err)
	}
	if strings.Contains(out, "Nothing to remove") {
		t.Errorf("this is the exact lie to avoid: an unreachable runtime is not an empty "+
			"project. Output:\n%s", out)
	}
	if joined := strings.Join(readLog(), "\n"); strings.Contains(joined, "system start") {
		t.Errorf("--dry-run must not start the runtime, the runtime saw:\n%s", joined)
	}
}

// The counterpart: with the runtime up and genuinely nothing left, destroy says
// so rather than asking a question with no stakes.
func TestDestroyOnAnEmptyProjectSaysSo(t *testing.T) {
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_ABSENT", "web.gone.opossum web-run.gone.opossum")
	t.Setenv("NETWORK_ABSENT", "gone-net")
	t.Setenv("IMAGE_ABSENT", "web")
	t.Setenv("VOLUME_LS", "NAME")
	dir := t.TempDir() // no .opossum, no overlay: a project opossum never touched
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: gone\nservices:\n  web:\n    image: web\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := run(t, "destroy")
	if err != nil {
		t.Fatalf("destroy on an empty project should not fail (there is nothing to ask about): %v", err)
	}
	if !strings.Contains(out, "Nothing to remove") {
		t.Errorf("with nothing left, destroy should say so, got:\n%s", out)
	}
}

// A delete can succeed and change nothing: `container image delete --force`
// exits 0 for a ref it doesn't recognise, and a volume another container holds
// survives its own removal. Trusting the exit code would let destroy report a
// clean sweep over things that are still on the disk.
func TestDestroyReportsWhatSurvived(t *testing.T) {
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "stuck")
	t.Setenv("VOLUME_LS", "NAME\nstuck_data")
	t.Setenv("DELETE_STICKY", "stuck_data") // the volume's removal quietly does nothing
	dir := destroyProject(t, "name: stuck\nservices:\n"+
		"  web:\n    image: web\n    volumes:\n      - data:/d\nvolumes:\n  data: {}\n")
	t.Chdir(dir)

	out, err := run(t, "destroy", "--force")
	if err == nil {
		t.Fatalf("destroy should report that something survived, output was:\n%s", out)
	}
	if !strings.Contains(err.Error(), "stuck_data") {
		t.Errorf("the error should name what is still there, got: %v", err)
	}
	// The rest still went — a survivor is a report, not a reason to stop.
	if _, statErr := os.Stat(filepath.Join(dir, ".opossum")); !os.IsNotExist(statErr) {
		t.Errorf("everything else should still have been removed, .opossum stat err = %v", statErr)
	}
}

// A round trip through the real writer: let `up --from-docker-compose` generate an
// overlay, then destroy it. The keep-a-hand-written-one test above uses a fixture
// with the header spelled out, so on its own it can't notice the day the writer
// stops emitting that header — after which destroy would keep every overlay it
// ever generated, and no test would care.
func TestDestroyRemovesAnOverlayItActuallyGenerated(t *testing.T) {
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "roundtrip")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(
		"name: roundtrip\nservices:\n  db:\n    image: postgres:16\n    volumes:\n"+
			"      - ./pgdata:/var/lib/postgresql/data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	if _, err := run(t, "up", "--from-docker-compose", "--no-build"); err != nil {
		t.Fatalf("up --from-docker-compose: %v", err)
	}
	overlay := filepath.Join(dir, "compose.opossum.yaml")
	if _, err := os.Stat(overlay); err != nil {
		t.Fatalf("this test needs a generated overlay to destroy: %v", err)
	}

	out, err := run(t, "destroy", "--force")
	if err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	if _, err := os.Stat(overlay); !os.IsNotExist(err) {
		body, _ := os.ReadFile(overlay)
		t.Errorf("destroy should remove an overlay opossum generated, but it is still there. "+
			"Its first line is what the check reads:\n%s", firstLine(string(body)))
	}
}

// firstLine is for a failure message: the whole point is what the file starts with.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// "Couldn't ask" is not "still there". VolumeExists answers true when the listing
// fails — right for deciding whether to seed a volume, wrong for deciding whether
// one survived a removal, where it turns a teardown that worked into a reported
// failure and an agent's retry loop that never goes green.
func TestDestroyDoesNotReportASurvivorItCouldNotCheck(t *testing.T) {
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "unknown")
	t.Setenv("VOLUME_LS_FAIL", "1") // the listing errors, before and after removal
	dir := destroyProject(t, "name: unknown\nservices:\n"+
		"  web:\n    image: web\n    volumes:\n      - data:/d\nvolumes:\n  data: {}\n")
	t.Chdir(dir)

	out, err := run(t, "destroy", "--force")
	if err != nil {
		t.Fatalf("a volume whose listing can't be read must not be reported as surviving "+
			"its own removal: %v\n%s", err, out)
	}
}

// The cap has to apply to the log a real supervisor writes, not just to the
// writer in isolation. Reverting the one line that wires them together leaves
// every unit test green, which is the whole reason this exists: the supervisor
// gets its own bounded handle instead of the stdout the parent redirects.
//
// Deterministic without waiting for a crash loop: the log starts over the cap, so
// the supervisor's first line trims it.
func TestSupervisorLogIsCappedWhenAnUpStartsIt(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: capped\nservices:\n  web:\n    image: web\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Cleanup(func() { run(t, "down") })

	// A log left over the cap by an earlier run.
	logDir := filepath.Join(state, "opossum", "capped")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "supervisor.log")
	const maxLog = 1 << 20
	old := strings.Repeat("an earlier supervisor restarted something here\n", maxLog/46+100)
	if err := os.WriteFile(logPath, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(logPath); fi.Size() <= maxLog {
		t.Fatalf("this test needs a log over the cap to start with, got %d bytes", fi.Size())
	}

	if _, err := run(t, "up", "--no-build"); err != nil {
		t.Fatalf("up: %v", err)
	}
	// The trim's own words, not its code: the fallback notice for "couldn't open a
	// capped log" would otherwise satisfy this — it is written to the same file, and
	// it used to carry the same code. A predicate that a failure can satisfy is not
	// a predicate.
	waitFor(t, "the supervisor to trim the log it writes through", func() bool {
		b, err := os.ReadFile(logPath)
		return err == nil && strings.Contains(string(b), "earlier lines dropped")
	})
	fi, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > maxLog {
		t.Errorf("the supervisor's log is %d bytes against a %d-byte cap — is the supervisor "+
			"still writing to the stdout the parent redirects, rather than a capped handle?",
			fi.Size(), maxLog)
	}
	// The trim note has to be in the log itself, not on a stream nobody keeps.
	b, _ := os.ReadFile(logPath)
	if first, _, _ := strings.Cut(string(b), "\n"); !strings.Contains(first, "OPSM-410") {
		t.Errorf("the log should open with the note explaining the gap, got %q", first)
	}
}

// `destroy -p other` names a project by hand. Its containers and volumes are
// other's and removing them is the point — but `.opossum/` and the generated
// overlay belong to the *directory*, which is someone else's project. With
// --force there is nobody to read a warning, so this refuses.
func TestDestroyRefusesToRemoveThisDirectorysFilesUnderAnotherName(t *testing.T) {
	readLog := fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "other")
	dir := destroyProject(t, "name: mine\nservices:\n  web:\n    image: web\n")
	t.Chdir(dir)

	out, err := run(t, "-p", "other", "destroy", "--force")
	if err == nil {
		t.Fatalf("destroying another project by name must not take this directory's files with "+
			"it, output was:\n%s", out)
	}
	for _, want := range []string{"mine", "--keep-local"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should mention %q so it is actionable, got: %v", want, err)
		}
	}
	// Nothing at all was removed: refusing means refusing.
	if joined := strings.Join(readLog(), "\n"); strings.Contains(joined, "delete") {
		t.Errorf("a refused destroy must remove nothing, the runtime saw:\n%s", joined)
	}
	for _, rel := range []string{".opossum", "compose.opossum.yaml", "compose.yaml", ".env"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("%s should still be there after a refusal, but: %v", rel, err)
		}
	}
}

// …and the escape hatch works: --keep-local removes the named project's runtime
// objects and leaves this directory alone.
func TestDestroyKeepLocalRemovesOnlyRuntimeObjects(t *testing.T) {
	readLog := fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "other")
	dir := destroyProject(t, "name: mine\nservices:\n  web:\n    image: web\n")
	t.Chdir(dir)

	if _, err := run(t, "-p", "other", "destroy", "--force", "--keep-local"); err != nil {
		t.Fatalf("destroy --keep-local: %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "delete --force web.other.opossum") {
		t.Errorf("--keep-local should still remove the named project's containers, the runtime saw:\n%s", joined)
	}
	for _, rel := range []string{".opossum", "compose.opossum.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("--keep-local must leave %s alone, but: %v", rel, err)
		}
	}
}

// The ordinary case must not be caught by any of this: a compose file naming its
// project something other than its directory is normal, and destroying it from
// its own directory is exactly what the command is for. Refusing here would make
// the guard worse than the hole it closes.
func TestDestroyDoesNotRefuseWhenTheComposeFileNamesTheProject(t *testing.T) {
	readLog := fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "named")
	// The directory is a random temp name; the compose file calls the project "named".
	dir := destroyProject(t, "name: named\nservices:\n  web:\n    image: web\n")
	t.Chdir(dir)

	out, err := run(t, "destroy", "--force")
	if err != nil {
		t.Fatalf("a project named by its own compose file must destroy normally: %v\n%s", err, out)
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "delete --force web.named.opossum") {
		t.Errorf("the containers should have gone, the runtime saw:\n%s", joined)
	}
	if _, err := os.Stat(filepath.Join(dir, ".opossum")); !os.IsNotExist(err) {
		t.Errorf(".opossum should have gone, stat err = %v", err)
	}
}

// Interactively there is somebody to read a warning, so a mistargeted destroy is
// allowed — but the plan has to say whose files these are, and which directory
// they are in.
func TestDestroyPlanNamesTheDirectoryAndWarnsAboutMistargeting(t *testing.T) {
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "other")
	dir := destroyProject(t, "name: mine\nservices:\n  web:\n    image: web\n")
	t.Chdir(dir)

	out, err := run(t, "-p", "other", "destroy", "--dry-run")
	if err != nil {
		t.Fatalf("destroy --dry-run: %v", err)
	}
	if !strings.Contains(out, dir) {
		t.Errorf("the plan should name the directory whose files it would remove, got:\n%s", out)
	}
	if !strings.Contains(out, "belongs to project \"mine\"") {
		t.Errorf("the plan should say whose files these are, got:\n%s", out)
	}
	if !strings.Contains(out, "--keep-local") {
		t.Errorf("the plan should name the way to avoid it, got:\n%s", out)
	}
}

// A volume left by a service that was renamed or deleted is still on the disk
// after a destroy. opossum can't safely remove it — an `external: true` volume can
// carry the same prefix and there is no label to tell them apart — but reporting
// a clean teardown over it is the thing to avoid.
func TestDestroyListsVolumesItCannotRemove(t *testing.T) {
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "renamed")
	// `renamed_old` is left from a service the compose file no longer has, so it is
	// stranded. `renamed_shared` carries the project's prefix too — but it is the
	// real name of an `external: true` volume, so it belongs to whoever made it and
	// is nobody's orphan. Giving the external volume a prefixed real name is the
	// point: without it the prefix filter alone would exclude it and the external
	// check would never be exercised.
	t.Setenv("VOLUME_LS", "NAME\nrenamed_data\nrenamed_old\nrenamed_shared")
	dir := destroyProject(t, "name: renamed\nservices:\n"+
		"  web:\n    image: web\n    volumes:\n      - data:/d\n      - shared:/s\n"+
		"volumes:\n  data: {}\n  shared:\n    external: true\n    name: renamed_shared\n")
	t.Chdir(dir)

	out, err := run(t, "destroy", "--dry-run")
	if err != nil {
		t.Fatalf("destroy --dry-run: %v", err)
	}
	if !strings.Contains(out, "renamed_old") {
		t.Errorf("a volume no service claims should be reported rather than left invisible, got:\n%s", out)
	}
	// Fatal, not Error: the slice below indexes on this marker, so continuing after
	// it is missing turns a clean failure into a panic.
	if !strings.Contains(out, "NOT removed") {
		t.Fatalf("the plan should be clear that it isn't removing them, got:\n%s", out)
	}
	// The ones it does remove must not appear in the stranded list, and an external
	// volume is never stranded.
	stranded := out[strings.Index(out, "NOT removed"):]
	// Anchored to a whole list entry: "shared" is a suffix of "renamed_shared", and a
	// substring test would report a pass or a failure for the wrong reason.
	for _, notStranded := range []string{"renamed_data", "renamed_shared"} {
		if strings.Contains(stranded, "    - "+notStranded+"\n") {
			t.Errorf("%s is accounted for, so it is not stranded. Plan:\n%s", notStranded, out)
		}
	}
}

// The refusal is about *this directory's* files. A project with none of them —
// no `.opossum/`, no generated overlay — has nothing here to protect, so naming
// another project must go through. Refusing anyway would block a legitimate
// teardown and explain it with files that do not exist.
func TestDestroyDoesNotRefuseWhenThereAreNoLocalFilesToProtect(t *testing.T) {
	readLog := fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("INSPECT_PROJECT", "other")
	// A supervisor state directory exists for the *named* project: it is keyed by
	// name, not by this directory, so it must not count as a local file.
	if err := os.MkdirAll(filepath.Join(state, "opossum", "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "opossum", "other", "supervisor.log"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir() // bare project: compose file only
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: mine\nservices:\n  web:\n    image: web\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := run(t, "-p", "other", "destroy", "--force")
	if err != nil {
		t.Fatalf("there is nothing of this directory's to protect, so this should go through: "+
			"%v\n%s", err, out)
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "delete --force web.other.opossum") {
		t.Errorf("the named project's containers should have been removed, the runtime saw:\n%s", joined)
	}
	// …and the plan must not call the named project's state "this directory's files".
	if strings.Contains(out, "files opossum generated in") {
		t.Errorf("there are no generated files in this directory, so that heading is a lie:\n%s", out)
	}
}

// --keep-local is the escape hatch, so it has to work in the case that needs it:
// a mistargeted destroy where this directory *does* have generated files.
func TestDestroyKeepLocalWorksWhenThereIsSomethingLocalToKeep(t *testing.T) {
	readLog := fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("INSPECT_PROJECT", "other")
	if err := os.MkdirAll(filepath.Join(state, "opossum", "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "opossum", "other", "supervisor.log"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := destroyProject(t, "name: mine\nservices:\n  web:\n    image: web\n")
	t.Chdir(dir)

	if _, err := run(t, "-p", "other", "destroy", "--force", "--keep-local"); err != nil {
		t.Fatalf("--keep-local is the way past the refusal, so it must not be refused: %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "delete --force web.other.opossum") {
		t.Errorf("the named project's containers should still go, the runtime saw:\n%s", joined)
	}
	for _, rel := range []string{".opossum", "compose.opossum.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("--keep-local must keep %s, but: %v", rel, err)
		}
	}
}

// --dry-run removes nothing, so it must never be refused: a preview that exits
// non-zero and explains itself with "--force would remove…" describes something
// that was never going to happen.
func TestDestroyDryRunIsNeverRefused(t *testing.T) {
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "other")
	dir := destroyProject(t, "name: mine\nservices:\n  web:\n    image: web\n")
	t.Chdir(dir)

	out, err := run(t, "-p", "other", "destroy", "--force", "--dry-run")
	if err != nil {
		t.Fatalf("--dry-run removes nothing and must not be refused, even with --force: %v", err)
	}
	if !strings.Contains(out, "would remove") {
		t.Errorf("it should still print the plan, got:\n%s", out)
	}
	if !strings.Contains(out, "belongs to project \"mine\"") {
		t.Errorf("…including the warning about whose files these are, got:\n%s", out)
	}
}

// The plan's heading is the claim that these files are in this directory. A
// substring test against the whole output is satisfied by the absolute paths
// alone, so this pins the heading itself.
func TestDestroyPlanHeadingNamesTheDirectory(t *testing.T) {
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "named")
	dir := destroyProject(t, "name: named\nservices:\n  web:\n    image: web\n")
	t.Chdir(dir)

	out, err := run(t, "destroy", "--dry-run")
	if err != nil {
		t.Fatalf("destroy --dry-run: %v", err)
	}
	if want := "files opossum generated in " + dir; !strings.Contains(out, want) {
		t.Errorf("the plan should head the local files with %q, got:\n%s", want, out)
	}
}

// The stranded list is scoped by the project's own prefix. Without that scope it
// would list every volume on the machine as this project's leftover — and tell
// the reader they might be theirs to delete.
func TestDestroyStrandedListIsScopedToThisProject(t *testing.T) {
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "scoped")
	t.Setenv("VOLUME_LS", "NAME\nscoped_old\notherproj_data\npgdata")
	dir := destroyProject(t, "name: scoped\nservices:\n  web:\n    image: web\n")
	t.Chdir(dir)

	out, err := run(t, "destroy", "--dry-run")
	if err != nil {
		t.Fatalf("destroy --dry-run: %v", err)
	}
	if !strings.Contains(out, "scoped_old") {
		t.Fatalf("this project's leftover should be listed, got:\n%s", out)
	}
	for _, foreign := range []string{"otherproj_data", "pgdata"} {
		if strings.Contains(out, foreign) {
			t.Errorf("%s has nothing to do with this project and must not be listed, got:\n%s", foreign, out)
		}
	}
}

// "Nothing to remove" must not be said over volumes that are still on the disk.
// A second destroy is exactly when this matters: the containers and images have
// gone, and a leftover volume is the only thing left to report.
func TestDestroyDoesNotSayNothingLeftWhileVolumesRemain(t *testing.T) {
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_ABSENT", "web.left.opossum web-run.left.opossum")
	t.Setenv("NETWORK_ABSENT", "left-net")
	t.Setenv("IMAGE_ABSENT", "web")
	t.Setenv("VOLUME_LS", "NAME\nleft_old") // nothing claims it; nothing else is left
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: left\nservices:\n  web:\n    image: web\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := run(t, "destroy", "--dry-run")
	if err != nil {
		t.Fatalf("destroy --dry-run: %v", err)
	}
	if strings.Contains(out, "Nothing to remove") {
		t.Errorf("left_old is still on the disk, so this is the sentence to avoid. Output:\n%s", out)
	}
	if !strings.Contains(out, "left_old") {
		t.Errorf("the volume that is still there should be reported, got:\n%s", out)
	}
}
