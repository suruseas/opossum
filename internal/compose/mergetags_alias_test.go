package compose

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// An alias to an anchor that is itself tagged `!reset` or `!override` (`x-z: &z !reset [/z]` and
// `tmpfs: *z`) is read where it is used, as docker compose reads it: a `!reset` alias takes the key or the
// list item out (for every use of the anchor, and over an earlier file's value), an `!override` alias stands
// whole; and a merge key to such an anchored mapping (`<<: *c`) puts the tag on each key it merges in (#1058). The alias used to be read as the plain value it stands for (`tmpfs: [/z]`, `command: [null]`).
// Every row is docker compose v5.5.1's `command`, `tmpfs`, `environment` and the number of `ports` per service
// (`config --format json`), for the files in the order of `-f`.
func TestAnAliasToATaggedAnchorIsReadWhereItIsUsedAsDockerComposeReadsIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
		want  string
	}{
		{"F1 tmpfs: *z over &z !reset [/z]", []string{"x-z: &z !reset [/z]\nservices:\n  a:\n    image: x\n    tmpfs: *z\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":null}}`},
		{"F1b two uses", []string{"x-z: &z !reset [/z]\nservices:\n  a:\n    image: x\n    tmpfs: *z\n  b:\n    image: x\n    tmpfs: *z\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":null},"b":{"command":null,"environment":null,"ports":0,"tmpfs":null}}`},
		{"F2 command: *n over &n !reset null", []string{"x-n: &n !reset null\nservices:\n  a:\n    image: x\n    command: *n\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":null}}`},
		{"F3 list item *p over &p !reset", []string{"x-p: &p !reset \"8080:80\"\nservices:\n  a:\n    image: x\n    ports: [*p, \"9090:90\"]\n"},
			`{"a":{"command":null,"environment":null,"ports":1,"tmpfs":null}}`},
		{"F4 later file, tmpfs: *z reset", []string{"services:\n  a:\n    image: x\n    tmpfs: [/t]\n", "x-z: &z !reset []\nservices:\n  a:\n    image: x\n    tmpfs: *z\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":null}}`},
		{"F5 later file, tmpfs: *o override", []string{"services:\n  a:\n    image: x\n    tmpfs: [/t]\n", "x-o: &o !override [/u]\nservices:\n  a:\n    image: x\n    tmpfs: *o\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":["/u"]}}`},
		{"F5b single file, tmpfs: *o override", []string{"x-o: &o !override [/u]\nservices:\n  a:\n    image: x\n    tmpfs: *o\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":["/u"]}}`},
		{"F6 env value alias to reset null", []string{"x-n: &n !reset null\nservices:\n  a:\n    image: x\n    environment: {A: *n, B: \"1\"}\n"},
			`{"a":{"command":null,"environment":{"B":"1"},"ports":0,"tmpfs":null}}`},
		{"F6b later file env value alias to reset null", []string{"services:\n  a:\n    image: x\n    environment: {A: \"0\", B: \"1\"}\n", "x-n: &n !reset null\nservices:\n  a:\n    image: x\n    environment: {A: *n}\n"},
			`{"a":{"command":null,"environment":{"B":"1"},"ports":0,"tmpfs":null}}`},
		{"F7 same anchor in command and entrypoint", []string{"x-n: &n !reset null\nservices:\n  a:\n    image: x\n    command: *n\n    entrypoint: *n\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":null}}`},
		{"F8 alias to a reset scalar in a long-form port field", []string{"x-p: &p !reset \"8080\"\nservices:\n  a:\n    image: x\n    ports: [{target: 80, published: *p}]\n"},
			`{"a":{"command":null,"environment":null,"ports":1,"tmpfs":null}}`},
		{"F9 override scalar alias", []string{"x-s: &s !override 08080\nservices:\n  a:\n    image: x\n    environment: {A: *s}\n"},
			`{"a":{"command":null,"environment":{"A":"08080"},"ports":0,"tmpfs":null}}`},
		{"F10 alias to a plain value (no tag) as control", []string{"x-z: &z [/z]\nservices:\n  a:\n    image: x\n    tmpfs: *z\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":["/z"]}}`},
		{"G1 anchor defined inside a service, used in another", []string{"services:\n  a:\n    image: x\n    tmpfs: &z !reset [/z]\n  b:\n    image: x\n    tmpfs: *z\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":null},"b":{"command":null,"environment":null,"ports":0,"tmpfs":null}}`},
		{"G2 later file, anchor defined in the service itself and used twice", []string{"services:\n  a:\n    image: x\n    tmpfs: [/t]\n  b:\n    image: x\n    tmpfs: [/u]\n", "services:\n  a:\n    image: x\n    tmpfs: &z !reset []\n  b:\n    image: x\n    tmpfs: *z\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":null},"b":{"command":null,"environment":null,"ports":0,"tmpfs":null}}`},
		{"G3 override mapping alias in env, later file", []string{"services:\n  a:\n    image: x\n    environment: {A: \"1\", B: \"2\"}\n", "x-e: &e !override {C: \"3\"}\nservices:\n  a:\n    image: x\n    environment: *e\n"},
			`{"a":{"command":null,"environment":{"C":"3"},"ports":0,"tmpfs":null}}`},
		{"G4 override alias as a list item", []string{"x-o: &o !override /u\nservices:\n  a:\n    image: x\n    tmpfs: [*o, /b]\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":["/u","/b"]}}`},
		{"G5 reset alias in a list of strings, middle item", []string{"x-z: &z !reset /b\nservices:\n  a:\n    image: x\n    tmpfs: [/a, *z, /c]\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":["/a","/c"]}}`},
		{"G6 two anchors, one reset one override, in one service", []string{"x-o: &o !override [/u]\nx-n: &n !reset null\nservices:\n  a:\n    image: x\n    tmpfs: *o\n    command: *n\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":["/u"]}}`},
		{"G7 merge key to a tagged anchor", []string{"x-c: &c !override {tmpfs: [/u]}\nservices:\n  a:\n    image: x\n    <<: *c\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":["/u"]}}`},
		{"G8 reset alias in depends_on value", []string{"x-n: &n !reset null\nservices:\n  a:\n    image: x\n    depends_on: {db: *n}\n  db:\n    image: x\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":null},"db":{"command":null,"environment":null,"ports":0,"tmpfs":null}}`},
		{"G9 alias to a reset anchor under an !override value (not read)", []string{"x-n: &n !reset null\nservices:\n  a:\n    image: x\n    environment: !override {A: *n}\n"},
			`{"a":{"command":null,"environment":{"A":"null"},"ports":0,"tmpfs":null}}`},
		{"G10 reset alias, then the same anchor name reused after redefinition", []string{"x-z: &z !reset [/z]\nx-w: &z [/w]\nservices:\n  a:\n    image: x\n    tmpfs: *z\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":["/w"]}}`},
		{"H1 merge key to a reset-tagged anchor", []string{"x-c: &c !reset {tmpfs: [/u]}\nservices:\n  a:\n    image: x\n    <<: *c\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":null}}`},
		{"H1b merge key to reset anchor + own tmpfs", []string{"x-c: &c !reset {tmpfs: [/u], command: [x]}\nservices:\n  a:\n    image: x\n    <<: *c\n    tmpfs: [/own]\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":["/own"]}}`},
		{"H1c merge key to reset anchor, own command", []string{"x-c: &c !reset {tmpfs: [/u], command: [x]}\nservices:\n  a:\n    image: x\n    <<: *c\n    command: [own]\n"},
			`{"a":{"command":["own"],"environment":null,"ports":0,"tmpfs":null}}`},
		{"H1d later file: merge key to reset anchor over earlier", []string{"services:\n  a:\n    image: x\n    tmpfs: [/t]\n    command: [e]\n", "x-c: &c !reset {tmpfs: [/u], command: [x]}\nservices:\n  a:\n    image: x\n    <<: *c\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":null}}`},
		{"H1e later file: reset merge + own key in the later file", []string{"services:\n  a:\n    image: x\n    tmpfs: [/t]\n    command: [e]\n", "x-c: &c !reset {tmpfs: [/u], command: [x]}\nservices:\n  a:\n    image: x\n    <<: *c\n    tmpfs: [/own]\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":["/own"]}}`},
		{"H2 merge key to an override-tagged anchor, later file", []string{"services:\n  a:\n    image: x\n    tmpfs: [/t]\n", "x-c: &c !override {tmpfs: [/u]}\nservices:\n  a:\n    image: x\n    <<: *c\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":["/u"]}}`},
		{"H2b later file, override anchor via merge key + own key", []string{"services:\n  a:\n    image: x\n    tmpfs: [/t]\n    command: [e]\n", "x-c: &c !override {tmpfs: [/u], command: [x]}\nservices:\n  a:\n    image: x\n    <<: *c\n    command: [own]\n"},
			`{"a":{"command":["own"],"environment":null,"ports":0,"tmpfs":["/u"]}}`},
		{"H2c single file, override anchor via merge key", []string{"x-c: &c !override {tmpfs: [/u], command: [x]}\nservices:\n  a:\n    image: x\n    <<: *c\n"},
			`{"a":{"command":["x"],"environment":null,"ports":0,"tmpfs":["/u"]}}`},
		{"H5 merge key to an untagged anchor (control, tags inside not read)", []string{"services:\n  a:\n    image: x\n    tmpfs: [/t]\n", "x-c: &c {tmpfs: [/u]}\nservices:\n  a:\n    image: x\n    <<: *c\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":["/t","/u"]}}`},
		{"H6 merge list of anchors, one tagged", []string{"services:\n  a:\n    image: x\n    tmpfs: [/t]\n", "x-c: &c !override {tmpfs: [/u]}\nx-d: &d {command: [d]}\nservices:\n  a:\n    image: x\n    <<: [*c, *d]\n"},
			`{"a":{"command":["d"],"environment":null,"ports":0,"tmpfs":["/t","/u"]}}`},
		{"H7 merge key in a list-item mapping to a reset anchor", []string{"x-c: &c !reset {published: \"8080\"}\nservices:\n  a:\n    image: x\n    ports: [{<<: *c, target: 80}]\n"},
			`{"a":{"command":null,"environment":null,"ports":1,"tmpfs":null}}`},
		{"H3 anchor tagged inside an !override list, used elsewhere", []string{"x-w: !override [&o !override /u]\nservices:\n  a:\n    image: x\n    tmpfs: [*o, /b]\n"},
			`{"a":{"command":null,"environment":null,"ports":0,"tmpfs":["/u","/b"]}}`},
		{"H4 reset anchor inside an !override mapping, used elsewhere", []string{"x-w: !override {k: &n !reset null}\nservices:\n  a:\n    image: x\n    environment: {A: *n, B: \"1\"}\n"},
			`{"a":{"command":null,"environment":{"B":"1"},"ports":0,"tmpfs":null}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var paths []string
			for i, f := range tc.files {
				p := filepath.Join(dir, "f"+string(rune('0'+i))+".yaml")
				if err := os.WriteFile(p, []byte(f), 0o644); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, p)
			}
			project, err := LoadFiles(paths, nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			got := map[string]map[string]any{}
			for name, svc := range project.Services {
				env, _ := svc.ResolvedEnv()
				var e map[string]string
				if len(env) > 0 {
					e = map[string]string{}
					for _, kv := range env {
						k, v, _ := strings.Cut(kv, "=")
						e[k] = v
					}
				}
				var cmd any
				if len(svc.Command) > 0 {
					cmd = []string(svc.Command)
				}
				var tmpfs any
				if len(svc.Tmpfs) > 0 {
					tmpfs = []string(svc.Tmpfs)
				}
				var envAny any
				if e != nil {
					envAny = e
				}
				got[name] = map[string]any{"command": cmd, "tmpfs": tmpfs, "environment": envAny, "ports": len(svc.Ports)}
			}
			b, _ := json.Marshal(got)
			if string(b) != tc.want {
				t.Errorf("got  %s\nwant %s", b, tc.want)
			}
		})
	}
}

// The same, with each service's whole projection (image, command, tmpfs, environment, labels and the
// published and target of each port) and the cases a merge key gave trouble in: an alias to a tagged
// mapping in a list item shares the anchor (its tags are not read there, as inside any `!override`); a merge
// key to a tagged mapping puts the tag on the mapping that holds the key, which stands whole over the
// files before it (`!override`, with the anchor's keys in it; `!reset`, with none), however deep it is
// and with an `extends` beside it; one in a list item is not read. docker compose v5.5.1's answers.
func TestATaggedAnchorIsReadWhereItIsUsedWhateverTheWholeProjectionIs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
		want  string
	}{
		{"K1 list item alias to an override mapping holding a reset", []string{"x-o: &o !override {published: !reset \"8080\", target: 80}\nservices:\n  a: {image: x, ports: [*o]}\n  b: {image: x, ports: [*o]}\n"},
			`{"a":{"command":null,"environment":null,"image":"x","labels":null,"ports":["8080:80"],"tmpfs":null},"b":{"command":null,"environment":null,"image":"x","labels":null,"ports":["8080:80"],"tmpfs":null}}`},
		{"K5/K6 the same anchor in ports and labels", []string{"x-o: &o !override {published: !reset \"8080\", target: 80}\nservices:\n  a: {image: x, ports: [*o], labels: *o}\n  b: {image: x, ports: [*o]}\n"},
			`{"a":{"command":null,"environment":null,"image":"x","labels":{"published":"8080","target":"80"},"ports":["8080:80"],"tmpfs":null},"b":{"command":null,"environment":null,"image":"x","labels":null,"ports":["8080:80"],"tmpfs":null}}`},
		{"K2b override anchor holding a reset, merged into two services", []string{"x-c: &c !override {environment: {A: !reset null, B: \"1\"}}\nservices:\n  a: {image: x, <<: *c}\n  b: {image: x, <<: *c}\n"},
			`{"a":{"command":null,"environment":{"A":"null","B":"1"},"image":"x","labels":null,"ports":[],"tmpfs":null},"b":{"command":null,"environment":{"A":"null","B":"1"},"image":"x","labels":null,"ports":[],"tmpfs":null}}`},
		{"K3 the same, one service", []string{"x-c: &c !override {environment: {A: !reset null, B: \"1\"}}\nservices:\n  a: {image: x, <<: *c}\n"},
			`{"a":{"command":null,"environment":{"A":"null","B":"1"},"image":"x","labels":null,"ports":[],"tmpfs":null}}`},
		{"K9 later file merging it", []string{"services:\n  a: {image: x, environment: {Z: \"0\"}}\n", "x-c: &c !override {environment: {A: !reset null, B: \"1\"}}\nservices:\n  a: {image: x, <<: *c}\n"},
			`{"a":{"command":null,"environment":{"A":"null","B":"1"},"image":"x","labels":null,"ports":[],"tmpfs":null}}`},
		{"K11 whole service override via merge key", []string{"services:\n  a: {image: x, environment: {A: \"1\"}, labels: {l: \"1\"}, tmpfs: [/t]}\n", "x-c: &c !override {environment: {B: \"2\"}}\nservices:\n  a: {image: y, <<: *c}\n"},
			`{"a":{"command":null,"environment":{"B":"2"},"image":"y","labels":null,"ports":[],"tmpfs":null}}`},
		{"K12 the same with reset", []string{"services:\n  a: {image: x, environment: {A: \"1\"}, labels: {l: \"1\"}, tmpfs: [/t]}\n", "x-c: &c !reset {environment: {B: \"2\"}}\nservices:\n  a: {image: y, <<: *c}\n"},
			`{"a":{"command":null,"environment":null,"image":"y","labels":null,"ports":[],"tmpfs":null}}`},
		{"M1 environment mapping with a reset merge", []string{"services:\n  a: {image: x, environment: {A: \"1\", B: \"2\", C: \"3\"}}\n", "x-c: &c !reset {A: \"9\", B: \"9\"}\nservices:\n  a: {image: x, environment: {<<: *c, X: \"1\"}}\n"},
			`{"a":{"command":null,"environment":{"X":"1"},"image":"x","labels":null,"ports":[],"tmpfs":null}}`},
		{"M2 environment with an override merge", []string{"services:\n  a: {image: x, environment: {A: \"1\", B: \"2\", C: \"3\"}}\n", "x-c: &c !override {A: \"9\"}\nservices:\n  a: {image: x, environment: {<<: *c, X: \"1\"}}\n"},
			`{"a":{"command":null,"environment":{"A":"9","X":"1"},"image":"x","labels":null,"ports":[],"tmpfs":null}}`},
		{"M3 environment with an empty override merge", []string{"services:\n  a: {image: x, environment: {A: \"1\", B: \"2\", C: \"3\"}}\n", "x-c: &c !override {}\nservices:\n  a: {image: x, environment: {<<: *c, X: \"1\"}}\n"},
			`{"a":{"command":null,"environment":{"X":"1"},"image":"x","labels":null,"ports":[],"tmpfs":null}}`},
		{"E3 extends and an override merge in one file", []string{"x-c: &c !override {tmpfs: [/u]}\nservices:\n  base: {image: x, tmpfs: [/b]}\n  a: {extends: {service: base}, <<: *c}\n"},
			`{"a":{"command":null,"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":["/b","/u"]},"base":{"command":null,"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":["/b"]}}`},
		{"E4 extends and a reset merge in one file", []string{"x-c: &c !reset {tmpfs: [/u]}\nservices:\n  base: {image: x, tmpfs: [/b]}\n  a: {extends: {service: base}, <<: *c}\n"},
			`{"a":{"command":null,"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":["/b"]},"base":{"command":null,"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":["/b"]}}`},
		{"H1d later file, reset merge over earlier tmpfs and command", []string{"services:\n  a: {image: x, tmpfs: [/t], command: [e]}\n", "x-c: &c !reset {tmpfs: [/u], command: [x]}\nservices:\n  a: {image: x, <<: *c}\n"},
			`{"a":{"command":null,"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":null}}`},
		{"H1e later file, reset merge with own key", []string{"services:\n  a: {image: x, tmpfs: [/t], command: [e]}\n", "x-c: &c !reset {tmpfs: [/u], command: [x]}\nservices:\n  a: {image: x, <<: *c, tmpfs: [/own]}\n"},
			`{"a":{"command":null,"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":["/own"]}}`},
		{"H2b later file, override merge with own key", []string{"services:\n  a: {image: x, tmpfs: [/t], command: [e]}\n", "x-c: &c !override {tmpfs: [/u], command: [x]}\nservices:\n  a: {image: x, <<: *c, command: [own]}\n"},
			`{"a":{"command":["own"],"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":["/u"]}}`},
		{"H5 untagged anchor merge, control", []string{"services:\n  a: {image: x, tmpfs: [/t]}\n", "x-c: &c {tmpfs: [/u]}\nservices:\n  a: {image: x, <<: *c}\n"},
			`{"a":{"command":null,"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":["/t","/u"]}}`},
		{"H6 merge list", []string{"services:\n  a: {image: x, tmpfs: [/t]}\n", "x-c: &c !override {tmpfs: [/u]}\nx-d: &d {command: [d]}\nservices:\n  a: {image: x, <<: [*c, *d]}\n"},
			`{"a":{"command":["d"],"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":["/t","/u"]}}`},
		{"N2 merge of a tagged list anchor", []string{"x-z: &z !reset [/z]\nservices:\n  a: {image: x, <<: *z}\n"},
			`{"a":{"command":null,"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":null}}`},
		{"N2a merge of a reset list anchor", []string{"x-z: &z !reset [/z]\nservices:\n  a: {image: x, <<: *z}\n"},
			`{"a":{"command":null,"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":null}}`},
		{"N2b merge of a reset scalar anchor", []string{"x-z: &z !reset \"s\"\nservices:\n  a: {image: x, <<: *z}\n"},
			`{"a":{"command":null,"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":null}}`},
		{"N2e later file: merge of a reset list anchor over earlier", []string{"services:\n  a: {image: x, tmpfs: [/t]}\n", "x-z: &z !reset [/z]\nservices:\n  a: {image: x, <<: *z}\n"},
			`{"a":{"command":null,"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":null}}`},
		{"H8 override anchor defined inside an override value", []string{"x-w: !override {c: &c !override {tmpfs: [/u]}}\nservices:\n  a: {image: x, <<: *c}\n"},
			`{"a":{"command":null,"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":["/u"]}}`},
		{"H9 later file: the same over earlier tmpfs", []string{"services:\n  a: {image: x, tmpfs: [/t]}\n", "x-w: !override {c: &c !override {tmpfs: [/u]}}\nservices:\n  a: {image: x, <<: *c}\n"},
			`{"a":{"command":null,"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":["/u"]}}`},
		{"n1 inline override merge", []string{"services:\n  a: {image: x, tmpfs: [/t], environment: {A: \"1\"}}\n", "services:\n  a: {image: y, <<: !override {tmpfs: [/u]}}\n"},
			`{"a":{"command":null,"environment":null,"image":"y","labels":null,"ports":[],"tmpfs":["/u"]}}`},
		{"n2 inline reset merge", []string{"services:\n  a: {image: x, tmpfs: [/t], environment: {A: \"1\"}}\n", "services:\n  a: {image: y, <<: !reset {tmpfs: [/u]}}\n"},
			`{"a":{"command":null,"environment":null,"image":"y","labels":null,"ports":[],"tmpfs":null}}`},
		{"r5 merge list with a reset list anchor", []string{"x-z: &z !reset [/z]\nservices:\n  a: {image: x, labels: {k: v}, <<: [*z]}\n"},
			`{"a":{"command":null,"environment":null,"image":"x","labels":{"k":"v"},"ports":[],"tmpfs":null}}`},
		{"r5b merge list with a reset list anchor and a mapping", []string{"x-z: &z !reset [/z]\nx-d: &d {command: [d]}\nservices:\n  a: {image: x, <<: [*z, *d]}\n"},
			`{"a":{"command":["d"],"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":null}}`},
		{"T1 reset merge keeps the other services of the earlier file", []string{"services:\n  a: {image: x, tmpfs: [/t]}\n  b: {image: x, tmpfs: [/b]}\n", "x-c: &c !reset {command: [x]}\nservices:\n  a: {image: x, <<: *c}\n"},
			`{"a":{"command":null,"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":null},"b":{"command":null,"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":["/b"]}}`},
		{"T2 reset merge in environment keeps the other keys of the service", []string{"services:\n  a: {image: x, command: [e], environment: {A: \"1\", B: \"2\"}}\n", "x-c: &c !reset {A: \"9\"}\nservices:\n  a: {image: x, environment: {<<: *c, X: \"1\"}}\n"},
			`{"a":{"command":["e"],"environment":{"X":"1"},"image":"x","labels":null,"ports":[],"tmpfs":null}}`},
		{"T3 reset merge in labels keeps the service's other fields", []string{"services:\n  a: {image: x, tmpfs: [/t], labels: {l: \"1\", m: \"2\"}}\n", "x-c: &c !reset {l: \"9\"}\nservices:\n  a: {image: x, labels: {<<: *c}}\n"},
			`{"a":{"command":null,"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":["/t"]}}`},
		{"p1 override merge key keeps the other services of the earlier file", []string{"services:\n  a: {image: x, tmpfs: [/t]}\n  b: {image: x, tmpfs: [/b]}\n", "x-c: &c !override {image: y, command: [x]}\nservices:\n  a: {<<: *c}\n"},
			`{"a":{"command":["x"],"environment":null,"image":"y","labels":null,"ports":[],"tmpfs":null},"b":{"command":null,"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":["/b"]}}`},
		{"p2 override merge key in environment keeps the service's other fields", []string{"services:\n  a: {image: x, tmpfs: [/t], environment: {A: \"1\", B: \"2\"}}\n", "x-c: &c !override {A: \"9\"}\nservices:\n  a: {environment: {<<: *c, X: \"1\"}}\n"},
			`{"a":{"command":null,"environment":{"A":"9","X":"1"},"image":"x","labels":null,"ports":[],"tmpfs":["/t"]}}`},
		{"r7 merge list with a reset mapping anchor and a plain one", []string{"x-c: &c !reset {command: [c]}\nx-d: &d {tmpfs: [/d]}\nservices:\n  a: {image: x, <<: [*c, *d]}\n"},
			`{"a":{"command":null,"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":["/d"]}}`},
		{"r8 merge list with a reset mapping anchor, later file over earlier", []string{"services:\n  a: {image: x, command: [e], tmpfs: [/t]}\n", "x-c: &c !reset {command: [c]}\nx-d: &d {tmpfs: [/d]}\nservices:\n  a: {image: x, <<: [*c, *d]}\n"},
			`{"a":{"command":["e"],"environment":null,"image":"x","labels":null,"ports":[],"tmpfs":["/t","/d"]}}`},
		{"J1 two anchors in one list", []string{"x-p: &p !reset \"8080:80\"\nx-q: &q !override \"9090:90\"\nservices:\n  a: {image: x, ports: [*p, *q, \"7070:70\"]}\n"},
			`{"a":{"command":null,"environment":null,"image":"x","labels":null,"ports":["9090:90","7070:70"],"tmpfs":null}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var paths []string
			for i, f := range tc.files {
				p := filepath.Join(dir, "f"+string(rune('0'+i))+".yaml")
				if err := os.WriteFile(p, []byte(f), 0o644); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, p)
			}
			project, err := LoadFiles(paths, nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			got := map[string]map[string]any{}
			for name, svc := range project.Services {
				env, _ := svc.ResolvedEnv()
				e := map[string]string{}
				for _, kv := range env {
					k, v, _ := strings.Cut(kv, "=")
					e[k] = v
				}
				l := map[string]string{}
				for _, kv := range svc.Labels {
					k, v, _ := strings.Cut(kv, "=")
					l[k] = v
				}
				ports := []string{}
				for _, p := range svc.Ports {
					if !strings.Contains(p, ":") {
						p = ":" + p
					}
					ports = append(ports, p)
				}
				var cmd, tmpfs, envAny, lblAny any
				if len(svc.Command) > 0 {
					cmd = []string(svc.Command)
				}
				if len(svc.Tmpfs) > 0 {
					tmpfs = []string(svc.Tmpfs)
				}
				if len(e) > 0 {
					envAny = e
				}
				if len(l) > 0 {
					lblAny = l
				}
				got[name] = map[string]any{"image": svc.Image, "command": cmd, "tmpfs": tmpfs, "environment": envAny, "labels": lblAny, "ports": ports}
			}
			b, _ := json.Marshal(got)
			if string(b) != tc.want {
				t.Errorf("got  %s\nwant %s", b, tc.want)
			}
		})
	}
}

