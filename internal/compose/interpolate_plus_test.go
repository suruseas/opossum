package compose

import (
	"os"
	"strings"
	"testing"
)

// `${VAR:+word}` and `${VAR+word}` give word when the variable is set — and, with the colon, not
// empty — and nothing when it is not (#1628). They were refused as `invalid variable name`. Every
// value below is what docker compose v5.5.1 printed for the same `command` (measured, with the
// variable unset, set to nothing, and set to `ev`, and F set to `fv`).
func TestAnAlternativeValueIsGivenWhenTheVariableIsSet(t *testing.T) {
	const unset, empty, set = "unset", "empty", "set"
	for _, tc := range []struct {
		name, word string
		unset      string // what it gives with E not set
		empty      string // with E set to nothing
		set        string // with E set to ev
	}{
		{"${E:+x}", "${E:+x}", "", "", "x"},
		{"${E+x}", "${E+x}", "", "x", "x"},
		{"${E:+} with no word", "${E:+}", "", "", ""},
		{"${E+} with no word", "${E+}", "", "", ""},
		{"a word of two words", "${E:+a b}", "", "", "a b"},
		{"a nested default in the word, F set", "${E:+${F:-y}}", "", "", "fv"},
		{"a bare variable in the word", "${E:+$F}", "", "", "fv"},
		{"a braced variable in the word", "${E:+${F}}", "", "", "fv"},
		{"the same reference twice", "${E:+x}${E:+y}", "", "", "xy"},
		{"in the middle of a word", "--a=${E:+x}", "--a=", "--a=", "--a=x"},
		{"a colon and a dash in the word", "${E:+x:y}", "", "", "x:y"},
		{"a dash in the word of the plain form", "${E+a-b}", "", "a-b", "a-b"},
		{"an escaped dollar in the word", "${E:+$$}", "", "", "$"},
		{"text after it", "${E:+}end", "end", "end", "end"},
		// A word of spaces is the value, as it is written: nothing trims it.
		{"a word of one space", "${E:+ }", "", "", " "},
		{"a word of one space, plain form", "${E+ }", "", " ", " "},
		{"a word with spaces around it", "${E:+ a }", "", "", " a "},
	} {
		for state, want := range map[string]string{unset: tc.unset, empty: tc.empty, set: tc.set} {
			t.Run(tc.name+", E "+state, func(t *testing.T) {
				switch state {
				case unset:
					unsetEnv(t, "E")
				case empty:
					t.Setenv("E", "")
				case set:
					t.Setenv("E", "ev")
				}
				t.Setenv("F", "fv")
				body := "services:\n  web:\n    image: alpine\n    command: [\"" + strings.ReplaceAll(tc.word, `"`, `\"`) + "\"]\n"
				p, err := Load(writeTemp(t, body))
				if err != nil {
					t.Fatalf("docker compose reads this, and it was refused: %v", err)
				}
				if got := strings.Join(p.Services["web"].Command, "\x00"); got != want {
					t.Errorf("command = %q, want %q", got, want)
				}
			})
		}
	}
}

// The word is read only when it is taken: a `${F:?…}` in it asks for F only then (measured: with E
// unset docker compose reads the file and gives nothing; with E set it fails for F).
func TestTheWordOfAnAlternativeIsReadOnlyWhenItIsTaken(t *testing.T) {
	const body = "services:\n  web:\n    image: alpine\n    command: [\"${E:+${F:?needF}}\"]\n"
	unsetEnv(t, "F")
	t.Run("E unset: the word is not read", func(t *testing.T) {
		unsetEnv(t, "E")
		p, err := Load(writeTemp(t, body))
		if err != nil {
			t.Fatalf("the word is not taken, so F is not asked for, and it was refused: %v", err)
		}
		if got := strings.Join(p.Services["web"].Command, "\x00"); got != "" {
			t.Errorf("command = %q, want nothing", got)
		}
	})
	t.Run("E set: the word is read and F is asked for", func(t *testing.T) {
		t.Setenv("E", "ev")
		if _, err := Load(writeTemp(t, body)); err == nil || !strings.Contains(err.Error(), "needF") {
			t.Errorf("F is required there, want a refusal naming needF, got: %v", err)
		}
	})
	t.Run("E set, F set: the word's value", func(t *testing.T) {
		t.Setenv("E", "ev")
		t.Setenv("F", "fv")
		p, err := Load(writeTemp(t, body))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(p.Services["web"].Command, "\x00"); got != "fv" {
			t.Errorf("command = %q, want fv", got)
		}
	})
}

// unsetEnv makes name not set for the test, and puts back what it was after.
func unsetEnv(t *testing.T, name string) {
	t.Helper()
	old, had := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv(name, old)
		}
	})
}
