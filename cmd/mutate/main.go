// Command mutate runs a set of hand-written mutations and reports which tests
// each one killed.
//
// It is a repository tool, not part of the opossum binary (goreleaser builds only
// ./cmd/opossum).
//
//	mutate sweep.json                     apply each mutation, print a markdown table
//	mutate -baseline main sweep.json      the same, then the same sweep against that
//	                                      tree, and the difference between the two
//
// The difference is the part a pull request quotes — what this change newly
// guards, what was guarded anyway, what still is not — and it is printed with
// its counts summed over its own rows, because every time that table was
// assembled by hand a total ended up beside a breakdown that added to
// something else. A mutation whose text the baseline tree does not carry is a
// category of its own ("not present before this change"), not an error: a
// sweep written against the new tree is expected to name new sentences.
//
// The file is a list of mutations an author chose, not a set of rules for
// generating them:
//
//	[
//	  {
//	    "name": "the second look never happens",
//	    "file": "internal/orchestrator/orchestrator.go",
//	    "from": "if len(watching) == 0 || look > 0 {",
//	    "to":   "if true {",
//	    "packages": ["./internal/orchestrator/"]
//	  }
//	]
//
// Each `from` must appear exactly once in its file and must actually change
// something; the tree must still build; the failing tests are collected by name;
// and the file is put back and compared byte for byte. Any mutation nothing
// caught is reported in words rather than as an empty cell, because that row is
// the reason to run this.
//
// Interrupting it restores the file first. The usual reason to run this is to
// test work that isn't committed, so a mutation left behind by an interrupted run
// would be mixed into the next commit — and `git checkout` is not available as a
// way out for exactly the same reason.
//
// Before the first mutation is applied, the suite is run once as it stands. A
// test that is already failing fails again under every mutation, and a failing
// test is what this reads as "caught" — so one red test would make a whole sweep
// report that everything is guarded while measuring nothing. There is no way to
// carry on past it.
//
// Exit status: 0 only when every mutation was caught, 1 when one survived (a
// finding — the tool worked), 2 when the run did not measure something (a
// mutation that would not compile, a test run that named nobody, a sweep that
// could not be read, a suite that was already failing). "It found something" and
// "it never ran" must not look alike, and neither may look like "everything is
// guarded".
//
// Whether the tests appear to run a survivor's line is a note beside it, not an
// outcome and not part of the exit status. It is worth knowing and it is measured
// over the packages this sweep named, which is less than "every test"; reported
// as an outcome, a wrong reading turns "write a test" into "find out why this
// code is dead". As a note it can be wrong without moving anyone off the survivor
// in front of them.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/suruseas/opossum/internal/mutate"
)

// Exit statuses. A survivor is a result, not a malfunction, and a script driving
// this needs to tell "it ran and found something" from "it never ran".
const (
	exitAllCaught   = 0
	exitSurvivor    = 1
	exitFailed      = 2
	exitInterrupted = 130
)

func main() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, sigs, os.Exit))
}

// interruptMessage says what an interrupt actually did. Saying "the file has been
// put back" when no file was ever written tells the author something that did not
// happen — and with a baseline run in front of the sweep, the likeliest moment to
// interrupt is one where nothing has been written yet.
func interruptMessage(restored bool, err error) string {
	switch {
	case err != nil:
		return "\nmutate: interrupted, AND THE FILE COULD NOT BE PUT BACK: " + err.Error() +
			"\n  Check `git diff` before committing anything."
	case restored:
		return "\nmutate: interrupted — the file being mutated has been put back."
	default:
		return "\nmutate: interrupted with no mutation in flight — the tree is as you left it."
	}
}

