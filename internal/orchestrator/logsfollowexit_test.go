package orchestrator_test

// `logs --follow` ends a followed service's stream when its container stops or
// is gone while it is followed, says so (`web-1 exited`), and ends once every
// stream it follows has, as docker compose v5.5.1 does (measured; it says the
// exit code too, which container 1.4.1 does not report). A container already
// stopped when the follow begins, and a service with a `restart:` policy, are
// followed on, as there.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// lockedBuffer is a bytes.Buffer safe to read while a follow writes to it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// slowWriter is a lockedBuffer whose reader stalls: every tenth write waits
// before it is taken.
type slowWriter struct {
	lockedBuffer
	n int
}

func (w *slowWriter) Write(p []byte) (int, error) {
	w.n++
	if w.n%10 == 0 {
		time.Sleep(300 * time.Millisecond)
	}
	return w.lockedBuffer.Write(p)
}

// restartOf and staleRestartMarker are what a row's `during` uses to restart a
// service with `opossum restart`, or to leave the marker a restart that died
// leaves, in the subtest under way; expectExitedWithin fails it unless the
// `exited` line for name is written within d.
var (
	restartOf          func(services ...string)
	staleRestartMarker func(service string)
	expectExitedWithin func(name string, d time.Duration)
)

func TestLogsFollowEndsWhenTheContainerDoes(t *testing.T) {
	poll, settle, drain := orchestrator.LogsExitPoll, orchestrator.LogsExitSettle, orchestrator.LogsExitDrain
	orchestrator.LogsExitPoll, orchestrator.LogsExitSettle, orchestrator.LogsExitDrain = 50*time.Millisecond, 300*time.Millisecond, 150*time.Millisecond
	t.Cleanup(func() {
		orchestrator.LogsExitPoll, orchestrator.LogsExitSettle, orchestrator.LogsExitDrain = poll, settle, drain
	})

	type row struct {
		name     string
		services map[string]*compose.Service
		follow   []string
		before   func(rt *runtime.Runtime) // before the follow begins
		during   func(rt *runtime.Runtime) // once the follow is under way
		ended    bool                      // the follow ends well before the streams would
		exited   []string                  // the `exited` lines, in any order
		noFollow bool                      // read without --follow
		env      []string                  // fake knobs for the row
		lastLine string                    // the line that must come before `exited`, if any
		poll     time.Duration             // this row's poll, when not the table's
		at0      bool                      // `during` as soon as the lines are written
		restart  bool                      // `during` is `opossum restart web`
		slow     bool                      // the reader stalls
		within   time.Duration             // ended within this (default 3 s); the row keeps its streams open a second longer
	}
	plain := func() *compose.Service { return &compose.Service{Image: "alpine:3.20"} }
	restarting := func() *compose.Service { return &compose.Service{Image: "alpine:3.20", Restart: "always"} }
	rows := []row{
		{"stopped while followed", map[string]*compose.Service{"web": plain()}, []string{"web"}, nil,
			func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, true, []string{"web-1"}, false, nil, "", 0, false, false, false, 0},
		{"deleted while followed", map[string]*compose.Service{"web": plain()}, []string{"web"}, nil,
			func(rt *runtime.Runtime) { rt.Delete("web.demo.opossum") }, true, []string{"web-1"}, false, nil, "", 0, false, false, false, 0},
		{"already stopped when the follow began", map[string]*compose.Service{"web": plain()}, []string{"web"},
			func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, nil, false, nil, false, nil, "", 0, false, false, false, 0},
		{"a restart: policy", map[string]*compose.Service{"web": restarting()}, []string{"web"}, nil,
			func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, false, nil, false, nil, "", 0, false, false, false, 0},
		{"two followed, one stopped", map[string]*compose.Service{"web": plain(), "db": plain()}, []string{"web", "db"}, nil,
			func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, false, []string{"web-1"}, false, nil, "", 0, false, false, false, 0},
		{"two followed, both stopped", map[string]*compose.Service{"web": plain(), "db": plain()}, []string{"web", "db"}, nil,
			func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum"); rt.Stop("db.demo.opossum") }, true, []string{"web-1", "db-1"}, false, nil, "", 0, false, false, false, 0},
		{"one of two named, it stopped", map[string]*compose.Service{"web": plain(), "db": plain()}, []string{"web"}, nil,
			func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, true, []string{"web-1"}, false, nil, "", 0, false, false, false, 0},
		// Without --follow the log is read to its end whatever the container
		// does meanwhile (a long read, stopped part-way), and nothing is said.
		{"stopped during a read without --follow", map[string]*compose.Service{"web": plain()}, []string{"web"}, nil,
			func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, false, nil, true, nil, "", 0, false, false, false, 0},
		// A restart leaves the container stopped for a moment and starts it
		// again: the stream goes on (docker compose says `(restarting)` and
		// follows on).
		{name: "stopped and started again at once, as a restart does", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) {
				rt.Stop("web.demo.opossum")
				time.Sleep(100 * time.Millisecond)
				rt.Start("web.demo.opossum")
			}},
		// What the runtime still hands over after the container has ended
		// comes before `exited`, not cut off.
		{name: "lines still coming after the stop", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, ended: true, exited: []string{"web-1"},
			env: []string{"LOGS_DRIP=40", "LOGS_DRIP_AFTER=900"}, lastLine: "drip 40 web.demo.opossum"},
		// The lines still come after the stop is seen and settled: they are
		// written, and `exited` after them (the stream is not cut on the end).
		{name: "lines starting after the stop", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, ended: true, exited: []string{"web-1"},
			env: []string{"LOGS_DRIP=40", "LOGS_DRIP_AFTER=1150"}, lastLine: "drip 40 web.demo.opossum"},
		// Stopped before the first tick after the follow began: it had been
		// seen running at once, so the stop counts.
		{name: "stopped before the first tick", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, ended: true, exited: []string{"web-1"},
			poll: 500 * time.Millisecond, at0: true},
		// Two restarts a second apart, each a moment's gap: neither is the end.
		{name: "restarted twice", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) {
				rt.Stop("web.demo.opossum")
				time.Sleep(100 * time.Millisecond)
				rt.Start("web.demo.opossum")
				time.Sleep(time.Second)
				rt.Stop("web.demo.opossum")
				time.Sleep(100 * time.Millisecond)
				rt.Start("web.demo.opossum")
			}},
		// `opossum restart` whose stop leaves the container stopped longer than
		// the settle: it marks the service as restarting, so the follow goes on.
		{name: "opossum restart with a long gap", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			env: []string{"STOP_THEN_SLEEP_MS=800"}, restart: true},
		// A reader that stalls: lines waiting to be taken are not a stream gone
		// quiet, and are not cut off.
		{name: "a slow reader, lines still coming after the stop", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, ended: true, exited: []string{"web-1"},
			env: []string{"LOGS_DRIP=60", "LOGS_DRIP_AFTER=1150", "LOGS_SLEEP=8"}, lastLine: "drip 60 web.demo.opossum", slow: true, within: 7 * time.Second},
		// A restart that died part way left its marker, which nobody holds: a
		// stop after it is the end.
		{name: "a marker left by a restart that died", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) {
				staleRestartMarker("web")
				rt.Stop("web.demo.opossum")
			}, ended: true, exited: []string{"web-1"}},
		// A restart, then a stop, in one follow: the restart is followed on and
		// the stop is the end.
		{name: "opossum restart, then a stop", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) {
				restartOf("web")
				time.Sleep(20 * orchestrator.LogsExitPoll)
				rt.Stop("web.demo.opossum")
			}, env: []string{"STOP_THEN_SLEEP_MS=800", "LOGS_SLEEP=8"}, ended: true, exited: []string{"web-1"}, within: 7 * time.Second},
		// Restarting one service is not a reason to go on following another
		// that stopped meanwhile.
		{name: "one stopped while another is restarted", services: map[string]*compose.Service{"web": plain(), "db": plain()}, follow: []string{"web", "db"},
			during: func(rt *runtime.Runtime) {
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); restartOf("db") }()
				time.Sleep(200 * time.Millisecond) // db's restart is under way
				go func() { defer wg.Done(); rt.Stop("web.demo.opossum") }()
				expectExitedWithin("web-1", 2*time.Second)
				wg.Wait()
			}, env: []string{"STOP_THEN_SLEEP_MS=4000"}, exited: []string{"web-1"}},
		// `opossum restart` of several services holds the followed one's
		// marker for all of it: while another's stop, then another's start,
		// keeps it stopped longer than the settle time, it is followed on.
		{name: "restarted with another whose stop is slow", services: map[string]*compose.Service{"web": plain(), "db": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) { restartOf("db", "web") },
			env:    []string{"STOP_THEN_SLEEP_MS=800", "SLOW_ONLY=db.demo.opossum"}},
		{name: "restarted with another whose start is slow", services: map[string]*compose.Service{"web": plain(), "db": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) { restartOf("db", "web") },
			env:    []string{"START_THEN_SLEEP_MS=800", "SLOW_ONLY=db.demo.opossum"}},
		// Two restarts of the same service, the second waiting for the first:
		// the whole of both is one gap, not the container's end.
		{name: "restarted twice over, the second waiting for the first", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) {
				var wg sync.WaitGroup
				for i := 0; i < 2; i++ {
					wg.Add(1)
					go func() { defer wg.Done(); restartOf("web") }()
					time.Sleep(200 * time.Millisecond)
				}
				wg.Wait()
			}, env: []string{"STOP_THEN_SLEEP_MS=1200", "LOGS_SLEEP=8"}},
		// `restart: "no"` is no policy: watched as any other.
		{name: "restart: no", services: map[string]*compose.Service{"web": {Image: "alpine:3.20", Restart: "no"}}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, ended: true, exited: []string{"web-1"}},
		// The same long gap by a plain stop and start is the end, and it is said.
		{name: "a plain stop with the same long gap", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) {
				rt.Stop("web.demo.opossum")
				time.Sleep(800 * time.Millisecond)
				rt.Start("web.demo.opossum")
			}, env: []string{"STOP_THEN_SLEEP_MS=0"}, ended: true, exited: []string{"web-1"}},
		// Not answering while the container stops, then answering again: the
		// end is counted from the answers, not lost.
		{name: "the runtime not answering while it stops, then answering", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) {
				marker := ""
				for _, kv := range rt.Env {
					if f, ok := strings.CutPrefix(kv, "INSPECT_FAIL_WHILE="); ok {
						marker = f
					}
				}
				os.WriteFile(marker, nil, 0o644)
				rt.Stop("web.demo.opossum")
				time.Sleep(10 * orchestrator.LogsExitPoll)
				os.Remove(marker)
			}, ended: true, exited: []string{"web-1"}},
		// The runtime not answering while followed is not the container's
		// end: the follow goes on (docker compose knows from its events).
		{"the runtime not answering while followed", map[string]*compose.Service{"web": plain()}, []string{"web"}, nil,
			func(rt *runtime.Runtime) {
				for _, kv := range rt.Env {
					if f, ok := strings.CutPrefix(kv, "INSPECT_FAIL_WHILE="); ok {
						os.WriteFile(f, nil, 0o644)
					}
				}
			}, false, nil, false, nil, "", 0, false, false, false, 0},
	}
	for _, tc := range rows {
		for _, noPrefix := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "", true: ", --no-log-prefix"}[noPrefix], func(t *testing.T) {
				t.Setenv("XDG_STATE_HOME", t.TempDir())
				rt, _ := fakeShim(t)
				// The streams stay open 4 s, as a follow does; ending well
				// before that is the container's end.
				setShimEnv(rt, "LOGS_SLEEP=4", "INSPECT_FAIL_WHILE="+filepath.Join(t.TempDir(), "not-answering"))
				setShimEnv(rt, tc.env...)
				if tc.before != nil {
					tc.before(rt)
				}
				if tc.poll != 0 {
					saved := orchestrator.LogsExitPoll
					orchestrator.LogsExitPoll = tc.poll
					defer func() { orchestrator.LogsExitPoll = saved }()
				}
				var out interface {
					Write([]byte) (int, error)
					String() string
				} = &lockedBuffer{}
				if tc.slow {
					out = &slowWriter{}
				}
				proj := project("demo", tc.services)
				o := orchestrator.New(proj, rt, "opossum", out)
				restartOf = func(services ...string) {
					if err := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).Restart(services); err != nil {
						t.Errorf("restart: %v", err)
					}
				}
				staleRestartMarker = func(service string) {
					other := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})
					other.MarkStopped(service)
					stops, _ := filepath.Glob(filepath.Join(os.Getenv("XDG_STATE_HOME"), "*", "*", "stopped-*"))
					if len(stops) == 0 {
						stops, _ = filepath.Glob(filepath.Join(os.Getenv("XDG_STATE_HOME"), "*", "stopped-*"))
					}
					if len(stops) != 1 {
						t.Fatalf("want one stop marker to put the restart marker beside, got %v", stops)
					}
					os.WriteFile(filepath.Join(filepath.Dir(stops[0]), "restarting-"+strings.TrimPrefix(filepath.Base(stops[0]), "stopped-")), nil, 0o644)
					other.ClearStopped(service)
				}
				expectExitedWithin = func(name string, d time.Duration) {
					deadline := time.Now().Add(d)
					for !strings.Contains(out.String(), name+" exited") {
						if time.Now().After(deadline) {
							t.Errorf("want %s exited within %v, got\n%s", name, d, out.String())
							return
						}
						time.Sleep(10 * time.Millisecond)
					}
				}
				done := make(chan error, 1)
				start := time.Now()
				go func() {
					done <- o.Logs(tc.follow, runtime.LogsOptions{Follow: !tc.noFollow, NoLogPrefix: noPrefix})
				}()
				// Under way: every followed stream has written its line, and the
				// state has been looked at a few times.
				for i := 0; i < 200 && strings.Count(out.String(), "log-line") < len(tc.follow); i++ {
					time.Sleep(10 * time.Millisecond)
				}
				if !tc.at0 {
					time.Sleep(20 * orchestrator.LogsExitPoll)
				}
				if tc.during != nil {
					tc.during(rt)
				}
				if tc.restart {
					restartOf("web")
				}
				var err error
				select {
				case err = <-done:
				case <-time.After(20 * time.Second):
					t.Fatal("the follow did not end")
				}
				took := time.Since(start)
				if err != nil {
					t.Errorf("want a zero exit, got %v", err)
				}
				within := 3 * time.Second
				if tc.within != 0 {
					within = tc.within
				}
				if tc.ended && took > within {
					t.Errorf("want the follow ended by the container's end, took %v", took)
				}
				if !tc.ended && took < 3*time.Second {
					t.Errorf("want the follow to go on until its streams end, it ended after %v", took)
				}
				var lines []string
				for _, l := range strings.Split(out.String(), "\n") {
					if strings.HasSuffix(l, " exited") {
						lines = append(lines, strings.TrimSuffix(l, " exited"))
					}
				}
				if tc.lastLine != "" {
					all := out.String()
					if i, j := strings.Index(all, tc.lastLine), strings.Index(all, " exited"); i < 0 || j < i {
						t.Errorf("want %q before the exited line, got\n%s", tc.lastLine, all)
					}
				}
				if strings.Join(sortedCopy(lines), ",") != strings.Join(sortedCopy(tc.exited), ",") {
					t.Errorf("want exited lines for %v, got %v in\n%s", tc.exited, lines, out.String())
				}
			})
		}
	}
}

