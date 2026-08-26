// Package swaps finds the format calls where two arguments of the same kind sit
// next to each other, and could be exchanged without anything saying so.
//
// It exists because counting these with a search of the text kept going wrong.
// Four times a grep gave a number that a syntax tree later contradicted, and the
// difference was never noise: once it hid a live defect, once it hid a message
// whose output actually changed. The reason the search kept winning anyway is
// that a grep is one line and a parser is fifty, so this is the fifty, written
// once.
//
// What it reports is sites, not defects. Whether exchanging two arguments
// changes what a person reads is a judgement; this only says where the compiler
// would stay quiet.
//
// # What it does not see
//
// Every entry here is a case this misses, and each one has a test below that
// holds it to that. The list grew by being wrong: when a miss turns up, its
// cause is added rather than the case that revealed it.
//
//   - A verb whose kind is not known — %v and %w take anything, so a %v and a %s
//     that both receive strings are not paired.
//   - A format that is not a literal. A format held in a variable, or returned
//     by a call, is not read at all.
//   - Two arguments in different calls. A name printed by one line and a path by
//     the next could be exchanged between them, and nothing here looks across a
//     call boundary.
//   - A width or precision taken from an argument — %*d, %.*f. The star eats an
//     argument the positions here do not account for, so the whole format is
//     given up rather than reported from positions that are off by one. A pair
//     read past a star names two arguments the format does not read together,
//     and a mutation built from it edits the wrong two — worse than not seeing
//     it, because it looks like an answer.
//   - Explicit indexing written after a flag or a precision, %-[2]s or %.[2]d.
//     fmt accepts both; this looks for the bracket before the flags and so does
//     not see the format at all.
//   - A format function reached any way but by its own name at the call: through
//     a variable holding it, through a generic, or declared with a named string
//     type rather than string. A format method declared on an interface is the
//     same miss from the other side — the name is right there at the call and
//     the declaration this looks for is not.
//   - A format function from another package that is not fmt. log.Printf is not
//     read, and neither is fmt.Fscanf or fmt.Appendf: the table names five and
//     stops. (Nothing in this repository calls one, so today the totals do not
//     move.)
//
// And two things it can get wrong in the other direction.
//
// A pair here is a guess from the format, never from the values. %q and %s both
// say "string" to this, so a %q holding an int is reported as exchangeable with
// a %s holding a string. The exchange would build; `go vet` is what would
// object.
//
// A function is taken for a format function by its shape — a string, then a
// variadic that accepts anything — and plenty of functions have that shape
// without formatting anything. `func note(msg string, fields ...any)` is
// counted, and its "format" is a message.
//
// # The answer depends on where you point it
//
// Format functions are found in the files walked, so a call is counted only when
// the function it calls was declared somewhere under the same root. Pointing at
// one file of a package gives a smaller number than pointing at the package —
// in this repository, watch.go alone reports 3 sites and the same file inside
// internal/orchestrator reports 6, because logf and warnf are declared in
// another file. Walk a whole package, or know that you did not.
//
// A format written in pieces with `+`, or wrapped in parentheses, is read: the
// concatenation was a miss, and closing it is what turned "no swappable pairs
// here" into a live defect being found.
package swaps

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Site is one format call with at least one exchangeable pair.
//
// It carries which pairs rather than how many, because the two were once
// separate fields and a table that adds up its own rows is the only kind that
// cannot disagree with itself. Pairs reports the count from the list.
type Site struct {
	File   string
	Line   int
	Col    int    // the column the call starts at, which is what tells two on one line apart
	Format string // the format as written, pieces joined

	// Exchanges holds the argument index pairs, counted from the first
	// argument after the format, each pair in ascending order.
	Exchanges [][2]int

	call     string   // the call as written, from the name to the closing paren
	spans    [][2]int // where each argument after the format sits inside call
	simple   []bool   // per argument, whether it can be read twice unchanged (probe.go)
	fmtPlain bool     // the file imports "fmt" under its own name, so a probe can call it
}

// Pairs is how many ways two of this call's arguments could be exchanged.
func (s Site) Pairs() int { return len(s.Exchanges) }

// Swap returns the call as written and the same call with the arguments at a
// and b exchanged — the two halves of a mutation, ready for a sweep.
//
// The indexes are the ones in Exchanges, so a and b count from the first
// argument after the format and a is the smaller. It returns false for a pair
// this call does not have, rather than slicing something that is not there.
func (s Site) Swap(a, b int) (from, to string, ok bool) {
	if a >= b || a < 0 || b >= len(s.spans) {
		return "", "", false
	}
	x, y := s.spans[a], s.spans[b]
	to = s.call[:x[0]] + s.call[y[0]:y[1]] + s.call[x[1]:y[0]] + s.call[x[0]:x[1]] + s.call[y[1]:]
	return s.call, to, true
}

