package compose

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// docker compose holds `environment`, `labels` and `build.args` as a list once two sources have written the key, and a `!reset` of one name in a later file then reaches none
// of them. The two sources are not only `-f` files: an `include` that brings the key in beside what the including file writes, two includes that write it, and a service
// that `extends` another and writes the key itself count the same (v5.5.1, `config`, every row measured), while an include or an extends that is the only writer leaves the name
// to be reached, and a later `!override` or a `!reset` of the whole key does what it does everywhere (#1794).
func TestTwoSourcesInsideOneFileMakeTheKeyAListAsTwoFilesDo(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		files     map[string]string
		order     []string
		want      string // the names left, in order, one letter each; ERR where docker compose refuses the files (a service left with no image and no build)
	}{
		{"environment include+own, later !reset null A", "environment", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    environment:
      C: c
`, "b.yaml": `services:
  web:
    environment:
      A: !reset null
`, "inc.yaml": `services:
  web:
    image: wi
    environment:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"environment include only (control), later !reset null A", "environment", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  other:
    image: oi
`, "b.yaml": `services:
  web:
    environment:
      A: !reset null
`, "inc.yaml": `services:
  web:
    image: wi
    environment:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml"}, "B"},
		{"environment extends+own, later !reset null A", "environment", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
    environment:
      C: c
`, "b.yaml": `services:
  web:
    environment:
      A: !reset null
`, "base.yaml": `services:
  y:
    image: yi
    environment:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"environment extends only (control), later !reset null A", "environment", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
`, "b.yaml": `services:
  web:
    environment:
      A: !reset null
`, "base.yaml": `services:
  y:
    image: yi
    environment:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml"}, "B"},
		{"environment two includes write it, later !reset null A", "environment", map[string]string{"a.yaml": `include: [i1.yaml, i2.yaml]
services:
  other:
    image: oi
`, "b.yaml": `services:
  web:
    environment:
      A: !reset null
`, "i1.yaml": `services:
  web:
    image: wi
    environment:
      A: a
      B: b
`, "i2.yaml": `services:
  web:
    environment:
      C: c
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"environment same-file extends+own, later !reset null A", "environment", map[string]string{"a.yaml": `services:
  y:
    image: yi
    environment:
      A: a
      B: b
  web:
    extends: y
    environment:
      C: c
`, "b.yaml": `services:
  web:
    environment:
      A: !reset null
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"environment include+own, later !override z A", "environment", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    environment:
      C: c
`, "b.yaml": `services:
  web:
    environment:
      A: !override z
`, "inc.yaml": `services:
  web:
    image: wi
    environment:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"environment include only (control), later !override z A", "environment", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  other:
    image: oi
`, "b.yaml": `services:
  web:
    environment:
      A: !override z
`, "inc.yaml": `services:
  web:
    image: wi
    environment:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml"}, "AB"},
		{"environment extends+own, later !override z A", "environment", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
    environment:
      C: c
`, "b.yaml": `services:
  web:
    environment:
      A: !override z
`, "base.yaml": `services:
  y:
    image: yi
    environment:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"environment extends only (control), later !override z A", "environment", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
`, "b.yaml": `services:
  web:
    environment:
      A: !override z
`, "base.yaml": `services:
  y:
    image: yi
    environment:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml"}, "AB"},
		{"environment two includes write it, later !override z A", "environment", map[string]string{"a.yaml": `include: [i1.yaml, i2.yaml]
services:
  other:
    image: oi
`, "b.yaml": `services:
  web:
    environment:
      A: !override z
`, "i1.yaml": `services:
  web:
    image: wi
    environment:
      A: a
      B: b
`, "i2.yaml": `services:
  web:
    environment:
      C: c
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"environment same-file extends+own, later !override z A", "environment", map[string]string{"a.yaml": `services:
  y:
    image: yi
    environment:
      A: a
      B: b
  web:
    extends: y
    environment:
      C: c
`, "b.yaml": `services:
  web:
    environment:
      A: !override z
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"environment include+own, later file resets the whole key", "environment", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    environment:
      C: c
`, "b.yaml": `services:
  web:
    environment: !reset null
`, "inc.yaml": `services:
  web:
    image: wi
    environment:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml"}, ""},
		{"environment include+own, f2 writes again, f3 resets A", "environment", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    environment:
      C: c
`, "b.yaml": `services:
  web:
    environment:
      D: d
`, "c.yaml": `services:
  web:
    environment:
      A: !reset null
`, "inc.yaml": `services:
  web:
    image: wi
    environment:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, "ABCD"},
		{"labels include+own, later !reset null A", "labels", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    labels:
      C: c
`, "b.yaml": `services:
  web:
    labels:
      A: !reset null
`, "inc.yaml": `services:
  web:
    image: wi
    labels:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"labels include only (control), later !reset null A", "labels", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  other:
    image: oi
`, "b.yaml": `services:
  web:
    labels:
      A: !reset null
`, "inc.yaml": `services:
  web:
    image: wi
    labels:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml"}, "B"},
		{"labels extends+own, later !reset null A", "labels", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
    labels:
      C: c
`, "b.yaml": `services:
  web:
    labels:
      A: !reset null
`, "base.yaml": `services:
  y:
    image: yi
    labels:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"labels extends only (control), later !reset null A", "labels", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
`, "b.yaml": `services:
  web:
    labels:
      A: !reset null
`, "base.yaml": `services:
  y:
    image: yi
    labels:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml"}, "B"},
		{"labels two includes write it, later !reset null A", "labels", map[string]string{"a.yaml": `include: [i1.yaml, i2.yaml]
services:
  other:
    image: oi
`, "b.yaml": `services:
  web:
    labels:
      A: !reset null
`, "i1.yaml": `services:
  web:
    image: wi
    labels:
      A: a
      B: b
`, "i2.yaml": `services:
  web:
    labels:
      C: c
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"labels same-file extends+own, later !reset null A", "labels", map[string]string{"a.yaml": `services:
  y:
    image: yi
    labels:
      A: a
      B: b
  web:
    extends: y
    labels:
      C: c
`, "b.yaml": `services:
  web:
    labels:
      A: !reset null
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"labels include+own, later !override z A", "labels", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    labels:
      C: c
`, "b.yaml": `services:
  web:
    labels:
      A: !override z
`, "inc.yaml": `services:
  web:
    image: wi
    labels:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"labels include only (control), later !override z A", "labels", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  other:
    image: oi
`, "b.yaml": `services:
  web:
    labels:
      A: !override z
`, "inc.yaml": `services:
  web:
    image: wi
    labels:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml"}, "AB"},
		{"labels extends+own, later !override z A", "labels", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
    labels:
      C: c
`, "b.yaml": `services:
  web:
    labels:
      A: !override z
`, "base.yaml": `services:
  y:
    image: yi
    labels:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"labels extends only (control), later !override z A", "labels", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
`, "b.yaml": `services:
  web:
    labels:
      A: !override z
`, "base.yaml": `services:
  y:
    image: yi
    labels:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml"}, "AB"},
		{"labels two includes write it, later !override z A", "labels", map[string]string{"a.yaml": `include: [i1.yaml, i2.yaml]
services:
  other:
    image: oi
`, "b.yaml": `services:
  web:
    labels:
      A: !override z
`, "i1.yaml": `services:
  web:
    image: wi
    labels:
      A: a
      B: b
`, "i2.yaml": `services:
  web:
    labels:
      C: c
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"labels same-file extends+own, later !override z A", "labels", map[string]string{"a.yaml": `services:
  y:
    image: yi
    labels:
      A: a
      B: b
  web:
    extends: y
    labels:
      C: c
`, "b.yaml": `services:
  web:
    labels:
      A: !override z
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"labels include+own, later file resets the whole key", "labels", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    labels:
      C: c
`, "b.yaml": `services:
  web:
    labels: !reset null
`, "inc.yaml": `services:
  web:
    image: wi
    labels:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml"}, ""},
		{"labels include+own, f2 writes again, f3 resets A", "labels", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    labels:
      C: c
`, "b.yaml": `services:
  web:
    labels:
      D: d
`, "c.yaml": `services:
  web:
    labels:
      A: !reset null
`, "inc.yaml": `services:
  web:
    image: wi
    labels:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, "ABCD"},
		{"build.args include+own, later !reset null A", "build.args", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    build:
      context: .
      args:
        C: c
`, "b.yaml": `services:
  web:
    build:
      args:
        A: !reset null
`, "inc.yaml": `services:
  web:
    build:
      context: .
      args:
        A: a
        B: b
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"build.args include only (control), later !reset null A", "build.args", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  other:
    image: oi
`, "b.yaml": `services:
  web:
    build:
      args:
        A: !reset null
`, "inc.yaml": `services:
  web:
    build:
      context: .
      args:
        A: a
        B: b
`}, []string{"a.yaml", "b.yaml"}, "B"},
		{"build.args extends+own, later !reset null A", "build.args", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
    build:
      context: .
      args:
        C: c
`, "b.yaml": `services:
  web:
    build:
      args:
        A: !reset null
`, "base.yaml": `services:
  y:
    build:
      context: .
      args:
        A: a
        B: b
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"build.args extends only (control), later !reset null A", "build.args", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
`, "b.yaml": `services:
  web:
    build:
      args:
        A: !reset null
`, "base.yaml": `services:
  y:
    build:
      context: .
      args:
        A: a
        B: b
`}, []string{"a.yaml", "b.yaml"}, "B"},
		{"build.args two includes write it, later !reset null A", "build.args", map[string]string{"a.yaml": `include: [i1.yaml, i2.yaml]
services:
  other:
    image: oi
`, "b.yaml": `services:
  web:
    build:
      args:
        A: !reset null
`, "i1.yaml": `services:
  web:
    build:
      context: .
      args:
        A: a
        B: b
`, "i2.yaml": `services:
  web:
    build:
      context: .
      args:
        C: c
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"build.args same-file extends+own, later !reset null A", "build.args", map[string]string{"a.yaml": `services:
  y:
    build:
      context: .
      args:
        A: a
        B: b
  web:
    extends: y
    build:
      context: .
      args:
        C: c
`, "b.yaml": `services:
  web:
    build:
      args:
        A: !reset null
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"build.args include+own, later !override z A", "build.args", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    build:
      context: .
      args:
        C: c
`, "b.yaml": `services:
  web:
    build:
      args:
        A: !override z
`, "inc.yaml": `services:
  web:
    build:
      context: .
      args:
        A: a
        B: b
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"build.args include only (control), later !override z A", "build.args", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  other:
    image: oi
`, "b.yaml": `services:
  web:
    build:
      args:
        A: !override z
`, "inc.yaml": `services:
  web:
    build:
      context: .
      args:
        A: a
        B: b
`}, []string{"a.yaml", "b.yaml"}, "AB"},
		{"build.args extends+own, later !override z A", "build.args", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
    build:
      context: .
      args:
        C: c
`, "b.yaml": `services:
  web:
    build:
      args:
        A: !override z
`, "base.yaml": `services:
  y:
    build:
      context: .
      args:
        A: a
        B: b
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"build.args extends only (control), later !override z A", "build.args", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
`, "b.yaml": `services:
  web:
    build:
      args:
        A: !override z
`, "base.yaml": `services:
  y:
    build:
      context: .
      args:
        A: a
        B: b
`}, []string{"a.yaml", "b.yaml"}, "AB"},
		{"build.args two includes write it, later !override z A", "build.args", map[string]string{"a.yaml": `include: [i1.yaml, i2.yaml]
services:
  other:
    image: oi
`, "b.yaml": `services:
  web:
    build:
      args:
        A: !override z
`, "i1.yaml": `services:
  web:
    build:
      context: .
      args:
        A: a
        B: b
`, "i2.yaml": `services:
  web:
    build:
      context: .
      args:
        C: c
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"build.args same-file extends+own, later !override z A", "build.args", map[string]string{"a.yaml": `services:
  y:
    build:
      context: .
      args:
        A: a
        B: b
  web:
    extends: y
    build:
      context: .
      args:
        C: c
`, "b.yaml": `services:
  web:
    build:
      args:
        A: !override z
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"build.args include+own, later file resets the whole key", "build.args", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    build:
      context: .
      args:
        C: c
`, "b.yaml": `services:
  web:
    build: !reset null
`, "inc.yaml": `services:
  web:
    build:
      context: .
      args:
        A: a
        B: b
`}, []string{"a.yaml", "b.yaml"}, "ERR"},
		{"build.args include+own, f2 writes again, f3 resets A", "build.args", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    build:
      context: .
      args:
        C: c
`, "b.yaml": `services:
  web:
    build:
      context: .
      args:
        D: d
`, "c.yaml": `services:
  web:
    build:
      args:
        A: !reset null
`, "inc.yaml": `services:
  web:
    build:
      context: .
      args:
        A: a
        B: b
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, "ABCD"},
		{"environment include+own in the second file, third resets A", "environment", map[string]string{"f1.yaml": `services:
  web:
    image: wi
`, "f2.yaml": `include: [inc.yaml]
services:
  web:
    environment:
      C: c
`, "f3.yaml": `services:
  web:
    environment:
      A: !reset null
`, "inc.yaml": `services:
  web:
    image: wi
    environment:
      A: a
      B: b
`}, []string{"f1.yaml", "f2.yaml", "f3.yaml"}, "ABC"},
		{"environment extends+own in the second file, third resets A", "environment", map[string]string{"base.yaml": `services:
  y:
    image: yi
    environment:
      A: a
      B: b
`, "f1.yaml": `services:
  web:
    image: wi
`, "f2.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
    environment:
      C: c
`, "f3.yaml": `services:
  web:
    environment:
      A: !reset null
`}, []string{"f1.yaml", "f2.yaml", "f3.yaml"}, "ABC"},
		{"environment mixed in the first file, taken out whole, written again, then A reset", "environment", map[string]string{"f1.yaml": `include: [inc.yaml]
services:
  web:
    environment:
      C: c
`, "f2.yaml": `services:
  web:
    environment: !reset null
`, "f3.yaml": `services:
  web:
    image: wi
    environment:
      A: a
      B: b
`, "f4.yaml": `services:
  web:
    environment:
      A: !reset null
`, "inc.yaml": `services:
  web:
    image: wi
    environment:
      A: a
      B: b
`}, []string{"f1.yaml", "f2.yaml", "f3.yaml", "f4.yaml"}, "B"},
		{"environment a nested include writes it beside its own, later resets A", "environment", map[string]string{"a.yaml": `include: [mid.yaml]
services:
  other:
    image: oi
`, "b.yaml": `services:
  web:
    environment:
      A: !reset null
`, "inc.yaml": `services:
  web:
    image: wi
    environment:
      A: a
      B: b
`, "mid.yaml": `include: [inc.yaml]
services:
  web:
    environment:
      C: c
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"labels include+own in the second file, third resets A", "labels", map[string]string{"f1.yaml": `services:
  web:
    image: wi
`, "f2.yaml": `include: [inc.yaml]
services:
  web:
    labels:
      C: c
`, "f3.yaml": `services:
  web:
    labels:
      A: !reset null
`, "inc.yaml": `services:
  web:
    image: wi
    labels:
      A: a
      B: b
`}, []string{"f1.yaml", "f2.yaml", "f3.yaml"}, "ABC"},
		{"labels extends+own in the second file, third resets A", "labels", map[string]string{"base.yaml": `services:
  y:
    image: yi
    labels:
      A: a
      B: b
`, "f1.yaml": `services:
  web:
    image: wi
`, "f2.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
    labels:
      C: c
`, "f3.yaml": `services:
  web:
    labels:
      A: !reset null
`}, []string{"f1.yaml", "f2.yaml", "f3.yaml"}, "ABC"},
		{"labels mixed in the first file, taken out whole, written again, then A reset", "labels", map[string]string{"f1.yaml": `include: [inc.yaml]
services:
  web:
    labels:
      C: c
`, "f2.yaml": `services:
  web:
    labels: !reset null
`, "f3.yaml": `services:
  web:
    image: wi
    labels:
      A: a
      B: b
`, "f4.yaml": `services:
  web:
    labels:
      A: !reset null
`, "inc.yaml": `services:
  web:
    image: wi
    labels:
      A: a
      B: b
`}, []string{"f1.yaml", "f2.yaml", "f3.yaml", "f4.yaml"}, "B"},
		{"labels a nested include writes it beside its own, later resets A", "labels", map[string]string{"a.yaml": `include: [mid.yaml]
services:
  other:
    image: oi
`, "b.yaml": `services:
  web:
    labels:
      A: !reset null
`, "inc.yaml": `services:
  web:
    image: wi
    labels:
      A: a
      B: b
`, "mid.yaml": `include: [inc.yaml]
services:
  web:
    labels:
      C: c
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"build.args include+own in the second file, third resets A", "build.args", map[string]string{"f1.yaml": `services:
  web:
    build:
      context: .
`, "f2.yaml": `include: [inc.yaml]
services:
  web:
    build:
      context: .
      args:
        C: c
`, "f3.yaml": `services:
  web:
    build:
      args:
        A: !reset null
`, "inc.yaml": `services:
  web:
    build:
      context: .
      args:
        A: a
        B: b
`}, []string{"f1.yaml", "f2.yaml", "f3.yaml"}, "ABC"},
		{"build.args extends+own in the second file, third resets A", "build.args", map[string]string{"base.yaml": `services:
  y:
    build:
      context: .
      args:
        A: a
        B: b
`, "f1.yaml": `services:
  web:
    build:
      context: .
`, "f2.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
    build:
      context: .
      args:
        C: c
`, "f3.yaml": `services:
  web:
    build:
      args:
        A: !reset null
`}, []string{"f1.yaml", "f2.yaml", "f3.yaml"}, "ABC"},
		{"build.args mixed in the first file, taken out whole, written again, then A reset", "build.args", map[string]string{"f1.yaml": `include: [inc.yaml]
services:
  web:
    build:
      context: .
      args:
        C: c
`, "f2.yaml": `services:
  web:
    build: !reset null
`, "f3.yaml": `services:
  web:
    build:
      context: .
      args:
        A: a
        B: b
`, "f4.yaml": `services:
  web:
    build:
      args:
        A: !reset null
`, "inc.yaml": `services:
  web:
    build:
      context: .
      args:
        A: a
        B: b
`}, []string{"f1.yaml", "f2.yaml", "f3.yaml", "f4.yaml"}, "B"},
		{"build.args a nested include writes it beside its own, later resets A", "build.args", map[string]string{"a.yaml": `include: [mid.yaml]
services:
  other:
    image: oi
`, "b.yaml": `services:
  web:
    build:
      args:
        A: !reset null
`, "inc.yaml": `services:
  web:
    build:
      context: .
      args:
        A: a
        B: b
`, "mid.yaml": `include: [inc.yaml]
services:
  web:
    build:
      context: .
      args:
        C: c
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"environment one include entry with two paths, later resets A", "environment", map[string]string{"a.yaml": `include:
  - path: [i1.yaml, i2.yaml]
services:
  other:
    image: oi
`, "b.yaml": `services:
  web:
    environment:
      A: !reset null
`, "i1.yaml": `services:
  web:
    image: wi
    environment:
      A: a
      B: b
`, "i2.yaml": `services:
  web:
    environment:
      C: c
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"environment the extending service alone writes it, later resets A", "environment", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
    environment:
      A: a
      B: b
`, "b.yaml": `services:
  web:
    environment:
      A: !reset null
`, "base.yaml": `services:
  y:
    image: yi
`}, []string{"a.yaml", "b.yaml"}, "B"},
		{"environment the extended service alone writes it, own overrides, later resets C", "environment", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    environment: !override
      C: c
`, "b.yaml": `services:
  web:
    environment:
      C: !reset null
`, "inc.yaml": `services:
  web:
    image: wi
    environment:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml"}, ""},
		{"environment an included file whose service extends and writes it, later resets A", "environment", map[string]string{"a.yaml": `include: [inc.yaml]
`, "b.yaml": `services:
  web:
    environment:
      A: !reset null
`, "inc.yaml": `services:
  y:
    image: yi
    environment:
      A: a
      B: b
  web:
    extends: y
    environment:
      C: c
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"labels one include entry with two paths, later resets A", "labels", map[string]string{"a.yaml": `include:
  - path: [i1.yaml, i2.yaml]
services:
  other:
    image: oi
`, "b.yaml": `services:
  web:
    labels:
      A: !reset null
`, "i1.yaml": `services:
  web:
    image: wi
    labels:
      A: a
      B: b
`, "i2.yaml": `services:
  web:
    labels:
      C: c
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"labels the extending service alone writes it, later resets A", "labels", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
    labels:
      A: a
      B: b
`, "b.yaml": `services:
  web:
    labels:
      A: !reset null
`, "base.yaml": `services:
  y:
    image: yi
`}, []string{"a.yaml", "b.yaml"}, "B"},
		{"labels the extended service alone writes it, own overrides, later resets C", "labels", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    labels: !override
      C: c
`, "b.yaml": `services:
  web:
    labels:
      C: !reset null
`, "inc.yaml": `services:
  web:
    image: wi
    labels:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml"}, ""},
		{"labels an included file whose service extends and writes it, later resets A", "labels", map[string]string{"a.yaml": `include: [inc.yaml]
`, "b.yaml": `services:
  web:
    labels:
      A: !reset null
`, "inc.yaml": `services:
  y:
    image: yi
    labels:
      A: a
      B: b
  web:
    extends: y
    labels:
      C: c
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"environment as lists, include+own, later resets A", "environment", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    environment: [C=c]
`, "b.yaml": `services:
  web:
    environment:
      A: !reset null
`, "inc.yaml": `services:
  web:
    image: wi
    environment: [A=a, B=b]
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"environment as a list over a mapping, include+own, later resets A", "environment", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    environment: [C=c]
`, "b.yaml": `services:
  web:
    environment:
      A: !reset null
`, "inc.yaml": `services:
  web:
    image: wi
    environment:
      A: a
      B: b
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"environment as a mapping over a list, include+own, later resets A", "environment", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    environment:
      C: c
`, "b.yaml": `services:
  web:
    environment:
      A: !reset null
`, "inc.yaml": `services:
  web:
    image: wi
    environment: [A=a, B=b]
`}, []string{"a.yaml", "b.yaml"}, "ABC"},
		{"extra_hosts include+own, later resets every name", "extra_hosts", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    extra_hosts:
      b: 2.2.2.2
`, "b.yaml": `services:
  web:
    extra_hosts:
      a: !reset null
      b: !reset null
`, "inc.yaml": `services:
  web:
    image: wi
    extra_hosts:
      a: 1.1.1.1
`}, []string{"a.yaml", "b.yaml"}, ""},
		{"extra_hosts extends+own, later resets every name", "extra_hosts", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: y}
    extra_hosts:
      b: 2.2.2.2
`, "b.yaml": `services:
  web:
    extra_hosts:
      a: !reset null
      b: !reset null
`, "base.yaml": `services:
  y:
    image: yi
    extra_hosts:
      a: 1.1.1.1
`}, []string{"a.yaml", "b.yaml"}, ""},
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
			p, err := LoadFiles(paths, nil)
			if tc.want == "ERR" {
				if err == nil {
					t.Errorf("docker compose refuses it, and it was read")
				}
				return
			}
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			svc := p.Services["web"]
			var entries []string
			switch tc.key {
			case "environment":
				entries = svc.Environment
			case "labels":
				entries = svc.Labels
			case "extra_hosts":
				// Not shown by `config`: what the rows ask is that the files load (every name taken out by a later file's `!reset` was a refusal before).
			default:
				if svc.Build != nil {
					entries = svc.Build.Args
				}
			}
			var names []string
			for _, e := range entries {
				names = append(names, strings.SplitN(e, "=", 2)[0])
			}
			sort.Strings(names)
			if got := strings.Join(names, ""); got != tc.want {
				t.Errorf("names left = %q, want %q (%v)", got, tc.want, entries)
			}
		})
	}
}
