package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// What these check, and what they do not.
//
// The property this exists for is that **nothing it starts can outlive it**, and
// that is checked from the world's side: while a command is running, its process
// group holds this and the command and nothing else, and killing this leaves
// exactly that one process behind. Both are sampled over a window rather than
// waited for — a version that spawns its workers reaches the right count as soon
// as they die of old age, and a wait would sit there letting it. Measured: the
// spawning version fails both in about two and a half seconds, naming every
// extra process.
//
// The load itself is checked by how much CPU this burns, against a run with no
// workers at all. Without the second half the first says nothing: a process that
// did no work at all also finishes.
//
// **Not checked:**
//
//   - The command being orphaned. Killing this does leave the command running —
//     `sleep` in the test below, `go test` in real use. That is the same limit
//     `cmd/noleftovers` has, and the test pins it rather than pretending
//     otherwise.
//   - Whether the load is enough to make any particular flake appear. It makes
//     the machine busy; whether that is the kind of busy a given race needs is
//     a question for whoever is hunting it.
//   - Anything about fairness. Workers spin; the scheduler decides the rest, and
//     on a machine with other work the share each one gets is not this tool's to
//     promise.
//   - `-for` firing while the command still runs leaves the command running
//     unloaded, which is deliberate: the deadline is a bound on the load, not
//     on the work.

func TestNothingElseIsRunningWhileTheCommandRuns(t *testing.T) {
	bin := build(t)
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Fatalf("pgrep is not on PATH, so this cannot tell a spawned worker from an absent one: %v", err)
	}

	cmd := exec.Command(bin, "-n", "4", "sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })

	// Wait only for the command to be up, then watch. Waiting for the group to
	// *become* two was the bug in the first version of this test: a version that
	// spawned its workers reached two as soon as they died of old age, and the
	// wait sat there for twenty seconds letting it happen. What has to be true is
	// that the group is two the whole time the command is running.
	waitFor(t, func() bool { return len(inGroup(cmd.Process.Pid)) >= 2 })
	worst := 0
	var seen []int
	for i := 0; i < 30; i++ {
		if members := inGroup(cmd.Process.Pid); len(members) > worst {
			worst, seen = len(members), members
		}
		time.Sleep(50 * time.Millisecond)
	}
	if worst != 2 {
		t.Errorf("the group held %d processes (%v) while the command ran, want 2 — this tool and the "+
			"command. A worker that was spawned rather than run in this process would show up here", worst, seen)
	}
}

