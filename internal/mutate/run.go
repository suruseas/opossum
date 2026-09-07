package mutate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/suruseas/opossum/internal/suitedir"
)

// Runner applies mutations to real files and runs the real toolchain. Every
// side-effecting piece is a field so the sweep itself can be tested without a
// checkout to damage.
type Runner struct {
	// Root is the directory commands run in, and the boundary a mutation may not
	// write outside of.
	Root string
	// Go runs the toolchain. It returns stdout and stderr separately because the
	// test output is parsed as `go test -json`: the toolchain writes build errors
	// and panics to stderr, and folding them into the stream would corrupt events
	// — losing a `fail` line is losing the answer.
	//
	// A non-nil error means the command exited non-zero, which for `test` is the
	// normal case.
	Go func(args ...string) (stdout, stderr string, err error)
	// Read and Write reach the tree.
	Read  func(path string) ([]byte, error)
	Write func(path string, b []byte) error
	// Log receives a line per mutation as the sweep goes, so a long run says
	// where it is rather than sitting silent.
	Log func(string)

	// Ctx stops the toolchain when the run is being abandoned. Cancelling it
	// sends an interrupt to the `go test` NewRunner starts and to the test
	// binary underneath it — two processes rather than one: ending only the
	// first leaves the second holding the pipes open, adopted away (#607 has
	// the measurement). A test binary that catches the interrupt and does not
	// exit is past what this can end: the escalation five seconds later
	// (WaitDelay) reaches `go test` alone, and such a binary is adopted away
	// exactly as before.
	//
	// Read when a command is started rather than when the Runner is built, so a
	// caller that sets it after NewRunner — which is every caller — is wired,
	// and nobody may rewire it while a sweep is running: nothing guards the
	// field. A nil one is an uncancellable run rather than a panic.
	Ctx context.Context

	// mu guards the in-flight mutation, which RestorePending reads from another
	// goroutine when the process is being interrupted. The mutation is written
	// while holding it, so an interrupt cannot land between "this file is
	// registered as mutated" and "the mutation is on disk".
	mu       sync.Mutex
	pending  bool
	pendPath string
	pendOrig []byte
	aborted  bool
	// restoredOnCancel records that the sweep itself put the in-flight file
	// back after Ctx was cancelled — the interrupt handler cancels first and
	// asks second, and the toolchain it killed can hand the sweep the lock
	// before the handler gets there. restoreErrOnCancel is the other half of
	// the same moment: the sweep tried and failed, and the handler has to be
	// told that rather than "nothing was in flight".
	restoredOnCancel   bool
	restoreErrOnCancel error

	// reach is the baseline's coverage, taken over the tree as it was. Which
	// lines the tests run is a question about that tree, not about a mutated one,
	// so every mutation is answered from this one profile.
	reach []byte
	// measured records whether the baseline was instrumented, so the runs it
	// vouches for are instrumented the same way — including when it could not be.
	measured bool
}

// NewRunner returns a Runner wired to the real toolchain and filesystem.
func NewRunner(root string) *Runner {
	r := &Runner{
		Root:  root,
		Ctx:   context.Background(),
		Read:  func(p string) ([]byte, error) { return os.ReadFile(filepath.Join(root, p)) },
		Write: func(p string, b []byte) error { return os.WriteFile(filepath.Join(root, p), b, 0o644) },
		Log:   func(s string) { fmt.Fprintln(os.Stderr, s) },
	}
	r.Go = func(args ...string) (string, string, error) {
		// r.Ctx rather than a captured one: callers set it after this returns.
		ctx := r.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = root
		// Its own process group, and the signal goes to the group. `go test` is
		// not the process doing the work — it builds, then runs a test binary —
		// so ending the toolchain means ending both, and only the group reaches
		// the second. Measured, cancelling while a test slept:
		//
		//	nothing (the shape before this)   Wait never returns; both live on
		//	CommandContext's default SIGKILL  go test dies at once; the test binary is adopted away
		//	SIGINT to `go test` alone         it does not pass it on: WaitDelay kills it 5s
		//	                                  later, and the test binary is adopted away
		//	SIGINT to the group               both gone in 0.01s, and `go test` exits
		//	                                  (status 1) rather than being killed
		//
		// The cost, measured under a pty: a ^C typed at the terminal reached
		// the child directly before this (it died 130 before the parent moved),
		// and with Setpgid it no longer does — the parent alone hears it. The
		// ending still happens, but it now goes through whoever cancels this
		// context — in cmd/mutate the interrupt handler, before it starts
		// putting the tree back. What is actually given up is the terminal's
		// own delivery as a backstop: a run whose handler is stuck used to lose
		// its toolchain to the ^C anyway, and now keeps it until the WaitDelay
		// or the sleep runs out. (A mutate killed outright — SIGKILL runs no
		// handler — leaves the toolchain running under either wiring.)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT) }
		// A toolchain that ignores the interrupt does not get to hold the run
		// open. Long enough for a test binary shutting down normally to finish
		// first, short enough not to read as a hang. What it kills on expiry
		// is `go test` alone — exec offers no group-wide escalation — so a
		// test binary that caught the interrupt and stayed is left behind, as
		// the Ctx doc says.
		cmd.WaitDelay = 5 * time.Second
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		err := cmd.Run()
		return out.String(), errOut.String(), err
	}
	return r
}

