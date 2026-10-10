package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A `healthcheck.test` written as a list must start with `NONE`, `CMD` or `CMD-SHELL` (in capitals), and a `start_interval` is a duration written as text: docker compose refuses `[a]`, `[cmd, x]`,
// `[a, b]` and a date, a list or a mapping for the interval of the service the files make, and reads a string, an empty list, the three words and a good duration; an extending service or a later
// file that writes a good test over a bad one makes the files fine, and a service nothing takes is not asked (v5.5.1, `config -q`, every row measured; #1991).
func TestAHealthcheckTestListAndStartIntervalAreHeldToTheirShape(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   map[string]string
		order   []string
		refused bool
	}{
		{"{test: [a]} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [a]}
`}, []string{"a.yaml"}, true},
		{"{test: [a]} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [a]}
`}, []string{"a.yaml"}, true},
		{"{test: [a]} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [a]}
`}, []string{"a.yaml"}, false},
		{"{test: [a]} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {test: [a]}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{test: [a]} | extender", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, ok]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [a]}
`}, []string{"a.yaml"}, false},
		{"{test: []} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: []}
`}, []string{"a.yaml"}, false},
		{"{test: []} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: []}
`}, []string{"a.yaml"}, false},
		{"{test: []} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: []}
`}, []string{"a.yaml"}, false},
		{"{test: []} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {test: []}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"{test: []} | extender", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, ok]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: []}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD]} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [CMD]}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD]} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD]}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD]} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [CMD]}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD]} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {test: [CMD]}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"{test: [CMD]} | extender", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, ok]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD]}
`}, []string{"a.yaml"}, false},
		{"{test: [NONE]} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [NONE]}
`}, []string{"a.yaml"}, false},
		{"{test: [NONE]} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [NONE]}
`}, []string{"a.yaml"}, false},
		{"{test: [NONE]} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [NONE]}
`}, []string{"a.yaml"}, false},
		{"{test: [NONE]} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {test: [NONE]}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"{test: [NONE]} | extender", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, ok]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [NONE]}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD-SHELL, x]} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [CMD-SHELL, x]}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD-SHELL, x]} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD-SHELL, x]}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD-SHELL, x]} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [CMD-SHELL, x]}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD-SHELL, x]} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {test: [CMD-SHELL, x]}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"{test: [CMD-SHELL, x]} | extender", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, ok]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD-SHELL, x]}
`}, []string{"a.yaml"}, false},
		{"{test: [cmd, x]} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [cmd, x]}
`}, []string{"a.yaml"}, true},
		{"{test: [cmd, x]} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [cmd, x]}
`}, []string{"a.yaml"}, true},
		{"{test: [cmd, x]} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [cmd, x]}
`}, []string{"a.yaml"}, false},
		{"{test: [cmd, x]} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {test: [cmd, x]}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{test: [cmd, x]} | extender", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, ok]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [cmd, x]}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD, 1]} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [CMD, 1]}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD, 1]} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, 1]}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD, 1]} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [CMD, 1]}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD, 1]} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {test: [CMD, 1]}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{test: [CMD, 1]} | extender", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, ok]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD, 1]}
`}, []string{"a.yaml"}, false},
		{"{test: [[a]]} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [[a]]}
`}, []string{"a.yaml"}, true},
		{"{test: [[a]]} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [[a]]}
`}, []string{"a.yaml"}, true},
		{"{test: [[a]]} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [[a]]}
`}, []string{"a.yaml"}, false},
		{"{test: [[a]]} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {test: [[a]]}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{test: [[a]]} | extender", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, ok]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [[a]]}
`}, []string{"a.yaml"}, false},
		{"{test: a} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: a}
`}, []string{"a.yaml"}, false},
		{"{test: a} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: a}
`}, []string{"a.yaml"}, false},
		{"{test: a} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: a}
`}, []string{"a.yaml"}, false},
		{"{test: a} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {test: a}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"{test: a} | extender", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, ok]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: a}
`}, []string{"a.yaml"}, false},
		{"{test: 5} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: 5}
`}, []string{"a.yaml"}, true},
		{"{test: 5} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: 5}
`}, []string{"a.yaml"}, true},
		{"{test: 5} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: 5}
`}, []string{"a.yaml"}, false},
		{"{test: 5} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {test: 5}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{test: 5} | extender", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, ok]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: 5}
`}, []string{"a.yaml"}, false},
		{"{test: [~]} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [~]}
`}, []string{"a.yaml"}, true},
		{"{test: [~]} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [~]}
`}, []string{"a.yaml"}, true},
		{"{test: [~]} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [~]}
`}, []string{"a.yaml"}, false},
		{"{test: [~]} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {test: [~]}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{test: [a, b]} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [a, b]}
`}, []string{"a.yaml"}, true},
		{"{test: [a, b]} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [a, b]}
`}, []string{"a.yaml"}, true},
		{"{test: [a, b]} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [a, b]}
`}, []string{"a.yaml"}, false},
		{"{test: [a, b]} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {test: [a, b]}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{test: [a, b]} | extender", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, ok]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [a, b]}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD], start_interval: 2024-01-01} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [CMD], start_interval: 2024-01-01}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD], start_interval: 2024-01-01} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD], start_interval: 2024-01-01}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD], start_interval: 2024-01-01} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [CMD], start_interval: 2024-01-01}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD], start_interval: 2024-01-01} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {test: [CMD], start_interval: 2024-01-01}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{test: [CMD], start_interval: 2024-01-01} | extender", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, ok]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD], start_interval: 2024-01-01}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD], start_interval: [1]} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [CMD], start_interval: [1]}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD], start_interval: [1]} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD], start_interval: [1]}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD], start_interval: [1]} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [CMD], start_interval: [1]}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD], start_interval: [1]} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {test: [CMD], start_interval: [1]}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{test: [CMD], start_interval: [1]} | extender", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, ok]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD], start_interval: [1]}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD], start_interval: {a: b}} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [CMD], start_interval: {a: b}}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD], start_interval: {a: b}} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD], start_interval: {a: b}}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD], start_interval: {a: b}} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [CMD], start_interval: {a: b}}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD], start_interval: {a: b}} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {test: [CMD], start_interval: {a: b}}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{test: [CMD], start_interval: {a: b}} | extender", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, ok]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD], start_interval: {a: b}}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD], start_interval: abc} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [CMD], start_interval: abc}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD], start_interval: abc} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD], start_interval: abc}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD], start_interval: abc} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [CMD], start_interval: abc}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD], start_interval: abc} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {test: [CMD], start_interval: abc}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{test: [CMD], start_interval: abc} | extender", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, ok]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD], start_interval: abc}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD], start_interval: 5} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [CMD], start_interval: 5}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD], start_interval: 5} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD], start_interval: 5}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD], start_interval: 5} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [CMD], start_interval: 5}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD], start_interval: 5} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {test: [CMD], start_interval: 5}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{test: [CMD], start_interval: 5} | extender", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, ok]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD], start_interval: 5}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD], start_interval: 5s} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [CMD], start_interval: 5s}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD], start_interval: 5s} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD], start_interval: 5s}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD], start_interval: 5s} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [CMD], start_interval: 5s}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD], start_interval: 5s} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {test: [CMD], start_interval: 5s}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"{test: [CMD], start_interval: 5s} | extender", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, ok]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD], start_interval: 5s}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD], start_interval: ~} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [CMD], start_interval: ~}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD], start_interval: ~} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD], start_interval: ~}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD], start_interval: ~} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [CMD], start_interval: ~}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD], start_interval: ~} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {test: [CMD], start_interval: ~}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{test: [CMD], start_interval: ~} | extender", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, ok]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD], start_interval: ~}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD], start_interval: true} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [CMD], start_interval: true}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD], start_interval: true} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD], start_interval: true}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD], start_interval: true} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    healthcheck: {test: [CMD], start_interval: true}
