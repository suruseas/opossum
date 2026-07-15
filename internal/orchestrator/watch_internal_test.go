package orchestrator

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

// targetFor picks the first rule whose tree contains the path.
func TestTargetForFindsContainingRule(t *testing.T) {
	ts := []watchTarget{{service: "app", hostDir: filepath.FromSlash("/a/src")}, {service: "web", hostDir: filepath.FromSlash("/a/web")}}
	if tgt, ok := targetFor(ts, filepath.FromSlash("/a/src/x.js")); !ok || tgt.service != "app" {
		t.Errorf("in-tree path should match app, got %v ok=%v", tgt.service, ok)
	}
	if _, ok := targetFor(ts, filepath.FromSlash("/a/other/x.js")); ok {
		t.Error("a path under no rule must not match")
	}
}

// addSubtree must not register ignored directories with the watcher (so a large
// node_modules doesn't flood it) — the exclusion the flood-prevention relies on.
func TestAddSubtreeSkipsIgnoredDirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "dep"), 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := addSubtree(w, watchTarget{hostDir: dir, ignore: []string{"node_modules/"}}, dir); err != nil {
		t.Fatal(err)
	}
	var srcWatched bool
	for _, p := range w.WatchList() {
		if strings.Contains(p, "node_modules") {
			t.Errorf("ignored subtree must not be watched, but %q is", p)
		}
		if strings.HasSuffix(p, string(filepath.Separator)+"src") {
			srcWatched = true
		}
	}
	if !srcWatched {
		t.Errorf("a non-ignored subdir should be watched, WatchList=%v", w.WatchList())
	}
}

// When two services watch the same path, only the first (in startup order) acts
// — never both.
func TestHandleChangeFirstServiceWins(t *testing.T) {
	rt, log := watchShim(t)
	dir := t.TempDir()
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Name: "a", Image: "a", Develop: &compose.Develop{Watch: []compose.WatchRule{{Action: "sync", Path: dir, Target: "/x"}}}},
		"b": {Name: "b", Image: "b", Develop: &compose.Develop{Watch: []compose.WatchRule{{Action: "sync", Path: dir, Target: "/y"}}}},
	}}
	o := New(p, rt, "opossum", &bytes.Buffer{})
	o.handleChange(filepath.Join(dir, "f.js"))
	got := log()
	if n := strings.Count(got, "a.demo.opossum:") + strings.Count(got, "b.demo.opossum:"); n != 1 {
		t.Errorf("exactly one service should sync (first-match), got %d cp targets in %q", n, got)
	}
}

