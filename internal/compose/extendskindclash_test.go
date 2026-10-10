package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// A service that an extends takes from another file cannot have its `healthcheck` written as a mapping over the extended service's list, nor its `extra_hosts` as a list or a mapping over
// the extended service's string: docker compose refuses the merge (`cannot override services.x.healthcheck`, `invalid additional host`), whatever else is written, in a hop through a file that
// is only extended from as well, and does not look at a service nothing takes. Every row is measured (docker compose v5.5.1, `config -q`, #1945). The rows opossum answers otherwise than docker compose, as before, are not in this table (a null `extra_hosts` over a null in the middle of a chain; a list over a string that
// is a host entry merges: `a:1.2.3.4`, `a=1.2.3.4`, `a:`, `a:b:c`, which only a string that is no entry (`abc`, `""`, `1.2.3.4`, `:1.2.3.4`) is refused for).
func TestAnExtenderCannotWriteAKeyAsAnotherKindThanTheServiceItExtends(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   map[string]string
		order   []string
		refused bool
	}{
		{"healthcheck: list over list | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [1]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over list | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [1]
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over list | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: [1]
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over list | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: [1]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over list | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: [1]
`, "b.yaml": `services:
  web:
    healthcheck: [1]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: list over listcmd | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [CMD, "true"]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over listcmd | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [CMD, "true"]
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over listcmd | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: [CMD, "true"]
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over listcmd | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: [CMD, "true"]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over listcmd | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: [1]
`, "b.yaml": `services:
  web:
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: list over str | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: abc
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over str | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: abc
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over str | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: abc
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over str | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: abc
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over str | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: [1]
`, "b.yaml": `services:
  web:
    healthcheck: abc
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: list over map | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {interval: 5s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over map | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {interval: 5s}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: [1]
`}, []string{"a.yaml"}, false},
		{"healthcheck: list over map | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: {interval: 5s}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over map | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: {interval: 5s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over map | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: [1]
`, "b.yaml": `services:
  web:
    healthcheck: {interval: 5s}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: list over map2 | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, "true"]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over map2 | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, "true"]}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: [1]
`}, []string{"a.yaml"}, false},
		{"healthcheck: list over map2 | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over map2 | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, "true"]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over map2 | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: [1]
`, "b.yaml": `services:
  web:
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: list over null | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: ~
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over null | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: ~
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over null | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: ~
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over null | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: ~
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, true},
		{"healthcheck: list over null | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: [1]
`, "b.yaml": `services:
  web:
    healthcheck: ~
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: list over reset | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, false},
		{"healthcheck: list over reset | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !reset null
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: [1]
`}, []string{"a.yaml"}, false},
		{"healthcheck: list over reset | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: !reset null
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, false},
		{"healthcheck: list over reset | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, false},
		{"healthcheck: list over reset | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: [1]
`, "b.yaml": `services:
  web:
    healthcheck: !reset null
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: list over override | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !override {interval: 7s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, false},
		{"healthcheck: list over override | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !override {interval: 7s}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: [1]
`}, []string{"a.yaml"}, false},
		{"healthcheck: list over override | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: !override {interval: 7s}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, false},
		{"healthcheck: list over override | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: !override {interval: 7s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [1]
`}, []string{"a.yaml"}, false},
		{"healthcheck: list over override | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: [1]
`, "b.yaml": `services:
  web:
    healthcheck: !override {interval: 7s}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: listcmd over list | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [1]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over list | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [1]
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over list | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: [1]
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over list | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: [1]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over list | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: [CMD, "true"]
`, "b.yaml": `services:
  web:
    healthcheck: [1]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: listcmd over listcmd | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [CMD, "true"]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over listcmd | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [CMD, "true"]
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over listcmd | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: [CMD, "true"]
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over listcmd | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: [CMD, "true"]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over listcmd | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: [CMD, "true"]
`, "b.yaml": `services:
  web:
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: listcmd over str | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: abc
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over str | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: abc
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over str | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: abc
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over str | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: abc
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over str | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: [CMD, "true"]
`, "b.yaml": `services:
  web:
    healthcheck: abc
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: listcmd over map | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {interval: 5s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over map | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {interval: 5s}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, false},
		{"healthcheck: listcmd over map | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: {interval: 5s}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over map | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: {interval: 5s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over map | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: [CMD, "true"]
`, "b.yaml": `services:
  web:
    healthcheck: {interval: 5s}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: listcmd over map2 | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, "true"]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over map2 | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, "true"]}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, false},
		{"healthcheck: listcmd over map2 | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over map2 | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, "true"]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over map2 | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: [CMD, "true"]
`, "b.yaml": `services:
  web:
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: listcmd over null | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: ~
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over null | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: ~
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over null | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: ~
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over null | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: ~
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, true},
		{"healthcheck: listcmd over null | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: [CMD, "true"]
`, "b.yaml": `services:
  web:
    healthcheck: ~
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: listcmd over reset | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, false},
		{"healthcheck: listcmd over reset | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !reset null
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, false},
		{"healthcheck: listcmd over reset | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: !reset null
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, false},
		{"healthcheck: listcmd over reset | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, false},
		{"healthcheck: listcmd over reset | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: [CMD, "true"]
`, "b.yaml": `services:
  web:
    healthcheck: !reset null
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: listcmd over override | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !override {interval: 7s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, false},
		{"healthcheck: listcmd over override | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !override {interval: 7s}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, false},
		{"healthcheck: listcmd over override | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: !override {interval: 7s}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, false},
		{"healthcheck: listcmd over override | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: !override {interval: 7s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml"}, false},
		{"healthcheck: listcmd over override | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: [CMD, "true"]
`, "b.yaml": `services:
  web:
    healthcheck: !override {interval: 7s}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: str over list | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [1]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, true},
		{"healthcheck: str over list | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [1]
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: abc
`}, []string{"a.yaml"}, true},
		{"healthcheck: str over list | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: [1]
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, true},
		{"healthcheck: str over list | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: [1]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, true},
		{"healthcheck: str over list | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: abc
`, "b.yaml": `services:
  web:
    healthcheck: [1]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: str over listcmd | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [CMD, "true"]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, true},
		{"healthcheck: str over listcmd | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [CMD, "true"]
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: abc
`}, []string{"a.yaml"}, true},
		{"healthcheck: str over listcmd | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: [CMD, "true"]
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, true},
		{"healthcheck: str over listcmd | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: [CMD, "true"]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, true},
		{"healthcheck: str over listcmd | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: abc
`, "b.yaml": `services:
  web:
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: str over str | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: abc
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, true},
		{"healthcheck: str over str | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: abc
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: abc
`}, []string{"a.yaml"}, true},
		{"healthcheck: str over str | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: abc
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, true},
		{"healthcheck: str over str | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: abc
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, true},
		{"healthcheck: str over str | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: abc
`, "b.yaml": `services:
  web:
    healthcheck: abc
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: str over map | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {interval: 5s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, false},
		{"healthcheck: str over map | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {interval: 5s}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: abc
`}, []string{"a.yaml"}, false},
		{"healthcheck: str over map | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: {interval: 5s}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, false},
		{"healthcheck: str over map | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: {interval: 5s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, false},
		{"healthcheck: str over map | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: abc
`, "b.yaml": `services:
  web:
    healthcheck: {interval: 5s}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: str over map2 | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, "true"]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, false},
		{"healthcheck: str over map2 | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, "true"]}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: abc
`}, []string{"a.yaml"}, false},
		{"healthcheck: str over map2 | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, false},
		{"healthcheck: str over map2 | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, "true"]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, false},
		{"healthcheck: str over map2 | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: abc
`, "b.yaml": `services:
  web:
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: str over null | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: ~
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, true},
		{"healthcheck: str over null | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: ~
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: abc
`}, []string{"a.yaml"}, true},
		{"healthcheck: str over null | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: ~
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, true},
		{"healthcheck: str over null | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: ~
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, true},
		{"healthcheck: str over null | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: abc
`, "b.yaml": `services:
  web:
    healthcheck: ~
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: str over reset | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, false},
		{"healthcheck: str over reset | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !reset null
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: abc
`}, []string{"a.yaml"}, false},
		{"healthcheck: str over reset | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: !reset null
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, false},
		{"healthcheck: str over reset | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, false},
		{"healthcheck: str over reset | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: abc
`, "b.yaml": `services:
  web:
    healthcheck: !reset null
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: str over override | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !override {interval: 7s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, false},
		{"healthcheck: str over override | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !override {interval: 7s}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: abc
`}, []string{"a.yaml"}, false},
		{"healthcheck: str over override | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: !override {interval: 7s}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, false},
		{"healthcheck: str over override | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: !override {interval: 7s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: abc
`}, []string{"a.yaml"}, false},
		{"healthcheck: str over override | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: abc
`, "b.yaml": `services:
  web:
    healthcheck: !override {interval: 7s}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: map over list | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [1]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map over list | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [1]
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map over list | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: [1]
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map over list | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: [1]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map over list | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {interval: 5s}
`, "b.yaml": `services:
  web:
    healthcheck: [1]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: map over listcmd | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [CMD, "true"]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map over listcmd | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [CMD, "true"]
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map over listcmd | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: [CMD, "true"]
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map over listcmd | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: [CMD, "true"]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map over listcmd | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {interval: 5s}
`, "b.yaml": `services:
  web:
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: map over str | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: abc
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map over str | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: abc
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map over str | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: abc
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map over str | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: abc
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map over str | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {interval: 5s}
`, "b.yaml": `services:
  web:
    healthcheck: abc
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: map over map | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {interval: 5s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map over map | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {interval: 5s}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map over map | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: {interval: 5s}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map over map | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: {interval: 5s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map over map | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {interval: 5s}
`, "b.yaml": `services:
  web:
    healthcheck: {interval: 5s}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"healthcheck: map over map2 | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, "true"]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map over map2 | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, "true"]}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map over map2 | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map over map2 | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, "true"]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map over map2 | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {interval: 5s}
`, "b.yaml": `services:
  web:
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"healthcheck: map over null | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: ~
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map over null | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: ~
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map over null | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: ~
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map over null | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: ~
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map over null | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {interval: 5s}
`, "b.yaml": `services:
  web:
    healthcheck: ~
`}, []string{"a.yaml", "b.yaml"}, false},
		{"healthcheck: map over reset | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map over reset | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !reset null
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map over reset | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: !reset null
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map over reset | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map over reset | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {interval: 5s}
`, "b.yaml": `services:
  web:
    healthcheck: !reset null
`}, []string{"a.yaml", "b.yaml"}, false},
		{"healthcheck: map over override | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !override {interval: 7s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map over override | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !override {interval: 7s}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map over override | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: !override {interval: 7s}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map over override | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: !override {interval: 7s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {interval: 5s}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map over override | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {interval: 5s}
`, "b.yaml": `services:
  web:
    healthcheck: !override {interval: 7s}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"healthcheck: map2 over list | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [1]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map2 over list | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [1]
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map2 over list | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: [1]
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map2 over list | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: [1]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map2 over list | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    healthcheck: [1]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: map2 over listcmd | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [CMD, "true"]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map2 over listcmd | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [CMD, "true"]
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map2 over listcmd | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: [CMD, "true"]
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map2 over listcmd | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: [CMD, "true"]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map2 over listcmd | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: map2 over str | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: abc
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map2 over str | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: abc
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map2 over str | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: abc
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map2 over str | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: abc
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map2 over str | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    healthcheck: abc
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: map2 over map | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {interval: 5s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map2 over map | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {interval: 5s}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map2 over map | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: {interval: 5s}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map2 over map | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: {interval: 5s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map2 over map | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    healthcheck: {interval: 5s}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"healthcheck: map2 over map2 | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, "true"]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map2 over map2 | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, "true"]}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map2 over map2 | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map2 over map2 | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, "true"]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map2 over map2 | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"healthcheck: map2 over null | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: ~
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map2 over null | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: ~
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"healthcheck: map2 over null | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: ~
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map2 over null | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: ~
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map2 over null | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    healthcheck: ~
`}, []string{"a.yaml", "b.yaml"}, false},
		{"healthcheck: map2 over reset | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map2 over reset | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !reset null
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map2 over reset | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: !reset null
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map2 over reset | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map2 over reset | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    healthcheck: !reset null
`}, []string{"a.yaml", "b.yaml"}, false},
		{"healthcheck: map2 over override | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !override {interval: 7s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map2 over override | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !override {interval: 7s}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map2 over override | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: !override {interval: 7s}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map2 over override | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: !override {interval: 7s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"healthcheck: map2 over override | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    healthcheck: !override {interval: 7s}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"healthcheck: none over list | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [1]
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"healthcheck: none over list | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [1]
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
`}, []string{"a.yaml"}, true},
		{"healthcheck: none over list | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: [1]
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"healthcheck: none over list | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: [1]
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"healthcheck: none over list | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: [1]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: none over listcmd | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [CMD, "true"]
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"healthcheck: none over listcmd | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [CMD, "true"]
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
`}, []string{"a.yaml"}, true},
		{"healthcheck: none over listcmd | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: [CMD, "true"]
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"healthcheck: none over listcmd | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: [CMD, "true"]
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"healthcheck: none over listcmd | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: none over str | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: abc
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"healthcheck: none over str | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: abc
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
`}, []string{"a.yaml"}, true},
		{"healthcheck: none over str | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: abc
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"healthcheck: none over str | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: abc
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"healthcheck: none over str | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: abc
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: none over map | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {interval: 5s}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"healthcheck: none over map | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {interval: 5s}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
`}, []string{"a.yaml"}, false},
		{"healthcheck: none over map | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: {interval: 5s}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"healthcheck: none over map | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: {interval: 5s}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"healthcheck: none over map | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {interval: 5s}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"healthcheck: none over map2 | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, "true"]}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"healthcheck: none over map2 | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, "true"]}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
`}, []string{"a.yaml"}, false},
		{"healthcheck: none over map2 | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"healthcheck: none over map2 | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, "true"]}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"healthcheck: none over map2 | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"healthcheck: none over null | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: ~
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"healthcheck: none over null | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: ~
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
`}, []string{"a.yaml"}, true},
		{"healthcheck: none over null | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: ~
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"healthcheck: none over null | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: ~
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"healthcheck: none over null | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: ~
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: none over reset | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !reset null
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"healthcheck: none over reset | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !reset null
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
`}, []string{"a.yaml"}, false},
		{"healthcheck: none over reset | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: !reset null
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"healthcheck: none over reset | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: !reset null
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"healthcheck: none over reset | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: !reset null
`}, []string{"a.yaml", "b.yaml"}, false},
		{"healthcheck: none over override | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !override {interval: 7s}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"healthcheck: none over override | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !override {interval: 7s}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
`}, []string{"a.yaml"}, false},
		{"healthcheck: none over override | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: !override {interval: 7s}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"healthcheck: none over override | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: !override {interval: 7s}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"healthcheck: none over override | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: !override {interval: 7s}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"healthcheck: null over list | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [1]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, true},
		{"healthcheck: null over list | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [1]
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: ~
`}, []string{"a.yaml"}, true},
		{"healthcheck: null over list | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: [1]
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, true},
		{"healthcheck: null over list | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: [1]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, true},
		{"healthcheck: null over list | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: ~
`, "b.yaml": `services:
  web:
    healthcheck: [1]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: null over listcmd | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [CMD, "true"]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, true},
		{"healthcheck: null over listcmd | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: [CMD, "true"]
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: ~
`}, []string{"a.yaml"}, true},
		{"healthcheck: null over listcmd | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: [CMD, "true"]
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, true},
		{"healthcheck: null over listcmd | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: [CMD, "true"]
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, true},
		{"healthcheck: null over listcmd | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: ~
`, "b.yaml": `services:
  web:
    healthcheck: [CMD, "true"]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: null over str | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: abc
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, true},
		{"healthcheck: null over str | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: abc
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: ~
`}, []string{"a.yaml"}, true},
		{"healthcheck: null over str | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: abc
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, true},
		{"healthcheck: null over str | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: abc
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, true},
		{"healthcheck: null over str | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: ~
`, "b.yaml": `services:
  web:
    healthcheck: abc
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: null over map | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {interval: 5s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, false},
		{"healthcheck: null over map | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {interval: 5s}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: ~
`}, []string{"a.yaml"}, false},
		{"healthcheck: null over map | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: {interval: 5s}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, false},
		{"healthcheck: null over map | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: {interval: 5s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, false},
		{"healthcheck: null over map | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: ~
`, "b.yaml": `services:
  web:
    healthcheck: {interval: 5s}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: null over map2 | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, "true"]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, false},
		{"healthcheck: null over map2 | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, "true"]}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: ~
`}, []string{"a.yaml"}, false},
		{"healthcheck: null over map2 | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, false},
		{"healthcheck: null over map2 | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, "true"]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, false},
		{"healthcheck: null over map2 | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: ~
`, "b.yaml": `services:
  web:
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: null over null | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: ~
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, true},
		{"healthcheck: null over null | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: ~
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: ~
`}, []string{"a.yaml"}, true},
		{"healthcheck: null over null | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: ~
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, true},
		{"healthcheck: null over null | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: ~
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, true},
		{"healthcheck: null over null | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: ~
`, "b.yaml": `services:
  web:
    healthcheck: ~
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: null over reset | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, false},
		{"healthcheck: null over reset | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !reset null
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: ~
`}, []string{"a.yaml"}, false},
		{"healthcheck: null over reset | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: !reset null
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, false},
		{"healthcheck: null over reset | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: !reset null
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, false},
		{"healthcheck: null over reset | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: ~
`, "b.yaml": `services:
  web:
    healthcheck: !reset null
`}, []string{"a.yaml", "b.yaml"}, true},
		{"healthcheck: null over override | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !override {interval: 7s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, false},
		{"healthcheck: null over override | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: !override {interval: 7s}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: ~
`}, []string{"a.yaml"}, false},
		{"healthcheck: null over override | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    healthcheck: !override {interval: 7s}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, false},
		{"healthcheck: null over override | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    healthcheck: !override {interval: 7s}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: ~
`}, []string{"a.yaml"}, false},
		{"healthcheck: null over override | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: ~
`, "b.yaml": `services:
  web:
    healthcheck: !override {interval: 7s}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extra_hosts: str over str | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: abc
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: abc
`}, []string{"a.yaml"}, true},
		{"extra_hosts: str over str | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: abc
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: abc
`}, []string{"a.yaml"}, true},
		{"extra_hosts: str over str | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: abc
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: abc
`}, []string{"a.yaml"}, true},
		{"extra_hosts: str over str | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: abc
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: abc
`}, []string{"a.yaml"}, true},
		{"extra_hosts: str over str | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: abc
`, "b.yaml": `services:
  web:
    extra_hosts: abc
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extra_hosts: str over list | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: abc
`}, []string{"a.yaml"}, true},
		{"extra_hosts: str over list | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: abc
`}, []string{"a.yaml"}, false},
		{"extra_hosts: str over list | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: ["a=1.2.3.4"]
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: abc
`}, []string{"a.yaml"}, true},
		{"extra_hosts: str over list | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: abc
`}, []string{"a.yaml"}, true},
		{"extra_hosts: str over list | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: abc
`, "b.yaml": `services:
  web:
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extra_hosts: str over map | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: abc
`}, []string{"a.yaml"}, true},
		{"extra_hosts: str over map | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: abc
`}, []string{"a.yaml"}, false},
		{"extra_hosts: str over map | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: {a: 1.2.3.4}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: abc
`}, []string{"a.yaml"}, true},
		{"extra_hosts: str over map | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: abc
`}, []string{"a.yaml"}, true},
		{"extra_hosts: str over map | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: abc
`, "b.yaml": `services:
  web:
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extra_hosts: str over null | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: abc
`}, []string{"a.yaml"}, true},
		{"extra_hosts: str over null | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: abc
`}, []string{"a.yaml"}, true},
		{"extra_hosts: str over null | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: ~
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: abc
`}, []string{"a.yaml"}, true},
		{"extra_hosts: str over null | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: abc
`}, []string{"a.yaml"}, true},
		{"extra_hosts: str over null | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: abc
`, "b.yaml": `services:
  web:
    extra_hosts: ~
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extra_hosts: str over reset | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: abc
`}, []string{"a.yaml"}, false},
		{"extra_hosts: str over reset | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: abc
`}, []string{"a.yaml"}, false},
		{"extra_hosts: str over reset | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: !reset []
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: abc
`}, []string{"a.yaml"}, false},
		{"extra_hosts: str over reset | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: abc
`}, []string{"a.yaml"}, false},
		{"extra_hosts: str over reset | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: abc
`, "b.yaml": `services:
  web:
    extra_hosts: !reset []
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extra_hosts: str over override | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: abc
`}, []string{"a.yaml"}, false},
		{"extra_hosts: str over override | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: abc
`}, []string{"a.yaml"}, false},
		{"extra_hosts: str over override | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: !override {b: 5.6.7.8}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: abc
`}, []string{"a.yaml"}, false},
		{"extra_hosts: str over override | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: abc
`}, []string{"a.yaml"}, false},
		{"extra_hosts: str over override | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: abc
`, "b.yaml": `services:
  web:
    extra_hosts: !override {b: 5.6.7.8}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extra_hosts: ok_colon over emptylist | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_colon over emptylist | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_colon over list | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_colon over list | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_colon over map | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_colon over map | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_colon over reset | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_colon over reset | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_colon over override | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_colon over override | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_eq over emptylist | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a=1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_eq over emptylist | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a=1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_eq over list | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a=1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_eq over list | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a=1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_eq over map | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a=1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_eq over map | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a=1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_eq over reset | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a=1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_eq over reset | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a=1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_eq over override | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a=1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_eq over override | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a=1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_trail over emptylist | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_trail over emptylist | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_trail over list | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_trail over list | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_trail over map | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_trail over map | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_trail over reset | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_trail over reset | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_trail over override | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_trail over override | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_multi over emptylist | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:b:c"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_multi over emptylist | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:b:c"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_multi over list | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:b:c"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_multi over list | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:b:c"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_multi over map | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:b:c"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_multi over map | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:b:c"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_multi over reset | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:b:c"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_multi over reset | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:b:c"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_multi over override | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:b:c"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: ok_multi over override | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "a:b:c"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: empty over emptylist | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ""
`}, []string{"a.yaml"}, true},
		{"extra_hosts: empty over emptylist | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ""
`}, []string{"a.yaml"}, true},
		{"extra_hosts: empty over list | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ""
`}, []string{"a.yaml"}, true},
		{"extra_hosts: empty over list | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ""
`}, []string{"a.yaml"}, true},
		{"extra_hosts: empty over map | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ""
`}, []string{"a.yaml"}, true},
		{"extra_hosts: empty over map | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ""
`}, []string{"a.yaml"}, true},
		{"extra_hosts: empty over null | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ""
`}, []string{"a.yaml"}, true},
		{"extra_hosts: empty over null | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ""
`}, []string{"a.yaml"}, true},
		{"extra_hosts: empty over reset | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ""
`}, []string{"a.yaml"}, false},
		{"extra_hosts: empty over reset | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ""
`}, []string{"a.yaml"}, false},
		{"extra_hosts: empty over override | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ""
`}, []string{"a.yaml"}, false},
		{"extra_hosts: empty over override | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ""
`}, []string{"a.yaml"}, false},
		{"extra_hosts: nocolon over emptylist | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "1.2.3.4"
`}, []string{"a.yaml"}, true},
		{"extra_hosts: nocolon over emptylist | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "1.2.3.4"
`}, []string{"a.yaml"}, true},
		{"extra_hosts: nocolon over list | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "1.2.3.4"
`}, []string{"a.yaml"}, true},
		{"extra_hosts: nocolon over list | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "1.2.3.4"
`}, []string{"a.yaml"}, true},
		{"extra_hosts: nocolon over map | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "1.2.3.4"
`}, []string{"a.yaml"}, true},
		{"extra_hosts: nocolon over map | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "1.2.3.4"
`}, []string{"a.yaml"}, true},
		{"extra_hosts: nocolon over null | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "1.2.3.4"
`}, []string{"a.yaml"}, true},
		{"extra_hosts: nocolon over null | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "1.2.3.4"
`}, []string{"a.yaml"}, true},
		{"extra_hosts: nocolon over reset | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: nocolon over reset | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: nocolon over override | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: nocolon over override | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: "1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: lead over emptylist | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ":1.2.3.4"
`}, []string{"a.yaml"}, true},
		{"extra_hosts: lead over emptylist | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ":1.2.3.4"
`}, []string{"a.yaml"}, true},
		{"extra_hosts: lead over list | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ":1.2.3.4"
`}, []string{"a.yaml"}, true},
		{"extra_hosts: lead over list | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ":1.2.3.4"
`}, []string{"a.yaml"}, true},
		{"extra_hosts: lead over map | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ":1.2.3.4"
`}, []string{"a.yaml"}, true},
		{"extra_hosts: lead over map | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ":1.2.3.4"
`}, []string{"a.yaml"}, true},
		{"extra_hosts: lead over null | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ":1.2.3.4"
`}, []string{"a.yaml"}, true},
		{"extra_hosts: lead over null | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ":1.2.3.4"
`}, []string{"a.yaml"}, true},
		{"extra_hosts: lead over reset | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ":1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: lead over reset | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ":1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: lead over override | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ":1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: lead over override | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ":1.2.3.4"
`}, []string{"a.yaml"}, false},
		{"extra_hosts: emptylist over emptylist | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: []
`}, []string{"a.yaml"}, false},
		{"extra_hosts: emptylist over emptylist | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: []
`}, []string{"a.yaml"}, false},
		{"extra_hosts: emptylist over list | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: []
`}, []string{"a.yaml"}, false},
		{"extra_hosts: emptylist over list | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: []
`}, []string{"a.yaml"}, false},
		{"extra_hosts: emptylist over map | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: []
`}, []string{"a.yaml"}, false},
		{"extra_hosts: emptylist over map | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: []
`}, []string{"a.yaml"}, false},
		{"extra_hosts: emptylist over null | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: []
`}, []string{"a.yaml"}, false},
		{"extra_hosts: emptylist over null | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: []
`}, []string{"a.yaml"}, false},
		{"extra_hosts: emptylist over reset | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: []
`}, []string{"a.yaml"}, false},
		{"extra_hosts: emptylist over reset | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: []
`}, []string{"a.yaml"}, false},
		{"extra_hosts: emptylist over override | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: []
`}, []string{"a.yaml"}, false},
		{"extra_hosts: emptylist over override | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: []
`}, []string{"a.yaml"}, false},
		{"extra_hosts: list over str | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: abc
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, true},
		{"extra_hosts: list over str | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: abc
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, true},
		{"extra_hosts: list over str | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: abc
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, true},
		{"extra_hosts: list over str | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: abc
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, true},
		{"extra_hosts: list over str | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: ["a=1.2.3.4"]
`, "b.yaml": `services:
  web:
    extra_hosts: abc
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extra_hosts: list over list | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, false},
		{"extra_hosts: list over list | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, false},
		{"extra_hosts: list over list | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: ["a=1.2.3.4"]
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, false},
		{"extra_hosts: list over list | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, false},
		{"extra_hosts: list over list | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: ["a=1.2.3.4"]
`, "b.yaml": `services:
  web:
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extra_hosts: list over map | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, false},
		{"extra_hosts: list over map | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, false},
		{"extra_hosts: list over map | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: {a: 1.2.3.4}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, false},
		{"extra_hosts: list over map | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, false},
		{"extra_hosts: list over map | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: ["a=1.2.3.4"]
`, "b.yaml": `services:
  web:
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extra_hosts: list over null | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, false},
		{"extra_hosts: list over null | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, true},
		{"extra_hosts: list over null | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: ~
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, false},
		{"extra_hosts: list over null | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, false},
		{"extra_hosts: list over null | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: ["a=1.2.3.4"]
`, "b.yaml": `services:
  web:
    extra_hosts: ~
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extra_hosts: list over reset | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, false},
		{"extra_hosts: list over reset | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, false},
		{"extra_hosts: list over reset | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: !reset []
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, false},
		{"extra_hosts: list over reset | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, false},
		{"extra_hosts: list over reset | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: ["a=1.2.3.4"]
`, "b.yaml": `services:
  web:
    extra_hosts: !reset []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extra_hosts: list over override | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, false},
		{"extra_hosts: list over override | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, false},
		{"extra_hosts: list over override | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: !override {b: 5.6.7.8}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, false},
		{"extra_hosts: list over override | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml"}, false},
		{"extra_hosts: list over override | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: ["a=1.2.3.4"]
`, "b.yaml": `services:
  web:
    extra_hosts: !override {b: 5.6.7.8}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extra_hosts: map over str | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: abc
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, true},
		{"extra_hosts: map over str | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: abc
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, true},
		{"extra_hosts: map over str | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: abc
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, true},
		{"extra_hosts: map over str | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: abc
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, true},
		{"extra_hosts: map over str | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: {a: 1.2.3.4}
`, "b.yaml": `services:
  web:
    extra_hosts: abc
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extra_hosts: map over list | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, false},
		{"extra_hosts: map over list | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, false},
		{"extra_hosts: map over list | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: ["a=1.2.3.4"]
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, false},
		{"extra_hosts: map over list | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, false},
		{"extra_hosts: map over list | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: {a: 1.2.3.4}
`, "b.yaml": `services:
  web:
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extra_hosts: map over map | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, false},
		{"extra_hosts: map over map | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, false},
		{"extra_hosts: map over map | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: {a: 1.2.3.4}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, false},
		{"extra_hosts: map over map | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, false},
		{"extra_hosts: map over map | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: {a: 1.2.3.4}
`, "b.yaml": `services:
  web:
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extra_hosts: map over null | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, false},
		{"extra_hosts: map over null | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, true},
		{"extra_hosts: map over null | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: ~
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, false},
		{"extra_hosts: map over null | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, false},
		{"extra_hosts: map over null | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: {a: 1.2.3.4}
`, "b.yaml": `services:
  web:
    extra_hosts: ~
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extra_hosts: map over reset | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, false},
		{"extra_hosts: map over reset | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, false},
		{"extra_hosts: map over reset | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: !reset []
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, false},
		{"extra_hosts: map over reset | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, false},
		{"extra_hosts: map over reset | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: {a: 1.2.3.4}
`, "b.yaml": `services:
  web:
    extra_hosts: !reset []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extra_hosts: map over override | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, false},
		{"extra_hosts: map over override | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, false},
		{"extra_hosts: map over override | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: !override {b: 5.6.7.8}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, false},
		{"extra_hosts: map over override | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml"}, false},
		{"extra_hosts: map over override | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: {a: 1.2.3.4}
`, "b.yaml": `services:
  web:
    extra_hosts: !override {b: 5.6.7.8}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extra_hosts: none over str | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: abc
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"extra_hosts: none over str | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: abc
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
`}, []string{"a.yaml"}, true},
		{"extra_hosts: none over str | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: abc
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"extra_hosts: none over str | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: abc
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"extra_hosts: none over str | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    extra_hosts: abc
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extra_hosts: none over list | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extra_hosts: none over list | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
`}, []string{"a.yaml"}, false},
		{"extra_hosts: none over list | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: ["a=1.2.3.4"]
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extra_hosts: none over list | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extra_hosts: none over list | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extra_hosts: none over map | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extra_hosts: none over map | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
`}, []string{"a.yaml"}, false},
		{"extra_hosts: none over map | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: {a: 1.2.3.4}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extra_hosts: none over map | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extra_hosts: none over map | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extra_hosts: none over null | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"extra_hosts: none over null | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
`}, []string{"a.yaml"}, true},
		{"extra_hosts: none over null | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: ~
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"extra_hosts: none over null | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, true},
		{"extra_hosts: none over null | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    extra_hosts: ~
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extra_hosts: none over reset | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extra_hosts: none over reset | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
`}, []string{"a.yaml"}, false},
		{"extra_hosts: none over reset | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: !reset []
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extra_hosts: none over reset | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extra_hosts: none over reset | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    extra_hosts: !reset []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extra_hosts: none over override | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extra_hosts: none over override | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
`}, []string{"a.yaml"}, false},
		{"extra_hosts: none over override | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: !override {b: 5.6.7.8}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extra_hosts: none over override | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
`}, []string{"a.yaml"}, false},
		{"extra_hosts: none over override | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    extra_hosts: !override {b: 5.6.7.8}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"extra_hosts: null over str | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: abc
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ~
`}, []string{"a.yaml"}, true},
		{"extra_hosts: null over str | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: abc
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: ~
`}, []string{"a.yaml"}, true},
		{"extra_hosts: null over str | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: abc
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ~
`}, []string{"a.yaml"}, true},
		{"extra_hosts: null over str | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: abc
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ~
`}, []string{"a.yaml"}, true},
		{"extra_hosts: null over str | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: ~
`, "b.yaml": `services:
  web:
    extra_hosts: abc
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extra_hosts: null over list | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ~
`}, []string{"a.yaml"}, false},
		{"extra_hosts: null over list | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: ~
`}, []string{"a.yaml"}, false},
		{"extra_hosts: null over list | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: ["a=1.2.3.4"]
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ~
`}, []string{"a.yaml"}, false},
		{"extra_hosts: null over list | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: ["a=1.2.3.4"]
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ~
`}, []string{"a.yaml"}, false},
		{"extra_hosts: null over list | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: ~
`, "b.yaml": `services:
  web:
    extra_hosts: ["a=1.2.3.4"]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extra_hosts: null over map | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ~
`}, []string{"a.yaml"}, false},
		{"extra_hosts: null over map | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: ~
`}, []string{"a.yaml"}, false},
		{"extra_hosts: null over map | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: {a: 1.2.3.4}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ~
`}, []string{"a.yaml"}, false},
		{"extra_hosts: null over map | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: {a: 1.2.3.4}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ~
`}, []string{"a.yaml"}, false},
		{"extra_hosts: null over map | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: ~
`, "b.yaml": `services:
  web:
    extra_hosts: {a: 1.2.3.4}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extra_hosts: null over null | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ~
`}, []string{"a.yaml"}, true},
		{"extra_hosts: null over null | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: ~
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: ~
`}, []string{"a.yaml"}, true},
		{"extra_hosts: null over null | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: ~
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ~
`}, []string{"a.yaml"}, true},
		{"extra_hosts: null over null | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: ~
`, "b.yaml": `services:
  web:
    extra_hosts: ~
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extra_hosts: null over reset | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ~
`}, []string{"a.yaml"}, false},
		{"extra_hosts: null over reset | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: ~
`}, []string{"a.yaml"}, false},
		{"extra_hosts: null over reset | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: !reset []
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ~
`}, []string{"a.yaml"}, false},
		{"extra_hosts: null over reset | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !reset []
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ~
`}, []string{"a.yaml"}, false},
		{"extra_hosts: null over reset | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: ~
`, "b.yaml": `services:
  web:
    extra_hosts: !reset []
`}, []string{"a.yaml", "b.yaml"}, true},
		{"extra_hosts: null over override | extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ~
`}, []string{"a.yaml"}, false},
		{"extra_hosts: null over override | extends untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    extra_hosts: ~
`}, []string{"a.yaml"}, false},
		{"extra_hosts: null over override | extends chain root", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
    extra_hosts: !override {b: 5.6.7.8}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ~
`}, []string{"a.yaml"}, false},
		{"extra_hosts: null over override | extends chain middle", map[string]string{"a.yaml": `services:
  web:
    extends: {file: b.yaml, service: m}
`, "b.yaml": `services:
  m:
    extends: {file: base.yaml, service: x}
    extra_hosts: !override {b: 5.6.7.8}
`, "base.yaml": `services:
  x:
    image: xi
    extra_hosts: ~
`}, []string{"a.yaml"}, false},
		{"extra_hosts: null over override | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
    extra_hosts: ~
`, "b.yaml": `services:
  web:
    extra_hosts: !override {b: 5.6.7.8}
`}, []string{"a.yaml", "b.yaml"}, true},
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
			if _, err := LoadFiles(paths, nil); (err != nil) != tc.refused {
				t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
			}
		})
	}
}
