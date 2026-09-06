package changelog

import (
	"strings"
	"testing"
)

// The first release has no previous tag to compare against, so its definition
// points at the tag's own page. The version appears twice on that line and the
// URL once — three strings on one format call that no test reached (#559's
// swap sweep reported the line unreached).
//
// Asked of withLinks directly, and with a link block that already names a tag
// no section carries: the compare URL is learnt from a version's own link, so a
// block holding only [Unreleased] is left alone, and Release itself never gets
// this far before the first release — WithUnreleased keeps only what follows the
// next version heading, and there is none. What the branch does when it is
// reached is what this holds.
func TestTheFirstReleaseLinksToItsTag(t *testing.T) {
	got := withLinks("# Changelog\n\n## [Unreleased]\n\n## [0.1.0] - 2026-09-06\n\n- X.\n\n"+
		"[Unreleased]: https://example.com/r/compare/v0.0.0...HEAD\n"+
		"[0.0.1]: https://example.com/r/compare/v0.0.0...v0.0.1\n", "0.1.0", "")
	for _, want := range []string{
		"[0.1.0]: https://example.com/r/releases/tag/v0.1.0\n",
		"[Unreleased]: https://example.com/r/compare/v0.1.0...HEAD\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the first release should carry %q, got:\n%s", want, got)
		}
	}
}
