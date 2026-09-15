package orchestrator_test

// `logs` prefixes every line with its service as docker compose v5.5.1 writes
// it (measured): `<service>-1` padded to one more than the longest of the
// services shown, then ` | ` — for one service as for several, followed or
// not. `--no-log-prefix` leaves the lines as the container wrote them.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/signal"
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

func TestLogsPrefixEveryLineAsDockerComposeWritesThem(t *testing.T) {
	// database is the longest name, x the shortest; web depends on database and
	// database on x, so the startup order (x, database, web) is not the order
	// of the names.
	svcs := func() map[string]*compose.Service {
		return map[string]*compose.Service{
			"web":      {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "database"}}},
			"database": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "x"}}},
			"x":        {Image: "alpine:3.20"},
		}
	}
	type row struct {
		name     string
		services []string
		opts     runtime.LogsOptions
		text     string // $LOGS_TEXT; "" leaves the fake's own line
		want     string // the whole of stdout; "" when the order is not fixed
		contains []string
	}
	rows := []row{
		{"one service", []string{"web"}, runtime.LogsOptions{}, "", "web-1  | log-line web.demo.opossum\n", nil},
		{"one service followed", []string{"web"}, runtime.LogsOptions{Follow: true}, "", "web-1  | log-line web.demo.opossum\n", nil},
		{"one service with a longer name", []string{"database"}, runtime.LogsOptions{}, "", "database-1  | log-line database.demo.opossum\n", nil},
		{"every service, in startup order, padded to the longest", nil, runtime.LogsOptions{}, "",
			"x-1         | log-line x.demo.opossum\ndatabase-1  | log-line database.demo.opossum\nweb-1       | log-line web.demo.opossum\n", nil},
		// The width is of the services shown: x beside web is padded to web-1.
		{"two named, in the order named", []string{"x", "web"}, runtime.LogsOptions{}, "",
			"x-1    | log-line x.demo.opossum\nweb-1  | log-line web.demo.opossum\n", nil},
		{"two named, followed", []string{"x", "web"}, runtime.LogsOptions{Follow: true}, "", "",
			[]string{"x-1    | log-line x.demo.opossum\n", "web-1  | log-line web.demo.opossum\n"}},
		{"several lines, a blank one, a tab and no last newline", []string{"web"}, runtime.LogsOptions{}, "hello {name}\n\ntab\there\nnonl",
			"web-1  | hello web.demo.opossum\nweb-1  | \nweb-1  | tab\there\nweb-1  | nonl\n", nil},
		// A carriage return the container wrote is kept, at the end of a line
		// or inside it, as docker compose v5.5.1 keeps it (measured).
		{"carriage returns", []string{"web"}, runtime.LogsOptions{}, "cr\r\nmid\rx\ncrcr\r\r\n",
			"web-1  | cr\r\nweb-1  | mid\rx\nweb-1  | crcr\r\r\n", nil},
		{"carriage returns, followed", []string{"x", "web"}, runtime.LogsOptions{Follow: true}, "cr\r\n", "",
			[]string{"x-1    | cr\r\n", "web-1  | cr\r\n"}},
		{"carriage returns, --no-log-prefix", []string{"web"}, runtime.LogsOptions{NoLogPrefix: true}, "cr\r\nmid\rx\n",
			"cr\r\nmid\rx\n", nil},
		{"--no-log-prefix, one service", []string{"web"}, runtime.LogsOptions{NoLogPrefix: true}, "hello {name}\n\nnonl",
			"hello web.demo.opossum\n\nnonl\n", nil},
		{"--no-log-prefix, every service", nil, runtime.LogsOptions{NoLogPrefix: true}, "",
			"log-line x.demo.opossum\nlog-line database.demo.opossum\nlog-line web.demo.opossum\n", nil},
		{"--no-log-prefix, followed", []string{"x", "web"}, runtime.LogsOptions{Follow: true, NoLogPrefix: true}, "", "",
			[]string{"log-line x.demo.opossum\n", "log-line web.demo.opossum\n"}},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			if tc.text != "" {
				setShimEnv(rt, "LOGS_TEXT="+tc.text)
			}
			var out bytes.Buffer
			if err := orchestrator.New(project("demo", svcs()), rt, "opossum", &out).Logs(tc.services, tc.opts); err != nil {
				t.Fatalf("Logs: %v", err)
			}
			if tc.want != "" && out.String() != tc.want {
				t.Errorf("\n got %q\nwant %q", out.String(), tc.want)
			}
			for _, c := range tc.contains {
				if !strings.Contains(out.String(), c) {
					t.Errorf("want %q in\n%s", c, out.String())
				}
			}
			if tc.want == "" && strings.Count(out.String(), "\n") != len(tc.contains) {
				t.Errorf("want %d lines, got\n%s", len(tc.contains), out.String())
			}
			if tc.opts.NoLogPrefix && strings.Contains(out.String(), " | ") {
				t.Errorf("want no prefix, got\n%s", out.String())
			}
		})
	}
}

