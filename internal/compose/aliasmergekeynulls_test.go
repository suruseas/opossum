package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// A key with nothing after it that comes by an alias or by a merge key — a whole block written as an alias (`logging: *l`), a whole
// service (`a: *z`), the services of a merge key directly under `services:` (`<<: *s`), a merge key inside a block, or a key name written as an alias — over an earlier file's value is read as one
// written there, as docker compose reads it (`config -q`, every row measured, v5.5.1: `refused` is its rc 1; the control rows are the
// keys it reads over a value). The rows were the cases of the issue this guards (#1787): they all agree since the merge key and
// alias readers (#1788, #1790), and these keep them agreeing.
func TestANullThatComesByAnAliasOrAMergeKeyOverAnEarlierValueIsReadAsDockerComposeReadsIt(t *testing.T) {
	type file struct{ name, body string }
	for _, tc := range []struct {
		name    string
		files   []file
		refused bool
	}{
		{"logging: a mapping written with an alias, with nothing in it, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    logging: {driver: json-file, options: {a: b}}
`}, {"f2.yaml", `x-l: &l {driver: }
services:
  a:
    logging: *l
`}}, true},
		{"logging.options: a mapping written with an alias, with nothing in it, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    logging: {driver: json-file, options: {a: b}}
`}, {"f2.yaml", `x-l: &l {a: }
services:
  a:
    logging:
      options: *l
`}}, false},
		{"extra_hosts: a mapping written with an alias, with nothing in it, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
`}, {"f2.yaml", `x-l: &l {h: }
services:
  a:
    extra_hosts: *l
`}}, true},
		{"environment: a mapping written with an alias, with nothing in it, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {E: '1'}
`}, {"f2.yaml", `x-l: &l {E: }
services:
  a:
    environment: *l
`}}, false},
		{"labels: a mapping written with an alias, with nothing in it, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    labels: {l: '1'}
`}, {"f2.yaml", `x-l: &l {l: }
services:
  a:
    labels: *l
`}}, false},
		{"healthcheck: a mapping written with an alias, with nothing in it, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], interval: 5s}
`}, {"f2.yaml", `x-l: &l {test: }
services:
  a:
    healthcheck: *l
`}}, true},
		{"deploy: a mapping written with an alias, with nothing in it, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    deploy: {replicas: 2}
`}, {"f2.yaml", `x-l: &l {replicas: }
services:
  a:
    deploy: *l
`}}, false},
		{"build: a mapping written with an alias, with nothing in it, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., args: {A: '1'}}
`}, {"f2.yaml", `x-l: &l {dockerfile: }
services:
  a:
    build: *l
`}}, true},
		{"ulimits: a mapping written with an alias, with nothing in it, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    ulimits: {nofile: 1}
`}, {"f2.yaml", `x-l: &l {nofile: }
services:
  a:
    ulimits: *l
`}}, true},
		{"networks: a mapping written with an alias, with nothing in it, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    networks: [n]
networks:
  default: {}
  n: {}
`}, {"f2.yaml", `x-l: &l 
services:
  a:
    networks: *l
networks:
  default: {}
  n: {}
`}}, true},
		{"networks: a service written as an alias, with nothing in it, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    networks: [n]
  b:
    image: y
networks:
  default: {}
  n: {}
`}, {"f2.yaml", `x-z: &z {image: x, networks: }
services:
  a: *z
networks:
  default: {}
  n: {}
`}}, true},
		{"depends_on: a service written as an alias, with nothing in it, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    depends_on: {b: {condition: service_started}}
  b:
    image: y
`}, {"f2.yaml", `x-z: &z {image: x, depends_on: }
services:
  a: *z
`}}, true},
		{"models: a service written as an alias, with nothing in it, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    models: [m]
  b:
    image: y
models:
  m: {model: ai/x}
`}, {"f2.yaml", `x-z: &z {image: x, models: }
services:
  a: *z
models:
  m: {model: ai/x}
`}}, true},
		{"logging: a service written as an alias, with nothing in it, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    logging: {driver: json-file}
  b:
    image: y
`}, {"f2.yaml", `x-z: &z {image: x, logging: }
services:
  a: *z
`}}, true},
		{"extra_hosts: a service written as an alias, with nothing in it, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
  b:
    image: y
`}, {"f2.yaml", `x-z: &z {image: x, extra_hosts: {h: }}
services:
  a: *z
`}}, true},
		{"command (control: read): a service written as an alias, with nothing in it, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    command: [x]
  b:
    image: y
`}, {"f2.yaml", `x-z: &z {image: x, command: }
services:
  a: *z
`}}, false},
		{"networks: a service with nothing in it brought by a merge key directly under services, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    networks: [n]
  b:
    image: y
networks:
  default: {}
  n: {}
`}, {"f2.yaml", `x-s: &s {a: {networks: }}
services:
  <<: *s
networks:
  default: {}
  n: {}
`}}, true},
		{"depends_on: a service with nothing in it brought by a merge key directly under services, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    depends_on: {b: {condition: service_started}}
  b:
    image: y
`}, {"f2.yaml", `x-s: &s {a: {depends_on: }}
services:
  <<: *s
`}}, true},
		{"models: a service with nothing in it brought by a merge key directly under services, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    models: [m]
  b:
    image: y
models:
  m: {model: ai/x}
`}, {"f2.yaml", `x-s: &s {a: {models: }}
services:
  <<: *s
models:
  m: {model: ai/x}
`}}, true},
		{"logging: a service with nothing in it brought by a merge key directly under services, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    logging: {driver: json-file}
  b:
    image: y
`}, {"f2.yaml", `x-s: &s {a: {logging: }}
services:
  <<: *s
`}}, true},
		{"extra_hosts: a service with nothing in it brought by a merge key directly under services, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
  b:
    image: y
`}, {"f2.yaml", `x-s: &s {a: {extra_hosts: {h: }}}
services:
  <<: *s
`}}, true},
		{"command (control: read): a service with nothing in it brought by a merge key directly under services, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    command: [x]
  b:
    image: y
`}, {"f2.yaml", `x-s: &s {a: {command: }}
services:
  <<: *s
`}}, false},
		{"logging: a merge key inside the block brings a null over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    logging: {driver: json-file}
`}, {"f2.yaml", `x-l: &l {driver: }
services:
  a:
    logging:
      <<: *l
`}}, true},
		{"logging: a key written in the block wins over the merge key's null", []file{{"f1.yaml", `services:
  a:
    image: x
    logging: {driver: json-file}
`}, {"f2.yaml", `x-l: &l {driver: }
services:
  a:
    logging:
      <<: *l
      driver: syslog
`}}, false},
		{"extra_hosts: a merge key inside the block brings a null over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
`}, {"f2.yaml", `x-l: &l {h: }
services:
  a:
    extra_hosts:
      <<: *l
`}}, true},
		{"extra_hosts: a key written in the block wins over the merge key's null", []file{{"f1.yaml", `services:
  a:
    image: x
    extra_hosts: {h: 1.2.3.4}
`}, {"f2.yaml", `x-l: &l {h: }
services:
  a:
    extra_hosts:
      <<: *l
      h: 1.2.3.9
`}}, false},
		{"environment: a merge key inside the block brings a null over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {E: '1'}
`}, {"f2.yaml", `x-l: &l {E: }
services:
  a:
    environment:
      <<: *l
`}}, false},
		{"environment: a key written in the block wins over the merge key's null", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {E: '1'}
`}, {"f2.yaml", `x-l: &l {E: }
services:
  a:
    environment:
      <<: *l
      E: '2'
`}}, false},
		{"healthcheck: a merge key inside the block brings a null over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], interval: 5s}
`}, {"f2.yaml", `x-l: &l {test: }
services:
  a:
    healthcheck:
      <<: *l
`}}, true},
		{"healthcheck: a key written in the block wins over the merge key's null", []file{{"f1.yaml", `services:
  a:
    image: x
    healthcheck: {test: [CMD, 'true'], interval: 5s}
`}, {"f2.yaml", `x-l: &l {test: }
services:
  a:
    healthcheck:
      <<: *l
      test: [CMD, 'false']
`}}, false},
		{"build: a merge key inside the block brings a null over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., args: {A: '1'}}
`}, {"f2.yaml", `x-l: &l {dockerfile: }
services:
  a:
    build:
      <<: *l
`}}, true},
		{"build: a key written in the block wins over the merge key's null", []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., args: {A: '1'}}
