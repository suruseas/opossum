package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeShim writes a `container` stand-in that logs each invocation to $FAKE_LOG
// and returns plausible output, then points OPOSSUM_CONTAINER_BIN at it.
func fakeShim(t *testing.T) func() []string {
	t.Helper()
	dir := t.TempDir()
	shim := filepath.Join(dir, "fake-container.sh")
	logPath := filepath.Join(dir, "invocations.log")
	script := `#!/bin/sh
echo "$*" >> "$FAKE_LOG"
case "$1" in
  system) [ "$2" = dns ] && [ "$3" = list ] && printf 'DOMAIN\nopossum\n' ;;
  network) [ "$2" = create ] && echo "$3" ;;
  inspect) echo '[{"status":{"state":"running","networks":[{"ipv4Address":"192.168.66.9/24"}]},"configuration":{"labels":{},"publishedPorts":[{"containerPort":80,"hostAddress":"0.0.0.0","hostPort":8080,"proto":"tcp"}]}}]' ;;
esac
exit 0
`
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPOSSUM_CONTAINER_BIN", shim)
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

func TestLogsFollowMultipleCLIErrors(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, `
name: demo
services:
  db:
    image: postgres:16
  web:
    image: web:latest
`)
	// --follow across all services is rejected.
	if _, err := run(t, "-f", compose, "logs", "--follow"); err == nil {
		t.Fatal("expected an error following multiple services")
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

func TestRunCLIOneOff(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web:latest\n")
	// --rm before the service; `-la` after passes through to the command.
	if _, err := run(t, "-f", compose, "run", "--rm", "web", "ls", "-la"); err != nil {
		t.Fatalf("run: %v", err)
	}
	joined := strings.Join(readLog(), "\n")
	if !strings.Contains(joined, "run --name web-run.demo.opossum") || !strings.Contains(joined, "web:latest ls -la") {
		t.Errorf("one-off run should override the command, got:\n%s", joined)
	}
	if !strings.Contains(joined, "delete --force web-run.demo.opossum") {
		t.Errorf("--rm should remove the one-off, got:\n%s", joined)
	}
}

func TestBuildCLI(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  api:\n    build: /ctx\n")
	if _, err := run(t, "-f", compose, "build"); err != nil {
		t.Fatalf("build: %v", err)
	}
	if joined := strings.Join(readLog(), "\n"); !strings.Contains(joined, "build -t demo-api:latest /ctx") {
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

func TestNoComposeFileWithoutFlagErrors(t *testing.T) {
	fakeShim(t)
	t.Chdir(t.TempDir()) // empty dir, no compose file
	if _, err := run(t, "ps"); err == nil {
		t.Fatal("expected an error when no compose file can be discovered")
	}
}
