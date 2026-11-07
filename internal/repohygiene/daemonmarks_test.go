package repohygiene_test

import (
	"go/ast"
	"go/token"
	"strconv"
	"testing"
)

// The daemon measurement (daemonTest) is what the sentences in AGENTS.md and
// docs/compatibility.md about reaching a Docker daemon rest on, and it only
// runs on a machine with the real runtime and a daemon, so a change that stops
// it deciding anything is green everywhere else. This reads its source and
// holds the three things it decides on, one subtest each:
//
//   - the marks: daemonMarksVar is a list of string literals that holds the
//     Engine API's own header, which a plain HTTP server does not write;
//   - the loop: the test ranges over that list by name, as one of its own
//     statements with no return ahead of it, and for each mark asks whether the
//     output of the run it made lacks it, failing the test when it does;
//   - the path: what the run binds is what dockerDaemonSocket returns, and
//     that is what filepath.EvalSymlinks made of daemonSocketConst.
const (
	daemonTest       = "TestARealDockerDaemonAnswersThroughABoundSocket"
	daemonMarksVar   = "daemonMarks"
	daemonSocketFunc = "dockerDaemonSocket"
	engineAPIMark    = "Api-Version:"
)

func TestTheDaemonMeasurementDecidesOnWhatItSays(t *testing.T) {
	suite := conformanceReach(t)
	body := func(name string) *ast.BlockStmt {
		t.Helper()
		c, ok := suite.bodies[name]
		if !ok {
			t.Fatalf("no %s in the conformance suite", name)
		}
		switch n := c.node.(type) {
		case *ast.FuncDecl:
			return n.Body
		case *ast.FuncLit:
			return n.Body
		}
		t.Fatalf("%s is not a function with a body", name)
		return nil
	}

	t.Run("the marks hold the Engine API's header", func(t *testing.T) {
		var marks []string
		found := false
		for _, f := range suite.files {
			ast.Inspect(f, func(n ast.Node) bool {
				vs, ok := n.(*ast.ValueSpec)
				if !ok {
					return true
				}
				for i, name := range vs.Names {
					if name.Name != daemonMarksVar || i >= len(vs.Values) {
						continue
					}
					found = true
					lit, ok := vs.Values[i].(*ast.CompositeLit)
					if !ok {
						t.Fatalf("%s is not written as a list of strings", daemonMarksVar)
					}
					for _, e := range lit.Elts {
						b, ok := e.(*ast.BasicLit)
						if !ok || b.Kind != token.STRING {
							t.Fatalf("%s holds something other than a string literal", daemonMarksVar)
						}
						s, _ := strconv.Unquote(b.Value)
						marks = append(marks, s)
					}
				}
				return true
			})
		}
		if !found {
			t.Fatalf("no var %s in the conformance suite", daemonMarksVar)
		}
		has := false
		for _, m := range marks {
			if m == engineAPIMark {
				has = true
			}
		}
		if !has {
			t.Errorf("%s is %q: without %q, any HTTP server on that path passes for a daemon", daemonMarksVar, marks, engineAPIMark)
		}
	})

	t.Run("every mark is asked of the run's output and can fail the test", func(t *testing.T) {
		b := body(daemonTest)
		outFromRun := assignedFromCall(b, "run")
		var loop *ast.RangeStmt
		for _, st := range b.List { // one of the test's own statements, not nested
			rs, isRange := st.(*ast.RangeStmt)
			if !isRange || !isIdent(rs.X, daemonMarksVar) {
				continue
			}
			mark, _ := rs.Value.(*ast.Ident)
			if mark == nil {
				continue
			}
			for _, st := range rs.Body.List {
				if ifs, isIf := st.(*ast.IfStmt); isIf && lacks(ifs.Cond, outFromRun, mark.Name) && fails(ifs.Body) {
					loop = rs
				}
			}
		}
		if loop != nil {
			if at := returnBefore(b, loop.Pos()); at.IsValid() {
				t.Errorf("%s can return before it ranges over %s (%s)", daemonTest, daemonMarksVar, suite.fset.Position(at))
			}
		}
		if loop == nil {
			t.Errorf("%s has no `for _, m := range %s { if !strings.Contains(out, m) { t.Errorf(…) } }` "+
				"with out the output of its run: what it decides on is no longer the marks", daemonTest, daemonMarksVar)
		}
	})

	t.Run("what the run binds is the resolved socket name", func(t *testing.T) {
		resolved := ""
		returned := false
		ast.Inspect(body(daemonSocketFunc), func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				if len(n.Rhs) == 1 && isCallTo(n.Rhs[0], "filepath", "EvalSymlinks") {
					call := n.Rhs[0].(*ast.CallExpr)
					if len(call.Args) == 1 && isIdent(call.Args[0], daemonSocketConst) {
						if id, ok := n.Lhs[0].(*ast.Ident); ok {
							resolved = id.Name
						}
					}
				}
			case *ast.ReturnStmt:
				if resolved != "" && len(n.Results) == 1 && isIdent(n.Results[0], resolved) {
					returned = true
				}
			}
			return true
		})
		if !returned {
			t.Errorf("%s does not return what filepath.EvalSymlinks(%s) resolved", daemonSocketFunc, daemonSocketConst)
		}
		b := body(daemonTest)
		target := assignedFromCall(b, daemonSocketFunc)
		binds := false
		ast.Inspect(b, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isIdent(call.Fun, "run") {
				return true
			}
			for i, a := range call.Args {
				if i > 0 && isStringLit(call.Args[i-1], "-v") {
					if be, ok := a.(*ast.BinaryExpr); ok && be.Op == token.ADD && target != "" && isIdent(be.X, target) {
						binds = true
					}
				}
			}
			return true
		})
		if !binds {
			t.Errorf("the run in %s does not bind (`-v`) what %s returned", daemonTest, daemonSocketFunc)
		}
	})
}

