package orchestrator

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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
	o.ensureBindDirs("svc", []string{"./data:/data", "named:/x", "/anon"})

	if _, err := os.Stat(filepath.Join(base, "data")); err != nil {
		t.Errorf("a missing bind-mount host dir should be created: %v", err)
	}
	// A named volume ("named:/x") must not create a host directory.
	if _, err := os.Stat(filepath.Join(base, "named")); err == nil {
		t.Error("a named volume must not be created as a host directory")
	}
}

// A bind source that names a file gets a directory, because a directory is the
// only thing that can be created. The container then starts and carries on
// without the file — the init script never runs — so the failure arrives later,
// as something else, with nothing pointing back here. Saying it at the moment of
// creation is the only place the connection is still visible.
func TestEnsureBindDirsSaysWhenAFileBecameADirectory(t *testing.T) {
	base := t.TempDir()
	var out bytes.Buffer
	o := &Orchestrator{Project: &compose.Project{Name: "demo", BaseDir: base}, out: &out}
	o.ensureBindDirs("mongo", []string{
		"./mongodb-init-replica-set.js:/docker-entrypoint-initdb.d/mongodb-init-replica-set.js",
		"./appdata:/opt/app/data",
	})
	s := out.String()
	if !strings.Contains(s, string(codeBindFilePlaceholder)) {
		t.Errorf("a file-shaped bind source that had to be created should say so, got:\n%s", s)
	}
	// The recovery step is a delete, so the path it names has to be the one on the
	// host — not the container target, and not (as the first version of this had it)
	// the service name. Asserting that `rmdir` is followed by something is what let
	// that ship; the path itself is the thing to pin.
	if want := "rmdir " + filepath.Join(base, "mongodb-init-replica-set.js"); !strings.Contains(s, want) {
		t.Errorf("the warning should say %q, got:\n%s", want, s)
	}
	if !strings.Contains(s, "opossum up") {
		t.Errorf("the warning should say how to carry on, got:\n%s", s)
	}
	// The name is the only evidence, so a directory legitimately called `conf.d` or
	// `.ssh` reaches here too. The instruction has to stay inside that condition and
	// name the other case, or this tells someone to delete a directory opossum was
	// right to create. AGENTS.md says it works this way; this is what makes that true.
	for _, want := range []string{"If that path is meant to be a file", "meant to be a directory"} {
		if !strings.Contains(s, want) {
			t.Errorf("the advice has to stay conditional — missing %q in:\n%s", want, s)
		}
	}
	// The host path and the container target are both in the sentence, and swapping
	// them reads as sense while pointing at the wrong filesystem.
	if want := "mounts " + filepath.Join(base, "mongodb-init-replica-set.js") +
		" at /docker-entrypoint-initdb.d/mongodb-init-replica-set.js"; !strings.Contains(s, want) {
		t.Errorf("the warning should read %q, got:\n%s", want, s)
	}
	// The directory mount beside it is ordinary and must stay quiet, or the
	// warning fires on every project that ever had a missing bind source.
	if strings.Count(s, string(codeBindFilePlaceholder)) != 1 {
		t.Errorf("only the file-shaped mount should warn, got:\n%s", s)
	}
}

// The predicate needs both halves — the same name on both sides AND an extension.
// A quiet case that misses on both at once (`./appdata:/opt/app/data`) cannot tell
// which half is doing the work, so dropping either one leaves the tests green.
// These miss on exactly one each.
func TestEnsureBindDirsNeedsBothHalvesOfTheFileShape(t *testing.T) {
	for _, tc := range []struct{ name, mount string }{
		// An extension on the target, but a different name: not a file passed through.
		{"different names", "./logs:/var/log/app.log"},
		// The same name on both sides, but no extension: an ordinary directory.
		{"no extension", "./conf:/etc/conf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			var out bytes.Buffer
			o := &Orchestrator{Project: &compose.Project{Name: "demo", BaseDir: base}, out: &out}
			o.ensureBindDirs("svc", []string{tc.mount})
			if s := out.String(); strings.Contains(s, string(codeBindFilePlaceholder)) {
				t.Errorf("%s is not a file handed through, so this should be quiet, got:\n%s", tc.mount, s)
			}
		})
	}
}

// The warning is about creating a placeholder. A file that is already there is
// the normal case and has nothing to report.
func TestEnsureBindDirsIsQuietWhenTheFileExists(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "init.js"), []byte("// real\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	o := &Orchestrator{Project: &compose.Project{Name: "demo", BaseDir: base}, out: &out}
	o.ensureBindDirs("mongo", []string{"./init.js:/docker-entrypoint-initdb.d/init.js"})
	if s := out.String(); strings.Contains(s, string(codeBindFilePlaceholder)) {
		t.Errorf("the file is there, so there is nothing to warn about, got:\n%s", s)
	}
}
