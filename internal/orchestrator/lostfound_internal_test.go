package orchestrator

// Evals for #372: Apple `container` gives every volume its own ext4 filesystem, so
// a "fresh" volume already holds `lost+found` where docker's holds nothing. opossum
// removes it from the volumes it creates, which is what makes a plain
// `pgdata:/var/lib/postgresql/data` work here. What is left — a volume opossum did
// not create — is decoded from initdb's own refusal rather than predicted.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// initdbRefusal is what postgres:16-alpine prints, captured from the real runtime
// on a volume that still had `lost+found` in it. Both gates the decoder keys on
// (`initdb:` and `lost+found`) come from these lines; a flattened paraphrase here
// would let the decoder key on something the image never says.
const initdbRefusal = `The default text search configuration will be set to "english".
initdb: error: directory "/var/lib/postgresql/data" exists but is not empty
initdb: detail: It contains a lost+found directory, perhaps due to it being a mount point.
initdb: hint: Using a mount point directly as the data directory is not recommended.`

func pgProject() *compose.Project {
	return &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"db": {Name: "db", Image: "postgres:16", Volumes: []string{"pgdata:/var/lib/postgresql/data"}},
	}}
}

func TestCrashReportDecodesInitdbRefusingALostFoundVolume(t *testing.T) {
	var out bytes.Buffer
	o := New(pgProject(), inspectShim(t, stoppedInspect, initdbRefusal), "", &out)

	if err := o.verifyStarted([]string{"db"}, map[string]bool{}); err == nil {
		t.Fatal("a service that exited should fail the up")
	}
	s := out.String()
	if !strings.Contains(s, "[OPSM-101]") {
		t.Fatalf("initdb's refusal should be decoded, got:\n%s", s)
	}
	// The three things a reader needs, and the volume it is about — a hint that
	// says "some volume" leaves them to work out which of their mounts died.
	for _, want := range []string{
		"pgdata",                                 // which volume
		"lost+found",                             // why
		"down -v",                                // one way out
		"PGDATA=/var/lib/postgresql/data/pgdata", // the other
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the hint should mention %q, got:\n%s", want, s)
		}
	}
	// `down -v` destroys volumes. It must arrive with what it costs attached, not
	// as a bare instruction to run.
	if !strings.Contains(s, "check nothing else did") {
		t.Errorf("the destructive option must say what it costs, got:\n%s", s)
	}
}

// Both gates have to hold. A crash that merely mentions one of the two words is
// not this failure, and a hint about the wrong thing is worse than none.
func TestInitdbHintNeedsBothHalvesOfTheSignature(t *testing.T) {
	o := New(pgProject(), nil, "", &bytes.Buffer{})
	svc := o.Project.Services["db"]
	for _, tc := range []struct {
		name, logs string
		want       bool
	}{
		{"the real refusal", initdbRefusal, true},
		{"initdb, but a different complaint", `initdb: error: could not access directory "/var/lib/postgresql/data": Permission denied`, false},
		{"lost+found, but not from initdb", "ls: /data/lost+found: Permission denied", false},
		{"neither", "panic: bad config", false},
	} {
		if got := o.initdbNotEmptyHint("db", svc, tc.logs) != ""; got != tc.want {
			t.Errorf("%s: hinted=%v, want %v", tc.name, got, tc.want)
		}
	}
}

// An `external: true` volume is the user's. `down -v` does not remove one, so
// offering that route would be advice that cannot work — and the volume must not
// be named as though opossum could replace it.
func TestInitdbHintDoesNotOfferToReplaceAnExternalVolume(t *testing.T) {
	p := pgProject()
	p.Volumes = map[string]compose.VolumeDecl{"pgdata": {External: true, Name: "real_pg_vol"}}
	o := New(p, nil, "", &bytes.Buffer{})
	h := o.initdbNotEmptyHint("db", o.Project.Services["db"], initdbRefusal)
	if h == "" {
		t.Fatal("the failure is still the failure; the hint should be given")
	}
	if strings.Contains(h, "down -v") {
		t.Errorf("`down -v` does not remove an external volume; it must not be offered:\n%s", h)
	}
	if !strings.Contains(h, "PGDATA=/var/lib/postgresql/data/pgdata") {
		t.Errorf("the remedy that does work must still be there:\n%s", h)
	}
}

// The volume is named as the runtime names it — the project-namespaced name that
// `container volume ls` shows — not the key from the compose file. A reader who
// goes looking for what was named has to find it.
func TestInitdbHintNamesTheVolumeTheRuntimeShows(t *testing.T) {
	o := New(pgProject(), nil, "", &bytes.Buffer{})
	h := o.initdbNotEmptyHint("db", o.Project.Services["db"], initdbRefusal)
	if !strings.Contains(h, `"demo_pgdata"`) {
		t.Errorf("the hint should name the real volume (demo_pgdata):\n%s", h)
	}
}

// The decoder must not claim a mount the service does not have. A Postgres that
// keeps its data somewhere else can still hit this signature (a second volume, a
// nested mount), and naming "pgdata" then would send the reader after a volume
// that has nothing to do with it.
func TestInitdbHintNamesNoVolumeItCannotSee(t *testing.T) {
	o := New(&compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"db": {Name: "db", Image: "postgres:16", Volumes: []string{"cache:/var/cache"}},
	}}, nil, "", &bytes.Buffer{})
	h := o.initdbNotEmptyHint("db", o.Project.Services["db"], initdbRefusal)
	if h == "" {
		t.Fatal("the signature is still the signature; the hint should be given")
	}
	if strings.Contains(h, "cache") {
		t.Errorf("no volume is mounted at the data directory, so none may be named:\n%s", h)
	}
	// Positively: it took the branch for "nothing here is opossum's to replace",
	// rather than the one that names a volume and happens to have nothing to put
	// in the quotes.
	if !strings.Contains(h, "not opossum's to replace") {
		t.Errorf("the hint should say why it names nothing:\n%s", h)
	}
	if strings.Contains(h, "down -v") || strings.Contains(h, `The volume is ""`) {
		t.Errorf("with no volume of its own to point at, it must not offer to replace one:\n%s", h)
	}
}
