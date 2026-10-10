package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// What docker compose answers (1 refuses, 0 reads) for each key of `build` of a service nothing takes, with `context: .` beside it, for each of twenty-two shapes of its value, in
// the order of buildShapes (measured, v5.5.1, `config -q`, #1958, #1970, #1980). `context` is left out (it is the one beside), and a key the schema does not have is `zzz_unknown`.
var buildAnswers = map[string]string{
	"dockerfile":          "0000000000000000000000",
	"dockerfile_inline":   "0000000000000000000000",
	"args":                "0000001001010101110011",
	"ssh":                 "1111111001110111110011",
	"labels":              "0000001001010101110011",
	"cache_from":          "0000000000000000000000",
	"cache_to":            "0000000000000000000000",
	"no_cache":            "0000000000000000000000",
	"additional_contexts": "1111111011111111111111",
	"network":             "0000000000000000000000",
	"provenance":          "0000000000000000000000",
	"sbom":                "0000000000000000000000",
	"pull":                "0000000000000000000000",
	"target":              "0000000000000000000000",
	"shm_size":            "0000000000000000000000",
	"extra_hosts":         "0000000000000000000000",
	"isolation":           "0000000000000000000000",
	"privileged":          "0000000000000000000000",
	"secrets":             "0000001010011001111110",
	"tags":                "0000001001010101110011",
	"ulimits":             "0000010100011001111110",
	"platforms":           "0000000000000000000000",
	"entitlements":        "0000000000000000000000",
	"zzz_unknown":         "0000000000000000000000",
}

var buildShapes = []string{"5", "1.5", "true", "abc", `""`, "[x]", "[1]", "{a: b}", "{a: 1}", "[{a: b}]", "~", "[~]", "{a: ~}", "[{a: ~}]", "2024-01-01", "[[x]]", "[true]", "[2024-01-01]", "{a: true}", "{a: 2024-01-01}", "[1.5]", "[{a: 1}]"}

// The forms where opossum reads what docker compose refuses, not made different here: `additional_contexts` of every shape but a mapping of words and a list of `name=context`,
// which are read for their names on purpose (TestAdditionalContextsAreReadForTheirNames).
var buildReadWronglyAlready = map[string]bool{
	"additional_contexts/5": true, "additional_contexts/1.5": true, "additional_contexts/true": true, "additional_contexts/abc": true, `additional_contexts/""`: true, "additional_contexts/[x]": true, "additional_contexts/[1]": true, "additional_contexts/{a: 1}": true, "additional_contexts/[{a: b}]": true, "additional_contexts/~": true, "additional_contexts/[~]": true, "additional_contexts/{a: ~}": true, "additional_contexts/[{a: ~}]": true, "additional_contexts/2024-01-01": true, "additional_contexts/[[x]]": true, "additional_contexts/[true]": true, "additional_contexts/[2024-01-01]": true, "additional_contexts/{a: true}": true, "additional_contexts/{a: 2024-01-01}": true, "additional_contexts/[1.5]": true, "additional_contexts/[{a: 1}]": true,
}

// loadWithUntaken loads a compose file whose `web` extends `x` of base.yaml, and base.yaml has `y` with what is under test: nothing takes `y` (target x) or `web` takes it
// (target y).
func loadWithUntaken(t *testing.T, target, ySpec string) error {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("compose.yaml", "services:\n  web:\n    extends: {file: base.yaml, service: "+target+"}\n")
	write("base.yaml", "x-b: &b {context: ., a: ~}\nx-e: &e [~]\nx-u: &u [{a: ~}]\nx-k: &k build\nx-m: &m {a: ~}\nx-um: &um {soft: ~}\nx-d: &d dockerfile\nx-a: &a args\nx-s: &s {soft: abc}\nx-us: &us {soft: abc}\nx-ul: &ul {nofile: {soft: abc}}\nservices:\n  x:\n    image: xi\n  y:\n    image: yi\n"+ySpec)
	_, err := LoadFiles([]string{filepath.Join(dir, "compose.yaml")}, nil)
	return err
}

func TestWhatDockerComposeReadsInTheBuildOfAServiceNothingTakesIsRead(t *testing.T) {
	for key, answers := range buildAnswers {
		for i, shape := range buildShapes {
			name := key + "/" + shape
			t.Run(name, func(t *testing.T) {
				field := key
				if key == "zzz_unknown" {
					field = "zzz"
				}
				err := loadWithUntaken(t, "x", "    build: {context: ., "+field+": "+shape+"}\n")
				want := answers[i] == '1'
				if buildReadWronglyAlready[name] {
					want = false
				}
				if (err != nil) != want {
					t.Errorf("refused = %v (%v), want %v (docker compose answers %c)", err != nil, err, want, answers[i])
				}
			})
		}
	}
}

