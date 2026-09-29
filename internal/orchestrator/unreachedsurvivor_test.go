package orchestrator_test

// #1357: the deferred survivors loop that builds Started() after a failed `up`
// treats an Inspect the runtime could not answer (Unknown) as "present" — the
// same reading StillSupervised makes, and for the same reason (a service that
// crashed while nobody was watching must not lose its restart: policy over a
// passing outage). That reading is only trustworthy for a service this run's
// own pre-flight already found a container for (existedBefore) — before the
// fix this issue is about, it was applied unconditionally, including to a
// service the loop in Up never even reached because an earlier one in the
// order failed first: an Unknown there is not "this run's container had a
// hiccup", since nothing here ever confirmed it had one to begin with.

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/suruseas/opossum/internal/orchestrator"
)

func TestAServiceTheRunNeverReachedIsNotSupervisedOverAnUnaskableInspect(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, _ := fakeShim(t)
	// The runtime answers normally for both services up front (so the
	// pre-flight ownership check that walks every name before anything starts
	// — a check unrelated to #1357 — passes cleanly): a is absent (nothing
	// there yet), b is absent too (this run never makes it, in or out of the
	// bug). a then fails to run — RUN_FAIL_THEN_INSPECT_FAIL marks a file
	// that, named by INSPECT_FAIL_WHILE, makes every Inspect answer Unknown
	// from that point on, including the deferred survivors loop's ask about b
	// — which never got asked before, because the loop in Up never reached it.
	unaskable := filepath.Join(t.TempDir(), "unaskable")
	setShimEnv(rt, "RUN_FAIL=a.demo.opossum", "INSPECT_ABSENT=b.demo.opossum",
		"INSPECT_ABSENT_BEFORE_RUN=a.demo.opossum",
		"RUN_FAIL_THEN_INSPECT_FAIL="+unaskable, "INSPECT_FAIL_WHILE="+unaskable)
	proj, err := loadProject(t, "services:\n  a:\n    image: alpine:3\n  b:\n    image: alpine:3\n")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	o := orchestrator.New(proj, rt, "opossum", &out)
	if err := o.Up(true); err == nil {
		t.Fatal("want the up refused (a's run fails)")
	}
	for _, name := range o.Started() {
		if name == "b" {
			t.Errorf("want b left out of Started() — Up's loop never reached it (a failed first), so an Inspect it could not answer is not evidence b is running, got: %v", o.Started())
		}
	}
}
