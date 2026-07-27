package compose

// Translation-freshness ratchet: README.ja.md is a faithful translation of
// README.md (the English file is canonical). Docs rot here looks like "the
// English README gained a section/table row/example and the Japanese one
// silently kept the old story". Prose can't be diffed across languages, but the
// document *skeleton* can: the sequence of heading levels, fenced code blocks
// (with their info strings — code examples are language-invariant), and table
// sizes must match line for line. This test fails when README.md changes shape
// without README.ja.md following (or vice versa), so a PR that edits one is
// forced to touch the other. Wording-only edits inside a section don't trip it.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readmeSkeleton reduces a markdown file to its structural outline: one token
// per heading ("h2", "h3", …), per fenced code block ("code:sh", "code:mermaid",
// "code:" for plain fences), and per table ("table:12" — row count including
// the header and separator rows). Everything inside fences except the opening
// info string is ignored.
func readmeSkeleton(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var skel []string
	inFence := false
	tableRows := 0
	flushTable := func() {
		if tableRows > 0 {
			skel = append(skel, fmt.Sprintf("table:%d", tableRows))
			tableRows = 0
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			flushTable()
			if !inFence {
				skel = append(skel, "code:"+strings.TrimPrefix(trimmed, "```"))
			}
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if strings.HasPrefix(trimmed, "|") {
			tableRows++
			continue
		}
		flushTable()
		if strings.HasPrefix(trimmed, "#") {
			level := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
			skel = append(skel, fmt.Sprintf("h%d", level))
		}
	}
	flushTable()
	return skel
}

func TestReadmeTranslationStructureInSync(t *testing.T) {
	en := readmeSkeleton(t, filepath.Join("..", "..", "README.md"))
	ja := readmeSkeleton(t, filepath.Join("..", "..", "README.ja.md"))
	for i := 0; i < len(en) && i < len(ja); i++ {
		if en[i] != ja[i] {
			t.Fatalf("README.md and README.ja.md diverge at structural element %d: English has %q, Japanese has %q "+
				"(context EN: %v / JA: %v) — README.ja.md is a translation of README.md, so a change to one file's "+
				"sections, tables, or code examples must be mirrored in the other in the same PR",
				i, en[i], ja[i], contextSlice(en, i), contextSlice(ja, i))
		}
	}
	if len(en) != len(ja) {
		t.Fatalf("README.md has %d structural elements (headings/tables/code blocks) but README.ja.md has %d — "+
			"README.ja.md is a translation of README.md, so added or removed sections, tables, and code examples "+
			"must be mirrored in the other file in the same PR (EN tail: %v / JA tail: %v)",
			len(en), len(ja), tailSlice(en, len(ja)), tailSlice(ja, len(en)))
	}
}

// contextSlice returns a few elements around index i for a readable failure message.
func contextSlice(s []string, i int) []string {
	lo, hi := i-2, i+3
	if lo < 0 {
		lo = 0
	}
	if hi > len(s) {
		hi = len(s)
	}
	return s[lo:hi]
}

// tailSlice returns the elements of the longer skeleton past the shorter one's length.
func tailSlice(s []string, from int) []string {
	if from >= len(s) {
		return nil
	}
	if end := from + 5; end < len(s) {
		return s[from:end]
	}
	return s[from:]
}
