package compose

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A `!reset` or `!override` on one name of a mapping, written in a later file, reaches a name that the files before it wrote as docker
// compose (v5.5.1, `config`, every row measured) lets it: docker holds `environment`, `labels` and `build.args` as a list once a second
// file has merged into them, so once two files have written the key a tag in a later file reaches none of its names (#1736); a name is reached where at most one earlier file wrote the key. A key a
// later file took out whole (`!reset`, `!override`, of the key or of the service) is written afresh by the next file that writes it.
func TestATagOnAMappingNameInAThirdFileReachesWhatDockerComposeLetsItReach(t *testing.T) {
	type file struct{ name, body string }
	for _, tc := range []struct {
		name, key string
		files     []file
		want      []string
	}{
		{"environment: f2 writes the same mapping, f3 resets one name", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    environment: {C: '3'}
`}, {"f3.yaml", `services:
  a:
    environment:
      A: !reset null
`}}, []string{"A", "B", "C"}},
		{"environment: f2 does not touch it, f3 resets one name", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    working_dir: /w
`}, {"f3.yaml", `services:
  a:
    environment:
      A: !reset null
`}}, []string{"B"}},
		{"environment: only two files, f2 resets one name", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    environment:
      A: !reset null
`}}, []string{"B"}},
		{"labels: f2 writes the same mapping, f3 resets one name", "labels", []file{{"f1.yaml", `services:
  a:
    image: x
    labels: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    labels: {C: '3'}
`}, {"f3.yaml", `services:
  a:
    labels:
      A: !reset null
`}}, []string{"A", "B", "C"}},
		{"labels: f2 does not touch it, f3 resets one name", "labels", []file{{"f1.yaml", `services:
  a:
    image: x
    labels: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}, {"f3.yaml", `services:
  a:
    labels:
      A: !reset null
`}}, []string{"B"}},
		{"labels: only two files, f2 resets one name", "labels", []file{{"f1.yaml", `services:
  a:
    image: x
    labels: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    labels:
      A: !reset null
`}}, []string{"B"}},
		{"build.args: f2 writes the same mapping, f3 resets one name", "build.args", []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., args: {A: '1', B: '2'}}
`}, {"f2.yaml", `services:
  a:
    build: {args: {C: '3'}}
`}, {"f3.yaml", `services:
  a:
    build:
      args:
        A: !reset null
`}}, []string{"A", "B", "C"}},
		{"build.args: f2 does not touch it, f3 resets one name", "build.args", []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., args: {A: '1', B: '2'}}
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}, {"f3.yaml", `services:
  a:
    build:
      args:
        A: !reset null
