package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An `extra_hosts` (or `build.extra_hosts`) mapping that an included file gave, and that the including file writes and takes every entry out of with `!reset`, is refused naming that
// file, as it is for a later `-f` file: it holds no entry once it is merged in, and docker compose says `must be a mapping` (v5.5.1, `config -q`, every row measured; #1804). A mapping
// that keeps an entry, one the file adds to, overrides, empties with `{}` or resets whole is read, and so is one that holds something whatever the file takes out: entries or a list a
// file before gave it, or the entries of two paths of one include entry, which merge as files do and which the including file's `!reset` does not reach.
func TestAHostsMappingTheIncludeGaveAndTheIncludingFileEmptiesIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   map[string]string
		order   []string
		refused bool
	}{
		{"extra_hosts | include then own: reset the only entry", map[string]string{"f1.yaml": `include: [inc.yaml]
services:
  a:
    extra_hosts:
      h: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, []string{"f1.yaml"}, true},
		{"extra_hosts | include then a later -f: reset the only entry", map[string]string{"f1.yaml": `include: [inc.yaml]
`, "f2.yaml": `services:
  a:
    extra_hosts:
      h: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, []string{"f1.yaml", "f2.yaml"}, true},
		{"extra_hosts | -f then -f: reset the only entry", map[string]string{"f1.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`, "f2.yaml": `services:
  a:
    extra_hosts:
      h: !reset null
`}, []string{"f1.yaml", "f2.yaml"}, true},
		{"extra_hosts | include then own: reset one of two", map[string]string{"f1.yaml": `include: [inc.yaml]
services:
  a:
    extra_hosts:
      h: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
      g: 9.9.9.9
`}, []string{"f1.yaml"}, false},
		{"extra_hosts | include then a later -f: reset one of two", map[string]string{"f1.yaml": `include: [inc.yaml]
`, "f2.yaml": `services:
  a:
    extra_hosts:
      h: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
      g: 9.9.9.9
`}, []string{"f1.yaml", "f2.yaml"}, false},
		{"extra_hosts | -f then -f: reset one of two", map[string]string{"f1.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
      g: 9.9.9.9
`, "f2.yaml": `services:
  a:
    extra_hosts:
      h: !reset null
`}, []string{"f1.yaml", "f2.yaml"}, false},
		{"extra_hosts | include then own: reset the whole key", map[string]string{"f1.yaml": `include: [inc.yaml]
services:
  a:
    extra_hosts: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, []string{"f1.yaml"}, false},
		{"extra_hosts | include then a later -f: reset the whole key", map[string]string{"f1.yaml": `include: [inc.yaml]
`, "f2.yaml": `services:
  a:
    extra_hosts: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, []string{"f1.yaml", "f2.yaml"}, false},
		{"extra_hosts | -f then -f: reset the whole key", map[string]string{"f1.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`, "f2.yaml": `services:
  a:
    extra_hosts: !reset null
`}, []string{"f1.yaml", "f2.yaml"}, false},
		{"extra_hosts | include then own: override with entries", map[string]string{"f1.yaml": `include: [inc.yaml]
services:
  a:
    extra_hosts: !override
      k: 5.6.7.8
`, "inc.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, []string{"f1.yaml"}, false},
		{"extra_hosts | include then a later -f: override with entries", map[string]string{"f1.yaml": `include: [inc.yaml]
`, "f2.yaml": `services:
  a:
    extra_hosts: !override
      k: 5.6.7.8
`, "inc.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, []string{"f1.yaml", "f2.yaml"}, false},
		{"extra_hosts | -f then -f: override with entries", map[string]string{"f1.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`, "f2.yaml": `services:
  a:
    extra_hosts: !override
      k: 5.6.7.8
`}, []string{"f1.yaml", "f2.yaml"}, false},
		{"extra_hosts | include then own: add an entry", map[string]string{"f1.yaml": `include: [inc.yaml]
services:
  a:
    extra_hosts:
      k: 5.6.7.8
`, "inc.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, []string{"f1.yaml"}, false},
		{"extra_hosts | include then a later -f: add an entry", map[string]string{"f1.yaml": `include: [inc.yaml]
`, "f2.yaml": `services:
  a:
    extra_hosts:
      k: 5.6.7.8
`, "inc.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, []string{"f1.yaml", "f2.yaml"}, false},
		{"extra_hosts | -f then -f: add an entry", map[string]string{"f1.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`, "f2.yaml": `services:
  a:
    extra_hosts:
      k: 5.6.7.8
`}, []string{"f1.yaml", "f2.yaml"}, false},
		{"extra_hosts | include then own: empty mapping", map[string]string{"f1.yaml": `include: [inc.yaml]
services:
  a:
    extra_hosts: {}
`, "inc.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, []string{"f1.yaml"}, false},
		{"extra_hosts | include then a later -f: empty mapping", map[string]string{"f1.yaml": `include: [inc.yaml]
`, "f2.yaml": `services:
  a:
    extra_hosts: {}
`, "inc.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, []string{"f1.yaml", "f2.yaml"}, false},
		{"extra_hosts | -f then -f: empty mapping", map[string]string{"f1.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`, "f2.yaml": `services:
  a:
    extra_hosts: {}
`}, []string{"f1.yaml", "f2.yaml"}, false},
		{"build.extra_hosts | include then own: reset the only entry", map[string]string{"f1.yaml": `include: [inc.yaml]
services:
  a:
    build:
      context: .
      extra_hosts:
        h: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, []string{"f1.yaml"}, true},
		{"build.extra_hosts | include then a later -f: reset the only entry", map[string]string{"f1.yaml": `include: [inc.yaml]
`, "f2.yaml": `services:
  a:
    build:
      context: .
      extra_hosts:
        h: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, []string{"f1.yaml", "f2.yaml"}, true},
		{"build.extra_hosts | -f then -f: reset the only entry", map[string]string{"f1.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`, "f2.yaml": `services:
  a:
    build:
      context: .
      extra_hosts:
        h: !reset null
`}, []string{"f1.yaml", "f2.yaml"}, true},
		{"build.extra_hosts | include then own: reset one of two", map[string]string{"f1.yaml": `include: [inc.yaml]
services:
  a:
    build:
      context: .
      extra_hosts:
        h: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
        g: 9.9.9.9
`}, []string{"f1.yaml"}, false},
		{"build.extra_hosts | include then a later -f: reset one of two", map[string]string{"f1.yaml": `include: [inc.yaml]
`, "f2.yaml": `services:
  a:
    build:
      context: .
      extra_hosts:
        h: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
        g: 9.9.9.9
`}, []string{"f1.yaml", "f2.yaml"}, false},
		{"build.extra_hosts | -f then -f: reset one of two", map[string]string{"f1.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
        g: 9.9.9.9
`, "f2.yaml": `services:
  a:
    build:
      context: .
      extra_hosts:
        h: !reset null
`}, []string{"f1.yaml", "f2.yaml"}, false},
		{"build.extra_hosts | include then own: reset the whole key", map[string]string{"f1.yaml": `include: [inc.yaml]
services:
  a:
    build: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, []string{"f1.yaml"}, false},
		{"build.extra_hosts | include then a later -f: reset the whole key", map[string]string{"f1.yaml": `include: [inc.yaml]
`, "f2.yaml": `services:
  a:
    build: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, []string{"f1.yaml", "f2.yaml"}, false},
		{"build.extra_hosts | -f then -f: reset the whole key", map[string]string{"f1.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`, "f2.yaml": `services:
  a:
    build: !reset null
`}, []string{"f1.yaml", "f2.yaml"}, false},
		{"build.extra_hosts | include then own: override with entries", map[string]string{"f1.yaml": `include: [inc.yaml]
services:
  a:
    build:
      context: .
      extra_hosts: !override
        k: 5.6.7.8
`, "inc.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, []string{"f1.yaml"}, false},
		{"build.extra_hosts | include then a later -f: override with entries", map[string]string{"f1.yaml": `include: [inc.yaml]
`, "f2.yaml": `services:
  a:
    build:
      context: .
      extra_hosts: !override
        k: 5.6.7.8
`, "inc.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, []string{"f1.yaml", "f2.yaml"}, false},
		{"build.extra_hosts | -f then -f: override with entries", map[string]string{"f1.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`, "f2.yaml": `services:
  a:
    build:
      context: .
      extra_hosts: !override
        k: 5.6.7.8
`}, []string{"f1.yaml", "f2.yaml"}, false},
		{"build.extra_hosts | include then own: add an entry", map[string]string{"f1.yaml": `include: [inc.yaml]
services:
  a:
    build:
      context: .
      extra_hosts:
        k: 5.6.7.8
`, "inc.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, []string{"f1.yaml"}, false},
		{"build.extra_hosts | include then a later -f: add an entry", map[string]string{"f1.yaml": `include: [inc.yaml]
`, "f2.yaml": `services:
  a:
    build:
      context: .
      extra_hosts:
        k: 5.6.7.8
`, "inc.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, []string{"f1.yaml", "f2.yaml"}, false},
		{"build.extra_hosts | -f then -f: add an entry", map[string]string{"f1.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`, "f2.yaml": `services:
  a:
    build:
      context: .
      extra_hosts:
        k: 5.6.7.8
`}, []string{"f1.yaml", "f2.yaml"}, false},
		{"build.extra_hosts | include then own: empty mapping", map[string]string{"f1.yaml": `include: [inc.yaml]
services:
  a:
    build:
      context: .
      extra_hosts: {}
`, "inc.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, []string{"f1.yaml"}, false},
		{"build.extra_hosts | include then a later -f: empty mapping", map[string]string{"f1.yaml": `include: [inc.yaml]
`, "f2.yaml": `services:
  a:
    build:
      context: .
      extra_hosts: {}
`, "inc.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, []string{"f1.yaml", "f2.yaml"}, false},
		{"build.extra_hosts | -f then -f: empty mapping", map[string]string{"f1.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`, "f2.yaml": `services:
  a:
    build:
      context: .
      extra_hosts: {}
`}, []string{"f1.yaml", "f2.yaml"}, false},
		{"extra_hosts | earlier list, then a file that includes and resets the entry", map[string]string{"f0.yaml": `services:
  a:
    image: x
    extra_hosts: ["k=5.6.7.8"]
`, "f1.yaml": `include: [inc.yaml]
services:
  a:
    extra_hosts:
      h: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, []string{"f0.yaml", "f1.yaml"}, false},
		{"extra_hosts | earlier empty list, then a file that includes and resets the entry", map[string]string{"f0.yaml": `services:
  a:
    image: x
    extra_hosts: []
`, "f1.yaml": `include: [inc.yaml]
services:
  a:
    extra_hosts:
      h: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, []string{"f0.yaml", "f1.yaml"}, false},
		{"extra_hosts | earlier mapping with an entry, then a file that includes and resets the entry", map[string]string{"f0.yaml": `services:
  a:
    image: x
    extra_hosts:
      k: 5.6.7.8
`, "f1.yaml": `include: [inc.yaml]
services:
  a:
    extra_hosts:
      h: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, []string{"f0.yaml", "f1.yaml"}, false},
		{"extra_hosts | earlier empty mapping, then a file that includes and resets the entry", map[string]string{"f0.yaml": `services:
  a:
    image: x
    extra_hosts: {}
`, "f1.yaml": `include: [inc.yaml]
services:
  a:
    extra_hosts:
      h: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, []string{"f0.yaml", "f1.yaml"}, true},
		{"extra_hosts | earlier without the key, then a file that includes and resets the entry", map[string]string{"f0.yaml": `services:
  a:
    image: x
`, "f1.yaml": `include: [inc.yaml]
services:
  a:
    extra_hosts:
      h: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, []string{"f0.yaml", "f1.yaml"}, true},
		{"extra_hosts | one include entry, two paths, the wrapper resets both", map[string]string{"f1.yaml": `include:
  - path: [inc.yaml, inc2.yaml]
services:
  a:
    extra_hosts:
      h: !reset null
      g: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`, "inc2.yaml": `services:
  a:
    extra_hosts:
      g: 9.9.9.9
`}, []string{"f1.yaml"}, false},
		{"extra_hosts | two include entries, the wrapper resets both", map[string]string{"f1.yaml": `include: [inc.yaml, inc2.yaml]
services:
  a:
    extra_hosts:
      h: !reset null
      g: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`, "inc2.yaml": `services:
  a:
    extra_hosts:
      g: 9.9.9.9
`}, []string{"f1.yaml"}, true},
		{"extra_hosts | the include gives an empty mapping, the wrapper writes nothing", map[string]string{"f1.yaml": `include: [inc.yaml]
services:
  b:
    image: y
`, "inc.yaml": `services:
  a:
    image: x
    extra_hosts: {}
`}, []string{"f1.yaml"}, false},
		{"extra_hosts | the include gives an entry, the wrapper resets the service", map[string]string{"f1.yaml": `include: [inc.yaml]
services:
  a: !reset null
  b:
    image: y
`, "inc.yaml": `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, []string{"f1.yaml"}, false},
		{"build.extra_hosts | earlier list, then a file that includes and resets the entry", map[string]string{"f0.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: ["k=5.6.7.8"]
`, "f1.yaml": `include: [inc.yaml]
services:
  a:
    build:
      context: .
      extra_hosts:
        h: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, []string{"f0.yaml", "f1.yaml"}, false},
		{"build.extra_hosts | earlier empty list, then a file that includes and resets the entry", map[string]string{"f0.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: []
`, "f1.yaml": `include: [inc.yaml]
services:
  a:
    build:
      context: .
      extra_hosts:
        h: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, []string{"f0.yaml", "f1.yaml"}, false},
		{"build.extra_hosts | earlier mapping with an entry, then a file that includes and resets the entry", map[string]string{"f0.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        k: 5.6.7.8
`, "f1.yaml": `include: [inc.yaml]
services:
  a:
    build:
      context: .
      extra_hosts:
        h: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, []string{"f0.yaml", "f1.yaml"}, false},
		{"build.extra_hosts | earlier empty mapping, then a file that includes and resets the entry", map[string]string{"f0.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: {}
`, "f1.yaml": `include: [inc.yaml]
services:
  a:
    build:
      context: .
      extra_hosts:
        h: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, []string{"f0.yaml", "f1.yaml"}, true},
		{"build.extra_hosts | earlier without the key, then a file that includes and resets the entry", map[string]string{"f0.yaml": `services:
  a:
    image: x
`, "f1.yaml": `include: [inc.yaml]
services:
  a:
    build:
      context: .
      extra_hosts:
        h: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, []string{"f0.yaml", "f1.yaml"}, true},
		{"build.extra_hosts | one include entry, two paths, the wrapper resets both", map[string]string{"f1.yaml": `include:
  - path: [inc.yaml, inc2.yaml]
services:
  a:
    build:
      context: .
      extra_hosts:
        h: !reset null
        g: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`, "inc2.yaml": `services:
  a:
    build:
      context: .
      extra_hosts:
        g: 9.9.9.9
`}, []string{"f1.yaml"}, false},
		{"build.extra_hosts | two include entries, the wrapper resets both", map[string]string{"f1.yaml": `include: [inc.yaml, inc2.yaml]
services:
  a:
    build:
      context: .
      extra_hosts:
        h: !reset null
        g: !reset null
`, "inc.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`, "inc2.yaml": `services:
  a:
    build:
      context: .
      extra_hosts:
        g: 9.9.9.9
`}, []string{"f1.yaml"}, true},
		{"build.extra_hosts | the include gives an empty mapping, the wrapper writes nothing", map[string]string{"f1.yaml": `include: [inc.yaml]
services:
  b:
    image: y
`, "inc.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: {}
`}, []string{"f1.yaml"}, false},
		{"build.extra_hosts | the include gives an entry, the wrapper resets the service", map[string]string{"f1.yaml": `include: [inc.yaml]
services:
  a: !reset null
  b:
    image: y
`, "inc.yaml": `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, []string{"f1.yaml"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, text := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
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
			// The refusal says what is wrong, and names the file that took the entries out (the including file where the include gave them).
			if err != nil && !strings.Contains(err.Error(), "must be a mapping") {
				t.Errorf("the refusal does not say the key must be a mapping: %v", err)
			}
			if err != nil && strings.Contains(tc.name, "include then own") && !strings.Contains(err.Error(), "f1.yaml") {
				t.Errorf("the refusal does not name the including file: %v", err)
			}
		})
	}
}
