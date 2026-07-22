package orchestrator

// Internal test (package orchestrator, not _test): it reaches the unexported
// `sleep` seam to observe the healthcheck timing without waiting in real time.

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

// waitHealthy sleeps the StartPeriod once, before the first probe, then probes.
// With a probe that passes immediately, exactly that one StartPeriod sleep should
// happen (no Interval sleeps, since there's no retry). Guards the StartPeriod
// grace period against being dropped or applied per-attempt.
func TestWaitHealthySleepsStartPeriodBeforeFirstProbe(t *testing.T) {
	// A `container` stand-in whose `exec` (the healthcheck) passes on the first try.
	shim := filepath.Join(t.TempDir(), "c.sh")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := &runtime.Runtime{Bin: shim}

	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{"db": {Image: "x"}}}
	o := New(p, rt, "opossum", io.Discard)
	var slept []time.Duration
	o.sleep = func(d time.Duration) { slept = append(slept, d) } // record instead of waiting

	hc := &compose.Healthcheck{
		Test:        []string{"true"},
		StartPeriod: 7 * time.Second,
		Interval:    3 * time.Second,
		Retries:     3,
	}
	if err := o.waitHealthy("db", hc); err != nil {
		t.Fatalf("waitHealthy with a passing probe should succeed, got %v", err)
	}
	// Healthy on the first probe ⇒ only the StartPeriod grace sleep happened; no
	// Interval sleeps (those are strictly between retries).
	if len(slept) != 1 || slept[0] != 7*time.Second {
		t.Errorf("expected exactly one sleep of 7s (StartPeriod), got %v", slept)
	}
}
