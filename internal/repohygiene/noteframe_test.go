package repohygiene_test

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
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

	// The record of what has been run on real hardware. It is not published, and
	// it is the first place someone opens to find out what has been measured — so
	// when it goes stale, the answer is either a measurement taken twice or one
	// that was never taken at all. It had gone stale in several places before this
	// was added, and nothing said so — the count is left out on purpose, because
	// counting them is how the issue that started this got its own number wrong.
	{"docs/real-runtime-review.md", "現在の内容: socket 直 bind が通信まで通ること",
		"review-conformance-scope.txt", "socket ファイルそのものの bind"},
	{"docs/real-runtime-review.md", "`OPSM-109` は3つのことを言う",
		"review-opsm-109-says-three.txt", "この文書に記録が無い"},
	{"docs/real-runtime-review.md", hostDevicePassage,
		"review-opsm-106-unmeasured.txt", "測っていない"},
	{"docs/real-runtime-review.md", "2026-08-27 — socket bind の「通る」と「使える」を分けて測った",
		"review-2026-08-27.txt", ""},
	// What the daemon measurement binds is not the name the documents talk
	// about: it is what that name resolves to on this machine. The boundary
	// word is the one that says "on this machine" — a rewrite that drops it
	// turns a path this run happened to see into a path everyone has.
	{"docs/real-runtime-review.md", "`filepath.EvalSymlinks` で解決し",
		"review-docker-daemon-conformance.txt", "この機械では"},
	{"docs/real-runtime-review.md", "**3. `[OPSM-109]` の境界**",
		"review-opsm-109-boundary.txt", "3通りを実機で"},
	{"docs/real-runtime-review.md", "**portainer（inspection）**", "review-portainer.txt", "当時の結論"},
	{"docs/real-runtime-review.md", "2026-08-27 に分かったこと（上の記録）",
		"review-portainer-update.txt", "そもそもマウントで止まる"},
	// The line that was forgotten once already. It spells the subject with a
	// hyphen, so the sweep over subjects walked past it — the net was built to
	// stop this line being forgotten, and this line was outside it.
	{"docs/real-runtime-review.md", "**知見（第2弾）**", "review-second-findings.txt", "2026-08-27 に狭まった"},
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
// that mentions one of the subjects below is found, and each has to be one of the
// copies. Passages about the same thing in other words are not found — the name
// of this says "on these subjects" and means it.
//
// What this rests on, said plainly: the phrases listed below, and the documents
// boundedClaims names. Not a count of either — the last two counts written in
// this file went false in the change that wrote them.
// A passage that speaks about the same thing in other words, or in another file,
// is not found — which is the same shape as the gap it closes, one step further
// out.
var claimSubjects = []string{"session socket", "docker.sock", "docker-socket", "Docker socket"}

func TestEveryPassageOnTheseSubjectsIsRead(t *testing.T) {
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

// How many places assemble one. Four kinds come out of two places, because the
// service notes share a constructor; the number is here because a place that
// builds a note without naming a diagnostic — a code written as a literal
// string, say — adds nothing to the list of kinds, and a list that cannot grow
// is one that looks complete.
const noteSites = 2

// noteKindsIn reads the package as Go rather than as text, and answers which
// diagnostics it builds notes for.
//
// It used to be two regular expressions over one file. They read the two shapes
// a note was written in, and a note written in a third — or in another file —
// was one they did not see, while the list above went on looking complete. The
// count of construction sites that was added to catch that missed a note whose
// call had been wrapped across two lines: the shapes were the same, the source
// text was not. Source read as text is a different thing from source.
//
// A note is a composite literal that sets class to classNote. Which diagnostic it
// carries is not always written there — the service notes take it as an argument
// — so what comes back is every diagnostic named anywhere in the function that
// builds one. That is wider than the truth where a function names a code it does
// not use for a note, and wider fails loudly: an extra kind is a mismatch someone
// has to look at, where a missing one is silence.
func noteKindsIn(t *testing.T, dir string) []string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	byName, sites := map[string]string{}, 0
	seen := map[string]bool{}
	// The diagnostics are declared in one file and used in another, so they are
	// all read before anything is looked up.
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				gen, ok := d.(*ast.GenDecl)
				if !ok || gen.Tok != token.CONST {
					continue
				}
				for _, spec := range gen.Specs {
					v, ok := spec.(*ast.ValueSpec)
					if !ok || len(v.Names) != 1 || len(v.Values) != 1 {
						continue
					}
					lit, ok := v.Values[0].(*ast.BasicLit)
					if ok && lit.Kind == token.STRING && strings.HasPrefix(lit.Value, `"OPSM-`) {
						byName[v.Names[0].Name] = strings.Trim(lit.Value, `"`)
					}
				}
			}
		}
	}
	// Every declaration, not every function: a note assembled in a package
	// variable is still a note, and the version of this that walked function
	// bodies could not see one. The count is of the literals themselves, so
	// folding a call across two lines does not change it — which is what the
	// count this replaced could not say.
	isNote := func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return false
		}
		for _, e := range lit.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			k, kok := kv.Key.(*ast.Ident)
			v, vok := kv.Value.(*ast.Ident)
			if kok && vok && k.Name == "class" && v.Name == "classNote" {
				return true
			}
		}
		return false
	}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				builds := false
				ast.Inspect(d, func(n ast.Node) bool {
					if isNote(n) {
						sites++
						builds = true
					}
					return true
				})
				if !builds {
					continue
				}
				ast.Inspect(d, func(n ast.Node) bool {
					if id, ok := n.(*ast.Ident); ok {
						if code, known := byName[id.Name]; known {
							seen[code] = true
						}
					}
					return true
				})
			}
		}
	}
	if len(byName) == 0 {
		t.Fatalf("no diagnostic is declared in %s the way this reads them (a constant whose "+
			"value is an OPSM string). Nothing below would find anything, and an empty "+
			"answer would look like an answer.", dir)
	}
	if sites != noteSites {
		t.Fatalf("notes are built in %d places in %s, and there were %d. A note is a note "+
			"wherever it is assembled, so a new one belongs in the list of kinds and in "+
			"the note class in AGENTS.md and docs/compatibility.md — and if it went away, "+
			"it belongs out of them.", sites, dir, noteSites)
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func TestEveryKindOfNoteIsInTheDocuments(t *testing.T) {
	root := repoRoot(t)
	got := noteKindsIn(t, filepath.Join(root, "internal", "orchestrator"))
	if strings.Join(got, " ") != strings.Join(noteKinds, " ") {
		t.Fatalf("the kinds of note opossum writes changed:\n got %v\nwant %v\n"+
			"Whichever way it moved, the list here and the note class in AGENTS.md and "+
			"docs/compatibility.md follow it. Read the difference before writing it down: "+
			"what comes back is every diagnostic named in a declaration that builds a "+
			"note, which is wider than the notes themselves — a code named there for "+
			"something else arrives looking like a kind.", got, noteKinds)
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

// conformanceCallable is a place a call written as a plain name can land: a
// function declaration, or a package-level variable holding a function
// literal. Names collide only where Go allows a duplicate — `init` and `_`,
// neither of which anything calls by name — so what a caller could reach is
// what these hold.
type conformanceCallable struct {
	node ast.Node
	from token.Pos
	to   token.Pos
}

// conformanceSuite is what the conformance tests can reach, read once and used
// by the rules below: one asks where a skip may sit, the other asks how many
// tests a run of the suite has to account for. A set worked out twice is a set
// that can differ twice.
type conformanceSuite struct {
	fset    *token.FileSet
	dir     string
	files   map[string]*ast.File
	bodies  map[string]conformanceCallable
	reached map[string]bool
	order   []string // reached, sorted, so a report reads the same twice
	roots   []string // the conformance tests themselves, sorted
}

// conformanceReach parses the conformance test package and follows calls from
// the tests named by conformancePrefix: every call written as a plain name
// that lands in something this package declares, and on from there. It fails
// the caller rather than returning empty — a rule that reads nothing passes,
// and passing for that reason is the failure this file is about.
func conformanceReach(t *testing.T) conformanceSuite {
	t.Helper()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "orchestrator")
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, nil, 0)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	p := pkgs[conformancePackage]
	if p == nil {
		t.Fatalf("no package %s under %s: the conformance suite is written about that "+
			"package, and the rules here read nothing without it", conformancePackage, dir)
	}

	bodies := map[string]conformanceCallable{}
	for _, f := range p.Files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv != nil || d.Body == nil {
					continue
				}
				bodies[d.Name.Name] = conformanceCallable{d, d.Pos(), d.End()}
			case *ast.GenDecl:
				if d.Tok != token.VAR {
					continue
				}
				for _, sp := range d.Specs {
					vs, ok := sp.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, n := range vs.Names {
						if i >= len(vs.Values) {
							continue
						}
						if lit, ok := vs.Values[i].(*ast.FuncLit); ok {
							bodies[n.Name] = conformanceCallable{lit, lit.Pos(), lit.End()}
						}
					}
				}
			}
		}
	}

	var roots []string
	for name := range bodies {
		if strings.HasPrefix(name, conformancePrefix) {
			roots = append(roots, name)
		}
	}
	sort.Strings(roots)
	if len(roots) == 0 {
		t.Fatalf("nothing under %s is named %s…, so there is no conformance suite to read "+
			"and the rules here would pass on an empty set", dir, conformancePrefix)
	}

	reached := map[string]bool{}
	queue := append([]string(nil), roots...)
	for _, name := range roots {
		reached[name] = true
	}
	for len(queue) > 0 {
		from := bodies[queue[0]]
		queue = queue[1:]
		ast.Inspect(from.node, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			if _, declared := bodies[id.Name]; !declared || reached[id.Name] {
				return true
			}
			reached[id.Name] = true
			queue = append(queue, id.Name)
			return true
		})
	}
	order := make([]string, 0, len(reached))
	for name := range reached {
		order = append(order, name)
	}
	sort.Strings(order)
	return conformanceSuite{fset, dir, p.Files, bodies, reached, order, roots}
}

