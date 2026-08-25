package claimcite_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/changelog"
	"github.com/suruseas/opossum/internal/claimcite"
)

// repoRoot is two levels up from this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(wd))
}

// knownRuns is the set of saved runs this repository actually has, by file name.
// The check resolves citations against it, so a name nobody can open is not a
// citation — see TestANameNobodyCanOpenIsNotACitation.
func knownRuns(t *testing.T) claimcite.KnownRuns {
	t.Helper()
	have := map[string]bool{}
	root := filepath.Join(repoRoot(t), "testdata")
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			// Folded, because a file name is the same run whether or not the
			// sentence shouted it; the check hands the name over as written.
			have[strings.ToLower(d.Name())] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(have) == 0 {
		t.Fatalf("no saved runs found under %s; the citation side of this test would pass vacuously", root)
	}
	return func(name string) bool { return have[strings.ToLower(name)] }
}

// The fragment that prompted this package, as it went out: a claim about what a
// host path does, with no run behind it, and a saved run in this repository that
// records that operation failing.
//
// It is here whole, hand-wrapping and all, for two reasons. The claim sits in the
// third sentence, so a check that reads only the first one passes this text while
// missing the thing it was built for. And the claim and its wrapping sit on
// either side of a line break, which is how a citation two words later ends up on
// the far side of a naive split.
const fragmentThatPromptedThis = `- ` + "`up`" + ` now explains Postgres 18's refusal to start when the mount sits at the data
  directory 17 and earlier used. Postgres 18 keeps the cluster in a major-version
  subdirectory, so the image wants a single mount at ` + "`/var/lib/postgresql`" + `; a mount
  at ` + "`/var/lib/postgresql/data`" + ` is data it will not use, and it exits saying so.
  The crash report now names the fix (` + "`[OPSM-110]`" + `): move the mount up one level —
  a host path works there, since the image creates the subdirectory itself. Data an
  earlier major version wrote is a separate question: the image asks for
  ` + "`pg_upgrade`" + `, and moving the mount does not do that — 18 refuses a cluster an
  earlier version wrote even at the path it asks for.
`

func TestTheClaimThatPromptedThisIsReportedFromWhereItSits(t *testing.T) {
	found := claimcite.Check("485.md", fragmentThatPromptedThis, knownRuns(t))
	if len(found) != 1 {
		t.Fatalf("the uncited claim in this fragment should be reported exactly once, got %d: %v", len(found), found)
	}
	if found[0].Verb != "a host path works" {
		t.Errorf("the report should name the wording that makes it a claim, got %q", found[0].Verb)
	}
	if !strings.Contains(found[0].Sentence, "move the mount up one level") {
		t.Errorf("the report should quote the sentence the claim is in, got %q", found[0].Sentence)
	}
}

// Every phrase in the vocabulary has to be one the check actually fires on.
// Without a case each, most of the list can be deleted with the tests still green.
func TestEveryWordingIsReported(t *testing.T) {
	for _, s := range []string{
		"Move the mount up one level: a host path works at the new place.",
		"With the mount one level up, a bind works there.",
		"A bind works here, unlike at the old path.",
		"The image starts and leaves the directory alone.",
		"The image starts and writes to the host directory.",
		"The image leaves the mount empty and puts the cluster elsewhere.",
	} {
		if found := claimcite.Check("frag.md", s, knownRuns(t)); len(found) != 1 {
			t.Errorf("%q reports something someone watched; it should be flagged, got %v", s, found)
		}
	}
}

