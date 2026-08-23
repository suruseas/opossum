// Package claimcite finds sentences that state something someone had to watch a
// container do, without naming where it was seen.
//
// The changelog is where opossum tells people what it does, and a few of those
// sentences are reports about another program's behaviour: a host path works at
// this mount, an image starts and leaves the directory empty. When a report like
// that is written from memory rather than from a recorded run, nothing downstream
// notices — it reads exactly like the ones that were measured. A fragment went out
// saying a shape "works" with nothing tying the sentence to anything. The run
// existed, and landed in the same commit; a reader had no way to know that, and
// neither did anyone checking.
//
// So the rule is a vocabulary one: a sentence using one of a few phrases has to
// name a run that exists here, say where it was measured, or say plainly that it
// was not.
//
// The phrases are not an inventory of mistakes. One of them — "a host path works"
// — is the wording that actually went out uncited; "starts and writes" was added
// after a sentence using it went past this check; the rest are near neighbours,
// picked by hand. Two things shaped the list more than recall did:
// phrases beat single words ("works" alone catches "works out where the data
// goes", which describes opossum, not something watched), and wordings this
// project uses for opossum's own behaviour are left out on purpose. "refuses to
// start" and "fails with" are how the documentation describes opossum declining
// a project; a check that fires on those would fire on ordinary sentences, and a
// check that cries wolf is answered by bending the sentence until it stops.
//
// The line that draws is narrow and worth stating: left out are the wordings for
// opossum turning a project away, which it does by design and which no run is
// needed to support. "works here" sits closest to that line — a sentence can say
// a compose file works here and mean opossum rather than an image — and it stays,
// because a claim that something works somewhere is the one that went out wrong.
//
// What this does not do, so a green run is not read as more than it is:
//
//   - It does not check that the named run shows what the sentence says. The file
//     has to exist; whether it supports the claim is not something this reads,
//     and that difference is exactly where several days went.
//   - It does not read what people write outside the repository — a pull request
//     description is not here to be checked.
//   - It reads the fragments waiting for a release. A release folds them into
//     CHANGELOG.md and deletes them, and no published section is ever read again,
//     so a sentence has one chance to be looked at: while its fragment exists.
//     A published section already carries one such sentence (#496).
//   - It matches phrases, so a claim worded another way passes, including the
//     past and plural forms of these ones ("worked there", "host paths work
//     there") and every wording that avoids them ("a bind mount works at that
//     path", "mounting one level up works fine"). The list grows when a sentence
//     gets past it, not before. Code ticks, stars and underscores inside a phrase
//     are ignored, so dressing a word up does not hide it; a word inserted into
//     the middle of one does.
//   - A sentence is the unit, and it cuts both ways. Saying where one claim was
//     measured clears the whole sentence, so a second, uncited claim beside it
//     passes too; and naming the run in the *next* sentence does not count, so
//     prose that cites itself a sentence later is reported.
//   - Provenance is a phrase, not a fact: "measured on a bad day" answers this
//     check as well as a version number does.
package claimcite

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// verbs are the wordings that turn a sentence into a report of something someone
// watched a container do. See the package comment for where they came from and
// for the wordings deliberately left out.
var verbs = []string{
	"a host path works", "works there", "works here",
	"starts and leaves", "starts and writes",
	"leaves the mount empty",
}

// hedges let a sentence use one of those verbs while saying it was not measured.
// A claim withdrawn in the open is not the problem this package is about.
var hedges = []string{
	"not measured",
	"is not something measured here",
}

// provenance is the other way to say where a claim comes from, for prose that
// people outside this repository read: a changelog cannot sensibly cite a file
// under testdata/, but it can say which version or runtime the claim was taken
// from. Either form answers the same question — where did you see this?
var provenance = []string{
	"measured on", "measured in", "measured with",
	"measured against", "measured over",
	"recorded in", "observed on", "observed in",
}

// cite is a possible reference to a saved run. Matching here only makes a name a
// candidate: KnownRuns decides whether the name is a run this repository has, so
// an ordinary word like compose.json is not a citation unless a run is called
// that.
//
// Both forms stop at the extension rather than running to the end of the word.
// Letting the path form take any character meant it swallowed the full stop of
// the sentence it ended, and a run cited correctly at the end of a sentence was
// reported — the check punishing the thing it asks for.
var cite = regexp.MustCompile(`testdata/[A-Za-z0-9_/-]+\.[A-Za-z0-9]+|\b[A-Za-z0-9][A-Za-z0-9_-]*\.(?i:txt|json)\b`)

