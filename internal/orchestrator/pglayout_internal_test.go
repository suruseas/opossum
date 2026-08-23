package orchestrator

// Evals for #485: Postgres 18 moved where the image keeps its data — one mount at
// /var/lib/postgresql, with the cluster in a major-version subdirectory below it.
// A project that mounts the old /var/lib/postgresql/data starts, prints what it
// found, and exits; before this the crash report carried no hint at all.
//
// The log text below is what the real runtime produced: `postgres:18-alpine`
// (sha256:9a8afca5…) under `container` 1.2.2 on 2026-08-23, with `./data` bound at
// the old data directory. It is kept verbatim, including the wrapping, because the
// whole question is which words survive to the reader.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// pg18CrashTail is what the crash report actually passed to the decoder: the last
// lines of the container's log, which is where the `Error:` opening has already
// scrolled out of view.
const pg18CrashTail = `format which is compatible with "pg_ctlcluster" (specifically, using
       major-version-specific directory names).  This better reflects how
       PostgreSQL itself works, and how upgrades are to be performed.
       See also https://github.com/docker-library/postgres/pull/1259
       Counter to that, there appears to be PostgreSQL data in:
         /var/lib/postgresql/data (unused mount/volume)
       This is usually the result of upgrading the Docker image without
       upgrading the underlying database using "pg_upgrade" (which requires both
       versions).
       The suggested container configuration for 18+ is to place a single mount
       at /var/lib/postgresql which will then place PostgreSQL data in a
       subdirectory, allowing usage of "pg_upgrade --link" without mount point
       boundary issues.
       See https://github.com/docker-library/postgres/issues/37 for a (long)
       discussion around this process, and suggestions for how to do so.`

// pg18FullMessage is the whole refusal, as `opossum logs` prints it.
const pg18FullMessage = `Error: in 18+, these Docker images are configured to store database data in a
       format which is compatible with "pg_ctlcluster" (specifically, using
       major-version-specific directory names).  This better reflects how
       PostgreSQL itself works, and how upgrades are to be performed.

       See also https://github.com/docker-library/postgres/pull/1259

       Counter to that, there appears to be PostgreSQL data in:
         /var/lib/postgresql/data (unused mount/volume)

       This is usually the result of upgrading the Docker image without
       upgrading the underlying database using "pg_upgrade" (which requires both
       versions).

       The suggested container configuration for 18+ is to place a single mount
       at /var/lib/postgresql which will then place PostgreSQL data in a
       subdirectory, allowing usage of "pg_upgrade --link" without mount point
       boundary issues.

       See https://github.com/docker-library/postgres/issues/37 for a (long)
       discussion around this process, and suggestions for how to do so.`

// pg18OldClusterTail is the other way 18 refuses (the run is kept in
// testdata/error-wordings/pg18-old-cluster-at-new-path.txt, together with a
// control showing the same build decoding the misplaced mount): the mount is already where 18
// wants it, and what it found there is a cluster an earlier major version wrote.
// Same runtime and image, a volume `postgres:17-alpine` had initialised. The words
// are close to the refusal above and the answer is not — moving the mount is not
// what this reader needs — so it is here to hold the decoder off it.
const pg18OldClusterTail = `format which is compatible with "pg_ctlcluster" (specifically, using
       major-version-specific directory names).  This better reflects how
       PostgreSQL itself works, and how upgrades are to be performed.
       See also https://github.com/docker-library/postgres/pull/1259
       Counter to that, there appears to be PostgreSQL data in:
         /var/lib/postgresql
       This is usually the result of upgrading the Docker image without
       upgrading the underlying database using "pg_upgrade" (which requires both
       versions).
       The suggested container configuration for 18+ is to place a single mount
       at /var/lib/postgresql which will then place PostgreSQL data in a
       subdirectory, allowing usage of "pg_upgrade --link" without mount point
       boundary issues.
       See https://github.com/docker-library/postgres/issues/37 for a (long)
       discussion around this process, and suggestions for how to do so.`