// Sweep applies each mutation in turn, records what caught it, and puts the file
// back.
//
// The tree is left as it was found, and that is checked rather than trusted: the
// restore is compared against the bytes read at the start, and a mismatch is an
// error even though everything else may have gone well. The alternative — a
// mutation silently left in a file the author then commits — is the worst thing
// this could do, and it is not hypothetical: a sweep once died on a timeout
// mid-mutation and left the source altered.
//
// Restoring is deliberately not `git checkout`, which would throw away every
// uncommitted change in the file. The point of running this is usually to test
// work that is not committed yet.
func (r *Runner) Sweep(ms []Mutation) ([]Result, error) {
	// The whole sweep is checked before anything runs. A mutation that names no
	// package, points outside the tree, or does not apply is a fault in the sweep
	// rather than a finding — and finding that out after six mutations, or after
	// a baseline run that takes minutes, wastes the time it was supposed to save.
	for _, m := range ms {
		if _, _, err := r.prepare(m); err != nil {
			return nil, err
		}
	}
	if err := r.baseline(ms); err != nil {
		return nil, err
	}
	var out []Result
	for _, m := range ms {
		// A cancelled run stops asking. Every round below writes a mutation
		// into the tree and puts it back, and once the context is gone the
		// toolchain answers "context canceled" to every question — rounds that
		// measure nothing, while the writes race whatever the canceller is
		// doing to this tree (for the baseline worktree, removing it). Checked
		// here rather than left to the toolchain error so the answer says
		// "cancelled", not something about the mutation it happened to be on.
		if r.Ctx != nil && r.Ctx.Err() != nil {
			return out, fmt.Errorf("cancelled after %d of %d mutations: %w", len(out), len(ms), r.Ctx.Err())
		}
		res, err := r.one(m)
		if err != nil {
			return out, err
		}
		if r.Log != nil {
			r.Log(fmt.Sprintf("%s: %s", m.Name, describe(res)))
		}
		out = append(out, res)
	}
	return out, nil
}