// These files are hand-wrapped, so a claim and the run it names land on either
// side of a line break as a matter of course. Reading line by line reports a
// sentence that is perfectly well cited — a false alarm that teaches writers to
// distrust the check.
func TestACitationOnTheNextLineStillCounts(t *testing.T) {
	wrapped := "- The mount moves up one level, and a host path works there,\n  measured on Postgres 18, since the image makes the subdirectory.\n"
	if found := claimcite.Check("frag.md", wrapped, knownRuns(t)); len(found) != 0 {
		t.Errorf("the provenance is one line down, not absent; this should pass, got %v", found)
	}
	cited := "- The mount moves up one level, and a host path works there; the run is in\n  pg18-mount-one-level-up.txt.\n"
	if found := claimcite.Check("frag.md", cited, knownRuns(t)); len(found) != 0 {
		t.Errorf("the run is named one line down; this should pass, got %v", found)
	}
	// The same wrapping decides where one sentence ends. A full stop at a line
	// break has to end a sentence like any other, or a cited sentence and the
	// uncited one after it are read as one, and the citation covers both.
	twoSentences := "- The image was measured on Postgres 18, which keeps the cluster deeper.\n  A host path works there, so the mount is left alone.\n"
	if found := claimcite.Check("frag.md", twoSentences, knownRuns(t)); len(found) != 1 {
		t.Errorf("the second sentence cites nothing of its own; it should be flagged, got %v", found)
	}
}

func TestNamingTheRunIsEnough(t *testing.T) {
	for _, s := range []string{
		"A host path works there — measured in pg18-mount-one-level-up.txt.",
		"A host path works there (testdata/error-wordings/pg18-mount-one-level-up.txt).",
		// A run under testdata/ that is not a .txt or .json is reachable by its
		// path and no other way; without a case for it the path form carries no
		// weight, because every other path here ends in a name the bare form
		// matches too.
		"A host path works there — see testdata/real-cli-output.md.",
		// Prose that people outside this repository read cannot sensibly cite a
		// file, so saying which version it was measured on is the other way
		// through. No file name appears here on purpose: without this case the
		// citation path alone would cover both.
		"A host path works there, measured on Postgres 18.",
		"A host path works there, measured against docker compose v5.3.1.",
	} {
		if found := claimcite.Check("frag.md", s, knownRuns(t)); len(found) != 0 {
			t.Errorf("%q says where it came from; it should pass, got %v", s, found)
		}
	}
}

// Every way through has to be one the check actually honours — one case each, the
// same standard the vocabulary is held to. Without this most of these lists can be
// deleted with the tests still green, and an exemption nobody tests is an exemption
// anyone can widen.
func TestEveryWayThroughIsHonoured(t *testing.T) {
	for _, s := range []string{
		// provenance: where it was seen, for prose that cannot cite a file.
		"A host path works there, measured on Postgres 18.",
		"A host path works there, measured in the 18 image.",
		"A host path works there, measured with container 1.2.2.",
		"A host path works there, measured against docker compose v5.3.1.",
		"A host path works there, measured over the whole corpus.",
		"A host path works there, recorded in the 18 run.",
		"A host path works there, observed on Postgres 18.",
		"A host path works there, observed in the 18 run.",
		// hedges: the claim withdrawn in the open.
		"Whether a host path works there on 19 is not measured.",
		"Whether a host path works there on 19 is not something measured here.",
	} {
		if found := claimcite.Check("frag.md", s, knownRuns(t)); len(found) != 0 {
			t.Errorf("%q answers the question this check asks; it should pass, got %v", s, found)
		}
	}
}

// Each shape a citation can take, fixed on its own. The path form and the bare
// form are separate branches, and a test that happens to satisfy both leaves one
// of them free to be deleted.
func TestEveryShapeOfCitationCounts(t *testing.T) {
	for _, s := range []string{
		"A host path works there (testdata/error-wordings/pg18-mount-one-level-up.txt).",
		"A host path works there — see pg18-mount-one-level-up.txt for the run.",
		"A host path works there; the image said so in postgres-declares-no-pgdata.json.",
		// The end of a sentence is where a citation most naturally lands.
		"A host path works there — see testdata/error-wordings/pg18-mount-one-level-up.txt.",
		"A host path works there — see pg18-mount-one-level-up.txt.",
		// A file name is a file name however it is capitalised.
		"A host path works there — see PG18-MOUNT-ONE-LEVEL-UP.TXT.",
	} {
		if found := claimcite.Check("frag.md", s, knownRuns(t)); len(found) != 0 {
			t.Errorf("%q names a run that exists; it should pass, got %v", s, found)
		}
	}
}

