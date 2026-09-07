package compose

// A variable's value in the mapping form of `environment` and `build.args`
// (#784, the last of the sweep's differences). docker compose (v5.5.0)
// refuses a list, a mapping or an empty list there (`services.web.environment.A
// must be a boolean, null, number or string`) and an infinity or a NaN
// (`json: unsupported value: +Inf`); it takes a string, a number (written out in decimal, as this does:
// `1e3` → 1000, `0x10` → 16), a boolean, a date (written out the way Go
// prints a time, as docker does) and null (the variable taken from the
// shell). opossum wrote a list out as `A=[1]`, a mapping as `A=map[b:c]`,
// an infinity as `A=+Inf`. What a `${VAR}` expands to is text, so a shell
// value that is YAML flow syntax (`[1]`) is the value "[1]", as docker
// compose reads it (see expandtext_test.go). With several -f
// files the check runs on the merged document: a later file's list value
// is refused here where docker compose, which validates each file alone
// and not the merge, writes it out as Go does.

import (
	"strings"
	"testing"
)

func TestAVariableWrittenAsAListOrAMappingIsRefused(t *testing.T) {
	env := "services:\n  web:\n    image: alpine\n    environment:\n      "
	args := "services:\n  web:\n    build:\n      context: .\n      args:\n        "
	shape := "environment variable A must be a string, a number, a boolean or null, got "
	for _, tc := range []struct{ name, body, want string }{
		{"a list", env + "A: [1]\n", shape + "a list"},
		{"a mapping", env + "A: {b: c}\n", shape + "a mapping"},
		{"an empty list", env + "A: []\n", shape + "a list"},
		{"an empty mapping", env + "A: {}\n", shape + "a mapping"},
		{"an infinity", env + "A: .inf\n", shape + ".inf — quote it (`\".inf\"`)"},
		{"a NaN", env + "A: .nan\n", shape + ".nan"},
		{"through an alias", "x-l: &l [1]\n" + env + "A: *l\n", shape + "a list"},
		{"the second variable", env + "A: 1\n      B: [2]\n", "environment variable B must be"},
		{"in build.args", args + "A: [1]\n", "build.args variable A must be a string, a number, a boolean or null, got a list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
		})
	}
	// Taken, as docker takes them, and written out the same way.
	for _, tc := range []struct{ name, value, want string }{
		{"a string", "hello", "A=hello"},
		{"a number", "1", "A=1"},
		{"a float with a trailing zero", "1.50", "A=1.5"},
		{"an exponent", "1e3", "A=1000"},
		{"hex", "0x10", "A=16"},
		{"a boolean", "true", "A=true"},
		{"a quoted number", "\"1\"", "A=1"},
		{"yes, a string in YAML 1.2", "yes", "A=yes"},
		{"an empty string", "\"\"", "A="},
		{"null, from the shell", "~", "A"},
		{"a date, as docker writes it", "2020-01-01", "A=2020-01-01 00:00:00 +0000 UTC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, env+"A: "+tc.value+"\n"))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := p.Services["web"].Environment; len(got) != 1 || got[0] != tc.want {
				t.Errorf("environment = %v, want [%s]", got, tc.want)
			}
		})
	}
}