// baseline runs the suite once, before anything is mutated, over every package
// the sweep will test.
//
// A test that is already failing fails again under every mutation, and a failing
// test is exactly what this tool reads as "the mutation was caught" — so one red
// test makes a whole sweep report that everything is guarded while measuring
// nothing at all. That is the failure the exit statuses promise not to produce,
// and it is not hypothetical: a sweep once reported a mutation as caught by a
// test that could not run, and the mutation it was supposed to be measuring was
// never observed by anything.
//
// There is no way to say "yes, one is red, carry on". A sweep over a red tree
// cannot distinguish the two answers this tool exists to keep apart.
func (r *Runner) baseline(ms []Mutation) error {
	pkgs := packagesOf(ms)
	if len(pkgs) == 0 {
		return errors.New("no packages to test")
	}
	if r.Log != nil {
		r.Log("baseline: running the suite unmutated over " + strings.Join(pkgs, " "))
	}
	// Instrumented, like every run that decides a mutation's fate, and the only run
	// whose profile is kept.
	//
	// Instrumented because coverage makes the tests slower: a baseline run without
	// it can pass while a test with a deadline in it fails under every mutation,
	// and that is reported as a suite which caught all of them. The baseline exists
	// to rule that out, and it can only do so from the same instrumentation.
	//
	// Kept because which lines the tests run is a question about the tree as the
	// author wrote it. Answered from a mutated tree, a mutation that stops its own
	// line from running looks like a line nothing reaches.
	prof, cleanup := r.profilePath()
	defer cleanup()
	// Whether this run is measured decides whether the mutation runs are, so that
	// the two cannot end up instrumented differently when there is nowhere to
	// write a profile.
	r.measured = prof != ""
	out, errOut, err := r.Go(append(withCoverage([]string{"test", "-count=1", "-json"}, prof), pkgs...)...)
	if prof != "" {
		// Read before the failure checks below: a red baseline stops the sweep, and
		// then nobody asks about reach anyway, but a profile that was written and
		// then dropped on the floor is the kind of thing that gets quietly removed
		// later for looking unused.
		r.reach, _ = os.ReadFile(prof)
	}
	if failing := Failures(out); len(failing) > 0 {
		return fmt.Errorf("the suite is already failing before any mutation is applied:\n  %s\n"+
			"Every mutation would be reported as caught by these, so the sweep would measure "+
			"nothing while reporting that everything is guarded. Make them pass first.",
			strings.Join(failing, "\n  "))
	}
	if err != nil {
		return fmt.Errorf("the suite could not be run before any mutation was applied, so nothing "+
			"the sweep reported afterwards would mean anything: %s", oneLine(whyItDied(out, errOut)))
	}
	if r.Log != nil {
		// Said out loud: the baseline is the longest single run here, and without
		// this the first mutation's line arrives after minutes of silence.
		r.Log("baseline: green — every failure from here belongs to a mutation")
	}
	return nil
}

// packagesOf is every package any mutation names, once each, in the order first
// seen so a run is reproducible.
func packagesOf(ms []Mutation) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range ms {
		for _, p := range m.Packages {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

func describe(r Result) string {
	if len(r.Killers) > 0 {
		return r.Outcome.String() + " by " + strings.Join(r.Killers, ", ")
	}
	if r.Detail != "" {
		return r.Outcome.String() + ": " + oneLine(r.Detail)
	}
	return r.Outcome.String()
}

// RestorePending puts back the file the sweep is in the middle of, if any, and
// stops the sweep from writing another one. It is safe to call from a signal
// handler, and safe to call when nothing is pending.
//
// It exists because the deferred restore inside a sweep cannot help an
// interrupted process: `os.Exit` runs no defers, and a handler that merely warns
// leaves the author's uncommitted work mutated. Which is the accident this whole
// package is supposed to prevent, arriving through its own front door.
//
// Latching `aborted` is what makes the answer stay true. Without it a handler
// that arrived a moment before the mutation was written would restore a file
// nobody had touched yet, report success, and then watch the sweep write the
// mutation anyway — "put it back" printed over a mutated tree, which is the worst
// thing this could say.
// The bool says whether there was anything to put back. With a baseline run in
// front of the sweep, the likeliest moment to interrupt is one where no mutation
// has been written yet — and a message saying a file was restored, when none was
// touched, is the tool telling the author something that did not happen.
func (r *Runner) RestorePending() (restored bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.aborted = true
	if !r.pending {
		// Nothing to do now — but if the sweep already put the file back
		// because the cancellation ahead of this call killed its toolchain
		// run, that file was in flight at the interrupt, and "nothing in
		// flight" would be the wrong thing to tell the author. If the sweep
		// tried and could not, that is the answer — the file is still mutated.
		return r.restoredOnCancel, r.restoreErrOnCancel
	}
	err = r.restoreLocked(r.pendPath, r.pendOrig)
	r.pending = false
	return true, err
}

// errAborted ends a sweep that was interrupted before it wrote its mutation.
var errAborted = errors.New("interrupted before the mutation was written")

// errInterrupted ends a sweep whose toolchain run was cut short by the
// cancellation of Ctx. The run's exit status says nothing about the mutation
// then — a killed `go vet` is not "did not compile" and a killed `go test` is
// not "inconclusive" — so no Result is made of it.
var errInterrupted = errors.New("interrupted while a mutation was being measured")

// cancelled reports whether Ctx has been cancelled.
func (r *Runner) cancelled() bool { return r.Ctx != nil && r.Ctx.Err() != nil }

// armAndWrite registers the restore and writes the mutation as one step, so no
// interrupt can see one without the other.
func (r *Runner) armAndWrite(path string, original, mutated []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.aborted {
		return errAborted
	}
	// Registered before the write, not after. `os.WriteFile` truncates first, so
	// a write that fails partway — a full disk, a quota, a network mount — leaves
	// the file already destroyed. Registering afterwards would mean the one case
	// where the file is definitely damaged is the one case nothing puts it back.
	r.pending, r.pendPath, r.pendOrig = true, path, original
	return r.Write(path, mutated)
}

// prepare checks a mutation and works out what it would write, without writing
// anything. Sweep runs it over every mutation before the first one is applied,
// and one runs it again for the bytes.
func (r *Runner) prepare(m Mutation) (original, mutated []byte, err error) {
	if len(m.Packages) == 0 {
		return nil, nil, fmt.Errorf("%s: no packages to test — a mutation nothing runs against "+
			"cannot be caught, and would be reported as survived", m.Name)
	}
	if err := r.inRoot(m.File); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", m.Name, err)
	}
	original, err = r.Read(m.File)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", m.Name, err)
	}
	applied, err := Apply(string(original), m)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", m.Name, err)
	}
	return original, []byte(applied), nil
}

