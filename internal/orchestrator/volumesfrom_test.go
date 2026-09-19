package orchestrator_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A file's `volumes_from`, through the load and `up`: the user's container
// (`app`) is run with the holder's (`store`) mounts — the bind mount made
// absolute, the named volume under the project's name — as well as its own,
// and after the holder's, which it now depends on. The names sort the other
// way round, so the order is the dependency's doing.
func TestUpMountsAVolumesFromHoldersVolumesOnTheUser(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"h", "u"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(p, []byte("name: demo\nvolumes:\n  named: {}\nservices:\n  app:\n    image: alpine:3.20\n    volumes: [./u:/own]\n    volumes_from: [store]\n  store:\n    image: alpine:3.20\n    volumes: [./h:/shared, 'named:/data']\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := compose.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	rt, log := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	o := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatal(err)
	}
	lines := log()
	holder, user := -1, -1
	for i, l := range lines {
		if !strings.HasPrefix(l, "run ") {
			continue
		}
		switch {
		case strings.Contains(l, "store.demo."):
			holder = i
		case strings.Contains(l, "app.demo."):
			user = i
		}
	}
	if holder < 0 || user < 0 {
		t.Fatalf("want both containers run, got\n%s", strings.Join(lines, "\n"))
	}
	if holder > user {
		t.Errorf("the holder is run after the user that mounts its volumes:\n%s", strings.Join(lines, "\n"))
	}
	// The user's `-v` list, whole: the holder's two, then its own, and
	// nothing else.
	var mounts []string
	fields := strings.Fields(lines[user])
	for i, f := range fields {
		if f == "-v" && i+1 < len(fields) {
			mounts = append(mounts, fields[i+1])
		}
	}
	want := []string{filepath.Join(dir, "h") + ":/shared", "demo_named:/data", filepath.Join(dir, "u") + ":/own"}
	if strings.Join(mounts, " ") != strings.Join(want, " ") {
		t.Errorf("the user's mounts are %q, want %q", mounts, want)
	}
	if strings.Contains(lines[holder], "/own") {
		t.Errorf("the user's own mount is on the holder's run line: %q", lines[holder])
	}
}
