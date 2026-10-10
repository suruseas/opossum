package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A string for `gpus` (a word, a blank, a quoted number) in a service an extends takes from another file is read by docker compose, whether the extender writes over
// it or not and however many files the extends goes through; the same string in a file read on its own, in the extender's own service, or beside an extends in the same
// file is refused, and so is a number, `true`, a mapping and a null — a string the extender writes over another is too (measured, v5.5.1, `config -q`, #1959). Each row is what docker compose answers.
func TestAStringForGpusInAnExtendedFileIsRead(t *testing.T) {
	const base = "services:\n  x:\n    image: xi\n    gpus: V\n"
	for _, tc := range []struct {
		name    string
		files   map[string]string // `V` in a file is the value under test
		order   []string
		value   string
		refused bool
	}{
		{"a word in the extended file", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n", "base.yaml": base}, []string{"compose.yaml"}, "abc", false},
		{"a blank in the extended file", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n", "base.yaml": base}, []string{"compose.yaml"}, `""`, false},
		{"a quoted number in the extended file", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n", "base.yaml": base}, []string{"compose.yaml"}, `"1"`, false},
		{"a word in the extended file, the extender writes a list over it", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n    gpus: [{count: all}]\n", "base.yaml": base}, []string{"compose.yaml"}, "abc", false},
		{"a word in the extended file, a file after it writes a list over it", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n", "base.yaml": base, "o.yaml": "services:\n  web:\n    gpus: []\n"}, []string{"compose.yaml", "o.yaml"}, "abc", false},
		{"a word through a file that extends in turn", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: b1.yaml, service: m}\n", "b1.yaml": "services:\n  m:\n    extends: {file: base.yaml, service: x}\n", "base.yaml": base}, []string{"compose.yaml"}, "abc", false},
		{"a word through a merge key in the extended file", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n", "base.yaml": "x-g: &g {gpus: abc}\nservices:\n  x:\n    image: xi\n    <<: *g\n"}, []string{"compose.yaml"}, "abc", false},
		{"a word through an alias in the extended file", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n", "base.yaml": "x-v: &v abc\nservices:\n  x:\n    image: xi\n    gpus: *v\n"}, []string{"compose.yaml"}, "abc", false},

		{"a word in a file read on its own", map[string]string{"compose.yaml": "services:\n  web:\n    image: xi\n    gpus: abc\n"}, []string{"compose.yaml"}, "abc", true},
		{"a word beside an extends in the same file", map[string]string{"compose.yaml": "services:\n  web:\n    extends: x\n  x:\n    image: xi\n    gpus: abc\n"}, []string{"compose.yaml"}, "abc", true},
		{"a word the extender writes itself", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n    gpus: abc\n", "base.yaml": "services:\n  x:\n    image: xi\n    gpus: all\n"}, []string{"compose.yaml"}, "abc", true},
		{"a word the extender writes over a word", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n    gpus: abc\n", "base.yaml": base}, []string{"compose.yaml"}, "abc", true},
		{"a word a later file writes over the extended one's", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n", "base.yaml": base, "o.yaml": "services:\n  web:\n    gpus: abc\n"}, []string{"compose.yaml", "o.yaml"}, "abc", true},
		{"a number in the extended file", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n", "base.yaml": base}, []string{"compose.yaml"}, "1", true},
		{"true in the extended file", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n", "base.yaml": base}, []string{"compose.yaml"}, "true", true},
		{"a mapping in the extended file", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n", "base.yaml": base}, []string{"compose.yaml"}, "{a: b}", true},
		{"a list of a number in the extended file", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n", "base.yaml": base}, []string{"compose.yaml"}, "[1]", true},
		{"a number in the extended file, the extender writes a list over it", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n    gpus: []\n", "base.yaml": base}, []string{"compose.yaml"}, "1", true},
		{"true in the extended file, the extender writes a list over it", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n    gpus: []\n", "base.yaml": base}, []string{"compose.yaml"}, "true", true},
		{"a mapping in the extended file, the extender writes a list over it", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n    gpus: []\n", "base.yaml": base}, []string{"compose.yaml"}, "{a: b}", true},
		{"a list of a number in the extended file, the extender writes a list over it", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n    gpus: []\n", "base.yaml": base}, []string{"compose.yaml"}, "[1]", true},
		{"a word in a service nothing takes", map[string]string{"compose.yaml": "services:\n  web:\n    image: wi\n", "base.yaml": "services:\n  x:\n    image: xi\n  y:\n    image: yi\n    gpus: V\n", "o.yaml": "services:\n  z:\n    extends: {file: base.yaml, service: x}\n"}, []string{"o.yaml"}, "abc", false},
		{"a word through an alias of the whole service in the extended file", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n", "base.yaml": "x-va: &va abc\nx-s: &s {image: xi, gpus: *va}\nservices:\n  x: *s\n"}, []string{"compose.yaml"}, "abc", false},
		{"a list of a number in the file at the end of the chain, a word written over it in the middle one", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: b1.yaml, service: m}\n", "b1.yaml": "services:\n  m:\n    extends: {file: base.yaml, service: x}\n    gpus: V\n", "base.yaml": "services:\n  x:\n    image: xi\n    gpus: [1]\n"}, []string{"compose.yaml"}, "abc", true},
		{"a null in the extended file", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n", "base.yaml": base}, []string{"compose.yaml"}, "~", true},
		{"a word the extender writes a number over", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n    gpus: 1\n", "base.yaml": base}, []string{"compose.yaml"}, "abc", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				body = strings.ReplaceAll(body, "gpus: V\n", "gpus: "+tc.value+"\n")
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			paths := make([]string, len(tc.order))
			for i, f := range tc.order {
				paths[i] = filepath.Join(dir, f)
			}
			_, err := LoadFiles(paths, nil)
			if (err != nil) != tc.refused {
				t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
			}
		})
	}
}
