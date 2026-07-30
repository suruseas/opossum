package changelog_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/changelog"
)

// repoRoot is two levels up from this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(wd))
}

// The `## [Unreleased]` section is generated, not written. Keeping it provably
// equal to what changelog.d renders is what makes the fragment workflow real:
// a hand-edited CHANGELOG and a forgotten `make changelog` both fail here,
// instead of surfacing as a merge conflict or an entry landing inside an already
// published release.
func TestUnreleasedMatchesFragments(t *testing.T) {
	root := repoRoot(t)
	frags, err := changelog.Load(filepath.Join(root, "changelog.d"))
	if err != nil {
		t.Fatalf("reading changelog.d: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimRight(changelog.RenderBody(frags), "\n")
	got := changelog.Unreleased(string(b))
	if got != want {
		t.Errorf("CHANGELOG.md's [Unreleased] is out of sync with changelog.d.\n"+
			"Run `make changelog` to regenerate it, and don't edit that section by hand.\n\n"+
			"--- CHANGELOG.md has ---\n%s\n\n--- changelog.d renders ---\n%s", got, want)
	}
}

// Every fragment has to be readable: a name that doesn't parse, or a body that
// isn't a `- ` entry, would be silently dropped from a release otherwise.
func TestFragmentsAreWellFormed(t *testing.T) {
	root := repoRoot(t)
	if _, err := changelog.Load(filepath.Join(root, "changelog.d")); err != nil {
		t.Errorf("changelog.d holds a fragment that can't be read: %v\n"+
			"Name them <number>-<slug>.<type>.md and start the body with \"- \".", err)
	}
}

// The strongest formatting check available: take a section a human actually wrote
// and shipped, explode it back into fragments, and reassemble it. If the result
// differs by a byte, the assembler would visibly change the published changelog
// the first time it is used for real.
func TestRebuildsAPublishedSectionByteForByte(t *testing.T) {
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	// Compare against a changelog whose Unreleased is empty: a release legitimately
	// clears that section, so leaving pending entries in the expected side would
	// make this test fail for a reason that has nothing to do with formatting.
	full, err := changelog.WithUnreleased(string(b), "")
	if err != nil {
		t.Fatal(err)
	}

	// The most recent released section, and the one after it (the insert point).
	heads := releaseHeadings(full)
	if len(heads) < 2 {
		t.Skip("needs at least two released sections to exercise insertion")
	}
	start, next := heads[0], heads[1]
	section := full[start.at:next.at]

	dir := t.TempDir()
	n := 0
	var typ string
	for _, line := range strings.Split(section, "\n") {
		switch {
		case strings.HasPrefix(line, "### "):
			typ = strings.ToLower(strings.TrimSpace(line[4:]))
		case strings.HasPrefix(line, "- ") && typ != "":
			n++
			write(t, dir, fmt.Sprintf("%03d-e.%s.md", n, typ), line+"\n")
		case typ != "" && n > 0 && strings.HasPrefix(line, "  ") && strings.TrimSpace(line) != "":
			p := filepath.Join(dir, fmt.Sprintf("%03d-e.%s.md", n, typ))
			f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(f, line)
			f.Close()
		}
	}
	if n == 0 {
		t.Skip("the latest release section has no entries to rebuild")
	}
	frags, err := changelog.Load(dir)
	if err != nil {
		t.Fatalf("exploding %s into fragments: %v", start.version, err)
	}

	without := full[:start.at] + full[next.at:]
	got, err := changelog.Release(without, frags, start.version, start.date)
	if err != nil {
		t.Fatal(err)
	}
	if got != full {
		t.Errorf("reassembling the published %s section changed the changelog.\n"+
			"The assembled formatting must be indistinguishable from what was shipped.", start.version)
	}
}

type headingRef struct {
	at      int
	version string
	date    string
}

// releaseHeadings finds the `## [X.Y.Z] - DATE` headings, newest first.
func releaseHeadings(s string) []headingRef {
	re := regexp.MustCompile(`(?m)^## \[([0-9][^\]]*)\] - (\S+)$`)
	var out []headingRef
	for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
		out = append(out, headingRef{at: m[0], version: s[m[2]:m[3]], date: s[m[4]:m[5]]})
	}
	return out
}
