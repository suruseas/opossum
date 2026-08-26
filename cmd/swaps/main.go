// Command swaps lists the format calls where two arguments of the same kind
// could be exchanged without the compiler noticing.
//
//	swaps ./internal/orchestrator          one line per site, then the totals
//	swaps -tsv ./internal/orchestrator     file, line, pairs — tab separated
//	swaps -sweep ./internal/compose        a sweep file, on stdout, for `mutate`
//	swaps -probe ./internal/compose        surviving rows on stdin; a verdict each
//
// A survivor is ambiguous: no test caught the exchange, but that reads the
// same when no input the tests supply could have shown it — the two arguments
// always held the same value — and those call for opposite work. `-probe`
// reruns each surviving pair with the exchange replaced by a recorder, and
// says which it was: the arguments differed (write the test), never differed
// (fix the fixture), or the call never ran (reach is the problem first).
// Rows come in as the sweep report wrote them; pipe the SURVIVED lines back.
//
// The sweep is the point of the counting. `swaps -sweep ./internal/compose >
// s.json && mutate s.json` exchanges each pair in turn and says which tests
// noticed; the rows nothing caught are messages this repository could print
// backwards without a test objecting.
//
// Pairs that do not become mutations are named on stderr, one line each, with
// the reason. Two of them are ordinary: the call is written more than once in
// its file, so the text does not name one place; or the two arguments are the
// same expression, so the exchange produces the file it started from. A third
// says the call is no longer there, which means the tree was edited while the
// sweep was being built. None of them is a finding, and all are printed because
// a count of these that does not say what it left out gets quoted as if it had
// looked everywhere.
//
// One directory, walked. Not a package pattern: `./internal/...` is a thing the
// go tool understands and this does not, and it says so rather than reporting
// nothing.
//
// Point it at a whole package. The format functions a package writes for itself
// are found in the files walked, so one file of a package answers with a smaller
// number than the package does — the same call, counted or not depending on
// whether the file declaring logf came along.
//
// It is a repository tool, not part of the opossum binary (goreleaser builds
// only ./cmd/opossum).
//
// The totals are the point. Counting these by searching the text went wrong four
// times, and every time the number was quoted in a pull request as if it had
// been measured. What this package does not see is listed in its own comment,
// and each miss is held there by a test.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/suruseas/opossum/internal/mutate"
	"github.com/suruseas/opossum/internal/swaps"
)

func main() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, sigs, os.Exit))
}

// run is main without the process. The signal channel and exit reach only the
// probe mode, which is the one that writes to the tree and so must restore on
// an interrupt; every other mode only reads.
func run(args []string, in io.Reader, out, errOut io.Writer, sigs <-chan os.Signal, exit func(int)) int {
	fs := flag.NewFlagSet("swaps", flag.ContinueOnError)
	fs.SetOutput(errOut)
	tsv := fs.Bool("tsv", false, "file, line and pairs, tab separated")
	sweep := fs.Bool("sweep", false, "a sweep file on stdout, for `mutate`")
	probe := fs.Bool("probe", false, "read surviving rows on stdin, run a probe for each, and say what each silence meant")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(errOut, "usage: swaps [-tsv | -sweep | -probe] <dir>")
		return 2
	}
	picked := 0
	for _, on := range []bool{*tsv, *sweep, *probe} {
		if on {
			picked++
		}
	}
	if picked > 1 {
		fmt.Fprintln(errOut, "swaps: -tsv, -sweep and -probe write different things; pick one")
		return 2
	}
	sites, err := swaps.Find(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(errOut, "swaps: "+err.Error())
		return 1
	}
	if *sweep {
		return writeSweep(sites, out, errOut)
	}
	if *probe {
		return runProbes(sites, in, out, errOut, sigs, exit)
	}
	pairs := 0
	for _, s := range sites {
		pairs += s.Pairs()
		if *tsv {
			fmt.Fprintf(out, "%s\t%d\t%d\n", s.File, s.Line, s.Pairs())
			continue
		}
		fmt.Fprintf(out, "%s:%d  pairs=%d  %q\n", s.File, s.Line, s.Pairs(), head(s.Format))
	}
	if !*tsv {
		// The two numbers a pull request quotes, printed by the thing that
		// counted them rather than added up afterwards by a person.
		fmt.Fprintf(out, "\n%d sites, %d pairs\n", len(sites), pairs)
	}
	return 0
}

