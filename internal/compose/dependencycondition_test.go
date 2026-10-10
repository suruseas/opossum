package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The `condition` of a dependency is one of `service_started`, `service_healthy` and `service_completed_successfully`, and docker compose asks it of each file as it reads it
// (`condition value must be one of …`; v5.5.1, `config -q`, every row measured): a later file that writes a good word over a bad one, or resets the dependency, does not take the
// refusal away, in a file of its own, a second or third `-f`, an `include`; a file that is only extended from is asked of the service that results, so an extender that writes over
// is read (#1985).
func TestTheConditionOfADependencyIsAskedOfEachFile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   map[string]string
		order   []string
		refused bool
	}{
		{"bogus | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: bogus}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"bogus | later rewrites with a valid one", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: bogus}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"bogus | later resets", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: bogus}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: !reset null
`}, []string{"a.yaml", "b.yaml"}, true},
		{"bogus | later bad over valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: bogus}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"bogus | extender rewrites", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    depends_on:
      db: {condition: service_started}
  db:
    image: di
`, "base.yaml": `services:
  x:
    image: xi
    depends_on:
      db: {condition: bogus}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"bogus | include then the including file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    depends_on:
      db: {condition: service_started}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: bogus}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"bogus | include then a later file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_healthy}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: bogus}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"bogus | third file", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: bogus}
`, "c.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"bogus | untaken service in an extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    depends_on:
      x: {condition: bogus}
`}, []string{"a.yaml"}, false},
		{"service_started | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"service_started | later rewrites with a valid one", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"service_started | later resets", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: !reset null
`}, []string{"a.yaml", "b.yaml"}, false},
		{"service_started | later bad over valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"service_started | extender rewrites", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    depends_on:
      db: {condition: service_started}
  db:
    image: di
`, "base.yaml": `services:
  x:
    image: xi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"service_started | include then the including file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    depends_on:
      db: {condition: service_started}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"service_started | include then a later file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_healthy}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"service_started | third file", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`, "c.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"service_started | untaken service in an extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    depends_on:
      x: {condition: service_started}
`}, []string{"a.yaml"}, false},
		{"service_healthy | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_healthy}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"service_healthy | later rewrites with a valid one", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_healthy}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"service_healthy | later resets", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_healthy}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: !reset null
`}, []string{"a.yaml", "b.yaml"}, false},
		{"service_healthy | later bad over valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_healthy}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"service_healthy | extender rewrites", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    depends_on:
      db: {condition: service_started}
  db:
    image: di
`, "base.yaml": `services:
  x:
    image: xi
    depends_on:
      db: {condition: service_healthy}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"service_healthy | include then the including file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    depends_on:
      db: {condition: service_started}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_healthy}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"service_healthy | include then a later file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_healthy}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_healthy}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"service_healthy | third file", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_healthy}
`, "c.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"service_healthy | untaken service in an extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    depends_on:
      x: {condition: service_healthy}
`}, []string{"a.yaml"}, false},
		{"service_completed_successfully | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_completed_successfully}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"service_completed_successfully | later rewrites with a valid one", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_completed_successfully}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"service_completed_successfully | later resets", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_completed_successfully}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: !reset null
`}, []string{"a.yaml", "b.yaml"}, false},
		{"service_completed_successfully | later bad over valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_completed_successfully}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"service_completed_successfully | extender rewrites", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    depends_on:
      db: {condition: service_started}
  db:
    image: di
`, "base.yaml": `services:
  x:
    image: xi
    depends_on:
      db: {condition: service_completed_successfully}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"service_completed_successfully | include then the including file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    depends_on:
      db: {condition: service_started}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_completed_successfully}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"service_completed_successfully | include then a later file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_healthy}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_completed_successfully}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"service_completed_successfully | third file", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_completed_successfully}
`, "c.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"service_completed_successfully | untaken service in an extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    depends_on:
      x: {condition: service_completed_successfully}
`}, []string{"a.yaml"}, false},
		{"\"\" | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: ""}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"\"\" | later rewrites with a valid one", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: ""}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"\"\" | later resets", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: ""}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: !reset null
`}, []string{"a.yaml", "b.yaml"}, true},
		{"\"\" | later bad over valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: ""}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"\"\" | extender rewrites", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    depends_on:
      db: {condition: service_started}
  db:
    image: di
`, "base.yaml": `services:
  x:
    image: xi
    depends_on:
      db: {condition: ""}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"\"\" | include then the including file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    depends_on:
      db: {condition: service_started}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: ""}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"\"\" | include then a later file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_healthy}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: ""}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"\"\" | third file", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: ""}
