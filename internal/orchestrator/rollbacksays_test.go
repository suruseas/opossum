package orchestrator_test

// A failed `up` rolls back what it started — it has since #28 — but the output
// never said so: "Starting cache", then the failure, and nothing about what
// became of cache, which reads as "still running". It is not (docker compose
// does leave it running; measured on v5.5.0 with the same two-service shape).
// Now the rollback names what it stopped and removed — checked against the
// runtime, not assumed — and stays quiet under --dry-run and when nothing
// was started.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// readOnlyParent gives a directory a bind source cannot be created under.
func readOnlyParent(t *testing.T) string {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	parent := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o700) })
	return parent
}

func TestAFailedUpSaysWhatItRolledBack(t *testing.T) {
	parent := readOnlyParent(t)
	// The fake shim answers `inspect` for any name; the rolled-back one must
	// read as gone for the message to say "removed".
	t.Setenv("INSPECT_ABSENT", "cache.demo.opossum")
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"cache": {Image: "alpine:3"},
		"db": {
			Image:     "alpine:3",
			DependsOn: compose.DependsOn{{Name: "cache"}},
			Volumes:   []string{filepath.Join(parent, "child") + ":/data"},
		},
	})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	err := o.Up(true)
	if err == nil || !strings.Contains(err.Error(), "[OPSM-104]") {
		t.Fatalf("db's bind source cannot be made, so up must fail with OPSM-104, got: %v", err)
	}
	lines := log()
	if indexOf(lines, "run -d --name cache.demo.opossum") < 0 || indexOf(lines, "stop cache.demo.opossum") < 0 {
		t.Fatalf("cache should have started and then been stopped by the rollback, got %v", lines)
	}
	got := out.String()
	if !strings.Contains(got, "Rolled back cache — stopped and removed; nothing this `up` started is left running") {
		t.Errorf("the output should say what was rolled back, got:\n%s", got)
	}
	if strings.Contains(got, "db") {
		t.Errorf("db never started and must not be named, got:\n%s", got)
	}
}

// The names come in the order the services started — five of them, in an
// order that is not alphabetical, so a map walk would be caught.
func TestTheRolledBackServicesAreNamedInStartOrder(t *testing.T) {
	parent := readOnlyParent(t)
	t.Setenv("INSPECT_ABSENT", "zulu.demo.opossum yankee.demo.opossum xray.demo.opossum whiskey.demo.opossum victor.demo.opossum")
	chain := []string{"zulu", "yankee", "xray", "whiskey", "victor"}
	// Several runs: a walk over a map of five would come out in start order
	// by luck one time in five, and an order-keeping walk never varies.
	for run := 0; run < 6; run++ {
		rt, _ := fakeShim(t)
		services := map[string]*compose.Service{}
		for i, name := range chain {
			svc := &compose.Service{Image: "alpine:3"}
			if i > 0 {
				svc.DependsOn = compose.DependsOn{{Name: chain[i-1]}}
			}
			services[name] = svc
		}
		services["bad"] = &compose.Service{Image: "alpine:3", DependsOn: compose.DependsOn{{Name: "victor"}}, Volumes: []string{filepath.Join(parent, "child") + ":/data"}}
		var out bytes.Buffer
		if err := orchestrator.New(project("demo", services), rt, "opossum", &out).Up(true); err == nil {
			t.Fatal("up must fail on bad")
		}
		if !strings.Contains(out.String(), "Rolled back zulu, yankee, xray, whiskey, victor — stopped and removed") {
			t.Fatalf("run %d: want the five in start order, got:\n%s", run, out.String())
		}
	}
}

// Nothing to say: a failure before anything started, and a dry run (which
// starts nothing and tears nothing down, whatever the plan reached).
func TestNoRollbackMessageWhenNothingWasStarted(t *testing.T) {
	parent := readOnlyParent(t)
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"db": {Image: "alpine:3", Volumes: []string{filepath.Join(parent, "child") + ":/data"}},
	})
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err == nil {
		t.Fatal("up must fail on db")
	}
	if strings.Contains(out.String(), "oll") {
		t.Errorf("nothing started, so nothing to roll back, got:\n%s", out.String())
	}
}

