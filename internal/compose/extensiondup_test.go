package compose

import (
	"strings"
	"testing"
	"time"
)

// docker compose refuses a file that writes one key twice in the same mapping,
// wherever it is — `mapping key "a" already defined at line 2` — and that is a
// property of the YAML, not of what the block is for. opossum's decoder says the
// same for every block that is read into a typed shape, and read past a block that
// is not: an `x-` extension holds whatever the writer likes and is never decoded,
// so a repeated key there was accepted where docker compose refuses the file.
//
// Measured (docker compose v5.5.1, `config`): a repeated key inside a top-level
// `x-foo`, inside a service's `x-foo`, inside a nested mapping and inside a
// sequence's mapping is refused by docker compose in every one of them; the same
// keys once each are accepted. A repeated key outside an `x-` block was already
// refused here, so those rows are the control that the check does not need the
// extension to fire.
func TestARepeatedKeyInsideAnExtensionIsRefused(t *testing.T) {
	const svc = "services:\n  web:\n    image: alpine\n"
	for _, tc := range []struct {
		name string
		body string
		// want is what the refusal has to say; empty means the file loads.
		want []string
	}{
		{"top level, block form", "x-foo:\n  a: 1\n  a: 2\n" + svc, []string{"same key twice", `mapping key "a" already defined at line 2`, "line 3"}},
		{"top level, flow form", "x-foo: {a: 1, a: 2}\n" + svc, []string{"same key twice", `mapping key "a" already defined`}},
		{"in a service", "services:\n  web:\n    image: alpine\n    x-foo:\n      a: 1\n      a: 2\n", []string{"same key twice", `mapping key "a" already defined at line 5`, "line 6"}},
		{"in a nested mapping", "x-foo:\n  outer:\n    b: 1\n    b: 2\n" + svc, []string{"same key twice", `mapping key "b" already defined at line 3`}},
		{"in a mapping inside a list", "x-foo:\n  - c: 1\n  - d: 1\n    d: 2\n" + svc, []string{"same key twice", `mapping key "d" already defined at line 3`}},
		{"in an extension inside a declaration", svc + "networks:\n  n:\n    x-note:\n      e: 1\n      e: 2\n", []string{"same key twice", `mapping key "e" already defined at line 7`}},
		{"in an extension inside a volume declaration", svc + "volumes:\n  v:\n    x-note:\n      f: 1\n      f: 2\n", []string{"same key twice", `mapping key "f" already defined`}},
		{"in an extension nested in another block", "services:\n  web:\n    image: alpine\n    deploy:\n      x-note:\n        g: 1\n        g: 2\n", []string{"same key twice", `mapping key "g" already defined`}},
		{"in a list inside a mapping inside an extension", "x-foo:\n  list:\n    - g: 1\n      g: 2\n" + svc, []string{"same key twice", `mapping key "g" already defined at line 3`}},
		// docker compose's decoder takes two keys as the same when their text is,
		// whatever the tag, and takes `<<` written twice as a repeat.
		{"a number and the same text quoted", "x-foo:\n  1: a\n  \"1\": b\n" + svc, []string{"same key twice", `mapping key "1" already defined at line 2`}},
		{"a merge key written twice", "x-b: &b {a: 1}\nx-foo:\n  <<: *b\n  <<: *b\n" + svc, []string{"same key twice", `mapping key "<<" already defined`}},
		// The one that was already refused before this check (the top level is
		// read into a typed shape), kept as a control that it still is.
		{"an extension key written twice at the top level", "x-foo: 1\nx-foo: 2\n" + svc, []string{"same key twice", `mapping key "x-foo" already defined at line 1`}},

		// The control: the same keys, once each, and the same key at two levels.
		{"the keys once each", "x-foo:\n  a: 1\n  b: 2\n" + svc, nil},
		{"one key at two levels is not a repeat", "x-foo:\n  a:\n    a: 1\n" + svc, nil},
		{"the same key in two sibling mappings is not a repeat", "x-foo:\n  - a: 1\n  - a: 2\n" + svc, nil},
		// An alias reuses a whole block; the keys it carries are written once.
		{"an anchor and its alias", "x-base: &base\n  a: 1\nx-copy: *base\n" + svc, nil},
		// The other place a key is written twice, which was already refused: not
		// an extension, and the reason the check has to be about extensions and not
		// about the file's shape as a whole.
		{"a repeated key outside an extension", "services:\n  web:\n    image: alpine\n    image: busybox\n", []string{"same key twice", `mapping key "image" already defined`, "unmarshal errors"}},
		// A second file: each file is read on its own, and a repeat inside an
		// extension in the overlay is the overlay's mistake.
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, tc.body))
			if len(tc.want) == 0 {
				if err != nil {
					t.Fatalf("want the file loaded, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("a key written twice inside an extension loaded, and docker compose refuses the file:\n%s", tc.body)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the refusal does not say %q:\n%v", w, err)
				}
			}
			if strings.Contains(err.Error(), "not valid YAML") {
				t.Errorf("the file is valid YAML with a repeated key, and the refusal says it is not:\n%v", err)
			}
		})
	}
}

