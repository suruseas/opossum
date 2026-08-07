package orchestrator

// Evals for #283: a detached `up` must not report success over a service whose
// container exited right after starting (no healthcheck/depends_on to catch it).
// verifyStarted reports each crashed service with its logs and fails the up; the
// containers are left up for inspection (no rollback), and one-shots are exempt.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	rt "github.com/suruseas/opossum/internal/runtime"
)

const stoppedInspect = `[{"status":{"state":"stopped"},"configuration":{"id":"web"}}]`
const runningInspect = `[{"status":{"state":"running"},"configuration":{"id":"web"}}]`

func inspectShim(t *testing.T, inspectJSON, logsLine string) *rt.Runtime {
	return scriptShim(t, ""+
		"  inspect) echo '"+inspectJSON+"' ;;\n"+
		"  logs) echo '"+logsLine+"' ;;\n")
}

func webProject() *compose.Project {
	return &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"web": {Name: "web", Image: "web:latest"},
	}}
}

// Captured before init() zeroes it. Package-level variables initialise before any
// init(), so this is the value opossum actually ships with — the one the suite
// then turns off for itself.
var shippedCrashGrace = defaultCrashGrace

// `up` gives a service a second to fall over before calling it started, and
// nearly every eval in this package drives an `up`. Paying that in all of them
// cost ~150s of wall clock for nothing — none of them are about the window. The
// evals that ARE about it set their own value.
func init() {
	defaultCrashGrace = 0
	// And make the suite deaf to the environment: zeroing the fallback is not
	// enough, because OPOSSUM_CRASH_GRACE wins over it. Left set in a shell or on
	// CI, it would drag every `up` eval along with it — a large value would turn
	// into a timeout nobody could explain from the diff.
	os.Unsetenv("OPOSSUM_CRASH_GRACE")
}

// The grace is a measured number, and nothing else here would notice it changing:
// every eval that exercises the wait sets its own value. Measured on the real
// runtime, four real misconfigurations were gone 0.08–0.44s after `run -d`
// returned, so half a second is not enough margin and a second is.
func TestTheShippedGraceIsTheOneThatWasMeasuredFor(t *testing.T) {
	if shippedCrashGrace != time.Second {
		t.Errorf("the grace `up` ships with is %v, want 1s — the slowest failure measured took 0.44s, "+
			"and anything under that silently stops catching it", shippedCrashGrace)
	}
}

// The environment seam is what lets an eval driving the whole CLI reach this, and
// what lets someone opt out of the extra second. A value that doesn't parse must
// not fail an `up` over a preference.
func TestTheGraceCanBeSetFromTheEnvironment(t *testing.T) {
	for _, c := range []struct {
		env  string
		want time.Duration
	}{
		{"250ms", 250 * time.Millisecond},
		{"0", 0},
		{"2s", 2 * time.Second},
		{"", defaultCrashGrace},
		{"not-a-duration", defaultCrashGrace},
		{"-1s", defaultCrashGrace}, // a negative wait is nonsense, not an instruction
	} {
		t.Run(c.env, func(t *testing.T) {
			t.Setenv("OPOSSUM_CRASH_GRACE", c.env)
			if got := New(webProject(), inspectShim(t, runningInspect, ""), "", &bytes.Buffer{}).crashGrace; got != c.want {
				t.Errorf("OPOSSUM_CRASH_GRACE=%q gave %v, want %v", c.env, got, c.want)
			}
		})
	}
}

// dyingService is a container stand-in that reads its state and its logs from
// files, so a test can decide *when* the service falls over rather than after how
// many looks. The two are not the same thing, and only the first can tell whether
// the wait happens between the looks: a shim that flips on the second inspect
// reports a crash whether opossum waited or not.
type dyingService struct {
	rt         *rt.Runtime
	state, log string // paths the shim reads
}

func newDyingService(t *testing.T) *dyingService {
	t.Helper()
	dir := t.TempDir()
	d := &dyingService{state: filepath.Join(dir, "state"), log: filepath.Join(dir, "log")}
	if err := os.WriteFile(d.state, []byte(runningInspect), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.log, []byte("Starting up\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d.rt = scriptShim(t, "  inspect) cat "+d.state+" ;;\n  logs) cat "+d.log+" ;;\n")
	return d
}