// A SIGTERM ends the follow as any SIGTERM does. A look at the container's
// state under way is waited for only for a cancelled stream's grace: one the
// runtime does not answer does not hold the command up longer, one answered
// within it leaves no inspect of this command running once it returns, and no
// look is started after the SIGTERM. With no look under way nothing is waited
// for. A stream that ended in an error while a look goes unanswered is ended by
// a SIGTERM that comes during that wait, as well. The signal is sent to this
// process, as `kill` sends it to opossum: `logs` shares no context with the
// runtime, so the look in flight is not ended by it (a Ctrl-C on a terminal
// also reaches the runtime's own process, and ends it).
func TestLogsFollowSIGTERMDoesNotWaitForTheRuntimeToAnswer(t *testing.T) {
	poll, grace := orchestrator.LogsExitPoll, runtime.LogsCancelGrace
	orchestrator.LogsExitPoll, runtime.LogsCancelGrace = 50*time.Millisecond, time.Second
	t.Cleanup(func() { orchestrator.LogsExitPoll, runtime.LogsCancelGrace = poll, grace })
	for _, tc := range []struct {
		name    string
		hang    bool          // looks go unanswered from before the SIGTERM
		answers time.Duration // the look answers this long after the SIGTERM
		failed  bool          // the stream ends in an error before the SIGTERM
		within  time.Duration // the follow ends within this of the SIGTERM
		slower  time.Duration // and not before this
	}{
		{name: "a look that does not answer", hang: true, within: 1800 * time.Millisecond, slower: 800 * time.Millisecond},
		{name: "a look answered within the grace", hang: true, answers: 100 * time.Millisecond, within: 800 * time.Millisecond},
		{name: "no look under way", within: 500 * time.Millisecond},
		{name: "a stream that failed while a look does not answer", hang: true, failed: true, within: 1800 * time.Millisecond, slower: 800 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, log := fakeShim(t)
			dir := t.TempDir()
			hang, answered := filepath.Join(dir, "hang"), filepath.Join(dir, "answered")
			env := []string{"LOGS_SLEEP=30", "INSPECT_HANG_WHILE=" + hang, "INSPECT_ANSWERED=" + answered}
			if tc.failed {
				env = append(env, "LOGS_SELF_INT=300")
			}
			setShimEnv(rt, env...)
			asked := func() int {
				n := 0
				for _, l := range log() {
					if strings.HasPrefix(l, "inspect ") {
						n++
					}
				}
				return n
			}
			answers := func() int { b, _ := os.ReadFile(answered); return strings.Count(string(b), "answered") }
			// Every inspect answered before the subtest's directories go.
			defer func() {
				os.Remove(hang)
				for i := 0; i < 300 && answers() < asked(); i++ {
					time.Sleep(10 * time.Millisecond)
				}
			}()
			out := &lockedBuffer{}
			o := orchestrator.New(project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20"}}), rt, "opossum", out)
			done := make(chan error, 1)
			go func() { done <- o.Logs([]string{"web"}, runtime.LogsOptions{Follow: true}) }()
			for i := 0; i < 200 && !strings.Contains(out.String(), "log-line"); i++ {
				time.Sleep(10 * time.Millisecond)
			}
			if !strings.Contains(out.String(), "log-line") {
				t.Fatal("the follow did not start")
			}
			if tc.hang {
				os.WriteFile(hang, nil, 0o644)
			}
			if tc.failed {
				// the stream's own end, and the grace it waits before it counts as a failure
				for i := 0; i < 200 && !strings.Contains(out.String(), "self-int"); i++ {
					time.Sleep(10 * time.Millisecond)
				}
				time.Sleep(runtime.LogsCancelGrace + 300*time.Millisecond)
			} else {
				time.Sleep(10 * orchestrator.LogsExitPoll) // a look is under way and not answered
			}
			start := time.Now()
			if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			if tc.answers != 0 {
				time.AfterFunc(tc.answers, func() { os.Remove(hang) })
			}
			var err error
			select {
			case err = <-done:
			case <-time.After(15 * time.Second):
				t.Fatal("the follow waited for the runtime's answer")
			}
			took := time.Since(start)
			if !tc.failed && !errors.Is(err, orchestrator.ErrInterrupted) {
				t.Errorf("want ErrInterrupted, got %v", err)
			}
			if took > tc.within || took < tc.slower {
				t.Errorf("want the follow ended between %v and %v after the SIGTERM, took %v", tc.slower, tc.within, took)
			}
			// The container is running throughout: an `exited` line would say
			// the signal ended it. Here that is the order — `Logs` gives the
			// interrupt back before it would say it — and the oracle for the
			// line itself is the multiplexed side, which says it in the
			// goroutine of the stream (TestLogsFollowStartsNoLookOnceTheFollowIsOver).
			if strings.Contains(out.String(), " exited") {
				t.Errorf("want nothing said about the container ending, got\n%s", out.String())
			}
			if tc.answers == 0 {
				return
			}
			n := asked()
			if got := answers(); got != n || n == 0 {
				t.Errorf("want every inspect answered once the follow returned, %d asked and %d answered", n, got)
			}
			time.Sleep(4*orchestrator.LogsExitPoll + 50*time.Millisecond)
			if later := asked(); later != n {
				t.Errorf("want no look started once the follow returned, %d asked then %d", n, later)
			}
		})
	}
}

