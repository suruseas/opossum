package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// This is the net under the runs that cannot see themselves.
//
// The suites build under $TMPDIR and remove what they built when they finish,
// and cmd/opossum looks for processes the tests left running. Both live after
// m.Run(), so a panic — and a -timeout firing is a panic — skips them. Those
// runs leak, and they are precisely the runs whose own check never speaks.
//
// It was written against a real leak first, not against this file: a 1s timeout
// on a 1.7s test in cmd/opossum leaves a directory and an `opossum __supervise`
// running out of it, the suite says nothing at all, and this named both.
//
// **What it does not catch, so a green run is not read as more than it is:**
//
//   - Which test leaked. Out here a directory is new or it is not; nothing
//     connects it to a name. That is the in-suite check's job, and why this is
//     a second net rather than a replacement.
//   - Anything that was already there. The difference starts when the command
//     does, so a leak from an earlier run is invisible — including the one this
//     machine is carrying right now. It also means a second run straight after
//     a red one goes green while the leak is still sitting there, which is
//     exactly what somebody who has just seen it go red will try.
//   - Whose a new directory is. The difference is in time, not in ownership: a
//     suite that starts in another terminal while this one runs appears here
//     too, and cannot be told apart from a leak. The report says so rather than
//     pretending otherwise, and does not hand over an unconditional removal.
//   - Anything not named `opossum-*`. internal/orchestrator makes one temp
//     directory with the prefix `sk` — a Unix socket path has to stay short —
//     and no pattern that matches it in a shared $TMPDIR would leave strangers
//     alone. A run that leaks only that one passes.
//   - A leak a concurrent run tidies up. The difference is taken across the
//     command, so something that appears and disappears while it runs is never
//     seen.
//   - Any way of being killed but ^C. Only os.Interrupt is survived. SIGTERM
//     (`timeout`, a cancelled CI job, a harness) and SIGHUP (closing the
//     terminal on a run that takes minutes) kill this as silently as they
//     killed the suite, and those are the same kind of ending. Notifying on
//     SIGTERM would make `kill <pid>` stop working on it, which is worse.
//   - Leftovers below the top of $TMPDIR, or inside somebody else's directory.
//   - The difference between directories and files. The glob takes both, and
//     cmd/mutate writes `opossum-mutate-*.cover` files at the top of $TMPDIR —
//     so an interrupted sweep can put a *file* under a heading that says
//     directories, with a `rm -rf` beside it.
//   - Whether the process it names is ours. `pgrep -f` is handed the whole
//     directory path (pinned below), but reads it as a regular expression
//     rather than as text.
//   - The command's exit status, once `go run` has it. run() returns the
//     command's own number — except that a run which left something behind is
//     never green, so a command that succeeded, or that chose 0 after catching
//     ^C, comes back as 1. `make test` throws even that away: `go run` collapses
//     every non-zero status to 1, which is why the number is also printed. make
//     itself only tells zero from non-zero, so the gate is unaffected; a script
//     reading `make test`'s status is not.
//   - `make cover` and the commit hook, which do not go through this at all,
//     and CI, which is tracked separately.
//
// The signal paths are exercised through a built binary rather than through
// run(), because what they check is what the *process* does when a signal
// arrives. Coverage does not see those runs, so this package reads thinner than
// it is; what guards those paths is the mutations, not the percentage.

func TestARunThatLeavesSomethingBehindIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		leaves string
		exit   int
		code   int
		want   []string
		absent []string
	}{
		{
			// The shape of a panicking suite: it made its directory and died
			// before removing it.
			name:   "a directory of ours is left",
			leaves: "opossum-cmd-test-left",
			exit:   0,
			code:   1,
			want: []string{
				"these appeared in",
				"opossum-cmd-test-left",
				"a suite that finishes removes its own",
				"once nothing is using them:",
				"rm -rf",
			},
		},
		{
			// The whole point of taking a difference: another terminal's suite
			// is not this run's fault.
			name:   "nothing new is left",
			exit:   0,
			code:   0,
			absent: []string{"these appeared in", "opossum-cmd-test-before"},
		},
		{
			// A failing command keeps its own status. Turning a 3 into a 1 tells
			// the reader the tests leaked when they only failed.
			name:   "the command fails and leaves nothing",
			exit:   3,
			code:   3,
			absent: []string{"these appeared in"},
		},
		{
			// And a run that both fails and leaks says both things.
			name:   "the command fails and also leaves something",
			leaves: "opossum-cmd-test-both",
			exit:   3,
			code:   3,
			// `go run` — how the Makefile calls this — collapses every non-zero
			// status to 1, so the number has to survive as text or not at all.
			want: []string{"opossum-cmd-test-both", "rm -rf", "the command itself exited 3"},
		},
		{
			// Not everything under $TMPDIR is ours, and a check that swept the
			// whole directory would delete somebody's afternoon.
			name:   "somebody else's directory is left",
			leaves: "some-other-tool-xyz",
			exit:   0,
			code:   0,
			absent: []string{"these appeared in", "some-other-tool-xyz"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := tempdir(t)
			// Present before the command runs, so the difference has something
			// to leave alone.
			mkdir(t, filepath.Join(tmp, "opossum-cmd-test-before"))

			script := fmt.Sprintf("exit %d", tc.exit)
			if tc.leaves != "" {
				script = fmt.Sprintf("mkdir %q; exit %d", filepath.Join(tmp, tc.leaves), tc.exit)
			}
			var out bytes.Buffer
			code := run([]string{"sh", "-c", script}, &out)

			if code != tc.code {
				t.Errorf("exit = %d, want %d, said:\n%s", code, tc.code, out.String())
			}
			for _, w := range tc.want {
				if !strings.Contains(out.String(), w) {
					t.Errorf("should say %q, said:\n%s", w, out.String())
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(out.String(), a) {
					t.Errorf("should not say %q, said:\n%s", a, out.String())
				}
			}
		})
	}
}

// The difference is taken in time, not in ownership. A suite that starts in
// another terminal while this one runs makes a directory that was not here
// before, and from out here that is indistinguishable from a leak — so it is
// reported, and the report has to say so rather than telling the reader to
// remove a directory something is still building into.
func TestADirectoryThatAppearsDuringTheRunIsReportedWithTheCaveat(t *testing.T) {
	tmp := tempdir(t)
	other := filepath.Join(tmp, "opossum-cmd-test-somebody-else")

	var out bytes.Buffer
	code := run([]string{"sh", "-c", fmt.Sprintf("mkdir %q; sleep 0.2", other)}, &out)
	if code != 1 {
		t.Errorf("exit = %d, want 1, said:\n%s", code, out.String())
	}
	// It is named — pretending not to see it would be worse.
	if !strings.Contains(out.String(), "opossum-cmd-test-somebody-else") {
		t.Errorf("should name what appeared, said:\n%s", out.String())
	}
	// And the two things the old wording got wrong: it must not claim these are
	// this run's, and it must not hand over an unconditional removal.
	for _, w := range []string{
		"these appeared in ",
		"a suite started in another terminal while this one ran looks exactly the same",
		"once nothing is using them:",
	} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("should say %q, said:\n%s", w, out.String())
		}
	}
	// Nothing in the tree says this today, so no mutation reaches this line —
	// it is here to stop the ownership claim coming back, not to catch anything
	// now. The `want` above is what does the work.
	if strings.Contains(out.String(), "this run left") {
		t.Errorf("must not claim these are this run's, said:\n%s", out.String())
	}
}

// The pattern handed to pgrep is the whole path. A base name would match any
// process anywhere whose command line happens to contain it, and this prints
// `kill` for whatever comes back.
func TestPgrepIsAskedAboutTheWholePath(t *testing.T) {
	tmp := tempdir(t)
	shim := t.TempDir()
	asked := filepath.Join(shim, "asked")
	shimBody := "#!/bin/sh\nprintf '%s\\n' \"$2\" >> " + asked + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(shim, "pgrep"), []byte(shimBody), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := filepath.Join(tmp, "opossum-cmd-test-whole")
	var out bytes.Buffer
	run([]string{"sh", "-c", fmt.Sprintf("mkdir %q", dir)}, &out)

	b, err := os.ReadFile(asked)
	if err != nil {
		t.Fatalf("pgrep was never asked anything: %v\nsaid:\n%s", err, out.String())
	}
	if got := strings.TrimSpace(string(b)); got != dir {
		t.Errorf("pgrep was asked about %q, want the whole path %q", got, dir)
	}
}

