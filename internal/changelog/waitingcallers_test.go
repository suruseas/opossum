package changelog_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The checks that read the fragments waiting in this repository read them through
// changelog.Waiting, which is what tells "no fragments" from "the wrong place":
// Load on a directory that does not exist answers nothing, and a check with
// nothing in front of it passes. These are those checks, by file and function.
var waitingReaders = []struct{ file, fn string }{
	{"internal/changelog/markup_test.go", "TestFragmentsRenderAsWritten"},
	{"internal/changelog/published_test.go", "waitingEntriesInPublishedSections"},
	{"internal/changelog/ratchet_test.go", "TestEveryFragmentWaitingToShipSurvivesTheRoundTrip"},
	{"internal/changelog/ratchet_test.go", "TestFragmentsAreWellFormed"},
	{"internal/claimcite/claimcite_test.go", "TestEveryChangelogFragmentNamesItsRuns"},
}

// waitingSelf asks Waiting about states it builds and checks each error itself,
// row by row; it is the one caller that does not stop on the error.
const waitingSelf = "TestWaitingTellsNoFragmentsFromTheWrongPlace"

// Each reader above calls Waiting and not Load, and every call to Waiting in the
// repository's tests stops the test on its error. An error from Waiting comes
// back with no fragments, so a call site that only logs it carries on into the
// healthy "nothing waiting" skip, and the wrong place reads as an empty one.
func TestTheFragmentChecksStopWhenWaitingSaysTheWrongPlace(t *testing.T) {
	root := repoRoot(t)
	calls := waitingCalls(t, root)

	t.Run("readers", func(t *testing.T) {
		for _, r := range waitingReaders {
			if p := readerProblem(t, filepath.Join(root, r.file), r.fn); p != "" {
				t.Errorf("%s: %s", r.file, p)
			}
		}
	})

	t.Run("every call stops on its error", func(t *testing.T) {
		// Each reader's own call has to be among those found, or the walker is
		// not reading the tree and every call passes by not being seen.
		for _, r := range waitingReaders {
			found := false
			for _, c := range calls {
				found = found || (c.fn == r.fn && strings.HasPrefix(c.where, r.file+":"))
			}
			if !found {
				t.Errorf("%s: %s's call to changelog.Waiting was not found by the walker", r.file, r.fn)
			}
		}
		skipped := 0
		for _, c := range calls {
			if c.fn == waitingSelf {
				skipped++
				continue
			}
			if c.problem != "" {
				t.Errorf("%s: %s: %s", c.where, c.fn, c.problem)
			}
		}
		if skipped != 1 {
			t.Errorf("%d calls were let through as %s's own, want its one", skipped, waitingSelf)
		}
	})
}

type waitingCall struct {
	where, fn, problem string
}

// waitingCalls finds every call to changelog.Waiting in the repository's test
// files and says, for each, what is wrong with how its error is handled ("" for
// nothing).
func waitingCalls(t *testing.T, root string) []waitingCall {
	t.Helper()
	var out []waitingCall
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		before := len(out)
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				blk, ok := n.(*ast.BlockStmt)
				if !ok {
					return true
				}
				for i, st := range blk.List {
					call, errName, cond, body, ok := waitingAt(st)
					if !ok {
						continue
					}
					c := waitingCall{where: rel + ":" + strconv.Itoa(fset.Position(call.Pos()).Line), fn: fd.Name.Name}
					if cond == nil && i+1 < len(blk.List) {
						if next, ok := blk.List[i+1].(*ast.IfStmt); ok && next.Init == nil {
							cond, body = next.Cond, next
						}
					}
					c.problem = stopsOn(errName, cond, body)
					out = append(out, c)
				}
				return true
			})
			// A call in a shape this does not read — inside a closure's return,
			// assigned with =, passed straight to another call — is not checked
			// above, so it is reported rather than let through.
			if n := countCalls(fd, "Waiting"); n != len(out)-before {
				out = append(out, waitingCall{where: rel, fn: fd.Name.Name,
					problem: "calls changelog.Waiting in a shape this check does not read; assign it with := and ask err != nil on the next line"})
			}
			before = len(out)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].where < out[j].where })
	return out
}