// run is main without the process. Everything that decides an exit status lives
// here so it can be tested; main only supplies the real streams, signals, and the
// way out. `exit` is a parameter for the same reason the signal channel is: the
// interrupt path is the one that was wrong, so it has to be reachable from a
// test.
func run(args []string, stdout, stderr io.Writer, sigs <-chan os.Signal, exit func(int)) int {
	fs := flag.NewFlagSet("mutate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	baseline := fs.String("baseline", "", "a git ref: run the same sweep against that tree too, and print the difference")
	if err := fs.Parse(args); err != nil {
		return exitFailed
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: mutate [-baseline <ref>] <sweep.json>")
		return exitFailed
	}
	ms, err := load(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, "mutate: "+err.Error())
		return exitFailed
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "mutate: "+err.Error())
		return exitFailed
	}
	// Resolved before the sweep spends minutes: a ref that does not name a
	// commit should be today's first line of output, not the last.
	baseSHA := ""
	if *baseline != "" {
		baseSHA, err = gitOut(cwd, "rev-parse", "--verify", *baseline+"^{commit}")
		if err != nil {
			fmt.Fprintln(stderr, "mutate: -baseline "+*baseline+": "+err.Error())
			return exitFailed
		}
	}
	// Takes the toolchain with us: a `go test` that outlives this command holds
	// the pipes open, and so does the test binary underneath it — the runner
	// puts both in one process group and interrupts the group, because ending
	// only the first leaves the second adopted away (#607). "Takes" is as
	// strong as an interrupt: a test binary that catches it and stays is
	// beyond this — the runner's Ctx doc has the boundary.
	//
	// Three other places read it: making the baseline tree, the baseline
	// runner's own toolchain, and deciding below whether an interrupt is in
	// flight.
	ctx, stopToolchain := context.WithCancel(context.Background())
	defer stopToolchain()

	// started says an interrupt is being handled; handlerDone says the handling
	// is over. Two rather than one because the question below is "is the
	// handler on its way", and the context only answers "has the toolchain been
	// stopped" — which the handler does a step later, leaving a window where a
	// run could return out from under a handler that had already begun.
	started, handlerDone := make(chan struct{}), make(chan struct{})
	r := mutate.NewRunner(cwd)
	r.Ctx = ctx

	// The restore has to happen here, before the process goes away: os.Exit runs
	// no defers, so the sweep's own deferred restore never gets a turn. A handler
	// that only printed a warning would leave the author's uncommitted work
	// mutated — the exact accident this tool is for.
	// The baseline worktree, when there is one, has to go away on the
	// interrupt path too — os.Exit runs no defers, and a worktree left under
	// $TMPDIR is exactly the leftover the temp-dir net exists to catch.
	// Idempotent by construction: whoever runs it first takes the func.
	var cleanupMu sync.Mutex
	var cleanupWorktree func()
	setCleanup := func(f func()) { cleanupMu.Lock(); cleanupWorktree = f; cleanupMu.Unlock() }
	runCleanup := func() {
		cleanupMu.Lock()
		f := cleanupWorktree
		cleanupWorktree = nil
		cleanupMu.Unlock()
		if f != nil {
			f()
		}
	}
	defer runCleanup()

	// Once an interrupt is in flight the handler owns what happens next: it is
	// the only thing that restores the tree and removes the baseline worktree,
	// and it ends the process itself. Returning out from under it would hand
	// os.Exit a status the handler did not choose and cut its removal off
	// partway — which is how making the creation cancellable, further down,
	// would otherwise trade one leftover for another.
	//
	// Registered after the cleanup above so it runs before it: whoever takes
	// the cleanup func has to be the one that finishes it, and on this path
	// that is the handler. Waiting here first means the deferred cleanup below
	// finds nothing left to do rather than taking the work out from under a
	// handler that is about to end the process.
	defer func() {
		select {
		case <-started:
			<-handlerDone
		default:
		}
	}()

	done := make(chan struct{})
	go func() {
		select {
		case <-sigs:
		case <-done:
			return
		}
		// Both before anything that takes time. started first, so a run that
		// reaches its own way out from here on finds the handler and waits;
		// handlerDone deferred so that wait always ends, however this goes. In
		// the real command exit does not come back and nobody is left to
		// notice either one.
		close(started)
		defer close(handlerDone)
		stopToolchain()
		restored, rerr := r.RestorePending()
		runCleanup()
		fmt.Fprintln(stderr, interruptMessage(restored, rerr))
		exit(exitInterrupted)
	}()
	defer close(done)

	results, sweepErr := r.Sweep(ms)
	// An interrupt reaches this as a cancelled context, and what the sweep made
	// of it is not news. The likeliest place for a ^C to land is the baseline
	// run in front of everything, and a baseline cut off mid-run comes back as
	// "the suite could not be run" — told, without this, to the author who
	// pressed the ^C. The handler says the true thing and ends the process; the
	// baseline comparison below has carried this same guard all along, and this
	// side of it only became reachable when cancellation was wired through to
	// the toolchain (#607).
	if ctx.Err() != nil {
		return exitFailed
	}
	// The table and the counts go out together. Quoting the table into a pull
	// request and then writing the totals by hand is how a body ends up with a
	// total beside a breakdown that adds to something else.
	fmt.Fprint(stdout, mutate.Report(results)+mutate.Tally(results))
	if sweepErr != nil {
		fmt.Fprintln(stderr, "mutate: "+sweepErr.Error())
		return exitFailed
	}
	if baseSHA != "" {
		cmp, err := compareAgainst(ctx, cwd, *baseline, baseSHA, ms, results, stderr, setCleanup)
		if err != nil {
			// An interrupt reaches this as a cancelled context, and what it
			// makes of the comparison is not news: "context canceled", or a
			// tree the handler has already taken apart, reported as though the
			// ref were a bad choice. The handler says the true thing.
			if ctx.Err() == nil {
				fmt.Fprintln(stderr, baselineFailure+err.Error())
			}
			return exitFailed
		}
		fmt.Fprint(stdout, cmp)
		runCleanup()
	}
	if n := count(results, mutate.Survived); n > 0 {
		fmt.Fprintf(stderr, "mutate: %d of %d mutations survived — the defects they introduce are "+
			"invisible to the suite\n", n, len(results))
		return exitSurvivor
	}
	// A mutation that would not build, or a run that named nobody, measured
	// nothing. Reporting that as "all caught" would be the same lie in a quieter
	// place: the table says so, and the exit status has to agree with the table.
	if n := count(results, mutate.Broken) + count(results, mutate.Inconclusive); n > 0 {
		fmt.Fprintf(stderr, "mutate: %d of %d mutations measured nothing (they did not build, or "+
			"the run named no tests) — that is not the same as being caught\n", n, len(results))
		return exitFailed
	}
	return exitAllCaught
}