// isSkipCall says whether a method name reached through a selector is one that
// skips. Everything testing.T spells `Skip…` does — SkipNow, Skipf, Skip — with
// one exception: `Skipped` reports whether one happened, and reading a fact is
// not producing it. Nothing else is looked at, so a helper of one's own named
// `Skipper` is still counted (#657).
func isSkipCall(name string) bool {
	return strings.HasPrefix(name, "Skip") && name != "Skipped"
}

// The rule that uses isSkipCall reads a suite that has no `Skipped` in it, so
// dropping the exception leaves that rule green: the guard would be one for a
// day that has not come. This asks isSkipCall directly, so it is measurable
// today — and it is the only thing that fails if the exception is dropped.
func TestOnlySkipsCountAsSkips(t *testing.T) {
	for name, want := range map[string]bool{
		"Skip":    true,
		"Skipf":   true,
		"SkipNow": true,
		"Skipper": true, // not testing.T's, and nothing here can tell
		"Skipped": false,
		"Fatal":   false,
		"skip":    false,
	} {
		if got := isSkipCall(name); got != want {
			t.Errorf("isSkipCall(%q) = %v, want %v — %s", name, got, want,
				map[bool]string{
					true:  "everything testing.T spells Skip… skips, except the one that only reports",
					false: "reading whether a skip happened, or a name that is not one at all, is not skipping",
				}[want])
		}
	}
}

// With the flag set, a missing precondition fails; it does not skip. The record
// says so in as many words, and the reason is the one the whole file is built
// around: a run that comes back green having measured nothing is the outcome it
// must not produce.
//
// Nothing held that. Turning the two `t.Fatalf` calls in the precondition
// helpers into `t.Skipf` left the gate green, and the suite would then report
// success on a machine where the runtime was not running (#657).
//
// What this reads, in two passes over the same test package: the functions the
// conformance suite can reach, and — whole — the files the gate and the anchor
// are written in. The
// functions named by conformancePrefix are the roots; from each, every call
// written as a plain name that lands in something this package declares — a
// function declaration, or a package-level variable holding a function literal
// — is followed, and so on. Across both passes there must be exactly one call
// written as `<something>.Skip…`, and it must sit in realRuntimeGate.
//
// Two passes, because each alone leaks, and a review measured both directions.
// Reading only the declaring file was the hole: `func noRuntime(t *testing.T,
// m string) { t.Skip(m) }` in a sibling file of the same package, called where
// the missing `container` binary used to fail, left the gate green. Reading
// only what is reachable dropped two the file had been catching — a skip one
// hop off the call graph but still in that file, reached through a value
// (`var noRuntime = mkSkipper()`) or through a method (`bailer{t}.no(…)`).
// Both were measured green under reach alone and red under either the file
// rule that came before or the two passes here.
//
// Nothing narrows the file pass — it reads those files whole. What keeps it
// off the rest of the package is only that the rest is elsewhere: this test
// package skips in three other places — two where the test runs as root and a
// read-only directory is writable anyway, one where there is no $HOME to
// compare against — and none of them is in either file or reached from the
// suite. Two files rather than one because a review moved them apart: carrying
// daemonSocketConst into a sibling file, a natural way to separate the daemon
// measurement, left a gate whose file nothing read, and a skip put there was
// green. Only the gate's file has to exist: taking the daemon measurement out
// altogether removes the anchor and removes nothing this rule was reading for,
// so it is read when it is there and not asked for when it is not. That was a
// Fatal until it was measured — the rule refused a change that cost it no
// coverage at all.
//
// Today the reach pass adds nothing the files do not already cover: all eight
// bodies it reaches are in the gate's own file. What it buys is the next
// helper to move out of it — measured as a skip in a sibling, which reach
// catches and the files do not.
//
// Two shapes this was wrong about first, both caught by mutation:
//
//   - Exempting the gate function instead of counting. realRuntimeGate holds
//     the opt-out skip and two of the precondition failures, so exempting it
//     left the very case this exists for — a missing `container` binary
//     answered with a skip — passing.
//   - Walking function bodies instead of the file. A package-level
//     `var bail = func(t *testing.T, m string) { t.Skip(m) }` was invisible
//     while the rule said "in the file" and read something narrower. A
//     package-level variable holding a function literal is a callable here, so
//     a skip written into one is read when the suite can reach it.
//
// What it does not catch, measured rather than guessed:
//
//   - Either dodge above, moved one file over. `var noRuntime = mkSkipper()`
//     in a sibling file is neither something a plain-name call follows nor in
//     the file this reads: green.
//   - A helper in another package. `zzmut.Bail(t, m)` called from the gate:
//     green.
//   - A widened opt-out. Folding the precondition into the flag test —
//     `if os.Getenv(...) == "" || err != nil { t.Skip(...) }` — is still one
//     skip in the gate.
//   - A method value. `skipf := t.Skipf` then `skipf(...)` is read as neither:
//     the binding is not a call, and the call's function is a plain name.
//   - Ending green without skipping at all. Keeping the opt-out skip and
//     answering the two preconditions with `t.Log` and `return "", false`,
//     with `if !ok { return }` at each call site, is one skip in the gate and
//     passes here — and with the flag set on a machine the runtime is missing
//     from, the three conformance tests then report PASS in 0.00s rather than
//     SKIP, which is worse than the mutation this guards. Converting the
//     opt-out as well is red, on `0 skip(s)`: the shape that gets through is
//     the one that keeps it.
//   - `var f func(*testing.T, string)` in a sibling file, assigned its literal
//     in `init`: only a variable declared with its literal is a callable here,
//     so this is a value hop by another spelling. Green, measured.
//   - A generic helper called with its type written out. `bailOn[string](t, …)`
//     is green and `bailOn(t, …)` — the same helper, type inferred — is red:
//     one is a call whose function is a name, the other is a call whose
//     function is an index expression. There are no generics in this suite;
//     this is here because the difference is invisible from the source.
//
// What it reports that is not a skip: any other name starting with `Skip`. A
// helper of one's own called `Skipper` would be counted. testing.T's own
// `Skipped()` was, until a review wrote `if t.Skipped() { return }` into a
// cleanup — an ordinary thing to write — and this went red and called it a
// skip. That one name is excluded now; the rest of the prefix is not, because
// every other `Skip…` testing.T has does skip — `Skip`, `Skipf`, `SkipNow`
// and `Skipped` are all four of them, on T, B, TB and F alike.
//
// What that exception costs, measured: a helper of one's own named `Skipped`
// that really does skip is green, and so is a field holding `t.Skip` under
// that name. Both were red before the exception — but renaming either one made
// them green then too, so what was lost is the catching of a name that
// happened to collide, not a rule anything relied on.
//
// A closure in the gate is not one of these. `bail := func(m string) {
// t.Skip(m) }` called where the missing `container` binary used to fail is a
// second skip in the gate, and red on the count — the earlier note here said
// it passed, which was written rather than measured.
//
// The report names the body a skip was reached in, or the file it was found
// in. A skip nested in a closure is named by the outermost body reached, so
// the line number is where to look, not the name.
//
// conformanceJudgements still reads by file and still has the sibling-file
// hole this closes. That, and the shapes above, are #657.
func TestOnlyTheOptOutMaySkipTheConformanceSuite(t *testing.T) {
	suite := conformanceReach(t)
	fset, bodies := suite.fset, suite.bodies

	var declaring []*ast.File // the file(s) the daemon measurement is written in
	for _, f := range suite.files {
		for _, d := range f.Decls {
			gen, ok := d.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, sp := range gen.Specs {
				vs, ok := sp.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, n := range vs.Names {
					if n.Name == daemonSocketConst {
						declaring = append(declaring, f)
					}
				}
			}
		}
	}
	gate, hasGate := bodies[realRuntimeGate]
	var gateFile *ast.File
	for _, f := range suite.files {
		if hasGate && gate.from >= f.Pos() && gate.from < f.End() {
			gateFile = f
		}
	}
	if !hasGate || !suite.reached[realRuntimeGate] {
		t.Fatalf("no %s… test reaches %s, so the one skip this rule allows is not one the "+
			"suite runs — the rule is written about that function",
			conformancePrefix, realRuntimeGate)
	}

	type skip struct {
		name string
		via  string
		at   string // file:line, so a reader has somewhere to look
		pos  token.Pos
	}
	var skips []skip
	seen := map[token.Pos]bool{}
	collect := func(n ast.Node, via string) {
		ast.Inspect(n, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !isSkipCall(sel.Sel.Name) || seen[call.Pos()] {
				return true
			}
			seen[call.Pos()] = true
			pos := fset.Position(call.Pos())
			skips = append(skips, skip{sel.Sel.Name, via,
				filepath.Base(pos.Filename) + ":" + strconv.Itoa(pos.Line), call.Pos()})
			return true
		})
	}
	for _, name := range suite.order {
		collect(bodies[name].node, "reached from the conformance suite, in "+name)
	}
	// And whole files, reached or not: a skip one hop off the call graph —
	// through a value, a method, a package variable assigned from a call — is
	// still a skip sitting in one of them. Both files rather than one, because
	// the anchor and the gate can be parted: moving the daemon measurement and
	// daemonSocketConst with it into a sibling leaves the gate behind, and
	// reading only the anchor's file would stop reading the gate's. The gate's
	// file is the one that has to be there; the anchor's is read when there is
	// one.
	if gateFile != nil {
		collect(gateFile, "in the file declaring "+realRuntimeGate)
	}
	for _, f := range declaring {
		if f != gateFile {
			collect(f, "in the file declaring "+daemonSocketConst)
		}
	}

	inGate := func(s skip) bool { return s.pos >= gate.from && s.pos < gate.to }
	if len(skips) != 1 || !inGate(skips[0]) {
		var where []string
		for _, s := range skips {
			at := s.via
			if inGate(s) {
				at = "in " + realRuntimeGate
			}
			where = append(where, s.at+" "+s.name+" ("+at+")")
		}
		t.Errorf("the conformance suite carries %d skip(s): %v.\n"+
			"  There is one skip to have, and it is the opt-out in %s: without the flag "+
			"there is nothing to measure. Everywhere else, with the flag set, a missing "+
			"precondition has to fail — a skip there hands back a green run that measured "+
			"nothing, which is the one outcome that suite refuses to produce.",
			len(skips), where, realRuntimeGate)
	}
}

