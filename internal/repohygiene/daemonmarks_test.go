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
//   - the marks: daemonMarksVar is declared once, at package level, as a list
//     of string literals that holds the Engine API's own header (which a plain
//     HTTP server does not write), and the name appears nowhere else in the
//     package but the measurement's loop;
//   - the loop: the test makes one run, the one that binds the socket and runs
//     pingDaemonConst, and takes its output into a name nothing assigns again;
//     then, as one of its own statements with no return ahead of it, it ranges
//     over the marks and fails when that output lacks one;
//   - the path: that run's arguments are exactly the expected ones, binding
//     `<target>:/s.sock` once, and target is what dockerDaemonSocket returns, and every return there is what
//     filepath.EvalSymlinks made of daemonSocketConst, never assigned again.
//
// It matches names and shapes, not what a name refers to, and it does not
// follow control flow: a shadowed `t`, `strings` or loop variable, a
// conditional goto or panic+recover ahead of the loop, a conditional continue
// or break inside it, an `if false {}` around the failure, or what pingDaemon
// itself sends, are not told apart. Those are rewrites made to get past it,
// not slips.
const (
	daemonTest       = "TestARealDockerDaemonAnswersThroughABoundSocket"
	daemonMarksVar   = "daemonMarks"
	daemonSocketFunc = "dockerDaemonSocket"
	pingDaemonConst  = "pingDaemon"
	engineAPIMark    = "Api-Version:"
	socketInside     = ":/s.sock"
)

func TestTheDaemonMeasurementDecidesOnWhatItSays(t *testing.T) {
	suite := conformanceReach(t)

	t.Run("the marks hold the Engine API's header", func(t *testing.T) {
		var marks []string
		var spec ast.Node
		declared := 0
		for _, f := range suite.files {
			for _, d := range f.Decls { // package level only
				gen, ok := d.(*ast.GenDecl)
				if !ok || gen.Tok != token.VAR {
					continue
				}
				for _, sp := range gen.Specs {
					vs := sp.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if name.Name != daemonMarksVar {
							continue
						}
						declared++
						spec = vs
						if i >= len(vs.Values) {
							t.Fatalf("%s is declared with no value", daemonMarksVar)
						}
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
				}
			}
		}
		if declared != 1 {
			t.Fatalf("%s is declared %d times at package level, want once", daemonMarksVar, declared)
		}
		// The name appears twice in the package: where it is declared and where
		// the measurement ranges over it. Anywhere else — assigned, indexed,
		// copied into, declared again — the list the loop reads may not be the
		// one written.
		allowed := map[*ast.Ident]bool{}
		for _, n := range spec.(*ast.ValueSpec).Names {
			allowed[n] = true
		}
		for _, st := range funcBody(t, suite, daemonTest).List {
			if rs, ok := st.(*ast.RangeStmt); ok {
				if id, ok := rs.X.(*ast.Ident); ok && id.Name == daemonMarksVar {
					allowed[id] = true
				}
			}
		}
		for _, f := range suite.files {
			ast.Inspect(f, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == daemonMarksVar && !allowed[id] {
					t.Errorf("%s is used at %s, outside its declaration and the measurement's loop", daemonMarksVar, suite.fset.Position(id.Pos()))
				}
				return true
			})
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

	t.Run("every mark is asked of the bound run's output and can fail the test", func(t *testing.T) {
		b := funcBody(t, suite, daemonTest)
		call, out, def := theRun(t, b)
		if last := call.Args[len(call.Args)-1]; !isIdent(last, pingDaemonConst) {
			t.Errorf("the run in %s does not run %s: its output is not the daemon's answer", daemonTest, pingDaemonConst)
		}
		if at := bindsName(b, out, def); at.IsValid() {
			t.Errorf("%s assigns %s again (%s): what the loop reads is not the run's output", daemonTest, out, suite.fset.Position(at))
		}
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
			// The body is that one if: a break or continue beside it would stop
			// asking the later marks.
			if len(rs.Body.List) == 1 {
				if ifs, isIf := rs.Body.List[0].(*ast.IfStmt); isIf && ifs.Else == nil && lacks(ifs.Cond, out, mark.Name) && fails(ifs.Body) {
					loop = rs
				}
			}
		}
		if loop == nil {
			t.Fatalf("%s has no `for _, m := range %s { if !strings.Contains(%s, m) { t.Errorf(…) } }`: "+
				"what it decides on is no longer the marks", daemonTest, daemonMarksVar, out)
		}
		if at := returnBefore(b, loop.Pos()); at.IsValid() {
			t.Errorf("%s can return before it ranges over %s (%s)", daemonTest, daemonMarksVar, suite.fset.Position(at))
		}
	})

	t.Run("what the run binds is the resolved socket name", func(t *testing.T) {
		sb := funcBody(t, suite, daemonSocketFunc)
		resolved, rdef := "", ast.Node(nil)
		ast.Inspect(sb, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || resolved != "" || len(as.Rhs) != 1 || !isCallTo(as.Rhs[0], "filepath", "EvalSymlinks") {
				return true
			}
			call := as.Rhs[0].(*ast.CallExpr)
			if id, ok := as.Lhs[0].(*ast.Ident); ok && len(call.Args) == 1 && isIdent(call.Args[0], daemonSocketConst) {
				resolved, rdef = id.Name, as
			}
			return true
		})
		if resolved == "" {
			t.Fatalf("%s does not resolve %s with filepath.EvalSymlinks", daemonSocketFunc, daemonSocketConst)
		}
		if at := bindsName(sb, resolved, rdef); at.IsValid() {
			t.Errorf("%s assigns %s again (%s) after resolving it", daemonSocketFunc, resolved, suite.fset.Position(at))
		}
		returns := 0
		ast.Inspect(sb, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			r, ok := n.(*ast.ReturnStmt)
			if !ok {
				return true
			}
			returns++
			if len(r.Results) != 1 || !isIdent(r.Results[0], resolved) {
				t.Errorf("%s returns something other than %s (%s)", daemonSocketFunc, resolved, suite.fset.Position(r.Pos()))
			}
			return true
		})
		if returns == 0 {
			t.Errorf("%s returns nothing", daemonSocketFunc)
		}

		b := funcBody(t, suite, daemonTest)
		target, tdef := "", ast.Node(nil)
		ast.Inspect(b, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || target != "" || len(as.Rhs) != 1 {
				return true
			}
			if c, ok := as.Rhs[0].(*ast.CallExpr); ok && isIdent(c.Fun, daemonSocketFunc) {
				if id, ok := as.Lhs[0].(*ast.Ident); ok {
					target, tdef = id.Name, as
				}
			}
			return true
		})
		if target == "" {
			t.Fatalf("%s does not take what %s returns", daemonTest, daemonSocketFunc)
		}
		if at := bindsName(b, target, tdef); at.IsValid() {
			t.Errorf("%s assigns %s again (%s): what it binds is not what was resolved", daemonTest, target, suite.fset.Position(at))
		}
		// The run's arguments, all of them: one bind, of the resolved path, in any
		// spelling the runtime takes (`-v`, `--volume`, `-v=`, `--mount`) another
		// would be an argument this does not expect.
		call, _, _ := theRun(t, b)
		a := call.Args
		ok := len(a) == 10 && isIdent(a[0], "t") && isIdent(a[1], "bin") &&
			isStringLit(a[2], "run") && isStringLit(a[3], "--rm") && isStringLit(a[4], "-v") &&
			isIdent(a[6], "clientImage") && isStringLit(a[7], "python3") && isStringLit(a[8], "-c") &&
			isIdent(a[9], pingDaemonConst)
		if ok {
			be, isBin := a[5].(*ast.BinaryExpr)
			ok = isBin && be.Op == token.ADD && isIdent(be.X, target) && isStringLit(be.Y, socketInside)
		}
		if !ok {
			t.Errorf("the run in %s is not `run(t, bin, \"run\", \"--rm\", \"-v\", %s+%q, clientImage, \"python3\", \"-c\", %s)`",
				daemonTest, target, socketInside, pingDaemonConst)
		}
	})
}

