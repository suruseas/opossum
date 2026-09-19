package compose

import (
	"strings"
	"testing"
)

// The long form's `required`, as docker compose (v5.5.1, measured 2026-09-19)
// reads it: written beside a `condition` or not at all; the YAML 1.1 words
// make it — `false`, `no`, `off`, `n` and their casings, quoted or not, are
// false, `true`, `yes`, `on`, `y` true — and any other word is refused, as is
// `null` or a number; a dependency the file does not define is refused
// whatever `required` says. The entry itself has to be a mapping and its
// `condition` a string. `config` prints `required` on every dependency,
// `true` where the file left it out. Every row here is a cell of the table
// on the pull request, measured against `docker compose config`.
func TestDependsOnRequired(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		optional   bool
		err        string
	}{
		{"required: false beside a condition", "      cache: {condition: service_started, required: false}\n", true, ""},
		{"required: true beside a condition", "      cache: {condition: service_started, required: true}\n", false, ""},
		{"required left out", "      cache: {condition: service_started}\n", false, ""},
		{"required without a condition", "      cache: {required: false}\n", false, "`required` is written without a `condition`"},
		{"required in quotes", "      cache: {condition: service_started, required: \"false\"}\n", true, ""},
		{"required: null", "      cache: {condition: service_started, required: null}\n", false, "required must be a boolean"},
		{"required: yes reads as true", "      cache: {condition: service_started, required: yes}\n", false, ""},
		{"the list form is required", "      - cache\n", false, ""},
		{"a dependency the file does not define, optional", "      nope: {condition: service_started, required: false}\n", false, `unknown service "nope"`},
		{"required: 1 is not a bool", "      cache: {condition: service_started, required: 1}\n", false, "required must be a boolean, got a number"},
		{"required: False (YAML's own spelling)", "      cache: {condition: service_started, required: False}\n", true, ""},
		// Through a merge key or an alias the keys read as the decode reads
		// them (docker compose v5.5.1, measured 2026-09-19).
		{"the condition through a merge key", "      cache: {<<: *base, required: false}\n", true, ""},
		{"the condition through an alias", "      cache: {condition: *c, required: false}\n", true, ""},
		{"required through an alias, no condition", "      cache: {required: *f}\n", false, "`required` is written without a `condition`"},
		{"required through an alias to a quoted false", "      cache: {condition: service_started, required: *q}\n", true, ""},
		{"required through an alias to null", "      cache: {condition: service_started, required: *n}\n", false, "required must be a boolean"},
		// The entry is a mapping: a word, a list, a number or a bool there is
		// refused (`must be a mapping`).
		{"the entry is a word", "      cache: hello\n", false, "depends_on.cache must be a mapping, got a single value"},
		{"the entry is a list", "      cache: [a]\n", false, "depends_on.cache must be a mapping, got a list"},
		{"the entry is a number", "      cache: 5\n", false, "depends_on.cache must be a mapping"},
		{"the entry is a bool", "      cache: true\n", false, "depends_on.cache must be a mapping"},
		{"the entry is a word through an alias", "      cache: *c\n", false, "depends_on.cache must be a mapping, got a single value"},
		// …and the condition a string.
		{"the condition is a list", "      cache: {condition: [service_started]}\n", false, "depends_on.cache.condition must be a string, got a list"},
		{"the condition is a mapping", "      cache: {condition: {a: b}}\n", false, "depends_on.cache.condition must be a string, got a mapping"},
		{"the condition is a list beside required: false", "      cache: {condition: [service_started], required: false}\n", false, "depends_on.cache.condition must be a string"},
		// The false side of the YAML 1.1 words, which YAML 1.2 hands over as
		// strings: docker compose reads each as false.
		{"required: no", "      cache: {condition: service_started, required: no}\n", true, ""},
		{"required: off", "      cache: {condition: service_started, required: off}\n", true, ""},
		{"required: n", "      cache: {condition: service_started, required: n}\n", true, ""},
		{"required: N", "      cache: {condition: service_started, required: N}\n", true, ""},
		{"required: Off", "      cache: {condition: service_started, required: Off}\n", true, ""},
		{"required: \"OFF\" in quotes", "      cache: {condition: service_started, required: \"OFF\"}\n", true, ""},
		{"required: \"NO\" in quotes", "      cache: {condition: service_started, required: \"NO\"}\n", true, ""},
		{"required: on reads as true", "      cache: {condition: service_started, required: on}\n", false, ""},
		{"required: \"y\" in quotes reads as true", "      cache: {condition: service_started, required: \"y\"}\n", false, ""},
		// Words that are not the YAML 1.1 bools are refused (docker compose:
		// `invalid boolean`), the key named and the value not.
		{"required: \"t\"", "      cache: {condition: service_started, required: \"t\"}\n", false, "required is not a boolean"},
		{"required: \"1\"", "      cache: {condition: service_started, required: \"1\"}\n", false, "required is not a boolean"},
		{"required: \"\"", "      cache: {condition: service_started, required: \"\"}\n", false, "required is not a boolean"},
		{"required: \"false \" with a space", "      cache: {condition: service_started, required: \"false \"}\n", false, "required is not a boolean"},
		{"required: maybe", "      cache: {condition: service_started, required: maybe}\n", false, "required is not a boolean"},
		{"required: [false]", "      cache: {condition: service_started, required: [false]}\n", false, "required must be a boolean —"},
		{"required: {}", "      cache: {condition: service_started, required: {}}\n", false, "required must be a boolean —"},
		{"required: 0 is not a bool", "      cache: {condition: service_started, required: 0}\n", false, "required must be a boolean, got a number"},
		{"required: !!bool 1 is not a bool", "      cache: {condition: service_started, required: !!bool 1}\n", false, "required is not a boolean"},
		// A key written twice in the entry is refused, as docker compose
		// refuses it (`mapping key "condition" already defined`).
		{"condition written twice", "      cache:\n        condition: service_started\n        condition: service_healthy\n", false, "already defined"},
		{"required written twice", "      cache:\n        condition: service_started\n        required: false\n        required: true\n", false, "already defined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "x-base: &base {condition: service_started}\nx-c: &c service_started\nx-f: &f false\nx-q: &q \"false\"\nx-n: &n null\nservices:\n  web:\n    image: a\n    depends_on:\n" + tc.body + "  cache:\n    image: a\n    profiles: [tools]\n"
			p, err := Load(writeTemp(t, body))
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want a refusal saying %q, got %v", tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			deps := p.Services["web"].DependsOn
			if len(deps) != 1 || deps[0].Name != "cache" || deps[0].Optional != tc.optional {
				t.Errorf("got %+v, want cache optional=%v", deps, tc.optional)
			}
			// Read, so not named among the ignored fields.
			if got := p.Services["web"].Unsupported; len(got) != 0 {
				t.Errorf("required is read, yet listed as ignored: %v", got)
			}
			out, err := RenderConfig(p)
			if err != nil {
				t.Fatal(err)
			}
			want := "required: true"
			if tc.optional {
				want = "required: false"
			}
			if !strings.Contains(out, want) {
				t.Errorf("config: want %q, got\n%s", want, out)
			}
		})
	}
}

