package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A build puts the labels it is given on the image.
func TestBuildLabelsTheImage(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "argv.log")
	r := &Runtime{Bin: fakeShimBin, Env: []string{"SHIM_LOG=" + logFile}}
	if err := r.Build(BuildOptions{Tag: "org/app:v1", Context: "/ctx", Labels: []string{"opossum.project=demo", "a=b"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != "build --progress plain -t org/app:v1 -l opossum.project=demo -l a=b /ctx" {
		t.Errorf("unexpected build argv: %q", got)
	}
}

// Evals for #1126: telling an opossum build's image from a docker compose
// build's from neither needs to read what an image's own labels say back —
// the other half of what TestBuildLabelsTheImage above puts there. The
// `image inspect` shim and its JSON shape are shared with ImageEnv
// (imageenv_test.go), and these follow the same pattern.
//
// The JSON shape (`variants[].config.config.Labels`) is real `container
// image inspect` output (container 1.4.1, 2026-09-28): a `container build -l
// opossum.project=testproj -l com.docker.compose.project=my_app .` puts both
// labels there, surviving a cache hit, and a Dockerfile `LABEL` loses to `-l`
// (measured for #1114 — the same build TestBuildLabelsTheImage checks the
// argv of).

func TestImageLabelsReadsWhatABuildStamped(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "c")
	writeShimFile(t, shim, "#!/bin/sh\necho '[{\"variants\":[{\"config\":{\"config\":{\"Labels\":"+
		"{\"opossum.project\":\"testproj\",\"com.docker.compose.project\":\"my_app\"}}}}]}]'\n")
	labels, ok := (&Runtime{Bin: shim}).ImageLabels("x")
	if !ok {
		t.Fatalf("the image answered; its labels should be read")
	}
	if labels["opossum.project"] != "testproj" || labels["com.docker.compose.project"] != "my_app" {
		t.Errorf("did not read both labels, got: %v", labels)
	}
}

// An image nobody has pulled yet is the ordinary case for a fresh project,
// and the answer has to be "I could not ask" rather than an empty label set
// that reads like "the image declares nothing".
func TestImageLabelsSaysSoWhenTheImageIsNotHere(t *testing.T) {
	if labels, ok := imageInspectShim(t, "").ImageLabels("nope"); ok {
		t.Errorf("a missing image cannot be asked, got: %v", labels)
	}
}

func TestImageLabelsSaysSoWhenTheOutputIsNotWhatWeExpect(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "c")
	writeShimFile(t, shim, "#!/bin/sh\necho 'not json at all'\n")
	if labels, ok := (&Runtime{Bin: shim}).ImageLabels("x"); ok {
		t.Errorf("output we cannot parse is not an answer, got: %v", labels)
	}
}

// The exit status is the answer to "could we ask", not whether something
// parseable came out.
func TestImageLabelsTrustsTheExitStatusOverTheOutput(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "c")
	writeShimFile(t, shim, "#!/bin/sh\necho '[{\"variants\":[{\"config\":{\"config\":{\"Labels\":"+
		"{\"opossum.project\":\"testproj\"}}}}]}]'\nexit 1\n")
	if labels, ok := (&Runtime{Bin: shim}).ImageLabels("x"); ok {
		t.Errorf("the runtime failed; what it printed is not an answer, got: %v", labels)
	}
}

// Output we only half understood is not an answer either — Go fills what it
// can before it fails, so ignoring the error would let a truncated or
// malformed document answer.
func TestImageLabelsDoesNotAnswerFromOutputItOnlyHalfParsed(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "c")
	writeShimFile(t, shim, "#!/bin/sh\ncat <<'JSON'\n"+
		`[{"variants":[{"config":{"config":{"Labels":{"opossum.project":"testproj"}}}}]},{"variants":5}]`+
		"\nJSON\n")
	if labels, ok := (&Runtime{Bin: shim}).ImageLabels("x"); ok {
		t.Errorf("a document we could not finish reading is not an answer, got: %v", labels)
	}
}

// An image that declares no labels answered. Reporting that as "could not
// ask" would have a caller tell its reader the image was not there.
func TestImageLabelsCountsNoLabelsAsAnAnswer(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "c")
	writeShimFile(t, shim, "#!/bin/sh\necho '[{\"variants\":[{\"config\":{\"config\":{}}}]}]'\n")
	labels, ok := (&Runtime{Bin: shim}).ImageLabels("x")
	if !ok {
		t.Errorf("the image answered; no labels is what it said, got ok=false")
	}
	if len(labels) != 0 {
		t.Errorf("it declared nothing, got: %v", labels)
	}
}

// The first variant's value for a label wins, the same rule ImageEnv uses:
// variants are the same image for different machines, and a name that
// differs between them is not something to pick a side on here.
func TestImageLabelsTakesTheFirstVariantThatDeclaresOne(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "c")
	writeShimFile(t, shim, "#!/bin/sh\necho '[{\"variants\":["+
		"{\"config\":{\"config\":{\"Labels\":{\"opossum.project\":\"first\"}}}},"+
		"{\"config\":{\"config\":{\"Labels\":{\"opossum.project\":\"second\"}}}}"+
		"]}]'\n")
	labels, ok := (&Runtime{Bin: shim}).ImageLabels("x")
	if !ok || labels["opossum.project"] != "first" {
		t.Errorf("should take the first variant's value, got ok=%v labels=%v", ok, labels)
	}
}

// A label the first variant does not declare at all is still read from a
// later one — "first wins" is a per-name tie-break, not "only the first
// variant's labels exist". This is the case #1126's caller depends on: an
// image built for multiple platforms where only one variant's build carried
// -l opossum.project=... would otherwise read as unlabeled.
func TestImageLabelsMergesKeysThatOnlyALaterVariantDeclares(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "c")
	writeShimFile(t, shim, "#!/bin/sh\necho '[{\"variants\":["+
		"{\"config\":{\"config\":{\"Labels\":{\"a\":\"1\"}}}},"+
		"{\"config\":{\"config\":{\"Labels\":{\"opossum.project\":\"p\"}}}}"+
		"]}]'\n")
	labels, ok := (&Runtime{Bin: shim}).ImageLabels("x")
	if !ok || labels["a"] != "1" || labels["opossum.project"] != "p" {
		t.Errorf("should merge keys across variants, got ok=%v labels=%v", ok, labels)
	}
}

// Whatever the runtime says on stderr is neither part of the JSON nor
// something to print at a caller only checking whose build an image is.
func TestImageLabelsKeepsStderrOutOfTheAnswer(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "c")
	writeShimFile(t, shim, "#!/bin/sh\necho 'warning: something' >&2\n"+
		"echo '[{\"variants\":[{\"config\":{\"config\":{\"Labels\":{\"opossum.project\":\"testproj\"}}}}]}]'\n")
	labels, ok := (&Runtime{Bin: shim}).ImageLabels("x")
	if !ok || labels["opossum.project"] != "testproj" {
		t.Errorf("a warning on stderr should not break the answer, got ok=%v labels=%v", ok, labels)
	}
}