// No look at a container's state is started once the follow is over — the
// signal taken, or the stream ended — however many ticks went by while the look
// before it was answered: the watcher asks whether it is over before each look,
// and the run here holds every look until this test lets it through, so a look
// started after that is one this test never released. A look left behind holds
// a `container inspect` of a command that has returned, and one the follow then
// waits for holds the command itself.
//
// A watcher that did not ask has what is over ready beside the tick and Go
// picks between them at random, so a round catches it only some of the time.
// The rounds, the services followed together (a watcher each, so several draws
// in one round) and the kinds are what make it near-certain — each kind holds
// a different one of the two asks: taking the ask out altogether failed 11 of
// 18 signal rounds and every run; taking out the stream's end alone failed the
// rounds without a signal (2 of 6) and both runs, where a look started then is
// one the follow waits for, so the command hangs rather than merely lingers;
// taking out the context alone failed the rounds with a stalled reader, where
// the stream is still ending when the watcher looks (7 of 9, and all three
// runs).
func TestLogsFollowStartsNoLookOnceTheFollowIsOver(t *testing.T) {
	poll := orchestrator.LogsExitPoll
	orchestrator.LogsExitPoll = 50 * time.Millisecond
	t.Cleanup(func() { orchestrator.LogsExitPoll = poll })
	for _, k := range []struct {
		name     string
		rounds   int
		services int
		signal   bool // a SIGTERM ends the follow; otherwise the stream fails
		slow     bool // the reader stalls, so the stream is a while ending
	}{
		{name: "a SIGTERM", rounds: 6, services: 1, signal: true},
		{name: "a SIGTERM, several services followed together", rounds: 2, services: 8, signal: true},
		// The reader stalls, so the stream is still ending while the watcher
		// looks: what is over then is the follow's own context, not the stream.
		{name: "a SIGTERM the stream is a while taking", rounds: 3, services: 1, signal: true, slow: true},
		{name: "the stream ending in an error, no signal", rounds: 3, services: 1},
	} {
		for round := 0; round < k.rounds; round++ {
			t.Run(k.name+"/round "+strconv.Itoa(round), func(t *testing.T) {
				t.Setenv("XDG_STATE_HOME", t.TempDir())
				rt, _ := fakeShim(t)
				gate, dir := t.TempDir(), t.TempDir()
				answered := filepath.Join(dir, "answered")
				env := []string{"LOGS_SLEEP=30", "INSPECT_GATE=" + gate, "INSPECT_ANSWERED=" + answered}
				if !k.signal {
					env = append(env, "LOGS_SELF_INT=300")
				}
				if k.slow {
					env = append(env, "LOGS_DRIP=200")
				}
				setShimEnv(rt, env...)
				held := func() []string {
					entries, _ := os.ReadDir(gate)
					let := map[string]bool{}
					for _, e := range entries {
						if n, ok := strings.CutSuffix(e.Name(), "-go"); ok {
							let[n] = true
						}
					}
					var names []string
					for _, e := range entries {
						if !strings.HasSuffix(e.Name(), "-go") && !let[e.Name()] {
							names = append(names, e.Name())
						}
					}
					return names
				}
				release := func(name string) { os.WriteFile(filepath.Join(gate, name+"-go"), nil, 0o644) }
				releaseAll := func() {
					for _, n := range held() {
						release(n)
					}
				}
				answers := func() int { b, _ := os.ReadFile(answered); return strings.Count(string(b), "answered") }
				defer releaseAll()
				services := map[string]*compose.Service{}
				for i := 0; i < k.services; i++ {
					services["web"+strconv.Itoa(i)] = &compose.Service{Image: "alpine:3.20"}
				}
				var out interface {
					Write([]byte) (int, error)
					String() string
				} = &lockedBuffer{}
				if k.slow {
					out = &slowWriter{}
				}
				o := orchestrator.New(project("demo", services), rt, "opossum", out)
				done := make(chan error, 1)
				go func() { done <- o.Logs(nil, runtime.LogsOptions{Follow: true}) }()
				// The looks before the follow are let through so it can begin.
				for i := 0; i < 600 && strings.Count(out.String(), "log-line") < k.services; i++ {
					releaseAll()
					time.Sleep(10 * time.Millisecond)
				}
				if n := strings.Count(out.String(), "log-line"); n < k.services {
					t.Fatalf("want every stream started, %d of %d did", n, k.services)
				}
				var inFlight []string
				for i := 0; i < 600 && inFlight == nil; i++ {
					if names := held(); len(names) == k.services {
						inFlight = names
					}
					time.Sleep(10 * time.Millisecond)
				}
				if inFlight == nil {
					t.Fatalf("want a look of every watcher under way to hold, got %v", held())
				}
				time.Sleep(10 * orchestrator.LogsExitPoll) // ticks go by while they are held
				if k.signal {
					if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
						t.Fatal(err)
					}
					time.Sleep(50 * time.Millisecond) // the signal is taken before the looks answer
				} else {
					// The stream ends by itself, and the follow waits out the
					// grace it gives a cancel before it counts as a failure.
					for i := 0; i < 600 && !strings.Contains(out.String(), "self-int"); i++ {
						time.Sleep(10 * time.Millisecond)
					}
					time.Sleep(runtime.LogsCancelGrace + 200*time.Millisecond)
				}
				before := answers()
				for _, n := range inFlight {
					release(n)
				}
				for i := 0; i < 600 && answers() < before+len(inFlight); i++ {
					time.Sleep(10 * time.Millisecond)
				}
				if got := answers(); got < before+len(inFlight) {
					t.Fatalf("want the %d looks let through answered, %d did", len(inFlight), got-before)
				}
				select {
				case err := <-done:
					if k.signal && !errors.Is(err, orchestrator.ErrInterrupted) {
						t.Errorf("want ErrInterrupted, got %v", err)
					}
					if !k.signal && err == nil {
						t.Error("want the stream's failure reported, got a zero exit")
					}
				case <-time.After(15 * time.Second):
					t.Fatalf("the follow did not end; looks held: %v", held())
				}
				time.Sleep(4 * orchestrator.LogsExitPoll)
				if names := held(); len(names) != 0 {
					t.Errorf("want no look started once the follow was over, %d held: %v", len(names), names)
				}
				// Every container is running throughout: an `exited` line
				// would say the signal, or the stream's failure, ended one.
				if strings.Contains(out.String(), " exited") {
					t.Errorf("want nothing said about a container ending, got\n%s", out.String())
				}
			})
		}
	}
}

func sortedCopy(s []string) []string {
	c := append([]string(nil), s...)
	for i := range c {
		for j := i + 1; j < len(c); j++ {
			if c[j] < c[i] {
				c[i], c[j] = c[j], c[i]
			}
		}
	}
	return c
}
