package orchestrator

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

// Redis says `chown: .:` and nothing more, so for a long time the only shape it
// could be helped with was a service whose one bind mount left nothing else it
// could have been. That is not how Redis is usually written: a bind for the data
// and a bind for the config is ordinary, and there the crash used to end in a
// shrug.
//
// The image says where `.` is. Asking it is the same move as asking where a
// database keeps its data (#488), and it is a declaration — where the process
// starts — not a guess about what the process touches.
func imageInspectShim(t *testing.T, fixture string) *runtime.Runtime {
	t.Helper()
	dir := t.TempDir()
	shim := filepath.Join(dir, "c")
	body := "#!/bin/sh\necho 'Error: image not found' >&2\nexit 1\n"
	if fixture != "" {
		abs, err := filepath.Abs(fixture)
		if err != nil {
			t.Fatal(err)
		}
		body = "#!/bin/sh\ncat " + abs + "\n"
	}
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return &runtime.Runtime{Bin: shim}
}

func TestTheMountIsFoundByAskingTheImageWhereDotIs(t *testing.T) {
	const redis7 = "../../testdata/image-inspect/redis-7-alpine.json"
	const noWorkdir = "../../testdata/image-inspect/redis-redis-stack-server.json"

	for _, c := range []struct {
		name, fixture, workingDir string
		volumes                   []string
		wantTarget                string // "" = nothing may be blamed
	}{
		{
			name:    "the ordinary way Redis is written: data and config, both binds",
			fixture: redis7, volumes: []string{"./rdata:/data", "./conf:/usr/local/etc/redis"},
			wantTarget: "/data",
		},
		{
			// Nothing here says where `.` is, so the old answer stands: with more
			// than one bind, which of them died is a guess.
			name:    "an image that declares no working directory",
			fixture: noWorkdir, volumes: []string{"./rdata:/data", "./conf:/usr/local/etc/redis"},
			wantTarget: "",
		},
		{
			// The image is not on the machine yet — the case `up
			// --from-docker-compose` actually meets.
			name:    "an image that cannot be asked at all",
			fixture: "", volumes: []string{"./rdata:/data", "./conf:/usr/local/etc/redis"},
			wantTarget: "",
		},
		{
			// The compose file names one, and that is what the runtime is given,
			// so it wins over what the image declares.
			name:    "the compose file overrides the image",
			fixture: redis7, workingDir: "/usr/local/etc/redis",
			volumes:    []string{"./rdata:/data", "./conf:/usr/local/etc/redis"},
			wantTarget: "/usr/local/etc/redis",
		},
		{
			// Resolving `.` does not make a guess where there was none: the
			// working directory is not mounted at all here.
			name:    "the working directory is not one of the mounts",
			fixture: redis7, volumes: []string{"./conf:/usr/local/etc/redis", "./logs:/var/log/redis"},
			wantTarget: "",
		},
	} {
		svc := &compose.Service{Image: "redis:7-alpine", Volumes: c.volumes, WorkingDir: c.workingDir}
		o := New(&compose.Project{Name: "demo", BaseDir: t.TempDir(), Services: map[string]*compose.Service{"cache": svc}},
			imageInspectShim(t, c.fixture), "opossum", io.Discard)
		got := o.chownCrashHint("cache", svc, "chown: .: Operation not permitted")
		if c.wantTarget == "" {
			if !strings.Contains(got, "could not work out which") {
				t.Errorf("%s: nothing says which mount died, so nothing should be named, got:\n%s", c.name, got)
			}
			continue
		}
		if !strings.Contains(got, "died on "+c.wantTarget+",") {
			t.Errorf("%s: the guidance should name %s, got:\n%s", c.name, c.wantTarget, got)
		}
	}
}

