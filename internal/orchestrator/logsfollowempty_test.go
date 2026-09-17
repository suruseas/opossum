package orchestrator_test

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// A followed container that has written nothing yet is followed on, as docker
// compose v5.5.1 follows it (lines it writes later are shown; a stop ends it
// with `web-1 exited`): container 1.4.1's `container logs -f` ends at once,
// exit 0, on an empty log without -n and follows it with -n, so opossum asks
// again with -n and every line. Only then: a follow with a tail already has -n,
// a read without --follow ends there as the runtime's does, and a runtime that
// handed a line over or failed is not asked again.
func TestLogsFollowAContainerThatHasWrittenNothing(t *testing.T) {
	poll, settle, drain := orchestrator.LogsExitPoll, orchestrator.LogsExitSettle, orchestrator.LogsExitDrain
	orchestrator.LogsExitPoll, orchestrator.LogsExitSettle, orchestrator.LogsExitDrain = 50*time.Millisecond, 300*time.Millisecond, 150*time.Millisecond
	t.Cleanup(func() {
		orchestrator.LogsExitPoll, orchestrator.LogsExitSettle, orchestrator.LogsExitDrain = poll, settle, drain
	})
	const again = "logs -f -n 9223372036854775807 "
	for _, tc := range []struct {
		name     string
		follow   []string
		opts     runtime.LogsOptions
		env      []string
		stop     bool          // stop web once the follow is under way
		within   time.Duration // the follow ends within this, when set
		asks     []string      // the `container logs` asked, in order (a set when several services)
		lasts    time.Duration // the follow is still under way after this
		contains []string      // in the output
		failed   bool          // Logs returns an error
	}{
		{name: "followed", follow: []string{"web"}, opts: runtime.LogsOptions{Follow: true}, env: []string{"LOGS_EMPTY=web.demo.opossum", "LOGS_SLEEP=2"},
			asks: []string{"logs -f web.demo.opossum", again + "web.demo.opossum"}, lasts: time.Second},
		{name: "followed, and lines written later are shown", follow: []string{"web"}, opts: runtime.LogsOptions{Follow: true}, env: []string{"LOGS_EMPTY=web.demo.opossum", "LOGS_DRIP=2"},
			asks: []string{"logs -f web.demo.opossum", again + "web.demo.opossum"}, contains: []string{"web-1  | drip 1 web.demo.opossum\n", "web-1  | drip 2 web.demo.opossum\n"}},
		// The stream asked again stays open far past the test's wait, as the
		// runtime's does: only the stop, through the context, ends it.
		{name: "followed, and a stop ends it", follow: []string{"web"}, opts: runtime.LogsOptions{Follow: true}, env: []string{"LOGS_EMPTY=web.demo.opossum", "LOGS_SLEEP=60"}, stop: true,
			asks: []string{"logs -f web.demo.opossum", again + "web.demo.opossum"}, contains: []string{"web-1 exited\n"}, within: 5 * time.Second},
		{name: "two followed, both have written nothing", follow: []string{"x", "web"}, opts: runtime.LogsOptions{Follow: true}, env: []string{"LOGS_EMPTY=web.demo.opossum x.demo.opossum", "LOGS_SLEEP=2"},
			asks: []string{"logs -f web.demo.opossum", again + "web.demo.opossum", "logs -f x.demo.opossum", again + "x.demo.opossum"}, lasts: time.Second},
		// One stream handing lines over is no reason for the other, empty, not
		// to be asked again: each is counted on its own.
		{name: "two followed, one has written nothing and the other a line", follow: []string{"x", "web"}, opts: runtime.LogsOptions{Follow: true}, env: []string{"LOGS_EMPTY=web.demo.opossum", "LOGS_DRIP=2"},
			asks:     []string{"logs -f web.demo.opossum", again + "web.demo.opossum", "logs -f x.demo.opossum"},
			contains: []string{"x-1    | log-line x.demo.opossum\n", "web-1  | drip 1 web.demo.opossum\n", "web-1  | drip 2 web.demo.opossum\n"}},
		{name: "not followed", follow: []string{"web"}, env: []string{"LOGS_EMPTY=web.demo.opossum", "LOGS_SLEEP=2"},
			asks: []string{"logs web.demo.opossum"}},
		// A tail below zero is no tail: -n is not passed, so the log is as empty.
		{name: "followed with a tail below zero", follow: []string{"web"}, opts: runtime.LogsOptions{Follow: true, Tail: -1}, env: []string{"LOGS_EMPTY=web.demo.opossum", "LOGS_SLEEP=2"},
			asks: []string{"logs -f web.demo.opossum", again + "web.demo.opossum"}, lasts: time.Second},
		{name: "followed with a tail", follow: []string{"web"}, opts: runtime.LogsOptions{Follow: true, Tail: 5}, env: []string{"LOGS_EMPTY=web.demo.opossum", "LOGS_SLEEP=2"},
			asks: []string{"logs -f -n 5 web.demo.opossum"}, lasts: time.Second},
		{name: "followed, and the runtime handed a line over before it ended", follow: []string{"web"}, opts: runtime.LogsOptions{Follow: true},
			asks: []string{"logs -f web.demo.opossum"}, contains: []string{"web-1  | log-line web.demo.opossum\n"}},
		{name: "followed, and the runtime failed", follow: []string{"web"}, opts: runtime.LogsOptions{Follow: true}, env: []string{"LOGS_EMPTY=web.demo.opossum", "LOGS_FAIL=web.demo.opossum"},
			asks: []string{"logs -f web.demo.opossum"}, failed: true},
		{name: "followed, and the runtime failed when asked again", follow: []string{"web"}, opts: runtime.LogsOptions{Follow: true}, env: []string{"LOGS_EMPTY=web.demo.opossum", "LOGS_FAIL_N=web.demo.opossum"},
			asks: []string{"logs -f web.demo.opossum", again + "web.demo.opossum"}, failed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, log := fakeShim(t)
			setShimEnv(rt, tc.env...)
			out := &lockedBuffer{}
			o := orchestrator.New(project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20"}, "x": {Image: "alpine:3.20"}}), rt, "opossum", out)
			done := make(chan error, 1)
			start := time.Now()
			go func() { done <- o.Logs(tc.follow, tc.opts) }()
			if tc.stop {
				time.Sleep(20 * orchestrator.LogsExitPoll)
				rt.Stop("web.demo.opossum")
			}
			var err error
			select {
			case err = <-done:
			case <-time.After(15 * time.Second):
				t.Fatal("the follow did not end")
			}
			if took := time.Since(start); tc.within != 0 && took > tc.within {
				t.Errorf("want the follow ended within %v, took %v", tc.within, took)
			}
			if took := time.Since(start); took < tc.lasts {
				t.Errorf("want the follow under way for %v, it ended after %v with\n%s", tc.lasts, took, out.String())
			}
			if (err != nil) != tc.failed {
				t.Errorf("want failed=%v, got %v", tc.failed, err)
			}
			if errors.Is(err, orchestrator.ErrInterrupted) {
				t.Errorf("want no interrupt, got %v", err)
			}
			var asks []string
			for _, l := range log() {
				if strings.HasPrefix(l, "logs ") {
					asks = append(asks, l)
				}
			}
			if strings.Join(sortedCopy(asks), "\n") != strings.Join(sortedCopy(tc.asks), "\n") {
				t.Errorf("want the runtime asked\n%s\ngot\n%s", strings.Join(tc.asks, "\n"), strings.Join(asks, "\n"))
			}
			if len(tc.follow) == 1 && strings.Join(asks, "\n") != strings.Join(tc.asks, "\n") {
				t.Errorf("want them in the order\n%s\ngot\n%s", strings.Join(tc.asks, "\n"), strings.Join(asks, "\n"))
			}
			for _, c := range tc.contains {
				if !strings.Contains(out.String(), c) {
					t.Errorf("want %q in\n%s", c, out.String())
				}
			}
			if len(tc.contains) == 0 && strings.Contains(out.String(), " | ") {
				t.Errorf("want no line shown, got\n%s", out.String())
			}
		})
	}
}

