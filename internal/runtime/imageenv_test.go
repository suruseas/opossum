package runtime

import (
	"path/filepath"
	"strings"
	"testing"
)

// Evals for #488: opossum used to keep Postgres's data directory as a constant,
// and Postgres 18 moved it. The image declares where it is, so the question is
// whether we can read that back — including from an image that is not here yet,
// which is the case `up --from-docker-compose` actually meets, since it writes the
// overlay before anything is pulled.
//
// The fixtures are real `container image inspect` output (container 1.2.2,
// 2026-08-23), kept in testdata/image-inspect/ so the orchestrator can use the same
// files, with the layer history dropped: it has nothing to do with the environment
// and would only make them long.

// imageInspectShim answers `image inspect` with the given file, and fails the way the
// runtime does for a reference it does not have.
func imageInspectShim(t *testing.T, fixture string) *Runtime {
	t.Helper()
	shim := filepath.Join(t.TempDir(), "c")
	if fixture == "" {
		writeShimFile(t, shim, "#!/bin/sh\necho 'Error: image not found: nope' >&2\nexit 1\n")
	} else {
		abs, err := filepath.Abs(fixture)
		if err != nil {
			t.Fatal(err)
		}
		writeShimFile(t, shim, "#!/bin/sh\ncat "+abs+"\n")
	}
	return &Runtime{Bin: shim}
}

func TestImageEnvReadsWhereTheImageKeepsItsData(t *testing.T) {
	for _, tc := range []struct{ fixture, want string }{
		{"../../testdata/image-inspect/postgres17.json", "/var/lib/postgresql/data"},
		// The move that made a constant wrong.
		{"../../testdata/image-inspect/postgres18.json", "/var/lib/postgresql/18/docker"},
	} {
		env, ok := imageInspectShim(t, tc.fixture).ImageEnv("postgres")
		if !ok {
			t.Fatalf("%s: the image declares an environment; it should be read", tc.fixture)
		}
		if got := env["PGDATA"]; got != tc.want {
			t.Errorf("%s: PGDATA is %q, the image says %q", tc.fixture, got, tc.want)
		}
	}
}

// The two fixtures have to disagree, or reading the wrong one would still pass.
func TestTheFixturesDisagreeAboutWhereTheDataGoes(t *testing.T) {
	a, _ := imageInspectShim(t, "../../testdata/image-inspect/postgres17.json").ImageEnv("postgres")
	b, _ := imageInspectShim(t, "../../testdata/image-inspect/postgres18.json").ImageEnv("postgres")
	if a["PGDATA"] == b["PGDATA"] {
		t.Fatalf("both fixtures say %q, so this file cannot tell a stale constant from a read", a["PGDATA"])
	}
}

// An image nobody has pulled yet is the ordinary case when the overlay is written,
// and the answer has to be "I could not ask" rather than an empty environment that
// reads like "the image declares nothing".
func TestImageEnvSaysSoWhenTheImageIsNotHere(t *testing.T) {
	if env, ok := imageInspectShim(t, "").ImageEnv("nope"); ok {
		t.Errorf("a missing image cannot be asked, got: %v", env)
	}
}

func TestImageEnvSaysSoWhenTheOutputIsNotWhatWeExpect(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "c")
	writeShimFile(t, shim, "#!/bin/sh\necho 'not json at all'\n")
	if env, ok := (&Runtime{Bin: shim}).ImageEnv("x"); ok {
		t.Errorf("output we cannot parse is not an answer, got: %v", env)
	}
}

// The exit status is the answer to "could we ask", not whether something parseable
// came out. Dropping the error check leaves this the only thing between a missing
// image and an environment read from whatever the runtime happened to print.
func TestImageEnvTrustsTheExitStatusOverTheOutput(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "c")
	abs, err := filepath.Abs("../../testdata/image-inspect/postgres18.json")
	if err != nil {
		t.Fatal(err)
	}
	writeShimFile(t, shim, "#!/bin/sh\ncat "+abs+"\nexit 1\n")
	if env, ok := (&Runtime{Bin: shim}).ImageEnv("postgres:18-alpine"); ok {
		t.Errorf("the runtime failed; what it printed is not an answer, got: %v", env)
	}
}

// Output we only half understood is not an answer either. Go fills what it can
// before it fails, so ignoring the error here would let a truncated or malformed
// document answer — the first element read, the rest silently dropped.
func TestImageEnvDoesNotAnswerFromOutputItOnlyHalfParsed(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "c")
	// Valid up to the second element, which is the wrong type for the field.
	writeShimFile(t, shim, "#!/bin/sh\ncat <<'JSON'\n"+
		`[{"variants":[{"config":{"config":{"Env":["PGDATA=/read/from/broken"]}}}]},{"variants":5}]`+
		"\nJSON\n")
	if env, ok := (&Runtime{Bin: shim}).ImageEnv("x"); ok {
		t.Errorf("a document we could not finish reading is not an answer, got: %v", env)
	}
}

// An image that declares nothing answered. Reporting that as "could not ask" would
// have a caller tell its reader the image was not there.
func TestImageEnvCountsAnEmptyEnvironmentAsAnAnswer(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "c")
	writeShimFile(t, shim, "#!/bin/sh\necho '[{\"variants\":[{\"config\":{\"config\":{}}}]}]'\n")
	env, ok := (&Runtime{Bin: shim}).ImageEnv("x")
	if !ok {
		t.Errorf("the image answered; an empty environment is what it said, got ok=false")
	}
	if len(env) != 0 {
		t.Errorf("it declared nothing, got: %v", env)
	}
}

// Whatever the runtime says on stderr is neither part of the JSON nor something to
// print at someone who is only having an overlay written — not finding the image is
// the ordinary case there.
func TestImageEnvKeepsStderrOutOfTheAnswerAndOffTheScreen(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "c")
	writeShimFile(t, shim, "#!/bin/sh\necho 'warning: something' >&2\n"+
		"echo '[{\"variants\":[{\"config\":{\"config\":{\"Env\":[\"PGDATA=/var/lib/postgresql/data\"]}}}]}]'\n")
	var env map[string]string
	var ok bool
	printed := captureStderr(t, func() { env, ok = (&Runtime{Bin: shim}).ImageEnv("x") })
	if !ok || env["PGDATA"] != "/var/lib/postgresql/data" {
		t.Errorf("a warning on stderr is not part of the answer, got ok=%v env=%v", ok, env)
	}
	if strings.Contains(printed, "warning: something") {
		t.Errorf("the runtime's stderr should not reach the screen here, got: %q", printed)
	}
}
