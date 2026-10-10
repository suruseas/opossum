package compose

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// An `!override` written on an anchor that a plain anchor merges (`x-c: &c !override {labels: {z: '9'}}`, `x-b: &b {<<: *c}`) stands where the plain anchor is used, whole
// over the earlier files', as it does where the tagged anchor is merged by the service itself (#1825): by a merge key (`s: {<<: *b}`), through any number of plain anchors,
// beside the service's own keys, as the value of the key (`s: *b`), and in a key of the service (`labels: {<<: *b}`); and not through a merge of a list of anchors
// (`<<: [*b]`, or inside the plain anchor), which docker compose leaves plain; a `!reset` is read the same way as an `!override` in each of these (#1926). Every row is docker compose v5.5.1's `image`, `ports` (published:target) and `labels` of the service `s`
// (`config --format json`), with `working_dir` and `command`, over this base file, with the row's file merged after it with `-f`; `error` is where docker compose refuses it — the service stands
// whole and has neither an image nor a build context.
func TestAnOverrideReachedThroughAPlainAnchorStandsWhereTheAnchorIsUsed(t *testing.T) {
	const base = "services:\n  s:\n    image: a\n    ports:\n      - '80:80'\n    labels:\n      keep: me\n    working_dir: /base\n"
	for _, tc := range []struct {
		name, over, want string
	}{
		{"!override reached through a plain anchor, used by a merge key", "x-c: &c !override {labels: {z: '9'}}\nx-b: &b {<<: *c}\nservices:\n  s: {<<: *b}\n", "error"},
		{"through two plain anchors", "x-c: &c !override {labels: {z: '9'}}\nx-b: &b {<<: *c}\nx-a: &a {<<: *b}\nservices:\n  s: {<<: *a}\n", "error"},
		{"the plain anchor has a key of its own", "x-c: &c !override {labels: {z: '9'}}\nx-b: &b {<<: *c, ports: ['1:1']}\nservices:\n  s: {<<: *b}\n", "error"},
		{"the service writes its own image beside the merge key", "x-c: &c !override {labels: {z: '9'}}\nx-b: &b {<<: *c}\nservices:\n  s:\n    <<: *b\n    image: b\n", "image=b ports=[] labels={\"z\": \"9\"} working_dir=None command=null"},
		{"the plain anchor has a key of its own and the service an image", "x-c: &c !override {labels: {z: '9'}}\nx-b: &b {<<: *c, ports: ['1:1']}\nservices:\n  s:\n    <<: *b\n    image: x\n", "image=x ports=['1:1'] labels={\"z\": \"9\"} working_dir=None command=null"},
		{"the service is the alias to the plain anchor", "x-c: &c !override {labels: {z: '9'}}\nx-b: &b {<<: *c, image: x}\nservices:\n  s: *b\n", "image=x ports=[] labels={\"z\": \"9\"} working_dir=None command=null"},
		{"the plain anchor is merged into a key of the service", "x-c: &c !override {z: '9'}\nx-b: &b {<<: *c}\nservices:\n  s:\n    image: x\n    labels: {<<: *b}\n", "image=x ports=['80:80'] labels={\"z\": \"9\"} working_dir=/base command=null"},
		{"a list of anchors is left plain", "x-c: &c !override {labels: {z: '9'}}\nx-b: &b {<<: *c}\nservices:\n  s: {<<: [*b]}\n", "image=a ports=['80:80'] labels={\"keep\": \"me\", \"z\": \"9\"} working_dir=/base command=null"},
		{"no tag anywhere in the chain", "x-c: &c {labels: {z: '9'}}\nx-b: &b {<<: *c}\nservices:\n  s: {<<: *b}\n", "image=a ports=['80:80'] labels={\"keep\": \"me\", \"z\": \"9\"} working_dir=/base command=null"},
		{"the tagged anchor merged straight, as before", "x-c: &c !override {labels: {z: '9'}}\nservices:\n  s:\n    <<: *c\n    image: b\n", "image=b ports=[] labels={\"z\": \"9\"} working_dir=None command=null"},
		{"a plain anchor in the middle has keys of its own", "x-c: &c !override {labels: {z: '9'}}\nx-b: &b {<<: *c, working_dir: /hb}\nx-a: &a {<<: *b, command: [w]}\nservices:\n  s:\n    <<: *a\n    image: q\n", "image=q ports=[] labels={\"z\": \"9\"} working_dir=/hb command=[\"w\"]"},
		{"a plain anchor in the middle, the merge key written after its keys", "x-c: &c !override {labels: {z: '9'}}\nx-b: &b {working_dir: /hb, <<: *c}\nx-a: &a {command: [w], <<: *b}\nservices:\n  s:\n    <<: *a\n    image: q\n", "image=q ports=[] labels={\"z\": \"9\"} working_dir=/hb command=[\"w\"]"},
		{"the service's own key beats the middle anchor's", "x-c: &c !override {labels: {z: '9'}}\nx-b: &b {<<: *c, working_dir: /hb}\nservices:\n  s:\n    <<: *b\n    image: q\n    working_dir: /own\n", "image=q ports=[] labels={\"z\": \"9\"} working_dir=/own command=null"},
		{"a !reset reached through a plain anchor, used by a merge key", "x-c: &c !reset {labels: {z: '9'}}\nx-b: &b {<<: *c}\nservices:\n  s: {<<: *b}\n", "error"},
		{"a !reset through two plain anchors, with the service's image", "x-c: &c !reset {labels: {z: '9'}}\nx-b: &b {<<: *c}\nx-a: &a {<<: *b}\nservices:\n  s:\n    <<: *a\n    image: x\n", "image=x ports=[] labels={} working_dir=None command=null"},
		{"a !reset, the plain anchor has an image and a key of its own", "x-c: &c !reset {labels: {z: '9'}}\nx-b: &b {<<: *c, image: q, ports: ['1:1']}\nservices:\n  s: {<<: *b}\n", "image=q ports=['1:1'] labels={} working_dir=None command=null"},
		{"a !reset, the service is the alias to the plain anchor", "x-c: &c !reset {labels: {z: '9'}}\nx-b: &b {<<: *c, image: x}\nservices:\n  s: *b\n", "image=x ports=[] labels={} working_dir=None command=null"},
		{"a !reset, merged into a key of the service", "x-c: &c !reset {z: '9'}\nx-b: &b {<<: *c}\nservices:\n  s:\n    image: x\n    labels: {<<: *b}\n", "image=x ports=['80:80'] labels={} working_dir=/base command=null"},
		{"a !reset, a list of anchors is left plain", "x-c: &c !reset {labels: {z: '9'}}\nx-b: &b {<<: *c}\nservices:\n  s: {<<: [*b], image: x}\n", "image=x ports=['80:80'] labels={\"keep\": \"me\"} working_dir=/base command=null"},
		{"a list of anchors inside the plain anchor is left plain (the tagged one is the list's)", "x-c: &c !override {labels: {z: '9'}}\nx-b: &b {<<: [*c]}\nservices:\n  s: {<<: *b, image: x}\n", "image=x ports=['80:80'] labels={\"keep\": \"me\", \"z\": \"9\"} working_dir=/base command=null"},
		{"a !reset in the middle of an !override chain", "x-c: &c !override {labels: {z: '9'}}\nx-r: &r !reset {working_dir: /r}\nx-d: &d {<<: *r}\nservices:\n  s:\n    <<: *d\n    image: x\n", "image=x ports=[] labels={} working_dir=None command=null"},
		{"an !override written in place after the merge key of an anchor, used by a merge key", "x-b: &b {<<: !override {labels: {z: '9'}}}\nservices:\n  s: {<<: *b, image: x}\n", "image=x ports=[] labels={\"z\": \"9\"} working_dir=None command=null"},
		{"a !reset written in place after the merge key of an anchor, used by a merge key", "x-b: &b {<<: !reset {labels: {z: '9'}}}\nservices:\n  s: {<<: *b, image: x}\n", "image=x ports=[] labels={} working_dir=None command=null"},
		{"an in-place !override, the anchor has a key of its own", "x-b: &b {<<: !override {labels: {z: '9'}}, ports: ['1:1']}\nservices:\n  s: {<<: *b, image: x}\n", "image=x ports=['1:1'] labels={\"z\": \"9\"} working_dir=None command=null"},
		{"an in-place !override through two anchors", "x-b: &b {<<: !override {labels: {z: '9'}}}\nx-a: &a {<<: *b}\nservices:\n  s: {<<: *a, image: x}\n", "image=x ports=[] labels={\"z\": \"9\"} working_dir=None command=null"},
		{"an in-place !override, the service is the alias to the anchor", "x-b: &b {<<: !override {labels: {z: '9'}}, image: x}\nservices:\n  s: *b\n", "image=x ports=[] labels={\"z\": \"9\"} working_dir=None command=null"},
		{"an in-place !override, the service writes its own key beside the merge key", "x-b: &b {<<: !override {labels: {z: '9'}}}\nservices:\n  s:\n    <<: *b\n    image: x\n    working_dir: /own\n", "image=x ports=[] labels={\"z\": \"9\"} working_dir=/own command=null"},
		{"an in-place !override written in a list is left plain", "x-b: &b {<<: [!override {labels: {z: '9'}}]}\nservices:\n  s: {<<: *b, image: x}\n", "image=x ports=['80:80'] labels={\"keep\": \"me\", \"z\": \"9\"} working_dir=/base command=null"},
		{"an in-place !override written straight in the service, as before", "services:\n  s: {<<: !override {labels: {z: '9'}}, image: x}\n", "image=x ports=[] labels={\"z\": \"9\"} working_dir=None command=null"},
		{"an in-place mapping with no tag is plain", "x-b: &b {<<: {labels: {z: '9'}}}\nservices:\n  s: {<<: *b, image: x}\n", "image=x ports=['80:80'] labels={\"keep\": \"me\", \"z\": \"9\"} working_dir=/base command=null"},
		{"an in-place mapping with no tag is plain in a file that tags another anchor", "x-c: &c !override {unused: 1}\nx-b: &b {<<: {labels: {z: '9'}}}\nservices:\n  s: {<<: *b, image: x}\n", "image=x ports=['80:80'] labels={\"keep\": \"me\", \"z\": \"9\"} working_dir=/base command=null"},
		{"an !override list in place after the merge key of an anchor, of an alias", "x-c: &c {labels: {z: '9'}}\nx-b: &b {<<: !override [*c]}\nservices:\n  s: {<<: *b, image: x}\n", "image=x ports=[] labels={\"z\": \"9\"} working_dir=None command=null"},
		{"a !reset list in place after the merge key of an anchor, of an alias", "x-c: &c {labels: {z: '9'}}\nx-b: &b {<<: !reset [*c]}\nservices:\n  s: {<<: *b, image: x}\n", "image=x ports=[] labels={} working_dir=None command=null"},
		{"an !override list in place, of a mapping", "x-b: &b {<<: !override [{labels: {z: '9'}}]}\nservices:\n  s: {<<: *b, image: x}\n", "image=x ports=[] labels={\"z\": \"9\"} working_dir=None command=null"},
		{"a !reset list in place, of a mapping", "x-b: &b {<<: !reset [{labels: {z: '9'}}]}\nservices:\n  s: {<<: *b, image: x}\n", "image=x ports=[] labels={} working_dir=None command=null"},
		{"an !override list in place, empty", "x-b: &b {<<: !override []}\nservices:\n  s: {<<: *b, image: x}\n", "image=x ports=[] labels={} working_dir=None command=null"},
		{"a !reset list in place, empty", "x-b: &b {<<: !reset []}\nservices:\n  s: {<<: *b, image: x}\n", "image=x ports=[] labels={} working_dir=None command=null"},
		{"a mapping in place with a tag of another name is plain", "x-b: &b {<<: !foo {labels: {z: '9'}}}\nservices:\n  s: {<<: *b, image: x}\n", "image=x ports=['80:80'] labels={\"keep\": \"me\", \"z\": \"9\"} working_dir=/base command=null"},
		{"a mapping in place with the tag !!map is plain", "x-b: &b {<<: !!map {labels: {z: '9'}}}\nservices:\n  s: {<<: *b, image: x}\n", "image=x ports=['80:80'] labels={\"keep\": \"me\", \"z\": \"9\"} working_dir=/base command=null"},
		{"an in-place !override anchor merged into the labels of the service", "x-b: &b {<<: !override {z: '9'}}\nservices:\n  s:\n    image: x\n    labels: {<<: *b}\n", "image=x ports=['80:80'] labels={\"z\": \"9\"} working_dir=/base command=null"},
		{"an in-place !reset anchor merged into the labels of the service", "x-b: &b {<<: !reset {z: '9'}}\nservices:\n  s:\n    image: x\n    labels: {<<: *b}\n", "image=x ports=['80:80'] labels={} working_dir=/base command=null"},
		{"a mapping in place with a tag of another name is plain in a file that tags another anchor", "x-c: &c !override {unused: 1}\nx-b: &b {<<: !foo {labels: {z: '9'}}}\nservices:\n  s: {<<: *b, image: x}\n", "image=x ports=['80:80'] labels={\"keep\": \"me\", \"z\": \"9\"} working_dir=/base command=null"},
		{"a mapping in place with the tag !!map is plain in a file that tags another anchor", "x-c: &c !override {unused: 1}\nx-b: &b {<<: !!map {labels: {z: '9'}}}\nservices:\n  s: {<<: *b, image: x}\n", "image=x ports=['80:80'] labels={\"keep\": \"me\", \"z\": \"9\"} working_dir=/base command=null"},
		{"a !override list of an alias written straight after the merge key of the service", "x-c: &c {labels: {z: '9'}}\nservices:\n  s: {<<: !override [*c], image: x}\n", "image=x ports=[] labels={\"z\": \"9\"} working_dir=None command=null"},
		{"a !override list of a mapping written straight after the merge key of the service", "services:\n  s: {<<: !override [{labels: {z: '9'}}], image: x}\n", "image=x ports=[] labels={\"z\": \"9\"} working_dir=None command=null"},
		{"a !override empty list written straight after the merge key of the service", "services:\n  s: {<<: !override [], image: x}\n", "image=x ports=[] labels={} working_dir=None command=null"},
		{"an anchor that is itself an !override list of a mapping, merged by the service", "x-c: &c !override [{labels: {z: '9'}}]\nservices:\n  s: {<<: *c, image: x}\n", "image=x ports=[] labels={\"z\": \"9\"} working_dir=None command=null"},
		{"an anchor that is itself an !override empty list, merged by the service", "x-c: &c !override []\nservices:\n  s: {<<: *c, image: x}\n", "image=x ports=[] labels={} working_dir=None command=null"},
		{"an anchor that is itself an !override list, merged by a plain anchor the service merges", "x-c: &c !override [{labels: {z: '9'}}]\nx-b: &b {<<: *c}\nservices:\n  s: {<<: *b, image: x}\n", "image=x ports=[] labels={\"z\": \"9\"} working_dir=None command=null"},
		{"a !reset list of a mapping written straight after the merge key of the service", "services:\n  s: {<<: !reset [{labels: {z: '9'}}], image: x}\n", "image=x ports=[] labels={} working_dir=None command=null"},
		{"a !reset empty list written straight after the merge key of the service", "services:\n  s: {<<: !reset [], image: x}\n", "image=x ports=[] labels={} working_dir=None command=null"},
		{"an anchor that is itself a !reset list, merged by the service", "x-c: &c !reset [{labels: {z: '9'}}]\nservices:\n  s: {<<: *c, image: x}\n", "image=x ports=[] labels={} working_dir=None command=null"},
		{"a plain list of a mapping with an !override item stays plain", "services:\n  s: {<<: [!override {labels: {z: '9'}}], image: x}\n", "image=x ports=['80:80'] labels={\"keep\": \"me\", \"z\": \"9\"} working_dir=/base command=null"},
		{"a plain anchor list is plain", "x-c: &c [{labels: {z: '9'}}]\nservices:\n  s: {<<: *c, image: x}\n", "image=x ports=['80:80'] labels={\"keep\": \"me\", \"z\": \"9\"} working_dir=/base command=null"},
		{"a plain anchor list is plain in a file that tags another anchor", "x-d: &d !override {unused: 1}\nx-c: &c [{labels: {z: '9'}}]\nx-b: &b {<<: *c}\nservices:\n  s: {<<: *b, image: x}\n", "image=x ports=['80:80'] labels={\"keep\": \"me\", \"z\": \"9\"} working_dir=/base command=null"},
		{"an !override list merged into the labels of the service", "services:\n  s:\n    image: x\n    labels: {<<: !override [{z: '9'}]}\n", "image=x ports=['80:80'] labels={\"z\": \"9\"} working_dir=/base command=null"},
		{"an anchor that is an !override list merged into the labels of the service", "x-l: &l !override [{z: '9'}]\nservices:\n  s:\n    image: x\n    labels: {<<: *l}\n", "image=x ports=['80:80'] labels={\"z\": \"9\"} working_dir=/base command=null"},
		{"a !reset list anchor reached through a plain anchor", "x-l: &l !reset [{labels: {z: '9'}}]\nx-b: &b {<<: *l}\nservices:\n  s: {<<: *b, image: x}\n", "image=x ports=[] labels={} working_dir=None command=null"},
		{"a !reset list anchor reached through a plain anchor, the service is the alias", "x-l: &l !reset [{labels: {z: '9'}}]\nx-b: &b {<<: *l, image: x}\nservices:\n  s: *b\n", "image=x ports=[] labels={} working_dir=None command=null"},
		{"a !reset list anchor reached through a plain anchor that has a key of its own", "x-l: &l !reset [{labels: {z: '9'}}]\nx-b: &b {<<: *l, ports: ['1:1']}\nservices:\n  s: {<<: *b, image: x}\n", "image=x ports=['1:1'] labels={} working_dir=None command=null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var paths []string
			for i, text := range []string{base, tc.over} {
				p := filepath.Join(dir, "f"+string(rune('0'+i))+".yaml")
				if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, p)
			}
			project, err := LoadFiles(paths, nil)
			if tc.want == "error" {
				if err == nil {
					t.Fatal("load: docker compose refuses it, and it was read")
				}
				return
			}
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			svc := project.Services["s"]
			if svc == nil {
				t.Fatal("no service s")
			}
			ports := []string{}
			for _, p := range svc.Ports {
				ports = append(ports, strings.SplitN(strings.SplitN(p, "/", 2)[0], ":", 3)[0]+":"+lastPart(p))
			}
			sort.Strings(ports)
			byKey := map[string]string{}
			for _, l := range svc.Labels {
				k, v, _ := strings.Cut(l, "=")
				byKey[k] = v
			}
			labels, _ := json.Marshal(byKey)
			portText := "[]"
			if len(ports) > 0 {
				portText = "['" + strings.Join(ports, "', '") + "']"
			}
			got := "image=" + svc.Image + " ports=" + portText + " labels=" + string(labels) + " working_dir=" + orNone(svc.WorkingDir) + " command=" + cmdText(svc.Command)
			// docker compose's JSON puts a space after the colon and the comma in a map: the same text here.
			got = strings.NewReplacer(`":"`, `": "`, `","`, `", "`).Replace(got)
			if got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

// cmdText is the command as docker compose's JSON writes it: a list in brackets with quoted words, or `null`.
func cmdText(c Command) string {
	if len(c) == 0 {
		return "null"
	}
	b, _ := json.Marshal([]string(c))
	return strings.ReplaceAll(string(b), `","`, `", "`)
}

// orNone writes an unset field as docker compose's JSON leaves it, `None` in the rows above: the text of a missing key.
func orNone(v string) string {
	if v == "" {
		return "None"
	}
	return v
}