// assignedFromCall is the name the first value of a call to fn is assigned to
// in b ("" where there is none).
func assignedFromCall(b *ast.BlockStmt, fn string) string {
	name := ""
	ast.Inspect(b, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || name != "" || len(as.Rhs) != 1 {
			return true
		}
		if call, ok := as.Rhs[0].(*ast.CallExpr); ok && isIdent(call.Fun, fn) {
			if id, ok := as.Lhs[0].(*ast.Ident); ok && id.Name != "_" {
				name = id.Name
			}
		}
		return true
	})
	return name
}

// returnBefore is the position of a return in b ahead of pos, outside any
// function literal (NoPos where there is none).
func returnBefore(b *ast.BlockStmt, pos token.Pos) token.Pos {
	at := token.NoPos
	ast.Inspect(b, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			if n.Pos() < pos && !at.IsValid() {
				at = n.Pos()
			}
		}
		return true
	})
	return at
}

// lacks reports whether cond is `!strings.Contains(out, mark)`.
func lacks(cond ast.Expr, out, mark string) bool {
	u, ok := cond.(*ast.UnaryExpr)
	if !ok || u.Op != token.NOT || !isCallTo(u.X, "strings", "Contains") {
		return false
	}
	args := u.X.(*ast.CallExpr).Args
	return out != "" && len(args) == 2 && isIdent(args[0], out) && isIdent(args[1], mark)
}

// fails reports whether the block calls t.Error, t.Errorf, t.Fatal or t.Fatalf.
func fails(b *ast.BlockStmt) bool {
	found := false
	ast.Inspect(b, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			for _, m := range []string{"Error", "Errorf", "Fatal", "Fatalf"} {
				if isCallTo(call, "t", m) {
					found = true
				}
			}
		}
		return true
	})
	return found
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func isStringLit(e ast.Expr, s string) bool {
	b, ok := e.(*ast.BasicLit)
	return ok && b.Kind == token.STRING && b.Value == strconv.Quote(s)
}

func isCallTo(e ast.Expr, pkg, fn string) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == fn && isIdent(sel.X, pkg)
}
