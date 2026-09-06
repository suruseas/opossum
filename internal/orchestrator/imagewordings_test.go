package orchestrator_test

// The image-side wordings (#480): each declaration in ImageWordings is held
// to the code (the literal is still at the site), to a capture (the file
// carries the wording, and its header names the image and the date), and
// to the table in testdata/real-cli-output.md (every declared wording is a
// row, every row is declared) — the same three-way hold the runtime-side
// table has, for the strings whose upstream is an image.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/orchestrator"
)

func TestEveryImageWordingIsInTheCodeAndShownByADatedCapture(t *testing.T) {
	if len(orchestrator.ImageWordings) == 0 {
		t.Fatal("no image wordings declared; the image-side signatures (initdb, chown) must be here")
	}
	date := regexp.MustCompile(`20[0-9]{2}-[0-9]{2}-[0-9]{2}`)
	for _, w := range orchestrator.ImageWordings {
		t.Run(w.Site+"/"+w.Image+"/"+w.Wording, func(t *testing.T) {
			lits := literalsAt(t, ".", w.Site)
			if len(w.InCode) == 0 {
				if !lits[w.Wording] {
					t.Errorf("no literal taking part in a match at %s is %q — the match is gone, moved, or widened", w.Site, w.Wording)
				}
			} else {
				for _, piece := range w.InCode {
					found := false
					for lit := range lits {
						if strings.Contains(lit, piece) {
							found = true
						}
					}
					if !found {
						t.Errorf("no literal taking part in a match at %s carries %q", w.Site, piece)
					}
				}
			}
			body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "error-wordings", w.Capture))
			if err != nil {
				t.Fatalf("the capture %s is not there: %v", w.Capture, err)
			}
			text := string(body)
			if !strings.Contains(text, w.Wording) {
				t.Errorf("the capture %s does not carry %q — the image changed its words, or the wrong capture is cited", w.Capture, w.Wording)
			}
			header := ""
			for _, line := range strings.Split(text, "\n") {
				if !strings.HasPrefix(line, "#") {
					break
				}
				header += line + "\n"
			}
			if !strings.Contains(header, w.Image) {
				t.Errorf("the capture %s's header does not name the image %q — say which image, with its tag", w.Capture, w.Image)
			}
			if !date.MatchString(header) {
				t.Errorf("the capture %s's header carries no date — the day the wording was last seen is the point", w.Capture)
			}
		})
	}
}

func TestTheImageWordingTableAndTheCodeAgree(t *testing.T) {
	md, err := os.ReadFile("../../testdata/real-cli-output.md")
	if err != nil {
		t.Fatal(err)
	}
	// The table lives under its own heading; rows are `| site | wording | image | capture | date |`.
	text := string(md)
	start := strings.Index(text, "## image 由来の文言")
	if start < 0 {
		t.Fatal("testdata/real-cli-output.md has no 「image 由来の文言」 section")
	}
	section := text[start:]
	if next := strings.Index(section[1:], "\n## "); next >= 0 {
		section = section[:next+1]
	}
	backticked := regexp.MustCompile("`([^`]+)`")
	table := map[string]bool{}
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "|---") || strings.Contains(line, "一致させている場所") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 6 {
			continue
		}
		site, wording, capture := "", "", ""
		if m := backticked.FindStringSubmatch(cells[1]); m != nil {
			site = m[1]
		}
		if m := backticked.FindStringSubmatch(cells[2]); m != nil {
			wording = m[1]
		}
		if m := backticked.FindStringSubmatch(cells[4]); m != nil {
			capture = m[1]
		}
		table[site+"|"+wording+"|"+capture] = true
	}
	declared := map[string]bool{}
	for _, w := range orchestrator.ImageWordings {
		key := w.Site + "|" + w.Wording + "|" + w.Capture
		declared[key] = true
		if !table[key] {
			t.Errorf("declared but not in the table: %s matches %q shown by %s — add the row", w.Site, w.Wording, w.Capture)
		}
	}
	for key := range table {
		if !declared[key] {
			t.Errorf("in the table but not declared: %s — a row that says more than the code does", key)
		}
	}
}

// literalsAt returns the string literals that take part in a match at site
// (a function name, or a file name) in dir: arguments to strings/regexp
// comparing calls, operands of ==/!=, switch cases, and file-level regexp
// declarations of the function's file.
func literalsAt(t *testing.T, dir, site string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	var roots []ast.Node
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			if filepath.Base(name) == site {
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
		t.Fatalf("%s: no function or file named %q", dir, site)
	}
	comparing := map[string]bool{"Contains": true, "HasPrefix": true, "HasSuffix": true, "EqualFold": true, "Index": true, "MustCompile": true, "Compile": true, "Match": true, "MatchString": true}
	out := map[string]bool{}
	add := func(e ast.Expr) {
		if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			v := lit.Value
			if len(v) >= 2 {
				v = v[1 : len(v)-1]
			}
			out[v] = true
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
			}
			return true
		})
	}
	return out
}
