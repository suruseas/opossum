// Package site turns the repository's markdown into the sources for the
// documentation site.
//
// It exists because pointing GitHub Pages at docs/ directly gets three things
// wrong, each quietly:
//
//   - Liquid. Jekyll expands `{{ … }}` and `{% … %}` including inside fenced code
//     blocks, and docs/benchmarks.md shows a real `docker info --format
//     '{{.MemTotal}}'`. Served as-is, that example is eaten.
//   - Titles. The point of the site is one indexable page per topic, and a page
//     whose <title> is the site's name is not that. Taking the title from each
//     file's own H1 keeps it out of the sources, where GitHub would render front
//     matter as a stray table at the top of every document.
//   - Links. `networking.md` is right in the repository and wrong on the site.
//
// The sources are never modified: everything here reads and writes copies. The
// link rewriting is the part that has to be exactly right — a moved section
// leaves a link that still looks fine — so it resolves each link to a repository
// path first and decides from that, rather than pattern-matching the text.
package site

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// RepoURL is where a file that stays in the repository is linked from the site.
const RepoURL = "https://github.com/suruseas/opossum"

// Pages are the documents the site publishes, in the order a reader meets them.
// Everything else under docs/ stays a repository file: real-runtime-review.md is
// a contributor procedure, not something a person searching for "docker compose
// on Apple container" should land on.
var Pages = []string{
	"compatibility",
	"networking",
	"troubleshooting",
	"benchmarks",
	"mcp",
	"agent-sandbox",
	"vs-docker-desktop",
}

// Unpublished names the documents under docs/ that deliberately stay repository
// files. Saying so here rather than leaving them out of Pages is what lets a test
// insist that every document is one or the other: a new page nobody added to
// Pages would otherwise be published nowhere and linked as a GitHub blob, and
// nothing would say so.
var Unpublished = []string{"real-runtime-review"}

var (
	// The text part spans newlines without any flag — a negated class matches
	// them — which is what lets a link wrapped at the margin be found.
	mdLinkRE   = regexp.MustCompile(`(!?\[[^\]]*\])\(([^)\s]+)(\s+"[^"]*")?\)`)
	htmlAttrRE = regexp.MustCompile(`(src|href)="([^"]+)"`)
	// A footnote definition (`[^1]: …`) has the same shape as a reference
	// definition but holds prose, not a target, so the label may not start with ^.
	refDefRE   = regexp.MustCompile(`(?m)^(\s{0,3}\[[^^\]][^\]]*\]:[ \t]+)(\S+)`)
	h1RE       = regexp.MustCompile(`(?m)^# (.+)$`)
	fenceRE    = regexp.MustCompile(`^\s*` + "```")
	descLinkRE = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	descTagRE  = regexp.MustCompile(`<[^>]*>`)
	descSpaces = regexp.MustCompile(`\s+`)
)

