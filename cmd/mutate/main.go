// Command mutate runs a set of hand-written mutations and reports which tests
// each one killed.
//
// It is a repository tool, not part of the opossum binary (goreleaser builds only
// ./cmd/opossum).
//
//	mutate sweep.json     apply each mutation, print a markdown table
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
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
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
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: mutate <sweep.json>")
		return exitFailed
	}
	ms, err := load(args[0])
	if err != nil {
		fmt.Fprintln(stderr, "mutate: "+err.Error())
		return exitFailed
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "mutate: "+err.Error())
		return exitFailed
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
	done := make(chan struct{})
	go func() {
		select {
		case <-sigs:
		case <-done:
			return
		}
		stopToolchain()
		restored, rerr := r.RestorePending()
		fmt.Fprintln(stderr, interruptMessage(restored, rerr))
		exit(exitInterrupted)
	}()
	defer close(done)

	results, sweepErr := r.Sweep(ms)
	fmt.Fprint(stdout, mutate.Report(results))
	if sweepErr != nil {
		fmt.Fprintln(stderr, "mutate: "+sweepErr.Error())
		return exitFailed
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
		fmt.Fprintf(stderr, "mutate: %d of %d mutations measured nothing (they did not build, or the "+
			"run named no tests) — that is not the same as being caught\n", n, len(results))
		return exitFailed
	}
	return exitAllCaught
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
	for i, m := range ms {
		if strings.TrimSpace(m.Name) == "" {
			return nil, fmt.Errorf("%s: mutation %d has no name; the name is what the report says", path, i+1)
		}
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
