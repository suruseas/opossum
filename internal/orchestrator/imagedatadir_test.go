package orchestrator

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/runtime"
)

// Evals for #488. The overlay writes two halves for a bind-mounted Postgres data
// directory: the mount becomes a named volume (OPSM-105) and PGDATA moves to a
// subdirectory of it (OPSM-101). Which of them is needed depends on where the
// image keeps its cluster, and it declares that in its own config as PGDATA.
//
// Three shapes, each measured on the real runtime and kept in
// testdata/error-wordings/:
//
//   - declared AT the mount (17 and earlier): the image chowns the mount and a
//     bind mount fails — pg17-bind-old-datadir.txt. Both halves, as before.
//   - declared BELOW the mount: the image makes that subdirectory inside the mount
//     and chowns what it made, so a bind mount works —
//     pg-image-declares-pgdata-below-the-mount.txt. Neither half.
//   - declared SOMEWHERE ELSE (18 keeps its cluster under /var/lib/postgresql):
//     the pair starts — pg18-named-old-datadir-with-pgdata.txt — but the swap on
//     its own does not — pg18-named-old-datadir.txt. So both halves, or neither
//     plus a note.
//
// These tests answer `image inspect` with real output, kept in
// testdata/image-inspect/.

// planWithImage plans the overlay with a runtime whose `image inspect` answers
// from the given fixture; an empty fixture means the image is not here, which is
// the ordinary case when the overlay is written, since nothing is pulled yet.
func planWithImage(t *testing.T, body, fixture string) (string, []Adaptation) {
	t.Helper()
	shim := filepath.Join(t.TempDir(), "c")
	script := "#!/bin/sh\necho 'Error: image not found' >&2\nexit 1\n"
	if fixture != "" {
		abs, err := filepath.Abs(fixture)
		if err != nil {
			t.Fatal(err)
		}
		script = "#!/bin/sh\ncat " + abs + "\n"
	}
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	o := New(loadProject(t, body), &runtime.Runtime{Bin: shim}, "opossum", io.Discard)
	return o.PlanOverlay()
}

const pgBindOldPath = `services:
  db:
    image: postgres:18-alpine
    environment:
      POSTGRES_PASSWORD: x
    volumes:
      - ./data:/var/lib/postgresql/data
`

// 18 will not look at this mount as it stands, but the pair puts the cluster back
// into it and that starts (pg18-named-old-datadir-with-pgdata.txt). Both halves go
// in, as they did before this change.
func TestOverlayWritesBothHalvesForPostgres18(t *testing.T) {
	overlay, changes := planWithImage(t, pgBindOldPath, "../../testdata/image-inspect/postgres18.json")
	var codes []string
	for _, c := range changes {
		codes = append(codes, c.Code)
	}
	joined := strings.Join(codes, ",")
	if !strings.Contains(joined, string(codeBindDataDirChown)) || !strings.Contains(joined, string(codePGDATADatadir)) {
		t.Errorf("the swap only starts alongside the PGDATA half; got %v", codes)
	}
	if !strings.Contains(overlay, "PGDATA") || !strings.Contains(overlay, "db-data") {
		t.Errorf("the overlay should carry both halves:\n%s", overlay)
	}
}

// The bug this issue is about: when the PGDATA half cannot be written — here
// because something is mounted below the data directory — the swap on its own
// leaves a project that does not start (pg18-named-old-datadir.txt). Neither half
// goes in, and the overlay says so rather than falling silent.
func TestOverlayWritesNeitherHalfAndSaysSoWhenPGDATACannotBeWritten(t *testing.T) {
	body := `services:
  db:
    image: postgres:18-alpine
    volumes:
      - ./data:/var/lib/postgresql/data
      - ./wal:/var/lib/postgresql/data/pg_wal
`
	overlay, changes := planWithImage(t, body, "../../testdata/image-inspect/postgres18.json")
	for _, c := range changes {
		if c.Code == string(codeBindDataDirChown) {
			t.Errorf("half the fix does not start; it should not be written: %s", c.Summary)
		}
	}
	var noted bool
	for _, c := range changes {
		if c.Code == string(codeDataDirNotThisMount) && c.Kind == "note" {
			noted = true
		}
	}
	if !noted {
		t.Errorf("the overlay is where a reader meets what opossum could not fix; got %v", changes)
	}
	if strings.Contains(overlay, "db-data") {
		t.Errorf("the overlay swapped in a named volume without the half that makes it work:\n%s", overlay)
	}
}

func TestOverlayStillFixesPostgres17WhereTheImageDoesChown(t *testing.T) {
	body := strings.Replace(pgBindOldPath, "postgres:18-alpine", "postgres:17-alpine", 1)
	overlay, changes := planWithImage(t, body, "../../testdata/image-inspect/postgres17.json")
	var codes []string
	for _, c := range changes {
		codes = append(codes, c.Code)
	}
	if !strings.Contains(strings.Join(codes, ","), string(codeBindDataDirChown)) {
		t.Errorf("17 chowns that directory, so the swap is still the fix; got %v", codes)
	}
	if !strings.Contains(overlay, "db-data") {
		t.Errorf("the overlay should carry the named volume:\n%s", overlay)
	}
}

// The image is normally not here yet when the overlay is written. Then the answer
// is the one that has always been written — and the comment says it was assumed.
func TestOverlaySaysWhenItCouldNotAskTheImage(t *testing.T) {
	overlay, changes := planWithImage(t, pgBindOldPath, "")
	if len(changes) == 0 {
		t.Fatal("with no image to ask, the fix that has always been written should still be written")
	}
	if !strings.Contains(overlay, "was not here to be asked") {
		t.Errorf("the overlay should say the path was assumed, not read:\n%s", overlay)
	}
	if strings.Contains(overlay, "the one this image declares: PGDATA=") {
		t.Errorf("nothing was read, so the overlay should not claim it was:\n%s", overlay)
	}
}

