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
	"syscall"
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
	err := o.ensureBindDirs("svc", []string{src + ":/data"}, "`opossum up`")

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
	// The whole line, not the command out of it: `Contains` on "`mkdir -p <src>`"
	// is satisfied by a line that goes on to say something else entirely, and
	// this is the line a person will copy. The first line is not pinned here —
	// it ends in whatever the OS said about the failure.
	want := "  the container cannot start without it — create it yourself (`mkdir -p " + src +
		"`) or fix the parent directory's permissions, then run `opossum up` again"
	if got, n := lineStarting(err.Error(), "  the container"); n != 1 || got != want {
		t.Errorf("the line with the fix on it is not what it should be (%d lines start with it).\n got: %q\nwant: %q", n, got, want)
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

// The permissions matter because the directory is mounted into a container that
// may not run as the host user: an image declaring `USER app` cannot enter a 0700
// directory, and the failure then arrives from inside the container as a
// permission error with nothing pointing back at who made the path.
//
// Measured against docker before pinning it (Docker Desktop 29.6.2, macOS, umask
// 022): a missing bind source comes out 0755, owned by the host user. So 0755 is
// not a number someone typed, it is what docker produces —
// ~/opossum-dogfood/results/df397-bind-mode.md.
//
// The umask is cleared for the duration so the mode opossum *asks for* is what
// lands on disk. Comparing against a reference directory instead looked tidier
// and was weaker: at umask 022 a request for 0777 also arrives as 0755, so the
// world-writable version of this bug was invisible (measured — that mutation
// survived). Restoring the umask matters more than it looks: it is process-wide,
// and this package's evals run sequentially, which is the only reason this is
// safe here.
func TestEnsureBindDirsCreatesTheSourceDockerWould(t *testing.T) {
	// A tripwire, not a setting: t.Setenv makes the standard library panic if
	// t.Parallel is ever added to this test, which is the one change that would
	// make the umask below unsafe without anything else noticing.
	t.Setenv("OPOSSUM_UMASK_EVAL", "1")
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	dir := t.TempDir()
	src := filepath.Join(dir, "made", "by", "opossum")
	p := &compose.Project{Name: "demo", BaseDir: t.TempDir(), Services: map[string]*compose.Service{}}
	if err := New(p, &rt.Runtime{}, "", &bytes.Buffer{}).ensureBindDirs("web", []string{src + ":/data"}, "`opossum up`"); err != nil {
		t.Fatalf("ensureBindDirs: %v", err)
	}

	made, err := os.Stat(src)
	if err != nil {
		t.Fatalf("the bind source should have been created: %v", err)
	}
	if got := made.Mode().Perm(); got != 0o755 {
		t.Errorf("the bind source came out %04o, want 0755 — what docker creates, and what a "+
			"container running as a non-root user can enter without being world-writable", got)
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
	err := New(p, &rt.Runtime{}, "", &bytes.Buffer{}).ensureBindDirs("web", []string{link + ":/data"}, "`opossum up`")
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
	if err := o.ensureBindDirs("web", []string{conf + ":/etc/nginx/nginx.conf:ro"}, "`opossum up`"); err != nil {
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

// The advice names the command the reader actually typed. Both verbs reach
// ensureBindDirs, and the advice used to say `opossum up` to both — the person
// who typed `run` was told to redo a command they never ran. One table for
// both directions, because fixing one verb and leaving the other is exactly
// the shape this regressed into before.
func TestTheRetryAdviceNamesTheVerbThatWasTyped(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	// Every shape that speaks the advice, crossed with both verbs: the three
	// sentences are three separate format strings, and putting a verb back
	// into just one of them would slip past a table that only ever provokes
	// the mkdir failure.
	shapes := map[string]func(t *testing.T, dir string) (mount string){
		"the source cannot be made": func(t *testing.T, dir string) string {
			parent := filepath.Join(dir, "ro")
			if err := os.Mkdir(parent, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(parent, 0o700) })
			return filepath.Join(parent, "child") + ":/data"
		},
		"a dangling symlink": func(t *testing.T, dir string) string {
			link := filepath.Join(dir, "cfg")
			if err := os.Symlink(filepath.Join(dir, "gone"), link); err != nil {
				t.Fatal(err)
			}
			return link + ":/data"
		},
		"a directory stands in for a file": func(t *testing.T, dir string) string {
			return filepath.Join(dir, "nginx.conf") + ":/etc/nginx/nginx.conf"
		},
	}
	for _, verb := range []struct {
		redo, mustNot string
	}{
		{"`opossum up`", "`opossum run`"},
		{"`opossum run`", "`opossum up`"},
	} {
		for name, mk := range shapes {
			t.Run(verb.redo+"/"+name, func(t *testing.T) {
				dir := t.TempDir()
				mount := mk(t, dir)
				p := &compose.Project{Name: "demo", BaseDir: t.TempDir(), Services: map[string]*compose.Service{}}
				var out bytes.Buffer
				o := New(p, &rt.Runtime{}, "", &out)
				err := o.ensureBindDirs("svc", []string{mount}, verb.redo)
				// The advice lives in the error for the two refusals and on the
				// warn stream for the placeholder; read wherever it went.
				said := out.String()
				if err != nil {
					said += err.Error()
				}
				if !strings.Contains(said, "run "+verb.redo+" again") {
					t.Errorf("the advice should say to run %s again, said:\n%s", verb.redo, said)
				}
				if strings.Contains(said, verb.mustNot) {
					t.Errorf("the advice names %s — a command the reader never typed, said:\n%s", verb.mustNot, said)
				}
			})
		}
	}
}

// And the wiring: RunOneOff hands its own verb in. The table above proves the
// words follow the argument; this proves the argument is the right one on the
// path a real `opossum run` takes — a call site quietly passing `opossum up`
// would satisfy every case above.
func TestARefusedRunIsToldToRunAgain(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	parent := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o700) })
	p := &compose.Project{Name: "demo", BaseDir: t.TempDir(), Services: map[string]*compose.Service{
		"svc": {Image: "app:1", Volumes: []string{filepath.Join(parent, "child") + ":/data"}},
	}}
	// A real (fake) runtime binary, so the run gets past the CLI check and to
	// the bind pre-flight this measures. Nothing should start: the refusal
	// comes before any container does.
	shim, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake-container.sh"))
	if err != nil {
		t.Fatal(err)
	}
	o := New(p, &rt.Runtime{Bin: shim}, "", &bytes.Buffer{})
	err = o.RunOneOff("svc", []string{"/bin/true"}, RunOneOffOptions{})
	if err == nil {
		t.Fatal("the bind source cannot be made, so the run must refuse — nothing was measured")
	}
	if !strings.Contains(err.Error(), "then run `opossum run` again") {
		t.Errorf("someone who typed `run` should be told to run `opossum run` again, got: %v", err)
	}
	if strings.Contains(err.Error(), "`opossum up`") {
		t.Errorf("the advice names `opossum up`, which the reader never typed: %v", err)
	}
}

