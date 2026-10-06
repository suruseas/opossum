package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A value with nothing after it, or a `!reset` of an entry, that a later file writes over an earlier file's is read as docker compose
// reads it (`config`, every row measured, v5.5.1: `refused` is its rc 1; the control rows are the forms it reads). Of the mappings a
// `!reset` can leave empty only `extra_hosts` and `build.extra_hosts` are refused, naming the file that took the last entry out
// (`named`, as docker compose names it), where an earlier file wrote the mapping (an empty one too) and this one writes the key and
// does not write it with `!override`; after two files wrote the key, a `!reset` in a third reaches none of the names (#1785).
func TestANullOrAResetOfAnEntryInALaterFileIsReadAsDockerComposeReadsIt(t *testing.T) {
	type file struct{ name, body string }
	for _, tc := range []struct {
		name    string
		order   []string // the files given with `-f`, in order
		files   []file   // every file written, an extended one too
		refused bool
		named   string // the file the refusal of a mapping left empty names, "" for any other refusal
	}{
		{"extra_hosts: a later file writes it with nothing, no extends (control)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    extra_hosts: {h: }
`}}, true, ""},
		{"extra_hosts: a later file extends (same file) and writes it with nothing", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    extends: b
    extra_hosts: {h: }
  b:
    image: z
`}}, true, ""},
		{"extra_hosts: a later file extends (another file) and writes it with nothing", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    extends: {file: base.yaml, service: bs}
    extra_hosts: {h: }
`}, {"base.yaml", `services:
  bs:
    image: z
`}}, true, ""},
		{"extra_hosts (the whole key): a later file writes it with nothing, no extends (control)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    extra_hosts:
`}}, false, ""},
		{"extra_hosts (the whole key): a later file extends (same file) and writes it with nothing", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    extends: b
    extra_hosts:
  b:
    image: z
`}}, false, ""},
		{"extra_hosts (the whole key): a later file extends (another file) and writes it with nothing", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    extends: {file: base.yaml, service: bs}
    extra_hosts:
`}, {"base.yaml", `services:
  bs:
    image: z
`}}, false, ""},
		{"models: a later file writes it with nothing, no extends (control)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    models: [m]
  b:
    image: y
models:
  m: {model: ai/x}
`}, {"f2.yaml", `services:
  a:
    models:
`}}, true, ""},
		{"models: a later file extends (same file) and writes it with nothing", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    models: [m]
  b:
    image: y
models:
  m: {model: ai/x}
`}, {"f2.yaml", `services:
  a:
    extends: b
    models:
  b:
    image: z
`}}, true, ""},
		{"models: a later file extends (another file) and writes it with nothing", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    models: [m]
  b:
    image: y
models:
  m: {model: ai/x}
`}, {"f2.yaml", `services:
  a:
    extends: {file: base.yaml, service: bs}
    models:
`}, {"base.yaml", `services:
  bs:
    image: z
`}}, true, ""},
		{"depends_on: a later file writes it with nothing, no extends (control)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    depends_on: {b: {condition: service_started}}
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    depends_on:
`}}, true, ""},
		{"depends_on: a later file extends (same file) and writes it with nothing", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    depends_on: {b: {condition: service_started}}
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    extends: b
    depends_on:
  b:
    image: z
`}}, true, ""},
		{"depends_on: a later file extends (another file) and writes it with nothing", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    depends_on: {b: {condition: service_started}}
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    extends: {file: base.yaml, service: bs}
    depends_on:
`}, {"base.yaml", `services:
  bs:
    image: z
`}}, true, ""},
		{"ports: a later file writes it with nothing, no extends (control)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    ports: ['80:80']
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    ports:
`}}, false, ""},
		{"ports: a later file extends (same file) and writes it with nothing", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    ports: ['80:80']
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    extends: b
    ports:
  b:
    image: z
`}}, false, ""},
		{"ports: a later file extends (another file) and writes it with nothing", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    ports: ['80:80']
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    extends: {file: base.yaml, service: bs}
    ports:
`}, {"base.yaml", `services:
  bs:
    image: z
`}}, false, ""},
		{"command: a later file writes it with nothing, no extends (control)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    command: ['x']
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    command:
`}}, false, ""},
		{"command: a later file extends (same file) and writes it with nothing", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    command: ['x']
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    extends: b
    command:
  b:
    image: z
`}}, false, ""},
		{"command: a later file extends (another file) and writes it with nothing", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    command: ['x']
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    extends: {file: base.yaml, service: bs}
    command:
`}, {"base.yaml", `services:
  bs:
    image: z
`}}, false, ""},
		{"networks: a later file writes it with nothing, no extends (control)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    networks: [n]
  b:
    image: y
networks:
  n: {}
`}, {"f2.yaml", `services:
  a:
    networks:
`}}, true, ""},
		{"networks: a later file extends (same file) and writes it with nothing", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    networks: [n]
  b:
    image: y
networks:
  n: {}
`}, {"f2.yaml", `services:
  a:
    extends: b
    networks:
  b:
    image: z
`}}, true, ""},
		{"networks: a later file extends (another file) and writes it with nothing", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    networks: [n]
  b:
    image: y
networks:
  n: {}
`}, {"f2.yaml", `services:
  a:
    extends: {file: base.yaml, service: bs}
    networks:
`}, {"base.yaml", `services:
  bs:
    image: z
`}}, true, ""},
		{"extra_hosts: {<<: *c} with c: {h: } over an earlier value", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
`}, {"f2.yaml", `x-c: &c {h: }
services:
  a:
    extra_hosts: {<<: *c}
`}}, true, ""},
		{"service <<: *c with c: {models: } over an earlier value", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    models: [m]
models:
  m: {model: ai/x}
`}, {"f2.yaml", `x-c: &c {models: }
services:
  a:
    <<: *c
`}}, true, ""},
		{"service <<: *c with c: {extra_hosts: } over an earlier value", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
`}, {"f2.yaml", `x-c: &c {extra_hosts: }
services:
  a:
    <<: *c
`}}, false, ""},
		{"extra_hosts: {h: !reset} over an earlier {h: 1.2.3.4}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
`}, {"f2.yaml", `services:
  a:
    extra_hosts: {h: !reset}
`}}, true, ""},
		{"extra_hosts: {h: !reset null} over an earlier {h: 1.2.3.4} (control)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
`}, {"f2.yaml", `services:
  a:
    extra_hosts: {h: !reset null}
`}}, true, "f2.yaml"},
		{"environment: {E: !reset} over an earlier {E: 1}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {E: '1'}
`}, {"f2.yaml", `services:
  a:
    environment: {E: !reset}
`}}, true, ""},
		{"labels: {l: !reset} over an earlier {l: 1}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    labels: {l: '1'}
`}, {"f2.yaml", `services:
  a:
    labels: {l: !reset}
`}}, true, ""},
		{"ports: !reset over an earlier list", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    ports: ['80:80']
`}, {"f2.yaml", `services:
  a:
    ports: !reset
`}}, false, ""},
		{"extra_hosts: !reset null over an earlier value", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    extra_hosts: {h: !reset null}
`}}, true, "f2.yaml"},
		{"extra_hosts: !reset null in the only file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: !reset null}
  b:
    image: y
`}}, false, ""},
		{"extra_hosts (list earlier): !reset null over an earlier value", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: ['h=1.2.3.4']
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    extra_hosts: {h: !reset null}
`}}, false, ""},
		{"extra_hosts (list earlier): !reset null in the only file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: !reset null}
  b:
    image: y
`}}, false, ""},
		{"environment: !reset null over an earlier value", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {E: '1'}
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    environment: {E: !reset null}
`}}, false, ""},
		{"environment: !reset null in the only file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {E: !reset null}
  b:
    image: y
`}}, false, ""},
		{"labels: !reset null over an earlier value", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    labels: {l: '1'}
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    labels: {l: !reset null}
`}}, false, ""},
		{"labels: !reset null in the only file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    labels: {l: !reset null}
  b:
    image: y
`}}, false, ""},
		{"sysctls: !reset null over an earlier value", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    sysctls: {s: 1}
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    sysctls: {s: !reset null}
`}}, false, ""},
		{"sysctls: !reset null in the only file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    sysctls: {s: !reset null}
  b:
    image: y
`}}, false, ""},
		{"annotations: !reset null over an earlier value", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    annotations: {x: y}
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    annotations: {x: !reset null}
`}}, false, ""},
		{"annotations: !reset null in the only file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    annotations: {x: !reset null}
  b:
    image: y
`}}, false, ""},
		{"ulimits: !reset null over an earlier value", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    ulimits: {nofile: 1}
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    ulimits: {nofile: !reset null}
`}}, false, ""},
		{"ulimits: !reset null in the only file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    ulimits: {nofile: !reset null}
  b:
    image: y
`}}, false, ""},
		{"depends_on: !reset null over an earlier value", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    depends_on: {b: {condition: service_started}}
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    depends_on: {b: !reset null}
`}}, false, ""},
		{"depends_on: !reset null in the only file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    depends_on: {b: !reset null}
  b:
    image: y
`}}, false, ""},
		{"networks: !reset null over an earlier value", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    networks: {n: {}}
  b:
    image: y
networks:
  n: {}
`}, {"f2.yaml", `services:
  a:
    networks: {n: !reset null}
`}}, false, ""},
		{"networks: !reset null in the only file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    networks: {n: !reset null}
  b:
    image: y
networks:
  n: {}
`}}, false, ""},
		{"models: !reset null over an earlier value", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    models: {m: {}}
  b:
    image: y
models:
  m: {model: ai/x}
`}, {"f2.yaml", `services:
  a:
    models: {m: !reset null}
`}}, false, ""},
		{"models: !reset null in the only file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    models: {m: !reset null}
  b:
    image: y
models:
  m: {model: ai/x}
`}}, false, ""},
		{"build.args: !reset null over an earlier value", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., args: {A: '1'}}
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    build: {args: {A: !reset null}}
`}}, false, ""},
		{"build.args: !reset null in the only file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build: {args: {A: !reset null}}
  b:
    image: y
`}}, false, ""},
		{"build.extra_hosts: !reset null over an earlier value", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., extra_hosts: {h: 1.2.3.4}}
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    build: {extra_hosts: {h: !reset null}}
`}}, true, "f2.yaml"},
		{"build.extra_hosts: !reset null in the only file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build: {extra_hosts: {h: !reset null}}
  b:
    image: y
`}}, false, ""},
		{"deploy.labels: !reset null over an earlier value", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    deploy: {labels: {l: '1'}}
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    deploy: {labels: {l: !reset null}}
`}}, false, ""},
		{"deploy.labels: !reset null in the only file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    deploy: {labels: {l: !reset null}}
  b:
    image: y
`}}, false, ""},
		{"logging.options: !reset null over an earlier value", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    logging: {driver: json-file, options: {o: '1'}}
  b:
    image: y
`}, {"f2.yaml", `services:
  a:
    logging: {options: {o: !reset null}}
`}}, false, ""},
		{"logging.options: !reset null in the only file", []string{"f1.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    logging: {options: {o: !reset null}}
  b:
    image: y
`}}, false, ""},
		{"extra_hosts: earlier {h}, later resets h", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, {"f2.yaml", `services:
  a:
    extra_hosts:
      h: !reset null
`}}, true, "f2.yaml"},
		{"extra_hosts: earlier {h, k}, later resets h", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
      k: 1.2.3.5
`}, {"f2.yaml", `services:
  a:
    extra_hosts:
      h: !reset null
`}}, false, ""},
		{"extra_hosts: earlier {k}, later resets h (not there)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts:
      k: 1.2.3.5
`}, {"f2.yaml", `services:
  a:
    extra_hosts:
      h: !reset null
`}}, false, ""},
		{"extra_hosts: earlier {h}, later resets h and writes k", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, {"f2.yaml", `services:
  a:
    extra_hosts:
      h: !reset null
      k: 1.2.3.5
`}}, false, ""},
		{"extra_hosts: earlier {h}, later writes k, a third resets h", []string{"f1.yaml", "f2.yaml", "f3.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, {"f2.yaml", `services:
  a:
    extra_hosts:
      k: 1.2.3.5
`}, {"f3.yaml", `services:
  a:
    extra_hosts:
      h: !reset null
`}}, false, ""},
		{"extra_hosts: earlier {h}, later resets h, a third writes k", []string{"f1.yaml", "f2.yaml", "f3.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, {"f2.yaml", `services:
  a:
    extra_hosts:
      h: !reset null
`}, {"f3.yaml", `services:
  a:
    extra_hosts:
      k: 1.2.3.5
`}}, true, "f2.yaml"},
		{"extra_hosts: earlier {h}, later resets h, a third resets k", []string{"f1.yaml", "f2.yaml", "f3.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
      k: 1.2.3.5
`}, {"f2.yaml", `services:
  a:
    extra_hosts:
      h: !reset null
`}, {"f3.yaml", `services:
  a:
    extra_hosts:
      k: !reset null
`}}, false, ""},
		{"extra_hosts: earlier {h}, later resets the whole key (!reset)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, {"f2.yaml", `services:
  a:
    extra_hosts: !reset null
`}}, false, ""},
		{"extra_hosts: earlier {h}, later overrides with !override", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, {"f2.yaml", `services:
  a:
    extra_hosts: !override {k: 1.2.3.5}
`}}, false, ""},
		{"extra_hosts: earlier {h}, later {h: !override 1.2.3.9}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts:
      h: 1.2.3.4
`}, {"f2.yaml", `services:
  a:
    extra_hosts:
      h: !override 1.2.3.9
`}}, false, ""},
		{"extra_hosts: earlier {h} in a list, later {h: !reset null} (control)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: ['h=1.2.3.4']
`}, {"f2.yaml", `services:
  a:
    extra_hosts:
      h: !reset null
`}}, false, ""},
		{"build.extra_hosts: earlier {h}, later resets h", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts:
        h: !reset null
`}}, true, "f2.yaml"},
		{"build.extra_hosts: earlier {h, k}, later resets h", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
        k: 1.2.3.5
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts:
        h: !reset null
`}}, false, ""},
		{"build.extra_hosts: earlier {k}, later resets h (not there)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        k: 1.2.3.5
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts:
        h: !reset null
`}}, false, ""},
		{"build.extra_hosts: earlier {h}, later resets h and writes k", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts:
        h: !reset null
        k: 1.2.3.5
`}}, false, ""},
		{"build.extra_hosts: earlier {h}, later writes k, a third resets h", []string{"f1.yaml", "f2.yaml", "f3.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts:
        k: 1.2.3.5
`}, {"f3.yaml", `services:
  a:
    build:
      extra_hosts:
        h: !reset null
`}}, false, ""},
		{"build.extra_hosts: earlier {h}, later resets h, a third writes k", []string{"f1.yaml", "f2.yaml", "f3.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts:
        h: !reset null
`}, {"f3.yaml", `services:
  a:
    build:
      extra_hosts:
        k: 1.2.3.5
`}}, true, "f2.yaml"},
		{"build.extra_hosts: earlier {h}, later resets h, a third resets k", []string{"f1.yaml", "f2.yaml", "f3.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
        k: 1.2.3.5
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts:
        h: !reset null
`}, {"f3.yaml", `services:
  a:
    build:
      extra_hosts:
        k: !reset null
`}}, false, ""},
		{"build.extra_hosts: earlier {h}, later resets the whole key (!reset)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts: !reset null
`}}, false, ""},
		{"build.extra_hosts: earlier {h}, later overrides with !override", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts: !override {k: 1.2.3.5}
`}}, false, ""},
		{"build.extra_hosts: earlier {h}, later {h: !override 1.2.3.9}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts:
        h: 1.2.3.4
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts:
        h: !override 1.2.3.9
`}}, false, ""},
		{"build.extra_hosts: earlier {h} in a list, later {h: !reset null} (control)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., extra_hosts: ['h=1.2.3.4']}
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts:
        h: !reset null
`}}, false, ""},
		{"extra_hosts: earlier {}, later {}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {}
`}, {"f2.yaml", `services:
  a:
    extra_hosts: {}
`}}, true, "f2.yaml"},
		{"extra_hosts: earlier {}, later []", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {}
`}, {"f2.yaml", `services:
  a:
    extra_hosts: []
`}}, true, "f2.yaml"},
		{"extra_hosts: earlier {}, later resets h (not there)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {}
`}, {"f2.yaml", `services:
  a:
    extra_hosts: {h: !reset null}
`}}, true, "f2.yaml"},
		{"extra_hosts: earlier {}, later writes another key of the service", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {}
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}}, false, ""},
		{"extra_hosts: earlier {}, later {h}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {}
`}, {"f2.yaml", `services:
  a:
    extra_hosts: {h: 1.2.3.4}
`}}, false, ""},
		{"extra_hosts: earlier {}, later [k]", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {}
`}, {"f2.yaml", `services:
  a:
    extra_hosts: [k=1.2.3.5]
`}}, false, ""},
		{"extra_hosts: earlier {h}, later []", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
`}, {"f2.yaml", `services:
  a:
    extra_hosts: []
`}}, false, ""},
		{"extra_hosts: earlier [], later {}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: []
`}, {"f2.yaml", `services:
  a:
    extra_hosts: {}
`}}, false, ""},
		{"extra_hosts: earlier [h], later {}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: [h=1.2.3.4]
`}, {"f2.yaml", `services:
  a:
    extra_hosts: {}
`}}, false, ""},
		{"extra_hosts: three files, {} then another key then {}", []string{"f1.yaml", "f2.yaml", "f3.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {}
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}, {"f3.yaml", `services:
  a:
    extra_hosts: {}
`}}, true, "f3.yaml"},
		{"build.extra_hosts: earlier {}, later {}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: {}
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts: {}
`}}, true, "f2.yaml"},
		{"build.extra_hosts: earlier {}, later []", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: {}
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts: []
`}}, true, "f2.yaml"},
		{"build.extra_hosts: earlier {}, later resets h (not there)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: {}
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts: {h: !reset null}
`}}, true, "f2.yaml"},
		{"build.extra_hosts: earlier {}, later writes another key of the service", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: {}
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}}, false, ""},
		{"build.extra_hosts: earlier {}, later {h}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: {}
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts: {h: 1.2.3.4}
`}}, false, ""},
		{"build.extra_hosts: earlier {}, later [k]", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: {}
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts: [k=1.2.3.5]
`}}, false, ""},
		{"build.extra_hosts: earlier {h}, later []", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: {h: 1.2.3.4}
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts: []
`}}, false, ""},
		{"build.extra_hosts: earlier [], later {}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: []
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts: {}
`}}, false, ""},
		{"build.extra_hosts: earlier [h], later {}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: [h=1.2.3.4]
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts: {}
`}}, false, ""},
		{"build.extra_hosts: three files, {} then another key then {}", []string{"f1.yaml", "f2.yaml", "f3.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: {}
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}, {"f3.yaml", `services:
  a:
    build:
      extra_hosts: {}
`}}, true, "f3.yaml"},
		{"extra_hosts: !override {} over {h}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
`}, {"f2.yaml", `services:
  a:
    extra_hosts: !override {}
`}}, false, ""},
		{"extra_hosts: !override {} over {}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {}
`}, {"f2.yaml", `services:
  a:
    extra_hosts: !override {}
`}}, false, ""},
		{"extra_hosts: !override [] over {}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {}
`}, {"f2.yaml", `services:
  a:
    extra_hosts: !override []
`}}, false, ""},
		{"build: {extra_hosts: !override {}} over {h}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., extra_hosts: {h: 1.2.3.4}}
`}, {"f2.yaml", `services:
  a:
    build: {extra_hosts: !override {}}
`}}, false, ""},
		{"build: !override {context: ., extra_hosts: {}} over {h}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., extra_hosts: {h: 1.2.3.4}}
`}, {"f2.yaml", `services:
  a:
    build: !override {context: ., extra_hosts: {}}
`}}, false, ""},
		{"a: !override {image: x, extra_hosts: {}} over {h}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
`}, {"f2.yaml", `services:
  a: !override {image: x, extra_hosts: {}}
`}}, false, ""},
		{"a: <<: *c, c is !override {image: x, extra_hosts: {}}, over {h}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
`}, {"f2.yaml", `x-c: &c !override {image: x, extra_hosts: {}}
services:
  a:
    <<: *c
`}}, false, ""},
		{"extra_hosts: !reset {} over {h} (control)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
`}, {"f2.yaml", `services:
  a:
    extra_hosts: !reset {}
`}}, false, ""},
		{"two services: a keeps an entry, b is left empty", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4, k: 1.2.3.5}
  b:
    image: y
    extra_hosts: {h: 1.2.3.4}