// waitingAt reports whether st assigns the result of changelog.Waiting, either
// as a statement of its own or as the init of an if; for the if, it hands back
// its condition and the if itself.
func waitingAt(st ast.Stmt) (call *ast.CallExpr, errName string, cond ast.Expr, ifs *ast.IfStmt, ok bool) {
	as, ok := st.(*ast.AssignStmt)
	if is, isIf := st.(*ast.IfStmt); isIf {
		as, ok = is.Init.(*ast.AssignStmt)
		ifs = is
	}
	if !ok || len(as.Rhs) != 1 || len(as.Lhs) != 2 {
		return nil, "", nil, nil, false
	}
	call, ok = as.Rhs[0].(*ast.CallExpr)
	if !ok || !isChangelogCall(call, "Waiting") {
		return nil, "", nil, nil, false
	}
	if id, isID := as.Lhs[1].(*ast.Ident); isID {
		errName = id.Name
	}
	if ifs != nil {
		cond = ifs.Cond
	}
	return call, errName, cond, ifs, true
}

// stopsOn says what is wrong with the if that follows a call to Waiting: it has
// to ask `<err> != nil` of the error the call returned, have no else, and stop the
// test as the first thing it does.
func stopsOn(errName string, cond ast.Expr, ifs *ast.IfStmt) string {
	switch {
	case errName == "" || errName == "_":
		return "the error is not kept"
	case ifs == nil || cond == nil:
		return "the error is not asked about straight after the call"
	}
	be, ok := cond.(*ast.BinaryExpr)
	if !ok || be.Op != token.NEQ || !isIdent(be.X, errName) || !isIdent(be.Y, "nil") {
		return "the if after the call does not ask " + errName + " != nil"
	}
	if ifs.Else != nil {
		return "the if on the error has an else"
	}
	if len(ifs.Body.List) == 0 {
		return "the if on the error does nothing"
	}
	es, ok := ifs.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return "the if on the error does not stop the test first"
	}
	call, ok := es.X.(*ast.CallExpr)
	if !ok {
		return "the if on the error does not stop the test first"
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !isIdent(sel.X, "t") || (sel.Sel.Name != "Fatal" && sel.Sel.Name != "Fatalf") {
		return "the if on the error does not stop the test with t.Fatal or t.Fatalf"
	}
	return ""
}

func isChangelogCall(call *ast.CallExpr, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && isIdent(sel.X, "changelog") && sel.Sel.Name == name
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func countCalls(fd *ast.FuncDecl, name string) int {
	n := 0
	ast.Inspect(fd, func(x ast.Node) bool {
		if call, ok := x.(*ast.CallExpr); ok && isChangelogCall(call, name) {
			n++
		}
		return true
	})
	return n
}

// readerProblem says what is wrong with fn in the file at path as one of the
// readers: it has to be there, call Waiting once, and not call Load at all.
func readerProblem(t *testing.T, path, fn string) string {
	t.Helper()
	fd := funcDecl(t, path, fn)
	switch {
	case fd == nil:
		return "no function " + fn
	case countCalls(fd, "Load") != 0:
		return fn + " calls changelog.Load, which answers nothing for a directory that is not there"
	case countCalls(fd, "Waiting") != 1:
		return fn + " calls changelog.Waiting " + strconv.Itoa(countCalls(fd, "Waiting")) + " times, want once"
	}
	return ""
}

func funcDecl(t *testing.T, path, name string) *ast.FuncDecl {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range f.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == name {
			return fd
		}
	}
	return nil
}