// The point of keeping the load in this process: killing it is enough.
func TestKillingItLeavesNothingButTheCommand(t *testing.T) {
	bin := build(t)
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Fatalf("pgrep is not on PATH, so survivors cannot be counted: %v", err)
	}

	cmd := exec.Command(bin, "-n", "4", "sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })

	waitFor(t, func() bool { return len(inGroup(pgid)) >= 2 })
	// Two in the group is not yet the situation: a child counts from fork,
	// and one killed with its parent before exec dies holding borrowed state
	// — the survivor question would be reading scheduler luck (the clean-
	// container sieve, six saturated CPUs, read it wrong). The situation is
	// the command existing as itself, and "as itself" has to mean the whole
	// command line: a child between fork and exec shows its parent's argv,
	// which contains the word "sleep" too — a precondition that looked for
	// the word was the same free pass with an extra ps in it.
	waitFor(t, func() bool {
		for _, pid := range inGroup(pgid) {
			if pid != cmd.Process.Pid && commandOf(t, pid) == "sleep 300" {
				return true
			}
		}
		return false
	})
	if err := syscall.Kill(cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	// Sampled rather than waited for. Waiting until the group holds one was the
	// same free pass as in the test above: workers spawned as processes reach one as
	// soon as they die of old age, and a twenty-second wait sat there letting it
	// happen. What has to be true is that one is all there ever is.
	time.Sleep(200 * time.Millisecond)
	worst, left := 0, []int(nil)
	for i := 0; i < 20; i++ {
		if members := inGroup(pgid); len(members) > worst {
			worst, left = len(members), members
		}
		time.Sleep(50 * time.Millisecond)
	}
	if worst != 1 {
		t.Fatalf("survivors = %v, want exactly one (the orphaned command): anything else was a worker "+
			"that outlived the process that started it", left)
	}
	if got := commandOf(t, left[0]); !strings.Contains(got, "sleep") {
		t.Errorf("the survivor is %q, want the command itself", got)
	}
}

// Both directions: that CPU was burned means nothing unless a run that asks for
// no load burns none.
//
// Measured against the command's own second, not against this process's whole
// life. The ratio this used to take had exec, the runtime coming up, and the
// wait on the way out in its denominator, all of which stretch when the machine
// is busy — so the check failed three runs in four with "the workers are not
// running" while the workers were burning their full 4.03 seconds. A threshold
// that moves with how loaded the machine is cannot be a check on load.
func TestTheLoadIsRealAndAskedFor(t *testing.T) {
	bin := build(t)
	const commandTakes = time.Second
	// What the hardware can give: four spinning threads on a two-core runner
	// burn two cores' worth, and no threshold above that is reachable there.
	capable := 4.0
	if n := float64(runtime.NumCPU()); n < capable {
		capable = n
	}
	for _, tc := range []struct {
		name    string
		workers string
		atLeast time.Duration
		atMost  time.Duration
	}{
		{"four workers keep the machine busy", "4",
			time.Duration(0.4 * capable * float64(commandTakes)), 0},
		// Startup and teardown, and nothing else.
		{"none means none", "0", 0, commandTakes / 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bin, "-n", tc.workers, "sleep", "1")
			var said bytes.Buffer
			cmd.Stderr = &said
			if err := cmd.Run(); err != nil {
				t.Fatal(err)
			}
			// Both directions, again: without the second half a version that
			// says "no workers" whatever it was asked for passes, because only
			// the zero case is ever read.
			if tc.workers == "0" && !strings.Contains(said.String(), "no workers asked for; the command runs unloaded") {
				t.Errorf("a run with no load has to say so, said:\n%s", said.String())
			}
			if tc.workers != "0" && strings.Contains(said.String(), "no workers asked for") {
				t.Errorf("a run with %s workers must not claim it had none, said:\n%s", tc.workers, said.String())
			}
			burned := cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()
			if tc.atLeast > 0 && burned < tc.atLeast {
				t.Errorf("burned %s of CPU during a %s command, want at least %s: the workers are not running",
					burned, commandTakes, tc.atLeast)
			}
			if tc.atMost > 0 && burned > tc.atMost {
				t.Errorf("burned %s of CPU during a %s command, want at most %s: something is spinning that was not asked for",
					burned, commandTakes, tc.atMost)
			}
		})
	}
}