// fmtFormatAt names the argument holding the format, for the functions in fmt.
// A table is unavoidable for another package: nothing here reads fmt's source.
//
// The functions this repository writes are found instead of listed — see
// localFormatters. The first version of this file had only this table, and so
// could not see the two format functions the package it was pointed at defines;
// the count it gave was a quarter short.
var fmtFormatAt = map[string]int{
	"Errorf":  0,
	"Sprintf": 0,
	"Printf":  0,
	"Fprintf": 1,
	"Sscanf":  1,
}

// Find walks dir and returns every site, in the order the files were walked.
//
// Test files and anything under testdata are skipped for the same reason: a
// fixture that could be exchanged is not something a user reads. (The first
// version skipped only _test.go, and counted seven pairs in a fake runtime shim
// as if they were the product's.)
func Find(dir string) ([]Site, error) {
	fset := token.NewFileSet()
	var files []*ast.File
	src := map[string][]byte{}
	err := filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			// testdata for the same reason as _test.go; vendor, dot and
			// underscore directories because the go tool does not build them
			// either, and a count of the product should not include what is not
			// built.
			//
			// Never the root. Asked to walk `..` or a directory whose name begins
			// with a dot, an earlier version skipped it and answered zero — the
			// silent kind of zero this file says elsewhere it will not give.
			if path == dir {
				return nil
			}
			if n := fi.Name(); n == "testdata" || n == "vendor" || n[0] == '.' || n[0] == '_' {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		f, perr := parser.ParseFile(fset, path, b, 0)
		if perr != nil {
			// A file that will not parse is not a file with no pairs. Saying so
			// is the difference between "none here" and "not looked at".
			return perr
		}
		files = append(files, f)
		src[path] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	local := localFormatters(files)
	var out []Site
	for _, f := range files {
		fmtPlain := importsFmtPlainly(f)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			at, known := formatArgOf(call, local)
			if !known || len(call.Args) <= at+1 {
				return true
			}
			format, ok := literalFormat(call.Args[at])
			if !ok {
				return true
			}
			pairs := pairsIn(format, len(call.Args)-at-1)
			if len(pairs) == 0 {
				return true
			}
			pos := fset.Position(call.Pos())
			from := fset.Position(call.Pos()).Offset
			b := src[pos.Filename]
			spans := make([][2]int, 0, len(call.Args)-at-1)
			simple := make([]bool, 0, len(call.Args)-at-1)
			for _, arg := range call.Args[at+1:] {
				spans = append(spans, [2]int{
					fset.Position(arg.Pos()).Offset - from,
					fset.Position(arg.End()).Offset - from,
				})
				simple = append(simple, simpleExpr(arg))
			}
			out = append(out, Site{
				File:      pos.Filename,
				Line:      pos.Line,
				Col:       pos.Column,
				Format:    format,
				Exchanges: pairs,
				call:      string(b[from:fset.Position(call.End()).Offset]),
				spans:     spans,
				simple:    simple,
				fmtPlain:  fmtPlain,
			})
			return true
		})
	}
	return out, nil
}

// localFormatters finds the format functions the files define themselves, by
// what they take rather than by what they are called: a `format string` followed
// by a variadic, which is the shape fmt.Errorf has and the shape anything
// wrapping it has to have. The name is the key because that is what a call site
// shows; two functions of the same name taking the format in different places
// would confuse this, and the map keeps the first.
func localFormatters(files []*ast.File) map[string]int {
	out := map[string]int{}
	for _, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Type.Params == nil {
				continue
			}
			at, n := -1, 0
			for _, p := range fn.Type.Params.List {
				names := len(p.Names)
				if names == 0 {
					names = 1 // `func f(string, ...any)`: a parameter with no name is still one
				}
				if id, ok := p.Type.(*ast.Ident); ok && id.Name == "string" {
					// `a, b string` is two parameters, and the format is the last
					// of them. Counting the group as one put the format one place
					// to the left of where a call passes it.
					at = n + names - 1
					n += names
					continue
				}
				// at >= 0: a variadic with no string in front of it is not a
				// format function. Without this, `func fatal(a ...any)` was
				// registered with the format at -1, and the next call to it read
				// argument minus one.
				if e, variadic := p.Type.(*ast.Ellipsis); variadic && at >= 0 && at == n-1 && takesAnything(e.Elt) {
					if _, dup := out[fn.Name.Name]; !dup {
						out[fn.Name.Name] = at
					}
				}
				n += names
			}
		}
	}
	return out
}

// takesAnything says whether a variadic takes values of any type, which is what
// a format function's arguments are. Without this, a function taking a name and
// then a list of strings looks the same from here — one of them was picked up
// the first time this ran.
func takesAnything(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name == "any"
	case *ast.InterfaceType:
		return v.Methods == nil || len(v.Methods.List) == 0
	}
	return false
}

