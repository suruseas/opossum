package site_test

// Evals for the entry point an agent arriving at the site reads.
//
// The site publishes HTML for people. An agent that lands on it has nowhere to
// start: it can guess at the navigation, or it can read one file that says
// what is here and where the Markdown is. /llms.txt is that file, and what
// these rows hold is that it stays true as the site changes — a page added to
// the site and not to the index is a page an agent will not find.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/site"
)

// buildSite renders the real repository into a directory of this test's own and
// answers with what the site would publish. The repository is the input because
// the index is built from it: a fixture would say what the pages used to say.
func buildSite(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "site")
	// The repository itself, two levels up: the index is built from the
	// documents that are actually published, so a fixture would hold what
	// they used to say rather than what they say.
	if err := site.Build(filepath.Join("..", ".."), out); err != nil {
		t.Fatalf("building the site: %v", err)
	}
	return out
}

func readLLMs(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(buildSite(t), "llms.txt"))
	if err != nil {
		t.Fatalf("the site has no llms.txt, so an agent that arrives has nowhere to start: %v", err)
	}
	return string(b)
}

var llmsLinkRE = regexp.MustCompile(`^- \[([^\]]+)\]\(([^)]+)\): (.+)$`)

func TestTheAgentIndexNamesEveryPageTheSitePublishes(t *testing.T) {
	body := readLLMs(t)
	linked := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		if m := llmsLinkRE.FindStringSubmatch(line); m != nil {
			linked[m[2]] = true
		}
	}
	t.Run("every published page is in it", func(t *testing.T) {
		// The site's own list, not a copy of it: a page added there and
		// nowhere else is the failure this row is for.
		for _, page := range site.Pages {
			want := site.RawURL("docs/" + page + ".md")
			if !linked[want] {
				t.Errorf("the site publishes docs/%s.md and llms.txt does not link it (%s). An "+
					"agent reading the index would not find that page at all.\n%s", page, want, body)
			}
		}
	})
	t.Run("the README and AGENTS.md are in it", func(t *testing.T) {
		for _, repoPath := range []string{"README.md", "AGENTS.md"} {
			if want := site.RawURL(repoPath); !linked[want] {
				t.Errorf("llms.txt does not link %s (%s)", repoPath, want)
			}
		}
	})
	t.Run("one link is spelled out in full", func(t *testing.T) {
		// The rows above ask site.RawURL what the URL should be, so a
		// change to RawURL moves the answer and the question together: a
		// branch that is not there, or GitHub's viewer instead of the file,
		// passes them all. This row writes the URL out, so the spelling has
		// somewhere to be wrong.
		const want = "https://github.com/suruseas/opossum/raw/main/AGENTS.md"
		if !linked[want] {
			t.Errorf("llms.txt does not link AGENTS.md at %s. Following a URL that is not "+
				"exactly this — another branch, or /blob/ or /tree/, which are GitHub's "+
				"viewer — an agent gets 404 or a page of HTML.\n%s", want, body)
		}
	})
	t.Run("AGENTS.md is the first link", func(t *testing.T) {
		// An agent that reads one link should read the one written for
		// agents. Nothing else in the file says which that is.
		for _, line := range strings.Split(body, "\n") {
			m := llmsLinkRE.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			if want := site.RawURL("AGENTS.md"); m[2] != want {
				t.Errorf("the first link in llms.txt is %s, not %s. An agent that follows "+
					"only the first one reads a document written for people.", m[2], want)
			}
			return
		}
		t.Errorf("llms.txt has no links at all:\n%s", body)
	})
	t.Run("nothing the site keeps out of the site is in it", func(t *testing.T) {
		// Unpublished documents are repository procedure, not pages. An index
		// that linked them would send an agent reading about opossum into the
		// instructions for working on it.
		for _, page := range site.Unpublished {
			if bad := site.RawURL("docs/" + page + ".md"); linked[bad] {
				t.Errorf("llms.txt links docs/%s.md, which the site deliberately does not publish", page)
			}
		}
	})
}

