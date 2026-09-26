package runtime

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// A build the runtime refuses because of the name it is to tag the image with
// (`Error: invalid reference Abc/Def:V1`, exit 64) was answered with the advice
// every build failure gets — build it with Docker and import it — and with the
// runtime's usage text after the refusal. The name is what is wrong, whoever
// builds the Dockerfile (#1121). The runtime's own output is read out of the
// capture, not retyped: what is matched is upstream's wording.
const invalidReferenceCapture = "../../testdata/error-wordings/build-invalid-reference-141.txt"

// capturedInvalid is what the runtime wrote for the build in the capture tagged name.
func capturedInvalid(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(invalidReferenceCapture)
	if err != nil {
		t.Fatal(err)
	}
	_, after, ok := strings.Cut(string(raw), "$ container build -t "+name+" .\n")
	if !ok {
		t.Fatalf("the capture has no build tagged %q", name)
	}
	_, after, ok = strings.Cut(after, " lines) ---\n")
	if !ok {
		t.Fatalf("the capture of %q has no stderr section", name)
	}
	body, _, _ := strings.Cut(after, "\n\n")
	if !strings.HasPrefix(body, "Error: invalid reference "+name) {
		t.Fatalf("the capture of %q does not open with the runtime's refusal: %q", name, body)
	}
	return body + "\n"
}

func TestABuildTheRuntimeRefusedTheNameOfIsToldAsOne(t *testing.T) {
	for _, name := range []string{"Abc/Def:V1", "org/App:v1"} {
		t.Run(name, func(t *testing.T) {
			rt := replayBuild(t, capturedInvalid(t, name))
			err := rt.Build(BuildOptions{Tag: name, Context: ".", Redo: "opossum build"})
			if err == nil {
				t.Fatal("the build was refused, so it must fail")
			}
			if !errors.Is(err, ErrBuildInvalidReference) {
				t.Errorf("the failure is not marked as a refused name: %v", err)
			}
			for _, want := range []string{"the image name `" + name + "`", "not the Dockerfile", "lower case", "then run `opossum build` again"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error does not say %q:\n%v", want, err)
				}
			}
			if errors.Is(err, ErrBuildImageRefused) {
				t.Errorf("a refused name was told as a refused image: %v", err)
			}
		})
	}
}

// What is read as this refusal: the runtime's line, from its first byte, naming
// one token. A build step that prints the same words behind its own prefix is not
// it, and a line with more after the name is not.
func TestWhatIsReadAsARefusedName(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		want       string
	}{
		{"the runtime's line", "Error: invalid reference Abc/Def:V1", "Abc/Def:V1"},
		{"a digest reference", "Error: invalid reference Org/app@sha256:0000", "Org/app@sha256:0000"},
		{"a step's copy behind its prefix", "#5 0.045 Error: invalid reference Abc/Def:V1", ""},
		{"indented", " Error: invalid reference Abc/Def:V1", ""},
		{"a name with more after it", "Error: invalid reference Abc/Def:V1 is not something", ""},
		{"a name with a tab in it", "Error: invalid reference Abc/Def\tV1", ""},
		{"a name with a quote in it", "Error: invalid reference \"Abc/Def:V1\"", ""},
		{"a name that ends in a carriage return", "Error: invalid reference Abc/Def:V1\r", ""},
		{"no name", "Error: invalid reference ", ""},
		{"another error", "Error: invalid argument x", ""},
		{"the usage line", "Usage: container build [<options>] [<context-dir>]", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := invalidReferenceName(tc.line); got != tc.want {
				t.Errorf("invalidReferenceName(%q) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
}

// The control: a build that fails for another reason is told as it always was,
// and one whose output only quotes the words is not a refused name.
func TestABuildThatFailsForAnotherReasonIsNotToldAsARefusedName(t *testing.T) {
	rt := replayBuild(t, "#5 0.045 Error: invalid reference Abc/Def:V1\nError: unknown: \"failed to solve: process \"/bin/sh -c false\" did not complete successfully: exit code: 1\"\n")
	err := rt.Build(BuildOptions{Tag: "x", Context: ".", Redo: "opossum build"})
	if err == nil {
		t.Fatal("the build failed, so it must")
	}
	if errors.Is(err, ErrBuildInvalidReference) || strings.Contains(err.Error(), "the image name") {
		t.Errorf("a step's quoted words were read as the runtime's refusal: %v", err)
	}
}

// What is said of the cause is what was measured: upper case is named where the
// name has it in its repository, and a malformed name in lower case (`org/app:`,
// `org//app`) is told as refused and no more, as is one whose only capitals are in
// its tag or its registry host (which the runtime takes) — a "write it in lower
// case" over a name already in lower case would send the reader nowhere.
func TestTheCauseOfARefusedNameIsNamedWhereItIsKnown(t *testing.T) {
	for _, tc := range []struct {
		name       string
		upperCased bool
	}{
		{"Abc/Def:V1", true},
		{"org/App:v1", true},
		{"ABC", true},
		{"localhost/Org/app", true},
		{"LOCALHOST/org/app", true}, // no `.` or `:` in it: a path component
		{"neko-Web:latest", true},   // a service name with upper case in the default name
		{"org/app:", false},
		{"org//app", false},
		{"org/app:v1!", false},
		{"org/app:V1", false},
		{"LOCALHOST:5001/org/app", false},
		{"registry.Example.com/org/app:", false},
		{"org/app@sha256:ABC", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := invalidReferenceHint(tc.name, "opossum build")
			if said := strings.Contains(got, "upper case"); said != tc.upperCased {
				t.Errorf("upper case named as the cause = %v, want %v:\n%s", said, tc.upperCased, got)
			}
			if !strings.Contains(got, "not written") {
				t.Errorf("the hint should say where the name comes from:\n%s", got)
			}
		})
	}
}