// The skip rule above counts skips, so the way past it is to end green without
// one. Measured, that shape is worse than the mutation the skip rule guards:
// keeping the opt-out skip and answering a precondition with `t.Log` and
// `return "", false`, with the call site reading the second value, leaves one
// skip in the gate — and with the flag set on a machine the runtime is missing
// from, the three conformance tests report PASS in 0.00s. Not SKIP. A green
// run that measured nothing, and says nothing about it (#657).
//
// Two rules were written against that and both were taken apart by review,
// each in a few lines, because both read where a statement sits:
//
//   - "A conformance test may not `return`" — `if !ok { return }` written
//     instead as `if ok { …body… }` reaches the same three PASSes with no
//     `return` anywhere, three lines shorter than the mutation it forbade.
//   - "In every body the suite reaches, a `return` is the last statement" —
//     folding the same gate into one trailing `return bin, measurable`, or
//     replacing its `t.Fatal` with `panic(sentinel)` and a `recover` in each
//     test, gets there too, in 37 and 17 lines.
//
// The property is about a run, not about where statements sit, and every rule
// about placement is one spelling away from the next. So this runs it. Three
// machines are made out of what `container` is on the PATH — absent, present
// and answering "not running", present and answering "running" while being
// nothing of the sort — and on each one every conformance test has to fail.
// Not pass and not skip: those two are what a machine with nothing to measure
// against looks like when the suite lies about it. The exit status has to
// agree with the report, which is a second fact and asked for separately.
//
// The third machine is not a general one, and a review measured how far it
// falls short. Counted rather than recalled, nine failures after the gate say
// "this machine cannot answer the question": the socket directory, four in
// listen and the symlink test's own setup, the two-minute run, and both of
// dockerDaemonSocket's. With the gate out of the way the suite reaches all
// nine and breaks none of them, so loosening any of the nine is green here.
// What each machine buys is the preconditions it actually violates.
//
// This replaced the placement rule rather than joining it, and that is a
// trade, not a free upgrade — measured both ways:
//
//   - The placement rule refused ordinary Go in the helpers it covered: a memo
//     (`if cached != "" { return cached }`) and a loop returning on success
//     were red, though both only measure more. Those are green here.
//   - The placement rule was machine-independent: it read a precondition that
//     answers with a value wherever one was written. This reads only the
//     preconditions the machine it runs on actually violates — the six above.
//     What that costs is smaller than it looks: a precondition the machine
//     meets is a branch the run never takes, so answering it with a value
//     changes nothing and the suite still measures. Made unmet — a socket name
//     that resolves to nothing — dockerDaemonSocket's branch is taken, and the
//     third machine turns it red while an unmutated suite on the same machine
//     stays green. Measured both ways. The shape that has to be there for
//     either is the whole of it: the helper answers with a value *and* the
//     caller stops on it. A helper changed alone leaves the test running, so
//     it fails downstream on its own — green there is the right answer, and a
//     review reading only that half found this claim did not reproduce.
//     What this does cost is a machine that never violates a precondition: it
//     will not notice that one rotting.
//
// What this does not reach:
//
//   - Anything outside internal/orchestrator. A second conformance suite in
//     another package would be neither built nor read here (#657).
//   - A suite that fails for its own reasons. This asks for the verdict, not
//     the reason, so a conformance test broken some other way looks the same —
//     and on the third machine one of the three already fails on an assertion
//     rather than a precondition: the stub is refused, but not with the errno
//     that test is written about. Loosen that assertion and this goes red for
//     a reason it does not name.
//   - What a real runtime answers. This measures the refusal, which is the
//     part that has to hold on every machine; the answer is what the suite
//     itself is for.
//   - A run that finds a runtime by a route other than the PATH. The gate
//     looks `container` up by name today; OPOSSUM_CONTAINER_BIN and
//     DOCKER_HOST are dropped from the child's environment so that stays true
//     if it ever reads them, but a third route would pass unnoticed.
//
// Cost, measured warm and alone on this package across two people's runs:
// about a second and a half, on a package that takes some thirty-three. The
// three children are around half a second each, plus a build the toolchain has
// cached. Each child carries a two-minute timeout, so a hung conformance test
// costs up to six minutes here — and the `go build` the suite's TestMain runs
// is outside that timeout, with nothing but the outer test binary's own to
// stop it.
func TestTheConformanceSuiteFailsWhenItCannotMeasure(t *testing.T) {
	suite := conformanceReach(t) // for how many tests a run has to account for
	root := repoRoot(t)
	goBin, err := exec.LookPath("go")
	if err != nil {
		goBin = filepath.Join(runtime.GOROOT(), "bin", "go")
	}

	bin := filepath.Join(t.TempDir(), "conformance.test")
	build := exec.Command(goBin, "test", "-c", "-o", bin, "./internal/orchestrator")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the conformance suite: %v\n%s", err, out)
	}

	// Each machine is made by what `container` is on the PATH, and each one
	// stops the suite at a different precondition. The last is the general
	// one: a `container` that answers "running" gets the gate out of the way,
	// so everything the suite asks after that is on this side of the line too.
	for _, machine := range []struct {
		name string
		stub string // the `container` to put on the PATH; empty means none
	}{
		{"no `container` binary", ""},
		{"a runtime that is installed and not running", `#!/bin/sh
echo "apiserver is not running"
exit 0
`},
		{"a runtime that answers but is not one", `#!/bin/sh
if [ "$1 $2" = "system status" ]; then echo "apiserver is running"; exit 0; fi
echo "this is not a runtime" >&2
exit 1
`},
	} {
		t.Run(machine.name, func(t *testing.T) {
			// `go` has to stay findable — the suite's TestMain builds the
			// fake shim with it — so the directory holds a link to that, and
			// whatever `container` this machine is supposed to have.
			pathDir := t.TempDir()
			if err := os.Symlink(goBin, filepath.Join(pathDir, "go")); err != nil {
				t.Fatalf("preparing a PATH: %v", err)
			}
			if machine.stub != "" {
				stub := filepath.Join(pathDir, "container")
				if err := os.WriteFile(stub, []byte(machine.stub), 0o755); err != nil {
					t.Fatalf("preparing the `container` this machine has: %v", err)
				}
			}

			// Everything else the environment carries goes through, minus the
			// ways a run could find a runtime by another route than the PATH.
			env := []string{"OPOSSUM_REAL_RUNTIME=1", "PATH=" + pathDir}
			for _, kv := range os.Environ() {
				switch {
				case strings.HasPrefix(kv, "PATH="),
					strings.HasPrefix(kv, "OPOSSUM_REAL_RUNTIME="),
					strings.HasPrefix(kv, "OPOSSUM_CONTAINER_BIN="),
					strings.HasPrefix(kv, "DOCKER_HOST="):
				default:
					env = append(env, kv)
				}
			}
			run := exec.Command(bin, "-test.run", "^"+conformancePrefix, "-test.v", "-test.timeout", "2m")
			// `go test` runs a package's binary in that package's directory,
			// and the suite's TestMain builds the fake shim from a path
			// relative to it.
			run.Dir = filepath.Join(root, "internal", "orchestrator")
			run.Env = env
			out, runErr := run.CombinedOutput()

			// The name has to be followed by the elapsed time, which a
			// subtest's is not: its own name comes after a `/` first. So a
			// subtest's verdict is not read as its parent's.
			verdict := regexp.MustCompile(`(?m)^\s*--- (PASS|FAIL|SKIP): (` + conformancePrefix + `[A-Za-z0-9_]*) \(`)
			found := verdict.FindAllStringSubmatch(string(out), -1)
			if len(found) != len(suite.roots) {
				t.Fatalf("the suite declares %d %s… test(s) %v but the run reported %d "+
					"verdict(s): a rule that reads fewer than were written passes for "+
					"the ones it never saw:\n%s",
					len(suite.roots), conformancePrefix, suite.roots, len(found), out)
			}
			var wrong []string
			for _, m := range found {
				if m[1] != "FAIL" {
					wrong = append(wrong, m[2]+" "+m[1])
				}
			}
			sort.Strings(wrong)
			if len(wrong) > 0 {
				t.Errorf("%d of the %d conformance test(s) did not fail on a machine with %s: %v.\n"+
					"  With the flag set and nothing to measure against, the run has to say so. "+
					"A pass says the opposite; a skip says the flag was not set, which it was. "+
					"Both hand back a verdict that reads like a measurement.\n%s",
					len(wrong), len(found), machine.name, wrong, out)
			}
			// A printed FAIL line and a failed run are two different facts.
			// Only worth saying when the report itself is what it should be:
			// an exit 0 with passes in it is already covered above.
			if len(wrong) == 0 && runErr == nil {
				t.Errorf("every conformance test failed on a machine with %s, and the suite "+
					"still exited 0 — whoever reads the exit status is told the opposite of "+
					"what the report says:\n%s", machine.name, out)
			}
		})
	}
}

