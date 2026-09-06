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
