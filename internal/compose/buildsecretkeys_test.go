package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// A long-form entry of `build.secrets` holds the keys `source`, `target`, `uid`, `gid` and `mode` (and `x-` notes): any other key is refused, as docker compose refuses it
// (`additional properties 'bogus' not allowed`), in a service that is taken, and left unread in one nothing takes. In a file that is only extended from it is asked of the service the extends
// results in, so an extender that writes the list over it makes the file fine. Every row is measured (docker compose v5.5.1, `config -q`, #2037).
func TestABuildSecretsEntryWithAKeyDockerComposeDoesNotTakeIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   map[string]string
		order   []string
		refused bool
	}{
		{"{source: s, bogus: 1} | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, bogus: 1}
`}, []string{"a.yaml"}, true},
		{"{source: s, bogus: 1} | taken", map[string]string{"a.yaml": `secrets:
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
        - {source: s, bogus: 1}
`}, []string{"a.yaml"}, true},
		{"{source: s, bogus: 1} | untaken", map[string]string{"a.yaml": `secrets:
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
        - {source: s, bogus: 1}
`}, []string{"a.yaml"}, false},
		{"{source: s, bogus: 1} | profile off", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    profiles: [p]
    build:
      context: .
      secrets:
        - {source: s, bogus: 1}
`}, []string{"a.yaml"}, true},
		{"{source: s, bogus: 1} | second -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      context: .
      secrets:
        - {source: s, bogus: 1}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{source: s, bogus: 1} | over-written by later -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, bogus: 1}
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      secrets: !override [s]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{source: s, bogus: 1} | include", map[string]string{"a.yaml": `include:
  - inc.yaml
services:
  z:
    image: zi