// The record copies two lists out of the source: the conformance tests that run
// against a real runtime, and the paths OPSM-106 fires on. A copy is only as
// current as the last person to look at both — and this record went stale in
// several places before anyone did.
//
// So the lists are read from the source and looked for in the record. What is
// read is narrow, and worth saying plainly: the elements of the slice a function
// ranges over, and functions whose name starts with the prefix the gate selects
// by. A path tested for before that range, or a measurement written as a
// subtest, is not read, and the record can go on not mentioning it.
//
// The record is free in its order and its sentences, and free in how it marks a
// name — backticks, bold, a link, or nothing. What it is not free in is the
// characters either side: a name has to stand as a word. That is how a name is
// found without being found inside a longer one — against a word character;
// `Name/Sub` still satisfies `Name`, which the comment beside the search says
// more about.
//
// A path is asked for in backticks, because a word boundary cannot hold one: `/`
// is not a word character, so a boundary in front of `/dev/` asks for a word
// character there and finds a backtick or a space instead. The backticks the
// record already writes are the only edge a path has.
func TestTheRecordNamesWhatTheSourceHas(t *testing.T) {
	for _, p := range recordNamesWhatTheSourceHas(t, repoRoot(t)) {
		t.Error(p)
	}
}

// recordNamesWhatTheSourceHas is the rule above as a function of the tree it reads, so a
// copy of the tree with something broken in it can be held to the same rule
// (TestTheRecordRulesReadTheTreeTheyAreGiven). Fatal conditions — the
// tree not having what the rule reads — stay fatal.
func recordNamesWhatTheSourceHas(t *testing.T, root string) (problems []string) {
	record := string(readFile(t, filepath.Join(root, "docs", "real-runtime-review.md")))
	// A name the record has retired (struck through) is the record saying the
	// test is gone. It is not the record naming a test the source has, so it
	// is taken out before anything below looks for a mention.
	_, record = retiredNames(record)

	pkg := filepath.Join(root, "internal", "orchestrator")
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, pkg, nil, 0)
	if err != nil {
		t.Fatalf("reading %s: %v", pkg, err)
	}

	var conformance []string
	var prefixes []string
	for name, p := range pkgs {
		for _, f := range p.Files {
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok {
					continue
				}
				// The Makefile picks the conformance tests by this prefix, and
				// they live in the external test package, so both packages are
				// read for them.
				if strings.HasPrefix(fn.Name.Name, conformancePrefix) {
					conformance = append(conformance, fn.Name.Name)
				}
				// The paths, only from the package itself. A function of the
				// same name in the test package compiles fine and would be read
				// as though it were this one.
				if fn.Name.Name != hostDeviceFunc || name != "orchestrator" {
					continue
				}
				prefixes = append(prefixes, rangedStrings(fn)...)
			}
		}
	}
	if len(conformance) == 0 || len(prefixes) == 0 {
		t.Fatalf("read %d conformance tests and %d paths out of %s — one of them is zero, "+
			"so either they moved or this reads for something that is no longer there.",
			len(conformance), len(prefixes), pkg)
	}
	if got := makefileConformanceSelector(t, root); got != conformancePrefix {
		problems = append(problems, fmt.Sprintf("the gate selects conformance tests with %q and this reads them by %q. "+
			"One of the two moved, and whichever it was, the other now reads a "+
			"different set than the gate runs.", got, conformancePrefix))
	}
	// Both halves are after the same thing: a new item satisfying this by being a
	// piece of an old one. `TestARealSocketBind` is already there as the front of
	// `TestARealSocketBindCarriesTraffic`, and `/dev` as the front of `/dev/`.
	// What differs is what can serve as the edge, and how much it holds.
	//
	// A name is a word, so word boundaries hold it — but only against a word
	// character. `TestARealSocketBind` written in the record as
	// `TestARealSocketBind/CarriesTraffic` satisfies this, where asking for
	// backticks would not have. That is a loosening, taken knowingly: the two
	// directions disagreeing about what "named" means was the worse fault, and
	// the record writes no such name today.
	//
	// A path cannot use a boundary at all — `/` is not a word character, so one
	// in front of `/dev/` asks for a word character there and finds a backtick or
	// a space. For those, the backticks the record already writes are the edge,
	// and they hold unconditionally.
	//
	// Two more things this reads differently from the rest of the file, both
	// measured rather than reasoned:
	//
	//   - It reads fenced blocks. The splitter above skips them, on the grounds
	//     that an example of what to type is not a claim; here a name inside one
	//     still counts as named, so a record whose prose says a test was removed
	//     while a `go test -run …` example still spells it stays green.
	//   - A name that does not end in an ASCII word character cannot satisfy it
	//     at all. Go's word boundary is ASCII-only, so `TestARealソケット` is
	//     unfindable however the record writes it. That is the one place this is
	//     stricter than asking for backticks was, and there is no such name today.
	// Two more things the record has to name, both taken out of the source
	// rather than listed here. The pattern is the one above: what the test
	// stands on is read from the tree, and the record is held to it.
	//
	//   - The strings the daemon measurement declares as what an answer has to
	//     carry, and that it walks them. Changing a listed mark without the
	//     record, or leaving the declaration unused, fail here. Dropping a mark
	//     does not, and neither does what a body then does with one; the
	//     boundary is written out on conformanceJudgements.
	//   - The path the documents name, and that it is the only one resolved in
	//     the file declaring it. The value alone was not enough: a second name
	//     resolved beside it moved the measurement while the documented one
	//     stayed put.
	judged, socketPath, resolved, ranged := conformanceJudgements(t, fset, pkgs)

	namedWord := func(s string) bool {
		return regexp.MustCompile(`\b` + regexp.QuoteMeta(s) + `\b`).MatchString(record)
	}
	namedPath := func(s string) bool { return strings.Contains(record, "`"+s+"`") }
	for _, name := range conformance {
		if !namedWord(name) {
			problems = append(problems, fmt.Sprintf("%s runs against a real runtime and docs/real-runtime-review.md never "+
				"names it. The record says what has been measured; a test it does not "+
				"know about is a measurement nobody there can see.", name))
		}
	}
	for _, p := range prefixes {
		if !namedPath(p) {
			problems = append(problems, fmt.Sprintf("%s covers the host path %q and docs/real-runtime-review.md does not "+
				"say so. The record explains which paths that note covers, and this one "+
				"is not among them.", hostDeviceFunc, p))
		}
	}
	// Judged strings are held to the same edges as paths, and for a sharper
	// reason than paths have. Plain containment was tried first and let the
	// mutation this rule exists for through: an assertion weakened from
	// `Api-Version:` to `HTTP/` stayed green, because the record says
	// `HTTP/1.0 200 OK` and every weakening of a string the record spells is a
	// substring of it. Backticks make the record name the decision exactly, so
	// a test that decides on less has to say so.
	for _, j := range judged {
		if !namedPath(j.lit) {
			problems = append(problems, fmt.Sprintf("%s decides on %q and docs/real-runtime-review.md does not name it "+
				"between backticks. What a measurement accepts as an answer is the "+
				"measurement; a record that does not say it cannot tell a reader what "+
				"was ruled out — and one that says only a longer string containing it "+
				"reads as though more were required than is.", j.fn, j.lit))
		}
	}
	if socketPath == "" {
		t.Fatalf("no %s constant was read out of %s, so the rule below passes on anything",
			daemonSocketConst, pkg)
	}
	if !namedPath(socketPath) {
		problems = append(problems, fmt.Sprintf("%s is %q and docs/real-runtime-review.md does not name it. The published "+
			"claim is about the path the documents name; a measurement that starts "+
			"somewhere else measures a different claim.", daemonSocketConst, socketPath))
	}
	if len(judged) > 0 && !ranged {
		problems = append(problems, fmt.Sprintf("nothing walks %s any more, so holding it against the record above says "+
			"nothing about what the measurement asks for. A declaration no test reads is "+
			"a record of an intention, not of a measurement.", daemonMarksVar))
	}
	if len(resolved) != 1 || resolved[0] != daemonSocketConst {
		problems = append(problems, fmt.Sprintf("the file holding the daemon measurement resolves %v, and the record is "+
			"written about resolving %s and nothing else. Either one path is resolved "+
			"there, or the sentence above about which name the measurement starts from "+
			"describes a different run than the one that happens.", resolved, daemonSocketConst))
	}
	return problems
}

