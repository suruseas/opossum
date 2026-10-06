package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// A `deploy.replicas` that is a list or a mapping in a service taken from another file by `extends` is read where the extending service
// writes `replicas` or `deploy` over it with `!override` or `!reset` (8 forms), and refused where it stays, where a count is written over
// it without a tag, and where a list is written over a list, as docker compose reads the pair (`config -q`, every row measured, v5.5.1:
// `refused` is its rc 1). In a sibling of the extended service it is not asked; in the same file, or in an earlier `-f` file, it is
// refused as the file is read (#1776).
func TestAListOrAMappingForReplicasOfAnExtendedServiceIsAskedAfterItIsMerged(t *testing.T) {
	type file struct{ name, body string }
	for _, tc := range []struct {
		name    string
		order   []string // the files given with `-f`, in order
		files   []file   // every file written, an extended one too
		refused bool
	}{
		{"extended replicas list, extender replicas: !override 3", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    deploy: {replicas: !override 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: [1]}
`}}, false},
		{"extended replicas list, extender replicas: !reset null", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    deploy: {replicas: !reset null}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: [1]}
`}}, false},
		{"extended replicas list, extender deploy: !override {replicas: 3}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    deploy: !override {replicas: 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: [1]}
`}}, false},
		{"extended replicas list, extender deploy: !reset null", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    deploy: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: [1]}
`}}, false},
		{"extended replicas list, extender replicas: 3 (no tag)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    deploy: {replicas: 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: [1]}
`}}, true},
		{"extended replicas list, extender nothing written (kept)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: [1]}
`}}, true},
		{"extended replicas list, extender replicas: [2] (same kind, no tag)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    deploy: {replicas: [2]}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: [1]}
`}}, true},
		{"extended replicas list, extender deploy: {} (no replicas)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    deploy: {}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: [1]}
`}}, true},
		{"extended replicas list, extender replicas: !override [2]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    deploy: {replicas: !override [2]}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: [1]}
`}}, true},
		{"extended replicas list in a sibling of the extended service", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    deploy: {replicas: [1]}
`}}, false},
		{"same file: extended replicas list, extender replicas: !override 3", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    deploy: {replicas: [1]}
  web:
    extends: bs
    deploy: {replicas: !override 3}
`}}, true},
		{"-f: earlier replicas list, later replicas: !override 3", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    deploy: {replicas: [1]}
`}, {"f2.yaml", `services:
  a:
    deploy: {replicas: !override 3}
`}}, true},
		{"-f: earlier replicas list, later replicas: 3 (no tag)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    deploy: {replicas: [1]}
`}, {"f2.yaml", `services:
  a:
    deploy: {replicas: 3}
`}}, true},
		{"extended replicas mapping, extender replicas: !override 3", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    deploy: {replicas: !override 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: {a: 1}}
`}}, false},
		{"extended replicas mapping, extender replicas: !reset null", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    deploy: {replicas: !reset null}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: {a: 1}}
`}}, false},
		{"extended replicas mapping, extender deploy: !override {replicas: 3}", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    deploy: !override {replicas: 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: {a: 1}}
`}}, false},
		{"extended replicas mapping, extender deploy: !reset null", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    deploy: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: {a: 1}}
`}}, false},
		{"extended replicas mapping, extender replicas: 3 (no tag)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    deploy: {replicas: 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: {a: 1}}
`}}, true},
		{"extended replicas mapping, extender nothing written (kept)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: {a: 1}}
`}}, true},
		{"extended replicas mapping, extender replicas: [2] (same kind, no tag)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    deploy: {replicas: [2]}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: {a: 1}}
`}}, true},
		{"extended replicas mapping, extender deploy: {} (no replicas)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    deploy: {}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: {a: 1}}
`}}, true},
		{"extended replicas mapping, extender replicas: !override [2]", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
    deploy: {replicas: !override [2]}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: {a: 1}}
`}}, true},
		{"extended replicas mapping in a sibling of the extended service", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: bs}
`}, {"base.yaml", `services:
  bs:
    image: y
  sib:
    image: z
    deploy: {replicas: {a: 1}}
