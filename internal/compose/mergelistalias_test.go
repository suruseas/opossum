package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// A merge key that holds an alias to a list of mappings (`<<: *l` over `x-l: &l [*a, *b]`) merges those mappings, as docker compose reads it (v5.5.1,
// `config`, every row measured): in `ulimits`, `environment`, `labels` and an `x-` field, with a key of the mapping's own beside it, the
// earlier mapping of the list winning where two write one name. A list of lists, an alias to a list inside a list (`<<: [*l]`), and a
// merge key at the top of the document are refused, and one at the top of a service is read (#1539).
func TestAMergeKeyThatHoldsAnAliasToAListOfMappings(t *testing.T) {
	const head = "x-a: &a {nofile: 1024}\nx-b: &b {nproc: 2048}\nx-l: &l [*a, *b]\nx-m: &m [*a]\nx-e: &e [{A: '1'}, {B: '2'}, {A: '3'}]\n"
	for _, tc := range []struct {
		name, body string
		want       string // the names the service ends up with under the key (`key=n1,n2` with the value of each), or "error", or "error: " and a part of what it says (the layer that refuses it)
	}{
		{"ulimits <<: *l", "    ulimits:\n      <<: *l\n", "ulimits=nofile,nproc"},
		{"ulimits <<: *m (a list of one)", "    ulimits:\n      <<: *m\n", "ulimits=nofile"},
		{"ulimits <<: *l with a key of its own", "    ulimits:\n      <<: *l\n      core: 5\n", "ulimits=core,nofile,nproc"},
		{"ulimits <<: [*l]", "    ulimits:\n      <<: [*l]\n", "error: `<<:` merge requires a mapping or a list of mappings"},
		{"ulimits <<: [*a, *l]", "    ulimits:\n      <<: [*a, *l]\n", "error"},
		{"ulimits <<: [*m, *b]", "    ulimits:\n      <<: [*m, *b]\n", "error"},
		{"ulimits <<: [[*a]]", "    ulimits:\n      <<: [[*a]]\n", "error: `<<:` merge requires a mapping or a list of mappings"},
		{"environment <<: *e", "    environment:\n      <<: *e\n", "environment=A=1,B=2"},
		{"environment <<: *e with a key of its own", "    environment:\n      <<: *e\n      C: '4'\n", "environment=A=1,B=2,C=4"},
		{"environment <<: [*e]", "    environment:\n      <<: [*e]\n", "error: map merge requires map or sequence of maps"},
		{"labels <<: *e", "    labels:\n      <<: *e\n", "labels=A=1,B=2"},
		{"the top of a service <<: *e, whose names no service has", "    <<: *e\n", "error: is not a key docker compose takes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "c.yaml")
			if err := os.WriteFile(p, []byte(head+"services:\n  s:\n    image: x\n"+tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			project, err := LoadFiles([]string{p}, nil)
			if tc.want == "error" || strings.HasPrefix(tc.want, "error: ") {
				if err == nil {
					t.Fatal("docker compose refuses it, and it was read")
				}
				if said := strings.TrimPrefix(tc.want, "error: "); said != "error" && !strings.Contains(err.Error(), said) {
					t.Errorf("refused, but as %q, want it to say %q", err, said)
				}
				return
			}
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			svc := project.Services["s"]
			key, _, _ := strings.Cut(tc.want, "=")
			var got []string
			switch key {
			case "ulimits":
				for name := range svc.Ulimits {
					got = append(got, name)
				}
			case "environment":
				env, _ := svc.ResolvedEnv()
				got = append(got, env...)
			case "labels":
				got = append(got, svc.Labels...)
			}
			sort.Strings(got)
			joined := key + "=" + strings.Join(got, ",")
			if joined != tc.want {
				t.Errorf("got %q, want %q", joined, tc.want)
			}
		})
	}
}

// The same where the file is not the only one read: an `-f` file after another, an extended file, an included file, the top of the document
// (refused), the top of a service with the keys of a service in the list, an item of `ports`, and two keys of one file (#1539; docker compose v5.5.1 `config`).
func TestAMergeKeyThatHoldsAnAliasToAListOfMappingsInMoreThanOneFile(t *testing.T) {
	const head = "x-l: &l [{A: '1'}, {B: '2'}]\n"
	const svc = "services:\n  s:\n    image: x\n"
	for _, tc := range []struct {
		name    string
		files   map[string]string
		order   []string // the files given, in order
		refused bool
		want    string // what service `s` ends up with, as docker compose's `config` shows it, when it is not refused
	}{
		{"the second -f file writes it", map[string]string{"c.yaml": svc, "o.yaml": head + "services:\n  s:\n    environment:\n      <<: *l\n"}, []string{"c.yaml", "o.yaml"}, false, "image=x env=A=1,B=2"},
		{"the first -f file writes it", map[string]string{"c.yaml": head + svc + "    environment:\n      <<: *l\n", "o.yaml": "services:\n  s:\n    working_dir: /w\n"}, []string{"c.yaml", "o.yaml"}, false, "image=x env=A=1,B=2 working_dir=/w"},
		{"the extended file writes it", map[string]string{"base.yaml": head + "services:\n  b:\n    image: y\n    environment:\n      <<: *l\n", "c.yaml": "services:\n  s:\n    extends: {file: base.yaml, service: b}\n"}, []string{"c.yaml"}, false, "image=y env=A=1,B=2"},
		{"the extended service is in the same file", map[string]string{"c.yaml": head + "services:\n  b:\n    image: y\n    environment:\n      <<: *l\n  s:\n    extends: {service: b}\n"}, []string{"c.yaml"}, false, "image=y env=A=1,B=2"},
		{"the included file writes it", map[string]string{"sub.yaml": head + svc + "    environment:\n      <<: *l\n", "c.yaml": "include:\n  - sub.yaml\n"}, []string{"c.yaml"}, false, "image=x env=A=1,B=2"},
		{"the top of the document", map[string]string{"c.yaml": "x-l: &l [{name: foo}]\n<<: *l\n" + svc}, []string{"c.yaml"}, true, ""},
		{"the top of the document, with a section in the list", map[string]string{"c.yaml": "x-l: &l [{networks: {n: {}}}]\n<<: *l\n" + svc}, []string{"c.yaml"}, true, ""},
		{"the top of a service, a list of service blocks", map[string]string{"c.yaml": "x-l: &l [{image: x}, {command: [a]}]\nservices:\n  s:\n    <<: *l\n"}, []string{"c.yaml"}, false, "image=x command=a"},
		{"an item of ports", map[string]string{"c.yaml": "x-l: &l [{target: 80}, {published: 8080}]\nservices:\n  s:\n    image: x\n    ports:\n      - <<: *l\n"}, []string{"c.yaml"}, false, "image=x ports=8080:80"},
		{"an anchor under !reset that the environment of a service uses", map[string]string{"c.yaml": head + "x-r: !reset {a: &in {<<: *l}}\nservices:\n  s:\n    image: x\n    environment: *in\n"}, []string{"c.yaml"}, false, "image=x env=A=1,B=2"},
		{"an anchor under !reset that the labels of a service use", map[string]string{"c.yaml": head + "x-r: !reset {a: &in {<<: *l}}\nservices:\n  s:\n    image: x\n    labels: *in\n"}, []string{"c.yaml"}, false, "image=x labels=A=1,B=2"},
		{"an anchor that is an item of a !reset list", map[string]string{"c.yaml": head + "x-r: !reset [&in {<<: *l}]\nservices:\n  s:\n    image: x\n    environment: *in\n"}, []string{"c.yaml"}, false, "image=x env=A=1,B=2"},
		{"an anchor under !reset that a merge key of the environment holds", map[string]string{"c.yaml": head + "x-r: !reset {a: &in {<<: *l}}\nservices:\n  s:\n    image: x\n    environment:\n      <<: *in\n"}, []string{"c.yaml"}, false, "image=x env=A=1,B=2"},
		{"an anchor under a service that is reset, used by another service", map[string]string{"c.yaml": head + "services:\n  t: !reset\n    image: y\n    environment: &in {<<: *l}\n  s:\n    image: x\n    environment: *in\n"}, []string{"c.yaml"}, false, "image=x env=A=1,B=2"},
		{"an anchor under !reset that two keys of a service use", map[string]string{"c.yaml": head + "x-r: !reset {a: &in {<<: *l}}\nservices:\n  s:\n    image: x\n    environment: *in\n    labels: *in\n"}, []string{"c.yaml"}, false, "image=x env=A=1,B=2 labels=A=1,B=2"},
		{"an anchor under !reset that is a list, used by the ports of a service", map[string]string{"c.yaml": head + "x-pl: &pl [{target: 80}, {published: 8080}]\nx-r: !reset {a: &p [{<<: *pl}]}\nservices:\n  s:\n    image: x\n    ports: *p\n"}, []string{"c.yaml"}, false, "image=x ports=8080:80"},
		{"a block that holds itself", map[string]string{"c.yaml": head + "x-a: &a {k: *a}\nservices:\n  s:\n    image: x\n"}, []string{"c.yaml"}, true, ""},
		{"a block that holds itself, under !reset, used by a service", map[string]string{"c.yaml": head + "x-r: !reset {a: &a {k: *a}}\nservices:\n  s:\n    image: x\n    environment: *a\n"}, []string{"c.yaml"}, true, ""},
		{"the same anchor under !reset, without the !reset", map[string]string{"c.yaml": head + "x-r: {a: &in {<<: *l}}\nservices:\n  s:\n    image: x\n    environment: *in\n"}, []string{"c.yaml"}, false, "image=x env=A=1,B=2"},
		{"environment and labels of one file", map[string]string{"c.yaml": head + svc + "    environment:\n      <<: *l\n    labels:\n      <<: *l\n"}, []string{"c.yaml"}, false, "image=x env=A=1,B=2 labels=A=1,B=2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var paths []string
			for _, name := range tc.order {
				paths = append(paths, filepath.Join(dir, name))
			}
			project, err := LoadFiles(paths, nil)
			if (err != nil) != tc.refused {
				t.Fatalf("err %v, docker compose refuses it: %v", err, tc.refused)
			}
			if tc.refused {
				return
			}
			// The values, not only that it was read: a merge key whose mappings are dropped on the way is read as well.
			svc := project.Services["s"]
			env, _ := svc.ResolvedEnv()
			sort.Strings(env)
			labels := append([]string(nil), svc.Labels...)
			sort.Strings(labels)
			got := []string{"image=" + svc.Image}
			if len(env) > 0 {
				got = append(got, "env="+strings.Join(env, ","))
			}
			if len(labels) > 0 {
				got = append(got, "labels="+strings.Join(labels, ","))
			}
			if len(svc.Command) > 0 {
				got = append(got, "command="+strings.Join(svc.Command, ","))
			}
			if svc.WorkingDir != "" {
				got = append(got, "working_dir="+svc.WorkingDir)
			}
			if len(svc.Ports) > 0 {
				got = append(got, "ports="+strings.Join(svc.Ports, ","))
			}
			if joined := strings.Join(got, " "); joined != tc.want {
				t.Errorf("the service is %q, want %q", joined, tc.want)
			}
		})
	}
}

