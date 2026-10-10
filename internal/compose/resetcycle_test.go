package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A block that holds itself is not a cycle when a `!reset` stands over it: docker compose drops the block before it reads it, so `x-r: !reset {a: &l [{K: v, <<: *l}]}` is read
// (rc 0, v5.5.1, `config -q`) wherever it stands — under an `x-` key, a service's key, a list, a whole service reset, the anchor itself, in a second `-f` file, in a file that is
// extended from or included — while one with no reset, under `!override` (a `!reset` inside an `!override` is not dropped), or an anchor of the reset that something outside it points at, is a cycle (rc 1). Every row is docker
// compose's answer (#1904).
func TestABlockThatHoldsItselfUnderAResetIsNotACycle(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   map[string]string
		order   []string
		refused bool
		cycle   bool // refused as an alias cycle (an alias that holds itself, not a merge key), and says so
	}{
		{"reset x- top, list self", map[string]string{"compose.yaml": `x-r: !reset {a: &l [{K: v, <<: *l}]}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, false, false},
		{"plain x- top, list self (control)", map[string]string{"compose.yaml": `x-r: {a: &l [{K: v, <<: *l}]}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, false},
		{"override x- top, list self (control)", map[string]string{"compose.yaml": `x-r: !override {a: &l [{K: v, <<: *l}]}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, false},
		{"override in service key (control)", map[string]string{"compose.yaml": `services:
  web:
    image: wi
    labels: !override {a: &l [{K: v, <<: *l}]}
`}, []string{"compose.yaml"}, true, false},
		{"reset used by alias", map[string]string{"compose.yaml": `x-r: !reset &r {a: &l [{K: v, <<: *l}]}
x-u: *r
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, false, false},
		{"reset used by a merge key outside it (control)", map[string]string{"compose.yaml": `x-r: &r {a: &l [{K: v, <<: *l}]}
services:
  web:
    image: wi
    labels: !reset {<<: *r}
`}, []string{"compose.yaml"}, true, false},
		{"an anchor in the reset used outside it (control)", map[string]string{"compose.yaml": `x-r: {b: !reset {a: &l [{K: v, <<: *l}]}, c: *l}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, false},
		{"a plain self block used elsewhere (control)", map[string]string{"compose.yaml": `x-r: {a: &l [{K: v, <<: *l}]}
x-q: *l
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, false},
		{"reset empty (control)", map[string]string{"compose.yaml": `x-r: !reset {}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, false, false},
		{"reset list in list", map[string]string{"compose.yaml": `x-r: !reset [&l [{K: v, <<: *l}]]
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, false, false},
		{"override x- top, a map that holds an alias to itself (control)", map[string]string{"compose.yaml": `x-r: !override {a: &m {K: *m}}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, true},
		{"override x- top, a list that holds itself (control)", map[string]string{"compose.yaml": `x-r: !override {a: &s [*s]}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, true},
		{"override in service key, a map that holds an alias to itself (control)", map[string]string{"compose.yaml": `services:
  web:
    image: wi
    labels: !override {a: &m {K: *m}}
`}, []string{"compose.yaml"}, true, false},
		{"reset nested under a plain mapping", map[string]string{"compose.yaml": `x-r: {b: !reset {a: &l [{K: v, <<: *l}]}}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, false, false},
		{"reset under x- top, a list that merges itself", map[string]string{"compose.yaml": `x-r: !reset {a: &l [{K: v, <<: *l}]}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, false, false},
		{"reset under a service key, a list that merges itself", map[string]string{"compose.yaml": `services:
  web:
    image: wi
    labels: !reset {a: &l [{K: v, <<: *l}]}
`}, []string{"compose.yaml"}, false, false},
		{"reset under a service key that is a list, a list that merges itself", map[string]string{"compose.yaml": `services:
  web:
    image: wi
    command: !reset [&l [{K: v, <<: *l}]]
`}, []string{"compose.yaml"}, false, false},
		{"reset under a service reset whole, a list that merges itself", map[string]string{"compose.yaml": `services:
  web: !reset
    image: wi
    labels: {a: &l [{K: v, <<: *l}]}
  other:
    image: oi
`}, []string{"compose.yaml"}, false, false},
		{"reset on the anchor itself, a list that merges itself", map[string]string{"compose.yaml": `x-r: !reset &l [{K: v, <<: *l}]
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, false, false},
		{"reset in a second -f file, a list that merges itself", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `x-r: !reset {a: &l [{K: v, <<: *l}]}
services:
  web:
    image: wi
`}, []string{"a.yaml", "b.yaml"}, false, false},
		{"reset in a file that is extended from, a list that merges itself", map[string]string{"base.yaml": `x-r: !reset {a: &l [{K: v, <<: *l}]}
services:
  y:
    image: yi
`, "e.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
`}, []string{"e.yaml"}, false, false},
		{"reset in an included file, a list that merges itself", map[string]string{"i.yaml": `include: [inc.yaml]
services:
  web:
    image: wi
`, "inc.yaml": `x-r: !reset {a: &l [{K: v, <<: *l}]}
services:
  y:
    image: yi
`}, []string{"i.yaml"}, false, false},
		{"reset under x- top, a map that merges itself", map[string]string{"compose.yaml": `x-r: !reset {a: &m {K: v, <<: *m}}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, false, false},
		{"reset under a service key, a map that merges itself", map[string]string{"compose.yaml": `services:
  web:
    image: wi
    labels: !reset {a: &m {K: v, <<: *m}}
`}, []string{"compose.yaml"}, false, false},
		{"reset under a service key that is a list, a map that merges itself", map[string]string{"compose.yaml": `services:
  web:
    image: wi
    command: !reset [&m {K: v, <<: *m}]
`}, []string{"compose.yaml"}, false, false},
		{"reset under a service reset whole, a map that merges itself", map[string]string{"compose.yaml": `services:
  web: !reset
    image: wi
    labels: {a: &m {K: v, <<: *m}}
  other:
    image: oi
`}, []string{"compose.yaml"}, false, false},
		{"reset on the anchor itself, a map that merges itself", map[string]string{"compose.yaml": `x-r: !reset &m {K: v, <<: *m}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, false, false},
		{"reset in a second -f file, a map that merges itself", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `x-r: !reset {a: &m {K: v, <<: *m}}
services:
  web:
    image: wi
`}, []string{"a.yaml", "b.yaml"}, false, false},
		{"reset in a file that is extended from, a map that merges itself", map[string]string{"base.yaml": `x-r: !reset {a: &m {K: v, <<: *m}}
services:
  y:
    image: yi
`, "e.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
`}, []string{"e.yaml"}, false, false},
		{"reset in an included file, a map that merges itself", map[string]string{"i.yaml": `include: [inc.yaml]
services:
  web:
    image: wi
`, "inc.yaml": `x-r: !reset {a: &m {K: v, <<: *m}}
services:
  y:
    image: yi
`}, []string{"i.yaml"}, false, false},
		{"reset under x- top, a map that holds an alias to itself", map[string]string{"compose.yaml": `x-r: !reset {a: &m {K: *m}}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, false, false},
		{"reset under a service key, a map that holds an alias to itself", map[string]string{"compose.yaml": `services:
  web:
    image: wi
    labels: !reset {a: &m {K: *m}}
`}, []string{"compose.yaml"}, false, false},
		{"reset under a service key that is a list, a map that holds an alias to itself", map[string]string{"compose.yaml": `services:
  web:
    image: wi
    command: !reset [&m {K: *m}]
`}, []string{"compose.yaml"}, false, false},
		{"reset under a service reset whole, a map that holds an alias to itself", map[string]string{"compose.yaml": `services:
  web: !reset
    image: wi
    labels: {a: &m {K: *m}}
  other:
    image: oi
`}, []string{"compose.yaml"}, false, false},
		{"reset on the anchor itself, a map that holds an alias to itself", map[string]string{"compose.yaml": `x-r: !reset &m {K: *m}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, false, false},
		{"reset in a second -f file, a map that holds an alias to itself", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `x-r: !reset {a: &m {K: *m}}
services:
  web:
    image: wi
`}, []string{"a.yaml", "b.yaml"}, false, false},
		{"reset in a file that is extended from, a map that holds an alias to itself", map[string]string{"base.yaml": `x-r: !reset {a: &m {K: *m}}
services:
  y:
    image: yi
`, "e.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
`}, []string{"e.yaml"}, false, false},
		{"reset in an included file, a map that holds an alias to itself", map[string]string{"i.yaml": `include: [inc.yaml]
services:
  web:
    image: wi
`, "inc.yaml": `x-r: !reset {a: &m {K: *m}}
services:
  y:
    image: yi
`}, []string{"i.yaml"}, false, false},
		{"reset under x- top, a list that holds itself", map[string]string{"compose.yaml": `x-r: !reset {a: &s [*s]}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, false, false},
		{"reset under a service key, a list that holds itself", map[string]string{"compose.yaml": `services:
  web:
    image: wi
    labels: !reset {a: &s [*s]}
`}, []string{"compose.yaml"}, false, false},
		{"reset under a service key that is a list, a list that holds itself", map[string]string{"compose.yaml": `services:
  web:
    image: wi
    command: !reset [&s [*s]]
`}, []string{"compose.yaml"}, false, false},
		{"reset under a service reset whole, a list that holds itself", map[string]string{"compose.yaml": `services:
  web: !reset
    image: wi
    labels: {a: &s [*s]}
  other:
    image: oi
`}, []string{"compose.yaml"}, false, false},
		{"reset on the anchor itself, a list that holds itself", map[string]string{"compose.yaml": `x-r: !reset &s [*s]
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, false, false},
		{"reset in a second -f file, a list that holds itself", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `x-r: !reset {a: &s [*s]}
services:
  web:
    image: wi
`}, []string{"a.yaml", "b.yaml"}, false, false},
		{"reset in a file that is extended from, a list that holds itself", map[string]string{"base.yaml": `x-r: !reset {a: &s [*s]}
services:
  y:
    image: yi
`, "e.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
`}, []string{"e.yaml"}, false, false},
		{"reset in an included file, a list that holds itself", map[string]string{"i.yaml": `include: [inc.yaml]
services:
  web:
    image: wi
`, "inc.yaml": `x-r: !reset {a: &s [*s]}
services:
  y:
    image: yi
`}, []string{"i.yaml"}, false, false},
		{"reset under x- top, a map that holds a list that holds it", map[string]string{"compose.yaml": `x-r: !reset {a: &m {K: [*m]}}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, false, false},
		{"reset under a service key, a map that holds a list that holds it", map[string]string{"compose.yaml": `services:
  web:
    image: wi
    labels: !reset {a: &m {K: [*m]}}
`}, []string{"compose.yaml"}, false, false},
		{"reset under a service key that is a list, a map that holds a list that holds it", map[string]string{"compose.yaml": `services:
  web:
    image: wi
    command: !reset [&m {K: [*m]}]
`}, []string{"compose.yaml"}, false, false},
		{"reset under a service reset whole, a map that holds a list that holds it", map[string]string{"compose.yaml": `services:
  web: !reset
    image: wi
    labels: {a: &m {K: [*m]}}
  other:
    image: oi
`}, []string{"compose.yaml"}, false, false},
		{"reset on the anchor itself, a map that holds a list that holds it", map[string]string{"compose.yaml": `x-r: !reset &m {K: [*m]}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, false, false},
		{"reset in a second -f file, a map that holds a list that holds it", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `x-r: !reset {a: &m {K: [*m]}}
services:
  web:
    image: wi
`}, []string{"a.yaml", "b.yaml"}, false, false},
		{"reset in a file that is extended from, a map that holds a list that holds it", map[string]string{"base.yaml": `x-r: !reset {a: &m {K: [*m]}}
services:
  y:
    image: yi
`, "e.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
`}, []string{"e.yaml"}, false, false},
		{"reset in an included file, a map that holds a list that holds it", map[string]string{"i.yaml": `include: [inc.yaml]
services:
  web:
    image: wi
`, "inc.yaml": `x-r: !reset {a: &m {K: [*m]}}
services:
  y:
    image: yi
`}, []string{"i.yaml"}, false, false},
		{"override over a reset, x- top, a list that merges itself", map[string]string{"compose.yaml": `x-r: !override {a: !reset &l [{K: v, <<: *l}]}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, false},
		{"override over a reset, service key, a list that merges itself", map[string]string{"compose.yaml": `services:
  web:
    image: wi
    labels: !override {a: !reset &l [{K: v, <<: *l}]}
`}, []string{"compose.yaml"}, true, false},
		{"override over a reset, list item, a list that merges itself", map[string]string{"compose.yaml": `x-r: !override [!reset &l [{K: v, <<: *l}]]
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, false},
		{"override on the anchor itself over a reset, a list that merges itself", map[string]string{"compose.yaml": `x-r: !override &o {a: !reset &l [{K: v, <<: *l}]}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, false},
		{"reset over an override over a reset, a list that merges itself", map[string]string{"compose.yaml": `x-r: !reset {b: !override {a: !reset &l [{K: v, <<: *l}]}}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, false, false},
		{"override over a reset, x- top, a map that merges itself", map[string]string{"compose.yaml": `x-r: !override {a: !reset &m {K: v, <<: *m}}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, false},
		{"override over a reset, service key, a map that merges itself", map[string]string{"compose.yaml": `services:
  web:
    image: wi
    labels: !override {a: !reset &m {K: v, <<: *m}}
`}, []string{"compose.yaml"}, true, false},
		{"override over a reset, list item, a map that merges itself", map[string]string{"compose.yaml": `x-r: !override [!reset &m {K: v, <<: *m}]
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, false},
		{"override on the anchor itself over a reset, a map that merges itself", map[string]string{"compose.yaml": `x-r: !override &o {a: !reset &m {K: v, <<: *m}}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, false},
		{"reset over an override over a reset, a map that merges itself", map[string]string{"compose.yaml": `x-r: !reset {b: !override {a: !reset &m {K: v, <<: *m}}}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, false, false},
		{"override over a reset, x- top, a map that holds an alias to itself", map[string]string{"compose.yaml": `x-r: !override {a: !reset &m {K: *m}}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, true},
		{"override over a reset, service key, a map that holds an alias to itself", map[string]string{"compose.yaml": `services:
  web:
    image: wi
    labels: !override {a: !reset &m {K: *m}}
`}, []string{"compose.yaml"}, true, false},
		{"override over a reset, list item, a map that holds an alias to itself", map[string]string{"compose.yaml": `x-r: !override [!reset &m {K: *m}]
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, true},
		{"override on the anchor itself over a reset, a map that holds an alias to itself", map[string]string{"compose.yaml": `x-r: !override &o {a: !reset &m {K: *m}}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, true},
		{"reset over an override over a reset, a map that holds an alias to itself", map[string]string{"compose.yaml": `x-r: !reset {b: !override {a: !reset &m {K: *m}}}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, false, false},
		{"override over a reset, x- top, a list that holds itself", map[string]string{"compose.yaml": `x-r: !override {a: !reset &s [*s]}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, true},
		{"override over a reset, service key, a list that holds itself", map[string]string{"compose.yaml": `services:
  web:
    image: wi
    labels: !override {a: !reset &s [*s]}
`}, []string{"compose.yaml"}, true, false},
		{"override over a reset, list item, a list that holds itself", map[string]string{"compose.yaml": `x-r: !override [!reset &s [*s]]
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, true},
		{"override on the anchor itself over a reset, a list that holds itself", map[string]string{"compose.yaml": `x-r: !override &o {a: !reset &s [*s]}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, true},
		{"reset over an override over a reset, a list that holds itself", map[string]string{"compose.yaml": `x-r: !reset {b: !override {a: !reset &s [*s]}}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, false, false},
		{"override over a reset, x- top, a map that holds a list that holds it", map[string]string{"compose.yaml": `x-r: !override {a: !reset &m {K: [*m]}}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, true},
		{"override over a reset, service key, a map that holds a list that holds it", map[string]string{"compose.yaml": `services:
  web:
    image: wi
    labels: !override {a: !reset &m {K: [*m]}}
`}, []string{"compose.yaml"}, true, false},
		{"override over a reset, list item, a map that holds a list that holds it", map[string]string{"compose.yaml": `x-r: !override [!reset &m {K: [*m]}]
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, true},
		{"override on the anchor itself over a reset, a map that holds a list that holds it", map[string]string{"compose.yaml": `x-r: !override &o {a: !reset &m {K: [*m]}}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, true},
		{"reset over an override over a reset, a map that holds a list that holds it", map[string]string{"compose.yaml": `x-r: !reset {b: !override {a: !reset &m {K: [*m]}}}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, false, false},
		{"a reset block used from under an override", map[string]string{"compose.yaml": `x-p: !reset &r [*r]
x-q: !override {b: *r}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, true, true},
		{"a reset block used from under a reset (control)", map[string]string{"compose.yaml": `x-p: !reset &r [*r]
x-q: !reset {b: *r}
services:
  web:
    image: wi
`}, []string{"compose.yaml"}, false, false},
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
			if tc.cycle && (err == nil || !strings.Contains(err.Error(), "cycle detected")) {
				t.Errorf("want the refusal to say it is a cycle, got %v", err)
			}
		})
	}
}
