package orchestrator_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// dockerHolding is a docker CLI that holds the images in `have` (name → how long ago it built it) and logs every call it is given to the file it returns: `image
// inspect --format {{.Created}} <ref>` answers the time as the real CLI writes it, and for an image it does not hold fails as the real one does. Every other
// call is one `up` has no business making here, and is answered with a failure as well as logged, so that a test that asks what was run reads it.
func dockerHolding(t *testing.T, have map[string]time.Duration) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "docker.log")
	var cases strings.Builder
	for ref, ago := range have {
		fmt.Fprintf(&cases, "  image\\ inspect\\ --format\\ {{.Created}}\\ %s) echo %s; exit 0 ;;\n", ref, time.Now().Add(-ago).UTC().Format(time.RFC3339Nano))
	}
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\ncase \"$*\" in\n" + cases.String() +
		"  image\\ inspect*) echo 'Error response from daemon: No such image' >&2; exit 1 ;;\nesac\nexit 1\n"
	bin = filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func dockerCalls(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// `up` says, before it builds a service whose image Docker holds, that Docker holds it and how old it is, and how to take it (`opossum import <service>`): and then
// builds, as it did. Nothing is imported from there, and Docker is asked nothing but when it built the image (#1905).
func TestUpSaysDockerHoldsTheImageBeforeBuildingIt(t *testing.T) {
	rt, calls := fakeShim(t)
	setShimEnv(rt, "IMAGE_ABSENT=pj-web:latest")
	docker, dlog := dockerHolding(t, map[string]time.Duration{"pj-web:latest": 3*24*time.Hour + time.Hour})
	rt.DockerBin = docker
	p := project("pj", map[string]*compose.Service{"web": {Build: &compose.Build{Context: "."}}})
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	s := out.String()
	hint := "web: found a Docker-built image (pj-web:latest, built 3 days ago)\n     to reuse it and skip this build, run: opossum import web\n"
	at := strings.Index(s, hint)
	if at < 0 {
		t.Fatalf("want the hint %q, got: %s", hint, s)
	}
	if building := strings.Index(s, "Building web"); building < 0 || building < at {
		t.Errorf("the hint comes before the build starts (hint at %d, Building at %d): %s", at, building, s)
	}
	if !strings.Contains(strings.Join(calls(), "\n"), "build --progress") {
		t.Errorf("the build goes on, nothing is imported in its place; calls: %v", calls())
	}
	if got := dockerCalls(t, dlog); len(got) != 1 || got[0] != "image inspect --format {{.Created}} pj-web:latest" {
		t.Errorf("Docker is asked only when it built the image, read-only; its calls: %q", got)
	}
}

// What Docker cannot be asked or does not hold is not said: the build starts as it did, in the time it took.
func TestUpSaysNothingWhenDockerCannotBeAskedOrHasNoSuchImage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, rt *runtime.Runtime)
	}{
		{"Docker holds no image of that name", func(t *testing.T, rt *runtime.Runtime) {
			rt.DockerBin, _ = dockerHolding(t, map[string]time.Duration{"another:latest": time.Hour})
		}},
		{"the docker CLI is not installed", func(t *testing.T, rt *runtime.Runtime) {
			rt.DockerBin = filepath.Join(t.TempDir(), "not-there")
		}},
		{"the daemon does not answer in time", func(t *testing.T, rt *runtime.Runtime) {
			bin := filepath.Join(t.TempDir(), "docker")
			if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			rt.DockerBin = bin
			rt.DockerProbeTimeout = 300 * time.Millisecond
		}},
		{"Docker prints a time and fails", func(t *testing.T, rt *runtime.Runtime) {
			bin := filepath.Join(t.TempDir(), "docker")
			script := "#!/bin/sh\necho " + time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano) + "\nexit 1\n"
			if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			rt.DockerBin = bin
		}},
		{"what Docker prints is not a time", func(t *testing.T, rt *runtime.Runtime) {
			bin := filepath.Join(t.TempDir(), "docker")
			if err := os.WriteFile(bin, []byte("#!/bin/sh\necho yesterday\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			rt.DockerBin = bin
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, calls := fakeShim(t)
			setShimEnv(rt, "IMAGE_ABSENT=pj-web:latest")
			tc.setup(t, rt)
			p := project("pj", map[string]*compose.Service{"web": {Build: &compose.Build{Context: "."}}})
			var out bytes.Buffer
			start := time.Now()
			if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
				t.Fatalf("Up: %v", err)
			}
			// The row that sets a timeout of 300 ms is the one that is read to the end: a wait of three seconds is a timeout that was not given.
			if d := time.Since(start); d > 2*time.Second {
				t.Errorf("up waited %v for Docker, which it must not (a daemon that does not answer is not waited for)", d)
			}
			if strings.Contains(out.String(), "Docker-built image") || strings.Contains(out.String(), "opossum import") {
				t.Errorf("up said a word about Docker that it could not know: %s", out.String())
			}
			if !strings.Contains(out.String(), "Building web") || !strings.Contains(strings.Join(calls(), "\n"), "build --progress") {
				t.Errorf("the build goes on as it did; out: %s calls: %v", out.String(), calls())
			}
		})
	}
}

