package mutate

// Evals for what a sweep says about its own work.
//
// A report that only names defects leaves the reader to supply the rest: how
// many mutations that count is out of, whether a green row is a hole or a
// control doing its job, which tree was measured. Each of those was supplied
// wrongly at least once — a control counted as a survivor, a denominator one
// too high, a sweep run against yesterday's commit — and none of them was
// visible in the output.

import (
	"strings"
	"testing"
)

func resultsFor(kinds ...struct {
	control  bool
	survived bool
}) []Result {
	var rs []Result
	for i, k := range kinds {
		outcome := Caught
		killers := []string{"TestSomething"}
		if k.survived {
			outcome, killers = Survived, nil
		}
		name := "a defect"
		if k.control {
			name = "a control"
		}
		rs = append(rs, Result{
			Mutation: Mutation{Name: name + string(rune('A'+i)), Control: k.control},
			Outcome:  outcome,
			Killers:  killers,
		})
	}
	return rs
}

func caughtDefect() struct{ control, survived bool } {
	return struct{ control, survived bool }{false, false}
}
func survivingDefect() struct{ control, survived bool } {
	return struct{ control, survived bool }{false, true}
}
func survivingControl() struct{ control, survived bool } {
	return struct{ control, survived bool }{true, true}
}
func caughtControl() struct{ control, survived bool } {
	return struct{ control, survived bool }{true, false}
}

func TestAControlIsNotCountedAmongTheMutationsItVouchesFor(t *testing.T) {
	rs := resultsFor(caughtDefect(), caughtDefect(), survivingControl())
	out := Report(rs) + Tally(rs)

	t.Run("the count is of the mutations, not of the controls", func(t *testing.T) {
		// Two defects, both caught. Saying "3 mutations: 2 caught, 1
		// SURVIVED" reads as a hole and puts full marks out of reach.
		if want := "**2 mutations: 2 caught.**"; !strings.Contains(out, want) {
			t.Errorf("the tally does not say %q — a control in the denominator means a "+
				"sweep where every row went right cannot report every row going right:\n%s",
				want, out)
		}
	})
	t.Run("the control is counted, apart", func(t *testing.T) {
		if want := "1 control: all as expected"; !strings.Contains(out, want) {
			t.Errorf("the tally does not account for the control (%q). Leaving it out "+
				"entirely would hide that the sweep was checked at all:\n%s", want, out)
		}
	})
	t.Run("a surviving control is not called SURVIVED", func(t *testing.T) {
		// The same word for "nothing caught this defect" and "the sweep
		// works" is what made a control unreadable in the table.
		if strings.Contains(out, "| a control") && !strings.Contains(out, "control ok") {
			t.Errorf("the control's row does not say `control ok`:\n%s", out)
		}
		if strings.Contains(out, "| a controlC | SURVIVED") {
			t.Errorf("the control is printed with the word a defect nothing caught gets:\n%s", out)
		}
	})
	t.Run("nothing is said to have gone the wrong way", func(t *testing.T) {
		if got := Wrong(rs); len(got) != 0 {
			t.Errorf("Wrong() = %v, want none — two defects caught and a control that "+
				"survived is a sweep in which every row did what it was written to do", got)
		}
	})
}

func TestAControlThatWasCaughtIsTheLoudestThingInTheReport(t *testing.T) {
	// The one outcome that makes every other row meaningless: the edits are
	// reaching something other than what the rows describe, so "caught" and
	// "survived" beside it are about a sweep nobody can vouch for.
	rs := resultsFor(caughtDefect(), caughtControl())
	out := Report(rs) + Tally(rs)

	if !strings.Contains(out, "CONTROL CAUGHT") {
		t.Errorf("the report does not say the control was caught:\n%s", out)
	}
	if !strings.Contains(out, "changed the answer") {
		t.Errorf("the report does not say what a caught control means:\n%s", out)
	}
	if got := Wrong(rs); len(got) != 1 || !got[0].Mutation.Control {
		t.Errorf("Wrong() = %v, want the control alone — the defect beside it was caught", got)
	}
}

func TestASurvivingDefectStillGoesTheWrongWay(t *testing.T) {
	// The original reason this tool exists. Controls must not soften it.
	rs := resultsFor(caughtDefect(), survivingDefect(), survivingControl())
	if got := Wrong(rs); len(got) != 1 || got[0].Mutation.Control {
		t.Errorf("Wrong() = %v, want the surviving defect alone", got)
	}
	if want := "**2 mutations: 1 caught, 1 SURVIVED.**"; !strings.Contains(Tally(rs), want) {
		t.Errorf("the tally does not say %q:\n%s", want, Tally(rs))
	}
}