`}}, []string{"B"}},
		{"build.args: only two files, f2 resets one name", "build.args", []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., args: {A: '1', B: '2'}}
`}, {"f2.yaml", `services:
  a:
    build:
      args:
        A: !reset null
`}}, []string{"B"}},
		{"environment (a list in f1): f2 writes the same mapping, f3 resets one name", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: ['A=1','B=2']
`}, {"f2.yaml", `services:
  a:
    environment: {C: '3'}
`}, {"f3.yaml", `services:
  a:
    environment:
      A: !reset null
`}}, []string{"A", "B", "C"}},
		{"environment (a list in f1): f2 does not touch it, f3 resets one name", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: ['A=1','B=2']
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}, {"f3.yaml", `services:
  a:
    environment:
      A: !reset null
`}}, []string{"A", "B"}},
		{"environment (a list in f1): only two files, f2 resets one name", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: ['A=1','B=2']
`}, {"f2.yaml", `services:
  a:
    environment:
      A: !reset null
`}}, []string{"A", "B"}},
		{"environment: four files, the second writes it, the fourth resets", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    environment: {C: '3'}
`}, {"f3.yaml", `services:
  a:
    hostname: h
`}, {"f4.yaml", `services:
  a:
    environment:
      A: !reset null
`}}, []string{"A", "B", "C"}},
		{"environment: four files, none of the middle two writes it, the fourth resets", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}, {"f3.yaml", `services:
  a:
    working_dir: /w
`}, {"f4.yaml", `services:
  a:
    environment:
      A: !reset null
`}}, []string{"B"}},
		{"environment: the first does not write it, the second and third do, the third resets", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
`}, {"f2.yaml", `services:
  a:
    environment: {C: '3'}
`}, {"f3.yaml", `services:
  a:
    environment:
      C: !reset null
`}}, []string{}},
		{"environment: the first does not write it, the second writes it, the third resets a name of the second", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    hostname: h
`}, {"f2.yaml", `services:
  a:
    environment: {C: '3'}
`}, {"f3.yaml", `services:
  a:
    environment:
      C: !reset null
`}}, []string{}},
		{"environment: the third resets a name no file wrote", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    environment: {C: '3'}
`}, {"f3.yaml", `services:
  a:
    environment:
      Z: !reset null
`}}, []string{"A", "B", "C"}},
		{"environment: the third overrides a name of the first", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    environment: {C: '3'}
`}, {"f3.yaml", `services:
  a:
    environment:
      A: !override '9'
`}}, []string{"A", "B", "C"}},
		{"environment: the second resets a name of the first, the third writes it again", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    environment:
      A: !reset null
`}, {"f3.yaml", `services:
  a:
    environment: {C: '3'}
`}}, []string{"B", "C"}},
		{"environment: the second writes a name, the third resets the name the second wrote", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    environment: {C: '3'}
`}, {"f3.yaml", `services:
  a:
    environment:
      C: !reset null
`}}, []string{"A", "B", "C"}},
		{"environment: the whole key reset in the third", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    environment: {C: '3'}
`}, {"f3.yaml", `services:
  a:
    environment: !reset null
`}}, []string{}},
		{"environment: the whole key overridden in the third", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    environment: {C: '3'}
`}, {"f3.yaml", `services:
  a:
    environment: !override {D: '4'}
`}}, []string{"D"}},
		{"labels: four files, the second writes it, the fourth resets", "labels", []file{{"f1.yaml", `services:
  a:
    image: x
    labels: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    labels: {C: '3'}
`}, {"f3.yaml", `services:
  a:
    hostname: h
`}, {"f4.yaml", `services:
  a:
    labels:
      A: !reset null
`}}, []string{"A", "B", "C"}},
		{"labels: four files, none of the middle two writes it, the fourth resets", "labels", []file{{"f1.yaml", `services:
  a:
    image: x
    labels: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}, {"f3.yaml", `services:
  a:
    working_dir: /w
`}, {"f4.yaml", `services:
  a:
    labels:
      A: !reset null
`}}, []string{"B"}},
		{"labels: the first does not write it, the second and third do, the third resets", "labels", []file{{"f1.yaml", `services:
  a:
    image: x
`}, {"f2.yaml", `services:
  a:
    labels: {C: '3'}
`}, {"f3.yaml", `services:
  a:
    labels:
      C: !reset null
`}}, []string{}},
		{"labels: the first does not write it, the second writes it, the third resets a name of the second", "labels", []file{{"f1.yaml", `services:
  a:
    image: x
    hostname: h
`}, {"f2.yaml", `services:
  a:
    labels: {C: '3'}
`}, {"f3.yaml", `services:
  a:
    labels:
      C: !reset null
`}}, []string{}},
		{"labels: the third resets a name no file wrote", "labels", []file{{"f1.yaml", `services:
  a:
    image: x
    labels: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    labels: {C: '3'}
`}, {"f3.yaml", `services:
  a:
    labels:
      Z: !reset null
`}}, []string{"A", "B", "C"}},
		{"labels: the third overrides a name of the first", "labels", []file{{"f1.yaml", `services:
  a:
    image: x
    labels: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    labels: {C: '3'}
`}, {"f3.yaml", `services:
  a:
    labels:
      A: !override '9'
`}}, []string{"A", "B", "C"}},
		{"labels: the second resets a name of the first, the third writes it again", "labels", []file{{"f1.yaml", `services:
  a:
    image: x
    labels: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    labels:
      A: !reset null
`}, {"f3.yaml", `services:
  a:
    labels: {C: '3'}
`}}, []string{"B", "C"}},
		{"labels: the second writes a name, the third resets the name the second wrote", "labels", []file{{"f1.yaml", `services:
  a:
    image: x
    labels: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    labels: {C: '3'}
`}, {"f3.yaml", `services:
  a:
    labels:
      C: !reset null
`}}, []string{"A", "B", "C"}},
		{"labels: the whole key reset in the third", "labels", []file{{"f1.yaml", `services:
  a:
    image: x
    labels: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    labels: {C: '3'}
`}, {"f3.yaml", `services:
  a:
    labels: !reset null
`}}, []string{}},
		{"labels: the whole key overridden in the third", "labels", []file{{"f1.yaml", `services:
  a:
    image: x
    labels: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    labels: {C: '3'}
`}, {"f3.yaml", `services:
  a:
    labels: !override {D: '4'}
`}}, []string{"D"}},
		{"build.args: four files, the second writes it, the fourth resets", "build.args", []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., args: {A: '1', B: '2'}}