// compareAgainst runs the same sweep against ref's tree, in a worktree of its
// own, and renders the difference. The worktree lives under the temp dir with
// the repository's own prefix, so a leak lands in the same net every other
// leftover does; removal is registered with the interrupt handler before the
// first long-running thing happens inside it.
func compareAgainst(ctx context.Context, cwd, ref, sha string, ms []mutate.Mutation,
	now []mutate.Result, stderr io.Writer, setCleanup func(func())) (string, error) {
	// Cleanup must not overlap anything else that has its hands in the tree.
	// Removing a directory means reading it, deleting what was read, and then
	// asking for the directory itself; a `git worktree add` still running
	// underneath — or a sweep still writing a mutation in and putting it back —
	// can turn any of those steps into a removal that does not remove, and
	// every way it can is quiet: the only report is a return value on a path
	// whose caller is on its way to os.Exit. So both the creation and the sweep
	// hold this, and cleanup takes it: whenever cleanup is asked, it waits for
	// whichever is in flight rather than racing it. The wait is short by
	// construction — the handler cancels the context before it asks, and a
	// cancelled sweep lets go within milliseconds (the toolchain is interrupted,
	// and the next round is refused) — and bounded even when it is not: under a
	// test binary that ignores the interrupt, the runner's WaitDelay ends `go
	// test` alone five seconds later and Go returns, so the sweep lets go then;
	// the binary itself may outlive all of this (Runner.Ctx has that boundary).
	// Taken before the handler is given anything to run.
	var inUse sync.Mutex
	var parent, tree string
	cleanup := func() {
		inUse.Lock()
		defer inUse.Unlock()
		if parent == "" {
			// Only reachable when the directory was never made, so there is
			// nothing here to remove. Returning while the name is still unset
			// would otherwise spend the one cleanup the handler gets: it takes
			// the func before calling it, and does not put it back.
			return
		}
		// Forced: the tree usually holds a half-applied mutation when this
		// runs from the interrupt path, and asking politely would refuse.
		//
		// These are the last slow thing in front of ^C now that the run waits
		// for this handler, and nothing here bounds them. A delay was tried and
		// measured: against a git that will not exit it moved the wait by 14ms
		// out of 8.3 seconds, because the timer it starts needs the child to
		// have exited or the context to be done, and cleanup's context never
		// is. It bounds the other shape — git gone, a grandchild still holding
		// the pipes — and nothing here shows that shape happening, though
		// neither is it ruled out. Left unbounded rather than fitted with a
		// stopper for a case nobody has produced yet (#610).
		var reasons []string
		if _, err := gitOut(cwd, "worktree", "remove", "--force", tree); err != nil {
			reasons = append(reasons, err.Error())
			// The worktree metadata can outlive a directory that was removed
			// out from under git; prune is the documented way to reconcile.
			if _, perr := gitOut(cwd, "worktree", "prune"); perr != nil {
				reasons = append(reasons, perr.Error())
			}
		}
		if err := removeAll(parent); err != nil {
			reasons = append(reasons, err.Error())
		}
		// The verdict is the directory's final state, not the steps' returns.
		// A removal can fail without an error surviving to here — the race
		// this file closed had exactly that shape, and "something recreated
		// it" is a shape nothing above would report. Whatever the route, a
		// leftover the user was never told about costs them disk until they
		// stumble on it; a leftover with its path and a way out costs them a
		// minute.
		reportLeftover(stderr, "baseline worktree", parent, reasons)
		// And the registration is a leftover of its own: a directory can be
		// gone while git still lists the worktree — produced for real, as a
		// red, by a mutation in this file's own review. Checked only when the
		// directory is gone; while it stands, the report above already hands
		// the reader the prune.
		if _, err := os.Lstat(parent); err != nil {
			reportStaleListing(stderr, cwd, "baseline worktree", tree)
		}
	}
	// setCleanup is expected to store the func, not run it: this holds the lock
	// that cleanup takes, so a caller that ran it here would wait on itself.
	//
	// Both before the directory exists. Registering first would leave an
	// instant where the handler holds a cleanup that finds no name yet, spends
	// it, and lets whatever is created next go unremoved; taking the lock first
	// means any cleanup from here on waits and finds the name. An interrupt
	// that arrives before this line finds nothing registered, which is the same
	// as anywhere else in the run that has not made anything yet.
	if err := func() error {
		inUse.Lock()
		defer inUse.Unlock()
		setCleanup(cleanup)
		made, err := os.MkdirTemp("", "opossum-mutate-baseline-")
		if err != nil {
			return err
		}
		// Resolved while it exists, so the name kept here is the one git will
		// print: `git worktree list` answers in resolved paths, and on macOS
		// $TMPDIR reaches the same directory through a symlink. After the
		// removal there is no directory left to ask.
		if r, rerr := filepath.EvalSymlinks(made); rerr == nil {
			made = r
		}
		parent, tree = made, filepath.Join(made, "tree")
		return addWorktree(ctx, cwd, tree, sha)
	}(); err != nil {
		return "", err
	}
	applicable, fresh, ambiguous, err := mutate.Applicable(ms, func(p string) ([]byte, bool, error) {
		b, err := os.ReadFile(filepath.Join(tree, p))
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return b, true, err
	})
	if err != nil {
		return "", err
	}
	var before []mutate.Result
	if len(applicable) > 0 {
		rb := baselineRunner(tree)
		// The same ctx as the main runner, so cancellation reaches the
		// toolchain in both trees rather than only one. This is the tree it
		// matters most in: the sweep here is where the minutes go, and it is
		// this tree the interrupt handler then takes apart.
		rb.Ctx = ctx
		rb.Log = func(s string) { fmt.Fprintln(stderr, "baseline tree: "+s) }
		// Held for the whole sweep, not per round: every round writes into
		// the tree and puts it back, and the removal has to find the tree
		// idle. See inUse for why the hold is short once the context is gone.
		before, err = func() ([]mutate.Result, error) {
			inUse.Lock()
			defer inUse.Unlock()
			return rb.Sweep(applicable)
		}()
		if err != nil {
			// The inner message may say "make them pass first", which nobody
			// can do to a committed tree; what they can do is pick a ref
			// whose suite was green.
			return "", fmt.Errorf("against %s: %w\n  (the baseline tree's suite has to answer "+
				"cleanly for the comparison to mean anything — pick a ref where it does)", ref, err)
		}
	}
	short := sha
	if len(short) > 12 {
		short = short[:12]
	}
	return mutate.CompareReport(ref+" ("+short+")", now, before, fresh, ambiguous), nil
}

