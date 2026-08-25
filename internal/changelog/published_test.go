package changelog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/changelog"
)

// A release folds the fragments into a version's section and deletes them. A
// branch opened before that release still holds its fragment, and its copy of the
// file still has the entry under `## [Unreleased]` — so merging the release into
// the branch puts the new heading above an entry that belongs below it, and the
// entry lands inside the published section. `make changelog` does not clean that
// up: it rewrites `[Unreleased]` and nothing else.
//
// This happened, and the file it produced told anyone reading it that a fix
// shipped in a version that does not contain it. A published section is the record
// of what went out; an entry that is still waiting to go out cannot be in one.
func TestNothingWaitingToShipIsAlreadyInAPublishedSection(t *testing.T) {
	for _, where := range waitingEntriesInPublishedSections(t, repoRoot(t)) {
		t.Error(where)
	}
}

// The repository is expected to be clean, so the ratchet above says nothing about
// whether it can see the thing it is looking for. This does: a directory laid out
// the way this one is, with an entry in both places, has to be caught — and it
// goes through the same loader and the same reading of the file, so breaking
// either of those breaks this too.
func TestAnEntryInBothPlacesIsCaught(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "changelog.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	const entry = "- The thing that is still waiting to go out, at some length so that\n  it wraps the way a real one does.\n"
	if err := os.WriteFile(filepath.Join(dir, "changelog.d", "999-waiting.fixed.md"), []byte(entry), 0o644); err != nil {
		t.Fatal(err)
	}
	// The same entry in both places, rewrapped in the published one — which is how
	// it arrives: a merge puts the heading above it and nothing rewrites it.
	doc := "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n" + entry +
		"\n## [1.0.0] - 2020-01-01\n\n### Fixed\n\n" +
		"- The thing that is still waiting\n  to go out, at some length so that it wraps the way a real one does.\n" +
		"- Something that really did ship.\n"
	if err := os.WriteFile(filepath.Join(dir, "CHANGELOG.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	found := waitingEntriesInPublishedSections(t, dir)
	if len(found) != 1 {
		t.Fatalf("expected the entry in both places to be caught once, got %d: %v", len(found), found)
	}
	if !strings.Contains(found[0], "999-waiting.fixed.md") || !strings.Contains(found[0], "1.0.0") {
		t.Errorf("the report should name the fragment and the section it is in, got %q", found[0])
	}

	// …and a directory where only the published entry exists is not reported: the
	// check is about entries still waiting, not about every line in the file.
	if err := os.Remove(filepath.Join(dir, "changelog.d", "999-waiting.fixed.md")); err != nil {
		t.Fatal(err)
	}
	if found := waitingEntriesInPublishedSections(t, dir); len(found) != 0 {
		t.Errorf("with nothing waiting, nothing is misplaced; got %v", found)
	}
}

// waitingEntriesInPublishedSections is the check itself, over a directory laid out
// like this repository: the fragments still waiting, and the CHANGELOG beside them.
func waitingEntriesInPublishedSections(t *testing.T, root string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	published := publishedSections(t, string(b))
	if len(published) == 0 {
		t.Fatal("no published sections found; this check is reading the file wrong")
	}
	frags, err := changelog.Load(filepath.Join(root, "changelog.d"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range frags {
		// The first line of an entry is enough to find it again and short enough
		// to survive rewrapping of the rest.
		first := strings.SplitN(strings.TrimPrefix(f.Body, "- "), "\n", 2)[0]
		if strings.TrimSpace(first) == "" {
			continue
		}
		for version, body := range published {
			if strings.Contains(flatten(body), flatten(first)) {
				out = append(out, filepath.Base(f.Path)+" is still waiting for a release, but its entry is already inside "+
					version+" — a release merged into this branch put it there, and `make changelog` does not take it back out")
			}
		}
	}
	return out
}

// publishedSections is every `## [x.y.z]` section, by heading.
func publishedSections(t *testing.T, doc string) map[string]string {
	t.Helper()
	out := map[string]string{}
	var heading string
	var body []string
	flush := func() {
		if heading != "" {
			out[heading] = strings.Join(body, "\n")
		}
		heading, body = "", nil
	}
	for _, line := range strings.Split(doc, "\n") {
		// Spacing and case are the writer's, not the format's: `##  [1.2.3]` is a
		// heading and `## [unreleased]` is the one section that is not published.
		// Reading either strictly would put a published section's body into the
		// section before it, or treat the waiting one as shipped.
		if after, ok := strings.CutPrefix(strings.TrimSpace(line), "##"); ok && strings.HasPrefix(strings.TrimSpace(after), "[") {
			flush()
			if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(after)), "[unreleased]") {
				heading = strings.Join(strings.Fields(line), " ")
			}
			continue
		}
		if heading != "" {
			body = append(body, line)
		}
	}
	flush()
	return out
}

// flatten makes a line comparable across rewrapping.
func flatten(s string) string { return strings.Join(strings.Fields(s), " ") }
