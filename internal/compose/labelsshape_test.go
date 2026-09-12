package compose

// The two shapes the service-key sweep left (#784): `labels` (read since
// #882; the shape check stays in front of the read) and an empty `env_file`
// item. docker compose (v5.5.0)
// refuses `labels` written as one value (`labels: x`, `labels: 1`, a bare
// `labels:` — `must be a mapping`), a list item that is not a string
// (`[42]`, `[{a: b}]`, `- ` — `unexpected type int`, `map[string]interface
// {}`, `<nil>`), and a mapping value that is a list or a mapping (`must be
// a boolean, null, number or string`) or an infinity; it takes a list of
// names (`[a]` is the label `a: ""`), a mapping of scalars (a number or a
// boolean is written out as text), and the empty list or mapping. opossum
// read every one of these past and listed "labels" among the ignored
// fields. An empty `env_file` item docker compose refuses as `must be a
// string` (`- `, `- null`) or when it opens the file (`- ""`: `env file
// not found`); opossum read it as the path "" and failed opening the
// project directory ("is a directory").

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLabelsWrittenInAShapeDockerComposeRefusesAreRefused(t *testing.T) {
	svc := "services:\n  web:\n    image: alpine\n    labels:"
	for _, tc := range []struct{ name, body, want string }{
		{"one value", svc + " x\n", "labels must be a mapping or a list, got a single value — write them as `{key: value}` or `- key=value`"},
		{"a number", svc + " 1\n", "labels must be a mapping or a list, got a single value"},
		{"nothing after it", svc + "\n", "labels must be a mapping or a list, got nothing — write the labels, as in `{com.example.team: web}`, or remove the key"},
		{"an empty string", svc + " \"\"\n", "labels must be a mapping or a list, got a single value"},
		{"a list item that is a number", svc + " [42]\n", "labels entry 1 of 1 must be a string, got a number — quote it (`\"42\"`)"},
		{"an empty list item", svc + "\n      - \n", "labels entry 1 of 1 is empty — write the value or remove the `- `"},
		{"a mapping value that is a list", svc + " {a: [1]}\n", "labels entry a must be a string, a number, a boolean or null, got a list"},
		{"a mapping value that is a mapping", svc + " {a: 1, b: {c: d}}\n", "labels entry b must be a string, a number, a boolean or null, got a mapping"},
		{"a mapping value that is an infinity", svc + " {a: .inf}\n", "labels entry a must be a string, a number, a boolean or null, got .inf — quote it (`\".inf\"`)"},
		{"a list item that is a mapping", svc + " [{a: b}]\n", "labels entry 1 of 1 must be a string, got a mapping — write it as `key=value`"},
		{"a list item that is a list", svc + " [a=b, [c]]\n", "labels entry 2 of 2 must be a string, got a list"},
		{"through an alias", "x-l: &l x\n" + svc + " *l\n", "labels must be a mapping or a list, got a single value"},
		{"a mapping value through an alias", "x-i: &i [1]\n" + svc + " {a: *i}\n", "labels entry a must be a string, a number, a boolean or null, got a list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
		})
	}
	// Taken, as docker compose takes them — and read (so not listed as
	// ignored); the values are checked in labels_test.go.
	for _, tc := range []struct{ name, head, value string }{
		{"a list of names", "", "[a, b=c]"},
		{"a mapping of scalars", "", "{a: 1, b: true, c: ~, d: \"\"}"},
		{"an empty list", "", "[]"},
		{"an empty mapping", "", "{}"},
		// The value is read through the alias, not the alias node itself.
		{"a scalar value through an alias", "x-v: &v x\n", "{a: *v}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, tc.head+svc+" "+tc.value+"\n"))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			// Read since #882: an accepted shape is not listed as ignored.
			if got := p.Services["web"].Unsupported; len(got) != 0 {
				t.Errorf("ignored fields = %v, want none (labels is read)", got)
			}
		})
	}
	// With several -f files a later file's bare `labels:` is "not given"
	// and keeps the earlier file's value, as every other key does. A later
	// file's `labels: x` is refused naming that file, as each file is
	// checked on its own; docker compose, which validates a later file
	// against the merge, folds the value into the mapping there (`x: ""`)
	// when an earlier file gave a mapping — and refuses it when none did.
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := write("base.yml", svc+" {a: 1}\n")
	over := write("over.yml", "services:\n  web:\n    labels:\n")
	if _, err := LoadFiles([]string{base, over}, nil); err != nil {
		t.Errorf("a bare labels: in a later file should keep the earlier file's, got: %v", err)
	}
	if _, err := LoadFiles([]string{base, write("bad.yml", "services:\n  web:\n    labels: x\n")}, nil); err == nil || !strings.Contains(err.Error(), "bad.yml") {
		t.Errorf("a later file's one-value labels should be refused naming that file, got: %v", err)
	}
}

func TestAnEmptyEnvFileItemIsRefused(t *testing.T) {
	svc := "services:\n  web:\n    image: alpine\n    env_file:\n"
	for _, tc := range []struct{ name, body, want string }{
		{"a dash alone", svc + "      - \n", "env_file entry 1 of 1 is empty — write the file (`./app.env`, or a mapping with `path:`) or remove the `- `"},
		{"null", svc + "      - null\n", "env_file entry 1 of 1 is empty"},
		{"an empty string", svc + "      - \"\"\n", "env_file entry 1 of 1 is empty"},
		{"the second of two", svc + "      - ./a.env\n      - \n", "env_file entry 2 of 2 is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
		})
	}
}
