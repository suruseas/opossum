package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/mutate"
	"github.com/suruseas/opossum/internal/suitedir"
)

// gitFixture writes the smallest module a baseline sweep can run over and
// commits it, so a worktree of HEAD has something to build.
func gitFixture(t *testing.T) string {
	t.Helper()
	mod := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":    "module example.com/m\n\ngo 1.24\n",
		"m.go":      "package m\n\nfunc Answer() int { return 42 }\n",
		"m_test.go": "package m\n\nimport \"testing\"\n\nfunc TestAnswer(t *testing.T) {\n\tif Answer() != 42 {\n\t\tt.Fatal(\"wrong\")\n\t}\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(mod, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"commit", "-q", "-m", "baseline"}} {
		cmd := exec.Command("git", append([]string{"-C", mod, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return mod
}

// ownTemp gives the test a temp dir of its own to be the temp dir. The baseline
// worktrees land inside it, so the leftover checks in this file see only what
// this test made — a bare glob would also name, and then remove, the working
// directory of anyone else running a baseline sweep on the same machine.
//
// Made through suitedir rather than beside it. cmd/noleftovers looks for
// opossum-* directly under the temp dir, and a directory named after the test
// would sit outside that: the runs the net exists for — a panic, a -timeout,
// where no t.Cleanup gets a turn — would leave the worktrees somewhere nothing
// looks. Putting one there under our own name is not enough either, because
// then nothing removes it when this run dies; suitedir puts the pid in the name
// so a later run can tell an abandoned directory from a live one.
func ownTemp(t *testing.T) {
	t.Helper()
	dir, err := suitedir.Make("opossum-mutate-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("TMPDIR", dir)
}

// holdingHook writes a `post-checkout` hook that keeps whatever pipes it is
// given until this test lets go, and hands back the file it announces itself
// in. It sleeps rather than polls: this runs beside a package that measures how
// much CPU four workers can burn, and a hook that woke twenty times a second to
// look at a file was taking some of what that package was trying to count.
//
// Ended by killing the pid it records — the sleep is the process, so there is
// no child left behind it — with the sleep's own length as the second way out
// for a test that dies before it can.
func holdingHook(t *testing.T, mod string, background bool) string {
	t.Helper()
	dir := t.TempDir()
	pidFile, running := filepath.Join(dir, "pid"), filepath.Join(dir, "running")
	hold := "echo $$ > " + pidFile + "; touch " + running + "; exec sleep 30"
	body := "#!/bin/sh\n" + hold + "\n"
	if background {
		// Backgrounded, so git exits while this still holds the pipes it
		// inherited. Its own shell, so the pid recorded is the one to end.
		body = "#!/bin/sh\nsh -c '" + hold + "' &\n"
	}
	if err := os.WriteFile(filepath.Join(mod, ".git", "hooks", "post-checkout"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return running
}

// baselineDirs names the baseline worktrees currently under the temp dir.
//
// It reports through t.Errorf rather than t.Fatal because the interrupt tests
// call it from the handler's goroutine, where a Fatal ends the wrong one. And
// it does report: the pattern carries $TMPDIR, so a temp dir with a bracket in
// its name is a syntax error, and swallowing that would turn every leftover
// check here into one that finds nothing and says so approvingly.
func baselineDirs(t *testing.T) map[string]bool {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(os.TempDir(), "opossum-mutate-baseline-*"))
	if err != nil {
		t.Errorf("listing baseline worktrees: %v — the leftover checks in this file "+
			"cannot see anything while this is true", err)
	}
	set := map[string]bool{}
	for _, f := range found {
		set[f] = true
	}
	return set
}

// Cleanup and creation must not overlap. The interrupt handler removes the
// baseline worktree, and `git worktree add` writes into that same directory;
// removing one while the other fills it can end with the directory still
// there, by more than one route — the last step of a removal failing on a
// directory that is no longer empty, or the removal finishing and the add
// making it again. Which of them a given run takes is not something this
// pins, and neither is it what the fix depends on: no overlap, no route.
//
// The other interrupt test fires as soon as the directory exists and then
// depends on which of the two wins, which is why it passes on a fast disk and
// fails on a slow one. This one holds a creation in flight and interrupts
// exactly there, so the order is the same on every machine.
func TestTheHandlerWaitsForATreeThatIsStillBeingMade(t *testing.T) {
	mod := gitFixture(t)
	t.Chdir(mod)
	ownTemp(t)
	preexisting := baselineDirs(t)
	// The suite's tidiness must not depend on the thing this test is checking.
	// When the handler fails to run, the assertions below say so; this still
	// leaves $TMPDIR as it was found, so the next test is not handed a
	// directory that looks like its own leak.
	t.Cleanup(func() {
		for d := range baselineDirs(t) {
			if !preexisting[d] {
				_ = os.RemoveAll(d)
			}
		}
	})

	// The seam: the creation announces itself and then waits, so the interrupt
	// below lands in the middle of it rather than wherever the disk decides.
	inFlight := make(chan struct{})
	release := make(chan struct{})
	realAdd := addWorktree
	addWorktree = func(ctx context.Context, cwd, tree, sha string) error {
		// Make the tree for real first, under a context of its own so the
		// interrupt below cannot stop it. What the handler then has to take
		// apart is a worktree git has actually registered — the state its own
		// git commands exist for — rather than a name nothing was ever made
		// under. Holding afterwards is what keeps the creation in flight.
		if err := realAdd(context.Background(), cwd, tree, sha); err != nil {
			return err
		}
		close(inFlight)
		<-release
		return nil
	}
	t.Cleanup(func() { addWorktree = realAdd })

	sigs := make(chan os.Signal, 1)
	type atExit struct {
		code      int
		leftovers []string
	}
	exited := make(chan atExit, 4)
	exit := func(c int) {
		var left []string
		for d := range baselineDirs(t) {
			if !preexisting[d] {
				left = append(left, d)
			}
		}
		exited <- atExit{code: c, leftovers: left}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		var out bytes.Buffer
		errOut := &lockedBuf{}
		run([]string{"-baseline", "HEAD", spec(t, sweepFor("func Answer() int { return 43 }"))},
			&out, errOut, sigs, exit)
	}()

	// Every exit from here lets the creation go and waits for the run: leaving
	// it parked would have t.Cleanup put addWorktree back while the run is
	// still reading it, and would hand the next test a directory this one made.
	stopRun := func() {
		select {
		case <-release:
		default:
			close(release)
		}
		<-done
	}
	select {
	case <-inFlight:
	case <-time.After(60 * time.Second):
		stopRun()
		t.Fatal("the baseline worktree was never created, so this measured nothing")
	}
	sigs <- os.Interrupt

	// A wall clock, but only in the safe direction: a handler that waits sends
	// nothing here, so no machine is slow enough to fail this. A machine slow
	// enough to matter makes this pass more easily, not less — which is the
	// wrong way round, and the reason the interrupt lands at a held creation
	// rather than at whatever moment the disk happens to be in.
	//
	// While the creation is held, the handler has nothing it can safely remove.
	// Exiting here is the defect: it ends the process with a directory that is
	// still gaining entries, which is how the parent survives the removal.
	select {
	case e := <-exited:
		stopRun()
		t.Fatalf("the run exited while the baseline worktree was still being made "+
			"(code %d, leftovers %v): cleanup ran beside the creation instead of after "+
			"it. What that overlap leaves behind varies — this pins the order, not the "+
			"leftover", e.code, e.leftovers)
	case <-time.After(2 * time.Second):
	}

	close(release)
	select {
	case e := <-exited:
		if e.code != exitInterrupted {
			t.Errorf("exit = %d, want %d", e.code, exitInterrupted)
		}
		if len(e.leftovers) > 0 {
			t.Errorf("the worktree outlived the handler: %v", e.leftovers)
		}
	case <-time.After(60 * time.Second):
		<-done
		t.Fatal("the signal was never heard: nothing tried to exit")
	}
	<-done

	// Removing the directory is only half of it: a real `git worktree add` ran
	// here, so git holds a record of that tree. Cleanup's own git commands are
	// what release it, and a record left behind names a tree nobody can visit.
	out, err := exec.Command("git", "-C", mod, "worktree", "list", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("git worktree list: %v\n%s", err, out)
	}
	// git answers in resolved paths, and on macOS the temp dir is reached
	// through a link, so the fixture's own path has to be resolved to be
	// recognised in the answer.
	self, err := filepath.EvalSymlinks(mod)
	if err != nil {
		t.Fatal(err)
	}
	var sawSelf bool
	for _, line := range strings.Split(string(out), "\n") {
		rest, ok := strings.CutPrefix(line, "worktree ")
		switch {
		case !ok:
		case rest == self:
			sawSelf = true
		default:
			t.Errorf("git still has a worktree registered at %s: the directory is gone but "+
				"the record is not, so `git worktree list` names a tree that is not there", rest)
		}
	}
	// Without this, an answer that named nothing at all — a changed format, an
	// empty read — would read as "no worktree left behind" and pass.
	if !sawSelf {
		t.Errorf("the listing did not name the repository itself (%s), so it was not read "+
			"the way this check assumes:\n%s", self, out)
	}
}

// The wait above is only safe because the thing being waited for can end. An
// interrupt cancels the run's context before it asks for cleanup, so a `git
// worktree add` that is stuck on a slow or wedged disk dies rather than
// holding the handler forever — the failure that a wait without a way out
// would trade the leftover for, and the worse of the two.
func TestCreatingTheTreeEndsWhenTheRunIsCancelled(t *testing.T) {
	mod := gitFixture(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tree := filepath.Join(t.TempDir(), "tree")
	if err := addWorktree(ctx, mod, tree, "HEAD"); err == nil {
		t.Error("a cancelled run still made the tree: nothing here would end an add that hangs, " +
			"so cleanup would wait for it as long as the disk took")
	}
	if _, err := os.Stat(tree); err == nil {
		t.Errorf("%s exists: the cancelled add still wrote a tree, which is the directory "+
			"the handler would then have to remove", tree)
	}
}

// The wait is only bounded because the interrupt reaches the creation, and it
// reaches it through the run's own context. A creation handed some other
// context waits on a `release` that never comes, and so does the handler behind
// it — which is the failure the context is there to prevent. Checking that
// addWorktree honours whatever context it is given would not see this: what
// this pins is which context compareAgainst hands it.
func TestTheInterruptReachesACreationThatIsStillWaiting(t *testing.T) {
	mod := gitFixture(t)
	t.Chdir(mod)
	ownTemp(t)
	preexisting := baselineDirs(t)
	t.Cleanup(func() {
		for d := range baselineDirs(t) {
			if !preexisting[d] {
				_ = os.RemoveAll(d)
			}
		}
	})

	inFlight := make(chan struct{})
	// Never closed. The only way out of the creation is the context, so if the
	// run's own context does not arrive, nothing here ever finishes.
	release := make(chan struct{})
	realAdd := addWorktree
	addWorktree = func(ctx context.Context, cwd, tree, sha string) error {
		close(inFlight)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return realAdd(ctx, cwd, tree, sha)
	}
	t.Cleanup(func() { addWorktree = realAdd })

	sigs := make(chan os.Signal, 1)
	exited := make(chan int, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var out bytes.Buffer
		errOut := &lockedBuf{}
		run([]string{"-baseline", "HEAD", spec(t, sweepFor("func Answer() int { return 43 }"))},
			&out, errOut, sigs, func(c int) { exited <- c })
	}()

	select {
	case <-inFlight:
	case <-time.After(60 * time.Second):
		t.Fatal("the creation never started, so this measured nothing")
		// The run is parked in the creation on purpose; nothing here can free
		// it, which is what the timeout above is reporting.
	}
	sigs <- os.Interrupt

	select {
	case c := <-exited:
		if c != exitInterrupted {
			t.Errorf("exit = %d, want %d", c, exitInterrupted)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the interrupt never reached the creation: it is waiting on a context " +
			"the run cannot cancel, so cleanup waits behind it and ^C never returns")
	}
	<-done
}

// Where the other interrupt tests look is one step too early. They read the
// moment the handler calls exit, and the handler is careful by then. What ends
// the process is os.Exit(run(...)), so what matters is the moment run comes
// back: if it beats the handler to that, the removal the handler is partway
// through is cut off, and the status the handler chose is replaced by whatever
// run was going to say.
//
// The creation is cancellable, which is what makes this reachable at all — an
// interrupt cancels it, compareAgainst gets an error and has almost nothing
// left to do, while the handler still has a restore, two git commands and a
// removal ahead of it.
func TestTheRunDoesNotOutrunTheHandlerItInterrupted(t *testing.T) {
	mod := gitFixture(t)
	t.Chdir(mod)
	ownTemp(t)
	preexisting := baselineDirs(t)
	t.Cleanup(func() {
		for d := range baselineDirs(t) {
			if !preexisting[d] {
				_ = os.RemoveAll(d)
			}
		}
	})

	inFlight := make(chan struct{})
	realAdd := addWorktree
	addWorktree = func(ctx context.Context, cwd, tree, sha string) error {
		close(inFlight)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(60 * time.Second):
			return realAdd(ctx, cwd, tree, sha)
		}
	}
	t.Cleanup(func() { addWorktree = realAdd })

	sigs := make(chan os.Signal, 1)
	type atExit struct {
		code      int
		leftovers []string
	}
	exited := make(chan atExit, 4)
	returned := make(chan int, 1)
	errOut := &lockedBuf{}
	go func() {
		var out bytes.Buffer
		returned <- run([]string{"-baseline", "HEAD", spec(t, sweepFor("func Answer() int { return 43 }"))},
			&out, errOut, sigs, func(c int) {
				// os.Exit is here in the real command, so what is on disk at
				// this instant is what survives. Whoever took the cleanup has
				// to have finished it by now, and on this path that is the
				// handler: a run that got there first would still be removing.
				var left []string
				for d := range baselineDirs(t) {
					if !preexisting[d] {
						left = append(left, d)
					}
				}
				exited <- atExit{code: c, leftovers: left}
			})
	}()

	select {
	case <-inFlight:
	case <-time.After(60 * time.Second):
		t.Fatal("the creation never started, so this measured nothing")
	}
	sigs <- os.Interrupt

	var code int
	select {
	case code = <-returned:
	case <-time.After(90 * time.Second):
		t.Fatal("run never came back")
	}

	// Read at the moment os.Exit would have been handed this status.
	var left []string
	for d := range baselineDirs(t) {
		if !preexisting[d] {
			left = append(left, d)
		}
	}
	if len(left) > 0 {
		t.Errorf("run returned while the handler was still removing %v: os.Exit takes that "+
			"status immediately, so what the handler had not finished stays on disk", left)
	}
	select {
	case e := <-exited:
		if e.code != exitInterrupted {
			t.Errorf("the handler chose exit %d, want %d", e.code, exitInterrupted)
		}
		if len(e.leftovers) > 0 {
			t.Errorf("at the moment the process would have gone, %v was still there: the "+
				"cleanup was taken by whoever was not going to finish it", e.leftovers)
		}
	default:
		t.Errorf("run returned %d before the handler chose a status: os.Exit would carry "+
			"this one, and an interrupted run would report itself as a failed one", code)
	}
	// What the cancelled comparison made of itself is not news, and reads as a
	// verdict on the ref that was picked. The handler says the true thing.
	if got := errOut.String(); strings.Contains(got, baselineFailure) {
		t.Errorf("an interrupted run blamed the comparison:\n%s", got)
	}
}

// The other side of that silence. Only an interrupt earns it: a comparison that
// failed on its own has nothing else to say for it, and swallowing that would
// leave a run that returns a failure with no reason anywhere.
func TestAComparisonThatFailsOnItsOwnStillSaysSo(t *testing.T) {
	mod := gitFixture(t)
	t.Chdir(mod)
	// The directory is made before the creation is asked for, so this leaves
	// one behind for the run's own cleanup to take. Kept where the temp dir
	// goes with the test.
	ownTemp(t)
	realAdd := addWorktree
	addWorktree = func(ctx context.Context, cwd, tree, sha string) error {
		return errors.New("the tree could not be made")
	}
	t.Cleanup(func() { addWorktree = realAdd })

	var out bytes.Buffer
	errOut := &lockedBuf{}
	code := run([]string{"-baseline", "HEAD", spec(t, sweepFor("func Answer() int { return 43 }"))},
		&out, errOut, nil, func(int) {})
	if code != exitFailed {
		t.Errorf("exit = %d, want %d", code, exitFailed)
	}
	if got := errOut.String(); !strings.Contains(got, baselineFailure+"the tree could not be made") {
		t.Errorf("the failure went unreported:\n%s", got)
	}
}

// Cancelling reaches git at once; getting its output back does not. The output
// is read until every holder of the pipes git handed out has gone, and git
// hands them to its own children — here a `post-checkout` hook, which is what
// a smudge filter, an LFS fetch or a background auto-gc would look like from
// this side. Waiting for those is exactly the "^C does not come back" that
// making the creation cancellable was for, arriving by the other door.
//
// This one runs the real git, with a real hook, and interrupts while the hook
// is running. What it measures is not the hook — it is how long the command
// takes to come back after it has been told to stop.
func TestAnInterruptDoesNotWaitOutTheChildrenOfACancelledGit(t *testing.T) {
	mod := gitFixture(t)
	t.Chdir(mod)
	ownTemp(t)
	preexisting := baselineDirs(t)
	t.Cleanup(func() {
		for d := range baselineDirs(t) {
			if !preexisting[d] {
				_ = os.RemoveAll(d)
			}
		}
	})

	// The hook says when it is running and then holds the pipes, so the
	// interrupt lands inside it and what comes back is a measurement rather
	// than a race.
	running := holdingHook(t, mod, false)

	sigs := make(chan os.Signal, 1)
	exited := make(chan int, 4)
	returned := make(chan int, 1)
	go func() {
		var out bytes.Buffer
		errOut := &lockedBuf{}
		returned <- run([]string{"-baseline", "HEAD", spec(t, sweepFor("func Answer() int { return 43 }"))},
			&out, errOut, sigs, func(c int) { exited <- c })
	}()

	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(running); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the hook never ran, so the interrupt would not have landed inside git")
		}
		time.Sleep(5 * time.Millisecond)
	}
	sigs <- os.Interrupt
	sent := time.Now()

	select {
	case <-returned:
	case <-time.After(20 * time.Second):
		t.Fatal("the run never came back while the hook still held the pipes")
	}
	took := time.Since(sent)
	// The generous end of "came back promptly" is used on purpose: this is a
	// wall clock, and the two outcomes it separates are two orders of magnitude
	// apart, not two ticks.
	if took > 5*time.Second {
		t.Errorf("^C took %v to come back: the cancelled git was reached, but its output was "+
			"read until the children holding the pipes let go — which is the wait this path "+
			"exists to bound", took)
	}
	select {
	case c := <-exited:
		if c != exitInterrupted {
			t.Errorf("the handler chose exit %d, want %d", c, exitInterrupted)
		}
	default:
		t.Error("the run came back before the handler chose a status")
	}
}

// The delay that keeps ^C from waiting out git's children has a second half
// nobody asked for: it also fires when git finished perfectly well and someone
// it handed a pipe to is still holding on — a hook that backgrounded itself, an
// fsmonitor, an auto-gc. No interrupt is involved. Reported as a failure, that
// turns a worktree that was made correctly into a failed comparison, and the
// reason printed is git's own success message.
func TestAWorktreeMadeWhileAGrandchildHoldsOnIsNotAFailure(t *testing.T) {
	mod := gitFixture(t)
	t.Chdir(mod)
	ownTemp(t)

	holdingHook(t, mod, true)

	var out bytes.Buffer
	errOut := &lockedBuf{}
	code := run([]string{"-baseline", "HEAD", spec(t, sweepFor("func Answer() int { return 43 }"))},
		&out, errOut, nil, func(int) {})
	if got := errOut.String(); strings.Contains(got, baselineFailure) {
		t.Errorf("the comparison was called a failure though the tree was made:\n%s", got)
	}
	if code == exitFailed {
		t.Errorf("exit = %d: git succeeded and only its pipes outlived it", code)
	}
}

// The creation was one of two things that have their hands in the tree; the
// sweep is the other, and the longer one — minutes, against the creation's
// milliseconds. An interrupt during it used to run cleanup beside a `go test`
// still writing there (the toolchain outlived the interrupt), and after the
// toolchain learned to stop, beside the sweep's own last write — the mutation
// being put back. Either way the removal ran on a tree that was not idle, and
// what that leaves behind is the leftover with the caveat nobody can act on.
//
// This holds the sweep in flight and interrupts exactly there. The handler
// must wait: exiting while the sweep is parked is the defect, and the order is
// what this pins — not the leftover, which is the symptom and varies with the
// disk.
func TestTheHandlerWaitsForASweepStillRunningInTheTree(t *testing.T) {
	mod := gitFixture(t)
	t.Chdir(mod)
	ownTemp(t)
	preexisting := baselineDirs(t)
	t.Cleanup(func() {
		for d := range baselineDirs(t) {
			if !preexisting[d] {
				_ = os.RemoveAll(d)
			}
		}
	})

	// The seam: the baseline tree's runner is real — its reads and writes land
	// in the actual worktree, which is what the removal must not overlap —
	// but its first toolchain call announces itself and then waits. The wait
	// is what keeps the sweep in flight; what the call returns afterwards is
	// whatever the real toolchain says under a context the handler has by
	// then cancelled.
	inFlight := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	realNew := baselineRunner
	baselineRunner = func(tree string) *mutate.Runner {
		r := realNew(tree)
		realGo := r.Go
		r.Go = func(args ...string) (string, string, error) {
			once.Do(func() { close(inFlight) })
			<-release
			return realGo(args...)
		}
		return r
	}
	t.Cleanup(func() { baselineRunner = realNew })

	sigs := make(chan os.Signal, 1)
	type atExit struct {
		code      int
		leftovers []string
	}
	exited := make(chan atExit, 4)
	exit := func(c int) {
		var left []string
		for d := range baselineDirs(t) {
			if !preexisting[d] {
				left = append(left, d)
			}
		}
		exited <- atExit{code: c, leftovers: left}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		var out bytes.Buffer
		errOut := &lockedBuf{}
		run([]string{"-baseline", "HEAD", spec(t, sweepFor("func Answer() int { return 43 }"))},
			&out, errOut, sigs, exit)
	}()
	stopRun := func() {
		select {
		case <-release:
		default:
			close(release)
		}
		<-done
	}
	select {
	case <-inFlight:
	case <-time.After(120 * time.Second):
		stopRun()
		t.Fatal("the baseline sweep never reached the toolchain, so this measured nothing")
	}
	sigs <- os.Interrupt

	// Same shape as the creation test above, and the same direction of
	// safety: a handler that waits sends nothing here, so no machine is slow
	// enough to fail this by accident.
	select {
	case e := <-exited:
		stopRun()
		t.Fatalf("the run exited while the baseline sweep was still in the tree "+
			"(code %d, leftovers %v): cleanup ran beside the sweep instead of after it. "+
			"This pins the order, not the leftover", e.code, e.leftovers)
	case <-time.After(2 * time.Second):
	}

	close(release)
	select {
	case e := <-exited:
		if e.code != exitInterrupted {
			t.Errorf("exit = %d, want %d", e.code, exitInterrupted)
		}
		if len(e.leftovers) > 0 {
			t.Errorf("the worktree outlived the handler: %v", e.leftovers)
		}
	case <-time.After(60 * time.Second):
		<-done
		t.Fatal("the signal was never heard: nothing tried to exit")
	}
	<-done
}

// The test above parks the sweep at its first toolchain call, which is the
// baseline run — and during the baseline the sweep has written nothing into
// the tree. It proves the handler waits for a sweep, not that it waits for the
// part of one that matters: a lock narrowed to each toolchain call, leaving
// the mutation's write-in and put-back outside it, keeps that test green while
// reopening the exact window this closes. So this one parks the sweep on its
// second write into the tree — the restore of the first mutation, the last
// thing a cancelled round does there — and interrupts with the write in hand.
//
// The order is what is pinned, and by a flag rather than a clock: exit
// records whether the write had been let go of yet.
func TestTheHandlerWaitsForARestoreStillBeingWrittenIntoTheTree(t *testing.T) {
	mod := gitFixture(t)
	t.Chdir(mod)
	ownTemp(t)
	preexisting := baselineDirs(t)
	t.Cleanup(func() {
		for d := range baselineDirs(t) {
			if !preexisting[d] {
				_ = os.RemoveAll(d)
			}
		}
	})

	inFlight := make(chan struct{})
	release := make(chan struct{})
	var writes int
	var once sync.Once
	realNew := baselineRunner
	baselineRunner = func(tree string) *mutate.Runner {
		r := realNew(tree)
		realWrite := r.Write
		r.Write = func(p string, b []byte) error {
			writes++
			if writes == 2 {
				once.Do(func() { close(inFlight) })
				<-release
			}
			return realWrite(p, b)
		}
		return r
	}
	t.Cleanup(func() { baselineRunner = realNew })

	released := func() bool {
		select {
		case <-release:
			return true
		default:
			return false
		}
	}
	sigs := make(chan os.Signal, 1)
	type atExit struct {
		code      int
		early     bool // exit reached while the write was still parked
		leftovers []string
	}
	exited := make(chan atExit, 4)
	exit := func(c int) {
		var left []string
		for d := range baselineDirs(t) {
			if !preexisting[d] {
				left = append(left, d)
			}
		}
		exited <- atExit{code: c, early: !released(), leftovers: left}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		var out bytes.Buffer
		errOut := &lockedBuf{}
		run([]string{"-baseline", "HEAD", spec(t, sweepFor("func Answer() int { return 43 }"))},
			&out, errOut, sigs, exit)
	}()
	stopRun := func() {
		if !released() {
			close(release)
		}
		<-done
	}
	select {
	case <-inFlight:
	case <-time.After(120 * time.Second):
		stopRun()
		t.Fatal("the baseline sweep never got as far as restoring a mutation, so this measured nothing")
	}
	sigs <- os.Interrupt

	// Give a handler that does not wait every chance to show it: the clock
	// here only decides how long a correct handler is watched, and a correct
	// one sends nothing whatever the machine.
	select {
	case e := <-exited:
		stopRun()
		t.Fatalf("the run exited while the restore write was still parked in the tree "+
			"(code %d, leftovers %v): cleanup ran beside the sweep's own write instead of "+
			"after it", e.code, e.leftovers)
	case <-time.After(2 * time.Second):
	}

	close(release)
	select {
	case e := <-exited:
		if e.early {
			t.Errorf("the handler exited before the write was let go of")
		}
		if e.code != exitInterrupted {
			t.Errorf("exit = %d, want %d", e.code, exitInterrupted)
		}
		if len(e.leftovers) > 0 {
			t.Errorf("the worktree outlived the handler: %v", e.leftovers)
		}
	case <-time.After(60 * time.Second):
		<-done
		t.Fatal("the signal was never heard: nothing tried to exit")
	}
	<-done
}
