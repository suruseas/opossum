package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The `target` of a secret is read as docker compose reads it (`config -q`, every row measured, v5.5.1: `refused` is its rc 1): a name, a path
// of names under /run/secrets, or an absolute path (the default `/run/secrets/k` written out is the same place as the bare name), and a `mode`
// written `-0`, which docker compose reads as a float, is refused only where it stays once the files are merged: a later file, or an extending
// service, that writes the mode over it makes the file fine, and so does a service nothing takes (#1778). The targets that are no file's path
// (`..`, `/`, a trailing `/`, `//`) are not here: docker compose lets them through, its engine cannot mount them, and they are refused here
// (a known difference).
func TestTheTargetAndModeOfASecretAreReadAsDockerComposeReadsThem(t *testing.T) {
	type file struct{ name, body string }
	for _, tc := range []struct {
		name    string
		order   []string // the files given with `-f`, in order
		files   []file   // every file written, an extended one too
		refused bool
	}{
		{"secrets target k", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, target: 'k'}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets target /run/secrets/k", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, target: '/run/secrets/k'}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets target /k", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, target: '/k'}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets target a/b", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, target: 'a/b'}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets target /a/b", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, target: '/a/b'}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets target .", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, target: '.'}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets target ''", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, target: ''}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets target 'k '", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, target: 'k '}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets target a b", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, target: 'a b'}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets mode 0440", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: 0440}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets mode 0o440", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: 0o440}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets mode 440", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: 440}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets mode -0", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: -0}
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"secrets mode 0", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: 0}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets mode -1", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: -1}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets mode 0x1ff", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: 0x1ff}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets mode 0o777", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: 0o777}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets mode 0o1000", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: 0o1000}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets mode 511", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: 511}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets mode 512", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: 512}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets mode '0440'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: '0440'}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets mode '-0'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: '-0'}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets mode '0o440'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: '0o440'}
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"configs target k", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, target: 'k'}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs target /run/secrets/k", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, target: '/run/secrets/k'}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs target /k", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, target: '/k'}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs target /", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, target: '/'}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs target a/b", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, target: 'a/b'}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs target /a/b", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, target: '/a/b'}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs target .", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, target: '.'}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs target a/", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, target: 'a/'}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs target ./k", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, target: './k'}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs target /run/secrets/", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, target: '/run/secrets/'}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs target k/", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, target: 'k/'}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs target //k", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, target: '//k'}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs target /k/", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, target: '/k/'}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs target ''", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, target: ''}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs target 'k '", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, target: 'k '}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs target a b", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, target: 'a b'}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs mode 0440", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: 0440}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs mode 0o440", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: 0o440}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs mode 440", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: 440}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs mode -0", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: -0}
configs:
  k: {file: ./k.txt}
`}}, true},
		{"configs mode 0", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: 0}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs mode -1", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: -1}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs mode 0x1ff", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: 0x1ff}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs mode 0o777", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: 0o777}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs mode 0o1000", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: 0o1000}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs mode 511", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: 511}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs mode 512", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: 512}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs mode '0440'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: '0440'}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs mode '-0'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: '-0'}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs mode '0o440'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: '0o440'}
configs:
  k: {file: ./k.txt}
`}}, true},
		{"secrets: short k and {source k, target /run/secrets/k} (same place)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - k
      - {source: k, target: /run/secrets/k}
secrets:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}}, false},
		{"secrets: short k and {source k, target k}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - k
      - {source: k, target: k}
secrets:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}}, false},
		{"secrets: {source k, target a/b} and {source j, target /run/secrets/a/b}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, target: a/b}
      - {source: j, target: /run/secrets/a/b}
secrets:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}}, false},
		{"secrets: {source k, target /x/y} and {source j, target /x/y}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, target: /x/y}
      - {source: j, target: /x/y}
secrets:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}}, false},
		{"secrets: mode -0, one file", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: -0}
secrets:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}}, true},
		{"secrets: mode -0 in the first file, the second writes 0440", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: -0}
