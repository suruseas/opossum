package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// The orphan sweep is the one part of destroy that acts on containers nobody
// named: anything the runtime reports for this project that no current service
// claims. What keeps it inside the project is the label — and this is the shim
// that can model a foreign container, so this is where that has to be proved.
func TestDestroyPlanOrphansAreScopedByLabel(t *testing.T) {
	rt, _ := fakeShim(t)
	// Keep supervisor state out of the developer's real ~/.local/state.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt,
		"LS_CONTAINERS=old.demo.opossum", // ours, but no service claims it any more
		"LS_PROJECT=demo",
		"LS_FOREIGN=web.other.opossum", // labelled for a different project
		"INSPECT_PROJECT=demo",
	)
	p := project("demo", map[string]*compose.Service{"web": {Image: "web"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})

	plan, err := o.DestroyPlanFor(false, false, false)
	if err != nil {
		t.Fatalf("DestroyPlanFor: %v", err)
	}
	joined := strings.Join(plan.Containers, " ")
	if !strings.Contains(joined, "old.demo.opossum") {
		t.Errorf("a container left behind by this project is an orphan and should be removed, got %v", plan.Containers)
	}
	if strings.Contains(joined, "other") {
		t.Errorf("another project's container must never be in the plan, got %v", plan.Containers)
	}
}

// A plan is shown to someone about to approve it. Listing things that are not
// there teaches them not to read it — and it is how a plan starts to drift from
// what the removal does.
func TestDestroyPlanOnlyListsWhatExists(t *testing.T) {
	rt, _ := fakeShim(t)
	// Keep supervisor state out of the developer's real ~/.local/state.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt,
		"INSPECT_PROJECT=demo",
		"INSPECT_ABSENT=web.demo.opossum web-run.demo.opossum",
		"IMAGE_ABSENT=demo-web:latest",
		"NETWORK_ABSENT=demo-net",
		"VOLUME_LS=NAME", // no volumes at all
	)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "", Build: &compose.Build{Context: "."}, Volumes: []string{"data:/d"}},
	})
	p.Volumes = map[string]compose.VolumeDecl{"data": {}}
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})

	plan, err := o.DestroyPlanFor(false, false, false)
	if err != nil {
		t.Fatalf("DestroyPlanFor: %v", err)
	}
	if len(plan.Containers) != 0 || len(plan.Images) != 0 || len(plan.Networks) != 0 || len(plan.Volumes) != 0 {
		t.Errorf("nothing exists, so the plan should be empty of runtime objects, got %+v", plan)
	}
}

// --keep-images is for the case the default gets wrong: a pulled image shared
// with other projects, minutes to fetch again.
func TestDestroyPlanKeepImages(t *testing.T) {
	rt, _ := fakeShim(t)
	// Keep supervisor state out of the developer's real ~/.local/state.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt, "INSPECT_PROJECT=demo")
	p := project("demo", map[string]*compose.Service{"db": {Image: "postgres:16"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})

	with, err := o.DestroyPlanFor(false, false, false)
	if err != nil {
		t.Fatalf("DestroyPlanFor: %v", err)
	}
	if len(with.Images) == 0 {
		t.Fatal("by default destroy removes the images it pulled")
	}
	without, err := o.DestroyPlanFor(false, true, false)
	if err != nil {
		t.Fatalf("DestroyPlanFor: %v", err)
	}
	if len(without.Images) != 0 {
		t.Errorf("--keep-images should leave images out of the plan, got %v", without.Images)
	}
	if len(without.Containers) == 0 {
		t.Error("--keep-images is about images alone; the containers still go")
	}
}
