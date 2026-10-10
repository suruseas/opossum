package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A service whose `models` names a model that the project does not declare under the top-level `models` is refused, as docker compose refuses it (`service "web" refers to undefined
// model m`; v5.5.1, `config -q`, every row measured; #1963): the names are the items of a list or the keys of a mapping, the declared models are those of the files merged (the
// top-level `models` of a file an `extends` takes a service from does not come with it), a service nothing takes is not asked, and neither is one behind a profile (which is asked when
// its profile is on, not known here). A list that names a model twice is refused whatever the profiles, unless an earlier file or an include already gave the service a `models`: the lists are put together without repeats.
func TestAServiceThatNamesAModelTheProjectDoesNotDeclareIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   map[string]string
		order   []string
		refused bool
	}{
		{"none | [m] | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    models: [m]
`}, []string{"a.yaml"}, true},
		{"none | [m] | profile off", map[string]string{"a.yaml": `services:
  web:
    image: wi
    profiles: [p]
    models: [m]
`}, []string{"a.yaml"}, false},
		{"none | [m] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    models: [m]
`}, []string{"a.yaml"}, true},
		{"none | [m] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    models: [m]
`}, []string{"a.yaml"}, false},
		{"none | [m] | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: [m]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"none | [n] | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    models: [n]
`}, []string{"a.yaml"}, true},
		{"none | [n] | profile off", map[string]string{"a.yaml": `services:
  web:
    image: wi
    profiles: [p]
    models: [n]
`}, []string{"a.yaml"}, false},
		{"none | [n] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    models: [n]
`}, []string{"a.yaml"}, true},
		{"none | [n] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    models: [n]
`}, []string{"a.yaml"}, false},
		{"none | [n] | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: [n]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"none | [m, n] | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    models: [m, n]
`}, []string{"a.yaml"}, true},
		{"none | [m, n] | profile off", map[string]string{"a.yaml": `services:
  web:
    image: wi
    profiles: [p]
    models: [m, n]
`}, []string{"a.yaml"}, false},
		{"none | [m, n] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    models: [m, n]
`}, []string{"a.yaml"}, true},
		{"none | [m, n] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    models: [m, n]
`}, []string{"a.yaml"}, false},
		{"none | [m, n] | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: [m, n]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"none | {m: {}} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    models: {m: {}}
`}, []string{"a.yaml"}, true},
		{"none | {m: {}} | profile off", map[string]string{"a.yaml": `services:
  web:
    image: wi
    profiles: [p]
    models: {m: {}}
`}, []string{"a.yaml"}, false},
		{"none | {m: {}} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    models: {m: {}}
`}, []string{"a.yaml"}, true},
		{"none | {m: {}} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    models: {m: {}}
`}, []string{"a.yaml"}, false},
		{"none | {m: {}} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: {m: {}}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"none | {n: {}} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    models: {n: {}}
`}, []string{"a.yaml"}, true},
		{"none | {n: {}} | profile off", map[string]string{"a.yaml": `services:
  web:
    image: wi
    profiles: [p]
    models: {n: {}}
`}, []string{"a.yaml"}, false},
		{"none | {n: {}} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    models: {n: {}}
`}, []string{"a.yaml"}, true},
		{"none | {n: {}} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    models: {n: {}}
`}, []string{"a.yaml"}, false},
		{"none | {n: {}} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: {n: {}}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"none | {m: ~} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    models: {m: ~}
`}, []string{"a.yaml"}, true},
		{"none | {m: ~} | profile off", map[string]string{"a.yaml": `services:
  web:
    image: wi
    profiles: [p]
    models: {m: ~}
`}, []string{"a.yaml"}, false},
		{"none | {m: ~} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    models: {m: ~}
`}, []string{"a.yaml"}, true},
		{"none | {m: ~} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    models: {m: ~}
`}, []string{"a.yaml"}, false},
		{"none | {m: ~} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: {m: ~}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"none | {n: ~} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    models: {n: ~}
`}, []string{"a.yaml"}, true},
		{"none | {n: ~} | profile off", map[string]string{"a.yaml": `services:
  web:
    image: wi
    profiles: [p]
    models: {n: ~}
`}, []string{"a.yaml"}, false},
		{"none | {n: ~} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    models: {n: ~}
`}, []string{"a.yaml"}, true},
		{"none | {n: ~} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    models: {n: ~}
`}, []string{"a.yaml"}, false},
		{"none | {n: ~} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: {n: ~}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"none | {m: {endpoint_var: A}} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    models: {m: {endpoint_var: A}}
`}, []string{"a.yaml"}, true},
		{"none | {m: {endpoint_var: A}} | profile off", map[string]string{"a.yaml": `services:
  web:
    image: wi
    profiles: [p]
    models: {m: {endpoint_var: A}}
`}, []string{"a.yaml"}, false},
		{"none | {m: {endpoint_var: A}} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    models: {m: {endpoint_var: A}}
`}, []string{"a.yaml"}, true},
		{"none | {m: {endpoint_var: A}} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    models: {m: {endpoint_var: A}}
`}, []string{"a.yaml"}, false},
		{"none | {m: {endpoint_var: A}} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: {m: {endpoint_var: A}}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"none | [1] | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    models: [1]
`}, []string{"a.yaml"}, true},
		{"none | [1] | profile off", map[string]string{"a.yaml": `services:
  web:
    image: wi
    profiles: [p]
    models: [1]
`}, []string{"a.yaml"}, true},
		{"none | [1] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    models: [1]
`}, []string{"a.yaml"}, true},
		{"none | [1] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    models: [1]
`}, []string{"a.yaml"}, true},
		{"none | [1] | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: [1]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"none | [~] | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    models: [~]
`}, []string{"a.yaml"}, true},
		{"none | [~] | profile off", map[string]string{"a.yaml": `services:
  web:
    image: wi
    profiles: [p]
    models: [~]
`}, []string{"a.yaml"}, true},
		{"none | [~] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    models: [~]
`}, []string{"a.yaml"}, true},
		{"none | [~] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    models: [~]
`}, []string{"a.yaml"}, true},
		{"none | [~] | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: [~]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"none | [[m]] | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    models: [[m]]
`}, []string{"a.yaml"}, true},
		{"none | [[m]] | profile off", map[string]string{"a.yaml": `services:
  web:
    image: wi
    profiles: [p]
    models: [[m]]
`}, []string{"a.yaml"}, true},
		{"none | [[m]] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    models: [[m]]
`}, []string{"a.yaml"}, true},
		{"none | [[m]] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    models: [[m]]
`}, []string{"a.yaml"}, true},
		{"none | [[m]] | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: [[m]]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"none | m | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    models: m
`}, []string{"a.yaml"}, true},
		{"none | m | profile off", map[string]string{"a.yaml": `services:
  web:
    image: wi
    profiles: [p]
    models: m
`}, []string{"a.yaml"}, true},
		{"none | m | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    models: m
`}, []string{"a.yaml"}, true},
		{"none | m | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    models: m
`}, []string{"a.yaml"}, false},
		{"none | m | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: m
`}, []string{"a.yaml", "b.yaml"}, true},
		{"none | [m, m] | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    models: [m, m]
`}, []string{"a.yaml"}, true},
		{"none | [m, m] | profile off", map[string]string{"a.yaml": `services:
  web:
    image: wi
    profiles: [p]
    models: [m, m]
`}, []string{"a.yaml"}, true},
		{"none | [m, m] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    models: [m, m]
`}, []string{"a.yaml"}, true},
		{"none | [m, m] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    models: [m, m]
`}, []string{"a.yaml"}, false},
		{"none | [m, m] | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: [m, m]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"none | {} | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    models: {}
`}, []string{"a.yaml"}, false},
		{"none | {} | profile off", map[string]string{"a.yaml": `services:
  web:
    image: wi
    profiles: [p]
    models: {}
`}, []string{"a.yaml"}, false},
		{"none | {} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    models: {}
`}, []string{"a.yaml"}, false},
		{"none | {} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    models: {}
`}, []string{"a.yaml"}, false},
		{"none | {} | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: {}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"none | [] | single", map[string]string{"a.yaml": `services:
  web:
    image: wi
    models: []
`}, []string{"a.yaml"}, false},
		{"none | [] | profile off", map[string]string{"a.yaml": `services:
  web:
    image: wi
    profiles: [p]
    models: []
`}, []string{"a.yaml"}, false},
		{"none | [] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
    models: []
`}, []string{"a.yaml"}, false},
		{"none | [] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `services:
  x:
    image: xi
  y:
    image: yi
    models: []
`}, []string{"a.yaml"}, false},
		{"none | [] | second -f", map[string]string{"a.yaml": `services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"m defined | [m] | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    models: [m]
`}, []string{"a.yaml"}, false},
		{"m defined | [m] | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    profiles: [p]
    models: [m]
`}, []string{"a.yaml"}, false},
		{"m defined | [m] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
    models: [m]
`}, []string{"a.yaml"}, true},
		{"m defined | [m] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
  y:
    image: yi
    models: [m]
`}, []string{"a.yaml"}, false},
		{"m defined | [m] | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: [m]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"m defined | [n] | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    models: [n]
`}, []string{"a.yaml"}, true},
		{"m defined | [n] | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    profiles: [p]
    models: [n]
`}, []string{"a.yaml"}, false},
		{"m defined | [n] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
    models: [n]
`}, []string{"a.yaml"}, true},
		{"m defined | [n] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
  y:
    image: yi
    models: [n]
`}, []string{"a.yaml"}, false},
		{"m defined | [n] | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: [n]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"m defined | [m, n] | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    models: [m, n]
`}, []string{"a.yaml"}, true},
		{"m defined | [m, n] | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    profiles: [p]
    models: [m, n]
`}, []string{"a.yaml"}, false},
		{"m defined | [m, n] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
    models: [m, n]
`}, []string{"a.yaml"}, true},
		{"m defined | [m, n] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
  y:
    image: yi
    models: [m, n]
`}, []string{"a.yaml"}, false},
		{"m defined | [m, n] | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: [m, n]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"m defined | {m: {}} | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    models: {m: {}}
`}, []string{"a.yaml"}, false},
		{"m defined | {m: {}} | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    profiles: [p]
    models: {m: {}}
`}, []string{"a.yaml"}, false},
		{"m defined | {m: {}} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
    models: {m: {}}
`}, []string{"a.yaml"}, true},
		{"m defined | {m: {}} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
  y:
    image: yi
    models: {m: {}}
`}, []string{"a.yaml"}, false},
		{"m defined | {m: {}} | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: {m: {}}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"m defined | {n: {}} | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    models: {n: {}}
`}, []string{"a.yaml"}, true},
		{"m defined | {n: {}} | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    profiles: [p]
    models: {n: {}}
`}, []string{"a.yaml"}, false},
		{"m defined | {n: {}} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
    models: {n: {}}
`}, []string{"a.yaml"}, true},
		{"m defined | {n: {}} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
  y:
    image: yi
    models: {n: {}}
`}, []string{"a.yaml"}, false},
		{"m defined | {n: {}} | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: {n: {}}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"m defined | {m: ~} | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    models: {m: ~}
`}, []string{"a.yaml"}, false},
		{"m defined | {m: ~} | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    profiles: [p]
    models: {m: ~}
`}, []string{"a.yaml"}, false},
		{"m defined | {m: ~} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
    models: {m: ~}
`}, []string{"a.yaml"}, true},
		{"m defined | {m: ~} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
  y:
    image: yi
    models: {m: ~}
`}, []string{"a.yaml"}, false},
		{"m defined | {m: ~} | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: {m: ~}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"m defined | {n: ~} | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    models: {n: ~}
`}, []string{"a.yaml"}, true},
		{"m defined | {n: ~} | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    profiles: [p]
    models: {n: ~}
`}, []string{"a.yaml"}, false},
		{"m defined | {n: ~} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
    models: {n: ~}
`}, []string{"a.yaml"}, true},
		{"m defined | {n: ~} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
  y:
    image: yi
    models: {n: ~}
`}, []string{"a.yaml"}, false},
		{"m defined | {n: ~} | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: {n: ~}
`}, []string{"a.yaml", "b.yaml"}, true},
		{"m defined | {m: {endpoint_var: A}} | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    models: {m: {endpoint_var: A}}
`}, []string{"a.yaml"}, false},
		{"m defined | {m: {endpoint_var: A}} | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    profiles: [p]
    models: {m: {endpoint_var: A}}
`}, []string{"a.yaml"}, false},
		{"m defined | {m: {endpoint_var: A}} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
    models: {m: {endpoint_var: A}}
`}, []string{"a.yaml"}, true},
		{"m defined | {m: {endpoint_var: A}} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
  y:
    image: yi
    models: {m: {endpoint_var: A}}
`}, []string{"a.yaml"}, false},
		{"m defined | {m: {endpoint_var: A}} | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: {m: {endpoint_var: A}}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"m defined | [1] | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    models: [1]
`}, []string{"a.yaml"}, true},
		{"m defined | [1] | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    profiles: [p]
    models: [1]
`}, []string{"a.yaml"}, true},
		{"m defined | [1] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
    models: [1]
`}, []string{"a.yaml"}, true},
		{"m defined | [1] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
  y:
    image: yi
    models: [1]
`}, []string{"a.yaml"}, true},
		{"m defined | [1] | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: [1]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"m defined | [~] | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    models: [~]
`}, []string{"a.yaml"}, true},
		{"m defined | [~] | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    profiles: [p]
    models: [~]
`}, []string{"a.yaml"}, true},
		{"m defined | [~] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
    models: [~]
`}, []string{"a.yaml"}, true},
		{"m defined | [~] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
  y:
    image: yi
    models: [~]
`}, []string{"a.yaml"}, true},
		{"m defined | [~] | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: [~]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"m defined | [[m]] | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    models: [[m]]
`}, []string{"a.yaml"}, true},
		{"m defined | [[m]] | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    profiles: [p]
    models: [[m]]
`}, []string{"a.yaml"}, true},
		{"m defined | [[m]] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
    models: [[m]]
`}, []string{"a.yaml"}, true},
		{"m defined | [[m]] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
  y:
    image: yi
    models: [[m]]
`}, []string{"a.yaml"}, true},
		{"m defined | [[m]] | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: [[m]]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"m defined | m | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    models: m
`}, []string{"a.yaml"}, true},
		{"m defined | m | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    profiles: [p]
    models: m
`}, []string{"a.yaml"}, true},
		{"m defined | m | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
    models: m
`}, []string{"a.yaml"}, true},
		{"m defined | m | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
  y:
    image: yi
    models: m
`}, []string{"a.yaml"}, false},
		{"m defined | m | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: m
`}, []string{"a.yaml", "b.yaml"}, true},
		{"m defined | [m, m] | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    models: [m, m]
`}, []string{"a.yaml"}, true},
		{"m defined | [m, m] | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    profiles: [p]
    models: [m, m]
`}, []string{"a.yaml"}, true},
		{"m defined | [m, m] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
    models: [m, m]
`}, []string{"a.yaml"}, true},
		{"m defined | [m, m] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
  y:
    image: yi
    models: [m, m]
`}, []string{"a.yaml"}, false},
		{"m defined | [m, m] | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: [m, m]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"m defined | {} | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    models: {}
`}, []string{"a.yaml"}, false},
		{"m defined | {} | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    profiles: [p]
    models: {}
`}, []string{"a.yaml"}, false},
		{"m defined | {} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
    models: {}
`}, []string{"a.yaml"}, false},
		{"m defined | {} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
  y:
    image: yi
    models: {}
`}, []string{"a.yaml"}, false},
		{"m defined | {} | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: {}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"m defined | [] | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    models: []
`}, []string{"a.yaml"}, false},
		{"m defined | [] | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
    profiles: [p]
    models: []
`}, []string{"a.yaml"}, false},
		{"m defined | [] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
    models: []
`}, []string{"a.yaml"}, false},
		{"m defined | [] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
services:
  x:
    image: xi
  y:
    image: yi
    models: []
`}, []string{"a.yaml"}, false},
		{"m defined | [] | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"m and n defined | [m] | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    models: [m]
`}, []string{"a.yaml"}, false},
		{"m and n defined | [m] | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    profiles: [p]
    models: [m]
`}, []string{"a.yaml"}, false},
		{"m and n defined | [m] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
    models: [m]
`}, []string{"a.yaml"}, true},
		{"m and n defined | [m] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
  y:
    image: yi
    models: [m]
`}, []string{"a.yaml"}, false},
		{"m and n defined | [m] | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: [m]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"m and n defined | [n] | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    models: [n]
`}, []string{"a.yaml"}, false},
		{"m and n defined | [n] | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    profiles: [p]
    models: [n]
`}, []string{"a.yaml"}, false},
		{"m and n defined | [n] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
    models: [n]
`}, []string{"a.yaml"}, true},
		{"m and n defined | [n] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
  y:
    image: yi
    models: [n]
`}, []string{"a.yaml"}, false},
		{"m and n defined | [n] | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: [n]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"m and n defined | [m, n] | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    models: [m, n]
`}, []string{"a.yaml"}, false},
		{"m and n defined | [m, n] | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    profiles: [p]
    models: [m, n]
`}, []string{"a.yaml"}, false},
		{"m and n defined | [m, n] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
    models: [m, n]
`}, []string{"a.yaml"}, true},
		{"m and n defined | [m, n] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
  y:
    image: yi
    models: [m, n]
`}, []string{"a.yaml"}, false},
		{"m and n defined | [m, n] | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: [m, n]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"m and n defined | {m: {}} | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    models: {m: {}}
`}, []string{"a.yaml"}, false},
		{"m and n defined | {m: {}} | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    profiles: [p]
    models: {m: {}}
`}, []string{"a.yaml"}, false},
		{"m and n defined | {m: {}} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
    models: {m: {}}
`}, []string{"a.yaml"}, true},
		{"m and n defined | {m: {}} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
  y:
    image: yi
    models: {m: {}}
`}, []string{"a.yaml"}, false},
		{"m and n defined | {m: {}} | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: {m: {}}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"m and n defined | {n: {}} | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    models: {n: {}}
`}, []string{"a.yaml"}, false},
		{"m and n defined | {n: {}} | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    profiles: [p]
    models: {n: {}}
`}, []string{"a.yaml"}, false},
		{"m and n defined | {n: {}} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
    models: {n: {}}
`}, []string{"a.yaml"}, true},
		{"m and n defined | {n: {}} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
  y:
    image: yi
    models: {n: {}}
`}, []string{"a.yaml"}, false},
		{"m and n defined | {n: {}} | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: {n: {}}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"m and n defined | {m: ~} | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    models: {m: ~}
`}, []string{"a.yaml"}, false},
		{"m and n defined | {m: ~} | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    profiles: [p]
    models: {m: ~}
`}, []string{"a.yaml"}, false},
		{"m and n defined | {m: ~} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
    models: {m: ~}
`}, []string{"a.yaml"}, true},
		{"m and n defined | {m: ~} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
  y:
    image: yi
    models: {m: ~}
`}, []string{"a.yaml"}, false},
		{"m and n defined | {m: ~} | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: {m: ~}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"m and n defined | {n: ~} | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    models: {n: ~}
`}, []string{"a.yaml"}, false},
		{"m and n defined | {n: ~} | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    profiles: [p]
    models: {n: ~}
`}, []string{"a.yaml"}, false},
		{"m and n defined | {n: ~} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
    models: {n: ~}
`}, []string{"a.yaml"}, true},
		{"m and n defined | {n: ~} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
  y:
    image: yi
    models: {n: ~}
`}, []string{"a.yaml"}, false},
		{"m and n defined | {n: ~} | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: {n: ~}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"m and n defined | {m: {endpoint_var: A}} | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    models: {m: {endpoint_var: A}}
`}, []string{"a.yaml"}, false},
		{"m and n defined | {m: {endpoint_var: A}} | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    profiles: [p]
    models: {m: {endpoint_var: A}}
`}, []string{"a.yaml"}, false},
		{"m and n defined | {m: {endpoint_var: A}} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
    models: {m: {endpoint_var: A}}
`}, []string{"a.yaml"}, true},
		{"m and n defined | {m: {endpoint_var: A}} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
  y:
    image: yi
    models: {m: {endpoint_var: A}}
`}, []string{"a.yaml"}, false},
		{"m and n defined | {m: {endpoint_var: A}} | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: {m: {endpoint_var: A}}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"m and n defined | [1] | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    models: [1]
`}, []string{"a.yaml"}, true},
		{"m and n defined | [1] | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    profiles: [p]
    models: [1]
`}, []string{"a.yaml"}, true},
		{"m and n defined | [1] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
    models: [1]
`}, []string{"a.yaml"}, true},
		{"m and n defined | [1] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
  y:
    image: yi
    models: [1]
`}, []string{"a.yaml"}, true},
		{"m and n defined | [1] | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: [1]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"m and n defined | [~] | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    models: [~]
`}, []string{"a.yaml"}, true},
		{"m and n defined | [~] | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    profiles: [p]
    models: [~]
`}, []string{"a.yaml"}, true},
		{"m and n defined | [~] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
    models: [~]
`}, []string{"a.yaml"}, true},
		{"m and n defined | [~] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
  y:
    image: yi
    models: [~]
`}, []string{"a.yaml"}, true},
		{"m and n defined | [~] | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: [~]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"m and n defined | [[m]] | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    models: [[m]]
`}, []string{"a.yaml"}, true},
		{"m and n defined | [[m]] | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    profiles: [p]
    models: [[m]]
`}, []string{"a.yaml"}, true},
		{"m and n defined | [[m]] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
    models: [[m]]
`}, []string{"a.yaml"}, true},
		{"m and n defined | [[m]] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
  y:
    image: yi
    models: [[m]]
`}, []string{"a.yaml"}, true},
		{"m and n defined | [[m]] | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: [[m]]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"m and n defined | m | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    models: m
`}, []string{"a.yaml"}, true},
		{"m and n defined | m | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    profiles: [p]
    models: m
`}, []string{"a.yaml"}, true},
		{"m and n defined | m | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
    models: m
`}, []string{"a.yaml"}, true},
		{"m and n defined | m | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
  y:
    image: yi
    models: m
`}, []string{"a.yaml"}, false},
		{"m and n defined | m | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: m
`}, []string{"a.yaml", "b.yaml"}, true},
		{"m and n defined | [m, m] | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    models: [m, m]
`}, []string{"a.yaml"}, true},
		{"m and n defined | [m, m] | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    profiles: [p]
    models: [m, m]
`}, []string{"a.yaml"}, true},
		{"m and n defined | [m, m] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
    models: [m, m]
`}, []string{"a.yaml"}, true},
		{"m and n defined | [m, m] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
  y:
    image: yi
    models: [m, m]
`}, []string{"a.yaml"}, false},
		{"m and n defined | [m, m] | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: [m, m]
`}, []string{"a.yaml", "b.yaml"}, true},
		{"m and n defined | {} | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    models: {}
`}, []string{"a.yaml"}, false},
		{"m and n defined | {} | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    profiles: [p]
    models: {}
`}, []string{"a.yaml"}, false},
		{"m and n defined | {} | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
    models: {}
`}, []string{"a.yaml"}, false},
		{"m and n defined | {} | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
  y:
    image: yi
    models: {}
`}, []string{"a.yaml"}, false},
		{"m and n defined | {} | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: {}
`}, []string{"a.yaml", "b.yaml"}, false},
		{"m and n defined | [] | single", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    models: []
`}, []string{"a.yaml"}, false},
		{"m and n defined | [] | profile off", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
    profiles: [p]
    models: []
`}, []string{"a.yaml"}, false},
		{"m and n defined | [] | taken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
    models: []
`}, []string{"a.yaml"}, false},
		{"m and n defined | [] | untaken", map[string]string{"a.yaml": `services:
  web:
    extends: {file: base.yaml, service: x}
`, "base.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  x:
    image: xi
  y:
    image: yi
    models: []
`}, []string{"a.yaml"}, false},
		{"m and n defined | [] | second -f", map[string]string{"a.yaml": `models:
  m:
    model: ai/smollm2
  n:
    model: ai/other
services:
  web:
    image: wi
`, "b.yaml": `services:
  web:
    models: []
`}, []string{"a.yaml", "b.yaml"}, false},
		{"more | include declares m, the including service names it | -", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    image: wi
    models: [m]
`, "inc.yaml": `models:
  m:
    model: ai/a
services:
  other:
    image: oi
`}, []string{"a.yaml"}, false},
		{"more | include declares m, its own service names it | -", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    image: wi
`, "inc.yaml": `models:
  m:
    model: ai/a
services:
  other:
    image: oi
    models: [m]
`}, []string{"a.yaml"}, false},
		{"more | include service names m, including file does not declare | -", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    image: wi
`, "inc.yaml": `services:
  other:
    image: oi
    models: [m]
`}, []string{"a.yaml"}, true},
		{"more | second -f writes the same [m] again | -", map[string]string{"a.yaml": `models:
  m:
    model: ai/a
  n:
    model: ai/b
services:
  web:
    image: wi
    models: [m]
`, "b.yaml": `services:
  web:
    models: [m]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"more | second -f adds [n] after [m, n] | -", map[string]string{"a.yaml": `models:
  m:
    model: ai/a
  n:
    model: ai/b
services:
  web:
    image: wi
    models: [m, n]
`, "b.yaml": `services:
  web:
    models: [n]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"more | second -f writes [m, m] | -", map[string]string{"a.yaml": `models:
  m:
    model: ai/a
  n:
    model: ai/b
services:
  web:
    image: wi
    models: [m]
`, "b.yaml": `services:
  web:
    models: [m, m]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"more | extends both sides [m] | -", map[string]string{"a.yaml": `models:
  m:
    model: ai/a
  n:
    model: ai/b
services:
  web:
    extends: {file: base.yaml, service: x}
    models: [m]
`, "base.yaml": `models:
  m:
    model: ai/a
  n:
    model: ai/b
services:
  x:
    image: xi
    models: [m]
`}, []string{"a.yaml"}, false},
		{"more | same file extends both sides [m] | -", map[string]string{"a.yaml": `models:
  m:
    model: ai/a
  n:
    model: ai/b
services:
  x:
    image: xi
    models: [m]
  web:
    extends: x
    models: [m]
`}, []string{"a.yaml"}, false},
		{"more | include service and including override both [m] | -", map[string]string{"a.yaml": `include: [inc.yaml]
services:
  web:
    models: [m]
`, "inc.yaml": `models:
  m:
    model: ai/a
  n:
    model: ai/b
services:
  web:
    image: wi
    models: [m]
`}, []string{"a.yaml"}, false},
		{"more | profile service, second -f writes [m] again | -", map[string]string{"a.yaml": `models:
  m:
    model: ai/a
  n:
    model: ai/b
services:
  web:
    image: wi
    profiles: [p]
    models: [m]
`, "b.yaml": `services:
  web:
    models: [m]
`}, []string{"a.yaml", "b.yaml"}, false},
		{"more | far apart duplicate [m, n, m] | -", map[string]string{"a.yaml": `models:
  m:
    model: ai/a
  n:
    model: ai/b
services:
  web:
    image: wi
    models: [m, n, m]
`}, []string{"a.yaml"}, true},
		{"more | profiles: [] names an undefined model | -", map[string]string{"a.yaml": `services:
  web:
    image: wi
    profiles: []
    models: [m]
`}, []string{"a.yaml"}, true},
		{"more | profile service names an undefined model | -", map[string]string{"a.yaml": `services:
  web:
    image: wi
    profiles: [p]
    models: [m]
`}, []string{"a.yaml"}, false},
		{"more | second -f resets the top-level models | -", map[string]string{"a.yaml": `models:
  m:
    model: ai/a
  n:
    model: ai/b
services:
  web:
    image: wi
    models: [m]
`, "b.yaml": `models: !reset {}
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
			_, err := LoadFiles(paths, nil)
			if (err != nil) != tc.refused {
				t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
			}
			if err != nil && tc.refused && strings.Contains(tc.name, "none |") && !strings.Contains(err.Error(), "model") {
				t.Errorf("the refusal does not name the model: %v", err)
			}
		})
	}
}

// The commands that take a project down read the files softly: a project an earlier opossum started with a model it did not ask for must still come down, so the refusal is kept for what
// reports the faults (CheckValueFaults) and the read goes on.
func TestAnUndeclaredModelDoesNotStopTheReadThatTakesAProjectDown(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(p, []byte("services:\n  web:\n    image: wi\n    models: [m]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFiles([]string{p}, nil); err == nil {
		t.Fatal("the strict read took a project docker compose refuses")
	}
	project, err := LoadFilesEnvDirSoft([]string{p}, nil, "")
	if err != nil {
		t.Fatalf("the soft read stopped: %v", err)
	}
	if fault := project.CheckValueFaults(); fault == nil || !strings.Contains(fault.Error(), "undefined model") {
		t.Errorf("the soft read kept no refusal naming the model: %v", fault)
	}
}
