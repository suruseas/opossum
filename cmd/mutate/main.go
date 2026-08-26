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
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

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

// run is main without the process. Everything that decides an exit status lives
// here so it can be tested; main only supplies the real streams, signals, and the
// way out. `exit` is a parameter for the same reason the signal channel is: the
// interrupt path is the one that was wrong, so it has to be reachable from a
// test.
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
	// The toolchain is a child process; abandoning the run has to take it with us,
	// or a `go test` outlives this command holding the pipes open.
	ctx, stopToolchain := context.WithCancel(context.Background())
	defer stopToolchain()
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

	done := make(chan struct{})
	go func() {
		select {
		case <-sigs:
		case <-done:
			return
		}
		stopToolchain()
		restored, rerr := r.RestorePending()
		runCleanup()
		fmt.Fprintln(stderr, interruptMessage(restored, rerr))
		exit(exitInterrupted)
	}()
	defer close(done)

	results, sweepErr := r.Sweep(ms)
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
			fmt.Fprintln(stderr, "mutate: -baseline: "+err.Error())
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
	parent, err := os.MkdirTemp("", "opossum-mutate-baseline-")
	if err != nil {
		return "", err
	}
	tree := filepath.Join(parent, "tree")
	cleanup := func() {
		// Forced: the tree usually holds a half-applied mutation when this
		// runs from the interrupt path, and asking politely would refuse.
		if _, err := gitOut(cwd, "worktree", "remove", "--force", tree); err != nil {
			// The worktree metadata can outlive a directory that was removed
			// out from under git; prune is the documented way to reconcile.
			_, _ = gitOut(cwd, "worktree", "prune")
		}
		os.RemoveAll(parent)
	}
	setCleanup(cleanup)
	if _, err := gitOut(cwd, "worktree", "add", "--detach", tree, sha); err != nil {
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
		rb := mutate.NewRunner(tree)
		// The same ctx as the main runner, so the day cancellation reaches
		// the toolchain it reaches both trees — a second runner without it
		// would be the one run an interrupt cannot stop.
		rb.Ctx = ctx
		rb.Log = func(s string) { fmt.Fprintln(stderr, "baseline tree: "+s) }
		before, err = rb.Sweep(applicable)
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

// gitOut runs git in dir and hands back trimmed stdout; an error carries what
// git said, because "exit status 128" on its own sends someone to run the same
// command by hand to hear the actual sentence.
func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
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