`, "c.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"\"\" | untaken service in an extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    depends_on:
      x: {condition: ""}
`}, []string{"a.yaml"}, false},
		{"Service_Started | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: Service_Started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"Service_Started | later rewrites with a valid one", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: Service_Started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"Service_Started | later resets", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: Service_Started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: !reset null
`}, []string{"a.yaml", "b.yaml"}, true},
		{"Service_Started | later bad over valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: Service_Started}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"Service_Started | extender rewrites", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    depends_on:
      db: {condition: service_started}
  db:
    image: di
`, "base.yaml": `services:
  x:
    image: xi
    depends_on:
      db: {condition: Service_Started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"Service_Started | include then the including file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    depends_on:
      db: {condition: service_started}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: Service_Started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"Service_Started | include then a later file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_healthy}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: Service_Started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"Service_Started | third file", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: Service_Started}
`, "c.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"Service_Started | untaken service in an extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    depends_on:
      x: {condition: Service_Started}
`}, []string{"a.yaml"}, false},
		{"5 | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: 5}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"5 | later rewrites with a valid one", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: 5}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"5 | later resets", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: 5}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: !reset null
`}, []string{"a.yaml", "b.yaml"}, true},
		{"5 | later bad over valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: 5}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"5 | extender rewrites", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    depends_on:
      db: {condition: service_started}
  db:
    image: di
`, "base.yaml": `services:
  x:
    image: xi
    depends_on:
      db: {condition: 5}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"5 | include then the including file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    depends_on:
      db: {condition: service_started}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: 5}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"5 | include then a later file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_healthy}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: 5}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"5 | third file", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: 5}
`, "c.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"5 | untaken service in an extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    depends_on:
      x: {condition: 5}
`}, []string{"a.yaml"}, false},
		{"~ | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: ~}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"~ | later rewrites with a valid one", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: ~}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"~ | later resets", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: ~}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: !reset null
`}, []string{"a.yaml", "b.yaml"}, true},
		{"~ | later bad over valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: ~}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"~ | extender rewrites", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    depends_on:
      db: {condition: service_started}
  db:
    image: di
`, "base.yaml": `services:
  x:
    image: xi
    depends_on:
      db: {condition: ~}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"~ | include then the including file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    depends_on:
      db: {condition: service_started}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: ~}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"~ | include then a later file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_healthy}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: ~}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"~ | third file", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: ~}
`, "c.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"true | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: true}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"true | later rewrites with a valid one", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: true}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"true | later resets", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: true}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: !reset null
`}, []string{"a.yaml", "b.yaml"}, true},
		{"true | later bad over valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: true}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"true | extender rewrites", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    depends_on:
      db: {condition: service_started}
  db:
    image: di
`, "base.yaml": `services:
  x:
    image: xi
    depends_on:
      db: {condition: true}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"true | include then the including file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    depends_on:
      db: {condition: service_started}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: true}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"true | include then a later file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_healthy}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: true}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"true | third file", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: true}
`, "c.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"true | untaken service in an extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    depends_on:
      x: {condition: true}
