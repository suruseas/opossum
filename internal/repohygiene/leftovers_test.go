package repohygiene_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An unrun check is no check. cmd/noleftovers is the net under the runs that end
// before their own cleanup does — a panic, a -timeout, an interrupt — and it is
// only that net if the gate goes through it.
//
// Read rather than run: running the gate from inside the gate runs the whole
// suite a second time. So this says the recipe names the tool, and nothing about
// whether make, or a developer, or CI actually invokes that recipe.
func TestTheGateRunsTheSuiteThroughTheLeftoverCheck(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	// The `test` target and nothing else: `cover` runs the same suite for a
	// different reason, and a rule that every recipe must go through the check
	// would be a rule about recipes nobody has written yet.
	var recipes []string
	inTarget := false
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "test:") {
			inTarget = true
			continue
		}
		if inTarget && !strings.HasPrefix(line, "\t") {
			break
		}
		if inTarget && strings.Contains(line, "go test") {
			recipes = append(recipes, line)
		}
	}
	if len(recipes) == 0 {
		t.Fatal("the `test` target no longer runs the suite, so this checks nothing")
	}
	for _, r := range recipes {
		if !strings.Contains(r, "cmd/noleftovers") {
			t.Errorf("the suite should run through the leftover check, got: %q", r)
		}
	}
}