// funcBody is the body of the suite's function name.
func funcBody(t *testing.T, suite conformanceSuite, name string) *ast.BlockStmt {
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

// theRun is the test's one call to run — every call counts, kept or not — and
// the name its first value goes to and the statement that defines it. A test
// with another call is refused, so the output and the bind are of one run.
func theRun(t *testing.T, b *ast.BlockStmt) (*ast.CallExpr, string, ast.Node) {
	t.Helper()
	var call *ast.CallExpr
	var out string
	var def ast.Node
	runs := 0
	ast.Inspect(b, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok || !isIdent(c.Fun, "run") {
			return true
		}
		runs++
		call = c
		return true
	})
	if runs != 1 {
		t.Fatalf("%s calls run %d times, want once: the output read and the socket bound have to be one run's", daemonTest, runs)
	}
	ast.Inspect(b, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if ok && len(as.Rhs) == 1 && as.Rhs[0] == ast.Expr(call) {
			if id, ok := as.Lhs[0].(*ast.Ident); ok && id.Name != "_" {
				out, def = id.Name, as
			}
		}
		return true
	})
	if out == "" {
		t.Fatalf("%s does not keep the output of its run", daemonTest)
	}
	return call, out, def
}

// bindsName is the position of a statement in n, other than def, that assigns
// name (or an element of it), takes its address, or declares a new one of it
// (NoPos where there is none). A write through another name it was copied to
// is not seen.
func bindsName(n ast.Node, name string, def ast.Node) token.Pos {
	at := token.NoPos
	ast.Inspect(n, func(x ast.Node) bool {
		if at.IsValid() || x == nil || x == def {
			return x != def
		}
		switch x := x.(type) {
		case *ast.AssignStmt:
			for _, l := range x.Lhs {
				if ix, ok := l.(*ast.IndexExpr); ok {
					l = ix.X
				}
				if isIdent(l, name) {
					at = x.Pos()
				}
			}
		case *ast.UnaryExpr: // &name, handed to something that can write it
			if x.Op == token.AND && isIdent(x.X, name) {
				at = x.Pos()
			}
		case *ast.IncDecStmt:
			if isIdent(x.X, name) {
				at = x.Pos()
			}
		case *ast.ValueSpec:
			for _, v := range x.Names {
				if v.Name == name {
					at = x.Pos()
				}
			}
		case *ast.RangeStmt:
			if isIdent(x.Key, name) || isIdent(x.Value, name) {
				at = x.Pos()
			}
		case *ast.Field:
			for _, v := range x.Names {
				if v.Name == name {
					at = x.Pos()
				}
			}
		}
		return true
	})
	return at
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
