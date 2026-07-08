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
	"path/filepath"
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
	script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n  inspect) echo %q; exit %d ;;\nesac\nexit 0\n", out, code)
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
	shim := filepath.Join(dir, "fake-container.sh")
	logPath := filepath.Join(dir, "invocations.log")
	// `exec` simulates a healthcheck: it fails until the HEALTH_OK_AT-th call
	// (default 1 = healthy immediately), letting evals drive the retry loop.
	script := `#!/bin/sh
echo "$*" >> "$FAKE_LOG"
case "$1" in
  inspect)
    # Optionally attach an opossum.project label so evals can drive the
    # foreign-project collision guard (default: unlabeled). A prior run records
    # the container's config-hash under STATE_DIR, echoed back here so a second
    # up can detect an unchanged container and skip recreating it.
    lbl=""
    [ -n "$INSPECT_PROJECT" ] && lbl='"opossum.project":"'"$INSPECT_PROJECT"'"'
    if [ -n "$STATE_DIR" ] && [ -f "$STATE_DIR/$2.hash" ]; then
      h=$(cat "$STATE_DIR/$2.hash")
      [ -n "$lbl" ] && lbl="$lbl,"
      lbl="$lbl"'"opossum.config-hash":"'"$h"'"'
    fi
    echo '[{"status":{"state":"'"${INSPECT_STATE:-running}"'","networks":[{"network":"n","ipv4Address":"192.168.64.10/24","ipv4Gateway":"192.168.64.1"}]},"configuration":{"labels":{'"$lbl"'},"publishedPorts":[{"containerPort":8080,"hostAddress":"0.0.0.0","hostPort":8080,"proto":"tcp"}]}}]'
    ;;
  network)
    # NET_EXISTS makes create report the network already exists, so EnsureNetwork
    # returns created=false (drives the no-rollback-of-network case).
    if [ "$2" = create ]; then
      [ -n "$NET_EXISTS" ] && { echo "network $3 already exists" >&2; exit 1; }
      echo "created network $3"
    fi
    ;;
  run)
    # Record the container's config-hash (from -l opossum.config-hash=…) keyed by
    # its --name, so a later inspect reports it and up idempotency can be tested.
    if [ -n "$STATE_DIR" ]; then
      cname=""; chash=""; prev=""
      for a in $*; do
        [ "$prev" = --name ] && cname="$a"
        case "$a" in opossum.config-hash=*) chash="${a#opossum.config-hash=}" ;; esac
        prev="$a"
      done
      [ -n "$cname" ] && [ -n "$chash" ] && echo "$chash" > "$STATE_DIR/$cname.hash"
    fi
    # Simulate a container whose process exits non-zero: a foreground run of
    # $RUN_FAIL returns 1, letting evals drive the completed-successfully failure.
    case " $* " in *" --name $RUN_FAIL "*) [ -n "$RUN_FAIL" ] && exit 1 ;; esac
    ;;
  exec)
    # HEALTH_HANG makes the probe never return, to drive the per-attempt timeout.
    [ -n "$HEALTH_HANG" ] && sleep 30
    n=$(cat "$HEALTH_COUNTER" 2>/dev/null || echo 0)
    n=$((n + 1))
    echo "$n" > "$HEALTH_COUNTER"
    [ "$n" -ge "${HEALTH_OK_AT:-1}" ] || exit 1
    ;;
  volume)
    # volume ls lists the (newline-separated) names in VOLUME_LS, so evals can
    # drive VolumeExists (default: no volumes exist yet). VOLUME_LS_FAIL makes it
    # error, to drive the fail-safe path.
    [ -n "$VOLUME_LS_FAIL" ] && exit 1
    [ "$2" = ls ] && printf '%s\n' "$VOLUME_LS"
    ;;
  logs)
    # emit one line tagged with the container name (last arg) so log multiplexing
    # and per-service prefixing can be verified.
    for a in "$@"; do last="$a"; done
    echo "log-line $last"
    ;;
  ls)
    # container list: emit a summary per name in $LS_CONTAINERS (labeled with
    # project $LS_PROJECT) and $LS_FOREIGN (labeled with a different project, to
    # verify orphan removal never touches another project's containers).
    printf '['
    first=1
    for n in $LS_CONTAINERS; do
      [ "$first" = 1 ] || printf ','
      printf '{"status":{"state":"running"},"configuration":{"id":"%s","labels":{"opossum.project":"%s"}}}' "$n" "$LS_PROJECT"
      first=0
    done
    for n in $LS_FOREIGN; do
      [ "$first" = 1 ] || printf ','
      printf '{"status":{"state":"running"},"configuration":{"id":"%s","labels":{"opossum.project":"otherproj"}}}' "$n"
      first=0
    done
    printf ']'
    ;;
  image)
    # image inspect exits 0 (present) unless the ref is listed in IMAGE_ABSENT,
    # matching the real CLI (present=0, absent=1); drives ImageExists.
    if [ "$2" = inspect ]; then
      for m in $IMAGE_ABSENT; do [ "$3" = "$m" ] && exit 1; done
    fi
    ;;
esac
exit 0
`
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_LOG", logPath)
	t.Setenv("HEALTH_COUNTER", filepath.Join(dir, "health.count"))
	t.Setenv("STATE_DIR", dir) // remembers each run's config-hash for idempotency evals
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
	return &runtime.Runtime{Bin: shim}, read
}