// Where `up` is not about to build because it was not asked to, or was asked to, or was told not to, or builds nothing, Docker is not asked.
func TestUpDoesNotAskDockerWhereItIsNotAboutToBuildWhatTheStoreLacks(t *testing.T) {
	const have = "pj-web:latest"
	for _, tc := range []struct {
		name    string
		absent  bool // the runtime's store lacks the image
		options func(o *orchestrator.Orchestrator)
		wantErr bool
	}{
		{"the image is in the store", false, func(o *orchestrator.Orchestrator) {}, false},
		{"--build asks for the build", true, func(o *orchestrator.Orchestrator) { o.SetUpOptions(false, true, false, false, false) }, false},
		{"--no-build refuses it", true, func(o *orchestrator.Orchestrator) { o.SetUpOptions(false, false, true, false, false) }, true},
		// the fake docker refuses the `image save` that the import is, so the run ends in its error; the row is about what Docker was asked before that
		{"--from-docker-compose imports instead", true, func(o *orchestrator.Orchestrator) { o.SetUpOptions(false, false, false, false, true) }, true},
		{"--dry-run touches nothing", true, func(o *orchestrator.Orchestrator) { o.SetDryRun(true) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			if tc.absent {
				setShimEnv(rt, "IMAGE_ABSENT=pj-web:latest")
			}
			docker, dlog := dockerHolding(t, map[string]time.Duration{have: time.Hour})
			rt.DockerBin = docker
			p := project("pj", map[string]*compose.Service{"web": {Build: &compose.Build{Context: "."}}})
			var out bytes.Buffer
			o := orchestrator.New(p, rt, "opossum", &out)
			tc.options(o)
			err := o.Up(true)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Up err %v, want error: %v", err, tc.wantErr)
			}
			for _, call := range dockerCalls(t, dlog) {
				if strings.HasPrefix(call, "image inspect") {
					t.Errorf("Docker was asked when it built the image where up is not about to build: %q", call)
				}
			}
			if strings.Contains(out.String(), "Docker-built image") {
				t.Errorf("up said Docker holds the image where it is not about to build: %s", out.String())
			}
		})
	}
}

// A build service that names its image is asked for by that name, which is the name Docker tags it with and the one `import` brings over; a service that does not
// build is not asked about at all.
func TestUpAsksDockerForTheNameImportBringsOver(t *testing.T) {
	rt, _ := fakeShim(t)
	setShimEnv(rt, "IMAGE_ABSENT=myco/api:9")
	docker, dlog := dockerHolding(t, map[string]time.Duration{"myco/api:9": 5 * time.Minute})
	rt.DockerBin = docker
	p := project("pj", map[string]*compose.Service{
		"api": {Build: &compose.Build{Context: "."}, Image: "myco/api:9"},
		"db":  {Image: "postgres:16"},
	})
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !strings.Contains(out.String(), "api: found a Docker-built image (myco/api:9, built 5 minutes ago)") {
		t.Errorf("want the hint for api by its image: name, got: %s", out.String())
	}
	if strings.Contains(out.String(), "db:") && strings.Contains(out.String(), "Docker-built image (postgres") {
		t.Errorf("a service that builds nothing is not asked about: %s", out.String())
	}
	if got := dockerCalls(t, dlog); len(got) != 1 {
		t.Errorf("Docker is asked once, about the service that builds; its calls: %q", got)
	}
}

// Docker is asked again by a service only if it answered the one before: the wait for one that does not is paid once in an `up`, not once for each service that
// builds (three seconds each is twelve for four).
func TestUpDoesNotWaitForADockerThatDidNotAnswerOnceForEachService(t *testing.T) {
	rt, _ := fakeShim(t)
	setShimEnv(rt, "IMAGE_ABSENT=pj-a:latest pj-b:latest pj-c:latest pj-d:latest")
	dir := t.TempDir()
	log := filepath.Join(dir, "asked.log")
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$*\" >> '"+log+"'\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt.DockerBin = bin
	rt.DockerProbeTimeout = 400 * time.Millisecond
	svcs := map[string]*compose.Service{}
	for _, n := range []string{"a", "b", "c", "d"} {
		svcs[n] = &compose.Service{Build: &compose.Build{Context: "."}}
	}
	p := project("pj", svcs)
	start := time.Now()
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	// The guard of "once" is the count below. The time is only that the up did not wait for the Docker's own sleep (30 s): a bound close to the four timeouts
	// (1.6 s) is a bound a loaded host passes through.
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("four services that build waited %v for a Docker that does not answer, which sleeps for 30 s", d)
	}
	if got := dockerCalls(t, log); len(got) != 1 {
		t.Errorf("Docker is asked once in an up when it did not answer; it was asked %d times: %q", len(got), got)
	}
}

// Each `up` asks Docker afresh: one that did not answer in a first is asked again by the next (it may be answering by then), once.
func TestEachUpAsksADockerThatDidNotAnswerOnceMore(t *testing.T) {
	rt, _ := fakeShim(t)
	setShimEnv(rt, "IMAGE_ABSENT=pj-a:latest pj-b:latest")
	dir := t.TempDir()
	log := filepath.Join(dir, "asked.log")
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$*\" >> '"+log+"'\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt.DockerBin = bin
	rt.DockerProbeTimeout = 300 * time.Millisecond
	p := project("pj", map[string]*compose.Service{
		"a": {Build: &compose.Build{Context: "."}},
		"b": {Build: &compose.Build{Context: "."}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	for i := 0; i < 2; i++ {
		if err := o.Up(true); err != nil {
			t.Fatalf("Up %d: %v", i+1, err)
		}
	}
	if got := dockerCalls(t, log); len(got) != 2 {
		t.Errorf("two ups ask a Docker that does not answer once each, %d asks were made: %q", len(got), got)
	}
}
