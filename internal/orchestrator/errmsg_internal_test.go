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

// A bind mount whose host source can't be created went through two shapes before
// this one. It failed silently, leaving the container to die later on an opaque
// runtime error; then it warned with OPSM-104 and started the service anyway,
// which put that same opaque error a second after the warning with nothing tying
// them together. Now it stops.
func TestEnsureBindDirsFailsWhenTheSourceCannotBeMade(t *testing.T) {
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
	src := filepath.Join(parent, "child")
	err := o.ensureBindDirs("svc", []string{src + ":/data"})

	// A failure, not a warning: the source is not there and opossum could not put
	// it there, so the runtime will refuse the mount (measured: `path '…' does not
	// exist`). Warning and starting the service anyway left the user with that
	// runtime error a second later and nothing tying the two together.
	if err == nil {
		t.Fatal("a bind source that cannot be created must fail the up, not just warn")
	}
	if !strings.Contains(err.Error(), "[OPSM-104]") {
		t.Errorf("the error should carry OPSM-104, got: %v", err)
	}
	// The command has to name the PATH. Checking only that the words `mkdir -p`
	// appear leaves the argument free to be anything — and this very function has
	// already shipped that bug once, telling the user to `rmdir` the service name
	// (see the note next to codeBindFilePlaceholder). A format string with two
	// same-typed arguments will take that shape again.
	if want := "`mkdir -p " + src + "`"; !strings.Contains(err.Error(), want) {
		t.Errorf("the fix should be %s, got: %v", want, err)
	}
	if !strings.Contains(err.Error(), `"svc"`) {
		t.Errorf("the error should name the service, got: %v", err)
	}
	// And it says it once. The failure carries the code and the fix, so warning as
	// well would print the same sentence twice — once as advice, once as the reason
	// the up ended.
	if s := out.String(); strings.Contains(s, "[OPSM-104]") {
		t.Errorf("the same thing should not also be warned about: %s", s)
	}
}

// `opossum run` is the other caller of ensureBindDirs, and it was the one nobody
// checked. The eval on the `up` side says the point out loud — "the helper
// refusing is not the same as the up refusing" — and the same sentence is true
// here, so the same test belongs on both. Fixing one side of a pair and leaving
// the other is how this repository has produced defects before.
func TestRunOneOffStopsWhenABindSourceCannotBeCreated(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	parent := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o700) })

	p := &compose.Project{Name: "demo", BaseDir: t.TempDir(), Services: map[string]*compose.Service{
		"app": {Name: "app", Image: "app:1", Volumes: []string{filepath.Join(parent, "data") + ":/data"}},
	}}
	shim := scriptShim(t, "  system) echo 'status running' ;;\n  ls) echo '[]' ;;\n")
	err := New(p, shim, "", &bytes.Buffer{}).RunOneOff("app", nil, RunOneOffOptions{NoDeps: true})
	if err == nil {
		t.Fatal("a bind source that cannot be created must fail the one-off too")
	}
	if !strings.Contains(err.Error(), "[OPSM-104]") {
		t.Errorf("the error should carry the code, got: %v", err)
	}
}

// A symlink whose target is gone reaches this the confusing way round: stat says
// the path is not there (it follows the link) and mkdir says it is (it does not).
// The generic message told the user to `mkdir -p` it, which fails with `file
// exists` on a path nothing can see — advice that cannot be followed is worse
// than none.
func TestEnsureBindDirsNamesADanglingSymlinkForWhatItIs(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "data")
	if err := os.Symlink(filepath.Join(dir, "gone"), link); err != nil {
		t.Fatal(err)
	}
	p := &compose.Project{Name: "demo", BaseDir: t.TempDir(), Services: map[string]*compose.Service{}}
	err := New(p, &rt.Runtime{}, "", &bytes.Buffer{}).ensureBindDirs("web", []string{link + ":/data"})
	if err == nil {
		t.Fatal("a bind source that is a broken symlink must fail the up")
	}
	if !strings.Contains(err.Error(), "[OPSM-104]") {
		t.Errorf("the error should carry the code, or it cannot be looked up: %v", err)
	}
	if !strings.Contains(err.Error(), "symlink") || !strings.Contains(err.Error(), "gone") {
		t.Errorf("the error should say it is a symlink and where it points, got: %v", err)
	}
	if strings.Contains(err.Error(), "mkdir -p") {
		t.Errorf("`mkdir -p` fails with `file exists` here, so it must not be the advice: %v", err)
	}
}

// The question is whether the source is there now, not whether MkdirAll said so.
// A bind source that is an existing FILE is the everyday case: half the compose
// files in the wild mount a config file, MkdirAll refuses to make a directory
// where a file already is, and the mount works perfectly well — a regular host
// file mounts fine (measured against `container` 1.1.0). Judging by the mkdir
// error rather than by what is on disk would fail every one of those ups.
func TestEnsureBindDirsAcceptsASourceThatIsAlreadyAFile(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "nginx.conf")
	if err := os.WriteFile(conf, []byte("server {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &compose.Project{Name: "demo", BaseDir: t.TempDir(), Services: map[string]*compose.Service{}}
	o := New(p, &rt.Runtime{}, "", &bytes.Buffer{})
	if err := o.ensureBindDirs("web", []string{conf + ":/etc/nginx/nginx.conf:ro"}); err != nil {
		t.Errorf("a config file that is already there is a mount that works: %v", err)
	}
	// And it is still a file afterwards — nothing replaced it with a directory.
	if fi, serr := os.Stat(conf); serr != nil || fi.IsDir() {
		t.Errorf("the source should still be the file it was, got %v (dir=%v)", serr, fi != nil && fi.IsDir())
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