// die makes the service report as stopped from now on, and leaves the log line it
// died with. Called from the sleep stub, it happens *during* the grace.
func (d *dyingService) die(t *testing.T, logLine string) {
	t.Helper()
	if err := os.WriteFile(d.state, []byte(stoppedInspect), 0o644); err != nil {
		t.Error(err)
	}
	if err := os.WriteFile(d.log, []byte(logLine+"\n"), 0o644); err != nil {
		t.Error(err)
	}
}

// The wait has to happen BETWEEN the two looks. That is the whole mechanism, and
// asserting "it looked twice" and "it slept a second" separately does not say it:
// moving the sleep after both looks satisfies each of those and leaves the two
// inspects ~36ms apart, which catches nothing the old snapshot didn't. The cost
// would still be paid on every `up` — the worst possible shape, a wait that buys
// nothing.
//
// So the service dies from inside the sleep. A version that waits in the wrong
// place, or does not wait at all, sees "running" both times and reports success.
//
// It takes this and TestUpDoesNotWaitWhenEverythingIsAlreadyDown together to say
// "between": a version that slept first and then looked twice would satisfy this
// one on its own, and is caught over there by waiting when there is nothing left
// to watch. Deleting either leaves the other passing over a hole.
func TestUpLooksAgainAfterTheWaitAndNotBeforeIt(t *testing.T) {
	svc := newDyingService(t)
	var out bytes.Buffer
	var slept []time.Duration
	o := New(webProject(), svc.rt, "", &out)
	o.crashGrace = time.Second
	o.sleep = func(d time.Duration) {
		slept = append(slept, d)
		svc.die(t, "Error: Database is uninitialized and superuser password is not specified.")
	}

	err := o.verifyStarted([]string{"web"}, map[string]bool{})
	if err == nil || !strings.Contains(err.Error(), "[OPSM-407]") {
		t.Fatalf("a service that died during the wait must fail the up, got: %v", err)
	}
	if len(slept) != 1 || slept[0] != time.Second {
		t.Errorf("slept %v, want exactly one second — the window a service has to fall over in", slept)
	}
	// The logs come from the end, so they are what the service died saying rather
	// than what it had printed by the first look.
	if s := out.String(); !strings.Contains(s, "superuser password is not specified") {
		t.Errorf("the report should carry the logs the service left behind, got: %s", s)
	}
	if s := out.String(); strings.Contains(s, "Starting up") {
		t.Errorf("the report carried the logs from before the crash: %s", s)
	}
}

// Ctrl-C during the grace. Nothing is rolled back from here — the services are
// up, and that is the point at which `up` stops undoing things — but the second
// this change added is a second of a person pressing Ctrl-C and nothing
// happening, which is the same thing every other wait in `up` already avoids.
func TestUpDoesNotSitOutTheGraceAfterCtrlC(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var slept []time.Duration
	o := New(webProject(), inspectShim(t, runningInspect, ""), "", &bytes.Buffer{})
	o.ctx = ctx
	o.crashGrace = time.Second
	o.sleep = func(d time.Duration) { slept = append(slept, d) }

	if err := o.verifyStarted([]string{"web"}, map[string]bool{}); err != nil {
		t.Fatalf("the service was running; an interrupted watch is not a crash: %v", err)
	}
	if len(slept) != 0 {
		t.Errorf("waited %v after Ctrl-C", slept)
	}
}

// A service that stays up must not be reported, and must be waited for exactly
// once — the other side of the eval above, which would otherwise be satisfied by
// something that always reports a crash.
func TestUpDoesNotFlagAServiceThatStaysUp(t *testing.T) {
	var slept []time.Duration
	o := New(webProject(), inspectShim(t, runningInspect, ""), "", &bytes.Buffer{})
	o.crashGrace = time.Second
	o.sleep = func(d time.Duration) { slept = append(slept, d) }

	if err := o.verifyStarted([]string{"web"}, map[string]bool{}); err != nil {
		t.Fatalf("a running service must not fail the up: %v", err)
	}
	if len(slept) != 1 || slept[0] != time.Second {
		t.Errorf("verifyStarted slept %v, want exactly one second", slept)
	}
}