// blankFences replaces the contents of fenced blocks with empty lines, keeping
// the line count so positions stay true. Anything that reads the document's own
// structure has to go through this: `# not a heading, a shell comment` inside an
// example is not an H1, and taking it for one puts a shell comment in <title>.
func blankFences(md string) string {
	lines := strings.Split(md, "\n")
	inFence := false
	for i, line := range lines {
		if fenceRE.MatchString(line) {
			inFence = !inFence
			lines[i] = ""
			continue
		}
		if inFence {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// Title is the page's own H1, which becomes its <title>.
func Title(md string) string {
	m := h1RE.FindStringSubmatch(blankFences(md))
	if m == nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(m[1], "`", ""))
}

// Description is the page's opening paragraph, flattened onto one line, and it
// becomes the sentence a search result or an AI answer shows under the title.
// Without it every page inherits the site's own description, which is the same as
// having none: seven pages that all describe the project rather than themselves.
func Description(md string) string {
	body := blankFences(md)
	loc := h1RE.FindStringIndex(body)
	if loc == nil {
		return ""
	}
	var para []string
	for _, line := range strings.Split(body[loc[1]:], "\n") {
		t := strings.TrimSpace(line)
		// The opening paragraph ends at the first blank line after it; markup that
		// is not prose (a heading, an HTML block, a comment) is skipped before it and
		// ends it after.
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "<") ||
			strings.HasPrefix(t, ">") || strings.HasPrefix(t, "|") {
			if len(para) > 0 {
				break
			}
			continue
		}
		para = append(para, t)
	}
	s := descLinkRE.ReplaceAllString(strings.Join(para, " "), "$1")
	s = descTagRE.ReplaceAllString(s, "")
	s = strings.NewReplacer("`", "", "**", "", `"`, "'").Replace(s)
	s = strings.TrimSpace(descSpaces.ReplaceAllString(s, " "))
	const max = 200
	if len(s) > max {
		cut := strings.LastIndex(s[:max], " ")
		if cut < max/2 {
			cut = max
		}
		s = strings.TrimRight(s[:cut], " ,;:—-") + "…"
	}
	return s
}

// published reports whether a repository path is one of the site's own pages,
// and what the page is called there.
func published(repoPath string) (string, bool) {
	// The README is the site's landing page, so a link to it from any other page
	// belongs on the site too — otherwise every "back to the README" is a door out
	// of the site, and the site has no way back to its own front page.
	if repoPath == "README.md" {
		return "index.html", true
	}
	if !strings.HasPrefix(repoPath, "docs/") || !strings.HasSuffix(repoPath, ".md") {
		return "", false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(repoPath, "docs/"), ".md")
	for _, p := range Pages {
		if p == name {
			return name + ".html", true
		}
	}
	return "", false
}

// rewriteTarget maps one link, as written in a file at srcDir, to where it should
// point from the site. srcDir is the file's directory relative to the repository
// root ("" for the README).
//
// Resolving to a repository path first is what keeps this honest: "../examples"
// from docs/ and "examples" from the root are the same destination, and a rule
// written against the text would have to know which file it was reading.
func rewriteTarget(srcDir, target string) string {
	// A target may be written in angle brackets. Left wrapped, it resolves to a
	// path with the brackets in it, and the result is an absolute URL, so nothing
	// downstream notices. (The bracketed form also allows a space in the path;
	// that one never gets here, because mdLinkRE's target stops at whitespace.)
	if len(target) > 1 && strings.HasPrefix(target, "<") && strings.HasSuffix(target, ">") {
		return "<" + rewriteTarget(srcDir, target[1:len(target)-1]) + ">"
	}
	if target == "" || strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") ||
		strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "#") {
		return target
	}
	// Everything after the path — `#section`, and a query if one is ever written —
	// is carried through untouched: dropping it lands the reader at the top of a
	// long page instead of at the section the link named.
	frag := ""
	if i := strings.IndexAny(target, "#?"); i >= 0 {
		frag, target = target[i:], target[:i]
	}
	if target == "" {
		return frag
	}
	// A link written from the repository root ("/docs/…") means the same file as
	// the relative one, so it has to resolve the same way rather than being joined
	// onto the directory it was written in.
	base := srcDir
	if strings.HasPrefix(target, "/") {
		base, target = "", strings.TrimPrefix(target, "/")
	}
	repoPath := path.Clean(path.Join(base, target))
	if repoPath == "." || repoPath == "/" {
		return RepoURL + frag
	}
	if name, ok := published(repoPath); ok {
		return name + frag
	}
	// Images have to be served, not linked to a page about themselves.
	if ext := strings.ToLower(path.Ext(repoPath)); ext == ".png" || ext == ".jpg" ||
		ext == ".jpeg" || ext == ".gif" || ext == ".webp" || ext == ".svg" {
		return RepoURL + "/raw/main/" + repoPath + frag
	}
	return RepoURL + "/blob/main/" + repoPath + frag
}