// one applies a single mutation and always restores the file before returning.
func (r *Runner) one(m Mutation) (res Result, err error) {
	original, mutated, err := r.prepare(m)
	if err != nil {
		return Result{}, err
	}

	werr := r.armAndWrite(m.File, original, mutated)
	defer func() {
		if rerr := r.disarmAndRestore(); rerr != nil {
			err = errors.Join(err, rerr)
		}
	}()
	if werr != nil {
		return Result{}, fmt.Errorf("%s: %w", m.Name, werr)
	}

	// Build before testing. A mutation that does not compile runs no tests at
	// all, and reading the resulting red as "the suite caught it" is how a
	// mutation with no evidence behind it ends up in a pull request.
	if vetOut, vetErr, buildErr := r.Go(append([]string{"vet"}, m.Packages...)...); buildErr != nil {
		if r.cancelled() {
			return Result{}, errInterrupted
		}
		return Result{Mutation: m, Outcome: Broken, Detail: whyItWouldNotBuild(vetOut + vetErr)}, nil
	}
	// Instrumented the same way the baseline was. Coverage costs time, and a
	// baseline measured in a cheaper configuration than the runs it vouches for is
	// no baseline at all: a test with a deadline in it passes there and fails under
	// every mutation, and the sweep calls that a suite which caught everything. No
	// profile is asked for here — the reach question is answered from the baseline.
	args := []string{"test", "-count=1", "-json"}
	if r.measured {
		args = append(args, "-cover", coverPkg)
	}
	testOut, testErrOut, testErr := r.Go(append(args, m.Packages...)...)
	transcript := testOut + testErrOut
	killers := Failures(testOut)
	switch {
	case len(killers) > 0:
		return Result{Mutation: m, Outcome: Caught, Killers: killers, TestOutput: transcript}, nil
	case testErr != nil:
		if r.cancelled() {
			return Result{}, errInterrupted
		}
		// Red, but nobody is named: a panic, a package-level timeout, a toolchain
		// that could not start. Whatever it was, it is not "the suite is fine
		// with this defect" — and the mutations worth writing here are the ones
		// most likely to hang.
		return Result{Mutation: m, Outcome: Inconclusive, Detail: whyItDied(testOut, testErrOut), TestOutput: transcript}, nil
	}
	// Nothing failed. That reads as "the suite is fine with this defect" — but it
	// reads the same way when no test runs the line at all, and those are opposite
	// findings. Ask which one this is.
	//
	// Asked of the baseline, not of the run that just finished. "Do the tests run
	// this line" is a question about the tree the author wrote; the tree being
	// measured here has a defect in it, and the defect can stop its own line from
	// running. A mutation to a `case` label is the plain example — the label is
	// where its coverage block begins, so changing it makes the block count zero
	// while the tests stay green. Read from the mutated tree, that is a live
	// survivor reported as a line no test reaches, which is the one answer this
	// must never invent.
	//
	// Written as a note beside the survivor, never as the outcome. A note that is
	// wrong leaves a reader looking at a survivor with a misleading hint; an
	// outcome that is wrong sends them to find out why live code is dead.
	return Result{Mutation: m, Outcome: Survived, Detail: r.reachNote(m, original), TestOutput: transcript}, nil
}