// A directory left behind is untidy. A directory left behind with the binary
// inside it still running is the thing this exists for.
func TestAProcessStillRunningOutOfTheLeftoverIsNamed(t *testing.T) {
	tmp := tempdir(t)
	dir := filepath.Join(tmp, "opossum-cmd-test-alive")
	bin := filepath.Join(dir, "opossum")
	pidFile := filepath.Join(dir, "pid")

	// The command makes the directory, starts something out of it, and dies —
	// the same order a timing-out suite dies in. The child's output goes to
	// /dev/null so it does not hold this test open until the child is gone.
	//
	// And it does not die until the child is real. The look that follows the
	// command's death finds the child by its command line, which only carries
	// the directory once the script has exec'd — a child still between fork
	// and exec is invisible to it, and on a saturated machine the command's
	// exit used to win that race (the clean-container sieve lost it about
	// every other run). So the script's first act is to say it has started —
	// a file only the post-exec script can write — and the command waits for
	// that word before dying. The situation is then established, not likely.
	ready := filepath.Join(dir, "ready")
	t.Cleanup(func() { killUnder(dir) })
	var out bytes.Buffer
	code := run([]string{"sh", "-c", fmt.Sprintf(
		"mkdir %[1]q; printf '#!/bin/sh\\n: > %[4]q\\nsleep 30\\n' > %[2]q; chmod +x %[2]q; "+
			"%[2]q >/dev/null 2>&1 & echo $! > %[3]q; "+
			"n=0; while [ $n -lt 400 ] && [ ! -e %[4]q ]; do sleep 0.01; n=$((n+1)); done",
		dir, bin, pidFile, ready)}, &out)

	// Read before anything else is asserted: if the child was never there, this
	// case looked at an empty world and every check below passes for the wrong
	// reason.
	pid := livePidFrom(t, pidFile)
	if pid == 0 {
		t.Fatalf("nothing was left running, so this never entered the situation it guards, said:\n%s", out.String())
	}

	if code != 1 {
		t.Errorf("exit = %d, want 1, said:\n%s", code, out.String())
	}
	want := fmt.Sprintf("\nstill running out of opossum-cmd-test-alive: %d\n`kill %d` ends them\n", pid, pid)
	if !strings.Contains(out.String(), want) {
		t.Errorf("should say word for word:\n%s\nsaid:\n%s", want, out.String())
	}
}

// A look that could not happen is not a look that found nothing. The in-suite
// check learned this the hard way; the distinction has to survive out here, or
// a machine without pgrep reports every leak as "just a directory".
func TestPgrepNotAnsweringIsNotTheSameAsNothingRunning(t *testing.T) {
	tmp := tempdir(t)
	shim := t.TempDir()
	if err := os.WriteFile(filepath.Join(shim, "pgrep"), []byte("#!/bin/sh\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))

	var out bytes.Buffer
	code := run([]string{"sh", "-c", fmt.Sprintf("mkdir %q", filepath.Join(tmp, "opossum-cmd-test-blind"))}, &out)
	if code != 1 {
		t.Errorf("exit = %d, want 1, said:\n%s", code, out.String())
	}
	want := "\ncould not look for processes still running out of opossum-cmd-test-blind\n" +
		"so this says nothing about whether any are — the note above is why\n"
	if !strings.Contains(out.String(), want) {
		t.Errorf("should say word for word:\n%s\nsaid:\n%s", want, out.String())
	}
	// And it must not also claim a finding: "nothing running" and "could not
	// look" are the two answers this exists to keep apart.
	if strings.Contains(out.String(), "still running out of opossum-cmd-test-blind:") {
		t.Errorf("a failed look must not read as a finding, said:\n%s", out.String())
	}
}

// pgrep answering "nothing matched" is an answer, and must stay silent rather
// than reporting a failed look.
func TestPgrepFindingNothingIsSilent(t *testing.T) {
	tmp := tempdir(t)
	shim := t.TempDir()
	if err := os.WriteFile(filepath.Join(shim, "pgrep"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))

	var out bytes.Buffer
	run([]string{"sh", "-c", fmt.Sprintf("mkdir %q", filepath.Join(tmp, "opossum-cmd-test-quiet"))}, &out)
	for _, unwanted := range []string{"could not look", "still running out of"} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("should not say %q when pgrep answered, said:\n%s", unwanted, out.String())
		}
	}
	// The directory is still reported: only the process line is silent.
	if !strings.Contains(out.String(), "opossum-cmd-test-quiet") {
		t.Errorf("the leftover itself should still be named, said:\n%s", out.String())
	}
}