// Services die during the wait while one stays up. This is the only path where
// the watch list shrinks without emptying — and the only place the reported order
// can be seen at all. The report is built by walking the order `up` started them
// in, so the same failing project reads the same way every time; built from the
// map they are collected in, it would shuffle between runs.
//
// Five of them, deliberately. With two, a map iteration lands on the right order
// most of the time by luck — measured at 7 runs in 8 — so a guard written that way
// fails only occasionally, which is no guard at all. The names are reverse
// alphabetical so sorting cannot produce the answer either.
func TestUpReportsTheServicesThatDiedInTheOrderTheyStarted(t *testing.T) {
	order := []string{"zulu", "yankee", "xray", "whiskey", "victor", "alive"}
	dying := order[:len(order)-1]

	dir := t.TempDir()
	state := map[string]string{}
	svcs := map[string]*compose.Service{}
	var body strings.Builder
	body.WriteString("  inspect) case \"$2\" in\n")
	for _, name := range order {
		state[name] = filepath.Join(dir, name)
		if err := os.WriteFile(state[name], []byte(runningInspect), 0o644); err != nil {
			t.Fatal(err)
		}
		svcs[name] = &compose.Service{Name: name, Image: "i"}
		body.WriteString("    " + name + ") cat " + state[name] + " ;;\n")
	}
	body.WriteString("  esac ;;\n  logs) echo 'boom' ;;\n")

	shim := scriptShim(t, body.String())
	verify := func() ([]string, error) {
		for _, name := range order { // back to life for the next run
			if err := os.WriteFile(state[name], []byte(runningInspect), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		var out bytes.Buffer
		o := New(&compose.Project{Name: "demo", Services: svcs}, shim, "", &out)
		o.crashGrace = time.Second
		o.sleep = func(time.Duration) {
			for _, name := range dying {
				if err := os.WriteFile(state[name], []byte(stoppedInspect), 0o644); err != nil {
					t.Error(err)
				}
			}
		}
		err := o.verifyStarted(order, map[string]bool{})
		return reportedOrder(out.String()), err
	}

	// Several times, because the claim is that it does not vary. One run of an
	// order that varies still lands on the right answer often enough to look fine.
	for i := 0; i < 8; i++ {
		got, err := verify()
		if err == nil {
			t.Fatal("five services died and the up succeeded")
		}
		if strings.Contains(err.Error(), `"alive"`) {
			t.Errorf("the one that stayed up must not be named: %v", err)
		}
		if !reflect.DeepEqual(got, dying) {
			t.Fatalf("run %d reported %v, want %v — the order they were started in", i, got, dying)
		}
	}
}

// reportedOrder is the services named by the per-service crash reports, in the
// order they were printed.
func reportedOrder(s string) []string {
	var got []string
	for _, line := range strings.Split(s, "\n") {
		if !strings.Contains(line, "exited right after starting") {
			continue
		}
		if i := strings.Index(line, `service "`); i >= 0 {
			rest := line[i+len(`service "`):]
			if j := strings.Index(rest, `"`); j > 0 {
				got = append(got, rest[:j])
			}
		}
	}
	return got
}

// Nothing is left to wait for when every service is already gone, so that project
// fails immediately instead of sitting out a grace it cannot learn anything in.
//
// It also holds the other half of "the wait goes between the looks" — see
// TestUpLooksAgainAfterTheWaitAndNotBeforeIt. A version that slept first and then
// looked would pass that one and fail here, because it would wait for services it
// could already see were gone.
func TestUpDoesNotWaitWhenEverythingIsAlreadyDown(t *testing.T) {
	var slept []time.Duration
	o := New(webProject(), inspectShim(t, stoppedInspect, "panic: bad config"), "", &bytes.Buffer{})
	o.crashGrace = time.Hour // a wait here would hang the suite, which is the point
	o.sleep = func(d time.Duration) { slept = append(slept, d) }

	if err := o.verifyStarted([]string{"web"}, map[string]bool{}); err == nil {
		t.Fatal("a stopped service must still fail the up")
	}
	if len(slept) != 0 {
		t.Errorf("nothing was still running, so there was nothing to wait for; slept %v", slept)
	}
}

func TestVerifyStartedFlagsCrash(t *testing.T) {
	var out bytes.Buffer
	o := New(webProject(), inspectShim(t, stoppedInspect, "panic: bad config"), "", &out)

	err := o.verifyStarted([]string{"web"}, map[string]bool{})
	if err == nil || !strings.Contains(err.Error(), "[OPSM-407]") || !strings.Contains(err.Error(), `"web"`) {
		t.Errorf("a crashed service should fail the up with OPSM-407 naming it, got: %v", err)
	}
	// The per-service warning embeds the last log lines so the cause is visible.
	if s := out.String(); !strings.Contains(s, "[OPSM-407]") || !strings.Contains(s, "exited right after starting") || !strings.Contains(s, "panic: bad config") {
		t.Errorf("verifyStarted should warn with the crashed service's logs, got: %s", s)
	}
}

func TestVerifyStartedSkipsOneShot(t *testing.T) {
	// A completed-target (one-shot) is *supposed* to exit — never flag it.
	o := New(webProject(), inspectShim(t, stoppedInspect, "done"), "", &bytes.Buffer{})
	if err := o.verifyStarted([]string{"web"}, map[string]bool{"web": true}); err != nil {
		t.Errorf("a one-shot that exited must not be flagged, got: %v", err)
	}
}

func TestVerifyStartedOkWhenRunning(t *testing.T) {
	o := New(webProject(), inspectShim(t, runningInspect, ""), "", &bytes.Buffer{})
	if err := o.verifyStarted([]string{"web"}, map[string]bool{}); err != nil {
		t.Errorf("a running service should pass, got: %v", err)
	}
}

// upWithLog drives a full `up` against a shim that logs every invocation and runs
// `runBody` for the `run` subcommand (inspect always reports the container
// stopped). Returns the up error and the invocation log — so one harness can show
// BOTH that a bring-up failure rolls back (a `stop` appears) and that a post-start
// crash does not (no `stop`), making the differential explicit.
func upWithLog(t *testing.T, runBody string) (error, string) {
	t.Helper()
	dir := t.TempDir()
	logf := filepath.Join(dir, "invocations.log")
	shim := filepath.Join(dir, "c.sh")
	body := "#!/bin/sh\necho \"$*\" >> " + logf + "\n" +
		"case \"$1\" in\n" +
		"  system) echo 'status running' ;;\n" +
		"  inspect) echo '" + stoppedInspect + "' ;;\n" +
		"  logs) echo 'boom' ;;\n" +
		"  ls) echo '[]' ;;\n" +
		"  run) " + runBody + " ;;\n" +
		"esac\nexit 0\n"
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	err := New(webProject(), &rt.Runtime{Bin: shim}, "", &bytes.Buffer{}).Up(true)
	log, _ := os.ReadFile(logf)
	return err, string(log)
}

// A post-start crash fails the up but must NOT roll the stack back — the crashed
// container stays up for inspection (like docker compose). Guards the broughtUp
// suppression of the rollback defer.
func TestUpKeepsContainersOnPostStartCrash(t *testing.T) {
	err, log := upWithLog(t, "exit 0") // run succeeds; inspect says stopped -> OPSM-407
	if err == nil || !strings.Contains(err.Error(), "[OPSM-407]") {
		t.Fatalf("up should fail with OPSM-407 on a post-start crash, got: %v", err)
	}
	// Rollback is the only path that `stop`s a container; assert it never runs.
	if strings.Contains(log, "stop ") {
		t.Errorf("a post-start crash must not roll back (no `stop`), got invocations:\n%s", log)
	}
}

// The differential: a genuine bring-up failure (the run itself fails) STILL rolls
// back on the same harness — so the no-`stop` assertion above is meaningful (the
// harness would show `stop` if the rollback fired).
func TestUpRollsBackOnBringUpFailure(t *testing.T) {
	err, log := upWithLog(t, "exit 1") // the run fails mid-loop -> rollback
	if err == nil {
		t.Fatal("up should fail when the run fails")
	}
	if !strings.Contains(log, "stop ") {
		t.Errorf("a bring-up failure must roll back (expected a `stop`), got invocations:\n%s", log)
	}
}