func TestOverlaySaysWhenItDidAskTheImage(t *testing.T) {
	body := strings.Replace(pgBindOldPath, "postgres:18-alpine", "postgres:17-alpine", 1)
	overlay, _ := planWithImage(t, body, "../../testdata/image-inspect/postgres17.json")
	if !strings.Contains(overlay, "the one this image declares: PGDATA=") {
		t.Errorf("the path was read from the image; the overlay should say so:\n%s", overlay)
	}
	if strings.Contains(overlay, "was not here to be asked") {
		t.Errorf("the image answered, so the overlay should not say otherwise:\n%s", overlay)
	}
}

// "The image told us nothing" is not "we could not ask it", and the overlay has to
// say which. The fixture is an image built for this (testdata/image-inspect/
// README.md); whether an image in the wild declares no PGDATA is not something
// measured here, but the two answers have to read differently either way.
func TestOverlaySaysWhenTheImageDeclaresNothing(t *testing.T) {
	body := strings.Replace(pgBindOldPath, "postgres:18-alpine", "postgres:b480-nodecl", 1)
	overlay, changes := planWithImage(t, body, "../../testdata/image-inspect/postgres-declares-no-pgdata.json")
	if len(changes) == 0 {
		t.Fatal("an image that declares nothing gets the fix that has always been written")
	}
	if !strings.Contains(overlay, "declares no PGDATA") {
		t.Errorf("the overlay should say the image declares none:\n%s", overlay)
	}
	for _, wrong := range []string{"was not here to be asked", "the one this image declares: PGDATA="} {
		if strings.Contains(overlay, wrong) {
			t.Errorf("the image answered and declared nothing; the overlay should not say %q:\n%s", wrong, overlay)
		}
	}
}

// A service that sets its own PGDATA overrides the image, so the question "where
// does the cluster go" is answered by the compose file, not the image. Reading only
// the image describes a different project than the one being adapted.
func TestOverlayFollowsThePGDATATheServiceSets(t *testing.T) {
	// 18, pointed back at the directory 17 used: the container will use this mount
	// and chown it, so it fails on a bind mount exactly as 17 does — and the swap is
	// the fix, not a note about a cluster kept elsewhere.
	atTheMount := `services:
  db:
    image: postgres:18-alpine
    environment:
      PGDATA: /var/lib/postgresql/data
    volumes:
      - ./data:/var/lib/postgresql/data
`
	overlay, changes := planWithImage(t, atTheMount, "../../testdata/image-inspect/postgres18.json")
	var codes []string
	for _, c := range changes {
		codes = append(codes, c.Code)
	}
	if strings.Contains(strings.Join(codes, ","), string(codeDataDirNotThisMount)) {
		t.Errorf("the service put the cluster in this mount; a note about it being elsewhere is false: %v", codes)
	}
	if !strings.Contains(overlay, "db-data") {
		t.Errorf("this is the shape the swap is for:\n%s", overlay)
	}

	// The other way: PGDATA below the mount is what docker-library tells people to
	// write, and the image then makes that subdirectory inside the mount and chowns
	// what it made — measured in
	// testdata/error-wordings/pg-image-declares-pgdata-below-the-mount.txt, which is
	// the same arrangement declared by the image instead of the service.
	below := `services:
  db:
    image: postgres:17-alpine
    environment:
      PGDATA: /var/lib/postgresql/data/pgdata
    volumes:
      - ./data:/var/lib/postgresql/data
`
	overlay, changes = planWithImage(t, below, "../../testdata/image-inspect/postgres17.json")
	for _, c := range changes {
		if c.Code == string(codeBindDataDirChown) {
			t.Errorf("the image initialises below this mount, so a bind mount works: %s", c.Summary)
		}
	}
	if strings.Contains(overlay, "db-data") {
		t.Errorf("the overlay moved a mount that works:\n%s", overlay)
	}
}

// The difference between "the image chowns this mount" and "it keeps its cluster
// elsewhere" only shows when the PGDATA half cannot be written. With 17 — which
// chowns the mount — that shape still gets the swap, and must never get the note
// written for the other one.
func TestOverlayStillSwapsFor17WhenThePGDATAHalfCannotBeWritten(t *testing.T) {
	body := `services:
  db:
    image: postgres:17-alpine
    volumes:
      - ./data:/var/lib/postgresql/data
      - ./wal:/var/lib/postgresql/data/pg_wal
`
	overlay, changes := planWithImage(t, body, "../../testdata/image-inspect/postgres17.json")
	var swapped, noted bool
	for _, c := range changes {
		switch c.Code {
		case string(codeBindDataDirChown):
			swapped = true
		case string(codeDataDirNotThisMount):
			noted = true
		}
	}
	if !swapped {
		t.Errorf("17 chowns this mount; the swap is the fix with or without the PGDATA half: %v", changes)
	}
	if noted {
		t.Errorf("17 keeps its cluster in this very directory; a note saying otherwise is false:\n%s", overlay)
	}
}

