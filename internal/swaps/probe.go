package swaps

import (
	"go/ast"
	"strconv"
)

// A probe answers the question a survivor leaves open. A swap that no test
// catches reads as "write a test" — but it reads the same way when, under
// every input the tests supply, the two arguments held the same value, and
// then the fixture is what needs the work, not the tests. Which of the two it
// is cannot be read from test logs: a survivor passes either way, and a green
// test prints nothing about the values it never asserted. So the question is
// put to the run itself: instead of exchanging the arguments, wrap one so the
// call says — on stderr, where `go test`'s stream carries it — whether the two
// were ever formatted differently. Silence, from a call the tests reach, means
// no input the suite has can tell the swap from the original.
//
// Silence from a call the tests never reach means nothing at all, so the
// probe says out loud that it ran — the Evaluated marker — and a difference
// is only read against that.
//
// "Formatted differently" is fmt.Sprint's answer, which is %v's, not the
// verb's. A precision verb can collapse two values Sprint tells apart —
// %.2f prints 1.001 and 1.004 the same — and there the probe says the
// arguments differed when the printed message would not have. That error
// leans toward "write the test", the direction that costs a look rather
// than the one that buries a defect.

// Reasons a pair cannot be probed. Like a sweep's skips, neither is a finding
// about the code — only about what a probe could prove. The set is closed and
// tested: a reason added here without being added there is a red test, so the
// prose about "the reasons" cannot quietly fall behind the list.
const (
	// The probe's wrapper calls fmt.Sprint, so the file has to import fmt
	// under its own name. Every file with a site does today — measured, 28 of
	// 28 — but a formatting wrapper like logf needs no fmt at its call sites,
	// so the day one appears this refuses rather than writing a file that
	// does not build.
	ProbeSkipNoFmt = "the file does not import fmt under its own name"
	// The wrapper reads the second argument twice — once for the comparison,
	// once in the argument's own place — and ahead of its turn. Only a simple
	// expression (an identifier, a selector, an index, a literal) survives
	// that unchanged: anything with a side effect would make the probed run a
	// different run, and the difference the probe then reports could be one
	// the probe itself created.
	//
	// Arguments after b are not examined. One of them could still alias what
	// the pair reads — a call draining a buffer the pair formats — and then
	// the comparison sees a value the format's own read no longer would. The
	// guard covers the reads the wrapper adds and reorders; what a later
	// argument does to shared state was a hazard of the original call too.
	ProbeSkipImpure = "an argument would be read twice and ahead of its turn, which only a simple expression survives unchanged"
	// Asked for a pair the site does not have. A caller walking Exchanges
	// never sees this; it is the answer to an index made up elsewhere.
	ProbeSkipNoPair = "no such pair"
)

// What a probe appends to its marker. Both, not just the difference: a probe
// that only spoke when the values differed would be silent for two opposite
// reasons — the call never ran, or it ran on equal values — and those call for
// opposite work. "|evaluated" says the call ran; "|differs" says the pair told
// the arguments apart. The pipe keeps a marker from matching its own suffix
// by accident.
const (
	Evaluated = "|evaluated"
	Differs   = "|differs"
)

// Probe returns the call as written and the same call with the argument at a
// wrapped, so that running the tests answers two questions on stderr: marker
// followed by "|evaluated" each time the call runs at all, and marker followed
// by "|differs" each time the two arguments format differently. The first is
// what makes the second readable — silence about a difference means nothing
// until something says the call was reached, and with both printed the probe
// carries its own reach instead of borrowing a note measured elsewhere.
// The indexes are the ones in Exchanges, as for Swap.
//
// A reason means no probe: the pair is one of the shapes named above, or does
// not exist. An empty reason means from and to are the two halves of a probe
// mutation, ready for the same machinery that applies a swap.
func (s Site) Probe(a, b int, marker string) (from, to string, reason string) {
	if a >= b || a < 0 || b >= len(s.spans) {
		return "", "", ProbeSkipNoPair
	}
	if !s.fmtPlain {
		return "", "", ProbeSkipNoFmt
	}
	// b is read twice and early; the arguments between a and b are what could
	// change it between the early read and its own — the wrapper sits in a's
	// place, so everything from there to b runs after the comparison read.
	for i := a + 1; i <= b; i++ {
		if !s.simple[i] {
			return "", "", ProbeSkipImpure
		}
	}
	x, y := s.spans[a], s.spans[b]
	wrapped := "func(x, y any) any { println(" + strconv.Quote(marker+Evaluated) +
		"); if fmt.Sprint(x) != fmt.Sprint(y) { println(" + strconv.Quote(marker+Differs) +
		") }; return x }(" +
		s.call[x[0]:x[1]] + ", " + s.call[y[0]:y[1]] + ")"
	return s.call, s.call[:x[0]] + wrapped + s.call[x[1]:], ""
}

// simpleExpr is an expression that can be read twice, and out of turn, and be
// the same value both times: no calls, no channel receives, nothing that moves
// state. The list is deliberately short — a shape not on it is skipped with a
// reason, which costs a probe, where a shape wrongly on it costs a probed run
// that behaves differently from the real one.
func simpleExpr(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return true
	case *ast.BasicLit:
		return true
	case *ast.SelectorExpr:
		return simpleExpr(v.X)
	case *ast.IndexExpr:
		return simpleExpr(v.X) && simpleExpr(v.Index)
	case *ast.ParenExpr:
		return simpleExpr(v.X)
	}
	return false
}

// importsFmtPlainly says whether the file brings fmt in under its own name —
// the only form the probe's wrapper can rely on. An alias would work if the
// wrapper knew it; it does not, and refusing is cheaper than being wrong.
func importsFmtPlainly(f *ast.File) bool {
	for _, imp := range f.Imports {
		if imp.Path.Value == `"fmt"` && imp.Name == nil {
			return true
		}
	}
	return false
}
