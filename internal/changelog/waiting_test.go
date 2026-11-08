package changelog_test

import (
	"errors"
	"io/fs"
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
	const notExist = "\x00the read error"
	const listed = "## [Unreleased]\n\n### Fixed\n\n- A fix.\n\n## [1.0.0] - 2026-10-24\n\n- Old.\n"
	const empty = "## [Unreleased]\n\n## [1.0.0] - 2026-10-24\n\n- Old.\n"
	const entry, unreadable = "- A fix.\n", "not an entry\n"
	for _, tc := range []struct {
		name      string
		changelog string // "" for no CHANGELOG.md at all
		fragment  string // the body of the one fragment in the directory asked about; "" for none
		// apart puts the fragment directory somewhere other than beside the
		// changelog, with a decoy CHANGELOG.md beside it that says the opposite,
		// so only the changelog named in the call gives the row's answer.
		apart   bool
		wantN   int
		wantErr string // "" for no error; notExist for the read error itself
	}{
		{"entries listed and none found is the wrong place", listed, "", false, 0, "looking in the wrong place"},
		{"entries listed and found", listed, entry, false, 1, ""},
		{"nothing listed and nothing found, as after a release", empty, "", false, 0, ""},
		{"entries listed and none found, the changelog elsewhere", listed, "", true, 0, "looking in the wrong place"},
		{"nothing listed and nothing found, the changelog elsewhere", empty, "", true, 0, ""},
		// The fragment where the call says and nowhere near the changelog: only
		// the directory named in the call finds it.
		{"entries listed and found, the changelog elsewhere", listed, entry, true, 1, ""},
		// The read error, not the wrong-place one, which names CHANGELOG.md too.
		{"no changelog is no answer", "", "", false, 0, notExist},
		// A fragment Load cannot read is an error, not "nothing found": the
		// checks that lean on Waiting would otherwise skip over it.
		{"a fragment that cannot be read is no answer", listed, unreadable, false, 0, "4-a-fix.fixed.md"},
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
			if tc.apart {
				elsewhere := filepath.Join(root, "elsewhere")
				if err := os.Mkdir(elsewhere, 0o755); err != nil {
					t.Fatal(err)
				}
				decoy := listed
				if tc.changelog == listed {
					decoy = empty
				}
				if err := os.WriteFile(filepath.Join(elsewhere, "CHANGELOG.md"), []byte(decoy), 0o644); err != nil {
					t.Fatal(err)
				}
				dir = filepath.Join(elsewhere, "changelog.d")
			}
			if tc.fragment != "" {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "4-a-fix.fixed.md"), []byte(tc.fragment), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			frags, err := changelog.Waiting(filepath.Join(root, "CHANGELOG.md"), dir)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("want %d fragments, got error %v", tc.wantN, err)
			case tc.wantErr == notExist && !errors.Is(err, fs.ErrNotExist):
				t.Fatalf("want the error reading the missing changelog, got %v (%d fragments)", err, len(frags))
			case tc.wantErr != "" && tc.wantErr != notExist && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("want an error saying %q, got %v (%d fragments)", tc.wantErr, err, len(frags))
			}
			if len(frags) != tc.wantN {
				t.Errorf("got %d fragments, want %d", len(frags), tc.wantN)
			}
		})
	}
}