// A sibling directory is somewhere else, not below: /var/lib/postgresql/datax is
// not inside /var/lib/postgresql/data, and treating it as below would leave a mount
// the image will not use with no fix and no note.
func TestOverlayTreatsASiblingDirectoryAsSomewhereElse(t *testing.T) {
	body := `services:
  db:
    image: postgres:17-alpine
    environment:
      PGDATA: /var/lib/postgresql/datax
    volumes:
      - ./data:/var/lib/postgresql/data
`
	overlay, changes := planWithImage(t, body, "../../testdata/image-inspect/postgres17.json")
	// The service set PGDATA, so opossum cannot write the half that would point it
	// back into a named volume — which leaves the note, not silence, and not a swap
	// that would not help. The "below" shape, by contrast, gets neither.
	var noted, swapped bool
	for _, c := range changes {
		switch c.Code {
		case string(codeDataDirNotThisMount):
			noted = true
		case string(codeBindDataDirChown):
			swapped = true
		}
	}
	if swapped {
		t.Errorf("the cluster is not in this mount, so moving it alone changes nothing: %v", changes)
	}
	if !noted {
		t.Errorf("a sibling directory is somewhere else; that is worth writing down: %v", changes)
	}
	// And the note names who decided, since here it was not the image.
	if !strings.Contains(overlay, "the one this service sets: PGDATA=") {
		t.Errorf("the service set this path, not the image:\n%s", overlay)
	}
}

