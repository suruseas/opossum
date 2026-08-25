// Command noleftovers runs a command and refuses a run that left something of
// ours behind in the temporary directory.
//
// The suites build their binaries under $TMPDIR and remove them when they are
// done. "When they are done" is the catch: a panic — and a -timeout firing is a
// panic — ends the test binary where it stands, so neither the removal nor the
// check that looks for processes the tests left running is reached. Those are
// the runs that leak, and they are exactly the runs that cannot see themselves.
//
// So the look happens out here, where the test binary's death is only an exit
// status. What is lost out here is attribution: this knows a directory is new,
// not which test made it. The in-suite check keeps that, and this is the net
// under the runs where the in-suite check never gets to speak.
//
// Usage: noleftovers <command> [args...]
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Only what this repository names. `opossum-*` covers the three suites that
// build under $TMPDIR and the dry-run directory the product itself makes; what
// it leaves out is written down in CONTRIBUTING.md.
const ours = "opossum-*"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: noleftovers <command> [args...]")
		os.Exit(2)
	}
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(argv []string, w io.Writer) int {
	// ^C reaches the whole process group, so the command dies either way. This
	// keeps *us* alive long enough to say what it left — without it we would be
	// killed alongside the thing we are watching, which is the failure this
	// exists to close.
	//
	// The channel is kept rather than thrown away: it is the only thing that
	// knows this run was interrupted. A command that handles ^C itself — `go
	// test` does, and that is the command the Makefile passes — exits normally
	// afterwards, so its status says nothing about the interrupt at all.
	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt)

	// os.TempDir() hands back $TMPDIR as it was written, and on macOS that ends
	// in a slash — which would be doubled in every path this prints.
	tmp := strings.TrimSuffix(os.TempDir(), string(os.PathSeparator))
	before := snapshot(tmp)

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	code := 0
	var killedBy syscall.Signal
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			fmt.Fprintf(w, "could not run %s: %v\n", argv[0], err)
			return 2
		}
		code = ee.ExitCode()
		// A command that a signal killed never exited, and ExitCode() says -1
		// for it. Passing that on would print a number that is not a status and
		// call an interruption a failure — and `os.Exit(-1)` lands as 255. The
		// shell's 128+n is what the rest of the world writes for this.
		if ws, wok := ee.Sys().(syscall.WaitStatus); wok && ws.Signaled() {
			killedBy = ws.Signal()
			code = 128 + int(killedBy)
		}
	}

	// Anything that was not here before. A difference rather than "everything
	// matching", so what the machine already had is left alone — but it is a
	// difference in time, not in ownership: a suite that starts in another
	// terminal while this one runs appears here too, and this cannot tell the
	// two apart. Hence the wording below, which says when they appeared rather
	// than whose they are.
	var left []string
	for d := range snapshot(tmp) {
		if !before[d] {
			left = append(left, d)
		}
	}
	if len(left) == 0 {
		return code
	}
	sort.Strings(left)

	fmt.Fprintf(w, "\nthese appeared in %s while this ran:\n", tmp)
	for _, d := range left {
		fmt.Fprintf(w, "  %s\n", filepath.Base(d))
	}
	fmt.Fprintln(w, "a suite that finishes removes its own, so this is most likely a run that ended")
	fmt.Fprintln(w, "before it could — a panic, a -timeout, or an interrupt — but")
	fmt.Fprintln(w, "a suite started in another terminal while this one ran looks exactly the same")
	fmt.Fprintln(w, "from here, so check that nothing else is using them")

	// A directory merely left is untidy. One with the binary inside it still
	// running is what this exists for: the in-suite check would have named that
	// process, and on this path it never ran.
	for _, d := range left {
		pids, looked := processesUnder(d)
		switch {
		case !looked:
			fmt.Fprintf(w, "\ncould not look for processes still running out of %s\n", filepath.Base(d))
			fmt.Fprintln(w, "so this says nothing about whether any are — the note above is why")
		case len(pids) > 0:
			fmt.Fprintf(w, "\nstill running out of %s: %s\n", filepath.Base(d), join(pids))
			fmt.Fprintf(w, "`kill %s` ends them\n", join(pids))
		}
	}

	fmt.Fprintln(w, "\nonce nothing is using them:")
	for _, d := range left {
		fmt.Fprintf(w, "  rm -rf %s\n", d)
	}

	// A failing command keeps its own status: turning a 3 into a 1 would tell
	// the reader the tests leaked when they only failed. `go run` — which is how
	// the Makefile calls this — collapses every non-zero status to 1 on the way
	// out, so the number is also printed here, where nothing can flatten it.
	if killedBy != 0 {
		// Not "exited": it did not. Saying so keeps the reader from reading an
		// interruption as a failure of the tests.
		fmt.Fprintf(w, "\nthe command was killed by %s rather than exiting\n", killedBy)
		return code
	}
	select {
	case <-interrupted:
		// Asked here rather than inferred from the command's status, because a
		// command that catches ^C for itself leaves no trace of it in that
		// status — `go test` answers 1, the same as a run whose tests failed.
		fmt.Fprintf(w, "\nthis run was interrupted; the command handled that itself and exited %d,\n", code)
		fmt.Fprintln(w, "so its status says nothing about whether the tests were going to pass")
		if code == 0 {
			return 1
		}
		return code
	default:
	}
	if code == 0 {
		return 1
	}
	fmt.Fprintf(w, "\nthe command itself exited %d; it failed as well as leaving these\n", code)
	return code
}

func snapshot(tmp string) map[string]bool {
	found := map[string]bool{}
	names, err := filepath.Glob(filepath.Join(tmp, ours))
	if err != nil {
		return found
	}
	for _, n := range names {
		found[n] = true
	}
	return found
}

// processesUnder reports the pids running out of dir, and whether the look
// happened at all. A look that could not be taken is not a look that found
// nothing, and the two must not print the same.
func processesUnder(dir string) (pids []int, looked bool) {
	out, err := exec.Command("pgrep", "-f", dir).Output()
	if err != nil {
		// pgrep says 1 for "nothing matched", which is an answer. Anything else
		// — no pgrep on this machine, a permission problem — is not.
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, true
		}
		return nil, false
	}
	for _, f := range strings.Fields(string(out)) {
		if pid, cerr := strconv.Atoi(f); cerr == nil {
			pids = append(pids, pid)
		}
	}
	return pids, true
}

func join(pids []int) string {
	var s []string
	for _, p := range pids {
		s = append(s, strconv.Itoa(p))
	}
	return strings.Join(s, " ")
}
