package orchestrator_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A service with both `build:` and `image:` is built under the name `image:`
// gives, which is what docker compose does (measured on v5.5.1, #1113). opossum
// named every built image `<project>-<service>:latest` and never read `image:`,
// so a second service using the first one's image by that name went to a
// registry for it, was refused, and `up` failed over a file docker compose
// brings up.
//
// The name was put together in five places, and every command that touches a
// built image has to agree on it: one that builds under one name and removes
// under another leaves the image behind. Each row below is a command; each
// shape is a way of writing the service.
type namedBuild struct {
	shape string
	svc   compose.Service
	want  string
}

func namedBuilds() []namedBuild {
	build := &compose.Build{Context: "."}
	return []namedBuild{
		{"build and image", compose.Service{Build: build, Image: "org/custom:v9"}, "org/custom:v9"},
		// Handed over as written. The runtime reads a name without a tag as
		// `:latest` when it builds, inspects and runs (measured on 1.4.1), which
		// is what docker compose makes of it too, so nothing is filled in here.
		{"build and image, no tag", compose.Service{Build: build, Image: "org/notag"}, "org/notag"},
		{"build alone", compose.Service{Build: build}, "demo-web:latest"},
	}
}

// oldName is what every built image used to be called.
const oldName = "demo-web:latest"

func namedProject(svc compose.Service) *compose.Project {
	s := svc
	return project("demo", map[string]*compose.Service{"web": &s})
}

// argvNames says whether a logged command line names the image, as a whole word.
func argvNames(line, image string) bool {
	return strings.Contains(" "+line+" ", " "+image+" ")
}

func linesWith(lines []string, prefix string) []string {
	var out []string
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

func TestEveryCommandAgreesOnABuiltImagesName(t *testing.T) {
	for _, nb := range namedBuilds() {
		// only says a line names the wanted image and, where that differs from
		// the old name, not the old one.
		only := func(t *testing.T, what string, lines []string) {
			t.Helper()
			if len(lines) == 0 {
				t.Fatalf("%s: nothing was asked of the runtime, so this row saw nothing", what)
			}
			for _, l := range lines {
				if !argvNames(l, nb.want) {
					t.Errorf("%s should name %q, got: %s", what, nb.want, l)
				}
				if nb.want != oldName && argvNames(l, oldName) {
					t.Errorf("%s still names %q, got: %s", what, oldName, l)
				}
			}
		}
		t.Run(nb.shape+"/build", func(t *testing.T) {
			rt, log := fakeShim(t)
			if err := orchestrator.New(namedProject(nb.svc), rt, "opossum", &bytes.Buffer{}).Build(nil); err != nil {
				t.Fatal(err)
			}
			only(t, "the build", linesWith(log(), "build "))
		})
		t.Run(nb.shape+"/up", func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, log := fakeShim(t)
			o := orchestrator.New(namedProject(nb.svc), rt, "opossum", &bytes.Buffer{})
			o.SetUpOptions(false, true, false, false, false) // --build
			if err := o.Up(true); err != nil {
				t.Fatal(err)
			}
			only(t, "the build", linesWith(log(), "build "))
			only(t, "the run", linesWith(log(), "run "))
		})
		t.Run(nb.shape+"/up finds the image it would build", func(t *testing.T) {
			// Without --build, `up` builds only when the image is missing, and the
			// name it looks for has to be the one it builds under: looking for one
			// and building the other rebuilds on every `up`.
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, log := fakeShim(t)
			if err := orchestrator.New(namedProject(nb.svc), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
				t.Fatal(err)
			}
			only(t, "the look for the image", linesWith(log(), "image inspect "))
			if built := linesWith(log(), "build "); len(built) != 0 {
				t.Errorf("the image is there, so nothing is built; got: %v", built)
			}
		})
		t.Run(nb.shape+"/run", func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, log := fakeShim(t)
			o := orchestrator.New(namedProject(nb.svc), rt, "opossum", &bytes.Buffer{})
			if err := o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{Rm: true}); err != nil {
				t.Fatal(err)
			}
			only(t, "the build", linesWith(log(), "build "))
			only(t, "the run", linesWith(log(), "run "))
		})
		t.Run(nb.shape+"/ps", func(t *testing.T) {
			rt, _ := fakeShim(t)
			var out bytes.Buffer
			if err := orchestrator.New(namedProject(nb.svc), rt, "opossum", &out).Ps(orchestrator.PsOptions{Format: "json"}); err != nil {
				t.Fatal(err)
			}
			var rows []orchestrator.ServiceStatus
			if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 1 {
				t.Fatalf("want one row of json, got %q (%v)", out.String(), err)
			}
			if rows[0].Image != nb.want {
				t.Errorf("ps shows the image as %q, want %q", rows[0].Image, nb.want)
			}
		})
		t.Run(nb.shape+"/images", func(t *testing.T) {
			rt, _ := fakeShim(t)
			var out bytes.Buffer
			if err := orchestrator.New(namedProject(nb.svc), rt, "opossum", &out).Images(orchestrator.ImagesOptions{Format: "json"}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), `"`+nb.want+`"`) {
				t.Errorf("images should show %q, got: %s", nb.want, out.String())
			}
			if nb.want != oldName && strings.Contains(out.String(), oldName) {
				t.Errorf("images still shows %q, got: %s", oldName, out.String())
			}
		})
		t.Run(nb.shape+"/down --rmi all", func(t *testing.T) {
			rt, log := fakeShim(t)
			// The old name is not there: nothing is said or done about it.
			setShimEnv(rt, "IMAGE_ABSENT="+absentUnlessWanted(nb.want))
			if err := orchestrator.New(namedProject(nb.svc), rt, "opossum", &bytes.Buffer{}).Down(false, "all", false); err != nil {
				t.Fatal(err)
			}
			only(t, "the removal", linesWith(log(), "image delete "))
		})
		t.Run(nb.shape+"/destroy", func(t *testing.T) {
			rt, _ := fakeShim(t)
			setShimEnv(rt, "IMAGE_ABSENT="+absentUnlessWanted(nb.want))
			plan, err := orchestrator.New(namedProject(nb.svc), rt, "opossum", &bytes.Buffer{}).DestroyPlanFor(false, false, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Images) != 1 || plan.Images[0] != nb.want {
				t.Errorf("destroy would remove %v, want just %q", plan.Images, nb.want)
			}
		})
	}
}

