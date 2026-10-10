package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// A list or a mapping docker compose reads of the keys that take a number, a boolean or a list is read in a service nothing takes whatever is in it — a key or an entry that
// holds nothing too — and a date is read for a flag (measured, v5.5.1, `config -q`: each key below with the 10 values of valuesWithNothingInThem, in a service of an extended file that the
// extending service names and in one it does not; #1951). Each row is the values docker compose refuses, not taken and taken. The forms below where opossum answers otherwise
// than docker compose are the ones this change does not make (a date for a number in the service taken, a list of nothing for configs and secrets, devices, the
// mapping of nothing for models): left out of the comparison and named, so that a change of them is a change a test sees.
var valuesWithNothingInThem = map[string]string{
	"listnull": "[~]", "mapnull": "{a: ~}", "emptymap": "{}", "listmap": "[{a: 1}]", "listmapnull": "[{a: ~}]", "date": "2024-01-01", "ts": "2024-01-01T10:00:00Z",
	"listlistnull": "[[~]]", "nulllist": "[~, 1]", "listbool": "[true]",
}

var knownDifferencesNothing = map[string]bool{
	"configs/not taken/listmapnull": true,
	"configs/not taken/listnull":    true,
	"cpu_count/taken/date":          true,
	"cpu_count/taken/ts":            true,
	"cpu_percent/taken/date":        true,
	"cpu_percent/taken/ts":          true,
	"cpu_period/taken/date":         true,
	"cpu_period/taken/ts":           true,
	"cpu_quota/taken/date":          true,
	"cpu_quota/taken/ts":            true,
	"cpu_rt_period/taken/date":      true,
	"cpu_rt_period/taken/ts":        true,
	"cpu_rt_runtime/taken/date":     true,
	"cpu_rt_runtime/taken/ts":       true,
	"cpu_shares/taken/date":         true,
	"cpu_shares/taken/ts":           true,
	"devices/not taken/listmap":     true,
	"devices/not taken/listmapnull": true,
	"dns/taken/date":                true,
	"dns/taken/ts":                  true,
	"dns_search/taken/date":         true,
	"dns_search/taken/ts":           true,
	"models/taken/mapnull":          true,
	"oom_kill_disable/taken/date":   true,
	"oom_kill_disable/taken/ts":     true,
	"oom_score_adj/taken/date":      true,
	"oom_score_adj/taken/ts":        true,
	"pids_limit/taken/date":         true,
	"pids_limit/taken/ts":           true,
	"privileged/taken/date":         true,
	"privileged/taken/ts":           true,
	"scale/taken/date":              true,
	"scale/taken/ts":                true,
	"secrets/not taken/listmapnull": true,
	"secrets/not taken/listnull":    true,
	"stdin_open/taken/date":         true,
	"stdin_open/taken/ts":           true,
}

