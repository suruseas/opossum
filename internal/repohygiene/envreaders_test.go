package repohygiene_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	path0 "path"
	"path/filepath"
	"reflect"
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
// field without going through ResolvedEnv — each read spelled out, in source
// order — and the function that has already asked the door by the time it
// runs, named with its receiver so that a method of the same name on another
// type is not mistaken for it.
//
// The guard is not reachability: it is a claim that this reader only runs
// below that function. What this test holds is narrower — that the guard named
// exists and still asks ResolvedEnv. A reader moved out from under its guard
// keeps the same name here and nothing goes red, which is why the entries also
// carry the path from the guard down, for a reader to check by hand.
//
// The reads are listed one by one rather than per function (#837): a second
// read added inside a function already listed — of another service, say —
// used to be invisible, because the key was the function's name.
var environmentReaders = []struct {
	file, fn  string
	reads     []string // each straight read of the field, as written, in source order
	guardFile string
	guardRecv string // the guard's receiver type, "" for a plain function
	guardFn   string
	path      string
}{
	{
		"internal/orchestrator/audit.go", "egressProxy",
		[]string{"svc.Environment", "svc.Environment"},
		"internal/orchestrator/audit.go", "*Orchestrator", "RunAudited",
		"RunAudited asks the door, then calls this",
	},
	{
		"internal/orchestrator/adapt.go", "servicePGDATA",
		[]string{"svc.Environment"},
		"internal/orchestrator/adapt.go", "*Orchestrator", "adaptService",
		"adaptService → adaptBindMountedDataDir → swapHelpsHere → postgresDataDirFor → this",
	},
	{
		"internal/orchestrator/adapt.go", "hasPGDATA",
		[]string{"svc.Environment"},
		"internal/orchestrator/adapt.go", "*Orchestrator", "adaptService",
		"adaptService → adaptPGDATA / postgresDataDirFor → this",
	},
	{
		"internal/orchestrator/orchestrator.go", "hasPGDATASubdir",
		[]string{"svc.Environment"},
		"internal/orchestrator/orchestrator.go", "*Orchestrator", "Up",
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
//   - Tell a read from a write: an assignment to the field is listed like a
//     read of it.
//
// It reads a name, not a type: `svc.Environment` and `compose.Environment` look
// alike, so the file's imports are used to drop the second. That leaves a type
// of one's own called Environment landing here, and a field of that name on
// another type landing here too.
func TestEveryStraightEnvironmentReadIsCoveredByTheDoor(t *testing.T) {
	root := repoRoot(t)
	found, err := straightEnvironmentReads(root)
	if err != nil {
		t.Fatalf("reading the tree: %v", err)
	}

	listed := map[envReader][]string{}
	for _, r := range environmentReaders {
		listed[envReader{r.file, r.fn}] = r.reads
	}
	var added, gone, changed []string
	for r, reads := range found {
		want, ok := listed[r]
		switch {
		case !ok:
			added = append(added, r.file+":"+r.fn+" "+strings.Join(reads, ", "))
		case !reflect.DeepEqual(reads, want):
			changed = append(changed, r.file+":"+r.fn+" reads "+strings.Join(reads, ", ")+"; listed "+strings.Join(want, ", "))
		}
	}
	for r := range listed {
		if _, ok := found[r]; !ok {
			gone = append(gone, r.file+":"+r.fn)
		}
	}
	sort.Strings(added)
	sort.Strings(gone)
	sort.Strings(changed)
	if len(added) > 0 {
		t.Errorf("%d place(s) read .%s straight and are not listed here: %v.\n"+
			"  A straight read gets whatever load could assemble, which for an env_file "+
			"that did not resolve is the declared half and no error. Ask through %s(), or "+
			"add the place here with the function that has already asked by the time it "+
			"runs.", len(added), envField, added, resolvedEnvFunc)
	}
	if len(changed) > 0 {
		t.Errorf("%d listed place(s) read .%s differently from what is listed: %v.\n"+
			"  Every read is listed one by one. A read added inside a function that is "+
			"already here — of another service, or the same one a second time — is a new "+
			"straight read, and the guard named for the function was asked about the "+
			"reads it had then, not this one.", len(changed), envField, changed)
	}
	if len(gone) > 0 {
		t.Errorf("%d place(s) listed here no longer read .%s: %v.\n"+
			"  The list is what a reader is told to check; one naming places that are not "+
			"there reads as though the tree had been looked at more recently than it has.",
			len(gone), envField, gone)
	}

	// And the guards themselves: named with their receiver, present, and still
	// asking the door.
	for _, r := range environmentReaders {
		present, asks, err := guardAsksTheDoor(root, r.guardFile, r.guardRecv, r.guardFn)
		if err != nil {
			t.Fatalf("reading %s: %v", r.guardFile, err)
		}
		if !present {
			t.Errorf("%s:%s names %s:(%s).%s as its guard, and %s declares no such function on "+
				"that receiver. A method of the same name on another type is not this guard; "+
				"if the guard moved or its receiver changed, the list is where the new name goes.",
				r.file, r.fn, r.guardFile, r.guardRecv, r.guardFn, r.guardFile)
			continue
		}
		if !asks {
			t.Errorf("%s:%s reads .%s straight and names %s:(%s).%s as what has already asked "+
				"%s() — and that function does not test what comes back (%s).\n"+
				"  What is looked for is `if _, err := …%s(); err != …`: the door opened "+
				"and the reason it gives read. Opening it and dropping the reason leaves "+
				"a straight read with nothing in front of it, which is the drift this "+
				"exists for.",
				r.file, r.fn, envField, r.guardFile, r.guardRecv, r.guardFn, resolvedEnvFunc, r.path,
				resolvedEnvFunc)
		}
	}
}

// envReader is one function that reads the field straight.
type envReader struct{ file, fn string }

// straightEnvironmentReads walks the tree under root and returns, for every
// function outside internal/compose (tests and testdata skipped) that reads
// the field straight, each read as written, in source order.
func straightEnvironmentReads(root string) (map[envReader][]string, error) {
	found := map[envReader][]string{}
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
				key := envReader{rel, fn.Name.Name}
				found[key] = append(found[key], types.ExprString(sel))
				return true
			})
		}
		return nil
	})
	return found, err
}

