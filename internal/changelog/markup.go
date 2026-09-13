package changelog

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// FileMarkupProblem is MarkupProblem for a file as it is written, line endings
// normalised the way Load reads fragments, so a reported line number is the
// file's own.
func FileMarkupProblem(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return MarkupProblem(strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", "\n"), "\r", "\n")), nil
}

// htmlTagRe is what a Markdown renderer takes for raw HTML: an open or closing
// tag (`<`, an optional `/`, a letter, then letters, digits and hyphens, ending
// at `>`, `/>` or whitespace before attributes), a comment (`<!--`) or a
// processing instruction (`<?`). GitHub drops tags it does not know, text and
// all — `<svc>` disappears from the page — and hides comments. An autolink
// (`<https://…>`) does not match: its name is followed by `:`.
var htmlTagRe = regexp.MustCompile(`^(</?[A-Za-z][A-Za-z0-9-]*(\s[^<>]*)?/?>|<!--|<\?)`)

// MarkupProblem reports the first place where a fragment's body renders
// differently from how it reads, or "" when there is none. It looks only
// outside code spans, which is where Markdown does something to the text:
//
//   - a backslash before a backtick. Outside a code span that is an escaped
//     backtick — the character itself — so `a \`b\` c` does not nest one code
//     span in another; the outer span ends at the first backtick inside. A code
//     span that has to contain backticks is opened and closed with two
//     backticks each instead.
//     Inside a code span a backslash is literal, and `\"` there is correct.
//   - an HTML tag, such as a `<name>` placeholder: GitHub drops it from the page.
//     Placeholders belong inside a code span.
//
// Code spans follow CommonMark: a run of N backticks opens one, the next run of
// exactly N in the same paragraph closes it (across single line breaks), and a
// run with no closing partner is literal text. A paragraph ends at a blank line
// or at a line that starts the next entry (`- ` at the margin); neither a code
// span nor a tag reaches past it. A code fence (``` or ~~~) within three
// spaces of the margin — a fragment's own continuation lines are indented two —
// or within three spaces after a block quote marker (`>` and its one optional
// space, nested or not), is skipped whole.
// A fence or a `>` indented further, a `>` after a list marker, a tab, and an
// indented code block are not recognised — telling most of them apart needs the list
// structure — so their contents are checked as text, and every report says so.
func MarkupProblem(body string) string {
	text := blankFencedBlocks(body)
	for i := 0; i < len(text); {
		switch c := text[i]; {
		case c == '\\' && i+1 < len(text) && text[i+1] == '`':
			return fmt.Sprintf("line %d has \\` outside a code span, which renders as a plain backtick, never as code inside code — "+
				"to show backticks inside a code span, open and close it with two backticks and a space on each side (`` like `this` ``): %q"+notRecognised, lineOf(body, i), around(body, i))
		case c == '\\' && i+1 < len(text):
			i += 2 // an escaped character, a backslash included
		case c == '`':
			n := runLength(text, i)
			if end := closingRun(text[:blockEnd(text, i)], i+n, n); end >= 0 {
				i = end + n
			} else {
				i += n
			}
		case c == '<' && htmlTagRe.MatchString(text[i:blockEnd(text, i)]):
			tag := htmlTagRe.FindString(text[i:blockEnd(text, i)])
			return fmt.Sprintf("line %d has %s outside a code span, which GitHub reads as HTML — an unknown tag is dropped from the page, a comment is hidden — "+
				"put it inside backticks: %q"+notRecognised, lineOf(body, i), tag, around(body, i))
		default:
			i++
		}
	}
	return ""
}

// notRecognised says, in every report, where the check stops reading Markdown,
// so a red result is not taken as proof that correct Markdown is wrong.
const notRecognised = " (a code fence is skipped when it starts within three spaces of the margin, or within three spaces after a block quote marker — `>` and its one optional space, nested or not; " +
	"a fence or a `>` indented further, a `>` after a list marker, a tab, and an indented code block are not recognised: their contents are checked as text, " +
	"and a fence line after them may be taken as opening a block, which hides what follows from the check)"

// fenceOpenRe is an opening code fence: up to three spaces, then three or more
// backticks or tildes. A backtick fence's info string may not hold a backtick.
var fenceOpenRe = regexp.MustCompile("^ {0,3}(`{3,}[^`]*|~{3,}.*)$")

// blankFencedBlocks returns body with every fenced code block — its fence lines
// and what lies between — replaced by spaces, byte for byte, so offsets and line
// numbers still point into body. Each line is read after its block quote
// markers (`>`, and one space after each). A block closes at a line holding only
// a fence of the same character at least as long as the one that opened it; one
// opened inside a block quote also ends where the quote does; one that never
// closes runs to the end.
func blankFencedBlocks(body string) string {
	lines := strings.SplitAfter(body, "\n")
	var open byte
	var width, depth int
	for i, line := range lines {
		content := strings.TrimRight(line, "\n")
		limit := -1
		if open != 0 {
			limit = depth // inside a fence, a further `>` is the code's own text
		}
		inner, d := unquote(content, limit)
		if open != 0 && d < depth {
			open = 0 // the quote holding the fence has ended
		}
		trimmed := strings.TrimLeft(inner, " ")
		lead := len(inner) - len(trimmed)
		switch {
		case open == 0:
			if !fenceOpenRe.MatchString(inner) {
				continue
			}
			open, width, depth = trimmed[0], fenceWidth(trimmed), d
		case lead <= 3 && trimmed != "" && trimmed[0] == open && fenceWidth(trimmed) >= width &&
			strings.TrimSpace(trimmed[fenceWidth(trimmed):]) == "":
			open = 0
		}
		lines[i] = strings.Repeat(" ", len(content)) + line[len(content):]
	}
	return strings.Join(lines, "")
}

// unquote strips a line's block quote markers — up to three spaces, `>`, and one
// optional space, as many times as they nest, but no more than limit when limit
// is not negative — returning the rest and how many markers it stripped.
func unquote(line string, limit int) (string, int) {
	d := 0
	for d != limit {
		rest := strings.TrimLeft(line, " ")
		if len(line)-len(rest) > 3 || !strings.HasPrefix(rest, ">") {
			return line, d
		}
		line = strings.TrimPrefix(rest[1:], " ")
		d++
	}
	return line, d
}

// fenceWidth is the number of fence characters s starts with.
func fenceWidth(s string) int {
	n := 0
	for n < len(s) && s[n] == s[0] {
		n++
	}
	return n
}

// blockEnd is where the paragraph holding i ends: the start of the next line
// that is blank — fenced code has been blanked to spaces by then, so a fence
// ends a paragraph too — or that starts a new entry with `- ` at the margin; or
// the end of text.
func blockEnd(text string, i int) int {
	for j := strings.IndexByte(text[i:], '\n'); j >= 0; {
		next := i + j + 1
		rest := text[next:]
		line := rest
		if k := strings.IndexByte(rest, '\n'); k >= 0 {
			line = rest[:k]
		}
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "- ") {
			return next
		}
		i = next
		j = strings.IndexByte(text[i:], '\n')
	}
	return len(text)
}

// runLength is the number of backticks starting at i.
func runLength(s string, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] == '`' {
		n++
	}
	return n
}

// closingRun finds, at or after from, the start of a run of exactly n backticks,
// or -1.
func closingRun(s string, from, n int) int {
	for j := from; j < len(s); {
		if s[j] != '`' {
			j++
			continue
		}
		m := runLength(s, j)
		if m == n {
			return j
		}
		j += m
	}
	return -1
}

func lineOf(s string, i int) int { return strings.Count(s[:i], "\n") + 1 }

// around is the text of the line holding i, trimmed, for the message.
func around(s string, i int) string {
	start := strings.LastIndex(s[:i], "\n") + 1
	end := strings.Index(s[i:], "\n")
	if end < 0 {
		end = len(s)
	} else {
		end += i
	}
	line := []rune(strings.TrimSpace(s[start:end]))
	if len(line) > 120 {
		return string(line[:120]) + "…"
	}
	return string(line)
}