// The load has to survive a machine whose GOMAXPROCS is smaller than -n. It did
// not: the runtime runs that many goroutines at once whatever is asked for, so
// four workers on a one-thread default burned one core's worth. CI found that.
//
// Both numbers are taken here, under the same GOMAXPROCS and within a second of
// each other, and compared to each other rather than to a constant. One worker
// burns one core-second whether or not the cap is there, so it is the yardstick
// the machine itself provides.
//
// It does not drag both down equally — that would be the tidy story, and it is
// wrong. Measured under eight competing spinners: one worker loses 3-6% (it is
// a single runnable thread, and the scheduler keeps giving it nearly a whole
// core), four workers lose 26%. So the ratio falls too, 4.0 to 3.0. What it buys
// is that it falls about four times slower than the absolute number does, which
// is enough: the worst of eighteen measured pairs was 2.71 against a line at
// 1.6.
//
// Two cores is the tight end, not the roomy one. The ceiling there is 2.0 rather
// than the 4.0 eight cores allow, so the line at 1.6 uses 80% of the room
// against 40% here — and the test harness is not contention added on top of an
// idle machine, it is there from the first measurement: under -race a single try
// gave 1.52. That is why the best of a few is load-bearing on two cores and a
// nicety on eight.
//
// A fixed threshold is what this had before, twice. First "0.4 x what the
// machine can give", which wanted 0.80s on a two-core runner and so let the
// unfixed 1.07s straight through — blind on the very machine that found the bug.
// Then a flat 1.5s, which is a wide window on eight cores and a quarter of the
// available room on two.
func TestTheLoadIsNotCappedByGOMAXPROCS(t *testing.T) {
	if runtime.NumCPU() < 2 {
		t.Skip("one core cannot tell a capped load from an uncapped one: both burn one core-second per second")
	}
	bin := build(t)
	// Best of a few, because contention only ever subtracts. The capped version
	// cannot exceed one core-second however many tries it gets — that is what
	// being capped means — so the best run is the machine's honest answer for
	// each side, and taking it removes whatever the test harness happened to be
	// doing at the time. Measured on a two-core runner under -race, one try gave
	// 1.52 against a line at 1.6; the harness was the competitor.
	one := bestOfThree(t, bin, "1")
	four := bestOfThree(t, bin, "4")
	// A floor under the yardstick. `four < one*1.6` with a zero on the right is
	// `four < 0`, which is false for every possible four: the comparison does not
	// merely mismeasure, it disappears, and the test passes with the bug in place
	// — demonstrated. Contention can only push this number down, so a small one
	// means the measurement failed rather than that the machine is slow.
	if one < 500*time.Millisecond {
		t.Fatalf("the yardstick came back as %s: nothing can be concluded from a ratio against it", one)
	}
	if four < one*16/10 {
		t.Errorf("four workers burned %s where one burned %s: the load is capped by the runtime, "+
			"which lets four workers do one worker's work", four, one)
	}
}

func bestOfThree(t *testing.T, bin, workers string) time.Duration {
	t.Helper()
	best := time.Duration(0)
	for i := 0; i < 3; i++ {
		if got := burnedUnderOneThread(t, bin, workers); got > best {
			best = got
		}
	}
	return best
}

// burnedUnderOneThread runs the command with GOMAXPROCS=1 — the small world the
// cap used to hide in — and reports the CPU it consumed.
func burnedUnderOneThread(t *testing.T, bin, workers string) time.Duration {
	t.Helper()
	cmd := exec.Command(bin, "-n", workers, "sleep", "1")
	cmd.Env = append(os.Environ(), "GOMAXPROCS=1")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()
}

// It has to come back when the command does. A version that never stops its
// workers hangs instead — which the suite would eventually notice, ten minutes
// later, as a timeout panic that leaves the load orphaned on the way out.
func TestItComesBackWhenTheCommandDoes(t *testing.T) {
	bin := build(t)
	started := time.Now()
	// -for given explicitly: without it the default five minutes is what a
	// version that never stops its workers would take to go red, and "comes back
	// when the command does" would be a claim nobody waits long enough to test.
	cmd := exec.Command(bin, "-n", "4", "-for", "3s", "true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took > 2*time.Second {
		t.Errorf("took %s for a command that returns at once: the load is not being stopped", took)
	}
}

func TestALoadThatCannotBeAppliedIsRefusedRatherThanSkipped(t *testing.T) {
	for _, tc := range []struct {
		name    string
		workers int
		limit   time.Duration
		says    string
	}{
		// A deadline in the past would stop the load before it started, and the
		// run would look exactly like one that was never asked to be busy.
		{"a deadline of zero", 2, 0, "would stop the load before it started"},
		{"a deadline in the past", 2, -5 * time.Second, "would stop the load before it started"},
		{"more workers than the machine can be asked for", 4*runtime.NumCPU() + 1, time.Minute, "more than this machine can be asked for"},
		{"a negative count", -1, time.Minute, "cannot spin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if got := run([]string{"true"}, tc.workers, tc.limit, &out); got != 2 {
				t.Errorf("exit = %d, want 2, said:\n%s", got, out.String())
			}
			if !strings.Contains(out.String(), tc.says) {
				t.Errorf("should say %q, said:\n%s", tc.says, out.String())
			}
		})
	}
}

