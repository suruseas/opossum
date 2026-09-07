package compose

// A variable's name in the mapping form of `environment`, `build.args` and
// `labels`, and an `env_file` that is the empty string. docker compose
// (v5.5.0) refuses a name YAML reads as anything but a string — a number,
// a boolean, null (`non-string key in services.web.environment: 1`); read
// here into a string map, `1: a` became the variable "1", `true: a` the
// variable "true", `~: a` the variable "". A quoted name (`"1": a`) is a
// string and is taken; a `<<:` merge key is not a name, but the mapping
// it brings in is checked, since its names end up here (docker compose
// refuses them where they are written: `non-string key in x-c: 1`).
// `env_file: ""` docker compose fails on when it
// opens the file (`env file  not found`); opossum read it as the path ""
// and failed opening the project directory ("is a directory").

import (
	"strings"
	"testing"
)

func TestAVariableWhoseNameIsNotAStringIsRefused(t *testing.T) {
	env := "services:\n  web:\n    image: alpine\n    environment:\n      "
	args := "services:\n  web:\n    build:\n      context: .\n      args:\n        "
	labels := "services:\n  web:\n    image: alpine\n    labels:\n      "
	for _, tc := range []struct{ name, body, want string }{
		{"a number", env + "1: a\n", "environment variable 1 has a name that is not a string, got a number — quote it (`\"1\"`) if it is meant literally"},
		{"a boolean", env + "true: a\n", "environment variable true has a name that is not a string, got true/false"},
		{"a date", env + "2020-01-01: a\n", "environment variable 2020-01-01 has a name that is not a string, got a date"},
		{"null", env + "~: a\n", "environment variable has a name with nothing in it (line 5) — write the name, or remove the entry"},
		{"the second name", env + "A: 1\n      2: b\n", "environment variable 2 has a name"},
		{"in build.args", args + "1: a\n", "build.args variable 1 has a name that is not a string, got a number"},
		{"in labels", labels + "1: a\n", "labels entry 1 has a name that is not a string, got a number"},
		{"null in labels", labels + "~: a\n", "labels entry has a name with nothing in it (line 5)"},
		{"a quoted empty name", env + "\"\": a\n", "environment variable has a name with nothing in it (line 5)"},
		{"an alias as the name", "x-k: &k 1\n" + env + "*k : a\n", "environment variable 1 has a name that is not a string, got a number"},
		{"a merge key that brings in a number", "x-c: &c {1: a}\n" + env + "<<: *c\n      B: 2\n", "environment variable 1 has a name that is not a string"},
		{"a list of merge keys, the second bringing in a number", "x-c: &c {A: 1}\nx-d: &d {2: b}\n" + env + "<<: [*c, *d]\n", "environment variable 2 has a name that is not a string"},
		{"a merge key in labels", "x-c: &c {~: a}\n" + labels + "<<: *c\n", "labels entry has a name with nothing in it (line 1)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
		})
	}
	// A quoted name is a string; a `<<:` merge key is not a name.
	for _, tc := range []struct{ name, body, want string }{
		{"a quoted number", env + "\"1\": a\n", "1=a"},
		{"a quoted boolean", env + "\"true\": a\n", "true=a"},
		{"a merge key", "x-c: &c {A: 1}\n" + env + "<<: *c\n      B: 2\n", "A=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, tc.body))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := strings.Join(p.Services["web"].Environment, ","); !strings.Contains(got, tc.want) {
				t.Errorf("environment = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestAnEmptyEnvFileIsRefused(t *testing.T) {
	svc := "services:\n  web:\n    image: alpine\n    env_file: "
	for _, tc := range []struct{ name, body, want string }{
		{"an empty string", svc + "\"\"\n", "env_file is empty — write the file, as in `env_file: ./app.env`, or remove the key"},
		{"a variable that expands to nothing", svc + "${OPOSSUM_TEST_NO_SUCH_FILE_VAR:-}\n", "env_file is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
		})
	}
}