`, "inc.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, bogus: 1}
`}, []string{"a.yaml"}, true},
		{"{source: s, bogus: 1} | override tag", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets: !override
        - {source: s, bogus: 1}
`}, []string{"a.yaml"}, true},
		{"{source: s, bogus: 1} | later extends over", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      secrets: !override [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, bogus: 1}
`}, []string{"a.yaml"}, false},
		{"{source: s, bogus: 1} | anchor entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {source: s, bogus: 1}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - *e
`}, []string{"a.yaml"}, true},
		{"{source: s, bogus: 1} | merge key entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {source: s, bogus: 1}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - <<: *e
          target: t
`}, []string{"a.yaml"}, true},
		{"{source: s, bogus: 1} | whole-service alias", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-s: &s
  image: yi
  build:
    context: .
    secrets:
      - {source: s, bogus: 1}
services:
  y: *s
`}, []string{"a.yaml"}, true},
		{"{source: s, mod: 0440} | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mod: 0440}
`}, []string{"a.yaml"}, true},
		{"{source: s, mod: 0440} | taken", map[string]string{"a.yaml": `secrets:
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
        - {source: s, mod: 0440}
`}, []string{"a.yaml"}, true},
		{"{source: s, mod: 0440} | untaken", map[string]string{"a.yaml": `secrets:
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
        - {source: s, mod: 0440}
`}, []string{"a.yaml"}, false},
		{"{source: s, mod: 0440} | profile off", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    profiles: [p]
    build:
      context: .
      secrets:
        - {source: s, mod: 0440}
`}, []string{"a.yaml"}, true},
		{"{source: s, mod: 0440} | second -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      context: .
      secrets:
        - {source: s, mod: 0440}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{source: s, mod: 0440} | over-written by later -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mod: 0440}
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      secrets: !override [s]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{source: s, mod: 0440} | include", map[string]string{"a.yaml": `include:
  - inc.yaml
services:
  z:
    image: zi
`, "inc.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mod: 0440}
`}, []string{"a.yaml"}, true},
		{"{source: s, mod: 0440} | override tag", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets: !override
        - {source: s, mod: 0440}
`}, []string{"a.yaml"}, true},
		{"{source: s, mod: 0440} | later extends over", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      secrets: !override [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, mod: 0440}
`}, []string{"a.yaml"}, false},
		{"{source: s, mod: 0440} | anchor entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {source: s, mod: 0440}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - *e
`}, []string{"a.yaml"}, true},
		{"{source: s, mod: 0440} | merge key entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {source: s, mod: 0440}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - <<: *e
          target: t
`}, []string{"a.yaml"}, true},
		{"{source: s, mod: 0440} | whole-service alias", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-s: &s
  image: yi
  build:
    context: .
    secrets:
      - {source: s, mod: 0440}
services:
  y: *s
`}, []string{"a.yaml"}, true},
		{"{source: s, Mode: 0440} | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, Mode: 0440}
`}, []string{"a.yaml"}, true},
		{"{source: s, Mode: 0440} | taken", map[string]string{"a.yaml": `secrets:
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
        - {source: s, Mode: 0440}
`}, []string{"a.yaml"}, true},
		{"{source: s, Mode: 0440} | untaken", map[string]string{"a.yaml": `secrets:
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
        - {source: s, Mode: 0440}
`}, []string{"a.yaml"}, false},
		{"{source: s, Mode: 0440} | profile off", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    profiles: [p]
    build:
      context: .
      secrets:
        - {source: s, Mode: 0440}
`}, []string{"a.yaml"}, true},
		{"{source: s, Mode: 0440} | second -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      context: .
      secrets:
        - {source: s, Mode: 0440}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{source: s, Mode: 0440} | over-written by later -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, Mode: 0440}
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      secrets: !override [s]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{source: s, Mode: 0440} | include", map[string]string{"a.yaml": `include:
  - inc.yaml
services:
  z:
    image: zi
`, "inc.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, Mode: 0440}
`}, []string{"a.yaml"}, true},
		{"{source: s, Mode: 0440} | override tag", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets: !override
        - {source: s, Mode: 0440}
`}, []string{"a.yaml"}, true},
		{"{source: s, Mode: 0440} | later extends over", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      secrets: !override [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, Mode: 0440}
`}, []string{"a.yaml"}, false},
		{"{source: s, Mode: 0440} | anchor entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {source: s, Mode: 0440}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - *e
`}, []string{"a.yaml"}, true},
		{"{source: s, Mode: 0440} | merge key entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {source: s, Mode: 0440}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - <<: *e
          target: t
`}, []string{"a.yaml"}, true},
		{"{source: s, Mode: 0440} | whole-service alias", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-s: &s
  image: yi
  build:
    context: .
    secrets:
      - {source: s, Mode: 0440}
services:
  y: *s
`}, []string{"a.yaml"}, true},
		{"{source: s, x-ext: 1} | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, x-ext: 1}
`}, []string{"a.yaml"}, false},
		{"{source: s, x-ext: 1} | taken", map[string]string{"a.yaml": `secrets:
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
        - {source: s, x-ext: 1}
`}, []string{"a.yaml"}, false},
		{"{source: s, x-ext: 1} | untaken", map[string]string{"a.yaml": `secrets:
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
        - {source: s, x-ext: 1}
`}, []string{"a.yaml"}, false},
		{"{source: s, x-ext: 1} | profile off", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    profiles: [p]
    build:
      context: .
      secrets:
        - {source: s, x-ext: 1}
`}, []string{"a.yaml"}, false},
		{"{source: s, x-ext: 1} | second -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      context: .
      secrets:
        - {source: s, x-ext: 1}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"{source: s, x-ext: 1} | over-written by later -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, x-ext: 1}
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      secrets: !override [s]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"{source: s, x-ext: 1} | include", map[string]string{"a.yaml": `include:
  - inc.yaml
services:
  z:
    image: zi
`, "inc.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, x-ext: 1}
`}, []string{"a.yaml"}, false},
		{"{source: s, x-ext: 1} | override tag", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets: !override
        - {source: s, x-ext: 1}
`}, []string{"a.yaml"}, false},
		{"{source: s, x-ext: 1} | later extends over", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      secrets: !override [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, x-ext: 1}
`}, []string{"a.yaml"}, false},
		{"{source: s, x-ext: 1} | anchor entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {source: s, x-ext: 1}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - *e
`}, []string{"a.yaml"}, false},
		{"{source: s, x-ext: 1} | merge key entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {source: s, x-ext: 1}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - <<: *e
          target: t
`}, []string{"a.yaml"}, false},
		{"{source: s, x-ext: 1} | whole-service alias", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-s: &s
  image: yi
  build:
    context: .
    secrets:
      - {source: s, x-ext: 1}
services:
  y: *s
`}, []string{"a.yaml"}, false},
		{"{source: s, mode: 0440, target: t, uid: \"1\", gid: \"1\"} | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: 0440, target: t, uid: "1", gid: "1"}
`}, []string{"a.yaml"}, false},
		{"{source: s, mode: 0440, target: t, uid: \"1\", gid: \"1\"} | taken", map[string]string{"a.yaml": `secrets:
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
        - {source: s, mode: 0440, target: t, uid: "1", gid: "1"}
`}, []string{"a.yaml"}, false},
		{"{source: s, mode: 0440, target: t, uid: \"1\", gid: \"1\"} | untaken", map[string]string{"a.yaml": `secrets:
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
        - {source: s, mode: 0440, target: t, uid: "1", gid: "1"}
`}, []string{"a.yaml"}, false},
		{"{source: s, mode: 0440, target: t, uid: \"1\", gid: \"1\"} | profile off", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    profiles: [p]
    build:
      context: .
      secrets:
        - {source: s, mode: 0440, target: t, uid: "1", gid: "1"}
`}, []string{"a.yaml"}, false},
		{"{source: s, mode: 0440, target: t, uid: \"1\", gid: \"1\"} | second -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      context: .
      secrets:
        - {source: s, mode: 0440, target: t, uid: "1", gid: "1"}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"{source: s, mode: 0440, target: t, uid: \"1\", gid: \"1\"} | over-written by later -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: 0440, target: t, uid: "1", gid: "1"}
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      secrets: !override [s]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"{source: s, mode: 0440, target: t, uid: \"1\", gid: \"1\"} | include", map[string]string{"a.yaml": `include:
  - inc.yaml
services:
  z:
    image: zi
`, "inc.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, mode: 0440, target: t, uid: "1", gid: "1"}
`}, []string{"a.yaml"}, false},
		{"{source: s, mode: 0440, target: t, uid: \"1\", gid: \"1\"} | override tag", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets: !override
        - {source: s, mode: 0440, target: t, uid: "1", gid: "1"}
`}, []string{"a.yaml"}, false},
		{"{source: s, mode: 0440, target: t, uid: \"1\", gid: \"1\"} | later extends over", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      secrets: !override [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, mode: 0440, target: t, uid: "1", gid: "1"}
`}, []string{"a.yaml"}, false},
		{"{source: s, mode: 0440, target: t, uid: \"1\", gid: \"1\"} | anchor entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {source: s, mode: 0440, target: t, uid: "1", gid: "1"}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - *e
`}, []string{"a.yaml"}, false},
		{"{source: s, mode: 0440, target: t, uid: \"1\", gid: \"1\"} | merge key entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {source: s, mode: 0440, target: t, uid: "1", gid: "1"}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - <<: *e
          target: t
`}, []string{"a.yaml"}, false},
		{"{source: s, mode: 0440, target: t, uid: \"1\", gid: \"1\"} | whole-service alias", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-s: &s
  image: yi
  build:
    context: .
    secrets:
      - {source: s, mode: 0440, target: t, uid: "1", gid: "1"}
services:
  y: *s
`}, []string{"a.yaml"}, false},
		{"{source: s} | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s}
`}, []string{"a.yaml"}, false},
		{"{source: s} | taken", map[string]string{"a.yaml": `secrets:
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
        - {source: s}
`}, []string{"a.yaml"}, false},
		{"{source: s} | untaken", map[string]string{"a.yaml": `secrets:
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
        - {source: s}
`}, []string{"a.yaml"}, false},
		{"{source: s} | profile off", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    profiles: [p]
    build:
      context: .
      secrets:
        - {source: s}
`}, []string{"a.yaml"}, false},
		{"{source: s} | second -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      context: .
      secrets:
        - {source: s}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"{source: s} | over-written by later -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s}
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      secrets: !override [s]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"{source: s} | include", map[string]string{"a.yaml": `include:
  - inc.yaml
services:
  z:
    image: zi
`, "inc.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s}
`}, []string{"a.yaml"}, false},
		{"{source: s} | override tag", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets: !override
        - {source: s}
`}, []string{"a.yaml"}, false},
		{"{source: s} | later extends over", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      secrets: !override [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s}
`}, []string{"a.yaml"}, false},
		{"{source: s} | anchor entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {source: s}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - *e
`}, []string{"a.yaml"}, false},
		{"{source: s} | merge key entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {source: s}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - <<: *e
          target: t
`}, []string{"a.yaml"}, false},
		{"{source: s} | whole-service alias", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-s: &s
  image: yi
  build:
    context: .
    secrets:
      - {source: s}
services:
  y: *s
`}, []string{"a.yaml"}, false},
		{"{source: s, bogus: ~} | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, bogus: ~}
`}, []string{"a.yaml"}, true},
		{"{source: s, bogus: ~} | taken", map[string]string{"a.yaml": `secrets:
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
        - {source: s, bogus: ~}
`}, []string{"a.yaml"}, true},
		{"{source: s, bogus: ~} | untaken", map[string]string{"a.yaml": `secrets:
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
        - {source: s, bogus: ~}
`}, []string{"a.yaml"}, false},
		{"{source: s, bogus: ~} | profile off", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    profiles: [p]
    build:
      context: .
      secrets:
        - {source: s, bogus: ~}
`}, []string{"a.yaml"}, true},
		{"{source: s, bogus: ~} | second -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      context: .
      secrets:
        - {source: s, bogus: ~}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{source: s, bogus: ~} | over-written by later -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, bogus: ~}
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      secrets: !override [s]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{source: s, bogus: ~} | include", map[string]string{"a.yaml": `include:
  - inc.yaml
services:
  z:
    image: zi
`, "inc.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, bogus: ~}
`}, []string{"a.yaml"}, true},
		{"{source: s, bogus: ~} | override tag", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets: !override
        - {source: s, bogus: ~}
`}, []string{"a.yaml"}, true},
		{"{source: s, bogus: ~} | later extends over", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      secrets: !override [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, bogus: ~}
`}, []string{"a.yaml"}, false},
		{"{source: s, bogus: ~} | anchor entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {source: s, bogus: ~}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - *e
`}, []string{"a.yaml"}, true},
		{"{source: s, bogus: ~} | merge key entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {source: s, bogus: ~}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - <<: *e
          target: t
`}, []string{"a.yaml"}, true},
		{"{source: s, bogus: ~} | whole-service alias", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-s: &s
  image: yi
  build:
    context: .
    secrets:
      - {source: s, bogus: ~}
services:
  y: *s
`}, []string{"a.yaml"}, true},
		{"{bogus: 1} | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {bogus: 1}
`}, []string{"a.yaml"}, true},
		{"{bogus: 1} | taken", map[string]string{"a.yaml": `secrets:
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
        - {bogus: 1}
`}, []string{"a.yaml"}, true},
		{"{bogus: 1} | untaken", map[string]string{"a.yaml": `secrets:
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
        - {bogus: 1}
`}, []string{"a.yaml"}, false},
		{"{bogus: 1} | profile off", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    profiles: [p]
    build:
      context: .
      secrets:
        - {bogus: 1}
`}, []string{"a.yaml"}, true},
		{"{bogus: 1} | second -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      context: .
      secrets:
        - {bogus: 1}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{bogus: 1} | over-written by later -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {bogus: 1}
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      secrets: !override [s]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{bogus: 1} | include", map[string]string{"a.yaml": `include:
  - inc.yaml
services:
  z:
    image: zi
`, "inc.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {bogus: 1}
`}, []string{"a.yaml"}, true},
		{"{bogus: 1} | override tag", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets: !override
        - {bogus: 1}
`}, []string{"a.yaml"}, true},
		{"{bogus: 1} | later extends over", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      secrets: !override [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {bogus: 1}
`}, []string{"a.yaml"}, false},
		{"{bogus: 1} | anchor entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {bogus: 1}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - *e
`}, []string{"a.yaml"}, true},
		{"{bogus: 1} | merge key entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {bogus: 1}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - <<: *e
          target: t
`}, []string{"a.yaml"}, true},
		{"{bogus: 1} | whole-service alias", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-s: &s
  image: yi
  build:
    context: .
    secrets:
      - {bogus: 1}
services:
  y: *s
`}, []string{"a.yaml"}, true},
		{"{source: s, \"\": 1} | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, "": 1}
`}, []string{"a.yaml"}, true},
		{"{source: s, \"\": 1} | taken", map[string]string{"a.yaml": `secrets:
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
        - {source: s, "": 1}
`}, []string{"a.yaml"}, true},
		{"{source: s, \"\": 1} | untaken", map[string]string{"a.yaml": `secrets:
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
        - {source: s, "": 1}
`}, []string{"a.yaml"}, false},
		{"{source: s, \"\": 1} | profile off", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    profiles: [p]
    build:
      context: .
      secrets:
        - {source: s, "": 1}
`}, []string{"a.yaml"}, true},
		{"{source: s, \"\": 1} | second -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      context: .
      secrets:
        - {source: s, "": 1}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{source: s, \"\": 1} | over-written by later -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, "": 1}
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      secrets: !override [s]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{source: s, \"\": 1} | include", map[string]string{"a.yaml": `include:
  - inc.yaml
services:
  z:
    image: zi
`, "inc.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, "": 1}
`}, []string{"a.yaml"}, true},
		{"{source: s, \"\": 1} | override tag", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets: !override
        - {source: s, "": 1}
`}, []string{"a.yaml"}, true},
		{"{source: s, \"\": 1} | later extends over", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      secrets: !override [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, "": 1}
`}, []string{"a.yaml"}, false},
		{"{source: s, \"\": 1} | anchor entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {source: s, "": 1}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - *e
`}, []string{"a.yaml"}, true},
		{"{source: s, \"\": 1} | merge key entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {source: s, "": 1}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - <<: *e
          target: t
`}, []string{"a.yaml"}, true},
		{"{source: s, \"\": 1} | whole-service alias", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-s: &s
  image: yi
  build:
    context: .
    secrets:
      - {source: s, "": 1}
services:
  y: *s
`}, []string{"a.yaml"}, true},
		{"{source: s, 1: 2} | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, 1: 2}
`}, []string{"a.yaml"}, true},
		{"{source: s, 1: 2} | taken", map[string]string{"a.yaml": `secrets:
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
        - {source: s, 1: 2}
`}, []string{"a.yaml"}, true},
		{"{source: s, 1: 2} | untaken", map[string]string{"a.yaml": `secrets:
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
        - {source: s, 1: 2}
`}, []string{"a.yaml"}, true},
		{"{source: s, 1: 2} | profile off", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    profiles: [p]
    build:
      context: .
      secrets:
        - {source: s, 1: 2}
`}, []string{"a.yaml"}, true},
		{"{source: s, 1: 2} | second -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      context: .
      secrets:
        - {source: s, 1: 2}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{source: s, 1: 2} | over-written by later -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, 1: 2}
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      secrets: !override [s]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{source: s, 1: 2} | include", map[string]string{"a.yaml": `include:
  - inc.yaml
services:
  z:
    image: zi
`, "inc.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, 1: 2}
`}, []string{"a.yaml"}, true},
		{"{source: s, 1: 2} | override tag", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets: !override
        - {source: s, 1: 2}
`}, []string{"a.yaml"}, true},
		{"{source: s, 1: 2} | later extends over", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      secrets: !override [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, 1: 2}
`}, []string{"a.yaml"}, true},
		{"{source: s, 1: 2} | anchor entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {source: s, 1: 2}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - *e
`}, []string{"a.yaml"}, true},
		{"{source: s, 1: 2} | merge key entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {source: s, 1: 2}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - <<: *e
          target: t
`}, []string{"a.yaml"}, true},
		{"{source: s, 1: 2} | whole-service alias", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-s: &s
  image: yi
  build:
    context: .
    secrets:
      - {source: s, 1: 2}
services:
  y: *s
`}, []string{"a.yaml"}, true},
		{"s | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - s
`}, []string{"a.yaml"}, false},
		{"s | taken", map[string]string{"a.yaml": `secrets:
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
        - s
`}, []string{"a.yaml"}, false},
		{"s | untaken", map[string]string{"a.yaml": `secrets:
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
        - s
`}, []string{"a.yaml"}, false},
		{"s | profile off", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    profiles: [p]
    build:
      context: .
      secrets:
        - s
`}, []string{"a.yaml"}, false},
		{"s | second -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      context: .
      secrets:
        - s
`}, []string{"a.yaml", "b.yaml"}, false},
		{"s | over-written by later -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - s
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      secrets: !override [s]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"s | include", map[string]string{"a.yaml": `include:
  - inc.yaml
services:
  z:
    image: zi
`, "inc.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - s
`}, []string{"a.yaml"}, false},
		{"s | override tag", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets: !override
        - s
`}, []string{"a.yaml"}, false},
		{"s | later extends over", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      secrets: !override [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - s
`}, []string{"a.yaml"}, false},
		{"s | whole-service alias", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-s: &s
  image: yi
  build:
    context: .
    secrets:
      - s
services:
  y: *s
`}, []string{"a.yaml"}, false},
		{"{source: s, target: t, bogus: [1]} | single", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: t, bogus: [1]}
