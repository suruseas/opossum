package repohygiene_test

// A public document names no issue by number. The README, AGENTS.md, the
// CHANGELOG and its fragments, CONTRIBUTING.md and everything under docs/
// ship with the product, and a reader there cannot open `#598`: the number
// points into a repository they do not have. Twice a sentence in a public
// file cited one (#600, found in review), and a released CHANGELOG section
// is the published record — it cannot be corrected afterwards. Code
// comments are a different reader and keep their numbers.
//
// This is a ratchet: the references already there are counted per file in
// a baseline and tolerated; a file that gains one, or a new file with any,
// is refused. Removing one shrinks the baseline (a count that no longer
// holds is reported, so the baseline is kept true).

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// issueNumber is `#` and digits standing on their own: not a URL fragment
// (`/x#123` has a slash before it), not an HTML entity (`&#123;`), not a
// heading (`# 1.`) and not part of a word (`abc#1`).
var issueNumber = regexp.MustCompile(`(^|[^A-Za-z0-9/&\\])#[0-9]+\b`)

const issueNumberBaseline = "testdata/issue-numbers-baseline.txt"

// publicDocuments lists the tracked files a user of the product reads.
func publicDocuments(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "-z", "--", "README.md", "README.ja.md", "AGENTS.md", "CONTRIBUTING.md", "CHANGELOG.md", "changelog.d", "docs")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var files []string
	for _, f := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if strings.HasSuffix(f, ".md") {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	return files
}

// issueNumbersIn counts the references per public document, with the first
// few lines each sits on, for the message.
func issueNumbersIn(t *testing.T, root string) (counts map[string]int, where map[string][]string) {
	t.Helper()
	counts, where = map[string]int{}, map[string][]string{}
	for _, f := range publicDocuments(t, root) {
		fh, err := os.Open(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for n := 1; sc.Scan(); n++ {
			if m := issueNumber.FindAllString(sc.Text(), -1); len(m) > 0 {
				counts[f] += len(m)
				if len(where[f]) < 3 {
					where[f] = append(where[f], f+":"+strconv.Itoa(n)+": "+strings.TrimSpace(sc.Text()))
				}
			}
		}
		fh.Close()
	}
	return counts, where
}

func readIssueNumberBaseline(t *testing.T) map[string]int {
	t.Helper()
	b, err := os.ReadFile(issueNumberBaseline)
	if err != nil {
		t.Fatalf("the baseline %s is missing: %v", issueNumberBaseline, err)
	}
	base := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f, n, ok := strings.Cut(line, " ")
		c, err := strconv.Atoi(strings.TrimSpace(n))
		if !ok || err != nil {
			t.Fatalf("baseline line %q: want `<path> <count>`", line)
		}
		base[f] = c
	}
	return base
}

func TestAPublicDocumentNamesNoNewIssueByNumber(t *testing.T) {
	root := repoRoot(t)
	counts, where := issueNumbersIn(t, root)
	base := readIssueNumberBaseline(t)
	var files []string
	for f := range counts {
		files = append(files, f)
	}
	for f := range base {
		files = append(files, f)
	}
	sort.Strings(files)
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f] {
			continue
		}
		seen[f] = true
		got, had := counts[f], base[f]
		switch {
		case got > had:
			t.Errorf("%s names %d issue(s) by number, %d more than the baseline allows — a reader of the shipped product cannot open `#NNN`; say what the reference says instead (first ones):\n  %s",
				f, got, got-had, strings.Join(where[f], "\n  "))
		case got < had:
			t.Errorf("%s names %d issue(s) by number, fewer than the baseline's %d — lower the count in %s so the baseline stays true", f, got, had, issueNumberBaseline)
		}
	}
}

// The pattern itself: what counts as a reference and what does not, so a
// change to it is measured here rather than discovered on the ratchet.
func TestWhatCountsAsAnIssueNumber(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int
	}{
		{"fixed in #598", 1},
		{"(#421 → #477)", 2},
		{"see #10.", 1},
		{"名前(#9)", 1},
		{"https://example.com/page#123", 0},
		{"&#123;", 0},
		{"# 1. Heading", 0},
		{"abc#12", 0},
		{"C#11 is a language", 0},
		{"#L10 is a line anchor", 0},
		{"issue 598", 0},
	} {
		if got := len(issueNumber.FindAllString(tc.text, -1)); got != tc.want {
			t.Errorf("%q: %d references, want %d", tc.text, got, tc.want)
		}
	}
}
