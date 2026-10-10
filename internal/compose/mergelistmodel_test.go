package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// What a merge key carries from a list of mappings (`<<: *b` over `x-b: &b [{…}, {…}]`) into a service is checked in the model the files make, as what is written there is:
// a build, a deploy, a gpu, a port, a bound or a size that docker compose refuses is refused when it comes by the list as well, and one it reads is read (v5.5.1, `config -q`,
// every row; #1853). The model is read apart from the service (`modelDoc`), and a read of it that dropped the list's contents would let these through unseen.
func TestWhatAMergeKeyCarriesFromAListIsCheckedInTheModel(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		refused     bool
	}{
		{"a deploy cpus that is a word", "{deploy: {resources: {limits: {cpus: abc}}}}", true},
		{"an oom_score_adj above its bound", "{oom_score_adj: 2000}", true},
		{"a cpu_percent above its bound", "{cpu_percent: 150}", true},
		{"a date in the cache_from of a build", "{build: {context: ., cache_from: [2024-01-01]}}", true},
		{"a build secret mode that is -0", "{build: {context: ., secrets: [{source: s, mode: -0}]}}", true},
		{"a gpus count that is a word", "{gpus: [{count: abc}]}", true},
		{"a port with a host_ip that is no address", "{ports: [{target: 80, host_ip: nothost}]}", true},
		{"a date for the network of a build", "{build: {context: ., network: 2024-01-01}}", true},
		{"a mem_limit that is a word", "{mem_limit: abc}", true},
		{"a ulimit that is a word", "{ulimits: {nofile: abc}}", true},
		{"an oom_score_adj within its bound", "{oom_score_adj: 5}", false},
		{"a deploy cpus that is a number in text", `{deploy: {resources: {limits: {cpus: "1"}}}}`, false},
	} {
		for _, form := range []struct{ name, text string }{
			{"direct", "x-b: &b " + tc.value + "\nservices:\n  s:\n    image: x\n    <<: *b\n"},
			{"by a list", "x-b: &b [" + tc.value + ", {labels: {a: b}}]\nservices:\n  s:\n    image: x\n    <<: *b\n"},
			{"by a list, with a key of its own", "x-b: &b [" + tc.value + "]\nservices:\n  s:\n    image: x\n    <<: *b\n    labels: {k: v}\n"},
		} {
			t.Run(tc.name+"/"+form.name, func(t *testing.T) {
				p := filepath.Join(t.TempDir(), "c.yaml")
				if err := os.WriteFile(p, []byte(form.text), 0o644); err != nil {
					t.Fatal(err)
				}
				if _, err := LoadFiles([]string{p}, nil); (err != nil) != tc.refused {
					t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
				}
			})
		}
	}
}