`}, {"f2.yaml", `services:
  a:
    build: {args: {C: '3'}}
`}, {"f3.yaml", `services:
  a:
    hostname: h
`}, {"f4.yaml", `services:
  a:
    build:
      args:
        A: !reset null
`}}, []string{"A", "B", "C"}},
		{"build.args: four files, none of the middle two writes it, the fourth resets", "build.args", []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., args: {A: '1', B: '2'}}
`}, {"f2.yaml", `services:
  a:
    hostname: h
`}, {"f3.yaml", `services:
  a:
    working_dir: /w
`}, {"f4.yaml", `services:
  a:
    build:
      args:
        A: !reset null
`}}, []string{"B"}},
		{"build.args: the first does not write it, the second and third do, the third resets", "build.args", []file{{"f1.yaml", `services:
  a:
    image: x
`}, {"f2.yaml", `services:
  a:
    build: {args: {C: '3'}}
`}, {"f3.yaml", `services:
  a:
    build:
      args:
        C: !reset null
`}}, []string{}},
		{"build.args: the first does not write it, the second writes it, the third resets a name of the second", "build.args", []file{{"f1.yaml", `services:
  a:
    image: x
    hostname: h
`}, {"f2.yaml", `services:
  a:
    build: {args: {C: '3'}}
`}, {"f3.yaml", `services:
  a:
    build:
      args:
        C: !reset null
`}}, []string{}},
		{"build.args: the third resets a name no file wrote", "build.args", []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., args: {A: '1', B: '2'}}
`}, {"f2.yaml", `services:
  a:
    build: {args: {C: '3'}}
`}, {"f3.yaml", `services:
  a:
    build:
      args:
        Z: !reset null
`}}, []string{"A", "B", "C"}},
		{"build.args: the third overrides a name of the first", "build.args", []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., args: {A: '1', B: '2'}}
`}, {"f2.yaml", `services:
  a:
    build: {args: {C: '3'}}
`}, {"f3.yaml", `services:
  a:
    build:
      args:
        A: !override '9'
`}}, []string{"A", "B", "C"}},
		{"build.args: the second resets a name of the first, the third writes it again", "build.args", []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., args: {A: '1', B: '2'}}
`}, {"f2.yaml", `services:
  a:
    build:
      args:
        A: !reset null
`}, {"f3.yaml", `services:
  a:
    build: {args: {C: '3'}}
`}}, []string{"B", "C"}},
		{"build.args: the second writes a name, the third resets the name the second wrote", "build.args", []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., args: {A: '1', B: '2'}}
`}, {"f2.yaml", `services:
  a:
    build: {args: {C: '3'}}
`}, {"f3.yaml", `services:
  a:
    build:
      args:
        C: !reset null
`}}, []string{"A", "B", "C"}},
		{"build.args: the whole key reset in the third", "build.args", []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., args: {A: '1', B: '2'}}
`}, {"f2.yaml", `services:
  a:
    build: {args: {C: '3'}}
`}, {"f3.yaml", `services:
  a:
    build: !reset null