// Asking the runtime is a process each time, and planning an overlay considers
// every mount of every service. Only a Postgres image with something mounted at its
// data directory is worth asking about, and only once.
func TestTheOverlayAsksAboutAnImageOnceAndOnlyWhenItMatters(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	shim := filepath.Join(dir, "c")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\ncase \"$1 $2\" in\n\"image inspect\") echo '[]' ;;\nesac\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `services:
  api:
    image: myorg/api:1.2
  # A database that is not Postgres, with something at its data directory: it
  # gets adapted, but it must not be asked what its data directory is. Nothing
  # below can tell this apart from any other non-Postgres image — the count is
  # the same either way — so it is here to keep the fixture meaning what it says.
  cache:
    image: mongo:7
    volumes:
      - ./r:/data/db
  db:
    image: postgres:17-alpine
    volumes:
      - ./data:/var/lib/postgresql/data
      - ./more:/var/lib/postgresql/data/extra
  reports:
    image: postgres:17-alpine
    volumes:
      - ./reports:/var/lib/postgresql/data
  proxy:
    image: nginx:alpine
`
	o := New(loadProject(t, body), &runtime.Runtime{Bin: shim}, "opossum", io.Discard)
	o.PlanOverlay()
	asked := 0
	if b, err := os.ReadFile(log); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "image inspect") {
				asked++
				if !strings.Contains(line, "postgres") {
					t.Errorf("nothing to ask about %q — it is not a Postgres image", line)
				}
			}
		}
	}
	// Two services on the same image, and a mount below the data directory that is
	// considered and passed over: one question, not four.
	if asked != 1 {
		t.Errorf("one image between them, asked %d times", asked)
	}
}

// Every sentence the overlay writes about the data directory comes from one place,
// so the applied comment cannot say the image decided something the service did —
// which is what it said for three rounds while only the note was being fixed.
func TestTheAppliedCommentAlsoNamesWhoDecided(t *testing.T) {
	// The service decided. Measured: this shape fails on a bind mount exactly as 17
	// does (testdata/error-wordings/pg18-service-pgdata-at-the-mount.txt), so the
	// swap is written — with a comment that says who put the cluster here.
	body := `services:
  db:
    image: postgres:18-alpine
    environment:
      PGDATA: /var/lib/postgresql/data
    volumes:
      - ./data:/var/lib/postgresql/data
`
	overlay, _ := planWithImage(t, body, "../../testdata/image-inspect/postgres18.json")
	if !strings.Contains(overlay, "the one this service sets: PGDATA=") {
		t.Errorf("the service set PGDATA; the comment should say so:\n%s", overlay)
	}
	if strings.Contains(overlay, "the one this image declares: PGDATA=") {
		t.Errorf("the image declares somewhere else entirely:\n%s", overlay)
	}

	// And with no image to ask, a service that sets PGDATA is still the one who
	// decided — the comment must not fall back to "the path the images used".
	overlay, _ = planWithImage(t, body, "")
	if !strings.Contains(overlay, "the one this service sets: PGDATA=") {
		t.Errorf("nothing was read, but the service still decided:\n%s", overlay)
	}
	if strings.Contains(overlay, "was not here to be asked") {
		t.Errorf("where the cluster goes was not in doubt:\n%s", overlay)
	}
}

// PGDATA passed in from the environment (`environment: [PGDATA]`) sets it without
// a value opossum can read. The image's path is the best guess left, and the
// overlay says that rather than claiming either side declared it.
func TestTheOverlaySaysWhenPGDATAComesFromTheEnvironment(t *testing.T) {
	body := `services:
  db:
    image: postgres:18-alpine
    environment:
      - PGDATA
    volumes:
      - ./data:/var/lib/postgresql/data
`
	overlay, changes := planWithImage(t, body, "../../testdata/image-inspect/postgres18.json")
	if len(changes) == 0 {
		t.Fatal("the mount is still worth writing about")
	}
	if !strings.Contains(overlay, "passes PGDATA in from the environment") {
		t.Errorf("the overlay should say the value could not be read:\n%s", overlay)
	}
	for _, wrong := range []string{"the one this service sets: PGDATA=", "the one this image declares: PGDATA="} {
		if strings.Contains(overlay, wrong) {
			t.Errorf("nobody's declaration is known here; %q overstates it:\n%s", wrong, overlay)
		}
	}
}

// A trailing slash is the same directory. Without the trimming, the same project
// falls into "somewhere else" and gets a note instead of the fix.
func TestOverlayReadsATrailingSlashAsTheSameDirectory(t *testing.T) {
	// From the image, too: it declares the same directory with a slash on the end,
	// and reading that as somewhere else would leave the mount unfixed.
	fromImage := `services:
  db:
    image: postgres:b480-slash
    volumes:
      - ./data:/var/lib/postgresql/data
`
	_, changes := planWithImage(t, fromImage, "../../testdata/image-inspect/postgres-pgdata-with-trailing-slash.json")
	var swappedByImage bool
	for _, c := range changes {
		if c.Code == string(codeBindDataDirChown) {
			swappedByImage = true
		}
		if c.Code == string(codeDataDirNotThisMount) {
			t.Errorf("the image names this very directory, slash and all: %s", c.Summary)
		}
	}
	if !swappedByImage {
		t.Errorf("a trailing slash does not make it another directory: %v", changes)
	}

	body := `services:
  db:
    image: postgres:17-alpine
    environment:
      PGDATA: /var/lib/postgresql/data/
    volumes:
      - ./data:/var/lib/postgresql/data
`
	_, changes = planWithImage(t, body, "../../testdata/image-inspect/postgres17.json")
	var swapped bool
	for _, c := range changes {
		if c.Code == string(codeBindDataDirChown) {
			swapped = true
		}
		if c.Code == string(codeDataDirNotThisMount) {
			t.Errorf("the cluster is in this very directory: %s", c.Summary)
		}
	}
	if !swapped {
		t.Errorf("this is the shape the swap is for: %v", changes)
	}
}

// The summary line — the one that reaches the terminal — attributes the path too,
// and it is written by a different method than the comment body. Both are read
// here, because the whole point of folding the decision into one value was that
// these two cannot disagree.
func TestTheSummaryLineNamesWhoDecidedToo(t *testing.T) {
	byService := `services:
  db:
    image: postgres:17-alpine
    environment:
      PGDATA: /var/lib/postgresql/datax
    volumes:
      - ./data:/var/lib/postgresql/data
`
	_, changes := planWithImage(t, byService, "../../testdata/image-inspect/postgres17.json")
	var summary string
	for _, c := range changes {
		if c.Code == string(codeDataDirNotThisMount) {
			summary = c.Summary
		}
	}
	if !strings.Contains(summary, "this service points PGDATA at") {
		t.Errorf("the service put the cluster there; the line should say so, got: %q", summary)
	}

	byImage := `services:
  db:
    image: postgres:18-alpine
    volumes:
      - ./data:/var/lib/postgresql/data
      - ./wal:/var/lib/postgresql/data/pg_wal
`
	_, changes = planWithImage(t, byImage, "../../testdata/image-inspect/postgres18.json")
	summary = ""
	for _, c := range changes {
		if c.Code == string(codeDataDirNotThisMount) {
			summary = c.Summary
		}
	}
	if !strings.Contains(summary, "this image keeps its cluster in") {
		t.Errorf("the image decided this one, got: %q", summary)
	}
	if strings.Contains(summary, "service points") {
		t.Errorf("the service said nothing about PGDATA here, got: %q", summary)
	}

	// And the third: PGDATA came in from the environment, so neither side declared
	// the path opossum is naming. The line says the image declares it — because
	// that is where the fallback came from — and must not credit the service with a
	// value nobody could read.
	fromEnv := `services:
  db:
    image: postgres:18-alpine
    environment:
      - PGDATA
    volumes:
      - ./data:/var/lib/postgresql/data
`
	_, changes = planWithImage(t, fromEnv, "../../testdata/image-inspect/postgres18.json")
	summary = ""
	for _, c := range changes {
		if c.Code == string(codeDataDirNotThisMount) {
			summary = c.Summary
		}
	}
	// Nobody read a path here, so the line names neither side as having declared
	// one — it says where PGDATA comes from and stops. This is the line most readers
	// see, since a note-only overlay is not written at all.
	if !strings.Contains(summary, "PGDATA comes in from the environment") {
		t.Errorf("the line should say where PGDATA comes from, got: %q", summary)
	}
	for _, wrong := range []string{"this image declares", "service points PGDATA at", "instead"} {
		if strings.Contains(summary, wrong) {
			t.Errorf("%q asserts a path nobody read, got: %q", wrong, summary)
		}
	}

	// And the body must not assert the destination — nor the ending, which depends
	// on whether the effective value happens to be this mount.
	overlay, _ := planWithImage(t, fromEnv, "../../testdata/image-inspect/postgres18.json")
	if !strings.Contains(overlay, "cannot be read here") {
		t.Errorf("where the cluster goes was not readable; the note should say so:\n%s", overlay)
	}
	for _, wrong := range []string{"the cluster goes to", "will not start", "starts and leaves"} {
		if strings.Contains(overlay, wrong) {
			t.Errorf("%q assumes a destination nobody could read:\n%s", wrong, overlay)
		}
	}
	// The Why block says where the cluster lands cannot be read, rather than saying
	// it lands somewhere — the note is the same in every shape now, and this is the
	// one line that differs with it.
	if !strings.Contains(overlay, "cannot be read here either") {
		t.Errorf("nobody read where the cluster goes; the note should say that:\n%s", overlay)
	}
	if strings.Contains(overlay, "does not land in") {
		t.Errorf("that states what nobody could read:\n%s", overlay)
	}
	// And the reason names how PGDATA got here, since the service did not set a
	// value anyone can read.
	if !strings.Contains(overlay, "this service passes PGDATA in from the environment") {
		t.Errorf("the reason should say where PGDATA comes from:\n%s", overlay)
	}
	if strings.Contains(overlay, "this service sets its own PGDATA") {
		t.Errorf("it set no value opossum could read:\n%s", overlay)
	}
	if strings.Contains(overlay, "That is not /var/lib/postgresql/data, so the container will not use") {
		t.Errorf("that states what nobody could read:\n%s", overlay)
	}
}

// The wording for a PGDATA that came in from the environment still has to say
// where the fallback path came from: asked and told, asked and told nothing, or
// never asked. "What the image declares" is a claim about a question that may not
// have been put.
func TestTheEnvironmentPGDATAWordingSaysWhereTheFallbackCameFrom(t *testing.T) {
	body := `services:
  db:
    image: postgres:18-alpine
    environment:
      - PGDATA
    volumes:
      - ./data:/var/lib/postgresql/data
`
	for _, tc := range []struct{ fixture, want, wrong string }{
		{"../../testdata/image-inspect/postgres18.json", "is what the image declares", "not here to be asked"},
		{"../../testdata/image-inspect/postgres-declares-no-pgdata.json", "the image declares none", "is what the image declares"},
		{"", "was not here to be asked", "is what the image declares"},
	} {
		overlay, _ := planWithImage(t, body, tc.fixture)
		if !strings.Contains(overlay, tc.want) {
			t.Errorf("fixture %q: the overlay should say %q:\n%s", tc.fixture, tc.want, overlay)
		}
		if strings.Contains(overlay, tc.wrong) {
			t.Errorf("fixture %q: %q is not true here:\n%s", tc.fixture, tc.wrong, overlay)
		}
	}
}

// The note sends its reader to OPSM-111's entry in AGENTS.md for what this costs
// and what to change. That entry has to actually say both — the note claimed it
// did for a round while it said neither, and nothing noticed.
func TestTheEntryTheNoteSendsReadersToSaysWhatItPromises(t *testing.T) {
	b, err := os.ReadFile("../../AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	i := strings.Index(doc, "**`["+string(codeDataDirNotThisMount)+"]`")
	if i < 0 {
		t.Fatalf("%s has no entry in AGENTS.md, and the note points at one", codeDataDirNotThisMount)
	}
	entry := doc[i:]
	if j := strings.Index(entry, "\n- **`["); j > 0 {
		entry = entry[:j]
	}
	// The entry is hand-wrapped prose, so a phrase can sit across two lines. Match
	// on the words, not on where the wrapping happens to fall — a reflow is not a
	// change of meaning, and this test went red for one once.
	flat := strings.Join(strings.Fields(entry), " ")
	// Each row is a promise the note makes on the entry's behalf. Each is pinned to
	// a small set of wordings rather than one — not to meaning, which nothing here
	// can check: a paraphrase outside the set will turn this red, and that is the
	// price of catching an entry that quietly stops saying the thing.
	// Every capture the entry names has to be on disk: the entry is only worth
	// reading because each line points at a run, and a name that no longer resolves
	// is a line with nothing behind it.
	named := regexp.MustCompile(`\x60(pg[a-z0-9-]+\.txt)\x60`).FindAllStringSubmatch(entry, -1)
	for _, line := range strings.Split(entry, "\n  - ")[1:] {
		// Each bullet ends where the next one or the closing paragraph begins;
		// without the cut, a bullet borrows the capture named after the list.
		if k := strings.Index(line, "\n\n"); k > 0 {
			line = line[:k]
		}
		runs := regexp.MustCompile("`([a-z0-9-]+\\.txt)`").FindAllStringSubmatch(line, -1)
		if len(runs) == 0 {
			t.Errorf("this shape points at no run, and the entry says each one does:\n  - %s", line)
			continue
		}
		for _, m := range runs {
			if _, err := os.Stat(filepath.Join("..", "..", "testdata", "error-wordings", m[1])); err != nil {
				t.Errorf("this shape points at %s, which is not in testdata/error-wordings/", m[1])
			}
		}
	}
	for _, m := range named {
		if _, err := os.Stat(filepath.Join("..", "..", "testdata", "error-wordings", m[1])); err != nil {
			t.Errorf("the entry points at %s, which is not in testdata/error-wordings/", m[1])
		}
	}
	// Each shape row is matched inside its own bullet, cut at the paragraph that
	// follows the list. Reading the whole entry at once let the consequence move to
	// the neighbouring line and pass.
	bullets := map[string]string{}
	for _, b := range strings.Split(entry, "\n  - ")[1:] {
		if k := strings.Index(b, "\n\n"); k > 0 {
			b = b[:k]
		}
		bullets[strings.Join(strings.Fields(b), " ")] = strings.Join(strings.Fields(b), " ")
	}
	inBullet := func(heading string) string {
		for k := range bullets {
			if strings.Contains(k, heading) {
				return k
			}
		}
		return ""
	}
	for _, want := range []struct {
		what  string
		anyOf []string
		allOf []string
		in    string // the bullet whose heading this row is about; "" = the whole entry
	}{
		// One row per shape, so a line cannot go missing and a version cannot be
		// paired with the wrong ending — both happened while this entry was being
		// written.
		{what: "the shape that works: below the mount", in: "The cluster lands *below* the mount",
			anyOf: []string{"a bind mount works", "nothing is wrong"}},
		{what: "18 paired with refusing to start", in: "on 18 or later",
			anyOf: []string{"18 and later refuse", "will not start: 18"}},
		{what: "17 paired with an empty mount", in: "on 17 or earlier",
			anyOf: []string{"the container starts", "17 and earlier start"}},
		{what: "18's ending", anyOf: []string{"refuse to start", "will not start", "refuses to start"}},
		{what: "17's ending", allOf: []string{"empty"}, anyOf: []string{"leave", "leaves", "leaving"}},
		{what: "what to change", allOf: []string{"Make the two agree"},
			anyOf: []string{"point `PGDATA` at a subdirectory"}},
		{what: "the other way out", allOf: []string{"Make the two agree"},
			anyOf: []string{"move the mount so that the cluster", "lands below it"}},
		{what: "the shape that fails: at the mount", in: "The cluster lands *at* the mount",
			anyOf: []string{"so it fails with `[" + string(codeBindDataDirChown) + "]`"}},
		// The warning that keeps a reader off that shape. Reversing it left the entry
		// contradicting itself and nothing went red.
		{what: "the warning against mounting the cluster's own directory",
			allOf: []string{"is not one of them"}, anyOf: []string{"and it fails"}},
		// And which run each way out was measured in — this attribution was wrong
		// once, and putting it back the wrong way went unnoticed.
		// The pair, not the two tokens: swapping which version each way out was
		// measured on left both tokens in place and passed.
		{what: "the first way out, measured on 17", allOf: []string{"the first line above, on 17"}},
		{what: "the second way out, measured on 18", allOf: []string{"`pg18-mount-one-level-up.txt`, on 18"}},
		{what: "the unreadable case the note sends here", allOf: []string{"environment"},
			anyOf: []string{"cannot read", "cannot be read", "could not be read"}},
	} {
		hay := flat
		if want.in != "" {
			if hay = inBullet(want.in); hay == "" {
				t.Errorf("the entry has no shape for %q, which %s is about", want.in, want.what)
				continue
			}
		}
		for _, must := range want.allOf {
			if !strings.Contains(hay, must) {
				t.Errorf("the %s entry should carry %s (missing %q):\n%s",
					codeDataDirNotThisMount, want.what, must, entry)
			}
		}
		found := len(want.anyOf) == 0
		for _, one := range want.anyOf {
			if strings.Contains(hay, one) {
				found = true
			}
		}
		if !found {
			t.Errorf("the %s entry should carry %s (none of %v):\n%s",
				codeDataDirNotThisMount, want.what, want.anyOf, entry)
		}
	}
}