// A mapping in a list item that merges a `!reset` anchor takes it and merges nothing, as docker compose v5.5.1 reads it
// (what the port is left with is the target: the row of a long-form port in the table shows what the project holds).
func TestAMergeKeyInAListItemToAResetAnchorMergesNothing(t *testing.T) {
	for name, anchor := range map[string]string{"a list": "[/z]", "a mapping": `{published: "8080"}`} {
		t.Run(name, func(t *testing.T) {
			p, err := Load(writeTemp(t, "x-z: &z !reset "+anchor+"\nservices:\n  a: {image: x, ports: [{<<: *z, target: 80}]}\n"))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := p.Services["a"].Ports; len(got) != 1 || !strings.HasSuffix(got[0], ":80") && got[0] != "80" {
				t.Errorf("ports = %q, want the one port with target 80 and nothing merged in", got)
			}
		})
	}
}

// A tag on a service itself — written on it, brought by an alias, or by a merge key to a tagged mapping — leaves
// what an `include` brought in as it is, and the service is merged with it, as docker compose v5.5.1 reads it
// (measured; a field of the service, and a key under one, are read over an include as over an earlier file).
// The answers are docker's; the include brings `web: {image: x, tmpfs: [/a], environment: {A: "1"}}` and `db`.
func TestATagOnAServiceOverAnIncludeMergesWithTheIncludedOne(t *testing.T) {
	type want struct {
		image string
		tmpfs []string
		env   []string
	}
	for _, tc := range []struct {
		name string
		main string
		want want
	}{
		{"merge key to an override mapping", "x-c: &c !override {image: y, tmpfs: [/u]}\nservices:\n  web:\n    <<: *c\n",
			want{"y", []string{"/a", "/u"}, []string{"A=1"}}},
		{"merge key to a reset mapping", "x-c: &c !reset {}\nservices:\n  web:\n    image: y\n    <<: *c\n",
			want{"y", []string{"/a"}, []string{"A=1"}}},
		{"override written on the service", "services:\n  web: !override {image: y, tmpfs: [/u]}\n",
			want{"y", []string{"/a", "/u"}, []string{"A=1"}}},
		{"alias to an override mapping as the service", "x-s: &s !override {image: y, tmpfs: [/u]}\nservices:\n  web: *s\n",
			want{"y", []string{"/a", "/u"}, []string{"A=1"}}},
		{"reset written on the service", "services:\n  web: !reset null\n",
			want{"x", []string{"/a"}, []string{"A=1"}}},
		{"a field is still read", "services:\n  web: {tmpfs: !override [/u]}\n",
			want{"x", []string{"/u"}, []string{"A=1"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "inc.yaml"), []byte("services:\n  web: {image: x, tmpfs: [/a], environment: {A: \"1\"}}\n  db: {image: x}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			main := filepath.Join(dir, "a.yaml")
			if err := os.WriteFile(main, []byte("include: [inc.yaml]\n"+tc.main), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := LoadFiles([]string{main}, nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			web := p.Services["web"]
			if web.Image != tc.want.image || !reflect.DeepEqual([]string(web.Tmpfs), tc.want.tmpfs) || !reflect.DeepEqual([]string(web.Environment), tc.want.env) {
				t.Errorf("web = image %q tmpfs %q env %q, want image %q tmpfs %q env %q", web.Image, []string(web.Tmpfs), []string(web.Environment), tc.want.image, tc.want.tmpfs, tc.want.env)
			}
			if _, ok := p.Services["db"]; !ok {
				t.Errorf("db, brought by the include, is gone")
			}
		})
	}
}

// A merge key at the top of the file to a tagged anchor is not read (docker compose v5.5.1 reads it: `<<: *c` over
// `c: &c !reset {networks: {n: {}}}` brings no network, so a service that names `n` is refused there). The row
// holds what is given here: the network is merged in and the service goes on.
func TestAMergeKeyAtTheTopOfTheFileToATaggedAnchorIsNotRead(t *testing.T) {
	_, err := Load(writeTemp(t, "x-c: &c !reset {networks: {n: {}}}\n<<: *c\nservices:\n  a: {image: x, networks: [n]}\n"))
	if err != nil {
		t.Errorf("load: %v (docker compose refuses the undefined network; the merge key is not read here)", err)
	}
}

// A merge key to an `!override` anchor of a list or a scalar is refused, as docker compose v5.5.1 refuses it
// (`!reset` of the same merges nothing and is taken: see the table above).
func TestAMergeKeyToAnOverrideAnchorOfAListIsRefused(t *testing.T) {
	for name, anchor := range map[string]string{"a list": "[/z]", "a scalar": `"s"`} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeTemp(t, "x-z: &z !override "+anchor+"\nservices:\n  a: {image: x, <<: *z}\n")); err == nil {
				t.Error("loads; docker compose refuses a merge of it")
			}
		})
	}
}

