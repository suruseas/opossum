package changelog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/changelog"
)

// A fragment is published as Markdown — into CHANGELOG.md and, from there, the
// GitHub release page — and two ways of writing it read fine as a file but not
// on the page. Each row is one body and what the check must say about it; the
// quiet rows are the correct spellings next to each refused one, so a check that
// refuses too much is caught as surely as one that refuses too little.
func TestMarkupProblem(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       string // a piece of the message, or "" for none
	}{
		{"plain prose", "- `down` leaves the container alone.", ""},
		{"a backslash-escaped backtick meant to nest code", "- the refusal says `ports entry 2 of 2 has no target — as in \\`target: 80\\``.", "line 1 has \\` outside a code span"},
		{"the same, nested with a double-backtick span", "- the refusal says `` ports entry 2 of 2 has no target — as in `target: 80` ``.", ""},
		{"a backslash inside a code span is literal", "- escapes `\\\"`, `\\\\` and `\\$` the way the shell does.", ""},
		{"a lone backslash in a code span", "- a trailing `\\` continues the line.", ""},
		{"an escaped backtick on a later line", "- first line\n  second line has \\` here", "line 2 has \\`"},
		{"a code span across a line break", "- `a\n  b` then \\` after", "line 2 has \\`"},
		{"an unmatched run is text, and what follows is still checked", "- three ``` then later \\`", "line 1 has \\`"},
		{"a placeholder outside a code span", "- run opossum logs <svc> to see it.", "line 1 has <svc> outside a code span"},
		{"a placeholder inside a code span", "- run `opossum logs <svc>` to see it.", ""},
		{"a closing tag outside", "- ends with </details> here", "has </details> outside"},
		{"a tag with attributes", "- <a href=\"x\">link</a>", "has <a href=\"x\"> outside"},
		{"an autolink is not a tag", "- see <https://example.com/x>.", ""},
		{"comparison signs are not a tag", "- when a < b and c > d, nothing happens.", ""},
		{"a version placeholder with dots is not a tag", "- release-notes.d/<x.y.z>.md is theirs.", ""},
		{"an escaped angle bracket", "- \\<svc> is literal.", ""},
		{"an escaped backslash before a backtick opens a span", "- path `C:\\\\` then `x`", ""},
		{"an escaped backslash outside, then a code span", "- ends in \\\\`x` here", ""},
		{"a longer run inside a span does not close it", "- `a``b` then \\` after", "line 1 has \\`"},
		{"only a run of the same length closes a span", "- `x``` <svc> `", ""},
		{"a less-than sign before a placeholder in a code span", "- when a < b, run `opossum logs <svc>`", ""},
		{"a tilde fence is code", "- run it:\n\n  ~~~\n  opossum logs <svc>\n  ~~~", ""},
		{"a backtick fence is code, escapes and all", "- it says:\n\n  ```text\n  as in \\`x\\` and <svc>\n  ```", ""},
		{"after a fence closes, text is checked again, on its own line number", "- run it:\n\n  ~~~\n  ok <svc>\n  ~~~\n  then <svc> here", "line 6 has <svc>"},
		{"a shorter fence does not close a longer one", "- x\n\n  ````\n  ```\n  <svc>\n  ````\n  done", ""},
		{"a fence of the other character does not close it", "- x\n\n  ~~~\n  ```\n  <svc>\n  ~~~\n  after <svc>", "line 7 has <svc>"},
		{"a fence that never closes runs to the end", "- x\n\n  ```\n  <svc>", ""},
		{"an indented code block is not recognised, and the report says so", "- x\n\n        <svc>", "and an indented code block are not recognised: their contents are checked as text"},
		{"an HTML comment is hidden from the page", "- see <!-- note --> here", "has <!-- outside"},
		{"a code span does not pair across a blank line", "- `a <svc>\n\n  b` end", "line 1 has <svc>"},
		{"a code span does not pair across entries", "- first `a <svc>\n- second b` end", "line 1 has <svc>"},
		{"an unpaired backtick in one paragraph leaves the next one alone", "- `a\n\n  b` <svc> `c`", ""},
		{"a tag does not reach across a blank line", "- the count <n\n\n  paragraph two -> done", ""},
		{"a problem after a correct code span on the same line", "- run `opossum logs <svc>` then <svc>", "has <svc> outside"},
		{"a self-closing tag", "- a <br/> here", "has <br/> outside"},
		{"a tag whose attributes continue on the next line", "- a <a\n  href=\"x\">link</a>", "line 1 has <a\n  href=\"x\"> outside"},
		{"a digit after the bracket is not a tag", "- a <1> b", ""},
		{"the report quotes the line", "- first\n  run opossum logs <svc> to see it.", `: "run opossum logs <svc> to see it."`},
		{"a backtick fence's info string may not hold a backtick", "- x\n\n  ``` a`b\n  <svc>\n  ```", "line 4 has <svc>"},
		{"a tilde fence's info string may hold a tilde", "- x\n\n  ~~~ a~b\n  <svc>\n  ~~~", ""},
		{"a closing fence has nothing after it but spaces", "- x\n\n  ~~~\n  a\n  ~~~x\n  <svc>\n  ~~~   ", ""},
		{"a longer closing fence closes", "- x\n\n  ~~~\n  <svc>\n  ~~~~\n  then <svc>", "line 6 has <svc>"},
		{"a blank line inside a fence", "- x\n\n  ~~~\n  a\n\n  <svc>\n  ~~~", ""},
		{"a fence ends a paragraph without a blank line before it", "- `a\n  ~~~\n  code\n  ~~~\n  b <svc>` z", "line 5 has <svc>"},
		{"a processing instruction", "- a <?php x ?> b", "has <? outside"},
		{"the backtick report states the limit too", "- a \\` b", "are not recognised"},
		{"a tilde fence in a block quote is code", "- x\n\n  > ~~~\n  > <svc>\n  > ~~~", ""},
		{"a backtick fence in a block quote is code, info string and all", "> [!WARNING]\n> ```sh\n> opossum logs <svc>\n> ````", ""},
		{"an odd backtick inside a quoted fence stays code, and checking resumes after it", "> ```\n> a ` b <svc>\n> ````\n\nthen <svc>", "line 5 has <svc>"},
		{"a deeper `>` inside a fence is code, not a closing fence", "~~~\n> ~~~\n<svc>\n~~~", ""},
		{"a quoted fence shown inside a fence does not close it", "```markdown\n> [!WARNING]\n> ```sh\n> opossum logs <svc>\n> ```\n```\n\nafter <svc>", "line 8 has <svc>"},
		{"a deeper `>` inside a quoted fence is code", "> ~~~\n> > ~~~\n> <svc>\n> ~~~", ""},
		{"a quoted fence closes inside the quote, and checking resumes there", "> ~~~\n> <svc>\n> ~~~\n> after <svc>", "line 4 has <svc>"},
		{"one of two nested quotes ending ends the fence", "> > ~~~\n> <svc>", "line 2 has <svc>"},
		{"a fence after a quoted fence starts at the margin again", "> ~~~\n> x\n> ~~~\n\n~~~\n<svc>\n~~~", ""},
		{"a `>` indented three is a block quote", "   > ~~~\n   > <svc>\n   > ~~~", ""},
		{"five spaces after `>` is an indented code block, not a fence", ">     ~~~\n> <svc>", "line 2 has <svc>"},
		{"a nested block quote", "> > ~~~\n> > <svc>\n> > ~~~", ""},
		{"a marker indented four continues the paragraph, so its fence is not a fence", "text\n    > ~~~\n    > <svc>\n    > ~~~", "line 3 has <svc>"},
		{"a quoted fence after a margin fence is still skipped", "~~~\nx\n~~~\n\n> ~~~\n> <svc>\n> ~~~", ""},
		{"the line that ends a quote can open a new fence", "> > ~~~\n> ~~~\n> <svc>\n> ~~~\n> after <svc>", "line 5 has <svc>"},
		{"the one space after the marker is the quote's, the next three the fence's", ">    ~~~\n>    <svc>\n>    ~~~", ""},
		{"a quoted fence ends where the quote ends", "> ~~~\n> <svc>\n\nafter <svc>", "line 4 has <svc>"},
		{"a fence indented further is not recognised, and the report says so", "- x\n\n      ~~~\n      <svc>\n      ~~~", "a fence or a `>` indented further"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := changelog.MarkupProblem(tc.body)
			switch {
			case tc.want == "" && got != "":
				t.Errorf("want no problem, got: %s", got)
			case tc.want != "" && !strings.Contains(got, tc.want):
				t.Errorf("want a problem containing %q, got: %q", tc.want, got)
			}
		})
	}
}

