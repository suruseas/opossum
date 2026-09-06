package runtime_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/doctor"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// The error-wording table says how far anyone actually checked, and this holds it
// to that.
//
// Each row's two verdict cells begin with one of three words: `raw:<file>` for a
// saved capture, `path-tried:<file>` for a route someone tried, `unverified` for
// one nobody did. For `raw:`, every wording the row lists has to appear in the
// capture it names.
//
// Every one of those was added after the version before it was shown to pass on
// something it should not have: a capture that showed nothing of the kind, prose
// in place of a verdict, one wording out of three matching by accident on a word
// as short as `MB`, a path climbing out of the capture directory to quote the
// table itself. The rule of thumb was that each version prevented citing nothing,
// not claiming more than was seen.
//
// What it still does not check, so that nobody reads more into a green run:
//
//   - Whether a capture is what the machine actually printed. Nothing signs them,
//     and they carry redactions already, so an edited one and a fabricated one
//     look the same from here.
//   - Whether a wording matched the line it was supposed to. A word as short as
//     `in use` appears in captures about other things, and any of them will do.
//   - What the row says about where the wording is matched, or which diagnostic
//     it belongs to. Those columns are read by people only.
//   - Whether a match written without a declaration got a row. The test below
//     binds the table to what each package *declares* it matches (its
//     UpstreamWordings), and a declared wording to a literal still taking part
//     in a match at its site — so a row cannot be thinned below what the code
//     declares, and a declaration cannot outlive its literal. A
//     `strings.Contains` against upstream text that nobody declared is still
//     invisible here, and so is a match written in a shape the test does not
//     read as a comparison (its doc lists the shapes).
//   - What `path-tried:` files show. Only that they exist.
//   - Whether a capture's matching line came from upstream or from opossum.
//     Dropping `#` and `$ ` lines removes recipes and typed commands, not the
//     warnings opossum printed into the capture itself, so a row can cite
//     opossum's own sentence as the upstream wording.
//   - Whether the file a row cites is a capture. `.txt` is a name, not a
//     property: hand-written lines sit inside these files too, and an index
//     saved under that extension would be citable like any other.
//   - What a narrowing note claims. Its shape is checked; the claim inside it is
//     not, so a note in that shape can widen what the verdict says.
//   - Whether a row was deleted along with the code it described. The count below
//     catches a bare deletion; a deletion with the count edited to match is a
//     deliberate act, and this is not a defence against those.
//
// The table's own prose counts the captures kept for the image-side signatures,
// which are recorded but deliberately not table rows (their upstream is an image,
// so what has to be written down is a version, and that shape is still being
// settled). A count written by hand went wrong twice in one afternoon, so it is
// read back from the directory here.
func TestTheCaptureCountsInTheProseAreTheOnesOnDisk(t *testing.T) {
	const table = "../../testdata/real-cli-output.md"
	const captures = "../../testdata/error-wordings"
	md, err := os.ReadFile(table)
	if err != nil {
		t.Fatal(err)
	}
	claim := regexp.MustCompile("`(pg[0-9]+)-\\*\\.txt` ([0-9]+)件")
	counted := map[string]string{}
	for _, m := range claim.FindAllStringSubmatch(string(md), -1) {
		counted[m[1]] = m[2]
	}
	// Every prefix on disk has to be counted, not just the ones still written in the
	// shape this looks for. Checking only what matched let one of the two be
	// reworded out of the pattern while the other kept the test green.
	all, err := filepath.Glob(filepath.Join(captures, "pg*-*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	onDisk := map[string]int{}
	prefix := regexp.MustCompile(`^(pg[0-9]+)-`)
	for _, f := range all {
		if m := prefix.FindStringSubmatch(filepath.Base(f)); m != nil {
			onDisk[m[1]]++
		}
	}
	if len(onDisk) == 0 {
		t.Fatal("no image-side captures on disk; delete this test or restore them")
	}
	// And the other way: a count for a prefix nothing on disk starts with is a
	// sentence about captures that do not exist.
	for p := range counted {
		if _, ok := onDisk[p]; !ok {
			t.Errorf("the table's prose counts captures starting with %q; the directory holds none", p+"-")
		}
	}
	for p, want := range onDisk {
		got, ok := counted[p]
		if !ok {
			t.Errorf("the directory holds %d captures starting with %q and the table's prose "+
				"counts none of them — a count that is not written cannot go stale, and cannot be read either",
				want, p+"-")
			continue
		}
		if got != strconv.Itoa(want) {
			t.Errorf("the prose says %s captures start with %q, the directory holds %d", got, p+"-", want)
		}
	}
}

func TestTheWordingTableSaysOnlyWhatWasChecked(t *testing.T) {
	const table = "../../testdata/real-cli-output.md"
	const captures = "../../testdata/error-wordings"
	// Every signature in the census has a row. Changing this number is how you say
	// the census changed; a row that goes missing on its own trips it.
	const wantRows = 12

	md, err := os.ReadFile(table)
	if err != nil {
		t.Fatal(err)
	}
	rows, malformed := wordingRows(string(md))
	for _, line := range malformed {
		// Dropped rows are the quiet failure this whole table is about: a stray `|`
		// in a cell would leave the row looking checked and checking nothing.
		t.Errorf("this row has the wrong number of cells, so it would be skipped: %s", line)
	}
	if len(rows) != wantRows {
		t.Fatalf("the table has %d rows, expected %d — a row that disappeared takes its "+
			"signature out of the census without anything else noticing", len(rows), wantRows)
	}
	quoted := regexp.MustCompile("`([^`]+)`")
	// A capture, not any file that happens to sit beside them. The index in that
	// directory quotes every wording in the table, so `raw:README.md` would let any
	// row cite it and pass — the same self-quoting that reaching `../` allowed.
	cite := regexp.MustCompile(`^(raw|path-tried):([^/\s（(]+\.txt)$`)
	// The note may only narrow, in one shape, so that a reader cannot be told the
	// opposite of what the verdict says.
	narrows := regexp.MustCompile(`^[^。]+のみ。[^。]+は unverified$`)
	for _, cells := range rows {
		// | # | 診断 | 場所 | 文言 | 経路 | 上流の文言 |
		wording, verdicts := cells[3], cells[4:]
		for i, cell := range verdicts {
			t.Run(cells[0]+"-"+[]string{"path", "upstream"}[i], func(t *testing.T) {
				norm := strings.NewReplacer("(", "（", ")", "）").Replace(cell)
				head, note, hasNote := strings.Cut(norm, "（")
				v := strings.TrimSpace(strings.Trim(head, "`* "))
				if hasNote {
					// The note may only narrow. Anything a reader could take as more
					// coverage has to be a row of its own, or it is a claim nobody
					// checked riding along with one they did.
					note = strings.TrimSuffix(strings.TrimSpace(note), "）")
					if !narrows.MatchString(note) {
						t.Fatalf("the note on %q has to say what is left out, in the form "+
							"`（X のみ。Y は unverified）`. A note that reads as more coverage "+
							"is the thing this column exists to stop: %q", v, note)
					}
				}
				if v == "unverified" {
					return
				}
				m := cite.FindStringSubmatch(v)
				if m == nil {
					t.Fatalf("verdict %q is not one of raw:<file>, path-tried:<file>, unverified "+
						"— and a file here names one in %s, nothing further away", cell, captures)
				}
				b, err := os.ReadFile(filepath.Join(captures, m[2]))
				if err != nil {
					t.Fatalf("%s names a capture that is not there: %v", v, err)
				}
				if m[1] != "raw" {
					return
				}
				// Only what the machine printed. The captures carry a header naming
				// the recipe and the command, written by hand — a wording matched
				// there would be this table quoting itself back.
				var printed []string
				for _, line := range strings.Split(string(b), "\n") {
					if t := strings.TrimSpace(line); strings.HasPrefix(t, "#") || strings.HasPrefix(t, "$ ") {
						continue
					}
					printed = append(printed, line)
				}
				body := strings.Join(printed, "\n")
				want := quoted.FindAllStringSubmatch(wording, -1)
				if len(want) == 0 {
					t.Fatalf("row lists no wording, so there is nothing to look for")
				}
				for _, w := range want {
					if !strings.Contains(body, w[1]) {
						t.Errorf("%s does not show %q — a capture cited for a wording it does "+
							"not contain is a claim with nothing behind it", v, w[1])
					}
				}
			})
		}
	}
}

// wordingRows returns the table's data rows split into cells, and separately any
// row it could not split. It finds the table by its header rather than by
// position, so moving the section does not leave this checking nothing.
func wordingRows(md string) (rows [][]string, malformed []string) {
	inTable := false
	for _, line := range strings.Split(md, "\n") {
		switch {
		case strings.HasPrefix(line, "| # | 診断 |"):
			inTable = true
		case inTable && !strings.HasPrefix(line, "|"):
			inTable = false
		case inTable && strings.HasPrefix(line, "|---"):
		case inTable:
			cells := strings.Split(strings.Trim(line, "|"), "|")
			if len(cells) != 6 {
				malformed = append(malformed, line)
				continue
			}
			for i := range cells {
				cells[i] = strings.TrimSpace(cells[i])
			}
			rows = append(rows, cells)
		}
	}
	return rows, malformed
}

// The table and the code, held to each other. Each package that matches
// upstream text declares what it matches (UpstreamWordings), next to the code;
// this reads those declarations and the table and requires:
//
//   - every declared wording is in a row whose "一致させている場所" names the
//     declaration's site, and every wording a row lists is declared for that
//     site — so a row thinned below what the code matches goes red, and so
//     does a declaration with no row;
//   - every declaration names a literal still taking part in a match in its
//     site — the function's body and the file-level var/const of its file —
//     so a declaration cannot outlive the match it describes: not through
//     opossum's own sentence about the failure, which repeats the wording,
//     and not through the same literal in a neighbouring function.
//
// Rows that say 同上 inherit the site of the row above; the site is the last
// backticked token of the row's "一致させている場所" cell. What this cannot see
// is a match nobody declared, and a match written in a shape the comparing
// contexts above do not cover (#483).
func TestTheTableAndTheCodeDeclareTheSameUpstreamWordings(t *testing.T) {
	md, err := os.ReadFile("../../testdata/real-cli-output.md")
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := wordingRows(string(md))
	backticked := regexp.MustCompile("`([^`]+)`")
	// table: site token -> set of wordings
	table := map[string]map[string]bool{}
	site := ""
	for _, row := range rows {
		if !strings.Contains(row[2], "同上") {
			site = ""
			for _, m := range backticked.FindAllStringSubmatch(row[2], -1) {
				// The last token names the function; a file-only site has one.
				site = m[1]
			}
		}
		if site == "" {
			t.Errorf("row %s names no site in backticks: %q", row[0], row[2])
			continue
		}
		if table[site] == nil {
			table[site] = map[string]bool{}
		}
		for _, m := range backticked.FindAllStringSubmatch(row[3], -1) {
			table[site][m[1]] = true
		}
	}

	// code: site token -> set of wordings, per package, with the literal check.
	declared := map[string]map[string]bool{}
	for _, pkg := range []struct {
		dir   string
		words []runtime.UpstreamWording
	}{
		{"../runtime", runtime.UpstreamWordings},
		{"../orchestrator", orchestrator.UpstreamWordings},
		{"../doctor", doctor.UpstreamWordings},
	} {
		for _, w := range pkg.words {
			if declared[w.Site] == nil {
				declared[w.Site] = map[string]bool{}
			}
			declared[w.Site][w.Wording] = true
			lits := matchLiteralsAt(t, pkg.dir, w.Site)
			if len(w.InCode) == 0 {
				if !lits.exact(w.Wording) {
					t.Errorf("%s declares %q for %s, but no literal taking part in a match in %s is that wording — the match is gone, moved to another function, widened, or is only printed",
						pkg.dir, w.Wording, w.Site, w.Site)
				}
				continue
			}
			for _, lit := range w.InCode {
				if !lits.carries(lit) {
					t.Errorf("%s declares %q for %s as carried by %q, but no literal taking part in a match in %s carries it",
						pkg.dir, w.Wording, w.Site, lit, w.Site)
				}
			}
		}
	}

	for site, words := range table {
		for w := range words {
			if !declared[site][w] {
				t.Errorf("the table's row for %s lists %q, and no package declares matching it — a row that says more than the code does", site, w)
			}
		}
	}
	for site, words := range declared {
		for w := range words {
			if !table[site][w] {
				t.Errorf("%s is declared to match %q, and no row for that site lists it — add the row, with the capture that shows the wording", site, w)
			}
		}
	}
}

// matchLiteralsAt returns the string literals that take part in a match at
// site in dir. Site is a function name or a file name. For a function, what is
// read is its body plus the file-level var and const declarations of the file
// that holds it — a regexp compiled at file scope is that function's match —
// and nothing of the other functions in the file: a literal in a neighbouring
// function used to stand in for one that had gone, and did, three times out
// of sixteen. For a file name, the whole file.
//
// A literal takes part in a match when it is an argument to a comparing call
// of the strings or regexp package (Contains, HasPrefix, HasSuffix, EqualFold,
// Index, MustCompile, Compile, Match, MatchString), an operand of == or !=, a
// switch case, or an element of a []string composite literal (a list of
// signatures walked by a loop — the one context in which a list of sentences
// written for the user would pass as well, so the prose says so). A literal in
// a message printed to the user does not count. Literals come back as their
// source text, so a regexp's backslashes read as written in the declaration.
func matchLiteralsAt(t *testing.T, dir, site string) literalSet {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var roots []ast.Node
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			if strings.HasSuffix(site, ".go") && filepath.Base(name) == site {
				roots = append(roots, f)
				continue
			}
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == site && fd.Body != nil {
					roots = append(roots, fd.Body)
					for _, d2 := range f.Decls {
						if gd, ok := d2.(*ast.GenDecl); ok && (gd.Tok == token.VAR || gd.Tok == token.CONST) {
							roots = append(roots, gd)
						}
					}
				}
			}
		}
	}
	if len(roots) == 0 {
		t.Errorf("%s: no file holds a function or file named %q, so its declaration binds to nothing", dir, site)
		return nil
	}
	comparing := map[string]bool{"Contains": true, "HasPrefix": true, "HasSuffix": true, "EqualFold": true,
		"Index": true, "MustCompile": true, "Compile": true, "Match": true, "MatchString": true}
	var lits []string
	add := func(e ast.Expr) {
		if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			lits = append(lits, lit.Value)
		}
	}
	for _, root := range roots {
		ast.Inspect(root, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok && comparing[sel.Sel.Name] {
					if pkg, ok := sel.X.(*ast.Ident); ok && (pkg.Name == "strings" || pkg.Name == "regexp") {
						for _, a := range x.Args {
							add(a)
						}
					}
				}
			case *ast.BinaryExpr:
				if x.Op == token.EQL || x.Op == token.NEQ {
					add(x.X)
					add(x.Y)
				}
			case *ast.CaseClause:
				for _, e := range x.List {
					add(e)
				}
			case *ast.CompositeLit:
				if at, ok := x.Type.(*ast.ArrayType); ok {
					if id, ok := at.Elt.(*ast.Ident); ok && id.Name == "string" {
						for _, e := range x.Elts {
							add(e)
						}
					}
				}
			}
			return true
		})
	}
	return literalSet(lits)
}

// literalSet is the source text of the literals found at a site.
type literalSet []string

// exact says whether some literal's value is the wording, whole: a wording
// declared without InCode is the literal, and a literal that grew a suffix no
// longer matches what upstream prints even though it still contains the words.
func (ls literalSet) exact(wording string) bool {
	for _, l := range ls {
		if v, err := strconv.Unquote(l); err == nil && v == wording {
			return true
		}
	}
	return false
}

// carries says whether some literal's source text contains the text — for
// InCode, which names a piece of a regexp or a token as it is written.
func (ls literalSet) carries(text string) bool {
	for _, l := range ls {
		if strings.Contains(l, text) {
			return true
		}
	}
	return false
}