// A command that cannot be started at all is not a clean run.
func TestACommandThatWillNotStartIsNotGreen(t *testing.T) {
	tempdir(t)
	var out bytes.Buffer
	code := run([]string{filepath.Join(t.TempDir(), "there-is-no-such-thing")}, &out)
	if code == 0 {
		t.Errorf("exit = 0, want non-zero, said:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "could not run") {
		t.Errorf("should say it could not run the command, said:\n%s", out.String())
	}
}

// $TMPDIR, not a fixed /tmp: the suites build where os.MkdirTemp puts them, and
// on macOS that is a per-user directory nothing else calls /tmp.
func tempdir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	t.Setenv("TMPDIR", d)
	if os.TempDir() != d {
		t.Skipf("os.TempDir() does not follow TMPDIR here (%s)", os.TempDir())
	}
	return d
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func livePidFrom(t *testing.T, path string) int {
	t.Helper()
	for i := 0; i < 100; i++ {
		b, err := os.ReadFile(path)
		if err == nil {
			if pid, cerr := strconv.Atoi(strings.TrimSpace(string(b))); cerr == nil {
				if syscall.Kill(pid, 0) == nil {
					return pid
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return 0
}

func killUnder(dir string) {
	out, _ := exec.Command("pgrep", "-f", dir).Output()
	for _, f := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(f); err == nil {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
	}
}

// ^C is the third way a run ends before its own cleanup, and the only one where
// the watcher is killed too: the signal goes to the process group, so without
// asking to survive it this would die alongside the thing it is watching.
//
// This one runs the built command rather than run(), because what is being
// checked is what the process does when a signal arrives — which is a property
// of the program, not of the function.
func TestAnInterruptStillGetsAReport(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "noleftovers")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("building: %v\n%s", err, out)
	}
	tmp := t.TempDir()
	leak := filepath.Join(tmp, "opossum-cmd-test-interrupted")

	// Long enough that a run whose interrupt never lands takes 300s and fails
	// the elapsed check below, rather than finishing on its own and looking
	// exactly like a run that was interrupted. A short sleep made those two
	// indistinguishable, and the test passed for the wrong reason.
	cmd := exec.Command(bin, "sh", "-c", fmt.Sprintf("mkdir %q; sleep 300", leak))
	cmd.Env = append(os.Environ(), "TMPDIR="+tmp)
	said := saidInto(t, cmd)
	// Its own group, so the interrupt below reaches it and its child without
	// reaching the test binary running this.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })

	waitForSleeper(t, cmd.Process.Pid, leak)
	if _, err := os.Stat(leak); err != nil {
		t.Fatalf("nothing was leaked to report: %v", err)
	}

	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	sent := time.Now()
	err := cmd.Wait()
	nothingLeftInTheGroup(t, cmd.Process.Pid)
	if took := time.Since(sent); took > 30*time.Second {
		t.Errorf("took %s to come back: the interrupt is not what ended this", took)
	}
	if err == nil {
		t.Errorf("an interrupted run that leaked should not be green, said:\n%s", said())
	}
	if !strings.Contains(said(), "opossum-cmd-test-interrupted") {
		t.Errorf("an interrupt must still be told about, said:\n%s", said())
	}
	// A signal is not an exit. `ExitCode()` says -1 for one, and passing that on
	// printed a number that is not a status and called an interruption a
	// failure of the tests.
	if !strings.Contains(said(), "the command was killed by interrupt rather than exiting") {
		t.Errorf("should say it was killed rather than that it exited, said:\n%s", said())
	}
	for _, wrong := range []string{"exited -1", "it failed as well"} {
		if strings.Contains(said(), wrong) {
			t.Errorf("must not say %q about a signalled command, said:\n%s", wrong, said())
		}
	}
	// 128+n, as a shell writes it. Without this the number is only promised in
	// prose: a mutation returning the bare signal number survived.
	if got := cmd.ProcessState.ExitCode(); got != 130 {
		t.Errorf("exit = %d, want 130 (128 + SIGINT), said:\n%s", got, said())
	}
}

// A command that catches ^C for itself — `go test` does, and that is the
// command the Makefile passes — exits normally afterwards. Its status carries
// no trace of the interrupt: `go test` answers 1, the same as a run whose tests
// failed. Read from the command's status alone, this run was a failure; it was
// not, and saying so was the thing being fixed.
func TestAnInterruptTheCommandHandlesItselfIsStillCalledAnInterrupt(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "noleftovers")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("building: %v\n%s", err, out)
	}

	for _, tc := range []struct {
		name string
		// What the command answers after catching the signal itself.
		exits int
		// What this must answer. Both are 1: a run that leaked does not pass,
		// and the guard that says so lives in the interrupted branch as much as
		// in the ordinary one — a mutation deleting the copy in the interrupted
		// branch let a leaking run through and no test noticed.
		want int
	}{
		{"the command exits 1 after catching it", 1, 1},
		{"the command exits 0 after catching it", 0, 1},
		// A third status, because with only 0 and 1 every row wants 1 and the
		// table is a constant wearing a table's clothes: a branch that always
		// answered 1 passed both rows. The command's own status is what this
		// whole tool is about keeping.
		{"the command exits 7 after catching it", 7, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			leak := filepath.Join(tmp, "opossum-cmd-test-handled")

			// `trap` makes this shell behave the way `go test` does: it hears
			// the interrupt, tidies up, and exits with a status of its own
			// choosing.
			cmd := exec.Command(bin, "sh", "-c", fmt.Sprintf(
				"trap 'exit %d' INT; mkdir %q; sleep 300", tc.exits, leak))
			cmd.Env = append(os.Environ(), "TMPDIR="+tmp)
			said := saidInto(t, cmd)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })

			waitForSleeper(t, cmd.Process.Pid, leak)

			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
				t.Fatal(err)
			}
			sent := time.Now()
			_ = cmd.Wait()
			nothingLeftInTheGroup(t, cmd.Process.Pid)
			if took := time.Since(sent); took > 30*time.Second {
				t.Errorf("took %s to come back: the interrupt is not what ended this", took)
			}
			// Said before the answer is judged: if the trap did not take, the
			// shell died of the signal and this case never reached the branch
			// it is about — a different thing from the tool answering wrongly,
			// and the two must not share a message.
			if got := cmd.ProcessState.ExitCode(); got == 130 {
				t.Fatalf("the trap did not take, so the command was signalled rather than choosing "+
					"its own status: this never entered the situation it guards, said:\n%s", said())
			}
			if got := cmd.ProcessState.ExitCode(); got != tc.want {
				t.Errorf("exit = %d, want %d, said:\n%s", got, tc.want, said())
			}
			// The command chose its own status, so nothing in it says
			// "interrupted" — that has to come from the signal we were sent.
			want := fmt.Sprintf("this run was interrupted; the command handled that itself and exited %d,\n"+
				"so its status says nothing about whether the tests were going to pass\n", tc.exits)
			if !strings.Contains(said(), want) {
				t.Errorf("should say word for word:\n%s\nsaid:\n%s", want, said())
			}
			if strings.Contains(said(), "it failed as well as leaving these") {
				t.Errorf("an interrupted run is not a failed one, said:\n%s", said())
			}
		})
	}
}