// A rollback the runtime cannot be asked about is not reported as done: the
// container may well be there, and "stopped and removed; nothing … is left
// running" would be the same claim `run`'s interruption used to make before
// it checked. It is said as what it is, with the command that shows the truth.
func TestAFailedUpDoesNotCallARollbackDoneWhenTheRuntimeCannotBeAsked(t *testing.T) {
	parent := readOnlyParent(t)
	// The runtime answers while cache is started (the ownership check needs an
	// answer) and stops answering once the rollback has deleted it.
	t.Setenv("INSPECT_FAIL_ONCE_GONE", "cache.demo.opossum")
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"cache": {Image: "alpine:3"},
		"db": {
			Image:     "alpine:3",
			DependsOn: compose.DependsOn{{Name: "cache"}},
			Volumes:   []string{filepath.Join(parent, "child") + ":/data"},
		},
	})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	if err := o.Up(true); err == nil {
		t.Fatal("db's bind source cannot be made, so up must fail")
	}
	got := out.String()
	if !strings.Contains(got, "Tried to roll back cache, but the runtime could not be asked whether it is gone — `container ls -a` shows it") {
		t.Errorf("an unanswered inspect must be reported as such, got:\n%s", got)
	}
	if strings.Contains(got, "stopped and removed") || strings.Contains(got, "left running") {
		t.Errorf("must not claim the rollback is done, got:\n%s", got)
	}
}

// What a failed `up` hands to the supervisor (Started) is what is still there
// among the services it did not create. One the runtime cannot be asked about
// is kept: dropping it would end its supervision over an outage — the same
// reading StillSupervised makes.
func TestStartedKeepsAServiceTheRuntimeCannotBeAskedAbout(t *testing.T) {
	parent := readOnlyParent(t)
	rt, _ := fakeShim(t)
	// web is brought up once and is up to date on the second up; cache is new
	// and starts, db is new and cannot. Once the rollback has deleted cache,
	// the runtime stops answering about everything — web included.
	t.Setenv("INSPECT_FAIL_ONCE_GONE_ALL", "cache.demo.opossum")
	p := project("demo", map[string]*compose.Service{"web": {Image: "alpine:3"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}
	p.Services["cache"] = &compose.Service{Image: "alpine:3"}
	p.Services["db"] = &compose.Service{Image: "alpine:3", DependsOn: compose.DependsOn{{Name: "cache"}}, Volumes: []string{filepath.Join(parent, "child") + ":/data"}}
	if err := o.Up(true); err == nil {
		t.Fatal("db's bind source cannot be made, so the second up must fail")
	}
	started := strings.Join(o.Started(), ",")
	if !strings.Contains(started, "web") {
		t.Errorf("web was up to date and could not be asked about afterwards — it stays supervised; Started = %q", started)
	}
	if strings.Contains(started, "cache") {
		t.Errorf("cache was created by this up and rolled back; it is not handed to the supervisor; Started = %q", started)
	}
}

// The rollback report is one line built from three lists — removed, still
// there, could not be asked — and each combination reads as one true sentence.
// Three created services with a different fate each, so a fate folded into
// another list, or the "nothing is left running" clause attached while
// something is, shows as a different line. Matched whole: the line is the
// claim.
func TestTheRollbackReportIsTrueForEveryMixOfOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
		want string
	}{
		{"removed and could not be asked",
			[]string{"INSPECT_FAIL_ONCE_GONE=queue.demo.opossum"},
			"Rolled back cache, mail — stopped and removed; tried to roll back queue, but the runtime could not be asked whether it is gone — `container ls -a` shows it"},
		{"only still there",
			[]string{"DELETE_STICKY=cache.demo.opossum queue.demo.opossum mail.demo.opossum"},
			"Tried to roll back cache, queue, mail, but the container is still there — `opossum down` removes it"},
		{"removed, still there and could not be asked",
			[]string{"DELETE_STICKY=mail.demo.opossum", "INSPECT_FAIL_ONCE_GONE=queue.demo.opossum"},
			"Rolled back cache — stopped and removed; tried to roll back mail, but the container is still there — `opossum down` removes it; tried to roll back queue, but the runtime could not be asked whether it is gone — `container ls -a` shows it"},
		{"only removed",
			nil,
			"Rolled back cache, queue, mail — stopped and removed; nothing this `up` started is left running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := readOnlyParent(t)
			rt, _ := fakeShim(t)
			setShimEnv(rt, tc.env...)
			chain := []string{"cache", "queue", "mail"}
			services := map[string]*compose.Service{}
			for i, name := range chain {
				svc := &compose.Service{Image: "alpine:3"}
				if i > 0 {
					svc.DependsOn = compose.DependsOn{{Name: chain[i-1]}}
				}
				services[name] = svc
			}
			services["db"] = &compose.Service{Image: "alpine:3", DependsOn: compose.DependsOn{{Name: "mail"}},
				Volumes: []string{filepath.Join(parent, "child") + ":/data"}}
			var out bytes.Buffer
			if err := orchestrator.New(project("demo", services), rt, "opossum", &out).Up(true); err == nil {
				t.Fatal("db's bind source cannot be made, so up must fail")
			}
			if got := rollbackLine(out.String()); got != tc.want {
				t.Errorf("rollback report:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}
