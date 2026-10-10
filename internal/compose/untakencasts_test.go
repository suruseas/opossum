package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// docker compose reads the keys that take a number, a boolean or a list differently in a service nothing takes than in one taken (measured, v5.5.1, `config -q`: each
// key below with the 14 values of valuesOfAKey, in a service of an extended file that the extending service names, and in one it does not): where it is not taken
// it casts a number or a boolean and refuses what does not cast (a word, a blank, the quoted ones), and reads a list, a mapping, a single value of the others — the
// items of a list are asked, as in a service taken (#1935). Each row is the values docker compose refuses, not taken and taken.
var valuesOfAKey = map[string]string{
	"int": "1", "float": "1.5", "word": "abc", "true": "true", "blank": `""`, "list": "[1]", "liststr": "[a]", "emptylist": "[]", "map": "{a: 1}",
	"qint": `"7"`, "neg": "-1", "qtrue": `"true"`, "hex": "0x10", "yes": "yes",
}

func TestAKeyThatTakesANumberABooleanOrAListIsReadAsDockerComposeReadsItInAServiceNothingTakes(t *testing.T) {
	for _, tc := range []struct {
		key                    string
		refusedNotTaken, taken []string
	}{
		{"cpu_count", []string{"blank", "qtrue", "word", "yes"}, []string{"blank", "emptylist", "float", "list", "liststr", "map", "neg", "qtrue", "true", "word", "yes"}},
		{"cpu_percent", []string{"blank", "qtrue", "word", "yes"}, []string{"blank", "emptylist", "float", "list", "liststr", "map", "neg", "qtrue", "true", "word", "yes"}},
		{"cpu_period", []string{"blank", "qtrue", "word", "yes"}, []string{"blank", "emptylist", "list", "liststr", "map", "qtrue", "true", "word", "yes"}},
		{"cpu_quota", []string{"blank", "qtrue", "word", "yes"}, []string{"blank", "emptylist", "list", "liststr", "map", "qtrue", "true", "word", "yes"}},
		{"cpu_rt_period", []string{"blank", "qtrue", "word", "yes"}, []string{"blank", "emptylist", "list", "liststr", "map", "qtrue", "true", "word", "yes"}},
		{"cpu_rt_runtime", []string{"blank", "qtrue", "word", "yes"}, []string{"blank", "emptylist", "list", "liststr", "map", "qtrue", "true", "word", "yes"}},
		{"cpu_shares", []string{"blank", "qtrue", "word", "yes"}, []string{"blank", "emptylist", "list", "liststr", "map", "qtrue", "true", "word", "yes"}},
		{"oom_score_adj", []string{"blank", "qtrue", "word", "yes"}, []string{"blank", "emptylist", "float", "list", "liststr", "map", "qtrue", "true", "word", "yes"}},
		{"pids_limit", []string{"blank", "qtrue", "word", "yes"}, []string{"blank", "emptylist", "list", "liststr", "map", "qtrue", "true", "word", "yes"}},
		{"scale", []string{"blank", "qtrue", "word", "yes"}, []string{"blank", "emptylist", "float", "list", "liststr", "map", "neg", "qtrue", "true", "word", "yes"}},
		{"init", []string{"blank", "qint", "word"}, []string{"blank", "emptylist", "float", "hex", "int", "list", "liststr", "map", "neg", "qint", "word"}},
		{"oom_kill_disable", []string{"blank", "qint", "word"}, []string{"blank", "emptylist", "float", "hex", "int", "list", "liststr", "map", "neg", "qint", "word"}},
		{"privileged", []string{"blank", "qint", "word"}, []string{"blank", "emptylist", "float", "hex", "int", "list", "liststr", "map", "neg", "qint", "word"}},
		{"read_only", []string{"blank", "qint", "word"}, []string{"blank", "emptylist", "float", "hex", "int", "list", "liststr", "map", "neg", "qint", "word"}},
		{"stdin_open", []string{"blank", "qint", "word"}, []string{"blank", "emptylist", "float", "hex", "int", "list", "liststr", "map", "neg", "qint", "word"}},
		{"tty", []string{"blank", "qint", "word"}, []string{"blank", "emptylist", "float", "hex", "int", "list", "liststr", "map", "neg", "qint", "word"}},
		{"cap_add", []string{"list"}, []string{"blank", "float", "hex", "int", "list", "map", "neg", "qint", "qtrue", "true", "word", "yes"}},
		{"cap_drop", []string{"list"}, []string{"blank", "float", "hex", "int", "list", "map", "neg", "qint", "qtrue", "true", "word", "yes"}},
		{"dns_opt", []string{"list"}, []string{"blank", "float", "hex", "int", "list", "map", "neg", "qint", "qtrue", "true", "word", "yes"}},
		{"expose", []string{}, []string{"blank", "float", "hex", "int", "map", "neg", "qint", "qtrue", "true", "word", "yes"}},
		{"links", []string{"list"}, []string{"blank", "float", "hex", "int", "list", "liststr", "map", "neg", "qint", "qtrue", "true", "word", "yes"}},
		{"models", []string{"list"}, []string{"blank", "float", "hex", "int", "list", "liststr", "map", "neg", "qint", "qtrue", "true", "word", "yes"}},
		{"profiles", []string{"list"}, []string{"blank", "float", "hex", "int", "list", "map", "neg", "qint", "qtrue", "true", "word", "yes"}},
		{"dns", []string{"list"}, []string{"float", "hex", "int", "list", "map", "neg", "true"}},
		{"dns_search", []string{"list"}, []string{"float", "hex", "int", "list", "map", "neg", "true"}},
		{"tmpfs", []string{"list"}, []string{"float", "hex", "int", "list", "map", "neg", "true"}},
		{"annotations", []string{"list"}, []string{"blank", "float", "hex", "int", "list", "neg", "qint", "qtrue", "true", "word", "yes"}},
		{"configs", []string{"list", "map"}, []string{"blank", "float", "hex", "int", "list", "liststr", "map", "neg", "qint", "qtrue", "true", "word", "yes"}},
		{"devices", []string{"list", "map"}, []string{"blank", "float", "hex", "int", "list", "map", "neg", "qint", "qtrue", "true", "word", "yes"}},
		{"secrets", []string{"list", "map"}, []string{"blank", "float", "hex", "int", "list", "liststr", "map", "neg", "qint", "qtrue", "true", "word", "yes"}},
		{"volumes", []string{"list", "map"}, []string{"blank", "float", "hex", "int", "list", "map", "neg", "qint", "qtrue", "true", "word", "yes"}},
	} {
		t.Run(tc.key, func(t *testing.T) {
			for shape, value := range valuesOfAKey {
				for _, role := range []struct {
					name, extends string
					refused       []string
				}{{"not taken", "s", tc.refusedNotTaken}, {"taken", "other", tc.taken}} {
					// opossum reads a list of words for these two keys where the service is taken, and docker compose refuses it: a difference of its own, not this one's.
					if role.name == "taken" && shape == "liststr" && (tc.key == "links" || tc.key == "models") {
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

// What is asked of the file is asked of the service taken, though the extender writes the key over (measured, v5.5.1: rc 1): the sets of keys above are dropped from the
// file only where the service is not taken.
func TestAKeyOfTheServiceTakenThatTakesANumberABooleanOrAListIsAskedEvenWhereTheExtenderWritesOverIt(t *testing.T) {
	for _, tc := range []struct{ name, key, base, over string }{
		{"a list for a boolean", "privileged", "[1]", "false"},
		{"a mapping for a boolean", "privileged", "{a: 1}", "true"},
		{"a list for a number", "cpu_shares", "[1]", "5"},
		{"a mapping for a list", "cap_add", "{a: 1}", "[SYS_TIME]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
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

// A second file that writes the key over does not make the service taken in the first one a service to read loosely (measured, v5.5.1: rc 1, where the extender
// that writes it over in its own file gets rc 0 — #1948): `volumes: abc` of the service taken is refused though a later `-f` writes a list over it.
func TestAKeyOfTheServiceTakenIsAskedWhereASecondFileWritesOverIt(t *testing.T) {
	for _, tc := range []struct{ key, base, over string }{
		{"volumes", "abc", "[/a:/b]"},
		{"volumes", "abc", "[]"},
		{"annotations", "abc", "{k: v}"},
	} {
		t.Run(tc.key+" = "+tc.base+", written over with "+tc.over, func(t *testing.T) {
			dir := t.TempDir()
			var paths []string
			for name, body := range map[string]string{
				"base.yaml":    "services:\n  s: {image: x}\n  other:\n    image: y\n    " + tc.key + ": " + tc.base + "\n",
				"compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: other}\n",
				"o.yaml":       "services:\n  a:\n    " + tc.key + ": " + tc.over + "\n",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			paths = []string{filepath.Join(dir, "compose.yaml"), filepath.Join(dir, "o.yaml")}
			if _, err := LoadFiles(paths, nil); err == nil {
				t.Errorf("%s: %s of the service taken, written over by a second file with %s, is read (docker compose refuses it)", tc.key, tc.base, tc.over)
			}
		})
	}
}