// writeSweep marshals the mutations and names the pairs that did not become
// one. The skips go to stderr so that redirecting stdout into a sweep file
// still shows them: what this tool did not offer is the part its numbers have
// been wrong about before.
func writeSweep(sites []swaps.Site, out, errOut io.Writer) int {
	muts, skips, err := swaps.Sweep(sites)
	if err != nil {
		fmt.Fprintln(errOut, "swaps: "+err.Error())
		return 1
	}
	for _, s := range skips {
		fmt.Fprintf(errOut, "not offered: %s:%d arguments %d and %d — %s\n",
			s.File, s.Line, s.A+1, s.B+1, s.Reason)
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(muts); err != nil {
		fmt.Fprintln(errOut, "swaps: "+err.Error())
		return 1
	}
	fmt.Fprintf(errOut, "%d mutations, %d pairs not offered\n", len(muts), len(skips))
	return 0
}

func head(s string) string {
	const n = 60
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// runProbes reads survivor rows, applies a probe for each pair that can carry
// one, and reads each run's transcript back into a verdict. The runs go
// through the same machinery as a sweep — baseline first, byte-checked
// restore, an interrupt puts the file back — because a probe is a mutation
// with a different question, not a lighter one.
func runProbes(sites []swaps.Site, in io.Reader, out, errOut io.Writer, sigs <-chan os.Signal, exit func(int)) int {
	var reqs []swaps.ProbeRequest
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		q, ok := swaps.ParseSurvivorRow(line)
		if !ok {
			// Named, not dropped: a report pasted whole carries headers and
			// totals, and the reader should know they were not read as rows.
			fmt.Fprintf(errOut, "not a survivor row: %s\n", line)
			continue
		}
		reqs = append(reqs, q)
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintln(errOut, "swaps: reading stdin: "+err.Error())
		return 2
	}
	if len(reqs) == 0 {
		fmt.Fprintln(errOut, "swaps: no survivor rows on stdin — pipe in the sweep report's SURVIVED lines")
		return 2
	}
	muts, skips, err := swaps.Probes(sites, reqs)
	if err != nil {
		fmt.Fprintln(errOut, "swaps: "+err.Error())
		return 1
	}
	for _, s := range skips {
		fmt.Fprintf(errOut, "not probed: %s:%d arguments %d and %d — %s\n",
			s.File, s.Line, s.A+1, s.B+1, s.Reason)
	}
	if len(muts) == 0 {
		fmt.Fprintf(errOut, "swaps: none of the %d rows could carry a probe — the reasons are above\n", len(reqs))
		return 1
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(errOut, "swaps: "+err.Error())
		return 1
	}
	ctx, stopToolchain := context.WithCancel(context.Background())
	defer stopToolchain()
	r := mutate.NewRunner(cwd)
	r.Ctx = ctx
	r.Log = func(s string) { fmt.Fprintln(errOut, s) }

	// The restore has to happen before the process goes away: os.Exit runs no
	// defers, so a handler that only warned would leave a probe in the
	// author's uncommitted work. Same shape, same reason as cmd/mutate.
	done := make(chan struct{})
	go func() {
		select {
		case <-sigs:
		case <-done:
			return
		}
		stopToolchain()
		restored, rerr := r.RestorePending()
		switch {
		case rerr != nil:
			fmt.Fprintln(errOut, "\nswaps: interrupted, AND THE FILE COULD NOT BE PUT BACK: "+rerr.Error()+
				"\n  Check `git diff` before committing anything.")
		case restored:
			fmt.Fprintln(errOut, "\nswaps: interrupted — the file being probed has been put back.")
		default:
			fmt.Fprintln(errOut, "\nswaps: interrupted with no probe in flight — the tree is as you left it.")
		}
		exit(130)
	}()
	defer close(done)

	// The same five fields under two names: swaps writes sweep files, mutate
	// reads them, and this is the one place they meet without a file between.
	ms := make([]mutate.Mutation, len(muts))
	for i, m := range muts {
		ms[i] = mutate.Mutation{Name: m.Name, File: m.File, From: m.From, To: m.To, Packages: m.Packages}
	}
	results, sweepErr := r.Sweep(ms)
	counts := map[string]int{}
	for _, res := range results {
		if res.Outcome != mutate.Survived {
			// A probe is built not to change what the tests see, so any other
			// outcome means this run cannot be read — the probe itself, or the
			// tree, did something no verdict covers.
			fmt.Fprintf(out, "%s: no verdict — the probe run was not clean (%s)\n",
				res.Mutation.Name, describeOutcome(res))
			counts["no verdict"]++
			continue
		}
		v := swaps.ProbeVerdict(res.Mutation.Name, res.TestOutput)
		fmt.Fprintf(out, "%s: %s\n", res.Mutation.Name, v)
		counts[v]++
	}
	fmt.Fprintf(errOut, "%d probed", len(results))
	for _, c := range []struct{ verdict, label string }{
		{swaps.VerdictDiffers, "write-the-test"},
		{swaps.VerdictNeverDiffered, "fix-the-fixture"},
		{swaps.VerdictUnreached, "unreached"},
		{"no verdict", "no-verdict"},
	} {
		if counts[c.verdict] > 0 {
			fmt.Fprintf(errOut, ", %d %s", counts[c.verdict], c.label)
		}
	}
	fmt.Fprintf(errOut, ", %d not probed\n", len(skips))
	if sweepErr != nil {
		fmt.Fprintln(errOut, "swaps: "+sweepErr.Error())
		return 1
	}
	// A run that could not be read is not a run that answered. Exiting 0 over
	// a "no verdict" row would let a script take "nothing measured" for
	// "nothing found" — the same reading the sweep's exit statuses refuse.
	if counts["no verdict"] > 0 {
		return 1
	}
	return 0
}

func describeOutcome(r mutate.Result) string {
	if r.Detail != "" {
		return r.Outcome.String() + ": " + r.Detail
	}
	if len(r.Killers) > 0 {
		return r.Outcome.String() + " by " + strings.Join(r.Killers, ", ")
	}
	return r.Outcome.String()
}