// Every fragment waiting in changelog.d renders as it reads. Published sections
// are not checked: they are never rewritten here.
func TestFragmentsRenderAsWritten(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "changelog.d")
	// No fragments is a real state (right after a release); no directory is a
	// test looking in the wrong place, which Load would also answer with none.
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Fatalf("changelog.d is not where this test looks: %v", err)
	}
	frags, err := changelog.Load(dir)
	if err != nil {
		t.Fatalf("reading changelog.d: %v", err)
	}
	for _, f := range frags {
		p, err := changelog.FileMarkupProblem(f.Path)
		if err != nil {
			t.Fatal(err)
		}
		if p != "" {
			t.Errorf("%s: %s", filepath.Base(f.Path), p)
		}
	}
}

// A line number names the file's line, not the trimmed entry's: blank lines at
// the top of a fragment are counted.
func TestFileMarkupProblemCountsTheFilesLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "1-x.fixed.md")
	// A CRLF, a lone CR and an LF: each is one line ending.
	if err := os.WriteFile(p, []byte("\r\n\r- first\n  then <svc>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := changelog.FileMarkupProblem(p)
	if err != nil || !strings.Contains(got, "line 4 has <svc>") {
		t.Errorf("want line 4 of the file, got %q (err %v)", got, err)
	}
}