// A directory created while watching must be picked up (fsnotify isn't recursive).
func TestWatchWatchesNewlyCreatedDirectory(t *testing.T) {
	rt, log := watchShim(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	o := New(watchProject(src, "sync"), rt, "opossum", &bytes.Buffer{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- o.Watch(ctx) }()
	time.Sleep(300 * time.Millisecond) // let the watcher register src before we act

	newdir := filepath.Join(src, "feature")
	if err := os.MkdirAll(newdir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Re-write y.js each iteration at an interval longer than the 100ms debounce:
	// once the new dir's Create event registers newdir with the watcher, a later
	// write is caught — so the test doesn't hinge on write-before-register timing,
	// and the interval is long enough not to keep re-arming the debounce timer.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(log(), "feature") {
		_ = os.WriteFile(filepath.Join(newdir, "y.js"), []byte("hi"), 0o644)
		time.Sleep(250 * time.Millisecond)
	}
	cancel()
	<-done
	if want := "app.demo.opossum:/app/src/feature/y.js"; !strings.Contains(log(), want) {
		t.Errorf("a file in a newly created directory should sync to %q, got: %q", want, log())
	}
}

func TestIgnored(t *testing.T) {
	cases := []struct {
		rel     string
		ignore  []string
		want    bool
		comment string
	}{
		{"main.go", nil, false, "no patterns"},
		{"node_modules/x/y.js", []string{"node_modules/"}, true, "dir prefix ignores subtree"},
		{"node_modules", []string{"node_modules/"}, true, "the dir itself"},
		{"src/app.js", []string{"node_modules"}, false, "unrelated"},
		{"a/b/c.log", []string{"*.log"}, true, "glob on basename"},
		{"a/b/c.js", []string{"*.log"}, false, "glob doesn't match"},
		{"tmp/x", []string{"tmp/*"}, true, "glob on relative path"},
	}
	for _, c := range cases {
		if got := ignored(c.rel, c.ignore); got != c.want {
			t.Errorf("ignored(%q, %v) = %v, want %v (%s)", c.rel, c.ignore, got, c.want, c.comment)
		}
	}
}

func TestWatchTargetMatchAndContainerTarget(t *testing.T) {
	root := filepath.Join("/host", "proj", "src")
	tgt := watchTarget{service: "app", action: "sync", hostDir: root, target: "/app/src"}

	// A file under the watched dir maps to target + relative path.
	rel, ok := tgt.match(filepath.Join(root, "components", "x.js"))
	if !ok || rel != filepath.Join("components", "x.js") {
		t.Fatalf("match under dir = (%q, %v)", rel, ok)
	}
	if got := tgt.containerTarget(rel); got != "/app/src/components/x.js" {
		t.Errorf("containerTarget = %q", got)
	}

	// A path outside the watched dir doesn't match.
	if _, ok := tgt.match(filepath.Join("/host", "proj", "other", "y.js")); ok {
		t.Error("path outside the watched dir must not match")
	}

	// A sibling that shares the watched dir's name as a prefix must not match
	// (the classic Rel-based prefix trap: /host/proj/src vs /host/proj/src-gen).
	if _, ok := tgt.match(filepath.Join("/host", "proj", "src-gen", "z.js")); ok {
		t.Error("a prefix-sharing sibling directory must not match")
	}

	// A single-file watch (rel ".") maps straight to target.
	file := watchTarget{hostDir: "/host/proj/config.js", target: "/app/config.js"}
	rel, ok = file.match("/host/proj/config.js")
	if !ok || rel != "." {
		t.Fatalf("single-file match = (%q, %v)", rel, ok)
	}
	if got := file.containerTarget(rel); got != "/app/config.js" {
		t.Errorf("single-file containerTarget = %q", got)
	}
}

// loggingShim writes a `container` stand-in that records each invocation, for
// asserting which command handleChange dispatches (kept local to the internal
// test package, which can't see the external suite's shim).
func watchShim(t *testing.T) (*runtime.Runtime, func() string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "log")
	shim := filepath.Join(dir, "shim.sh")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\necho \"$*\" >> \""+logPath+"\"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &runtime.Runtime{Bin: shim}, func() string {
		b, _ := os.ReadFile(logPath)
		return string(b)
	}
}

func watchProject(dir, action string) *compose.Project {
	return &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"app": {Name: "app", Image: "app", Develop: &compose.Develop{Watch: []compose.WatchRule{
			{Action: action, Path: dir, Target: "/app/src", Ignore: []string{"node_modules/"}},
		}}},
	}}
}

func TestHandleChangeSyncsToContainer(t *testing.T) {
	rt, log := watchShim(t)
	dir := t.TempDir()
	o := New(watchProject(dir, "sync"), rt, "opossum", &bytes.Buffer{})

	changed := filepath.Join(dir, "components", "x.js")
	if svc, kind, matched := o.handleChange(changed); !matched || svc != "" || kind != "" {
		t.Fatalf("sync change: (svc=%q, kind=%q, matched=%v), want ('','',true)", svc, kind, matched)
	}
	// The sync copies the host file to <container>:<target>/<relative path>.
	want := "cp " + changed + " app.demo.opossum:/app/src/components/x.js"
	if got := log(); !strings.Contains(got, want) {
		t.Errorf("want a %q invocation, got: %q", want, got)
	}
}

