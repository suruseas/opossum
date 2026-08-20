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

// emptyChangelog is the smallest changelog a release can be cut into: one
// published section for the new one to be inserted above.
const emptyChangelog = "# Changelog\n\n## [Unreleased]\n\n## [0.0.1] - 2020-01-01\n\n### Fixed\n\n- Older.\n"

// roundTrip assembles frags into a release section, decomposes that section back
// into fragments, and assembles again. The two sections have to be identical:
// whatever the decomposer drops, the second one is missing.
//
// The decomposer's own refusal comes back as err rather than failing the test
// here, so each caller can say which fragments it was working from.
func roundTrip(t *testing.T, frags []changelog.Fragment) (section, rebuilt string, err error) {
	t.Helper()
	cut := func(fs []changelog.Fragment) string {
		t.Helper()
		out, err := changelog.Release(emptyChangelog, fs, "9.9.9", "2026-01-01")
		if err != nil {
			t.Fatal(err)
		}
		heads := releaseHeadings(out)
		return out[heads[0].at:heads[1].at]
	}
	section = cut(frags)
	dir, err := explode(t, section)
	if err != nil {
		return section, "", err
	}
	refrags, err := changelog.Load(dir)
	if err != nil {
		t.Fatalf("reloading the decomposed section: %v", err)
	}
	return section, cut(refrags), nil
}

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

	// No skip for an empty section: the decomposer refuses one, so reaching here
	// means there are fragments to rebuild from.
	dir, err := explode(t, section)
	if err != nil {
		t.Fatal(err)
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

// sectionHeading matches the `## [x.y.z] - date` line that opens a release
// section — the same shape releaseHeadings looks for, and the one Load refuses to
// let a fragment body start with.
var sectionHeading = regexp.MustCompile(`^## \[[0-9][^\]]*\] - \S+$`)

// headingFor is the heading line the assembler writes for a type, spelled the way
// it spells it. The decomposer is the assembler's inverse, so "is this a heading"
// is not a guess about `### ` lines: it is whether the assembler could have
// written this exact line here.
func headingFor(t string) string { return "### " + strings.ToUpper(t[:1]) + t[1:] }

// headingType returns the type a line opens a section for, and whether it opens
// one at all given the type already open.
//
// A `### ` line is a heading only if the assembler could have produced it in this
// position: it names one of the six types, is spelled exactly as the assembler
// spells it, and comes strictly later in Types order than the section already
// open — the assembler walks Types in order and writes each at most once.
//
// Everything else that starts with `### ` is a line inside an entry. Deciding
// that by whether a fragment file of the same name happened to exist read a
// repeated `### Fixed` as a real heading and glued two entries together in
// silence, which is the failure this whole change is about.
func headingType(line, open string) (string, bool) {
	at := func(t string) int {
		for i, x := range changelog.Types {
			if x == t {
				return i
			}
		}
		return -1 // nothing open yet, so every type is still ahead
	}
	for _, t := range changelog.Types {
		if line == headingFor(t) && at(t) > at(open) {
			return t, true
		}
	}
	return "", false
}

// explode turns a published section back into the fragments it was assembled
// from, writing them into a fresh directory and returning it.
//
// Give it one section: from a `## [x.y.z] - date` heading up to the next one.
// Running past the last heading takes in the link definitions at the foot of the
// file, which belong to no section. It refuses them — as lines inside the last
// entry, which is where they land and not what they are. Nothing calls it that
// way; the contract is here so nothing starts.
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
func explode(t *testing.T, section string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	n := 0
	inEntry := false // an entry under THIS heading has been opened
	var typ, heading string
	appendTo := func(lines ...string) {
		t.Helper()
		// inEntry is the guard: this file was written when the entry opened.
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
	// notAnEntry is what a heading turns out to be when the next thing under it is
	// not an entry: either the heading has nothing under it, or the section ends
	// there. Both are shapes the assembler cannot produce.
	notAnEntry := func(line string) error {
		return fmt.Errorf("the decomposer read this line as the start of a section:\n"+
			"  %q\n"+
			"but what comes under it is not a `- ` entry:\n"+
			"  %q\n"+
			"The assembler writes a heading only above the entries of that type, so a\n"+
			"section shaped like this cannot be rebuilt from fragments — and the\n"+
			"assembler is not what is wrong here.", heading, line)
	}
	// wrongBlanks is what a run of blank lines turns out to be when it is not the
	// run the assembler writes there. Measured from its output, a section is:
	//
	//	## [x.y.z] - date        one blank under it
	//	### Type                 one blank under it
	//	- entry                  entries of one type run back to back
	//	- entry                  one blank before the next heading, and at the end
	//
	// A blank line inside an entry is a paragraph break and is kept, however many
	// there are; those are the ones a continuation line follows. Every other blank
	// is structure, and a section with the wrong amount of it cannot be rebuilt.
	// Dropping the difference silently made the round trip fail at the assembler
	// instead: 0.5.0 has a blank line between two entries of its Added section, and
	// that is what it looked like.
	structural := func(pending []string, want int, before string) error {
		if len(pending) != want {
			return fmt.Errorf("the decomposer found %d blank line(s) where the assembler writes %d:\n"+
				"  before %q\n"+
				"Under a heading it writes one; between entries of one type, none; before\n"+
				"the next heading and at the end of the section, one. Reading a section\n"+
				"with any other amount would rebuild it differently from what was shipped\n"+
				"— and the assembler is not what is wrong here.", len(pending), want, before)
		}
		for _, l := range pending {
			if l != "" {
				return fmt.Errorf("the decomposer found a line of whitespace where the assembler writes\n"+
					"an empty one:\n  %q\n  before %q\n"+
					"Here the line is structure rather than part of an entry, so nothing\n"+
					"carries those characters and the rebuilt section would not have them —\n"+
					"and the assembler is not what is wrong here.", l, before)
			}
		}
		return nil
	}
	// pending holds the blank-ish lines seen since the last line that was not one.
	// They are two different things depending on what comes next: inside an entry a
	// continuation line follows and they are its paragraph breaks, written back
	// verbatim; anywhere else they are structure, and structure is where the
	// assembler writes an empty line and nothing else.
	var pending []string
	for i, line := range strings.Split(section, "\n") {
		nextTyp, opensSection := headingType(line, typ)
		switch {
		case i == 0:
			// The section's own heading, which the contract puts first. It has to be
			// one: the assembler writes this line from a version and a date, so a
			// section that opens with anything else — or with a heading carrying so
			// much as a trailing space — comes back changed.
			if !sectionHeading.MatchString(line) {
				return "", fmt.Errorf("the decomposer expected the section to open with its own heading:\n"+
					"  %q\nA section runs from a `## [x.y.z] - date` line up to the next one, and\n"+
					"the assembler writes that line from the version and the date — anything\n"+
					"else here would not come back.", line)
			}
		case strings.TrimSpace(line) == "":
			// A line of spaces is one of these too. Inside an entry the assembler
			// writes it back out — Load trims a fragment at its ends only, so one in
			// the middle survives — and as structure it does not, which is why what
			// these are is decided where they are used and not here.
			pending = append(pending, line)
			continue
		case sectionHeading.MatchString(line):
			return "", fmt.Errorf("the decomposer was given more than one section, or text from past the\n"+
				"end of one:\n  %q\nIt reads a single section, from a `## [x.y.z] - date` heading up to the\n"+
				"next one.", line)
		case opensSection:
			if typ != "" && !inEntry {
				return "", notAnEntry(line) // a heading with nothing under it
			}
			if err := structural(pending, 1, line); err != nil {
				return "", err
			}
			typ, heading, pending, inEntry = nextTyp, line, nil, false
		case strings.HasPrefix(line, "- ") && typ != "":
			want := 0 // entries of one type run back to back
			if !inEntry {
				want = 1 // this one is the first under its heading
			}
			if err := structural(pending, want, line); err != nil {
				return "", err
			}
			n++
			pending, inEntry = nil, true
			write(t, dir, fmt.Sprintf("%03d-e.%s.md", n, typ), line+"\n")
		case inEntry && strings.HasPrefix(line, "  "):
			appendTo(append(pending, line)...)
			pending = nil
		case inEntry:
			// Inside an entry and not part of it as far as this can tell. Dropping it
			// was the old behaviour, and it made the round trip fail one step later,
			// at the assembler, which had done nothing wrong.
			return "", fmt.Errorf("the decomposer cannot classify this line, so it would be dropped:\n"+
				"  %q\n"+
				"Inside an entry it knows a blank line and a continuation indented by two\n"+
				"spaces. If this line came from a fragment, that fragment uses a shape\n"+
				"CONTRIBUTING does not allow; otherwise the decomposer needs to learn it —\n"+
				"but the assembler is not what is wrong here.", line)
		case typ != "":
			return "", notAnEntry(line) // under a heading, before its first entry
		default:
			// Above the first heading of the section, where the assembler writes
			// nothing but the `## [x.y.z] - date` line. Silently dropping this is the
			// same defect as above, one region over, and it would surface the same
			// way: as the assembler changing a section it assembled correctly.
			return "", fmt.Errorf("the decomposer cannot classify this line, so it would be dropped:\n"+
				"  %q\n"+
				"It sits above the first `### ` heading of the section, where the assembler\n"+
				"writes only the `## [x.y.z] - date` line. A section holding anything else\n"+
				"there cannot be rebuilt from fragments — but the assembler is not what is\n"+
				"wrong here.", line)
		}
		pending = nil
	}
	if typ != "" && !inEntry {
		return "", notAnEntry("") // the section ended under a heading with no entry
	}
	// One blank line closes the section, and the newline ending that line splits
	// into one more. The two is a property of how a section is cut — up to the next
	// `## [` line — which is what every caller does and what the contract above
	// says. Anything else here is structure that would not come back.
	if typ == "" {
		return "", fmt.Errorf("the decomposer found no `### ` heading in this section:\n%q\n"+
			"A section with nothing in it decomposes into no fragments at all, and a\n"+
			"round trip over no fragments proves nothing — so this is refused rather\n"+
			"than quietly skipped.", section)
	}
	if err := structural(pending, 2, "the end of the section"); err != nil {
		return "", err
	}
	// Load normalizes what it reads — it trims each fragment, so a space at the end
	// of an entry's last line is gone by the time the assembler writes it out
	// again. Rather than guess which bytes those are, ask Load: anything it changes
	// is a byte this section holds and a fragment cannot carry.
	frags, err := changelog.Load(dir)
	if err != nil {
		return dir, nil // Load's own refusal belongs to the caller, which reports it
	}
	for _, f := range frags {
		b, err := os.ReadFile(f.Path)
		if err != nil {
			t.Fatal(err)
		}
		if written := strings.TrimSuffix(string(b), "\n"); f.Body != written {
			return "", fmt.Errorf("the decomposer read an entry that a fragment cannot carry unchanged:\n"+
				"  as written  %q\n  as read     %q\n"+
				"A fragment is trimmed when it is read, so those bytes would not come\n"+
				"back, and the rebuilt section would differ from what was shipped — the\n"+
				"assembler is not what is wrong here.", written, f.Body)
		}
	}
	return dir, nil
}

// A shape the decomposer cannot read has to say so, and say it about itself.
//
// Each of these already failed before — that was never the problem. They failed
// at the assembler, one step later and in a release rather than in the pull
// request, with a message about formatting that had been shipped correctly. The
// assertion is therefore on what the failure says, not on the fact of it.
func TestALineTheDecomposerCannotReadNamesTheDecomposer(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		// section is used instead of body for a shape no fragment can produce.
		section string
		// wantIn is what the message has to say. Asserting only that it mentions
		// the decomposer is not enough: swapping the two diagnoses for each other
		// leaves that assertion green, and a message describing the wrong cause is
		// the failure this change is about.
		wantIn []string
		// wantNotIn is a cause the message must NOT claim. A positive assertion
		// alone passes on the wording that was wrong.
		wantNotIn []string
	}{
		{
			name:   "a second paragraph at column zero",
			body:   "- One.\n\nTwo, at the margin.\n",
			wantIn: []string{"cannot classify", `"Two, at the margin."`, "Inside an entry"},
		},
		{
			name:   "a continuation indented by one space",
			body:   "- One.\n Two, one space in.\n",
			wantIn: []string{"cannot classify", `" Two, one space in."`, "indented by two"},
		},
		{
			name:   "a `### ` line inside an entry",
			body:   "- One.\n\n### Note\n\n  Two.\n",
			wantIn: []string{"cannot classify", `"### Note"`, "Inside an entry"},
		},
		{
			name:   "a `#### ` line inside an entry",
			body:   "- One.\n\n#### Note\n\n  Two.\n",
			wantIn: []string{"cannot classify", `"#### Note"`, "Inside an entry"},
		},
		{
			// The assembler cannot produce this, so it cannot come from a fragment —
			// but it is in the published 0.1.0 section, and a hand-written line of
			// introduction in a future one would put it back.
			name:    "prose before the first entry of a section",
			section: "## [1.0.0] - 2020-02-02\n\nFirst tagged release.\n\n### Fixed\n\n- One.\n\n",
			wantIn:  []string{"cannot classify", `"First tagged release."`, "above the first"},
		},
		{
			// The same line under the SECOND heading. Counting entries across the
			// whole section rather than the one heading called this "inside an entry"
			// — the wrong cause, which is the defect this change is about.
			name: "prose under a later heading, after an entry of another type",
			section: "## [1.0.0] - 2020-02-02\n\n### Added\n\n- One.\n\n" +
				"### Fixed\n\nSome prose.\n\n- Two.\n\n",
			wantIn: []string{"start of a section", `"### Fixed"`, `"Some prose."`},
		},
		{
			// The same shape as the `### Note` case, but the stray heading names the
			// type it is already under. Deciding it by whether a file of that name
			// happened to exist read this one as a real heading and glued the entries
			// together in silence — and the round trip then blamed the assembler.
			name:   "a `### ` line inside an entry naming the type it is already under",
			body:   "- One.\n\n### Fixed\n\n  Two.\n",
			wantIn: []string{"cannot classify", `"### Fixed"`, "Inside an entry"},
		},
		{
			// A heading with nothing under it. It used to vanish, taking the failure
			// to the assembler.
			name:    "a heading with no entries under it",
			section: "## [1.0.0] - 2020-02-02\n\n### Added\n\n- One.\n\n### Security\n\n",
			wantIn:  []string{"start of a section", `"### Security"`},
		},
		{
			// The same thing where the next heading, rather than the end of the
			// section, is what arrives. Catching only the end of the section would
			// leave this one to vanish.
			name:    "a heading with the next heading directly under it",
			section: "## [1.0.0] - 2020-02-02\n\n### Added\n\n### Fixed\n\n- One.\n\n",
			wantIn:  []string{"start of a section", `"### Added"`, `"### Fixed"`},
		},
		{
			// An indented line under a heading, before its first entry. The same
			// shape under the FIRST heading of a section used to be diagnosed
			// differently from the same shape under a later one.
			name: "an indented line under a later heading, before its first entry",
			section: "## [1.0.0] - 2020-02-02\n\n### Added\n\n- One.\n\n" +
				"### Fixed\n\n  Indented prose.\n\n- Two.\n\n",
			wantIn: []string{"start of a section", `"### Fixed"`, `"  Indented prose."`},
		},
		{
			// The assembler writes entries of one type back to back. A blank line
			// between two of them cannot have come from it — and 0.5.0 has one, so
			// this is not a shape nobody writes.
			name:    "a blank line between two entries",
			section: "## [1.0.0] - 2020-02-02\n\n### Added\n\n- One.\n\n- Two.\n\n",
			wantIn:  []string{"1 blank line(s) where the assembler writes 0", `"- Two."`},
		},
		{
			// One blank line separates one type's section from the next; a second
			// one cannot have come from the assembler either.
			name: "two blank lines before a heading",
			section: "## [1.0.0] - 2020-02-02\n\n### Added\n\n- One.\n\n\n" +
				"### Fixed\n\n- Two.\n\n",
			wantIn: []string{"2 blank line(s) where the assembler writes 1", `"### Fixed"`},
		},
		{
			// A `### ` line whose next line IS an entry. Deciding headings by what
			// follows them read this as a real heading and split one entry into two
			// in silence; deciding by what the assembler could have written here does
			// not, because it writes each type once, in order.
			name:   "a `### ` line inside an entry, with an entry under it",
			body:   "- One.\n\n### Fixed\n\n- Two.\n",
			wantIn: []string{"cannot classify", `"### Fixed"`, "Inside an entry"},
		},
		{
			// The same with a type that would come EARLIER in the order. The
			// assembler never goes back, so this is not a heading either — and
			// reading it as one reordered the rebuilt section.
			name:   "a `### ` line inside an entry naming an earlier type",
			body:   "- One.\n\n### Added\n\n- Two.\n",
			wantIn: []string{"cannot classify", `"### Added"`, "Inside an entry"},
		},
		{
			// Spelled with two spaces. The assembler writes `### Fixed` and nothing
			// else, so this line is not one it could have written — rebuilding a
			// section from it would silently correct the spacing.
			name: "a heading spelled differently from the way the assembler spells it",
			section: "## [1.0.0] - 2020-02-02\n\n### Added\n\n- One.\n\n" +
				"###  Fixed\n\n- Two.\n\n",
			wantIn: []string{"cannot classify", `"###  Fixed"`, "Inside an entry"},
		},
		{
			// Spelled with something after the type name. Matching only the start of
			// the line would take this for a heading.
			name: "a heading with something after the type name",
			section: "## [1.0.0] - 2020-02-02\n\n### Added\n\n- One.\n\n" +
				"### Fixedly\n\n- Two.\n\n",
			wantIn: []string{"cannot classify", `"### Fixedly"`, "Inside an entry"},
		},
		{
			// A line of nothing but spaces where the assembler writes an empty one.
			// Structure, so nothing carries those characters and rebuilding would
			// quietly replace them. Inside an entry the same line is a paragraph
			// break and is kept — which is why this is decided where the line is
			// used and not by what it looks like.
			name:    "a line of spaces where a blank line belongs",
			section: "## [1.0.0] - 2020-02-02\n\n### Added\n \n- One.\n\n",
			wantIn:  []string{"line of whitespace where the assembler writes", `" "`},
		},
		{
			// A section with nothing in it decomposes into no fragments, and the
			// ratchet that reads a published section skips when it gets none — so
			// this shape would take the check out rather than fail it.
			name:    "a section with no heading at all",
			section: "## [1.0.0] - 2020-02-02\n\n",
			wantIn:  []string{"no `### ` heading"},
		},
		{
			// One blank line closes the section. A second one would not come back.
			name:    "an extra blank line at the end of the section",
			section: "## [1.0.0] - 2020-02-02\n\n### Added\n\n- One.\n\n\n",
			wantIn:  []string{"3 blank line(s) where the assembler writes 2", "end of the section"},
		},
		{
			// A second section heading BEFORE the first `### `. Checking only after a
			// heading had opened let this one through, dropping the line.
			name: "a second section heading above the first `### `",
			section: "## [1.0.0] - 2020-02-02\n\n## [9.9.9] - 2019-01-01\n\n" +
				"### Changed\n\n- One.\n\n",
			wantIn: []string{"more than one section", `"## [9.9.9] - 2019-01-01"`},
		},
		{
			// A `## [` line that is not a version heading, inside an entry. Matching
			// every `## [` line called this "more than one section", which it is not
			// — and a fragment body may hold one, since Load only refuses `## [0-9`.
			name:      "a `## [` line inside an entry that is not a version heading",
			body:      "- One.\n\n## [Notes]\n\n  Two.\n",
			wantIn:    []string{"cannot classify", `"## [Notes]"`, "Inside an entry"},
			wantNotIn: []string{"more than one section"},
		},
		{
			// A `## ` line that is not a section heading. Matching every `## ` line
			// called this "more than one section", which it is not.
			name:   "a `## ` line inside an entry",
			body:   "- One.\n\n## Notes\n\n  Two.\n",
			wantIn: []string{"cannot classify", `"## Notes"`, "Inside an entry"},
		},
		{
			// A heading with no title at all. Reading the type as the empty string
			// wound the decomposer back to the top of the section and dropped the
			// line.
			name:   "a `### ` line with no title",
			body:   "- One.\n\n### \n\n  Two.\n",
			wantIn: []string{"cannot classify", `"### "`, "Inside an entry"},
		},
		{
			// A type name that is not a type, and would be a path if it were written
			// out as a file. It must not reach the filesystem.
			name:   "a `### ` line naming something with a slash in it",
			body:   "- One.\n\n### Foo/Bar\n\n  Two.\n",
			wantIn: []string{"cannot classify", `"### Foo/Bar"`, "Inside an entry"},
		},
		{
			// Above the first heading, an entry is as unplaceable as prose — and the
			// message must not call it prose.
			name:      "an entry above the first heading",
			section:   "## [1.0.0] - 2020-02-02\n\n- Stray bullet.\n\n### Fixed\n\n- One.\n\n",
			wantIn:    []string{"cannot classify", `"- Stray bullet."`, "above the first"},
			wantNotIn: []string{"prose"}, // it is a bullet
		},
		{
			// Reading past the end of one section. It is not a shape in a changelog,
			// it is the wrong input — and saying so beats describing it as a broken
			// entry, which is what the footer's link definitions used to get.
			name: "text from past the end of the section",
			section: "## [1.0.0] - 2020-02-02\n\n### Added\n\n- One.\n\n" +
				"## [0.9.0] - 2019-01-01\n\n### Fixed\n\n- Old.\n\n",
			wantIn: []string{"more than one section", `"## [0.9.0] - 2019-01-01"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			section := tc.section
			if section == "" {
				// Assembled here rather than through Load and Release. These shapes
				// are ones a fragment may no longer hold — Load refuses a line at the
				// left margin that is not the next entry — but a published section can
				// still be found holding one, by a hand or by an older release, and
				// that is the case the decomposer has to report on.
				section = "## [9.9.9] - 2026-01-01\n\n### Fixed\n\n" + tc.body + "\n"
			}
			_, err := explode(t, section)
			if err == nil {
				t.Fatal("the decomposer read a shape it does not support without saying so")
			}
			// The point of the whole change: whose fault the message says it is.
			if !strings.Contains(err.Error(), "the decomposer") {
				t.Errorf("the failure does not name the decomposer:\n%v", err)
			}
			if strings.Contains(err.Error(), "no such file") {
				t.Errorf("the failure blames the filesystem:\n%v", err)
			}
			for _, want := range tc.wantIn {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the failure does not say %q, so it is describing some other cause:\n%v", want, err)
				}
			}
			for _, dont := range tc.wantNotIn {
				if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(dont)) {
					t.Errorf("the failure says %q, which is not what this line is:\n%v", dont, err)
				}
			}
		})
	}
}

// Everything the assembler can produce has to come back from the decomposer
// unchanged. The written-out shapes below are the ones somebody thought of; this
// is the whole space instead — every non-empty combination of the six types, each
// holding one or two entries, drawn from a set of entry shapes.
//
// It guards the direction the cell-by-cell tests do not: those say that what the
// assembler cannot write is refused, and this says that what it can write is not.
// A rule tightened far enough to refuse a real section would pass every one of
// them and fail here.
func TestEveryShapeTheAssemblerCanProduceSurvivesTheRoundTrip(t *testing.T) {
	shapes := []string{
		"- One line.\n",
		"- An entry that runs on and\n  wraps onto a second line.\n",
		"- First paragraph.\n\n  Second paragraph.\n",
		"- One.\n\n\n  Two blank lines in between.\n",
		// A line of nothing but a space, inside an entry. Load trims a fragment at
		// its ends only, so this one survives being read and the assembler writes it
		// back out — it is a shape the assembler produces, however odd it looks.
		"- One.\n \n  A line of one space in between.\n",
	}
	combos := 0
	used := make([]bool, len(shapes))
	for mask := 1; mask < 1<<len(changelog.Types); mask++ {
		for _, two := range []bool{false, true} {
			dir := t.TempDir()
			k := 0
			for i, typ := range changelog.Types {
				if mask&(1<<i) == 0 {
					continue
				}
				// How many entries this type gets, and which shapes, both vary with
				// the type — a run where every type looks the same would not show a
				// section built from unlike ones.
				count := 1
				if two {
					count = 1 + (i+mask)%3
				}
				for j := 0; j < count; j++ {
					k++
					pick := (i + j + mask) % len(shapes)
					used[pick] = true
					write(t, dir, fmt.Sprintf("%03d-e.%s.md", k, typ), shapes[pick])
				}
			}
			frags, err := changelog.Load(dir)
			if err != nil {
				t.Fatalf("mask %b: loading: %v", mask, err)
			}
			combos++
			section, rebuilt, err := roundTrip(t, frags)
			if err != nil {
				t.Errorf("mask %b (%d entries per type): the decomposer refused a section the assembler wrote:\n%v\n--- section ---\n%s",
					mask, 1+b2i(two), err, section)
				continue
			}
			if rebuilt != section {
				t.Errorf("mask %b: the round trip changed a section the assembler wrote.\n--- assembled ---\n%s\n--- after ---\n%s",
					mask, section, rebuilt)
			}
		}
	}
	// Asserting the premise: a loop that silently ran zero combinations would pass,
	// and so would one that never reached half of the shapes it was given.
	if want := 2 * ((1 << len(changelog.Types)) - 1); combos != want {
		t.Errorf("ran %d combinations, want %d", combos, want)
	}
	for i := range shapes {
		if !used[i] {
			t.Errorf("shape %d was never used, so nothing here says anything about it:\n%q", i, shapes[i])
		}
	}
}

// bumpDigit changes one digit of s — the first or the last — leaving every other
// byte alone. A version or a date stays the shape it was, which is what lets an
// edit reach past the check on that shape to the contents underneath.
func bumpDigit(s string, first bool) string {
	r := []rune(s)
	for i := range r {
		j := i
		if !first {
			j = len(r) - 1 - i
		}
		if r[j] >= '0' && r[j] <= '9' {
			if r[j] == '9' {
				r[j] = '8'
			} else {
				r[j]++
			}
			return string(r)
		}
	}
	return s
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// The decomposer is allowed to refuse a section. It is not allowed to accept one
// and hand back something else — a section that goes through and comes out
// different is the failure every case above is an instance of, and it surfaces as
// the assembler changing a changelog it assembled correctly.
//
// The cases above are the near misses somebody thought of. This is the same thing
// enumerated: take what the assembler writes and break it in one place, in every
// way and at every position, and require refusal or an exact round trip. It exists
// because the cases above were found one at a time, over several reviews, and the
// last one found was not a new cell in the table being worked through — it was a
// column the table did not have. Whether an enumeration is complete cannot be
// checked from inside it.
func TestBreakingASectionAnywhereIsRefusedOrSurvivesExactly(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "001-e.added.md", "- One line.\n")
	write(t, dir, "002-e.added.md", "- First paragraph.\n\n  Second paragraph.\n")
	write(t, dir, "003-e.fixed.md", "- An entry that runs on and\n  wraps onto a second line.\n")
	frags, err := changelog.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	whole, _, err := roundTrip(t, frags)
	if err != nil {
		t.Fatalf("the assembler's own output was refused: %v", err)
	}
	lines := strings.Split(whole, "\n")

	// One edit, applied at one place. Each is something a hand could do to a
	// published section without it looking any different in Markdown.
	edits := []struct {
		name string
		at   func(i int, lines []string) []string
	}{
		{"a blank line inserted", func(i int, l []string) []string {
			return append(append(append([]string{}, l[:i]...), ""), l[i:]...)
		}},
		{"the line removed", func(i int, l []string) []string {
			return append(append([]string{}, l[:i]...), l[i+1:]...)
		}},
		{"the line duplicated", func(i int, l []string) []string {
			return append(append(append([]string{}, l[:i]...), l[i]), l[i:]...)
		}},
		{"a space added to the end of the line", func(i int, l []string) []string {
			out := append([]string{}, l...)
			out[i] += " "
			return out
		}},
		{"a space added to the front of the line", func(i int, l []string) []string {
			out := append([]string{}, l...)
			out[i] = " " + out[i]
			return out
		}},
		{"a character changed", func(i int, l []string) []string {
			out := append([]string{}, l...)
			if r := []rune(out[i]); len(r) > 0 {
				r[len(r)/2] = 'x'
				out[i] = string(r)
			}
			return out
		}},
		// A digit is the one edit that leaves the SHAPE of the heading alone — every
		// other edit here breaks it, and a broken shape is refused before anything
		// under it is looked at. Taken from the line rather than written out, so
		// renaming the fixture's version cannot quietly empty this out.
		//
		// What it establishes is narrower than "the version and the date are checked
		// too", which is what this said before and is not true: a fragment does not
		// carry either of them, so the decomposer cannot alter them and the
		// comparison cannot catch it doing so — it rebuilds the heading from THIS
		// input, and comes out equal whatever the digits are (measured). What is
		// checked is that a heading is still ACCEPTED when its contents differ, and
		// that the assembler spells it back the way it was given.
		{"its first digit changed", func(i int, l []string) []string {
			out := append([]string{}, l...)
			out[i] = bumpDigit(out[i], true)
			return out
		}},
		{"its last digit changed", func(i int, l []string) []string {
			out := append([]string{}, l...)
			out[i] = bumpDigit(out[i], false)
			return out
		}},
	}

	checked, compared := 0, 0
	comparedAt := make([]int, len(lines))
	for _, e := range edits {
		for i := range lines {
			broken := strings.Join(e.at(i, lines), "\n")
			if broken == whole {
				continue // the edit changed nothing here
			}
			checked++
			t.Run(fmt.Sprintf("%s at line %d", e.name, i+1), func(t *testing.T) {
				exploded, err := explode(t, broken)
				if err != nil {
					return // refused, which is one of the two allowed answers
				}
				refrags, err := changelog.Load(exploded)
				if err != nil {
					return // refused one step later, by the fragment reader
				}
				// Assembled from the heading THIS input carries, not from constants:
				// a version or a date the decomposer quietly replaced would otherwise
				// be replaced on both sides and never show up.
				// Unreachable: explode requires the first line to be a section heading
				// and refuses any later one, so a section it accepted has exactly one.
				heads := releaseHeadings(broken)
				if len(heads) != 1 {
					t.Fatalf("explode accepted a section with %d headings", len(heads))
				}
				out, err := changelog.Release(emptyChangelog, refrags, heads[0].version, heads[0].date)
				if err != nil {
					t.Fatalf("reassembling: %v", err)
				}
				h := releaseHeadings(out)
				compared++
				comparedAt[i]++
				if rebuilt := out[h[0].at:h[1].at]; rebuilt != broken {
					t.Errorf("the decomposer accepted this and gave back something else.\n"+
						"Refusing it is fine; changing it silently is what makes a release blame\n"+
						"the assembler.\n--- given ---\n%q\n--- came back ---\n%q", broken, rebuilt)
				}
			})
		}
	}
	// Asserting the premise, and asserting the right half of it. Counting the
	// edits that were TRIED says nothing: a decomposer that refused everything
	// would try them all and compare none, and this would stay green while
	// checking nothing at all. What matters is how many got as far as being
	// compared.
	if want := len(edits) * len(lines) / 2; checked < want {
		t.Errorf("only %d edits were tried, which is too few to have covered %d lines", checked, len(lines))
	}
	// Structural lines — the headings and the blanks around them — are refused
	// rather than compared, by design, so this is not one comparison per line. It
	// is a floor under the total: measured, an unbroken run compares about 25 of
	// 100 edits, and a decomposer that refused everything would compare none.
	if compared < len(lines) {
		t.Errorf("only %d of %d edits were accepted and compared; the rest were refused, so "+
			"this checked far less than it looks like", compared, checked)
	}
	// And the heading line in particular. Every edit to it but the digit ones is
	// refused, so if those stop being accepted the sweep stops saying anything at
	// all about that line — including that a heading with different contents is
	// taken rather than turned away.
	if comparedAt[0] == 0 {
		t.Error("no edit to the section heading was accepted and compared, so nothing here " +
			"says the heading line is read at all")
	}
}

// Every fragment waiting in changelog.d has to survive the round trip, checked
// here rather than at the release that first assembles it.
//
// The shapes written out below are the ones anybody thought to write down. This
// is the other half: whatever is actually in the tree right now, whether or not
// it resembles them. A fragment using a shape the decomposer cannot read fails in
// the pull request that adds it, naming the file — rather than months later,
// during a release, as a byte difference in a section nobody has touched.
func TestEveryFragmentWaitingToShipSurvivesTheRoundTrip(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "changelog.d")
	// Asserting the premise: with nothing to carry, the round trip proves nothing,
	// and a skip says so rather than passing.
	frags, err := changelog.Load(dir)
	if err != nil {
		t.Fatalf("reading changelog.d: %v", err)
	}
	if len(frags) == 0 {
		t.Skip("changelog.d is empty, which it is right after a release")
	}
	if err := fragmentsRoundTrip(t, dir); err != nil {
		t.Error(err)
	}
}

// Whether the failure above names the file, checked on a directory this test
// controls — the one it actually runs against is expected to be clean, so it
// could not tell the difference between a message that names the fragment and
// one that leaves the reader to find it.
func TestAFragmentTheDecomposerCannotReadIsNamed(t *testing.T) {
	dir := t.TempDir()
	// A shape Load takes and the decomposer does not: two entries in one fragment
	// with a blank line between them, where the assembler writes none. A line at
	// the left margin would be refused by Load first, one step before the decomposer
	// is reached, and would say nothing about which of the two spoke.
	write(t, dir, "999-probe.added.md", "- One.\n\n\n- Two.\n")
	err := fragmentsRoundTrip(t, dir)
	if err == nil {
		t.Fatal("a fragment the decomposer cannot read went through the round trip")
	}
	if !strings.Contains(err.Error(), "999-probe.added.md") {
		t.Errorf("the failure does not name the fragment to open:\n%v", err)
	}
	// Without this, dropping the line silently again leaves the test green: the
	// round trip still differs, and that failure names the fragments too.
	if !strings.Contains(err.Error(), "holds a shape the decomposer cannot read") {
		t.Errorf("the failure is the round trip noticing a difference, not the decomposer refusing:\n%v", err)
	}
}

// fragmentsRoundTrip runs every fragment in dir through the round trip, naming
// the fragments in whatever it reports so the reader has a file to open.
func fragmentsRoundTrip(t *testing.T, dir string) error {
	t.Helper()
	frags, err := changelog.Load(dir)
	if err != nil {
		return fmt.Errorf("reading %s: %w", dir, err)
	}
	names := make([]string, len(frags))
	for i, f := range frags {
		names[i] = filepath.Base(f.Path)
	}
	section, rebuilt, err := roundTrip(t, frags)
	if err != nil {
		return fmt.Errorf("one of %s holds a shape the decomposer cannot read:\n%w",
			strings.Join(names, ", "), err)
	}
	if rebuilt != section {
		return fmt.Errorf("the round trip changed the section assembled from %s.\n"+
			"One of these fragments uses a shape the round trip does not carry.\n"+
			"--- assembled ---\n%s\n--- after a round trip ---\n%s",
			strings.Join(names, ", "), section, rebuilt)
	}
	return nil
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
		// A fragment is trimmed at its ends only, so a line of spaces in the middle
		// of one is carried through and written back out. It looks like structure
		// and is not.
		{name: "a line of spaces inside an entry", body: "- One.\n \n  Two.\n"},
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
			section, rebuilt, err := roundTrip(t, frags)
			if err != nil {
				t.Fatal(err)
			}
			if rebuilt != section {
				t.Errorf("the round trip changed the section.\n--- first ---\n%s\n--- second ---\n%s",
					section, rebuilt)
			}
		})
	}
}