secrets:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}, {"f2.yaml", `services:
  a:
    secrets:
      - {source: k, mode: 0440}
`}}, false},
		{"secrets: mode -0 in the extended file, the extender writes 0440", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    extends: {file: base.yaml, service: bs}
    secrets:
      - {source: k, mode: 0440}
secrets:
  k: {file: ./k.txt}
`}, {"base.yaml", `services:
  bs:
    image: y
    secrets:
      - {source: k, mode: -0}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets: mode -0 in a sibling of the extended service", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    extends: {file: base.yaml, service: bs}
secrets:
  k: {file: ./k.txt}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    secrets:
      - {source: k, mode: -0}
secrets:
  k: {file: ./k.txt}
`}}, false},
		{"secrets: mode -0.0 and +0 and 00", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: -0.0}
secrets:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}}, true},
		{"secrets: mode +0", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: +0}
secrets:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}}, false},
		{"secrets: mode 00", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: 00}
secrets:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}}, false},
		{"secrets: mode !!int -0", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: !!int -0}
secrets:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}}, true},
		{"secrets: mode !!float 0", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: !!float 0}
secrets:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}}, true},
		{"configs: short k and {source k, target /run/secrets/k} (same place)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - k
      - {source: k, target: /k}
configs:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}}, false},
		{"configs: short k and {source k, target k}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - k
      - {source: k, target: k}
configs:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}}, false},
		{"configs: {source k, target a/b} and {source j, target /run/secrets/a/b}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, target: a/b}
      - {source: j, target: /run/secrets/a/b}
configs:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}}, false},
		{"configs: {source k, target /x/y} and {source j, target /x/y}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, target: /x/y}
      - {source: j, target: /x/y}
configs:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}}, false},
		{"configs: mode -0, one file", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: -0}
configs:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}}, true},
		{"configs: mode -0 in the first file, the second writes 0440", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: -0}
configs:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}, {"f2.yaml", `services:
  a:
    configs:
      - {source: k, mode: 0440}
`}}, false},
		{"configs: mode -0 in the extended file, the extender writes 0440", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    extends: {file: base.yaml, service: bs}
    configs:
      - {source: k, mode: 0440}
configs:
  k: {file: ./k.txt}
`}, {"base.yaml", `services:
  bs:
    image: y
    configs:
      - {source: k, mode: -0}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs: mode -0 in a sibling of the extended service", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    extends: {file: base.yaml, service: bs}
configs:
  k: {file: ./k.txt}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    configs:
      - {source: k, mode: -0}
configs:
  k: {file: ./k.txt}
`}}, false},
		{"configs: mode -0.0 and +0 and 00", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: -0.0}
configs:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}}, true},
		{"configs: mode +0", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: +0}
configs:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}}, false},
		{"configs: mode 00", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: 00}
configs:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}}, false},
		{"configs: mode !!int -0", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: !!int -0}
configs:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}}, true},
		{"configs: mode !!float 0", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: !!float 0}
configs:
  k: {file: ./k.txt}
  j: {file: ./k.txt}