// An entry with nothing in it — `cache:` alone, or `cache: {}` — reads as
// `condition: service_started`, required. Known difference: docker compose
// v5.5.1 refuses both (`must be a mapping` and `missing property 'condition'`,
// measured 2026-09-19); opossum read them so before `required` was read, and
// goes on to.
func TestDependsOnAnEmptyEntryReadsAsStarted(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"nothing after the name", "      cache:\n"},
		{"an empty mapping", "      cache: {}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, "services:\n  web:\n    image: a\n    depends_on:\n"+tc.body+"  cache:\n    image: a\n"))
			if err != nil {
				t.Fatal(err)
			}
			deps := p.Services["web"].DependsOn
			if len(deps) != 1 || deps[0].Name != "cache" || deps[0].Condition != ConditionStarted || deps[0].Optional {
				t.Errorf("got %+v, want cache, service_started, required", deps)
			}
		})
	}
}

// The refusal of a word that is not a boolean names the key and not the word:
// the value may have come from a `${...}` reference, where a secret lives (as
// the other quoted-bool refusals in this package do).
func TestDependsOnRequiredRefusalNamesTheKeyNotTheValue(t *testing.T) {
	body := "services:\n  web:\n    image: a\n    depends_on:\n      cache: {condition: service_started, required: \"hunter2\"}\n  cache:\n    image: a\n"
	_, err := Load(writeTemp(t, body))
	if err == nil || !strings.Contains(err.Error(), "required is not a boolean") {
		t.Fatalf("want a refusal saying required is not a boolean, got %v", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("the refusal repeats the value: %v", err)
	}
}

// A file with two dependencies at fault is refused for the same one every
// time it is read (the names are walked in order, not in map order).
func TestDependsOnRequiredRefusesTheSameOneEveryTime(t *testing.T) {
	body := "services:\n  web:\n    image: a\n    depends_on:\n      b: {required: false}\n      a: {required: false}\n  a:\n    image: a\n  b:\n    image: a\n"
	for i := 0; i < 20; i++ {
		_, err := Load(writeTemp(t, body))
		if err == nil || !strings.Contains(err.Error(), "depends_on.a:") {
			t.Fatalf("read %d: want the refusal to name a (the first in order), got %v", i, err)
		}
	}
}