// Render returns the site source for one document: front matter carrying its
// title, then the body with links rewritten and fenced blocks protected from
// Liquid. srcDir is the document's directory relative to the repository root.
func Render(md, srcDir string) (string, error) {
	title := Title(md)
	if title == "" {
		return "", fmt.Errorf("no H1, so the page would have no title")
	}
	var b strings.Builder
	b.WriteString("---\ntitle: " + yamlString(title) + "\n")
	if d := Description(md); d != "" {
		b.WriteString("description: " + yamlString(d) + "\n")
	}
	b.WriteString("---\n")

	// Rewriting runs over whole stretches of prose, not line by line: link text
	// wraps at the margin, so `[measured\ncompatibility](docs/…)` is one link split
	// across two lines and a per-line pass simply does not see it.
	rewrite := func(text string) string {
		text = mdLinkRE.ReplaceAllStringFunc(text, func(m string) string {
			parts := mdLinkRE.FindStringSubmatch(m)
			return parts[1] + "(" + rewriteTarget(srcDir, parts[2]) + parts[3] + ")"
		})
		text = htmlAttrRE.ReplaceAllStringFunc(text, func(m string) string {
			parts := htmlAttrRE.FindStringSubmatch(m)
			return parts[1] + `="` + rewriteTarget(srcDir, parts[2]) + `"`
		})
		// A reference definition (`[spec]: docs/networking.md`) is a link too, and
		// one written in the shorthand is the one nobody thinks to check.
		return refDefRE.ReplaceAllStringFunc(text, func(m string) string {
			parts := refDefRE.FindStringSubmatch(m)
			return parts[1] + rewriteTarget(srcDir, parts[2])
		})
	}

	inFence := false
	var prose strings.Builder
	flush := func() {
		b.WriteString(rewrite(prose.String()))
		prose.Reset()
	}
	for _, line := range strings.Split(md, "\n") {
		if fenceRE.MatchString(line) {
			// Wrapped rather than escaped in the source: an example that shows
			// `{{ … }}` is a real example, and it should read that way in the
			// repository too.
			if !inFence {
				flush()
				b.WriteString("{% raw %}\n" + line + "\n")
			} else {
				b.WriteString(line + "\n{% endraw %}\n")
			}
			inFence = !inFence
			continue
		}
		if inFence {
			// Never rewrite inside an example: the paths in it are the example.
			b.WriteString(line + "\n")
			continue
		}
		prose.WriteString(line + "\n")
	}
	flush()
	if inFence {
		return "", fmt.Errorf("an unterminated code fence would swallow the rest of the page")
	}
	return b.String(), nil
}

// ensureSafeToOverwrite refuses any directory that is not empty and does not
// carry this build's marker. Recognising our own output — rather than trusting a
// name, or a flag — is what makes the refusal hold for a path nobody anticipated.
func ensureSafeToOverwrite(out string) error {
	if strings.TrimSpace(out) == "" || out == "/" {
		return fmt.Errorf("refusing to build the site into %q", out)
	}
	entries, err := os.ReadDir(out)
	if os.IsNotExist(err) {
		return nil // nothing there to lose
	}
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	cfg, err := os.ReadFile(filepath.Join(out, "_config.yml"))
	if err == nil && strings.HasPrefix(string(cfg), generatedMarker) {
		return nil // a previous build of this site
	}
	return fmt.Errorf("%s is not empty and was not written by this build (no %s), so it will not "+
		"be emptied — point the build at a new directory, or delete that one yourself",
		out, "_config.yml with the generated marker")
}

// yamlString quotes a value for front matter. Inside a double-quoted YAML scalar
// a backslash is an escape, so a heading that contains one takes the quote after
// it with it and the site fails to build.
func yamlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// The plugins are named rather than left to the theme: jekyll-seo-tag is what
// turns the per-page title and description into <title> and <meta>, and
// jekyll-sitemap is not among the plugins GitHub Pages enables by default, so
// without this line there is no sitemap.xml for a crawler to read.
const configYAML = generatedMarker + `
title: opossum
description: Run docker compose projects on Apple's container runtime
theme: jekyll-theme-primer
plugins:
  - jekyll-seo-tag
  - jekyll-sitemap
`

// generatedMarker is the first line of the config this writes, and the proof that
// a directory is a previous build of this site rather than someone's work.
const generatedMarker = "# opossum documentation site — generated by internal/site; safe to delete."

// Build writes the whole site into out, reading from the repository at root.
//
// out is emptied first, so a page deleted from Pages does not linger on the site.
// That makes this the one destructive thing here, and it takes a path from the
// command line — so it will only empty a directory it can see it wrote itself.
// `go run ./internal/site/build ~/work` should be a refusal, not a bad afternoon.
func Build(root, out string) error {
	if err := ensureSafeToOverwrite(out); err != nil {
		return err
	}
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	render := func(srcPath, destName, srcDir string) error {
		md, err := os.ReadFile(filepath.Join(root, srcPath))
		if err != nil {
			return err
		}
		body, err := Render(string(md), srcDir)
		if err != nil {
			return fmt.Errorf("%s: %w", srcPath, err)
		}
		return os.WriteFile(filepath.Join(out, destName), []byte(body), 0o644)
	}
	for _, page := range Pages {
		if err := render("docs/"+page+".md", page+".md", "docs"); err != nil {
			return err
		}
	}
	// The README is the landing page: it opens with the problem this project
	// solves, which is the paragraph a search result or an AI answer quotes.
	if err := render("README.md", "index.md", ""); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "_config.yml"), []byte(configYAML), 0o644)
}
