package orchestrator

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	rt "github.com/suruseas/opossum/internal/runtime"
)

// carryOnFixture is six services, all stopped by an earlier `opossum stop`:
// aaa's and ddd's starts fail, bbb's works, the runtime gives no readable
// answer about who owns ccc, eee depends on bbb and then aaa, and fff depends
// on eee. It returns the
// orchestrator and the `start` lines the runtime saw.
func carryOnFixture(t *testing.T) (*Orchestrator, func() []string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %q
case "$1" in
  inspect)
    case "$2" in
      ccc*) echo 'boom' >&2; exit 2 ;;
      *) echo '[{"status":{"state":"stopped"},"configuration":{"id":"x","labels":{"opossum.project":"demo"}}}]' ;;
    esac ;;
  start)
    case "$2" in aaa*|ddd*) echo "Error: get failed: container $2 not found" >&2; exit 1 ;; esac ;;
esac
exit 0
`, log)
	shim := filepath.Join(dir, "c.sh")
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"aaa": {Name: "aaa", Image: "alpine"},
		"bbb": {Name: "bbb", Image: "alpine"},
		"ccc": {Name: "ccc", Image: "alpine"},
		"ddd": {Name: "ddd", Image: "alpine"},
		"eee": {Name: "eee", Image: "alpine", DependsOn: compose.DependsOn{{Name: "bbb"}, {Name: "aaa"}}},
		"fff": {Name: "fff", Image: "alpine", DependsOn: compose.DependsOn{{Name: "eee"}}},
	}}
	o := New(p, &rt.Runtime{Bin: shim}, "opossum", &bytes.Buffer{})
	for name := range p.Services {
		o.MarkStopped(name)
	}
	return o, func() []string {
		b, _ := os.ReadFile(log)
		var starts []string
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "start ") {
				starts = append(starts, strings.TrimSuffix(strings.TrimPrefix(l, "start "), ".demo.opossum"))
			}
		}
		return starts
	}
}

// A service whose container will not start does not decide the fate of the
// services after it: both commands go on to the rest and say every failure,
// in order, and then the container nobody would say the owner of. Returning at
// the first failure left the later services unstarted — for restart, stopped by
// the restart itself — and the unanswered container unmentioned (container
// 1.4.1). Start leaves out a service whose dependency did not start, as docker
// compose's start does; restart, like docker compose's, does not look at
// dependencies. Every service a command tried to start has its earlier stop
// undone, including those after a failure.
func TestStartAndRestartCarryOnPastAServiceThatWillNotStart(t *testing.T) {
	failure := func(verb, svc string) string {
		return fmt.Sprintf("\n- %sing service %q: starting %q: exit status 1\n    Error: get failed: container %s.demo.opossum not found\n    `", verb, svc, svc+".demo.opossum", svc)
	}
	for _, tc := range []struct {
		name     string
		run      func(o *Orchestrator) error
		starts   []string
		parts    []string // in order
		stillOff []string // services whose stop record is kept
	}{
		{"start", func(o *Orchestrator) error { return o.Start(nil) },
			[]string{"aaa", "bbb", "ddd"},
			[]string{"4 services did not start:", failure("start", "aaa"), failure("start", "ddd"),
				"\n- " + `not starting service "eee": it depends on "aaa", which did not start`,
				"\n- " + `not starting service "fff": it depends on "eee", which did not start` + "\n\nthe runtime gave no readable answer",
				"so they were left: ccc.demo.opossum — ", "run `opossum start` again once it answers"},
			[]string{"ccc", "eee", "fff"}},
		{"start, the dependent named first", func(o *Orchestrator) error { return o.Start([]string{"eee", "aaa"}) },
			[]string{"aaa"},
			[]string{"2 services did not start:", failure("start", "aaa"), "\n- " + `not starting service "eee": it depends on "aaa", which did not start`},
			[]string{"bbb", "ccc", "ddd", "eee", "fff"}},
		{"start, a dependency not asked for", func(o *Orchestrator) error { return o.Start([]string{"eee"}) },
			[]string{"eee"}, nil,
			[]string{"aaa", "bbb", "ccc", "ddd", "fff"}},
		{"restart", func(o *Orchestrator) error { return o.Restart(nil) },
			[]string{"aaa", "bbb", "ddd", "eee", "fff"},
			[]string{"2 services did not restart:", failure("restart", "aaa"), failure("restart", "ddd") + "restart` needs",
				"\n\nthe runtime gave no readable answer", "so they were left: ccc.demo.opossum — ", "run `opossum restart` again once it answers"},
			[]string{"ccc"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, starts := carryOnFixture(t)
			err := tc.run(o)
			if got := starts(); !slices.Equal(got, tc.starts) {
				t.Errorf("started %q, want %q", got, tc.starts)
			}
			if len(tc.parts) == 0 {
				if err != nil {
					t.Errorf("want no error, got:\n%v", err)
				}
			} else if err == nil {
				t.Fatal("want an error")
			} else {
				checkParts(t, err.Error(), tc.parts)
				// Listed under a message of its own, the refusal about ccc is
				// still what a caller like watch finds by type.
				if strings.Contains(err.Error(), "ccc.demo.opossum") && !errors.As(err, new(ownerRefusal)) {
					t.Errorf("the owner refusal should still unwrap from:\n%v", err)
				}
			}
			for _, name := range []string{"aaa", "bbb", "ccc", "ddd", "eee", "fff"} {
				if want := slices.Contains(tc.stillOff, name); o.wasStoppedByUs(name) != want {
					t.Errorf("%s: stop record kept = %v, want %v", name, !want, want)
				}
			}
		})
	}
}

// checkParts says whether msg begins with the first part, ends with the last,
// and holds each part exactly once, in order.
func checkParts(t *testing.T, msg string, parts []string) {
	t.Helper()
	{
		{
			at := 0
			if !strings.HasPrefix(msg, parts[0]) {
				t.Errorf("want the error to begin %q, got:\n%s", parts[0], msg)
			}
			for _, part := range parts {
				if strings.Count(msg, part) != 1 {
					t.Errorf("want %q once, got:\n%s", part, msg)
					continue
				}
				i := strings.Index(msg, part)
				if i < at {
					t.Errorf("%q is out of order in:\n%s", part, msg)
				}
				at = i
			}
			if !strings.HasSuffix(msg, parts[len(parts)-1]) {
				t.Errorf("want the error to end %q, got:\n%s", parts[len(parts)-1], msg)
			}
		}
	}
}
