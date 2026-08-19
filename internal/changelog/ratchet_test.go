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

	dir := explode(t, section)
	if len(mustReadDir(t, dir)) == 0 {
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

// explode turns a published section back into the fragments it was assembled
// from, writing them into a fresh directory and returning it.
//
// Being the exact inverse of the assembler is the whole point: this is what lets
// a round trip prove the assembler's formatting, so a lossy step here shows up as
// "the assembler is broken" when it is not. A blank line inside an entry — the
// break between two paragraphs — used to be dropped here, and the first fragment
// written with two paragraphs turned that into a failure blaming the assembler.
//
// A blank line is ambiguous while reading it: it separates paragraphs of one
// entry, and it also ends the last entry of a section. Which one it is only
// becomes clear at the next line, so blanks are held back and kept if a
// continuation follows, discarded otherwise.
func explode(t *testing.T, section string) string {
	t.Helper()
	dir := t.TempDir()
	n := 0
	var typ string
	var held []string
	appendTo := func(lines ...string) {
		t.Helper()
		p := filepath.Join(dir, fmt.Sprintf("%03d-e.%s.md", n, typ))
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range lines {
			fmt.Fprintln(f, l)
		}
		f.Close()
	}
	for _, line := range strings.Split(section, "\n") {
		switch {
		case strings.HasPrefix(line, "### "):
			typ, held = strings.ToLower(strings.TrimSpace(line[4:])), nil
		case strings.HasPrefix(line, "- ") && typ != "":
			n++
			held = nil
			write(t, dir, fmt.Sprintf("%03d-e.%s.md", n, typ), line+"\n")
		case typ != "" && n > 0 && strings.TrimSpace(line) == "":
			held = append(held, line)
		case typ != "" && n > 0 && strings.HasPrefix(line, "  "):
			appendTo(append(held, line)...)
			held = nil
		default:
			held = nil
		}
	}
	return dir
}

func mustReadDir(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ents
}

// The round trip above only ever looks at whatever the newest published section
// happens to contain, so a shape nobody has shipped yet is unguarded. That is how
// a two-paragraph entry got through: the check existed from the start, and the
// first fragment written with a paragraph break made it fail — pointing at the
// assembler, which was fine.
//
// These shapes are written out instead of found, so each is covered whether or
// not a release happens to hold one.
func TestRebuildsEveryEntryShapeByteForByte(t *testing.T) {
	// Each case is one fragment unless it names two. A release with two type
	// sections is what puts a blank line between one entry and the NEXT entry
	// rather than at the end of everything, which is where dropping the held-back
	// blank actually matters: an entry that keeps it would grow a trailing blank
	// belonging to its neighbour.
	for _, tc := range []struct {
		name  string
		body  string
		other string // an entry of a second type, when the case needs one
	}{
		{name: "one line", body: "- A single-line entry.\n"},
		{name: "wrapped over lines", body: "- An entry that runs on and\n  wraps onto a second line.\n"},
		{name: "two paragraphs", body: "- The first paragraph of an entry.\n\n" +
			"  The second paragraph, which the exploder used to drop the break before.\n"},
		{name: "three paragraphs", body: "- One.\n\n  Two.\n\n  Three.\n"},
		{name: "a paragraph after a wrapped one", body: "- An entry that wraps onto\n  a second line.\n\n" +
			"  Then a new paragraph.\n"},
		// Two blank lines in a row inside an entry stay two: holding only the first
		// of a run would render the same in Markdown but is not what was shipped.
		{name: "a run of blank lines is kept whole", body: "- One.\n\n\n  Two.\n"},
		{
			name:  "two type sections, the earlier one ending in a paragraph",
			body:  "- One paragraph.\n\n  And another, right before the next heading.\n",
			other: "- An entry of a different type.\n",
		},
		{
			name:  "two type sections, plain entries",
			body:  "- One.\n",
			other: "- Two.\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, "001-e.fixed.md", tc.body)
			if tc.other != "" {
				write(t, dir, "002-e.added.md", tc.other)
			}
			frags, err := changelog.Load(dir)
			if err != nil {
				t.Fatalf("loading the fragment: %v", err)
			}
			// Assemble it into a changelog, then explode that section and assemble
			// again. The two assemblies must be identical: whatever explode drops,
			// the second one is missing.
			const empty = "# Changelog\n\n## [Unreleased]\n\n## [0.0.1] - 2020-01-01\n\n### Fixed\n\n- Older.\n"
			once, err := changelog.Release(empty, frags, "9.9.9", "2026-01-01")
			if err != nil {
				t.Fatal(err)
			}
			heads := releaseHeadings(once)
			section := once[heads[0].at:heads[1].at]

			refrags, err := changelog.Load(explode(t, section))
			if err != nil {
				t.Fatalf("reloading the exploded section: %v", err)
			}
			twice, err := changelog.Release(empty, refrags, "9.9.9", "2026-01-01")
			if err != nil {
				t.Fatal(err)
			}
			if twice != once {
				t.Errorf("the round trip changed the section.\n--- first ---\n%s\n--- second ---\n%s",
					section, twice[heads[0].at:heads[1].at])
			}
		})
	}
}
