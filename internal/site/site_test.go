package site

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestTitleComesFromTheH1(t *testing.T) {
	for _, tc := range []struct{ md, want string }{
		{"# Networking model\n\nprose\n", "Networking model"},
		{"# Benchmarks: Apple `container` vs Docker Desktop\n", "Benchmarks: Apple container vs Docker Desktop"},
		{"prose first\n\n# Later heading\n", "Later heading"},
		{"## only an h2\n", ""},
		// A `#` line inside an example is a shell comment, not the page's heading.
		// docs/compatibility.md really contains one, and taking it for the H1 puts
		// a shell comment in <title> the moment the real heading moves below it.
		{"```sh\n# not a heading, a shell comment\n```\n\n# Real title\n", "Real title"},
	} {
		if got := Title(tc.md); got != tc.want {
			t.Errorf("Title(%q) = %q, want %q", tc.md, got, tc.want)
		}
	}
}

// A page with no H1 has no title, and a page with no title is the thing this
// build step exists to prevent. Failing is better than shipping one.
func TestRenderRefusesAPageWithNoTitle(t *testing.T) {
	if _, err := Render("## nothing at the top\n", "docs"); err == nil {
		t.Error("a document with no H1 should not render")
	}
}

// The front matter is YAML, and a heading is prose someone writes without
// thinking about YAML. Both a quote and a backslash end the scalar early, and the
// result is a site that does not build at all.
func TestFrontMatterSurvivesAwkwardHeadings(t *testing.T) {
	for _, h := range []string{`# The "quoted" case`, `# A path C:\Users\x`} {
		got, err := Render(h+"\n\nprose\n", "docs")
		if err != nil {
			t.Fatal(err)
		}
		line, _, _ := strings.Cut(strings.TrimPrefix(got, "---\n"), "\n")
		value := strings.TrimPrefix(line, "title: ")
		if !strings.HasPrefix(value, `"`) || !strings.HasSuffix(value, `"`) {
			t.Errorf("%s produced front matter %q, which is not a closed YAML scalar", h, line)
		}
		// Read the scalar back the way YAML does: every backslash introduces an
		// escape, and an unescaped quote ends the value early.
		inner, decoded := value[1:len(value)-1], strings.Builder{}
		for i := 0; i < len(inner); i++ {
			switch inner[i] {
			case '\\':
				if i+1 >= len(inner) || (inner[i+1] != '\\' && inner[i+1] != '"') {
					t.Fatalf("%s produced front matter %q with a stray escape", h, line)
				}
				decoded.WriteByte(inner[i+1])
				i++
			case '"':
				t.Fatalf("%s produced front matter %q, which ends early at an unescaped quote", h, line)
			default:
				decoded.WriteByte(inner[i])
			}
		}
		if got := decoded.String(); got != Title(h) {
			t.Errorf("%s round-tripped to %q, want %q", h, got, Title(h))
		}
	}
}

// Fences are indented wherever they sit inside a list, and docs/troubleshooting.md
// has three. An indented one that goes unrecognised takes its Liquid protection
// and its example paths with it — and leaves the fence state inverted for the
// rest of the page.
func TestRenderRecognisesAnIndentedFence(t *testing.T) {
	md := "# Title\n\n1. do this:\n\n   ```sh\n   docker info --format '{{.MemTotal}}'\n   ```\n\nafter.\n"
	got, err := Render(md, "docs")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "{% raw %}") {
		t.Errorf("an indented fence needs protecting too, got:\n%s", got)
	}
	if strings.Index(got, "{% raw %}") > strings.Index(got, "{{.MemTotal}}") {
		t.Errorf("the protection has to start before the thing it protects, got:\n%s", got)
	}
}

// The opening sentence is what a search result and an AI answer quote. Without
// one, every page inherits the site's description and seven pages describe the
// project rather than themselves.
func TestDescriptionIsTheOpeningParagraph(t *testing.T) {
	md := "<p align=\"center\"><img src=\"x.png\"></p>\n\n# Networking model\n\n" +
		"How services find each other on a `container` [network](docs/networking.md).\nOne paragraph.\n\n" +
		"## A later heading\n\nnot this.\n"
	if got, want := Description(md), "How services find each other on a container network. One paragraph."; got != want {
		t.Errorf("Description = %q, want %q", got, want)
	}
	if got := Description("## no h1 here\n\nprose\n"); got != "" {
		t.Errorf("a page with no H1 has no opening paragraph, got %q", got)
	}
	long := "# T\n\n" + strings.Repeat("word ", 100) + "\n"
	if got := Description(long); len(got) > 210 || !strings.HasSuffix(got, "…") {
		t.Errorf("a long opening paragraph should be cut short, got %d chars: %q", len(got), got)
	}
}