func TestTheAgentIndexIsTheShapeAnAgentExpects(t *testing.T) {
	body := readLLMs(t)
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")

	t.Run("it opens with an H1", func(t *testing.T) {
		// The one part llmstxt.org requires. Without it the file is prose an
		// agent has to guess the shape of.
		if len(lines) == 0 || !strings.HasPrefix(lines[0], "# ") {
			t.Errorf("llms.txt does not open with an H1:\n%s", body)
		}
	})
	t.Run("a block quote says what the project is", func(t *testing.T) {
		found := false
		for _, l := range lines {
			if strings.HasPrefix(l, "> ") && len(l) > 40 {
				found = true
			}
		}
		if !found {
			t.Errorf("llms.txt carries no summary quote, so an agent has to open a link to "+
				"learn what opossum is:\n%s", body)
		}
	})
	t.Run("each line says what that document says about itself", func(t *testing.T) {
		// Where the title and the description come from. Reading them off
		// the documents is the point — written out here, or taken from one
		// document for all of them, they would say what the pages used to
		// say and nobody would hear about it. So each line is compared with
		// what the file itself carries.
		root := filepath.Join("..", "..")
		check := func(repoPath string) {
			md, err := os.ReadFile(filepath.Join(root, repoPath))
			if err != nil {
				t.Fatal(err)
			}
			wantTitle, wantDesc := site.Title(string(md)), site.Description(string(md))
			url := site.RawURL(repoPath)
			for _, line := range lines {
				m := llmsLinkRE.FindStringSubmatch(line)
				if m == nil || m[2] != url {
					continue
				}
				if m[1] != wantTitle {
					t.Errorf("%s is listed as %q; the document calls itself %q", repoPath, m[1], wantTitle)
				}
				if m[3] != wantDesc {
					t.Errorf("%s is described as %q;\nthe document opens with %q", repoPath, m[3], wantDesc)
				}
				return
			}
			t.Errorf("llms.txt has no line for %s", repoPath)
		}
		for _, page := range site.Pages {
			check("docs/" + page + ".md")
		}
		check("README.md")
		check("AGENTS.md")
	})
	t.Run("the summary is the README's own", func(t *testing.T) {
		md, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
		if err != nil {
			t.Fatal(err)
		}
		wantH1, wantSummary := "# "+site.Title(string(md)), "> "+site.Description(string(md))
		if lines[0] != wantH1 {
			t.Errorf("llms.txt opens with %q; the README's title is %q", lines[0], wantH1)
		}
		found := false
		for _, l := range lines {
			if l == wantSummary {
				found = true
			}
		}
		if !found {
			t.Errorf("llms.txt carries no summary matching the README's opening paragraph.\nwanted: %q\n%s",
				wantSummary, body)
		}
	})
	t.Run("every link line says what the document is", func(t *testing.T) {
		// A bare link makes an agent fetch each one to find out whether it
		// wanted it. The description is what lets it choose.
		for _, l := range lines {
			if !strings.HasPrefix(l, "- ") {
				continue
			}
			m := llmsLinkRE.FindStringSubmatch(l)
			if m == nil {
				t.Errorf("a list line is not `- [title](url): description`: %q", l)
				continue
			}
			if strings.TrimSpace(m[3]) == "" {
				t.Errorf("%q links %s with no description", m[1], m[2])
			}
		}
	})
	t.Run("the links are Markdown, not pages about Markdown", func(t *testing.T) {
		// `/blob/` is GitHub's viewer: an agent following one reads the
		// interface with the document somewhere inside it.
		for _, l := range lines {
			m := llmsLinkRE.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			if strings.Contains(m[2], "/blob/") || !strings.HasSuffix(m[2], ".md") {
				t.Errorf("llms.txt links %s, which is not the Markdown itself. An agent "+
					"following it reads a page about the file instead of the file.", m[2])
			}
		}
	})
}

func TestTheAgentIndexIsServedAsWrittenByTheSiteGenerator(t *testing.T) {
	// Jekyll reads front matter and renders what carries it. llms.txt has to
	// arrive at the reader as the bytes written here, so it must have none —
	// and it must not be swept up by a rule that renders `.md`.
	body := readLLMs(t)
	if strings.HasPrefix(body, "---") {
		t.Errorf("llms.txt starts with front matter, so the site would render it into HTML "+
			"instead of serving it:\n%s", body[:min(len(body), 200)])
	}
	out := buildSite(t)
	if _, err := os.Stat(filepath.Join(out, "llms.md")); err == nil {
		t.Error("the site writes llms.md as well; a document rendered to HTML is not the " +
			"plain Markdown an agent asked for")
	}
}
