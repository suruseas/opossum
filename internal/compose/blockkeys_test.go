package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A block of a service that opossum takes as it comes — `blkio_config`, `credential_spec`, `logging`, `provider`, the entries of `post_start`, `pre_stop`,
// `devices`, a model — is held to the keys the schema gives it: one it does not know is refused (`additional properties 'a' not allowed`), and so is a
// required one that is missing (`provider` without `type`, a hook without `command`, a device without `source`). docker compose puts the schema to the
// project after each file it adds — the files before it merged, the extends read — so the files given with `-f` are each asked what the ones before them
// and itself made: a later file that lacks a key a block requires is fine where an earlier file gave it, and a block that lacks it at one step is refused
// whatever a later file gives it. An extending service that gives what the base lacks makes the file fine, a service a profile leaves out is asked as any,
// and one nothing takes is not. An `extra_hosts` entry that is neither `host=ip` nor `host:ip` is refused once the files are merged, as the block's keys
// are (#1644). Every row is docker compose v5.5.1's rc for the same file (`config -q`). `pid: ~` is left out: docker compose panics there.
func TestABlockOfAServiceIsHeldToTheKeysTheSchemaGivesIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   map[string]string
		order   []string
		refused bool
	}{
		{"blkio_config: {a: 1}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    blkio_config: {a: 1}\n"}, []string{"c.yaml"}, true},
		{"credential_spec: {a: 1}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    credential_spec: {a: 1}\n"}, []string{"c.yaml"}, true},
		{"logging: {a: 1}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    logging: {a: 1}\n"}, []string{"c.yaml"}, true},
		{"provider: {a: 1}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    provider: {a: 1}\n"}, []string{"c.yaml"}, true},
		{"provider: {}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    provider: {}\n"}, []string{"c.yaml"}, true},
		{"extra_hosts: [a]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    extra_hosts: [a]\n"}, []string{"c.yaml"}, true},
		{"post_start: [{}]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    post_start: [{}]\n"}, []string{"c.yaml"}, true},
		{"pre_stop: [{}]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    pre_stop: [{}]\n"}, []string{"c.yaml"}, true},
		{"blkio_config: {weight_device: [{}]}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    blkio_config: {weight_device: [{}]}\n"}, []string{"c.yaml"}, false},
		{"blkio_config: {weight_device: [{path: /dev/x}]}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    blkio_config: {weight_device: [{path: /dev/x}]}\n"}, []string{"c.yaml"}, false},
		{"blkio_config: {device_read_bps: [{path: /dev/x}]}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    blkio_config: {device_read_bps: [{path: /dev/x}]}\n"}, []string{"c.yaml"}, false},
		{"blkio_config: {weight: 5}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    blkio_config: {weight: 5}\n"}, []string{"c.yaml"}, false},
		{"credential_spec: {}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    credential_spec: {}\n"}, []string{"c.yaml"}, false},
		{"logging: {driver: json-file, options: {a: 1}}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    logging: {driver: json-file, options: {a: 1}}\n"}, []string{"c.yaml"}, false},
		{"logging: {options: 3}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    logging: {options: 3}\n"}, []string{"c.yaml"}, true},
		{"logging: {driver: 3}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    logging: {driver: 3}\n"}, []string{"c.yaml"}, true},
		{"provider: {type: x, a: 1}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    provider: {type: x, a: 1}\n"}, []string{"c.yaml"}, true},
		{"provider: {type: x}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    provider: {type: x}\n"}, []string{"c.yaml"}, false},
		{"provider: {options: {a: 1}}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    provider: {options: {a: 1}}\n"}, []string{"c.yaml"}, true},
		{"post_start: [{command: x, a: 1}]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    post_start: [{command: x, a: 1}]\n"}, []string{"c.yaml"}, true},
		{"post_start: [{command: [x], user: root, privileged: true, working_dir: /, environment: {A: b}}]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    post_start: [{command: [x], user: root, privileged: true, working_dir: /, environment: {A: b}}]\n"}, []string{"c.yaml"}, false},
		{"post_start: [{command: 3}]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    post_start: [{command: 3}]\n"}, []string{"c.yaml"}, true},
		{"pre_stop: [{command: x, a: 1}]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    pre_stop: [{command: x, a: 1}]\n"}, []string{"c.yaml"}, true},
		{"develop: {a: 1}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    develop: {a: 1}\n"}, []string{"c.yaml"}, true},
		{"deploy: {a: 1}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    deploy: {a: 1}\n"}, []string{"c.yaml"}, true},
		{"healthcheck: {a: 1}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    healthcheck: {a: 1}\n"}, []string{"c.yaml"}, true},
		{"ulimits: {a: {b: 1}}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    ulimits: {a: {b: 1}}\n"}, []string{"c.yaml"}, true},
		{"sysctls: {a: [1]}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    sysctls: {a: [1]}\n"}, []string{"c.yaml"}, true},
		{"annotations: {a: [1]}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    annotations: {a: [1]}\n"}, []string{"c.yaml"}, true},
		{"extra_hosts: [a:1.1.1.1]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    extra_hosts: [a:1.1.1.1]\n"}, []string{"c.yaml"}, false},
		{"extra_hosts: [\"a:b\"]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    extra_hosts: [\"a:b\"]\n"}, []string{"c.yaml"}, false},
		{"extra_hosts: [\"\"]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    extra_hosts: [\"\"]\n"}, []string{"c.yaml"}, true},
		{"volumes: [{type: bind, source: ., target: /x, a: 1}]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    volumes: [{type: bind, source: ., target: /x, a: 1}]\n"}, []string{"c.yaml"}, true},
		{"ports: [{target: 80, a: 1}]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    ports: [{target: 80, a: 1}]\n"}, []string{"c.yaml"}, true},
		{"configs: [{source: c, a: 1}]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    configs: [{source: c, a: 1}]\n"}, []string{"c.yaml"}, true},
		{"secrets: [{source: c, a: 1}]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    secrets: [{source: c, a: 1}]\n"}, []string{"c.yaml"}, true},
		{"networks: {n: {a: 1}}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    networks: {n: {a: 1}}\n"}, []string{"c.yaml"}, true},
		{"depends_on: {x: {a: 1}}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    depends_on: {x: {a: 1}}\n"}, []string{"c.yaml"}, true},
		{"tmpfs: [{a: 1}]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    tmpfs: [{a: 1}]\n"}, []string{"c.yaml"}, true},
		{"gpus: [{a: 1}]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    gpus: [{a: 1}]\n"}, []string{"c.yaml"}, false},
		{"devices: [{a: 1}]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    devices: [{a: 1}]\n"}, []string{"c.yaml"}, true},
		{"models: {m: {a: 1}}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    models: {m: {a: 1}}\n"}, []string{"c.yaml"}, true},
		{"develop: {watch: [{path: ., action: sync, target: /x, a: 1}]}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    develop: {watch: [{path: ., action: sync, target: /x, a: 1}]}\n"}, []string{"c.yaml"}, true},
		{"label_file: []", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    label_file: []\n"}, []string{"c.yaml"}, false},
		{"cgroup: bad", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    cgroup: bad\n"}, []string{"c.yaml"}, true},
		{"isolation: 3", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    isolation: 3\n"}, []string{"c.yaml"}, true},
		{"runtime: ~", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    runtime: ~\n"}, []string{"c.yaml"}, true},
		{"stop_signal: ~", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    stop_signal: ~\n"}, []string{"c.yaml"}, true},
		{"userns_mode: ~", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    userns_mode: ~\n"}, []string{"c.yaml"}, true},
		{"uts: ~", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    uts: ~\n"}, []string{"c.yaml"}, true},
		{"ipc: ~", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    ipc: ~\n"}, []string{"c.yaml"}, true},
		{"hostname: ~", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    hostname: ~\n"}, []string{"c.yaml"}, true},
		{"extra_hosts: [\":1.1.1.1\"]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    extra_hosts: [\":1.1.1.1\"]\n"}, []string{"c.yaml"}, true},
		{"extra_hosts: [\"=1.1.1.1\"]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    extra_hosts: [\"=1.1.1.1\"]\n"}, []string{"c.yaml"}, true},
		{"extra_hosts: [\"a:\"]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    extra_hosts: [\"a:\"]\n"}, []string{"c.yaml"}, false},
		{"extra_hosts: [\"a=\"]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    extra_hosts: [\"a=\"]\n"}, []string{"c.yaml"}, false},
		{"extra_hosts: [\"a:1.1.1.1\"]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    extra_hosts: [\"a:1.1.1.1\"]\n"}, []string{"c.yaml"}, false},
		{"extra_hosts: [\"a=1.1.1.1\"]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    extra_hosts: [\"a=1.1.1.1\"]\n"}, []string{"c.yaml"}, false},
		{"extra_hosts: [\"a:::1\"]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    extra_hosts: [\"a:::1\"]\n"}, []string{"c.yaml"}, false},
		{"extra_hosts: [\"a:host-gateway\"]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    extra_hosts: [\"a:host-gateway\"]\n"}, []string{"c.yaml"}, false},
		{"extra_hosts: [\"a:b:c\"]", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    extra_hosts: [\"a:b:c\"]\n"}, []string{"c.yaml"}, false},
		{"extra_hosts: {a: \"1.1.1.1\"}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    extra_hosts: {a: \"1.1.1.1\"}\n"}, []string{"c.yaml"}, false},
		{"extra_hosts: {a: \"\"}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    extra_hosts: {a: \"\"}\n"}, []string{"c.yaml"}, false},
		{"extra_hosts: {\"\": \"1.1.1.1\"}", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    extra_hosts: {\"\": \"1.1.1.1\"}\n"}, []string{"c.yaml"}, true},
		{"extra_hosts: bad then a later file adds a good one", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    extra_hosts: [a]\n", "o.yaml": "services:\n  web:\n    extra_hosts: [\"b:1.1.1.1\"]\n"}, []string{"c.yaml", "o.yaml"}, true},
		{"extra_hosts: bad then a later file resets the key", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    extra_hosts: [a]\n", "o.yaml": "services:\n  web:\n    extra_hosts: !reset []\n"}, []string{"c.yaml", "o.yaml"}, false},
		{"extra_hosts: bad then a later file overrides the key", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    extra_hosts: [a]\n", "o.yaml": "services:\n  web:\n    extra_hosts: !override [\"b:1.1.1.1\"]\n"}, []string{"c.yaml", "o.yaml"}, false},
		{"extra_hosts: bad in a gated service", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    profiles: [g]\n    extra_hosts: [a]\n"}, []string{"c.yaml"}, true},
		{"extra_hosts: bad in a sibling the extends does not take", map[string]string{"base.yaml": "services:\n  b:\n    image: y\n  sib:\n    image: z\n    extra_hosts: [a]\n", "c.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: b}\n"}, []string{"c.yaml"}, false},
		{"extra_hosts: bad in the extended service, the extender writes good over it", map[string]string{"base.yaml": "services:\n  b:\n    image: y\n    extra_hosts: [a]\n", "c.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: b}\n    extra_hosts: !override [\"b:1.1.1.1\"]\n"}, []string{"c.yaml"}, false},
		{"extra_hosts: bad in the extended service, kept", map[string]string{"base.yaml": "services:\n  b:\n    image: y\n    extra_hosts: [a]\n", "c.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: b}\n"}, []string{"c.yaml"}, true},
		{"untaken sibling: devices: [{target: /x}]", map[string]string{"base.yaml": "services:\n  b:\n    image: y\n  sib:\n    image: z\n    devices: [{target: /x}]\n", "c.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: b}\n"}, []string{"c.yaml"}, false},
		{"untaken sibling: devices: [{source: /a, target: /b, a: 1}]", map[string]string{"base.yaml": "services:\n  b:\n    image: y\n  sib:\n    image: z\n    devices: [{source: /a, target: /b, a: 1}]\n", "c.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: b}\n"}, []string{"c.yaml"}, false},
		{"untaken sibling: models: {m: {a: 1}}", map[string]string{"base.yaml": "services:\n  b:\n    image: y\n  sib:\n    image: z\n    models: {m: {a: 1}}\n", "c.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: b}\n"}, []string{"c.yaml"}, false},
		{"untaken sibling: models: {m: {endpoint_var: x, a: 1}}", map[string]string{"base.yaml": "services:\n  b:\n    image: y\n  sib:\n    image: z\n    models: {m: {endpoint_var: x, a: 1}}\n", "c.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: b}\n"}, []string{"c.yaml"}, false},
		{"untaken sibling: provider: {a: 1}", map[string]string{"base.yaml": "services:\n  b:\n    image: y\n  sib:\n    image: z\n    provider: {a: 1}\n", "c.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: b}\n"}, []string{"c.yaml"}, false},
		{"untaken sibling: logging: {a: 1}", map[string]string{"base.yaml": "services:\n  b:\n    image: y\n  sib:\n    image: z\n    logging: {a: 1}\n", "c.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: b}\n"}, []string{"c.yaml"}, false},
		{"extends: provider options + extender type", map[string]string{"base.yaml": "services:\n  b:\n    image: y\n    provider: {options: {a: 1}}\n", "c.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: b}\n    provider: {type: x}\n"}, []string{"c.yaml"}, false},
		{"extends: provider type null + extender type", map[string]string{"base.yaml": "services:\n  b:\n    image: y\n    provider: {type: ~}\n", "c.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: b}\n    provider: {type: x}\n"}, []string{"c.yaml"}, false},
		{"extends: logging a + extender override", map[string]string{"base.yaml": "services:\n  b:\n    image: y\n    logging: {a: 1}\n", "c.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: b}\n    logging: !override {driver: x}\n"}, []string{"c.yaml"}, false},
		{"extends: post_start user + extender override", map[string]string{"base.yaml": "services:\n  b:\n    image: y\n    post_start: [{user: root}]\n", "c.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: b}\n    post_start: !override [{command: x}]\n"}, []string{"c.yaml"}, false},
		{"extends: devices target + extender override", map[string]string{"base.yaml": "services:\n  b:\n    image: y\n    devices: [{target: /x}]\n", "c.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: b}\n    devices: !override ['/a:/b']\n"}, []string{"c.yaml"}, false},
		{"extends: provider no type, extender none", map[string]string{"base.yaml": "services:\n  b:\n    image: y\n    provider: {options: {a: 1}}\n", "c.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: b}\n"}, []string{"c.yaml"}, true},
		{"-f: provider type, then options only", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    provider: {type: x}\n", "o.yaml": "services:\n  web:\n    provider: {options: {a: 1}}\n"}, []string{"c.yaml", "o.yaml"}, false},
		{"-f: provider type, then type null", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    provider: {type: x}\n", "o.yaml": "services:\n  web:\n    provider: {type: ~}\n"}, []string{"c.yaml", "o.yaml"}, false},
		{"-f: provider without type, then none", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    provider: {options: {a: 1}}\n", "o.yaml": "services:\n  web:\n    working_dir: /w\n"}, []string{"c.yaml", "o.yaml"}, true},
		{"-f: provider type, then an unknown key", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    provider: {type: x}\n", "o.yaml": "services:\n  web:\n    provider: {b: 1}\n"}, []string{"c.yaml", "o.yaml"}, true},
		{"-f: post_start list concatenated, entry without command stays", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    post_start: [{command: x}]\n", "o.yaml": "services:\n  web:\n    post_start: [{user: root}]\n"}, []string{"c.yaml", "o.yaml"}, true},
		{"-f: provider without type, a later file gives it", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    provider: {options: {a: 1}}\n", "o.yaml": "services:\n  web:\n    provider: {type: x}\n"}, []string{"c.yaml", "o.yaml"}, true},
		{"logging x- key", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    logging: {x-a: 1}\n"}, []string{"c.yaml"}, false},
		{"blkio_config x- key", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    blkio_config: {x-a: 1}\n"}, []string{"c.yaml"}, true},
		{"extra_hosts second entry bad", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    extra_hosts: [\"a:1.1.1.1\", \"b\"]\n"}, []string{"c.yaml"}, true},
		{"3 -f: the 2nd lacks type, the 3rd gives it", map[string]string{"a.yaml": "services:\n  web:\n    image: x\n", "b.yaml": "services:\n  web:\n    provider: {options: {a: 1}}\n", "c.yaml": "services:\n  web:\n    provider: {type: x}\n"}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"3 -f: the 2nd logging a:1, the 3rd !override", map[string]string{"a.yaml": "services:\n  web:\n    image: x\n", "b.yaml": "services:\n  web:\n    logging: {a: 1}\n", "c.yaml": "services:\n  web:\n    logging: !override {driver: x}\n"}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"3 -f: type, options only, options only", map[string]string{"a.yaml": "services:\n  web:\n    image: x\n    provider: {type: x}\n", "b.yaml": "services:\n  web:\n    provider: {options: {a: 1}}\n", "c.yaml": "services:\n  web:\n    provider: {options: {b: 1}}\n"}, []string{"a.yaml", "b.yaml", "c.yaml"}, false},
		{"same-file extends: base type, web options", map[string]string{"c.yaml": "services:\n  base:\n    image: y\n    provider: {type: x}\n  web:\n    extends: base\n    provider: {options: {a: 1}}\n"}, []string{"c.yaml"}, false},
		{"cross-file extends: base type, web options", map[string]string{"base.yaml": "services:\n  b:\n    image: y\n    provider: {type: x}\n", "c.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: b}\n    provider: {options: {a: 1}}\n"}, []string{"c.yaml"}, false},
		{"include single file: base type, web options (same-file extends)", map[string]string{"i.yaml": "services:\n  base:\n    image: y\n    provider: {type: x}\n  web:\n    extends: base\n    provider: {options: {a: 1}}\n", "c.yaml": "include:\n  - i.yaml\n"}, []string{"c.yaml"}, false},
		{"include path list: i1 type, i2 options only", map[string]string{"i1.yaml": "services:\n  i:\n    image: y\n    provider: {type: x}\n", "i2.yaml": "services:\n  i:\n    provider: {options: {a: 1}}\n", "c.yaml": "include:\n  - path: [i1.yaml, i2.yaml]\n"}, []string{"c.yaml"}, false},
		{"deeper key: blkio weight_device item with an unknown key", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    blkio_config: {weight_device: [{path: x, weight: 1, a: 1}]}\n"}, []string{"c.yaml"}, true},
		{"second service has the bad block", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n  other:\n    image: z\n    logging: {a: 1}\n"}, []string{"c.yaml"}, true},
		{"first service bad, second fine", map[string]string{"c.yaml": "services:\n  a:\n    image: y\n    logging: {a: 1}\n  z:\n    image: z\n"}, []string{"c.yaml"}, true},
		{"-f: the first file's second service has the bad block", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n  other:\n    image: z\n    logging: {a: 1}\n", "o.yaml": "services:\n  web:\n    working_dir: /w\n"}, []string{"c.yaml", "o.yaml"}, true},
		{"3 -f: a later service lacks type at the 2nd step, the 3rd gives it", map[string]string{"a.yaml": "services:\n  web:\n    image: x\n", "b.yaml": "services:\n  zzz:\n    image: y\n    provider: {options: {a: 1}}\n", "c.yaml": "services:\n  zzz:\n    provider: {type: x}\n"}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var paths []string
			for _, name := range tc.order {
				paths = append(paths, filepath.Join(dir, name))
			}
			if _, err := LoadFiles(paths, nil); (err != nil) != tc.refused {
				t.Errorf("load: err %v, docker compose refuses it: %v", err, tc.refused)
			}
		})
	}
}

// The refusal of a key a block does not take says how to keep a note (`x-a`) only where the block takes an `x-` key: `logging` does, `blkio_config` and its items do not
// (docker compose refuses `blkio_config: {x-a: 1}`), and a key that already starts with `x-` is not told to start with it again.
func TestTheRefusalOfAKeyABlockDoesNotTakeSaysHowToKeepANoteOnlyWhereThatWorks(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		advice     bool
	}{
		{"logging, a key that is not a note", "    logging: {a: 1}\n", true},
		{"blkio_config, a key that is not a note", "    blkio_config: {a: 1}\n", false},
		{"blkio_config, a note", "    blkio_config: {x-a: 1}\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "c.yaml")
			if err := os.WriteFile(p, []byte("services:\n  web:\n    image: x\n"+tc.line), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadFiles([]string{p}, nil)
			if err == nil {
				t.Fatal("docker compose refuses it, and it was read")
			}
			if has := strings.Contains(err.Error(), "write it as `x-"); has != tc.advice {
				t.Errorf("the refusal says how to keep a note: %v, want %v\n%v", has, tc.advice, err)
			}
		})
	}
}

// What a read that goes on past refusals (the commands that take a project down) does with these: the first one is kept as a value it went past, and the
// project is read — a block's unknown key, a required key it lacks, and an `extra_hosts` entry that is no host — in one file and in several (#1644).
func TestABlockFaultDoesNotStopTheReadThatTakesAProjectDown(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		order []string
	}{
		{"one file, an unknown key", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    logging: {a: 1}\n"}, []string{"c.yaml"}},
		{"one file, a required key that is missing", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    provider: {}\n"}, []string{"c.yaml"}},
		{"one file, an extra_hosts entry that is no host", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    extra_hosts: [a]\n"}, []string{"c.yaml"}},
		{"two files, an unknown key in the second", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n", "o.yaml": "services:\n  web:\n    logging: {a: 1}\n"}, []string{"c.yaml", "o.yaml"}},
		{"two files, a required key the first lacks", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    provider: {options: {a: 1}}\n", "o.yaml": "services:\n  web:\n    working_dir: /w\n"}, []string{"c.yaml", "o.yaml"}},
		// The second file gives what the first lacks, so the merged service is whole and only the step after the first file can see the fault
		// (docker compose, v5.5.1, validates each file as it reads it: `validating c.yaml: services.web.provider missing property 'type'`).
		{"two files, a required key the first lacks and the second gives", map[string]string{"c.yaml": "services:\n  web:\n    image: x\n    provider: {options: {a: 1}}\n", "o.yaml": "services:\n  web:\n    provider: {type: t}\n"}, []string{"c.yaml", "o.yaml"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var paths []string
			for _, name := range tc.order {
				paths = append(paths, filepath.Join(dir, name))
			}
			if _, err := LoadFiles(paths, nil); err == nil {
				t.Fatal("the strict read took a project docker compose refuses")
			}
			project, err := LoadFilesEnvDirSoft(paths, nil, "")
			if err != nil {
				t.Fatalf("the soft read stopped: %v", err)
			}
			if project.CheckValueFaults() == nil {
				t.Errorf("the soft read kept no refusal")
			}
		})
	}
}