func TestWhatDockerComposeReadsInEnvFileUlimitsAndBuildOfAServiceNothingTakesIsRead(t *testing.T) {
	for _, tc := range []struct {
		name, spec string
		refused    bool
	}{
		{"build: a key the schema has not, a null", "    build: {context: ., a: ~}\n", false},
		{"build: a key the schema has not, a null, beside a form docker compose refuses", "    build: {context: ., a: ~, args: [~]}\n", true},
		{"build: context a null", "    build: {context: ~}\n", true},
		{"build: an alias of the whole", "    build: *b\n", false},
		{"build: the key an alias", "    *k : {context: ., args: ~}\n", false},
		{"build: a key the schema has not, a list of a null", "    build: {context: ., a: [~]}\n", false},
		{"build: a key the schema has not, a mapping of a null", "    build: {context: ., a: {b: ~}}\n", false},
		{"build: a list of nulls through an alias", "    build: {context: ., cache_from: *e}\n", false},
		{"build: a mapping of nulls through an alias", "    build: {context: ., platforms: *m}\n", false},
		{"build: a list of a mapping of nulls through an alias", "    build: {context: ., dockerfile: [*m]}\n", false},
		{"build: a key of the schema that is an alias, a list of nulls", "    build: {context: ., *d : [~]}\n", false},
		{"build: a key of the schema that is an alias, a mapping of nulls", "    build: {context: ., *d : {a: ~}}\n", false},
		{"build: a key of the schema that is an alias, with a form docker compose refuses", "    build: {context: ., *a : [~]}\n", true},
		{"env_file: a whole number", "    env_file: [1]\n", false},
		{"env_file: a number and a word", "    env_file: [5, x]\n", false},
		{"env_file: true", "    env_file: [true]\n", false},
		{"env_file: a fraction", "    env_file: [1.5]\n", false},
		{"env_file: a list in the list", "    env_file: [[x]]\n", false},
		{"env_file: a long entry with a path and a required that is a word", "    env_file: [{path: x, required: abc}]\n", false},
		{"env_file: a long entry with a path and a key that is neither", "    env_file: [{path: x, a: 1}]\n", false},
		{"env_file: a long entry with a blank path", "    env_file: [{path: \"\"}]\n", false},
		{"env_file: a mapping of a number", "    env_file: [{a: 1}]\n", true},
		{"env_file: a long entry with nothing in it", "    env_file: [{}]\n", true},
		{"env_file: a long entry with a required only", "    env_file: [{required: true}]\n", true},
		{"env_file: a mapping for the list", "    env_file: {a: 1}\n", true},
		{"ulimits: a list of a whole number", "    ulimits: [1]\n", false},
		{"ulimits: a list of a mapping of a number", "    ulimits: [{a: 1}]\n", false},
		{"ulimits: a limit with a key that is neither", "    ulimits: {nofile: {a: 1}}\n", false},
		{"ulimits: a limit with a soft only", "    ulimits: {nofile: {soft: 1}}\n", false},
		{"ulimits: a limit with a soft and a key that is neither", "    ulimits: {nofile: {soft: 1, a: 1}}\n", false},
		{"ulimits: a limit with a hard that is a quoted number", "    ulimits: {nofile: {hard: \"7\"}}\n", false},
		{"ulimits: a limit with a hard that is a word", "    ulimits: {nofile: {hard: abc}}\n", true},
		{"ulimits: a list of a number and a word", "    ulimits: [5, x]\n", true},
		{"ulimits: a list of a fraction", "    ulimits: [1.5]\n", true},
		{"ulimits: a list of true", "    ulimits: [true]\n", true},
		{"ulimits: a limit that is a list of a number", "    ulimits: {nofile: [1]}\n", true},
		{"build: a list of a word and a null, which is no shape measured, is asked as it has been", "    build: {context: ., labels: [x, ~]}\n", true},
		{"ulimits: a list item with a word to cast: [{soft: abc}]", "    ulimits: [{soft: abc}]\n", true},
		{"ulimits: a list item with a word to cast: [{hard: abc}]", "    ulimits: [{hard: abc}]\n", true},
		{"ulimits: a list item with a word to cast: [{soft: 1, hard: abc}]", "    ulimits: [{soft: 1, hard: abc}]\n", true},
		{"ulimits: a list item with a word to cast: [{hard: ``}]", "    ulimits: [{hard: \"\"}]\n", true},
		{"ulimits: a list item with a word to cast: [{soft: `1.5`}]", "    ulimits: [{soft: \"1.5\"}]\n", true},
		{"ulimits: a list item with a word to cast: [{a: 1}, {soft: abc}]", "    ulimits: [{a: 1}, {soft: abc}]\n", true},
		{"ulimits: a list item with a word to cast: [*s]", "    ulimits: [*s]\n", true},
		{"ulimits: a list item with a word to cast: [{<<: {soft: abc}}]", "    ulimits: [{<<: {soft: abc}}]\n", true},
		{"ulimits: a merge key, asked as it has been: {<<: {nofile: {soft: abc}}}", "    ulimits: {<<: {nofile: {soft: abc}}}\n", true},
		{"ulimits: a merge key, asked as it has been: {<<: {nofile: abc}}", "    ulimits: {<<: {nofile: abc}}\n", true},
		{"ulimits: a merge key, asked as it has been: {<<: {nofile: [1]}}", "    ulimits: {<<: {nofile: [1]}}\n", true},
		{"ulimits: a merge key, asked as it has been: {<<: *ul}", "    ulimits: {<<: *ul}\n", true},
		{"ulimits: a merge key, asked as it has been: {nofile: {<<: {soft: abc}}}", "    ulimits: {nofile: {<<: {soft: abc}}}\n", true},
		{"ulimits: a merge key, asked as it has been: {nofile: {<<: {soft: abc}, hard: 1}}", "    ulimits: {nofile: {<<: {soft: abc}, hard: 1}}\n", true},
		{"ulimits: a merge key, asked as it has been: {nofile: {<<: *us}}", "    ulimits: {nofile: {<<: *us}}\n", true},
		{"build: a merge key beside a key that stands over what it brings", "    build: {context: ., <<: {args: [1]}, args: {a: b}}\n", false},
		{"build: a merge key beside a key of the same kind", "    build: {context: ., <<: {args: [1]}, args: [x]}\n", false},
		{"build: a merge key that brings a shape docker compose refuses", "    build: {context: ., <<: {ssh: x}}\n", true},
		{"build: a merge key beside a key that is read", "    build: {context: ., <<: *bm, dockerfile: 5}\n", true},
		{"build: ulimits a list of a fraction", "    build: {context: ., ulimits: [1.5]}\n", true},
		{"build: ulimits a list of true", "    build: {context: ., ulimits: [true]}\n", true},
		{"env_file: a long entry with the path after another key", "    env_file: [{a: 1, path: x}]\n", false},
		{"env_file: a long entry with the path after a format", "    env_file: [{format: 5, path: x}]\n", false},
		{"env_file: a long entry with a list for the path", "    env_file: [{path: [x]}]\n", true},
		{"env_file: a long entry with a mapping for the path", "    env_file: [{path: {a: b}}]\n", true},
		{"env_file: a list of a list of nothing", "    env_file: [[~]]\n", false},
		{"env_file: a long entry and a list in the list", "    env_file: [{path: x}, [x]]\n", false},
		{"env_file: a null", "    env_file: [~]\n", false},
		{"env_file: a null and a word", "    env_file: [~, x]\n", false},
		{"env_file: a word and a null", "    env_file: [x, ~]\n", false},
		{"env_file: a long entry and a null", "    env_file: [{path: x}, ~]\n", false},
		{"env_file: an alias of the list", "    env_file: *e\n", false},
		{"env_file: a long entry with a path of nothing", "    env_file: [{path: ~}]\n", true},
		{"env_file: a null beside a long entry with a path of nothing", "    env_file: [~, {path: ~}]\n", true},
		{"env_file: a mapping of nothing", "    env_file: {a: ~}\n", true},
		{"ulimits: a list of a mapping of nothing", "    ulimits: [{a: ~}]\n", false},
		{"ulimits: a list of a mapping of a null", "    ulimits: [{nofile: ~}]\n", false},
		{"ulimits: a limit with a soft of nothing", "    ulimits: {nofile: {soft: ~}}\n", false},
		{"ulimits: a limit with a hard of nothing beside a soft", "    ulimits: {nofile: {soft: 1, hard: ~}}\n", false},
		{"ulimits: a limit with a key of nothing", "    ulimits: {nofile: {a: ~}}\n", false},
		{"ulimits: an alias of the list", "    ulimits: *u\n", false},
		{"ulimits: a limit of a mapping through an alias", "    ulimits: {nofile: *um}\n", false},
		{"ulimits: a list of a mapping through an alias", "    ulimits: [*m]\n", false},
		{"ulimits: a limit with a soft of nothing and a hard that is a word", "    ulimits: {nofile: {soft: ~, hard: abc}}\n", true},
		{"ulimits: a limit with a soft of nothing and a hard that is an empty word", "    ulimits: {nofile: {soft: ~, hard: \"\"}}\n", true},
		{"ulimits: a limit with a soft that is a word and a hard of nothing", "    ulimits: {core: {soft: abc, hard: ~}}\n", true},
		{"ulimits: a limit with a word, beside one with a null", "    ulimits: {nofile: {soft: ~}, core: {soft: ~, hard: abc}}\n", true},
		{"ulimits: a limit with a word, and one that is a number", "    ulimits: {nofile: {soft: ~, hard: abc}, core: 1}\n", true},
		{"ulimits: a limit with a soft of nothing and a hard that is a quoted number", "    ulimits: {nofile: {soft: ~, hard: \"5\"}}\n", false},
		{"ulimits: a limit with a soft of nothing and a hard that is a fraction", "    ulimits: {nofile: {soft: ~, hard: 1.5}}\n", false},
		{"ulimits: a limit with a soft of nothing and a hard that is true", "    ulimits: {nofile: {soft: ~, hard: true}}\n", false},
		{"ulimits: a limit with a soft of nothing and a hard that is a list", "    ulimits: {nofile: {soft: ~, hard: [1]}}\n", false},
		{"ulimits: a limit with a soft of nothing and a key that is neither, a word", "    ulimits: {nofile: {soft: ~, extra: abc}}\n", false},
		{"ulimits: a limit that is a list of nulls", "    ulimits: {nofile: [~, ~]}\n", true},
		{"ulimits: a limit that is a list of a null and a number", "    ulimits: {nofile: [1, ~]}\n", true},
		{"ulimits: a limit of nothing", "    ulimits: {nofile: ~}\n", true},
		{"ulimits: a list of nothing", "    ulimits: [~]\n", true},
		{"ulimits: a limit that is a list of nothing", "    ulimits: {nofile: [~]}\n", true},
		{"ulimits: a limit of nothing beside one with a soft of nothing", "    ulimits: {nofile: ~, core: {soft: ~}}\n", true},
	} {
		t.Run("untaken/"+tc.name, func(t *testing.T) {
			if err := loadWithUntaken(t, "x", tc.spec); (err != nil) != tc.refused {
				t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
			}
		})
	}
	// Where the extending service takes it, docker compose refuses all of these (measured, v5.5.1): the reading is of a service nothing takes.
	for _, spec := range []string{"    build: {context: ., a: ~}\n", "    build: {context: ., args: ~}\n", "    env_file: [~]\n", "    env_file: [x, ~]\n", "    ulimits: [{a: ~}]\n", "    ulimits: {nofile: {soft: ~}}\n"} {
		t.Run("taken/"+spec, func(t *testing.T) {
			if err := loadWithUntaken(t, "y", spec); err == nil {
				t.Errorf("a service that is taken is read as it was not")
			}
		})
	}
}