// The check above reads the repository as it is, where every call is right, so on
// its own it cannot show it would see a wrong one. Each row here is a call site
// written one way, read by the same walker.
func TestTheWaitingCheckSeesEachWayACallSiteGoesWrong(t *testing.T) {
	const call = `frags, err := changelog.Waiting("CHANGELOG.md", "changelog.d")`
	for _, tc := range []struct {
		name, body, want string // want: "" for no problem, else part of the problem
	}{
		{"stops on the error", call + "\nif err != nil {\nt.Fatal(err)\n}\n_ = frags", ""},
		{"stops with Fatalf", call + "\nif err != nil {\nt.Fatalf(\"%v\", err)\n}\n_ = frags", ""},
		{"in the if's init", "if _, err := changelog.Waiting(\"a\", \"b\"); err != nil {\nt.Fatal(err)\n}", ""},
		{"only logs it", call + "\nif err != nil {\nt.Log(err)\n}\n_ = frags", "does not stop the test with t.Fatal"},
		{"logs it first", call + "\nif err != nil {\nt.Log(err)\nt.Fatal(err)\n}\n_ = frags", "does not stop the test with t.Fatal"},
		{"a statement between", call + "\n_ = frags\nif err != nil {\nt.Fatal(err)\n}", "not asked about straight after"},
		{"not asked at all", call + "\n_, _ = frags, err", "not asked about straight after"},
		{"the error dropped", `frags, _ := changelog.Waiting("a", "b")` + "\n_ = frags", "not kept"},
		{"another value asked", call + "\nif frags != nil {\nt.Fatal(err)\n}", "does not ask err != nil"},
		{"asked the other way", call + "\nif err == nil {\n} else {\nt.Fatal(err)\n}\n_ = frags", "does not ask err != nil"},
		{"with an else", call + "\nif err != nil {\nt.Fatal(err)\n} else {\n_ = frags\n}", "has an else"},
		{"an empty if", call + "\nif err != nil {\n}\n_ = frags", "does nothing"},
		{"another t's Fatal", "var u *testing.T\n" + call + "\nif err != nil {\nu.Fatal(err)\n}\n_ = frags", "does not stop the test with t.Fatal"},
		{"a shape it does not read", "t.Log(changelog.Waiting(\"a\", \"b\"))", "shape this check does not read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			src := "package x_test\n\nimport (\n\"testing\"\n\n\"github.com/suruseas/opossum/internal/changelog\"\n)\n\nfunc TestX(t *testing.T) {\n" + tc.body + "\n}\n"
			if err := os.WriteFile(filepath.Join(root, "x_test.go"), []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
			calls := waitingCalls(t, root)
			var problems []string
			for _, c := range calls {
				if c.problem != "" {
					problems = append(problems, c.problem)
				}
			}
			switch {
			case len(calls) == 0:
				t.Fatalf("the call was not found at all")
			case tc.want == "" && len(problems) != 0:
				t.Errorf("want no problem, got %q", problems)
			case tc.want != "" && (len(problems) != 1 || !strings.Contains(problems[0], tc.want)):
				t.Errorf("want one problem saying %q, got %q", tc.want, problems)
			}
		})
	}
}

// What the readers check says about a function, one row per thing it asks.
func TestTheReadersCheckSeesAReaderGoneWrong(t *testing.T) {
	const w = `changelog.Waiting("CHANGELOG.md", "changelog.d")`
	const l = `changelog.Load("changelog.d")`
	for _, tc := range []struct{ name, fn, body, want string }{
		{"Waiting once", "TestR", "_, _ = " + w, ""},
		{"Load instead", "TestR", "_, _ = " + l, "calls changelog.Load"},
		{"Load beside Waiting", "TestR", "_, _ = " + w + "\n_, _ = " + l, "calls changelog.Load"},
		{"Waiting twice", "TestR", "_, _ = " + w + "\n_, _ = " + w, "2 times, want once"},
		{"neither", "TestR", "", "0 times, want once"},
		{"another function", "TestOther", "_, _ = " + w, "no function TestR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "x_test.go")
			src := "package x_test\n\nimport (\n\"testing\"\n\n\"github.com/suruseas/opossum/internal/changelog\"\n)\n\nfunc " + tc.fn + "(t *testing.T) {\n" + tc.body + "\n}\n"
			if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
			got := readerProblem(t, path, "TestR")
			switch {
			case tc.want == "" && got != "":
				t.Errorf("want no problem, got %q", got)
			case tc.want != "" && !strings.Contains(got, tc.want):
				t.Errorf("want a problem saying %q, got %q", tc.want, got)
			}
		})
	}
}