`}, []string{"a.yaml"}, false},
		{"{test: [CMD], start_interval: true} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    healthcheck: {test: [CMD], start_interval: true}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{test: [CMD], start_interval: true} | extender", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    healthcheck: {test: [CMD, ok]}
`, "base.yaml": `services:
  x:
    image: xi
    healthcheck: {test: [CMD], start_interval: true}
`}, []string{"a.yaml"}, true},
		{"{test: [a, b, c]} | long list", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [a, b, c]}
`}, []string{"a.yaml"}, true},
		{"{test: [CMD, a, b, c]} | long list", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [CMD, a, b, c]}
`}, []string{"a.yaml"}, false},
		{"{test: [curl, -f, http://x, y]} | long list", map[string]string{"a.yaml": `services:
  web:
    image: wi
    healthcheck: {test: [curl, -f, http://x, y]}
`}, []string{"a.yaml"}, true},
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

// `config` writes a healthcheck's test the way a compose file spells it, so what it prints loads back (and docker compose reads it): a string and `CMD-SHELL` come back as `CMD-SHELL`,
// a `CMD` list as `CMD` (the argv was kept without the word, and a bare argv would now be refused for not starting with one of the three) (#1991).
func TestConfigWritesAHealthchecksTestWithItsDirective(t *testing.T) {
	for _, tc := range []struct {
		name, test, want, list string
	}{
		{"a string", `"curl -f http://x"`, "- CMD-SHELL\n", "- CMD-SHELL\n                - curl -f http://x\n"},
		{"CMD-SHELL", `[CMD-SHELL, "curl -f http://x"]`, "- CMD-SHELL\n", "- CMD-SHELL\n                - curl -f http://x\n"},
		{"CMD", `[CMD, curl, -f, "http://x"]`, "- CMD\n", "- CMD\n                - curl\n                - -f\n                - http://x\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(p, []byte("services:\n  web:\n    image: wi\n    healthcheck:\n      test: "+tc.test+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			project, err := LoadFiles([]string{p}, nil)
			if err != nil {
				t.Fatal(err)
			}
			rendered, err := RenderConfig(project)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(rendered, tc.want) {
				t.Errorf("the test is not written with its directive:\n%s", rendered)
			}
			if !strings.Contains(rendered, tc.list) {
				t.Errorf("the test is not written whole, want %q:\n%s", tc.list, rendered)
			}
			again := filepath.Join(dir, "again.yaml")
			if err := os.WriteFile(again, []byte(rendered), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFiles([]string{again}, nil); err != nil {
				t.Errorf("the rendered config does not load back: %v", err)
			}
		})
	}
}

