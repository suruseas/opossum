package main

import (
	"bytes"
	"testing"

	"github.com/suruseas/opossum/internal/orchestrator"
)

// reportSupervisorStop is what both call sites that stop this project's
// supervisor (`down`, and `up` when it replaces one for a changed compose file)
// go through, so this table covers what each of orchestrator.StopSupervisor's
// two return values must produce on its own: the "not confirmed" case must always
// warn regardless of stoppedMsg, and a confirmed stop is silent exactly when the
// caller asked it to be (#1401 — `up`'s replace path intentionally passes "").
func TestReportSupervisorStop(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stopped    bool
		attempted  bool
		stoppedMsg string
		want       string
	}{
		{"nothing running", false, false, "opossum: stopped the restart supervisor", ""},
		{"nothing running, no stoppedMsg wanted anyway", false, false, "", ""},
		{"confirmed stop, caller wants a line", true, true, "opossum: stopped the restart supervisor", "opossum: stopped the restart supervisor\n"},
		{"confirmed stop, caller stays quiet", true, true, "", ""},
		{"not confirmed, caller had a stoppedMsg", false, true, "opossum: stopped the restart supervisor", "opossum: " + orchestrator.NoticeSupervisorStopFailed() + "\n"},
		{"not confirmed, caller had no stoppedMsg", false, true, "", "opossum: " + orchestrator.NoticeSupervisorStopFailed() + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			reportSupervisorStop(&out, tc.stopped, tc.attempted, tc.stoppedMsg)
			if got := out.String(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// supervisorReplacedSafely is what stops `up`'s replace path from starting a new
// supervisor and announcing a new service set when the old one could not be
// confirmed gone — StartSupervisor would silently no-op against the pid file
// StopSupervisor leaves behind in exactly that case (only "not attempted or
// confirmed" clears it), and the notice printed right after would then describe
// a supervisor that isn't the one actually running.
func TestSupervisorReplacedSafely(t *testing.T) {
	for _, tc := range []struct {
		name               string
		stopped, attempted bool
		want               bool
	}{
		{"nothing was running", false, false, true},
		{"confirmed stopped", true, true, true},
		{"attempted but not confirmed", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := supervisorReplacedSafely(tc.stopped, tc.attempted); got != tc.want {
				t.Errorf("supervisorReplacedSafely(%v, %v) = %v, want %v", tc.stopped, tc.attempted, got, tc.want)
			}
		})
	}
}
