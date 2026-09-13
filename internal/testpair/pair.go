// Package testpair holds one helper for evals that look at a sequence — the
// services of a project, the holders of a volume, the volumes of a service,
// the entries of a list — and decide something from order, position, or
// membership.
//
// It exists because the same mistake was made four times in a week: a case
// written with ONE element, against which every mutation of the ordering is
// equivalent (dropping the last entry, the first, or all of them look the
// same; comparing against the first element instead of the right one passes;
// sorting or not sorting changes nothing). Each time the principle was already
// written down. So the guard moved from the checklist into the thing that
// makes cases: a case that goes through here runs with two elements, in both
// orders, and a check that only looks at the first — or only survives one
// sort order — goes red.
//
// It is for new cases. Existing evals are not rewritten through it; they hold
// what they hold.
package testpair

import "testing"

// Pair is two elements of a sequence, given in one order.
type Pair[T any] struct{ A, B T }

// Orders is the pair in both orders: A before B, then B before A.
func (p Pair[T]) Orders() [][2]T { return [][2]T{{p.A, p.B}, {p.B, p.A}} }

// Run runs body as a subtest for each order of the pair. The subtest's name
// carries the order, so a failure says which one broke.
//
// Choose the two elements so that they sort on either side of whatever the
// code under test sorts around: for a name compared with "seed-…", one name
// that sorts before it ("ghost") and one after ("zombie"); a pair that sorts
// the same way in both orders leaves a first-only check alive.
func Run[T any](t *testing.T, name string, p Pair[T], body func(t *testing.T, first, second T)) {
	t.Helper()
	for i, o := range p.Orders() {
		label := name + " (A,B)"
		if i == 1 {
			label = name + " (B,A)"
		}
		t.Run(label, func(t *testing.T) { body(t, o[0], o[1]) })
	}
}

// Around gives two names that sort on either side of pivot, for a case whose
// code sorts names before reading them: the first sorts before pivot, the
// second after. They are built from pivot itself, so they stay adjacent to it
// and are not mistaken for it.
func Around(pivot string) Pair[string] {
	return Pair[string]{A: "a-" + pivot, B: "z-" + pivot}
}