// daemonSocketConst holds the path the daemon measurement resolves before it
// binds anything. Named here for the same reason hostDeviceFunc is: the rule
// is about this one thing, and reading every constant in the package would
// make it about something else.
const daemonSocketConst = "dockerSocketName"

// judgementLead names the passage in the record that says what the daemon
// measurement decides on. The passage is one paragraph and holds nothing else:
// the value that was observed once lives in the next one, so that everything
// between backticks here is a decision. That is what makes the comparison
// below possible — the record's own shape decides what counts as a judgement,
// rather than this guessing which of its backticked strings look like marks.
const judgementLead = "daemon を名指すのは後者"

// The rule above reads source → record: every string the measurement decides
// on has to be named in the record. That direction alone lets the record
// outlive what it describes. Deleting the daemon measurement, and with it the
// declaration this reads, left the record still saying in as many words that
// the answer is judged on `Api-Version:` — with nothing measuring it, and
// nothing to say so (#657).
//
// A Fatal stood there instead, refusing the deletion. That kept the record
// honest by forbidding a change that took nothing away, and it said as much
// itself: "the rule below passes on anything". What was missing was the other
// direction, so this reads it: the strings the record names between backticks
// in the judgement passage, and the strings the measurement decides on, are
// the same set.
//
// What each way round catches:
//
//   - A mark dropped from the declaration: the record still names it, the
//     source no longer decides on it. Red here; green under the old rules,
//     which is the hole #657 opened this with.
//   - The measurement deleted whole: every mark the record names is
//     unmeasured. Red here.
//   - Both taken out together: nothing is named and nothing is decided on.
//     Green — there is no claim left to keep honest. That is about the marks:
//     taking the whole daemon measurement out is still red elsewhere, on
//     daemonSocketConst's own rule and on the record naming a test that no
//     longer exists.
//   - A mark added without the record: red under the rule above, and here.
//     Written into the record's next paragraph rather than the judgement one,
//     it is green under the rule above and red here — which is what binding
//     this to one passage buys.
//
// What it does not catch, measured rather than guessed:
//
//   - Backticks taken off. Dropping a mark from the declaration and, in the
//     same sentence, writing Api-Version: without them changes no word a
//     reader sees and is green: what makes a string a judgement here is the
//     backticks, and nothing holds the record to using them.
//   - The lead rewritten while the marks go. Saying the same thing in other
//     words moves the passage out of reach, and with the declaration empty
//     this returns quietly — the "nothing said about it" half of that early
//     return is assumed, not read. Either alone is red; it takes both.
//   - A declaration this cannot read. conformanceJudgements used to skip what
//     it could not read, so a mark moved into a constant looked deleted and
//     this accused the record. It says so itself now, and fails before this
//     rule runs — except where the declaration is not where it looks: a
//     daemonMarks written inside a function is not a package-level
//     declaration, nothing reads it, and this accuses the record again
//     (measured, #657).
//
// The passage has to exist while anything is decided on. Missing it while the
// source still judges is a record that stopped describing its measurement, and
// this fails loudly rather than reading an empty set.
func TestTheSourceJudgesOnWhatTheRecordNames(t *testing.T) {
	root := repoRoot(t)
	recordPath := filepath.Join(root, "docs", "real-runtime-review.md")
	record := readLines(t, recordPath)

	pkg := filepath.Join(root, "internal", "orchestrator")
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, pkg, nil, 0)
	if err != nil {
		t.Fatalf("reading %s: %v", pkg, err)
	}
	judged, _, _, _ := conformanceJudgements(t, fset, pkgs)

	decides := map[string]bool{}
	for _, j := range judged {
		decides[j.lit] = true
	}

	block, err := blockContaining(record, judgementLead)
	if err != nil {
		if len(decides) == 0 {
			return // nothing decided on, and nothing said about it
		}
		t.Fatalf("the measurement decides on %d string(s) and docs/real-runtime-review.md "+
			"has no passage holding %q: %v. The record is where what a measurement "+
			"accepts is written down; one that has stopped saying it cannot be held to "+
			"anything.", len(decides), judgementLead, err)
	}

	names := map[string]bool{}
	for _, m := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(block, -1) {
		names[m[1]] = true
	}

	var unmeasured, unrecorded []string
	for s := range names {
		if !decides[s] {
			unmeasured = append(unmeasured, s)
		}
	}
	for s := range decides {
		if !names[s] {
			unrecorded = append(unrecorded, s)
		}
	}
	sort.Strings(unmeasured)
	sort.Strings(unrecorded)
	if len(unmeasured) > 0 {
		t.Errorf("docs/real-runtime-review.md says the answer is judged on %v and nothing "+
			"in %s decides on them. A record naming what no measurement asks for is the "+
			"shape this file exists to refuse: it reads as though the claim were still "+
			"being checked.\n"+
			"  Before taking it out of the record, check that the measurement is not "+
			"deciding on it under another name: a local of a different name, or a "+
			"package-level declaration this reads while the run uses one written inside "+
			"a function, both leave the mark measured and invisible here, and the record "+
			"would be the wrong thing to change. A declaration this cannot read, and a "+
			"name with no declaration this reads, each say so on their own before this "+
			"(#657).", unmeasured, pkg)
	}
	if len(unrecorded) > 0 {
		t.Errorf("%s decides on %v and the judgement passage in "+
			"docs/real-runtime-review.md does not name them. What a measurement accepts "+
			"is the measurement; a reader of the record would be told less is required "+
			"than is.", pkg, unrecorded)
	}
}

type judgement struct{ fn, lit string }

// daemonMarksVar holds what the daemon measurement accepts as an answer.
const daemonMarksVar = "daemonMarks"

// conformancePackage is the test package the conformance suite lives in. The
// suite is external ("_test") because it drives the built binary, so the
// package under measurement and the package this reads are not the same name.
const conformancePackage = "orchestrator_test"

// realRuntimeGate is the one function in the conformance suite allowed to skip.
// It implements the opt-out: without the flag there is nothing to measure, so
// skipping is the whole point of it. Everywhere else in that file a missing
// precondition has to be loud.
const realRuntimeGate = "realRuntime"

