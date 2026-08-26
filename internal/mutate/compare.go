package mutate

import (
	"fmt"
	"sort"
	"strings"
)

// This file is the difference between two sweeps: the same mutations, applied
// to the tree as it stands and to the tree before the change. What a pull
// request wants to quote is not either table but the difference — what this
// change newly guards, what was guarded anyway, what still is not — and every
// time that difference was assembled by hand, a number went wrong: a total
// beside a breakdown that added to something else, a count from an earlier
// draft, a category quietly holding rows from two. So the difference is a
// report the machine writes, whose totals are sums over its own rows.

// The categories a compared pair of sweeps can put a mutation in. A closed
// set, tested as one: a category added here without being added there is a red
// test, so prose that says "the categories are these" cannot quietly fall
// behind the list.
const (
	// Caught now, survived before: what this change newly guards. The reason
	// to have run the comparison.
	NewlyCaught = "newly caught by this change"
	// Caught in both trees: guarded, but not by this change. Quoting these as
	// wins is the overstatement the comparison exists to prevent.
	AlreadyCaught = "already caught before this change"
	// Survived in both trees: still open, and not this change's doing.
	StillSurvives = "still survives, as it did before"
	// Caught before, survived now: this change let a guarded defect through.
	// The loudest row in the report — a regression in the suite's reach.
	CaughtBeforeSurvivesNow = "was caught before this change, and now survives"
	// The mutation does not apply to the tree before the change — the text it
	// rewrites was written by the change itself. Not an error: a sweep built
	// against the new tree is expected to name new sentences.
	NotPresentBefore = "not present before this change"
	// The text is in the tree before the change more than once, so the
	// comparison cannot name the one place a baseline run would have
	// mutated. Its own heading, because "not present" would be the opposite
	// of the truth — the sentence was there, twice.
	AmbiguousBefore = "written more than once before this change"
	// One of the two runs did not measure: a mutation that would not build, or
	// a run that named nobody. The pair of outcomes is shown as it fell;
	// counting these into any other category would dress "unmeasured" as an
	// answer.
	NotMeasuredCleanly = "not measured cleanly"
	// A row one side has and the other does not, other than by NotPresentBefore
	// — two sweeps that were not the same sweep. Loud, because every quiet
	// explanation for it is a bug in this file.
	Unmatched = "in one sweep but not the other"
)

// A NewSite is a mutation the baseline tree cannot carry, and why — usually
// "the pattern appears 0 times", because the change wrote the sentence the
// mutation rewrites.
type NewSite struct {
	Mutation Mutation
	Reason   string
}

// Applicable splits mutations three ways: the ones the baseline tree can
// carry, the ones whose text that tree does not hold (fresh — the change
// wrote the sentence), and the ones whose text it holds more than once
// (ambiguous — no one place a baseline run would have mutated). read reaches
// the baseline tree and says whether the file exists there at all — a file
// the change added is a legitimate kind of absent, where any other read
// error is not: "could not look" stops everything rather than being filed
// as "was not there".
func Applicable(ms []Mutation, read func(string) (body []byte, exists bool, err error)) (ok []Mutation, fresh, ambiguous []NewSite, err error) {
	type cached struct {
		body   string
		exists bool
	}
	text := map[string]cached{}
	for _, m := range ms {
		c, seen := text[m.File]
		if !seen {
			b, exists, rerr := read(m.File)
			if rerr != nil {
				return nil, nil, nil, fmt.Errorf("%s: %w", m.File, rerr)
			}
			c = cached{body: string(b), exists: exists}
			text[m.File] = c
		}
		switch n := strings.Count(c.body, m.From); {
		case !c.exists:
			fresh = append(fresh, NewSite{Mutation: m, Reason: "the file itself is new in this change"})
		case n == 0:
			fresh = append(fresh, NewSite{Mutation: m, Reason: "the pattern appears 0 times in that tree"})
		case n > 1:
			ambiguous = append(ambiguous, NewSite{Mutation: m,
				Reason: fmt.Sprintf("the pattern appears %d times in that tree", n)})
		default:
			ok = append(ok, m)
		}
	}
	return ok, fresh, ambiguous, nil
}

