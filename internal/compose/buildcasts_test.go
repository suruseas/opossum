package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// The casts and forms of a service's `build` are read as docker compose reads them (`config -q`, every row measured, v5.5.1: `refused` is its
// rc 1; #1783). Asked of the merged project (a later file or an extending service that writes the value over makes the file fine, a service
// nothing takes is not asked, one a profile leaves out is, but for the secrets it names): `no_cache`, `privileged` and `pull` cast to a boolean,
// `shm_size` to a byte size, each `extra_hosts` entry is `host=ip`, a `ulimits` mapping has a whole `soft` and a whole `hard` and nothing else, and
// each `secrets` entry names a declared secret with a `mode` that reads. Asked as each file is read: an `ssh` entry is `default` or has an `=` (none
// twice), and a `ulimits` limit is not a word, a fraction or a boolean. The forms left as a difference are not here (the docs list them): a `labels`
// name of nothing with nothing after it, and a `ulimits` mapping with no `soft` or `hard`, a word for one, or a key beside them, that a later file or an
// extending service writes over without a tag.
func TestTheCastsAndFormsOfAServicesBuildAreReadAsDockerComposeReadsThem(t *testing.T) {
	type file struct{ name, body string }
	for _, tc := range []struct {
		name    string
		order   []string // the files given with `-f`, in order
		files   []file   // every file written, an extended one too
		refused bool
	}{
		{"build.no_cache: true", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: true
`}}, false},
		{"build.no_cache: false", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: false
`}}, false},
		{"build.no_cache: 'true'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: 'true'
`}}, false},
		{"build.no_cache: 'false'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: 'false'
`}}, false},
		{"build.no_cache: 'yes'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: 'yes'
`}}, false},
		{"build.no_cache: 'no'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: 'no'
`}}, false},
		{"build.no_cache: 'on'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: 'on'
`}}, false},
		{"build.no_cache: 'off'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: 'off'
`}}, false},
		{"build.no_cache: x", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: x
`}}, true},
		{"build.no_cache: ''", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: ''
`}}, true},
		{"build.no_cache: 1", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: 1
`}}, true},
		{"build.no_cache: 0", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: 0
`}}, true},
		{"build.no_cache: '1'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: '1'
`}}, true},
		{"build.no_cache: '0'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: '0'
`}}, true},
		{"build.no_cache: 'True'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: 'True'
`}}, false},
		{"build.no_cache: 'TRUE'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: 'TRUE'
`}}, false},
		{"build.no_cache: 't'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: 't'
`}}, true},
		{"build.no_cache: 'y'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: 'y'
`}}, false},
		{"build.no_cache: 2", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: 2
`}}, true},
		{"build.no_cache: 1.5", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: 1.5
`}}, true},
		{"build.no_cache: ~", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: ~
`}}, true},
		{"build.no_cache: []", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: []
`}}, true},
		{"build.no_cache: {}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: {}
`}}, true},
		{"build.privileged: true", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: true
`}}, false},
		{"build.privileged: false", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: false
`}}, false},
		{"build.privileged: 'true'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: 'true'
`}}, false},
		{"build.privileged: 'false'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: 'false'
`}}, false},
		{"build.privileged: 'yes'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: 'yes'
`}}, false},
		{"build.privileged: 'no'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: 'no'
`}}, false},
		{"build.privileged: 'on'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: 'on'
`}}, false},
		{"build.privileged: 'off'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: 'off'
`}}, false},
		{"build.privileged: x", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: x
`}}, true},
		{"build.privileged: ''", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: ''
`}}, true},
		{"build.privileged: 1", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: 1
`}}, true},
		{"build.privileged: 0", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: 0
`}}, true},
		{"build.privileged: '1'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: '1'
`}}, true},
		{"build.privileged: '0'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: '0'
`}}, true},
		{"build.privileged: 'True'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: 'True'
`}}, false},
		{"build.privileged: 'TRUE'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: 'TRUE'
`}}, false},
		{"build.privileged: 't'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: 't'
`}}, true},
		{"build.privileged: 'y'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: 'y'
`}}, false},
		{"build.privileged: 2", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: 2
`}}, true},
		{"build.privileged: 1.5", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: 1.5
`}}, true},
		{"build.privileged: ~", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: ~
`}}, true},
		{"build.privileged: []", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: []
`}}, true},
		{"build.privileged: {}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      privileged: {}
`}}, true},
		{"build.pull: true", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: true
`}}, false},
		{"build.pull: false", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: false
`}}, false},
		{"build.pull: 'true'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: 'true'
`}}, false},
		{"build.pull: 'false'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: 'false'
`}}, false},
		{"build.pull: 'yes'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: 'yes'
`}}, false},
		{"build.pull: 'no'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: 'no'
`}}, false},
		{"build.pull: 'on'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: 'on'
`}}, false},
		{"build.pull: 'off'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: 'off'
`}}, false},
		{"build.pull: x", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: x
`}}, true},
		{"build.pull: ''", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: ''
`}}, true},
		{"build.pull: 1", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: 1
`}}, true},
		{"build.pull: 0", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: 0
`}}, true},
		{"build.pull: '1'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: '1'
`}}, true},
		{"build.pull: '0'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: '0'
`}}, true},
		{"build.pull: 'True'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: 'True'
`}}, false},
		{"build.pull: 'TRUE'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: 'TRUE'
`}}, false},
		{"build.pull: 't'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: 't'
`}}, true},
		{"build.pull: 'y'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: 'y'
`}}, false},
		{"build.pull: 2", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: 2
`}}, true},
		{"build.pull: 1.5", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: 1.5
`}}, true},
		{"build.pull: ~", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: ~
`}}, true},
		{"build.pull: []", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: []
`}}, true},
		{"build.pull: {}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      pull: {}
`}}, true},
		{"build.shm_size: 64m", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      shm_size: 64m
`}}, false},
		{"build.shm_size: '64m'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      shm_size: '64m'
`}}, false},
		{"build.shm_size: x", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      shm_size: x
`}}, true},
		{"build.shm_size: ''", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      shm_size: ''
`}}, true},
		{"build.shm_size: 1.5", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      shm_size: 1.5
`}}, true},
		{"build.shm_size: -1", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      shm_size: -1
`}}, false},
		{"build.shm_size: 1e3", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      shm_size: 1e3
`}}, false},
		{"build.shm_size: 0x10", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      shm_size: 0x10
`}}, false},
		{"build.shm_size: '1k'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      shm_size: '1k'
`}}, false},
		{"build.shm_size: '1 k'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      shm_size: '1 k'
`}}, false},
		{"build.shm_size: 100", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      shm_size: 100
`}}, false},
		{"build.shm_size: '100'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      shm_size: '100'
`}}, false},
		{"build.shm_size: '1gb'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      shm_size: '1gb'
`}}, false},
		{"build.shm_size: '1.5g'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      shm_size: '1.5g'
`}}, false},
		{"build.shm_size: ' 64m'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      shm_size: ' 64m'
`}}, true},
		{"build.shm_size: '64m '", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      shm_size: '64m '
`}}, true},
		{"build.shm_size: true", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      shm_size: true
`}}, true},
		{"build.shm_size: []", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      shm_size: []
`}}, true},
		{"build.shm_size: ~", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      shm_size: ~
`}}, true},
		{"build.extra_hosts: [h=1.1.1.1]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: [h=1.1.1.1]
`}}, false},
		{"build.extra_hosts: [x]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: [x]
`}}, true},
		{"build.extra_hosts: ['h:1.1.1.1']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: ['h:1.1.1.1']
`}}, false},
		{"build.extra_hosts: ['h=']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: ['h=']
`}}, false},
		{"build.extra_hosts: ['=1.1.1.1']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: ['=1.1.1.1']
`}}, true},
		{"build.extra_hosts: ['h=::1']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: ['h=::1']
`}}, false},
		{"build.extra_hosts: {h: 1.1.1.1}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: {h: 1.1.1.1}
`}}, false},
		{"build.extra_hosts: {h: x}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: {h: x}
`}}, false},
		{"build.extra_hosts: {h: 1}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: {h: 1}
`}}, true},
		{"build.extra_hosts: [1]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: [1]
`}}, true},
		{"build.extra_hosts: [h=1.1.1.1, x]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: [h=1.1.1.1, x]
`}}, true},
		{"build.extra_hosts: x", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: x
`}}, true},
		{"build.extra_hosts: ~", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: ~
`}}, true},
		{"build.extra_hosts: ['h=1.1.1.1', 'h=2.2.2.2']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: ['h=1.1.1.1', 'h=2.2.2.2']
`}}, false},
		{"build.extra_hosts: ['h=1.1.1.1/24']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: ['h=1.1.1.1/24']
`}}, false},
		{"build.ssh: [default]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: [default]
`}}, false},
		{"build.ssh: ['id=path']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: ['id=path']
`}}, false},
		{"build.ssh: [x]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: [x]
`}}, true},
		{"build.ssh: ['id']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: ['id']
`}}, true},
		{"build.ssh: ['a=b=c']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: ['a=b=c']
`}}, false},
		{"build.ssh: ['=path']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: ['=path']
`}}, false},
		{"build.ssh: ['id=']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: ['id=']
`}}, false},
		{"build.ssh: {default: ''}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: {default: ''}
`}}, false},
		{"build.ssh: {id: path}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: {id: path}
`}}, false},
		{"build.ssh: {id: 1}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: {id: 1}
`}}, false},
		{"build.ssh: [1]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: [1]
`}}, true},
		{"build.ssh: default", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: default
`}}, true},
		{"build.ssh: ~", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: ~
`}}, true},
		{"build.ssh: ['default=/k']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: ['default=/k']
`}}, false},
		{"build.ssh: [default, x]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: [default, x]
`}}, true},
		{"build.ulimits: {nofile: 1}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: 1}
`}}, false},
		{"build.ulimits: {nofile: {soft: 1, hard: 2}}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: {soft: 1, hard: 2}}
`}}, false},
		{"build.ulimits: {a: b}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {a: b}
`}}, true},
		{"build.ulimits: {a: {b: 1}}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {a: {b: 1}}
`}}, true},
		{"build.ulimits: {nofile: 1.5}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: 1.5}
`}}, true},
		{"build.ulimits: {nofile: '1'}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: '1'}
`}}, true},
		{"build.ulimits: {nofile: {soft: 1}}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: {soft: 1}}
`}}, true},
		{"build.ulimits: {nofile: {soft: 1, hard: x}}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: {soft: 1, hard: x}}
`}}, true},
		{"build.ulimits: [nofile=1]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: [nofile=1]
`}}, true},
		{"build.ulimits: [1]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: [1]
`}}, true},
		{"build.ulimits: {}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {}
`}}, false},
		{"build.ulimits: x", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: x
`}}, true},
		{"build.ulimits: ~", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: ~
`}}, true},
		{"build.ulimits: {nofile: -1}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: -1}
`}}, false},
		{"build.ulimits: {nofile: true}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: true}
`}}, true},
		{"build.ulimits: {nofile: {soft: 1, hard: 2, extra: 3}}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: {soft: 1, hard: 2, extra: 3}}
`}}, true},
		{"build.secrets: [x]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [x]
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"build.secrets: [x] (none declared)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [x]
`}}, true},
		{"build.secrets: [k]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [k]
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"build.secrets: [k] (none declared)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [k]
`}}, true},
		{"build.secrets: [{source: k}]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [{source: k}]
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"build.secrets: [{source: k}] (none declared)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [{source: k}]
`}}, true},
		{"build.secrets: [{source: x}]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [{source: x}]
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"build.secrets: [{source: x}] (none declared)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [{source: x}]
`}}, true},
		{"build.secrets: [{source: k, target: /t, mode: 0400}]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [{source: k, target: /t, mode: 0400}]
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"build.secrets: [{source: k, target: /t, mode: 0400}] (none declared)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [{source: k, target: /t, mode: 0400}]
`}}, true},
		{"build.secrets: {k: {}}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: {k: {}}
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"build.secrets: {k: {}} (none declared)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: {k: {}}
`}}, true},
		{"build.secrets: k", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: k
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"build.secrets: k (none declared)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: k
`}}, true},
		{"build.secrets: ~", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: ~
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"build.secrets: ~ (none declared)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: ~
`}}, true},
		{"build.secrets: []", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: []
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"build.secrets: [] (none declared)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: []
`}}, false},
		{"build.labels: {'': 1}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      labels: {'': 1}
`}}, true},
		{"build.labels: {a: 1}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      labels: {a: 1}
`}}, false},
		{"build.labels: {'a b': 1}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      labels: {'a b': 1}
`}}, false},
		{"build.labels: ['=x']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      labels: ['=x']
`}}, false},
		{"build.labels: ['a=1']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      labels: ['a=1']
`}}, false},
		{"build.labels: ['']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      labels: ['']
`}}, false},
		{"build.labels: {'a': ~}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      labels: {'a': ~}
`}}, false},
		{"no_cache x: the first -f file, the second writes true", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: x
`}, {"f2.yaml", `services:
  a:
    build:
      no_cache: true
`}}, false},
		{"no_cache x: the second -f file writes it over a good one", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      no_cache: true
`}, {"f2.yaml", `services:
  a:
    build:
      no_cache: x
`}}, true},
		{"no_cache x: in the extended service, the extender writes true", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      no_cache: true
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      no_cache: x
`}}, false},
		{"no_cache x: in the extended service, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      no_cache: x
`}}, true},
		{"no_cache x: in a sibling of the extended service", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    build:
      context: .
      no_cache: x
`}}, false},
		{"no_cache x: in a profile-gated service not enabled", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    profiles: [g]
    build:
      context: .
      no_cache: x
`}}, true},
		{"shm_size x: the first -f file, the second writes 64m", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      shm_size: x
`}, {"f2.yaml", `services:
  a:
    build:
      shm_size: 64m
`}}, false},
		{"shm_size x: the second -f file writes it over a good one", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      shm_size: 64m
`}, {"f2.yaml", `services:
  a:
    build:
      shm_size: x
`}}, true},
		{"shm_size x: in the extended service, the extender writes 64m", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      shm_size: 64m
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      shm_size: x
`}}, false},
		{"shm_size x: in the extended service, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      shm_size: x
`}}, true},
		{"shm_size x: in a sibling of the extended service", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    build:
      context: .
      shm_size: x
`}}, false},
		{"shm_size x: in a profile-gated service not enabled", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    profiles: [g]
    build:
      context: .
      shm_size: x
`}}, true},
		{"extra_hosts [x]: the first -f file, the second writes [h=1.1.1.1]", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: [x]
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts: [h=1.1.1.1]
`}}, true},
		{"extra_hosts [x]: the second -f file writes it over a good one", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: [h=1.1.1.1]
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts: [x]
`}}, true},
		{"extra_hosts [x]: in the extended service, the extender writes [h=1.1.1.1]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      extra_hosts: [h=1.1.1.1]
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      extra_hosts: [x]
`}}, true},
		{"extra_hosts [x]: in the extended service, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      extra_hosts: [x]
`}}, true},
		{"extra_hosts [x]: in a sibling of the extended service", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    build:
      context: .
      extra_hosts: [x]
`}}, false},
		{"extra_hosts [x]: in a profile-gated service not enabled", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    profiles: [g]
    build:
      context: .
      extra_hosts: [x]
`}}, true},
		{"ssh [x]: the first -f file, the second writes [default]", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: [x]
`}, {"f2.yaml", `services:
  a:
    build:
      ssh: [default]
`}}, true},
		{"ssh [x]: the second -f file writes it over a good one", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: [default]
`}, {"f2.yaml", `services:
  a:
    build:
      ssh: [x]
`}}, true},
		{"ssh [x]: in the extended service, the extender writes [default]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      ssh: [default]
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ssh: [x]
`}}, true},
		{"ssh [x]: in the extended service, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ssh: [x]
`}}, true},
		{"ssh [x]: in a sibling of the extended service", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    build:
      context: .
      ssh: [x]
`}}, true},
		{"ssh [x]: in a profile-gated service not enabled", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    profiles: [g]
    build:
      context: .
      ssh: [x]
`}}, true},
		{"ulimits {a: b}: the first -f file, the second writes {nofile: 1}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {a: b}
`}, {"f2.yaml", `services:
  a:
    build:
      ulimits: {nofile: 1}
`}}, true},
		{"ulimits {a: b}: the second -f file writes it over a good one", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: 1}
`}, {"f2.yaml", `services:
  a:
    build:
      ulimits: {a: b}
`}}, true},
		{"ulimits {a: b}: in the extended service, the extender writes {nofile: 1}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      ulimits: {nofile: 1}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {a: b}
`}}, true},
		{"ulimits {a: b}: in the extended service, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {a: b}
`}}, true},
		{"ulimits {a: b}: in a sibling of the extended service", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    build:
      context: .
      ulimits: {a: b}
`}}, true},
		{"ulimits {a: b}: in a profile-gated service not enabled", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    profiles: [g]
    build:
      context: .
      ulimits: {a: b}
`}}, true},
		{"labels {'': ~}: the second -f file writes it over a good one", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      labels: {a: b}
`}, {"f2.yaml", `services:
  a:
    build:
      labels: {'': ~}
`}}, false},
		{"labels {'': ~}: in the extended service, the extender writes {a: b}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      labels: {a: b}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      labels: {'': ~}
`}}, false},
		{"labels {'': ~}: in a sibling of the extended service", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    build:
      context: .
      labels: {'': ~}
`}}, false},
		{"build.extra_hosts: [':x']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: [':x']
`}}, true},
		{"build.extra_hosts: ['h:']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: ['h:']
`}}, false},
		{"build.extra_hosts: ['h x']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: ['h x']
`}}, true},
		{"build.extra_hosts: ['h']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: ['h']
`}}, true},
		{"build.extra_hosts: ['']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: ['']
`}}, true},
		{"build.extra_hosts: ['h=ip']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: ['h=ip']
`}}, false},
		{"build.extra_hosts: ['h=1.1.1.1:80']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: ['h=1.1.1.1:80']
`}}, false},
		{"build.extra_hosts: [' h=1.1.1.1']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: [' h=1.1.1.1']
`}}, false},
		{"build.extra_hosts: ['h = 1.1.1.1']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: ['h = 1.1.1.1']
`}}, false},
		{"build.extra_hosts: ['host.name=1.1.1.1']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: ['host.name=1.1.1.1']
`}}, false},
		{"build.extra_hosts: ['a=b=c']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: ['a=b=c']
`}}, false},
		{"build.extra_hosts: ['h:1.1.1.1:80']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: ['h:1.1.1.1:80']
`}}, false},
		{"build.extra_hosts: ['h=1.1.1.1','h=2.2.2.2']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: ['h=1.1.1.1','h=2.2.2.2']
`}}, false},
		{"build.ssh: ['']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: ['']
`}}, true},
		{"build.ssh: ['Default']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: ['Default']
`}}, true},
		{"build.ssh: ['default=']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: ['default=']
`}}, false},
		{"build.ssh: [default, default]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: [default, default]
`}}, true},
		{"build.ssh: ['id=path', 'id=path2']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: ['id=path', 'id=path2']
`}}, false},
		{"build.ssh: ['a b']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: ['a b']
`}}, true},
		{"build.ssh: [' default']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: [' default']
`}}, true},
		{"build.ssh: ['default ']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: ['default ']
`}}, true},
		{"build.ssh: ['=']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: ['=']
`}}, false},
		{"build.ssh: ['a=b']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: ['a=b']
`}}, false},
		{"build.ulimits: {nofile: 0}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: 0}
`}}, false},
		{"build.ulimits: {nofile: 1.0}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: 1.0}
`}}, true},
		{"build.ulimits: {nofile: 1e3}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: 1e3}
`}}, true},
		{"build.ulimits: {nofile: 0x10}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: 0x10}
`}}, false},
		{"build.ulimits: {nofile: {soft: 1.5, hard: 2}}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: {soft: 1.5, hard: 2}}
`}}, true},
		{"build.ulimits: {nofile: {soft: '1', hard: 2}}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: {soft: '1', hard: 2}}
`}}, true},
		{"build.ulimits: {nofile: {hard: 2}}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: {hard: 2}}
`}}, true},
		{"build.ulimits: {nofile: {soft: 2, hard: 1}}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: {soft: 2, hard: 1}}
`}}, false},
		{"build.ulimits: {nofile: {soft: -1, hard: -1}}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: {soft: -1, hard: -1}}
`}}, false},
		{"build.ulimits: {'': 1}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {'': 1}
`}}, false},
		{"build.ulimits: {nofile: ~}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: ~}
`}}, true},
		{"build.ulimits: {nofile: {soft: ~, hard: 1}}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: {soft: ~, hard: 1}}
`}}, true},
		{"build.ulimits: {nofile: []}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: []}
`}}, true},
		{"build.secrets: [e]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [e]
secrets:
  k: {file: ./k.txt}
  e: {external: true}
`}}, false},
		{"build.secrets: [k, k]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [k, k]
secrets:
  k: {file: ./k.txt}
  e: {external: true}
`}}, false},
		{"build.secrets: [{source: k, uid: '1'}]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [{source: k, uid: '1'}]
secrets:
  k: {file: ./k.txt}
  e: {external: true}
`}}, false},
		{"build.secrets: [{source: k, gid: 1}]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [{source: k, gid: 1}]
secrets:
  k: {file: ./k.txt}
  e: {external: true}
`}}, true},
		{"build.secrets: [{source: k, mode: x}]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [{source: k, mode: x}]
secrets:
  k: {file: ./k.txt}
  e: {external: true}
`}}, true},
		{"build.secrets: [{target: t}]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [{target: t}]
secrets:
  k: {file: ./k.txt}
  e: {external: true}
`}}, true},
		{"build.secrets: [{source: ''}]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [{source: ''}]
secrets:
  k: {file: ./k.txt}
  e: {external: true}
`}}, true},
		{"build.secrets: ['']", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: ['']
secrets:
  k: {file: ./k.txt}
  e: {external: true}
`}}, true},
		{"build.secrets: [k, x]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [k, x]
secrets:
  k: {file: ./k.txt}
  e: {external: true}
`}}, true},
		{"build.secrets: {source: k}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: {source: k}
secrets:
  k: {file: ./k.txt}
  e: {external: true}
`}}, true},
		{"extended {nofile: {hard: 2}}, the extender writes a complete one", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      ulimits: {nofile: {soft: 1, hard: 2}}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: {hard: 2}}
`}}, false},
		{"extended {nofile: {hard: 2}}, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: {hard: 2}}
`}}, true},
		{"extended {nofile: {soft: 1}}, the extender writes a complete one", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      ulimits: {nofile: {soft: 1, hard: 2}}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: {soft: 1}}
`}}, false},
		{"extended {nofile: {soft: 1}}, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: {soft: 1}}
`}}, true},
		{"-f: first {nofile: {soft: 1, hard: 2, extra: 3}}, the second writes a complete one", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: {soft: 1, hard: 2, extra: 3}}
`}, {"f2.yaml", `services:
  a:
    build:
      ulimits: {nofile: {soft: 1, hard: 2}}
`}}, true},
		{"extended {nofile: {soft: 1, hard: 2, extra: 3}}, the extender writes a complete one", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      ulimits: {nofile: {soft: 1, hard: 2}}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: {soft: 1, hard: 2, extra: 3}}