// A service that is taken has its nulls asked of the file, whatever the extender writes over (measured, v5.5.1: rc 1 for each of these but the last, where the key that
// held the null is written over by a mapping).
func TestWhatHoldsNothingInATakenServiceIsAskedWhateverIsWrittenOverIt(t *testing.T) {
	for _, tc := range []struct {
		name, spec, over string
		refused          bool
	}{
		{"build with a key of nothing", "    build: {context: ., a: ~}\n", "build: {context: .}", true},
		{"env_file with a null", "    env_file: [~]\n", "env_file: [x]", true},
		{"env_file with a word and a null, written over by an empty list", "    env_file: [x, ~]\n", "env_file: []", true},
		{"ulimits with a list of a mapping of nothing", "    ulimits: [{a: ~}]\n", "ulimits: {nofile: 1}", true},
		{"ulimits with a soft of nothing", "    ulimits: {nofile: {soft: ~}}\n", "ulimits: {core: 2}", true},
		{"build args of nothing, written over by a mapping", "    build: {context: ., args: ~}\n", "build: {context: ., args: {A: b}}", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  web:\n    extends: {file: base.yaml, service: y}\n    "+tc.over+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "base.yaml"), []byte("services:\n  y:\n    image: yi\n"+tc.spec), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFiles([]string{filepath.Join(dir, "compose.yaml")}, nil); (err != nil) != tc.refused {
				t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
			}
		})
	}
}