// lastLines keeps the final n non-empty lines. The crash report asks the runtime
// for `logs -n 15` and filters nothing itself, so the dropping of blank lines is
// the runtime's; this models what was observed in the capture below, where the 15
// lines that reached the report are the last 15 non-empty ones. The window is what
// matters here, and the anchors clear it under either reading.
func lastLines(s string, n int) string {
	var kept []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			kept = append(kept, l)
		}
	}
	if len(kept) > n {
		kept = kept[len(kept)-n:]
	}
	return strings.Join(kept, "\n")
}

// canonicalHint and namedVolumeHint are the sentences the decoder produced in two
// captured runs, copied here by hand. Copying is where this change went wrong more
// than once, so TestTheHintsHereAreTheOnesTheCapturedRunsProduced reads the files
// back: reversing the hint and both copies together used to leave everything green.
// The shapes with no mount to name are derived from canonicalHint below, so the
// wording of those is not a third copy.
//
// canonicalHint is from the control block at the end of
// control block is at the end of testdata/error-wordings/pg18-old-cluster-at-new-path.txt,
// for the service that file shows. The other shapes of the same message are
// derived from it below, so there is one place the wording lives.
const canonicalHint = "\n" +
	"  → [OPSM-110] Postgres 18 and newer keep the cluster in a major-version subdirectory, so the mount belongs one level up: mount `/var/lib/postgresql` instead of `/var/lib/postgresql/data` (this service mounts ./data there). A host path works there — the image creates the subdirectory itself. Data an earlier major version wrote is a separate question: the image asks for `pg_upgrade`, which moving the mount does not do."

// namedVolumeHint is from testdata/error-wordings/pg18-named-old-datadir.txt.
const namedVolumeHint = "\n" +
	"  → [OPSM-110] Postgres 18 and newer keep the cluster in a major-version subdirectory, so the mount belongs one level up: mount `/var/lib/postgresql` instead of `/var/lib/postgresql/data` (this service mounts pgdata there). A host path works there — the image creates the subdirectory itself. Data an earlier major version wrote is a separate question: the image asks for `pg_upgrade`, which moving the mount does not do."

func TestCrashHintPostgres18MountOneLevelUp(t *testing.T) {
	svc := &compose.Service{Volumes: []string{"./data:/var/lib/postgresql/data"}}
	h := hintFor(t, pg18CrashTail, svc)
	if !strings.Contains(h, "OPSM-110") {
		t.Fatalf("Postgres 18 refusing the old data directory should be decoded, got: %q", h)
	}
	// The whole sentence, in order. Naming the two paths in either order reads as a
	// sentence either way, and the wrong order tells the reader to move the mount
	// back down to the directory Postgres 18 will not use.
	// Written out rather than built from the constants: the paths are the two that
	// were measured, and a hint that names some other directory has to go red even
	// if the constant moved with it.
	const move = "mount `/var/lib/postgresql` instead of `/var/lib/postgresql/data`"
	if !strings.Contains(h, move) {
		t.Errorf("the hint has to say %q, got: %q", move, h)
	}
	if strings.Contains(h, "mount `"+postgresDataDir+"` instead") {
		t.Errorf("the hint points at the directory 18 will not use, got: %q", h)
	}
	if !strings.Contains(h, "./data") {
		t.Errorf("the hint should name the mount that has to move, got: %q", h)
	}
	// And then the whole sentence, copied from the run in
	// testdata/error-wordings/pg18-old-cluster-at-new-path.txt (the control block at
	// the end of it). Every clause here is something a capture in that directory
	// shows, and each was got wrong at least once while writing this:
	//
	//   - the mount belongs one level up      pg18-versioned-layout.txt
	//   - a host path works there             pg18-mount-one-level-up.txt
	//   - the image makes the subdirectory    pg18-mount-one-level-up.txt (the `ls`)
	//   - moving it is not an upgrade         pg18-old-cluster-at-new-path.txt
	//
	// Checking for words rather than the sentence let a hint through that named
	// `pg_upgrade` and then said the move carries the data over. Comparing the whole
	// thing means changing the wording is a decision, made with those runs in hand,
	// rather than something that can drift a clause at a time.
	const want = canonicalHint
	if h != want {
		t.Errorf("the hint is not the sentence the captured run produced\n got: %q\nwant: %q", h, want)
	}
}