// conformanceJudgements reads four things out of the conformance suite, and
// refuses to hand back a half-read fifth: where it cannot read the marks, or
// cannot find a declaration of them while the name is used, it fails with the
// places rather than returning an empty set that reads as "nothing is judged
// on" (#657). The four are: the
// strings the daemon measurement declares as what an answer has to carry,
// whether that declaration is walked, the value of daemonSocketConst, and
// which paths the file declaring it resolves.
//
// Nothing here infers what a test decides on, and the limits of that are worth
// stating plainly. Three rounds of review were spent inferring: reading
// literals from every call into `strings` asked the record to spell a `Split`
// separator; narrowing to `if` conditions let
// `if ok := strings.Contains(out, "HTTP/"); !ok` through; following
// package-level constants left function-local ones — the place a person
// actually puts one — untouched. Each version was escaped by writing the same
// assertion a different way, because where a value flows is not visible in how
// the code is arranged.
//
// So the suite declares its marks and this reads the declaration. What that
// holds is every mark the declaration still lists: none of them can be changed
// without the record changing with it, and the declaration cannot be left
// behind unused.
//
// What it does not hold is a mark that stops being listed. Dropping
// `Api-Version:` from the slice leaves the record saying the answer is judged
// on it and nothing red, which is the same shape as the sentence this whole
// change was written to put a measurement behind. Neither is what a test
// means read: a body that walks the marks and then asks something else of the
// answer passes here, and so does one that asserts nothing. All three want the
// other direction — the record naming what must be asserted, compared as a
// set — and that is #657, not this.
//
// The resolved paths are counted rather than looked for. Asking whether the
// documented constant is resolved anywhere was satisfied by resolving it once
// for a failure message beside a second constant that was resolved for real.
// Counting says something a search cannot: within one file, one path is
// resolved, and it is the one the documents name.
//
// Within one file is the whole of it. The file is the one declaring the
// constant, because the package resolves other paths in other files for their
// own reasons — an earlier version counted those and was wrong about the
// package. The cost of that boundary is that a sibling file in the same
// package can resolve anything it likes and hand the result over, which is
// #657.
func conformanceJudgements(t *testing.T, fset *token.FileSet, pkgs map[string]*ast.Package) (judged []judgement, socketPath string, resolved []string, ranged bool) {
	t.Helper()
	var unreadable []string // places inside a declaration this reads but cannot make sense of
	var outOfReach []string // places the name appears when no declaration was read at all
	var shadows []string    // places the name is declared again inside a function
	var written []string    // places the name is assigned to after the declaration
	declared := false       // a package-level daemonMarks was found and read
	at := func(n ast.Node) string {
		pos := fset.Position(n.Pos())
		return filepath.Base(pos.Filename) + ":" + strconv.Itoa(pos.Line) + ":" + strconv.Itoa(pos.Column)
	}
	defer func() {
		t.Helper()
		switch {
		case len(unreadable) > 0:
			t.Fatalf("%s is declared where this reads it, and %d place(s) in that "+
				"declaration cannot be read (%s) — places, not marks: one of them may "+
				"stand for several. What is read is a list of string literals written "+
				"out; a mark built by a call, assigned somewhere else, or named by a "+
				"constant is invisible here, and invisible reads the same as absent — "+
				"which makes the record look wrong where it is right. Write the marks "+
				"out as literals, or teach this to read what is there (#657).",
				daemonMarksVar, len(unreadable), strings.Join(unreadable, ", "))
		case declared && len(shadows) > 0:
			t.Fatalf("%s is declared where this reads it, and a function gives the same "+
				"name to something of its own at %d place(s) (%s) — a short declaration, "+
				"a var, a parameter or result, a receiver, or a range variable. This "+
				"reads the package's declaration and holds it against the record; a run "+
				"reaching the other one measures something else, and nothing here goes "+
				"red about it. Names are all this compares: whether the second one is "+
				"even in scope where the marks are used is not worked out, so a name "+
				"reused somewhere unrelated lands here too. Give one of them another "+
				"name, or teach this to tell them apart (#657).",
				daemonMarksVar, len(shadows), strings.Join(shadows, ", "))
		case declared && len(written) > 0:
			t.Fatalf("%s is declared where this reads it, and assigned to at %d place(s) "+
				"(%s). What is read is the value written at the declaration; what the run "+
				"measures is whatever was last assigned. Those can differ with nothing "+
				"here going red about it, which is the same silence a second declaration "+
				"of the name would buy. What is read is the name standing alone on the "+
				"left of an assignment: writing an element, copying into the slice, or "+
				"going through a pointer changes it just as well and is not read here, "+
				"and neither is whether this assignment runs before the marks are used. "+
				"Build the list once where it is declared, or teach this to follow what "+
				"happens to it (#657).",
				daemonMarksVar, len(written), strings.Join(written, ", "))
		case len(outOfReach) > 0:
			t.Fatalf("no declaration of %s was read, and the name is used in %s at %d "+
				"place(s) (%s) — every mention, not only declarations. What is read is "+
				"package-level declarations, so one written inside a function is measured "+
				"and invisible here; invisible reads the same as absent, which makes the "+
				"record look wrong where it is right. Declare the marks where the package "+
				"can see them, or teach this to read what is there (#657).",
				daemonMarksVar, conformancePackage, len(outOfReach),
				strings.Join(outOfReach, ", "))
		}
	}()
	for name, p := range pkgs {
		// One package only. A declaration of the same name elsewhere in the
		// tree compiles fine and would be read as though it were this one —
		// the same reason the host paths above are read from one package.
		if name != conformancePackage {
			continue
		}
		for _, f := range p.Files {
			declares := false
			for _, d := range f.Decls {
				gen, ok := d.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, sp := range gen.Specs {
					vs, ok := sp.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, n := range vs.Names {
						if i >= len(vs.Values) {
							// A declaration with no value of its own — one
							// name of a grouped const, say. Nothing to read,
							// and for the marks that is not the same as
							// nothing to say.
							if n.Name == daemonMarksVar {
								declared = true
								unreadable = append(unreadable, at(n))
							}
							continue
						}
						switch n.Name {
						case daemonSocketConst:
							declares = true
							if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
								socketPath, _ = strconv.Unquote(lit.Value)
							}
						case daemonMarksVar:
							declared = true
							comp, ok := vs.Values[i].(*ast.CompositeLit)
							if !ok {
								// Built rather than written out — appended
								// to, assigned in init, taken from a call.
								// The elements are still there at run time
								// and unreadable from here, which is the
								// whole of what this is about.
								unreadable = append(unreadable, at(vs.Values[i]))
								continue
							}
							for _, el := range comp.Elts {
								lit, ok := el.(*ast.BasicLit)
								if !ok || lit.Kind != token.STRING {
									// Not skipped quietly: an element this
									// cannot read is not an element that is
									// not there, and the rules downstream
									// cannot tell those apart.
									unreadable = append(unreadable, at(el))
									continue
								}
								if v, err := strconv.Unquote(lit.Value); err == nil && v != "" {
									judged = append(judged, judgement{daemonMarksVar, v})
								}
							}
						}
					}
				}
			}
			// Every path the daemon measurement's own file resolves, so the
			// count can be held to one. The file is found by the declaration
			// rather than by name: the package resolves other paths in other
			// files for their own reasons, and holding those to this rule
			// would be a claim about the package that is not true.
			if !declares {
				continue
			}
			// And whether the declaration is what the measurement walks. A
			// slice can be declared and then not used: the marks stayed right
			// while the assertion ranged over a literal beside them, and
			// reading the declaration alone called that green.
			ast.Inspect(f, func(n ast.Node) bool {
				if rng, ok := n.(*ast.RangeStmt); ok {
					if id, ok := rng.X.(*ast.Ident); ok && id.Name == daemonMarksVar {
						ranged = true
					}
				}
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "EvalSymlinks" {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "filepath" {
					return true
				}
				if id, ok := call.Args[0].(*ast.Ident); ok {
					resolved = append(resolved, id.Name)
				} else {
					resolved = append(resolved, "an expression that is not a name")
				}
				return true
			})
		}
	}
	// A second declaration of the same name inside a function is not read here
	// and may be the one the measurement uses. Nothing downstream can tell:
	// the marks come back non-empty and wrong, which is the one shape in this
	// family that stays green.
	for name, p := range pkgs {
		if name != conformancePackage {
			continue
		}
		for _, f := range p.Files {
			// Every way a function gives the name to something of its own:
			// a short declaration, a var, a parameter or result, a receiver,
			// the key or value of a range. Each of them is a declaration Go
			// resolves before the package-level one.
			named := func(fl *ast.FieldList) {
				if fl == nil {
					return
				}
				for _, fld := range fl.List {
					for _, id := range fld.Names {
						if id.Name == daemonMarksVar {
							shadows = append(shadows, at(id))
						}
					}
				}
			}
			var bodies []ast.Node
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *ast.FuncDecl:
					if d.Body == nil {
						continue
					}
					named(d.Recv)
					named(d.Type.Params)
					named(d.Type.Results)
					bodies = append(bodies, d.Body)
				case *ast.GenDecl:
					// A function literal held by a package-level variable runs
					// like any other body, and the declaration it may write to
					// is the one being read here.
					if d.Tok != token.VAR {
						continue
					}
					for _, sp := range d.Specs {
						vs, ok := sp.(*ast.ValueSpec)
						if !ok {
							continue
						}
						for _, v := range vs.Values {
							if lit, ok := v.(*ast.FuncLit); ok {
								named(lit.Type.Params)
								named(lit.Type.Results)
								bodies = append(bodies, lit.Body)
							}
						}
					}
				}
			}
			for _, body := range bodies {
				ast.Inspect(body, func(n ast.Node) bool {
					switch n := n.(type) {
					case *ast.AssignStmt:
						// A short declaration makes a second name; a plain
						// assignment keeps the one name and changes what it
						// holds. Both leave the declaration this reads
						// describing something the run does not use.
						for _, lhs := range n.Lhs {
							id, ok := lhs.(*ast.Ident)
							if !ok || id.Name != daemonMarksVar {
								continue
							}
							if n.Tok == token.DEFINE {
								shadows = append(shadows, at(id))
							} else {
								written = append(written, at(id))
							}
						}
					case *ast.ValueSpec:
						for _, id := range n.Names {
							if id.Name == daemonMarksVar {
								shadows = append(shadows, at(id))
							}
						}
					case *ast.RangeStmt:
						if n.Tok != token.DEFINE {
							return true
						}
						for _, e := range []ast.Expr{n.Key, n.Value} {
							if id, ok := e.(*ast.Ident); ok && id.Name == daemonMarksVar {
								shadows = append(shadows, at(id))
							}
						}
					case *ast.FuncLit:
						named(n.Type.Params)
						named(n.Type.Results)
					}
					return true
				})
			}
		}
	}

	// The name used somewhere this does not read is not the name gone. Only
	// package-level declarations are walked above, so a daemonMarks written
	// inside a function is measured and invisible — and invisible would reach
	// the rules downstream as an empty set, which reads as "the record names
	// what nothing measures". Say what is true instead: it is here, and not
	// where this can read it.
	if !declared {
		for name, p := range pkgs {
			if name != conformancePackage {
				continue
			}
			for _, f := range p.Files {
				ast.Inspect(f, func(n ast.Node) bool {
					if id, ok := n.(*ast.Ident); ok && id.Name == daemonMarksVar {
						// Every mention, declarations and uses alike: which
						// of them is the declaration is exactly what this
						// could not work out.
						outOfReach = append(outOfReach, at(id))
					}
					return true
				})
			}
		}
	}
	return judged, socketPath, resolved, ranged
}

