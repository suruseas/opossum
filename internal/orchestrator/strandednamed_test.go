package orchestrator_test

import (
	"bytes"
	"sort"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/testpair"
)

// A volume the file declares with a `name:` of its own carries no project
// prefix, so the stranded scan — which finds `<project>_` leftovers by that
// prefix — could not see it once no service mounted it any more (#944): the
// teardown said everything was gone while it stayed on disk. Now the
// declaration counts as the project's claim.
//
// The mounted and the declared-only volume come as a pair in both orders
// (testpair), so each name plays each role: with one, a scan that listed every
// declared name, or none, would look right.
func TestDestroyListsADeclaredNameVolumeNoServiceMounts(t *testing.T) {
	// newO builds an orchestrator over a fake runtime that already lists the
	// given volumes (the ones opossum makes come on top of them).
	newO := func(t *testing.T, body string, onRuntime ...string) (*orchestrator.Orchestrator, func() []string) {
		t.Helper()
		rt, log := fakeShim(t)
		setShimEnv(rt, "VOLUME_LS="+strings.Join(onRuntime, " "),
			// So the destroy below can verify its own removals: what it deletes
			// reads as gone afterwards.
			"INSPECT_ABSENT=db.demo.opossum db-run.demo.opossum", "NETWORK_ABSENT=demo-net", "IMAGE_ABSENT=postgres:16")
		return orchestrator.New(loadBody(t, body), rt, "opossum", &bytes.Buffer{}), log
	}
	plan := func(t *testing.T, body string, onRuntime ...string) orchestrator.DestroyPlan {
		t.Helper()
		o, _ := newO(t, body, onRuntime...)
		p, err := o.DestroyPlanFor(false, false, false)
		if err != nil {
			t.Fatalf("destroy plan: %v", err)
		}
		return p
	}
	sorted := func(s ...string) string { sort.Strings(s); return strings.Join(s, " ") }

	testpair.Run(t, "mounted goes to the plan, declared-only to stranded", testpair.Pair[string]{A: "alpha-vol", B: "beta-vol"}, func(t *testing.T, mounted, declaredOnly string) {
		body := "name: demo\nservices:\n  db:\n    image: postgres:16\n    volumes: [\"used:/u\"]\n" +
			"volumes:\n  used: {name: " + mounted + "}\n  idle: {name: " + declaredOnly + "}\n"
		// `demo_old` is a prefix leftover, listed as before, beside the new kind.
		// It comes first from the runtime so the sorted list differs from the
		// runtime's order in at least one of the two orders. The mounted volume
		// is made by the `up`, so the destroy's removal of it reads as done.
		o, log := newO(t, body, "demo_old", declaredOnly)
		if err := o.Up(true); err != nil {
			t.Fatalf("up: %v", err)
		}
		p, err := o.DestroyPlanFor(false, false, false)
		if err != nil {
			t.Fatalf("destroy plan: %v", err)
		}
		if got := strings.Join(p.Volumes, " "); got != mounted {
			t.Errorf("the mounted volume is removed under its name, want %q got %q", mounted, got)
		}
		if got, want := strings.Join(p.StrandedVolumes, " "), sorted(declaredOnly, "demo_old"); got != want {
			t.Errorf("the declared-only volume is stranded beside the prefix leftover, want %q got %q", want, got)
		}
		// Listed is not removed: the destroy deletes what it planned and leaves
		// the stranded ones — of the new kind too — where they are.
		if err := o.Destroy(p); err != nil {
			t.Fatalf("destroy: %v", err)
		}
		lines := log()
		if !hasLine(lines, "volume delete "+mounted) {
			t.Errorf("destroy removes the mounted volume, want %q in %v", "volume delete "+mounted, lines)
		}
		for _, kept := range []string{declaredOnly, "demo_old"} {
			if hasLine(lines, "volume delete "+kept) {
				t.Errorf("a stranded volume is listed, never removed, got %q in %v", "volume delete "+kept, lines)
			}
		}
	})
	testpair.Run(t, "an external name: beside a project one: only the project's is stranded", testpair.Pair[string]{A: "ext-vol", B: "proj-vol"}, func(t *testing.T, first, second string) {
		body := "name: demo\nservices:\n  db:\n    image: postgres:16\n" +
			"volumes:\n  ext: {external: true, name: ext-vol}\n  proj: {name: proj-vol}\n"
		// The runtime lists them in either order; the answer must not depend on it.
		p := plan(t, body, first, second)
		if got := strings.Join(p.StrandedVolumes, " "); got != "proj-vol" {
			t.Errorf("an external volume is never stranded, the project's declared one is, want %q got %q", "proj-vol", got)
		}
		if len(p.Volumes) != 0 {
			t.Errorf("nothing is mounted, so nothing is removed, got %v", p.Volumes)
		}
	})
	// One declaration here on purpose: this case is about a volume that does not
	// exist, so there is no second element whose order or attribution could hide it.
	t.Run("a declared name: not on the runtime was never created and is not listed", func(t *testing.T) {
		body := "name: demo\nservices:\n  db:\n    image: postgres:16\nvolumes:\n  idle: {name: never-made}\n"
		p := plan(t, body, "demo_old")
		if got := strings.Join(p.StrandedVolumes, " "); got != "demo_old" {
			t.Errorf("only what is on the runtime can be stranded, want %q got %q", "demo_old", got)
		}
	})
}