// The anchor is read where it is used and is not copied: a lattice of tagged anchors, each level referring to
// the one below it many times over, is read in about as many steps as it has nodes — in a list item, in a
// value, through a merge key — where an earlier way opened every alias and a lattice of seven levels of ten
// took 23 seconds and 4 GB (#1058), and a copy of an anchor that was walked again at each level did the same
// from a list item. The work is counted in allocations, not in time: a count that does not depend on the
// machine, and a lattice small enough to be safe to read.
func TestAnAnchorLatticeIsNotExpanded(t *testing.T) {
	lattice := func(top, use string) string {
		var b strings.Builder
		b.WriteString("x-l0: &l0 !override [a, b, c]\n")
		for level := 1; level <= 6; level++ {
			b.WriteString("x-l" + string(rune('0'+level)) + ": &l" + string(rune('0'+level)) + " !override [")
			for i := 0; i < 10; i++ {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString("*l" + string(rune('0'+level-1)))
			}
			b.WriteString("]\n")
		}
		b.WriteString("x-c: &c !override {x-k: [*l6]}\n")
		return b.String() + top + "services:\n  a:\n    image: x\n" + use
	}
	for name, uses := range map[string][2]string{
		"a list item at the top":      {"x-use: [*l6]\n", ""},
		"a list item in a service":    {"", "    x-use: [*l6]\n"},
		"the same, three times":       {"x-use: [*l6, *l6, *l6]\n", ""},
		"a merge key to a lattice":    {"", "    <<: *c\n"},
		"a list item and a merge key": {"x-use: [*l6]\n", "    <<: *c\n"},
		"a value":                     {"", "    tmpfs: *l6\n"},
	} {
		t.Run(name, func(t *testing.T) {
			doc := lattice(uses[0], uses[1])
			path := writeTemp(t, doc)
			allocs := testing.AllocsPerRun(1, func() {
				_, _ = Load(path) // the file may be refused (a list where a field takes a string); what is counted is the work
			})
			if allocs > 100000 {
				t.Errorf("loading a lattice of tagged anchors took %.0f allocations", allocs)
			}
		})
	}
}