func TestAListOrAMappingWithNothingInItIsReadAsDockerComposeReadsItInAServiceNothingTakes(t *testing.T) {
	for _, tc := range []struct {
		key                    string
		refusedNotTaken, taken []string
	}{
		{"annotations", []string{"listbool", "listlistnull", "listmap", "listmapnull", "listnull", "nulllist"}, []string{"date", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "nulllist", "ts"}},
		{"cap_add", []string{"listbool", "listlistnull", "listmap", "listmapnull", "listnull", "nulllist"}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"cap_drop", []string{"listbool", "listlistnull", "listmap", "listmapnull", "listnull", "nulllist"}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"configs", []string{"listbool", "listlistnull", "listnull", "mapnull", "nulllist"}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"cpu_count", []string{}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"cpu_percent", []string{}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"cpu_period", []string{}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"cpu_quota", []string{}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"cpu_rt_period", []string{}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"cpu_rt_runtime", []string{}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"cpu_shares", []string{}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"cpus", []string{}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"devices", []string{"listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist"}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"dns", []string{"listbool", "listlistnull", "listmap", "listmapnull", "listnull", "nulllist"}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"dns_opt", []string{"listbool", "listlistnull", "listmap", "listmapnull", "listnull", "nulllist"}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"dns_search", []string{"listbool", "listlistnull", "listmap", "listmapnull", "listnull", "nulllist"}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"expose", []string{"listbool", "listlistnull", "listmap", "listmapnull", "listnull", "nulllist"}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"init", []string{}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"links", []string{"listbool", "listlistnull", "listmap", "listmapnull", "listnull", "nulllist"}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"models", []string{"listbool", "listlistnull", "listmap", "listmapnull", "listnull", "nulllist"}, []string{"date", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"oom_kill_disable", []string{}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"oom_score_adj", []string{}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"pids_limit", []string{}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"privileged", []string{}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"profiles", []string{"listbool", "listlistnull", "listmap", "listmapnull", "listnull", "nulllist"}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"read_only", []string{}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"scale", []string{}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"secrets", []string{"listbool", "listlistnull", "listnull", "mapnull", "nulllist"}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"stdin_open", []string{}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"tmpfs", []string{"listbool", "listlistnull", "listmap", "listmapnull", "listnull", "nulllist"}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"tty", []string{}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
		{"volumes", []string{"listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist"}, []string{"date", "emptymap", "listbool", "listlistnull", "listmap", "listmapnull", "listnull", "mapnull", "nulllist", "ts"}},
	} {
		t.Run(tc.key, func(t *testing.T) {
			for shape, value := range valuesWithNothingInThem {
				for _, role := range []struct {
					name, extends string
					refused       []string
				}{{"not taken", "s", tc.refusedNotTaken}, {"taken", "other", tc.taken}} {
					if knownDifferencesNothing[tc.key+"/"+role.name+"/"+shape] {
						continue
					}
					dir := t.TempDir()
					for name, body := range map[string]string{
						"base.yaml":    "services:\n  s: {image: x}\n  other:\n    image: y\n    " + tc.key + ": " + value + "\n",
						"compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: " + role.extends + "}\n",
					} {
						if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
							t.Fatal(err)
						}
					}
					_, err := Load(filepath.Join(dir, "compose.yaml"))
					want := false
					for _, r := range role.refused {
						want = want || r == shape
					}
					if (err != nil) != want {
						t.Errorf("%s: %s = %s: refused = %v, want %v (%v)", role.name, tc.key, value, err != nil, want, err)
					}
				}
			}
		})
	}
}

// What holds nothing is asked of the file of the service taken, though the extender writes the key over (measured, v5.5.1: rc 1): the early read of the lists and mappings that
// hold nothing is for a service nothing takes only.
func TestAListOrAMappingWithNothingInItOfTheServiceTakenIsAskedEvenWhereTheExtenderWritesOverIt(t *testing.T) {
	for _, tc := range []struct{ key, base, over string }{
		{"cpu_shares", "[~]", "5"},
		{"cpu_shares", "{a: ~}", "5"},
		{"cap_add", "{a: ~}", "[SYS_TIME]"},
		{"init", "[~]", "true"},
		{"cpus", "[~]", "2"},
	} {
		t.Run(tc.key+" = "+tc.base, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range map[string]string{
				"base.yaml":    "services:\n  other:\n    image: y\n    " + tc.key + ": " + tc.base + "\n",
				"compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: other}\n    " + tc.key + ": " + tc.over + "\n",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Load(filepath.Join(dir, "compose.yaml")); err == nil {
				t.Errorf("%s: %s of the service taken, written over with %s, is read (docker compose refuses it)", tc.key, tc.base, tc.over)
			}
		})
	}
}

// The early read of what holds nothing, and the list of mappings, go through an alias: the value (`cpu_shares: *l`), the key (`*k : [~]`) and an entry of the list
// (`configs: [*m]`) are what they stand for (measured, v5.5.1: rc 0 each, #1951); a `devices` entry that is not a source of a device is refused (rc 1).
func TestWhatHoldsNothingAndTheListOfMappingsGoThroughAnAlias(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		refused    bool
	}{
		{"a value that is an alias of a list that holds nothing", "x-l: &l [~]\nservices:\n  s: {image: x}\n  other:\n    image: y\n    cpu_shares: *l\n", false},
		{"a key that is an alias, of a list that holds nothing", "x-k: &k cpu_shares\nservices:\n  s: {image: x}\n  other:\n    image: y\n    *k : [~]\n", false},
		{"an entry of configs that is an alias of a mapping", "x-m: &m {a: 1}\nservices:\n  s: {image: x}\n  other:\n    image: y\n    configs: [*m]\n", false},
		{"a devices entry whose source is a number", "services:\n  s: {image: x}\n  other:\n    image: y\n    devices: [{source: 1}]\n", true},
		{"a devices entry with a target that is a number", "services:\n  s: {image: x}\n  other:\n    image: y\n    devices: [{source: /dev/a, target: 1}]\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range map[string]string{
				"base.yaml":    tc.text,
				"compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: s}\n",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Load(filepath.Join(dir, "compose.yaml")); (err != nil) != tc.refused {
				t.Errorf("refused = %v, want %v (%v)", err != nil, tc.refused, err)
			}
		})
	}
}