// waitForSleeper waits until the `sleep` the command started is running, and
// not merely until the leak is there. `mkdir` finishes before the sleeper is
// forked, and a SIGINT that arrives in that window is dropped by the shell while
// it waits on a foreground job: the sleep then starts with nobody left to
// interrupt it.
//
// It has to be the sleeper itself, and it has to be the running process. A
// marker the command writes just before `exec sleep 300` is not enough — it says
// the child exists, not that it has become something a signal ends. Measured
// over 200 runs of the interrupted case: waiting on such a marker hung 9 times,
// with the shell, the sleeper and this tool all still alive; waiting on the
// process below hung 0 times. Waiting on the leak instead hung 7 in 60.
//
// Matched by name, not by command line: `sh -c "... sleep 300"` carries those
// words too, so -f answers before the sleeper exists at all. The -g narrows it
// to the group this test made, so another run's sleeper is not mistaken for
// this one's.
func waitForSleeper(t *testing.T, pgid int, leak string) {
	t.Helper()
	// Not a skip: a machine without pgrep would drop every interrupt case from
	// the suite while `go test` still printed ok, and the only sign would be a
	// SKIP line nobody passes -v to see.
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Fatalf("pgrep is not on PATH, so this cannot tell a sleeping command from one that never got there: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if exec.Command("pgrep", "-g", strconv.Itoa(pgid), "-x", "sleep").Run() == nil {
			return
		}
		if time.Now().After(deadline) {
			// A pgrep that answers "nothing matched" because the machine hides
			// other processes looks exactly like one answering about a command
			// that never got there. The leak is the one independent witness: if
			// it exists, the command did run, and the silence is more likely
			// pgrep's than the command's.
			if _, err := os.Stat(leak); err == nil {
				t.Fatalf("the command leaked but no sleeper was ever visible: either it never got that "+
					"far, or pgrep cannot see processes here — a sandbox that hides them answers exactly "+
					"like an empty match, and this cannot tell the two apart (leak: %s)", leak)
			}
			t.Fatalf("the command never got as far as leaking, so this never entered the situation it guards")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// saidInto points a command's stderr at a file rather than at a pipe.
//
// exec's own buffering makes a pipe, and Wait() then waits for EOF on it — which
// means waiting for every process that inherited the write end. A child that
// outlived the interrupt therefore turned into a 300-second hang with a stack
// trace, rather than a sentence naming what survived. A file has no such reader:
// Wait() returns when the command does, and what was written is read afterwards.
//
// That hang was doing real work, though — it was the only thing that noticed a
// survivor. nothingLeftInTheGroup replaces it deliberately. Measured with a
// command whose background sleeper ignores SIGINT: the pipe version failed after
// 90s with a timeout panic, the file version alone passed in 0.9s, and the file
// version with the check below fails in about a second by name.
//
// It does not fix the other hang, where Wait() is waiting on this tool itself
// (which survives interrupts by design) while the tool waits on its own child.
// waitForSleeper is what keeps that one from happening.
func saidInto(t *testing.T, cmd *exec.Cmd) func() string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stderr")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	cmd.Stderr = f
	return func() string {
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("reading what it said: %v", rerr)
		}
		return string(b)
	}
}