// The note says the same thing in every shape: what was found, that opossum left
// it, and where the shapes are written down. Fourteen rounds of review found a
// defect in this note ten times, and every one was in a sentence that varied by
// branch — an ending predicted for the wrong version, an exemption merged with
// another, advice this repository had measured failing. What varies now lives in
// the code's entry in AGENTS.md, where each line names the run it comes from and a
// ratchet holds it there.
func TestTheNoteReadsTheSameInEveryShape(t *testing.T) {
	shapes := []struct{ name, image, fixture, env string }{
		{"18, cluster elsewhere", "postgres:18-alpine", "../../testdata/image-inspect/postgres18.json", "      PGDATA: /srv/elsewhere"},
		{"17, cluster elsewhere", "postgres:17-alpine", "../../testdata/image-inspect/postgres17.json", "      PGDATA: /srv/elsewhere"},
		{"cluster outside the tree", "postgres:b480-elsewhere", "../../testdata/image-inspect/postgres-cluster-outside-the-usual-tree.json", "      PGDATA: /srv/elsewhere"},
		{"version not a number", "postgres:b480-96", "../../testdata/image-inspect/postgres-major-not-a-number.json", "      PGDATA: /srv/elsewhere"},
		{"image not here", "postgres:17-alpine", "", "      PGDATA: /srv/elsewhere"},
	}
	var first string
	for _, sh := range shapes {
		body := fmt.Sprintf(`services:
  db:
    image: %s
    environment:
%s
    volumes:
      - ./data:/var/lib/postgresql/data
`, sh.image, sh.env)
		overlay, _ := planWithImage(t, body, sh.fixture)
		note := overlay[strings.Index(overlay, "[opossum note]"):]
		// The one thing that legitimately differs is the path each shape names.
		note = strings.ReplaceAll(note, "/srv/elsewhere", "<path>")
		if first == "" {
			first = note
			continue
		}
		if note != first {
			t.Errorf("%s reads differently:\n%s\n--- first shape:\n%s", sh.name, note, first)
		}
	}
	for _, want := range []string{
		"does not land in /var/lib/postgresql/data",
		"opossum left it alone",
		string(codeDataDirNotThisMount) + " in AGENTS.md has the shapes",
	} {
		if !strings.Contains(first, want) {
			t.Errorf("the note should say %q:\n%s", want, first)
		}
	}
}

