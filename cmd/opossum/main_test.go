package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
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

func TestMain(m *testing.M) {
	d, err := os.MkdirTemp("", "opossum-cmd-test-")
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
	if !strings.Contains(out, "db: restart") { // ignored field surfaced
		t.Errorf("config should list ignored fields, got:\n%s", out)
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