// KnownRuns reports whether a cited name is a run that exists. Names arrive as
// the bare file name, so a path and a bare mention of the same run answer alike.
// A nil KnownRuns takes any name on trust, which is only right for a caller that
// has no runs to check against.
type KnownRuns func(name string) bool

// Finding is one sentence that reports a measurement without naming a run.
type Finding struct {
	File     string
	Sentence string
	Verb     string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s: %q says %q without naming the run it came from", f.File, f.Sentence, f.Verb)
}

// Check returns a finding for every sentence in text that uses one of the verbs,
// names no run that known recognises, and does not withdraw the claim.
//
// Sentences are split where a full stop, question mark or exclamation mark meets
// whitespace, which is coarse in both directions: an abbreviation splits a
// sentence in two, and each half is judged on its own; a full stop with no space
// after it does not split at all, so two sentences run together and provenance in
// the first covers the second. The first errs towards reporting and the second
// towards silence.
func Check(file, text string, known KnownRuns) []Finding {
	var out []Finding
	for _, s := range sentences(text) {
		flat := unmarked.Replace(strings.ToLower(s))
		if cites(s, known) || containsAny(flat, hedgePhrases) || containsAny(flat, provenancePhrases) {
			continue
		}
		for i, v := range verbPhrases {
			if v.MatchString(flat) {
				out = append(out, Finding{File: file, Sentence: strings.TrimSpace(s), Verb: verbs[i]})
				break
			}
		}
	}
	return out
}

// cites reports whether the sentence names a run that exists. A name nobody can
// open is not a citation: without this, writing foo.txt answers the check.
// A sentence can mention more than one thing that looks like a file — compose.json
// and then the run it was actually measured in — so every candidate is offered,
// not just the first.
func cites(s string, known KnownRuns) bool {
	for _, m := range cite.FindAllString(s, -1) {
		if known == nil || known(path.Base(m)) {
			return true
		}
	}
	return false
}

func sentences(text string) []string {
	var out []string
	// Paragraphs first, joined: these files are hand-wrapped, and a claim and the
	// run it names sit on either side of a line break often enough that splitting
	// on newlines reports sentences that are perfectly well cited.
	for _, para := range paragraphs(text) {
		flat := strings.Join(strings.Fields(para), " ")
		out = append(out, sentenceEnd.Split(flat, -1)...)
	}
	return out
}

// sentenceEnd is where one sentence stops and the next begins. A question mark
// ends a sentence as surely as a full stop does, and treating it as ordinary text
// joins a question to the statement after it — so "Was it measured on 18? A host
// path works there." reads as one sentence with provenance in it.
var sentenceEnd = regexp.MustCompile(`[.?!]\s+`)

// paragraphs splits prose into the units a citation can cover. A blank line ends
// one, and so does the start of a list item: entries are written as bullets, and
// one bullet saying where it was measured says nothing about the next.
//
// Every list item is its own unit, however deeply it is nested. That cuts one way
// a writer may not expect: naming the run in a bullet *underneath* the claim does
// not answer for the claim, because the same rule that keeps a sibling's
// provenance out would otherwise let it in. Cite in the bullet that makes the
// claim.
//
// Lines are taken one at a time, so a carriage return needs no handling of its
// own: it leaves a blank line blank and is dropped with the rest of the
// whitespace when the paragraph is joined.
func paragraphs(text string) []string {
	var out []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if listItem.MatchString(line) {
			flush()
		}
		cur = append(cur, line)
	}
	flush()
	return out
}

// listItem is the start of an entry in a list, at any indent and by any of the
// markers markdown allows. Reading only "- " would let the same fragment written
// with stars keep a sibling's provenance.
var listItem = regexp.MustCompile(`^[ \t]*(?:[-*+]|[0-9]+[.)])\s`)

func containsAny(s string, res []*regexp.Regexp) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// phrases turns each phrase into a match that begins and ends where a word does.
// Plain substring matching read "measured only in my head" as the phrase
// "measured on" and let the sentence through — a way past this check that nobody
// chose and nobody could see. The leading boundary is the same rule read the
// other way: "reworks there" is not "works there".
func phrases(list []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(list))
	for i, s := range list {
		out[i] = regexp.MustCompile(`\b` + regexp.QuoteMeta(s) + `\b`)
	}
	return out
}

// unmarked drops the marks that dress a word up without changing it. A phrase
// stays the phrase when a writer puts code ticks round one of its words, and
// prose here is full of them: without this, "a host path `works` there" says
// exactly what the flagged sentence says and is not seen.
var unmarked = strings.NewReplacer("`", "", "*", "", "_", "")

var (
	verbPhrases       = phrases(verbs)
	hedgePhrases      = phrases(hedges)
	provenancePhrases = phrases(provenance)
)