// What the runtime itself says when it cannot read a container's logs goes to
// stderr as it said it, not onto stdout as a prefixed log line; the command
// fails naming the service, and stops there, as before.
func TestLogsSayTheRuntimesFailureOnStderr(t *testing.T) {
	for _, tc := range []struct {
		services []string
		follow   bool
	}{{[]string{"web"}, false}, {[]string{"web", "x"}, false}, {[]string{"web"}, true}} {
		services := tc.services
		t.Run(strings.Join(services, " ")+map[bool]string{false: "", true: " --follow"}[tc.follow], func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, "LOGS_FAIL=web.demo.opossum")
			p := project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20"}, "x": {Image: "alpine:3.20"}})
			var out bytes.Buffer
			var err error
			stderr := stderrOf(t, func() {
				err = orchestrator.New(p, rt, "opossum", &out).Logs(services, runtime.LogsOptions{Follow: tc.follow})
			})
			if err == nil || !strings.HasPrefix(err.Error(), `logs for service "web": exit status 1`) || !strings.Contains(err.Error(), "opossum ps") {
				t.Errorf("want the failure named with its next step, got %v", err)
			}
			if out.Len() != 0 {
				t.Errorf("want nothing on stdout, got %q", out.String())
			}
			if !strings.Contains(stderr, "Error: failed to get logs for container web.demo.opossum") {
				t.Errorf("want the runtime's words on stderr, got %q", stderr)
			}
			for _, l := range log() {
				if strings.HasPrefix(l, "logs x.") {
					t.Errorf("want x not read after web failed, got %v", log())
				}
			}
		})
	}
}

// The prefix is as wide as the services shown: a service whose container is
// another project's is not shown, and does not widen it, however long its
// name.
func TestLogsPrefixWidthLeavesOutAServiceNotShown(t *testing.T) {
	for _, follow := range []bool{false, true} {
		t.Run(map[bool]string{false: "logs", true: "logs --follow"}[follow], func(t *testing.T) {
			rt, _ := fakeShim(t)
			setShimEnv(rt, "INSPECT_OWNER=database.demo.opossum=otherproj")
			p := project("demo", map[string]*compose.Service{
				"db":       {Image: "alpine:3.20"},
				"web":      {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "db"}}},
				"database": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "web"}}},
			})
			var out bytes.Buffer
			var err error
			stderrOf(t, func() {
				err = orchestrator.New(p, rt, "opossum", &out).Logs(nil, runtime.LogsOptions{Follow: follow})
			})
			if err != nil {
				t.Fatalf("Logs: %v", err)
			}
			want := "db-1   | log-line db.demo.opossum\nweb-1  | log-line web.demo.opossum\n"
			if follow {
				for _, l := range strings.SplitAfter(want, "\n")[:2] {
					if !strings.Contains(out.String(), l) {
						t.Errorf("want %q in\n%s", l, out.String())
					}
				}
				if strings.Count(out.String(), "\n") != 2 {
					t.Errorf("want 2 lines, got\n%s", out.String())
				}
				return
			}
			if out.String() != want {
				t.Errorf("\n got %q\nwant %q", out.String(), want)
			}
		})
	}
}