// nothingLeftInTheGroup fails if anything is still running in the command's
// process group.
//
// Not the same as "nothing the command started". A child that leaves the group
// is invisible here — and the product's own watcher leaves it on purpose
// (`Setsid` in internal/orchestrator/supervisor.go), which is exactly the kind
// of survivor this tool exists to find. (The tool itself does find it — its
// pgrep -f is not scoped to a group. This helper is the narrower one.) What this
// reaches is whatever is left in the group — in these tests, the fixture's own
// sleeper, which stays in it; the SIGKILL in t.Cleanup reaches no further, so
// the two limits are at least consistent.
//
// It exists because the tests used to get this guarantee by accident. Buffering
// stderr into a pipe makes Wait() block until every process that inherited the
// write end is gone, so a child that outlived the interrupt turned into a
// 300-second hang. Pointing stderr at a file removed the hang — and with it the
// only thing that noticed. Checked by name instead: same red, arriving in a
// second with a sentence rather than in five minutes with a stack trace.
//
// pgrep's error is dropped because waitForSleeper has already run in every
// caller, and it fails the test outright when pgrep is missing or cannot see
// processes. Remove that call, or run this before it, and this check becomes a
// silent no-op that no green run would ever reveal.
func nothingLeftInTheGroup(t *testing.T, pgid int) {
	t.Helper()
	out, _ := exec.Command("pgrep", "-g", strconv.Itoa(pgid)).Output()
	if left := strings.TrimSpace(string(out)); left != "" {
		t.Errorf("the interrupt left something running in the command's process group: %s", left)
	}
}
