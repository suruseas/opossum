package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The weight of a `blkio_config` is a number to docker compose: a string for it is refused whatever it holds (`"10"`, `""`, a word, a date), and so is a negative number; the
// value is asked of the project the files make, so a number a later file or the extender writes over a string is read, a string a later file writes over a number is not, and
// the entries of `weight_device` are put together, so one with a string stays beside what a later file adds (measured, v5.5.1, `config -q`, every row; #1862).
func TestTheWeightOfABlkioConfigIsANumber(t *testing.T) {
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
	for _, tc := range []struct {
		value   string
		refused bool
	}{
		{"10", false},
		{"\"10\"", true},
		{"10.5", true},
		{"\"10.5\"", true},
		{"\"\"", true},
		{"abc", true},
		{"true", true},
		{"~", true},
		{"2024-01-01", true},
		{"[10]", true},
		{"{a: 1}", true},
		{"0", false},
		{"-1", true},
		{"-1.0", true},
		{"-0", false},
		{"-0.0", false},
		{"-0.5", true},
		{"1e2", false},
		{"!!str 10", true},
		{"!!int \"10\"", false},
		{"\"-1\"", true},
		{".inf", true},
		{"-.inf", true},
		{".nan", true},
	} {
		t.Run("weight "+tc.value, func(t *testing.T) {
			err := load(t, map[string]string{"compose.yaml": "services:\n  web:\n    image: wi\n    blkio_config: {weight: " + tc.value + "}\n"}, "compose.yaml")
			if (err != nil) != tc.refused {
				t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
			}
		})
		t.Run("weight_device "+tc.value, func(t *testing.T) {
			err := load(t, map[string]string{"compose.yaml": "services:\n  web:\n    image: wi\n    blkio_config: {weight_device: [{path: /dev/sda, weight: " + tc.value + "}]}\n"}, "compose.yaml")
			if (err != nil) != tc.refused {
				t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
			}
			if tc.value == `"10"` && (err == nil || !strings.Contains(err.Error(), "weight_device[0].weight")) {
				t.Errorf("the refusal does not name the entry: %v", err)
			}
		})
	}
	str := "services:\n  web:\n    image: wi\n    blkio_config: {weight: \"10\"}\n"
	num := "services:\n  web:\n    image: wi\n    blkio_config: {weight: 20}\n"
	strOver := "services:\n  web:\n    blkio_config: {weight: \"10\"}\n"
	numOver := "services:\n  web:\n    blkio_config: {weight: 20}\n"
	wdStr := "services:\n  web:\n    image: wi\n    blkio_config: {weight_device: [{path: /dev/sda, weight: \"10\"}]}\n"
	for _, tc := range []struct {
		name    string
		files   map[string]string
		order   []string
		refused bool
	}{
		{"a number written over a string by a second file", map[string]string{"a.yaml": str, "b.yaml": numOver}, []string{"a.yaml", "b.yaml"}, false},
		{"a string written over a number by a second file", map[string]string{"a.yaml": num, "b.yaml": strOver}, []string{"a.yaml", "b.yaml"}, true},
		{"a number written over a string by the extender", map[string]string{"base.yaml": "services:\n  y:\n    image: yi\n    blkio_config: {weight: \"10\"}\n", "e.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: y}\n    blkio_config: {weight: 20}\n"}, []string{"e.yaml"}, false},
		{"a string in the service that is taken", map[string]string{"base.yaml": "services:\n  y:\n    image: yi\n    blkio_config: {weight: \"10\"}\n", "e.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: y}\n"}, []string{"e.yaml"}, true},
		{"a string in a service nothing takes", map[string]string{"base.yaml": "services:\n  y:\n    image: yi\n    blkio_config: {weight: \"10\"}\n  z:\n    image: zi\n", "e.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: z}\n"}, []string{"e.yaml"}, false},
		{"a string entry beside a number entry for the same device", map[string]string{"a.yaml": wdStr, "b.yaml": "services:\n  web:\n    blkio_config: {weight_device: [{path: /dev/sda, weight: 20}]}\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"a string entry beside an entry for another device", map[string]string{"a.yaml": wdStr, "b.yaml": "services:\n  web:\n    blkio_config: {weight_device: [{path: /dev/sdb, weight: 20}]}\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"a string entry gone with an override of the list", map[string]string{"a.yaml": wdStr, "b.yaml": "services:\n  web:\n    blkio_config: {weight_device: !override [{path: /dev/sdb, weight: 20}]}\n"}, []string{"a.yaml", "b.yaml"}, false},
		{"a string gone with a reset of the block", map[string]string{"a.yaml": str, "b.yaml": "services:\n  web:\n    blkio_config: !reset null\n"}, []string{"a.yaml", "b.yaml"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := load(t, tc.files, tc.order...); (err != nil) != tc.refused {
				t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
			}
		})
	}
	t.Run("the second entry is named by its place", func(t *testing.T) {
		err := load(t, map[string]string{"compose.yaml": "services:\n  web:\n    image: wi\n    blkio_config: {weight_device: [{path: /dev/sda, weight: 20}, {path: /dev/sdb, weight: \"10\"}]}\n"}, "compose.yaml")
		if err == nil || !strings.Contains(err.Error(), "weight_device[1].weight") {
			t.Errorf("want a refusal naming weight_device[1], got %v", err)
		}
	})
}