// reachNote says what the baseline's coverage had to say about the lines this
// mutation changes, in the words the measurement can support.
//
// "No test" means no test that this sweep ran. The baseline runs the packages the
// sweep names, and -coverpkg widens the counting to what those tests reach, but a
// test in a package nobody named is not in the profile at all — and narrow is the
// way the guidance says to name them. Saying "no test runs this" on that evidence
// claims a suite that was never executed.
//
// An empty note means the coverage showed the change being run, or there is
// nothing to say about it. Silence is the safe direction here: a survivor read
// without a note is a survivor, which is what it is.
func (r *Runner) reachNote(m Mutation, original []byte) string {
	return r.noteFor(m.File, changedPlaces(original, m.From, m.To))
}

// noteFor is reachNote once the places are known, kept apart from finding them so
// the wording can be held to the measurement without a tree to mutate.
func (r *Runner) noteFor(file string, places []Pos) string {
	if len(places) == 0 {
		return ""
	}
	var answered []Pos
	for _, p := range places {
		ran, decided := Executed(r.reach, file, p)
		if ran {
			// Something ran part of what this changes. That is as much as the
			// question asks.
			return ""
		}
		if decided {
			answered = append(answered, p)
		}
	}
	// Listed, not spanned, and only the places the profile answered for.
	//
	// A mutation can change two lines with untouched ones between them, and a
	// range claims the ones it deliberately left out. The lines the profile said
	// nothing about are left out for the same reason: naming them among the ones
	// it called unrun says the coverage ruled them out, and it did not.
	if len(answered) == 0 {
		return fmt.Sprintf("whether any test reaches %s could not be measured",
			file+":"+strings.Join(lineNumbers(places), ", "))
	}
	return fmt.Sprintf("no test this sweep ran appears to reach %s, in the baseline's coverage",
		file+":"+strings.Join(lineNumbers(answered), ", "))
}

// profilePath names a file for `go test` to write coverage into, and returns the
// call that removes it. An empty name means no profile could be made; the run
// still happens, and the caller reports the reach as unmeasured.
//
// Outside the tree on purpose: written inside, a killed run would leave the file
// behind, which is the shape of accident the tracked-file gate exists to catch.
//
// The name carries this process's pid, the way suitedir's directories do, and
// for the same reason: the cleanup below is a defer, and an interrupt that
// leaves by os.Exit runs no defers — eighteen of these files were found under
// a $TMPDIR once, from runs interrupted by hand, and every net that looks for
// leftovers had a reason not to see them. A file whose maker is gone is
// reclaimed by reapStrays at the start of the next run; a file with no pid in
// its name belongs to an older build or to somebody else, and is left where
// it is rather than guessed about.
func (r *Runner) profilePath() (path string, cleanup func()) {
	reapStrays(os.TempDir())
	f, err := os.CreateTemp("", fmt.Sprintf("opossum-mutate-%d-*.cover", os.Getpid()))
	if err != nil {
		return "", func() {}
	}
	f.Close()
	return f.Name(), func() { os.Remove(f.Name()) }
}

// reapStrays removes the cover profiles of runs that are no longer alive. It
// runs at the start, not the end: the runs that need reclaiming are exactly
// the ones that never reached their own cleanup, so the only place their
// files can be collected is somebody else's beginning. Only names of the
// shape this package writes — prefix, pid, random, suffix — are touched; a
// live pid means a run in progress on this machine, and its file is its own.
func reapStrays(dir string) {
	names, err := filepath.Glob(filepath.Join(dir, "opossum-mutate-*.cover"))
	if err != nil {
		return
	}
	for _, name := range names {
		// CutPrefix with its found checked, not TrimPrefix: the glob already
		// anchors the family name, but a judgement this one-sided had both
		// anchors knocked off in review with every test still green — so the
		// prefix is asked for here too, the way suitedir's own makerOf asks.
		rest, found := strings.CutPrefix(filepath.Base(name), "opossum-mutate-")
		if !found {
			continue
		}
		if pid, ok := suitedir.PidLeading(rest); ok && !suitedir.Alive(pid) {
			os.Remove(name)
		}
	}
}