// Under `!override` an alias to a list that a merge key holds is refused, where under `!reset` and without a tag it is read (docker compose v5.5.1
// `config`, every row measured, rc 1 or 0): in the value itself, in a block or a service inside it, in an `x-` field, in a later `-f` file, in an
// extended service (the same file or another) and in an included file. A merge key that holds a mapping, or a list of aliases to mappings, is read
// under `!override` as everywhere (#1843).
func TestAMergeKeyThatHoldsAnAliasToAListIsRefusedUnderOverride(t *testing.T) {
	const head = "x-e: &e [{A: '1'}, {B: '2'}]\nx-m: &m {A: '7'}\n"
	const svc = "services:\n  s:\n    image: x\n"
	for _, tc := range []struct {
		name    string
		files   map[string]string
		order   []string
		refused bool
	}{
		{"environment !override", map[string]string{"c.yaml": head + svc + "    environment: !override {<<: *e}\n"}, []string{"c.yaml"}, true},
		{"labels !override", map[string]string{"c.yaml": head + svc + "    labels: !override {<<: *e}\n"}, []string{"c.yaml"}, true},
		{"a block inside a service !override", map[string]string{"c.yaml": head + "services:\n  s: !override {image: x, environment: {<<: *e}}\n"}, []string{"c.yaml"}, true},
		{"the top of a service !override", map[string]string{"c.yaml": head + "services:\n  s: !override {<<: *e}\n"}, []string{"c.yaml"}, true},
		{"an x- field !override", map[string]string{"c.yaml": head + "x-n: !override {<<: *e}\n" + svc}, []string{"c.yaml"}, true},
		{"the second -f file", map[string]string{"c.yaml": svc, "o.yaml": head + "services:\n  s:\n    environment: !override {<<: *e}\n"}, []string{"c.yaml", "o.yaml"}, true},
		{"an extended service of the same file", map[string]string{"c.yaml": head + "services:\n  b:\n    image: y\n    environment: !override {<<: *e}\n  s:\n    extends: {service: b}\n"}, []string{"c.yaml"}, true},
		{"an extended service of another file", map[string]string{"base.yaml": head + "services:\n  b:\n    image: y\n    environment: !override {<<: *e}\n", "c.yaml": "services:\n  s:\n    extends: {file: base.yaml, service: b}\n"}, []string{"c.yaml"}, true},
		{"an included file", map[string]string{"sub.yaml": head + svc + "    environment: !override {<<: *e}\n", "c.yaml": "include:\n  - sub.yaml\n"}, []string{"c.yaml"}, true},
		{"environment !reset", map[string]string{"c.yaml": head + svc + "    environment: !reset {<<: *e}\n"}, []string{"c.yaml"}, false},
		{"no tag", map[string]string{"c.yaml": head + svc + "    environment: {<<: *e}\n"}, []string{"c.yaml"}, false},
		{"a mapping under !override", map[string]string{"c.yaml": head + svc + "    environment: !override {<<: *m}\n"}, []string{"c.yaml"}, false},
		{"a list of aliases to mappings under !override", map[string]string{"c.yaml": head + svc + "    environment: !override {<<: [*m]}\n"}, []string{"c.yaml"}, false},
		{"the tag on a key beside the merge key", map[string]string{"c.yaml": head + svc + "    environment:\n      <<: *e\n      Z: !override '9'\n"}, []string{"c.yaml"}, false},
		{"the tag on a sibling of the key", map[string]string{"c.yaml": head + svc + "    labels: !override {a: b}\n    environment: {<<: *e}\n"}, []string{"c.yaml"}, false},
		{"!override without a merge key", map[string]string{"c.yaml": head + svc + "    environment: !override {A: '1'}\n"}, []string{"c.yaml"}, false},
		// The same, as measured of the forms the first review found: a key that is quoted is no merge key; an anchor inside the value that is used
		// somewhere is read there, and one that is not used, or that sits beside an unanchored block, is not; the tag may stand on an item of a list, on
		// the value of a merge key, on a value that is anchored itself; and the merge key may be anywhere in the mapping, after another key or a tag.
		// A value written with `!reset` is thrown away, and what is inside it is not looked into, an `!override` among it too (#1859); under an `!override` a
		// `!reset` is looked through (the tags are not read there).
		{"an !override in a mapping under !reset", map[string]string{"c.yaml": head + "x-r: !reset {a: !override {<<: *e}}\n" + svc}, []string{"c.yaml"}, false},
		{"an !override in a mapping under !reset, in a service", map[string]string{"c.yaml": head + svc + "    labels: !reset {a: !override {<<: *e}}\n"}, []string{"c.yaml"}, false},
		{"an !override in a list under !reset", map[string]string{"c.yaml": head + "x-r: !reset [!override {<<: *e}]\n" + svc}, []string{"c.yaml"}, false},
		{"a service reset that holds an !override", map[string]string{"c.yaml": head + svc + "  t: !reset\n    image: y\n    environment: !override {<<: *e}\n"}, []string{"c.yaml"}, false},
		{"the second -f file resets a service and keeps an !override in it", map[string]string{"c.yaml": head + svc, "o.yaml": head + "services:\n  t: !reset\n    image: y\n    environment: !override {<<: *e}\n"}, []string{"c.yaml", "o.yaml"}, false},
		{"the second -f file resets a service with a mapping that holds an !override", map[string]string{"c.yaml": head + svc + "  t:\n    image: y\n", "o.yaml": head + "services:\n  t: !reset {environment: !override {<<: *e}}\n"}, []string{"c.yaml", "o.yaml"}, false},
		// What a `!reset` holds is read where an alias outside it stands for it (the alias is in the living part), and not where the alias is itself in a
		// `!reset` or is none at all; an anchor an `!override` holds that only a `!reset` uses is not used (the first review of #1861).
		{"an !override anchored inside a !reset, used by an alias at the top", map[string]string{"c.yaml": head + "x-r: !reset {a: &o !override {<<: *e}}\nx-y: *o\n" + svc}, []string{"c.yaml"}, true},
		{"a mapping anchored inside a !reset that holds an !override, used at the top", map[string]string{"c.yaml": head + "x-r: !reset {a: &o {b: !override {<<: *e}}}\nx-y: *o\n" + svc}, []string{"c.yaml"}, true},
		{"an !override anchored as an item of a !reset list, used at the top", map[string]string{"c.yaml": head + "x-r: !reset [&o !override {<<: *e}]\nx-y: *o\n" + svc}, []string{"c.yaml"}, true},
		{"an !override anchored inside a !reset, used by an alias in a service", map[string]string{"c.yaml": head + "x-r: !reset {a: &o !override {<<: *e}}\n" + svc + "    x-y: *o\n"}, []string{"c.yaml"}, true},
		{"an !override anchored in a reset service, used by another service", map[string]string{"c.yaml": head + "services:\n  t: !reset\n    image: y\n    labels: &o !override {<<: *e}\n  s:\n    image: x\n    x-q: *o\n"}, []string{"c.yaml"}, true},
		{"an !override anchored in a reset service, used by none", map[string]string{"c.yaml": head + "services:\n  t: !reset\n    image: y\n    labels: &o !override {<<: *e}\n  s:\n    image: x\n"}, []string{"c.yaml"}, false},
		{"an !override anchored inside a !reset, used as the value of a merge key", map[string]string{"c.yaml": head + "x-r: !reset {a: &o !override {<<: *e}}\nx-y: {<<: *o}\n" + svc}, []string{"c.yaml"}, true},
		{"an !override anchored inside a !reset, used by no alias", map[string]string{"c.yaml": head + "x-r: !reset {a: &o !override {<<: *e}}\n" + svc}, []string{"c.yaml"}, false},
		{"an !override anchored inside a !reset, used only inside another !reset", map[string]string{"c.yaml": head + "x-r: !reset {a: &o !override {<<: *e}}\nx-y: !reset {b: *o}\n" + svc}, []string{"c.yaml"}, false},
		{"an anchor an !override holds, used only inside a !reset", map[string]string{"c.yaml": head + "x-z: !override {X: &in {<<: *e}}\nx-r: !reset {a: *in}\n" + svc}, []string{"c.yaml"}, true},
		{"an anchor an !override holds, used inside a !reset and outside it", map[string]string{"c.yaml": head + "x-z: !override {X: &in {<<: *e}}\nx-r: !reset {a: *in}\nx-y: *in\n" + svc}, []string{"c.yaml"}, false},
		{"an anchor an !override holds, used by a node under a !reset that an alias outside it uses", map[string]string{"c.yaml": head + "x-z: !override {X: &in {<<: *e}}\nx-r: !reset {k: &a {Y: *in}}\nx-y: *a\n" + svc}, []string{"c.yaml"}, false},
		{"an anchor an !override holds, used by a node under a !reset that nothing outside uses", map[string]string{"c.yaml": head + "x-z: !override {X: &in {<<: *e}}\nx-r: !reset {k: &a {Y: *in}}\n" + svc}, []string{"c.yaml"}, true},
		// A `!reset` written on a list holds the aliases in it as one written on a mapping does (#1873).
		{"an anchor an !override holds, used only by a !reset list of aliases", map[string]string{"c.yaml": head + "x-z: !override {X: &in {<<: *e}}\nx-r: !reset [*in]\n" + svc}, []string{"c.yaml"}, true},
		{"an anchor an !override holds, used by a !reset list and outside it", map[string]string{"c.yaml": head + "x-z: !override {X: &in {<<: *e}}\nx-r: !reset [*in]\nx-y: *in\n" + svc}, []string{"c.yaml"}, false},
		{"an !override anchored inside a !reset, used only by another !reset list of aliases", map[string]string{"c.yaml": head + "x-r: !reset {a: &o !override {<<: *e}}\nx-y: !reset [*o]\n" + svc}, []string{"c.yaml"}, false},
		{"an !override anchored inside a !reset, used by a list of aliases outside it", map[string]string{"c.yaml": head + "x-r: !reset {a: &o !override {<<: *e}}\nx-y: [*o]\n" + svc}, []string{"c.yaml"}, true},
		{"a plain anchor inside a !reset, used at the top, with no !override", map[string]string{"c.yaml": head + "x-r: !reset {a: &o {<<: *e}}\nx-y: *o\n" + svc}, []string{"c.yaml"}, false},
		{"a !reset sibling before the !override", map[string]string{"c.yaml": head + svc + "    labels: !reset {a: b}\n    environment: !override {<<: *e}\n"}, []string{"c.yaml"}, true},
		{"an !override with a !reset sibling", map[string]string{"c.yaml": head + svc + "    environment: !override {<<: *e}\n    labels: !reset {a: b}\n"}, []string{"c.yaml"}, true},
		{"a !reset inside an !override", map[string]string{"c.yaml": head + svc + "    environment: !override {X: !reset {<<: *e}}\n"}, []string{"c.yaml"}, true},
		{"a quoted << is a key", map[string]string{"c.yaml": head + "x-z: !override {'<<': *e}\n" + svc}, []string{"c.yaml"}, false},
		{"an anchor inside, used by an alias in a service", map[string]string{"c.yaml": head + "x-z: !override {X: &in {<<: *e}}\n" + svc + "    labels: *in\n"}, []string{"c.yaml"}, false},
		{"an anchor inside, used by an alias at the top", map[string]string{"c.yaml": head + "x-z: !override {X: &in {<<: *e}}\nx-y: *in\n" + svc}, []string{"c.yaml"}, false},
		{"an anchor inside, used by no alias", map[string]string{"c.yaml": head + "x-z: !override {X: &in {<<: *e}}\n" + svc}, []string{"c.yaml"}, true},
		{"an anchor inside, used, beside an unanchored block", map[string]string{"c.yaml": head + "x-z: !override {X: &in {<<: *e}, W: {<<: *e}}\nx-y: *in\n" + svc}, []string{"c.yaml"}, true},
		{"the tagged value is anchored and used", map[string]string{"c.yaml": head + "x-z: !override &in {<<: *e}\nx-y: *in\n" + svc}, []string{"c.yaml"}, true},
		{"an anchored tagged value used as environment", map[string]string{"c.yaml": head + "x-o: &o !override {<<: *e}\nservices:\n  s:\n    image: x\n    environment: *o\n"}, []string{"c.yaml"}, true},
		{"an item of a list tagged !override", map[string]string{"c.yaml": head + "x-l: [!override {<<: *e}]\n" + svc}, []string{"c.yaml"}, true},
		{"a list tagged !override", map[string]string{"c.yaml": head + "x-l: !override [{<<: *e}]\n" + svc}, []string{"c.yaml"}, true},
		{"the value of a merge key tagged !override", map[string]string{"c.yaml": head + svc + "    <<: !override {environment: {<<: *e}}\n"}, []string{"c.yaml"}, true},
		{"another !override after it", map[string]string{"c.yaml": head + svc + "    environment: !override {<<: *e}\n    labels: !override {a: b}\n"}, []string{"c.yaml"}, true},
		{"the merge key after another key", map[string]string{"c.yaml": head + svc + "    environment: !override {Z: '1', <<: *e}\n"}, []string{"c.yaml"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var paths []string
			for _, name := range tc.order {
				paths = append(paths, filepath.Join(dir, name))
			}
			_, err := LoadFiles(paths, nil)
			if (err != nil) != tc.refused {
				t.Fatalf("err %v, docker compose refuses it: %v", err, tc.refused)
			}
			if tc.refused && !strings.Contains(err.Error(), "`<<: *e` stands for a list, inside a value written with `!override`") {
				t.Errorf("refused, but not for the merge key under !override: %v", err)
			}
		})
	}
}