`}, []string{"a.yaml"}, true},
		{"{source: s, target: t, bogus: [1]} | taken", map[string]string{"a.yaml": `secrets:
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
        - {source: s, target: t, bogus: [1]}
`}, []string{"a.yaml"}, true},
		{"{source: s, target: t, bogus: [1]} | untaken", map[string]string{"a.yaml": `secrets:
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
        - {source: s, target: t, bogus: [1]}
`}, []string{"a.yaml"}, false},
		{"{source: s, target: t, bogus: [1]} | profile off", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    profiles: [p]
    build:
      context: .
      secrets:
        - {source: s, target: t, bogus: [1]}
`}, []string{"a.yaml"}, true},
		{"{source: s, target: t, bogus: [1]} | second -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      context: .
      secrets:
        - {source: s, target: t, bogus: [1]}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{source: s, target: t, bogus: [1]} | over-written by later -f", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: t, bogus: [1]}
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    build:
      secrets: !override [s]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{source: s, target: t, bogus: [1]} | include", map[string]string{"a.yaml": `include:
  - inc.yaml
services:
  z:
    image: zi
`, "inc.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, target: t, bogus: [1]}
`}, []string{"a.yaml"}, true},
		{"{source: s, target: t, bogus: [1]} | override tag", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets: !override
        - {source: s, target: t, bogus: [1]}
`}, []string{"a.yaml"}, true},
		{"{source: s, target: t, bogus: [1]} | later extends over", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: base.yaml, service: x}
    build:
      secrets: !override [s]
`, "base.yaml": `secrets:
  s:
    file: ./s.txt
services:
  x:
    image: xi
    build:
      context: .
      secrets:
        - {source: s, target: t, bogus: [1]}
`}, []string{"a.yaml"}, false},
		{"{source: s, target: t, bogus: [1]} | anchor entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {source: s, target: t, bogus: [1]}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - *e
`}, []string{"a.yaml"}, true},
		{"{source: s, target: t, bogus: [1]} | merge key entry", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-e: &e {source: s, target: t, bogus: [1]}
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - <<: *e
          target: t
`}, []string{"a.yaml"}, true},
		{"{source: s, target: t, bogus: [1]} | whole-service alias", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
x-s: &s
  image: yi
  build:
    context: .
    secrets:
      - {source: s, target: t, bogus: [1]}
services:
  y: *s
`}, []string{"a.yaml"}, true},
		{"several entries | second entry bogus", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
  t:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - s
        - {source: t, bogus: 1}
`}, []string{"a.yaml"}, true},
		{"several entries | first ok second ok", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
  t:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - s
        - {source: t, target: u}
`}, []string{"a.yaml"}, false},
		{"several entries | both bogus", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
  t:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, bogus: 1}
        - {source: t, bogus: 2}
`}, []string{"a.yaml"}, true},
		{"extends | chain: end bogus", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: b.yaml, service: y}
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    extends: {file: c.yaml, service: z}
`, "c.yaml": `secrets:
  s:
    file: ./s.txt