// The note's body, in both forms, word for word. Fifteen rounds found a defect in
// this note eleven times, and the forbidden-word lists only ever caught the
// wordings someone had already thought of: a sentence broke in half for a round,
// and advice this repository measured failing would still pass a list. There are
// only two forms now, so both are written out — changing either is a decision, made
// with the captures in hand, rather than something that can drift a clause at a
// time.
func TestTheNoteIsWordForWordWhatWeMeanToSay(t *testing.T) {
	readable := `services:
  db:
    image: postgres:17-alpine
    environment:
      PGDATA: /srv/elsewhere
    volumes:
      - ./data:/var/lib/postgresql/data
`
	overlay, changes := planWithImage(t, readable, "../../testdata/image-inspect/postgres17.json")
	wantReadable := strings.Join([]string{
		`# ── Notes ───────────────────────────────────────────────────────────────`,
		`# These are things opossum writes no YAML for.`,
		`# [opossum note] service "db": /var/lib/postgresql/data left as a bind mount.`,
		`# Why: The data directory is the one this service sets: PGDATA=/srv/elsewhere.`,
		`#   The cluster this service will write does not land in /var/lib/postgresql/data.`,
		`#   opossum left it alone: moving it to a named volume only helps together with`,
		`#   a PGDATA pointing back into it, and that could not be written here (this service sets its own PGDATA).`,
		`# What to expect: OPSM-111 in AGENTS.md has the shapes this comes in, what each costs,`,
		`#   and what to change.`,
	}, "\n")
	// The summary line is what a reader gets when the overlay is not written at all
	// (#491), so it is pinned as tightly as the body: a word-for-word body next to a
	// summary held by a keyword list is the wrong way round.
	if got := summaryOf(t, changes); got != `service "db": /var/lib/postgresql/data is a bind mount, and this service points PGDATA at /srv/elsewhere instead — left as it is` {
		t.Errorf("the summary line is not what this file says it should be:\n%s", got)
	}
	if got := noteBlockOf(t, overlay); got != wantReadable {
		t.Errorf("the note is not what this file says it should be\n got:\n%s\nwant:\n%s", got, wantReadable)
	}

	// The environment form needs an image whose own PGDATA is not this mount —
	// otherwise the cluster lands here and the swap is written instead of a note.
	fromEnv := strings.Replace(readable, "      PGDATA: /srv/elsewhere", "      - PGDATA", 1)
	fromEnv = strings.Replace(fromEnv, "postgres:17-alpine", "postgres:18-alpine", 1)
	overlay, changes = planWithImage(t, fromEnv, "../../testdata/image-inspect/postgres18.json")
	wantFromEnv := strings.Join([]string{
		`# ── Notes ───────────────────────────────────────────────────────────────`,
		`# These are things opossum writes no YAML for.`,
		`# [opossum note] service "db": /var/lib/postgresql/data left as a bind mount.`,
		`# Why: This service passes PGDATA in from the environment, so where its cluster`,
		`#   goes cannot be read here; /var/lib/postgresql/18/docker is what the image declares.`,
		`#   Whether it lands in this mount therefore cannot be read here either.`,
		`#   opossum left it alone: moving it to a named volume only helps together with`,
		`#   a PGDATA pointing back into it, and that could not be written here (this service passes PGDATA in from the environment).`,
		`# What to expect: OPSM-111 in AGENTS.md has the shapes this comes in, what each costs,`,
		`#   and what to change.`,
	}, "\n")
	if got := summaryOf(t, changes); got != `service "db": /var/lib/postgresql/data is a bind mount, and PGDATA comes in from the environment — left as it is` {
		t.Errorf("the environment summary line is not what this file says it should be:\n%s", got)
	}
	if got := noteBlockOf(t, overlay); got != wantFromEnv {
		t.Errorf("the environment form is not what this file says it should be\n got:\n%s\nwant:\n%s", got, wantFromEnv)
	}
}