// The same image refuses a second way: the mount is already where 18 wants it and
// holds a cluster from an earlier major version. Moving the mount is not the answer
// there, and the decoder has to stay quiet — which it does, because upstream calls
// a mount unused only when it is.
func TestCrashHintPostgres18SaysNothingAboutAnOldClusterAtTheNewPath(t *testing.T) {
	svc := &compose.Service{Volumes: []string{"pgdata:/var/lib/postgresql"}}
	if h := hintFor(t, pg18OldClusterTail, svc); strings.Contains(h, "OPSM-110") {
		t.Errorf("a cluster an earlier version wrote is not a misplaced mount, got: %q", h)
	}
}

// The anchors have to sit in the tail. Taking them from the `Error:` opening would
// read fine in a test that feeds the whole message and never fire in a real crash
// report, which only ever sees the end of it.
func TestCrashHintPostgres18SurvivesTheTailWindow(t *testing.T) {
	svc := &compose.Service{Volumes: []string{"pgdata:/var/lib/postgresql/data"}}
	if h := hintFor(t, lastLines(pg18FullMessage, 15), svc); !strings.Contains(h, "OPSM-110") {
		t.Errorf("the signature must survive the last 15 lines, got: %q", h)
	}
	if !strings.Contains(pg18FullMessage, "Error: in 18+") {
		t.Fatal("the fixture should hold the opening the decoder must not depend on")
	}
	if strings.Contains(lastLines(pg18FullMessage, 15), "Error: in 18+") {
		t.Error("the opening is inside the window, so this test proves nothing about the anchors")
	}
}

// A named volume at the old path fails the same way a bind mount does, and only
// starts once a PGDATA below it is added as well (both measured on the real
// runtime; the captures are in testdata/error-wordings/). So the chown decoder's
// answer — swap the bind mount for a named volume — is half a fix here, while
// moving the mount up one level needs nothing else. This decoder runs first for
// that reason, and leaves nothing recorded, so a later suggestion cannot offer the
// half.
func TestCrashHintPostgres18BeatsTheChownAdvice(t *testing.T) {
	svc := &compose.Service{Volumes: []string{"./data:/var/lib/postgresql/data"}}
	logs := "chown: /var/lib/postgresql/data: Operation not permitted\n" + pg18CrashTail
	p := &compose.Project{Name: "demo", BaseDir: t.TempDir(), Services: map[string]*compose.Service{"svc": svc}}
	o := &Orchestrator{Project: p}
	h := o.crashHint("svc", logs)
	if !strings.Contains(h, "OPSM-110") || strings.Contains(h, "OPSM-105") {
		t.Errorf("with both signatures present the mount has to move, not become a named volume, got: %q", h)
	}
	if rec := o.recordedChownFailures(); len(rec) != 0 {
		t.Errorf("recording this as a chown failure would make the overlay propose a named volume "+
			"at the path Postgres 18 will not use: %+v", rec)
	}
}

// The older layout is untouched: 17 and earlier fail by chowning, and that answer
// stays the one they get.
func TestCrashHintPostgres17StillGetsTheChownAdvice(t *testing.T) {
	svc := &compose.Service{Volumes: []string{"./data:/var/lib/postgresql/data"}}
	h := hintFor(t, "chown: /var/lib/postgresql/data: Operation not permitted", svc)
	if !strings.Contains(h, "OPSM-105") || strings.Contains(h, "OPSM-110") {
		t.Errorf("a plain chown failure is still the bind-mount gotcha, got: %q", h)
	}
}

// Half the signature is not the signature: `pg_upgrade` alone appears in ordinary
// upgrade advice, and a mount can be called unused for reasons of its own.
func TestCrashHintPostgres18NeedsBothAnchors(t *testing.T) {
	svc := &compose.Service{Volumes: []string{"./data:/var/lib/postgresql/data"}}
	for _, logs := range []string{
		"run pg_upgrade before starting this version",
		"note: /srv/cache (unused mount/volume)",
		// Both halves have to be the words Postgres 18 actually prints, and each
		// case below widens one of them the cheapest way it can be widened. The two
		// above only ever leave the other anchor out, so they pass a decoder that
		// kept a much looser word in its place.
		"PostgreSQL 18.4 starting up\nnote: /srv/cache (unused mount/volume)",
		// `pg_upgrade` loosened to `postgres`: a word every Postgres log carries,
		// paths included.
		"note: /var/lib/postgresql/tmp (unused mount/volume)",
		// `(unused mount/volume)` loosened to `unused`: an ordinary English word
		// that upgrade advice reaches for on its own.
		"pg_upgrade left unused files in the old cluster; remove them when done",
	} {
		if h := hintFor(t, logs, svc); strings.Contains(h, "OPSM-110") {
			t.Errorf("%q should not be read as Postgres 18's refusal, got: %q", logs, h)
		}
	}
}

