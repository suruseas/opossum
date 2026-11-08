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
	{"internal/changelog/ratchet_test.go", "TestUnreleasedMatchesFragments"},
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
		// The list above is the one found in the tree, so a check that reads the
		// fragments and is left off it, or taken off it, is seen.
		listed := map[string]bool{}
		for _, r := range waitingReaders {
			listed[r.file+" "+r.fn] = true
		}
		found := map[string]bool{}
		for _, r := range fragmentReaders(t, root) {
			found[r] = true
			if !listed[r] {
				t.Errorf("%s reads changelog.d and is not among the readers this check holds", r)
			}
		}
		for r := range listed {
			if !found[r] {
				t.Errorf("%s is listed as reading changelog.d and was not found reading it", r)
			}
		}
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
//
// And what it read is what it checks: the fragments Waiting returned are not
// assigned over afterwards, and no function it calls in its own package reads
// changelog.d again.
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
	if name := waitingResult(fd); name != "" && name != "_" {
		if assignedAgain(fd, name) {
			return fn + " assigns over " + name + ", the fragments Waiting returned, after reading them"
		}
	}
	pkg := packageFuncs(t, filepath.Dir(path))
	if helper := handsOff(fd, pkg); helper != "" {
		return fn + " hands changelog.d to " + helper + ", where only changelog.Waiting may read it"
	}
	seen := map[string]bool{fn: true}
	queue := calledIn(fd, pkg)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		if readsChangelogDir(pkg[name]) {
			return fn + " reads changelog.d again in " + name
		}
		queue = append(queue, calledIn(pkg[name], pkg)...)
	}
	return ""
}

// waitingResult is the name the fragments from fd's call to Waiting are put in.
func waitingResult(fd *ast.FuncDecl) string {
	name := ""
	ast.Inspect(fd, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok && len(as.Rhs) == 1 && len(as.Lhs) == 2 {
			if call, ok := as.Rhs[0].(*ast.CallExpr); ok && isChangelogCall(call, "Waiting") {
				if id, ok := as.Lhs[0].(*ast.Ident); ok {
					name = id.Name
				}
			}
		}
		return true
	})
	return name
}

// assignedAgain reports whether name is assigned anywhere in fd other than by
// the call to Waiting.
func assignedAgain(fd *ast.FuncDecl, name string) bool {
	again := false
	ast.Inspect(fd, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		if len(as.Rhs) == 1 {
			if call, ok := as.Rhs[0].(*ast.CallExpr); ok && isChangelogCall(call, "Waiting") {
				return true
			}
		}
		for _, l := range as.Lhs {
			if isIdent(l, name) {
				again = true
			}
		}
		return true
	})
	return again
}

// packageFuncs is every function declared in the test files of dir, by name.
func packageFuncs(t *testing.T, dir string) map[string]*ast.FuncDecl {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*ast.FuncDecl{}
	for _, p := range paths {
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Body != nil {
				out[fd.Name.Name] = fd
			}
		}
	}
	return out
}

// calledIn is the functions of pkg that fd calls by name.
func calledIn(fd *ast.FuncDecl, pkg map[string]*ast.FuncDecl) []string {
	var out []string
	ast.Inspect(fd, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && pkg[id.Name] != nil {
				out = append(out, id.Name)
			}
		}
		return true
	})
	return out
}

// readsChangelogDir reports whether fd calls changelog.Load or Waiting with an
// argument that names changelog.d.
func readsChangelogDir(fd *ast.FuncDecl) bool {
	reads := false
	ast.Inspect(fd, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !(isChangelogCall(call, "Load") || isChangelogCall(call, "Waiting")) {
			return true
		}
		for _, a := range call.Args {
			if namesChangelogDir(a) {
				reads = true
			}
		}
		return true
	})
	return reads
}

func namesChangelogDir(n ast.Node) bool {
	names := false
	ast.Inspect(n, func(x ast.Node) bool {
		if b, ok := x.(*ast.BasicLit); ok && b.Kind == token.STRING && strings.Contains(b.Value, "changelog.d") {
			names = true
		}
		return true
	})
	return names
}

// carriesChangelogDir reports whether the expression names changelog.d or uses
// a variable of fd that was given a value naming it.
func carriesChangelogDir(e ast.Expr, tainted map[string]bool) bool {
	carries := namesChangelogDir(e)
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && tainted[id.Name] {
			carries = true
		}
		return true
	})
	return carries
}

// taintedNames is the variables of fd given a value that names changelog.d,
// other than what a call to changelog.Load or Waiting returns (fragments, not
// a place).
func taintedNames(fd *ast.FuncDecl) map[string]bool {
	tainted := map[string]bool{}
	ast.Inspect(fd, func(n ast.Node) bool {
		var lhs []ast.Expr
		var rhs []ast.Expr
		switch x := n.(type) {
		case *ast.AssignStmt:
			lhs, rhs = x.Lhs, x.Rhs
		case *ast.ValueSpec:
			for _, id := range x.Names {
				lhs = append(lhs, id)
			}
			rhs = x.Values
		default:
			return true
		}
		for _, r := range rhs {
			if call, ok := r.(*ast.CallExpr); ok && (isChangelogCall(call, "Load") || isChangelogCall(call, "Waiting")) {
				return true
			}
		}
		for _, r := range rhs {
			if namesChangelogDir(r) {
				for _, l := range lhs {
					if id, ok := l.(*ast.Ident); ok {
						tainted[id.Name] = true
					}
				}
			}
		}
		return true
	})
	return tainted
}

