// Package mutate runs a set of hand-written mutations against the tree and
// reports which tests each one killed.
//
// It is not a mutation testing tool in the usual sense. Those generate mutations
// mechanically (`>` becomes `>=`, a branch is inverted) and report a score; what
// is wanted here is the opposite end — a handful of mutations an author chose
// because they express a specific fear ("stop calling this helper", "put the old
// vulnerable script back", "make this seam a no-op"), and an answer to "which
// test, by name, noticed?". A score says how much of the code is covered. This
// says whether the thing you just wrote is guarded, and by what.
//
// It exists because doing it by hand kept going wrong in the same ways, each of
// which produced a confident and false entry in a pull request:
//
//   - a mutation that did not apply, because the pattern matched nothing, read as
//     "the tests caught it" (the file was never changed)
//   - a mutation that did not compile, read as "caught" (no test ran at all)
//   - a whole package going red, credited to the test the author had just written
//   - a mutation left applied when the run was interrupted
//
// So every one of those is checked rather than assumed: the pattern must match
// exactly once and must actually change something, the tree must still build,
// the failing tests are collected by name from `go test -json`, and the file is
// restored and compared byte for byte afterwards.
package mutate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// A Mutation is one edit an author wants to prove is noticed.
type Mutation struct {
	// Name is how it appears in the report — write it as the defect it
	// introduces ("the second look never happens"), not as the edit.
	Name string `json:"name"`
	// File is the path to change, repo-relative.
	File string `json:"file"`
	// From is the exact text to replace. It must appear exactly once: a pattern
	// that matches nothing changes nothing, and a run that reports "survived" for
	// an unchanged file is worse than no run at all.
	From string `json:"from"`
	// To is what replaces it.
	To string `json:"to"`
	// Packages are the ones to build and test. Narrow is better — a whole-suite
	// run makes it easy to credit someone else's failing test to your mutation.
	Packages []string `json:"packages"`
}

// Apply replaces From with To in src, insisting the pattern appears exactly once
// and that the result is actually different.
//
// The count is the point. `perl -pi -e` and friends do nothing when the pattern
// misses, and say nothing about it, so the author reads a green suite as "the
// mutation survived" when the truth is that no mutation was ever applied. Both
// halves of the mistake are silent, which is what makes it worth an error.
//
// The same reasoning covers a From that equals To, which is what a copied-and-
// half-edited entry looks like: it applies cleanly, changes nothing, and every
// test passes. That is the exact false "survived" this package exists to prevent,
// arriving through the front door.
func Apply(src string, m Mutation) (string, error) {
	if m.From == "" {
		return "", fmt.Errorf("%s: `from` is empty, which matches everywhere and identifies nothing", m.File)
	}
	if m.From == m.To {
		return "", fmt.Errorf("%s: `from` and `to` are the same, so nothing would change — "+
			"a run like that reports the mutation as survived without ever making one", m.File)
	}
	switch n := strings.Count(src, m.From); {
	case n == 0:
		return "", fmt.Errorf("the pattern is not in %s, so nothing would change — "+
			"a run like that reports a mutation as survived when it was never applied", m.File)
	case n > 1:
		return "", fmt.Errorf("the pattern appears %d times in %s; replacing them all mutates more "+
			"than the one thing being tested — narrow it until it is unique", n, m.File)
	}
	return strings.Replace(src, m.From, m.To, 1), nil
}