// absentUnlessWanted makes the old name absent where it is not the name wanted,
// so the rows above see only what is done about the image's own name.
func absentUnlessWanted(want string) string {
	if want == oldName {
		return "none"
	}
	return oldName
}

// The shape the issue was found in: one service builds the image under a name,
// another uses it by that name. The second has to find what the first built —
// the runtime is asked to run the same name that was built, and is not asked to
// pull anything.
func TestAServiceCanUseTheImageAnotherBuilds(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"app":    {Build: &compose.Build{Context: "."}, Image: "org/shared:v1"},
		"worker": {Image: "org/shared:v1", DependsOn: compose.DependsOn{{Name: "app"}}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	o.SetUpOptions(false, true, false, false, false) // --build
	if err := o.Up(true); err != nil {
		t.Fatal(err)
	}
	lines := log()
	built := linesWith(lines, "build ")
	if len(built) != 1 || !argvNames(built[0], "-t org/shared:v1") {
		t.Fatalf("app should be built once, as org/shared:v1; got: %v", built)
	}
	runs := linesWith(lines, "run ")
	if len(runs) != 2 {
		t.Fatalf("want both services run, got: %v", runs)
	}
	for _, r := range runs {
		if !argvNames(r, "org/shared:v1") {
			t.Errorf("both services run the image that was built, got: %s", r)
		}
	}
	if indexOf(lines, built[0]) > indexOf(lines, runs[0]) {
		t.Errorf("the image is built before anything runs it, got: %v", lines)
	}
}

// `--rmi local` goes by opossum's own name for a built image and no other.
// Under `<project>-<service>:latest` is what a build of this project made; under
// a name `image:` gives there may be something pulled, tagged by hand or built
// by another project — `up` builds only when nothing is there — and a name
// alone does not say which. So "local" leaves an image under an `image:` name,
// whoever made it, and "all" removes it (telling this project's builds apart is
// #1126). What it does clear out is the image such a service had under the old
// name, before `image:` was read: a project brought up back then still has it,
// and nothing else reads that name now.
func TestRmiLocalGoesByOpossumsOwnName(t *testing.T) {
	const image = "org/custom:v9"
	build := &compose.Build{Context: "."}
	for _, tc := range []struct {
		name string
		svc  compose.Service
		// absent are the images that are not there.
		absent string
		rmi    string
		// want is exactly what is removed, in order.
		want []string
	}{
		{"build alone: its image, by name", compose.Service{Build: build}, "none", "local", []string{oldName}},
		{"build alone, all", compose.Service{Build: build}, "none", "all", []string{oldName}},
		// The name the file gave is left by "local" and removed by "all".
		{"build and image: the image is left", compose.Service{Build: build, Image: image}, oldName, "local", nil},
		{"build and image, all", compose.Service{Build: build, Image: image}, oldName, "all", []string{image}},
		// A project brought up before `image:` was read: the old name is there.
		{"build and image, the old name is there", compose.Service{Build: build, Image: image}, "none", "local", []string{oldName}},
		{"build and image, the old name is there, all", compose.Service{Build: build, Image: image}, "none", "all", []string{oldName, image}},
		// `image:` is the very name a built image used to get: one name, once.
		{"the old name written as the image", compose.Service{Build: build, Image: oldName}, "none", "local", []string{oldName}},
		{"the old name written as the image, all", compose.Service{Build: build, Image: oldName}, "none", "all", []string{oldName}},
		// Not there: it is this service's image under opossum's own name all the
		// same, removed by name as a `build:`-alone service's is — which is what
		// tells "the name is opossum's" from "`image:` is unset".
		{"the old name written as the image, and it is not there", compose.Service{Build: build, Image: oldName}, oldName, "local", []string{oldName}},
		// A service that builds nothing never had an image of opossum's naming:
		// one called `<project>-<service>:latest` on this machine is someone's.
		{"builds nothing", compose.Service{Image: "postgres:16"}, "none", "local", nil},
		{"builds nothing, all", compose.Service{Image: "postgres:16"}, "none", "all", []string{"postgres:16"}},
		// Even under that very name: the file says this service builds nothing,
		// so what is there was pulled, or is another project's.
		{"builds nothing, and names its image as opossum would", compose.Service{Image: oldName}, "none", "local", nil},
		{"builds nothing, and names its image as opossum would, all", compose.Service{Image: oldName}, "none", "all", []string{oldName}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, "IMAGE_ABSENT="+tc.absent)
			var out bytes.Buffer
			if err := orchestrator.New(namedProject(tc.svc), rt, "opossum", &out).Down(false, tc.rmi, false); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, l := range linesWith(log(), "image delete ") {
				got = append(got, l[strings.LastIndex(l, " ")+1:])
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("removed %v, want %v", got, tc.want)
			}
			// What is said is what is done: a line for each removal and no other.
			var said []string
			for _, l := range strings.Split(out.String(), "\n") {
				if rest, ok := strings.CutPrefix(l, "Removing image "); ok {
					said = append(said, rest)
				}
			}
			if strings.Join(said, ",") != strings.Join(tc.want, ",") {
				t.Errorf("said it removed %v, want %v", said, tc.want)
			}
		})
	}

	// One service's old name can be the name another service gives its image.
	// It is one image: removed once, and said once. Under "local" too — the old
	// name goes by name, as `<project>-<service>:latest` always did, so an image
	// another service pulled under it goes with it. That is what main does with
	// the same file, and whether it should is open (#1126); it is written down
	// here so that it does not change without anyone deciding it.
	for _, rmi := range []string{"local", "all"} {
		t.Run("an old name that is also another service's image, "+rmi, func(t *testing.T) {
			rt, log := fakeShim(t)
			p := project("demo", map[string]*compose.Service{
				"api": {Build: build, Image: "org/api:v1"},
				"web": {Image: "demo-api:latest"},
			})
			if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Down(false, rmi, false); err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, l := range linesWith(log(), "image delete ") {
				if argvNames(l, "demo-api:latest") {
					n++
				}
			}
			if n != 1 {
				t.Errorf("demo-api:latest should be removed once, got %d times in %v", n, linesWith(log(), "image delete "))
			}
		})
	}
}

