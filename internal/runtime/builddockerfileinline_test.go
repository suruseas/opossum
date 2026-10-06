package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Dockerfile given as text is handed to `container build -f -` on its standard
// input. The shim here writes down the arguments it was given and everything it
// could read from standard input, so a row can say what the builder saw.
func TestADockerfileGivenAsTextIsReadFromStandardInput(t *testing.T) {
	const text = "FROM alpine:3\nRUN echo hello > /hello\n"
	dir := t.TempDir()
	argsFile, stdinFile := filepath.Join(dir, "args"), filepath.Join(dir, "stdin")
	shim := filepath.Join(dir, "container")
	// It reads standard input only when told to (`-f -`): a build that is not asked
	// for a Dockerfile from there is never waiting on the test's own.
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" > " + argsFile + "\ncase \" $* \" in *\" -f - \"*) cat > " + stdinFile + " ;; esac\n"
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		opts      BuildOptions
		wantArgs  string
		wantStdin string
	}{
		{"the text", BuildOptions{Tag: "x:1", Context: "/ctx", DockerfileInline: text},
			"build --progress plain -t x:1 -f - /ctx", text},
		{"the text beside a target", BuildOptions{Tag: "x:1", Context: "/ctx", DockerfileInline: text, Target: "one"},
			"build --progress plain -t x:1 -f - --target=one /ctx", text},
		// The controls: a file name is passed as a file name, and with neither there
		// is no -f at all and nothing is written to the builder.
		{"a file name", BuildOptions{Tag: "x:1", Context: "/ctx", Dockerfile: "/ctx/Dockerfile.alt"},
			"build --progress plain -t x:1 -f /ctx/Dockerfile.alt /ctx", ""},
		{"neither", BuildOptions{Tag: "x:1", Context: "/ctx"}, "build --progress plain -t x:1 /ctx", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			os.Remove(argsFile)
			os.Remove(stdinFile)
			r := &Runtime{Bin: shim}
			if err := r.Build(tc.opts); err != nil {
				t.Fatalf("build: %v", err)
			}
			args, _ := os.ReadFile(argsFile)
			if got := strings.TrimSpace(string(args)); got != tc.wantArgs {
				t.Errorf("the builder was run as %q, want %q", got, tc.wantArgs)
			}
			stdin, _ := os.ReadFile(stdinFile)
			if string(stdin) != tc.wantStdin {
				t.Errorf("the builder read %q on standard input, want %q", stdin, tc.wantStdin)
			}
		})
	}
}