// changedPlaces is where in the file the mutation actually changes something.
//
// Not where its pattern lies, when those differ — and they differ often. A
// pattern has to match exactly once, and the way to make it match once is to
// widen it until it does, which the guidance for writing one says to do. Widened,
// it takes in lines the mutation leaves exactly as they were: above it, below it,
// and in between, when one mutation changes two places at once.
//
// All three borrow. Ask about a line the pattern starts on and a change the tests
// run is written up as unreachable code; ask about a line in the middle that the
// pattern only carried along, and a change nothing runs passes for one the tests
// watch. So the answer is the lines that differ, each of them, and none of the
// ones that came along for the ride.
//
// Compared line by line, and only when both sides have the same number of lines.
// When they do not, which line became which is a guess, and the whole span is
// treated as changed — an unchanged line in the span can then still silence the
// note, which is the quiet direction and the safe one.
func changedPlaces(src []byte, from, to string) []Pos {
	at := bytes.Index(src, []byte(from))
	if at < 0 {
		return nil
	}
	base := LineOf(src, at)
	// The pattern rarely starts at the left margin: its first line begins wherever
	// it begins, and every line after it begins at the margin.
	firstCol := at - lastBreakBefore(src, at) + 1
	f, t := lines(from), lines(to)
	var out []Pos
	for i := range f {
		if len(f) == len(t) && f[i] == t[i] {
			continue
		}
		col := 1
		if i == 0 {
			col = firstCol
		}
		switch {
		case i < len(t):
			// Past what this line keeps. A block begins at the first token on its
			// line, not at the margin, so asking about the indentation asks about a
			// place no block covers — and the change is not in the indentation.
			col += commonPrefix(f[i], t[i])
		default:
			// This line has no counterpart: the replacement is shorter, so the whole
			// of it goes. Asked about at the margin it would land outside every
			// block and answer nothing, every time — which is worse than it sounds,
			// because these are the lines that could have shown the change being
			// run and let the note fall silent. Ask where its code starts.
			col = max(col, firstToken(f[i]))
		}
		out = append(out, Pos{Line: base + i, Col: col})
	}
	return out
}

// lines splits text into the lines it occupies. Text ending in a break occupies
// the lines before it and not the empty one after: a pattern written to end at a
// line boundary does not reach into the line that follows, and a place invented
// there belongs to code the mutation never touches.
func lines(s string) []string {
	out := strings.Split(s, "\n")
	if n := len(out); n > 1 && out[n-1] == "" {
		return out[:n-1]
	}
	if len(out) == 1 && out[0] == "" {
		return nil
	}
	return out
}

// lineNumbers is the lines the places fall on, in the order they are given, with
// a run of places on one line named once. It does not sort, and it does not look
// past the place before: given lines out of order, or the same line twice with
// another between, it names them as many times as they arrive. Places are made
// one to a line and in file order, so here that is every line named once.
func lineNumbers(places []Pos) []string {
	var out []string
	last := 0
	for _, p := range places {
		// Not `p.Line == last` alone: an unset line is zero, and zero would be
		// swallowed by the initial value rather than named. Nothing makes one
		// today — a place comes from an offset that was found — and quietly
		// dropping a line is not the way to find out that something started to.
		if last != 0 && p.Line == last {
			continue
		}
		last = p.Line
		out = append(out, strconv.Itoa(p.Line))
	}
	return out
}

// firstToken is the column where a line's code starts, past its indentation.
func firstToken(line string) int {
	return 1 + len(line) - len(strings.TrimLeft(line, " \t"))
}

// lastBreakBefore is the offset just past the line break before at, so that
// at minus it is the one-based column.
func lastBreakBefore(src []byte, at int) int {
	return bytes.LastIndexByte(src[:at], '\n') + 1
}

