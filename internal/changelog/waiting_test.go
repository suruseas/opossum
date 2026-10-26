package changelog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/changelog"
)

// Waiting is what the fragment checks lean on to know they are looking in the
// right place, and the repository's own state cannot exercise it: right after a
// release [Unreleased] is empty and there is nothing to find. So each row builds
// the state it asks about. One row per answer the function gives.
func TestWaitingTellsNoFragmentsFromTheWrongPlace(t *testing.T) {
	const listed = "## [Unreleased]\n\n### Fixed\n\n- A fix.\n\n## [1.0.0] - 2026-10-24\n\n- Old.\n"
	const empty = "## [Unreleased]\n\n## [1.0.0] - 2026-10-24\n\n- Old.\n"
	for _, tc := range []struct {
		name      string
		changelog string // "" for no CHANGELOG.md at all
		fragment  bool   // a fragment in the directory asked about
		wantN     int
		wantErr   string
	}{
		{"entries listed and none found is the wrong place", listed, false, 0, "looking in the wrong place"},
		{"entries listed and found", listed, true, 1, ""},
		{"nothing listed and nothing found, as after a release", empty, false, 0, ""},
		{"no changelog is no answer", "", false, 0, "CHANGELOG.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.changelog != "" {
				if err := os.WriteFile(filepath.Join(root, "CHANGELOG.md"), []byte(tc.changelog), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			// The directory asked about is the one the fragment is put in, or,
			// with no fragment, one that does not exist — the shape a wrong name
			// for it takes.
			dir := filepath.Join(root, "changelog.d")
			if tc.fragment {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "4-a-fix.fixed.md"), []byte("- A fix.\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			frags, err := changelog.Waiting(filepath.Join(root, "CHANGELOG.md"), dir)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("want %d fragments, got error %v", tc.wantN, err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("want an error saying %q, got %v (%d fragments)", tc.wantErr, err, len(frags))
			}
			if len(frags) != tc.wantN {
				t.Errorf("got %d fragments, want %d", len(frags), tc.wantN)
			}
		})
	}
}