// firstWrite cancels a context when the first line is written to it.
type firstWrite struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	cancel context.CancelFunc
}

func (w *firstWrite) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cancel()
	return w.buf.Write(p)
}

func (w *firstWrite) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// A Ctrl-C (the cancel of the context the command runs under) ends `logs`
// where it is, whether it follows or not: it returns ErrInterrupted, which the
// CLI turns into a silent exit 130 as docker compose v5.5.1 exits, reads no
// further service, and says nothing on stderr — not "logs for service …
// context canceled", which is what the cancel does to the next stream.
func TestLogsEndQuietlyOnCtrlC(t *testing.T) {
	for _, tc := range []struct {
		name     string
		services []string
		opts     runtime.LogsOptions
		lines    int
	}{
		{"several services, not followed", []string{"x", "web"}, runtime.LogsOptions{}, 1},
		// Three, so the service after the cancel is not the last one.
		{"three services, not followed", []string{"x", "db", "web"}, runtime.LogsOptions{}, 1},
		{"one service, followed", []string{"web"}, runtime.LogsOptions{Follow: true}, 1},
		{"several services, followed", []string{"x", "web"}, runtime.LogsOptions{Follow: true}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			// The stream stays open, as a long read does, so the cancel comes
			// while the first service is read.
			setShimEnv(rt, "LOGS_SLEEP=30")
			p := project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20"}, "db": {Image: "alpine:3.20"}, "x": {Image: "alpine:3.20"}})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			out := &firstWrite{cancel: cancel}
			o := orchestrator.New(p, rt, "opossum", out)
			o.OnSignal(ctx)
			start := time.Now()
			var err error
			stderr := stderrOf(t, func() { err = o.Logs(tc.services, tc.opts) })
			if !errors.Is(err, orchestrator.ErrInterrupted) {
				t.Errorf("want ErrInterrupted, got %v", err)
			}
			if d := time.Since(start); d > 15*time.Second {
				t.Errorf("want the stream stopped by the cancel, took %v", d)
			}
			if stderr != "" {
				t.Errorf("want nothing said on stderr, got %q", stderr)
			}
			if tc.lines > 0 {
				if n := strings.Count(out.String(), "\n"); n != tc.lines {
					t.Errorf("want %d line before the cancel, got %q", tc.lines, out.String())
				}
				for _, l := range log() {
					for _, later := range tc.services[1:] {
						if strings.HasPrefix(l, "logs") && strings.Contains(l, later+".demo.opossum") {
							t.Errorf("want %s not read after the cancel, got %v", later, log())
						}
					}
				}
			}
			if strings.Contains(out.String(), "logs error") {
				t.Errorf("want no error line on stdout, got %q", out.String())
			}
		})
	} // The cancel between two streams: the first has ended, and the next is
	// about to start when the Ctrl-C comes, so it is the start that the cancel
	// refuses (`context canceled`). That is the interrupt too.
	t.Run("between two streams", func(t *testing.T) {
		rt, log := fakeShim(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		rt.Verbose = true
		rt.Trace = &cancelOnTrace{cancel: cancel, at: "logs web.demo.opossum"}
		p := project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20"}, "x": {Image: "alpine:3.20"}})
		var out bytes.Buffer
		o := orchestrator.New(p, rt, "opossum", &out)
		o.OnSignal(ctx)
		var err error
		stderr := stderrOf(t, func() { err = o.Logs([]string{"x", "web"}, runtime.LogsOptions{}) })
		if !errors.Is(err, orchestrator.ErrInterrupted) || stderr != "" {
			t.Errorf("want ErrInterrupted and nothing said, got %v and %q", err, stderr)
		}
		if out.String() != "x-1    | log-line x.demo.opossum\n" {
			t.Errorf("want only x's line, got %q", out.String())
		}
		for _, l := range log() {
			if strings.HasPrefix(l, "logs web.") {
				t.Errorf("want web not started, got %v", log())
			}
		}
	})
}

// cancelOnTrace cancels a context when the runtime traces a command holding at.
type cancelOnTrace struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	at     string
}

