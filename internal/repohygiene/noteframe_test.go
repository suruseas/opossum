package repohygiene_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/orchestrator"
)

// The words a note is introduced by, written out here and in one constant, and
// compared against nothing else.
//
// Every frame in the product reads that constant, and so did the tests, which is
// how a sentence that was wrong went on being printed while everything stayed
// green: a test that reads the same constant agrees with whatever it says. This
// one spells the words instead, so changing them is a change someone has to make
// twice — once where it is printed, once here, where the sentence is read as a
// sentence.
const noteFrame = "opossum writes no YAML for"

// Where each document introduces the note class. Reading the file as a whole
// would pass on a copy of the words parked anywhere in it, which is not what is
// being asked: the words have to be where a reader learns what a note is.
//
// The passage is found the same way as everywhere else in this file — collapsed,
// so re-wrapping a paragraph is not a change. It used to be a line to match and
// a count of lines to look inside, and re-wrapping the paragraph without
// touching a word turned it red, saying the product and the document disagreed
// about what a note is. They did not; the lines had moved.
var noteFrameDocs = []struct {
	path string
	lead string
}{
	{"AGENTS.md", "`# [opossum note]`"},
	{"docs/compatibility.md", "- **note**"},
	{"internal/orchestrator/adapt.go", "note        — something"},
}

func TestTheNoteFrameSaysTheSameThingEverywhere(t *testing.T) {
	if orchestrator.NoteFrame != noteFrame {
		t.Errorf("the words a note is introduced by changed:\n got %q\nwant %q\n"+
			"If the new wording is right, check it is true of every note — a Docker socket "+
			"mount, a host device, `restart: on-failure`, a Postgres data dir left alone — "+
			"and then change it here and in the documents too.", orchestrator.NoteFrame, noteFrame)
	}
	root := repoRoot(t)
	for _, d := range noteFrameDocs {
		lines := readLines(t, filepath.Join(root, filepath.FromSlash(d.path)))
		if filepath.Ext(d.path) == ".go" {
			lines = proseOf(lines)
		}
		got, err := blockContaining(lines, d.lead)
		if err != nil {
			t.Errorf("%s: %v, looking for the passage that opens the note class with %q",
				d.path, err, d.lead)
			continue
		}
		// From the lead onwards, not the whole passage. A block holds more than
		// the old line count did — in both documents it holds all three classes —
		// and a frame sitting in the paragraph about applied changes would have
		// satisfied a check that read the block whole. It is the note the reader
		// is learning about here.
		at := strings.Index(got, collapse(d.lead))
		if !strings.Contains(got[at:], noteFrame) {
			t.Errorf("%s introduces the note class and does not say %q:\n  %s\n"+
				"The product and the document disagree about what a note is.", d.path, noteFrame, got[at:])
		}
	}
}

// proseOf keeps the lines of a Go file that begin with a comment marker and
// blanks the rest, so that the same splitter reads it. A comment with nothing
// after the marker is a blank line, because that is what it is: the paragraph
// break of a comment.
//
// Lines that begin with a comment marker, exactly — not every comment. A comment
// after code on the same line is dropped, `/* */` is dropped, and a line inside a
// raw string literal that happens to begin with the marker is kept. What this
// reads are the paragraphs written above declarations, which is where this file's
// business lies; the fixture below says so in each case rather than leaving it to
// be discovered.
//
// This is here so there is one way of finding a passage rather than two. The
// second way was a line to match and a count of lines to look inside, and it
// went wrong the way counting lines does.
func proseOf(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "//") {
			continue
		}
		out[i] = strings.TrimSpace(strings.TrimPrefix(t, "//"))
	}
	return out
}

// Wordings the note class has carried and shed. Each of these was true of some
// notes and false of others, and each survived in a copy nobody was looking at
// after the note itself had stopped saying it.
//
// This knows these wordings and no others: a fresh way of saying the same wrong
// thing passes, and always will. It is worth having anyway, because the way this
// went wrong three times was a copy left behind, not a new claim.
var retiredNoteFrames = []string{
	"no compose change can fix",
	"will not write into the overlay",
	"nothing can be done about",
	"the overlay will not carry",
}