// handsOff is the first function of pkg that fd calls with an argument
// carrying changelog.d, or "".
func handsOff(fd *ast.FuncDecl, pkg map[string]*ast.FuncDecl) string {
	tainted := taintedNames(fd)
	found := ""
	ast.Inspect(fd, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || found != "" {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok || pkg[id.Name] == nil || id.Name == fd.Name.Name {
			return true
		}
		for _, a := range call.Args {
			if carriesChangelogDir(a, tainted) {
				found = id.Name
			}
		}
		return true
	})
	return found
}

// fragmentReaders finds, in the repository's test files, the functions that
// read the fragments waiting here: each names changelog.d, builds no directory
// of its own with t.TempDir, and either calls changelog.Load or Waiting or hands
// changelog.d to a function of its own package. Each is given as
// "<file> <function>".
func fragmentReaders(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	pkgs := map[string]map[string]*ast.FuncDecl{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		pkg := pkgs[filepath.Dir(path)]
		if pkg == nil {
			pkg = packageFuncs(t, filepath.Dir(path))
			pkgs[filepath.Dir(path)] = pkg
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			tempDir := false
			ast.Inspect(fd, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "TempDir" {
					tempDir = true
				}
				return true
			})
			if !tempDir && namesChangelogDir(fd.Body) && (countCalls(fd, "Load")+countCalls(fd, "Waiting") > 0 || handsOff(fd, pkg) != "") {
				out = append(out, filepath.ToSlash(rel)+" "+fd.Name.Name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
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
	const read = "frags, err := " + w + "\nif err != nil {\nt.Fatal(err)\n}\n"
	for _, tc := range []struct{ name, fn, body, extra, want string }{
		{"Waiting once", "TestR", "_, _ = " + w, "", ""},
		{"Load instead", "TestR", "_, _ = " + l, "", "calls changelog.Load"},
		{"Load beside Waiting", "TestR", "_, _ = " + w + "\n_, _ = " + l, "", "calls changelog.Load"},
		{"Waiting twice", "TestR", "_, _ = " + w + "\n_, _ = " + w, "", "2 times, want once"},
		{"neither", "TestR", "", "", "0 times, want once"},
		{"another function", "TestOther", "_, _ = " + w, "", "no function TestR"},
		{"what was read used", "TestR", read + "_ = frags", "", ""},
		{"only the error kept", "TestR", "_, err := " + w + "\n_ = err", "", ""},
		{"what was read assigned over", "TestR", read + "frags = frags[:0]\n_ = frags", "", "assigns over frags"},
		{"read again in a helper", "TestR", read + "_ = frags\nhelper()", "func helper() { _, _ = " + l + " }", "reads changelog.d again in helper"},
		{"read again two helpers down", "TestR", read + "_ = frags\nhelper()", "func helper() { helper2() }\nfunc helper2() { _, _ = changelog.Load(\"changelog.dd\") }", "reads changelog.d again in helper2"},
		{"a helper that reads somewhere of its own", "TestR", read + "_ = frags\nhelper(\"x\")", "func helper(dir string) { _, _ = changelog.Load(dir) }", ""},
		{"what was read handed to a helper", "TestR", read + "helper(frags)", "func helper(f []changelog.Fragment) { _ = f }", ""},
		{"changelog.d handed to a helper", "TestR", read + "_ = frags\nhelper(\"changelog.dd\")", "func helper(dir string) { _, _ = changelog.Load(dir) }", "hands changelog.d to helper"},
		{"a variable naming it handed to a helper", "TestR", "dir := \"changelog.d\"\n" + read + "_ = frags\nhelper(dir)", "func helper(dir string) { _ = dir }", "hands changelog.d to helper"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "x_test.go")
			src := "package x_test\n\nimport (\n\"testing\"\n\n\"github.com/suruseas/opossum/internal/changelog\"\n)\n\nfunc " + tc.fn + "(t *testing.T) {\n" + tc.body + "\n}\n" + tc.extra + "\n"
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

// What the search for readers finds, over a file with one function of each kind.
func TestFragmentReadersAreFound(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package a_test

import (
	"testing"

	"github.com/suruseas/opossum/internal/changelog"
)

func TestWaits(t *testing.T) { _, _ = changelog.Waiting("CHANGELOG.md", "changelog.d") }
func TestLoads(t *testing.T) { _, _ = changelog.Load("changelog.d") }
func helperLoads(root string) { _, _ = changelog.Load(root + "/changelog.d") }
func TestOwnDirectory(t *testing.T) { _, _ = changelog.Load(t.TempDir() + "/changelog.d") }
func TestNoName(t *testing.T) { _, _ = changelog.Load("fragments") }
func TestNameOnly(t *testing.T) { _ = "changelog.d" }
func TestThroughAHelper(t *testing.T) { _ = mustLoad("changelog.dd") }
func mustLoad(dir string) []changelog.Fragment { f, _ := changelog.Load(dir); return f }
func TestHandsSomethingElse(t *testing.T) { _ = "changelog.d"; describe("x") }
func describe(s string) {}
`
	if err := os.WriteFile(filepath.Join(root, "a", "x_test.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(fragmentReaders(t, root), "\n")
	want := "a/x_test.go TestWaits\na/x_test.go TestLoads\na/x_test.go helperLoads\na/x_test.go TestThroughAHelper"
	if got != want {
		t.Errorf("found:\n%s\nwant:\n%s", got, want)
	}
}