func (w *cancelOnTrace) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if strings.Contains(string(p), w.at) {
		w.cancel()
	}
	return len(p), nil
}

// onLine calls f once, the first time a written line holds at.
type onLine struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	at   string
	f    func()
	done bool
}

func (w *onLine) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	if !w.done && strings.Contains(w.buf.String(), w.at) {
		w.done = true
		w.f()
	}
	return len(p), nil
}

func (w *onLine) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// A Ctrl-C reaches the runtime and opossum alike, and the runtime (container
// 1.4.1 exits 130 of its own) can end before opossum has taken it: when the
// Ctrl-C reaches opossum within runtime.LogsCancelGrace after, that end is the
// interrupt — ErrInterrupted, nothing on stderr, no error line on stdout, no
// further service read. The stream runs longer than the grace before the
// runtime ends (a person presses Ctrl-C on a stream that has been running), and
// the Ctrl-C is a real SIGINT to this process, with nothing handed to
// OnSignal, as `opossum logs` has it. With no Ctrl-C to opossum (the runtime
// ended from outside) it is still the failure to read, said promptly.
func TestLogsReadTheRuntimeEndingJustBeforeTheCtrlCAsTheInterrupt(t *testing.T) {
	grace := runtime.LogsCancelGrace
	for _, tc := range []struct {
		name     string
		services []string
		opts     runtime.LogsOptions
	}{
		{"one service, followed", []string{"web"}, runtime.LogsOptions{Follow: true}},
		{"several services, followed", []string{"x", "web"}, runtime.LogsOptions{Follow: true}},
		{"three services, not followed", []string{"x", "db", "web"}, runtime.LogsOptions{}},
	} {
		for _, ctrlC := range []bool{true, false} {
			t.Run(tc.name+map[bool]string{true: ", Ctrl-C to opossum after", false: ", no Ctrl-C to opossum"}[ctrlC], func(t *testing.T) {
				held := make(chan os.Signal, 1)
				signal.Notify(held, syscall.SIGINT)
				defer signal.Stop(held)
				rt, log := fakeShim(t)
				setShimEnv(rt, "LOGS_SLEEP=30", "LOGS_SELF_INT="+strconv.Itoa(int(2*grace/time.Millisecond)))
				p := project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20"}, "db": {Image: "alpine:3.20"}, "x": {Image: "alpine:3.20"}})
				// 100 ms after the runtime has ended of the Ctrl-C it was given.
				out := &onLine{at: "self-int", f: func() {
					if ctrlC {
						time.AfterFunc(100*time.Millisecond, func() { syscall.Kill(os.Getpid(), syscall.SIGINT) })
					}
				}}
				o := orchestrator.New(p, rt, "opossum", out)
				var err error
				var took time.Duration
				stderr := stderrOf(t, func() {
					start := time.Now()
					err = o.Logs(tc.services, tc.opts)
					took = time.Since(start)
				})
				if !ctrlC {
					if took > 2*grace+3*time.Second {
						t.Errorf("want the failure said promptly, took %v", took)
					}
					said := err
					if len(tc.services) > 1 && tc.opts.Follow {
						said = errors.New(out.String())
						if err == nil || !strings.HasPrefix(err.Error(), "could not follow logs for any of the 2 service(s)") {
							t.Errorf("want every stream failed, got %v", err)
						}
					}
					if err == nil || errors.Is(err, orchestrator.ErrInterrupted) || !strings.Contains(said.Error(), "exit status 130") {
						t.Errorf("want the failure to read, as before, got %v (stdout %q)", err, out.String())
					}
					return
				}
				if !errors.Is(err, orchestrator.ErrInterrupted) || stderr != "" {
					t.Errorf("want ErrInterrupted and nothing said, got %v and %q", err, stderr)
				}
				if strings.Contains(out.String(), "logs error") {
					t.Errorf("want no error line on stdout, got %q", out.String())
				}
				if !tc.opts.Follow {
					for _, l := range log() {
						if strings.HasPrefix(l, "logs db.") || strings.HasPrefix(l, "logs web.") {
							t.Errorf("want nothing read after x, got %v", log())
						}
					}
				}
			})
		}
	}
}
