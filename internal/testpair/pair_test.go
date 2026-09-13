package testpair

import (
	"sort"
	"testing"
)

// The helper's own promise: both orders are run, and Around's two names do
// sort on either side of the pivot — a first-only check in a case that sorts
// its names would otherwise pass for one of the orders.
func TestRunCoversBothOrdersAndAroundStraddlesThePivot(t *testing.T) {
	var seen [][2]string
	Run(t, "pair", Pair[string]{A: "x", B: "y"}, func(t *testing.T, first, second string) {
		seen = append(seen, [2]string{first, second})
	})
	if len(seen) != 2 || seen[0] != [2]string{"x", "y"} || seen[1] != [2]string{"y", "x"} {
		t.Errorf("want both orders, (x,y) then (y,x); got %v", seen)
	}
	p := Around("seed-demo_data.opossum")
	names := []string{p.B, "seed-demo_data.opossum", p.A}
	sort.Strings(names)
	if names[0] != p.A || names[1] != "seed-demo_data.opossum" || names[2] != p.B {
		t.Errorf("Around's names should sort on either side of the pivot, got %v", names)
	}
	if p.A == "seed-demo_data.opossum" || p.B == "seed-demo_data.opossum" {
		t.Error("neither name may equal the pivot")
	}
}

// A first-only check is what the helper is for: shown here on a stand-in that
// reads only the first of two names, it fails in exactly one of the orders.
func TestAFirstOnlyCheckFailsInOneOrder(t *testing.T) {
	firstIsPivot := func(names []string) bool { sort.Strings(names); return names[0] == "seed-x" }
	p := Around("seed-x")
	results := map[string]bool{}
	for _, o := range (Pair[string]{A: "seed-x", B: p.B}).Orders() {
		results[o[0]+","+o[1]] = firstIsPivot([]string{o[0], o[1]})
	}
	// Both orders sort the same, so a first-only check gives the same answer
	// twice: it is the CHOICE of the second name, not the order, that exposes it.
	if !results["seed-x,"+p.B] || !results[p.B+",seed-x"] {
		t.Errorf("with a name sorting after the pivot, the first-only check wrongly holds in both orders: %v", results)
	}
	if firstIsPivot([]string{"seed-x", p.A}) {
		t.Error("with a name sorting before the pivot, the first-only check must fail — that is the name Around gives")
	}
}