// CompareReport files every mutation into one category and renders the
// difference, counts included. The totals are sums over the rows printed —
// there is no second place the numbers come from.
func CompareReport(ref string, now, before []Result, fresh, ambiguous []NewSite) string {
	prev := map[string]Result{}
	for _, r := range before {
		prev[r.Mutation.Name] = r
	}
	sites := map[string]NewSite{}
	siteCat := map[string]string{}
	for _, f := range fresh {
		sites[f.Mutation.Name], siteCat[f.Mutation.Name] = f, NotPresentBefore
	}
	for _, a := range ambiguous {
		sites[a.Mutation.Name], siteCat[a.Mutation.Name] = a, AmbiguousBefore
	}

	rows := map[string][]string{}
	file := func(cat, row string) { rows[cat] = append(rows[cat], row) }

	seen := map[string]bool{}
	for _, r := range now {
		seen[r.Mutation.Name] = true
		if cat, isSite := siteCat[r.Mutation.Name]; isSite {
			// The reason rides on the row — "appears 0 times" and "appears 2
			// times" are different facts, and a row whose grounds are not on
			// it is a row nobody can check.
			file(cat, fmt.Sprintf("%s — now: %s (%s)", r.Mutation.Name, r.Outcome, sites[r.Mutation.Name].Reason))
			continue
		}
		b, ok := prev[r.Mutation.Name]
		if !ok {
			file(Unmatched, r.Mutation.Name+" — ran against this tree only")
			continue
		}
		switch {
		case r.Outcome != Caught && r.Outcome != Survived,
			b.Outcome != Caught && b.Outcome != Survived:
			file(NotMeasuredCleanly, fmt.Sprintf("%s — before: %s, now: %s", r.Mutation.Name, b.Outcome, r.Outcome))
		case r.Outcome == Caught && b.Outcome == Survived:
			file(NewlyCaught, r.Mutation.Name)
		case r.Outcome == Caught && b.Outcome == Caught:
			file(AlreadyCaught, r.Mutation.Name)
		case r.Outcome == Survived && b.Outcome == Survived:
			file(StillSurvives, r.Mutation.Name)
		default: // survived now, caught before
			file(CaughtBeforeSurvivesNow, r.Mutation.Name)
		}
	}
	for _, b := range before {
		if !seen[b.Mutation.Name] {
			file(Unmatched, b.Mutation.Name+" — ran against the baseline only")
		}
	}
	for name := range sites {
		if !seen[name] {
			// Applicable dropped it from the baseline sweep and the current
			// sweep never produced a row either — still named, not swallowed.
			file(Unmatched, name+" — filed against the baseline only, and the current sweep has no row for it")
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n## Against %s\n", ref)
	total := 0
	for _, cat := range Categories {
		rs := rows[cat]
		if len(rs) == 0 {
			continue
		}
		total += len(rs)
		fmt.Fprintf(&b, "\n**%s — %d**\n\n", cat, len(rs))
		sort.Strings(rs)
		for _, r := range rs {
			fmt.Fprintf(&b, "- %s\n", r)
		}
	}
	// The total is the sum of the section counts because it is computed from
	// the same rows, in the same pass. That sentence is this file's reason to
	// exist — and it has a failure mode of its own: a row filed under a
	// category the list above does not carry would vanish without lowering
	// any count. So the two tallies are compared, and a mismatch is printed
	// as the loudest thing on the page rather than absorbed.
	filed := 0
	for _, rs := range rows {
		filed += len(rs)
	}
	if filed != total {
		fmt.Fprintf(&b, "\n**BUG IN THIS REPORT: %d rows were filed and %d printed — a category "+
			"is missing from the printed list, and the counts above cannot be trusted.**\n", filed, total)
	}
	fmt.Fprintf(&b, "\n**%d in all.**\n", total)
	return b.String()
}

// Categories, in the order the report prints them: findings first, then
// context, then the rows that mean the comparison itself needs attention.
var Categories = []string{
	NewlyCaught,
	CaughtBeforeSurvivesNow,
	StillSurvives,
	AlreadyCaught,
	NotPresentBefore,
	AmbiguousBefore,
	NotMeasuredCleanly,
	Unmatched,
}