`}}, true},
		{"extended {nofile: {soft: 1, hard: 2, extra: 3}}, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: {soft: 1, hard: 2, extra: 3}}
`}}, true},
		{"-f: first {nofile: {soft: '1', hard: 2}}, the second writes a complete one", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: {soft: '1', hard: 2}}
`}, {"f2.yaml", `services:
  a:
    build:
      ulimits: {nofile: {soft: 1, hard: 2}}
`}}, false},
		{"extended {nofile: {soft: '1', hard: 2}}, the extender writes a complete one", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      ulimits: {nofile: {soft: 1, hard: 2}}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: {soft: '1', hard: 2}}
`}}, false},
		{"extended {nofile: {soft: '1', hard: 2}}, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: {soft: '1', hard: 2}}
`}}, true},
		{"-f: first {nofile: 1.5}, the second writes a complete one", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: 1.5}
`}, {"f2.yaml", `services:
  a:
    build:
      ulimits: {nofile: {soft: 1, hard: 2}}
`}}, true},
		{"-f: first {nofile: 1.5}, the second writes an int", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: 1.5}
`}, {"f2.yaml", `services:
  a:
    build:
      ulimits: {nofile: 3}
`}}, true},
		{"extended {nofile: 1.5}, the extender writes a complete one", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      ulimits: {nofile: {soft: 1, hard: 2}}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: 1.5}
`}}, true},
		{"extended {nofile: 1.5}, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: 1.5}
`}}, true},
		{"-f: first nofile soft null hard 2, second nothing", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: {soft: ~, hard: 2}}
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}}, true},
		{"-f: first nofile soft null hard 2, second writes soft 1", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: {soft: ~, hard: 2}}
`}, {"f2.yaml", `services:
  a:
    build:
      ulimits: {nofile: {soft: 1}}
`}}, true},
		{"-f: first {nofile: '1'}, second writes {nofile: 3}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: '1'}
`}, {"f2.yaml", `services:
  a:
    build:
      ulimits: {nofile: 3}
`}}, true},
		{"extended {nofile: '1'}, the extender writes {nofile: 3}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      ulimits: {nofile: 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: '1'}
`}}, true},
		{"sibling {nofile: '1'}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    build:
      context: .
      ulimits: {nofile: '1'}
`}}, true},
		{"-f: first {nofile: 1.5}, second writes {nofile: 3}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: 1.5}
`}, {"f2.yaml", `services:
  a:
    build:
      ulimits: {nofile: 3}
`}}, true},
		{"extended {nofile: 1.5}, the extender writes {nofile: 3}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      ulimits: {nofile: 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: 1.5}
`}}, true},
		{"sibling {nofile: 1.5}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    build:
      context: .
      ulimits: {nofile: 1.5}
`}}, true},
		{"-f: first {nofile: 1e3}, second writes {nofile: 3}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: 1e3}
`}, {"f2.yaml", `services:
  a:
    build:
      ulimits: {nofile: 3}
`}}, true},
		{"extended {nofile: 1e3}, the extender writes {nofile: 3}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      ulimits: {nofile: 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: 1e3}
`}}, true},
		{"sibling {nofile: 1e3}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    build:
      context: .
      ulimits: {nofile: 1e3}
`}}, true},
		{"-f: first {a: b}, second writes {nofile: 3}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {a: b}
`}, {"f2.yaml", `services:
  a:
    build:
      ulimits: {nofile: 3}
`}}, true},
		{"extended {a: b}, the extender writes {nofile: 3}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      ulimits: {nofile: 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {a: b}
`}}, true},
		{"sibling {a: b}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    build:
      context: .
      ulimits: {a: b}
`}}, true},
		{"-f: first {nofile: true}, second writes {nofile: 3}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: true}
`}, {"f2.yaml", `services:
  a:
    build:
      ulimits: {nofile: 3}
`}}, true},
		{"extended {nofile: true}, the extender writes {nofile: 3}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      ulimits: {nofile: 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: true}
`}}, true},
		{"sibling {nofile: true}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    build:
      context: .
      ulimits: {nofile: true}
`}}, true},
		{"sibling {nofile: {soft: 1, hard: 2, extra: 3}}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    build:
      context: .
      ulimits: {nofile: {soft: 1, hard: 2, extra: 3}}
`}}, false},
		{"sibling {nofile: {soft: 1, hard: 2, soft2: 3}}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    build:
      context: .
      ulimits: {nofile: {soft: 1, hard: 2, soft2: 3}}
`}}, false},
		{"sibling {nofile: {a: 1}}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    build:
      context: .
      ulimits: {nofile: {a: 1}}
`}}, false},
		{"-f: first extra_hosts [x], the second writes build: !reset null", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: [x]
`}, {"f2.yaml", `services:
  a:
    build: !reset null
`}}, false},
		{"extended extra_hosts [x], the extender writes build: !reset null", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      extra_hosts: [x]
`}}, false},
		{"-f: first extra_hosts [x], the second writes build: !override {context: .}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: [x]
`}, {"f2.yaml", `services:
  a:
    build: !override {context: .}
`}}, false},
		{"extended extra_hosts [x], the extender writes build: !override {context: .}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build: !override {context: .}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      extra_hosts: [x]
`}}, false},
		{"-f: first extra_hosts [x], the second writes extra_hosts: !reset", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: [x]
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts: !reset null
`}}, false},
		{"extended extra_hosts [x], the extender writes extra_hosts: !reset", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      extra_hosts: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      extra_hosts: [x]
`}}, false},
		{"-f: first extra_hosts [x], the second writes extra_hosts: !override good", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      extra_hosts: [x]
`}, {"f2.yaml", `services:
  a:
    build:
      extra_hosts: !override ['h=1.1.1.1']
`}}, false},
		{"extended extra_hosts [x], the extender writes extra_hosts: !override good", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      extra_hosts: !override ['h=1.1.1.1']
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      extra_hosts: [x]
`}}, false},
		{"-f: first ssh [x], the second writes build: !reset null", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: [x]
`}, {"f2.yaml", `services:
  a:
    build: !reset null
`}}, true},
		{"extended ssh [x], the extender writes build: !reset null", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ssh: [x]
`}}, true},
		{"-f: first ssh [x], the second writes build: !override {context: .}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: [x]
`}, {"f2.yaml", `services:
  a:
    build: !override {context: .}
`}}, true},
		{"extended ssh [x], the extender writes build: !override {context: .}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build: !override {context: .}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ssh: [x]
`}}, true},
		{"-f: first ssh [x], the second writes ssh: !reset", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: [x]
`}, {"f2.yaml", `services:
  a:
    build:
      ssh: !reset null
`}}, true},
		{"extended ssh [x], the extender writes ssh: !reset", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      ssh: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ssh: [x]
`}}, true},
		{"-f: first ssh [x], the second writes ssh: !override good", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: [x]
`}, {"f2.yaml", `services:
  a:
    build:
      ssh: !override [default]
`}}, true},
		{"extended ssh [x], the extender writes ssh: !override good", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      ssh: !override [default]
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ssh: [x]
`}}, true},
		{"-f: first ulimits {nofile: '1'}, the second writes build: !reset null", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: '1'}