// Each file is checked on its own, so a repeated key in an extension of an overlay
// is refused naming that file. (Refused before this check existed, by the merge;
// kept as the control that the overlay path still is.)
func TestARepeatedKeyInAnExtensionOfALaterFileIsRefused(t *testing.T) {
	base := writeTemp(t, "services:\n  web:\n    image: alpine\n")
	overlay := writeTemp(t, "x-foo:\n  a: 1\n  a: 2\n")
	_, err := LoadFiles([]string{base, overlay}, nil)
	if err == nil {
		t.Fatal("the overlay repeats a key inside an extension and loaded")
	}
	if !strings.Contains(err.Error(), overlay) || !strings.Contains(err.Error(), `mapping key "a" already defined at line 2`) {
		t.Errorf("the refusal does not name the overlay and the key:\n%v", err)
	}
}

// A `${...}` written as a KEY is not expanded by docker compose, so two of them are
// two keys however the variables are set, and one that expands to the text of
// another key is not a repeat of it (v5.5.1, measured: `config` accepts each of
// these and prints the keys as written). The check reads the file as written; read
// after expansion it refused files docker compose takes, naming a key the file
// never wrote.
func TestAReferenceWrittenAsAKeyIsNotExpandedBeforeItIsCounted(t *testing.T) {
	const svc = "services:\n  web:\n    image: alpine\n"
	for _, tc := range []struct {
		name string
		body string
		env  map[string]string
	}{
		// A flow mapping needs the reference quoted to be YAML at all (`{${A}: 1}`
		// is not: docker compose says `did not find expected ',' or '}'`), so the
		// flow rows quote it; the block rows below need nothing.
		{"one expands to the text of the other key", "x-foo: {\"${A}\": 1, x: 2}\n" + svc, map[string]string{"A": "x"}},
		{"two expand to the same text", "x-foo: {\"${A}\": 1, \"${B}\": 2}\n" + svc, map[string]string{"A": "x", "B": "x"}},
		{"two that are not set", "x-foo: {\"${A}\": 1, \"${B}\": 2}\n" + svc, map[string]string{"A": "", "B": ""}},
		{"a default that is the other key", "x-foo:\n  ${A:-x}: 1\n  x: 2\n" + svc, nil},
		{"in block form", "x-foo:\n  ${A}: 1\n  ${B}: 2\n" + svc, map[string]string{"A": "x", "B": "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if _, err := Load(writeTemp(t, tc.body)); err != nil {
				t.Fatalf("docker compose takes this file, and it was refused: %v", err)
			}
		})
	}
}

// A block that contains itself — `x-a: &a {b: *a}` — is refused by docker compose
// as a cycle, and is read here as it always was. What this holds is that the check
// ends: it walks aliases, and an alias that reaches its own block would walk
// forever. Whether the file loads is not the question, so neither answer is
// asserted.
func TestTheCheckEndsOnABlockThatContainsItself(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = Load(writeTemp(t, "x-a: &a\n  b: *a\nservices:\n  web:\n    image: alpine\n"))
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the check did not end on an alias that reaches its own block")
	}
}