// baselineRunner makes the runner that sweeps the baseline tree. A variable
// for the same reason addWorktree is: what the handler must not overlap is a
// sweep in flight, and a test can only put one in flight — and interrupt
// exactly there, on every machine alike — by standing in for the toolchain.
var baselineRunner = mutate.NewRunner

// removeAll is a variable for the same reason addWorktree is: the report
// below exists for the runs where removal fails, and a test that cannot make
// removal fail is a test of the runs that never needed the report.
var removeAll = os.RemoveAll

// reportStaleListing speaks when git still lists a worktree whose directory
// is gone. The two can part ways: `git worktree remove` under a dead context
// leaves the registration while the directory falls to the later RemoveAll —
// and a stale registration silently refuses the next `git worktree add` at
// that path. Called only once the directory is known gone; while it stands,
// the leftover report already hands the reader the prune. tree must be the
// resolved path — the form git prints — because with the directory gone
// there is nothing left here to resolve.
func reportStaleListing(stderr io.Writer, cwd, what, tree string) {
	out, err := gitOut(cwd, "worktree", "list", "--porcelain")
	// A whole porcelain line, not a substring: another worktree's path could
	// carry this one's as a prefix.
	if err != nil || !slices.Contains(strings.Split(out, "\n"), "worktree "+tree) {
		return
	}
	fmt.Fprintf(stderr, "mutate: the %s's directory is gone, but git still lists it at %s\n"+
		"  (`git worktree prune` clears a registration whose directory has been removed)\n", what, tree)
}

