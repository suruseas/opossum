package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/suitedir"
	"github.com/suruseas/opossum/internal/workspace"
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
	// Recorded before any test chdirs: a child `go test .` has to be pointed at
	// the sources, and by then the working directory is somebody's t.TempDir().
	if wd, werr := os.Getwd(); werr == nil {
		testPackageDir = wd
	}

	d, err := suitedir.Make("opossum-cmd-test-")
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
	// Every test in this package runs against this directory rather than the
	// developer's own. `restart:` starts a supervisor, and a supervisor writes a
	// pid file and a log under XDG_STATE_HOME — so a test that forgets to say
	// where that goes writes into the home directory of whoever ran `go test`.
	// Tests that want their own still set it; this is only the floor.
	os.Setenv("XDG_STATE_HOME", filepath.Join(d, "state"))

	// Probes from an earlier run that was killed before it could clean up. The
	// probe names its project after the run that asked for it, so a probe whose
	// parent is gone is an orphan and nobody's but ours; one whose parent is
	// alive belongs to a suite running right now and is left alone. Swept before
	// the snapshot below, so they are not mistaken for this run's own leaks.
	sweepOrphanedProbes()

	// What was already supervising something before any test ran. Anything in
	// this set is somebody else's, however it got there. If the look failed, the
	// difference below is not taken at all: an empty "before" that means "could
	// not see" would make everything on this machine look new.
	supervisorsBefore, lookedBefore := runningSupervisors()

	code := m.Run()

	// A supervisor is meant to outlive the command that started it. That is the
	// point of it, and it is why a test that starts one and does not stop it
	// leaves a process running on this machine after `go test` has printed ok —
	// one that goes on watching a project whose files are about to be deleted.
	// Asked once: StopSupervisor waits for the process to go before it returns,
	// so a supervisor a test stopped is already gone by here. One that is still
	// running is one nobody stopped.
	reported := map[int]bool{}
	leaked, lookedPath := leakedProcesses(opossumBin)
	if len(leaked) > 0 {
		fmt.Fprint(os.Stderr, "\n"+leakReport(opossumBin, leaked))
		for _, pid := range leaked {
			reported[pid] = true
		}
		code = 1
	}
	// And any supervisor at all that was not running when this started. Searching
	// by path only finds the one path it was given, and a supervisor can be
	// started from three: the binary built above, this test binary (when a test
	// forgets OPOSSUM_SELF_BIN), and a copy a test made somewhere of its own.
	// Asking instead what is new since the suite began covers all three without
	// naming any of them — and leaves alone whatever was already running on this
	// machine, which is not this suite's to report.
	now, lookedAfter := runningSupervisors()
	if lookedAfter && lookedBefore {
		if started := unreported(newSupervisors(supervisorsBefore, now), reported); len(started) > 0 {
			fmt.Fprint(os.Stderr, "\n"+newSupervisorReport(started))
			code = 1
		}
	}
	// A look that did not happen is not a clean bill of health. Saying so on
	// stderr is not enough: `go test ./...` keeps the output of a package that
	// passed and throws away the rest, so in the one form the gate and CI use,
	// a note about not having looked lands in a green run and is never read.
	if why := uncheckedReport(lookedBefore, lookedPath, lookedAfter); why != "" {
		fmt.Fprint(os.Stderr, "\n"+why)
		code = 1
	}

	os.RemoveAll(d)
	os.Exit(code)
}

// leakReport is what the suite says when something outlived it. The command in
// it is meant to be typed, so the pids go in bare: printing a []int gives
// "kill [321 654]", which bash rejects as an argument and zsh tries to glob.
func leakReport(bin string, pids []int) string {
	var b strings.Builder
	b.WriteString("these processes outlived the tests that started them: ")
	for i, pid := range pids {
		if i > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "%d", pid)
	}
	fmt.Fprintf(&b, "\nthey are running %s, which is about to be removed — `kill", bin)
	for _, pid := range pids {
		fmt.Fprintf(&b, " %d", pid)
	}
	b.WriteString("` ends them\n")
	return b.String()
}

// uncheckedReport is what the suite says when one of its looks did not happen.
// It returns nothing when all three did.
//
// Each look is named rather than summarised, because they fail for different
// reasons and a reader who sees "the one before the tests" knows the comparison
// was skipped, while "the one after" means it was never made at all.
func uncheckedReport(before, path, after bool) string {
	var missed []string
	if !before {
		missed = append(missed, "the look before the tests")
	}
	if !path {
		missed = append(missed, "the look for processes running the binary this suite built")
	}
	if !after {
		missed = append(missed, "the look after the tests")
	}
	if len(missed) == 0 {
		return ""
	}
	return "this run did not check whether the tests left anything running: " +
		strings.Join(missed, " and ") + " did not happen.\n" +
		"the note above says why. a run that could not look is not a run that found nothing.\n"
}

// leakSaid is where the "I could not look" notes go. It is os.Stderr except
// while a test is reading them: those two branches are the ones that must not
// look like "no leaks", so something has to be able to see what they said.
var leakSaid io.Writer = os.Stderr

// runningSupervisors is every opossum supervisor on this machine right now,
// whoever started it, and whether the look succeeded at all. TestMain asks it
// before the tests and after, and the difference is what the tests left behind;
// the tests below ask it too, to check that it can see and that it says when it
// cannot.
func runningSupervisors() (pids []int, looked bool) {
	return leakedProcesses("__supervise")
}

// unreported drops the pids the search by path already named, so one process is
// named once. The path search speaks with more certainty — it found a process
// running a binary this suite built into a directory of its own — so it is the
// one that keeps them.
func unreported(pids []int, already map[int]bool) []int {
	var out []int
	for _, pid := range pids {
		if !already[pid] {
			out = append(out, pid)
		}
	}
	return out
}

// newSupervisorReport is what the suite says about supervisors that appeared
// while it ran. It is a weaker claim than the report by path and says so: these
// were not running before and are running now, which is usually a test that
// started one and did not stop it, and is sometimes someone else on this machine
// starting one while the tests ran. Nothing here can tell those apart, so the
// command is offered for the first case rather than given as the thing to do —
// and the second case has an exit worth naming, because a reader who has just
// been told about a process they recognise should not go looking for a bug.
func newSupervisorReport(pids []int) string {
	var b strings.Builder
	b.WriteString("these supervisors were not running when the tests started, and are now:")
	for _, pid := range pids {
		fmt.Fprintf(&b, " %d", pid)
	}
	b.WriteString("\nif the tests started them, `kill")
	for _, pid := range pids {
		fmt.Fprintf(&b, " %d", pid)
	}
	b.WriteString("` ends them; if something else on this machine did, leave them be and run the tests again — they will be there before the next run and it will not mention them\n")
	return b.String()
}

// newSupervisors is what is running now and was not before. A pid in both is
// the same process — pids are not reused while the process is alive, and these
// two lists are separated by one test run.
func newSupervisors(before, now []int) []int {
	was := make(map[int]bool, len(before))
	for _, pid := range before {
		was[pid] = true
	}
	var started []int
	for _, pid := range now {
		if !was[pid] {
			started = append(started, pid)
		}
	}
	return started
}

// leakedProcesses reports the pids still running the given binary. It is used
// after the suite, when the answer should be none.
//
// It asks pgrep rather than reading the pid files the supervisors write: a test
// that points XDG_STATE_HOME somewhere of its own would hide its pid file from
// us, and the process it leaves behind is the thing that matters. Where pgrep is
// not available it finds nothing, and says so rather than reporting none.
func leakedProcesses(bin string) (pids []int, looked bool) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		fmt.Fprintf(leakSaid, "\nno pgrep here, so nothing checked whether any process outlived the tests\n")
		return nil, false
	}
	// Quoted: pgrep -f takes an expression, and a temp directory with a bracket
	// in its name would turn into a pattern that matches something else — or
	// nothing, which reads here as "no leaks".
	out, err := exec.Command("pgrep", "-f", regexp.QuoteMeta(bin)).Output()
	if err != nil {
		// Exit 1 is pgrep's way of saying it matched nothing, which is the good
		// case. Anything else means it did not look, and that is not the same
		// answer even though it arrives in the same shape.
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return nil, true
		}
		fmt.Fprintf(leakSaid, "\npgrep could not look for processes outliving the tests: %v\n", err)
		return nil, false
	}
	// Nothing to exclude for this process. For the built binary's path there is
	// nothing to exclude at all — this test binary's command line does not carry
	// it. For "__supervise" the answer does not depend on the platform either:
	// the two snapshots are taken by the same process, so a pid that pgrep hands
	// back both times cancels in the difference. (An earlier version searched for
	// os.Executable(), and there the exclusion did matter, on Linux but not on
	// macOS — dropping it turned CI red with exactly one process reported while
	// every test passed, run 32671779974. That version is gone.)
	for _, line := range strings.Fields(string(out)) {
		if pid, cerr := strconv.Atoi(line); cerr == nil {
			pids = append(pids, pid)
		}
	}
	return pids, true
}

// fakeShim writes a `container` stand-in that logs each invocation to $FAKE_LOG
// and returns plausible output, then points OPOSSUM_CONTAINER_BIN at it.
func fakeShim(t *testing.T) func() []string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "invocations.log")
	t.Setenv("OPOSSUM_CONTAINER_BIN", fakeShimBin)
	t.Setenv("FAKE_LOG", logPath)
	// `up` watches each service for a second before calling it started. These evals
	// drive the real binary, so the only way to reach that setting is the
	// environment — and paying it in ~40 of them added 43s of pure sleep to a suite
	// where none of them are about the window. It is exercised where it belongs, in
	// the orchestrator's own evals.
	t.Setenv("OPOSSUM_CRASH_GRACE", "0")
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

// run executes the CLI with args and returns what it wrote to stdout and stderr
// together, in the order it was written, plus any error. Together, so a test
// that only cares what was said can read one string; a test that cares where
// it was said uses runSplit, because this cannot tell the two apart.
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

// runSplit is run with stdout and stderr captured separately, for a test about
// which of the two something is written to. Diagnostics, warnings and progress
// belong on stderr: a reader piping stdout into another tool sees only what
// that tool was meant to get.
func runSplit(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := newRootCmd()
	var out, errOut strings.Builder
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

// The suggestions a --from-docker-compose run hands over are diagnostics, and
// diagnostics go to stderr — said of this path when it was written, and until
// now unguarded: with stdout and stderr captured together, a run that wrote the
// suggestions to stdout read exactly the same (#511).
func TestTheSuggestionsTextGoesToStderr(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mine.yaml"), []byte(
		"name: sug\nservices:\n  web:\n    image: nginx\n    volumes:\n      - shared:/srv\n"+
			"  worker:\n    image: busybox\n    volumes:\n      - shared:/srv\nvolumes:\n  shared: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	stdout, stderr, err := runSplit(t, "up", "--from-docker-compose", "-f", "mine.yaml", "--no-build", "--dry-run")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	for _, line := range []string{
		"so none was written. Here is what one would hold",
		"# [opossum suggestion — NOT APPLIED]",
	} {
		if !strings.Contains(stderr, line) {
			t.Errorf("%q belongs on stderr, where diagnostics go, stderr:\n%s", line, stderr)
		}
		if strings.Contains(stdout, line) {
			t.Errorf("%q is a diagnostic and must not be on stdout, stdout:\n%s", line, stdout)
		}
	}
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

func TestPortCLI(t *testing.T) {
	// The shim's inspect publishes 80 -> 0.0.0.0:8080/tcp.
	readLog := fakeShim(t)
	compose := writeCompose(t, `
name: demo
services:
  web:
    image: web:latest
`)
	t.Run("prints host:port and nothing else on stdout", func(t *testing.T) {
		stdout, _, err := runSplit(t, "-f", compose, "port", "web", "80")
		if err != nil {
			t.Fatalf("port: %v", err)
		}
		if stdout != "0.0.0.0:8080\n" {
			t.Errorf("stdout = %q, want %q", stdout, "0.0.0.0:8080\n")
		}
	})
	// The service and the port come from the arguments, not from anywhere the
	// shim's one published port (web, 80) would agree with: a different port is
	// refused naming what is there, a different service is refused by name.
	t.Run("the container port asked for is the one looked up", func(t *testing.T) {
		_, err := run(t, "-f", compose, "port", "web", "443")
		if err == nil || err.Error() != "no port 443/tcp for container web.demo.opossum: 80/tcp" {
			t.Errorf("want the 443 miss listing 80/tcp, got: %v", err)
		}
	})
	t.Run("the service asked for is the one looked up", func(t *testing.T) {
		_, err := run(t, "-f", compose, "port", "nope", "80")
		if err == nil || !strings.Contains(err.Error(), `unknown service "nope"`) {
			t.Errorf("want the unknown-service refusal, got: %v", err)
		}
	})
	t.Run("--protocol picks the protocol", func(t *testing.T) {
		out, err := run(t, "-f", compose, "port", "--protocol", "udp", "web", "80")
		if err == nil || !strings.Contains(err.Error(), "no port 80/udp for container web.demo.opossum: 80/tcp") {
			t.Errorf("want the udp miss listing the tcp port, got err=%v out=%q", err, out)
		}
	})
	for _, tc := range []struct{ name, arg, want string }{
		{"a container port that is not a number", "abc", `container port must be a number from 1 to 65535, got "abc"`},
		{"a container port one past the range", "65536", `container port must be a number from 1 to 65535, got "65536"`},
		{"a container port of zero", "0", `container port must be a number from 1 to 65535, got "0"`},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			_, err := run(t, "-f", compose, "port", "web", tc.arg)
			if err == nil || err.Error() != tc.want {
				t.Errorf("want %q, got: %v", tc.want, err)
			}
		})
	}
	t.Run("a protocol other than tcp or udp is refused", func(t *testing.T) {
		_, err := run(t, "-f", compose, "port", "--protocol", "icmp", "web", "80")
		if err == nil || err.Error() != `--protocol must be tcp or udp, got "icmp"` {
			t.Errorf("want the protocol refusal, got: %v", err)
		}
	})
	t.Run("exactly two arguments are required", func(t *testing.T) {
		for _, args := range [][]string{{"web"}, {"web", "80", "extra"}} {
			if _, err := run(t, append([]string{"-f", compose, "port"}, args...)...); err == nil {
				t.Errorf("port %v must be refused", args)
			}
		}
	})
	t.Run("a stopped runtime is reported and not started", func(t *testing.T) {
		t.Setenv("SYSTEM_STOPPED", "1")
		t.Setenv("APISERVER_DOWN", "1")
		_, err := run(t, "-f", compose, "port", "web", "80")
		if err == nil || !strings.Contains(err.Error(), "OPSM-405") {
			t.Errorf("want the runtime-stopped signal, got: %v", err)
		}
		if joined := strings.Join(readLog(), "\n"); strings.Contains(joined, "system start") {
			t.Errorf("a read must not start the runtime, the runtime saw:\n%s", joined)
		}
	})
}

func TestVolumesCLI(t *testing.T) {
	readLog := fakeShim(t)
	compose := writeCompose(t, `
name: demo
services:
  web:
    image: web:latest
    volumes:
      - d1:/d1
      - ext:/ext
      - shared:/shared
      - later:/later
  db:
    image: db:latest
    volumes:
      - d2:/d2
      - shared:/shared
volumes:
  d1:
  d2:
  shared:
  later:
  unused:
  ext:
    external: true
`)
	// The runtime's table after both services started, out of name order, with
	// the external volume, another project's, and a volume bearing the
	// project's prefix that no service mounts, beside the project's own — and
	// without web's `later`, which the runtime has not made yet.
	t.Setenv("VOLUME_LS", "NAME         TYPE   DRIVER  OPTIONS\ndemo_d2      named  local\ndemo_unused  named  local\ndemo_shared  named  local\ndemo_d1      named  local\next          named  local\nother_d1     named  local")
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"the volumes the services mount that exist, in name order, the shared one once", []string{"volumes"},
			"DRIVER  VOLUME NAME\nlocal   demo_d1\nlocal   demo_d2\nlocal   demo_shared\n"},
		{"one service's volumes", []string{"volumes", "db"}, "DRIVER  VOLUME NAME\nlocal   demo_d2\nlocal   demo_shared\n"},
		{"-q prints names only", []string{"volumes", "-q"}, "demo_d1\ndemo_d2\ndemo_shared\n"},
		{"--format json prints an array", []string{"volumes", "--format", "json"},
			`[{"Name":"demo_d1","Driver":"local"},{"Name":"demo_d2","Driver":"local"},{"Name":"demo_shared","Driver":"local"}]` + "\n"},
		{"a mounted volume the runtime has not made yet is not listed", []string{"volumes", "web"},
			"DRIVER  VOLUME NAME\nlocal   demo_d1\nlocal   demo_shared\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _, err := runSplit(t, append([]string{"-f", compose}, tc.args...)...)
			if err != nil {
				t.Fatalf("%v: %v", tc.args, err)
			}
			if stdout != tc.want {
				t.Errorf("%v printed %q, want %q", tc.args, stdout, tc.want)
			}
		})
	}
	t.Run("a service the project does not define is refused", func(t *testing.T) {
		_, err := run(t, "-f", compose, "volumes", "nope")
		if err == nil || !strings.Contains(err.Error(), `unknown service "nope"`) {
			t.Errorf("want the unknown-service refusal, got: %v", err)
		}
	})
	t.Run("a format other than table or json is refused", func(t *testing.T) {
		_, err := run(t, "-f", compose, "volumes", "--format", "yaml")
		if err == nil || err.Error() != `--format must be table or json, got "yaml"` {
			t.Errorf("want the format refusal, got: %v", err)
		}
	})
	t.Run("a stopped runtime is reported and not started", func(t *testing.T) {
		t.Setenv("SYSTEM_STOPPED", "1")
		t.Setenv("APISERVER_DOWN", "1")
		stdout, _, err := runSplit(t, "-f", compose, "volumes")
		if err == nil || !strings.Contains(err.Error(), "OPSM-405") {
			t.Errorf("want the runtime-stopped signal, got: %v", err)
		}
		if stdout != "" {
			t.Errorf("an unreachable runtime is not an empty project, got %q", stdout)
		}
		if joined := strings.Join(readLog(), "\n"); strings.Contains(joined, "system start") {
			t.Errorf("a read must not start the runtime, the runtime saw:\n%s", joined)
		}
	})
}