`}}, false},
		{"same file: extended replicas mapping, extender replicas: !override 3", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  bs:
    image: y
    deploy: {replicas: {a: 1}}
  web:
    extends: bs
    deploy: {replicas: !override 3}
`}}, true},
		{"-f: earlier replicas mapping, later replicas: !override 3", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    deploy: {replicas: {a: 1}}
`}, {"f2.yaml", `services:
  a:
    deploy: {replicas: !override 3}
`}}, true},
		{"-f: earlier replicas mapping, later replicas: 3 (no tag)", []string{"f1.yaml", "f2.yaml"}, []file{{"f1.yaml", `services:
  a:
    image: x
    deploy: {replicas: {a: 1}}
`}, {"f2.yaml", `services:
  a:
    deploy: {replicas: 3}
`}}, true},
		{"chain, replicas list: mid writes 3 untagged", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: [1]}
`}, {"mid.yaml", `services:
  m:
    extends: {file: base.yaml, service: bs}
    deploy: {replicas: 3}
`}}, true},
		{"chain, replicas list: mid writes !override 3", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: [1]}
`}, {"mid.yaml", `services:
  m:
    extends: {file: base.yaml, service: bs}
    deploy: {replicas: !override 3}
`}}, false},
		{"chain, replicas list: mid writes nothing, top writes !override 3", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m}
    deploy: {replicas: !override 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: [1]}
`}, {"mid.yaml", `services:
  m:
    extends: {file: base.yaml, service: bs}
`}}, false},
		{"chain, replicas list: mid writes nothing, top writes 3 untagged", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m}
    deploy: {replicas: 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: [1]}
`}, {"mid.yaml", `services:
  m:
    extends: {file: base.yaml, service: bs}
`}}, true},
		{"chain, replicas list: mid writes nothing, top resets deploy", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m}
    deploy: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: [1]}
`}, {"mid.yaml", `services:
  m:
    extends: {file: base.yaml, service: bs}
`}}, false},
		{"chain, replicas list: mid resets deploy", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: [1]}
`}, {"mid.yaml", `services:
  m:
    extends: {file: base.yaml, service: bs}
    deploy: !reset null
`}}, false},
		{"chain, replicas mapping: mid writes 3 untagged", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: {a: 1}}
`}, {"mid.yaml", `services:
  m:
    extends: {file: base.yaml, service: bs}
    deploy: {replicas: 3}
`}}, true},
		{"chain, replicas mapping: mid writes !override 3", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: {a: 1}}
`}, {"mid.yaml", `services:
  m:
    extends: {file: base.yaml, service: bs}
    deploy: {replicas: !override 3}
`}}, false},
		{"chain, replicas mapping: mid writes nothing, top writes !override 3", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m}
    deploy: {replicas: !override 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: {a: 1}}
`}, {"mid.yaml", `services:
  m:
    extends: {file: base.yaml, service: bs}
`}}, false},
		{"chain, replicas mapping: mid writes nothing, top writes 3 untagged", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m}
    deploy: {replicas: 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: {a: 1}}
`}, {"mid.yaml", `services:
  m:
    extends: {file: base.yaml, service: bs}
`}}, true},
		{"chain, replicas mapping: mid writes nothing, top resets deploy", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m}
    deploy: !reset null
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: {a: 1}}
`}, {"mid.yaml", `services:
  m:
    extends: {file: base.yaml, service: bs}
`}}, false},
		{"chain, replicas mapping: mid resets deploy", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: {a: 1}}
`}, {"mid.yaml", `services:
  m:
    extends: {file: base.yaml, service: bs}
    deploy: !reset null
`}}, false},
		{"chain, replicas list: mid writes deploy without replicas, top writes !override 3", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m}
    deploy: {replicas: !override 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: [1]}
`}, {"mid.yaml", `services:
  m:
    extends: {file: base.yaml, service: bs}
    deploy: {labels: {a: b}}
`}}, false},
		{"chain, replicas mapping: mid writes deploy without replicas, top writes !override 3", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m}
    deploy: {replicas: !override 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: {a: 1}}
`}, {"mid.yaml", `services:
  m:
    extends: {file: base.yaml, service: bs}
    deploy: {labels: {a: b}}
`}}, false},
		{"a same-file chain in the extended file: m1 writes 3 over m2's list", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m1}
`}, {"mid.yaml", `services:
  m1:
    image: y
    extends: m2
    deploy: {replicas: 3}
  m2:
    image: y
    deploy: {replicas: [1]}
`}}, true},
		{"a same-file chain in the extended file: m1 writes 3 over m2's list, top writes !override 3", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m1}
    deploy: {replicas: !override 3}
`}, {"mid.yaml", `services:
  m1:
    image: y
    extends: m2
    deploy: {replicas: 3}
  m2:
    image: y
    deploy: {replicas: [1]}