// `destroy` takes everything the project's services name, and the old name with
// it when it is there.
func TestDestroyListsTheImageAndTheOldName(t *testing.T) {
	svc := compose.Service{Build: &compose.Build{Context: "."}, Image: "org/custom:v9"}
	for _, tc := range []struct{ name, absent, want string }{
		{"the old image is there", "none", oldName + ",org/custom:v9"},
		{"the old image is not there", oldName, "org/custom:v9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			setShimEnv(rt, "IMAGE_ABSENT="+tc.absent)
			plan, err := orchestrator.New(namedProject(svc), rt, "opossum", &bytes.Buffer{}).DestroyPlanFor(false, false, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(plan.Images, ","); got != tc.want {
				t.Errorf("destroy would remove %q, want %q", got, tc.want)
			}
		})
	}
}

// Every build says whose it is, on the image: `-l opossum.project=<project>`.
// Nothing reads the label yet (#1126); it is put there now so that images built
// from here on can be told apart when something does.
func TestEveryBuildCarriesTheProjectsLabel(t *testing.T) {
	named := compose.Service{Build: &compose.Build{Context: "."}, Image: "org/custom:v9"}
	for _, via := range []struct {
		name string
		run  func(o *orchestrator.Orchestrator) error
	}{
		{"build", func(o *orchestrator.Orchestrator) error { return o.Build(nil) }},
		{"up", func(o *orchestrator.Orchestrator) error {
			o.SetUpOptions(false, true, false, false, false)
			return o.Up(true)
		}},
		{"run", func(o *orchestrator.Orchestrator) error {
			return o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{Rm: true})
		}},
	} {
		t.Run(via.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, log := fakeShim(t)
			if err := via.run(orchestrator.New(namedProject(named), rt, "opossum", &bytes.Buffer{})); err != nil {
				t.Fatal(err)
			}
			built := linesWith(log(), "build ")
			if len(built) != 1 || !argvNames(built[0], "-l opossum.project=demo") {
				t.Errorf("the build should carry the project's label, got: %v", built)
			}
		})
	}
}