`}}, []string{}},
		{"build.args: the whole key overridden in the third", "build.args", []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., args: {A: '1', B: '2'}}
`}, {"f2.yaml", `services:
  a:
    build: {args: {C: '3'}}
`}, {"f3.yaml", `services:
  a:
    build:
      args: !override {D: '4'}
`}}, []string{"D"}},
		{"environment: f2 writes a list, f3 resets a name of the first", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    environment: ['C=3']
`}, {"f3.yaml", `services:
  a:
    environment:
      A: !reset null
`}}, []string{"A", "B", "C"}},
		{"environment: f1 and f2 are lists, f3 resets a name", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: ['A=1','B=2']
`}, {"f2.yaml", `services:
  a:
    environment: ['C=3']
`}, {"f3.yaml", `services:
  a:
    environment:
      A: !reset null
`}}, []string{"A", "B", "C"}},
		{"environment: a later file resets the whole key, writes it again, a name is reset", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    environment: {C: '3'}
`}, {"f3.yaml", `services:
  a:
    environment: !reset {}
`}, {"f4.yaml", `services:
  a:
    environment: {D: '4'}
`}, {"f5.yaml", `services:
  a:
    environment:
      D: !reset null
`}}, []string{}},
		{"environment: a later file overrides the whole key, a name is reset", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    environment: {C: '3'}
`}, {"f3.yaml", `services:
  a:
    environment: !override {D: '4'}
`}, {"f4.yaml", `services:
  a:
    environment:
      D: !reset null
`}}, []string{}},
		{"environment: a later file resets the whole service, writes it again, a name is reset", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    environment: {C: '3'}
`}, {"f3.yaml", `services:
  a: !reset {}
`}, {"f4.yaml", `services:
  a:
    image: x
    environment: {D: '4'}
`}, {"f5.yaml", `services:
  a:
    environment:
      D: !reset null
`}}, []string{}},
		{"environment: a later file overrides the whole service, a name is reset", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    environment: {C: '3'}
`}, {"f3.yaml", `services:
  a: !override {image: x, environment: {D: '4'}}
`}, {"f4.yaml", `services:
  a:
    environment:
      D: !reset null
`}}, []string{}},
		{"build.args: build reset in the third, written again in the fourth, a name is reset", "build.args", []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., args: {A: '1'}}
`}, {"f2.yaml", `services:
  a:
    build: {args: {C: '3'}}
`}, {"f3.yaml", `services:
  a:
    build: !reset null
`}, {"f4.yaml", `services:
  a:
    build: {context: ., args: {D: '4'}}
`}, {"f5.yaml", `services:
  a:
    build:
      args:
        D: !reset null
`}}, []string{}},
		{"build.args: the second writes build without args, the third resets a name", "build.args", []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., args: {A: '1', B: '2'}}
`}, {"f2.yaml", `services:
  a:
    build: {dockerfile: D}
`}, {"f3.yaml", `services:
  a:
    build:
      args:
        A: !reset null
`}}, []string{"B"}},
		{"environment: the second writes it as null, the third resets a name", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    environment:
`}, {"f3.yaml", `services:
  a:
    environment:
      A: !reset null
`}}, []string{"A", "B"}},
		{"environment: the second writes it as ~, the third resets a name", "environment", []file{{"f1.yaml", `services:
  a:
    image: x
    environment: {A: '1', B: '2'}
`}, {"f2.yaml", `services:
  a:
    environment: ~
`}, {"f3.yaml", `services:
  a:
    environment:
      A: !reset null
`}}, []string{"A", "B"}},
		{"build.args: the second writes it as null, the third resets a name", "build.args", []file{{"f1.yaml", `services:
  a:
    image: x
    build: {context: ., args: {A: '1'}}
`}, {"f2.yaml", `services:
  a:
    build: {args: }
`}, {"f3.yaml", `services:
  a:
    build:
      args:
        A: !reset null
`}}, []string{"A"}},
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
			p, err := LoadFiles(paths, nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			svc := p.Services["a"]
			var got []string
			switch tc.key {
			case "environment":
				for _, kv := range svc.Environment {
					got = append(got, strings.SplitN(kv, "=", 2)[0])
				}
			case "labels":
				for _, kv := range svc.Labels {
					got = append(got, strings.SplitN(kv, "=", 2)[0])
				}
			default:
				if svc.Build != nil {
					for _, kv := range svc.Build.Args {
						got = append(got, strings.SplitN(kv, "=", 2)[0])
					}
				}
			}
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("names left: %v, want %v", got, tc.want)
			}
		})
	}
}
