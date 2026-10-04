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
	"os/exec"
	"path/filepath"
	"sort"
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
	// stream is how long the fake keeps a followed stream open, where a row
	// does not say (LOGS_SLEEP). A follow that goes on is one that ran to
	// the stream's end; a follow the container ended is over well before it.
	const stream = 3 * time.Second
	if stream%time.Second != 0 {
		t.Fatal("stream is handed to the fake in whole seconds (LOGS_SLEEP)")
	}
	// endedWithin is how long a follow the container ended may take, where a
	// row does not say. It has to stay under every row's stream, or the row
	// could not tell the two apart (checked below). Its value, like the
	// stream's, is not derived here: what each row spends of it is measured
	// and tabled where the table is changed. As of #1148 the worst of them —
	// "lines starting after the stop" — took up to 1.63 s (4 runs, one under
	// load, the shim warmed first).
	const endedWithin = 2200 * time.Millisecond
	poll, settle, drain := orchestrator.LogsExitPoll, orchestrator.LogsExitSettle, orchestrator.LogsExitDrain
	orchestrator.LogsExitPoll, orchestrator.LogsExitSettle, orchestrator.LogsExitDrain = 50*time.Millisecond, 300*time.Millisecond, 150*time.Millisecond
	t.Cleanup(func() {
		orchestrator.LogsExitPoll, orchestrator.LogsExitSettle, orchestrator.LogsExitDrain = poll, settle, drain
	})

	type row struct {
		name      string
		services  map[string]*compose.Service
		follow    []string
		before    func(rt *runtime.Runtime) // before the follow begins
		during    func(rt *runtime.Runtime) // once the follow is under way
		ended     bool                      // the follow ends well before the streams would
		exited    []string                  // the `exited` lines, in any order
		noFollow  bool                      // read without --follow
		env       []string                  // fake knobs for the row
		lastLine  string                    // the line that must come before `exited`, if any
		poll      time.Duration             // this row's poll, when not the table's
		at0       bool                      // `during` as soon as the lines are written
		restart   bool                      // `during` is `opossum restart web`
		slow      bool                      // the reader stalls
		within    time.Duration             // ended within this (default endedWithin); the row keeps its streams open a second longer
		prefixToo bool                      // run with --no-log-prefix as well, though no `exited` line is looked for
	}
	plain := func() *compose.Service { return &compose.Service{Image: "alpine:3.20"} }
	restarting := func() *compose.Service { return &compose.Service{Image: "alpine:3.20", Restart: "always"} }
	// shimLog is the running row's fake runtime's invocation log. The watcher looks at each followed
	// container with an `inspect` that the log counts, so a row can wait for the looks it needs
	// rather than for a time that a slow `inspect` outruns (#1716).
	var shimLog func() []string
	inspects := func(services ...string) int {
		n := 0
		for _, svc := range services {
			n += countLines(shimLog(), "inspect "+svc+".demo.opossum")
		}
		return n
	}
	// rowT is the running row's *testing.T, for the helpers the rows' closures call.
	var rowT *testing.T
	// waitForInspects waits until each service has been looked at n more times than base says (a
	// map of the counts taken earlier): n looks begun are n−1 whole ones, each of which has answered
	// and been counted by the watcher before the next was begun. It says so, and goes on, if that
	// does not happen in a while (a watcher that is not looking, or a runtime so slow that the
	// follow could not have ended in the time the rows allow).
	waitForInspects := func(base map[string]int, n int) {
		deadline := time.Now().Add(3 * time.Second)
		for svc, was := range base {
			for inspects(svc) < was+n && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if got := inspects(svc) - was; got < n {
				rowT.Errorf("the watcher looked at %s %d times in 3 s, want %d: the row's premise (it was seen running, or stopped) does not hold", svc, got, n)
			}
		}
	}
	looksNow := func(services ...string) map[string]int {
		m := map[string]int{}
		for _, svc := range services {
			m[svc] = inspects(svc)
		}
		return m
	}
	rows := []row{
		{"stopped while followed", map[string]*compose.Service{"web": plain()}, []string{"web"}, nil,
			func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, true, []string{"web-1"}, false, nil, "", 0, false, false, false, 0, false},
		{"deleted while followed", map[string]*compose.Service{"web": plain()}, []string{"web"}, nil,
			func(rt *runtime.Runtime) { rt.Delete("web.demo.opossum") }, true, []string{"web-1"}, false, nil, "", 0, false, false, false, 0, false},
		{"already stopped when the follow began", map[string]*compose.Service{"web": plain()}, []string{"web"},
			func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, nil, false, nil, false, nil, "", 0, false, false, false, 0, true},
		{"a restart: policy", map[string]*compose.Service{"web": restarting()}, []string{"web"}, nil,
			func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, false, nil, false, nil, "", 0, false, false, false, 0, false},
		{"two followed, one stopped", map[string]*compose.Service{"web": plain(), "db": plain()}, []string{"web", "db"}, nil,
			func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, false, []string{"web-1"}, false, nil, "", 0, false, false, false, 0, false},
		{"two followed, both stopped", map[string]*compose.Service{"web": plain(), "db": plain()}, []string{"web", "db"}, nil,
			func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum"); rt.Stop("db.demo.opossum") }, true, []string{"web-1", "db-1"}, false, nil, "", 0, false, false, false, 0, false},
		{"one of two named, it stopped", map[string]*compose.Service{"web": plain(), "db": plain()}, []string{"web"}, nil,
			func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, true, []string{"web-1"}, false, nil, "", 0, false, false, false, 0, false},
		// Without --follow the log is read to its end whatever the container
		// does meanwhile (a long read, stopped part-way), and nothing is said.
		{"stopped during a read without --follow", map[string]*compose.Service{"web": plain()}, []string{"web"}, nil,
			func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, false, nil, true, nil, "", 0, false, false, false, 0, false},
		// A restart leaves the container stopped for a moment and starts it
		// again: the stream goes on (docker compose says `(restarting)` and
		// follows on).
		{name: "stopped and started again at once, as a restart does", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) {
				rt.Stop("web.demo.opossum")
				time.Sleep(100 * time.Millisecond)
				rt.Start("web.demo.opossum")
			}},
		// Bound LogsExitSettle (two seconds in these rows, SETTLE_MS) from both sides, with margins a slow
		// `inspect` does not eat: a gap of 1400 ms is what a halved settle (1000 ms) ends on and the real
		// one does not; a gap of 3600 ms is what the real one ends on and a doubled settle (4000 ms)
		// does not. The follow has to see the container stopped a settle long after a poll found it
		// stopped, and before it is started again, and each poll waits for its `inspect`: with 150 ms to
		// spare (a gap of 450 ms over a settle of 300) a slow `inspect` let the container be started again
		// before the second look, and the follow ran to the end of its stream (#1701, four times in CI; the
		// row below with a gap of 800 ms, 500 to spare, failed twice). With the `inspect` of every look
		// after the stop held back by D, the over row below passes up to D of about 800 ms (the old ones
		// failed from 100 and 450 ms). A settle of 1.5 times the real one is no longer told apart (the
		// old gap of 450 ms was under it): a gap that clear of a slow `inspect` and under 1.5 settles
		// would need a settle of four seconds.
		{name: "stopped and started again, a gap under settle", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) {
				rt.Stop("web.demo.opossum")
				time.Sleep(1400 * time.Millisecond)
				rt.Start("web.demo.opossum")
			}, env: []string{"SETTLE_MS=2000"}},
		// A plain stop and start after a gap longer than the settle is the end, and it is said (the
		// rows with an `opossum restart` marker below keep the follow going over the same kind of
		// gap).
		{name: "stopped and started again, a gap over settle", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) {
				rt.Stop("web.demo.opossum")
				time.Sleep(3600 * time.Millisecond)
				rt.Start("web.demo.opossum")
			}, env: []string{"SETTLE_MS=2000", "LOGS_SLEEP=6"}, ended: true, exited: []string{"web-1"}, within: 5000 * time.Millisecond},
		// What the runtime still hands over after the container has ended
		// comes before `exited`, not cut off. (The drips are timed from the
		// follow's start, one row's under way at the stop and the other's
		// starting after it; where the stop falls is in the log below.)
		{name: "lines still coming after the stop", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, ended: true, exited: []string{"web-1"},
			env: []string{"LOGS_DRIP=40", "LOGS_DRIP_AFTER=300"}, lastLine: "drip 40 web.demo.opossum"},
		// The lines still come after the stop is seen and settled: they are
		// written, and `exited` after them (the stream is not cut on the end).
		{name: "lines starting after the stop", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, ended: true, exited: []string{"web-1"},
			env: []string{"LOGS_DRIP=40", "LOGS_DRIP_AFTER=550"}, lastLine: "drip 40 web.demo.opossum"},
		// Stopped before the first tick after the follow began: it had been
		// seen running at once, so the stop counts.
		{name: "stopped before the first tick", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, ended: true, exited: []string{"web-1"},
			poll: time.Second, at0: true, env: []string{"LOGS_SLEEP=6"}, within: 5000 * time.Millisecond},
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
		// Bounds LogsExitDrain (150 ms in this test) from below: the drips
		// above are 20 ms apart, so any drain longer than that reads them the
		// same way and does not tell a shortened one from the real one. 100 ms
		// apart — under the drain, over a third of it — a shortened drain
		// would go idle between drips (the gap alone clears it) and cut the
		// stream before the last one arrives; the real one does not.
		{name: "lines a while apart, still under drain", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, ended: true, exited: []string{"web-1"},
			env: []string{"LOGS_DRIP=6", "LOGS_DRIP_AFTER=300", "LOGS_DRIP_INTERVAL=100"}, lastLine: "drip 6 web.demo.opossum"},
		// `opossum restart` whose stop leaves the container stopped longer than
		// the settle: it marks the service as restarting, so the follow goes on.
		{name: "opossum restart with a long gap", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			env: []string{"STOP_THEN_SLEEP_MS=800"}, restart: true},
		// A reader that stalls: lines waiting to be taken are not a stream gone
		// quiet, and are not cut off.
		{name: "a slow reader, lines still coming after the stop", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) { rt.Stop("web.demo.opossum") }, ended: true, exited: []string{"web-1"},
			env: []string{"LOGS_DRIP=60", "LOGS_DRIP_AFTER=550", "LOGS_SLEEP=8"}, lastLine: "drip 60 web.demo.opossum", slow: true, within: 7 * time.Second},
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
		// that stopped meanwhile. (db's restart holds the row for four seconds,
		// so its stream stays open six: the row acts while it is followed.)
		{name: "one stopped while another is restarted", services: map[string]*compose.Service{"web": plain(), "db": plain()}, follow: []string{"web", "db"},
			during: func(rt *runtime.Runtime) {
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); restartOf("db") }()
				time.Sleep(200 * time.Millisecond) // db's restart is under way
				go func() { defer wg.Done(); rt.Stop("web.demo.opossum") }()
				expectExitedWithin("web-1", 2*time.Second)
				wg.Wait()
			}, env: []string{"STOP_THEN_SLEEP_MS=4000", "LOGS_SLEEP=6"}, exited: []string{"web-1"}},
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
				// Three looks after the marker goes (the first sees it stopped, the one after
				// the settle ends it) each take as long as a slow `inspect` does: the end is
				// given room for that, with the stream kept open past it (#1716).
			}, env: []string{"LOGS_SLEEP=6"}, ended: true, exited: []string{"web-1"}, within: 5000 * time.Millisecond},
		// The gap was already under way, seen and answerable, before the runtime
		// stopped answering at all: the settle counts from then, not from when it
		// starts answering again. (The marker in the row above goes up before the
		// stop, so it never has a stopped answer to count from until it comes
		// down; this one does, well before the marker goes up.)
		{name: "the runtime not answering long after it was seen stopped", services: map[string]*compose.Service{"web": plain()}, follow: []string{"web"},
			during: func(rt *runtime.Runtime) {
				rt.Stop("web.demo.opossum")
				// Seen stopped and answerable at least once: two looks begun after the stop, the first of
				// which read the state after it and has answered — not two polls' time, which a slow
				// `inspect` outruns. (The look's own `now` may be taken before the stop returns, which
				// only makes the count a few milliseconds earlier.)
				waitForInspects(looksNow("web"), 2)
				marker := ""
				for _, kv := range rt.Env {
					if f, ok := strings.CutPrefix(kv, "INSPECT_FAIL_WHILE="); ok {
						marker = f
					}
				}
				os.WriteFile(marker, nil, 0o644)
				time.Sleep(2000 * time.Millisecond) // longer than the settle (SETTLE_MS): a reset here would show
				os.Remove(marker)
				// Measured from here, not from the follow's start (which the
				// wait for the first line and the looks before during runs make
				// too noisy a clock for a bound this tight): a reset needs a whole
				// settle (1500 ms) more than the real code does once the runtime
				// answers again — two looks and a drain, which a slow `inspect`
				// stretches (about 450 ms each is what 1000 ms holds).
				expectExitedWithin("web-1", 1000*time.Millisecond)
			}, env: []string{"SETTLE_MS=1500", "LOGS_SLEEP=6"}, ended: true, exited: []string{"web-1"}, within: 5000 * time.Millisecond},
		// The runtime not answering while followed is not the container's
		// end: the follow goes on (docker compose knows from its events).
		{"the runtime not answering while followed", map[string]*compose.Service{"web": plain()}, []string{"web"}, nil,
			func(rt *runtime.Runtime) {
				for _, kv := range rt.Env {
					if f, ok := strings.CutPrefix(kv, "INSPECT_FAIL_WHILE="); ok {
						os.WriteFile(f, nil, 0o644)
					}
				}
			}, false, nil, false, nil, "", 0, false, false, false, 0, false},
	}
	// The first exec of the compiled shim in this process costs measurably more
	// than a later one (measured: ~210-360 ms for the first `system status`
	// against this test's binary, 4-7 ms once warm — against a run's own timing
	// margin of a few hundred ms) — whichever row runs first would otherwise
	// pay it, and running this test alone (`-run .../<row>$`, a normal way to
	// chase one row down) has nothing earlier in the package to have paid it
	// already. One throwaway call here, untimed, so every row below measures
	// the shim once it is warm.
	_ = exec.Command(fakeShimBin, "system", "status").Run()
	for _, tc := range rows {
		// --no-log-prefix changes the lines, not the end: it is run again only
		// where an `exited` line (or a last line before it) is looked for — and
		// on one row whose follow goes on, so that a prefix-less follow that
		// counted every stream as ended (a mutation the rows with an `exited`
		// line let through) is still caught.
		variants := []bool{false}
		if len(tc.exited) > 0 || tc.lastLine != "" || tc.prefixToo {
			variants = append(variants, true)
		}
		for _, noPrefix := range variants {
			t.Run(tc.name+map[bool]string{false: "", true: ", --no-log-prefix"}[noPrefix], func(t *testing.T) {
				t.Setenv("XDG_STATE_HOME", t.TempDir())
				rt, log := fakeShim(t)
				shimLog, rowT = log, t
				// The stream stays open for stream, where the row does not say
				// (LOGS_SLEEP): a follow that ends on the container's end is over
				// within `within`, and one that goes on until its streams end
				// takes at least stream. What each row takes is in the log below.
				setShimEnv(rt, "LOGS_SLEEP="+strconv.Itoa(int(stream/time.Second)), "INSPECT_FAIL_WHILE="+filepath.Join(t.TempDir(), "not-answering"))
				setShimEnv(rt, tc.env...)
				// Every service the row follows has a container the runtime has: made before the row's own
				// `before` stops one, and before the clock below starts.
				var made []string
				for svc := range tc.services {
					made = append(made, svc+".demo.opossum")
				}
				sort.Strings(made)
				strictContainers(t, rt, made...)
				if tc.before != nil {
					tc.before(rt)
				}
				if tc.poll != 0 {
					saved := orchestrator.LogsExitPoll
					orchestrator.LogsExitPoll = tc.poll
					defer func() { orchestrator.LogsExitPoll = saved }()
				}
				// A row that bounds the settle says its own (SETTLE_MS in env, read here and not by the
				// fake): the rows that tell a halved or a doubled settle from the real one keep their
				// gaps within a few hundred milliseconds of it, and a CI machine's `inspect` is
				// slow enough to eat that (see the rows' comment), so they run with a settle of two
				// seconds and margins to match.
				for _, e := range tc.env {
					if n, ok := strings.CutPrefix(e, "SETTLE_MS="); ok {
						ms, err := strconv.Atoi(n)
						if err != nil {
							t.Fatal(err)
						}
						saved := orchestrator.LogsExitSettle
						orchestrator.LogsExitSettle = time.Duration(ms) * time.Millisecond
						defer func() { orchestrator.LogsExitSettle = saved }()
					}
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
				// The services the follow watches: not one with a `restart:` policy, and none without --follow.
				var watched []string
				for _, name := range tc.follow {
					if r := tc.services[name].Restart; !tc.noFollow && (r == "" || r == "no") {
						watched = append(watched, name)
					}
				}
				// A row that acts once the first look has answered counts the answers: the runtime is run
				// through a script that notes each `inspect` when it is over, because the shim logs a
				// look when it begins and reads the state a moment after.
				var answeredLooks func() int
				if tc.at0 {
					dir := t.TempDir()
					noted := filepath.Join(dir, "answered")
					wrap := filepath.Join(dir, "container")
					script := "#!/bin/sh\n" + shellQuote(rt.Bin) + " \"$@\"\nrc=$?\nif [ \"$1\" = inspect ]; then echo >> " + shellQuote(noted) + "; fi\nexit $rc\n"
					if err := os.WriteFile(wrap, []byte(script), 0o755); err != nil {
						t.Fatal(err)
					}
					rt.Bin = wrap
					answeredLooks = func() int { b, _ := os.ReadFile(noted); return strings.Count(string(b), "\n") }
				}
				done := make(chan error, 1)
				start := time.Now()
				go func() {
					done <- o.Logs(tc.follow, runtime.LogsOptions{Follow: !tc.noFollow, NoLogPrefix: noPrefix})
				}()
				// Under way: every followed stream has written its line; a row
				// that does not act at once waits eight polls more.
				for i := 0; i < 200 && strings.Count(out.String(), "log-line") < len(tc.follow); i++ {
					time.Sleep(10 * time.Millisecond)
				}
				if tc.at0 {
					// As soon as the lines are written, but after the first look has answered: a look reads
					// the state when the runtime is asked, and a slow `inspect` that held the first one back
					// past the stop would read it stopped, never having seen it running — a container found
					// stopped is followed on, as it is meant to be (#1716). The poll of this row is long, so
					// the stop is still before the second look.
					//
					// And the look is made at once, not at the first tick: a watcher that began by waiting a
					// poll would read it after the stop here (poll/2 is the room a slow `inspect` has).
					//
					// Each watched service is looked at twice before that: once to choose what to follow
					// (the project's own containers), once by the watcher.
					want := 2 * len(watched)
					first := time.Now().Add(orchestrator.LogsExitPoll / 2)
					for answeredLooks() < want && time.Now().Before(first) {
						time.Sleep(10 * time.Millisecond)
					}
					if n := answeredLooks(); n < want {
						t.Errorf("%d of %d inspects had answered within half a poll (%v) of the follow's start: the watcher's first look is made at once, not after a tick", n, want, orchestrator.LogsExitPoll/2)
					}
				} else {
					// Under way: eight polls' time, which the rows with a timed drip count on — and, where
					// a slow `inspect` outruns that, until each watched service has been looked at
					// twice since the lines were written (the first look answered: it was seen running).
					// A service with a `restart:` policy is not watched, and nothing is without --follow:
					// those wait the time only.
					looks := looksNow(watched...)
					time.Sleep(8 * orchestrator.LogsExitPoll)
					waitForInspects(looks, 2)
				}
				if tc.during != nil {
					tc.during(rt)
				}
				if tc.restart {
					restartOf("web")
				}
				// A row's action has to be over while the stream is still open,
				// or it is acting on nothing — and a follow that goes on would
				// pass for the wrong reason. The log below is said at once; the
				// check is said once the follow has ended, so that a row which
				// fails it has nothing left running into the next one (a follow
				// that never ends is failed as it is).
				rowStream := stream
				for _, e := range tc.env {
					if n, ok := strings.CutPrefix(e, "LOGS_SLEEP="); ok {
						secs, err := strconv.Atoi(n)
						if err != nil {
							t.Fatal(err)
						}
						rowStream = time.Duration(secs) * time.Second
					}
				}
				acted := time.Since(start)
				// Said on every run, before the wait, so a `-v` log shows the
				// row's action against its stream, even for a follow that
				// never ends.
				t.Logf("the row's action took %v of its %v stream", acted, rowStream)
				var err error
				select {
				case err = <-done:
				case <-time.After(20 * time.Second):
					t.Fatal("the follow did not end")
				}
				if acted > rowStream {
					t.Errorf("the row's action ran past the stream: %v of %v", acted, rowStream)
				}
				took := time.Since(start)
				t.Logf("the follow took %v", took)
				if err != nil {
					t.Errorf("want a zero exit, got %v", err)
				}
				within := endedWithin
				if tc.within != 0 {
					within = tc.within
				}
				if within >= rowStream {
					t.Fatalf("the row's ended-within (%v) is not under its stream (%v): a follow that ran to the stream's end would pass as one the container ended", within, rowStream)
				}
				if tc.ended && took > within {
					t.Errorf("want the follow ended by the container's end, took %v", took)
				}
				if !tc.ended && took < rowStream-200*time.Millisecond {
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
			strictContainers(t, rt, "web.demo.opossum")
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
				var made []string
				for i := 0; i < k.services; i++ {
					made = append(made, "web"+strconv.Itoa(i)+".demo.opossum")
				}
				strictContainers(t, rt, made...)
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

// shellQuote single-quotes s for a /bin/sh script.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