// rangedStrings returns the strings a function ranges over: the elements of a
// slice literal written in a range clause, and nothing else.
//
// Not every string in the function. The first version of this took them all,
// and so took the "/" from a TrimSuffix call as though it were a path opossum
// notes — which the record then satisfied by containing a slash. A later `return
// false` for some path would have been reported as a path the note fires on,
// leaving one honest way to go green: write something untrue in the record.
func rangedStrings(fn *ast.FuncDecl) []string {
	var out []string
	ast.Inspect(fn, func(n ast.Node) bool {
		rng, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		lit, ok := rng.X.(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, e := range lit.Elts {
			if s, ok := e.(*ast.BasicLit); ok && s.Kind == token.STRING {
				// Unquoted rather than trimmed of quote characters: a raw
				// string literal would otherwise arrive with its backticks
				// still on, and the failure would name a path nobody wrote.
				v, err := strconv.Unquote(s.Value)
				if err != nil {
					continue
				}
				out = append(out, v)
			}
		}
		return true
	})
	return out
}

// What makes a test one of the real-runtime measurements. The gate decides this
// with a -run pattern, so the pattern is where it is decided and this only
// repeats it — checked below, so that repeating it cannot go quietly wrong.
const conformancePrefix = "TestAReal"

// The function that decides which host paths get a note.
const hostDeviceFunc = "isHostDevicePath"

// Where the record says which paths that function covers. Two checks look for
// this same passage — one compares it against a copy, the other reads the paths
// out of it — and a lead written twice is a lead that can be half-updated.
//
// What one place buys is narrow, and worth saying exactly: if these words are
// reworded, both checks fail together. Rewording the rest of the passage fails
// only the one that compares it against a copy, because the other uses these
// words to find the passage and then reads paths out of it. The constant holds
// the finding, not the passage.
const hostDevicePassage = "`OPSM-106` が当たるパス"

// The other direction. TestTheRecordNamesWhatTheSourceHas asks whether what the
// source has is in the record; this asks whether what the record names is still
// in the source. A record that goes on naming a test nobody can run, or a path
// the note no longer covers, sends the next person looking for something that
// is not there — the same staleness, read from the other end.
//
// Two rules, and they are not the same rule:
//
//   - A conformance test named in the record has to exist. A rename is a change
//     to what a reader can run, and the record is where they look it up.
//   - A path named in the passage about which paths the note covers has to be
//     one the predicate actually covers — either in its list, or under one of
//     the prefixes in it. That is the predicate's own meaning, so the record is
//     checked against what the code does rather than against a copy of its list.
func TestTheSourceStillHasWhatTheRecordNames(t *testing.T) {
	for _, p := range sourceStillHasWhatTheRecordNames(t, repoRoot(t)) {
		t.Error(p)
	}
}

// sourceStillHasWhatTheRecordNames is the rule above as a function of the tree it reads, so a
// copy of the tree with something broken in it can be held to the same rule
// (TestTheRecordRulesReadTheTreeTheyAreGiven). Fatal conditions — the
// tree not having what the rule reads — stay fatal.
func sourceStillHasWhatTheRecordNames(t *testing.T, root string) (problems []string) {
	record := readLines(t, filepath.Join(root, "docs", "real-runtime-review.md"))
	joined := strings.Join(record, "\n")

	pkg := filepath.Join(root, "internal", "orchestrator")
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, pkg, nil, 0)
	if err != nil {
		t.Fatalf("reading %s: %v", pkg, err)
	}
	have := map[string]bool{}
	var prefixes []string
	for name, p := range pkgs {
		for _, f := range p.Files {
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok {
					continue
				}
				have[fn.Name.Name] = true
				if fn.Name.Name == hostDeviceFunc && name == "orchestrator" {
					prefixes = append(prefixes, rangedStrings(fn)...)
				}
			}
		}
	}
	if len(prefixes) == 0 {
		t.Fatalf("no paths read out of %s, so the second rule below would pass on anything", hostDeviceFunc)
	}

	// Names are read as words here, the same as in the other direction. That is
	// new: this used to require backticks, on the reasoning that the two
	// directions wanted different things. They do not. What wanted something
	// different was paths, and only because a word boundary cannot hold one.
	//
	// Reading too many names is a false red someone looks at and fixes; reading
	// too few is silence, so where the two rules differ this one is the loose
	// side on purpose.
	//
	// A name written struck through — ~~`TestARealX`~~ — is the record saying
	// the test is gone, which is the one true sentence the plain rule forbade
	// (#643). It is held to the opposite: the source must not have it, or the
	// record is announcing a removal that did not happen.
	found, outOfStep := namesOutOfStep(joined, have)
	if found == 0 {
		t.Fatalf("docs/real-runtime-review.md names no conformance test, so this reads " +
			"for something that is no longer written there")
	}
	for _, p := range outOfStep {
		problems = append(problems, fmt.Sprintf("docs/real-runtime-review.md %s (%s)", p, pkg))
	}

	covered := func(p string) bool {
		for _, pre := range prefixes {
			if p == strings.TrimSuffix(pre, "/") || strings.HasPrefix(p, pre) {
				return true
			}
		}
		return false
	}
	block, err := blockContaining(record, hostDevicePassage)
	if err != nil {
		t.Fatalf("the passage about which paths %s covers: %v", hostDeviceFunc, err)
	}
	paths := regexp.MustCompile("`(/[^`]*)`").FindAllStringSubmatch(block, -1)
	if len(paths) == 0 {
		t.Fatal("that passage names no path, so this reads for something no longer there")
	}
	for _, m := range paths {
		if !covered(m[1]) {
			problems = append(problems, fmt.Sprintf("docs/real-runtime-review.md names %q where it says which paths "+
				"%s covers, and %s does not cover it. Every path in that passage has "+
				"to be one the predicate fires on — a path named there to say it is "+
				"excluded reads, to someone skimming, as one of the covered ones.",
				m[1], hostDeviceFunc, hostDeviceFunc))
		}
	}
	return problems
}

// struckName is how the record writes a conformance test that no longer
// exists: the name, in backticks, struck through.
var struckName = regexp.MustCompile("~~`(" + conformancePrefix + "[A-Za-z0-9_]*)`~~")

// brokenStrike is the same thing written with spaces inside the strike-through
// (~~ `X` ~~). Read as plain text it would satisfy the plain rule whenever X
// still exists — a "removed" nobody checks — so it is named as the wrong form
// instead of silently passing.
var brokenStrike = regexp.MustCompile("~~(?:\\s+`(" + conformancePrefix + "[A-Za-z0-9_]*)`\\s*|\\s*`(" + conformancePrefix + "[A-Za-z0-9_]*)`\\s+)~~")

// retiredNames reads the names the record has struck through, and returns the
// record with those spans removed — the text in which a name still counts as a
// mention. Only conformance test names are read this way; a struck-through
// path or judged string stays as it is, and is still held to the plain rules.
func retiredNames(record string) (retired map[string]bool, live string) {
	retired = map[string]bool{}
	for _, m := range struckName.FindAllStringSubmatch(record, -1) {
		retired[m[1]] = true
	}
	// A space, not nothing: ~~`X`~~ glued between word characters would
	// otherwise fuse its neighbours into one word and hide a name from `\b`.
	return retired, struckName.ReplaceAllString(record, " ")
}

// namesOutOfStep is the record→source rule as a function of its inputs: every
// plain name the record writes must be a function the source has, and every
// struck-through name must not be. found is how many names were read at all,
// so a caller can tell "nothing out of step" from "nothing read".
//
// Names are read as words, the loose side on purpose: reading too many is a
// false red someone looks at and fixes, reading too few is silence. The same
// name written both ways is out of step with itself and both rules fire.
func namesOutOfStep(record string, have map[string]bool) (found int, problems []string) {
	retired, live := retiredNames(record)
	named := regexp.MustCompile(`\b(` + conformancePrefix + `[A-Za-z0-9_]*)\b`)
	seen := map[string]bool{}
	for _, m := range named.FindAllStringSubmatch(live, -1) {
		found++
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		if !have[m[1]] {
			problems = append(problems, "names "+m[1]+" and the source has no such function. "+
				"A reader looking it up finds nothing; if it was renamed, the record is where "+
				"the new name goes, and if it was removed, the record writes it struck through")
		}
	}
	for _, name := range sortedKeys(retired) {
		found++
		if have[name] {
			problems = append(problems, "writes "+name+" struck through — as a test that is gone — "+
				"and the source still has it. Either the removal did not happen, or the strike-through "+
				"is on the wrong name")
		}
	}
	for _, m := range brokenStrike.FindAllStringSubmatch(record, -1) {
		name := m[1] + m[2]
		problems = append(problems, "writes "+name+" with spaces inside the strike-through, which is "+
			"not the form and reads as a plain name; write ~~`"+name+"`~~ with nothing between")
	}
	return found, problems
}