func TestHandleChangeSkipsIgnored(t *testing.T) {
	rt, log := watchShim(t)
	dir := t.TempDir()
	o := New(watchProject(dir, "sync"), rt, "opossum", &bytes.Buffer{})

	if _, _, matched := o.handleChange(filepath.Join(dir, "node_modules", "dep", "index.js")); matched {
		t.Error("an ignored path must not match/sync")
	}
	if strings.Contains(log(), "cp ") {
		t.Errorf("ignored change must not run cp, got: %q", log())
	}
}

// A watch rule with no explicit action defaults to sync.
func TestWatchTargetsDefaultsActionToSync(t *testing.T) {
	p := &compose.Project{Name: "demo", BaseDir: "/base", Services: map[string]*compose.Service{
		"app": {Name: "app", Image: "app", Develop: &compose.Develop{Watch: []compose.WatchRule{
			{Path: "./src", Target: "/app/src"}, // no action
		}}},
	}}
	o := New(p, &runtime.Runtime{}, "opossum", &bytes.Buffer{})
	ts := o.watchTargets()
	if len(ts) != 1 || ts[0].action != "sync" {
		t.Fatalf("watchTargets = %+v, want one target defaulted to sync", ts)
	}
	if ts[0].hostDir != filepath.Clean("/base/src") {
		t.Errorf("relative path should resolve against BaseDir, got %q", ts[0].hostDir)
	}
}

