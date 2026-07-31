package orchestrator_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/workspace"
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

// Started() is measured, not remembered: it says what is running when Up
// finishes, so a container that disappeared between two calls is not reported
// just because the first call started it. The caller decides what to supervise
// from this, and supervising something that is gone means announcing a watcher
// for a container nobody can see.
func TestStartedIsMeasuredNotRemembered(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	state := t.TempDir()
	setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+state)
	p := project("demo", map[string]*compose.Service{
		"db": {
			Image: "db",
			Healthcheck: &compose.Healthcheck{
				Test: []string{"true"}, Timeout: 100 * time.Millisecond, Retries: 1,
			},
		},
		"web": {
			Image:     "web",
			DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}},
		},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}
	if len(o.Started()) != 2 {
		t.Fatalf("the first up should have started both services, got %v", o.Started())
	}

	// web goes away between the calls — someone removed it by hand. Done through the
	// shim's own `delete`, so this exercises the path the runtime would take rather
	// than hand-writing a marker whose format only this test knows.
	rt.Delete("web.demo.opossum")
	if rt.Inspect("web.demo.opossum").Exists {
		t.Fatal("this test needs web to be gone; the shim still reports it")
	}

	setShimEnv(rt, "HEALTH_HANG=1")
	if err := o.Up(true); err == nil {
		t.Fatal("the second up should fail when its dependency never becomes healthy")
	}
	for _, name := range o.Started() {
		if name == "web" {
			t.Errorf("Started() = %v, but web is gone — this answer is the first call's memory, "+
				"not a measurement", o.Started())
		}
	}
}

// A service left alone because it was already up to date is not in the rollback
// list, so it is still running when a later service fails the bring-up. Reporting
// nothing there would leave it running with nobody watching it.
//
// The survivor is named so it sorts *after* the service that fails: the startup
// order is alphabetical, so a survivor earlier in the order is one the loop
// happened to reach. An implementation that collects survivors as it walks passes
// with the earlier name and fails with this one — which is how the first version
// of this test passed while the set it measured was wrong.
func TestStartedListsWhatSurvivedARollback(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// STATE_DIR makes the shim remember deletions, so "still running" can be asked.
	setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir())
	p := project("demo", map[string]*compose.Service{
		"db": {
			Image: "db",
			Healthcheck: &compose.Healthcheck{
				Test: []string{"true"}, Timeout: 100 * time.Millisecond, Retries: 1,
			},
		},
		"web": {
			Image:     "web",
			DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}},
		},
		"zcache": {Image: "cache", Restart: "always"},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	// First: everything comes up, so the config hashes are recorded.
	if err := o.Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}

	// Second: db's healthcheck never passes, so the bring-up fails and rolls back.
	// db and zcache are unchanged, so they are skipped — and survive.
	setShimEnv(rt, "HEALTH_HANG=1")
	if err := o.Up(true); err == nil {
		t.Fatal("the second up should fail")
	}
	// The exact set, not membership: a version that returned the previous call's
	// answer, or the whole order, would satisfy a membership test.
	got := append([]string(nil), o.Started()...)
	sort.Strings(got)
	want := []string{"db", "web", "zcache"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Started() = %v, want %v — all three were left alone by the failed call (it "+
			"never got past db's healthcheck) and are still running from the first up", got, want)
	}
}

// A dry run starts nothing, so it must not leave the previous run's answer in
// place. This is the one path where the reset at the top of Up is observable:
// every other exit either assigns the set or rolls back and assigns what
// survived, so without a test here the reset looks redundant and would be
// removed — taking the "describes the last Up and nothing before it" guarantee
// with it.
func TestStartedIsEmptyAfterADryRun(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt, "INSPECT_PROJECT=demo")
	p := project("demo", map[string]*compose.Service{"web": {Image: "web", Restart: "always"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}
	if len(o.Started()) == 0 {
		t.Fatal("the first up should have started something for this test to mean anything")
	}

	o.SetDryRun(true)
	if err := o.Up(true); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if got := o.Started(); len(got) != 0 {
		t.Errorf("a dry run starts nothing, so Started() should be empty; got %v — that is the "+
			"previous run's answer", got)
	}
}

// A rollback whose removal did not take is the one case where "what is running"
// and "what should be supervised" come apart. The container is still there, but
// opossum was trying to delete it — handing it to the supervisor would have the
// supervisor bring back what the teardown was removing.
func TestStartedExcludesWhatTheRollbackTriedToRemove(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir(),
		// db is created by this call, and its removal quietly does nothing.
		"DELETE_STICKY=db.demo.opossum",
		"HEALTH_HANG=1",
		"INSPECT_ABSENT=web.demo.opossum")
	p := project("demo", map[string]*compose.Service{
		"db": {
			Image:   "db",
			Restart: "always",
			Healthcheck: &compose.Healthcheck{
				Test: []string{"true"}, Timeout: 100 * time.Millisecond, Retries: 1,
			},
		},
		"web": {
			Image:     "web",
			Restart:   "always",
			DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}},
		},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err == nil {
		t.Fatal("up should fail when a dependency never becomes healthy")
	}
	// Assert the premise. The whole point is a container that is *still there* after
	// the teardown tried to remove it — if the shim's sticky-delete knob stopped
	// working, db would simply be gone and this test would pass while proving
	// nothing about the exclusion.
	if !rt.Inspect("db.demo.opossum").Exists {
		t.Fatal("this test needs db to survive its own removal; the shim reports it as gone")
	}
	for _, name := range o.Started() {
		if name == "db" {
			t.Errorf("Started() = %v — db is still there only because its removal failed, and "+
				"supervising it would undo the rollback", o.Started())
		}
	}
}

