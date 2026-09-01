package orchestrator

// A failed build ends with "type this again" advice, and builds are reached from
// three commands (#667). The hint table in internal/runtime already proves each
// wording carries whatever verb it is given; what the table cannot prove is that
// each caller here hands over its own verb and not a neighbour's — the three
// call sites pass same-typed string literals, so a swap compiles and stays green
// everywhere except these tests, which type each command for real and read the
// advice that comes back.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

func TestABuildFailureRetypesTheCommandThatWasTyped(t *testing.T) {
	// The shim answers enough of the CLI for up/run/build to reach the build
	// step, then fails it with a disk-full signature so the hint (and its retry
	// verb) lands on the returned error. `image inspect` fails too, or up thinks
	// the image is already built and never builds.
	shim := scriptShim(t, ""+
		"  system) echo 'status running' ;;\n"+
		"  ls) echo '[]' ;;\n"+
		"  image) exit 1 ;;\n"+
		"  build) echo 'no space left on device' >&2; exit 1 ;;\n")
	project := func() *compose.Project {
		return &compose.Project{Name: "demo", BaseDir: t.TempDir(), Services: map[string]*compose.Service{
			"web": {Name: "web", Build: &compose.Build{Context: "."}},
		}}
	}
	verbs := []string{"opossum up", "opossum run", "opossum build"}
	check := func(t *testing.T, err error, typed string) {
		t.Helper()
		if err == nil {
			t.Fatal("the shim fails every build, so this had to fail")
		}
		s := err.Error()
		if !strings.Contains(s, "then: "+typed) {
			t.Errorf("the advice after a failed %s should retype it, got: %s", typed, s)
		}
		for _, other := range verbs {
			if other != typed && strings.Contains(s, other) {
				t.Errorf("after a failed %s the advice names %q: %s", typed, other, s)
			}
		}
	}
	t.Run("up", func(t *testing.T) {
		err := New(project(), shim, "", &bytes.Buffer{}).Up(true)
		check(t, err, "opossum up")
	})
	t.Run("run", func(t *testing.T) {
		err := New(project(), shim, "", &bytes.Buffer{}).RunOneOff("web", nil, RunOneOffOptions{NoDeps: true})
		check(t, err, "opossum run")
	})
	t.Run("build", func(t *testing.T) {
		err := New(project(), shim, "", &bytes.Buffer{}).Build(nil)
		check(t, err, "opossum build")
	})
}