// An alias is left an alias, and what it stands for is decoded the way it is without a tag: a copy of the
// anchor put where the alias was (an earlier way to read `!override` there) is no alias to the decoder,
// which counts the aliases it opens and refuses a file that opens too many — so a large anchor used many
// times was decoded whole at each use, and a file of 200 KB took 2 GB where it took 60 MB without the tag.
// The work is counted in allocations, against the same file with no tag on the anchor.
func TestALargeTaggedAnchorUsedManyTimesCostsAsMuchAsWithoutTheTag(t *testing.T) {
	const items, uses = 1000, 1000
	list := "[" + strings.TrimSuffix(strings.Repeat("i, ", items), ", ") + "]"
	for name, build := range map[string]func(tag string) string{
		"a value in an extension": func(tag string) string {
			var b strings.Builder
			b.WriteString("x-big: &big " + tag + list + "\nx-use:\n")
			for i := 0; i < uses; i++ {
				b.WriteString("  k" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + "_" + itoa(i) + ": *big\n")
			}
			return b.String() + "services:\n  a: {image: x}\n"
		},
		"a merge key in an extension": func(tag string) string {
			var b strings.Builder
			b.WriteString("x-c: &c " + tag + "{x-k: " + list + "}\nx-use:\n")
			for i := 0; i < uses; i++ {
				b.WriteString("  m" + itoa(i) + ": {<<: *c}\n")
			}
			return b.String() + "services:\n  a: {image: x}\n"
		},
		"a merge key in each service": func(tag string) string {
			var b strings.Builder
			b.WriteString("x-c: &c " + tag + "{x-k: " + list + "}\nservices:\n")
			for i := 0; i < uses; i++ {
				b.WriteString("  s" + itoa(i) + ": {image: x, <<: *c}\n")
			}
			return b.String()
		},
		"a value in each service": func(tag string) string {
			var b strings.Builder
			b.WriteString("x-big: &big " + tag + list + "\nservices:\n")
			for i := 0; i < uses; i++ {
				b.WriteString("  s" + itoa(i) + ": {image: x, x-k: *big}\n")
			}
			return b.String()
		},
	} {
		t.Run(name, func(t *testing.T) {
			count := func(tag string) float64 {
				path := writeTemp(t, build(tag))
				return testing.AllocsPerRun(1, func() { _, _ = Load(path) })
			}
			plain, tagged := count(""), count("!override ")
			if tagged > plain*1.5+10000 {
				t.Errorf("loading it took %.0f allocations with the tag on the anchor and %.0f without", tagged, plain)
			}
		})
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
