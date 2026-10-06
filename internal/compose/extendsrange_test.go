package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// docker compose puts the range of a number (`oom_score_adj` -1000..1000, `cpu_count` from 0, `cpu_percent` 0..100, `scale` from 0) to the
// service that results from an `extends` of a service of another file, not to the file it was read from (`config -q`, every row
// measured, v5.5.1: `refused` is its rc 1): a value the extender writes over is not asked, in the extended service or in a sibling that
// nothing extends, and one that stays is refused naming both files (#1461). The read that takes a project down goes on past the refusal.
func TestTheRangeOfANumberIsAskedOfTheServiceAnExtendsResultsIn(t *testing.T) {
	type file struct{ name, body string }
	for _, tc := range []struct {
		name    string
		files   []file
		refused bool
	}{
		{"oom_score_adj=\"2000\": the extended file only, not overridden", []file{{"base.yaml", `services:
  basesvc:
    image: y
    oom_score_adj: "2000"
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
`}}, true},
		{"oom_score_adj=\"2000\": overridden into range by the extender", []file{{"base.yaml", `services:
  basesvc:
    image: y
    oom_score_adj: "2000"
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    oom_score_adj: "5"
`}}, false},
		{"oom_score_adj=\"2000\": overridden out of range by the extender", []file{{"base.yaml", `services:
  basesvc:
    image: y
    oom_score_adj: "5"
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    oom_score_adj: "2000"
`}}, true},
		{"oom_score_adj=\"2000\": a sibling of the extended service, not extended", []file{{"base.yaml", `services:
  basesvc:
    image: y
    oom_score_adj: "5"
  other:
    image: z
    oom_score_adj: "2000"
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
`}}, false},
		{"oom_score_adj=\"2000\": written in the extender itself", []file{{"base.yaml", `services:
  basesvc:
    image: y
    oom_score_adj: "5"
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    oom_score_adj: "2000"
`}}, true},
		{"oom_score_adj=2000: the extended file only, not overridden", []file{{"base.yaml", `services:
  basesvc:
    image: y
    oom_score_adj: 2000
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
`}}, true},
		{"oom_score_adj=2000: overridden into range by the extender", []file{{"base.yaml", `services:
  basesvc:
    image: y
    oom_score_adj: 2000
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    oom_score_adj: 5
`}}, false},
		{"oom_score_adj=2000: overridden out of range by the extender", []file{{"base.yaml", `services:
  basesvc:
    image: y
    oom_score_adj: 5
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    oom_score_adj: 2000
`}}, true},
		{"oom_score_adj=2000: a sibling of the extended service, not extended", []file{{"base.yaml", `services:
  basesvc:
    image: y
    oom_score_adj: 5
  other:
    image: z
    oom_score_adj: 2000
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
`}}, false},
		{"oom_score_adj=2000: written in the extender itself", []file{{"base.yaml", `services:
  basesvc:
    image: y
    oom_score_adj: 5
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    oom_score_adj: 2000
`}}, true},
		{"cpu_count=\"-1\": the extended file only, not overridden", []file{{"base.yaml", `services:
  basesvc:
    image: y
    cpu_count: "-1"
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
`}}, true},
		{"cpu_count=\"-1\": overridden into range by the extender", []file{{"base.yaml", `services:
  basesvc:
    image: y
    cpu_count: "-1"
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    cpu_count: "2"
`}}, false},
		{"cpu_count=\"-1\": overridden out of range by the extender", []file{{"base.yaml", `services:
  basesvc:
    image: y
    cpu_count: "2"
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    cpu_count: "-1"
`}}, true},
		{"cpu_count=\"-1\": a sibling of the extended service, not extended", []file{{"base.yaml", `services:
  basesvc:
    image: y
    cpu_count: "2"
  other:
    image: z
    cpu_count: "-1"
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
`}}, false},
		{"cpu_count=\"-1\": written in the extender itself", []file{{"base.yaml", `services:
  basesvc:
    image: y
    cpu_count: "2"
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    cpu_count: "-1"
`}}, true},
		{"cpu_count=-1: the extended file only, not overridden", []file{{"base.yaml", `services:
  basesvc:
    image: y
    cpu_count: -1
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
`}}, true},
		{"cpu_count=-1: overridden into range by the extender", []file{{"base.yaml", `services:
  basesvc:
    image: y
    cpu_count: -1
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    cpu_count: 2
`}}, false},
		{"cpu_count=-1: overridden out of range by the extender", []file{{"base.yaml", `services:
  basesvc:
    image: y
    cpu_count: 2
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    cpu_count: -1
`}}, true},
		{"cpu_count=-1: a sibling of the extended service, not extended", []file{{"base.yaml", `services:
  basesvc:
    image: y
    cpu_count: 2
  other:
    image: z
    cpu_count: -1
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
`}}, false},
		{"cpu_count=-1: written in the extender itself", []file{{"base.yaml", `services:
  basesvc:
    image: y
    cpu_count: 2
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    cpu_count: -1
`}}, true},
		{"cpu_percent=\"2000\": the extended file only, not overridden", []file{{"base.yaml", `services:
  basesvc:
    image: y
    cpu_percent: "2000"
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
`}}, true},
		{"cpu_percent=\"2000\": overridden into range by the extender", []file{{"base.yaml", `services:
  basesvc:
    image: y
    cpu_percent: "2000"
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    cpu_percent: "50"
`}}, false},
		{"cpu_percent=\"2000\": overridden out of range by the extender", []file{{"base.yaml", `services:
  basesvc:
    image: y
    cpu_percent: "50"
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    cpu_percent: "2000"
`}}, true},
		{"cpu_percent=\"2000\": a sibling of the extended service, not extended", []file{{"base.yaml", `services:
  basesvc:
    image: y
    cpu_percent: "50"
  other:
    image: z
    cpu_percent: "2000"
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
`}}, false},
		{"cpu_percent=\"2000\": written in the extender itself", []file{{"base.yaml", `services:
  basesvc:
    image: y
    cpu_percent: "50"
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    cpu_percent: "2000"
`}}, true},
		{"cpu_percent=2000: the extended file only, not overridden", []file{{"base.yaml", `services:
  basesvc:
    image: y
    cpu_percent: 2000
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
`}}, true},
		{"cpu_percent=2000: overridden into range by the extender", []file{{"base.yaml", `services:
  basesvc:
    image: y
    cpu_percent: 2000
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    cpu_percent: 50
`}}, false},
		{"cpu_percent=2000: overridden out of range by the extender", []file{{"base.yaml", `services:
  basesvc:
    image: y
    cpu_percent: 50
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    cpu_percent: 2000
`}}, true},
		{"cpu_percent=2000: a sibling of the extended service, not extended", []file{{"base.yaml", `services:
  basesvc:
    image: y
    cpu_percent: 50
  other:
    image: z
    cpu_percent: 2000
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
`}}, false},
		{"cpu_percent=2000: written in the extender itself", []file{{"base.yaml", `services:
  basesvc:
    image: y
    cpu_percent: 50
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    cpu_percent: 2000
`}}, true},
		{"chain: base extends a third file's bad value, the extender overrides into range", []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    oom_score_adj: 5
`}, {"base.yaml", `services:
  basesvc:
    image: y
    extends: {file: third.yaml, service: t}
`}, {"third.yaml", `services:
  t:
    image: z
    oom_score_adj: 2000
`}}, false},
		{"chain: the middle service overrides the third file's bad value, the extender does not", []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
`}, {"base.yaml", `services:
  basesvc:
    image: y
    extends: {file: third.yaml, service: t}
    oom_score_adj: 7
`}, {"third.yaml", `services:
  t:
    image: z
    oom_score_adj: 2000
`}}, false},
		{"chain: the bad value reaches the extender", []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
`}, {"base.yaml", `services:
  basesvc:
    image: y
    extends: {file: third.yaml, service: t}
`}, {"third.yaml", `services:
  t:
    image: z
    oom_score_adj: 2000
`}}, true},
		{"deploy.replicas -1 in the extended file, the extender writes 2", []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    deploy: {replicas: 2}
`}, {"base.yaml", `services:
  basesvc:
    image: y
    deploy: {replicas: -1}
`}}, false},
		{"scale -1 in the extended file, the extender writes 2", []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    scale: 2
`}, {"base.yaml", `services:
  basesvc:
    image: y
    scale: -1
`}}, false},
		{"scale -1 in the extended file, not written over", []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
`}, {"base.yaml", `services:
  basesvc:
    image: y
    scale: -1
`}}, true},
		{"cpu_percent 150 as text in the extended file, the extender writes a text in range", []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    cpu_percent: '50'
`}, {"base.yaml", `services:
  basesvc:
    image: y
    cpu_percent: '150'
`}}, false},
		{"two keys out of range in the extended file, the extender fixes one", []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    cpu_count: 2
`}, {"base.yaml", `services:
  basesvc:
    image: y
    cpu_count: -1
    oom_score_adj: 2000
`}}, true},
		{"an extended service with a profile", []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    profiles: [p]
`}, {"base.yaml", `services:
  basesvc:
    image: y
    oom_score_adj: 2000
`}}, true},
		{"a same-file extends with the bad value overridden into range", []file{{"compose.yaml", `services:
  basesvc:
    image: y
    oom_score_adj: 2000
  web:
    extends: basesvc
    oom_score_adj: 5
`}}, true},
		{"the extender resets the key with !reset", []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    oom_score_adj: !reset null
`}, {"base.yaml", `services:
  basesvc:
    image: y
    oom_score_adj: 2000
`}}, false},
		{"the extender writes null over it", []file{{"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    oom_score_adj: ~
`}, {"base.yaml", `services:
  basesvc:
    image: y
    oom_score_adj: 2000
`}}, true},
		{"a sibling's text that reads as no number, with the words of a bound in it", []file{{"base.yaml", `services:
  basesvc:
    image: y
  o:
    image: z
    cpu_percent: "a is above the most, b"
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
`}}, true},
		{"a sibling's text for scale, with the words of a bound in it", []file{{"base.yaml", `services:
  basesvc:
    image: y
  o:
    image: z
    scale: "x is below the least, y"
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
`}}, true},
		{"the extended service's text that reads as no number, with the words of a bound in it, the extender writes 50", []file{{"base.yaml", `services:
  basesvc:
    image: y
    cpu_percent: "a is above the most, b"
`}, {"compose.yaml", `services:
  web:
    image: x
    extends: {file: base.yaml, service: basesvc}
    cpu_percent: 50
`}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			main := filepath.Join(dir, "compose.yaml")
			_, strictErr := LoadFiles([]string{main}, nil)
			if (strictErr != nil) != tc.refused {
				t.Errorf("the strict load: err %v, docker compose refuses it: %v", strictErr, tc.refused)
			}
			if strictErr != nil && !strings.Contains(tc.name, "words of a bound") && !strings.Contains(strictErr.Error(), "the least") && !strings.Contains(strictErr.Error(), "the most") {
				t.Errorf("the refusal is not the range of a number: %v", strictErr)
			}
			if strictErr != nil && strings.Contains(tc.name, "words of a bound") && !strings.Contains(strictErr.Error(), "does not read as") {
				t.Errorf("the refusal is not the one for text that reads as no number: %v", strictErr)
			}
			// A value that stays says which files it may come from: the extending file and the one it extends, and for a chain one more.
			if strictErr != nil && (strings.Contains(tc.name, "not overridden") || strings.Contains(tc.name, "reaches the extender")) {
				want := "(or " + filepath.Join(dir, "base.yaml") + ", the file it extends"
				if strings.Contains(tc.name, "chain") {
					want += ", or a file that one extends"
				}
				if !strings.Contains(strictErr.Error(), want) {
					t.Errorf("the refusal does not name the files the value may come from (%q): %v", want, strictErr)
				}
			}
			p, err := LoadFilesEnvDirSoft([]string{main}, nil, "")
			if err != nil {
				t.Fatalf("the soft load stopped: %v", err)
			}
			if fault := p.CheckValueFaults(); (fault != nil) != tc.refused {
				t.Errorf("the soft load kept the refusal: %v, docker compose refuses it: %v", fault, tc.refused)
			}
		})
	}
}
