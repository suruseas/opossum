package orchestrator

// Error-quality evals (#277): the highest-traffic orchestrator failures must tell
// the user what to do next, and a failure that used to be silent (a bind-mount
// directory that can't be created) must now speak. Golden-substring + a mutation
// on the silent path lock these in.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	rt "github.com/suruseas/opossum/internal/runtime"
)

func TestUnknownServiceErrListsServices(t *testing.T) {
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"web": {Name: "web", Image: "x"},
		"db":  {Name: "db", Image: "x"},
	}}
	o := New(p, &rt.Runtime{}, "", &bytes.Buffer{})
	s := o.unknownServiceErr("wbe").Error()
	// Names the typo, lists the real services, and points at the discovery command.
	for _, want := range []string{`"wbe"`, "db", "web", "opossum config --services"} {
		if !strings.Contains(s, want) {
			t.Errorf("unknown-service error missing %q, got: %s", want, s)
		}
	}
}

// A bind mount whose host source can't be created used to fail silently, leaving
// the container to die later on an opaque runtime error. Now it warns with OPSM-104
// and a concrete fix.
func TestEnsureBindDirsWarnsOnFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	// A read-only parent so MkdirAll of a child fails.
	parent := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o700) })

	p := &compose.Project{Name: "demo", BaseDir: t.TempDir(), Services: map[string]*compose.Service{}}
	var out bytes.Buffer
	o := New(p, &rt.Runtime{}, "", &out)
	o.ensureBindDirs([]string{filepath.Join(parent, "child") + ":/data"})

	s := out.String()
	if !strings.Contains(s, "[OPSM-104]") || !strings.Contains(s, "mkdir -p") {
		t.Errorf("a bind-dir creation failure should warn with OPSM-104 and a fix, got: %s", s)
	}
}

// scriptShim writes a /bin/sh container stand-in from the given case body (the
// contents of a `case "$1" in … esac`), for driving Up to a specific failure.
func scriptShim(t *testing.T, cases string) *rt.Runtime {
	t.Helper()
	shim := filepath.Join(t.TempDir(), "c.sh")
	body := "#!/bin/sh\ncase \"$1\" in\n" + cases + "esac\nexit 0\n"
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return &rt.Runtime{Bin: shim}
}

func TestNetworkCreateFailureHasNextStep(t *testing.T) {
	// `network create` fails (output without "exist") → Up must explain the fix.
	shim := scriptShim(t, ""+
		"  system) echo 'status running' ;;\n"+
		"  ls) echo '[]' ;;\n"+
		"  network) if [ \"$2\" = create ]; then echo 'boom' >&2; exit 1; fi ;;\n")
	p := &compose.Project{Name: "demo", BaseDir: t.TempDir(), Services: map[string]*compose.Service{
		"web": {Name: "web", Image: "web:latest"},
	}}
	err := New(p, shim, "", &bytes.Buffer{}).Up(true)
	if err == nil {
		t.Fatal("expected Up to fail on network create")
	}
	if s := err.Error(); !strings.Contains(s, "network") || !strings.Contains(s, "container network delete") {
		t.Errorf("network-create failure should point at the fix, got: %s", s)
	}
}

func TestOneShotDepFailureHasNextStep(t *testing.T) {
	// A run-to-completion dependency that exits non-zero blocks up; the error must
	// tell the user how to inspect it.
	shim := scriptShim(t, ""+
		"  system) echo 'status running' ;;\n"+
		"  ls) echo '[]' ;;\n"+
		"  run) echo 'nonzero' >&2; exit 1 ;;\n")
	p := &compose.Project{Name: "demo", BaseDir: t.TempDir(), Services: map[string]*compose.Service{
		"init": {Name: "init", Image: "init:latest"},
		"web": {Name: "web", Image: "web:latest",
			DependsOn: compose.DependsOn{{Name: "init", Condition: compose.ConditionCompleted}}},
	}}
	err := New(p, shim, "", &bytes.Buffer{}).Up(true)
	if err == nil {
		t.Fatal("expected Up to fail on the one-shot")
	}
	if s := err.Error(); !strings.Contains(s, "did not complete successfully") || !strings.Contains(s, "opossum run init") {
		t.Errorf("one-shot failure should point at inspecting it, got: %s", s)
	}
}