`}, {"f2.yaml", `services:
  a:
    build: !reset null
`}}, true},
		{"extended ulimits {nofile: '1'}, the extender writes build: !reset null", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: '1'}
`}}, true},
		{"-f: first ulimits {nofile: '1'}, the second writes build: !override {context: .}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: '1'}
`}, {"f2.yaml", `services:
  a:
    build: !override {context: .}
`}}, true},
		{"extended ulimits {nofile: '1'}, the extender writes build: !override {context: .}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build: !override {context: .}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: '1'}
`}}, true},
		{"-f: first ulimits {nofile: '1'}, the second writes ulimits: !reset", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: '1'}
`}, {"f2.yaml", `services:
  a:
    build:
      ulimits: !reset null
`}}, true},
		{"extended ulimits {nofile: '1'}, the extender writes ulimits: !reset", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      ulimits: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: '1'}
`}}, true},
		{"-f: first ulimits {nofile: '1'}, the second writes ulimits: !override good", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: '1'}
`}, {"f2.yaml", `services:
  a:
    build:
      ulimits: !override {nofile: 3}
`}}, true},
		{"extended ulimits {nofile: '1'}, the extender writes ulimits: !override good", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      ulimits: !override {nofile: 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: '1'}
`}}, true},
		{"extended ulimits {nofile: {soft: 1, hard: 2, z: 3}}, the extender writes build: !reset null", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: {soft: 1, hard: 2, z: 3}}