`}}, true},
		{"secrets: mode -0 in the first file, the second does not write the mode", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: -0}
secrets:
  k: {file: ./k.txt}
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}}, true},
		{"secrets: mode -0 in the second file, over a mode of the first", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: 0440}
secrets:
  k: {file: ./k.txt}
`}, {"f2.yaml", `services:
  a:
    secrets:
      - {source: k, mode: -0}
`}}, true},
		{"secrets: mode -0 in the extended service, the extender does not write it", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    extends: {file: base.yaml, service: bs}
secrets:
  k: {file: ./k.txt}
`}, {"base.yaml", `services:
  bs:
    image: y
    secrets:
      - {source: k, mode: -0}
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"secrets: mode -0 by an alias", []string{"compose.yaml"}, []file{{"compose.yaml", `x-e: &e {source: k, mode: -0}
services:
  a:
    image: x
    secrets:
      - *e
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"configs: mode -0 in the first file, the second does not write the mode", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: -0}
configs:
  k: {file: ./k.txt}
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}}, true},
		{"configs: mode -0 in the second file, over a mode of the first", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: 0440}
configs:
  k: {file: ./k.txt}
`}, {"f2.yaml", `services:
  a:
    configs:
      - {source: k, mode: -0}
`}}, true},
		{"configs: mode -0 in the extended service, the extender does not write it", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    extends: {file: base.yaml, service: bs}
configs:
  k: {file: ./k.txt}
`}, {"base.yaml", `services:
  bs:
    image: y
    configs:
      - {source: k, mode: -0}
configs:
  k: {file: ./k.txt}
`}}, true},
		{"configs: mode -0 by an alias", []string{"compose.yaml"}, []file{{"compose.yaml", `x-e: &e {source: k, mode: -0}
services:
  a:
    image: x
    configs:
      - *e
configs:
  k: {file: ./k.txt}
`}}, true},
		{"secrets: mode -0, a service of the file that extends another in the same file is beside it", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: -0}
  b:
    image: y
    extends: c
  c:
    image: z
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"secrets: mode by an alias to -0", []string{"compose.yaml"}, []file{{"compose.yaml", `x-z: &z -0
services:
  a:
    image: x
    secrets:
      - {source: k, mode: *z}
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"secrets: a whole service by an alias, holding mode -0", []string{"compose.yaml"}, []file{{"compose.yaml", `x-s: &s
  image: x
  secrets:
    - {source: k, mode: -0}
services:
  a: *s
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"secrets: the list by an alias, holding mode -0", []string{"compose.yaml"}, []file{{"compose.yaml", `x-l: &l [{source: k, mode: -0}]
services:
  a:
    image: x
    secrets: *l
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"secrets: services by an alias, holding mode -0", []string{"compose.yaml"}, []file{{"compose.yaml", `x-v: &v
  a:
    image: x
    secrets:
      - {source: k, mode: -0}
services: *v
secrets:
  k: {file: ./k.txt}
`}}, true},
		{"configs: mode -0, a service of the file that extends another in the same file is beside it", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    configs:
      - {source: k, mode: -0}
  b:
    image: y
    extends: c
  c:
    image: z
configs:
  k: {file: ./k.txt}
`}}, true},
		{"configs: mode by an alias to -0", []string{"compose.yaml"}, []file{{"compose.yaml", `x-z: &z -0
services:
  a:
    image: x
    configs:
      - {source: k, mode: *z}
configs:
  k: {file: ./k.txt}
`}}, true},
		{"configs: a whole service by an alias, holding mode -0", []string{"compose.yaml"}, []file{{"compose.yaml", `x-s: &s
  image: x
  configs:
    - {source: k, mode: -0}
services:
  a: *s
configs:
  k: {file: ./k.txt}
`}}, true},
		{"configs: the list by an alias, holding mode -0", []string{"compose.yaml"}, []file{{"compose.yaml", `x-l: &l [{source: k, mode: -0}]
services:
  a:
    image: x
    configs: *l
configs:
  k: {file: ./k.txt}
`}}, true},
		{"configs: services by an alias, holding mode -0", []string{"compose.yaml"}, []file{{"compose.yaml", `x-v: &v
  a:
    image: x
    configs:
      - {source: k, mode: -0}
services: *v
configs:
  k: {file: ./k.txt}
`}}, true},
		{"secrets mode '!-0' written as a string", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    secrets:
      - {source: k, mode: '!-0'}
secrets:
  k: {file: ./k.txt}
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
			_, err := LoadFiles(paths, nil)
			if (err != nil) != tc.refused {
				t.Errorf("the load: err %v, docker compose refuses it: %v", err, tc.refused)
			}
			// A mode written `-0` is said to be what docker compose reads as a float, and no private word of this load's shows in the words.
			if err != nil && strings.Contains(tc.name, "mode -0") && !strings.Contains(tc.name, "-0.0") && !strings.Contains(tc.name, "!!int") && (!strings.Contains(err.Error(), "-0 (which docker compose reads as a float)") || strings.Contains(err.Error(), negZeroMode)) {
				t.Errorf("the refusal of a mode -0 does not say so: %v", err)
			}
		})
	}
}

func TestASecretTargetIsMountedWhereItSaysAndTheDefaultWrittenOutIsTheBareName(t *testing.T) {
	for _, tc := range []struct {
		target, want string
	}{
		{"k", "/run/secrets/k"},
		{"/run/secrets/k", "/run/secrets/k"},
		{"/x/y", "/x/y"},
		{"a/b", "/run/secrets/a/b"},
	} {
		if got := SecretPath(tc.target); got != tc.want {
			t.Errorf("SecretPath(%q) = %q, want %q", tc.target, got, tc.want)
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "k.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "compose.yaml")
	body := "services:\n  a:\n    image: x\n    secrets:\n      - k\n      - {source: k, target: /run/secrets/k}\nsecrets:\n  k: {file: ./k.txt}\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(proj.Services["a"].Secrets); n != 1 {
		t.Errorf("the short form and the default written out are %d secrets, want one (docker compose folds them)", n)
	}
}

// A target that is a file's path is taken, and one that is not (an empty part, `.` or `..` in a path, a `/` at the end) is refused, where
// docker compose lets every string through and its engine cannot mount a file there (#1778).
func TestCheckSecretTarget(t *testing.T) {
	for _, tc := range []struct {
		target  string
		refused bool
	}{
		{"k", false}, {".", false}, {"a.b", false}, {"..x", false}, {"a..b", false}, {"/run/secrets/k", false}, {"a/b", false}, {"/x/y", false},
		{"/", true}, {"//k", true}, {"a//b", true}, {"a/", true}, {"/k/", true}, {"./k", true}, {"a/./b", true},
		{"..", true}, {"../k", true}, {"a/../b", true}, {"/..", true}, {"/.", true}, {"a/.", true},
	} {
		t.Run(tc.target, func(t *testing.T) {
			if err := checkSecretTarget(tc.target); (err != nil) != tc.refused {
				t.Errorf("checkSecretTarget(%q) = %v, refused should be %v", tc.target, err, tc.refused)
			}
		})
	}
}