// The reset at the top of Up has to run before the checks that can return early.
// Without that, a failure that never measures anything — the runtime is gone —
// leaves the previous call's answer in place, and the caller supervises a stack
// it was never told about.
func TestStartedIsEmptyWhenTheRuntimeIsGone(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt, "INSPECT_PROJECT=demo")
	p := project("demo", map[string]*compose.Service{"web": {Image: "web", Restart: "always"}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}
	if len(o.Started()) == 0 {
		t.Fatal("the first up should have started something for this test to mean anything")
	}

	rt.Bin = filepath.Join(t.TempDir(), "no-such-container-binary")
	if err := o.Up(true); err == nil {
		t.Fatal("expected the runtime-absent error")
	}
	if got := o.Started(); len(got) != 0 {
		t.Errorf("Started() = %v — the previous call's answer survived a failure that returned "+
			"before anything was measured", got)
	}
}

// Presence, not liveness. A service that crashed while nobody was watching is
// what `restart:` exists for, so a rollback elsewhere in the project must not
// quietly drop it from the set the supervisor is given.
func TestStartedListsAStoppedSurvivor(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir())
	p := project("demo", map[string]*compose.Service{
		"db": {
			Image: "db",
			Healthcheck: &compose.Healthcheck{
				Test: []string{"true"}, Timeout: 100 * time.Millisecond, Retries: 1,
			},
		},
		"web": {
			Image:     "web",
			DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}},
		},
		"zcache": {Image: "cache", Restart: "always"},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}
	// Between the calls zcache crashes: still there, no longer running.
	setShimEnv(rt, "HEALTH_HANG=1", "INSPECT_STOPPED=zcache.demo.opossum")
	if err := o.Up(true); err == nil {
		t.Fatal("the second up should fail")
	}
	var saw bool
	for _, name := range o.Started() {
		if name == "zcache" {
			saw = true
		}
	}
	if !saw {
		t.Errorf("zcache exists but is stopped — that is what `restart:` is for, so it belongs "+
			"in the set; got %v", o.Started())
	}
}

