package orchestrator_test

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A Dockerfile written in the compose file (`build.dockerfile_inline`) is given to
// the builder on its standard input: `container build -f -` reads it from there
// (measured on 1.4.1). What matters, and what a row asks, is what the builder was
// handed: `-f -`, the text on standard input exactly as written, and no other
// `-f` — the Dockerfile in the context is not what is built.
//
// The rows go through `up` and `build`, which assemble the build in one place,
// and through a service that also writes a target and an argument, since the
// other build options ride on the same command.
func TestADockerfileWrittenInTheComposeFileIsGivenToTheBuilder(t *testing.T) {
	const inline = "FROM alpine:3\nARG WHO=default\nRUN echo $WHO > /which\n"
	for _, tc := range []struct {
		name  string
		build *compose.Build
		// wantStdin is what the builder reads on standard input; empty means it
		// is given no Dockerfile on standard input and no `-f -`.
		wantStdin string
		// wantF is the `-f` given, "-" for standard input, "" for none.
		wantF string
		extra []string // options the line must also carry
	}{
		{name: "an inline Dockerfile", build: &compose.Build{Context: "app", DockerfileInline: inline},
			wantStdin: inline, wantF: "-"},
		{name: "with a target and an argument", build: &compose.Build{Context: "app", DockerfileInline: inline, Target: "one",
			Args: compose.Environment{"WHO=me"}},
			wantStdin: inline, wantF: "-", extra: []string{"--target=one", "--build-arg=WHO=me"}},
		{name: "the text as written, blank lines and all", build: &compose.Build{Context: "app", DockerfileInline: "FROM alpine:3\n\n# a note\nRUN true\n"},
			wantStdin: "FROM alpine:3\n\n# a note\nRUN true\n", wantF: "-"},
		// The controls: an empty text is not written, and the Dockerfile in the
		// context — or the one named — is what is built, as before.
		{name: "an empty inline Dockerfile builds the context's", build: &compose.Build{Context: "app", DockerfileInline: ""}, wantF: ""},
		// An absolute file name is given as it is, whichever way relative ones are read.
		{name: "a Dockerfile file name", build: &compose.Build{Context: "app", Dockerfile: "/somewhere/Dockerfile.alt"}, wantF: "/somewhere/Dockerfile.alt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, via := range []string{"up", "build"} {
				rt, log := fakeShim(t)
				setShimEnv(rt, "IMAGE_ABSENT=demo-api:latest")
				p := project("demo", map[string]*compose.Service{"api": {Build: tc.build}})
				o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
				var err error
				if via == "up" {
					err = o.Up(true)
				} else {
					err = o.Build(nil)
				}
				if err != nil {
					t.Fatalf("%s: %v", via, err)
				}
				var build, stdin string
				gotStdin := false
				for _, l := range log() {
					switch {
					case strings.HasPrefix(l, "build "):
						build = l
					case strings.HasPrefix(l, "build-stdin "):
						gotStdin = true
						q := strings.TrimPrefix(l, "build-stdin ")
						var uerr error
						if stdin, uerr = strconv.Unquote(q); uerr != nil {
							t.Fatalf("%s: the fake logged a stdin that does not unquote: %v", via, uerr)
						}
					}
				}
				if build == "" {
					t.Fatalf("%s: nothing was built: %v", via, log())
				}
				f := ""
				if _, after, ok := strings.Cut(build, " -f "); ok {
					f, _, _ = strings.Cut(after, " ")
				}
				if f != tc.wantF {
					t.Errorf("%s: -f was %q, want %q\n%s", via, f, tc.wantF, build)
				}
				if strings.Count(build, " -f ") > 1 {
					t.Errorf("%s: more than one -f on the line:\n%s", via, build)
				}
				if gotStdin != (tc.wantStdin != "") || stdin != tc.wantStdin {
					t.Errorf("%s: the builder read %q on standard input (given: %v), want %q", via, stdin, gotStdin, tc.wantStdin)
				}
				for _, e := range tc.extra {
					if !strings.Contains(build, e) {
						t.Errorf("%s: the line does not carry %q:\n%s", via, e, build)
					}
				}
			}
		})
	}
}