func TestLsCLI(t *testing.T) {
	readLog := fakeShim(t)
	// Two projects, one with a stopped container beside a running one, plus a
	// container that is not opossum's. No compose file is given: ls looks at
	// the machine, not at a project.
	// Same shape as the orchestrator's fixture: names and states out of order
	// (shop before old, stopped before running), a state that is neither
	// running nor stopped, a container named without a project (only its
	// label places it) sitting between shop's two, blog's containers apart
	// from each other, shop's last container repeating a state seen two rows
	// earlier, and one container that is not opossum's.
	t.Setenv("CONTAINER_LS", `[`+
		`{"configuration":{"id":"db.shop.opossum","labels":{"opossum.project":"shop"}},"status":{"state":"stopped"}},`+
		`{"configuration":{"id":"api","labels":{"opossum.project":"blog"}},"status":{"state":"running"}},`+
		`{"configuration":{"id":"web.shop.opossum","labels":{"opossum.project":"shop"}},"status":{"state":"running"}},`+
		`{"configuration":{"id":"a.old.opossum","labels":{"opossum.project":"old"}},"status":{"state":"stopping"}},`+
		`{"configuration":{"id":"web.blog.opossum","labels":{"opossum.project":"blog"}},"status":{"state":"running"}},`+
		`{"configuration":{"id":"buildkit","labels":{"com.apple.container.plugin":"builder"}},"status":{"state":"running"}},`+
		`{"configuration":{"id":"worker.shop.opossum","labels":{"opossum.project":"shop"}},"status":{"state":"stopped"}}`+
		`]`)
	t.Chdir(t.TempDir())
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"the default view counts running containers and hides a project with none", []string{"ls"},
			"NAME  STATUS\nblog  running(2)\nshop  running(1)\n"},
		{"--all counts every state", []string{"ls", "--all"},
			"NAME  STATUS\nblog  running(2)\nold   stopping(1)\nshop  running(1), stopped(2)\n"},
		{"-a is --all", []string{"ls", "-a"},
			"NAME  STATUS\nblog  running(2)\nold   stopping(1)\nshop  running(1), stopped(2)\n"},
		{"-q prints names only", []string{"ls", "-q", "-a"}, "blog\nold\nshop\n"},
		{"-q wins over --format json", []string{"ls", "-q", "--format", "json"}, "blog\nshop\n"},
		{"--format json prints an array", []string{"ls", "--format", "json"},
			`[{"Name":"blog","Status":"running(2)"},{"Name":"shop","Status":"running(1)"}]` + "\n"},
		{"--verbose is accepted", []string{"--verbose", "ls", "-q"}, "blog\nshop\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _, err := runSplit(t, tc.args...)
			if err != nil {
				t.Fatalf("%v: %v", tc.args, err)
			}
			if stdout != tc.want {
				t.Errorf("%v printed %q, want %q", tc.args, stdout, tc.want)
			}
		})
	}
	t.Run("a format other than table or json is refused", func(t *testing.T) {
		_, err := run(t, "ls", "--format", "yaml")
		if err == nil || err.Error() != `--format must be table or json, got "yaml"` {
			t.Errorf("want the format refusal, got: %v", err)
		}
	})
	t.Run("an argument is refused", func(t *testing.T) {
		if _, err := run(t, "ls", "shop"); err == nil {
			t.Errorf("ls takes no argument; a project name must be refused")
		}
	})
	t.Run("a stopped runtime is reported and not started", func(t *testing.T) {
		t.Setenv("SYSTEM_STOPPED", "1")
		t.Setenv("APISERVER_DOWN", "1")
		stdout, _, err := runSplit(t, "ls")
		if err == nil || !strings.Contains(err.Error(), "OPSM-405") {
			t.Errorf("want the runtime-stopped signal, got: %v", err)
		}
		if stdout != "" {
			t.Errorf("an unreachable runtime is not an empty machine, got %q", stdout)
		}
		if joined := strings.Join(readLog(), "\n"); strings.Contains(joined, "system start") {
			t.Errorf("a read must not start the runtime, the runtime saw:\n%s", joined)
		}
	})
}

// columnsOf splits a rendered table line on the padding a tabwriter leaves
// between cells. Two spaces or more: a single space is inside a value.
func columnsOf(line string) []string {
	var out []string
	for _, c := range regexp.MustCompile(`\s{2,}`).Split(strings.TrimRight(line, " \t"), -1) {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// A failure stays on the lines opossum gave it.
//
// The message is a format opossum wrote with a project's values inside it, and
// only the first line begins at the margin — that is where opossum's own
// sentences start, and it is the only place a value could put one of its own.
// Messages that are deliberately more than one line keep their continuations;
// they already indent them.
//
// The checks are the shape, not a list of what must not appear: a continuation
// that begins where the message begins, and any control character at all. The
// second is there because a carriage return changes neither the number of lines
// nor the number of columns — the checks that catch a newline and a tab both
// walk past it.
func TestAFailureStaysOnItsOwnLines(t *testing.T) {
	forged := "[opossum note] service \"payroll\": opossum deleted your database"
	for name, c := range map[string]struct{ in, want string }{
		"one line, untouched": {
			"[OPSM-104] service \"usb\" needs /dev/ttyUSB0",
			"[OPSM-104] service \"usb\" needs /dev/ttyUSB0",
		},
		"a continuation opossum indented, untouched": {
			"building service \"web\": boom\n  (if Apple's builder can't handle this, import it)",
			"building service \"web\": boom\n  (if Apple's builder can't handle this, import it)",
		},
		"the runtime's own output, quoted": {
			"starting \"web\": exit 1\nError: no such image\nnot found",
			"starting \"web\": exit 1\n  Error: no such image\n  not found",
		},
		"a value that ends its line": {
			"service \"usb\" needs /dev/ttyUSB0\n" + forged + " for a bind mount",
			"service \"usb\" needs /dev/ttyUSB0\n  " + forged + " for a bind mount",
		},
		// opossum's own advice under a host-port conflict, which ended its line
		// at the margin. Word for word from orchestrator.go, so that a change to
		// the message shows up here rather than in front of a user.
		"opossum's own line at the margin, moved in": {
			"[OPSM-201] host port already in use:\n  - 8080\nfree the port or remap it in the compose file, then retry",
			"[OPSM-201] host port already in use:\n  - 8080\n  free the port or remap it in the compose file, then retry",
		},
		// The build hint is opossum's own too, and it arrives as a value on the
		// next line rather than in the format — so it is one of the five, and it
		// moves in by two as well. Its own continuations already sit further in
		// and stay where they are. Saying "the runtime's own output" of all five
		// was wrong; one of them is opossum talking.
		"opossum's own hint, arriving as a value": {
			"building service \"web\": exit status 1\nhint: the build ran out of disk space — free space and retry:\n" +
				"    container image prune -f",
			"building service \"web\": exit status 1\n  hint: the build ran out of disk space — free space and retry:\n" +
				"    container image prune -f",
		},
		"a value that goes back over the line": {
			"service \"usb\" needs /dev/ttyUSB0\r" + forged,
			"service \"usb\" needs /dev/ttyUSB0 " + forged,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := quoted(c.in); got != c.want {
				t.Errorf("quoted() is not what this file says it should be\n got: %q\nwant: %q", got, c.want)
			}
		})
	}
}

// End to end, through the thing main runs.
//
// The helper the other tests use calls Execute directly and hands the error
// back, so it never sees what main prints — a first attempt at this test asked
// whether "opossum: " appeared and was satisfied by an unrelated line, while
// taking the quoting out of main left every test green.
func TestAFailurePrintedByTheCommandStartsNoLine(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	forged := `[opossum note] service \"payroll\": opossum deleted your database`
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(
		"name: notes\nservices:\n  usb:\n    image: alpine:3\n    volumes:\n"+
			"      - \"/dev/ttyUSB0\\n"+forged+":/data\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	var out, errOut strings.Builder
	code := runCLI([]string{"up", "--from-docker-compose", "--no-build", "--no-supervisor"}, &out, &errOut)
	if code == 0 {
		t.Fatalf("this case is about what a failure looks like, and nothing failed:\n%s%s", out.String(), errOut.String())
	}
	// The failure line itself, found by what the failure says. Other lines of
	// the run begin with "opossum: " too — a warning the orchestrator wrote —
	// and binding to the first of those is how this test came to pass while main
	// printed nothing at all.
	lines := strings.Split(strings.TrimRight(errOut.String(), "\n"), "\n")
	at := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "opossum: [OPSM-104] ") {
			at = i
			break
		}
	}
	if at < 0 {
		t.Fatalf("main printed no failure line for the bind mount it could not create:\n%s", errOut.String())
	}
	// From the failure to the end: every line after the first is a continuation
	// of it, and a continuation does not begin where opossum begins.
	for i, line := range lines[at+1:] {
		if line != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("line %d after the failure begins where opossum begins: %q\n%s", i+1, line, errOut.String())
		}
	}
}