// A test list docker compose refuses is asked of the service the profiles turn on, not of one behind a profile that is off: the file reads (measured, v5.5.1: `config -q` is 0 without
// the profile, 1 with `--profile` or COMPOSE_PROFILES), and the refusal is kept for the commands that start services to ask of the active ones (HealthcheckFault). One with no profile is refused when the file is read (#1991).
func TestAHealthcheckTestBehindAProfileIsKeptForTheActiveSet(t *testing.T) {
	for _, tc := range []struct {
		name     string
		profiles string
		refused  bool // the read
		kept     bool // HealthcheckFault of `p`
	}{
		{"a profile", "profiles: [dbg]", false, true},
		{"no profile", "", true, false},
		{"an empty profile list", "profiles: []", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(p, []byte("services:\n  web:\n    image: wi\n  p:\n    image: pi\n    "+tc.profiles+"\n    healthcheck: {test: [a]}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			project, err := LoadFiles([]string{p}, nil)
			if (err != nil) != tc.refused {
				t.Fatalf("refused = %v (%v), want %v", err != nil, err, tc.refused)
			}
			if err == nil && (project.HealthcheckFault("p") != nil) != tc.kept {
				t.Errorf("kept = %v, want %v", project.HealthcheckFault("p") != nil, tc.kept)
			}
			if err == nil && project.HealthcheckFault("web") != nil {
				t.Errorf("a service with no healthcheck is kept a fault: %v", project.HealthcheckFault("web"))
			}
		})
	}
}