// A way through has to be the phrase, not its first letters. "measured only" is
// not "measured on", and a sentence that reads as certainty rather than
// provenance has to stay reported.
func TestAPhraseThatMerelyStartsTheSameIsNotAWayThrough(t *testing.T) {
	for _, s := range []string{
		"A host path works there, measured only in my head.",
		"A host path works there, measured informally.",
		"A host path works there, recorded internally.",
		"A host path works there, observed only once.",
	} {
		if found := claimcite.Check("frag.md", s, knownRuns(t)); len(found) != 1 {
			t.Errorf("%q says where it came from only by accident of spelling; it should be flagged, got %v", s, found)
		}
	}
}

// Prose names things that look like files without being runs, and the run it was
// measured in often comes after one of them. Offering only the first candidate
// reports a sentence that cites itself perfectly well.
func TestARunNamedAfterSomethingElseStillCounts(t *testing.T) {
	s := "A host path works there when your compose.json names the mount; the run is pg18-mount-one-level-up.txt."
	if found := claimcite.Check("frag.md", s, knownRuns(t)); len(found) != 0 {
		t.Errorf("the run is named, just not first; this should pass, got %v", found)
	}
}

// The bare form takes the two extensions runs are saved under. Anything else has
// to be cited by path, which is what changelog.d/README.md tells contributors —
// widening this quietly would make README.md a citation.
func TestTheBareFormIsOnlyForTheExtensionsRunsUse(t *testing.T) {
	s := "A host path works there — see real-cli-output.md."
	if found := claimcite.Check("frag.md", s, knownRuns(t)); len(found) != 1 {
		t.Errorf("%q names a real file, but not in a form this accepts bare; it should be flagged, got %v", s, found)
	}
	if found := claimcite.Check("frag.md", "A host path works there — see testdata/real-cli-output.md.", knownRuns(t)); len(found) != 0 {
		t.Errorf("the same file by path should pass, got %v", found)
	}
}

// Where a sentence begins is where provenance most naturally goes, and there the
// phrase is capitalised. Matching without folding the case turns every one of the
// three documented answers into a false alarm, and quietly loses the claims that
// open a sentence.
func TestTheAnswersWorkAtTheStartOfASentenceToo(t *testing.T) {
	for _, s := range []string{
		"Measured on Postgres 18, a host path works there.",
		"Recorded in the 18 run: a host path works there.",
		"Not measured: whether a host path works there on 19.",
	} {
		if found := claimcite.Check("frag.md", s, knownRuns(t)); len(found) != 0 {
			t.Errorf("%q says where it came from, at the front; it should pass, got %v", s, found)
		}
	}
	for _, s := range []string{
		"Leaves the mount empty on 18 and 17 alike.",
		"Starts and leaves the directory alone.",
	} {
		if found := claimcite.Check("frag.md", s, knownRuns(t)); len(found) != 1 {
			t.Errorf("%q opens with the claim; it should be flagged, got %v", s, found)
		}
	}
}

// The report names the wording it found. Naming a fixed one instead reads as a
// quotation of the sentence and is not — and the report is the only thing that
// reaches whoever has to answer it.
func TestTheReportNamesTheWordingItActuallyFound(t *testing.T) {
	for s, want := range map[string]string{
		"The image leaves the mount empty on 18.":        "leaves the mount empty",
		"The image starts and leaves the directory be.":  "starts and leaves",
		"A bind works here, unlike at the old path.":     "works here",
		"Move it up: a host path works at the new place": "a host path works",
	} {
		found := claimcite.Check("frag.md", s, knownRuns(t))
		if len(found) != 1 {
			t.Errorf("%q should be flagged once, got %v", s, found)
			continue
		}
		if found[0].Verb != want {
			t.Errorf("%q is reported as %q; it should name the wording that matched, %q", s, found[0].Verb, want)
		}
	}
}

