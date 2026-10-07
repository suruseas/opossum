package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A `mode` of `-0` that a merge key brings into an entry of a service's `secrets` or `configs` is refused as one written there is (v5.5.1 `config`,
// every row measured, rc 1 or 0): docker compose reads `-0` there as a float, and the merge key may hold a mapping, a list of mappings, an alias to
// one and a list of aliases, at any depth. A `-0` in an anchor nothing merges into an entry is read as the note it is (#1837).
func TestAModeOfNegativeZeroThatAMergeKeyBringsIsRefusedAsOneWrittenThereIs(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		refused    bool
	}{
		{"secrets: a mode of -0 in the entry", "secrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    image: x\n    secrets:\n      - {source: s, mode: -0}\n", true},
		{"secrets: a mode of -0 in a mapping a merge key holds", "secrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    image: x\n    secrets:\n      - source: s\n        <<: {mode: -0}\n", true},
		{"secrets: a mode of -0 in a list of mappings a merge key holds", "secrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    image: x\n    secrets:\n      - <<: [{source: s}, {mode: -0}]\n", true},
		{"secrets: a mode of -0 in an anchor a merge key holds", "x-m: &m {mode: -0}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    image: x\n    secrets:\n      - source: s\n        <<: *m\n", true},
		{"secrets: a mode of -0 in an anchor in a list a merge key holds", "x-m: &m {mode: -0}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    image: x\n    secrets:\n      - source: s\n        <<: [*m]\n", true},
		{"secrets: a mode of -0 two merge keys down", "secrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    image: x\n    secrets:\n      - source: s\n        <<: {<<: {mode: -0}}\n", true},
		{"secrets: a mode of 0 in a mapping a merge key holds", "secrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    image: x\n    secrets:\n      - source: s\n        <<: {mode: 0}\n", false},
		{"secrets: a mode of 0o644 in a mapping a merge key holds", "secrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    image: x\n    secrets:\n      - source: s\n        <<: {mode: 0o644}\n", false},
		{"configs: a mode of -0 in the entry", "secrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    image: x\n    configs:\n      - {source: c, mode: -0}\n", true},
		{"configs: a mode of -0 in a mapping a merge key holds", "secrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    image: x\n    configs:\n      - source: c\n        <<: {mode: -0}\n", true},
		{"configs: a mode of -0 in a list of mappings a merge key holds", "secrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    image: x\n    configs:\n      - <<: [{source: c}, {mode: -0}]\n", true},
		{"configs: a mode of -0 in an anchor a merge key holds", "x-m: &m {mode: -0}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    image: x\n    configs:\n      - source: c\n        <<: *m\n", true},
		{"configs: a mode of -0 in an anchor in a list a merge key holds", "x-m: &m {mode: -0}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    image: x\n    configs:\n      - source: c\n        <<: [*m]\n", true},
		{"configs: a mode of -0 two merge keys down", "secrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    image: x\n    configs:\n      - source: c\n        <<: {<<: {mode: -0}}\n", true},
		{"configs: a mode of 0 in a mapping a merge key holds", "secrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    image: x\n    configs:\n      - source: c\n        <<: {mode: 0}\n", false},
		{"configs: a mode of 0o644 in a mapping a merge key holds", "secrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    image: x\n    configs:\n      - source: c\n        <<: {mode: 0o644}\n", false},
		{"a service that a merge key brings a secrets entry with -0 into (an alias)", "x-svc: &svc {image: x, secrets: [{source: s, mode: -0}]}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    <<: *svc\n", true},
		{"a service that a merge key brings a secrets entry with -0 into (inline)", "secrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    <<: {image: x, secrets: [{source: s, mode: -0}]}\n", true},
		{"a service that a merge key brings the list of secrets into (an alias)", "x-sl: &sl [{source: s, mode: -0}]\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    image: x\n    <<: {secrets: *sl}\n", true},
		{"a service that a merge key brings a configs entry with -0 into", "x-svc: &svc {image: x, configs: [{source: c, mode: -0}]}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    <<: *svc\n", true},
		{"services that a merge key brings in", "x-sv: &sv {a: {image: x, secrets: [{source: s, mode: -0}]}}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  <<: *sv\n", true},
		{"a service that a list of aliases brings secrets into", "x-b: &b {image: x}\nx-m: &m {secrets: [{source: s, mode: -0}]}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    <<: [*b, *m]\n", true},
		{"a service and its secrets entry that both merge", "x-svc: &svc {image: x, secrets: [{source: s, <<: {mode: -0}}]}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    <<: *svc\n", true},
		{"two services that share the service anchor", "x-svc: &svc {image: x, secrets: [{source: s, mode: -0}]}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    <<: *svc\n  b:\n    <<: *svc\n", true},
		{"a service whose merge holds another merge that brings the -0", "x-q: &q {secrets: [{source: s, mode: -0}]}\nx-r: &r {<<: *q, image: x}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    <<: *r\n", true},
		{"a service whose merge list holds a merge that brings the -0", "x-q: &q {secrets: [{source: s, mode: -0}]}\nx-r: &r {<<: *q, image: x}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    <<: [*r]\n", true},
		{"a service merge list with the -0 side first", "x-q: &q {image: x, secrets: [{source: s, mode: -0}]}\nx-p: &p {secrets: [{source: s, mode: 0o400}]}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    <<: [*q, *p]\n", true},
		{"a service merge list with the -0 side last, the first winning", "x-q: &q {image: x, secrets: [{source: s, mode: -0}]}\nx-p: &p {secrets: [{source: s, mode: 0o400}]}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    <<: [*p, *q]\n", false},
		{"services whose merge holds another merge that brings the -0", "x-q: &q {a: {image: x, secrets: [{source: s, mode: -0}]}}\nx-r: &r {<<: *q}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  <<: *r\n", true},
		{"services merge list with the -0 side first", "x-q: &q {a: {image: x, secrets: [{source: s, mode: -0}]}}\nx-p: &p {b: {image: x}}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  <<: [*q, *p]\n", true},
		{"services merge list with the -0 side last", "x-q: &q {a: {image: x, secrets: [{source: s, mode: -0}]}}\nx-p: &p {b: {image: x}}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  <<: [*p, *q]\n", true},
		{"a service merge with a mode of 0", "x-svc: &svc {image: x, secrets: [{source: s, mode: 0}]}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    <<: *svc\n", false},
		{"a service merge whose secrets the service overrides with its own", "x-svc: &svc {image: x, secrets: [{source: s, mode: -0}]}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    <<: *svc\n    secrets: [{source: s, mode: 0o400}]\n", false},
		{"a service anchor with -0 that no service merges", "x-svc: &svc {image: x, secrets: [{source: s, mode: -0}]}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    image: x\n", false},
		{"a -0 in the first of a list of mappings that a merge key holds, a mode after it", "secrets:\n  s: {file: ./f.txt}\nservices:\n  a:\n    image: x\n    secrets:\n      - <<: [{source: s, mode: -0}, {mode: 0o400}]\n", true},
		{"a -0 in the last of a list of mappings that a merge key holds, a mode before it", "secrets:\n  s: {file: ./f.txt}\nservices:\n  a:\n    image: x\n    secrets:\n      - <<: [{source: s, mode: 0o400}, {mode: -0}]\n", false},
		{"an anchor of -0 shared by a secrets entry and a build secrets entry", "secrets:\n  s: {file: ./f.txt}\nx-m: &m {mode: -0}\nservices:\n  a:\n    build: {context: ., secrets: [{source: s, <<: *m}]}\n    secrets:\n      - source: s\n        mode: 0o400\n        <<: *m\n", true},
		{"an anchor of -0 that nothing uses", "x-m: &m {mode: -0}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    image: x\n", false},
		{"an anchor of -0 that only an x- field uses", "x-m: &m {mode: -0}\nx-o: {<<: *m}\nsecrets:\n  s: {file: ./f.txt}\nconfigs:\n  c: {file: ./f.txt}\nservices:\n  a:\n    image: x\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadFiles([]string{path}, nil)
			if (err != nil) != tc.refused {
				t.Errorf("err %v, docker compose refuses it: %v", err, tc.refused)
			}
			// The word that stands for a -0 while it is read is no part of what is said, written as the character or as its escape.
			if err != nil && (strings.Contains(err.Error(), "\ue002") || strings.Contains(err.Error(), `\ue002`)) {
				t.Errorf("the refusal carries the private-use mark: %q", err)
			}
		})
	}
}