// reportLeftover says what the cleanup left, where, and how to take it back —
// or nothing, when nothing was left. The verdict comes from looking, not from
// the error values: reasons carries whatever the steps did report, and a
// leftover with no reason at all is said in those words rather than dressed
// up, because "no step objected and yet it is still there" is exactly the
// observation the next investigation starts from.
func reportLeftover(stderr io.Writer, what, path string, reasons []string) {
	if path == "" {
		return
	}
	if _, err := os.Lstat(path); err != nil {
		// Gone. A step may still have complained on the way — metadata git
		// has to reconcile, say — and that is worth one small line, not the
		// leftover report.
		if len(reasons) > 0 {
			fmt.Fprintf(stderr, "mutate: the %s is gone, but its removal reported: %s\n"+
				"  (`git worktree prune` reconciles metadata a removed directory leaves behind)\n",
				what, strings.Join(reasons, "; "))
		}
		return
	}
	why := "no step reported an error, and yet it is still there — something recreated it, " +
		"or a removal claimed more than it did"
	if len(reasons) > 0 {
		why = strings.Join(reasons, "; ")
	}
	// rm before prune: prune only reaps registrations whose directory is
	// gone, so the other order leaves a stale entry that silently refuses
	// the next `git worktree add` at this path.
	fmt.Fprintf(stderr, "mutate: the %s was not removed and is still at %s\n"+
		"  (%s)\n"+
		"  take it back by hand: rm -rf %q && git worktree prune\n", what, path, why, path)
}

// addWorktree makes the baseline worktree. It is a variable so a test can hold
// one creation in flight and interrupt exactly there: the window where cleanup
// and creation overlap is the one that leaves a directory behind.
var addWorktree = func(ctx context.Context, cwd, tree, sha string) error {
	// Under the run's context, so the interrupt that cancels it also ends this.
	// Without that, cleanup waiting for creation to finish would wait as long
	// as a stuck disk takes — turning "^C is not instant" into "^C never
	// returns", which is worse than the leftover it was meant to prevent.
	_, err := gitOutCtx(ctx, cwd, "worktree", "add", "--detach", tree, sha)
	return err
}