// A question ends a sentence. Reading it as ordinary text joins the question to
// the statement after it, and provenance asked about in the first covers a claim
// made in the second.
func TestAQuestionEndsASentence(t *testing.T) {
	s := "Was it measured on Postgres 18? A host path works there."
	if found := claimcite.Check("frag.md", s, knownRuns(t)); len(found) != 1 {
		t.Errorf("asking where it was measured is not saying; the claim should be flagged, got %v", found)
	}
}

// Entries are bullets, and bullets are written every way markdown allows: with a
// blank line between them and without, with stars, numbered, nested. One item
// saying where it was measured says nothing about the next in any of them —
// reading only "- " at the left margin makes the invariant a matter of which
// character the writer reached for.
func TestABulletsCitationDoesNotCoverTheNextBullet(t *testing.T) {
	for name, text := range map[string]string{
		"loose":    "- The mount moves up, measured on Postgres 18\n\n- A host path works there.\n",
		"tight":    "- The mount moves up, measured on Postgres 18\n- A host path works there.\n",
		"stars":    "* The mount moves up, measured on Postgres 18\n* A host path works there.\n",
		"numbered": "1. The mount moves up, measured on Postgres 18\n2. A host path works there.\n",
		"indented": "- The mount moves up:\n  - the 17 layout, measured on Postgres 18\n  - a host path works there.\n",
	} {
		if found := claimcite.Check("frag.md", text, knownRuns(t)); len(found) != 1 {
			t.Errorf("%s: the second bullet cites nothing of its own; it should be flagged, got %v", name, found)
		}
	}
}

// A phrase is the phrase when a word inside it is dressed up. This changelog is
// full of code ticks, and a claim wearing one says exactly what a claim without
// one says.
func TestMarkupInsideAPhraseDoesNotHideIt(t *testing.T) {
	for _, s := range []string{
		"So a host path `works` there once the mount moves up.",
		"So a host path *works* there once the mount moves up.",
		"So **a host path works** there once the mount moves up.",
	} {
		if found := claimcite.Check("frag.md", s, knownRuns(t)); len(found) != 1 {
			t.Errorf("%q makes the claim in plain words with marks around them; it should be flagged, got %v", s, found)
		}
	}
}

// A caller with no runs to check against takes names on trust — that is what the
// documented nil behaviour means, and reversing it changes who this package can
// serve without anything saying so.
func TestWithoutARunListAnyNameIsTakenOnTrust(t *testing.T) {
	s := "A host path works there — see made-up-run-that-does-not-exist.txt."
	if found := claimcite.Check("frag.md", s, nil); len(found) != 0 {
		t.Errorf("a nil run list takes names on trust; this should pass, got %v", found)
	}
	if found := claimcite.Check("frag.md", s, knownRuns(t)); len(found) != 1 {
		t.Errorf("with a run list the same sentence should be flagged, got %v", found)
	}
}

// Paragraphs are joined one at a time. Joining the whole file instead runs the
// last sentence of one paragraph into the first of the next, and a citation in
// one bullet then covers a claim in another.
func TestOneBulletsCitationDoesNotCoverAnother(t *testing.T) {
	two := "- The mount moves up one level, measured on Postgres 18\n\n- A host path works there, so the mount is left alone.\n"
	if found := claimcite.Check("frag.md", two, knownRuns(t)); len(found) != 1 {
		t.Errorf("the second bullet cites nothing of its own; it should be flagged, got %v", found)
	}
}

// A citation has to point at something. Without this, the check is answered by
// typing any file name at all — which is a worse outcome than the uncited
// sentence it replaced, because it reads like evidence.
func TestANameNobodyCanOpenIsNotACitation(t *testing.T) {
	for _, s := range []string{
		"A host path works there — see made-up-run-that-does-not-exist.txt.",
		"A host path works there, as anyone can see in notes.txt.",
		// compose.json and a version number are ordinary words in this project's
		// prose. Neither is a run, so neither answers the question.
		"A host path works there when your compose.json names the mount.",
		"A host path works there as of 1.2.2.",
		"A host path works there; see compose.yaml.",
	} {
		if found := claimcite.Check("frag.md", s, knownRuns(t)); len(found) != 1 {
			t.Errorf("%q names nothing anyone can open; it should still be flagged, got %v", s, found)
		}
	}
}