// "there" in the sentence points at the old data directory, so only a mount at
// that path can be named. Mounts elsewhere below /var/lib/postgresql are ordinary
// — an injected postgresql.conf, a pg_wal of its own — and naming one of those
// would tell the reader it is somewhere it is not.
func TestCrashHintPostgres18NamesOnlyTheMountAtTheDataDir(t *testing.T) {
	both := &compose.Service{Volumes: []string{
		"./data:/var/lib/postgresql/data",
		"./conf:/var/lib/postgresql/conf",
	}}
	h := hintFor(t, pg18CrashTail, both)
	if !strings.Contains(h, "this service mounts ./data there") {
		t.Errorf("the mount at the data directory is the one that has to move, got: %q", h)
	}
	if strings.Contains(h, "./conf") {
		t.Errorf("a mount that is not at the data directory should not be named, got: %q", h)
	}

	// A named volume is named the same way: what moves is the mount, whatever kind
	// of mount it is. This sentence is the one that run produced, copied from
	// testdata/error-wordings/pg18-named-old-datadir.txt.
	named := &compose.Service{Volumes: []string{"pgdata:/var/lib/postgresql/data"}}
	wantNamed := namedVolumeHint
	if h := hintFor(t, pg18CrashTail, named); h != wantNamed {
		t.Errorf("the named-volume hint is not the sentence that run produced\n got: %q\nwant: %q", h, wantNamed)
	}

	// With nothing at that path the sentence is the same one, minus the clause that
	// names a mount. Comparing the whole thing here too, because checking only that
	// the clause is absent let a rewritten hint through on this branch while the
	// branch with a mount stayed right.
	wantNoClause := strings.Replace(canonicalHint, " (this service mounts ./data there)", "", 1)
	if wantNoClause == canonicalHint {
		t.Fatal("the clause this test removes is no longer in the hint; update it against the captures")
	}
	confOnly := &compose.Service{Volumes: []string{"./conf:/var/lib/postgresql/conf"}}
	if h := hintFor(t, pg18CrashTail, confOnly); h != wantNoClause {
		t.Errorf("with nothing at the data directory the hint should be the same, without the clause\n got: %q\nwant: %q", h, wantNoClause)
	}
}

// Anything other than exactly one mount at that path leaves the clause off. Two is
// a strange thing to write, and it is exactly where a "remember the last one you
// saw" loop names the wrong one without anybody noticing.
func TestCrashHintPostgres18NamesNoMountWhenTwoSitAtTheDataDir(t *testing.T) {
	svc := &compose.Service{Volumes: []string{
		"./data:/var/lib/postgresql/data",
		"./other:/var/lib/postgresql/data",
	}}
	want := strings.Replace(canonicalHint, " (this service mounts ./data there)", "", 1)
	if h := hintFor(t, pg18CrashTail, svc); h != want {
		t.Errorf("with two mounts at that path the hint should name neither\n got: %q\nwant: %q", h, want)
	}
}

// Nothing else here opens the captures: the fixtures are pasted and the sentences
// are pasted, so a wrong paste reads as agreement. This is the one place the copies
// meet the files they claim to come from.
func TestTheHintsHereAreTheOnesTheCapturedRunsProduced(t *testing.T) {
	for _, c := range []struct{ file, hint string }{
		{"pg18-old-cluster-at-new-path.txt", canonicalHint},
		{"pg18-named-old-datadir.txt", namedVolumeHint},
	} {
		path := filepath.Join("..", "..", "testdata", "error-wordings", c.file)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if want := strings.TrimPrefix(c.hint, "\n"); !strings.Contains(string(b), want) {
			t.Errorf("%s does not contain the sentence this file says it produced:\n%s", c.file, want)
		}
	}
}
