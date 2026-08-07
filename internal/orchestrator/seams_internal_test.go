package orchestrator

// Evals for the seams `New` wires — the fields an eval is expected to replace.
//
// A seam has two halves: the thing tests put there, and the thing production
// gets. Only the first is ever exercised, because replacing it is the whole point
// — so the default can be quietly wrong and every eval that uses the seam keeps
// passing, on a value none of them ever see.
//
// That is not hypothetical here. `sleep` was replaced with a function that
// returns immediately and all nine packages stayed green, which would have meant
// `up` shipped with no healthcheck interval, no start period, and no crash grace,
// with nothing anywhere to say so.

import (
	"bytes"
	"testing"
	"time"
)

// Every wait in `up` — a healthcheck's start period and interval, the grace a
// just-started service gets to fall over in — is this one function. Each of those
// has an eval, and each of those evals replaces it, so between them they say
// nothing about what a real `up` does.
//
// Asking for a real duration and timing it is the only form that can tell: the
// two sides of the seam have the same type, and Go cannot compare functions.
func TestNewWiresASleepThatActuallyWaits(t *testing.T) {
	o := New(webProject(), inspectShim(t, runningInspect, ""), "", &bytes.Buffer{})
	// Long enough to be unambiguous against any clock, short enough that paying it
	// once in the suite costs nothing.
	const d = 20 * time.Millisecond
	start := time.Now()
	o.sleep(d)
	// `time.Sleep` pauses for at least the duration, so this is exact rather than a
	// tolerance — and a stand-in that waits a fraction of what it was asked for
	// fails it just as surely as one that does not wait at all.
	if elapsed := time.Since(start); elapsed < d {
		t.Errorf("the sleep New wires returned after %v when asked for %v — every wait in `up` is this function", elapsed, d)
	}
}
