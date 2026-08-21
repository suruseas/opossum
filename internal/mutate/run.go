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
	"strings"
	"sync"
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

	// Ctx stops the toolchain when the run is being abandoned. Without it a
	// `go test` started by this process outlives it, holding the pipes open.
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
}

// NewRunner returns a Runner wired to the real toolchain and filesystem.
func NewRunner(root string) *Runner {
	return &Runner{
		Root: root,
		Ctx:  context.Background(),
		Go: func(args ...string) (string, string, error) {
			cmd := exec.Command("go", args...)
			cmd.Dir = root
			var out, errOut bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &errOut
			err := cmd.Run()
			return out.String(), errOut.String(), err
		},
		Read:  func(p string) ([]byte, error) { return os.ReadFile(filepath.Join(root, p)) },
		Write: func(p string, b []byte) error { return os.WriteFile(filepath.Join(root, p), b, 0o644) },
		Log:   func(s string) { fmt.Fprintln(os.Stderr, s) },
	}
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
	out, errOut, err := r.Go(append([]string{"test", "-count=1", "-json"}, pkgs...)...)
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
		return false, nil
	}
	err = r.restoreLocked(r.pendPath, r.pendOrig)
	r.pending = false
	return true, err
}

// errAborted ends a sweep that was interrupted before it wrote its mutation.
var errAborted = errors.New("interrupted before the mutation was written")

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
		return Result{Mutation: m, Outcome: Broken, Detail: lastLines(vetOut+vetErr, 3)}, nil
	}
	testOut, testErrOut, testErr := r.Go(append([]string{"test", "-count=1", "-json"}, m.Packages...)...)
	killers := Failures(testOut)
	switch {
	case len(killers) > 0:
		return Result{Mutation: m, Outcome: Caught, Killers: killers}, nil
	case testErr != nil:
		// Red, but nobody is named: a panic, a package-level timeout, a toolchain
		// that could not start. Whatever it was, it is not "the suite is fine
		// with this defect" — and the mutations worth writing here are the ones
		// most likely to hang.
		return Result{Mutation: m, Outcome: Inconclusive, Detail: whyItDied(testOut, testErrOut)}, nil
	}
	return Result{Mutation: m, Outcome: Survived}, nil
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
