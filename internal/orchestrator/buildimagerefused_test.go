package orchestrator_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// Every build failure ends with the way round a builder that cannot cope: build
// the image with Docker and import it. Over a registry that would not hand over
// an image the Dockerfile names, that is advice about something that did not
// happen — the tag is not there whoever builds it (measured with nothing logged
// in and the image not held locally: `docker build` over the same Dockerfile
// fails too, on the same name) — and it was the only advice given (#1104). The runtime's own line is read out of the capture, not retyped.
const importAdvice = "build it with Docker and import it"

func refusedLine(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/error-wordings/build-image-refused-141.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, `Error: unknown: "HTTP request to `) {
			return l
		}
	}
	t.Fatal("the capture no longer holds the refusal of a tag that is not there")
	return ""
}

func buildableProject() *compose.Project {
	return project("demo", map[string]*compose.Service{
		"web": {Build: &compose.Build{Context: "."}},
	})
}

func TestARefusedImageIsNotAnsweredWithTheDockerWayRound(t *testing.T) {
	refused := refusedLine(t)
	// Each command that builds, because each has its own call and its own
	// wrapping of what comes back; the hint names the one that was typed.
	commands := []struct {
		name, redo string
		// seesStdout is whether the runtime's stdout is this test's to read.
		// `run` sends it to stderr itself before it builds, so nothing a build
		// writes there reaches a buffer set here.
		seesStdout bool
		run        func(o *orchestrator.Orchestrator) error
	}{
		// `up --build`: the fake answers that the image is already there, and an
		// `up` that finds it does not build at all.
		{"up", "opossum up", true, func(o *orchestrator.Orchestrator) error {
			o.SetUpOptions(false, true, false, false, false)
			return o.Up(true)
		}},
		{"run", "opossum run", false, func(o *orchestrator.Orchestrator) error {
			return o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{Rm: true})
		}},
		{"build", "opossum build", true, func(o *orchestrator.Orchestrator) error { return o.Build(nil) }},
	}
	for _, cmd := range commands {
		for _, tc := range []struct {
			name        string
			stderr      string
			wantRefused bool
		}{
			{"the registry refused an image", refused, true},
			// The control, and what keeps the first row from being "the advice
			// is gone": a step that fails is still where the way round belongs.
			{"a step failed", "", false},
			// The mirror: a failed step whose closing line quotes the refusal.
			{"a step failed quoting a refusal",
				`Error: unknown: "failed to solve: process "/bin/sh -c echo '` + refused + `'" did not complete successfully: exit code: 1"`, false},
		} {
			t.Run(cmd.name+"/"+tc.name, func(t *testing.T) {
				t.Setenv("XDG_STATE_HOME", t.TempDir())
				rt, _ := fakeShim(t)
				env := []string{"BUILD_FAIL=1"}
				if tc.stderr != "" {
					env = append(env, "BUILD_FAIL_STDERR="+tc.stderr)
				}
				setShimEnv(rt, env...)
				var stdout bytes.Buffer
				rt.Out = &stdout
				err := cmd.run(orchestrator.New(buildableProject(), rt, "opossum", &bytes.Buffer{}))
				// The runtime writes all of a build's output to stderr (the
				// capture: stdout is 0 bytes). What is read for the hint is read
				// from both streams, so a fake that failed on stdout would pass
				// with the stream a real build uses left unread. The `up` and
				// `build` rows are the ones that can see it.
				if cmd.seesStdout && strings.Contains(stdout.String(), "Error: ") {
					t.Fatalf("the fake wrote the build's failure to stdout; the runtime writes it to stderr: %q", stdout.String())
				}
				if err == nil {
					t.Fatal("the build fails, so the command must fail")
				}
				got := err.Error()
				if !strings.Contains(got, `building service "web"`) {
					t.Fatalf("this row needs the build to be what failed, got: %s", got)
				}
				if strings.Contains(got, "a registry refused an image this build pulls") != tc.wantRefused {
					t.Errorf("told as a refused image: want %v, got: %s", tc.wantRefused, got)
				}
				if strings.Contains(got, importAdvice) == tc.wantRefused {
					t.Errorf("the Docker way round belongs to every build failure but a refused image: want it %v, got: %s", !tc.wantRefused, got)
				}
				if tc.wantRefused && !strings.Contains(got, "then run `"+cmd.redo+"` again") {
					t.Errorf("the hint should end in the command that was typed (%s), got: %s", cmd.redo, got)
				}
			})
		}
	}
}