// Started() answers for the services this Up was asked about. `up web` says
// nothing about db, and reporting it here would hand the supervisor a set the
// user never named — the rollback counterpart of watching only what `up` started.
func TestStartedAfterARollbackStaysWithinTheSelectedServices(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir())
	p := project("demo", map[string]*compose.Service{
		"db":    {Image: "db", Restart: "always"},
		"probe": {Image: "probe", Healthcheck: &compose.Healthcheck{Test: []string{"true"}, Timeout: 100 * time.Millisecond, Retries: 1}},
		"web":   {Image: "web", Restart: "always", DependsOn: compose.DependsOn{{Name: "probe", Condition: compose.ConditionHealthy}}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}

	// Only web (and its dependency) is named; db is running but out of scope.
	setShimEnv(rt, "HEALTH_HANG=1")
	if err := o.Up(true, "web"); err == nil {
		t.Fatal("the partial up should fail")
	}
	for _, name := range o.Started() {
		if name == "db" {
			t.Errorf("Started() = %v — db was never named on this call, so it is not this "+
				"call's to report", o.Started())
		}
	}
}

// Workspace snapshots are the one thing `ws` leaves on disk, and they are usually
// the largest. destroy must not remove them — a snapshot belongs to the directory
// that was snapshotted, and the same directory outlives any number of projects —
// but it must say they are there, or a command that promises to leave no trace is
// the reason someone finds gigabytes a month later.
func TestDestroyPlanNamesWorkspaceSnapshotsWithoutRemovingThem(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// Nothing runtime-side exists, so the removal has only the on-disk work to do —
	// which is the half this test is about.
	setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir(),
		"INSPECT_ABSENT=web.demo.opossum web-run.demo.opossum",
		"NETWORK_ABSENT=demo-net", "IMAGE_ABSENT=web", "VOLUME_LS=NAME")

	base := t.TempDir()
	here := filepath.Join(base, workspace.SnapshotDirName)              // ws --path ./work
	nested := filepath.Join(base, "sandbox", workspace.SnapshotDirName) // ws --path ./sandbox/work
	for _, dir := range []string{here, nested} {
		if err := os.MkdirAll(filepath.Join(dir, "before-rollback-1"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := project("demo", map[string]*compose.Service{"web": {Image: "web"}})
	p.BaseDir = base
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})

	plan, err := o.DestroyPlanFor(false, false, false)
	if err != nil {
		t.Fatalf("DestroyPlanFor: %v", err)
	}
	if got := plan.SnapshotDirs; !reflect.DeepEqual(got, []string{here, nested}) {
		t.Errorf("SnapshotDirs = %v, want %v", got, []string{here, nested})
	}
	// The reason they are reported at all is that they are not removed. If one ever
	// meets the removal list — in either direction of containment — the report
	// becomes a lie in the worst direction: a path named as kept, with an `rm -rf`
	// for something that will not be there.
	for _, path := range plan.Paths {
		for _, snap := range plan.SnapshotDirs {
			if path == snap ||
				strings.HasPrefix(path, snap+string(filepath.Separator)) ||
				strings.HasPrefix(snap, path+string(filepath.Separator)) {
				t.Errorf("destroy would remove %q, and %q is reported as left alone — one of "+
					"those two statements is false", path, snap)
			}
		}
	}
	// And running it really does leave them alone.
	if err := o.Destroy(plan); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	for _, snap := range []string{here, nested} {
		if _, err := os.Stat(snap); err != nil {
			t.Errorf("%s is gone after destroy: %v", snap, err)
		}
	}
}

// A project that never used `ws` should not be told about snapshots it does not
// have: a leftovers list that names things that are not there is one people stop
// reading.
func TestDestroyPlanHasNoSnapshotsWhenNoneWereTaken(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt, "INSPECT_PROJECT=demo")

	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := project("demo", map[string]*compose.Service{"web": {Image: "web"}})
	p.BaseDir = base
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})

	plan, err := o.DestroyPlanFor(false, false, false)
	if err != nil {
		t.Fatalf("DestroyPlanFor: %v", err)
	}
	if len(plan.SnapshotDirs) != 0 {
		t.Errorf("nothing was snapshotted, so nothing should be reported, got %v", plan.SnapshotDirs)
	}
}

// A workspace can live inside `.opossum/`, and then its snapshots do too — and
// `.opossum/` is one of the things destroy removes. Reporting those snapshots as
// left alone would be the worst version of this report: a path named as kept,
// with an `rm -rf` for something that is already gone.
func TestDestroyPlanDoesNotReportSnapshotsItIsAboutToRemove(t *testing.T) {
	rt, _ := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setShimEnv(rt, "INSPECT_PROJECT=demo", "STATE_DIR="+t.TempDir(),
		"INSPECT_ABSENT=web.demo.opossum web-run.demo.opossum",
		"NETWORK_ABSENT=demo-net", "IMAGE_ABSENT=web", "VOLUME_LS=NAME")

	base := t.TempDir()
	doomed := filepath.Join(base, ".opossum", workspace.SnapshotDirName)
	safe := filepath.Join(base, workspace.SnapshotDirName)
	for _, dir := range []string{doomed, safe} {
		if err := os.MkdirAll(filepath.Join(dir, "try-1"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := project("demo", map[string]*compose.Service{"web": {Image: "web"}})
	p.BaseDir = base
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})

	plan, err := o.DestroyPlanFor(false, false, false)
	if err != nil {
		t.Fatalf("DestroyPlanFor: %v", err)
	}
	// `.opossum/` is in the removal list, so the snapshots under it are not kept.
	if !reflect.DeepEqual(plan.SnapshotDirs, []string{safe}) {
		t.Errorf("SnapshotDirs = %v, want only %v — the ones under .opossum/ are being "+
			"removed, so reporting them as left alone is a lie", plan.SnapshotDirs, safe)
	}
	if err := o.Destroy(plan); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	// And the claim holds both ways: what was reported survived, what was not
	// reported is genuinely gone rather than quietly kept.
	if _, err := os.Stat(safe); err != nil {
		t.Errorf("%s was reported as left alone but is gone: %v", safe, err)
	}
	if _, err := os.Stat(doomed); !os.IsNotExist(err) {
		t.Errorf("%s was not reported, so it should have gone with .opossum/, stat err = %v", doomed, err)
	}
}