// Asking the image can only add to what was known without asking. The resolved
// directory may land nowhere — a relative `working_dir:`, an image that declares
// `/`, a working directory nobody mounted — and none of that says anything about
// which mount died. An earlier version of this let those cases take away the
// answer the old code had: a service with one bind mount used to be told which
// mount died, and stopped being told.
func TestAskingTheImageNeverTakesAwayAnAnswer(t *testing.T) {
	const redis7 = "../../testdata/image-inspect/redis-7-alpine.json"
	const declaresRoot = "../../testdata/image-inspect/postgres17.json" // WorkingDir "/"

	for _, c := range []struct {
		name, fixture, workingDir, wantTarget string
		volumes                               []string
	}{
		{
			name:    "a relative working directory, which matches no mount target",
			fixture: redis7, workingDir: "data", volumes: []string{"./rdata:/data"},
			wantTarget: "/data",
		},
		{
			name:    "an image that declares /, which nobody mounts",
			fixture: declaresRoot, volumes: []string{"./pg:/var/lib/postgresql/data"},
			wantTarget: "/var/lib/postgresql/data",
		},
		{
			name:    "a working directory above the only mount",
			fixture: redis7, volumes: []string{"./sub:/data/sub"},
			wantTarget: "/data/sub",
		},
	} {
		svc := &compose.Service{Image: "redis:7-alpine", Volumes: c.volumes, WorkingDir: c.workingDir}
		o := New(&compose.Project{Name: "demo", BaseDir: t.TempDir(), Services: map[string]*compose.Service{"cache": svc}},
			imageInspectShim(t, c.fixture), "opossum", io.Discard)
		got := o.chownCrashHint("cache", svc, "chown: .: Operation not permitted")
		if !strings.Contains(got, "died on "+c.wantTarget+",") {
			t.Errorf("%s: nothing else could have been it, so it should still be named, got:\n%s", c.name, got)
		}
	}
}

// `.` is the directory the process was in. `..` is its parent, and `sub/dir` is
// something under it — resolving either of those to the working directory names a
// mount the container never mentioned, in prose that says it is not a guess. Only
// the one form is read.
func TestOnlyTheWorkingDirectoryItselfIsResolved(t *testing.T) {
	svc := &compose.Service{Image: "redis:7-alpine", Volumes: []string{"./rdata:/data", "./conf:/usr/local/etc/redis"}}
	for _, c := range []struct {
		name, logs string
		wantNamed  bool
	}{
		{name: "the directory itself", logs: "chown: .: Operation not permitted", wantNamed: true},
		{name: "the same, spelt with a slash", logs: "chown: ./: Operation not permitted", wantNamed: true},
		{name: "its parent", logs: "chown: ..: Operation not permitted"},
		{name: "something under it", logs: "chown: changing ownership of 'sub/dir': Operation not permitted"},
		{name: "somewhere else entirely, relative", logs: "chown: changing ownership of '../elsewhere': Operation not permitted"},
	} {
		o := New(&compose.Project{Name: "demo", BaseDir: t.TempDir(), Services: map[string]*compose.Service{"cache": svc}},
			imageInspectShim(t, "../../testdata/image-inspect/redis-7-alpine.json"), "opossum", io.Discard)
		got := o.chownCrashHint("cache", svc, c.logs)
		named := !strings.Contains(got, "could not work out which")
		if named != c.wantNamed {
			t.Errorf("%s: named=%v, want %v — got:\n%s", c.name, named, c.wantNamed, got)
		}
	}
}

// A path the container named is what the container said. Where the image declares
// something else, the container is the one that was there.
func TestWhatTheContainerNamedBeatsWhatTheImageDeclares(t *testing.T) {
	svc := &compose.Service{Image: "redis:7-alpine", Volumes: []string{"./m:/data/db", "./conf:/etc/x"}}
	o := New(&compose.Project{Name: "demo", BaseDir: t.TempDir(), Services: map[string]*compose.Service{"db": svc}},
		imageInspectShim(t, "../../testdata/image-inspect/redis-7-alpine.json"), "opossum", io.Discard) // declares /data
	got := o.chownCrashHint("db", svc, "chown: changing ownership of '/data/db': Operation not permitted")
	if !strings.Contains(got, "died on /data/db,") {
		t.Errorf("the container named /data/db; the image's /data must not take its place, got:\n%s", got)
	}
}