`}}, true},
		{"a same-file chain in the extended file: m1 writes a mapping over m2's list, top writes !override 3", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m1}
    deploy: {replicas: !override 3}
`}, {"mid.yaml", `services:
  m1:
    image: y
    extends: m2
    deploy: {replicas: {b: 2}}
  m2:
    image: y
    deploy: {replicas: [1]}
`}}, true},
		{"a same-file chain in the extended file: m1 writes [5] over m2's list, top writes !override 3 (docker merges the same kind)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m1}
    deploy: {replicas: !override 3}
`}, {"mid.yaml", `services:
  m1:
    image: y
    extends: m2
    deploy: {replicas: [5]}
  m2:
    image: y
    deploy: {replicas: [1]}
`}}, false},
		{"a same-file chain in the extended file: m1 writes null over m2's list, top writes !override 3", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m1}
    deploy: {replicas: !override 3}
`}, {"mid.yaml", `services:
  m1:
    image: y
    extends: m2
    deploy: {replicas: ~}
  m2:
    image: y
    deploy: {replicas: [1]}
`}}, false},
		{"mid: m1 extends m2, m2 extends base (list), m1 writes 3", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m1}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: [1]}
`}, {"mid.yaml", `services:
  m1:
    extends: m2
    deploy: {replicas: 3}
  m2:
    extends: {file: base.yaml, service: bs}
`}}, true},
		{"mid: m1 extends m2, m2 extends base (list), m1 writes nothing, top writes !override 3", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m1}
    deploy: {replicas: !override 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: [1]}
`}, {"mid.yaml", `services:
  m1:
    extends: m2
  m2:
    extends: {file: base.yaml, service: bs}
`}}, false},
		{"a same-file chain in the extended file: m1 writes 3 over m2's mapping", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m1}
`}, {"mid.yaml", `services:
  m1:
    image: y
    extends: m2
    deploy: {replicas: 3}
  m2:
    image: y
    deploy: {replicas: {a: 1}}
`}}, true},
		{"a same-file chain in the extended file: m1 writes 3 over m2's mapping, top writes !override 3", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m1}
    deploy: {replicas: !override 3}
`}, {"mid.yaml", `services:
  m1:
    image: y
    extends: m2
    deploy: {replicas: 3}
  m2:
    image: y
    deploy: {replicas: {a: 1}}
`}}, true},
		{"a same-file chain in the extended file: m1 writes a list over m2's mapping, top writes !override 3", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m1}
    deploy: {replicas: !override 3}
`}, {"mid.yaml", `services:
  m1:
    image: y
    extends: m2
    deploy: {replicas: [5]}
  m2:
    image: y
    deploy: {replicas: {a: 1}}
`}}, true},
		{"a same-file chain in the extended file: m1 writes {b: 2} over m2's mapping, top writes !override 3 (docker merges the same kind)", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m1}
    deploy: {replicas: !override 3}
`}, {"mid.yaml", `services:
  m1:
    image: y
    extends: m2
    deploy: {replicas: {b: 2}}
  m2:
    image: y
    deploy: {replicas: {a: 1}}
`}}, false},
		{"a same-file chain in the extended file: m1 writes null over m2's mapping, top writes !override 3", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m1}
    deploy: {replicas: !override 3}
`}, {"mid.yaml", `services:
  m1:
    image: y
    extends: m2
    deploy: {replicas: ~}
  m2:
    image: y
    deploy: {replicas: {a: 1}}
`}}, false},
		{"mid: m1 extends m2, m2 extends base (mapping), m1 writes 3", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m1}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: {a: 1}}
`}, {"mid.yaml", `services:
  m1:
    extends: m2
    deploy: {replicas: 3}
  m2:
    extends: {file: base.yaml, service: bs}
`}}, true},
		{"mid: m1 extends m2, m2 extends base (mapping), m1 writes nothing, top writes !override 3", []string{"compose.yaml"}, []file{{"compose.yaml", `services:
  top:
    image: x
    extends: {file: mid.yaml, service: m1}
    deploy: {replicas: !override 3}
`}, {"base.yaml", `services:
  bs:
    image: y
    deploy: {replicas: {a: 1}}
`}, {"mid.yaml", `services:
  m1:
    extends: m2
  m2:
    extends: {file: base.yaml, service: bs}
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
			if _, err := LoadFiles(paths, nil); (err != nil) != tc.refused {
				t.Errorf("the load: err %v, docker compose refuses it: %v", err, tc.refused)
			}
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