// A SIGTERM while the runtime is still finding the log empty ends the follow as
// any SIGTERM does: the runtime is not asked again — not even tried, which
// --verbose would show as a command that never ran — and nothing is said.
func TestLogsFollowOfAnEmptyLogEndsOnSIGTERMWithoutAskingAgain(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, log := fakeShim(t)
	setShimEnv(rt, "LOGS_EMPTY=web.demo.opossum", "LOGS_EMPTY_AFTER=3000", "LOGS_SLEEP=10")
	trace := &lockedBuffer{}
	rt.Verbose, rt.Trace = true, trace
	o := orchestrator.New(project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20"}}), rt, "opossum", &lockedBuffer{})
	go func() {
		time.Sleep(500 * time.Millisecond)
		syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}()
	var err error
	stderr := stderrOf(t, func() { err = o.Logs([]string{"web"}, runtime.LogsOptions{Follow: true}) })
	if !errors.Is(err, orchestrator.ErrInterrupted) {
		t.Errorf("want ErrInterrupted, got %v", err)
	}
	if stderr != "" {
		t.Errorf("want nothing said, got %q", stderr)
	}
	var asks []string
	for _, l := range log() {
		if strings.HasPrefix(l, "logs ") {
			asks = append(asks, l)
		}
	}
	if len(asks) != 1 {
		t.Errorf("want the runtime asked once, got %v", asks)
	}
	if n := strings.Count(trace.String(), " logs -f "); n != 1 {
		t.Errorf("want one logs command traced, got\n%s", trace.String())
	}
}

// A SIGTERM while the runtime asked again follows the empty log ends it at
// once: the second ask runs under the same context as the first, so the
// runtime, which would not end by itself, is stopped with it.
func TestLogsFollowOfAnEmptyLogAskedAgainEndsOnSIGTERM(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, log := fakeShim(t)
	setShimEnv(rt, "LOGS_EMPTY=web.demo.opossum", "LOGS_SLEEP=60")
	o := orchestrator.New(project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20"}}), rt, "opossum", &lockedBuffer{})
	sentAt := make(chan time.Time, 1)
	go func() {
		time.Sleep(time.Second)
		sentAt <- time.Now()
		syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}()
	done := make(chan error, 1)
	go func() { done <- o.Logs([]string{"web"}, runtime.LogsOptions{Follow: true}) }()
	select {
	case err := <-done:
		if !errors.Is(err, orchestrator.ErrInterrupted) {
			t.Errorf("want ErrInterrupted, got %v", err)
		}
		select {
		case sent := <-sentAt:
			if d := time.Since(sent); d > 3*time.Second {
				t.Errorf("want the follow ended by the SIGTERM, %v after it", d)
			}
		default:
			t.Error("the follow ended before the SIGTERM")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the follow did not end on the SIGTERM")
	}
	var asks []string
	for _, l := range log() {
		if strings.HasPrefix(l, "logs ") {
			asks = append(asks, l)
		}
	}
	if len(asks) != 2 {
		t.Errorf("want the runtime asked twice before the SIGTERM, got %v", asks)
	}
}
