package orchestrator

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

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
	if !o.handleChange(changed) {
		t.Fatal("expected the change to match a watch rule")
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

	if o.handleChange(filepath.Join(dir, "node_modules", "dep", "index.js")) {
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

func TestHandleChangeRebuildIsNoticedNotSynced(t *testing.T) {
	rt, log := watchShim(t)
	dir := t.TempDir()
	var out bytes.Buffer
	o := New(watchProject(dir, "rebuild"), rt, "opossum", &out)

	if !o.handleChange(filepath.Join(dir, "main.go")) {
		t.Fatal("a rebuild rule should still match")
	}
	if strings.Contains(log(), "cp ") {
		t.Errorf("rebuild must not run cp, got: %q", log())
	}
	if !strings.Contains(out.String(), "rebuild") {
		t.Errorf("rebuild should print an actionable notice, got: %q", out.String())
	}
}
