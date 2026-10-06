package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// The type of a value of an extended service is asked of the service that results from the extends, as docker compose asks it (`config
// -q`, every row measured, v5.5.1: `refused` is its rc 1): a `command`, `entrypoint`, `environment`, `labels`, `extra_hosts`,
// `sysctls`, `healthcheck` (but `retries`, and `disable` written as a word), the list form of `environment`, `labels` and `sysctls` is asked of the file, the fields of a long `ports` entry (but `target`) and a scalar `stop_grace_period` or
// `shm_size` of the wrong type in the extended file are not refused where the extender writes over them or resets them, in the extended
// service or in a sibling nothing extends, and are where they stay (#1771). What docker compose casts as it reads each file — a `cpus`, a
// count, a boolean, `ports` as a whole, `build`, `depends_on`, `env_file` — is refused in the file that writes it, written over or not.
// The forms outside these keys (a `deploy` mapping, `volumes`, `networks` and `cap_add` of the wrong kind) are not here: they are a known
// difference.
func TestTheTypeOfAnExtendedServiceIsAskedOfTheServiceTheExtendsResultsIn(t *testing.T) {
	type file struct{ name, body string }
	for _, tc := range []struct {
		name    string
		order   []string // the files given with `-f`, in order
		files   []file   // every file written, an extended one too
		refused bool
	}{
		{"ports long published [1]: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 80, published: [1]}
`}}, false},
		{"ports long published [1]: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 80, published: [1]}
`}}, true},
		{"ports long published [1]: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !reset []
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 80, published: [1]}
`}}, false},
		{"ports long mode 1: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 80, mode: 1}
`}}, false},
		{"ports long mode 1: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 80, mode: 1}
`}}, true},
		{"ports long mode 1: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !reset []
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 80, mode: 1}
`}}, false},
		{"healthcheck.interval abc: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    healthcheck: {interval: 5s}
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], interval: abc}
`}}, false},
		{"healthcheck.interval abc: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], interval: abc}
`}}, true},
		{"healthcheck.interval abc: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    healthcheck: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], interval: abc}
`}}, false},
		{"deploy abc: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    deploy: !override {replicas: 1}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: abc
`}}, false},
		{"deploy abc: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: abc
`}}, true},
		{"deploy abc: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    deploy: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: abc
`}}, false},
		{"command 5: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    command: ['x']
`}, {"base.yaml", `services:
  bs:
    image: y
    command: 5
`}}, false},
		{"command 5: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    command: 5
`}}, true},
		{"command 5: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    command: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    command: 5
`}}, false},
		{"extra_hosts {h: 1}: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    extra_hosts: !override {h: 1.2.3.4}
`}, {"base.yaml", `services:
  bs:
    image: y
    extra_hosts: {h: 1}
`}}, false},
		{"extra_hosts {h: 1}: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    extra_hosts: {h: 1}
`}}, true},
		{"extra_hosts {h: 1}: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    extra_hosts: !reset {}
`}, {"base.yaml", `services:
  bs:
    image: y
    extra_hosts: {h: 1}
`}}, false},
		{"stop_grace_period 5: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    stop_grace_period: 5s
`}, {"base.yaml", `services:
  bs:
    image: y
    stop_grace_period: 5
`}}, false},
		{"stop_grace_period 5: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    stop_grace_period: 5
`}}, true},
		{"stop_grace_period 5: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    stop_grace_period: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    stop_grace_period: 5
`}}, false},
		{"stop_grace_period abc: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    stop_grace_period: 5s
`}, {"base.yaml", `services:
  bs:
    image: y
    stop_grace_period: abc
`}}, false},
		{"stop_grace_period abc: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    stop_grace_period: abc
`}}, true},
		{"stop_grace_period abc: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    stop_grace_period: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    stop_grace_period: abc
`}}, false},
		{"environment {A: [1]}: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    environment: !override {A: '1'}
`}, {"base.yaml", `services:
  bs:
    image: y
    environment: {A: [1]}
`}}, false},
		{"environment {A: [1]}: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    environment: {A: [1]}
`}}, true},
		{"environment {A: [1]}: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    environment: !reset {}
`}, {"base.yaml", `services:
  bs:
    image: y
    environment: {A: [1]}
`}}, false},
		{"labels {a: [1]}: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    labels: !override {a: '1'}
`}, {"base.yaml", `services:
  bs:
    image: y
    labels: {a: [1]}
`}}, false},
		{"labels {a: [1]}: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    labels: {a: [1]}
`}}, true},
		{"labels {a: [1]}: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    labels: !reset {}
`}, {"base.yaml", `services:
  bs:
    image: y
    labels: {a: [1]}
`}}, false},
		{"shm_size abc: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    shm_size: 64m
`}, {"base.yaml", `services:
  bs:
    image: y
    shm_size: abc
`}}, false},
		{"shm_size abc: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    shm_size: abc
`}}, true},
		{"shm_size abc: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    shm_size: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    shm_size: abc
`}}, false},
		{"oom_score_adj 2000: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    oom_score_adj: 5
`}, {"base.yaml", `services:
  bs:
    image: y
    oom_score_adj: 2000
`}}, false},
		{"oom_score_adj 2000: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    oom_score_adj: 2000
`}}, true},
		{"oom_score_adj 2000: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    oom_score_adj: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    oom_score_adj: 2000
`}}, false},
		{"sysctls {'': 1}: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    sysctls: !override {a: 1}
`}, {"base.yaml", `services:
  bs:
    image: y
    sysctls: {'': 1}
`}}, false},
		{"sysctls {'': 1}: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    sysctls: {'': 1}
`}}, true},
		{"sysctls {'': 1}: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    sysctls: !reset {}
`}, {"base.yaml", `services:
  bs:
    image: y
    sysctls: {'': 1}
`}}, false},
		{"command .inf: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    command: ['x']
`}, {"base.yaml", `services:
  bs:
    image: y
    command: .inf
`}}, false},
		{"command .inf: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    command: .inf
`}}, true},
		{"command .inf: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    command: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    command: .inf
`}}, false},
		{"ports long published .inf: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 80, published: .inf}
`}}, false},
		{"ports long published .inf: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 80, published: .inf}
`}}, true},
		{"ports long published .inf: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !reset []
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 80, published: .inf}
`}}, false},
		{"extra_hosts .inf: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    extra_hosts: !override {h: 1.2.3.4}
`}, {"base.yaml", `services:
  bs:
    image: y
    extra_hosts: {h: .inf}
`}}, false},
		{"extra_hosts .inf: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    extra_hosts: {h: .inf}
`}}, true},
		{"extra_hosts .inf: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    extra_hosts: !reset {}
`}, {"base.yaml", `services:
  bs:
    image: y
    extra_hosts: {h: .inf}
`}}, false},
		{"stop_grace_period .inf: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    stop_grace_period: 5s
`}, {"base.yaml", `services:
  bs:
    image: y
    stop_grace_period: .inf
`}}, false},
		{"stop_grace_period .inf: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    stop_grace_period: .inf
`}}, true},
		{"stop_grace_period .inf: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    stop_grace_period: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    stop_grace_period: .inf
`}}, false},
		{"labels .inf: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    labels: !override {a: '1'}
`}, {"base.yaml", `services:
  bs:
    image: y
    labels: {a: .inf}
`}}, false},
		{"labels .inf: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    labels: {a: .inf}
`}}, true},
		{"labels .inf: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    labels: !reset {}
`}, {"base.yaml", `services:
  bs:
    image: y
    labels: {a: .inf}
`}}, false},
		{"cpus abc: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    cpus: 1
`}, {"base.yaml", `services:
  bs:
    image: y
    cpus: abc
`}}, true},
		{"cpus abc: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    cpus: abc
`}}, true},
		{"cpus abc: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    cpus: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    cpus: abc
`}}, true},
		{"cpu_percent abc: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    cpu_percent: 50
`}, {"base.yaml", `services:
  bs:
    image: y
    cpu_percent: abc
`}}, true},
		{"cpu_percent abc: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    cpu_percent: abc
`}}, true},
		{"cpu_percent abc: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    cpu_percent: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    cpu_percent: abc
`}}, true},
		{"scale abc: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    scale: 1
`}, {"base.yaml", `services:
  bs:
    image: y
    scale: abc
`}}, true},
		{"scale abc: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    scale: abc
`}}, true},
		{"scale abc: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    scale: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    scale: abc
`}}, true},
		{"privileged abc: overwritten by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    privileged: true
`}, {"base.yaml", `services:
  bs:
    image: y
    privileged: abc
`}}, true},
		{"privileged abc: not touched by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    privileged: abc
`}}, true},
		{"privileged abc: !reset by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    privileged: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    privileged: abc
`}}, true},
		{"ports long published [1]: in a sibling of the extended service, which nothing extends", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {target: 80, published: [1]}
`}}, false},
		{"ports long published [1]: in the extended service, the extender writes it over (the same file)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 80, published: [1]}
  web:
    extends: bs
    ports:
      - {target: 80, published: [1]}
`}}, true},
		{"ports long mode 1: in a sibling of the extended service, which nothing extends", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {target: 80, mode: 1}
`}}, false},
		{"ports long mode 1: in the extended service, the extender writes it over (the same file)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 80, mode: 1}
  web:
    extends: bs
    ports:
      - {target: 80, mode: 1}
`}}, true},
		{"healthcheck.interval abc: in a sibling of the extended service, which nothing extends", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    healthcheck: {test: [CMD, 'true'], interval: abc}
`}}, false},
		{"healthcheck.interval abc: in the extended service, the extender writes it over (the same file)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], interval: abc}
  web:
    extends: bs
    healthcheck: {test: [CMD, 'true'], interval: abc}
`}}, true},
		{"deploy abc: in a sibling of the extended service, which nothing extends", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    deploy: abc
`}}, false},
		{"deploy abc: in the extended service, the extender writes it over (the same file)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    deploy: abc
  web:
    extends: bs
    deploy: abc
`}}, true},
		{"command 5: in a sibling of the extended service, which nothing extends", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    command: 5
`}}, false},
		{"command 5: in the extended service, the extender writes it over (the same file)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    command: 5
  web:
    extends: bs
    command: 5
`}}, true},
		{"extra_hosts {h: 1}: in a sibling of the extended service, which nothing extends", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    extra_hosts: {h: 1}
`}}, false},
		{"extra_hosts {h: 1}: in the extended service, the extender writes it over (the same file)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    extra_hosts: {h: 1}
  web:
    extends: bs
    extra_hosts: {h: 1}
`}}, true},
		{"stop_grace_period 5: in a sibling of the extended service, which nothing extends", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    stop_grace_period: 5
`}}, false},
		{"stop_grace_period 5: in the extended service, the extender writes it over (the same file)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    stop_grace_period: 5
  web:
    extends: bs
    stop_grace_period: 5
`}}, true},
		{"stop_grace_period abc: in a sibling of the extended service, which nothing extends", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    stop_grace_period: abc
`}}, false},
		{"stop_grace_period abc: in the extended service, the extender writes it over (the same file)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    stop_grace_period: abc
  web:
    extends: bs
    stop_grace_period: abc
`}}, true},
		{"environment {A: [1]}: in a sibling of the extended service, which nothing extends", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    environment: {A: [1]}
`}}, false},
		{"environment {A: [1]}: in the extended service, the extender writes it over (the same file)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    environment: {A: [1]}
  web:
    extends: bs
    environment: {A: [1]}
`}}, true},
		{"labels {a: [1]}: in a sibling of the extended service, which nothing extends", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    labels: {a: [1]}
`}}, false},
		{"labels {a: [1]}: in the extended service, the extender writes it over (the same file)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    labels: {a: [1]}
  web:
    extends: bs
    labels: {a: [1]}
`}}, true},
		{"shm_size abc: in a sibling of the extended service, which nothing extends", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    shm_size: abc
`}}, false},
		{"shm_size abc: in the extended service, the extender writes it over (the same file)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    shm_size: abc
  web:
    extends: bs
    shm_size: abc
`}}, true},
		{"sysctls {'': 1}: in a sibling of the extended service, which nothing extends", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    sysctls: {'': 1}
`}}, false},
		{"sysctls {'': 1}: in the extended service, the extender writes it over (the same file)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    sysctls: {'': 1}
  web:
    extends: bs
    sysctls: {'': 1}
`}}, true},
		{"command .inf: in a sibling of the extended service, which nothing extends", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    command: .inf
`}}, false},
		{"command .inf: in the extended service, the extender writes it over (the same file)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    command: .inf
  web:
    extends: bs
    command: .inf
`}}, true},
		{"cpus abc: in a sibling of the extended service, which nothing extends", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    cpus: abc
`}}, true},
		{"cpus abc: in the extended service, the extender writes it over (the same file)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    cpus: abc
  web:
    extends: bs
    cpus: abc
`}}, true},
		{"ports long published .inf: in a sibling of the extended service, which nothing extends", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {target: 80, published: .inf}
`}}, false},
		{"ports long published .inf: in the extended service, the extender writes it over (the same file)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 80, published: .inf}
  web:
    extends: bs
    ports:
      - {target: 80, published: .inf}
`}}, true},
		{"extra_hosts .inf: in a sibling of the extended service, which nothing extends", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    extra_hosts: {h: .inf}
`}}, false},
		{"extra_hosts .inf: in the extended service, the extender writes it over (the same file)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    extra_hosts: {h: .inf}
  web:
    extends: bs
    extra_hosts: {h: .inf}
`}}, true},
		{"stop_grace_period .inf: in a sibling of the extended service, which nothing extends", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    stop_grace_period: .inf
`}}, false},
		{"stop_grace_period .inf: in the extended service, the extender writes it over (the same file)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    stop_grace_period: .inf
  web:
    extends: bs
    stop_grace_period: .inf
`}}, true},
		{"labels .inf: in a sibling of the extended service, which nothing extends", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    labels: {a: .inf}
`}}, false},
		{"labels .inf: in the extended service, the extender writes it over (the same file)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    labels: {a: .inf}
  web:
    extends: bs
    labels: {a: .inf}
`}}, true},
		{"-f: an earlier file writes cpus: abc, a later file writes 1", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    cpus: abc
`}, {"f2.yaml", `services:
  a:
    cpus: 1
`}}, true},
		{"-f: an earlier file writes cpus: abc, the later does not", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    cpus: abc
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}}, true},
		{"-f: an earlier file writes cpu_percent: abc, a later file writes 50", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    cpu_percent: abc
`}, {"f2.yaml", `services:
  a:
    cpu_percent: 50
`}}, true},
		{"-f: an earlier file writes cpu_percent: abc, the later does not", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    cpu_percent: abc
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}}, true},
		{"-f: an earlier file writes scale: abc, a later file writes 1", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    scale: abc
`}, {"f2.yaml", `services:
  a:
    scale: 1
`}}, true},
		{"-f: an earlier file writes scale: abc, the later does not", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    scale: abc
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}}, true},
		{"-f: an earlier file writes cpu_count: abc, a later file writes 2", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    cpu_count: abc
`}, {"f2.yaml", `services:
  a:
    cpu_count: 2
`}}, true},
		{"-f: an earlier file writes cpu_count: abc, the later does not", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    cpu_count: abc
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}}, true},
		{"-f: an earlier file writes privileged: abc, a later file writes true", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    privileged: abc
`}, {"f2.yaml", `services:
  a:
    privileged: true
`}}, true},
		{"-f: an earlier file writes privileged: abc, the later does not", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    privileged: abc
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}}, true},
		{"-f: an earlier file writes cpus: \"1_0\", a later file writes 1", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    cpus: "1_0"
`}, {"f2.yaml", `services:
  a:
    cpus: 1
`}}, false},
		{"-f: an earlier file writes cpus: \"1_0\", the later does not", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    cpus: "1_0"
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}}, false},
		{"-f: an earlier file writes cpus: '0x2', a later file writes 1", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    cpus: '0x2'
`}, {"f2.yaml", `services:
  a:
    cpus: 1
`}}, true},
		{"-f: an earlier file writes cpus: '0x2', the later does not", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    cpus: '0x2'
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}}, true},
		{"-f: a later file writes cpus: abc over a good one", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    cpus: 1
`}, {"f2.yaml", `services:
  a:
    cpus: abc
`}}, true},
		{"a stop_grace_period as a list, over an earlier value, -f", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    stop_grace_period: 5s
`}, {"f2.yaml", `services:
  a:
    stop_grace_period: [1]
`}}, true},
		{"command mapping: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    command: ['x']
`}, {"base.yaml", `services:
  bs:
    image: y
    command: {a: 1}
`}}, false},
		{"command mapping: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    command: {a: 1}
`}}, false},
		{"command mapping: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    command: {a: 1}
`}}, true},
		{"entrypoint 5: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    entrypoint: ['x']
`}, {"base.yaml", `services:
  bs:
    image: y
    entrypoint: 5
`}}, false},
		{"entrypoint 5: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    entrypoint: 5
`}}, false},
		{"entrypoint 5: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    entrypoint: 5
`}}, true},
		{"environment 5: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    environment: {A: '1'}
`}, {"base.yaml", `services:
  bs:
    image: y
    environment: 5
`}}, false},
		{"environment 5: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    environment: 5
`}}, false},
		{"environment 5: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    environment: 5
`}}, true},
		{"labels 5: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    labels: {a: '1'}
`}, {"base.yaml", `services:
  bs:
    image: y
    labels: 5
`}}, false},
		{"labels 5: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    labels: 5
`}}, false},
		{"labels 5: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    labels: 5
`}}, true},
		{"extra_hosts 5: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    extra_hosts: ['h=1.2.3.4']
`}, {"base.yaml", `services:
  bs:
    image: y
    extra_hosts: 5
`}}, false},
		{"extra_hosts 5: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    extra_hosts: 5
`}}, false},
		{"extra_hosts 5: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    extra_hosts: 5
`}}, true},
		{"stop_grace_period list: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    stop_grace_period: 5s
`}, {"base.yaml", `services:
  bs:
    image: y
    stop_grace_period: [1]
`}}, true},
		{"stop_grace_period list: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    stop_grace_period: [1]
`}}, false},
		{"stop_grace_period list: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    stop_grace_period: [1]
`}}, true},
		{"shm_size list: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    shm_size: 64m
`}, {"base.yaml", `services:
  bs:
    image: y
    shm_size: [1]
`}}, true},
		{"shm_size list: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    shm_size: [1]
`}}, false},
		{"shm_size list: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    shm_size: [1]
`}}, true},
		{"sysctls 5: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    sysctls: {a: 1}
`}, {"base.yaml", `services:
  bs:
    image: y
    sysctls: 5
`}}, false},
		{"sysctls 5: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    sysctls: 5
`}}, false},
		{"sysctls 5: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    sysctls: 5
`}}, true},
		{"healthcheck 5: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    healthcheck: {test: [CMD, 'true']}
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: 5
`}}, false},
		{"healthcheck 5: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    healthcheck: 5
`}}, false},
		{"healthcheck 5: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: 5
`}}, true},
		{"healthcheck test mapping: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    healthcheck: {test: [CMD, 'true']}
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: {a: 1}}
`}}, false},
		{"healthcheck test mapping: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    healthcheck: {test: {a: 1}}
`}}, false},
		{"healthcheck test mapping: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: {a: 1}}
`}}, true},
		{"healthcheck retries abc: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    healthcheck: {retries: 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: abc}
`}}, true},
		{"healthcheck retries abc: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    healthcheck: {test: [CMD, 'true'], retries: abc}
`}}, true},
		{"healthcheck retries abc: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {test: [CMD, 'true'], retries: abc}
`}}, true},
		{"ports scalar: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports: 5
`}}, true},
		{"ports scalar: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports: 5
`}}, true},
		{"ports scalar: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    ports: 5
`}}, true},
		{"ports entry float: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports: [5.5]
`}}, true},
		{"ports entry float: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports: [5.5]
`}}, true},
		{"ports entry float: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    ports: [5.5]
`}}, true},
		{"ports long target abc: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: abc}
`}}, true},
		{"ports long target abc: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {target: abc}
`}}, true},
		{"ports long target abc: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: abc}
`}}, true},
		{"ports long protocol 1: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 80, protocol: 1}
`}}, false},
		{"ports long protocol 1: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {target: 80, protocol: 1}
`}}, false},
		{"ports long protocol 1: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 80, protocol: 1}
`}}, true},
		{"ports long host_ip 1: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 80, host_ip: 1}
`}}, false},
		{"ports long host_ip 1: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {target: 80, host_ip: 1}
`}}, false},
		{"ports long host_ip 1: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 80, host_ip: 1}
`}}, true},
		{"build scalar 5: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    build: !override .
`}, {"base.yaml", `services:
  bs:
    image: y
    build: 5
`}}, true},
		{"build scalar 5: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    build: 5
`}}, true},
		{"build scalar 5: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    build: 5
`}}, true},
		{"depends_on scalar: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    depends_on: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    depends_on: 5
`}}, true},
		{"depends_on scalar: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    depends_on: 5
`}}, true},
		{"depends_on scalar: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    depends_on: 5
`}}, true},
		{"env_file 5: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    env_file: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    env_file: 5
`}}, true},
		{"env_file 5: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    env_file: 5
`}}, true},
		{"env_file 5: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    env_file: 5
`}}, true},
		{"user list: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    user: '1'
`}, {"base.yaml", `services:
  bs:
    image: y
    user: [1]
`}}, true},
		{"user list: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    user: [1]
`}}, false},
		{"user list: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    user: [1]
`}}, true},
		{"mem_limit abc: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    mem_limit: 1g
`}, {"base.yaml", `services:
  bs:
    image: y
    mem_limit: abc
`}}, false},
		{"mem_limit abc: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    mem_limit: abc
`}}, false},
		{"mem_limit abc: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    mem_limit: abc
`}}, true},
		{"restart 5: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    restart: always
`}, {"base.yaml", `services:
  bs:
    image: y
    restart: 5
`}}, false},
		{"restart 5: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    restart: 5
`}}, false},
		{"restart 5: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    restart: 5
`}}, true},
		{"dns 5.5: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    dns: ['1.1.1.1']
`}, {"base.yaml", `services:
  bs:
    image: y
    dns: [5.5]
`}}, true},
		{"dns 5.5: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    dns: [5.5]
`}}, true},
		{"dns 5.5: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    dns: [5.5]
`}}, true},
		{"read_only abc: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    read_only: true
`}, {"base.yaml", `services:
  bs:
    image: y
    read_only: abc
`}}, true},
		{"read_only abc: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    read_only: abc
`}}, true},
		{"read_only abc: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    read_only: abc
`}}, true},
		{"init abc: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    init: true
`}, {"base.yaml", `services:
  bs:
    image: y
    init: abc
`}}, true},
		{"init abc: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    init: abc
`}}, true},
		{"init abc: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    init: abc
`}}, true},
		{"tty abc: overwritten by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    tty: true
`}, {"base.yaml", `services:
  bs:
    image: y
    tty: abc
`}}, true},
		{"tty abc: in a sibling of the extended service (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    tty: abc
`}}, true},
		{"tty abc: kept by the extender (the forms probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    tty: abc
`}}, true},
		{"environment: [1], written over (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    environment: !override {A: '1'}
`}, {"base.yaml", `services:
  bs:
    image: y
    environment: [1]
`}}, true},
		{"environment: [1], reset (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    environment: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    environment: [1]
`}}, true},
		{"environment: [1], in a sibling (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    environment: [1]
`}}, true},
		{"environment: [true], written over (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    environment: !override ['A=1']
`}, {"base.yaml", `services:
  bs:
    image: y
    environment: [true]
`}}, true},
		{"environment: [true], reset (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    environment: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    environment: [true]
`}}, true},
		{"environment: [true], in a sibling (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    environment: [true]
`}}, true},
		{"environment: [A=1, 5], written over (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    environment: !override {A: '1'}
`}, {"base.yaml", `services:
  bs:
    image: y
    environment: [A=1, 5]
`}}, true},
		{"environment: [A=1, 5], reset (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    environment: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    environment: [A=1, 5]
`}}, true},
		{"environment: [A=1, 5], in a sibling (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    environment: [A=1, 5]
`}}, true},
		{"labels: [1], written over (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    labels: !override {a: '1'}
`}, {"base.yaml", `services:
  bs:
    image: y
    labels: [1]
`}}, true},
		{"labels: [1], reset (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    labels: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    labels: [1]
`}}, true},
		{"labels: [1], in a sibling (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    labels: [1]
`}}, true},
		{"labels: [[a]], written over (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    labels: !override {a: '1'}
`}, {"base.yaml", `services:
  bs:
    image: y
    labels: [[a]]
`}}, true},
		{"labels: [[a]], reset (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    labels: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    labels: [[a]]
`}}, true},
		{"labels: [[a]], in a sibling (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    labels: [[a]]
`}}, true},
		{"sysctls: [1], written over (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    sysctls: !override {a: 1}
`}, {"base.yaml", `services:
  bs:
    image: y
    sysctls: [1]
`}}, true},
		{"sysctls: [1], reset (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    sysctls: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    sysctls: [1]
`}}, true},
		{"sysctls: [1], in a sibling (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    sysctls: [1]
`}}, true},
		{"sysctls: [[1]], written over (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    sysctls: !override {a: 1}
`}, {"base.yaml", `services:
  bs:
    image: y
    sysctls: [[1]]
`}}, true},
		{"sysctls: [[1]], reset (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    sysctls: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    sysctls: [[1]]
`}}, true},
		{"sysctls: [[1]], in a sibling (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    sysctls: [[1]]
`}}, true},
		{"healthcheck.disable abc, written over by {disable: false} (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    healthcheck: {disable: false}
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {disable: abc}
`}}, true},
		{"healthcheck.disable abc, reset (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    healthcheck: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {disable: abc}
`}}, true},
		{"healthcheck.disable abc, in a sibling (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    healthcheck: {disable: abc}
`}}, true},
		{"healthcheck.disable 5, written over (docker reads it) (the list forms and disable probe)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    healthcheck: {disable: false}
`}, {"base.yaml", `services:
  bs:
    image: y
    healthcheck: {disable: 5}
`}}, false},
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
			// The read that takes a project down goes on past the files docker compose reads, and keeps a refusal of a value as one it
			// went past; a value the decode cannot read (the type of a key that stays) stops it, as it always has.
			p, err := LoadFilesEnvDirSoft(paths, nil, "")
			switch {
			case err != nil && !tc.refused:
				t.Errorf("the soft load stopped on a file docker compose reads: %v", err)
			case err == nil && (p.CheckValueFaults() != nil) != tc.refused:
				t.Errorf("the soft load kept the refusal: %v, docker compose refuses it: %v", p.CheckValueFaults(), tc.refused)
			}
		})
	}
}
