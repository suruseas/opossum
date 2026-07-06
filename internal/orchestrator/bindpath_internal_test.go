package orchestrator

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

func TestResolvePathExpandsTilde(t *testing.T) {
	o := &Orchestrator{Project: &compose.Project{BaseDir: "/base"}}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	if got, want := o.resolvePath("~/data"), filepath.Join(home, "data"); got != want {
		t.Errorf("~/data expanded to %q, want %q", got, want)
	}
	if got := o.resolvePath("/abs/x"); got != "/abs/x" {
		t.Errorf("absolute path changed: %q", got)
	}
	if got, want := o.resolvePath("./rel"), filepath.Join("/base", "rel"); got != want {
		t.Errorf("relative path = %q, want %q", got, want)
	}
}

func TestEnsureBindDirsCreatesMissingBindOnly(t *testing.T) {
	base := t.TempDir()
	o := &Orchestrator{Project: &compose.Project{Name: "demo", BaseDir: base}, out: &bytes.Buffer{}}
	o.ensureBindDirs([]string{"./data:/data", "named:/x", "/anon"})

	if _, err := os.Stat(filepath.Join(base, "data")); err != nil {
		t.Errorf("a missing bind-mount host dir should be created: %v", err)
	}
	// A named volume ("named:/x") must not create a host directory.
	if _, err := os.Stat(filepath.Join(base, "named")); err == nil {
		t.Error("a named volume must not be created as a host directory")
	}
}