// Failures are the test names that failed, sorted and deduplicated.
//
// Read from `go test -json` rather than by matching `--- FAIL:` in the plain
// output. A test that prints or asserts on go test's own output — this
// repository has several — puts that text through its own logs, and a regexp
// over stdout cannot tell a test's quoted fixture from a test's real result. The
// event stream can: the failure is a field, not a line.
//
// By name, because "the package went red" is not attribution: a mutation to one
// file can break an unrelated test, and reading a red package as proof that the
// test you just wrote is doing its job has produced false entries before.
func Failures(testJSON string) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(testJSON, "\n") {
		if line == "" {
			continue
		}
		var e struct {
			Action string `json:"Action"`
			Test   string `json:"Test"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // build output and other noise share the stream
		}
		if e.Action != "fail" || e.Test == "" || seen[e.Test] {
			continue
		}
		seen[e.Test] = true
		out = append(out, e.Test)
	}
	sort.Strings(out)
	return out
}

// Outcome is what happened to one mutation.
type Outcome int

const (
	// Caught means the tree built and at least one test failed, by name.
	Caught Outcome = iota
	// Survived means the tree built, every test passed, and the defect is
	// therefore invisible to the suite. This is the finding worth reporting.
	Survived
	// Broken means the mutated tree does not compile, so no test ran. It is not
	// evidence either way — recording it as caught is a mistake this reports
	// explicitly rather than quietly allowing.
	Broken
	// Inconclusive means the test run failed without naming a single test: a
	// binary that panicked, a package-level timeout, a toolchain that could not
	// start. Calling that "survived" would be the loudest lie this tool could
	// tell, because the mutations aimed at waits and loops are exactly the ones
	// that hang.
	Inconclusive
)

// Whether the tests appear to reach a survivor's line is reported as a note on
// the survivor, not as an outcome of its own.
//
// It was an outcome once. Six attempts at deciding it produced six ways of
// getting it wrong, every one of them the same shape — a line the tests do run,
// called a line nothing reaches — and every one of them turning a survivor, which
// says "write a test", into unreachable code, which says "go and find out why
// this is dead". Wrong work, from a confident-sounding report.
//
// The reach is worth knowing and it is not worth that. As a note it still tells
// the reader where to start; when the note is wrong, the reader is looking at a
// survivor with a misleading hint beside it rather than at the wrong task
// entirely. What the note may not do is sound more certain than the measurement
// behind it, which is why it says where it came from and hedges.

func (o Outcome) String() string {
	switch o {
	case Caught:
		return "caught"
	case Survived:
		return "SURVIVED"
	case Broken:
		return "did not compile"
	case Inconclusive:
		return "inconclusive"
	}
	// An outcome this switch has never been told about. Reading it as one of the
	// four would be the quiet kind of wrong this package refuses elsewhere:
	// "inconclusive" names a run that happened and said nothing, and a value
	// nobody has named here is not that.
	return fmt.Sprintf("outcome %d", int(o))
}

// Result pairs a mutation with what happened.
type Result struct {
	Mutation Mutation
	Outcome  Outcome
	Killers  []string // the tests that failed, by name
	Detail   string   // why, when the outcome is not a plain caught/survived

	// TestOutput is everything the test run wrote, stdout and stderr together,
	// and empty when no tests ran (a mutation that would not build). The
	// outcome is the summary; this is the transcript it was read from, kept
	// because a mutation can be built to say something through the run itself
	// — a probe prints what it saw to stderr — and the outcome alone would
	// throw those words away.
	TestOutput string
}

// namesItsKillers says whether a row with this outcome carries the tests that
// failed. Report prints them from this answer and Tally counts them from the
// same one: a column naming a test that no row above names would be one report
// disagreeing with itself, which is the accident Tally exists to make
// impossible.
//
// Every other outcome puts something else in that cell — "none", "n/a", a
// reason — and what they have in common is the only thing worth writing down
// here: none of them names a test.
func namesItsKillers(o Outcome) bool { return o == Caught }

// Report renders the results as a markdown table, ready to paste into a pull
// request. A mutation that survived says so in the table rather than being left
// out, because that row is the reason to run this at all.
func Report(rs []Result) string {
	if len(rs) == 0 {
		// No rows, no table. A run that stopped before it measured anything — a
		// red baseline, a mutation that would not apply — would otherwise print
		// the header on its own, which reads like a sweep that found nothing to
		// say rather than one that never started.
		return ""
	}
	var b strings.Builder
	b.WriteString("| mutation | outcome | tests that caught it |\n|---|---|---|\n")
	for _, r := range rs {
		// Starting from "no name for it" rather than from the killers: a switch
		// with no default printed the killers of an outcome it had never been
		// told about, while Tally left that same name out — the two halves of one
		// report disagreeing, in the function whose comment says they cannot.
		killers := "n/a (an outcome this table has no name for)"
		switch {
		case namesItsKillers(r.Outcome):
			killers = cell(strings.Join(r.Killers, ", "))
		case r.Outcome == Survived:
			killers = "**none — this defect is invisible to the suite**"
			if r.Detail != "" {
				// A survivor with something to add: the reach could not be measured,
				// so "invisible to the suite" is the louder of two readings rather
				// than an established one. Said here, in the table, because the table
				// is what gets pasted into a pull request — a note that only ever
				// appears in the running log is a note nobody reads.
				killers += " (" + cell(r.Detail) + ")"
			}
		case r.Outcome == Broken:
			// Why it would not build, in the table. The reason was captured all
			// along and printed only in the running log, so a table pasted into
			// a pull request said "it did not compile" and stopped — and this is
			// the row where a reader most needs the next step, because nothing
			// about the tests was measured.
			killers = "n/a (not evidence: no test ran"
			if r.Detail != "" {
				killers += " — " + cell(r.Detail)
			}
			killers += ")"
		case r.Outcome == Inconclusive:
			killers = "n/a (the run named no tests: " + cell(r.Detail) + ")"
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s |\n", cell(r.Mutation.Name), r.Outcome, killers))
	}
	return b.String()
}

// Tally counts what the table above already contains, so that the number a pull
// request quotes and the rows it was read from cannot disagree.
//
// They have disagreed. A body once carried a total of twenty-six beside a
// breakdown that added to twenty-five, and neither number was wrong on its own —
// nobody had added the second one up. Both halves were typed by hand beside a
// table that already held the rows they were counting.
//
// The outcomes are counted by iterating the results rather than by asking after
// each kind in turn: a kind added later shows up here without this function
// being told about it, and the parts add to the total because every result
// contributes to exactly one of them.
func Tally(rs []Result) string {
	if len(rs) == 0 {
		return ""
	}
	byOutcome := map[Outcome]int{}
	order := []Outcome{}
	perTest := map[string]int{}
	for _, r := range rs {
		if _, seen := byOutcome[r.Outcome]; !seen {
			order = append(order, r.Outcome)
		}
		byOutcome[r.Outcome]++
		// The same answer the table used, not a second copy of the rule. Written
		// out twice, one of them was wrong: this said the table printed "none"
		// for every other outcome, and the table had no default at all.
		if namesItsKillers(r.Outcome) {
			for _, k := range r.Killers {
				perTest[k]++
			}
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	parts := make([]string, 0, len(order))
	for _, o := range order {
		parts = append(parts, fmt.Sprintf("%d %s", byOutcome[o], o))
	}
	var b strings.Builder
	noun := "mutations"
	if len(rs) == 1 {
		noun = "mutation"
	}
	fmt.Fprintf(&b, "\n**%d %s: %s.**\n", len(rs), noun, strings.Join(parts, ", "))
	if len(perTest) == 0 {
		return b.String()
	}
	names := make([]string, 0, len(perTest))
	for n := range perTest {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if perTest[names[i]] != perTest[names[j]] {
			return perTest[names[i]] > perTest[names[j]]
		}
		return names[i] < names[j]
	})
	b.WriteString("\n| test | mutations it caught |\n|---|---|\n")
	for _, n := range names {
		fmt.Fprintf(&b, "| %s | %d |\n", cell(n), perTest[n])
	}
	b.WriteString("\nEach row counts the mutations that test failed on; a mutation two tests " +
		"caught appears in both rows.\n")
	return b.String()
}

// cell escapes what would otherwise end a markdown column early.
func cell(s string) string { return strings.ReplaceAll(oneLine(s), "|", `\|`) }

// oneLine folds a multi-line detail into something a table row can hold.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
}

// LineOf returns the 1-based line that byte offset idx falls on.
func LineOf(src []byte, idx int) int {
	if idx < 0 || idx > len(src) {
		return 0
	}
	return bytes.Count(src[:idx], []byte("\n")) + 1
}

// Pos is a place in a file: a line, and the column the text of interest starts
// at. Both count from one, the way a coverage profile writes them.
type Pos struct {
	Line, Col int
}

// Executed reports whether a coverage profile shows the given place being run,
// and whether it could tell at all.
//
// The profile names files by import path and the sweep names them by path from
// the root of the tree, so the two are matched by their tails. That match can be
// ambiguous — a sweep naming `types.go` matches every `types.go` in the module —
// and it can find nothing, if the package never made it into the profile.
//
// Both of those return decided=false, and the caller says nothing rather than
// something it cannot support.
func Executed(profile []byte, file string, at Pos) (ran, decided bool) {
	// Cleaned first: a sweep that writes "./m.go" names the same file as one that
	// writes "m.go", and a suffix comparison that keeps the "./" matches neither
	// profile line — which would turn the reach check off without saying so.
	want := "/" + strings.TrimPrefix(filepath.ToSlash(filepath.Clean(file)), "/")
	files := map[string]bool{}
	type block struct {
		start, end Pos
		count      int
	}
	var blocks []block
	for _, l := range strings.Split(string(profile), "\n") {
		colon := strings.LastIndex(l, ":")
		if colon < 0 || !strings.HasSuffix(l[:colon], want) {
			continue
		}
		var b block
		var stmts int
		if _, err := fmt.Sscanf(l[colon+1:], "%d.%d,%d.%d %d %d",
			&b.start.Line, &b.start.Col, &b.end.Line, &b.end.Col, &stmts, &b.count); err != nil {
			continue
		}
		files[l[:colon]] = true
		blocks = append(blocks, b)
	}
	if len(files) != 1 {
		return false, false
	}
	// Columns, not just lines. A block runs from one column of one line to another
	// column of another, and both ends can fall in the middle of a line that has
	// more on it: the block for an if-body ends at the `}` and the `else` that
	// follows on the same line belongs to no block at all. Matched by line alone,
	// that `else` inherits the if-body's count — zero, on the run where the else
	// branch is the one that was taken — and a line the tests take every time is
	// written up as one nothing reaches.
	//
	// A place no block covers is a place the profile says nothing about, and that
	// is not the same as a place it says nothing ran. Which of the two it is
	// changes with the compiler: Go 1.26 opened the block for a `case` clause on
	// the `case` line, Go 1.27 opens it on the line after. Answered as "no test
	// runs this", a toolchain upgrade alone turns a live survivor into a report
	// about unreachable code.
	covered := false
	for _, b := range blocks {
		if before(at, b.start) || !before(at, b.end) {
			continue
		}
		covered = true
		if b.count > 0 {
			return true, true
		}
	}
	return false, covered
}

// before reports whether a comes strictly before b in a file.
func before(a, b Pos) bool {
	return a.Line < b.Line || (a.Line == b.Line && a.Col < b.Col)
}
