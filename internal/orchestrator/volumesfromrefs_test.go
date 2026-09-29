package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A `volumes_from` naming a service the file does not define, or a
// `container:` entry, is nobody's business while the service that names it
// is gated and inactive — compose.expandVolumesFrom defers both to here
// (#1156, the same deferral #1094 made for an undefined depends_on target).
// Once that service turns out to be active, the fault is refused the same
// way reading the file refuses it for an ungated one.
func TestVolumesFromRefsDeferBothFaultsUntilActive(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  string
		want string
	}{
		{"a service the file does not define", "nope", `depends on undefined service "nope"`},
		{"a container: entry", "container:other", "names a container outside this compose file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := project("demo", map[string]*compose.Service{
				"keep": {Image: "alpine:3.20"},
				"user": {Image: "alpine:3.20", Profiles: []string{"x"}, VolumesFrom: []string{tc.ref}},
			})
			rt, _ := fakeShim(t)

			// Closed: user is inactive, so its fault is nobody's business —
			// exercised through both paths that read the project without
			// starting or stopping it (ValidateProfiles) and the one that
			// starts services (Up).
			o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
			if err := o.ValidateProfiles(); err != nil {
				t.Errorf("profile closed, ValidateProfiles: want no refusal, got %v", err)
			}
			o2 := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
			if err := o2.Up(true); err != nil {
				t.Errorf("profile closed, up: want no refusal, got %v", err)
			}

			// Open: user is now active, and the fault is refused before
			// anything starts.
			oOpen := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
			oOpen.EnableProfiles([]string{"x"})
			if err := oOpen.ValidateProfiles(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("profile open, ValidateProfiles: want %q, got %v", tc.want, err)
			}
			oOpen2 := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
			oOpen2.EnableProfiles([]string{"x"})
			if err := oOpen2.Up(true); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("profile open, up: want %q, got %v", tc.want, err)
			}

			// Named: `run` activates the service it names regardless of
			// --profile, and reaches checkVolumesFromRefs only through
			// checkProjectLoads (validateProfileDeps is Up's and config's,
			// not RunOneOff's) — the one call site the other two paths above
			// do not exercise.
			oRun := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
			if err := oRun.RunOneOff("user", []string{"true"}, orchestrator.RunOneOffOptions{}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("named (run): want %q, got %v", tc.want, err)
			}
		})
	}
}

// A holder's anonymous volume (`- /data`) at a path the referencing service
// does not mount itself is a third deferred fault (#1402): opossum cannot
// share it the way docker compose does — a permanent limit on what it can
// mount, not a timing question — but whether the referencing service ever
// runs is still nobody's business while it is gated and inactive.
// compose.expandVolumesFrom defers this the same way as the two above, and
// checkVolumesFromRefs refuses it once the service turns out to be active.
func TestVolumesFromRefsDefersTheAnonymousVolumeFaultUntilActive(t *testing.T) {
	want := "anonymous volume at /data"
	body := "services:\n" +
		"  keep:\n    image: alpine:3.20\n" +
		"  holder:\n    image: alpine:3.20\n    volumes: [/data]\n" +
		"  user:\n    image: alpine:3.20\n    profiles: [x]\n    volumes_from: [holder]\n"
	p, err := loadProject(t, body)
	if err != nil {
		t.Fatalf("loading test compose: %v", err)
	}
	rt, _ := fakeShim(t)

	// Closed: user is inactive, so the fault is nobody's business — both
	// paths that read the project without starting or stopping it
	// (ValidateProfiles) and the one that starts services (Up).
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.ValidateProfiles(); err != nil {
		t.Errorf("profile closed, ValidateProfiles: want no refusal, got %v", err)
	}
	o2 := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o2.Up(true); err != nil {
		t.Errorf("profile closed, up: want no refusal, got %v", err)
	}

	// Open: user is now active, and the fault is refused before anything
	// starts.
	oOpen := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	oOpen.EnableProfiles([]string{"x"})
	if err := oOpen.ValidateProfiles(); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("profile open, ValidateProfiles: want %q, got %v", want, err)
	}
	oOpen2 := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	oOpen2.EnableProfiles([]string{"x"})
	if err := oOpen2.Up(true); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("profile open, up: want %q, got %v", want, err)
	}

	// Named: `run` activates the service it names regardless of --profile.
	oRun := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := oRun.RunOneOff("user", []string{"true"}, orchestrator.RunOneOffOptions{}); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("named (run): want %q, got %v", want, err)
	}
}