`}, {"f2.yaml", `x-l: &l {dockerfile: }
services:
  a:
    build:
      <<: *l
      dockerfile: D
`}}, false},
		{"deploy: a merge key inside the block brings a null over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    deploy: {replicas: 2}
`}, {"f2.yaml", `x-l: &l {replicas: }
services:
  a:
    deploy:
      <<: *l
`}}, false},
		{"deploy: a key written in the block wins over the merge key's null", []file{{"f1.yaml", `services:
  a:
    image: x
    deploy: {replicas: 2}
`}, {"f2.yaml", `x-l: &l {replicas: }
services:
  a:
    deploy:
      <<: *l
      replicas: 3
`}}, false},
		{"a key of the service by an alias, with nothing after it, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    command: [x]
`}, {"f2.yaml", `x-k: &k command
services:
  a:
    *k :
`}}, false},
		{"networks as a key name by an alias, with nothing after it, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    networks: [n]
networks:
  n: {}
`}, {"f2.yaml", `x-k: &k networks
networks:
  n: {}
services:
  a:
    *k :
`}}, true},
		{"logging as a key name by an alias, with nothing after it, over an earlier value", []file{{"f1.yaml", `services:
  a:
    image: x
    logging: {driver: json-file}
`}, {"f2.yaml", `x-k: &k logging
services:
  a:
    *k :
`}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var paths []string
			for _, f := range tc.files {
				p := filepath.Join(dir, f.name)
				if err := os.WriteFile(p, []byte(f.body), 0o644); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, p)
			}
			_, strictErr := LoadFiles(paths, nil)
			if (strictErr != nil) != tc.refused {
				t.Errorf("the strict load: err %v, docker compose refuses it: %v", strictErr, tc.refused)
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
