package compose

// Figure ratchet: the compatibility numbers are opossum's headline claim, and a
// percentage is the fastest thing in a repository to go quietly stale. They live
// in one place — docs/compatibility.md, next to the method that produced them —
// and each README quotes them. This test is what makes "quotes" true: re-measure,
// update the source table, and the READMEs have to follow in the same commit.
//
// It compares the numbers, not the sentence. Prose differs between the two
// languages (and should), but "61 (39%)" cannot become "62 (40%)" in one file
// alone, and a README cannot drop the claim while the source keeps promising it.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	// The block each README wraps its quoted figures in. Markers rather than a
	// heading: the claim is a sentence in the opening prose, and the split-README
	// work will move it around.
	compatOpen  = "<!-- compat-figures -->"
	compatClose = "<!-- /compat-figures -->"
	// The section of docs/compatibility.md that is the source of truth.
	compatSection = "## Figures"
)

var intPattern = regexp.MustCompile(`\d+`)

// numbersIn returns every integer in s, sorted, so two texts can be compared as
// multisets regardless of how each language words the sentence around them.
func numbersIn(s string) []string {
	found := intPattern.FindAllString(s, -1)
	sort.Strings(found)
	return found
}

// figuresFromSource reads the canonical table out of docs/compatibility.md.
func figuresFromSource(t *testing.T, root string) []string {
	t.Helper()
	path := filepath.Join(root, "docs", "compatibility.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	i := strings.Index(string(data), compatSection)
	if i < 0 {
		t.Fatalf("%s must have a %q section — it is where the figures the READMEs quote "+
			"are defined", path, compatSection)
	}
	rest := string(data)[i+len(compatSection):]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		rest = rest[:j] // this section only: the later tables have numbers of their own
	}
	var rows []string
	for _, line := range strings.Split(rest, "\n") {
		line = strings.TrimSpace(line)
		// Table rows, minus the separator row (|---|---|), which is all punctuation.
		if strings.HasPrefix(line, "|") && strings.ContainsAny(line, "0123456789") {
			rows = append(rows, line)
		}
	}
	if len(rows) < 2 {
		t.Fatalf("the %q section of %s should be a table of figures, found %d rows with "+
			"numbers in them", compatSection, path, len(rows))
	}
	return numbersIn(strings.Join(rows, " "))
}

// quotedFigures returns the numbers a README quotes, and fails if the block is
// missing — a README that simply dropped the claim would otherwise pass.
func quotedFigures(t *testing.T, root, file string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	s := string(data)
	i := strings.Index(s, compatOpen)
	j := strings.Index(s, compatClose)
	if i < 0 || j < i {
		t.Fatalf("%s must quote the measured compatibility figures between %s and %s. "+
			"They are the one claim that says why to choose this over the alternatives, "+
			"and they have to be in the first screen", file, compatOpen, compatClose)
	}
	return numbersIn(s[i+len(compatOpen) : j])
}

func TestReadmesQuoteTheMeasuredFigures(t *testing.T) {
	root := filepath.Join("..", "..")
	want := figuresFromSource(t, root)
	for _, file := range []string{"README.md", "README.ja.md"} {
		t.Run(file, func(t *testing.T) {
			got := quotedFigures(t, root, file)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("%s quotes %v, but docs/compatibility.md measures %v — re-measuring "+
					"means updating the source table and every README that quotes it, in the "+
					"same commit", file, got, want)
			}
		})
	}
}