// What opossum says about itself and what it was asked for go to different
// places: a failure to stderr, a table to stdout. Somebody piping `opossum ps`
// into anything at all depends on the second, and nothing was looking at either
// — the writers are three arguments now where they used to be none, and an
// argument is a thing to get the wrong way round.
func TestTheTableAndTheFailureGoToDifferentPlaces(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(
		"name: demo\nservices:\n  web:\n    image: web:latest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	var out, errOut strings.Builder
	if code := runCLI([]string{"ps"}, &out, &errOut); code != 0 {
		t.Fatalf("ps: %d\n%s%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "SERVICE") {
		t.Errorf("the table belongs on stdout, and stdout has:\n%q", out.String())
	}
	if strings.Contains(errOut.String(), "SERVICE") {
		t.Errorf("the table is not a diagnostic, and stderr has:\n%q", errOut.String())
	}

	out.Reset()
	errOut.Reset()
	if code := runCLI([]string{"up", "--from-docker-compose", "nosuchservice"}, &out, &errOut); code == 0 {
		t.Fatalf("a service that is not there should fail:\n%s%s", out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "opossum: ") {
		t.Errorf("the failure belongs on stderr, and stderr has:\n%q", errOut.String())
	}
	if strings.Contains(out.String(), "opossum: ") {
		t.Errorf("the failure is not output, and stdout has:\n%q", out.String())
	}
}

// A table is one row per service, whatever the compose file called them.
//
// The rows are written through a tabwriter rather than through logf, so the
// flattening that keeps a project from ending a line early does not reach them.
// A service name with a newline in it used to split its own row: the column
// being filled was lost, the rest started at column zero where opossum's own
// sentences start, and every row below it stopped lining up.
func TestATableIsOneRowPerService(t *testing.T) {
	fakeShim(t)
	// A tab and a carriage return as well as a newline: a tab is read as the
	// column separator, so a name carrying one takes a column that is not its
	// own, and a carriage return moves the cursor back over the row already
	// printed. Flattening a newline alone leaves both.
	forged := `[opossum note] service \"payroll\": opossum\tdeleted\ryour database`
	// The image reference too, not only the service name. The first cell is the
	// one a check on line beginnings sees; the cells after it are where a column
	// can be taken quietly.
	compose := writeCompose(t, "name: demo\nservices:\n  \"web\\n"+forged+"\":\n    image: \"web:latest\\n"+forged+"\"\n")
	for _, cmd := range []string{"ps", "images"} {
		t.Run(cmd, func(t *testing.T) {
			out, err := run(t, "-f", compose, cmd)
			if err != nil {
				t.Fatalf("%s: %v", cmd, err)
			}
			body := strings.Split(strings.TrimRight(out, "\n"), "\n")
			// A header and one service: two rows, and the service is one of them.
			if len(body) != 2 {
				t.Errorf("one service, one row under the header, and this printed %d lines:\n%s", len(body), out)
			}
			for _, line := range body {
				if strings.HasPrefix(line, "[opossum note]") {
					t.Errorf("a service name started a line of its own:\n%s", out)
				}
			}
			// The row has as many columns as the header. A tab inside a cell
			// would open one more, and every value after it would be read under
			// the wrong heading.
			if len(body) == 2 {
				if head, got := columnsOf(body[0]), columnsOf(body[1]); len(head) != len(got) {
					t.Errorf("the header has %d columns and the row has %d:\n%s", len(head), len(got), out)
				}
			}
			// And no control character survived into a row at all. A carriage
			// return changes neither the number of lines nor the number of
			// columns — it moves the cursor back over what was already printed,
			// which the two checks above cannot see. The comment on this test
			// claimed the return was handled while nothing here looked for it.
			for _, line := range body {
				if i := strings.IndexFunc(line, func(r rune) bool { return r < ' ' || r == 0x7f }); i >= 0 {
					t.Errorf("a control character survived into a row at %d: %q\n%s", i, line, out)
				}
			}
		})
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

// The crash report for a bind mount that could not be chowned sends the reader to
// `up --from-docker-compose`, and does not say what that command will do — four
// versions of it tried and each was false somewhere. So what that command does is
// worth pinning here instead: with an overlay already present it says the mount on
// screen and leaves the file alone. That is the ordinary case, because the run
// that wrote the overlay is usually the one just before the crash.
func TestASuggestionFromACrashIsPrintedWhenAnOverlayAlreadyExists(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	for _, d := range []string{"rdata", "pgdata"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(
		"name: demo\nservices:\n  cache:\n    image: redis:7-alpine\n    volumes:\n      - ./rdata:/data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An overlay from an earlier run, generated by opossum.
	if err := os.WriteFile(filepath.Join(dir, "compose.opossum.yaml"), []byte(
		"# Generated by `opossum up --from-docker-compose`.\nservices: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// What the crash left behind: this service died taking ownership of this mount.
	if err := os.MkdirAll(filepath.Join(dir, ".opossum"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".opossum", "chown-failures.json"), []byte(
		`[{"service":"cache","target":"/data","source":"./rdata"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := run(t, "up", "--from-docker-compose", "--no-build")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if !strings.Contains(out, "OPSM-105") || !strings.Contains(out, "/data") {
		t.Errorf("the suggestion the crash guidance promised should be on screen, got:\n%s", out)
	}
	if !strings.Contains(out, "already exists") {
		t.Errorf("it should say why it is on screen rather than in the file, got:\n%s", out)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "compose.opossum.yaml"))
	if strings.Contains(string(got), "cache") {
		t.Errorf("an existing overlay is never overwritten, got:\n%s", got)
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

// A note is what opossum found and cannot fix with a compose change. Its body —
// what happens, and what to do instead — lives in the overlay, and the overlay is
// not written when notes are all there is: it is never overwritten once it exists,
// so a comment-only file would burn that one chance.
//
// That reasoning is about the file, not about the reader. Before this, a
// notes-only project got one summary line and nothing else, while the same note
// in a project that also needed a real change got its full body — the same
// diagnostic explained or not depending on what some other service required.
// The notes are read out without their comment marks, so a newline in anything
// they quote back — a path, a service name — would leave the rest of it standing
// on its own line, indistinguishable from a note opossum wrote. In the file that
// line is loose YAML and the overlay is rejected before it is written; on screen
// there is no such check, so the newline has to be gone before it gets there.
func TestAPathCannotWriteItsOwnNote(t *testing.T) {
	// Written as it appears inside a double-quoted YAML scalar.
	forged := `[opossum note] service \"payroll\": opossum deleted your database`
	// Three places a project's own text reaches the screen. The first is the one
	// this test was written for; the other two were still open while its comment
	// said the listing had been the last of them. Every field opossum quotes back
	// is a way in, which is why the flattening is at the one function they all
	// print through rather than at the places that call it.
	for name, c := range map[string]struct {
		body string
		// Which run reaches the line this case is about. The planned-command
		// listing is a dry run's; "Created host directory" is only ever printed
		// by a run that creates one, and a case that asks for it under
		// --dry-run is a second copy of the case above it.
		args []string
		// How many notes opossum has of its own to write about this project. A
		// device mount is one it cannot fix and says so; the other two are
		// ordinary projects, and the only note they could produce is a forged
		// one.
		notes int
	}{
		"a mount source, in the planned commands": {"name: notes\nservices:\n  usb:\n    image: alpine:3\n    volumes:\n" +
			"      - \"/dev/ttyUSB0\\n" + forged + ":/dev/ttyUSB0\"\n",
			[]string{"up", "--from-docker-compose", "--no-build", "--dry-run"}, 1},
		"an image reference, in the startup lines": {"name: notes\nservices:\n  usb:\n" +
			"    image: \"alpine:3\\n" + forged + "\"\n",
			[]string{"up", "--from-docker-compose", "--no-build", "--dry-run"}, 0},
		"a host directory opossum creates": {"name: notes\nservices:\n  usb:\n    image: alpine:3\n    volumes:\n" +
			"      - \"./data\\n" + forged + ":/data\"\n",
			[]string{"up", "--from-docker-compose", "--no-build", "--no-supervisor"}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			fakeShim(t)
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Chdir(dir)

			out, err := run(t, c.args...)
			if err != nil {
				t.Fatalf("up: %v", err)
			}
			// The line this case is about has to have been printed, or the loop
			// below satisfies itself by reading a screen that never mentioned it.
			if want := map[string]string{
				"a mount source, in the planned commands":  "Commands that would run:",
				"an image reference, in the startup lines": "Starting usb",
				"a host directory opossum creates":         "Created host directory",
			}[name]; !strings.Contains(out, want) {
				t.Fatalf("this case is about %q, and the run never printed it:\n%s", want, out)
			}
			// The whole screen, including the planned-command listing.
			starts := 0
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "[opossum note] service \"payroll\"") {
					t.Errorf("a project wrote itself a note:\n%s", out)
				}
				// Echoed inside a line it is a value; starting a line it is a
				// note. One service with one thing wrong with it should start
				// exactly one.
				if strings.HasPrefix(strings.TrimSpace(line), "[opossum note]") {
					starts++
				}
			}
			if starts != c.notes {
				t.Errorf("%d notes are opossum's to write here, and %d were written:\n%s", c.notes, starts, out)
			}
		})
	}
}

// One command, one line — whatever the compose file put in an argument.
//
// The check is the shape of the list, not a list of things that must not appear
// in it. A newline is the way found so far; naming it would leave the next one,
// and every line of this listing starting where the listing starts is the whole
// of what makes a line of it unmistakable.
func TestThePlannedCommandsAreOneCommandPerLine(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	// A mount source that ends its own line and starts a new one at column zero,
	// where opossum's own sentences start.
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(
		"name: notes\nservices:\n  usb:\n    image: alpine:3\n    volumes:\n"+
			"      - \"/dev/ttyUSB0\\n[opossum note] service \\\"payroll\\\": opossum deleted "+
			"your database:/dev/ttyUSB0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := run(t, "up", "--from-docker-compose", "--no-build", "--dry-run")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	listed, inList := 0, false
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "Commands that would run:"):
			inList = true
		case inList && strings.TrimSpace(line) == "":
			inList = false
		case inList:
			listed++
			if !strings.HasPrefix(line, "  ") {
				t.Errorf("a line of the listing does not start where the listing starts:\n%q\nin:\n%s", line, out)
			}
		}
	}
	// Exactly three, not at least three. A listing that never started would
	// satisfy the loop above by looking at nothing; a fourth line would mean an
	// argument had grown the listing a command of its own — the other half of
	// what a newline in there does, and the half a check on line beginnings
	// cannot see, because the forged line can begin with two spaces too.
	if listed != 3 {
		t.Errorf("%d commands listed, and this project creates a network, deletes, and runs:\n%s", listed, out)
	}
}

// The same path, doubled: the overlay escapes "$" so compose reads the text as
// the literal the user wrote. Read out on screen it goes through no compose, so
// a path with a "$" in it would come back saying something the user never typed
// — and the summary line two rows up would spell it the other way.
func TestAPathIsReadOutTheWayItWasWritten(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(
		"name: notes\nservices:\n  usb:\n    image: alpine:3\n    volumes:\n"+
			"      - \"/dev/tty$$USB0:/dev/ttyUSB0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := run(t, "up", "--from-docker-compose", "--no-build", "--dry-run")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if !strings.Contains(out, "/dev/tty$USB0") {
		t.Errorf("the path should be read out as it was written:\n%s", out)
	}
	if strings.Contains(out, "/dev/tty$$USB0") {
		t.Errorf("the doubled $ is for compose, and nothing here goes through compose:\n%s", out)
	}
}

// …and when there is nothing to read out, the offer to read it out goes too.
// Otherwise the reader is told "here is what it would have said" and handed a
// blank space, which reads as opossum having nothing to say rather than as
// opossum having lost track of it.
func TestNothingToReadOutMeansNoOfferToReadItOut(t *testing.T) {
	var buf strings.Builder
	body := "# Generated by opossum\n# ── Applied ───────────\n# [opossum] service \"db\": switched to a named volume.\n"
	reportNotesOnly(&buf, body, []orchestrator.Adaptation{{Code: "OPSM-106", Summary: "mounts a device"}})
	out := buf.String()
	if strings.Contains(out, "here is what it would have said") {
		t.Errorf("nothing was read out, so nothing should have been offered:\n%s", out)
	}
	if !strings.Contains(out, "OPSM-106") {
		t.Errorf("the summary is now the whole of it and should still be there:\n%s", out)
	}
	// Saying nothing at all is the other way to get this wrong: the reader is
	// looking for a file that is not there, and why it is not there is the one
	// thing left to tell them.
	if !strings.Contains(out, "no overlay was written") {
		t.Errorf("the file is still missing and still needs explaining:\n%s", out)
	}
}

// A body with no note in it is a body this function has stopped understanding.
// The old shape returned it whole, which would have put the overlay's own
// framing — or an applied block — on screen as if it were a note.
func TestProseFromABodyWithNoNoteInItIsEmpty(t *testing.T) {
	body := "# Generated by opossum\n# ── Applied ───────────\n# [opossum] service \"db\": switched to a named volume.\n"
	if got := noteProse(body); got != "" {
		t.Errorf("nothing here is a note, so there is nothing to read out; got:\n%s", got)
	}
}

// With -f, opossum sent the reader back for a second run without it — but for a
// project whose findings are all notes, that second run writes no overlay either,
// so the advice cost them a run and left them where they started. Which advice
// they get now comes from the same test the writing path uses, so it cannot
// promise a file that path would not write.
func TestMinusFDoesNotSendTheReaderBackForNothing(t *testing.T) {
	fakeShim(t)

	// Notes only: nothing here is fixable by a compose change.
	dir := t.TempDir()
	// The host side is a path that is not on this machine. What makes this a note
	// is the name — opossum reads `docker.sock` on either end of the mount — and
	// the note has to read the same whether or not Docker is installed on the
	// host running the eval. With the real /var/run/docker.sock, a machine with
	// Docker Desktop resolves it to a live socket and the pre-flight refuses the
	// up before any note is reached (OPSM-109), which is correct behaviour and
	// has nothing to do with what this test is about.
	sock := filepath.Join(dir, "absent", "docker.sock")
	if err := os.WriteFile(filepath.Join(dir, "mine.yaml"), []byte(
		"name: notes\nservices:\n  app:\n    image: alpine:3\n    volumes:\n      - "+sock+":/var/run/docker.sock\n"+
			"  usb:\n    image: alpine:3\n    volumes:\n      - /dev/ttyUSB0:/dev/ttyUSB0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	out, err := run(t, "up", "--from-docker-compose", "-f", "mine.yaml", "--no-build", "--dry-run")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if strings.Contains(out, "Re-run without -f") {
		t.Errorf("dropping -f writes nothing for these, so the reader should not be sent back:\n%s", out)
	}
	// Saying nothing is not the fix either — that was the state before opossum
	// said anything at all here. The line that explains why is the change.
	// The whole line, not a clause of it: how many, and why. Pinning one phrase
	// leaves the rest of the sentence free to say anything, and the count is the
	// half a reader checks against the list below it.
	const because = "An overlay is never written for these alone"
	want := "opossum: found 2 thing(s) opossum writes no YAML for. " + because +
		", so here is what one would have said:"
	if !strings.Contains(out, want) {
		t.Errorf("the reader should be told how many and why, got:\n%s\nwant a line: %s", out, want)
	}
	// And since the second run would not deliver them either, they are delivered
	// here — the same words the overlay would have held.
	if !strings.Contains(out, "does not answer for the containers here") || !strings.Contains(out, "PulseAudio") {
		t.Errorf("the notes' own words should reach the reader on this path too:\n%s", out)
	}
	// With the codes, which only the summary lines carry: the bodies name the
	// service, and the code is what AGENTS.md is indexed by.
	for _, code := range []string{"OPSM-204", "OPSM-106"} {
		if !strings.Contains(out, "opossum:   ["+code+"]") {
			t.Errorf("%s should be listed, not just described:\n%s", code, out)
		}
	}

	// The other side: one fixable thing, and dropping -f really does write it.
	fixable := t.TempDir()
	// The same absent host path as above, for the same reason.
	if err := os.WriteFile(filepath.Join(fixable, "mine.yaml"), []byte(
		"name: fix\nservices:\n  app:\n    image: alpine:3\n    volumes:\n      - "+
			filepath.Join(fixable, "absent", "docker.sock")+":/var/run/docker.sock\n"+
			"  db:\n    image: postgres:16\n    volumes:\n      - ./pgdata:/var/lib/postgresql/data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(fixable)
	out, err = run(t, "up", "--from-docker-compose", "-f", "mine.yaml", "--no-build", "--dry-run")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	// There is a change here, and it is handed over as the file's text — not
	// as an errand: a run without -f may write nothing, or something else,
	// and neither is this run's to promise (#518).
	if !strings.Contains(out, "so none was written. Here is what one would hold") {
		t.Errorf("a fixable change should be handed over, not promised for a second run:\n%s", out)
	}
	if strings.Contains(out, "Re-run without -f") {
		t.Errorf("the second run is not this run's to promise:\n%s", out)
	}
	// The headlines stay as the index of what follows.
	if !strings.Contains(out, "opossum: 2 change(s) opossum would apply:") {
		t.Errorf("the headlines should still index the text:\n%s", out)
	}
	if !strings.Contains(out, "      PGDATA: /var/lib/postgresql/data/pgdata") {
		t.Errorf("the overlay's own YAML should be on screen, since no file will be:\n%s", out)
	}
	// The way to use it is a command that keeps -f: discovery may find a
	// different file, or none, and an overlay named as a second -f merges
	// wherever it is. The advice is then followed, to the letter, and has to
	// work: the text on screen becomes the file, and the command applies it.
	const advice = "  opossum up --from-docker-compose -f mine.yaml -f compose.opossum.yaml"
	if !strings.Contains(out, "write it as compose.opossum.yaml next to mine.yaml and name both:\n"+advice+"\n") {
		t.Errorf("the reader should be told where to put it and how to name it, got:\n%s", out)
	}
	_, text, _ := strings.Cut(out, advice+"\n")
	var overlay []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "opossum:") || strings.HasPrefix(line, "Dry run") {
			if len(overlay) > 0 && strings.HasPrefix(line, "Dry run") {
				break
			}
			continue
		}
		overlay = append(overlay, strings.TrimPrefix(line, "  "))
	}
	if err := os.WriteFile(filepath.Join(fixable, "compose.opossum.yaml"), []byte(strings.Join(overlay, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	applied, err := run(t, "up", "--from-docker-compose", "-f", "mine.yaml", "-f", "compose.opossum.yaml", "--no-build", "--dry-run")
	if err != nil {
		t.Fatalf("up with the overlay named: %v", err)
	}
	if !strings.Contains(applied, "-e PGDATA=/var/lib/postgresql/data/pgdata") {
		t.Errorf("following the advice should apply the change to the planned run, got:\n%s", applied)
	}
	// The text carries the Docker-socket note's prose, so the up that follows
	// must not tell it again as a warning (the contract MarkNotesReported keeps).
	if n := strings.Count(out, "[OPSM-204]"); n != 1 {
		t.Errorf("the Docker-socket note was told %d times; its prose was shown, so once:\n%s", n, out)
	}
	// One or the other, never both: a project with a fixable thing and a note in
	// it takes the first branch whole.
	if strings.Contains(out, because) {
		t.Errorf("an overlay would hold something here, so the reader should not also be told it would not:\n%s", out)
	}

	// One note, not two. Two notes is the shape where "the notes" and "the last
	// note" are the same thing, and a count that starts at the wrong number reads
	// the same in both — a project with exactly one note is the one
	// this whole path exists for.
	single := t.TempDir()
	if err := os.WriteFile(filepath.Join(single, "mine.yaml"), []byte(
		"name: one\nservices:\n  sup:\n    image: alpine:3\n    restart: on-failure\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(single)
	// Not a dry run: the command in the report this came from is a plain `up`,
	// and a path that only spoke when nothing was going to happen anyway would
	// miss the reader it was written for. `restart:` is what starts a supervisor,
	// so this says where its state goes and asks for none — a background process
	// that outlives the test run is exactly what the opt-out is for.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	out, err = run(t, "up", "--from-docker-compose", "-f", "mine.yaml", "--no-build", "--no-supervisor")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if !strings.Contains(out, "opossum: found 1 thing(s) opossum writes no YAML for. "+because) ||
		!strings.Contains(out, "`always` and `unless-stopped` are honoured exactly") {
		t.Errorf("one note is still one the reader has to hear about:\n%s", out)
	}

	// What this does not cover, so that reading it is not mistaken for reading
	// more than it is. A second run also writes nothing when there is an overlay
	// in the directory already, when the compose file is under a name discovery
	// does not look for, and when the second run is another --dry-run. Opossum
	// still offers the second run in all three: -f puts it in the file's
	// directory and the second run reads the reader's, so from here it cannot
	// tell. They are #518, and none of them is measured by this test.
}

// A suggestion is not applied, but it is written down for the reader to
// uncomment — so an overlay does hold it, and its text is worth handing over.
// The question this asks is the same one the writing path asks, and asking a
// narrower one here (only applied changes count) would tell a project of
// suggestions that opossum was writing no YAML for them.
func TestMinusFHandsOverTheSuggestionsText(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mine.yaml"), []byte(
		"name: sug\nservices:\n  web:\n    image: nginx\n    volumes:\n      - shared:/srv\n"+
			"  worker:\n    image: busybox\n    volumes:\n      - shared:/srv\nvolumes:\n  shared: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	out, err := run(t, "up", "--from-docker-compose", "-f", "mine.yaml", "--no-build", "--dry-run")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if !strings.Contains(out, "so none was written. Here is what one would hold") {
		t.Errorf("an overlay holds a suggestion, so its text is worth handing over:\n%s", out)
	}
	// The suggestion itself, commented out as the file would carry it.
	if !strings.Contains(out, "# [opossum suggestion — NOT APPLIED]") {
		t.Errorf("the suggestion's text should be on screen, as the file would hold it:\n%s", out)
	}
	if strings.Contains(out, "opossum writes no YAML for") {
		t.Errorf("these are suggestions — opossum writes the YAML, commented:\n%s", out)
	}
}

// A suggestion is something the reader can act on by uncommenting it, so an
// overlay gets written to hold it. Both the writing path and the -f advice ask
// the same question to decide, and a version of that question that only counted
// applied changes would take a project of suggestions down the notes path — an
// overlay that never gets written, and advice saying no YAML was written.
func TestASuggestionIsSomethingToWriteDown(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	// Two services sharing one named volume: Apple container has no shared
	// volumes, so opossum writes the split as a suggestion rather than applying
	// it — which of the two should keep the data is not opossum's to decide.
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(
		"name: sug\nservices:\n  web:\n    image: nginx\n    volumes:\n      - shared:/srv\n"+
			"  worker:\n    image: busybox\n    volumes:\n      - shared:/srv\nvolumes:\n  shared: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	out, err := run(t, "up", "--from-docker-compose", "--no-build", "--dry-run")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if !strings.Contains(out, "would write compose.opossum.yaml") {
		t.Errorf("a suggestion is worth a file — it is the thing the reader uncomments:\n%s", out)
	}
	if strings.Contains(out, "no overlay was written") || strings.Contains(out, "opossum writes no YAML for") {
		t.Errorf("these are suggestions — opossum writes the YAML, commented:\n%s", out)
	}
}

// The notes a compose file cannot fix are written into the overlay, and when
// they are all there is, no overlay is written. This checks the words that would
// have been in it reach the reader instead — against the overlay itself, not
// against a list of phrases: a list only ever covers the lines someone thought
// to list, and the failure being fixed here is lines going missing.
func TestANoteReachesTheReaderWhenNoOverlayIsWritten(t *testing.T) {
	fakeShim(t)

	// Three things a compose file cannot fix, so all three are notes and there is
	// no actionable entry. Three and not one, and not two: with one note "the
	// notes" and "the note" are the same thing, and with two "the second" and
	// "the last" are — a middle note is the only one that is neither.
	dir := t.TempDir()
	// The host side is a path that is not on this machine. What makes this a note
	// is the name — opossum reads `docker.sock` on either end of the mount — and
	// the note has to read the same whether or not Docker is installed on the
	// host running the eval. With the real /var/run/docker.sock, a machine with
	// Docker Desktop resolves it to a live socket and the pre-flight refuses the
	// up before any note is reached (OPSM-109), which is correct behaviour and
	// has nothing to do with what this test is about.
	notes := "  app:\n    image: alpine:3\n    volumes:\n      - " +
		filepath.Join(dir, "absent", "docker.sock") + ":/var/run/docker.sock\n" +
		"  sup:\n    image: alpine:3\n    restart: on-failure\n" +
		"  usb:\n    image: alpine:3\n    volumes:\n      - /dev/ttyUSB0:/dev/ttyUSB0\n"

	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("name: notes\nservices:\n"+notes), 0o644); err != nil {
		t.Fatal(err)
	}

	// What the overlay would have held, taken from the planner that writes it
	// rather than from a list of phrases: a list only covers the lines someone
	// thought to list, and lines going missing is the failure being fixed here.
	proj, err := compose.Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	body, changes := orchestrator.New(proj, nil, "opossum", io.Discard).PlanOverlay()
	var want []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "# ──") {
			continue
		}
		trimmed = strings.ReplaceAll(strings.TrimPrefix(strings.TrimPrefix(trimmed, "#"), " "), "$$", "$")
		if !strings.HasPrefix(trimmed, "[opossum note]") {
			if len(want) == 0 {
				continue // the file describing its own sections, ahead of the first note
			}
		} else if len(want) > 0 {
			// In the file the column of "# " keeps the notes apart; on screen
			// nothing does but a blank line.
			want = append(want, "")
		}
		want = append(want, trimmed)
	}
	if n := countPrefixed(want, "[opossum note]"); n != len(changes) || n != 3 {
		t.Fatalf("three notes, %d planned and %d written up:\n%s", len(changes), n, body)
	}

	t.Chdir(dir)
	out, err := run(t, "up", "--from-docker-compose", "--no-build", "--dry-run")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.opossum.yaml")); err == nil {
		t.Error("a comment-only overlay is still not written; that part has not changed")
	}

	// Every line of it, in order, and nothing invented in between. What this
	// checks is the screen against the overlay: whatever the notes say, the
	// reader gets it word for word. Whether they say enough is the planner's own
	// business, and both sides here come from the planner — a note that thins out
	// upstream thins out on both sides of this comparison and stays green.
	got := readOut(t, out)
	if len(got) != len(want) {
		t.Errorf("the notes are %d lines in the overlay and %d on screen:\nwant:\n%s\ngot:\n%s",
			len(want), len(got), strings.Join(want, "\n"), strings.Join(got, "\n"))
	}
	for i := range want {
		if i >= len(got) {
			break
		}
		if got[i] != want[i] {
			t.Errorf("line %d differs:\nwant: %q\ngot:  %q\nfull screen:\n%s", i+1, want[i], got[i], out)
			break
		}
	}
	// The short form is the one for when there is nothing to read out. Having
	// read the notes out, saying it too would tell the reader the offer above
	// came to nothing.
	if strings.Contains(out, "(it would only hold comments).") {
		t.Errorf("the notes were read out, so the version that says they were not should be gone:\n%s", out)
	}
	// The summaries name the code; only the bodies name the service, and two
	// services on one image have identical summaries.
	for _, svc := range []string{`service "app"`, `service "sup"`, `service "usb"`} {
		if !strings.Contains(out, svc) {
			t.Errorf("a note that does not say whose it is cannot be acted on — %s is missing from:\n%s", svc, out)
		}
	}
}

// readOut is the block of notes opossum prints when it writes no overlay: the
// lines after it offers them, indented, with the blank line between notes kept
// so a note that loses its heading shows up as a line count that no longer
// matches.
func readOut(t *testing.T, out string) []string {
	t.Helper()
	const opening = "here is what it would have said:"
	i := strings.Index(out, opening)
	if i < 0 {
		t.Fatalf("the notes were not read out at all:\n%s", out)
	}
	var got []string
	for _, line := range strings.Split(out[i+len(opening):], "\n") {
		if strings.TrimSpace(line) == "" {
			if len(got) > 0 {
				got = append(got, "")
			}
			continue
		}
		if !strings.HasPrefix(line, "  ") {
			break
		}
		got = append(got, strings.TrimPrefix(line, "  "))
	}
	for len(got) > 0 && got[len(got)-1] == "" {
		got = got[:len(got)-1]
	}
	return got
}

func countPrefixed(lines []string, prefix string) int {
	n := 0
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// Two paths tell the reader to apply the changes by hand. Both used to point at
// "the warnings below" and print nothing below, which reads as a bug in opossum
// rather than a list the reader has to act on. Whatever the reason for not
// writing the overlay, the changes it would have held are the one thing the
// message is for.
func TestWhenTheOverlayIsNotWrittenTheChangesAreStillShown(t *testing.T) {
	compose := "name: demo\nservices:\n  db:\n    image: postgres:16\n    volumes:\n      - ./pgdata:/var/lib/postgresql/data\n"

	t.Run("an explicit -f, so an overlay would never be merged", func(t *testing.T) {
		fakeShim(t)
		dir := t.TempDir()
		cf := filepath.Join(dir, "compose.yaml")
		if err := os.WriteFile(cf, []byte(compose), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		out, err := run(t, "-f", cf, "up", "--from-docker-compose", "--no-build")
		if err != nil {
			t.Fatalf("up: %v", err)
		}
		if !strings.Contains(out, "OPSM-105") || !strings.Contains(out, "/var/lib/postgresql/data") {
			t.Errorf("the changes it will not write should be listed, got:\n%s", out)
		}
	})

	t.Run("a project directory that cannot be written to", func(t *testing.T) {
		fakeShim(t)
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o644); err != nil {
			t.Fatal(err)
		}
		// The bind source has to exist already: creating it is a separate failure
		// (OPSM-104) that stops the run before the overlay is ever attempted.
		if err := os.MkdirAll(filepath.Join(dir, "pgdata"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		out, err := run(t, "up", "--from-docker-compose", "--no-build")
		if err != nil {
			t.Fatalf("up: %v", err)
		}
		if !strings.Contains(out, "couldn't write") {
			t.Skipf("the overlay was written after all, so this path was not reached:\n%s", out)
		}
		if !strings.Contains(out, "OPSM-105") || !strings.Contains(out, "/var/lib/postgresql/data") {
			t.Errorf("the changes it could not write should be listed, got:\n%s", out)
		}
	})
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
	// ${PG_TAG:-16} in the fixture below resolves from the host if it is set,
	// which changes the image this asserts on.
	t.Setenv("PG_TAG", "") // records and restores the host's value, if any
	os.Unsetenv("PG_TAG")
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

// `*` activates every profile — on the `config --services` path as on `up`
// (docker compose, measured: `--profile '*'` and `COMPOSE_PROFILES=*` list
// every gated service, `backend,*` too; a partial pattern and an empty name
// activate nothing). The fixture is the oracle's: one service gated behind
// two profiles, one behind one, one open.
func TestConfigServicesUnderProfileStarCLI(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: nginx\n  db:\n    image: postgres\n    profiles: [backend]\n  tool:\n    image: alpine\n    profiles: [tools, debug]\n")
	services := func(t *testing.T, args ...string) string {
		t.Helper()
		out, err := run(t, append([]string{"-f", compose, "config"}, append(args, "--services")...)...)
		if err != nil {
			t.Fatalf("config --services %v: %v", args, err)
		}
		return strings.Join(strings.Fields(out), " ")
	}
	t.Setenv("COMPOSE_PROFILES", "")
	for _, tc := range []struct {
		name string
		args []string
		env  string
		want string
	}{
		{"nothing", nil, "", "web"},
		{"--profile tools", []string{"--profile", "tools"}, "", "tool web"},
		{"--profile *", []string{"--profile", "*"}, "", "db tool web"},
		{"COMPOSE_PROFILES=*", nil, "*", "db tool web"},
		{"COMPOSE_PROFILES=backend,*", nil, "backend,*", "db tool web"},
		{"--profile '' (empty name)", []string{"--profile", ""}, "", "web"},
		{"--profile to* (a name, not a pattern)", []string{"--profile", "to*"}, "", "web"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("COMPOSE_PROFILES", tc.env)
			if got := services(t, tc.args...); got != tc.want {
				t.Errorf("config --services = %q, want %q", got, tc.want)
			}
		})
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

// A service key with nothing under it used to crash the whole command with a
// nil dereference (#735); it is a load error now, and this runs the real CLI
// path so a crash would take the test binary down rather than pass unnoticed.
func TestConfigRefusesAServiceWithNothingUnderItCLI(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, "services:\n  web:\n")
	out, err := run(t, "-f", compose, "config")
	if err == nil {
		t.Fatal("config should refuse a service key with nothing under it")
	}
	if !strings.Contains(err.Error()+out, `service "web" must be a mapping`) {
		t.Errorf("want the mapping refusal, got err=%v out=%q", err, out)
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

// container 1.3.1 fails the whole `stats` call when one of the names it is
// handed does not exist (the shim does the same for $INSPECT_ABSENT). A
// service that was never started must not take the others' stats down with
// it: the CLI asks only for the containers that exist.
func TestStatsLeavesOutAServiceThatWasNeverStartedCLI(t *testing.T) {
	readLog := fakeShim(t)
	t.Setenv("INSPECT_ABSENT", "db.demo.opossum")
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web:latest\n  db:\n    image: db:latest\n")
	if _, err := run(t, "-f", compose, "stats", "--no-stream"); err != nil {
		t.Fatalf("stats with one never-started service should still show the other, got: %v", err)
	}
	var stats string
	for _, l := range readLog() {
		if strings.HasPrefix(l, "stats") {
			stats = l
		}
	}
	if !strings.Contains(stats, "web.demo.opossum") || strings.Contains(stats, "db.demo.opossum") {
		t.Errorf("stats should be asked for web only, got %q", stats)
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
	// The bind source has to exist before the directory is sealed: without it the
	// up fails for an unrelated reason (opossum cannot create a bind source, so
	// the container cannot start), and this eval would pass or fail on something
	// other than the overlay.
	if err := os.Mkdir(filepath.Join(dir, "pgdata"), 0o755); err != nil {
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
		"name: demo\nservices:\n  ci:\n    image: someci\n    volumes:\n      - ./sockdir:/var/run/docker.sock\n"), 0o644); err != nil {
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
	if !strings.Contains(out, "thing(s) opossum writes no YAML for") {
		t.Errorf("the note should still be reported, got:\n%s", out)
	}
}

// Every line that names a note, spelled out. Four of them go to the terminal and
// each is assembled from a fragment and a count, so reading the fragment back is
// not reading the line: `1 things` got past a suite that compared against the
// fragment, because the fragment was right and the sentence was not.
//
// The wording is written here rather than read from the code for the same reason
// it is written out in the documents: this is where it is read as a sentence.
func TestEveryLineThatNamesANoteIsWordForWord(t *testing.T) {
	fakeShim(t)

	// Notes and nothing else: no overlay is written, so these two lines are the
	// whole of what the reader gets.
	only := t.TempDir()
	if err := os.WriteFile(filepath.Join(only, "compose.yaml"), []byte(
		"name: only\nservices:\n  sup:\n    image: alpine:3\n    restart: on-failure\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(only)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	out, err := run(t, "up", "--from-docker-compose", "--no-build", "--no-supervisor")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if want := "opossum: nothing to fix or suggest, but 1 thing(s) opossum writes no YAML for:"; !strings.Contains(out, want) {
		t.Errorf("the notes-only report is not what this file says it should be:\n%s\nwant a line: %s", out, want)
	}

	// An overlay with all three classes in it: the note line here is a different
	// one, printed beside the changes that were written.
	mixed := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mixed, "pg"), 0o755); err != nil {
		t.Fatal(err)
	}
	mixed3 := []byte(`
name: mixed
services:
  db:
    image: postgres:16
    volumes:
      - ./pg:/var/lib/postgresql/data
  web:
    image: nginx
    volumes:
      - shared:/srv
  worker:
    image: busybox
    volumes:
      - shared:/srv
  sup:
    image: alpine:3
    restart: on-failure
volumes:
  shared: {}
`)
	if err := os.WriteFile(filepath.Join(mixed, "compose.yaml"), mixed3, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(mixed)
	const noteLine = "opossum: 1 note(s) about things opossum writes no YAML for:"
	out, err = run(t, "up", "--from-docker-compose", "--no-build", "--no-supervisor")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if !strings.Contains(out, noteLine) {
		t.Errorf("the note line beside a written overlay is not what this file says:\n%s\nwant a line: %s", out, noteLine)
	}

	// The same project with -f, in a directory of its own: nothing is written,
	// and the entries are grouped under their own labels. A second place the line
	// is assembled, so a second place it can go wrong. Its own directory because
	// the run above left an overlay in that one, and a case that only passes
	// because of what the case before it wrote is a case that reads as an
	// accident later.
	grouped := t.TempDir()
	if err := os.MkdirAll(filepath.Join(grouped, "pg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(grouped, "compose.yaml"), mixed3, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(grouped)
	out, err = run(t, "up", "--from-docker-compose", "-f", "compose.yaml", "--no-build", "--dry-run")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if !strings.Contains(out, noteLine) {
		t.Errorf("the note label in the grouped report is not what this file says:\n%s\nwant a line: %s", out, noteLine)
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
	// The container is never created, and `up` now reports what is *running* when it
	// finishes — so the shim has to admit this one does not exist. Its default is
	// that every container does, which would report one that was never created.
	t.Setenv("INSPECT_ABSENT", "app.rolled.opossum app-run.rolled.opossum")
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
	// The refusal names the service and then the image, both strings: swapped,
	// it reads as sound English about a service called after an image (#559).
	if want := `service "app": image "rolled-app:latest" is not built and --no-build was given`; !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal should say %q, got: %v", want, err)
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
	// The decline path never reaches the leftovers report, so it must at least
	// point at the one command that shows it (#361, option b).
	if !strings.Contains(buf.String(), "--dry-run") {
		t.Errorf("declining should point at --dry-run for the kept items, got:\n%s", buf.String())
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
	handwritten := "# My own Apple-container tweaks. I wrote this by hand.\nservices:\n  web:\n    mem_limit: 512m\n"
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
	// Both lines whole, because both carry two things that can be swapped for
	// each other. The heading's directory was checked with `Contains(out, dir)`,
	// which is satisfied by any of the three places the directory appears — the
	// heading and the two paths under it; the warning names two
	// project names and says which files belong to which, and `Contains(out,
	// "belongs to project \"mine\"")` reads only the half before the swap. The
	// format string behind it has two same-typed arguments and has already
	// shipped that fault once — see the note beside codeBindFilePlaceholder.
	//
	// Measured: swapping the last two verbs of that format string leaves the
	// whole of cmd/opossum green.
	wantHeading := "  files opossum generated in " + dir + ":"
	if got, n := oneLineWith(out, "  files opossum generated in"); n != 1 || got != wantHeading {
		t.Errorf("the heading over the local files is not what it should be (%d lines start "+
			"with it).\n got: %q\nwant: %q", n, got, wantHeading)
	}
	wantWarning := "    ! this directory belongs to project \"mine\", not \"other\" — these files " +
		"are \"mine\"'s. Use --keep-local to remove only \"other\"'s containers, volumes and images."
	if got, n := oneLineWith(out, "    ! this directory"); n != 1 || got != wantWarning {
		t.Errorf("the warning about whose files these are is not what it should be (%d lines "+
			"start with it).\n got: %q\nwant: %q", n, got, wantWarning)
	}
}

// oneLineWith returns the line of s that begins with prefix, and how many there
// were. The count comes back rather than being folded into an empty string: a
// line that vanished, one that grew a twin, and one whose wording changed are
// three different faults.
func oneLineWith(s, prefix string) (string, int) {
	found, n := "", 0
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, prefix) {
			found, n = line, n+1
		}
	}
	return found, n
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
// alone — and pinning only the heading is satisfied by a heading that names a
// directory below this one. The plan is short and every path in it comes from
// the directory this test made, so the whole thing is written out.
func TestDestroyPlanIsWhatItSaysItIs(t *testing.T) {
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
	want := "Destroying project \"named\" would remove:\n" +
		"  containers:\n" +
		"    - web-run.named.opossum\n" +
		"    - web.named.opossum\n" +
		"  networks:\n" +
		"    - named-net\n" +
		"  images:\n" +
		"    - web\n" +
		"  files opossum generated in " + dir + ":\n" +
		"    - " + filepath.Join(dir, ".opossum") + "\n" +
		"    - " + filepath.Join(dir, "compose.opossum.yaml") + "\n" +
		"  your compose file, .env and sources are NOT touched.\n" +
		"Left alone, because it isn't this project's to remove:\n" +
		"  - the \"opossum\" DNS domain — remove with: sudo container system dns delete opossum\n" +
		"  - the build cache and unused images — reclaim with: container builder delete --force && container image prune -a\n"
	if out != want {
		t.Errorf("the plan is not what it should be.\n got: %q\nwant: %q", out, want)
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

// The changelog's promise, at the layer the user sees it: a bring-up that fails
// and rolls back still leaves a supervisor watching the service it never touched.
// The orchestrator tests measure the set; this one measures that the set reaches
// the supervisor.
func TestRolledBackUpStillSupervisesAnUntouchedService(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	t.Setenv("STATE_DIR", t.TempDir()) // the shim remembers deletions
	t.Setenv("INSPECT_PROJECT", "survive")
	dir := t.TempDir()
	// cache has no `build:`, so it comes up and stays up. app must be built, and
	// --no-build refuses inside the start loop — which is what rolls the call back.
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: survive\nservices:\n"+
			"  cache:\n    image: cache\n    restart: always\n"+
			"  zapp:\n    build: .\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Cleanup(func() { run(t, "down") })

	// First: everything up, so cache's config hash is recorded and it is left alone
	// next time.
	if _, err := run(t, "up"); err != nil {
		t.Fatalf("first up: %v", err)
	}
	waitFor(t, "the first supervisor", func() bool { return supervisorPID(t, state, "survive") != 0 })
	if _, err := run(t, "down"); err != nil {
		t.Fatalf("down: %v", err)
	}
	if _, err := run(t, "up"); err != nil {
		t.Fatalf("second up: %v", err)
	}
	waitFor(t, "the supervisor to be back", func() bool { return supervisorPID(t, state, "survive") != 0 })
	first := supervisorPID(t, state, "survive")

	// Now a bring-up that fails: zapp needs building and --no-build refuses. cache is
	// unchanged, so it is skipped — and survives the rollback.
	t.Setenv("IMAGE_ABSENT", "survive-zapp:latest")
	if _, err := run(t, "up", "--no-build"); err == nil {
		t.Fatal("up should fail when a service must be built and --no-build was given")
	}
	// cache is still running under `restart: always`, so it must still be watched.
	if pid := supervisorPID(t, state, "survive"); pid == 0 {
		t.Error("the failed up left cache running, so a supervisor must still be watching it")
	} else if pid != first {
		t.Errorf("the supervisor was replaced (%d -> %d) when its set did not change", first, pid)
	}
	b, _ := os.ReadFile(filepath.Join(state, "opossum", "survive", "supervised"))
	if !strings.Contains(string(b), "cache") {
		t.Errorf("cache should be in the watched set, it holds %q", b)
	}
}

// `ws` snapshots are the largest thing opossum leaves behind, and destroy will
// not remove them: a snapshot belongs to the directory that was snapshotted, and
// that directory outlives any number of projects. So the teardown has to name
// them — and it must not name them for a project that never took one, or the
// leftovers list becomes something people stop reading.
func TestDestroyReportsWorkspaceSnapshotsOnlyWhenThereAreSome(t *testing.T) {
	for _, tc := range []struct {
		name      string
		snapshots bool
	}{{"with snapshots", true}, {"without", false}} {
		t.Run(tc.name, func(t *testing.T) {
			fakeShim(t)
			t.Setenv("STATE_DIR", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("INSPECT_PROJECT", "snaps")
			dir := destroyProject(t, "name: snaps\nservices:\n  web:\n    image: web\n")
			snapDir := filepath.Join(dir, workspace.SnapshotDirName)
			if tc.snapshots {
				if err := os.MkdirAll(filepath.Join(snapDir, "try-1"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(dir)

			out, err := run(t, "destroy", "--force")
			if err != nil {
				t.Fatalf("destroy: %v", err)
			}
			// The whole output, for the name alone: the block below is read
			// from "Left alone," onwards, so a plan that names the snapshot
			// directory *above* that line is outside it. Removing this in
			// favour of the block was a real loss — measured, on a mutation
			// that put the name in the plan.
			if got := strings.Contains(out, workspace.SnapshotDirName); got != tc.snapshots {
				t.Errorf("mentions snapshots = %v, want %v, got:\n%s", got, tc.snapshots, out)
			}
			// The whole block, not a phrase out of it. This hands the user an
			// `rm -rf`, and a check that looks for `rm -rf <dir>` is satisfied
			// by `rm -rf <dir>/..` — which removes the project. Measured: that
			// is exactly what the version before this let through.
			//
			// Built from the directory rather than normalised away, because the
			// test made the directory and can say what belongs on the line.
			want := "Left alone, because it isn't this project's to remove:\n" +
				"  - the \"opossum\" DNS domain — remove with: sudo container system dns delete opossum\n" +
				"  - the build cache and unused images — reclaim with: container builder delete --force && container image prune -a"
			if tc.snapshots {
				want += "\n  - workspace snapshots in " + snapDir +
					" — they belong to that directory, not to this project; see them with: ls " + snapDir +
					", remove with: rm -rf " + snapDir
			}
			_, block, found := strings.Cut(out, "Left alone,")
			if !found {
				t.Fatalf("no `Left alone` section at all, got:\n%s", out)
			}
			if got := strings.TrimRight("Left alone,"+block, "\n"); got != want {
				t.Errorf("the section that tells people what to run themselves is not what it "+
					"should be.\n got: %q\nwant: %q", got, want)
			}
			if !tc.snapshots {
				return
			}
			if _, err := os.Stat(filepath.Join(snapDir, "try-1")); err != nil {
				t.Errorf("destroy removed a workspace snapshot it said it was leaving alone: %v", err)
			}
		})
	}
}

// `--dry-run` is the mode for looking before deciding, and what destroy leaves
// behind is part of that decision. It used to be the one mode that didn't say:
// the leftovers were printed after the removal, so a preview of a project with
// something to remove ended without them.
//
// The two outputs are compared rather than checked for keywords, because the
// failure this guards against is a one-sided change — a leftover added to the
// real run and forgotten in the preview reads as "nothing else is left".
func TestDestroyDryRunReportsTheSameLeftoversAsTheRealRun(t *testing.T) {
	leftovers := func(out string) string {
		_, rest, found := strings.Cut(out, "Left alone, because it isn't this project's to remove:")
		if !found {
			return ""
		}
		return strings.TrimSpace(rest)
	}
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "preview")
	dir := destroyProject(t, "name: preview\nservices:\n  web:\n    image: web\n")
	// A snapshot directory, so the section has something project-specific in it and
	// the comparison is not just two copies of the same two static lines.
	if err := os.MkdirAll(filepath.Join(dir, workspace.SnapshotDirName, "try-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	preview, err := run(t, "destroy", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if leftovers(preview) == "" {
		t.Fatalf("the preview says nothing about what it would leave behind:\n%s", preview)
	}
	// There is something to remove, or this would be the already-working
	// "Nothing to remove" path and the test would prove nothing.
	if !strings.Contains(preview, "would remove") {
		t.Fatalf("this project should have something to remove, got:\n%s", preview)
	}

	real, err := run(t, "destroy", "--force")
	if err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if got, want := leftovers(preview), leftovers(real); got != want {
		t.Errorf("the preview and the real run disagree about what is left alone\npreview:\n%s\n\nreal:\n%s", got, want)
	}
}

// The interactive question comes before the leftovers, and `--dry-run` prints them
// with no question at all. Both halves are deliberate (see printSystemLeftovers),
// and both are here because the reasoning lives in a comment that cannot stop the
// call from being moved.
//
// The failure this guards against is not a crash: moving the list above the prompt
// reads as an improvement — it is what `--dry-run` does — and nothing else would
// notice that the question had been pushed off the screen by four lines about
// things destroy does not touch.
func TestDestroyAsksBeforeItSaysWhatItLeaves(t *testing.T) {
	const heading = "Left alone, because it isn't this project's to remove:"
	const question = "Remove all of it?"

	// destroy refuses to guess without a terminal, so the interactive path is only
	// reachable through this seam.
	orig := stdinIsTerminal
	stdinIsTerminal = func() bool { return true }
	defer func() { stdinIsTerminal = orig }()

	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "asks")
	dir := destroyProject(t, "name: asks\nservices:\n  web:\n    image: web\n")
	t.Chdir(dir)

	// The preview is the other half of the same decision: no question, and the list
	// is the point of running it. It runs FIRST — after the interactive destroy there
	// is nothing left, so a preview then goes down the "Nothing to remove" path, which
	// prints the list from somewhere else entirely and would assert nothing about
	// --dry-run at all.
	preview, err := run(t, "destroy", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if !strings.Contains(preview, "would remove") {
		t.Fatalf("the preview found nothing to remove, so it is not exercising the "+
			"--dry-run path this means to check:\n%s", preview)
	}
	if strings.Contains(preview, question) {
		t.Errorf("--dry-run should not ask anything, got:\n%s", preview)
	}
	if !strings.Contains(preview, heading) {
		t.Errorf("--dry-run is read to decide with, so it has to say what stays, got:\n%s", preview)
	}

	root := newRootCmd()
	var buf strings.Builder
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetIn(strings.NewReader("y\n"))
	root.SetArgs([]string{"destroy"})
	if err := root.Execute(); err != nil {
		t.Fatalf("destroy: %v\n%s", err, buf.String())
	}
	out := buf.String()
	q, l := strings.Index(out, question), strings.Index(out, heading)
	if q < 0 {
		t.Fatalf("the confirmation was never asked, so this proves nothing:\n%s", out)
	}
	if l < 0 {
		t.Fatalf("the leftovers were never printed:\n%s", out)
	}
	if q > l {
		t.Errorf("the leftovers came before the question — they say what destroy will NOT "+
			"touch, so they cannot change the answer, and putting them there separates the "+
			"question from the plan it is about:\n%s", out)
	}
}

// A suggestion driven by a crash arrives after the overlay was written — the
// service had to start and fail first — so it always lands on the "already
// exists" path. What that path says has to be right about whose file it is:
// telling someone to delete a hand-written overlay is telling them to throw work
// away, which is the mistake this branch exists to stop repeating.
func TestFromDockerComposeAdviceDependsOnWhoWroteTheOverlay(t *testing.T) {
	const compose = "name: demo\nservices:\n  db:\n    image: postgres:16\n    volumes:\n      - ./pgdata:/var/lib/postgresql/data\n"
	for _, tc := range []struct {
		name, overlay  string
		wantSubstrings []string
		notWant        string
	}{
		{
			name:    "opossum wrote it",
			overlay: "# Generated by `opossum up --from-docker-compose`.\nservices: {}\n",
			// Regenerating costs nothing, but any edits go with it — so say both.
			wantSubstrings: []string{"generated by opossum", "rm compose.opossum.yaml", "edits you made"},
			notWant:        "your own file",
		},
		{
			name:           "the user wrote it",
			overlay:        "# hand written\nservices: {}\n",
			wantSubstrings: []string{"your own file", "mv compose.opossum.yaml", "copy what you want back"},
			notWant:        "rm compose.opossum.yaml",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeShim(t)
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "compose.opossum.yaml"), []byte(tc.overlay), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Chdir(dir)
			out, err := run(t, "up", "--from-docker-compose", "--no-build")
			if err != nil {
				t.Fatalf("up: %v\n%s", err, out)
			}
			// There has to be something left to report, or the branch is never reached
			// and this asserts nothing.
			if !strings.Contains(out, "already exists") {
				t.Fatalf("the overlay should have been left alone and reported, got:\n%s", out)
			}
			for _, want := range tc.wantSubstrings {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q in:\n%s", want, out)
				}
			}
			if strings.Contains(out, tc.notWant) {
				t.Errorf("should not say %q here:\n%s", tc.notWant, out)
			}
		})
	}
}

// The override file is picked up without anyone passing `-f`, so the road where
// several files get merged is the one most people are on without knowing it. A
// failure there has to name the file it is in — the override nobody typed —
// the same as when the files were listed by hand: each file is checked on its
// own before the merge. The road, not just the message, is what this holds.
func TestAFailureNamesTheOverrideFileWhenItWasFoundNotPassed(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("compose.yaml", "services:\n  app:\n    image: app\n")
	// The problem is in the override, which nobody named on the command line.
	write("compose.override.yaml", "services:\n  app:\n    user: someone\nvolumes:\n  data: \"\"\n")

	t.Chdir(dir)
	out, err := run(t, "config")
	if err == nil {
		t.Fatalf("the pair loaded:\n%s", out)
	}
	got := err.Error() + out
	if !strings.Contains(got, "compose.override.yaml") || !strings.Contains(got, "line 5") {
		t.Errorf("the failure should name compose.override.yaml and its own line 5:\n%s", got)
	}
	if strings.Contains(got, "merged document") {
		t.Errorf("the mistake is in one file; nothing about a merged document belongs here:\n%s", got)
	}
}

// The check that runs after the suite, checked here: a ratchet that reports
// "none" is indistinguishable from one that cannot look, and this package would
// read the first as proof it leaks nothing. So it is asked about a process that
// is definitely there, through the same function the suite uses.
func TestTheLeakCheckCanSeeARunningProcess(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		// Not a skip: skipping is how the whole mechanism goes quiet and the
		// package still prints ok. If this machine cannot run the check, the gate
		// should say so where the gate is read.
		t.Fatal("pgrep is not on PATH, so the check that no process outlives this suite cannot look at all")
	}
	// A binary with a name nothing else on this machine is running, in a
	// directory whose name is also an expression: pgrep -f takes a pattern, and
	// an unquoted "(" here matches nothing — which arrives in the same shape as
	// "nothing is running", the answer this check exists to distinguish.
	dir := filepath.Join(t.TempDir(), "probe(1)")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "opossum-leak-probe")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 60 &\nwait\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := startProbe(t, bin)

	found, _ := leakedProcesses(bin)
	if len(found) == 0 {
		t.Fatalf("a process running %s is right there and the check did not see it", bin)
	}
	// …and it goes quiet once nothing is running it, so the suite's "none" means
	// none rather than "never looked".
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if _, err := cmd.Process.Wait(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if now, _ := leakedProcesses(bin); len(now) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	still, _ := leakedProcesses(bin)
	t.Errorf("nothing is running %s any more, but the check still reports %v", bin, still)
}

// Nothing in this package writes to the state directory of whoever ran the
// tests. TestMain points XDG_STATE_HOME at a directory of its own; this says so
// out loud, because the failure it prevents is silent — a supervisor.log
// appearing under someone's home with a panic in it, which is what happened.
func TestTheSuiteDoesNotWriteToTheRealStateDirectory(t *testing.T) {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		t.Fatal("XDG_STATE_HOME is unset, so a supervisor started by any test here lands in the home directory of whoever ran it")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory to compare against")
	}
	// The whole home directory, not just ~/.local/state: pointing it anywhere
	// under there leaves files on the machine of whoever ran the tests, and the
	// name of this test would still read as if that were covered.
	if rel, rerr := filepath.Rel(home, state); rerr == nil && !strings.HasPrefix(rel, "..") {
		t.Errorf("XDG_STATE_HOME is %s, inside the home directory %s", state, home)
	}
}

// startProbe runs a script that stays up, and takes the whole process group down
// again afterwards.
//
// The script keeps the shell in place rather than exec'ing: pgrep -f matches
// command lines, and exec'ing replaces the one holding the path this check
// searches for. It also backgrounds the sleep on purpose, so the shell always
// has a child — killing the shell alone then leaves that child running for a
// minute, which is the thing this whole file is about, and which is what makes
// the group kill below something a test can actually check.
func startProbe(t *testing.T, bin string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Wait for the shell to fork the sleep. Without this the kill lands first —
	// measured at a few milliseconds, against the ~200ms the fork takes — and the
	// child this whole helper is about never exists, so nothing below is testing
	// anything.
	deadline := time.Now().Add(10 * time.Second)
	for {
		// Reported from what the loop saw, not from a fresh look: the group moves,
		// and a message that re-asks can describe a moment the decision was not
		// made in.
		members := groupMembers(t, cmd.Process.Pid)
		if len(members) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the probe never started its child; the group is %v", members)
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
		// The group, not the binary: the child's command line does not carry the
		// path, so pgrep -f would report it gone while it is still running — the
		// same blind spot the suite's own ratchet has.
		until := time.Now().Add(5 * time.Second)
		var left []int
		for time.Now().Before(until) {
			if left = groupMembers(t, cmd.Process.Pid); len(left) == 0 {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Errorf("something the probe started outlived the test: %v", left)
	})
	return cmd
}

// groupMembers is the pids still in a process group. Reaped pids are gone from
// it, so an empty answer means the group is finished.
func groupMembers(t *testing.T, pgid int) []int {
	t.Helper()
	out, err := exec.Command("pgrep", "-g", strconv.Itoa(pgid)).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return nil
		}
		t.Fatalf("pgrep could not list process group %d: %v", pgid, err)
	}
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		if pid, cerr := strconv.Atoi(f); cerr == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// The command the report offers has to be one a shell will take. Printing a
// []int gives `kill [321 654]`: bash refuses the argument, zsh tries to glob it.
// A report nobody can act on is the same as no report, and this one is only read
// when something has already gone wrong.
func TestTheLeakReportOffersACommandAShellWillTake(t *testing.T) {
	// Held whole. Its sibling report is pinned this way because a list of things
	// the sentence must not say only ever catches the wordings someone already
	// thought of — and this one was written with such a list, which let a version
	// through that said the opposite of what it meant. Both inputs are chosen
	// here, so nothing has to be normalised away first.
	for _, tc := range []struct {
		pids []int
		want string
	}{
		{[]int{321}, "these processes outlived the tests that started them: 321\n" +
			"they are running /tmp/x/opossum, which is about to be removed — `kill 321` ends them\n"},
		{[]int{321, 654}, "these processes outlived the tests that started them: 321 654\n" +
			"they are running /tmp/x/opossum, which is about to be removed — `kill 321 654` ends them\n"},
		// Three, because two is where a list and a pair look the same: a version
		// that printed the slice for anything longer would read correctly at two.
		{[]int{321, 654, 987}, "these processes outlived the tests that started them: 321 654 987\n" +
			"they are running /tmp/x/opossum, which is about to be removed — `kill 321 654 987` ends them\n"},
	} {
		if got := leakReport("/tmp/x/opossum", tc.pids); got != tc.want {
			t.Errorf("with %d pids the report is not what this file says it should be\n got: %q\nwant: %q", len(tc.pids), got, tc.want)
		}
	}
	// Run it, with a shell, against a process that is really there.
	bin := filepath.Join(t.TempDir(), "opossum-kill-probe")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 60 &\nwait\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := startProbe(t, bin)

	line := leakReport(bin, []int{cmd.Process.Pid})
	_, after, ok := strings.Cut(line, "`")
	if !ok {
		t.Fatalf("no command in the report to run:\n%s", line)
	}
	killCmd, _, ok := strings.Cut(after, "`")
	if !ok {
		t.Fatalf("no command in the report to run:\n%s", line)
	}
	if out, err := exec.Command("sh", "-c", killCmd).CombinedOutput(); err != nil {
		t.Fatalf("the command the report offers does not run: %v\n%s\n%s", err, killCmd, out)
	}
	// Accepted by the shell is not the same as it did anything: `true` is also
	// accepted. The process has to have died of the signal it was sent — and
	// within a few seconds, because waiting on the probe's own minute would turn
	// a broken report into a test that looks slow rather than wrong.
	type done struct {
		st  *os.ProcessState
		err error
	}
	waited := make(chan done, 1)
	go func() {
		st, err := cmd.Process.Wait()
		waited <- done{st, err}
	}()
	var st *os.ProcessState
	select {
	case d := <-waited:
		if d.err != nil {
			t.Fatal(d.err)
		}
		st = d.st
	case <-time.After(10 * time.Second):
		t.Fatalf("the command the report offers ran, and %s is still going: %s", bin, killCmd)
	}
	// Not guarded for other platforms: this file already calls syscall.Kill
	// unconditionally, and a skip here would turn "the command ended nothing"
	// into silence — the one shape this whole check exists to avoid.
	ws := st.Sys().(syscall.WaitStatus)
	if !ws.Signaled() {
		t.Errorf("the process was still alive to exit on its own, so the command ended nothing: %v", st)
	}
}

// The three states pgrep can leave this in, driven from PATH: it is not there,
// it ran and could not look, and it ran and found nothing. The first two used to
// arrive in the shape of the third, which is the one answer this must never
// invent — the suite reads it as proof it leaks nothing. The third is here
// because it is the state every clean machine is in, and a version that read it
// as a failure would skip the comparison and print ok having checked nothing.
// What this does not reach:
//
//   - Half of each look's answer. The look by path is asked for its pids and its
//     `looked` is thrown away; the look for supervisors is asked the opposite
//     way round. Measured: a by-path look that prints the note and returns
//     looked=true survives here. The dangerous shape of that — pgrep present and
//     failing — is caught next door by
//     TestARunThatCouldNotLookDoesNotPassForGreen/the_look_for_the_binary_this_suite_built
//     (measured); the shape that survives everything is "no pgrep on the machine
//     at all". There the run still goes red, because the other two looks return
//     false and uncheckedReport fires on any of the three — measured on a PATH
//     with every tool but pgrep: the report names two looks instead of three and
//     the run still fails.
//   - The order the two looks come in. It is set by the two call sites in the
//     body below, not by the code under test, and TestMain calls them in a
//     different order again (supervisors, path, supervisors). If these two
//     lines are ever swapped, the values stay right and the names in the
//     message go wrong.
//   - The third look. TestMain takes the supervisor snapshot twice, before and
//     after; this drives two looks, not three. The third shares its code path
//     with the second.
func TestPgrepNotAnsweringIsNotTheSameAsNoLeaks(t *testing.T) {
	for _, tc := range []struct {
		name, script, want string
		looked             bool
	}{
		// Held whole, not by a phrase it must contain: the sentence's job is to
		// not be read as "and there are none", and a check for a substring lets
		// exactly that be appended to it.
		{name: "missing", want: "no pgrep here, so nothing checked whether any process outlived the tests"},
		{name: "cannot look", script: "#!/bin/sh\nexit 2\n",
			want: "pgrep could not look for processes outliving the tests: exit status 2"},
		// The third state, and the one every clean machine is in: pgrep ran and
		// found nothing. It has to read as an answer — a machine with no
		// supervisor on it is where this whole check normally runs, and if that
		// reads as "could not look", the comparison TestMain makes is skipped and
		// the suite goes green having checked nothing.
		{name: "found nothing", script: "#!/bin/sh\nexit 1\n", looked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.script != "" {
				if err := os.WriteFile(filepath.Join(dir, "pgrep"), []byte(tc.script), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", dir)
			var said strings.Builder
			leakSaid = &said
			defer func() { leakSaid = os.Stderr }()

			if got, _ := leakedProcesses("/tmp/whatever/opossum"); len(got) != 0 {
				t.Errorf("nothing was found, so nothing should be reported as leaked: %v", got)
			}
			// Taken here, before the second look, so the two are judged apart.
			// Read at the end instead, the two notes arrive as one string, and
			// the total is the same whichever look said them: a version where one
			// speaks twice while the other stays quiet passed. Measured, on the
			// commit before this line existed, in both directions (2/0 and 0/2).
			//
			// A silent look is not cosmetic: uncheckedReport says "the note above
			// says why", and above would be empty.
			//
			// Only that imbalance is new, and it is new at both places a note is
			// written — "no pgrep here" and "pgrep could not look" — so four
			// mutations in all. "One look went quiet entirely" was already
			// caught, because with a fixed total of two notes, losing one changes
			// the total too.
			byPath := said.String()
			said.Reset()
			// And it says so in the value, not only in the note: the difference
			// between the two snapshots is only meaningful if both were taken.
			// An empty "before" that means "could not see" would make every
			// supervisor on this machine look new; an answer misread as a failure
			// makes TestMain skip the comparison and print ok having checked
			// nothing.
			if _, looked := runningSupervisors(); looked != tc.looked {
				t.Errorf("this is %q; the look should read as looked=%v, got %v", tc.name, tc.looked, looked)
			}
			// Keyed on whether the look succeeded, not on whether this row has
			// something to look for in the note: those come apart the moment a
			// row is added for a look that failed quietly.
			looks := []struct{ which, got string }{
				{"the look by path", byPath},
				{"the look for supervisors", said.String()},
			}
			if tc.looked {
				// Split by look for the message, not for the reach: "nothing was
				// said in total" and "nothing was said by either" are the same
				// statement, and this half was already caught before the split.
				// What it buys is that a red says which look spoke.
				for _, look := range looks {
					if look.got != "" {
						t.Errorf("this is %q — an answer, not a failure — so %s has nothing to say: %q",
							tc.name, look.which, look.got)
					}
				}
				return
			}
			// Word for word, and once each: what was said before the second
			// look, and what was said during it.
			want := "\n" + tc.want + "\n"
			for _, look := range looks {
				if look.got != want {
					t.Errorf("this is %q, and %s has to say why it could not look\n got: %q\nwant: %q",
						tc.name, look.which, look.got, want)
				}
			}
		})
	}
}

// What the suite reports is the difference between two lists, so the difference
// is worth its own test: reading it wrong in the quiet direction (nothing is
// ever new) is the failure that leaves a process running and prints ok.
func TestOnlySupervisorsThatWereNotAlreadyThereAreReported(t *testing.T) {
	for _, tc := range []struct {
		name        string
		before, now []int
		want        []int
	}{
		{"nothing anywhere", nil, nil, nil},
		{"one already running, still running", []int{29867}, []int{29867}, nil},
		{"one already running, one started", []int{29867}, []int{29867, 40001}, []int{40001}},
		{"none before, two started", nil, []int{40001, 40002}, []int{40001, 40002}},
		{"one already running, and it stopped", []int{29867}, nil, nil},
		{"started and stopped again", []int{29867}, []int{29867}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := newSupervisors(tc.before, tc.now)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// …and that the list it works on is really every supervisor, including one this
// suite did not start. The machine this runs on has had one from a test days
// ago; a search that only found this suite's own would report the same "none"
// on a machine with nothing on it, and there would be no way to tell.
func TestTheSupervisorSearchLooksBeyondThisSuite(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "opossum-supervise-probe")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 60 &\nwait\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Started with the word the search looks for, from a path this suite never
	// built — which is what a copy a test made somewhere of its own looks like.
	cmd := exec.Command(bin, "__supervise", "-p", "probe")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		found, looked := runningSupervisors()
		if !looked {
			t.Fatal("pgrep could not look, so this test would pass by finding nothing")
		}
		if slices.Contains(found, cmd.Process.Pid) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a supervisor at %s is running as %d and the search did not find it: %v",
				bin, cmd.Process.Pid, found)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// What the suite says about supervisors that appeared while it ran, word for
// word. The wording is the whole of this report's correctness: the check cannot
// tell a test's supervisor from one someone else started, so a sentence that
// says it can — or a command attached to the wrong half of the condition — tells
// a reader to kill a process they are using.
//
// Held as the whole string rather than as a list of things it must not say. A
// list only catches the wordings someone already thought of: with one in place,
// a version that moved `kill` from the tests' case to the other one passed, and
// so did one that opened with a flat claim that the tests started them.
func TestTheNewSupervisorReportIsWordForWordWhatWeMeanToSay(t *testing.T) {
	want := "these supervisors were not running when the tests started, and are now: 321 654\n" +
		"if the tests started them, `kill 321 654` ends them; if something else on this machine did, " +
		"leave them be and run the tests again — they will be there before the next run and it will not mention them\n"
	if got := newSupervisorReport([]int{321, 654}); got != want {
		t.Errorf("the report is not what this file says it should be\n got: %q\nwant: %q", got, want)
	}
	// One pid reads the same way; the sentence is written for either count.
	wantOne := "these supervisors were not running when the tests started, and are now: 321\n" +
		"if the tests started them, `kill 321` ends them; if something else on this machine did, " +
		"leave them be and run the tests again — they will be there before the next run and it will not mention them\n"
	if got := newSupervisorReport([]int{321}); got != wantOne {
		t.Errorf("with one pid the report is not what this file says it should be\n got: %q\nwant: %q", got, wantOne)
	}
}

// One process, named once. The report by path speaks with more certainty, so it
// keeps the pids it found and the weaker report drops them; a reader given the
// same pid twice, in two voices, has to work out whether it is one problem.
func TestAProcessIsNamedByOneReportOrTheOther(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pids    []int
		already map[int]bool
		want    []int
	}{
		{"nothing already named", []int{1, 2}, map[int]bool{}, []int{1, 2}},
		{"one already named", []int{1, 2}, map[int]bool{1: true}, []int{2}},
		{"all already named", []int{1, 2}, map[int]bool{1: true, 2: true}, nil},
		{"named but not new", nil, map[int]bool{1: true}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := unreported(tc.pids, tc.already)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// What the suite says when a look did not happen, word for word. The sentence
// has one job — to stop a run that checked nothing from reading as a run that
// found nothing — so it is held whole rather than by the words it must avoid.
func TestTheUncheckedReportIsWordForWordWhatWeMeanToSay(t *testing.T) {
	const tail = "the note above says why. a run that could not look is not a run that found nothing.\n"
	for _, tc := range []struct {
		name                string
		before, path, after bool
		want                string
	}{
		{name: "all three looked", before: true, path: true, after: true, want: ""},
		{
			name: "only the one before failed", path: true, after: true,
			want: "this run did not check whether the tests left anything running: the look before the tests did not happen.\n" + tail,
		},
		{
			name: "only the path search failed", before: true, after: true,
			want: "this run did not check whether the tests left anything running: the look for processes running the binary this suite built did not happen.\n" + tail,
		},
		{
			name: "only the one after failed", before: true, path: true,
			want: "this run did not check whether the tests left anything running: the look after the tests did not happen.\n" + tail,
		},
		{
			name: "none of them looked",
			want: "this run did not check whether the tests left anything running: the look before the tests and the look for processes running the binary this suite built and the look after the tests did not happen.\n" + tail,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := uncheckedReport(tc.before, tc.path, tc.after); got != tc.want {
				t.Errorf("the report is not what this file says it should be\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// The wiring, not just the sentence: a run that could not look has to end with a
// non-zero status, or the whole thing is a note in a green run that `go test
// ./...` throws away. Checked by running this package again in a child process
// with a pgrep that refuses, and selecting no tests at all — TestMain runs
// either way, which is the property that makes this cheap (about a second).
//
// Two of the three looks are driven here. The third (the one after the tests)
// shares its code path with the one before it, and there is no way to fail only
// that one from outside without also failing the tests that use pgrep.
func TestARunThatCouldNotLookDoesNotPassForGreen(t *testing.T) {
	real, err := exec.LookPath("pgrep")
	if err != nil {
		t.Fatal("pgrep is not on PATH, so a child that refuses to look would be indistinguishable from this machine")
	}
	// Quoted into the script below: the path is not ours to assume is one word.
	quoted := "'" + strings.ReplaceAll(real, "'", `'\''`) + "'"
	for _, tc := range []struct {
		name, script, want string
	}{
		{
			// Fails the first call only, which is the look taken before any test
			// runs; every later call is the real thing, so the tests themselves
			// still pass and the run would otherwise be green.
			name: "the look before the tests",
			// The sweep for orphaned probes asks first and is let through by
			// name; the count is of the looks this is about, so adding another
			// call ahead of them does not move which one fails.
			script: "#!/bin/sh\ncase \"$*\" in *" + leakProbeProjectPrefix + "*) exec " + quoted + " \"$@\" ;; esac\n" +
				"C=\"$0.count\"\nn=$(cat \"$C\" 2>/dev/null || echo 0)\necho $((n+1)) > \"$C\"\n" +
				"[ \"$n\" -eq 0 ] && exit 2\nexec " + quoted + " \"$@\"\n",
			want: "the look before the tests did not happen",
		},
		{
			// Fails only the search for the binary this suite built.
			name:   "the look for the binary this suite built",
			script: "#!/bin/sh\ncase \"$*\" in *opossum-cmd-test*) exit 2 ;; esac\nexec " + quoted + " \"$@\"\n",
			want:   "the look for processes running the binary this suite built did not happen",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "pgrep"), []byte(tc.script), 0o755); err != nil {
				t.Fatal(err)
			}
			// No test is selected, so nothing here runs twice; TestMain still does.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "go", "test", "-count=1", "-run", "TestThereIsNoSuchTestAsThis", ".")
			cmd.Dir = packageDir(t)
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Errorf("this run checked nothing and still passed:\n%s", out)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("the run should say which look did not happen — %q is missing from:\n%s", tc.want, out)
			}
			// Red is not the same as red for this reason. A child that failed
			// because something really did outlive it would satisfy both checks
			// above while saying nothing about the look that did not happen.
			for _, other := range []string{"outlived the tests that started them", "were not running when the tests started"} {
				if strings.Contains(string(out), other) {
					t.Errorf("this child was meant to fail for not looking, and it also %q:\n%s", other, out)
				}
			}
		})
	}
}

// packageDir is where this package's sources are, so a child `go test .` finds
// them. The tests chdir a lot, so it is read once from the caller of TestMain
// rather than from the working directory.
func packageDir(t *testing.T) string {
	t.Helper()
	if testPackageDir == "" {
		t.Fatal("the package directory was not recorded")
	}
	return testPackageDir
}

var testPackageDir string

// leakProbeEnv turns one test in this package into a deliberate leak, so a child
// process can be made to fail the way a forgetful test would. It is only ever
// set by the test below, on the child.
const leakProbeEnv = "OPOSSUM_LEAK_PROBE"

// leakProbePidFileEnv is where the probe writes what it left running. A file and
// not stdout: `go test .` keeps the output of a package that failed and throws
// away the rest, and the parent breaks the report on purpose — so the runs that
// leak hardest are exactly the ones whose output never arrives.
const leakProbePidFileEnv = "OPOSSUM_LEAK_PROBE_PIDFILE"

// sweepOrphanedProbes ends leaked probe supervisors whose run is over. A run
// killed by a timeout or a Ctrl-C never reaches its own cleanup, and there is
// nothing a dead process can do about that — so the next run does it, which is
// the only place left that knows these are test leavings.
func sweepOrphanedProbes() {
	out, err := exec.Command("pgrep", "-fl", regexp.QuoteMeta("__supervise -p "+leakProbeProjectPrefix)).Output()
	if err != nil {
		return // exit 1 is "none", and anything else is not ours to guess about
	}
	for _, line := range strings.Split(string(out), "\n") {
		pid, rest, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		_, after, ok := strings.Cut(rest, "-p "+leakProbeProjectPrefix)
		if !ok {
			continue
		}
		// Read through suitedir: 0 is this whole process group, -1 is everything
		// reachable, and a number too large for a pid_t arrives as some other
		// process entirely. Those bounds used to be written out here and not
		// there, which is exactly the shape that drifts.
		ownerPid, ok := suitedir.PidLeading(after)
		if !ok {
			continue
		}
		// Alive, or alive and not ours to signal: only "no such process" says the
		// run that asked for this leak is over. Anything else is somebody there.
		// (A run that has exited but not been reaped answers as alive, so its
		// orphan waits for the run after this one. Late, and never wrong.)
		//
		// Asked through suitedir because the temp-directory sweep asks the same
		// question about the same kind of leftover, and two answers to one
		// question drift: these had already parted company over errors.Is before
		// they were joined.
		if suitedir.Alive(ownerPid) {
			continue
		}
		if p, perr := strconv.Atoi(pid); perr == nil {
			_ = syscall.Kill(p, syscall.SIGTERM)
		}
	}
}

// leakProbeProjectPrefix is how a probe's project is recognised as one. What
// follows it is the pid of the run that asked for the leak.
const leakProbeProjectPrefix = "leakprobe-"

// leakProbeProjectEnv names the project the probe leaves supervised. The parent
// makes it unique to itself so that sweeping by name afterwards cannot reach a
// supervisor belonging to another run — or to somebody working on this machine.
const leakProbeProjectEnv = "OPOSSUM_LEAK_PROBE_PROJECT"

// TestZZLeakProbeStartsASupervisorAndWalksAway is skipped unless a parent asks
// for it. When asked, it starts a supervisor and does not stop it — which is the
// thing TestMain is supposed to catch, and the thing no test in this package
// could check until one of them was willing to do it on purpose.
func TestZZLeakProbeStartsASupervisorAndWalksAway(t *testing.T) {
	kind := os.Getenv(leakProbeEnv)
	if kind == "" {
		t.Skip("only a parent asking for a leak runs this")
	}
	project := os.Getenv(leakProbeProjectEnv)
	if project == "" {
		t.Fatal("the parent has to name the project, so its own sweep cannot reach anyone else's")
	}
	fakeShim(t)
	self := opossumBin
	if kind == "elsewhere" {
		// A copy the test made somewhere of its own: the search by path never
		// looks here, so only the before/after difference can see it.
		self = filepath.Join(t.TempDir(), "opossum")
		src, err := os.ReadFile(opossumBin)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(self, src, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", self)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: "+project+"\nservices:\n  web:\n    image: web\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if _, err := run(t, "up", "--no-build"); err != nil {
		t.Fatalf("up: %v", err)
	}
	var pid int
	waitFor(t, "the supervisor to claim the project", func() bool {
		pid = supervisorPID(t, state, project)
		return pid != 0
	})
	// Written where the parent can always read it, however this run ends.
	if f := os.Getenv(leakProbePidFileEnv); f != "" {
		if err := os.WriteFile(f, []byte(strconv.Itoa(pid)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// And then nothing: no down, no cleanup. That is the point.
}

// A supervisor a test starts and does not stop has to fail the run, and fail it
// with the report that fits how it was found. Checked from a child process
// because the thing being checked is TestMain's own wiring, which nothing inside
// a test can reach — the two reports and the choice between them are written
// there, and until now deleting either block left this package green.
func TestALeakedSupervisorFailsTheRunItLeaked(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Fatal("pgrep is not on PATH, so a child could leak and still pass")
	}
	const (
		byPath = "outlived the tests that started them"
		byDiff = "were not running when the tests started"
	)
	for _, tc := range []struct {
		kind, want, absent string
	}{
		// Started from the binary this suite built: the search by path finds it,
		// and the difference must not name it a second time.
		{kind: "built", want: byPath, absent: byDiff},
		// Started from a copy somewhere else: only the difference can see it.
		{kind: "elsewhere", want: byDiff, absent: byPath},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "go", "test", "-count=1",
				"-run", "TestZZLeakProbeStartsASupervisorAndWalksAway", ".")
			cmd.Dir = packageDir(t)
			pidFile := filepath.Join(t.TempDir(), "pid")
			project := fmt.Sprintf("%s%d-%s", leakProbeProjectPrefix, os.Getpid(), tc.kind)
			cmd.Env = append(os.Environ(),
				leakProbeEnv+"="+tc.kind,
				leakProbePidFileEnv+"="+pidFile,
				leakProbeProjectEnv+"="+project)
			// The deadline has to reach the test binary `go` starts, not just
			// `go`: WaitDelay does not, having only the direct child to work
			// with. A group of its own does, and Cancel is where the group is
			// ended — measured by hanging the child and watching for what is
			// left behind.
			// (A side effect: the child is no longer in this terminal's foreground
			// group, so a Ctrl-C here does not reach it. It ends on its own in a
			// second or two, and anything it started is swept by the next run.)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Cancel = func() error {
				if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); errors.Is(err, syscall.ESRCH) {
					return os.ErrProcessDone // os/exec reads this as "it had already finished"
				} else if err != nil {
					return err
				}
				return nil
			}
			cmd.WaitDelay = 5 * time.Second
			t.Cleanup(func() { killProbe(t, pidFile, project) })
			out, err := cmd.CombinedOutput()

			if err == nil {
				t.Errorf("a supervisor was left running and the run passed:\n%s", out)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("the leak should be reported as %q, missing from:\n%s", tc.want, out)
			}
			if strings.Contains(string(out), tc.absent) {
				t.Errorf("one process, one report: %q should not also appear in:\n%s", tc.absent, out)
			}
			// The pid in the report has to be the one the probe left, not some
			// other supervisor that happened to supply the expected words.
			// Nothing the child started may outlive it. Without this, the group
			// kill above is unguarded — and the thing it replaced (WaitDelay) sat
			// here for a round doing nothing while its comment said otherwise.
			if kerr := syscall.Kill(-cmd.Process.Pid, 0); !errors.Is(kerr, syscall.ESRCH) {
				t.Errorf("the child's process group still has somebody in it: %v", kerr)
			}
			raw, rerr := os.ReadFile(pidFile)
			if rerr != nil {
				t.Errorf("the probe should have left a pid behind; without one the check below is skipped exactly when it matters: %v\n%s", rerr, out)
			} else if !strings.Contains(string(out), strings.TrimSpace(string(raw))) {
				t.Errorf("the report should name the process the probe left (%s):\n%s", raw, out)
			}
		})
	}
}

// killProbe ends what the probe left behind, reading the pid from a file rather
// than from the run's output: a run that broke the report on purpose still
// started a real supervisor, and the runs that break it hardest are the ones
// that pass — whose output `go test` discards.
func killProbe(t *testing.T, pidFile, project string) {
	t.Helper()
	if raw, err := os.ReadFile(pidFile); err == nil {
		if pid, cerr := strconv.Atoi(strings.TrimSpace(string(raw))); cerr == nil {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		} else {
			t.Errorf("the probe wrote %q where a pid should be", raw)
		}
	}
	// And then by name, because the pid file only exists when the probe got far
	// enough to write it — and the runs that leak are the ones where it did not.
	// The name carries this process's pid, so this reaches nothing that is not
	// ours. Waited out rather than fired and forgotten: the suite's own check
	// runs moments later and would report a supervisor that is on its way out.
	deadline := time.Now().Add(10 * time.Second)
	for {
		left, looked := leakedProcesses("__supervise -p " + project)
		if !looked {
			t.Errorf("could not look for the probe's supervisors, so this test cannot say it left none")
			return
		}
		if len(left) == 0 {
			return
		}
		for _, pid := range left {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
		if time.Now().After(deadline) {
			t.Errorf("the probe's supervisors are still running: %v", left)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// doctor reports the networks nothing is running on, all the way through the
// real command against the shim.
//
// The check was written and measured a level down, where the classification
// lives. What only this level can say is whether the command reaches it at all:
// the two listings it needs are ones the shim did not answer when the check was
// added, and a shim that cannot answer turns every run into "unavailable" —
// which is a passing check, and reads like a clean machine to anyone skimming.
func TestDoctorReportsTheNetworksNothingIsRunningOn(t *testing.T) {
	fakeShim(t)
	t.Setenv("NETWORK_LS", `[{"configuration":{"name":"gone-net","labels":{}}},`+
		`{"configuration":{"name":"sleeping-net","labels":{}}},`+
		`{"configuration":{"name":"default","labels":{"com.apple.container.resource.role":"builtin"}}}]`)
	// One stopped container, on one of the two. Its status carries no
	// attachments — that is what a stopped container looks like — so a run that
	// read the running side would call both networks empty.
	t.Setenv("CONTAINER_LS", `[{"configuration":{"id":"db.sleeping.opossum","networks":[{"network":"sleeping-net"}]},`+
		`"status":{"networks":[],"state":"stopped"}}]`)

	root := newRootCmd()
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"doctor", "--format", "json"})
	_ = root.Execute() // the environment may be unhealthy for other reasons; this is about one check

	var rep struct {
		Checks []struct{ ID, Status, Detail, Fix string } `json:"checks"`
	}
	if err := json.Unmarshal([]byte(out.String()), &rep); err != nil {
		t.Fatalf("doctor --format json: %v\n%s", err, out.String())
	}
	var got struct{ ID, Status, Detail, Fix string }
	for _, c := range rep.Checks {
		if c.ID == "leftover-networks" {
			got = c
		}
	}
	if got.ID == "" {
		t.Fatalf("no leftover-networks check in the report:\n%s", out.String())
	}
	if strings.Contains(got.Detail, "unavailable") {
		t.Fatalf("the shim could not answer, so nothing was measured: %q", got.Detail)
	}
	if got.Status != "warn" {
		t.Errorf("one network has nothing on it at all; status = %q\n%s", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, "1 with no containers at all (gone-net)") {
		t.Errorf("detail = %q", got.Detail)
	}
	if !strings.Contains(got.Detail, "1 holding only stopped containers (sleeping-net)") {
		t.Errorf("the stopped container still holds its network; detail = %q", got.Detail)
	}
	if strings.Contains(got.Fix, "sleeping-net") {
		t.Errorf("the offer names only what nothing is on: %q", got.Fix)
	}
}

// The shim honours the flags that change what a listing contains.
//
// Every check in this file that says "a caller who dropped `-a` would be caught"
// rests on the shim noticing. Nothing was checking the shim: loosening its flag
// reading to always-true takes those guards away and leaves every test green,
// which is the quiet kind of green this whole change is about. The shell shim
// next door got a test of its own; this is the one the tests actually drive.
func TestTheShimHonoursTheListingFlags(t *testing.T) {
	fakeShim(t)
	const doc = `[{"configuration":{"id":"cache.proj.opossum","networks":[{"network":"proj-net"}]},` +
		`"status":{"networks":[],"state":"stopped"}},` +
		`{"configuration":{"id":"web.live.opossum","networks":[{"network":"live-net"}]},` +
		`"status":{"networks":[{"network":"live-net"}],"state":"running"}}]`
	t.Setenv("CONTAINER_LS", doc)

	run := func(t *testing.T, args ...string) string {
		t.Helper()
		out, err := exec.Command(fakeShimBin, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	var got []struct {
		Configuration struct{ ID string } `json:"configuration"`
	}
	decode := func(t *testing.T, out string) []string {
		t.Helper()
		got = got[:0]
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("not a listing: %v\n%s", err, out)
		}
		var names []string
		for _, g := range got {
			names = append(names, g.Configuration.ID)
		}
		return names
	}

	// With -a, both. Without it, only what is running — which is what the real
	// CLI does, and what makes a caller who forgot the flag wrong here too.
	if names := decode(t, run(t, "ls", "-a", "--format", "json")); len(names) != 2 {
		t.Errorf("ls -a lists the stopped one too, got %v", names)
	}
	if names := decode(t, run(t, "ls", "--format", "json")); len(names) != 1 || names[0] != "web.live.opossum" {
		t.Errorf("without -a, only the running one, got %v", names)
	}
	// The same request the other way round is the same request.
	if names := decode(t, run(t, "ls", "--format", "json", "-a")); len(names) != 2 {
		t.Errorf("the flag is a flag, not a position, got %v", names)
	}
	// Without --format json, not JSON — a caller who forgot it must fail here
	// the way it would on a machine.
	for _, args := range [][]string{{"ls", "-a"}, {"network", "ls"}} {
		if out := run(t, args...); strings.HasPrefix(out, "[") {
			t.Errorf("%v answered JSON with no --format json:\n%s", args, out)
		}
	}
	// And the inspect document carries both lists called networks, holding
	// different names, so that reading the wrong one shows.
	var insp []struct {
		Status struct {
			Networks []struct{ Network string } `json:"networks"`
		} `json:"status"`
		Configuration struct {
			Networks []struct{ Network string } `json:"networks"`
		} `json:"configuration"`
	}
	out := run(t, "inspect", "web")
	if err := json.Unmarshal([]byte(out), &insp); err != nil {
		t.Fatalf("inspect: %v\n%s", err, out)
	}
	if len(insp) != 1 || len(insp[0].Status.Networks) != 1 || len(insp[0].Configuration.Networks) != 1 {
		t.Fatalf("inspect should carry both lists: %s", out)
	}
	if a, b := insp[0].Status.Networks[0].Network, insp[0].Configuration.Networks[0].Network; a == b {
		t.Errorf("both lists hold %q, so this fixture cannot show which one a parser read", a)
	}
}

// mustWrite writes body to path, next to a compose file written by
// writeCompose, so a case can bring its own env_file.
func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A service nobody is running must not break the project. docker resolves
// env_file when it needs a service's rendered environment, so a compose file
// whose profile-gated service points at an env file that does not parse still
// loads, still lists, and still starts everything else — measured on Docker
// Compose v5.4.0, where `config --services`, `ps` and `logs` all succeed on
// exactly this shape and only a full `config` with the profile active fails.
// That run is the gated pair of tables in testdata/docker-compose-env-file.md,
// with the commands beside it. `up` is asked here too, on the same reasoning,
// though docker's was not measured for it.
//
// opossum used to fail all of them at load, because load reads every service's
// env_file and load is a layer below profiles (#413).
func TestABrokenEnvFileOnAGatedServiceDoesNotBreakTheProject(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web\n"+
		"  extra:\n    image: dbg\n    profiles: [debug]\n    env_file: [g.env]\n")
	// The file exists and fails to expand: a missing file and an unresolvable
	// one are the same fact to a caller, and the harder one to get right is the
	// one that parses far enough to try.
	mustWrite(t, filepath.Join(filepath.Dir(compose), "g.env"), "BOOM=${NOPE:?you must set NOPE}\n")

	for _, args := range [][]string{{"config"}, {"config", "--services"}, {"ps"}, {"logs"}, {"up"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, err := run(t, append([]string{"-f", compose}, args...)...)
			if err != nil {
				t.Fatalf("a gated service's env_file should not reach this command: %v\n%s", err, out)
			}
		})
	}

	// And the gated service is still gated: `config` mirrors what `up` starts,
	// so recording the failure instead of raising it must not be the thing that
	// lets an inactive service through.
	out, err := run(t, "-f", compose, "config")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if strings.Contains(out, "extra:") {
		t.Errorf("the gated service is still gated, got:\n%s", out)
	}
}

// The other side of the same line: once the profile is active the service is
// being rendered and started, so the failure it has been holding is due. `ps`
// stays green — it needs no environment, and docker's does not fail here either
// (measured on Docker Compose v5.4.0: `ps`, `logs` and `config --services` all
// succeed against an active service whose env_file does not expand — that run,
// and the missing-file shape beside it, are kept with their commands and
// answers in testdata/docker-compose-env-file.md, so a later reader can take
// them again rather than take this sentence's word for it).
func TestABrokenEnvFileReachesTheCommandsThatNeedTheEnvironment(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web\n"+
		"  extra:\n    image: dbg\n    profiles: [debug]\n    env_file: [g.env]\n")
	mustWrite(t, filepath.Join(filepath.Dir(compose), "g.env"), "BOOM=${NOPE:?you must set NOPE}\n")

	for _, tc := range []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "config", args: []string{"config", "--profile", "debug"}, wantErr: true},
		{name: "up", args: []string{"up", "--profile", "debug"}, wantErr: true},
		// `run` starts a container too, from its own path rather than through
		// `up`, so it needs asking separately: the two are the only places a
		// container is born, and a guard on one of them is a guard on half.
		{name: "run", args: []string{"run", "--no-deps", "extra", "echo", "hi"}, wantErr: true},
		// `run --audit` reports a run's outcome as an exit code, so it is the
		// one path where the reason can be spent without being said: the
		// report would print `exit -1` and name neither the file nor the
		// variable. It has to refuse before it measures anything.
		{name: "run --audit", args: []string{"run", "--audit", "--no-deps", "extra", "echo", "hi"}, wantErr: true},
		// `ps` and `logs` take no --profile, so the profile arrives the other
		// way it can. Both are named in the changelog entry for this change as
		// commands that no longer carry the failure, which is a claim about
		// them and so gets asked here.
		{name: "ps", args: []string{"ps"}, wantErr: false},
		{name: "logs", args: []string{"logs"}, wantErr: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("COMPOSE_PROFILES", "debug")
			out, err := run(t, append([]string{"-f", compose}, tc.args...)...)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("the service is being run now, so its env_file failure is due; got:\n%s", out)
				}
				if !strings.Contains(err.Error(), "NOPE") {
					t.Errorf("the error should name the variable that has no value, got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("this command needs no environment, so it should not carry the failure: %v\n%s", err, out)
			}
		})
	}
}

// Which service the failure belongs to, asked where a person reads it. The
// name is put on the error at load, and internal/compose holds it to that —
// but a project has more than one service, and the message a user gets is the
// one the CLI prints. Dropping the name leaves the library tests red and every
// command still green (measured), so the fact that `up` says whose env_file it
// is has been resting on the layer below.
//
// The broken service is neither first nor last in the file and its name is not
// the project's, so a message that named the wrong thing — the project, the
// first service, the file only — would read differently from one that names
// the service. `config` and `up` are both asked: they reach the environment by
// different roads (rendering, starting, and running one service), and the name
// has to survive all of them.
//
// What is asked is the error the command returns. The line a person reads is
// that error printed by runCLI, which quotes it and adds a prefix; nothing
// between here and there drops a word (#660).
func TestTheEnvFileFailureNamesWhichServiceItBelongsTo(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n"+
		"  alpha:\n    image: a\n"+
		"  beta:\n    image: b\n    env_file: [gone.env]\n"+
		"  gamma:\n    image: c\n")

	for _, args := range [][]string{
		{"config"},
		{"up"},
		// Where a container is born, there are two roads, and the neighbour
		// above holds both to the same line; a name that survived one of them
		// would be half an answer.
		{"run", "--no-deps", "beta", "echo", "hi"},
		{"run", "--audit", "--no-deps", "beta", "echo", "hi"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, err := run(t, append([]string{"-f", compose}, args...)...)
			if err == nil {
				t.Fatalf("beta's env_file is missing, so this has to fail; got:\n%s", out)
			}
			if !strings.Contains(err.Error(), "beta") {
				t.Errorf("the failure should say which service it belongs to (beta), got: %v.\n"+
					"  A project has many services and only one of them is broken; a "+
					"message that does not name it leaves the reader to find out which.",
					err)
			}
			for _, other := range []string{"alpha", "gamma"} {
				if strings.Contains(err.Error(), other) {
					t.Errorf("the failure names %s, which has no env_file: %v", other, err)
				}
			}
		})
	}
}

// A command that cannot happen leaves nothing behind it. Every road here does
// work before it needs the environment — removing orphan containers, starting
// the service's dependencies, creating the network, building images, deleting
// a stale container of the same name; `run --audit` also takes a workspace
// snapshot — and a refusal after that leaves what was started running. `up`
// rolls containers and the network back, but not an image it built, not a
// deleted orphan, and a peer it recreated on the way is torn down by that same
// rollback: it was running before the command and is gone after it.
//
// Asking only that the command fails would not see any of this: it fails
// wherever the guard sits. What is asked here is that the fake runtime was
// never told to start, create, build or delete anything. Each verb needs a
// case that can produce it — a `build:` service and `--build` for the build,
// `--remove-orphans` for the delete — or the entry reads for something the
// suite cannot reach (#413).
func TestACommandRefusesAnUnreadableEnvironmentBeforeStartingAnything(t *testing.T) {
	for _, args := range [][]string{
		{"run", "extra", "echo", "hi"},
		{"run", "--audit", "extra", "echo", "hi"},
		{"up"},
		// --build is what makes a build reachable at all: without it the
		// runtime answers that the image is there and nothing is built. The
		// verb list below reads for `build `, and a case that cannot produce
		// one would leave that entry checking nothing.
		{"up", "--build"},
		// --remove-orphans stops and deletes containers before any of this,
		// and a delete is not taken back by the rollback.
		{"up", "--remove-orphans"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			readLog := fakeShim(t)
			compose := writeCompose(t, "name: demo\nservices:\n  web:\n    build: .\n"+
				"  extra:\n    image: dbg\n    depends_on: [web]\n    env_file: [g.env]\n")
			dir := filepath.Dir(compose)
			mustWrite(t, filepath.Join(dir, "Dockerfile"), "FROM scratch\n")
			mustWrite(t, filepath.Join(dir, "g.env"), "BOOM=${NOPE:?you must set NOPE}\n")

			out, err := run(t, append([]string{"-f", compose}, args...)...)
			if err == nil {
				t.Fatalf("this service's environment cannot be read, got:\n%s", out)
			}
			if !strings.Contains(err.Error(), "NOPE") {
				t.Errorf("the error should name the variable that has no value, got: %v", err)
			}
			// `web` is what would have been started, the network is what would
			// have been created, `build` is what would have been paid for, and
			// an orphan is what would have been deleted. None of them should
			// have been.
			for _, cmd := range readLog() {
				for _, verb := range []string{"run ", "start ", "network create", "build ", "stop ", "delete "} {
					if strings.HasPrefix(cmd, verb) {
						t.Errorf("a command that was refused had already asked the runtime to %q", cmd)
					}
				}
			}
		})
	}
}

// One run, one voice about the Docker socket. `up --from-docker-compose`
// prints the note and then runs the up, whose warning says the same thing in
// the same words — a screen apart, it read as two findings. Only the exact
// repeat goes: where the warning is the only voice (a plain up, a note whose
// prose never printed) it still speaks. Since #599 the warning asks the
// note's own question, so the cases below that once exercised its wider read
// now pin the opposite: a mount that binds no Docker socket gets no voice.
func TestTheDockerSocketIsSpokenAboutOnce(t *testing.T) {
	// The sentence the note's Why and the warning share, and the entry list does
	// not: the headline also appears in the entry list above the prose, which is
	// a table of contents, not a second finding.
	const claim = "and it has no socket to share"
	bind := "services:\n  watcher:\n    image: app:1\n    volumes:\n      - \"./docker.sock:/var/run/docker.sock\"\n"
	for name, tc := range map[string]struct {
		compose string
		overlay string // pre-existing compose.opossum.yaml, "" = none
		args    []string
		want    int
	}{
		"note and warning would both speak": {
			compose: bind,
			args:    []string{"up", "--from-docker-compose", "--no-build", "--no-supervisor"},
			want:    1,
		},
		"a plain up has only the warning": {
			compose: bind,
			args:    []string{"up", "--no-build", "--no-supervisor"},
			want:    1,
		},
		// An anonymous volume mounts nothing from the host under docker either
		// (Docker Compose v5.4.0 canonicalizes it to `type: volume`, no host
		// source), so there is no divergence and no voice speaks — the note's
		// predicate never saw it, and since #599 the warning asks the same
		// question. This case used to expect 1 from the warning's wider read;
		// the sentence it printed ("mounts the Docker socket") was false here.
		"an anonymous volume is nobody's socket": {
			compose: "services:\n  watcher:\n    image: app:1\n    volumes:\n      - \"/var/run/docker.sock\"\n",
			args:    []string{"up", "--from-docker-compose", "--no-build", "--no-supervisor"},
			want:    0,
		},
		// The steady state of a --from-docker-compose project: the overlay is
		// already on disk, so the run prints the note's HEADLINE only ("found
		// more:") — and a headline is a table of contents, not a finding. The
		// warning has to keep speaking here; the first version of this change
		// recorded the note when it was planned rather than when it was shown,
		// and went quiet on exactly this path.
		"an overlay already on disk leaves the warning speaking": {
			compose: bind,
			overlay: "# Generated by `opossum up --from-docker-compose`.\nservices: {}\n",
			args:    []string{"up", "--from-docker-compose", "--no-build", "--no-supervisor"},
			want:    1,
		},
		// With -f the overlay is never merged, but the notes-only prose still
		// prints (a different reporting site from the no-f path) — and having
		// been shown, it still counts as spoken. This is the case that pins
		// that second site's wiring; without it, unhooking the -f site leaves
		// every other case green.
		"with -f the prose still counts as spoken": {
			compose: bind,
			args:    []string{"-f", "compose.yaml", "up", "--from-docker-compose", "--no-build", "--no-supervisor"},
			want:    1,
		},
		// Two services, one voice each. This case used to pair a bind with an
		// anonymous volume so the second voice was the warning; since #599 the
		// warning asks the note's question and the anonymous volume is nobody's
		// socket, so both voices here are notes — which also means a
		// suppression keyed to the service and one keyed to the run can no
		// longer be told apart from the outside (notes report all services or
		// none). What still can: a dedupe of the SENTENCE keyed to the run —
		// "said once, hush the rest" — would print 1 here.
		"two services keep one voice each": {
			compose: "services:\n  ci:\n    image: app:1\n    volumes:\n      - \"./docker.sock:/var/run/docker.sock\"\n" +
				"  side:\n    image: app:1\n    volumes:\n      - \"./outer.sock:/var/run/docker.sock\"\n",
			args: []string{"up", "--from-docker-compose", "--no-build", "--no-supervisor"},
			want: 2,
		},
	} {
		t.Run(name, func(t *testing.T) {
			fakeShim(t)
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("name: sockonce\n"+tc.compose), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.overlay != "" {
				if err := os.WriteFile(filepath.Join(dir, "compose.opossum.yaml"), []byte(tc.overlay), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(dir)
			var out, errOut strings.Builder
			runCLI(tc.args, &out, &errOut)
			said := out.String() + errOut.String()
			if got := strings.Count(said, claim); got != tc.want {
				t.Errorf("the run speaks about the Docker socket %d time(s), want %d:\n%s", got, tc.want, said)
			}
		})
	}
}

// Three messages whose format strings carry two or more same-typed arguments,
// found by exchanging them (`swaps -sweep ./cmd/opossum`): the suite stayed
// green with each exchange, and the probe confirmed the suite does feed the
// arguments different values — nobody read the sentence whole. Each is pinned
// whole here, with the two things in their places.

// `ws rollback` names the snapshot it restored and the auto-save it made of
// what was there. Read the other way round, it sends the reader to recover
// from the wrong one. The auto-save carries a timestamp, so its name is taken
// from the line and the line is then compared whole.
func TestWsRollbackNamesTheTargetAndTheAutosaveInThatOrder(t *testing.T) {
	work := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExec(t, filepath.Join(work, "f.txt"), "v1")
	if _, err := run(t, "ws", "snapshot", "s1", "--path", work); err != nil {
		t.Fatalf("ws snapshot: %v", err)
	}
	out, err := run(t, "ws", "rollback", "s1", "--path", work)
	if err != nil {
		t.Fatalf("ws rollback: %v", err)
	}
	// The auto-save is named before-rollback-YYYYMMDD-HHMMSS.nnnnnnnnn (see
	// workspace's timestamp). A match that stopped short would shorten `want`
	// by the same amount and the whole-line comparison below would go red, so
	// the class is exact rather than generous.
	autosave := regexp.MustCompile(`before-rollback-[0-9]{8}-[0-9]{6}\.[0-9]{9}`).FindString(out)
	if autosave == "" {
		t.Fatalf("no auto-save named in %q", out)
	}
	want := fmt.Sprintf("Rolled workspace back to %q (the previous state was saved as %q)\n", "s1", autosave)
	if out != want {
		t.Errorf("ws rollback said:\n%q\nwant:\n%q", out, want)
	}
}

// `ws ls` is a two-column table, NAME then SAVED. A test that looks for the
// name somewhere in the output is satisfied with the columns the other way
// round; this reads each row by column, with the saved-at times set so that
// the two rows cannot be confused with each other either.
func TestWsLsPutsTheNameBeforeTheTimeOnEveryRow(t *testing.T) {
	work := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExec(t, filepath.Join(work, "f.txt"), "v1")
	stamps := map[string]time.Time{
		"older": time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local),
		"newer": time.Date(2026, 6, 7, 8, 9, 10, 0, time.Local),
	}
	for name, at := range stamps {
		if _, err := run(t, "ws", "snapshot", name, "--path", work); err != nil {
			t.Fatalf("ws snapshot %s: %v", name, err)
		}
		// The snapshot directory is a sibling of the workspace; its mtime is
		// what the table prints.
		dirs, _ := filepath.Glob(filepath.Join(filepath.Dir(work), "*", name))
		if len(dirs) != 1 {
			t.Fatalf("expected one snapshot directory for %s, found %v", name, dirs)
		}
		if err := os.Chtimes(dirs[0], at, at); err != nil {
			t.Fatal(err)
		}
	}
	out, err := run(t, "ws", "ls", "--path", work)
	if err != nil {
		t.Fatalf("ws ls: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 || strings.Join(strings.Fields(lines[0]), " ") != "NAME SAVED" {
		t.Fatalf("want a NAME SAVED header and two rows, got:\n%s", out)
	}
	// Split on whitespace, which reads the columns only because the names
	// this test chose carry none — a snapshot name may. The columns are the
	// question here, not the names.
	for _, row := range lines[1:] {
		cols := strings.Fields(row)
		if len(cols) != 3 { // name, date, time
			t.Errorf("row %q does not read as name, date, time", row)
			continue
		}
		at, ok := stamps[cols[0]]
		if !ok {
			t.Errorf("row %q: first column %q is not a snapshot name — the columns are the other way round", row, cols[0])
			continue
		}
		if got, want := cols[1]+" "+cols[2], at.Format("2006-01-02 15:04:05"); got != want {
			t.Errorf("row %q: saved-at column is %q, want %q", row, got, want)
		}
	}
}

// With --force there is nobody to read a warning, so a mistargeted destroy is
// refused — and the refusal names two projects: the one asked for and the one
// this directory belongs to. Exchanged, it tells the reader the opposite of
// what happened. Pinned whole, including which files would have gone.
func TestDestroyForceRefusalNamesBothProjectsTheRightWayRound(t *testing.T) {
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("INSPECT_PROJECT", "other")
	dir := destroyProject(t, "name: mine\nservices:\n  web:\n    image: web\n")
	t.Chdir(dir)

	_, err := run(t, "-p", "other", "destroy", "--force")
	if err == nil {
		t.Fatal("a mistargeted --force destroy must be refused")
	}
	want := fmt.Sprintf("refusing to destroy %q from a directory that belongs to %q: "+
		"--force would remove this directory's generated files (%s) under another "+
		"project's name, with nothing asked and nobody to read a warning.\n"+
		"  To remove only %[1]q's containers, volumes, images and supervisor, add --keep-local.\n"+
		"  To take this directory apart, run it here without -p.",
		"other", "mine", filepath.Join(dir, ".opossum")+", "+filepath.Join(dir, "compose.opossum.yaml"))
	if err.Error() != want {
		t.Errorf("the refusal said:\n%q\nwant:\n%q", err.Error(), want)
	}
}

// When the overlay cannot be written, "what it would have contained" used to be
// followed by one headline per entry — the index of a file nobody was getting,
// with the file's own words (Why, What to expect, the YAML itself) nowhere.
// The note-only road learned to print its prose (#491); this is the other
// road, the one with something to apply, and it prints the file's text so
// that a reader can put it in place by hand.
func TestAnOverlayThatCannotBeWrittenIsShownInFull(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a 0555 directory, so the road this measures is not there")
	}
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	// One change to apply (PGDATA under a named volume) and one note (a device
	// node), so both kinds of entry have text that only the file carried. The
	// bind's host path carries a "$": the overlay writes it as "$$" so compose
	// reads it back literally, and the text on screen is for writing into a
	// file, so it must keep the "$$" — which nothing can measure without one.
	host := filepath.Join(t.TempDir(), "price$data")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	// Written into the compose file as "$$": compose interpolates a bare "$"
	// as a variable, and "$data" is nobody's.
	inCompose := strings.ReplaceAll(host, "$", "$$")
	// The path sits at a second Postgres's data directory, which is what puts
	// it into an entry (a suggestion) at all: a bind anywhere else is not
	// something the overlay speaks about.
	compose := "name: n5\nservices:\n  db:\n    image: postgres:16\n    volumes:\n" +
		"      - pgdata:/var/lib/postgresql/data\n      - /dev/ttyUSB0:/dev/ttyUSB0\n" +
		"  pg2:\n    image: postgres:16\n    volumes:\n      - " + inCompose + ":/var/lib/postgresql/data\n" +
		"volumes:\n  pgdata: {}\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	// Nothing can be created here, so the overlay's temp file cannot either.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	out, _ := run(t, "up", "--from-docker-compose", "--no-build", "--no-supervisor")
	if !strings.Contains(out, "couldn't write compose.opossum.yaml") {
		t.Fatalf("this test is about the road where the overlay cannot be written, and that road was not taken:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.opossum.yaml")); err == nil {
		t.Fatal("the overlay was written after all, so nothing here measures the fallback")
	}
	// The headlines stay — they are the index — and then the file's words: the
	// applied entry's YAML and its Why, the note's Why and What to expect, and
	// the "$$" as the file would have carried it.
	for _, want := range []string{
		"What it would have contained:",
		"opossum: 3 change(s) opossum would apply:",
		"      PGDATA: /var/lib/postgresql/data/pgdata",
		"  # Why: Apple container attaches a named volume as a filesystem mount point",
		"  # [opossum note] service \"db\": mounts the host path /dev/ttyUSB0",
		"  # What to expect: this service's device-dependent features will not work",
		"price$$data",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the fallback should carry %q, said:\n%s", want, out)
		}
	}
	// And not the file introducing itself to a reader of the file: after the
	// headlines, the first line of the text is the YAML, two spaces in. Asked
	// by position rather than by the header's wording, which this test does
	// not own.
	_, after, _ := strings.Cut(out, "What it would have contained:")
	var first string
	for _, line := range strings.Split(after, "\n")[1:] {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "opossum:") {
			first = line
			break
		}
	}
	if first != "  services:" {
		t.Errorf("the text should open with the YAML, two spaces in, got %q in:\n%s", first, out)
	}
}

// The text just shown carries the notes' prose, so the up that follows must
// not say the same thing again as a warning — that is the contract the
// note-only road keeps through MarkNotesReported, and this road prints the
// same prose. A Docker-socket mount is the note that also has a warning in
// `up`, so it is the one that shows a second telling.
func TestAnOverlayShownInFullCountsAsItsNotesReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a 0555 directory, so the road this measures is not there")
	}
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// A file named docker.sock, outside the read-only project so the bind's
	// source exists (a symlink to a socket would be refused before any of
	// this, which is a different road).
	sock := filepath.Join(t.TempDir(), "docker.sock")
	if err := os.WriteFile(sock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	compose := "name: n6\nservices:\n  db:\n    image: postgres:16\n    volumes:\n" +
		"      - pgdata:/var/lib/postgresql/data\n      - " + sock + ":/var/run/docker.sock\nvolumes:\n  pgdata: {}\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	out, _ := run(t, "up", "--from-docker-compose", "--no-build", "--no-supervisor")
	if !strings.Contains(out, "couldn't write compose.opossum.yaml") {
		t.Fatalf("the road where the overlay cannot be written was not taken:\n%s", out)
	}
	if !strings.Contains(out, "  # [opossum note] service \"db\": mounts the Docker socket") {
		t.Fatalf("the note's prose should have been shown, said:\n%s", out)
	}
	if n := strings.Count(out, "[OPSM-204]"); n != 1 {
		t.Errorf("the Docker-socket note was told %d times; the prose was shown, so the warning must stay quiet:\n%s", n, out)
	}
}

// `up --from-docker-compose` used to plan the overlay before the run's
// profiles were enabled, and over every service in the file — so a service
// gated behind a profile got an entry, and a note, about a run it was not
// part of. Now the plan follows the run: without the profile the service is
// left out and said to be; with it (flag or COMPOSE_PROFILES) it is looked at.
func TestTheOverlayFollowsTheProfilesOfThisRun(t *testing.T) {
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("COMPOSE_PROFILES", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(
		"name: prof\nservices:\n  web:\n    image: alpine:3\n  db:\n    image: postgres:16\n"+
			"    profiles: [debug]\n    volumes:\n      - pgdata:/var/lib/postgresql/data\nvolumes:\n  pgdata: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := run(t, "up", "--from-docker-compose", "--no-build", "--dry-run")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if strings.Contains(out, "[OPSM-101]") {
		t.Errorf("db is not part of this run, so it should not be diagnosed:\n%s", out)
	}
	want := "opossum: 1 service(s) gated behind a profile not enabled in this run were not looked at " +
		"(docker compose leaves them out of `config` the same way): db. A run with the profile enabled " +
		"reports what they would need."
	if !strings.Contains(out, want) {
		t.Errorf("the service left out should be named, with why:\n%s\nwant: %s", out, want)
	}
	if regexp.MustCompile(`Startup order: .*\bdb\b`).MatchString(out) {
		t.Errorf("db is gated and not enabled, so it should not be in the plan:\n%s", out)
	}

	for name, args := range map[string][]string{
		"--profile":        {"up", "--from-docker-compose", "--no-build", "--dry-run", "--profile", "debug"},
		"COMPOSE_PROFILES": {"up", "--from-docker-compose", "--no-build", "--dry-run"},
		"named":            {"up", "--from-docker-compose", "--no-build", "--dry-run", "db"},
	} {
		if name == "COMPOSE_PROFILES" {
			t.Setenv("COMPOSE_PROFILES", "debug")
		} else {
			t.Setenv("COMPOSE_PROFILES", "")
		}
		out, err := run(t, args...)
		if err != nil {
			t.Fatalf("%s: up: %v", name, err)
		}
		if !strings.Contains(out, "[OPSM-101]") {
			t.Errorf("%s: db is part of this run, so it should be diagnosed:\n%s", name, out)
		}
		if strings.Contains(out, "were not looked at") {
			t.Errorf("%s: nothing was left out, so nothing should be said to be:\n%s", name, out)
		}
		// And the run itself starts db: the overlay reload begins from a fresh
		// orchestrator, and the profiles have to be enabled on it again, or the
		// entry is written for a service that then does not start.
		if !regexp.MustCompile(`Startup order: .*\bdb\b`).MatchString(out) {
			t.Errorf("%s: db is enabled for this run, so it should be in the plan:\n%s", name, out)
		}
	}
}

// The overlay is written once. A later run that enables a profile finds it in
// place and is told what more it found — and told how to have that written:
// by deleting the file and running again. That rerun has to be *this* run,
// profile flags and named services included, because the plan looks only at
// what the run enables; the plain command would look at fewer and leave the
// same services out again. The advice is followed to the letter and has to
// work.
func TestTheRewriteAdviceRepeatsThisRunsProfiles(t *testing.T) {
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("COMPOSE_PROFILES", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(
		"name: prof\nservices:\n  pg:\n    image: postgres:16\n    volumes:\n      - pg:/var/lib/postgresql/data\n"+
			"  db:\n    image: postgres:16\n    profiles: [debug]\n    volumes:\n      - pgdata:/var/lib/postgresql/data\n"+
			"volumes:\n  pg: {}\n  pgdata: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	// Written without the profile: pg's entry only.
	if _, err := run(t, "up", "--from-docker-compose", "--no-build", "--no-supervisor"); err != nil {
		t.Fatalf("up: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(dir, "compose.opossum.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(first), "\"db\"") {
		t.Fatalf("db was not part of the first run, so the overlay should not hold it:\n%s", first)
	}

	// With the profile: found more, and the advice names the profile.
	out, err := run(t, "up", "--from-docker-compose", "--no-build", "--dry-run", "--profile", "debug")
	if err != nil {
		t.Fatalf("up --profile debug: %v", err)
	}
	if !strings.Contains(out, "found more") || !strings.Contains(out, "[OPSM-101] service \"db\"") {
		t.Fatalf("the run with the profile should find db's entry missing:\n%s", out)
	}
	const advice = "`rm compose.opossum.yaml && opossum up --from-docker-compose --profile debug`"
	if !strings.Contains(out, advice) {
		t.Errorf("the rewrite advice should repeat this run's profile, got:\n%s\nwant: %s", out, advice)
	}

	// Followed to the letter: the rewrite holds db.
	if err := os.Remove(filepath.Join(dir, "compose.opossum.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "up", "--from-docker-compose", "--no-build", "--no-supervisor", "--profile", "debug"); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(dir, "compose.opossum.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(second), "service \"db\"") {
		t.Errorf("following the advice should write db's entry, got:\n%s", second)
	}
}

// The supervisor writes through a size-capped log. When that log cannot be
// opened it says so on the line it writes instead — the time, then the notice —
// and carries on uncapped. No test reached that line (the swap sweep of #559
// reported it unreached), so the time and the notice could have changed places
// with nothing going red. A directory where the log file should be is what
// keeps the pid file writable while the log is not.
func TestASupervisorThatCannotOpenItsLogSaysSoTimeFirst(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: uncapped\nservices:\n  web:\n    image: web:latest\n    restart: always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(state, "opossum", "uncapped", "supervisor.log"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(opossumBin, "__supervise", "--watch-service", "web")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+state)
	var out lockedOutput
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(out.String(), "[OPSM-411]") {
		if time.Now().After(deadline) {
			t.Fatalf("the supervisor never said its log was uncapped, said:\n%s", out.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := syscall.Kill(cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	// The time opens the line and the notice follows it: two strings on one
	// format call, and printed the other way round the line would still be
	// a line.
	line := regexp.MustCompile(`(?m)^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:Z|[+-]\d\d:\d\d) \[OPSM-411\] couldn't open a size-capped log \(`)
	if !line.MatchString(out.String()) {
		t.Errorf("the notice should read `<time> [OPSM-411] couldn't open a size-capped log (…)`, said:\n%s", out.String())
	}
}

// lockedOutput is a buffer a child's output can land in while the test reads
// it: exec copies stdout on its own goroutine, and a plain builder read from
// the test at the same time is a data race.
type lockedOutput struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedOutput) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedOutput) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
