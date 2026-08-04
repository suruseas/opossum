// Package changelog assembles CHANGELOG entries from per-change fragment files.
//
// Every PR editing a shared `## [Unreleased]` section is a merge conflict waiting
// to happen, and worse: when one branch is released before another merges, the
// late entry lands inside a published version's section. Both problems come from
// the same shape — many changes writing one file. So each change adds its own file
// under changelog.d/ instead, and this package is what turns those back into the
// CHANGELOG the world reads.
//
// A fragment is named `<number>-<slug>.<type>.md`, where number is the PR or issue
// it belongs to and type is one of the Keep a Changelog sections. Its body is the
// entry itself, exactly as it would have been written by hand.
package changelog

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// versionHeadingRe is what the changelog parser treats as the start of a released
// section; a fragment body must never contain one.
var versionHeadingRe = regexp.MustCompile(`(?m)^## \[[0-9]`)

// Types are the Keep a Changelog sections, in the order they must appear. The
// order is part of the output contract: released sections were written by hand in
// this order, and the assembled ones have to be indistinguishable from them.
var Types = []string{"added", "changed", "deprecated", "removed", "fixed", "security"}

// heading maps a fragment type to its section heading.
func heading(t string) string { return "### " + strings.ToUpper(t[:1]) + t[1:] }

// Fragment is one recorded change.
type Fragment struct {
	Path   string
	Number int // the PR or issue it belongs to; 0 when the name has no number
	Slug   string
	Type   string
	Body   string // the entry, trimmed, starting with "- "
}

// Load reads every fragment in dir, sorted the way they will be rendered: by
// number, then slug. Ordering is fixed so the assembled section is reproducible —
// a changelog that shuffles between runs can't be golden-tested.
func Load(dir string) ([]Fragment, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Fragment
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == "README.md" {
			continue
		}
		f, err := parseName(e.Name())
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		// Normalise line endings and trim both ends: a CR would reach the published
		// file verbatim, and leading blank lines would turn the section into a loose
		// list. The ratchet can't catch either — both sides derive from the same
		// fragment, so a corrupted entry round-trips cleanly.
		body := strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n"))
		if strings.TrimSpace(body) == "" {
			return nil, fmt.Errorf("%s: the fragment is empty", e.Name())
		}
		if !strings.HasPrefix(body, "- ") {
			return nil, fmt.Errorf("%s: a fragment must be one entry starting with \"- \"", e.Name())
		}
		// A line that looks like a version heading would be mistaken for the start of
		// a released section when the changelog is next parsed, and regeneration
		// would then duplicate everything after it. Refuse it here rather than let a
		// routine `make changelog` damage the file.
		if versionHeadingRe.MatchString(body) {
			return nil, fmt.Errorf("%s: the body has a line starting with \"## [<version>\", which would be read as a "+
				"release heading — indent it or reword it", e.Name())
		}
		// The changelog is written in English, and a fragment is published into it
		// verbatim. Everything else about a change is discussed in Japanese, so
		// writing the entry in Japanese too is the easy mistake — and it is only
		// visible once the entry has been assembled into the file the world reads.
		if r := firstCJK(body); r != "" {
			return nil, fmt.Errorf("%s: the entry is published into CHANGELOG.md as written, which is in English — "+
				"this one has %q in it", e.Name(), r)
		}
		f.Path = filepath.Join(dir, e.Name())
		f.Body = body
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Number != out[j].Number {
			return out[i].Number < out[j].Number
		}
		return out[i].Slug < out[j].Slug
	})
	return out, nil
}

// firstCJK returns the first Han, Hiragana, or Katakana character in s, or "" if
// there is none. It reports the character rather than a bool so the error can show
// what it found — "there is Japanese in this file" is a slow thing to act on when
// the file is a paragraph long.
//
// Deliberately not a general script check. Accented Latin, Greek letters in maths,
// and the em dashes and arrows this changelog is full of are all fine; what is
// being caught is an entry written in the language everything around the code is
// discussed in.
func firstCJK(s string) string {
	for _, r := range s {
		switch {
		case r >= 0x3040 && r <= 0x309F, // hiragana
			r >= 0x30A0 && r <= 0x30FF, // katakana
			r >= 0x4E00 && r <= 0x9FFF: // CJK unified ideographs
			return string(r)
		}
	}
	return ""
}

// parseName splits `<number>-<slug>.<type>.md`.
func parseName(name string) (Fragment, error) {
	base := strings.TrimSuffix(name, ".md")
	dot := strings.LastIndexByte(base, '.')
	if dot < 0 {
		return Fragment{}, fmt.Errorf("missing a .<type> before .md (want <number>-<slug>.<type>.md)")
	}
	typ := base[dot+1:]
	if !validType(typ) {
		return Fragment{}, fmt.Errorf("unknown type %q (want one of %s)", typ, strings.Join(Types, ", "))
	}
	rest := base[:dot]
	dash := strings.IndexByte(rest, '-')
	if dash <= 0 {
		return Fragment{}, fmt.Errorf("missing a <number>- prefix (want <number>-<slug>.<type>.md)")
	}
	n, err := strconv.Atoi(rest[:dash])
	if err != nil {
		return Fragment{}, fmt.Errorf("%q is not a PR or issue number", rest[:dash])
	}
	return Fragment{Number: n, Slug: rest[dash+1:], Type: typ}, nil
}

func validType(t string) bool {
	for _, k := range Types {
		if k == t {
			return true
		}
	}
	return false
}

// RenderBody renders the `### Section` blocks for a set of fragments, without any
// version heading. This is the shared core: the released section and the
// Unreleased preview must be assembled by the same code, or the preview stops
// predicting what a release will look like.
func RenderBody(frags []Fragment) string {
	var b strings.Builder
	for _, t := range Types {
		var section []Fragment
		for _, f := range frags {
			if f.Type == t {
				section = append(section, f)
			}
		}
		if len(section) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(heading(t) + "\n\n")
		for _, f := range section {
			b.WriteString(f.Body + "\n")
		}
	}
	return b.String()
}
