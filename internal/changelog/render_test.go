package changelog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/changelog"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The assembled section has to be indistinguishable from the ones written by hand
// before this existed — released notes are what the world reads, and a formatting
// drift would show up in public. This pins the exact bytes: heading, blank line,
// entries, blank line between sections, Keep a Changelog order.
func TestReleaseMatchesHandWrittenFormatting(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "10-b.added.md", "- Added B.\n")
	write(t, dir, "2-a.added.md", "- Added A.\n")
	write(t, dir, "3-c.fixed.md", "- Fixed C.\n")
	write(t, dir, "4-d.changed.md", "- Changed D.\n")
	frags, err := changelog.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	const before = "# Changelog\n\nIntro.\n\n## [Unreleased]\n\n## [0.1.0] - 2026-01-01\n\n### Added\n\n- The first thing.\n"
	got, err := changelog.Release(before, frags, "0.2.0", "2026-02-02")
	if err != nil {
		t.Fatal(err)
	}
	const want = "# Changelog\n\nIntro.\n\n## [Unreleased]\n\n" +
		"## [0.2.0] - 2026-02-02\n\n" +
		"### Added\n\n- Added A.\n- Added B.\n\n" +
		"### Changed\n\n- Changed D.\n\n" +
		"### Fixed\n\n- Fixed C.\n\n" +
		"## [0.1.0] - 2026-01-01\n\n### Added\n\n- The first thing.\n"
	if got != want {
		t.Errorf("assembled release differs from the hand-written shape.\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

// Ordering inside a section is by number then slug, so two people adding entries
// on separate branches always produce the same file — a changelog that shuffles
// between runs can't be golden-tested or reviewed.
func TestSectionOrderIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "9-z.added.md", "- Nine.\n")
	write(t, dir, "10-a.added.md", "- Ten.\n")
	write(t, dir, "9-a.added.md", "- Nine A.\n")
	frags, err := changelog.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := changelog.RenderBody(frags)
	want := "### Added\n\n- Nine A.\n- Nine.\n- Ten.\n"
	if got != want {
		t.Errorf("order = %q, want %q (number, then slug — not lexical on the whole name)", got, want)
	}
}

// A release empties Unreleased: its contents just became the release, and leaving
// them would publish them twice.
func TestReleaseClearsUnreleased(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "1-x.added.md", "- X.\n")
	frags, _ := changelog.Load(dir)
	before := "# C\n\n## [Unreleased]\n\n### Added\n\n- X.\n\n## [0.1.0] - 2026-01-01\n\n### Added\n\n- old\n"
	got, err := changelog.Release(before, frags, "0.2.0", "2026-02-02")
	if err != nil {
		t.Fatal(err)
	}
	if u := changelog.Unreleased(got); u != "" {
		t.Errorf("Unreleased should be empty after a release, got %q", u)
	}
	if strings.Count(got, "- X.") != 1 {
		t.Errorf("the entry should appear once, in the new release:\n%s", got)
	}
}

// sync is the inverse of preview: what it writes must be what preview prints, or
// the ratchet would be checking one thing and the release producing another.
func TestSyncWritesWhatPreviewPrints(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "5-y.changed.md", "- Y.\n")
	frags, _ := changelog.Load(dir)
	body := changelog.RenderBody(frags)
	out, err := changelog.WithUnreleased("# C\n\n## [Unreleased]\n\n## [0.1.0] - 2026-01-01\n\n### Added\n\n- old\n", body)
	if err != nil {
		t.Fatal(err)
	}
	if got := changelog.Unreleased(out); got != strings.TrimRight(body, "\n") {
		t.Errorf("sync wrote %q, preview prints %q", got, body)
	}
}