`}}, false},
		{"extended ulimits {nofile: {soft: 1, hard: 2, z: 3}}, the extender writes build: !override {context: .}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build: !override {context: .}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: {soft: 1, hard: 2, z: 3}}
`}}, false},
		{"extended ulimits {nofile: {soft: 1, hard: 2, z: 3}}, the extender writes ulimits: !reset", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      ulimits: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: {soft: 1, hard: 2, z: 3}}
`}}, false},
		{"extended ulimits {nofile: {soft: 1, hard: 2, z: 3}}, the extender writes ulimits: !override good", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      ulimits: !override {nofile: 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {nofile: {soft: 1, hard: 2, z: 3}}
`}}, false},
		{"-f: first ulimits {a: b}, the second writes build: !reset null", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {a: b}
`}, {"f2.yaml", `services:
  a:
    build: !reset null
`}}, true},
		{"extended ulimits {a: b}, the extender writes build: !reset null", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {a: b}
`}}, true},
		{"-f: first ulimits {a: b}, the second writes build: !override {context: .}", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {a: b}
`}, {"f2.yaml", `services:
  a:
    build: !override {context: .}
`}}, true},
		{"extended ulimits {a: b}, the extender writes build: !override {context: .}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build: !override {context: .}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {a: b}
`}}, true},
		{"-f: first ulimits {a: b}, the second writes ulimits: !reset", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {a: b}
`}, {"f2.yaml", `services:
  a:
    build:
      ulimits: !reset null
`}}, true},
		{"extended ulimits {a: b}, the extender writes ulimits: !reset", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      ulimits: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {a: b}
`}}, true},
		{"-f: first ulimits {a: b}, the second writes ulimits: !override good", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {a: b}
`}, {"f2.yaml", `services:
  a:
    build:
      ulimits: !override {nofile: 3}
`}}, true},
		{"extended ulimits {a: b}, the extender writes ulimits: !override good", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build:
      ulimits: !override {nofile: 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    build:
      context: .
      ulimits: {a: b}
`}}, true},
		{"build.secrets [zz] undeclared, in a profile-gated service", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    profiles: [dbg]
    build:
      context: .
      secrets: [zz]
`}}, false},
		{"build.secrets [{source: zz}] undeclared, in a profile-gated service", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    profiles: [dbg]
    build:
      context: .
      secrets: [{source: zz}]
`}}, false},
		{"build.secrets mode 1.5, in a profile-gated service", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    profiles: [dbg]
    build:
      context: .
      secrets: [{source: k, mode: 1.5}]
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"build.secrets [zz] undeclared, with the profile on", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    profiles: [dbg]
    build:
      context: .
      secrets: [zz]
`}}, false},
		{"build.secrets mode 1.5", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [{source: k, mode: 1.5}]
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"build.secrets mode 99999999999999999999", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [{source: k, mode: 99999999999999999999}]
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"build.secrets mode 2020-01-01", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [{source: k, mode: 2020-01-01}]
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"build.secrets mode '!!float 1'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [{source: k, mode: '!!float 1'}]
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"build.secrets mode !!float 1", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [{source: k, mode: !!float 1}]
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"build.secrets mode x", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [{source: k, mode: x}]
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"build.secrets mode 0400", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [{source: k, mode: 0400}]
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"build.secrets mode '0400'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      secrets: [{source: k, mode: '0400'}]
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"two services: the second has a bad no_cache", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build: {context: ., no_cache: true}
  b:
    image: y
    build: {context: ., no_cache: x}
`}}, true},
		{"two services: the second has a bad extra_hosts", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build: {context: ., extra_hosts: [h=1.1.1.1]}
  b:
    image: y
    build: {context: ., extra_hosts: [x]}
`}}, true},
		{"two services: the second names an undeclared secret", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build: {context: ., secrets: [k]}
  b:
    image: y
    build: {context: ., secrets: [zz]}
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"two limits: the second is bad", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: 3, nproc: {soft: 1}}
`}}, true},
		{"two limits: the second is a word", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: 3, nproc: x}
