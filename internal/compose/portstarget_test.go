package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The `target` of a long `ports` entry is read as docker compose reads it (`config -q`, every row measured, v5.5.1: `refused` is its rc 1): a
// whole float is the port it says, in hex too (`!!float 0x50`), and in a service nothing takes in a file that is only extended from a
// fraction, a boolean, a list or a mapping is left, where a word is refused (#1780). The forms that are a known difference are not here:
// a `target` of 65536 or more, or 0, which docker compose lets through, and two entries of the same port spelled differently, which it refuses.
func TestTheTargetOfALongPortIsReadAsDockerComposeReadsIt(t *testing.T) {
	type file struct{ name, body string }
	for _, tc := range []struct {
		name    string
		order   []string // the files given with `-f`, in order
		files   []file   // every file written, an extended one too
		refused bool
	}{
		{"long target 80", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    ports:
      - {target: 80}
`}}, false},
		{"long target 80.0", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    ports:
      - {target: 80.0}
`}}, false},
		{"long target 65535", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    ports:
      - {target: 65535}
`}}, false},
		{"long target -1", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    ports:
      - {target: -1}
`}}, true},
		{"long target 80.5", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    ports:
      - {target: 80.5}
`}}, true},
		{"long target 1e2", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    ports:
      - {target: 1e2}
`}}, false},
		{"long target !!float 0x50", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    ports:
      - {target: !!float 0x50}
`}}, false},
		{"long target '80'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    ports:
      - {target: '80'}
`}}, false},
		{"long target '80.0'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    ports:
      - {target: '80.0'}
`}}, true},
		{"long target '0x50'", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    ports:
      - {target: '0x50'}
`}}, true},
		{"two equal entries: 80 and 80", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    ports:
      - {target: 80}
      - {target: 80}
`}}, false},
		{"two equal entries: '80:80' and {target: 80, published: 80}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    ports:
      - '80:80'
      - {target: 80, published: 80}
`}}, false},
		{"two equal entries across -f files, same spelling", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    ports:
      - {target: 80}
`}, {"f2.yaml", `services:
  a:
    ports:
      - {target: 80}
`}}, false},
		{"target 1.5 in a service nothing takes (extended file sibling)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {target: 1.5}
`}}, false},
		{"target 1.5 in a profile-gated service not enabled", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    profiles: [g]
    ports:
      - {target: 1.5}
`}}, true},
		{"target abc in a service nothing takes (extended file sibling)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
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
		{"target abc in a profile-gated service not enabled", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    profiles: [g]
    ports:
      - {target: abc}
`}}, true},
		{"target true in a service nothing takes (extended file sibling)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {target: true}
`}}, false},
		{"target true in a profile-gated service not enabled", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    profiles: [g]
    ports:
      - {target: true}
`}}, true},
		{"target [1] in a service nothing takes (extended file sibling)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {target: [1]}
`}}, false},
		{"target [1] in a profile-gated service not enabled", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    profiles: [g]
    ports:
      - {target: [1]}
`}}, true},
		{"target 0x50 in a service nothing takes (extended file sibling)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {target: 0x50}
`}}, false},
		{"target 0x50 in a profile-gated service not enabled", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  a:
    image: x
    profiles: [g]
    ports:
      - {target: 0x50}
`}}, false},
		{"target 1.5 in the extended service, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 1.5}
`}}, true},
		{"target 1.5 in the extended service, written over by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 1.5}
`}}, false},
		{"target true in the extended service, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: true}
`}}, true},
		{"target true in the extended service, written over by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: true}
`}}, false},
		{"target [1] in the extended service, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: [1]}
`}}, true},
		{"target [1] in the extended service, written over by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: [1]}
`}}, false},
		{"target {a: 1} in the extended service, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: {a: 1}}
`}}, true},
		{"target {a: 1} in the extended service, written over by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: {a: 1}}
`}}, false},
		{"target abc in the extended service, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: abc}
`}}, true},
		{"target abc in the extended service, written over by the extender", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
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
		{"target -1 in a sibling", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {target: -1}
`}}, false},
		{"target -1 in the extended service, written over", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: -1}
`}}, false},
		{"target -1 in the extended service, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: -1}
`}}, true},
		{"target !!float -0x50 in a sibling", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {target: !!float -0x50}
`}}, false},
		{"target !!float -0x50 in the extended service, written over", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: !!float -0x50}
`}}, false},
		{"target !!float -0x50 in the extended service, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: !!float -0x50}
`}}, true},
		{"target .inf in a sibling", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {target: .inf}
`}}, false},
		{"target .inf in the extended service, written over", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: .inf}
`}}, false},
		{"target .inf in the extended service, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: .inf}
`}}, true},
		{"target .nan in a sibling", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {target: .nan}
`}}, false},
		{"target .nan in the extended service, written over", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: .nan}
`}}, false},
		{"target .nan in the extended service, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: .nan}
`}}, true},
		{"target 2026-01-01 in a sibling", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {target: 2026-01-01}
`}}, false},
		{"target 2026-01-01 in the extended service, written over", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 2026-01-01}
`}}, false},
		{"target 2026-01-01 in the extended service, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 2026-01-01}
`}}, true},
		{"target 0 in a sibling", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {target: 0}
`}}, false},
		{"target 0 in the extended service, written over", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 0}
`}}, false},
		{"target 70000 in a sibling", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {target: 70000}
`}}, false},
		{"target 70000 in the extended service, written over", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 70000}
`}}, false},
		{"target 1e20 in a sibling", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {target: 1e20}
`}}, false},
		{"target 1e20 in the extended service, written over", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: 1e20}
`}}, false},
		{"target !!float 0o120 in a sibling", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {target: !!float 0o120}
`}}, false},
		{"target !!float 0o120 in the extended service, written over", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: !!float 0o120}
`}}, false},
		{"target !!float 0o120 in the extended service, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: !!float 0o120}
`}}, false},
		{"target !!float 0b101 in a sibling", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {target: !!float 0b101}
`}}, false},
		{"target !!float 0b101 in the extended service, written over", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: !!float 0b101}
`}}, false},
		{"target !!float 0b101 in the extended service, kept", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    ports:
      - {target: !!float 0b101}
`}}, false},
		{"a sibling's target 1.5 by a merge key", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `x-t: &t {target: 1.5}
services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {<<: *t}
`}}, false},
		{"a sibling's target 1.5 by an alias", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `x-n: &n 1.5
services:
  bs:
    image: y
  sib:
    image: z
    ports:
      - {target: *n}
`}}, false},
		{"the extended service's target 1.5 by a merge key, written over", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    ports: !override ['80:80']
`}, {"base.yaml", `x-t: &t {target: 1.5}
services:
  bs:
    image: y
    ports:
      - {<<: *t}
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
			_, err := LoadFiles(paths, nil)
			if (err != nil) != tc.refused {
				t.Errorf("the load: err %v, docker compose refuses it: %v", err, tc.refused)
			}
			// A `target` that stays in the extended service is refused naming the file it is written in, beside the file that extends it.
			if err != nil && strings.Contains(tc.name, "in the extended service, kept") && !strings.Contains(err.Error(), filepath.Join(dir, "base.yaml")) {
				t.Errorf("the refusal does not name base.yaml, where the value is: %v", err)
			}
		})
	}
}