// Malformed fragments must be refused loudly: silently skipping one would drop a
// change from the release notes, which nobody would notice until after shipping.
func TestLoadRejectsMalformedFragments(t *testing.T) {
	cases := map[string]string{
		"no-number.added.md": "- x\n",
		"1-x.bogus.md":       "- x\n",
		"1-x.added.md":       "not an entry\n",
		"2-y.added.md":       "\n",
	}
	for name, body := range cases {
		dir := t.TempDir()
		write(t, dir, name, body)
		if _, err := changelog.Load(dir); err == nil {
			t.Errorf("%s (%q) should have been rejected", name, body)
		}
	}
}

// README.md in changelog.d documents the format; it is not a fragment.
func TestLoadIgnoresReadme(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "README.md", "how to write fragments\n")
	frags, err := changelog.Load(dir)
	if err != nil || len(frags) != 0 {
		t.Errorf("README.md must be ignored, got %d fragment(s), err=%v", len(frags), err)
	}
}

// Every heading in the changelog is a reference link resolved by the block at the
// foot of the file. A release that adds a section but no definition publishes a
// heading that renders as literal `[0.17.0]` text, and leaves `[Unreleased]`
// comparing against the previous tag.
func TestReleaseMaintainsLinkDefinitions(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "1-x.added.md", "- X.\n")
	frags, _ := changelog.Load(dir)

	const before = "# C\n\n## [Unreleased]\n\n## [0.2.0] - 2026-02-02\n\n### Added\n\n- old\n\n" +
		"[Unreleased]: https://example.com/r/compare/v0.2.0...HEAD\n" +
		"[0.2.0]: https://example.com/r/compare/v0.1.0...v0.2.0\n"
	got, err := changelog.Release(before, frags, "0.3.0", "2026-03-03")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "[0.3.0]: https://example.com/r/compare/v0.2.0...v0.3.0\n") {
		t.Errorf("the new version needs a link definition comparing against the previous tag:\n%s", got)
	}
	if !strings.Contains(got, "[Unreleased]: https://example.com/r/compare/v0.3.0...HEAD\n") {
		t.Errorf("[Unreleased] must compare against the new tag:\n%s", got)
	}
	// Newest-first, like the block was maintained by hand.
	if strings.Index(got, "[0.3.0]:") > strings.Index(got, "[0.2.0]:") {
		t.Errorf("link definitions should stay newest-first:\n%s", got)
	}
	// Re-running must not stack duplicates.
	again := got
	if n := strings.Count(again, "[0.3.0]: "); n != 1 {
		t.Errorf("expected one definition for 0.3.0, got %d", n)
	}
}

// Releasing a version that already has a section would silently publish two
// sections for it — the shape someone hits when recovering from a partial run.
func TestReleaseRefusesAnExistingVersion(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "1-x.added.md", "- X.\n")
	frags, _ := changelog.Load(dir)
	before := "# C\n\n## [Unreleased]\n\n## [0.2.0] - 2026-02-02\n\n### Added\n\n- old\n"
	if _, err := changelog.Release(before, frags, "0.2.0", "2026-03-03"); err == nil {
		t.Error("releasing an existing version should be refused")
	}
}

// A fragment body containing a line that looks like a release heading would be
// read as the start of a released section next time the file is parsed, and
// regeneration would duplicate everything after it. Refuse it at load.
func TestLoadRejectsBodyThatLooksLikeAReleaseHeading(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "1-x.added.md", "- Example:\n\n```\n## [1.2.3] - 2020-01-01\n```\n")
	if _, err := changelog.Load(dir); err == nil {
		t.Error("a body containing a release heading should be refused")
	}
}

// CRLF and stray blank lines must not reach the published file. The ratchet can't
// catch either — both sides derive from the same fragment, so corruption
// round-trips cleanly.
func TestLoadNormalizesLineEndingsAndPadding(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "1-x.added.md", "\n\n- Windows wrote this.\r\n  Second line.\r\n\n")
	frags, err := changelog.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	body := changelog.RenderBody(frags)
	if strings.Contains(body, "\r") {
		t.Errorf("CR must not survive into the changelog: %q", body)
	}
	if body != "### Added\n\n- Windows wrote this.\n  Second line.\n" {
		t.Errorf("body = %q", body)
	}
}