// TestARetiredNameIsHeldToTheOppositeRule pins what the two directions make of
// a struck-through name, on records small enough to read: it must be gone from
// the source, and it does not count as the record naming a test the source has.
func TestARetiredNameIsHeldToTheOppositeRule(t *testing.T) {
	have := map[string]bool{"TestARealAlive": true}
	for _, tc := range []struct {
		name, record string
		wantFound    int
		wantProblems int
		wantLive     string // what the source→record direction still sees
	}{
		{"a plain name the source has", "measured by `TestARealAlive`.", 1, 0, "measured by `TestARealAlive`."},
		{"a plain name the source lost", "measured by `TestARealGone`.", 1, 1, "measured by `TestARealGone`."},
		{"a retired name that is gone", "2026-09-01 に ~~`TestARealGone`~~ を消した。", 1, 0, "2026-09-01 に   を消した。"},
		{"a retired name the source still has", "~~`TestARealAlive`~~ を消した。", 1, 1, "  を消した。"},
		{"the same name both ways", "`TestARealAlive` and ~~`TestARealAlive`~~", 2, 1, "`TestARealAlive` and  "},
		{"a retired name in a fenced example is still retired", "```sh\ngo test -run ~~`TestARealGone`~~\n```", 1, 0, "```sh\ngo test -run  \n```"},
		{"a plain name in a fence is still a plain name", "```sh\ngo test -run TestARealGone\n```", 1, 1, "```sh\ngo test -run TestARealGone\n```"},
		{"strike-through without backticks is not the form", "~~TestARealGone~~", 1, 1, "~~TestARealGone~~"},
		{"strike-through with spaces inside is named as the wrong form, even on a live name", "~~ `TestARealAlive` ~~ を消した。", 1, 1, "~~ `TestARealAlive` ~~ を消した。"},
		{"strike-through with a space on one side, on a gone name, is the wrong form and a missing name", "~~`TestARealGone` ~~", 1, 2, "~~`TestARealGone` ~~"},
		{"a strike-through glued between words does not fuse them", "see~~`TestARealGone`~~TestARealAlive", 2, 0, "see TestARealAlive"},
		{"an empty record", "", 0, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found, problems := namesOutOfStep(tc.record, have)
			if found != tc.wantFound || len(problems) != tc.wantProblems {
				t.Errorf("found %d names, %d problems %v; want %d and %d", found, len(problems), problems, tc.wantFound, tc.wantProblems)
			}
			if _, live := retiredNames(tc.record); live != tc.wantLive {
				t.Errorf("the text the other direction reads = %q, want %q", live, tc.wantLive)
			}
		})
	}
}

// TestTheRecordRulesReadTheTreeTheyAreGiven holds the wiring of the two record
// rules, which the table test above cannot: it copies what the rules read out
// of the real tree — the record, the package it names, the Makefile the
// conformance selector is read from — and holds the rules to that copy, clean
// and then broken in each of the ways they exist for. Emptying either rule's
// findings at the call site used to leave every test green (#793); here it
// leaves a broken copy unreported.
func TestTheRecordRulesReadTheTreeTheyAreGiven(t *testing.T) {
	real := repoRoot(t)
	root := t.TempDir()
	for _, rel := range []string{"Makefile", "docs/real-runtime-review.md"} {
		dst := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, readFile(t, filepath.Join(real, filepath.FromSlash(rel))), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pkg := filepath.Join(root, "internal", "orchestrator")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(real, "internal", "orchestrator"))
	if err != nil {
		t.Fatal(err)
	}
	copied := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if err := os.WriteFile(filepath.Join(pkg, e.Name()), readFile(t, filepath.Join(real, "internal", "orchestrator", e.Name())), 0o644); err != nil {
			t.Fatal(err)
		}
		copied++
	}
	if copied == 0 {
		t.Fatal("copied no .go files, so the rules below would read an empty package")
	}

	record := filepath.Join(root, "docs", "real-runtime-review.md")
	original := string(readFile(t, record))
	const example = "~~`TestARealExample`~~"
	if strings.Count(original, example) != 1 {
		t.Fatalf("the record writes %s %d times; the edits below replace exactly one", example, strings.Count(original, example))
	}
	const symlink = "`TestARealSymlinkToASocketIsStillRefused`"
	if strings.Count(original, symlink) != 2 {
		t.Fatalf("the record writes %s %d times; the edit below retires the first and drops the second", symlink, strings.Count(original, symlink))
	}
	// The source-side edit renames one conformance test in its copy.
	const socket = "func TestARealSocketBindCarriesTraffic("
	var socketFile string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if strings.Contains(string(readFile(t, filepath.Join(pkg, e.Name()))), socket) {
			socketFile = filepath.Join(pkg, e.Name())
		}
	}
	if socketFile == "" {
		t.Fatalf("no copied file declares %s", socket)
	}
	socketOriginal := string(readFile(t, socketFile))

	both := func() (source, rec []string) {
		return recordNamesWhatTheSourceHas(t, root), sourceStillHasWhatTheRecordNames(t, root)
	}
	if source, rec := both(); len(source)+len(rec) != 0 {
		t.Fatalf("the untouched copy of the real tree is reported as out of step — the edits below would measure nothing:\n%v\n%v", source, rec)
	}

	saying := func(t *testing.T, direction string, got []string, want []string) {
		t.Helper()
		if len(want) == 0 {
			if len(got) != 0 {
				t.Errorf("%s reported %v; want nothing from that direction", direction, got)
			}
			return
		}
		for _, w := range want {
			found := false
			for _, g := range got {
				if strings.Contains(g, w) {
					found = true
				}
			}
			if !found {
				t.Errorf("%s did not say %q; it said %v", direction, w, got)
			}
		}
	}
	for _, tc := range []struct {
		name       string
		record     func(string) string
		source     func(string) string
		wantSource []string // source → record: what the source has and the record does not name
		wantRecord []string // record → source: what the record names and the source does not have
	}{
		{"a retired name the source still has",
			func(r string) string {
				return strings.Replace(r, example, "~~`TestARealSocketBindCarriesTraffic`~~", 1)
			}, nil,
			nil, []string{"TestARealSocketBindCarriesTraffic struck through", "source still has it"}},
		{"a plain name the source does not have",
			func(r string) string { return strings.Replace(r, example, "`TestARealExample`", 1) }, nil,
			nil, []string{"names TestARealExample", "no such function"}},
		{"a strike-through with spaces inside",
			func(r string) string {
				return strings.Replace(r, example, "~~ `TestARealSocketBindCarriesTraffic` ~~", 1)
			}, nil,
			nil, []string{"TestARealSocketBindCarriesTraffic with spaces inside the strike-through"}},
		{"a test retired in the record while the source still has it",
			func(r string) string {
				i := strings.Index(r, symlink)
				j := strings.Index(r[i+1:], symlink) + i + 1
				r = r[:j] + "(removed)" + r[j+len(symlink):]
				return r[:i] + "~~" + symlink + "~~" + r[i+len(symlink):]
			}, nil,
			[]string{"TestARealSymlinkToASocketIsStillRefused runs against a real runtime", "never names it"},
			[]string{"TestARealSymlinkToASocketIsStillRefused struck through"}},
		{"a test renamed in the source",
			nil, func(src string) string {
				return strings.Replace(src, socket, "func TestARealSocketBindCarriesTrafficX(", 1)
			},
			[]string{"TestARealSocketBindCarriesTrafficX runs against a real runtime", "never names it"},
			[]string{"names TestARealSocketBindCarriesTraffic", "no such function"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.record != nil {
				if err := os.WriteFile(record, []byte(tc.record(original)), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.source != nil {
				if err := os.WriteFile(socketFile, []byte(tc.source(socketOriginal)), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			defer func() {
				if err := os.WriteFile(record, []byte(original), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(socketFile, []byte(socketOriginal), 0o644); err != nil {
					t.Fatal(err)
				}
			}()
			source, rec := both()
			saying(t, "source → record", source, tc.wantSource)
			saying(t, "record → source", rec, tc.wantRecord)
		})
	}
}

// makefileConformanceSelector returns the -run pattern the real-conformance
// target uses, read from that recipe rather than from the first -run in the file.
func makefileConformanceSelector(t *testing.T, root string) string {
	t.Helper()
	lines := readLines(t, filepath.Join(root, "Makefile"))
	at := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "real-conformance:") {
			at = i
			break
		}
	}
	if at < 0 {
		t.Fatal("the Makefile has no real-conformance target, so this cannot tell which " +
			"tests the gate calls conformance")
	}
	for i, l := range lines[at:] {
		// A recipe is the tab-indented lines under the target. A comment at
		// column 0 sits inside one without ending it, and make reads it that
		// way, so this does too.
		if i > 0 && l != "" && !strings.HasPrefix(l, "\t") && !strings.HasPrefix(l, "#") {
			break
		}
		i := strings.Index(l, "-run ")
		if i < 0 {
			continue
		}
		rest := strings.TrimSpace(l[i+len("-run "):])
		if rest == "" {
			t.Fatal("a -run with nothing after it in the real-conformance recipe")
		}
		q := rest[0]
		if q != '\'' && q != '"' {
			t.Fatalf("the real-conformance recipe selects tests with an unquoted pattern "+
				"(%s), and this reads a quoted one", rest)
		}
		if end := strings.IndexByte(rest[1:], q); end >= 0 {
			return rest[1 : 1+end]
		}
		t.Fatal("an unterminated -run pattern in the real-conformance recipe")
	}
	t.Fatal("the real-conformance recipe no longer passes -run, so which tests the gate " +
		"calls conformance is decided somewhere this cannot see")
	return ""
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
