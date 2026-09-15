package orchestrator_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// `--remove-orphans` removes the containers this project's label is on and
// no service claims; a container with no project label was made outside
// opossum and is not an orphan of anyone's, whatever its name looks like.
func TestRemoveOrphansLeavesContainersWithNoProjectLabel(t *testing.T) {
	rt, log := fakeShim(t)
	setShimEnv(rt, "LS_CONTAINERS=old.demo.opossum", "LS_PROJECT=demo", "LS_UNLABELED=stray.demo.opossum", "INSPECT_PROJECT=demo")
	p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Down(false, "", true); err != nil {
		t.Fatalf("Down: %v", err)
	}
	lines := log()
	if !slices.Contains(lines, "delete --force old.demo.opossum") {
		t.Errorf("this project's orphan should be removed, got %v", lines)
	}
	if slices.ContainsFunc(lines, func(l string) bool {
		return strings.Contains(l, "stray.demo.opossum") && (strings.HasPrefix(l, "delete") || strings.HasPrefix(l, "stop"))
	}) {
		t.Errorf("a container with no project label must not be stopped or removed as an orphan, got %v", lines)
	}
}
