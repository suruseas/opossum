// Command busy runs a command with the machine deliberately loaded, so a test
// that only fails when things are slow gets a chance to fail.
//
// The load runs inside this process. That is the whole design: a busy loop
// started as a separate process outlives whatever was supposed to stop it —
// which is not hypothetical, it has happened twice here. Once a review's shell
// spawned two dozen of them and died before its last line, leaving forty orphans
// burning 600% of a laptop for five hours; once a flake sweep did the same and
// took a machine somebody was using to a load average of 157. Both times the
// cleanup was correct and simply never ran.
//
// Nothing this starts can be orphaned, because it starts nothing but the command
// itself: kill this and the load goes with it, by the same mechanism that ends
// any process. The deadline covers the other ending — being left running rather
// than killed — and it is short by default for the same reason.
//
// Run the built binary, not `go run ./cmd/busy`. `go run` is a second process
// that can be killed on its own, and killing it leaves this one behind holding
// every core: measured at 399% with a ppid of 1. It also collapses the command's
// exit status to 1.
//
// How much load it can actually apply is the machine's answer, not the number
// asked for: four spinning threads on a two-core runner burn two cores' worth.
//
// Usage: busy [-n workers] [-for duration] <command> [args...]
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"
)

func main() {
	n := flag.Int("n", runtime.NumCPU(), "how many workers to keep spinning")
	limit := flag.Duration("for", 5*time.Minute, "stop the load after this long, even if the command is still going")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: busy [-n workers] [-for duration] <command> [args...]")
		os.Exit(2)
	}
	os.Exit(run(flag.Args(), *n, *limit, os.Stderr))
}

// run returns the command's own exit status, or 2 when it could not get that far
// — which a command exiting 2 is indistinguishable from by number alone, though
// not by what is printed.
func run(argv []string, workers int, limit time.Duration, w io.Writer) int {
	// The deadline fires on its own goroutine, and main writes too. One lock is
	// cheaper than reasoning about which of them can be second.
	var saying sync.Mutex
	say := func(format string, a ...any) {
		saying.Lock()
		defer saying.Unlock()
		fmt.Fprintf(w, format, a...)
	}
	// A run that quietly applies no load is worse than one that refuses: the
	// person measuring believes they measured the busy case.
	if workers < 0 {
		say("cannot spin %d workers\n", workers)
		return 2
	}
	if ceiling := 4 * runtime.NumCPU(); workers > ceiling {
		say("%d workers is more than this machine can be asked for; the ceiling here is %d\n", workers, ceiling)
		return 2
	}
	if limit <= 0 {
		say("-for %s would stop the load before it started; give it a duration\n", limit)
		return 2
	}

	if workers == 0 {
		// The comment above says a run that quietly applies no load is worse than
		// one that refuses. Zero is the one input that has to be allowed — it is
		// the control the tests measure against — so it is said instead.
		say("no workers asked for; the command runs unloaded\n")
	}

	// Without this, -n above GOMAXPROCS is a lie: the runtime will only ever run
	// that many goroutines at once, so asking for four workers on a one-thread
	// default gets one core's worth rather than four. Measured: 1.07s of CPU per
	// second before, 4.04s after. Raised by one so the timer and the wait are not
	// competing with the load for the last slot, and put back before returning —
	// run() is called in-process by its own tests.
	if prev := runtime.GOMAXPROCS(0); workers+1 > prev {
		defer runtime.GOMAXPROCS(prev)
		runtime.GOMAXPROCS(workers + 1)
	}

	// Closed once, from whichever of the two paths gets there first — the command
	// finishing or the deadline. The Once lives here rather than at package scope
	// so that a second call starts a second load: at package scope the first run
	// consumed it, and every later run spun forever waiting for a channel nobody
	// would close.
	stop := make(chan struct{})
	var closing sync.Once
	closeStop := func() { closing.Do(func() { close(stop) }) }
	var spinning sync.WaitGroup
	for i := 0; i < workers; i++ {
		spinning.Add(1)
		go func() {
			defer spinning.Done()
			for {
				select {
				case <-stop:
					return
				default:
					// Deliberately empty. The point is to be in the way.
				}
			}
		}()
	}
	timer := time.AfterFunc(limit, func() {
		// Said out loud: a run whose load stopped early looks exactly like a run
		// that was never loaded, and the whole point of asking for load is that
		// the reader believes it was applied.
		say("the load stopped after %s; the command is still running, unloaded\n", limit)
		closeStop()
	})
	defer timer.Stop()

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()

	closeStop()
	// Waited for, so that "this returned" and "the load stopped" are the same
	// moment to anyone measuring from outside.
	spinning.Wait()

	if err == nil {
		return 0
	}
	ee, ok := err.(*exec.ExitError)
	if !ok {
		say("could not run %s: %v\n", argv[0], err)
		return 2
	}
	// A command a signal killed never exited, and ExitCode() says -1 for it.
	// 128+n is what a shell writes.
	if ws, wok := ee.Sys().(syscall.WaitStatus); wok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ee.ExitCode()
}
