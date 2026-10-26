package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A service that writes `platform:` and builds is built for that platform, as
// docker compose builds it when `build.platforms` is not written: the image was
// built for the host's arm64 and then run as linux/amd64 under Rosetta, so a
// Dockerfile that needs an amd64 package failed to build here and built there.
//
// The project holds two services that build, one naming a platform and one not,
// so a platform read from the wrong service, or the same value given to every
// build, shows; and the rows name more than linux/amd64, so a platform written
// into the build rather than taken from the service shows too. Each row goes
// through `up`, `build` and a one-off, the three places a build is asked for,
// and through `up --dry-run`, which prints the build it would run: that printed
// line is how the difference was first seen.
func TestABuildIsForTheServicesPlatform(t *testing.T) {
	for _, tc := range []struct {
		name     string
		platform string
		// want is the `--platform` the build of api is given; empty means none.
		want string
	}{
		{"linux/amd64", "linux/amd64", "--platform=linux/amd64"},
		{"linux/arm64", "linux/arm64", "--platform=linux/arm64"},
		{"a variant", "linux/arm/v7", "--platform=linux/arm/v7"},
		// The control: no platform written, none given, as before.
		{"no platform", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, c := range []struct {
				via string
				dry bool
			}{
				{"up", false}, {"build", false}, {"run", false},
				{"up", true},
			} {
				via := c.via
				name := via
				if c.dry {
					name += " --dry-run"
				}
				t.Run(name, func(t *testing.T) {
					rt, log := fakeShim(t)
					setShimEnv(rt, "IMAGE_ABSENT=demo-api:latest demo-worker:latest")
					p := project("demo", map[string]*compose.Service{
						"api":    {Platform: tc.platform, Build: &compose.Build{Context: "/api"}},
						"worker": {Build: &compose.Build{Context: "/worker"}},
					})
					o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
					// As the command line sets it: the orchestrator's plan mode
					// and the runtime's together.
					o.SetDryRun(c.dry)
					var err error
					switch via {
					case "up":
						err = o.Up(true)
					case "build":
						err = o.Build(nil)
					default:
						err = o.RunOneOff("api", nil, orchestrator.RunOneOffOptions{Rm: true})
					}
					if err != nil {
						t.Fatalf("%s: %v", via, err)
					}
					// Planned, the build is in the plan and never reaches the
					// runtime; run, it is the other way round.
					lines := log()
					if c.dry {
						lines = rt.Plan
					}
					builds := map[string]string{}
					for _, l := range lines {
						if !strings.HasPrefix(l, "build ") {
							continue
						}
						for _, ctx := range []string{"/api", "/worker"} {
							if strings.HasSuffix(l, " "+ctx) {
								builds[ctx] = l
							}
						}
					}
					api, ok := builds["/api"]
					if !ok {
						t.Fatalf("api was not built: %v", lines)
					}
					if got := platformFlag(api); got != tc.want {
						t.Errorf("api's build was given %q, want %q\n%s", got, tc.want, api)
					}
					// The one-off builds only the service it runs.
					if via == "run" {
						return
					}
					worker, ok := builds["/worker"]
					if !ok {
						t.Fatalf("worker was not built: %v", lines)
					}
					if got := platformFlag(worker); got != "" {
						t.Errorf("worker names no platform, and its build was given %q\n%s", got, worker)
					}
				})
			}
		})
	}
}

// platformFlag is the one `--platform` a build line carries, "" for none, and
// every one of them joined by "|" when there are several.
func platformFlag(line string) string {
	var got []string
	for _, f := range strings.Fields(line) {
		if f == "--platform" || strings.HasPrefix(f, "--platform=") {
			got = append(got, f)
		}
	}
	return strings.Join(got, "|")
}
