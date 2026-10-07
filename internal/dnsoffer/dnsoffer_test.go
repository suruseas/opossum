package dnsoffer_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/dnsoffer"
)

// offerWith is an Offer at a terminal, answering `input`, whose Create is counted and whose Valid is the real rule.
func offerWith(input string, createErr error) (*dnsoffer.Offer, *bytes.Buffer, *[]string) {
	var out bytes.Buffer
	var created []string
	return &dnsoffer.Offer{
		Interactive: func() bool { return true },
		In:          strings.NewReader(input),
		Out:         &out,
		Valid:       func(d string) bool { return d != "" && !strings.HasPrefix(d, "-") && !strings.ContainsAny(d, " ;/\n") },
		Command:     func(d string) string { return "sudo container system dns create " + d },
		Create:      func(d string) error { created = append(created, d); return createErr },
	}, &out, &created
}

// sudo is run for a yes and for nothing else: `y` and `yes`, in any case and with the blanks round them, and the end of the input after them. Everything else —
// an empty line, the end of the input, `n`, and every other word, however much it sounds like consent — is a no, and what is a no runs nothing (the rule
// of #1907: no sudo without having asked, and the default of the question is No).
func TestSudoIsRunForAYesAndForNothingElse(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		want        dnsoffer.Outcome
		runs        int
	}{
		{"y", "y\n", dnsoffer.Created, 1},
		{"Y", "Y\n", dnsoffer.Created, 1},
		{"yes", "yes\n", dnsoffer.Created, 1},
		{"YES with blanks", "  YES \n", dnsoffer.Created, 1},
		{"y and the input ends", "y", dnsoffer.Created, 1},
		{"an empty line is the default, which is No", "\n", dnsoffer.Declined, 0},
		{"the input ends with nothing typed", "", dnsoffer.Declined, 0},
		{"n", "n\n", dnsoffer.Declined, 0},
		{"no", "no\n", dnsoffer.Declined, 0},
		{"yes please is not yes", "yes please\n", dnsoffer.Declined, 0},
		{"yy", "yy\n", dnsoffer.Declined, 0},
		{"ye", "ye\n", dnsoffer.Declined, 0},
		{"sure", "sure\n", dnsoffer.Declined, 0},
		{"1", "1\n", dnsoffer.Declined, 0},
		{"only blanks", "   \n", dnsoffer.Declined, 0},
		{"a no, and a yes on the line after it", "n\ny\n", dnsoffer.Declined, 0},
		{"a yes after an empty line", "\ny\n", dnsoffer.Declined, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _, created := offerWith(tc.input, nil)
			if got := o.Ask("opossum"); got != tc.want {
				t.Errorf("outcome %v, want %v", got, tc.want)
			}
			if len(*created) != tc.runs {
				t.Errorf("sudo was run %d times, want %d: %v", len(*created), tc.runs, *created)
			}
		})
	}
}

// Not at a terminal nothing is asked and nothing is run, whatever the input says: an agent, CI, a pipe with a `y` in it. And a domain that is not a name for sudo
// is not asked about.
func TestNothingIsAskedOrRunWhereItIsNotAtATerminalOrTheNameIsNotOne(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(o *dnsoffer.Offer)
		domain string
	}{
		{"not a terminal", func(o *dnsoffer.Offer) { o.Interactive = func() bool { return false } }, "opossum"},
		{"no way to tell is not a terminal", func(o *dnsoffer.Offer) { o.Interactive = nil }, "opossum"},
		{"a name that starts with a hyphen is an option to sudo's program", func(o *dnsoffer.Offer) {}, "-x"},
		{"a name with a space", func(o *dnsoffer.Offer) {}, "a b"},
		{"a name with a semicolon", func(o *dnsoffer.Offer) {}, "a;b"},
		{"no name", func(o *dnsoffer.Offer) {}, ""},
		{"no rule for names", func(o *dnsoffer.Offer) { o.Valid = nil }, "opossum"},
		{"no input", func(o *dnsoffer.Offer) { o.In = nil }, "opossum"},
		{"no output", func(o *dnsoffer.Offer) { o.Out = nil }, "opossum"},
		{"no command to show", func(o *dnsoffer.Offer) { o.Command = nil }, "opossum"},
		{"nothing to run", func(o *dnsoffer.Offer) { o.Create = nil }, "opossum"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, out, created := offerWith("y\nyes\n", nil)
			tc.mutate(o)
			if got := o.Ask(tc.domain); got != dnsoffer.NotAsked {
				t.Errorf("outcome %v, want NotAsked", got)
			}
			if len(*created) != 0 {
				t.Errorf("sudo was run: %v", *created)
			}
			if out.Len() != 0 {
				t.Errorf("a question was written where none is asked: %q", out.String())
			}
		})
	}
	var none *dnsoffer.Offer
	if got := none.Ask("opossum"); got != dnsoffer.NotAsked {
		t.Errorf("no offer asks nothing, got %v", got)
	}
	if got := (&dnsoffer.Offer{}).Ask("opossum"); got != dnsoffer.NotAsked {
		t.Errorf("a zero offer asks nothing, got %v", got)
	}
}

// What is asked is the command that is run, and what happens to a failed one is said.
func TestTheQuestionNamesTheCommandThatIsRunAndAFailureIsSaid(t *testing.T) {
	o, out, created := offerWith("yes\n", errors.New("`sudo container system dns create opossum` did not succeed: exit status 1"))
	if got := o.Ask("opossum"); got != dnsoffer.Failed {
		t.Fatalf("outcome %v, want Failed", got)
	}
	s := out.String()
	for _, want := range []string{
		`DNS domain "opossum" not found`,
		"Create it now? This runs: sudo container system dns create opossum [y/N] ",
		"did not succeed: exit status 1",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("want %q in what was written: %q", want, s)
		}
	}
	if strings.Contains(s, "created.") {
		t.Errorf("a failed command is not said to have created the domain: %q", s)
	}
	if len(*created) != 1 || (*created)[0] != "opossum" {
		t.Errorf("Create was given %v, want the domain once", *created)
	}
	o2, out2, _ := offerWith("y\n", nil)
	if got := o2.Ask("opossum"); got != dnsoffer.Created || !strings.Contains(out2.String(), `DNS domain "opossum" created.`) {
		t.Errorf("a command that worked is said to have: %v %q", got, out2.String())
	}
	// The input that ends where the question stopped does not leave the next line on the question's.
	o3, out3, _ := offerWith("", nil)
	o3.Ask("opossum")
	if !strings.HasSuffix(out3.String(), "[y/N] \n") {
		t.Errorf("the line is ended when the input ends: %q", out3.String())
	}
}