// Bringing an image over from Docker asks Docker for it under the same name:
// docker compose names a built image as opossum now does, so there is one name
// on both sides and nothing to rename on the way. Read from what Docker and the
// runtime were actually asked — the line printed for the reader says a name too,
// and would go on saying the right one while the wrong one was fetched.
func TestAnImageIsBroughtOverFromDockerUnderItsOwnName(t *testing.T) {
	for _, nb := range namedBuilds() {
		for _, via := range []struct {
			name string
			run  func(o *orchestrator.Orchestrator) error
		}{
			{"import", func(o *orchestrator.Orchestrator) error { return o.Import() }},
			{"up --from-docker-compose", func(o *orchestrator.Orchestrator) error {
				o.SetUpOptions(false, false, false, false, true)
				return o.Up(true)
			}},
		} {
			t.Run(nb.shape+"/"+via.name, func(t *testing.T) {
				t.Setenv("XDG_STATE_HOME", t.TempDir())
				rt, log := fakeShim(t)
				// Not here yet, under either name, so `up` has something to bring.
				setShimEnv(rt, "IMAGE_ABSENT="+nb.want+" "+oldName)
				asked := filepath.Join(t.TempDir(), "docker-argv")
				docker := filepath.Join(t.TempDir(), "docker")
				script := "#!/bin/sh\necho \"$@\" >> " + asked + "\nexit 0\n"
				if err := os.WriteFile(docker, []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
				rt.DockerBin = docker
				if err := via.run(orchestrator.New(namedProject(nb.svc), rt, "opossum", &bytes.Buffer{})); err != nil {
					t.Fatal(err)
				}
				argv, err := os.ReadFile(asked)
				if err != nil {
					t.Fatalf("docker was never asked for anything: %v", err)
				}
				if got := strings.TrimSpace(string(argv)); got != "image save "+nb.want {
					t.Errorf("docker should be asked for %q, got: %q", nb.want, got)
				}
				// One name on both sides: nothing is retagged on arrival.
				if tagged := linesWith(log(), "image tag "); len(tagged) != 0 {
					t.Errorf("the image arrives under the name it is run by, so nothing is retagged; got: %v", tagged)
				}
			})
		}
	}
}