// summaryOf returns the one line a reader gets on the terminal for the note.
func summaryOf(t *testing.T, changes []Adaptation) string {
	t.Helper()
	for _, c := range changes {
		if c.Code == string(codeDataDirNotThisMount) {
			return c.Summary
		}
	}
	t.Fatal("no note among the changes")
	return ""
}

// noteBlockOf returns the note's comment block, whole. Comparing a prefix would
// leave the end of the note free — a sentence appended after the pinned lines
// passed, and it was advice this repository measured failing.
func noteBlockOf(t *testing.T, overlay string) string {
	t.Helper()
	// From the section header, not from the marker: a line added just above the
	// note reaches the same reader, and one was — advice this repository measured
	// failing, sitting one line outside what the golden covered.
	i := strings.Index(overlay, "# ── Notes")
	if i < 0 {
		i = strings.Index(overlay, "# [opossum note]")
	}
	if i < 0 {
		t.Fatalf("no note in this overlay:\n%s", overlay)
	}
	var out []string
	for _, line := range strings.Split(overlay[i:], "\n") {
		if !strings.HasPrefix(line, "#") {
			break
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// What these checks do not close, so that a green run is not read as more than it
// is. Eighteen rounds of review went into this file; each one shut a door and found
// the next one open, and these are the ones still open when it was landed:
//
//   - Lines outside the note. The body and the summary are pinned word for word,
//     and the block is taken from the section heading down — but the overlay's own
//     header, and anything after the first non-comment line, are not. A sentence
//     placed there reaches the same reader. Closing this needs the whole overlay
//     pinned, which would make every unrelated change to that file a failure here.
//   - What the entry in AGENTS.md says, beyond the phrases below. The rows check
//     that each shape names a consequence and a run that exists; they cannot tell
//     whether the sentence around them is true, and a contradiction added beside a
//     pinned phrase leaves both in place.
//   - Whether the runs named in the entry show what the entry says they show. They
//     are read by people; nothing here opens them.
//   - Whether the reader sees the body at all. A project whose only finding is a
//     note gets no overlay file (#491) — the summary line is all that arrives, and
//     that is why it is pinned here too.
//   - Wording, not meaning. Several rows hold one phrasing each; a rewrite that
//     says the same thing turns them red, and that is the price of catching one
//     that quietly says something else.

// The note carries no advice of its own. Nine rounds of review put a fix in it
// four times, and each one was either unmeasured or contradicted by a capture in
// this very directory — pointing PGDATA at a mount the image chowns
// (pg17-bind-old-datadir.txt), a named volume at a path no capture covers, an
// OPSM-110 that names one directory and nothing else. What to change lives in that
// code's entry, where it can say "it depends on the image" without a note having to
// pick one.
func TestTheNoteCarriesNoAdviceOfItsOwn(t *testing.T) {
	for _, tc := range []struct{ image, fixture, env string }{
		{"postgres:18-alpine", "../../testdata/image-inspect/postgres18.json", "      PGDATA: /srv/elsewhere"},
		{"postgres:17-alpine", "../../testdata/image-inspect/postgres17.json", "      PGDATA: /srv/elsewhere"},
		{"postgres:b480-elsewhere", "../../testdata/image-inspect/postgres-cluster-outside-the-usual-tree.json", "      PGDATA: /srv/elsewhere"},
		{"postgres:17-alpine", "", "      PGDATA: /srv/elsewhere"},
		// The branch this file kept forgetting: PGDATA passed in from the
		// environment, where the note has its own wording and had its own advice
		// creep back twice.
		{"postgres:18-alpine", "../../testdata/image-inspect/postgres18.json", "      - PGDATA"},
	} {
		body := fmt.Sprintf(`services:
  db:
    image: %s
    environment:
%s
    volumes:
      - ./data:/var/lib/postgresql/data
`, tc.image, tc.env)
		overlay, _ := planWithImage(t, body, tc.fixture)
		if !strings.Contains(overlay, string(codeDataDirNotThisMount)+" in AGENTS.md") {
			t.Errorf("%s: the note should send the reader to the code's entry:\n%s", tc.image, overlay)
		}
		for _, advice := range []string{
			"Point PGDATA",
			"named volume for the directory",
			"cluster does land in a named volume",
			string(codePGVersionedLayout) + " names the directory",
			"a host path works there",
		} {
			if strings.Contains(overlay, advice) {
				t.Errorf("%s: %q is advice this repository has not measured for every image:\n%s",
					tc.image, advice, overlay)
			}
		}
	}
}

// What an image declares goes into a comment block, so it cannot be allowed to
// leave the line it is written on. The value here is made up — no real image
// declares a newline — but the handling is what is being measured.
func TestAValueFromAnImageStaysOnItsLine(t *testing.T) {
	// The value comes from the image and is written into a comment block.
	doc := `[{"variants":[{"config":{"config":{"Env":["PGDATA=/srv/elsewhere\nvolumes:\n  evil: {}","PG_MAJOR=18\nvolumes:\n  evil: {}"]}}}]}]`
	assertNothingEscapesTheComment(t, doc, `services:
  db:
    image: postgres:whatever
    volumes:
      - ./data:/var/lib/postgresql/data
      - ./wal:/var/lib/postgresql/data/pg_wal
`)
}

// The service's own PGDATA wins over the image's, so it reaches the same comment
// block by a different road. Sanitising only what the image says leaves the road
// that is actually taken.
func TestAValueFromTheComposeFileStaysOnItsLineToo(t *testing.T) {
	assertNothingEscapesTheComment(t, `[{"variants":[{"config":{"config":{"Env":["PG_MAJOR=18"]}}}]}]`, `services:
  db:
    image: postgres:whatever
    environment:
      PGDATA: "/srv/x\nvolumes:\n  evil: {}"
    volumes:
      - ./data:/var/lib/postgresql/data
      - ./wal:/var/lib/postgresql/data/pg_wal
`)
}

// assertNothingEscapesTheComment plans an overlay with the given `image inspect`
// answer and compose file, and fails if anything from either lands outside a
// comment line.
func assertNothingEscapesTheComment(t *testing.T, inspectJSON, compose string) {
	t.Helper()
	shim := filepath.Join(t.TempDir(), "c")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\ncat <<'JSON'\n"+inspectJSON+"\nJSON\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	o := New(loadProject(t, compose), &runtime.Runtime{Bin: shim}, "opossum", io.Discard)
	overlay, _ := o.PlanOverlay()
	for _, line := range strings.Split(overlay, "\n") {
		if strings.Contains(line, "evil") && !strings.HasPrefix(strings.TrimSpace(line), "#") {
			t.Errorf("a value left its comment line: %q\n%s", line, overlay)
		}
	}
}

// A mount at the directory 18 does want is left alone — and this one is decided
// before the image is asked at all, because that path is not in the table of data
// directories. Kept as a boundary: it is the shape a user is told to move to, and
// the overlay must not then move it back.
func TestOverlayLeavesPostgres18AloneAtThePathItDoesUse(t *testing.T) {
	body := `services:
  db:
    image: postgres:18-alpine
    environment:
      POSTGRES_PASSWORD: x
    volumes:
      - ./data:/var/lib/postgresql
`
	overlay, changes := planWithImage(t, body, "../../testdata/image-inspect/postgres18.json")
	for _, c := range changes {
		t.Errorf("a host path works where 18 keeps its cluster; nothing to change: [%s] %s", c.Code, c.Summary)
	}
	if strings.Contains(overlay, "db-data") {
		t.Errorf("the overlay moved a mount that works:\n%s", overlay)
	}
}

// An image can also declare its data directory *below* the mount, which is what
// the PGDATA fix itself writes and what 18 does by default. Then the image makes
// that subdirectory inside the mount and chowns what it made, so a bind mount is
// fine and swapping it moves the data for nothing. The fixture is a real image
// built for this (FROM postgres:17-alpine with PGDATA set below the data dir);
// judging by "at or below" instead of "the very directory" swaps here.
func TestOverlayLeavesAMountAloneWhenTheImageInitialisesBelowIt(t *testing.T) {
	body := `services:
  db:
    image: postgres:b480-below
    volumes:
      - ./data:/var/lib/postgresql/data
`
	overlay, changes := planWithImage(t, body, "../../testdata/image-inspect/postgres-pgdata-below-datadir.json")
	for _, c := range changes {
		t.Errorf("the image initialises below this mount, so it chowns what it made: [%s] %s", c.Code, c.Summary)
	}
	if strings.Contains(overlay, "db-data") {
		t.Errorf("the overlay moved a mount that works:\n%s", overlay)
	}
}