`}, {"f2.yaml", `services:
  a:
    extra_hosts: {h: !reset null}
  b:
    extra_hosts: {h: !reset null}
`}}, true, "f2.yaml"},
		{"two services: a is left empty, b keeps an entry", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
  b:
    image: y
    extra_hosts: {h: 1.2.3.4, k: 1.2.3.5}
`}, {"f2.yaml", `services:
  a:
    extra_hosts: {h: !reset null}
  b:
    extra_hosts: {h: !reset null}
`}}, true, "f2.yaml"},
		{"one service: extra_hosts kept, build.extra_hosts left empty", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4, k: 1.2.3.5}
    build: {context: ., extra_hosts: {h: 1.2.3.4}}
`}, {"f2.yaml", `services:
  a:
    extra_hosts: {h: !reset null}
    build: {extra_hosts: {h: !reset null}}
`}}, true, "f2.yaml"},
		{"one service: extra_hosts left empty, build.extra_hosts kept", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
    build: {context: ., extra_hosts: {h: 1.2.3.4, k: 1.2.3.5}}
`}, {"f2.yaml", `services:
  a:
    extra_hosts: {h: !reset null}
    build: {extra_hosts: {h: !reset null}}
`}}, true, "f2.yaml"},
		{"two services: only b writes it in the later file, and is left empty", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
  b:
    image: y
    extra_hosts: {h: 1.2.3.4}
`}, {"f2.yaml", `services:
  b:
    extra_hosts: {h: !reset null}
`}}, true, "f2.yaml"},
		{"two services: only a writes it in the later file, and is left empty", []string{"f1.yaml", "f3.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
  b:
    image: y
    extra_hosts: {h: 1.2.3.4}
`}, {"f3.yaml", `services:
  a:
    extra_hosts: {h: !reset null}
`}}, true, "f3.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var paths []string
			for _, name := range tc.order {
				paths = append(paths, filepath.Join(dir, name))
			}
			_, strictErr := LoadFiles(paths, nil)
			if (strictErr != nil) != tc.refused {
				t.Errorf("the strict load: err %v, docker compose refuses it: %v", strictErr, tc.refused)
			}
			if tc.named != "" && strictErr != nil && !strings.Contains(strictErr.Error(), filepath.Join(dir, tc.named)+": services.") {
				t.Errorf("the refusal does not name %s as docker compose does: %v", tc.named, strictErr)
			}
			if tc.named != "" && strictErr != nil && !strings.Contains(strictErr.Error(), "extra_hosts must be a mapping") {
				t.Errorf("the refusal is not the one for a mapping left empty: %v", strictErr)
			}
			// A tag with no value (`{h: !reset}`) is not YAML that can be read at all, and every read stops on it.
			if strictErr != nil && strings.Contains(strictErr.Error(), "is not valid YAML") {
				return
			}
			p, err := LoadFilesEnvDirSoft(paths, nil, "")
			if err != nil {
				t.Fatalf("the soft load stopped: %v", err)
			}
			if fault := p.CheckValueFaults(); (fault != nil) != tc.refused {
				t.Errorf("the soft load kept the refusal: %v, docker compose refuses it: %v", fault, tc.refused)
			}
		})
	}
}