// Withdrawing the claim is the other way through, and it has to stay open: the
// alternative is a writer inventing a citation to get past the check.
func TestSayingItWasNotMeasuredIsEnough(t *testing.T) {
	if found := claimcite.Check("frag.md", "Whether a host path works there on 19 is not measured.", knownRuns(t)); len(found) != 0 {
		t.Errorf("a withdrawn claim is not the problem here, got %v", found)
	}
}

// The ways through are the two above. Confidence is not one of them: a phrase
// that merely sounds sure would let anyone widen the exemptions without a single
// test noticing.
func TestSoundingSureIsNotAWayThrough(t *testing.T) {
	for _, s := range []string{
		"In practice a host path works there.",
		"As expected, a host path works there.",
		"Obviously a host path works there.",
		"A host path works there, as everyone knows.",
	} {
		if found := claimcite.Check("frag.md", s, knownRuns(t)); len(found) != 1 {
			t.Errorf("%q asserts rather than cites; it should be flagged, got %v", s, found)
		}
	}
}

func TestSentencesWithoutTheWordingsAreLeftAlone(t *testing.T) {
	for _, s := range []string{
		"The overlay now asks the image where it keeps its data.",
		// A wording has to be the word, not its tail: these three contain the
		// letters of a phrase in the list and say nothing about a container.
		"The compose reworks there are needed for profiles.",
		"Frameworks there are left as they are.",
		"The workspace here is untouched.",
		// The wordings this project uses for opossum's own behaviour are outside
		// the vocabulary on purpose: firing on these would make the check
		// something writers work around. See the package comment.
		"opossum now refuses to start a project whose services publish the same host port.",
		"An unknown top-level key now fails with a message naming the key.",
	} {
		if found := claimcite.Check("frag.md", s, knownRuns(t)); len(found) != 0 {
			t.Errorf("%q describes opossum, not something someone watched, got %v", s, found)
		}
	}
}

// changelog.d/README.md shows a contributor what a report looks like, quoting the
// wording verbatim. Two copies of a format drift apart silently, and the one that
// goes stale is the one nobody runs — so the copy in the documentation is checked
// against the one in the code.
func TestTheDocumentedReportMatchesTheRealOne(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "changelog.d", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := strings.Join(strings.Fields(string(b)), " ")
	// The report with its three variable parts marked, so what is left between
	// the marks is the wording itself.
	const mark = "zzMARKzz" // survives the %q the report puts the sentence through
	marked := claimcite.Finding{File: mark, Sentence: mark, Verb: mark}.String()
	// The report opens with the file, so the first piece between the marks is
	// empty. Skipping it silently would leave the position of the name — the part
	// a reader looks for first — checked by nobody.
	parts := strings.Split(marked, mark)
	if len(parts) < 2 || parts[0] != "" {
		t.Errorf("the report no longer opens with the file it is about: %q", marked)
	}
	for _, fixed := range parts {
		fixed = strings.TrimSpace(strings.Join(strings.Fields(fixed), " "))
		if fixed == "" {
			continue
		}
		if !strings.Contains(readme, fixed) {
			t.Errorf("changelog.d/README.md shows contributors a report that no longer matches this package: it does not contain %q", fixed)
		}
	}
}

// A release folds the fragments into a version's section and deletes them, and
// until now nothing read a word of what it left behind. That was the whole
// lifetime of the check: a sentence had one chance to be looked at, while its
// fragment existed, and none afterwards.
//
// So the published sections are read too. Everything released since this check
// existed went through it as a fragment, so this is mostly a second pair of eyes
// on text that arrives some other way — a hand edit, a release tool that folds
// something it should not. What it also does is put a number on the sentence that
// was published before the check existed, rather than leaving it out of sight.
func TestWhatWasPublishedNamesItsRunsToo(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range unexemptedClaims(t, string(b)) {
		t.Errorf("%s — published text cannot be edited, so this has to be right before it goes out", f)
	}
}