// The other half of what asking widened: a volume beside the bind used to mean
// "something else could have been it, so say nothing". With the directory
// resolved, the bind either holds it or does not, and the volume is not in the
// way any more.
func TestAVolumeBesideTheBindNoLongerHidesTheAnswer(t *testing.T) {
	const redis7 = "../../testdata/image-inspect/redis-7-alpine.json"
	for _, c := range []struct {
		name, wantTarget string
		volumes          []string
	}{
		{name: "the volume is somewhere else, and the bind holds the working directory",
			volumes: []string{"./rdata:/data", "logs:/var/log/redis"}, wantTarget: "/data"},
		// …and where the volume is the working directory, nothing is named: a
		// volume is not a mount this can propose swapping.
		{name: "the volume is the working directory",
			volumes: []string{"cachedata:/data", "./conf:/usr/local/etc/redis"}, wantTarget: ""},
	} {
		svc := &compose.Service{Image: "redis:7-alpine", Volumes: c.volumes}
		o := New(&compose.Project{Name: "demo", BaseDir: t.TempDir(), Services: map[string]*compose.Service{"cache": svc}},
			imageInspectShim(t, redis7), "opossum", io.Discard)
		got := o.chownCrashHint("cache", svc, "chown: .: Operation not permitted")
		if c.wantTarget == "" {
			if !strings.Contains(got, "could not work out which") {
				t.Errorf("%s: nothing should be named, got:\n%s", c.name, got)
			}
			continue
		}
		if !strings.Contains(got, "died on "+c.wantTarget+",") {
			t.Errorf("%s: the guidance should name %s, got:\n%s", c.name, c.wantTarget, got)
		}
	}
}

// Asking costs a process, so the question is only put when the answer is needed.
// A log that names its own path does not need it; neither does a service with no
// bind mount to propose, or one whose compose file already says where it starts.
// An earlier version of this said "asked lazily" in a comment while asking every
// time — including on `up --from-docker-compose`, where the image is usually not
// on the machine and every ask is a subprocess that fails.
func TestTheImageIsOnlyAskedWhenTheAnswerIsNeeded(t *testing.T) {
	for _, c := range []struct {
		name, logs, workingDir string
		volumes                []string
		wantAsks               int
	}{
		{name: "the log names its own path", logs: "chown: changing ownership of '/data': Operation not permitted",
			volumes: []string{"./d:/data"}, wantAsks: 0},
		{name: "the log says only `.`", logs: "chown: .: Operation not permitted",
			volumes: []string{"./d:/data"}, wantAsks: 1},
		{name: "the compose file already says where it starts", logs: "chown: .: Operation not permitted",
			workingDir: "/data", volumes: []string{"./d:/data"}, wantAsks: 0},
		{name: "there is no bind mount to proposeAnything for", logs: "chown: .: Operation not permitted",
			volumes: []string{"vol:/data"}, wantAsks: 0},
	} {
		dir := t.TempDir()
		log := filepath.Join(dir, "calls")
		shim := filepath.Join(dir, "c")
		abs, err := filepath.Abs("../../testdata/image-inspect/redis-7-alpine.json")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(shim, []byte("#!/bin/sh\necho \"$@\" >> "+log+"\ncat "+abs+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		svc := &compose.Service{Image: "redis:7-alpine", Volumes: c.volumes, WorkingDir: c.workingDir}
		o := New(&compose.Project{Name: "demo", BaseDir: t.TempDir(), Services: map[string]*compose.Service{"cache": svc}},
			&runtime.Runtime{Bin: shim}, "opossum", io.Discard)
		o.chownCrashHint("cache", svc, c.logs)
		b, _ := os.ReadFile(log)
		if n := strings.Count(string(b), "image inspect"); n != c.wantAsks {
			t.Errorf("%s: the image was asked %d time(s), want %d", c.name, n, c.wantAsks)
		}
	}
}

// …and when it is needed more than once, it is asked once.
func TestTheImageIsAskedWhereDotIsOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	shim := filepath.Join(dir, "c")
	abs, err := filepath.Abs("../../testdata/image-inspect/redis-7-alpine.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shim, []byte("#!/bin/sh\necho \"$@\" >> "+log+"\ncat "+abs+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc := &compose.Service{Image: "redis:7-alpine", Volumes: []string{"./rdata:/data", "./conf:/etc/redis"}}
	o := New(&compose.Project{Name: "demo", BaseDir: t.TempDir(), Services: map[string]*compose.Service{"cache": svc}},
		&runtime.Runtime{Bin: shim}, "opossum", io.Discard)
	for range 3 {
		o.chownCrashHint("cache", svc, "chown: .: Operation not permitted")
	}
	b, _ := os.ReadFile(log)
	if n := strings.Count(string(b), "image inspect"); n != 1 {
		t.Errorf("the image should be asked once, not %d times", n)
	}
}
