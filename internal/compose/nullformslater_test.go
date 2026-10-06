package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A key with nothing after it that opossum read as "not given" and docker compose refuses (#1586; every row measured with `docker compose
// config -q`, v5.5.1): a key of a long-form entry of `volumes`, `secrets` or `configs` (at any depth, an `x-` key left), asked once the
// entries of one mount point or target are one, not in a service an extends takes from another file until it is merged with the one that
// extends it, and not in one that is not taken; a mapping written as an alias (`a: *s`, `logging: *l`, `deploy: *d`) asked key by key as
// one written there, over what an earlier file holds (a `<<` merge key at `services:` too: a service it brings is asked of what an earlier
// file holds under its own name); and a service written with nothing under it that no earlier file wrote, which a later file giving it a body
// does not take back. The names of a `labels` hold what they like, nothing included. (The forms of this issue that were refused already stay rows, so that they are held.)
func TestAKeyWithNothingAfterItThatWasReadAsNotGivenIsRefusedAsDockerComposeRefusesIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   map[string]string
		order   []string
		refused bool
	}{
		{"depends_on restart null, an earlier file lists the dependency", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    depends_on: [db]\n  db:\n    image: x\n", "b.yaml": "services:\n  a:\n    depends_on:\n      db:\n        restart:\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"depends_on restart null, an earlier file has the long form", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    depends_on: {db: {condition: service_started, restart: true}}\n  db:\n    image: x\n", "b.yaml": "services:\n  a:\n    depends_on:\n      db:\n        restart:\n"}, []string{"a.yaml", "b.yaml"}, false},
		{"depends_on whole null, an earlier file lists the dependency", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    depends_on: [db]\n  db:\n    image: x\n", "b.yaml": "services:\n  a:\n    depends_on:\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"depends_on whole null, an earlier file has the long form", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    depends_on: {db: {condition: service_started}}\n  db:\n    image: x\n", "b.yaml": "services:\n  a:\n    depends_on:\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"ulimits entry null, an earlier file gives an int", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    ulimits: {nofile: 100}\n  db:\n    image: x\n", "b.yaml": "services:\n  a:\n    ulimits:\n      nofile:\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"ulimits entry null, an earlier file gives soft/hard", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    ulimits: {nofile: {soft: 1, hard: 2}}\n  db:\n    image: x\n", "b.yaml": "services:\n  a:\n    ulimits:\n      nofile:\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"ulimits whole null, an earlier file gives an int", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    ulimits: {nofile: 100}\n  db:\n    image: x\n", "b.yaml": "services:\n  a:\n    ulimits:\n"}, []string{"a.yaml", "b.yaml"}, false},
		{"a merge key brings a null dns, nothing before", map[string]string{"a.yaml": "x-d: &d\n  dns:\nservices:\n  a:\n    image: x\n    <<: *d\n  db:\n    image: x\n"}, []string{"a.yaml"}, true},
		{"a merge key brings a null dns, a later file", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  db:\n    image: x\n", "b.yaml": "x-d: &d\n  dns:\nservices:\n  a:\n    <<: *d\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"a service as an alias with a null", map[string]string{"a.yaml": "x-s: &s\n  image: x\n  dns:\nservices:\n  web: *s\n  db:\n    image: x\n"}, []string{"a.yaml"}, true},
		{"a service as an alias with a null, a later file", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  db:\n    image: x\n", "b.yaml": "x-s: &s\n  dns:\nservices:\n  a: *s\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"deploy as an alias with a null", map[string]string{"a.yaml": "x-d: &d\n  labels:\nservices:\n  a:\n    image: x\n    deploy: *d\n  db:\n    image: x\n"}, []string{"a.yaml"}, true},
		{"deploy as an alias with a null, a later file", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  db:\n    image: x\n", "b.yaml": "x-d: &d\n  labels:\nservices:\n  a:\n    deploy: *d\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"a null through an alias of null", map[string]string{"a.yaml": "x-n: &n ~\nservices:\n  a:\n    image: x\n    dns: *n\n  db:\n    image: x\n"}, []string{"a.yaml"}, true},
		{"volumes long form with a null field", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: bind\n        source: .\n        target: /a\n        read_only:\n  db:\n    image: x\n"}, []string{"a.yaml"}, true},
		{"volumes long form with a null source", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: bind\n        source:\n        target: /a\n  db:\n    image: x\n"}, []string{"a.yaml"}, true},
		{"volumes long form with a null target", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: bind\n        source: .\n        target:\n  db:\n    image: x\n"}, []string{"a.yaml"}, true},
		{"volumes long form null field, a later file", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes: ['/a:/b']\n  db:\n    image: x\n", "b.yaml": "services:\n  a:\n    volumes:\n      - type: bind\n        source: .\n        target: /c\n        read_only:\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"build.extra_hosts entry null, a later file", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    build:\n      context: .\n      extra_hosts: {h: 1.2.3.4}\n  db:\n    image: x\n", "b.yaml": "services:\n  a:\n    build:\n      extra_hosts:\n        h:\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"a third file: a service null, a value in the last", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  db:\n    image: x\n", "b.yaml": "services:\n  other:\n", "c.yaml": "services:\n  other:\n    image: y\n"}, []string{"a.yaml", "b.yaml", "c.yaml"}, true},
		{"a service null in a later file over a service", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  db:\n    image: x\n", "b.yaml": "services:\n  a:\n"}, []string{"a.yaml", "b.yaml"}, false},
		{"a service null in the only file", map[string]string{"a.yaml": "services:\n  a:\n"}, []string{"a.yaml"}, true},
		{"a service extends a common that holds no value", map[string]string{"a.yaml": "services:\n  common:\n  a:\n    image: x\n    extends: common\n  db:\n    image: x\n"}, []string{"a.yaml"}, true},
		{"a service extends a common that holds nothing, a later file", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  db:\n    image: x\n", "b.yaml": "services:\n  common:\n  a:\n    extends: common\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"secrets long form with a null source", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source:\n  db:\n    image: x\nsecrets:\n  k:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs long form with a null source", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source:\n  db:\n    image: x\nconfigs:\n  k:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets long form with a null target", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: k\n        target:\n  db:\n    image: x\nsecrets:\n  k:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs long form with a null target", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: k\n        target:\n  db:\n    image: x\nconfigs:\n  k:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets long form with a null uid", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: k\n        uid:\n  db:\n    image: x\nsecrets:\n  k:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs long form with a null uid", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: k\n        uid:\n  db:\n    image: x\nconfigs:\n  k:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets long form with a null gid", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: k\n        gid:\n  db:\n    image: x\nsecrets:\n  k:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs long form with a null gid", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: k\n        gid:\n  db:\n    image: x\nconfigs:\n  k:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets long form with a null mode", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: k\n        mode:\n  db:\n    image: x\nsecrets:\n  k:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs long form with a null mode", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: k\n        mode:\n  db:\n    image: x\nconfigs:\n  k:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"volumes long form: a null type", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type:\n        source: /tmp\n        target: /a\n  db:\n    image: x\n"}, []string{"a.yaml"}, true},
		{"volumes long form: a null target", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: bind\n        source: /tmp\n        target:\n  db:\n    image: x\n"}, []string{"a.yaml"}, true},
		{"volumes long form: a null read_only", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n        read_only:\n  db:\n    image: x\n"}, []string{"a.yaml"}, true},
		{"volumes long form: a null consistency", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n        consistency:\n  db:\n    image: x\n"}, []string{"a.yaml"}, true},
		{"volumes long form: a null bind", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n        bind:\n  db:\n    image: x\n"}, []string{"a.yaml"}, true},
		{"volumes long form: a null volume", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n        volume:\n  db:\n    image: x\n"}, []string{"a.yaml"}, true},
		{"volumes long form: a null tmpfs", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n        tmpfs:\n  db:\n    image: x\n"}, []string{"a.yaml"}, true},
		{"volumes long form: a null image", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n        image:\n  db:\n    image: x\n"}, []string{"a.yaml"}, true},
		{"volumes long form: a null bind.propagation", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n        bind:\n          propagation:\n  db:\n    image: x\n"}, []string{"a.yaml"}, true},
		{"volumes long form: a null bind.create_host_path", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n        bind:\n          create_host_path:\n  db:\n    image: x\n"}, []string{"a.yaml"}, true},
		{"volumes long form: a null volume.nocopy", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: volume\n        source: v\n        target: /a\n        volume:\n          nocopy:\n  db:\n    image: x\nvolumes:\n  v:\n"}, []string{"a.yaml"}, true},
		{"volumes long form: a null volume.subpath", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: volume\n        source: v\n        target: /a\n        volume:\n          subpath:\n  db:\n    image: x\nvolumes:\n  v:\n"}, []string{"a.yaml"}, true},
		{"volumes long form: a null tmpfs.size", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: tmpfs\n        target: /a\n        tmpfs:\n          size:\n  db:\n    image: x\n"}, []string{"a.yaml"}, true},
		{"volumes long null: an extended taken service, the extender writes the entry again", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n        read_only:\n  db:\n    image: x\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n        read_only: true\n  db:\n    image: x\n"}, []string{"compose.yaml"}, false},
		{"volumes long null: an extended taken service, stays", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n        read_only:\n  db:\n    image: x\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n  db:\n    image: x\n"}, []string{"compose.yaml"}, true},
		{"volumes long null: a service of the extended file that is not taken", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n  other:\n    image: x\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n        read_only:\n  db:\n    image: x\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n  db:\n    image: x\n"}, []string{"compose.yaml"}, false},
		{"volumes long null: a service with a profile", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    profiles: [p]\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n        read_only:\n  db:\n    image: x\n"}, []string{"a.yaml"}, true},
		{"volumes long null: the same mount point again after it in the file", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n        read_only:\n      - type: bind\n        source: /tmp\n        target: /a\n        read_only: true\n  db:\n    image: x\n"}, []string{"a.yaml"}, false},
		{"volumes long null: a later file writes the same mount point again", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n        read_only:\n  db:\n    image: x\n", "b.yaml": "services:\n  a:\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n        read_only: true\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"volumes long null: over a short entry of an earlier file", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes: ['/tmp:/a']\n  db:\n    image: x\n", "b.yaml": "services:\n  a:\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n        read_only:\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"an x- key of a long volume with nothing after it", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n        x-a:\n  db:\n    image: x\n"}, []string{"a.yaml"}, false},
		{"secrets long null uid: the same entry again after it in the file", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: k\n        uid:\n      - source: k\n        uid: '1'\n  db:\n    image: x\nsecrets:\n  k:\n    file: ./s.txt\n"}, []string{"a.yaml"}, false},
		{"secrets long null uid: a later file writes the entry again", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: k\n        uid:\n  db:\n    image: x\nsecrets:\n  k:\n    file: ./s.txt\n", "b.yaml": "services:\n  a:\n    secrets:\n      - source: k\n        uid: '1'\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"secrets long null uid: an extended taken service, the extender writes the entry again", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    secrets:\n      - source: k\n        uid:\n  db:\n    image: x\nsecrets:\n  k:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n    secrets:\n      - source: k\n        uid: '1'\n  db:\n    image: x\nsecrets:\n  k:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"secrets long null uid: an extended taken service, stays", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    secrets:\n      - source: k\n        uid:\n  db:\n    image: x\nsecrets:\n  k:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n  db:\n    image: x\nsecrets:\n  k:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, true},
		{"secrets long null uid: a service of the extended file that is not taken", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n  other:\n    image: x\n    secrets:\n      - source: k\n        uid:\n  db:\n    image: x\nsecrets:\n  k:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n  db:\n    image: x\nsecrets:\n  k:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"configs long null uid: the same entry again after it in the file", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: k\n        uid:\n      - source: k\n        uid: '1'\n  db:\n    image: x\nconfigs:\n  k:\n    file: ./s.txt\n"}, []string{"a.yaml"}, false},
		{"configs long null uid: a later file writes the entry again", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: k\n        uid:\n  db:\n    image: x\nconfigs:\n  k:\n    file: ./s.txt\n", "b.yaml": "services:\n  a:\n    configs:\n      - source: k\n        uid: '1'\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"configs long null uid: an extended taken service, the extender writes the entry again", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    configs:\n      - source: k\n        uid:\n  db:\n    image: x\nconfigs:\n  k:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n    configs:\n      - source: k\n        uid: '1'\n  db:\n    image: x\nconfigs:\n  k:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"configs long null uid: an extended taken service, stays", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    configs:\n      - source: k\n        uid:\n  db:\n    image: x\nconfigs:\n  k:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n  db:\n    image: x\nconfigs:\n  k:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, true},
		{"configs long null uid: a service of the extended file that is not taken", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n  other:\n    image: x\n    configs:\n      - source: k\n        uid:\n  db:\n    image: x\nconfigs:\n  k:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n  db:\n    image: x\nconfigs:\n  k:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"a whole service as an alias, a null in it, over a value of an earlier file", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    dns: [1.1.1.1]\n  db:\n    image: x\n", "b.yaml": "x-s: &s\n  dns:\nservices:\n  a: *s\n"}, []string{"a.yaml", "b.yaml"}, false},
		{"a block as an alias, a null in it, over a value of an earlier file", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    logging: {driver: json-file}\n  db:\n    image: x\n", "b.yaml": "x-l: &l\n  driver:\nservices:\n  a:\n    logging: *l\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"a block as an alias, a null in it, deploy.labels over a value", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    deploy: {labels: {a: b}}\n  db:\n    image: x\n", "b.yaml": "x-d: &d\n  labels:\nservices:\n  a:\n    deploy: *d\n"}, []string{"a.yaml", "b.yaml"}, false},
		{"a service with nothing under it, defined in the next file, which is the same service", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  b:\n", "b.yaml": "services:\n  b:\n    image: y\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"a long volume: a label with nothing after it is an empty label", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: volume\n        source: v\n        target: /a\n        volume:\n          labels:\n            k:\n  db:\n    image: x\nvolumes:\n  v:\n"}, []string{"a.yaml"}, false},
		{"a long volume: a label with nothing after it, a later file", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  db:\n    image: x\nvolumes:\n  v:\n", "b.yaml": "services:\n  a:\n    volumes:\n      - type: volume\n        source: v\n        target: /a\n        volume:\n          labels:\n            k:\n"}, []string{"a.yaml", "b.yaml"}, false},
		{"a long volume: a label with nothing after it, an extended taken service", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    volumes:\n      - type: volume\n        source: v\n        target: /a\n        volume:\n          labels:\n            k:\n  db:\n    image: x\nvolumes:\n  v:\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n  db:\n    image: x\nvolumes:\n  v:\n"}, []string{"compose.yaml"}, false},
		{"a long volume: the whole labels with nothing after it", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: volume\n        source: v\n        target: /a\n        volume:\n          labels:\n  db:\n    image: x\nvolumes:\n  v:\n"}, []string{"a.yaml"}, true},
		{"a merge key at services: brings a service whose key is null, over a value of an earlier file", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    dns: [1.1.1.1]\n  db:\n    image: x\n", "b.yaml": "x-s: &s\n  a:\n    dns:\nservices:\n  <<: *s\n"}, []string{"a.yaml", "b.yaml"}, false},
		{"a merge key at services: brings a service whose key is null, nothing before", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  db:\n    image: x\n", "b.yaml": "x-s: &s\n  a:\n    dns:\nservices:\n  <<: *s\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"a list of merge keys at services: brings a service whose key is null, over a value", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    dns: [1.1.1.1]\n  db:\n    image: x\n", "b.yaml": "x-s: &s\n  a:\n    dns:\nservices:\n  <<: [*s]\n"}, []string{"a.yaml", "b.yaml"}, false},
		{"a merge key at services: brings a service whose key networks is null, over a value (refused)", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    networks: [default]\n  db:\n    image: x\n", "b.yaml": "x-s: &s\n  a:\n    networks:\nservices:\n  <<: *s\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"volumes: a null in the second entry of another mount point", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n      - type: bind\n        source: /tmp\n        target: /b\n        read_only:\n  db:\n    image: x\n"}, []string{"a.yaml"}, true},
		{"volumes: a null in the second entry, an extended taken service", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n      - type: bind\n        source: /tmp\n        target: /b\n        read_only:\n  db:\n    image: x\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n  db:\n    image: x\n"}, []string{"compose.yaml"}, true},
		{"secrets: a null in the second entry of another target", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: k\n      - source: j\n        uid:\n  db:\n    image: x\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: a null in the second entry, an extended taken service", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    secrets:\n      - source: k\n      - source: j\n        uid:\n  db:\n    image: x\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n  db:\n    image: x\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, true},
		{"a long volume: a deep x- key with nothing after it", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n        bind:\n          x-n:\n  db:\n    image: x\n"}, []string{"a.yaml"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "s.txt"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var paths []string
			for _, name := range tc.order {
				paths = append(paths, filepath.Join(dir, name))
			}
			_, err := LoadFiles(paths, nil)
			if (err != nil) != tc.refused {
				t.Errorf("refused is %v, want %v (err %v)", err != nil, tc.refused, err)
			}
		})
	}
}

// What is refused names the key as a path from the service: `services.a.volumes[1].read_only`, `services.a.secrets[0].uid`.
func TestTheRefusalOfANullInALongEntryNamesTheKey(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"a volume", "services:\n  a:\n    image: x\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n      - type: bind\n        source: /tmp\n        target: /b\n        read_only:\n", "services.a.volumes[1].read_only"},
		{"a volume's option", "services:\n  a:\n    image: x\n    volumes:\n      - type: volume\n        source: v\n        target: /a\n        volume:\n          nocopy:\nvolumes:\n  v:\n", "services.a.volumes[0].volume.nocopy"},
		{"a secret", "services:\n  a:\n    image: x\n    secrets:\n      - source: k\n        uid:\nsecrets:\n  k:\n    file: ./s.txt\n", "services.a.secrets[0].uid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "s.txt"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal is %v, want it to name %s", err, tc.want)
			}
		})
	}
}

// A load that goes on past a fault (the commands that take a project down) keeps what is refused here as a fault of the project, and does not
// stop: a service with nothing under it once a file is merged in, and a null in a long entry.
func TestWhatIsRefusedOfANullInALaterFileIsKeptWhereTheLoadGoesOn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		order []string
		want  string
	}{
		{"a service with nothing under it that a later file gives a body", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n", "b.yaml": "services:\n  other:\n", "c.yaml": "services:\n  other:\n    image: y\n"}, []string{"a.yaml", "b.yaml", "c.yaml"}, `service "other"`},
		{"a null in a long volume entry", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    volumes:\n      - type: bind\n        source: /tmp\n        target: /a\n        read_only:\n"}, []string{"a.yaml"}, "services.a.volumes[0].read_only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var paths []string
			for _, name := range tc.order {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(tc.files[name]), 0o644); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, filepath.Join(dir, name))
			}
			p, err := LoadFilesEnvDirSoft(paths, nil, dir)
			if err != nil {
				t.Fatalf("the load stopped: %v", err)
			}
			if err := p.CheckValueFaults(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the fault kept is %v, want it to name %s", err, tc.want)
			}
		})
	}
}