// The other half of the pair: Up hands in its own verb too. Only both wiring
// tests together pin the call sites — the table proves the words follow the
// argument, and each of these proves one caller passes the argument that names
// itself.
func TestARefusedUpIsToldToUpAgain(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	parent := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o700) })
	p := &compose.Project{Name: "demo", BaseDir: t.TempDir(), Services: map[string]*compose.Service{
		"svc": {Image: "app:1", Volumes: []string{filepath.Join(parent, "child") + ":/data"}},
	}}
	shim, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake-container.sh"))
	if err != nil {
		t.Fatal(err)
	}
	o := New(p, &rt.Runtime{Bin: shim}, "", &bytes.Buffer{})
	err = o.Up(true)
	if err == nil {
		t.Fatal("the bind source cannot be made, so the up must refuse — nothing was measured")
	}
	if !strings.Contains(err.Error(), "then run `opossum up` again") {
		t.Errorf("someone who typed `up` should be told to run `opossum up` again, got: %v", err)
	}
	if strings.Contains(err.Error(), "`opossum run`") {
		t.Errorf("the advice names `opossum run`, which the reader never typed: %v", err)
	}
}

// The dangling-symlink refusal names the service, the link and where the link
// points, in that order. Three strings on one format call: exchanged, the line
// would call the link a service or point the reader at the link as its own
// target, and still read as English (#559).
func TestADanglingSymlinkRefusalNamesTheServiceTheLinkThenItsTarget(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "cfg")
	gone := filepath.Join(dir, "gone")
	if err := os.Symlink(gone, link); err != nil {
		t.Fatal(err)
	}
	p := &compose.Project{Name: "demo", BaseDir: t.TempDir(), Services: map[string]*compose.Service{}}
	o := New(p, &rt.Runtime{}, "", &bytes.Buffer{})
	err := o.ensureBindDirs("svc", []string{link + ":/data"}, "`opossum up`")
	if err == nil {
		t.Fatal("a bind mount through a dangling symlink is refused")
	}
	want := "service \"svc\" needs " + link + " for a bind mount, but it is a symlink to \"" + gone + "\", and there is nothing there"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal should read %q, got: %v", want, err)
	}
}