// unexemptedClaims is the check itself: what the published text claims without
// naming a run, minus what was already out when this check was written.
func unexemptedClaims(t *testing.T, doc string) []claimcite.Finding {
	t.Helper()
	known := knownRuns(t)
	var out []claimcite.Finding
	for _, f := range claimcite.Check("CHANGELOG.md", doc, known) {
		if publishedBeforeThisCheck[strings.TrimSpace(f.Sentence)] {
			continue
		}
		out = append(out, f)
	}
	return out
}

// publishedBeforeThisCheck is the one sentence that was already out when this
// check was written. A published section is the record of what shipped and is not
// edited afterwards, so it stays — named here rather than skipped in silence.
//
// The claim itself was true; what it lacked was anywhere to look. The run behind
// it is in this repository (`pg17-external-volume-lostfound.txt`), and the entry
// simply predates the habit of saying so.
var publishedBeforeThisCheck = map[string]bool{
	"opossum now clears `lost+found` out of the volumes it creates, so a compose file that writes `pgdata:/var/lib/postgresql/data` works here as it does on Docker, with no need to move `PGDATA` into a subdirectory of the mount": true,
}

// The repository is expected to be clean, so the check above says nothing about
// whether it can see what it is looking for. This does: a released section with a
// claim in it that nobody exempted has to be reported. Without this, widening the
// exemption to everything would pass — there is only one finding today, and it is
// the exempt one.
func TestAClaimInAPublishedSectionIsReported(t *testing.T) {
	const doc = `# Changelog

## [Unreleased]

## [1.0.0] - 2020-01-01

### Fixed

- A host path works there now, so the mount is left alone.
`
	found := unexemptedClaims(t, doc)
	if len(found) != 1 {
		t.Fatalf("a claim published with nothing behind it should be reported once, got %d: %v", len(found), found)
	}

	// …and the one sentence that predates the check is still let through, so this
	// is measuring the exemption too, not just the reporting.
	if got := unexemptedClaims(t, exemptedSentence(t)); len(got) != 0 {
		t.Errorf("the sentence that was published before this check should still pass, got %v", got)
	}
}

func exemptedSentence(t *testing.T) string {
	t.Helper()
	for s := range publishedBeforeThisCheck {
		return s
	}
	t.Fatal("nothing is exempt, so the exemption cannot be measured")
	return ""
}

// An exemption nobody needs is an exemption that hides the next one. Each entry
// has to be a sentence the check actually reports.
func TestEveryExemptionIsOneTheCheckWouldOtherwiseReport(t *testing.T) {
	known := knownRuns(t)
	for sentence := range publishedBeforeThisCheck {
		if len(claimcite.Check("x.md", sentence, known)) == 0 {
			t.Errorf("this is exempt but would pass anyway; it is not carrying its weight: %q", sentence)
		}
	}
}

// The fragments in this repository have to pass, or the check is one nobody can
// turn on. This is the ratchet: a new fragment that reports a measurement has to
// name its run. Fragments are enumerated by the loader the release uses, so
// "what counts as a fragment" stays defined in one place.
func TestEveryChangelogFragmentNamesItsRuns(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "changelog.d")
	// Load answers "no fragments" for a directory that isn't there, which is the
	// same answer it gives the day after a release. Asking first means a wrong
	// path fails instead of skipping in the voice of a healthy repository.
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("looking for the fragments: %v", err)
	}
	frags, err := changelog.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	// A release folds the fragments into CHANGELOG.md and deletes them, so between
	// a release and the next fragment there is nothing here to read. Saying so out
	// loud beats passing: a check with nothing in front of it is not a check that
	// found nothing.
	if len(frags) == 0 {
		t.Skip("no fragments waiting for a release; the sentences that were here are now in CHANGELOG.md, which this check does not read")
	}
	known := knownRuns(t)
	for _, f := range frags {
		for _, found := range claimcite.Check(filepath.Base(f.Path), f.Body, known) {
			t.Errorf("%s", found)
		}
	}
}