services:
  z:
    image: zi
    build:
      context: .
      secrets:
        - {source: s, bogus: 1}
`}, []string{"a.yaml"}, true},
		{"extends | chain: middle !override", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: b.yaml, service: y}
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    extends: {file: c.yaml, service: z}
    build:
      secrets: !override [s]
`, "c.yaml": `secrets:
  s:
    file: ./s.txt
services:
  z:
    image: zi
    build:
      context: .
      secrets:
        - {source: s, bogus: 1}
`}, []string{"a.yaml"}, false},
		{"extends | chain: top !override", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  web:
    extends: {file: b.yaml, service: y}
    build:
      secrets: !override [s]
`, "b.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    extends: {file: c.yaml, service: z}
`, "c.yaml": `secrets:
  s:
    file: ./s.txt
services:
  z:
    image: zi
    build:
      context: .
      secrets:
        - {source: s, bogus: 1}
`}, []string{"a.yaml"}, false},
		{"extends | same file extends", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, bogus: 1}
  web:
    extends: y
`}, []string{"a.yaml"}, true},
		{"extends | same file extends, extender !override", map[string]string{"a.yaml": `secrets:
  s:
    file: ./s.txt
services:
  y:
    image: yi
    build:
      context: .
      secrets:
        - {source: s, bogus: 1}
  web:
    extends: y
    build:
      secrets: !override [s]
`}, []string{"a.yaml"}, true},
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
