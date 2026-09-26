package orchestrator_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// `build.dockerfile` is a path from the build CONTEXT, not from the directory the
// compose file is in (docker compose v5.5.1, measured on `build`). Passed on as
// written, `container build -f` took it from where opossum runs, so a file docker
// compose builds — `{context: ./sub, dockerfile: Dockerfile.alt}` — failed here
// (`dockerfile does not exist`), and one written for opossum's reading —
// `dockerfile: sub/Dockerfile.alt` — failed under docker compose.
//
// Measured on docker compose (a context `./sub` beside the compose file, the file
// in it and one outside):
//
//	dockerfile: Dockerfile.alt              builds (from the context)
//	dockerfile: ./Dockerfile.alt            builds
//	dockerfile: sub/Dockerfile.alt          fails (there is no sub/sub/)
//	dockerfile: ../outside.Dockerfile       builds (from the context)
//	dockerfile: /an/absolute/Dockerfile     builds (as it is)
//	context: ., dockerfile: sub/Dockerfile.alt   builds
//
// Every row puts the context in a directory of its own, which is the whole point:
// with the context and the project in one directory the two readings say the same
// thing and nothing tells them apart.
func TestABuildDockerfileIsAPathFromTheContext(t *testing.T) {
	ctx := filepath.Join(testBaseDir, "sub")
	for _, tc := range []struct {
		name       string
		context    string
		dockerfile string
		// wantF is what `container build -f` is given; empty means no -f at all.
		wantF string
	}{
		{"a name in the context", "sub", "Dockerfile.alt", filepath.Join(ctx, "Dockerfile.alt")},
		{"the same with ./", "sub", "./Dockerfile.alt", filepath.Join(ctx, "Dockerfile.alt")},
		{"a path in a directory of the context", "sub", "docker/Dockerfile.alt", filepath.Join(ctx, "docker", "Dockerfile.alt")},
		{"a path out of the context", "sub", "../outside.Dockerfile", filepath.Join(testBaseDir, "outside.Dockerfile")},
		{"an absolute path is taken as it is", "sub", "/somewhere/else/Dockerfile.abs", "/somewhere/else/Dockerfile.abs"},
		{"the context is the project directory", ".", "sub/Dockerfile.alt", filepath.Join(testBaseDir, "sub", "Dockerfile.alt")},
		{"an absolute context", ctx, "Dockerfile.alt", filepath.Join(ctx, "Dockerfile.alt")},
		// The control: nothing written, nothing passed.
		{"no dockerfile written", "sub", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, via := range []string{"up", "build", "run"} {
				rt, log := fakeShim(t)
				setShimEnv(rt, "IMAGE_ABSENT=demo-api:latest")
				p := project("demo", map[string]*compose.Service{
					"api": {Build: &compose.Build{Context: tc.context, Dockerfile: tc.dockerfile}},
				})
				o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
				var err error
				switch via {
				case "up":
					err = o.Up(true)
				case "build":
					err = o.Build(nil)
				default:
					// A one-off of the service: its image is built through the same
					// assembly, from its own call site.
					err = o.RunOneOff("api", nil, orchestrator.RunOneOffOptions{Rm: true})
				}
				if err != nil {
					t.Fatalf("%s: %v", via, err)
				}
				var build string
				for _, l := range log() {
					if strings.HasPrefix(l, "build ") {
						build = l
					}
				}
				if build == "" {
					t.Fatalf("%s: nothing was built: %v", via, log())
				}
				got := ""
				if _, after, ok := strings.Cut(build, " -f "); ok {
					got, _, _ = strings.Cut(after, " ")
				}
				if got != tc.wantF {
					t.Errorf("%s: `build -f` was given %q, want %q\n%s", via, got, tc.wantF, build)
				}
			}
		})
	}
}
