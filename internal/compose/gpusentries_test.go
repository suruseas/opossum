package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// An entry of `gpus` with a `count` that is a string but not `all` (any case) or a whole number, or with a `count` beside `device_ids`, is refused as docker compose refuses it:
// by the project the files make, so an entry a later file leaves in place is refused and one it resets or overrides is not; a service nothing takes reads it (measured, v5.5.1,
// `config -q`, #1975).
func TestAGpusEntryThatDockerComposeRefusesIsRefusedInTheMergedProject(t *testing.T) {
	load := func(t *testing.T, files map[string]string, order ...string) error {
		t.Helper()
		dir := t.TempDir()
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		paths := make([]string, len(order))
		for i, f := range order {
			paths[i] = filepath.Join(dir, f)
		}
		_, err := LoadFiles(paths, nil)
		return err
	}
	one := func(spec string) map[string]string {
		return map[string]string{"compose.yaml": "services:\n  web:\n    image: wi\n    gpus: " + spec + "\n"}
	}
	for _, tc := range []struct {
		name, spec string
		refused    bool
	}{
		{"a count that is a word", "[{count: abc}]", true},
		{"a count that is `all `", `[{count: "all "}]`, true},
		{"a count that is blank", `[{count: ""}]`, true},
		{"a count that is 1e2", `[{count: "1e2"}]`, true},
		{"a count that is 0x1", `[{count: "0x1"}]`, true},
		{"a count that is 1_0", `[{count: "1_0"}]`, true},
		{"a count that is a number with a blank before it", `[{count: " 3"}]`, true},
		{"a count beside device_ids", "[{count: 2, device_ids: [a]}]", true},
		{"a count of 0 beside device_ids", "[{count: 0, device_ids: [a]}]", true},
		{"a count of all beside no device_ids", "[{count: all, device_ids: []}]", true},
		{"a bad entry after a good one", "[{count: all}, {count: abc}]", true},
		{"a bad entry before a good one", "[{count: abc}, {device_ids: [a]}]", true},
		{"all", "[{count: all}]", false},
		{"ALL", "[{count: ALL}]", false},
		{"a quoted whole number", `[{count: "3"}]`, false},
		{"a negative quoted whole number", `[{count: "-1"}]`, false},
		{"a whole number", "[{count: 2}]", false},
		{"device_ids and capabilities", "[{device_ids: [a], capabilities: [gpu]}]", false},
		{"a count, a driver and capabilities", "[{count: all, capabilities: [gpu], driver: x}]", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := load(t, one(tc.spec), "compose.yaml"); (err != nil) != tc.refused {
				t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
			}
		})
	}
	bad := "services:\n  web:\n    gpus: [{count: abc}]\n"
	for _, tc := range []struct {
		name    string
		files   map[string]string
		order   []string
		refused bool
	}{
		{"a bad entry, a later file adds a good one", map[string]string{"a.yaml": one("[{count: abc}]")["compose.yaml"], "b.yaml": "services:\n  web:\n    gpus: [{count: all}]\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"a bad entry, a later file resets the key", map[string]string{"a.yaml": one("[{count: abc}]")["compose.yaml"], "b.yaml": "services:\n  web:\n    gpus: !reset null\n"}, []string{"a.yaml", "b.yaml"}, false},
		{"a bad entry, a later file overrides the key", map[string]string{"a.yaml": one("[{count: abc}]")["compose.yaml"], "b.yaml": "services:\n  web:\n    gpus: !override [{count: all}]\n"}, []string{"a.yaml", "b.yaml"}, false},
		{"a good entry, a later file adds a bad one", map[string]string{"a.yaml": one("[{count: all}]")["compose.yaml"], "b.yaml": bad}, []string{"a.yaml", "b.yaml"}, true},
		{"the extended file has a bad entry and the extender adds a good one", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: y}\n    gpus: [{count: all}]\n", "base.yaml": "services:\n  y:\n    image: yi\n    gpus: [{count: abc}]\n"}, []string{"compose.yaml"}, true},
		{"the extended file has a bad entry and the extender writes none", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: y}\n", "base.yaml": "services:\n  y:\n    image: yi\n    gpus: [{count: abc}]\n"}, []string{"compose.yaml"}, true},
		{"the extended file has a bad entry and the extender overrides the key", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: y}\n    gpus: !override [{count: all}]\n", "base.yaml": "services:\n  y:\n    image: yi\n    gpus: [{count: abc}]\n"}, []string{"compose.yaml"}, false},
		{"the extended file has a bad entry and the extender resets the key", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: y}\n    gpus: !reset null\n", "base.yaml": "services:\n  y:\n    image: yi\n    gpus: [{count: abc}]\n"}, []string{"compose.yaml"}, false},
		{"a bad entry in the second service, in the order of their names", map[string]string{"compose.yaml": "services:\n  a:\n    image: ai\n  z:\n    image: zi\n    gpus: [{count: abc}]\n"}, []string{"compose.yaml"}, true},
		{"a count beside device_ids in the second service", map[string]string{"compose.yaml": "services:\n  a:\n    image: ai\n    gpus: [{count: all}]\n  z:\n    image: zi\n    gpus: [{count: 2, device_ids: [a]}]\n"}, []string{"compose.yaml"}, true},
		{"a bad entry in the service a file adds after the first", map[string]string{"a.yaml": "services:\n  a:\n    image: ai\n", "b.yaml": "services:\n  z:\n    image: zi\n    gpus: [{count: abc}]\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"a service nothing takes holds a bad entry", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n", "base.yaml": "services:\n  x:\n    image: xi\n  y:\n    image: yi\n    gpus: [{count: abc}]\n"}, []string{"compose.yaml"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := load(t, tc.files, tc.order...); (err != nil) != tc.refused {
				t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
			}
		})
	}
}
