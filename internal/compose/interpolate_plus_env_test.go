package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The `.env` file next to the compose file gives `${E:+x}` and `${E+y}` the variable the same way
// the environment does (measured, v5.5.1: `E=val` gives x and y, `E=` gives nothing and y, no `E`
// gives nothing and nothing).
func TestAnAlternativeValueIsGivenByAVariableFromTheDotEnvFile(t *testing.T) {
	for _, tc := range []struct {
		name, dotenv, want string
	}{
		{"E has a value", "E=val\n", "x\x00y"},
		{"E is empty", "E=\n", "\x00y"},
		{"E is not there", "F=1\n", "\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unsetEnv(t, "E")
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(tc.dotenv), 0o644); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(p, []byte("services:\n  web:\n    image: alpine\n    command: [\"${E:+x}\", \"${E+y}\"]\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			proj, err := Load(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(proj.Services["web"].Command, "\x00"); got != tc.want {
				t.Errorf("command = %q, want %q", got, tc.want)
			}
		})
	}
}

// An alternative value inside a default: `${E:-${F:+y}}` and `${E-${F:+z}}` (measured, v5.5.1).
// The default is read only when it is taken, and what it holds is read by the same rules.
func TestAnAlternativeValueInsideADefaultIsRead(t *testing.T) {
	for _, tc := range []struct {
		name, e, f string // "unset", "empty" or a value
		want       string
	}{
		{"E unset, F unset", "unset", "unset", "\x00"},
		{"E unset, F set", "unset", "fv", "y\x00z"},
		{"E empty, F set", "empty", "fv", "y\x00"},
		{"E empty, F unset", "empty", "unset", "\x00"},
		{"E set, F set", "ev", "fv", "ev\x00ev"},
		{"E set, F unset", "ev", "unset", "ev\x00ev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := func(name, state string) {
				switch state {
				case "unset":
					unsetEnv(t, name)
				case "empty":
					t.Setenv(name, "")
				default:
					t.Setenv(name, state)
				}
			}
			set("E", tc.e)
			set("F", tc.f)
			p := writeTemp(t, "services:\n  web:\n    image: alpine\n    command: [\"${E:-${F:+y}}\", \"${E-${F:+z}}\"]\n")
			proj, err := Load(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(proj.Services["web"].Command, "\x00"); got != tc.want {
				t.Errorf("command = %q, want %q", got, tc.want)
			}
		})
	}
}