// formatArgOf says which argument holds the format, and whether this call takes
// one at all.
func formatArgOf(call *ast.CallExpr, local map[string]int) (int, bool) {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		// fmt first, and by the name in front of the dot. A local function called
		// Errorf used to take the name from fmt.Errorf and answer with its own
		// index — every fmt.Errorf in the tree then had a format that was not a
		// literal, and the sites disappeared without a word.
		if id, ok := fn.X.(*ast.Ident); ok && id.Name == "fmt" {
			at, ok := fmtFormatAt[fn.Sel.Name]
			return at, ok
		}
		// A method written here. The receiver is not an argument, so it uses the
		// same index as a plain call.
		if at, ok := local[fn.Sel.Name]; ok {
			return at, true
		}
		return 0, false
	case *ast.Ident:
		// A function in this package, called without a receiver.
		at, ok := local[fn.Name]
		return at, ok
	}
	return 0, false
}

// read is one verb and the argument it takes.
type read struct {
	arg  int
	verb byte
}

// pairsIn lists the ways two of the args could be exchanged: two verbs of the
// same kind, reading two different arguments that are both there. The order is
// the order the format offers them, so a sweep built from it reads in the order
// a person reads the line.
//
// Pairs of arguments, not pairs of verbs. A format that reads one argument twice
// offers the same exchange twice, and counting both made two sites in this
// repository report nine where six was the answer — the doc said arguments and
// the loop said verbs, and the number that reached a pull request was the loop's.
func pairsIn(format string, args int) [][2]int {
	reads, ok := verbReads(format)
	if !ok {
		return nil
	}
	seen := map[[2]int]bool{}
	var out [][2]int
	for a := 0; a < len(reads); a++ {
		for b := a + 1; b < len(reads); b++ {
			ia, ib := reads[a].arg, reads[b].arg
			k := kindOf(reads[a].verb)
			if ia == ib || ia >= args || ib >= args || k == "" || k != kindOf(reads[b].verb) {
				continue
			}
			if ia > ib {
				ia, ib = ib, ia
			}
			if !seen[[2]int{ia, ib}] {
				seen[[2]int{ia, ib}] = true
				out = append(out, [2]int{ia, ib})
			}
		}
	}
	return out
}

// verbReads walks a format and says which argument each verb reads, following
// explicit `%[n]` indexing the way fmt does: it sets the position, and the verbs
// after it carry on from there.
func verbReads(format string) ([]read, bool) {
	var reads []read
	next := 0
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			continue
		}
		j := i + 1
		if j < len(format) && format[j] == '%' {
			i = j
			continue
		}
		idx := -1
		if j < len(format) && format[j] == '[' {
			k := j + 1
			for k < len(format) && format[k] != ']' {
				k++
			}
			if k < len(format) {
				if v, err := strconv.Atoi(format[j+1 : k]); err == nil {
					idx = v - 1
				}
				j = k + 1
			}
		}
		for j < len(format) && strings.ContainsRune("+-# 0123456789.", rune(format[j])) {
			j++
		}
		if j < len(format) && format[j] == '*' {
			// A width or precision taken from an argument. The star eats an
			// argument these positions do not account for, so everything after
			// it is off by one — and being off by one is not the failure here
			// that it was when this only counted. A pair named from shifted
			// positions describes an exchange of two arguments the format does
			// not read together, and a mutation built from it edits the wrong
			// two. Counting short meant less than it said; this would look like
			// an answer. So the format is given up whole.
			return nil, false
		}
		if j >= len(format) {
			break
		}
		if idx < 0 {
			idx = next
		}
		next = idx + 1
		reads = append(reads, read{arg: idx, verb: format[j]})
		i = j
	}
	return reads, true
}

// kindOf says what a verb requires, or "" when it takes anything. Two verbs pair
// only when they require the same thing. A pair is a guess about the arguments,
// not a fact about them: this reads the format and never the values, so a %q
// holding an int pairs with a %s holding a string and the exchange would be
// caught by `go vet` rather than by nobody.
func kindOf(v byte) string {
	switch v {
	case 's', 'q':
		return "string"
	case 'd', 'x', 'X', 'o', 'b', 'c':
		return "int"
	case 'f', 'g', 'e':
		return "float"
	}
	return ""
}

// literalFormat reads the format out of an expression, following `+` when the
// string is written in pieces. An earlier version read a single literal, so
// every concatenated format was invisible — and one of the two it missed was a
// live defect. The shapes a format can be written in are few; the ways to write
// about one are not.
func literalFormat(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.ParenExpr:
		return literalFormat(v.X)
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		return s, err == nil
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		l, ok := literalFormat(v.X)
		if !ok {
			return "", false
		}
		r, ok := literalFormat(v.Y)
		if !ok {
			return "", false
		}
		return l + r, true
	}
	return "", false
}