`}, []string{"a.yaml"}, false},
		{"service_started  | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started }
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"service_started  | later rewrites with a valid one", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started }
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"service_started  | later resets", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started }
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: !reset null
`}, []string{"a.yaml", "b.yaml"}, false},
		{"service_started  | later bad over valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started }
`}, []string{"a.yaml", "b.yaml"}, false},
		{"service_started  | extender rewrites", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    depends_on:
      db: {condition: service_started}
  db:
    image: di
`, "base.yaml": `services:
  x:
    image: xi
    depends_on:
      db: {condition: service_started }
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"service_started  | include then the including file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    depends_on:
      db: {condition: service_started}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started }
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, false},
		{"service_started  | include then a later file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_healthy}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started }
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"service_started  | third file", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started }
`, "c.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"service_started  | untaken service in an extends file", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    depends_on:
      x: {condition: service_started }
`}, []string{"a.yaml"}, false},
		{"[a] | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: [a]}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"[a] | later rewrites with a valid one", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: [a]}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"[a] | later resets", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: [a]}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: !reset null
`}, []string{"a.yaml", "b.yaml"}, true},
		{"[a] | later bad over valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: [a]}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"[a] | extender rewrites", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    depends_on:
      db: {condition: service_started}
  db:
    image: di
`, "base.yaml": `services:
  x:
    image: xi
    depends_on:
      db: {condition: [a]}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"[a] | include then the including file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    depends_on:
      db: {condition: service_started}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: [a]}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"[a] | include then a later file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_healthy}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: [a]}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"[a] | third file", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: [a]}
`, "c.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"{a: b} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: {a: b}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"{a: b} | later rewrites with a valid one", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: {a: b}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{a: b} | later resets", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: {a: b}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: !reset null
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{a: b} | later bad over valid", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: {a: b}}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{a: b} | extender rewrites", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
    depends_on:
      db: {condition: service_started}
  db:
    image: di
`, "base.yaml": `services:
  x:
    image: xi
    depends_on:
      db: {condition: {a: b}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"{a: b} | include then the including file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    depends_on:
      db: {condition: service_started}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: {a: b}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml"}, true},
		{"{a: b} | include then a later file rewrites", map[string]string{"a.yaml": `include: [inc.yaml]
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: service_healthy}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: {a: b}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"{a: b} | third file", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on:
      db: {condition: service_started}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {condition: {a: b}}
`, "c.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started}
`}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
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
			if err != nil && tc.refused && strings.Contains(tc.name, "later rewrites") && !strings.Contains(err.Error(), "a.yaml") {
				t.Errorf("the refusal does not name the file that wrote the word: %v", err)
			}
		})
	}
}

// The commands that take a project down read the files softly: a project an earlier opossum started on a condition it did not ask must still come down, so the refusal is kept
// for what reports the faults (CheckValueFaults) and the read goes on, naming the file that wrote the word.
func TestABadConditionDoesNotStopTheReadThatTakesAProjectDown(t *testing.T) {
	dir := t.TempDir()
	a := "services:\n  web:\n    image: wi\n    depends_on:\n      db: {condition: bogus}\n  db:\n    image: di\n"
	b := "services:\n  web:\n    depends_on:\n      db: {condition: service_started}\n"
	for name, text := range map[string]string{"a.yaml": a, "b.yaml": b} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	paths := []string{filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")}
	if _, err := LoadFiles(paths, nil); err == nil {
		t.Fatal("the strict read took a project docker compose refuses")
	}
	project, err := LoadFilesEnvDirSoft(paths, nil, "")
	if err != nil {
		t.Fatalf("the soft read stopped: %v", err)
	}
	fault := project.CheckValueFaults()
	if fault == nil || !strings.Contains(fault.Error(), "a.yaml") {
		t.Errorf("the soft read kept no refusal naming the file that wrote the word: %v", fault)
	}
}

// A dependency after the first is asked as well, and the service that depends is found wherever its name sorts.
func TestTheConditionOfALaterDependencyIsAskedToo(t *testing.T) {
	dir := t.TempDir()
	a := "services:\n  zz:\n    image: zi\n    depends_on:\n      aa: {condition: service_started}\n      mm: {condition: bogus}\n  aa:\n    image: ai\n  mm:\n    image: mi\n"
	b := "services:\n  zz:\n    depends_on:\n      mm: {condition: service_started}\n"
	for name, text := range map[string]string{"a.yaml": a, "b.yaml": b} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadFiles([]string{filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")}, nil); err == nil || !strings.Contains(err.Error(), `"mm"`) {
		t.Errorf("want the refusal of the second dependency, got %v", err)
	}
}