// commonPrefix is how many leading bytes two strings share.
func commonPrefix(a, b string) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// coverPkg makes the profile record every package in the module, not only the
// ones whose own tests are running.
//
// Without it, `go test` counts a file's statements only in the test binary built
// for that file's package. A package with no test files of its own, exercised
// entirely by another package's tests, comes back with every count zero — stated
// as zero, not left out, so it reads as a definite "nothing runs this". That is a
// live survivor reported as unreachable code.
const coverPkg = "-coverpkg=./..."

// withCoverage adds the coverage flags when there is somewhere to write the
// profile. The baseline and the runs that decide each mutation are instrumented
// the same way, so neither vouches for a suite the other never ran.
func withCoverage(args []string, profile string) []string {
	if profile == "" {
		return args
	}
	return append(args, "-coverprofile="+profile, coverPkg)
}

// inRoot refuses a path that would write outside the tree being swept.
func (r *Runner) inRoot(p string) error {
	clean := filepath.ToSlash(filepath.Clean(p))
	if filepath.IsAbs(p) || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%q is outside the tree being swept; a mutation may only touch files under %s", p, r.Root)
	}
	return nil
}

func (r *Runner) disarmAndRestore() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.pending {
		return nil // a signal handler got there first
	}
	err := r.restoreLocked(r.pendPath, r.pendOrig)
	r.pending = false
	if r.cancelled() {
		if err == nil {
			r.restoredOnCancel = true
		} else {
			r.restoreErrOnCancel = err
		}
	}
	return err
}

// restoreLocked writes the original bytes back and reads them again to confirm.
func (r *Runner) restoreLocked(path string, original []byte) error {
	if err := r.Write(path, original); err != nil {
		return fmt.Errorf("could not restore %s — IT IS STILL MUTATED: %w", path, err)
	}
	now, err := r.Read(path)
	if err != nil {
		return fmt.Errorf("could not re-read %s to confirm the restore: %w", path, err)
	}
	if string(now) != string(original) {
		return fmt.Errorf("%s does not match what it was before the mutation — do not commit it", path)
	}
	return nil
}

// whyItDied pulls a usable reason out of a `go test -json` run that named no
// test. The panic or the timeout is inside the stream's Output fields — the raw
// tail of the stream is three timestamped JSON objects, which tells a reader
// nothing — and anything the toolchain itself refused to say in JSON is on
// stderr.
func whyItDied(testJSON, stderr string) string {
	var lines []string
	for _, l := range strings.Split(testJSON, "\n") {
		var e struct {
			Action string `json:"Action"`
			Output string `json:"Output"`
		}
		if json.Unmarshal([]byte(l), &e) != nil || e.Action != "output" {
			continue
		}
		if t := strings.TrimSpace(e.Output); t != "" {
			lines = append(lines, t)
		}
	}
	// The first lines of a panic say what happened; the last are the stack.
	if len(lines) > 3 {
		lines = lines[:3]
	}
	if out := strings.Join(lines, " "); out != "" {
		return out
	}
	return lastLines(stderr, 3)
}

// lastLines is the tail of s, for showing why a build failed without pasting the
// whole of it.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// whyItWouldNotBuild keeps the lines of a failed vet that say something, which
// means dropping the ones that only name the package.
//
// `go vet` announces the package it is about with lines beginning `#` — one, or
// two when the package has tests — and then prints the lines naming the file,
// the position and the problem. Kept whole, a table cell reads
// "# example.com/m # [example.com/m] vet: ./m.go:3:28: undefined: x", and the
// part a reader needs is at the end. Dropped, it reads as the sentence it is.
//
// The rule is what the headers are, not where they sit. Taking the last line
// would work for a build that failed to type-check — vet stops at the first of
// those, whether the problems are in one file or three — and would be wrong for
// vet's own findings, which come several at a time and carry no header at all.
//
// More than three lines are cut to the last three and said to be cut, because a
// cell that quietly drops the first problem is the mistake this function was
// written to avoid, one level up.
func whyItWouldNotBuild(s string) string {
	var kept []string
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	if len(kept) == 0 {
		// Nothing but headers, or nothing at all. Better the headers than a
		// mutation that says it would not build and will not say why.
		return lastLines(s, 3)
	}
	if len(kept) > 3 {
		return "(" + strconv.Itoa(len(kept)-3) + " more) " + strings.Join(kept[len(kept)-3:], "\n")
	}
	return strings.Join(kept, "\n")
}
