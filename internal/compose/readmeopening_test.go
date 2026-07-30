package compose

// Opening ratchet: the first screen of each README is load-bearing for
// discovery, and it was wrong for most of this project's life. The file began
// with a banner image, so neither a search engine nor GitHub's own search saw a
// single keyword in the most important position on the page. And what prose there
// was opened by naming the product — which is the one thing a person with this
// problem is not searching for, and the one thing an AI reading round the subject
// cannot match on.
//
// Both properties are mechanical, so they are checked rather than remembered.
// This matters most for the planned README split: a rewrite that drops the
// heading or leads with "opossum is a…" would undo the fix silently.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// readmeH1AndOpening returns a file's single text H1 and the first paragraph of
// prose after it, with markup reduced to plain text. Fenced blocks are skipped:
// a shell comment inside an example is not a heading.
func readmeH1AndOpening(t *testing.T, path string) (h1, opening string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var h1s []string
	var prose []string
	inFence, afterH1 := false, false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		switch {
		case strings.HasPrefix(line, "# "):
			h1s = append(h1s, strings.TrimSpace(strings.TrimPrefix(line, "# ")))
			afterH1 = true
		case strings.HasPrefix(line, "#"):
			afterH1 = false // a later heading ends the opening paragraph
		case afterH1 && line == "" && len(prose) > 0:
			afterH1 = false // blank line after some prose: paragraph over
		case afterH1 && line != "" && !strings.HasPrefix(line, "<") && !strings.HasPrefix(line, ">"):
			prose = append(prose, line)
		}
	}
	if len(h1s) != 1 {
		t.Fatalf("%s should have exactly one text H1 — it is the most important place on the "+
			"page for someone searching for what this does, and a banner image is not indexable "+
			"text. Found %d: %v", filepath.Base(path), len(h1s), h1s)
	}
	return h1s[0], plainText(strings.Join(prose, " "))
}

// plainText strips the markup that would otherwise land inside the first 200
// characters an AI or a search result quotes.
func plainText(s string) string {
	s = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`).ReplaceAllString(s, "$1")
	s = strings.NewReplacer("`", "", "*", "", "—", "-").Replace(s)
	return strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(s, " "))
}

func TestReadmeOpensWithTheProblemNotTheProductName(t *testing.T) {
	for _, tc := range []struct {
		file string
		// keywords the H1 has to carry: the long-tail phrasing someone with this
		// problem actually types. The product name alone loses to an unrelated,
		// far more popular package of the same name, so the heading cannot rely on it.
		h1Keywords []string
	}{
		{"README.md", []string{"docker compose", "container"}},
		{"README.ja.md", []string{"docker compose", "container"}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			h1, opening := readmeH1AndOpening(t, filepath.Join("..", "..", tc.file))
			lowerH1 := strings.ToLower(h1)
			for _, kw := range tc.h1Keywords {
				if !strings.Contains(lowerH1, kw) {
					t.Errorf("the H1 should say what this does in the words people search for; "+
						"%q is missing %q", h1, kw)
				}
			}
			if opening == "" {
				t.Fatal("there is no prose after the H1 — the opening paragraph is the snippet " +
					"search results and AI answers quote")
			}
			// The first thing said must be the problem. Someone who already knows the
			// product name is not the reader this paragraph is for.
			if strings.HasPrefix(strings.ToLower(opening), "opossum") {
				t.Errorf("the opening should declare the problem before naming the product — an "+
					"answer that starts with a name nobody searched for is not quotable. Got: %q",
					firstN(opening, 120))
			}
			// The problem, and the fact that this addresses it, both have to fit in the
			// window a search result or an AI answer actually shows.
			const window = 200
			head := strings.ToLower(firstN(opening, window))
			if !strings.Contains(head, "opossum") {
				t.Errorf("the first %d characters should get as far as what this project does, "+
					"not just the problem. Got: %q", window, firstN(opening, window))
			}
		})
	}
}

// firstN returns the first n characters — runes, not bytes. The window this
// stands for is what a search result or an AI answer shows, which is counted in
// characters; measuring the Japanese README in bytes would shrink its window to a
// third of the English one.
func firstN(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