// project builds a Project literal directly so evals control every field without
// YAML/path resolution noise.
// testBaseDir is a throwaway compose base directory shared by the tests, so that
// bind-mount resolution and `ensureBindDirs` (which creates missing host dirs)
// write under a temp dir instead of polluting the real /tmp (#132).
var testBaseDir string

func TestMain(m *testing.M) {
	d, err := os.MkdirTemp("", "opossum-orch-test-")
	if err != nil {
		panic(err)
	}
	testBaseDir = d
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
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(false); err != nil {
		t.Errorf("foreground up of a single service should be allowed, got %v", err)
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
	if err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Errorf("up should fail when a published host port is in use, got %v", err)
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

// Ignored service fields don't affect startup, so they're silent by default and
// only surface under --verbose (rt.Verbose) — a warning per field alarmed users.
func TestUpUnsupportedFieldsSilentUnlessVerbose(t *testing.T) {
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
	if got := upOutput(false); strings.Contains(got, "unsupported field") {
		t.Errorf("ignored-field warning should be hidden by default, got:\n%s", got)
	}
	got := upOutput(true)
	if !strings.Contains(got, "unsupported field") || !strings.Contains(got, "container_name") || !strings.Contains(got, "restart") {
		t.Errorf("--verbose should name the ignored fields, got:\n%s", got)
	}
}

func TestUpTopLevelIgnoredFieldsSilentUnlessVerbose(t *testing.T) {
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
	if got := upOutput(false); strings.Contains(got, "unsupported top-level") {
		t.Errorf("top-level ignored-fields warning should be hidden by default, got:\n%s", got)
	}
	if got := upOutput(true); !strings.Contains(got, "unsupported top-level field(s): networks, volumes") {
		t.Errorf("--verbose should show the top-level ignored fields, got:\n%s", got)
	}
}

func TestUpBuildsAndTags(t *testing.T) {
	rt, log := fakeShim(t)
	t.Setenv("IMAGE_ABSENT", "demo-api:latest") // a fresh build: the image isn't present yet
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
	t.Setenv("IMAGE_ABSENT", "demo-api:latest")
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
		t.Setenv("IMAGE_ABSENT", "demo-api:latest")
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
		t.Setenv("IMAGE_ABSENT", "demo-api:latest")
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
	dbRun := indexOf(lines, "run -d --name db.demo.opossum")
	oneOff := indexOf(lines, "run --name web-run.demo.opossum")
	if dbRun < 0 || oneOff < 0 || dbRun > oneOff {
		t.Fatalf("db should start before the one-off (db=%d one-off=%d) in %v", dbRun, oneOff, lines)
	}
	if !hasLine(lines, "run --name web-run.demo.opossum --network demo-net --dns-domain opossum --dns-search demo.opossum -l opossum.project=demo web:latest echo hi") {
		t.Errorf("one-off run mismatch, got %v", lines)
	}
	// The one-off is foreground (no -d) and publishes no ports.
	if indexOf(lines, "run -d --name web-run.demo.opossum") >= 0 {
		t.Error("one-off must run in the foreground (no -d)")
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
	if !hasLine(lines, "run --name web-run.demo.opossum --network demo-net --dns-domain opossum --dns-search demo.opossum -l opossum.project=demo web:latest serve") {
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
	oneOff := indexOf(lines, "run --name web-run.demo.opossum")
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

func TestUpSeedsFreshVolumesFromImage(t *testing.T) {
	// A fresh named or anonymous volume is seeded from the image's contents at the
	// mount path (a throwaway `run --rm -v <vol>:/__opossum_seed__`), mirroring
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
	if indexOf(lines, "run --rm -v demo_data:/__opossum_seed__ web:latest") < 0 {
		t.Errorf("named volume should be seeded from the image, got %v", lines)
	}
	if indexOf(lines, "run --rm -v demo_web_app_node_modules_") < 0 || indexOf(lines, ":/__opossum_seed__ web:latest") < 0 {
		t.Errorf("anonymous volume should be seeded from the image, got %v", lines)
	}
	// The bind mount's host path is never seeded.
	if indexOf(lines, "/src:/__opossum_seed__") >= 0 || indexOf(lines, "/app:/__opossum_seed__") >= 0 {
		t.Errorf("bind mounts must not be seeded, got %v", lines)
	}
}

func TestUpSkipsSeedingWhenVolumeExists(t *testing.T) {
	// An already-existing volume is left untouched — no re-seed — so user data and
	// prior state are preserved across re-ups.
	rt, log := fakeShim(t)
	t.Setenv("VOLUME_LS", "demo_data") // pretend this volume already exists
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
	t.Setenv("VOLUME_LS_FAIL", "1")
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
	t.Setenv("IMAGE_ABSENT", "postgres:16") // the pulled image isn't present locally
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

func TestWarnsPostgresDatadirNamedVolume(t *testing.T) {
	// A named volume mounted directly at Postgres's data dir will fail initdb, so
	// `up` warns and suggests the PGDATA subdirectory workaround — but only for
	// Postgres, only for named volumes, and only when the workaround isn't set (#103).
	const want = "won't start as written"
	run := func(svc *compose.Service, top map[string]compose.VolumeDecl) string {
		rt, _ := fakeShim(t)
		p := project("demo", map[string]*compose.Service{"db": svc})
		p.Volumes = top
		var out bytes.Buffer
		if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		return out.String()
	}

	cases := []struct {
		desc string
		svc  *compose.Service
		top  map[string]compose.VolumeDecl
		warn bool
	}{
		{"named volume at datadir, no PGDATA", &compose.Service{Image: "postgres:16", Volumes: []string{"pgdata:/var/lib/postgresql/data"}}, nil, true},
		{"named volume + trailing slash + :ro", &compose.Service{Image: "postgres:16", Volumes: []string{"pgdata:/var/lib/postgresql/data/:ro"}}, nil, true},
		{"named volume + :rw mode", &compose.Service{Image: "postgres:16", Volumes: []string{"pgdata:/var/lib/postgresql/data:rw"}}, nil, true},
		{"named volume + :cached mode", &compose.Service{Image: "postgres:16", Volumes: []string{"pgdata:/var/lib/postgresql/data:cached"}}, nil, true},
		{"PGDATA subdir set", &compose.Service{Image: "postgres:16", Environment: compose.Environment{"PGDATA=/var/lib/postgresql/data/pgdata"}, Volumes: []string{"pgdata:/var/lib/postgresql/data"}}, nil, false},
		{"PGDATA = datadir itself (trailing slash)", &compose.Service{Image: "postgres:16", Environment: compose.Environment{"PGDATA=/var/lib/postgresql/data/"}, Volumes: []string{"pgdata:/var/lib/postgresql/data"}}, nil, true},
		{"MySQL datadir", &compose.Service{Image: "mysql:8", Volumes: []string{"dbdata:/var/lib/mysql"}}, nil, false},
		{"bind mount at datadir", &compose.Service{Image: "postgres:16", Volumes: []string{"./data:/var/lib/postgresql/data"}}, nil, false},
		{"external volume at datadir", &compose.Service{Image: "postgres:16", Volumes: []string{"pgdata:/var/lib/postgresql/data"}}, map[string]compose.VolumeDecl{"pgdata": {External: true}}, false},
	}
	for _, c := range cases {
		got := strings.Contains(run(c.svc, c.top), want)
		if got != c.warn {
			t.Errorf("%s: warned=%v, want %v", c.desc, got, c.warn)
		}
	}

	// The warning is actionable: it names the fix (PGDATA subdirectory) and tells
	// the user to re-run up. It must not leak an internal tracking number.
	msg := run(&compose.Service{Image: "postgres:16", Volumes: []string{"pgdata:/var/lib/postgresql/data"}}, nil)
	for _, must := range []string{"PGDATA=/var/lib/postgresql/data/pgdata", "opossum up` again"} {
		if !strings.Contains(msg, must) {
			t.Errorf("warning missing %q; got: %s", must, msg)
		}
	}
	if strings.Contains(msg, "(#") {
		t.Errorf("warning leaks an internal issue number: %s", msg)
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

func TestPsShowsStoppedWhenExistsButNotRunning(t *testing.T) {
	// A container that exists but whose state is "stopped" must read "stopped",
	// not "absent" — the two are different situations.
	rt, _ := fakeShim(t)
	t.Setenv("INSPECT_STATE", "stopped")
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
	t.Setenv("HEALTH_OK_AT", "3") // db reports healthy only on the 3rd probe
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
	t.Setenv("HEALTH_HANG", "1") // the healthcheck exec never returns
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
	t.Setenv("HEALTH_OK_AT", "100000") // db never reports healthy, so up stays in the probe loop
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
	o.SetUpOptions(true, false, false, false) // --force-recreate
	if err := o.Up(true); err != nil {
		t.Fatalf("second up: %v", err)
	}
	if n := countLines(log(), "run -d --name web.demo.opossum"); n != 2 {
		t.Errorf("--force-recreate should recreate, want 2 runs got %d", n)
	}
}

// A configuration change (here: environment) recreates the service.
func TestUpRecreatesOnConfigChange(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}
	p.Services["web"].Environment = compose.Environment{"NEW=1"} // config changed
	if err := o.Up(true); err != nil {
		t.Fatalf("second up: %v", err)
	}
	if n := countLines(log(), "run -d --name web.demo.opossum"); n != 2 {
		t.Errorf("a changed service should be recreated, want 2 runs got %d", n)
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
		t.Setenv("IMAGE_ABSENT", "demo-api:latest")
		p := project("demo", map[string]*compose.Service{"api": {Build: &compose.Build{Context: "/ctx"}}})
		o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
		o.SetUpOptions(false, false, true, false) // --no-build
		if err := o.Up(true); err == nil || !strings.Contains(err.Error(), "no-build") {
			t.Fatalf("expected a --no-build error for a missing image, got %v", err)
		}
	})
}

// orphanProject: current compose has only `web`; the runtime still holds an
// `old` container from a since-removed service.
func orphanProject(t *testing.T) *compose.Project {
	t.Helper()
	t.Setenv("LS_CONTAINERS", "web.demo.opossum old.demo.opossum")
	t.Setenv("LS_PROJECT", "demo")
	return project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
}

func TestUpWarnsAboutOrphans(t *testing.T) {
	rt, _ := fakeShim(t)
	p := orphanProject(t)
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
	p := orphanProject(t)
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	o.SetUpOptions(false, false, false, true) // --remove-orphans
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
	p := orphanProject(t)
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Down(false, "", true); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if indexOf(log(), "delete --force old.demo.opossum") < 0 {
		t.Errorf("down --remove-orphans should delete the orphan, got %v", log())
	}
}

func TestDownWithoutFlagLeavesOrphans(t *testing.T) {
	rt, log := fakeShim(t)
	p := orphanProject(t)
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
	newProj := func(t *testing.T) *compose.Project {
		t.Setenv("LS_CONTAINERS", "web.demo.opossum")          // this project, current service
		t.Setenv("LS_PROJECT", "demo")                         // its label
		t.Setenv("LS_FOREIGN", "db.other.opossum otherproj-x") // a different project's containers
		return project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
	}

	rt, log := fakeShim(t)
	var out bytes.Buffer
	o := orchestrator.New(newProj(t), rt, "opossum", &out)
	o.SetUpOptions(false, false, false, true) // --remove-orphans
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
	t.Setenv("HEALTH_OK_AT", "999")      // probe never passes
	t.Setenv("INSPECT_STATE", "stopped") // the dependency container has exited
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
	// The error names the real cause and points at logs, not an opaque "healthcheck".
	if !strings.Contains(err.Error(), "not running") || !strings.Contains(err.Error(), "opossum logs db") {
		t.Errorf("error should report the exited container and suggest logs, got: %v", err)
	}
	// Fails fast: it bails after the first failed probe, not all 15.
	if n := countLines(log(), "exec db.demo.opossum"); n != 1 {
		t.Errorf("expected to bail after the first probe, got %d probes", n)
	}
}

func TestUpFailsWhenDependencyNeverHealthy(t *testing.T) {
	rt, log := fakeShim(t)
	t.Setenv("HEALTH_OK_AT", "999") // never healthy within the retry budget
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
	t.Setenv("RUN_FAIL", "web.demo.opossum") // web's run fails after db is up
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
	t.Setenv("NET_EXISTS", "1")              // network was already there (not ours)
	t.Setenv("RUN_FAIL", "web.demo.opossum") // and the up fails partway
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
	t.Setenv("INSPECT_PROJECT", "otherproj") // db.demo.opossum is owned by another project
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
	t.Setenv("INSPECT_PROJECT", "demo") // existing db.demo.opossum belongs to THIS project
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
	t.Setenv("RUN_FAIL", "migrate.demo.opossum") // migrate's process exits non-zero
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
