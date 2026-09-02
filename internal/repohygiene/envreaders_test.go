package repohygiene_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	path0 "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// envField is the field on a compose service that holds its environment. Read
// straight, it is whatever load could put together — which, when an env_file
// did not resolve, is the declared half with no sign that the rest is missing.
// ResolvedEnv is the door: it hands back the environment or the reason there
// is none.
const envField = "Environment"

// resolvedEnvFunc is that door.
const resolvedEnvFunc = "ResolvedEnv"

// composePkgDir declares envField and is where reading it straight is the only
// thing to do.
const composePkgDir = "internal/compose"

// environmentReaders is every place outside internal/compose that reads the
// field without going through ResolvedEnv, and the function that has already
// asked the door by the time it runs.
//
// The guard is not reachability: it is a claim that this reader only runs
// below that function. What this test holds is narrower — that the guard named
// exists and still asks ResolvedEnv. A reader moved out from under its guard
// keeps the same name here and nothing goes red, which is why the entries also
// carry the path from the guard down, for a reader to check by hand.
var environmentReaders = []struct {
	file, fn, guardFile, guardFn, path string
}{
	{
		"internal/orchestrator/audit.go", "egressProxy",
		"internal/orchestrator/audit.go", "RunAudited",
		"RunAudited asks the door, then calls this",
	},
	{
		"internal/orchestrator/adapt.go", "servicePGDATA",
		"internal/orchestrator/adapt.go", "adaptService",
		"adaptService → adaptBindMountedDataDir → swapHelpsHere → postgresDataDirFor → this",
	},
	{
		"internal/orchestrator/adapt.go", "hasPGDATA",
		"internal/orchestrator/adapt.go", "adaptService",
		"adaptService → adaptPGDATA / postgresDataDirFor → this",
	},
	{
		"internal/orchestrator/orchestrator.go", "hasPGDATASubdir",
		"internal/orchestrator/orchestrator.go", "Up",
		"Up's pre-flight asks the door for every service, then Up → warnForeignPostgresDataVolume → this",
	},
}

// Reading the environment straight is not wrong; reading it straight without
// something upstream having asked ResolvedEnv is. The failure that door exists
// for is quiet: an env_file that did not resolve leaves the declared half in
// place, so a straight read gets a partial environment and no error (#413).
//
// Two things go stale on their own and neither is loud: a new straight reader
// (nothing says the list grew), and a guard that moves or goes (the reader
// says nothing about being uncovered). #659 moved both guards in this list —
// Up's into pre-flight, adaptService's around the notes — so this is not
// hypothetical. The list here is held to the tree in both directions, and each
// named guard is checked for still asking the door.
//
// What this does not do, measured rather than guessed:
//
//   - Work out whether a reader is really below its guard. The path is written
//     out for a person; nothing verifies it.
//   - Tell a guard whose test is narrower than the function. One that asks
//     the door inside another `if` covers only that branch, and reads here the
//     same as one that covers everything.
//   - Notice a door of one's own. An accessor on the service that hands the
//     field back — `func (s *Service) RawEnv() Environment` — is inside
//     internal/compose, which is not read, and a caller of it is not reading
//     the field. Measured: green.
//   - Look outside function declarations. A package-level `var f = func(svc
//     *compose.Service) { … svc.Environment … }` is not walked. Measured:
//     green.
//   - Read tests, or testdata. Both are skipped: an assertion about the field
//     is not a use of it, and the programs under testdata are fakes.
//   - Tell a read from a write, or one receiver from another: what is compared
//     is the file and the function's name.
//
// It reads a name, not a type: `svc.Environment` and `compose.Environment` look
// alike, so the file's imports are used to drop the second. That leaves a type
// of one's own called Environment landing here, and a field of that name on
// another type landing here too.
func TestEveryStraightEnvironmentReadIsCoveredByTheDoor(t *testing.T) {
	root := repoRoot(t)

	type reader struct{ file, fn string }
	found := map[reader]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "testdata" || name == "vendor" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, composePkgDir+"/") {
			return nil // the field's own package
		}
		f, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		// `compose.Environment` is the type, not a read of the field. The
		// import names of this file are the only way to tell them apart
		// without resolving anything.
		imported := map[string]bool{}
		for _, im := range f.Imports {
			name := ""
			if im.Name != nil {
				name = im.Name.Name
			} else if p, err := strconv.Unquote(im.Path.Value); err == nil {
				name = path0.Base(p)
			}
			if name != "" && name != "_" && name != "." {
				imported[name] = true
			}
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != envField {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); ok && imported[id.Name] {
					return true // the type, named through its package
				}
				found[reader{rel, fn.Name.Name}] = true
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading the tree: %v", err)
	}

	listed := map[reader]bool{}
	for _, r := range environmentReaders {
		listed[reader{r.file, r.fn}] = true
	}
	var added, gone []string
	for r := range found {
		if !listed[r] {
			added = append(added, r.file+":"+r.fn)
		}
	}
	for r := range listed {
		if !found[r] {
			gone = append(gone, r.file+":"+r.fn)
		}
	}
	sort.Strings(added)
	sort.Strings(gone)
	if len(added) > 0 {
		t.Errorf("%d place(s) read .%s straight and are not listed here: %v.\n"+
			"  A straight read gets whatever load could assemble, which for an env_file "+
			"that did not resolve is the declared half and no error. Ask through %s(), or "+
			"add the place here with the function that has already asked by the time it "+
			"runs.", len(added), envField, added, resolvedEnvFunc)
	}
	if len(gone) > 0 {
		t.Errorf("%d place(s) listed here no longer read .%s: %v.\n"+
			"  The list is what a reader is told to check; one naming places that are not "+
			"there reads as though the tree had been looked at more recently than it has.",
			len(gone), envField, gone)
	}

	// And the guards themselves: named, present, and still asking the door.
	for _, r := range environmentReaders {
		f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(r.guardFile)), nil, 0)
		if err != nil {
			t.Fatalf("reading %s: %v", r.guardFile, err)
		}
		asks := false
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name != r.guardFn {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				// Not that the door is opened — that what comes back is
				// looked at. `_, _ = svc.ResolvedEnv()` opens it and walks
				// through either way, which is the drift this is about.
				ifs, ok := n.(*ast.IfStmt)
				if !ok {
					return true
				}
				as, ok := ifs.Init.(*ast.AssignStmt)
				if !ok || len(as.Lhs) != 2 {
					return true
				}
				called := false
				for _, rhs := range as.Rhs {
					ast.Inspect(rhs, func(n ast.Node) bool {
						if call, ok := n.(*ast.CallExpr); ok {
							if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == resolvedEnvFunc {
								called = true
							}
						}
						return true
					})
				}
				bound, ok := as.Lhs[1].(*ast.Ident)
				if !called || !ok || bound.Name == "_" {
					return true
				}
				ast.Inspect(ifs.Cond, func(n ast.Node) bool {
					if id, ok := n.(*ast.Ident); ok && id.Name == bound.Name {
						asks = true
					}
					return true
				})
				return true
			})
		}
		if !asks {
			t.Errorf("%s:%s reads .%s straight and names %s:%s as what has already asked "+
				"%s() — and that function does not test what comes back (%s).\n"+
				"  What is looked for is `if _, err := …%s(); err != …`: the door opened "+
				"and the reason it gives read. Opening it and dropping the reason leaves "+
				"a straight read with nothing in front of it, which is the drift this "+
				"exists for.",
				r.file, r.fn, envField, r.guardFile, r.guardFn, resolvedEnvFunc, r.path,
				resolvedEnvFunc)
		}
	}
}