func TestRewriteTarget(t *testing.T) {
	for _, tc := range []struct{ dir, in, want string }{
		// Between published pages: a site-relative page, not a repository file.
		{"docs", "networking.md", "networking.html"},
		{"docs", "troubleshooting.md#known-limitations", "troubleshooting.html#known-limitations"},
		{"", "docs/compatibility.md", "compatibility.html"},
		// The README is the site's own front page, reached from either place. Sent
		// to GitHub instead, every "back to the README" is a door out of the site.
		{"docs", "../README.md", "index.html"},
		{"", "README.md", "index.html"},
		{"docs", "../README.md#commands", "index.html#commands"},
		// A document that stays in the repository, reached from either place.
		{"docs", "real-runtime-review.md", RepoURL + "/blob/main/docs/real-runtime-review.md"},
		{"", "AGENTS.md", RepoURL + "/blob/main/AGENTS.md"},
		{"docs", "../examples/README.md", RepoURL + "/blob/main/examples/README.md"},
		{"", "examples/README.md", RepoURL + "/blob/main/examples/README.md"},
		// A section named in the link is the reason the link was written: dropping it
		// lands the reader at the top of a long page instead.
		{"", "CONTRIBUTING.md#changelog", RepoURL + "/blob/main/CONTRIBUTING.md#changelog"},
		// Written from the repository root, it means the same file as the relative
		// form and has to resolve the same way.
		{"docs", "/docs/networking.md", "networking.html"},
		{"", "/docs/networking.md", "networking.html"},
		// Images are served, not linked to a page about themselves — including the
		// diagrams, which are SVG.
		{"", "docs/assets/readme-banner.png", RepoURL + "/raw/main/docs/assets/readme-banner.png"},
		{"docs", "assets/network.svg", RepoURL + "/raw/main/docs/assets/network.svg"},
		{"docs", "assets/network.svg#layer", RepoURL + "/raw/main/docs/assets/network.svg#layer"},
		// Left alone.
		{"docs", "https://example.com/x", "https://example.com/x"},
		{"docs", "mailto:someone@example.com", "mailto:someone@example.com"},
		// A query is no more part of the path than a fragment is; left on, it turns a
		// page on the site into a repository file that does not exist.
		{"docs", "networking.md?plain=1", "networking.html?plain=1"},
		{"docs", "#a-heading-on-this-page", "#a-heading-on-this-page"},
		// Angle brackets are how a target containing a space is written. Left on, the
		// brackets end up in the path.
		{"docs", "<networking.md>", "<networking.html>"},
		{"", "<docs/compatibility.md#commands>", "<compatibility.html#commands>"},
		// A link to the repository itself resolves to no file, and "blob/main/." is
		// not a page anyone can open.
		{"docs", "..", RepoURL},
		{"", "/", RepoURL},
	} {
		if got := rewriteTarget(tc.dir, tc.in); got != tc.want {
			t.Errorf("rewriteTarget(%q, %q) = %q, want %q", tc.dir, tc.in, got, tc.want)
		}
	}
}