// A referencing service that mounts the same path itself takes it before a
// borrowed one does (expandVolumesFrom's own rule, unaffected by #1402): never
// a conflict to refuse, gated and active or not.
func TestVolumesFromRefsDoesNotFlagAnonymousVolumeTheReferencingServiceMountsItself(t *testing.T) {
	body := "services:\n" +
		"  holder:\n    image: alpine:3.20\n    volumes: [/data]\n" +
		"  user:\n    image: alpine:3.20\n    profiles: [x]\n    volumes: [/data]\n    volumes_from: [holder]\n"
	p, err := loadProject(t, body)
	if err != nil {
		t.Fatalf("loading test compose: %v", err)
	}
	rt, _ := fakeShim(t)
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	o.EnableProfiles([]string{"x"})
	if err := o.ValidateProfiles(); err != nil {
		t.Errorf("want no refusal when user mounts /data itself, got %v", err)
	}
}

// The same exclusion applies to a path the referencing service takes with its
// own `tmpfs:`, not only its own `volumes:`.
func TestVolumesFromRefsDoesNotFlagAnonymousVolumeThePathIsTmpfs(t *testing.T) {
	body := "services:\n" +
		"  holder:\n    image: alpine:3.20\n    volumes: [/data]\n" +
		"  user:\n    image: alpine:3.20\n    profiles: [x]\n    tmpfs: [/data]\n    volumes_from: [holder]\n"
	p, err := loadProject(t, body)
	if err != nil {
		t.Fatalf("loading test compose: %v", err)
	}
	rt, _ := fakeShim(t)
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	o.EnableProfiles([]string{"x"})
	if err := o.ValidateProfiles(); err != nil {
		t.Errorf("want no refusal when user's own tmpfs takes /data, got %v", err)
	}
}

// The exclusion is the referencing service's OWN declared mounts
// (OwnVolumes), not what it has already borrowed from an earlier holder in
// the same volumes_from list: an anonymous volume at a path an earlier
// holder's own (non-anonymous) mount already covers is still refused, the
// same as compose.expandVolumesFrom refuses it at load for an always-active
// service (TestVolumesFromKnownDifferences/an_anonymous_volume_at_a_path_a_later_holder_mounts_is_refused).
// Reading the already-borrowed set instead would read h1's bind at /data as
// user's own and silently wave h2's anonymous one through.
func TestVolumesFromRefsChecksOwnVolumesNotTheAlreadyBorrowedSet(t *testing.T) {
	want := "anonymous volume at /data"
	body := "services:\n" +
		"  h1:\n    image: alpine:3.20\n    volumes: ['./h:/data']\n" +
		"  h2:\n    image: alpine:3.20\n    volumes: [/data]\n" +
		"  user:\n    image: alpine:3.20\n    profiles: [x]\n    volumes_from: [h1, h2]\n"
	p, err := loadProject(t, body)
	if err != nil {
		t.Fatalf("loading test compose: %v", err)
	}
	rt, _ := fakeShim(t)
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	o.EnableProfiles([]string{"x"})
	if err := o.ValidateProfiles(); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("want %q even though an earlier holder's own bind already covers /data, got %v", want, err)
	}
}

// Every ref in a multi-holder volumes_from is checked, not just the first or
// the last: the conflict here is the first of three, and the middle of three
// in the second case.
func TestVolumesFromRefsChecksEveryRefNotJustFirstOrLast(t *testing.T) {
	for _, tc := range []struct {
		name, services, refs string
	}{
		{"conflict first of two",
			"  h1:\n    image: alpine:3.20\n    volumes: [/data]\n  h2:\n    image: alpine:3.20\n    volumes: ['./h:/other']\n",
			"h1, h2"},
		{"conflict middle of three",
			"  h1:\n    image: alpine:3.20\n    volumes: ['./h:/one']\n  h2:\n    image: alpine:3.20\n    volumes: [/data]\n  h3:\n    image: alpine:3.20\n    volumes: ['./h:/three']\n",
			"h1, h2, h3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "services:\n" + tc.services +
				"  user:\n    image: alpine:3.20\n    profiles: [x]\n    volumes_from: [" + tc.refs + "]\n"
			p, err := loadProject(t, body)
			if err != nil {
				t.Fatalf("loading test compose: %v", err)
			}
			rt, _ := fakeShim(t)
			o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
			o.EnableProfiles([]string{"x"})
			want := "anonymous volume at /data"
			if err := o.ValidateProfiles(); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("want %q, got %v", want, err)
			}
		})
	}
}