// gitOut runs git in dir and hands back trimmed stdout; an error carries what
// git said, because "exit status 128" on its own sends someone to run the same
// command by hand to hear the actual sentence.
//
// A variable for the same reason removeAll is: cleanup collects what these
// commands report, and a test that cannot make them fail is a test of the
// runs that had nothing to collect.
var gitOut = func(dir string, args ...string) (string, error) {
	// Background on purpose: cleanup runs after the interrupt has cancelled the
	// run's context, and exec.CommandContext will not even start under one that
	// is already done. The directory would still go — cleanup removes it
	// whatever git says — but the worktree git has registered for it would
	// stay, and the next `git worktree list` here would name a tree that is no
	// longer on disk. And no delay on the pipes: these callers read what git
	// said, and a delay can cut that short. The one caller that asks for a
	// delay reads nothing.
	return git(context.Background(), 0, dir, args...)
}

// baselineFailure opens the line a failed comparison writes. Named because two
// tests turn on whether it is there: one that an interrupt does not produce it,
// one that anything else does.
const baselineFailure = "mutate: -baseline: "

// interruptWaitDelay bounds how long git is given to let go of the pipes it
// handed out, whether or not anyone cancelled it. Cancelling reaches git itself
// at once, but the output is read until everyone holding those pipes is gone,
// and git hands them to its own children: a `post-checkout` hook, an fsmonitor,
// the auto-gc it leaves running behind it. Two shapes reach this. An interrupt
// during a creation that a hook is holding: without a bound the run took over
// twenty seconds to come back, with it a fifth of a second, nearly all of which
// is this. And no interrupt at all — git finished, a child of its own did not —
// where without a bound the read simply waits, and with it the command is
// reported as the success it was.
const interruptWaitDelay = 200 * time.Millisecond

// gitOutCtx is gitOut under a caller's context, for the one command that has to
// be interruptible: making the baseline tree.
//
// The answer it hands back can be short. A delay that expires closes the pipes
// where they are, and this reports that as success because the command itself
// succeeded — which is right for a caller that does not read the answer, and
// wrong for one that does. `gitOut` is the one to add commands to.
func gitOutCtx(ctx context.Context, dir string, args ...string) (string, error) {
	return git(ctx, interruptWaitDelay, dir, args...)
}

func git(ctx context.Context, waitDelay time.Duration, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.WaitDelay = waitDelay
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	// A delay that expires is not a failed command. It says git finished and
	// something it handed a pipe to has not let go — a hook that backgrounded
	// itself, an fsmonitor, an auto-gc. The command did what it was asked and
	// only the reading was cut short, which is why the caller that asks for a
	// delay is the one that does not read the answer. Reported as a failure,
	// this turned a worktree that had just been made correctly into a failed
	// comparison, with git's own success message as the reason.
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			// git said nothing — a missing binary, a killed process. The Go
			// error is all there is, and "git rev-parse: " with nothing after
			// it sends someone to rerun the command to hear an empty answer.
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(string(out)), nil
}

// load reads the sweep, refusing a file whose keys don't mean anything here: a
// mistyped "package" or "form" would otherwise leave a field empty and change
// what the sweep does without saying so.
func load(path string) ([]mutate.Mutation, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var ms []mutate.Mutation
	if err := dec.Decode(&ms); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(ms) == 0 {
		return nil, fmt.Errorf("%s: no mutations — a sweep of nothing reports nothing", path)
	}
	names := map[string]int{}
	for i, m := range ms {
		if strings.TrimSpace(m.Name) == "" {
			return nil, fmt.Errorf("%s: mutation %d has no name; the name is what the report says", path, i+1)
		}
		// The name is also the join key when two sweeps are compared: a
		// duplicate would make one row silently stand in for another, and the
		// counts would balance while saying the wrong thing.
		if at, dup := names[m.Name]; dup {
			return nil, fmt.Errorf("%s: mutations %d and %d share the name %q; every row of the "+
				"report has to mean exactly one mutation", path, at, i+1, m.Name)
		}
		names[m.Name] = i + 1
	}
	return ms, nil
}

func count(rs []mutate.Result, o mutate.Outcome) int {
	n := 0
	for _, r := range rs {
		if r.Outcome == o {
			n++
		}
	}
	return n
}