// The fsnotify wiring: a file written under a watched dir triggers a sync. This
// guards the riskiest layer (real events + debounce + dispatch), end to end.
func TestWatchDispatchesFilesystemEvent(t *testing.T) {
	rt, log := watchShim(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	o := New(watchProject(src, "sync"), rt, "opossum", &bytes.Buffer{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- o.Watch(ctx) }()
	time.Sleep(200 * time.Millisecond) // let the watcher register

	if err := os.WriteFile(filepath.Join(src, "x.js"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Poll for the sync (debounce is 100ms) up to a generous deadline.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(log(), "cp ") {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	if want := "app.demo.opossum:/app/src/x.js"; !strings.Contains(log(), want) {
		t.Errorf("a filesystem write should trigger a sync to %q, got: %q", want, log())
	}
}

// A `rebuild` rule doesn't sync; it returns the service so the caller batches
// one rebuild for a burst of edits.
func TestHandleChangeRebuildReturnsService(t *testing.T) {
	rt, log := watchShim(t)
	dir := t.TempDir()
	o := New(watchProject(dir, "rebuild"), rt, "opossum", &bytes.Buffer{})

	svc, kind, matched := o.handleChange(filepath.Join(dir, "main.go"))
	if !matched || svc != "app" || kind != "rebuild" {
		t.Fatalf("rebuild change: (svc=%q, kind=%q, matched=%v), want (app, rebuild, true)", svc, kind, matched)
	}
	if strings.Contains(log(), "cp ") {
		t.Errorf("rebuild must not run cp, got: %q", log())
	}
}

// applyChanges batches follow-ups: a burst of edits under one rebuild rule
// rebuilds the service once, not per file.
func TestApplyChangesBatchesRebuild(t *testing.T) {
	rt, log := watchShim(t)
	dir := t.TempDir()
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"app": {Name: "app", Build: &compose.Build{Context: "."}, Develop: &compose.Develop{Watch: []compose.WatchRule{
			{Action: "rebuild", Path: dir},
		}}},
	}}
	o := New(p, rt, "opossum", &bytes.Buffer{})

	o.applyChanges([]string{filepath.Join(dir, "a.go"), filepath.Join(dir, "b.go"), filepath.Join(dir, "c.go")})
	if n := strings.Count(log(), "run -d --name app.demo.opossum"); n != 1 {
		t.Errorf("three edits should rebuild app once, got %d recreations: %q", n, log())
	}
}

// A service rebuilt in the same batch skips its restart (the rebuild already
// recreated the container).
func TestApplyChangesRebuildSkipsRestart(t *testing.T) {
	rt, log := watchShim(t)
	dir := t.TempDir()
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"app": {Name: "app", Build: &compose.Build{Context: "."}, Develop: &compose.Develop{Watch: []compose.WatchRule{
			{Action: "rebuild", Path: filepath.Join(dir, "src")},
			{Action: "sync+restart", Path: filepath.Join(dir, "conf")},
		}}},
	}}
	o := New(p, rt, "opossum", &bytes.Buffer{})

	o.applyChanges([]string{filepath.Join(dir, "src", "main.go"), filepath.Join(dir, "conf", "app.conf")})
	// The rebuild recreates the container; a restart on top would be redundant.
	if strings.Contains(log(), "stop app.demo.opossum") {
		t.Errorf("a rebuilt service must not also be restarted, got: %q", log())
	}
}

// sync+restart both copies the file and asks for a container restart.
func TestHandleChangeSyncRestart(t *testing.T) {
	rt, log := watchShim(t)
	dir := t.TempDir()
	o := New(watchProject(dir, "sync+restart"), rt, "opossum", &bytes.Buffer{})

	svc, kind, matched := o.handleChange(filepath.Join(dir, "app.conf"))
	if !matched || svc != "app" || kind != "restart" {
		t.Fatalf("sync+restart: (svc=%q, kind=%q, matched=%v), want (app, restart, true)", svc, kind, matched)
	}
	if !strings.Contains(log(), "cp ") {
		t.Errorf("sync+restart should also copy the file, got: %q", log())
	}
}

// rebuildService rebuilds the image and recreates the container (build +
// force-recreate), scoped to the one service, then restores up options.
func TestRebuildServiceBuildsAndRecreates(t *testing.T) {
	rt, log := watchShim(t)
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"app": {Name: "app", Build: &compose.Build{Context: "."}},
	}}
	o := New(p, rt, "opossum", &bytes.Buffer{})

	if err := o.rebuildService("app"); err != nil {
		t.Fatalf("rebuildService: %v", err)
	}
	got := log()
	if !strings.Contains(got, "build") {
		t.Errorf("rebuild should build the image, got: %q", got)
	}
	// force-recreate deletes the old container and runs a fresh one.
	if !strings.Contains(got, "delete --force app.demo.opossum") || !strings.Contains(got, "run -d --name app.demo.opossum") {
		t.Errorf("rebuild should recreate the container, got: %q", got)
	}
	// up options are restored (not left in build/force-recreate mode).
	if o.up != (upOptions{}) {
		t.Errorf("up options should be restored after rebuild, got %+v", o.up)
	}
}

// Rebuilding a service must NOT touch its dependencies: editing app's source
// must never rebuild or recreate the (already running) db it depends on.
func TestRebuildServiceLeavesDependenciesAlone(t *testing.T) {
	rt, log := watchShim(t)
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"app": {Name: "app", Build: &compose.Build{Context: "."}, DependsOn: compose.DependsOn{{Name: "db"}}},
		"db":  {Name: "db", Build: &compose.Build{Context: "."}},
	}}
	o := New(p, rt, "opossum", &bytes.Buffer{})

	if err := o.rebuildService("app"); err != nil {
		t.Fatalf("rebuildService: %v", err)
	}
	got := log()
	if !strings.Contains(got, "run -d --name app.demo.opossum") {
		t.Errorf("app should be recreated, got: %q", got)
	}
	// The dependency db must be left running untouched.
	for _, forbidden := range []string{"demo-db:latest", "delete --force db.demo.opossum", "run -d --name db.demo.opossum"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("rebuilding app must not touch dependency db (found %q), got: %q", forbidden, got)
		}
	}
}