// The rewriter has to actually rewrite. Every other test here asserts that
// something was left alone, and a check made only of those passes just as well
// when the rewriting is deleted — which is how the banner and the Japanese link
// on the front page can break with the tests green.
func TestRenderRewritesHTMLAttributes(t *testing.T) {
	md := "# Title\n\n<p><img src=\"docs/assets/readme-banner.png\" width=\"920\"></p>\n" +
		"<p><a href=\"docs/networking.md\">the model</a> and <a href=\"README.ja.md\">日本語</a></p>\n"
	got, err := Render(md, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`src="` + RepoURL + `/raw/main/docs/assets/readme-banner.png"`,
		`href="networking.html"`,
		`href="` + RepoURL + `/blob/main/README.ja.md"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
}

// A link written in the reference shorthand is still a link, and it is the one
// nobody thinks to check: it carries no `](`, so a scan for that finds nothing
// and the site 404s.
func TestRenderRewritesAReferenceDefinition(t *testing.T) {
	md := "# Title\n\nsee [the model][net].\n\n[net]: docs/networking.md\n"
	got, err := Render(md, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "[net]: networking.html") {
		t.Errorf("the reference definition was not rewritten, got:\n%s", got)
	}
}

// A footnote definition is written the same way as a reference definition and is
// not a link at all: what follows the label is prose. Treating its first word as
// a target rewrites the sentence into a URL, and because the result is an
// absolute URL nothing downstream notices.
func TestRenderLeavesFootnotesAlone(t *testing.T) {
	md := "# Title\n\nclaimed[^src].\n\n[^src]: measured on an M2 Pro, see the benchmarks page.\n"
	got, err := Render(md, "docs")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "[^src]: measured on an M2 Pro") {
		t.Errorf("the footnote text was rewritten as if it were a link, got:\n%s", got)
	}
}

// Prose and fences are written to the page from two different places, and only
// their interleaving keeps the document in order. Lose it and the page opens with
// a code block and every paragraph falls to the bottom — while a test that only
// checks `{% raw %}` comes before `{{` still passes, because both move together.
func TestRenderKeepsTheDocumentInOrder(t *testing.T) {
	md := "# The heading\n\nthe opening paragraph\n\n```sh\nopossum up\n```\n\nclosing prose\n"
	got, err := Render(md, "docs")
	if err != nil {
		t.Fatal(err)
	}
	h1, fence := strings.Index(got, "# The heading"), strings.Index(got, "```sh")
	closing := strings.Index(got, "closing prose")
	if h1 < 0 || fence < 0 || closing < 0 {
		t.Fatalf("the page lost some of its content:\n%s", got)
	}
	if !(h1 < fence && fence < closing) {
		t.Errorf("heading/fence/closing are at %d/%d/%d — the page is out of order:\n%s",
			h1, fence, closing, got)
	}
}

// Link text wraps at the margin, so a link can straddle two lines. A per-line
// pass misses exactly those — which is how the first version of this shipped a
// site with `](docs/compatibility.md)` in it, a path that 404s there.
func TestRenderRewritesALinkSplitAcrossLines(t *testing.T) {
	md := "# Title\n\nsome prose — see [measured\ncompatibility](docs/compatibility.md) for the method.\n"
	got, err := Render(md, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "compatibility.html") {
		t.Errorf("the wrapped link should have been rewritten, got:\n%s", got)
	}
	if strings.Contains(got, "docs/compatibility.md") {
		t.Errorf("the repository path survived into the site, got:\n%s", got)
	}
}

// Jekyll expands Liquid inside fenced code blocks too. An example showing
// `docker info --format '{{.MemTotal}}'` is eaten unless the fence is protected,
// and it is a real example in docs/benchmarks.md.
func TestRenderProtectsCodeFencesFromLiquid(t *testing.T) {
	md := "# Title\n\n```sh\ndocker info --format '{{.MemTotal}}'\n```\n"
	got, err := Render(md, "docs")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "{% raw %}") || !strings.Contains(got, "{% endraw %}") {
		t.Errorf("the fence should be wrapped so Liquid leaves it alone, got:\n%s", got)
	}
	if strings.Index(got, "{% raw %}") > strings.Index(got, "{{.MemTotal}}") {
		t.Errorf("the protection has to start before the thing it protects, got:\n%s", got)
	}
}

// Paths inside an example are the example. Rewriting them would leave a command
// nobody can run.
func TestRenderLeavesExamplesAlone(t *testing.T) {
	// The example has to contain something the rewriter would otherwise change —
	// a bare path is not rewritten anywhere, so an example built from one proves
	// nothing about fences.
	md := "# Title\n\n```md\nsee [the model](docs/networking.md) and <img src=\"docs/assets/x.png\">\n```\n"
	got, err := Render(md, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "[the model](docs/networking.md)") {
		t.Errorf("the link inside the example was rewritten, got:\n%s", got)
	}
	if !strings.Contains(got, `src="docs/assets/x.png"`) {
		t.Errorf("the image path inside the example was rewritten, got:\n%s", got)
	}
}

// A fence that is never closed would swallow the rest of the page into a code
// block — silently, and only visible on the published site.
func TestRenderRefusesAnUnterminatedFence(t *testing.T) {
	if _, err := Render("# Title\n\n```sh\nnever closed\n", "docs"); err == nil {
		t.Error("an unterminated fence should not render")
	}
}

// The real thing: build the site from the repository and check that every page
// it publishes is there, titled, and free of links that only work in the
// repository.
func TestBuildTheRepositorysSite(t *testing.T) {
	out := t.TempDir()
	if err := Build(filepath.Join("..", ".."), out); err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, name := range append(append([]string{}, Pages...), "index") {
		body, err := os.ReadFile(filepath.Join(out, name+".md"))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		s := string(body)
		if !strings.HasPrefix(s, "---\ntitle: \"") {
			t.Errorf("%s has no title, so it is one more page called \"opossum\"", name)
		}
		if first, _, _ := strings.Cut(strings.TrimPrefix(s, "---\ntitle: \""), "\""); first == "" {
			t.Errorf("%s's title is empty", name)
		}
		// The description is the sentence shown under the title in a search result.
		// Missing, the page borrows the site's, and every page says the same thing.
		if !strings.Contains(s, "\ndescription: \"") {
			t.Errorf("%s has no description of its own", name)
		}
		// No repository-relative markdown link may survive: on the site those 404.
		for _, frag := range []string{"](docs/", "](AGENTS.md", "](CHANGELOG.md", "](examples/", "](../"} {
			if strings.Contains(s, frag) {
				t.Errorf("%s still contains %q, which does not resolve on the site", name, frag)
			}
		}
	}
	cfg, err := os.ReadFile(filepath.Join(out, "_config.yml"))
	if err != nil {
		t.Errorf("the site needs its config: %v", err)
	}
	// Neither plugin is enabled by default on GitHub Pages. Without seo-tag the
	// theme's `{% seo %}` is an unknown tag and the site does not build at all;
	// without sitemap there is no sitemap.xml for a crawler to read.
	for _, plugin := range []string{"jekyll-seo-tag", "jekyll-sitemap"} {
		if !strings.Contains(string(cfg), plugin) {
			t.Errorf("_config.yml does not enable %s", plugin)
		}
	}
	// A page that is deliberately not published must not be linked as if it were.
	index, _ := os.ReadFile(filepath.Join(out, "index.md"))
	if strings.Contains(string(index), "real-runtime-review.html") {
		t.Error("real-runtime-review is a contributor document and is not published")
	}
}

// Build empties its output directory, and takes that directory from the command
// line. `go run ./internal/site/build ~/work` should be a refusal, not a bad
// afternoon — so it only empties a directory it can see it wrote itself.

// The favicon must actually travel. head-custom.html is the primer theme's only
// <head> hook, and every icon it references must exist in the output byte-for-
// byte — a link to a missing or stale file is the browser's globe again, and
// nothing else on the site would notice.
func TestBuildShipsTheFavicon(t *testing.T) {
	out := t.TempDir()
	if err := Build(filepath.Join("..", ".."), out); err != nil {
		t.Fatalf("Build: %v", err)
	}
	headHTML, err := os.ReadFile(filepath.Join(out, "_includes", "head-custom.html"))
	if err != nil {
		t.Fatalf("head-custom.html: %v (without it the theme renders no icon links at all)", err)
	}
	// Search Console proves ownership by fetching this tag from the live site;
	// silently losing it revokes the property and, with it, the sitemap ping.
	if !strings.Contains(string(headHTML), `name="google-site-verification"`) {
		t.Errorf("head-custom.html lost the Search Console verification tag")
	}
	for _, name := range []string{"favicon.png", "favicon-512.png", "apple-touch-icon.png"} {
		if !strings.Contains(string(headHTML), "/assets/"+name) {
			t.Errorf("head-custom.html does not reference %s", name)
		}
		src, err := os.ReadFile(filepath.Join("..", "..", "docs", "assets", name))
		if err != nil {
			t.Fatalf("source icon %s: %v", name, err)
		}
		got, err := os.ReadFile(filepath.Join(out, "assets", name))
		if err != nil {
			t.Errorf("%s was not copied into the site: %v", name, err)
			continue
		}
		if !bytes.Equal(src, got) {
			t.Errorf("%s differs from docs/assets/%s — not copied verbatim", name, name)
		}
	}
}

func TestBuildRefusesADirectoryItDidNotWrite(t *testing.T) {
	out := t.TempDir()
	keep := filepath.Join(out, "someone-elses-work.txt")
	if err := os.WriteFile(keep, []byte("important\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Build(filepath.Join("..", ".."), out)
	if err == nil {
		t.Fatal("a directory with unrelated files in it should not be emptied")
	}
	if _, statErr := os.Stat(keep); statErr != nil {
		t.Errorf("the refusal must not have deleted anything first: %v", statErr)
	}

	// The marker is what turns "there is a _config.yml here" into "this is our own
	// output", and the accident this guard exists for is someone pointing the build
	// at a Jekyll site they already have. A directory with a config that is not ours
	// is exactly that case.
	// Jekyll's own scaffold opens with `# Welcome to Jekyll!`, so a config that
	// merely starts with a comment must not be mistaken for ours: that is the very
	// site someone would point this at by accident.
	for _, cfgBody := range []string{
		"title: someone else's site\n",
		"# Welcome to Jekyll!\ntitle: someone else's site\n",
	} {
		other := t.TempDir()
		cfg := filepath.Join(other, "_config.yml")
		if err := os.WriteFile(cfg, []byte(cfgBody), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := Build(filepath.Join("..", ".."), other); err == nil {
			t.Errorf("a Jekyll site this build did not write should not be emptied (config %q)", cfgBody)
		}
		if _, statErr := os.Stat(cfg); statErr != nil {
			t.Errorf("the refusal deleted someone else's config: %v", statErr)
		}
	}

	// An empty path is the shape a missing argument takes. It has to be turned away
	// by name — anything further along fails too, but with an error about mkdir,
	// which tells the person running it nothing about what they did wrong.
	if err := Build(filepath.Join("..", ".."), ""); err == nil ||
		!strings.Contains(err.Error(), "refusing to build the site into") {
		t.Errorf("building into an empty path should be refused as such, got %v", err)
	}
	// Anything the guard cannot read, it cannot vouch for.
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Build(filepath.Join("..", ".."), notADir); err == nil {
		t.Error("a directory the guard cannot read should not be built into")
	}

	// Its own previous output is fair game: a page removed from Pages should not
	// linger on the site.
	fresh := filepath.Join(t.TempDir(), "site")
	if err := Build(filepath.Join("..", ".."), fresh); err != nil {
		t.Fatalf("building into a new directory: %v", err)
	}
	stale := filepath.Join(fresh, "removed-page.md")
	if err := os.WriteFile(stale, []byte("---\ntitle: \"gone\"\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Build(filepath.Join("..", ".."), fresh); err != nil {
		t.Fatalf("rebuilding over this build's own output: %v", err)
	}
	if _, statErr := os.Stat(stale); !os.IsNotExist(statErr) {
		t.Errorf("a page that is no longer published should be gone, stat err = %v", statErr)
	}
}

// Pages is a list, and a test that walks Pages to check the pages exist shrinks
// with it: drop an entry and both sides agree, silently. This walks the documents
// on disk instead, so a document added to docs/ and forgotten here — published
// nowhere, linked as a GitHub blob — fails, and so does one dropped from Pages.
func TestEveryDocumentIsEitherPublishedOrDeliberatelyNot(t *testing.T) {
	found, err := filepath.Glob(filepath.Join("..", "..", "docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) < 5 {
		t.Fatalf("only %d documents found — the glob is not doing its job", len(found))
	}
	on := map[string]bool{}
	for _, p := range found {
		on[strings.TrimSuffix(filepath.Base(p), ".md")] = true
	}
	listed := map[string]bool{}
	for _, name := range append(append([]string{}, Pages...), Unpublished...) {
		if listed[name] {
			t.Errorf("%q is listed twice, so it is both published and not", name)
		}
		listed[name] = true
		if !on[name] {
			t.Errorf("%q is listed in the site's pages but docs/%s.md does not exist", name, name)
		}
		delete(on, name)
	}
	for name := range on {
		t.Errorf("docs/%s.md is neither published nor listed as deliberately unpublished, so it "+
			"is missing from the site and linked as a repository file", name)
	}
}

// These are written for the check, not shared with the rewriter, and they are
// deliberately looser than it: a link shape the rewriter does not recognise is
// exactly the one that reaches the site unrewritten, and a check built from the
// rewriter's own patterns is blind to precisely those.
var (
	siteLinkRE    = regexp.MustCompile(`\[[^\]]*\]\(\s*<?([^)>\s]+)`)
	siteAttrRE    = regexp.MustCompile(`(?:src|href|srcset)\s*=\s*"([^"]*)"`)
	siteRefDefRE  = regexp.MustCompile(`(?m)^\s{0,3}\[[^^\]][^\]]*\]:[ \t]+(\S+)`)
	siteHeadingRE = regexp.MustCompile(`^#{1,6}\s+(.*)$`)
	siteLinkText  = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	sitePunct     = regexp.MustCompile(`[^\w\s-]`)
	siteSpaces    = regexp.MustCompile(`\s+`)
)

func siteSlug(h string) string {
	h = siteLinkText.ReplaceAllString(h, "$1")
	h = strings.NewReplacer("`", "", "*", "", "_", "").Replace(h)
	h = sitePunct.ReplaceAllString(strings.ToLower(strings.TrimSpace(h)), "")
	return siteSpaces.ReplaceAllString(h, "-")
}

// The repository has a ratchet for links between its own documents; this is the
// same check pointed at what actually gets published. It is the one test here
// that sees the site as a reader does — every link either leaves for a real URL
// or lands on a page and a section that exist — and because it works from the
// output it catches the failures no single unit test names: a page missing from
// the site, a rewrite that stopped happening, a section renamed upstream.
func TestTheGeneratedSiteHangsTogether(t *testing.T) {
	out := t.TempDir()
	if err := Build(filepath.Join("..", ".."), out); err != nil {
		t.Fatalf("Build: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(out, "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	bodies, anchors := map[string]string{}, map[string]map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		page := strings.TrimSuffix(filepath.Base(f), ".md") + ".html"
		// Fences are blanked for the same reason as everywhere else: the paths in an
		// example are the example, and a `#` line in one is a shell comment.
		body := blankFences(string(data))
		bodies[page] = body
		a := map[string]bool{}
		for _, line := range strings.Split(body, "\n") {
			if m := siteHeadingRE.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
				a[siteSlug(m[1])] = true
			}
		}
		anchors[page] = a
	}
	if len(bodies) != len(Pages)+1 {
		t.Fatalf("the site has %d pages, expected %d plus the front page", len(bodies), len(Pages))
	}

	checked := 0
	for page, body := range bodies {
		var targets []string
		for _, re := range []*regexp.Regexp{siteLinkRE, siteAttrRE, siteRefDefRE} {
			for _, m := range re.FindAllStringSubmatch(body, -1) {
				targets = append(targets, m[1])
			}
		}
		for _, target := range targets {
			if strings.HasPrefix(target, "mailto:") {
				continue
			}
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
				// A document that is on the site must be reached as a page on the site;
				// sent to GitHub instead, the reader leaves and does not come back.
				repoPath := strings.TrimPrefix(target, RepoURL+"/blob/main/")
				repoPath, _, _ = strings.Cut(repoPath, "#")
				if name, ok := published(repoPath); ok {
					t.Errorf("%s links to %s, but that document is published here as %s",
						page, target, name)
				}
				continue
			}
			checked++
			dest, frag, _ := strings.Cut(target, "#")
			if dest == "" {
				dest = page
			}
			if _, ok := bodies[dest]; !ok {
				t.Errorf("%s links to %q, which is not a page on this site", page, target)
				continue
			}
			if frag != "" && !anchors[dest][strings.ToLower(frag)] {
				t.Errorf("%s links to %q, but %s has no such heading", page, target, dest)
			}
		}
		// Every page needs a way back to the front page: a site whose pages only
		// lead outwards is a set of loose documents, not a site.
		if page != "index.html" && !strings.Contains(body, "index.html") {
			t.Errorf("%s offers no link back to the front page", page)
		}
	}
	if checked < 30 {
		t.Fatalf("only %d links within the site were checked — the scan is missing something", checked)
	}
}

// The point of the check above having its own patterns is that it can see link
// shapes the rewriter cannot — those are precisely the ones that reach the site
// unrewritten. Nothing enforces that on its own: point the check at the
// rewriter's patterns and it still passes, because today's documents contain no
// such shape. So the difference is asserted directly.
func TestTheSiteCheckSeesMoreThanTheRewriter(t *testing.T) {
	for _, tc := range []struct{ what, text string }{
		{"a space after the opening paren", "[x]( docs/networking.md)"},
		{"spaces around the attribute's =", `<a href = "docs/networking.md">`},
	} {
		var byCheck []string
		for _, re := range []*regexp.Regexp{siteLinkRE, siteAttrRE, siteRefDefRE} {
			for _, m := range re.FindAllStringSubmatch(tc.text, -1) {
				byCheck = append(byCheck, m[1])
			}
		}
		if len(byCheck) == 0 {
			t.Errorf("the site check no longer sees %s (%q), so a link in that shape "+
				"would reach the site unrewritten with nothing to say so", tc.what, tc.text)
		}
		// And it is genuinely a shape the rewriter misses — if that stops being true,
		// this case has stopped showing anything.
		out, err := Render("# T\n\n"+tc.text+"\n", "")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "docs/networking.md") {
			t.Errorf("the rewriter now handles %s, so this case no longer shows the "+
				"check reaching further than it does", tc.what)
		}
	}
}