func TestWhatWentTheWrongWayIsNamedAgainAtTheEnd(t *testing.T) {
	// The table is long, and the last lines of the output are what a reader
	// takes when they pipe it — or paste it. Keeping the totals and losing
	// which rows they are about cost a re-run of the whole sweep once.
	rs := resultsFor(caughtDefect(), survivingDefect(), survivingControl())
	tail := Tally(rs)
	if !strings.Contains(tail, "Went the wrong way:") {
		t.Errorf("the tally does not list what went wrong at the end:\n%s", tail)
	}
	if !strings.Contains(tail, "a defectB") {
		t.Errorf("the list at the end does not name the survivor:\n%s", tail)
	}
	if strings.Contains(tail, "a defectA") {
		t.Errorf("the list at the end names a mutation that was caught:\n%s", tail)
	}
	// After the totals, not before: a reader who takes the last lines has to
	// get the names, and a reader who takes the first gets the table anyway.
	if strings.Index(tail, "Went the wrong way:") < strings.Index(tail, "mutations:") {
		t.Errorf("the list comes before the totals, so cutting the output keeps the "+
			"names and loses the counts — the wrong way round:\n%s", tail)
	}
}

// Two controls, one of them caught. With one control the count and the
// report read the same whether the code counts the caught ones or merely
// notices that not all of them survived; with two they part company.
func TestTwoControlsAreCountedOneByOne(t *testing.T) {
	rs := resultsFor(caughtDefect(), survivingControl(), caughtControl())
	tally := Tally(rs)
	if want := "**1 of 2 controls changed the answer.**"; !strings.Contains(tally, want) {
		t.Errorf("the tally does not say %q — with one of two controls caught, saying "+
			"\"all as expected\" or \"2 of 2\" are both wrong:\n%s", want, tally)
	}
	if strings.Contains(tally, "all as expected") {
		t.Errorf("the tally says every control was as expected while one was caught:\n%s", tally)
	}
}

// Report and Tally each say the control's outcome, and each has to say it on
// its own: read as one string, either half can carry the other.
func TestTheReportAndTheTallyEachNameACaughtControl(t *testing.T) {
	rs := resultsFor(caughtDefect(), caughtControl())
	t.Run("the table", func(t *testing.T) {
		if got := Report(rs); !strings.Contains(got, "CONTROL CAUGHT") ||
			!strings.Contains(got, "the sweep is not measuring what it says") {
			t.Errorf("the table alone does not say the control was caught, nor what that "+
				"means:\n%s", got)
		}
	})
	t.Run("the list at the end", func(t *testing.T) {
		// Separately from the table: a reader who keeps the last lines has
		// to see that a control is among what went wrong.
		tail := Tally(rs)
		if !strings.Contains(tail, "Went the wrong way:") || !strings.Contains(tail, "a controlB") {
			t.Errorf("the tally's closing list does not name the caught control:\n%s", tail)
		}
	})
}

// A control that never ran. "It would not build" and "the answer changed"
// are the two things this package keeps apart, and a control is not exempt:
// a row that measured nothing has neither vouched for the sweep nor failed
// to.
func TestAControlThatMeasuredNothingIsNotSaidToHaveChangedTheAnswer(t *testing.T) {
	for _, outcome := range []Outcome{Broken, Inconclusive} {
		t.Run(outcome.String(), func(t *testing.T) {
			rs := []Result{
				{Mutation: Mutation{Name: "a defect"}, Outcome: Caught, Killers: []string{"TestX"}},
				{Mutation: Mutation{Name: "a control", Control: true}, Outcome: outcome},
			}
			out := Report(rs) + Tally(rs)
			if strings.Contains(out, "CONTROL CAUGHT") || strings.Contains(out, "changed the answer") {
				t.Errorf("a control that %s is reported as having changed the answer. "+
					"Nothing was asked of it:\n%s", outcome, out)
			}
			if !strings.Contains(out, outcome.String()) {
				t.Errorf("the report does not say the control %s:\n%s", outcome, out)
			}
			if got := Wrong(rs); len(got) != 0 {
				t.Errorf("Wrong() = %v; a row that measured nothing did not go the wrong "+
					"way, it went nowhere — the exit status for that is the one about "+
					"measuring nothing", got)
			}
		})
	}
}