// A lattice of merge keys (two ways to each block, twenty-eight levels, about a kilobyte) that sits under a `!reset` and is reached only by an alias that a service
// uses is read once for each block, and not once for each way down to it: what the spread of a list an alias stands for, which goes into the target of every alias,
// has to come back from at once, whether the file is then taken or refused (#1890). Two things stop it going through every way, the targets of aliases that were
// walked (`seen`) and every node that was (`visited`), and each of them does it alone: this row is red only where both are gone.
func TestALatticeUnderAResetUsedByAnAliasIsWalkedOnceForEachBlock(t *testing.T) {
	var b strings.Builder
	b.WriteString("x-e: &e [{A: '1'}]\nx-l0: &l0 {<<: *e}\n")
	for i := 1; i <= 28; i++ {
		fmt.Fprintf(&b, "x-l%d: &l%d {a%d: 1, <<: [*l%d, *l%d]}\n", i, i, i, i-1, i-1)
	}
	b.WriteString("x-r: !reset {a: &in {<<: *l28}}\nservices:\n  s:\n    image: x\n    environment: *in\n")
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		_, _ = LoadFiles([]string{path}, nil)
		done <- time.Since(start)
	}()
	select {
	case took := <-done:
		if took > 5*time.Second {
			t.Errorf("took %v: the walk goes through every way to a block", took)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("the walk of the merges did not come back in eight seconds: it goes through every way to a block")
	}
}