// The deadline is a bound on the load, not on the work: the command keeps going,
// unloaded.
func TestTheDeadlineStopsTheLoadWithoutStoppingTheCommand(t *testing.T) {
	bin := build(t)
	started := time.Now()
	cmd := exec.Command(bin, "-n", "4", "-for", "200ms", "sleep", "1.5")
	var said bytes.Buffer
	cmd.Stderr = &said
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	// Said out loud, and checked: a run whose load stopped early looks exactly
	// like one that was never loaded, and the person measuring believes they
	// measured the busy case.
	if want := "the load stopped after 200ms; the command is still running, unloaded\n"; !strings.Contains(said.String(), want) {
		t.Errorf("should say word for word:\n%s\nsaid:\n%s", want, said.String())
	}
	wall := time.Since(started)
	if wall < 1400*time.Millisecond {
		t.Fatalf("came back in %s: the command did not run to the end, so this measures nothing", wall)
	}
	burned := cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()
	// A fifth of a second of load is well under one wall-second of CPU however
	// wide the machine is; the whole 1.5s run would be several.
	if burned > 2*time.Second {
		t.Errorf("burned %s of CPU over %s: the load outlived its deadline", burned, wall)
	}
	// Scaled the same way: a single-core runner can only give 200ms in 200ms.
	capable := float64(4)
	if n := float64(runtime.NumCPU()); n < capable {
		capable = n
	}
	if least := time.Duration(capable * 0.4 * float64(200*time.Millisecond)); burned < least {
		t.Errorf("burned only %s of CPU (want at least %s): the load never started, so the deadline proves nothing", burned, least)
	}
}

// Read through run() rather than the built binary: what is being pinned is the
// number this returns, and exec adds nothing to that.
func TestTheCommandsStatusComesBack(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"success", []string{"true"}, 0},
		{"failure keeps its own number", []string{"sh", "-c", "exit 7"}, 7},
		{"a signal is 128 plus it, not -1", []string{"sh", "-c", "kill -INT $$"}, 130},
		{"a command that will not start", []string{filepath.Join(t.TempDir(), "no-such-thing")}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if got := run(tc.args, 2, time.Minute, &out); got != tc.want {
				t.Errorf("exit = %d, want %d, said:\n%s", got, tc.want, out.String())
			}
		})
	}
}

func build(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "busy")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("building: %v\n%s", err, out)
	}
	// Run it once before anyone times it. The first run of a binary that has
	// just been written costs what every run after it does not: the kernel has
	// not seen these pages, and on macOS the file is checked before it is
	// allowed to start. Measured while the rest of the suite was running, that
	// first start took 0.46s to 1.58s where the second took 0.03s to 0.05s. It
	// lands on the wall clock only — the same runs differ by a hundredth of a
	// second in CPU time — so what it reaches is the two tests that read a
	// clock, and it reaches them as time the load never spent.
	//
	// A long deadline rather than a short one: the command returns at once and
	// the load stops with it, so this is over as fast either way, and a
	// deadline that could ring first would make every build here take the path
	// that says the load stopped before the command did.
	if out, err := exec.Command(bin, "-n", "1", "-for", "1h", "true").CombinedOutput(); err != nil {
		t.Fatalf("warming: %v\n%s", err, out)
	}
	return bin
}

func inGroup(pgid int) []int {
	out, _ := exec.Command("pgrep", "-g", strconv.Itoa(pgid)).Output()
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(f); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

func children(t *testing.T, parent int) []int {
	t.Helper()
	out, _ := exec.Command("pgrep", "-P", strconv.Itoa(parent)).Output()
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(f); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

func commandOf(t *testing.T, pid int) string {
	t.Helper()
	out, _ := exec.Command("ps", "-ww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	return strings.TrimSpace(string(out))
}

func waitFor(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("the world never reached the state this means to measure")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