`}}, true},
		{"a gated service with a bad extra_hosts", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    profiles: [g]
    build: {context: ., extra_hosts: [x]}
`}}, true},
		{"a gated service with an undeclared secret", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    profiles: [g]
    build: {context: ., secrets: [zz]}
`}}, false},
		{"a gated service with a bad no_cache", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    profiles: [g]
    build: {context: ., no_cache: x}
`}}, true},
		{"two limits: the second is a word, a later file writes both again", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: 3, nproc: x}
`}, {"f2.yaml", `services:
  a:
    build:
      ulimits: {nofile: 3, nproc: 4}
`}}, true},
		{"two limits: the second is a fraction, a later file writes both again", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ulimits: {nofile: 3, nproc: 1.5}
`}, {"f2.yaml", `services:
  a:
    build:
      ulimits: {nofile: 3, nproc: 4}
`}}, true},
		{"ssh: the second entry is a bare word, a later file writes it again", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    build:
      context: .
      ssh: [default, x]
`}, {"f2.yaml", `services:
  a:
    build:
      ssh: !override [default]
`}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "k.txt"), []byte("s"), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, f := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var paths []string
			for _, name := range tc.order {
				paths = append(paths, filepath.Join(dir, name))
			}
			if _, err := LoadFiles(paths, nil); (err != nil) != tc.refused {
				t.Errorf("the load: err %v, docker compose refuses it: %v", err, tc.refused)
			}
		})
	}
}

// The read that takes a project down goes on past the refusals of a build's casts and forms, keeping the first as a value it went past.
func TestTheCastsOfABuildDoNotStopATakeDown(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(p, []byte("services:\n  a:\n    image: x\n    build: {context: ., no_cache: x}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("the strict load read a no_cache that is no boolean")
	}
	proj, err := LoadFilesEnvDirSoft([]string{p}, nil, "")
	if err != nil {
		t.Fatalf("the soft load stopped: %v", err)
	}
	if proj.CheckValueFaults() == nil {
		t.Errorf("the soft load kept no refusal")
	}
}
