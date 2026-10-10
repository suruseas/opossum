package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// The `build.secrets` of a service an extends does not take is not read key by key: a boolean, a list, a mapping or nothing as the `mode` or the `target` is fine there (docker compose v5.5.1,
// `config -q`, rc 0), and refused in a service that is taken or in a file read as it is. An item that is no mapping and no string is refused in both. Every row is measured (#2000). The date forms of
// `target`, `uid` and `gid` that docker compose refuses are not in this table (opossum reads them).
func TestBuildSecretsOfAServiceThatIsNotTakenAreNotReadKeyByKey(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   map[string]string
		order   []string
		refused bool
	}{
		{"mode: true | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, mode: true}
`}, []string{"a.yaml"}, true},
		{"mode: true | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, mode: true}
`}, []string{"a.yaml"}, true},
		{"mode: true | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: true}
`}, []string{"a.yaml"}, false},
		{"mode: true | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: true}
`}, []string{"a.yaml"}, false},
		{"mode: true | secrets-list-in-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets: true
`}, []string{"a.yaml"}, false},
		{"mode: false | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, mode: false}
`}, []string{"a.yaml"}, true},
		{"mode: false | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, mode: false}
`}, []string{"a.yaml"}, true},
		{"mode: false | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: false}
`}, []string{"a.yaml"}, false},
		{"mode: false | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: false}
`}, []string{"a.yaml"}, false},
		{"mode: false | secrets-list-in-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets: false
`}, []string{"a.yaml"}, false},
		{"mode: [1] | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, mode: [1]}
`}, []string{"a.yaml"}, true},
		{"mode: [1] | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, mode: [1]}
`}, []string{"a.yaml"}, true},
		{"mode: [1] | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: [1]}
`}, []string{"a.yaml"}, false},
		{"mode: [1] | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: [1]}
`}, []string{"a.yaml"}, false},
		{"mode: [1] | secrets-list-in-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets: [1]
`}, []string{"a.yaml"}, true},
		{"mode: {a: b} | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, mode: {a: b}}
`}, []string{"a.yaml"}, true},
		{"mode: {a: b} | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, mode: {a: b}}
`}, []string{"a.yaml"}, true},
		{"mode: {a: b} | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: {a: b}}
`}, []string{"a.yaml"}, false},
		{"mode: {a: b} | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: {a: b}}
`}, []string{"a.yaml"}, false},
		{"mode: {a: b} | secrets-list-in-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets: {a: b}
`}, []string{"a.yaml"}, false},
		{"mode: ~ | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, mode: ~}
`}, []string{"a.yaml"}, true},
		{"mode: ~ | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, mode: ~}
`}, []string{"a.yaml"}, true},
		{"mode: ~ | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: ~}
`}, []string{"a.yaml"}, false},
		{"mode: ~ | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: ~}
`}, []string{"a.yaml"}, false},
		{"mode: ~ | secrets-list-in-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets: ~
`}, []string{"a.yaml"}, false},
		{"mode: 2024-01-01 | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, mode: 2024-01-01}
`}, []string{"a.yaml"}, true},
		{"mode: 2024-01-01 | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, mode: 2024-01-01}
`}, []string{"a.yaml"}, true},
		{"mode: 2024-01-01 | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: 2024-01-01}
`}, []string{"a.yaml"}, false},
		{"mode: 2024-01-01 | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: 2024-01-01}
`}, []string{"a.yaml"}, false},
		{"mode: 2024-01-01 | secrets-list-in-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets: 2024-01-01
`}, []string{"a.yaml"}, false},
		{"mode: 1.5 | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, mode: 1.5}
`}, []string{"a.yaml"}, true},
		{"mode: 1.5 | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, mode: 1.5}
`}, []string{"a.yaml"}, true},
		{"mode: 1.5 | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: 1.5}
`}, []string{"a.yaml"}, false},
		{"mode: 1.5 | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: 1.5}
`}, []string{"a.yaml"}, false},
		{"mode: 1.5 | secrets-list-in-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets: 1.5
`}, []string{"a.yaml"}, false},
		{"mode: \"0644\" | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, mode: "0644"}
`}, []string{"a.yaml"}, false},
		{"mode: \"0644\" | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, mode: "0644"}
`}, []string{"a.yaml"}, false},
		{"mode: \"0644\" | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: "0644"}
`}, []string{"a.yaml"}, false},
		{"mode: \"0644\" | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: "0644"}
`}, []string{"a.yaml"}, false},
		{"mode: \"0644\" | secrets-list-in-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets: "0644"
`}, []string{"a.yaml"}, false},
		{"mode: 420 | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, mode: 420}
`}, []string{"a.yaml"}, false},
		{"mode: 420 | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, mode: 420}
`}, []string{"a.yaml"}, false},
		{"mode: 420 | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: 420}
`}, []string{"a.yaml"}, false},
		{"mode: 420 | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: 420}
`}, []string{"a.yaml"}, false},
		{"mode: 420 | secrets-list-in-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets: 420
`}, []string{"a.yaml"}, false},
		{"mode: abc | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, mode: abc}
`}, []string{"a.yaml"}, true},
		{"mode: abc | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, mode: abc}
`}, []string{"a.yaml"}, true},
		{"mode: abc | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: abc}
`}, []string{"a.yaml"}, false},
		{"mode: abc | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: abc}
`}, []string{"a.yaml"}, false},
		{"mode: abc | secrets-list-in-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets: abc
`}, []string{"a.yaml"}, false},
		{"mode: \"\" | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, mode: ""}
`}, []string{"a.yaml"}, true},
		{"mode: \"\" | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, mode: ""}
`}, []string{"a.yaml"}, true},
		{"mode: \"\" | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: ""}
`}, []string{"a.yaml"}, false},
		{"mode: \"\" | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: ""}
`}, []string{"a.yaml"}, false},
		{"mode: \"\" | secrets-list-in-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets: ""
`}, []string{"a.yaml"}, false},
		{"target: true | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, target: true}
`}, []string{"a.yaml"}, true},
		{"target: true | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, target: true}
`}, []string{"a.yaml"}, true},
		{"target: true | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: true}
`}, []string{"a.yaml"}, false},
		{"target: true | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: true}
`}, []string{"a.yaml"}, false},
		{"target: false | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, target: false}
`}, []string{"a.yaml"}, true},
		{"target: false | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, target: false}
`}, []string{"a.yaml"}, true},
		{"target: false | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: false}
`}, []string{"a.yaml"}, false},
		{"target: false | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: false}
`}, []string{"a.yaml"}, false},
		{"target: [1] | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, target: [1]}
`}, []string{"a.yaml"}, true},
		{"target: [1] | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, target: [1]}
`}, []string{"a.yaml"}, true},
		{"target: [1] | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: [1]}
`}, []string{"a.yaml"}, false},
		{"target: [1] | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: [1]}
`}, []string{"a.yaml"}, false},
		{"target: {a: b} | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, target: {a: b}}
`}, []string{"a.yaml"}, true},
		{"target: {a: b} | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, target: {a: b}}
`}, []string{"a.yaml"}, true},
		{"target: {a: b} | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: {a: b}}
`}, []string{"a.yaml"}, false},
		{"target: {a: b} | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: {a: b}}
`}, []string{"a.yaml"}, false},
		{"target: ~ | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, target: ~}
`}, []string{"a.yaml"}, true},
		{"target: ~ | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, target: ~}
`}, []string{"a.yaml"}, true},
		{"target: ~ | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: ~}
`}, []string{"a.yaml"}, false},
		{"target: ~ | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: ~}
`}, []string{"a.yaml"}, false},
		{"target: 2024-01-01 | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: 2024-01-01}
`}, []string{"a.yaml"}, false},
		{"target: 2024-01-01 | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: 2024-01-01}
`}, []string{"a.yaml"}, false},
		{"target: 1.5 | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, target: 1.5}
`}, []string{"a.yaml"}, true},
		{"target: 1.5 | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, target: 1.5}
`}, []string{"a.yaml"}, true},
		{"target: 1.5 | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: 1.5}
`}, []string{"a.yaml"}, false},
		{"target: 1.5 | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: 1.5}
`}, []string{"a.yaml"}, false},
		{"target: \"0644\" | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, target: "0644"}
`}, []string{"a.yaml"}, false},
		{"target: \"0644\" | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, target: "0644"}
`}, []string{"a.yaml"}, false},
		{"target: \"0644\" | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: "0644"}
`}, []string{"a.yaml"}, false},
		{"target: \"0644\" | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: "0644"}
`}, []string{"a.yaml"}, false},
		{"target: 420 | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, target: 420}
`}, []string{"a.yaml"}, true},
		{"target: 420 | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, target: 420}
`}, []string{"a.yaml"}, true},
		{"target: 420 | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: 420}
`}, []string{"a.yaml"}, false},
		{"target: 420 | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: 420}
`}, []string{"a.yaml"}, false},
		{"target: abc | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, target: abc}
`}, []string{"a.yaml"}, false},
		{"target: abc | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, target: abc}
`}, []string{"a.yaml"}, false},
		{"target: abc | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: abc}
`}, []string{"a.yaml"}, false},
		{"target: abc | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: abc}
`}, []string{"a.yaml"}, false},
		{"target: \"\" | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, target: ""}
`}, []string{"a.yaml"}, false},
		{"target: \"\" | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, target: ""}
`}, []string{"a.yaml"}, false},
		{"target: \"\" | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: ""}
`}, []string{"a.yaml"}, false},
		{"target: \"\" | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: ""}
`}, []string{"a.yaml"}, false},
		{"uid: true | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, uid: true}
`}, []string{"a.yaml"}, true},
		{"uid: true | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, uid: true}
`}, []string{"a.yaml"}, true},
		{"uid: true | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: true}
`}, []string{"a.yaml"}, false},
		{"uid: true | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: true}
`}, []string{"a.yaml"}, false},
		{"uid: false | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, uid: false}
`}, []string{"a.yaml"}, true},
		{"uid: false | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, uid: false}
`}, []string{"a.yaml"}, true},
		{"uid: false | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: false}
`}, []string{"a.yaml"}, false},
		{"uid: false | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: false}
`}, []string{"a.yaml"}, false},
		{"uid: [1] | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, uid: [1]}
`}, []string{"a.yaml"}, true},
		{"uid: [1] | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, uid: [1]}
`}, []string{"a.yaml"}, true},
		{"uid: [1] | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: [1]}
`}, []string{"a.yaml"}, false},
		{"uid: [1] | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: [1]}
`}, []string{"a.yaml"}, false},
		{"uid: {a: b} | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, uid: {a: b}}
`}, []string{"a.yaml"}, true},
		{"uid: {a: b} | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, uid: {a: b}}
`}, []string{"a.yaml"}, true},
		{"uid: {a: b} | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: {a: b}}
`}, []string{"a.yaml"}, false},
		{"uid: {a: b} | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: {a: b}}
`}, []string{"a.yaml"}, false},
		{"uid: ~ | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, uid: ~}
`}, []string{"a.yaml"}, true},
		{"uid: ~ | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, uid: ~}
`}, []string{"a.yaml"}, true},
		{"uid: ~ | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: ~}
`}, []string{"a.yaml"}, false},
		{"uid: ~ | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: ~}
`}, []string{"a.yaml"}, false},
		{"uid: 2024-01-01 | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: 2024-01-01}
`}, []string{"a.yaml"}, false},
		{"uid: 2024-01-01 | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: 2024-01-01}
`}, []string{"a.yaml"}, false},
		{"uid: 1.5 | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, uid: 1.5}
`}, []string{"a.yaml"}, true},
		{"uid: 1.5 | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, uid: 1.5}
`}, []string{"a.yaml"}, true},
		{"uid: 1.5 | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: 1.5}
`}, []string{"a.yaml"}, false},
		{"uid: 1.5 | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: 1.5}
`}, []string{"a.yaml"}, false},
		{"uid: \"0644\" | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, uid: "0644"}
`}, []string{"a.yaml"}, false},
		{"uid: \"0644\" | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, uid: "0644"}
`}, []string{"a.yaml"}, false},
		{"uid: \"0644\" | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: "0644"}
`}, []string{"a.yaml"}, false},
		{"uid: \"0644\" | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: "0644"}
`}, []string{"a.yaml"}, false},
		{"uid: 420 | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, uid: 420}
`}, []string{"a.yaml"}, true},
		{"uid: 420 | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, uid: 420}
`}, []string{"a.yaml"}, true},
		{"uid: 420 | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: 420}
`}, []string{"a.yaml"}, false},
		{"uid: 420 | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: 420}
`}, []string{"a.yaml"}, false},
		{"uid: abc | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, uid: abc}
`}, []string{"a.yaml"}, false},
		{"uid: abc | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, uid: abc}
`}, []string{"a.yaml"}, false},
		{"uid: abc | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: abc}
`}, []string{"a.yaml"}, false},
		{"uid: abc | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: abc}
`}, []string{"a.yaml"}, false},
		{"uid: \"\" | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, uid: ""}
`}, []string{"a.yaml"}, false},
		{"uid: \"\" | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, uid: ""}
`}, []string{"a.yaml"}, false},
		{"uid: \"\" | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: ""}
`}, []string{"a.yaml"}, false},
		{"uid: \"\" | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, uid: ""}
`}, []string{"a.yaml"}, false},
		{"gid: true | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, gid: true}
`}, []string{"a.yaml"}, true},
		{"gid: true | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, gid: true}
`}, []string{"a.yaml"}, true},
		{"gid: true | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: true}
`}, []string{"a.yaml"}, false},
		{"gid: true | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: true}
`}, []string{"a.yaml"}, false},
		{"gid: false | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, gid: false}
`}, []string{"a.yaml"}, true},
		{"gid: false | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, gid: false}
`}, []string{"a.yaml"}, true},
		{"gid: false | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: false}
`}, []string{"a.yaml"}, false},
		{"gid: false | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: false}
`}, []string{"a.yaml"}, false},
		{"gid: [1] | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, gid: [1]}
`}, []string{"a.yaml"}, true},
		{"gid: [1] | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, gid: [1]}
`}, []string{"a.yaml"}, true},
		{"gid: [1] | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: [1]}
`}, []string{"a.yaml"}, false},
		{"gid: [1] | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: [1]}
`}, []string{"a.yaml"}, false},
		{"gid: {a: b} | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, gid: {a: b}}
`}, []string{"a.yaml"}, true},
		{"gid: {a: b} | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, gid: {a: b}}
`}, []string{"a.yaml"}, true},
		{"gid: {a: b} | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: {a: b}}
`}, []string{"a.yaml"}, false},
		{"gid: {a: b} | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: {a: b}}
`}, []string{"a.yaml"}, false},
		{"gid: ~ | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, gid: ~}
`}, []string{"a.yaml"}, true},
		{"gid: ~ | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, gid: ~}
`}, []string{"a.yaml"}, true},
		{"gid: ~ | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: ~}
`}, []string{"a.yaml"}, false},
		{"gid: ~ | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: ~}
`}, []string{"a.yaml"}, false},
		{"gid: 2024-01-01 | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: 2024-01-01}
`}, []string{"a.yaml"}, false},
		{"gid: 2024-01-01 | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: 2024-01-01}
`}, []string{"a.yaml"}, false},
		{"gid: 1.5 | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, gid: 1.5}
`}, []string{"a.yaml"}, true},
		{"gid: 1.5 | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, gid: 1.5}
`}, []string{"a.yaml"}, true},
		{"gid: 1.5 | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: 1.5}
`}, []string{"a.yaml"}, false},
		{"gid: 1.5 | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: 1.5}
`}, []string{"a.yaml"}, false},
		{"gid: \"0644\" | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, gid: "0644"}
`}, []string{"a.yaml"}, false},
		{"gid: \"0644\" | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, gid: "0644"}
`}, []string{"a.yaml"}, false},
		{"gid: \"0644\" | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: "0644"}
`}, []string{"a.yaml"}, false},
		{"gid: \"0644\" | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: "0644"}
`}, []string{"a.yaml"}, false},
		{"gid: 420 | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, gid: 420}
`}, []string{"a.yaml"}, true},
		{"gid: 420 | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, gid: 420}
`}, []string{"a.yaml"}, true},
		{"gid: 420 | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: 420}
`}, []string{"a.yaml"}, false},
		{"gid: 420 | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: 420}
`}, []string{"a.yaml"}, false},
		{"gid: abc | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, gid: abc}
`}, []string{"a.yaml"}, false},
		{"gid: abc | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, gid: abc}
`}, []string{"a.yaml"}, false},
		{"gid: abc | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: abc}
`}, []string{"a.yaml"}, false},
		{"gid: abc | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: abc}
`}, []string{"a.yaml"}, false},
		{"gid: \"\" | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: s, gid: ""}
`}, []string{"a.yaml"}, false},
		{"gid: \"\" | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, gid: ""}
`}, []string{"a.yaml"}, false},
		{"gid: \"\" | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: ""}
`}, []string{"a.yaml"}, false},
		{"gid: \"\" | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, gid: ""}
`}, []string{"a.yaml"}, false},
		{"source: true | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: true}
`}, []string{"a.yaml"}, true},
		{"source: true | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: true}
`}, []string{"a.yaml"}, true},
		{"source: true | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: true}
`}, []string{"a.yaml"}, false},
		{"source: true | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: true}
`}, []string{"a.yaml"}, false},
		{"source: false | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: false}
`}, []string{"a.yaml"}, true},
		{"source: false | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: false}
`}, []string{"a.yaml"}, true},
		{"source: false | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: false}
`}, []string{"a.yaml"}, false},
		{"source: false | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: false}
`}, []string{"a.yaml"}, false},
		{"source: [1] | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: [1]}
`}, []string{"a.yaml"}, true},
		{"source: [1] | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: [1]}
`}, []string{"a.yaml"}, true},
		{"source: [1] | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: [1]}
`}, []string{"a.yaml"}, false},
		{"source: [1] | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: [1]}
`}, []string{"a.yaml"}, false},
		{"source: {a: b} | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: {a: b}}
`}, []string{"a.yaml"}, true},
		{"source: {a: b} | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: {a: b}}
`}, []string{"a.yaml"}, true},
		{"source: {a: b} | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: {a: b}}
`}, []string{"a.yaml"}, false},
		{"source: {a: b} | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: {a: b}}
`}, []string{"a.yaml"}, false},
		{"source: ~ | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: ~}
`}, []string{"a.yaml"}, true},
		{"source: ~ | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: ~}
`}, []string{"a.yaml"}, true},
		{"source: ~ | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: ~}
`}, []string{"a.yaml"}, false},
		{"source: ~ | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: ~}
`}, []string{"a.yaml"}, false},
		{"source: 2024-01-01 | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: 2024-01-01}
`}, []string{"a.yaml"}, true},
		{"source: 2024-01-01 | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: 2024-01-01}
`}, []string{"a.yaml"}, true},
		{"source: 2024-01-01 | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: 2024-01-01}
`}, []string{"a.yaml"}, false},
		{"source: 2024-01-01 | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: 2024-01-01}
`}, []string{"a.yaml"}, false},
		{"source: 1.5 | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: 1.5}
`}, []string{"a.yaml"}, true},
		{"source: 1.5 | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: 1.5}
`}, []string{"a.yaml"}, true},
		{"source: 1.5 | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: 1.5}
`}, []string{"a.yaml"}, false},
		{"source: 1.5 | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: 1.5}
`}, []string{"a.yaml"}, false},
		{"source: \"0644\" | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: "0644"}
`}, []string{"a.yaml"}, true},
		{"source: \"0644\" | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: "0644"}
`}, []string{"a.yaml"}, true},
		{"source: \"0644\" | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: "0644"}
`}, []string{"a.yaml"}, false},
		{"source: \"0644\" | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: "0644"}
`}, []string{"a.yaml"}, false},
		{"source: 420 | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: 420}
`}, []string{"a.yaml"}, true},
		{"source: 420 | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: 420}
`}, []string{"a.yaml"}, true},
		{"source: 420 | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: 420}
`}, []string{"a.yaml"}, false},
		{"source: 420 | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: 420}
`}, []string{"a.yaml"}, false},
		{"source: abc | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: abc}
`}, []string{"a.yaml"}, true},
		{"source: abc | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: abc}
`}, []string{"a.yaml"}, true},
		{"source: abc | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: abc}
`}, []string{"a.yaml"}, false},
		{"source: abc | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: abc}
`}, []string{"a.yaml"}, false},
		{"source: \"\" | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    image: wi
    build:
      context: .
      secrets:
        - {source: ""}
`}, []string{"a.yaml"}, true},
		{"source: \"\" | taken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: ""}
`}, []string{"a.yaml"}, true},
		{"source: \"\" | untaken", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: ""}
`}, []string{"a.yaml"}, false},
		{"source: \"\" | taken-also-untaken-bad", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      context: .
      secrets: [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: ""}
`}, []string{"a.yaml"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "s.txt"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			for name, text := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			paths := make([]string, len(tc.order))
			for i, f := range tc.order {
				paths[i] = filepath.Join(dir, f)
			}
			if _, err := LoadFiles(paths, nil); (err != nil) != tc.refused {
				t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
			}
		})
	}
}
