package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// The keys of a service whose shapes docker compose reads one by one are read in a service nothing takes as they are in one taken only where docker compose asks the same
// of them (measured, v5.5.1, `config -q`: each key below with the 14 values of valuesOfAKeyToo, in a service of an extended file that the extending service names, and in one it
// does not; #1640). Each row is the values docker compose refuses, not taken and taken. The forms below where opossum answers otherwise than docker compose are the ones
// this change does not make: they are left out of the comparison and named, so that a change of them is a change a test sees.
var valuesOfAKeyToo = map[string]string{
	"int": "1", "float": "1.5", "word": "abc", "true": "true", "blank": `""`, "list": "[1]", "liststr": "[a]", "emptylist": "[]", "map": "{a: 1}",
	"qint": `"7"`, "neg": "-1", "yes": "yes", "null": "~", "listmap": "[{a: 1}]", "ts": "2024-01-01",
}

// knownDifferences are the (key, role, value) forms where opossum and docker compose differ before this change and after it.
var knownDifferences = map[string]bool{
	"ulimits/not taken/list": true, "ulimits/not taken/listmap": true, "env_file/not taken/list": true,
	"networks/taken/blank": true, "label_file/taken/word": true, "label_file/taken/blank": true, "label_file/taken/liststr": true, "label_file/taken/qint": true,
	"label_file/taken/yes": true, "label_file/taken/ts": true, "gpus/taken/word": true, "gpus/taken/blank": true, "gpus/taken/qint": true, "gpus/taken/yes": true,
	// docker compose refuses an `env_file` of the service taken that names a file that is not there; Load does not look for the file (the commands that read the project do).
	"env_file/taken/yes": true, "env_file/taken/word": true, "env_file/taken/qint": true, "env_file/taken/liststr": true,
}

func TestAKeyWhoseShapesAreReadOneByOneIsReadAsDockerComposeReadsItInAServiceNothingTakes(t *testing.T) {
	for _, tc := range []struct {
		key                    string
		refusedNotTaken, taken []string
	}{
		{"develop", []string{}, []string{"blank", "emptylist", "float", "int", "list", "listmap", "liststr", "map", "neg", "qint", "true", "word", "yes", "ts"}},
		{"networks", []string{"list", "listmap"}, []string{"blank", "float", "int", "list", "listmap", "liststr", "map", "neg", "null", "qint", "true", "word", "yes", "ts"}},
		{"ulimits", []string{"liststr"}, []string{"blank", "emptylist", "float", "int", "list", "listmap", "liststr", "neg", "null", "qint", "true", "word", "yes", "ts"}},
		{"label_file", []string{"list", "listmap", "map"}, []string{"blank", "float", "int", "list", "listmap", "liststr", "map", "neg", "null", "qint", "true", "word", "yes", "ts"}},
		{"gpus", []string{"float", "int", "map", "neg", "null", "true", "ts"}, []string{"float", "int", "list", "liststr", "map", "neg", "null", "true", "ts"}},
		{"env_file", []string{"float", "int", "listmap", "map", "neg", "null", "true", "ts"}, []string{"blank", "float", "int", "list", "listmap", "liststr", "map", "neg", "null", "qint", "true", "word", "yes", "ts"}},
	} {
		t.Run(tc.key, func(t *testing.T) {
			for shape, value := range valuesOfAKeyToo {
				for _, role := range []struct {
					name, extends string
					refused       []string
				}{{"not taken", "s", tc.refusedNotTaken}, {"taken", "other", tc.taken}} {
					if knownDifferences[tc.key+"/"+role.name+"/"+shape] {
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

// What is asked of the file is asked of the service taken, though the extender writes the key over (measured, v5.5.1: rc 1): the values above are dropped from the file
// only where the service is not taken.
func TestAKeyOfTheServiceTakenWhoseShapesAreReadOneByOneIsAskedEvenWhereTheExtenderWritesOverIt(t *testing.T) {
	for _, tc := range []struct{ name, key, base, over string }{
		{"a list of develop", "develop", "[1]", "{watch: []}"},
		{"a mapping of develop", "develop", "{a: 1}", "{watch: []}"},
		{"a number of networks", "networks", "1", "[default]"},
		{"a word of networks", "networks", "abc", "[default]"},
		{"true for networks", "networks", "true", "[default]"},
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

// A mapping of `ulimits` that docker compose refuses in a service nothing takes is refused here, and the one it reads (`{a: 1}`) is not (measured, v5.5.1).
func TestAUlimitsMappingDockerComposeRefusesIsRefusedInAServiceNothingTakes(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		refused     bool
	}{
		{"a limit that is a word", "{nofile: abc}", true},
		{"a limit that is a list", "{nofile: [1]}", true},
		{"a limit with a soft that is a word", "{nofile: {soft: abc, hard: 1}}", true},
		{"a limit that is a number", "{nofile: 1024}", false},
		{"a limit with a soft and a hard", "{nofile: {soft: 1, hard: 2}}", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range map[string]string{
				"base.yaml":    "services:\n  s: {image: x}\n  other:\n    image: y\n    ulimits: " + tc.value + "\n",
				"compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: s}\n",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Load(filepath.Join(dir, "compose.yaml")); (err != nil) != tc.refused {
				t.Errorf("ulimits: %s: refused = %v, want %v (%v)", tc.value, err != nil, tc.refused, err)
			}
		})
	}
}
