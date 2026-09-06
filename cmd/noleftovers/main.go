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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/suruseas/opossum/internal/suitedir"
)

// interruptGrace is how long the end of a run waits for an interrupt that may
// still be on its way from the kernel (see interruptedExit).
// Longer than the runtime's hand-off takes on a machine running a whole
// gate's worth of test binaries at once; shorter than anyone notices.
const interruptGrace = 500 * time.Millisecond

// Only what this repository's runs make. `opossum-*` covers the three suites
// that build under $TMPDIR and the dry-run directory the product itself makes.
// The other shape is t.TempDir's — removed by t.Cleanup, which a killed process
// never reaches, and carrying no pid, so nothing can ask afterwards whether its
// maker is still alive (#552). Being told it is there is all that is possible.
// What both leave out is written down in CONTRIBUTING.md.
const ours = "opossum-*"

// testTempDirGlob gathers t.TempDir's candidates; testTempDir is the shape that
// keeps them. Go writes the test's name — subtests joined on, "/" dropped,
// spaces made "_" — keeping letters and digits of any script and the symbols
// !#$%&()+,-.=@^_{}~, then the digits MkdirTemp adds. A name carrying anything
// else (a "[" or a ":", say) is not one a test could have had.
const testTempDirGlob = "Test*"

var testTempDir = regexp.MustCompile(`^Test[\p{L}\p{N}!#$%&()+,\-.=@^_{}~ ]*[0-9]+$`)

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
	// terminal while this one runs appears here too.
	//
	// The name says who made it. internal/suitedir builds <prefix><pid>-<random>
	// and sweeps by asking whether that pid is still alive; the same question is
	// asked here. A directory whose maker is still running is not something this
	// run left behind — it is someone else's, in progress — and reporting it as
	// a leak taught the reader to shrug at this check (#668).
	var left, theirs []string
	for d := range snapshot(tmp) {
		if before[d] {
			continue
		}
		// Only our own names carry a pid. A t.TempDir's name is whatever the
		// test was called, and "-12-345" inside one would read as the pid of a
		// process that has nothing to do with it — so it is never asked.
		if strings.HasPrefix(filepath.Base(d), "opossum-") {
			if pid, ok := suitedir.MakerPid(filepath.Base(d)); ok && suitedir.Alive(pid) {
				theirs = append(theirs, fmt.Sprintf("%s (pid %d)", filepath.Base(d), pid))
				continue
			}
		}
		left = append(left, d)
	}
	sort.Strings(theirs)
	if len(theirs) > 0 {
		fmt.Fprintf(w, "\nthese appeared in %s while this ran and belong to a run still going:\n", tmp)
		for _, t := range theirs {
			fmt.Fprintf(w, "  %s\n", t)
		}
		fmt.Fprintln(w, "the process that made each is alive, so they are not this run's to clean up")
		fmt.Fprintln(w, "(a pid the system has since handed to something else would read the same way)")
	}
	if len(left) == 0 {
		if killedBy != 0 {
			sayKilled(w, killedBy)
			return code
		}
		// Nothing left behind does not mean nothing happened. A ^C ends the
		// command with a status of its own — `go test` answers 1 — and handing
		// that back alone reads as tests that failed; until #727 this path did
		// exactly that, and only a run that had also leaked was told apart.
		status, _ := interruptedExit(interrupted, w, code)
		return status
	}
	sort.Strings(left)

	fmt.Fprintf(w, "\nthese appeared in %s while this ran:\n", tmp)
	for _, d := range left {
		fmt.Fprintf(w, "  %s\n", filepath.Base(d))
	}
	fmt.Fprintln(w, "a suite that finishes removes its own, so this is a run that ended before it")
	fmt.Fprint(w, "could — a panic, a -timeout, or an interrupt. ")
	// "listed above" only reads as an instruction when there is something
	// above. With no live maker among what appeared, the same sentence sends
	// the reader up the page to find nothing — so say which of the two the
	// reader is looking at.
	if len(theirs) > 0 {
		fmt.Fprintln(w, "A suite still going in another")
		fmt.Fprintln(w, "terminal is listed above instead, by the pid in its name; a name with no pid")
		fmt.Fprintln(w, "to read is here too, since nothing can be asked about it")
	} else {
		fmt.Fprintln(w, "None of these belongs to a suite")
		fmt.Fprintln(w, "still going: one whose pid is alive would be listed on its own, and none")
		fmt.Fprintln(w, "was. A name with no pid to read is here too, since nothing can be asked")
		fmt.Fprintln(w, "about it")
	}

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
		sayKilled(w, killedBy)
		return code
	}
	if status, was := interruptedExit(interrupted, w, code); was {
		return status
	}
	if code == 0 {
		return 1
	}
	fmt.Fprintf(w, "\nthe command itself exited %d; it failed as well as leaving these\n", code)
	return code
}

// interruptedExit asks whether this run was interrupted and, when it was, says
// so and hands back the status to end with: the command's own, or 1 for a
// command that answered 0 — an interrupted run is not a passing one, whatever
// the command made of the signal. Both ends of run ask this the same way, the
// one that found leftovers and the one that did not.
//
// Asked here rather than inferred from the command's status, because a
// command that catches ^C for itself leaves no trace of it in that status
// — `go test` answers 1, the same as a run whose tests failed.
//
// Asked with a grace period rather than at once. A ^C reaches this process
// and the command together, and the command can be gone — trapped, exited,
// reaped by Run — before the runtime here has moved the signal from the
// kernel to this channel: that hand-off is a goroutine's turn, and under a
// loaded machine it comes late. A read that did not wait called such a run
// "failed" — one gate in five, measured in the sieve's container (#711).
// Every run whose command exited — rather than dying of a signal, or never
// starting — pays this much, once, at the end, whether or not anyone
// interrupted it.
func interruptedExit(interrupted <-chan os.Signal, w io.Writer, code int) (status int, was bool) {
	select {
	case <-interrupted:
		fmt.Fprintf(w, "\nthis run was interrupted; the command handled that itself and exited %d,\n", code)
		fmt.Fprintln(w, "so its status says nothing about whether the tests were going to pass")
		if code == 0 {
			return 1, true
		}
		return code, true
	case <-time.After(interruptGrace):
		return code, false
	}
}

// sayKilled says that the command died of a signal. Not "exited": it did not.
// Saying so keeps the reader from reading an interruption as a failure of the
// tests. Said the same way at both ends of run.
func sayKilled(w io.Writer, by syscall.Signal) {
	fmt.Fprintf(w, "\nthe command was killed by %s rather than exiting\n", by)
}

func snapshot(tmp string) map[string]bool {
	found := map[string]bool{}
	for _, pattern := range []string{ours, testTempDirGlob} {
		names, err := filepath.Glob(filepath.Join(tmp, pattern))
		if err != nil {
			continue
		}
		for _, n := range names {
			if pattern == testTempDirGlob && !testTempDir.MatchString(filepath.Base(n)) {
				continue
			}
			found[n] = true
		}
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