// Files that quote the old wording on purpose. Released notes say what was said
// at the time; `[Unreleased]` is not exempt by this, because it is generated
// from `changelog.d/`, and the fragments are read like anything else.
var quotesRetiredFrames = map[string]bool{
	"CHANGELOG.md": true,
	// This file spells the retired wordings to look for them.
	"internal/repohygiene/noteframe_test.go": true,
}

// TestTheRetiredNoteFramesDoNotComeBack reads prose and source alike. The copy
// that outlived the last two corrections was a Go comment four lines above the
// constant, and a walk that only read `.md` stepped over it.
func TestTheRetiredNoteFramesDoNotComeBack(t *testing.T) {
	root := repoRoot(t)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".md", ".go":
		default:
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if quotesRetiredFrames[rel] {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, old := range retiredNoteFrames {
			if strings.Contains(string(b), old) {
				t.Errorf("%s still says %q. A note is %q — the two disagree, and "+
					"whichever a reader meets first is the one they believe.", rel, old, noteFrame)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A claim is bounded by what was actually measured, and the boundary has to
// travel with it. `OPSM-106` is the case that taught this: the note said a
// session socket could not be reached, two documents repeated it, and two more
// passages cited it as the reason a neighbouring claim was safe — so correcting
// the note left four sentences speaking for it and saying the opposite.
//
// What is checked is the whole passage, against a copy of it kept beside this
// file. Looking for a phrase instead — "does it still say `has not been
// measured`" — was the first attempt, and it passes on a passage that keeps the
// hedge and adds a sentence flatly contradicting it, which is the shape the
// mistake actually takes. A passage is what a reader reads, so a passage is what
// gets compared.
//
// Whitespace is collapsed on both sides first: how the lines are wrapped is not
// a claim about anything, and a check that objected to re-wrapping would be a
// check that decided where the paragraphs break.
//
// To change one of these: change the document, run this, and copy what it prints
// into the file it names. The copying is the point — it is where the new sentence
// gets read as a sentence, away from the diff it arrived in.
var boundedClaims = []struct {
	// document the passage lives in
	path string
	// anything unique to the bullet, used to find it
	lead string
	// the copy, under testdata/claims
	golden string
	// the words the boundary is made of, which the copy has to keep saying.
	// Empty when the passage bounds nothing and is here only so that it is read.
	//
	// Comparing whole passages says the document did not change behind anyone's
	// back; it does not say what the passage is for. Someone who believes the
	// question has been settled edits the document, runs this, pastes what it
	// prints, and every check is green — the honest mistake, and the one this
	// exists to make expensive. So the copy is asked, separately, whether it
	// still draws a boundary at all.
	boundary string
}{
	{"AGENTS.md", "**`[OPSM-106]`", "agents-opsm-106.txt", "has not been measured"},
	{"AGENTS.md", "That is the one that has been measured", "agents-opsm-204.txt", "has not been"},
	{"docs/compatibility.md", "**Won't manage *these* containers**", "compatibility-docker-socket.txt", "has not been measured"},

	// The rest speak about the same things without bounding a measurement — an
	// index line, a class list, a record of what was said before. They were
	// carried for a while as a list of leads with a sentence each saying why
	// they claimed nothing. Four of those sentences were wrong: the passages did
	// say how far something reaches, and a note written once about a passage is
	// a note about the passage as it was that day. So they are read the same way
	// as the three above, and nothing is written down about them except what
	// they say.
	{"AGENTS.md", "`up --from-docker-compose` **generates** that overlay", "agents-overlay-intro.txt", ""},
	{"AGENTS.md", "- Entries come in three classes", "agents-entry-classes.txt", ""},
	{"AGENTS.md", "**`[OPSM-109]`", "agents-opsm-109.txt", ""},
	{"AGENTS.md", "It used to say something else for a `docker.sock`", "agents-opsm-204-record.txt", ""},
	{"AGENTS.md", "- `OPSM-106` — a host device or session socket is mounted", "agents-index-opsm-106.txt", ""},
	{"AGENTS.md", "- `OPSM-204` — a service mounts `docker.sock`", "agents-index-opsm-204.txt", ""},
	{"docs/compatibility.md", "| Outcome | Projects | What it is |", "compatibility-survey-table.txt", ""},
	{"docs/compatibility.md", "- **note** — something opossum writes no YAML for", "compatibility-note-class.txt", ""},
}

func TestAClaimCarriesWhatWasMeasured(t *testing.T) {
	root := repoRoot(t)
	for _, c := range boundedClaims {
		got, err := blockContaining(readLines(t, filepath.Join(root, filepath.FromSlash(c.path))), c.lead)
		if err != nil {
			t.Errorf("%s: %v (looking for the passage containing %q). If it moved, point "+
				"this at where it went; if it went away, say here why the boundary it "+
				"carried is no longer needed.", c.path, err, c.lead)
			continue
		}
		want := collapse(string(readFile(t, filepath.Join(root, "internal", "repohygiene", "testdata", "claims", c.golden))))
		if got != want {
			t.Errorf("%s no longer says what testdata/claims/%s says it says.\n"+
				" got: %s\nwant: %s\n"+
				"These passages say how far a measurement reaches. One socket was measured, "+
				"and it was mounted as the socket file itself; a mount of the directory a "+
				"session socket sits in is a different thing and has not been tried here. "+
				"If the new wording is right, put the passage in the file above — as it "+
				"reads in the document, wrapped the same way. Only the words are compared.",
				c.path, c.golden, got, want)
		}
		if c.boundary != "" && !strings.Contains(want, c.boundary) {
			t.Errorf("testdata/claims/%s no longer says %q. That is the boundary — how far "+
				"the measurement behind this passage reaches. If it really has been "+
				"measured since, the measurement goes in the tree first, and this line "+
				"comes out afterwards.", c.golden, c.boundary)
		}
	}
}

// TestEveryClaimCopyIsRead catches the copy nobody compares against any more.
// A file under testdata/claims that no entry names is not a record of anything:
// it is a passage that used to be checked, sitting where a reader would take it
// for one that still is.
func TestEveryClaimCopyIsRead(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "internal", "repohygiene", "testdata", "claims")
	read := map[string]bool{}
	for _, c := range boundedClaims {
		read[c.golden] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !read[e.Name()] {
			t.Errorf("testdata/claims/%s is not named by anything. Either point an entry "+
				"at it, or delete it — a passage nobody compares is not kept, it is left.",
				e.Name())
		}
	}
	if len(entries) != len(read) {
		t.Errorf("%d copies on disk, %d named", len(entries), len(read))
	}
}

// A block is one thing a reader reads at a time: a list item with whatever runs
// under it, or a paragraph standing on its own. Blocks are separated by a blank
// line, by the next item, or by a heading — which is what these documents do and
// no more. This is not a Markdown parser and should not become one: what it is
// for is finding passages in two files, and a parser would be a second thing to
// be wrong.
//
// Everything is collapsed, so where the lines break is not a claim about
// anything: the first version of this looked line by line, and re-wrapping a
// paragraph without changing a word was enough to lose the passage.
type docBlock struct {
	line int    // 1-based, for saying where
	text string // collapsed
}

func docBlocks(lines []string) []docBlock {
	var out []docBlock
	for i := 0; i < len(lines); i++ {
		// Fenced blocks are skipped whole. What is inside one is an example of
		// something to type, not a sentence about how far anything reaches, and
		// a compose example naming a socket is not a claim about that socket.
		// It also keeps a `#` comment in a shell example from reading as a
		// heading.
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
			}
			continue
		}
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		end := i + 1
		for end < len(lines) && strings.TrimSpace(lines[end]) != "" &&
			!strings.HasPrefix(lines[end], "- ") && !strings.HasPrefix(lines[end], "#") {
			end++
		}
		out = append(out, docBlock{i + 1, collapse(strings.Join(lines[i:end], " "))})
		i = end - 1
	}
	return out
}

// blockContaining returns the one block that holds lead.
func blockContaining(lines []string, lead string) (string, error) {
	want := collapse(lead)
	var found []string
	for _, b := range docBlocks(lines) {
		if strings.Contains(b.text, want) {
			found = append(found, b.text)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", errors.New("no passage in this file holds it")
	default:
		return "", fmt.Errorf("%d passages hold it, so which one is meant is not decided here", len(found))
	}
}

// collapse makes one line of a passage, so that re-wrapping it is not a change.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// Subjects a measurement bounds. Anywhere one of these is spoken about, someone
// is saying how far something reaches — and #614 was four sentences doing that
// on behalf of a note that had stopped saying it.
//
// Comparing the passages that carry the claim says those passages did not
// change. It says nothing about another arriving: a sibling item, or a paragraph
// tucked under one, both of which these documents already use. So every passage
// that mentions a subject is found, and each has to be one of the copies.
//
// What this rests on, said plainly: these three phrases and these two documents.
// A passage that speaks about the same thing in other words, or in another file,
// is not found — which is the same shape as the gap it closes, one step further
// out.
var claimSubjects = []string{"session socket", "docker.sock", "Docker socket"}

func TestNothingElseSpeaksForTheseMeasurements(t *testing.T) {
	root := repoRoot(t)
	copies := map[string]string{} // collapsed passage -> which file it came from
	docs := map[string]bool{}
	for _, c := range boundedClaims {
		copies[collapse(string(readFile(t, filepath.Join(root, "internal", "repohygiene",
			"testdata", "claims", c.golden))))] = c.golden
		docs[c.path] = true
	}
	for doc := range docs {
		for _, b := range docBlocks(readLines(t, filepath.Join(root, filepath.FromSlash(doc)))) {
			var subject string
			for _, s := range claimSubjects {
				if strings.Contains(strings.ToLower(b.text), strings.ToLower(s)) {
					subject = s
					break
				}
			}
			if subject == "" || copies[b.text] != "" {
				continue
			}
			t.Errorf("%s:%d speaks about %q and nothing here has read it:\n  %s\n"+
				"Read it. If it says how far a measurement reaches, it needs the words "+
				"that say how far. Either way it needs a copy under testdata/claims and "+
				"an entry in boundedClaims — a passage on one of these subjects that "+
				"nobody compares is one that can be edited into saying anything.",
				doc, b.line, subject, b.text)
		}
	}
}

// The kinds of note there are. Both documents list them by code, and a list
// nobody checks goes quietly out of date — which is the same way the wording
// went wrong, wearing different clothes.
var noteKinds = []string{"OPSM-106", "OPSM-111", "OPSM-204", "OPSM-409"}

// noteSite finds a diagnostic handed to the note constructor; noteEntry finds one
// built as a note entry directly. Two shapes because there are two: everything
// service-shaped goes through the first, and the Postgres data-directory note is
// assembled on its own.
var (
	noteSite  = regexp.MustCompile(`note\((code[A-Za-z]+),`)
	noteEntry = regexp.MustCompile(`Code:\s+string\((code[A-Za-z]+)\)[^\n]*\n\s+class:\s+classNote`)
	codeDecl  = regexp.MustCompile(`\n\t(code[A-Za-z]+)\s+diagCode = "(OPSM-\d+)"`)
	// Where a note entry is built at all. The two patterns above read the two
	// shapes there are; a third would be a note neither of them sees, and the
	// list below would go on looking complete.
	noteClass = regexp.MustCompile(`class:\s+classNote`)
)

func TestEveryKindOfNoteIsInTheDocuments(t *testing.T) {
	root := repoRoot(t)
	src := string(readFile(t, filepath.Join(root, "internal", "orchestrator", "adapt.go")))
	byName := map[string]string{}
	for _, m := range codeDecl.FindAllStringSubmatch(
		string(readFile(t, filepath.Join(root, "internal", "orchestrator", "diagnostics.go"))), -1) {
		byName[m[1]] = m[2]
	}
	if n := len(noteClass.FindAllString(src, -1)); n != 2 {
		t.Fatalf("notes are built in %d places in adapt.go, and the two patterns here read "+
			"two of them. Teach this test the new shape, or the kinds below will look "+
			"complete while one goes unlisted.", n)
	}
	seen := map[string]bool{}
	for _, re := range []*regexp.Regexp{noteSite, noteEntry} {
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			code, ok := byName[m[1]]
			if !ok {
				t.Errorf("adapt.go writes a note for %s, which diagnostics.go does not declare", m[1])
				continue
			}
			seen[code] = true
		}
	}
	got := make([]string, 0, len(seen))
	for c := range seen {
		got = append(got, c)
	}
	sort.Strings(got)
	if strings.Join(got, " ") != strings.Join(noteKinds, " ") {
		t.Fatalf("the kinds of note opossum writes changed:\n got %v\nwant %v\n"+
			"A new kind has to be added to this list and to the note class in "+
			"AGENTS.md and docs/compatibility.md, which name them by code.", got, noteKinds)
	}
	// The documents name the kinds by code; the class list in the source does not,
	// and is only here for the words a note is introduced by.
	for _, rel := range []string{"AGENTS.md", "docs/compatibility.md"} {
		body := string(readFile(t, filepath.Join(root, filepath.FromSlash(rel))))
		for _, code := range noteKinds {
			if !strings.Contains(body, code) {
				t.Errorf("%s describes the note class but never mentions %s", rel, code)
			}
		}
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return b
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	return strings.Split(string(readFile(t, path)), "\n")
}

// What the splitter does, said on inputs small enough to read.
//
// Everything above runs over the repository's own files, which is where it has
// to work — and which is why nothing here was pinned until a change to this file
// went out with no eval of its own. These say the two things the rest leans on:
// that re-wrapping a passage does not change it, and which lines of a Go file
// count as prose.
func TestTheSplitterOnInputsSmallEnoughToRead(t *testing.T) {
	same := func(t *testing.T, a, b []string) {
		t.Helper()
		ga, gb := docBlocks(a), docBlocks(b)
		if len(ga) != len(gb) {
			t.Fatalf("%d blocks and %d blocks:\n%v\n%v", len(ga), len(gb), ga, gb)
		}
		for i := range ga {
			if ga[i].text != gb[i].text {
				t.Errorf("block %d differs:\n got: %s\nwant: %s", i, gb[i].text, ga[i].text)
			}
		}
	}

	t.Run("wrapping a passage differently is not a change", func(t *testing.T) {
		same(t,
			[]string{"- **note** — something opossum writes no YAML for, which is", "  why there is nothing to uncomment."},
			[]string{"- **note** — something opossum", "  writes no YAML for, which is why", "  there is nothing", "  to uncomment."})
	})

	t.Run("an item runs to the next one", func(t *testing.T) {
		got := docBlocks([]string{"- first", "  still first", "- second"})
		if len(got) != 2 || got[0].text != "- first still first" || got[1].text != "- second" {
			t.Errorf("got %v", got)
		}
	})

	t.Run("a paragraph under an item stands on its own", func(t *testing.T) {
		got := docBlocks([]string{"- first", "", "  a paragraph beneath it"})
		if len(got) != 2 || got[1].text != "a paragraph beneath it" {
			t.Errorf("got %v", got)
		}
	})

	t.Run("a fenced example is not prose", func(t *testing.T) {
		got := docBlocks([]string{"- first", "", "```yaml", "- /var/run/docker.sock:/x", "```", "", "- second"})
		if len(got) != 2 || got[1].text != "- second" {
			t.Errorf("a fence holds what to type, not what is claimed; got %v", got)
		}
	})

	t.Run("prose is the lines that begin with the marker", func(t *testing.T) {
		got := docBlocks(proseOf([]string{
			"// a paragraph",
			"//",
			"// another paragraph",
			"func f() {}",
			"var x = 1 // after code, dropped",
			"/* a block comment, dropped */",
		}))
		want := []string{"a paragraph", "another paragraph"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i].text != want[i] {
				t.Errorf("block %d: got %q, want %q", i, got[i].text, want[i])
			}
		}
	})

	t.Run("a marker inside a raw string is kept, which is the known cost", func(t *testing.T) {
		got := docBlocks(proseOf([]string{"const s = `", "// this is data, and it is read as prose", "`"}))
		if len(got) != 1 || got[0].text != "this is data, and it is read as prose" {
			t.Errorf("the cost of reading lines rather than parsing them changed; got %v", got)
		}
	})
}