// receiverType spells a method's receiver type the way it is written
// (`*Orchestrator`, `Service`), or "" for a plain function.
func receiverType(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	return types.ExprString(fn.Recv.List[0].Type)
}

// guardAsksTheDoor reads the file and reports whether a function named fn on
// the receiver type recv is declared there, and whether it asks the door and
// reads the answer — `if _, err := …ResolvedEnv(); err != …`. Opening the
// door and dropping what it says (`_, _ = svc.ResolvedEnv()`) does not count.
func guardAsksTheDoor(root, file, recv, fn string) (present, asks bool, err error) {
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, filepath.FromSlash(file)), nil, 0)
	if err != nil {
		return false, false, err
	}
	for _, d := range f.Decls {
		decl, ok := d.(*ast.FuncDecl)
		if !ok || decl.Body == nil || decl.Name.Name != fn || receiverType(decl) != recv {
			continue
		}
		present = true
		ast.Inspect(decl.Body, func(n ast.Node) bool {
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
	return present, asks, nil
}

// TestTheEnvironmentReaderRulesOnATreeSmallEnoughToRead holds the two rules
// above to fixture trees where the answer is known: reads are counted one by
// one (a second read inside a listed function is a new read), the type named
// through its package is not a read, and a guard is the function on the
// receiver named — a method of the same name on another type, however well it
// asks the door, is not it.
func TestTheEnvironmentReaderRulesOnATreeSmallEnoughToRead(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("internal/compose/env.go", "package compose\n\ntype Environment []string\n")
	write("pkg/readers.go", `package pkg

import "example.com/x/internal/compose"

func one(svc *compose.Service) int   { return len(svc.Environment) }
func twice(svc *compose.Service) int { return len(svc.Environment) + len(svc.Environment) }
func other(o *O, svc *compose.Service) int {
	if len(svc.Environment) > 0 {
		return len(o.Project.Services["proxy"].Environment)
	}
	return 0
}
func onlyTheType(e compose.Environment) int { return len(e) }
`)
	write("pkg/readers_test.go", "package pkg\n\nfunc skipped(svc *compose.Service) int { return len(svc.Environment) }\n")
	write("pkg/guards.go", `package pkg

type O struct{}
type Decoy struct{}

func (o *O) Guarded(svc *compose.Service) error {
	if _, err := svc.ResolvedEnv(); err != nil {
		return err
	}
	return nil
}
func (o *O) Dropped(svc *compose.Service) { _, _ = svc.ResolvedEnv() }
func (d *Decoy) Dropped(svc *compose.Service) error {
	if _, err := svc.ResolvedEnv(); err != nil {
		return err
	}
	return nil
}
func Plain(svc *compose.Service) error {
	if _, err := svc.ResolvedEnv(); err != nil {
		return err
	}
	return nil
}
`)

	found, err := straightEnvironmentReads(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[envReader][]string{
		{"pkg/readers.go", "one"}:   {"svc.Environment"},
		{"pkg/readers.go", "twice"}: {"svc.Environment", "svc.Environment"},
		{"pkg/readers.go", "other"}: {"svc.Environment", `o.Project.Services["proxy"].Environment`},
	}
	if !reflect.DeepEqual(found, want) {
		t.Errorf("reads = %v, want %v (one per read, in source order; the type through its package and the test file are not reads)", found, want)
	}

	for _, tc := range []struct {
		name, recv, fn string
		present, asks  bool
	}{
		{"a method that asks and reads the answer", "*O", "Guarded", true, true},
		{"a method that opens the door and drops what it says", "*O", "Dropped", true, false},
		{"the decoy of the same name on another type is not this guard", "*Decoy", "Dropped", true, true},
		{"a plain function", "", "Plain", true, true},
		{"the receiver named is not the one declared", "*Other", "Guarded", false, false},
		{"a plain function looked up as a method", "*O", "Plain", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			present, asks, err := guardAsksTheDoor(root, "pkg/guards.go", tc.recv, tc.fn)
			if err != nil {
				t.Fatal(err)
			}
			if present != tc.present || asks != tc.asks {
				t.Errorf("(%s).%s: present=%v asks=%v, want present=%v asks=%v", tc.recv, tc.fn, present, asks, tc.present, tc.asks)
			}
		})
	}
}
