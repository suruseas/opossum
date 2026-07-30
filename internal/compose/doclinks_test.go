package compose

// Link ratchet: every relative link between the project's documents resolves,
// and every `#anchor` names a heading that exists.
//
// This exists because the documentation was split: what used to be one README
// with in-page anchors is now a README plus half a dozen pages under docs/, and
// a moved section leaves behind a link that still looks right. Ten were broken
// the first time the split was assembled — including one in an example's README
// pointing at a section that had left the file entirely. A reader finds those one
// at a time; a test finds all of them at once.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	docLinkRE    = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)\)`)
	docHeadingRE = regexp.MustCompile(`^#{1,6}\s+(.*)$`)
	docLinkText  = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	docPunct     = regexp.MustCompile(`[^\w\s-]`)
	docSpaces    = regexp.MustCompile(`\s+`)
)

// docsSkipped are drafts, not published documents: they carry deliberate
// placeholder targets (`URL1`, an image added at publishing time) that are filled
// in outside this repository. Scanning them would mean either a permanently red
// test or teaching the checker to ignore particular strings, and neither says
// anything about whether the project's own documentation hangs together.
var docsSkipped = map[string]bool{"articles": true}

// blankFences replaces the contents of fenced code blocks with empty lines, so a
// scan of the whole file sees no example code but still counts lines correctly.
func blankFences(s string) string {
	var out []string
	inFence := false
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			out = append(out, "")
			continue
		}
		if inFence {
			out = append(out, "")
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// headingSlug renders a heading the way GitHub does when it builds an anchor.
func headingSlug(h string) string {
	h = docLinkText.ReplaceAllString(h, "$1")
	h = strings.NewReplacer("`", "", "*", "", "_", "").Replace(h)
	h = docPunct.ReplaceAllString(strings.ToLower(strings.TrimSpace(h)), "")
	return docSpaces.ReplaceAllString(h, "-")
}

// markdownAnchors returns every anchor a file offers, skipping fenced blocks so a
// shell comment is not mistaken for a heading.
func markdownAnchors(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	inFence := false
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if m := docHeadingRE.FindStringSubmatch(t); m != nil {
			out[headingSlug(m[1])] = true
		}
	}
	return out, nil
}

func TestDocumentLinksResolve(t *testing.T) {
	root := filepath.Join("..", "..")
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || docsSkipped[name] {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".md") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repository: %v", err)
	}
	// The split moved sections across six files; a walk that found almost nothing
	// would pass while checking nothing.
	if len(files) < 10 {
		t.Fatalf("only %d markdown files found — the walk is not doing its job", len(files))
	}

	anchors := map[string]map[string]bool{}
	checked := 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		// Scanned as one string, not line by line: link text wraps at the margin, so
		// `[measured\ncompatibility](docs/…)` is one link split over two lines and a
		// per-line scan simply does not see it. Fenced blocks are blanked rather than
		// dropped, to keep the line numbers in the failure message true.
		body := blankFences(string(data))
		for _, target := range docLinkRE.FindAllStringSubmatchIndex(body, -1) {
			link := body[target[2]:target[3]]
			n := strings.Count(body[:target[0]], "\n")
			{
				if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") ||
					strings.HasPrefix(link, "mailto:") {
					continue
				}
				checked++
				path, frag, _ := strings.Cut(link, "#")
				dest := file
				if path != "" {
					dest = filepath.Join(filepath.Dir(file), path)
				}
				if _, err := os.Stat(dest); err != nil {
					t.Errorf("%s:%d links to %q, which does not exist", file, n+1, link)
					continue
				}
				if frag == "" || !strings.HasSuffix(dest, ".md") {
					continue
				}
				if _, ok := anchors[dest]; !ok {
					a, err := markdownAnchors(dest)
					if err != nil {
						t.Fatalf("reading %s: %v", dest, err)
					}
					anchors[dest] = a
				}
				if !anchors[dest][strings.ToLower(frag)] {
					t.Errorf("%s:%d links to %q, but %s has no such heading — a section that "+
						"moved leaves a link that still looks right", file, n+1, link, dest)
				}
			}
		}
	}
	if checked < 50 {
		t.Fatalf("only %d relative links were checked — that is too few for this project's "+
			"documentation, so the scan is missing something", checked)
	}
}
