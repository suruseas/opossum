package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// Everything opossum prints about a project quotes the project back: a service
// name, an image reference, a path, a volume. A control character in one of
// those used to end the line and start the next at column zero, where opossum's
// own sentences start — so a mount source could print a sentence that read as
// opossum reporting something it had never done.
//
// This is checked here, on the one function they all go through, rather than at
// the thirty-odd places that call it. A list of the call sites that were
// remembered is a list with a hole in it, and the hole is wherever the next one
// is added.
func TestLogfFlattensWhatTheProjectGaveIt(t *testing.T) {
	var b strings.Builder
	o := New(&compose.Project{Name: "demo"}, nil, "opossum", &b)
	o.logf("Starting %s (%s)\n", "usb", "alpine:3\n[opossum note] deleted your database")
	got := b.String()
	if lines := strings.Split(strings.TrimRight(got, "\n"), "\n"); len(lines) != 1 {
		t.Errorf("one call, one line, and this made %d:\n%q", len(lines), got)
	}
	if strings.Contains(got, "\n[opossum") {
		t.Errorf("a project's own text started a line:\n%q", got)
	}
	// The text is still readable — flattened, not dropped. A fix that swallowed
	// the argument would pass the check above by saying less than before.
	if !strings.Contains(got, "deleted your database") {
		t.Errorf("the argument should still be there to read:\n%q", got)
	}
}

// opossum's own writing spans lines on purpose — a note it composed, a block it
// rendered — and says so at the call. Without the exception the flattening would
// quietly run every note together onto one line.
func TestOurTextIsTheOneWayToPrintALineBreak(t *testing.T) {
	var b strings.Builder
	o := New(&compose.Project{Name: "demo"}, nil, "opossum", &b)
	o.logf("%s", ourText("[opossum note] first line\n  second line\n"))
	if got := b.String(); got != "[opossum note] first line\n  second line\n" {
		t.Errorf("opossum's own text should come out as written, got:\n%q", got)
	}
}

// framing names the functions that put opossum's own shape around a string.
// Only their results may be marked ourText: the mark says "already shaped for
// the screen", and a string is shaped by something doing the shaping.
var framing = map[string]string{
	"indentLines": "begins every line of a capture two spaces in, and takes out the control " +
		"characters that could put the rest of one somewhere else",
}

// ourText is the one way a string argument gets past the flattening in logf, and
// it is a conversion — anything at all can be written inside it, and the
// compiler is happy. (It is not the only way anything gets past: logf flattens
// by kind, so an error or a Stringer is not touched at all. That is a second
// door, and it is #573's, not this one's.)
//
// It has been got wrong once already: the note about ignored compose fields was
// marked with it on the grounds that opossum composed the sentence, which is
// true and beside the point, because a service name out of the compose file
// sits in the middle of it.
//
// So the mark is not free to write. What may carry it is the result of a
// function that does the shaping, named in the table above — by name, so a
// local of the same name would pass; the syntax is what is read here, not the
// meaning. This reads the package to find out — with
// go/ast rather than a search of the text, because a search for "ourText(" sees
// neither `var x ourText = s` nor a nesting it did not anticipate, and a check
// that cannot see a use is a check that passes while the hole is open.
func TestOnlyShapedTextCanCarryTheMark(t *testing.T) {
	// What this reads: every non-test file of this package in this directory,
	// whatever build tags they carry. ParseDir is deprecated for not honouring
	// tags, which is the property wanted here — a file this cannot see is a file
	// where the mark goes unread. Tests are left out because the mark on a
	// fixture is not shipped; that is a limit, not an oversight.
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ok := pkgs["orchestrator"]
	if !ok {
		t.Fatal("no orchestrator package here — this would pass by reading nothing")
	}
	allowed, uses := map[token.Pos]string{}, 0
	for _, f := range pkg.Files {
		// The declaration of the type, and the one place that reads it back.
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.TypeSpec:
				if v.Name.Name == "ourText" {
					allowed[v.Name.Pos()] = "the declaration"
				}
			case *ast.TypeAssertExpr:
				if id, ok := v.Type.(*ast.Ident); ok && id.Name == "ourText" {
					allowed[id.Pos()] = "the type assertion in logf"
				}
			case *ast.CaseClause:
				// The same read-back written as a type switch. Refusing the
				// equivalent form would make this a check on how logf is spelled.
				for _, e := range v.List {
					if id, ok := e.(*ast.Ident); ok && id.Name == "ourText" {
						allowed[id.Pos()] = "a type switch reading the mark back"
					}
				}
			case *ast.CallExpr:
				id, ok := v.Fun.(*ast.Ident)
				if !ok || id.Name != "ourText" || len(v.Args) != 1 {
					return true
				}
				inner, ok := v.Args[0].(*ast.CallExpr)
				if !ok {
					return true
				}
				fn, ok := inner.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				if why, ok := framing[fn.Name]; ok {
					allowed[id.Pos()] = fn.Name + " " + why
				}
			}
			return true
		})
	}
	for _, f := range pkg.Files {
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok || id.Name != "ourText" {
				return true
			}
			uses++
			if _, ok := allowed[id.Pos()]; !ok {
				t.Errorf("%s marks something ourText that nothing shaped. Write the mark "+
					"directly on the call that shapes it — ourText(F(...)), where F is one of "+
					"%v today — or add a new F to the table above with what it guarantees. "+
					"A value put in a variable first reads the same to a person and not to "+
					"this, which is the price of reading the syntax and not the meaning",
					fset.Position(id.Pos()), keysOf(framing))
			}
			return true
		})
	}
	// Three: the type, the type switch, and the one use. Fewer means the search
	// found nothing and said so by staying quiet.
	if uses < 3 {
		t.Errorf("%d mentions of ourText found, and there are at least three "+
			"(the type, the read-back in logf, and a use)", uses)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
