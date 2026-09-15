package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// `!reset` and `!override` are read as docker compose v5.5.0 reads them
// (each row measured with `config --format json`): a reset key has no value
// once the files are merged, whatever was written after the tag; an override
// key has the later value whole, not merged with an earlier one. They used
// to be left unread: `!reset []` merged as an empty list (the earlier value
// stood), `!override [/u]` merged, and `!reset null` was the string "null".
func TestResetAndOverrideTagsReadAsDockerComposeReadsThem(t *testing.T) {
	const base = "name: demo\nservices:\n  web:\n    image: alpine\n    command: [echo, a]\n    init: false\n" +
		"    environment: [A=1, K=a]\n    tmpfs: [/t]\n    ports: [\"8080:80\"]\n    labels: {la: \"1\", lb: \"2\", lc: \"3\"}\n" +
		"    volumes: [\"./x:/x\"]\n    depends_on: [db]\n  db:\n    image: alpine\n"
	// Each row: the later file's web body, and what web reads as afterwards
	// ("-" where the key has no value).
	for _, tc := range []struct {
		over, field, want string
	}{
		{"tmpfs: !reset []", "tmpfs", "-"},
		{"tmpfs: !reset null", "tmpfs", "-"},
		{"tmpfs: !reset [/z]", "tmpfs", "-"},
		{"tmpfs: !override [/u]", "tmpfs", "/u"},
		{"tmpfs: [/u]", "tmpfs", "/t /u"}, // control: merged
		{"environment: !reset {}", "environment", "-"},
		{"environment: !override [B=2]", "environment", "B=2"},
		{"environment: [B=2]", "environment", "A=1 B=2 K=a"}, // control
		// A variable under an earlier list is not reached.
		{"environment: {A: !reset null}", "environment", "A=1 K=a"},
		{"ports: !reset []", "ports", "-"},
		{"ports: !override [\"9090:90\"]", "ports", "9090:90"},
		{"labels: !override {lb: \"2\"}", "labels", "lb=2"},
		{"labels: {la: !reset null}", "labels", "lb=2 lc=3"},
		{"volumes: !reset []", "volumes", "-"},
		{"depends_on: !reset []", "depends_on", "-"},
		{"command: !reset null", "command", "-"},
		{"command: !override [echo, b]", "command", "echo b"},
		// A tag on a list item: reset drops the item, override means nothing.
		{"tmpfs: [!reset /z]", "tmpfs", "/t"},
		{"tmpfs: [!override /u]", "tmpfs", "/t /u"},
		// Any other tag is not read.
		{"tmpfs: !foo [/u]", "tmpfs", "/t /u"},
		// An override value reads as its plain value: a bool is a bool.
		{"init: !override true", "init", "true"},
		// Inside an override value the tags are not read: A is the string
		// "null", B stands, and the earlier C is gone with the override.
		{"environment: !override {A: !reset null, B: \"2\"}", "environment", "A=null B=2"},
		// Two resets side by side each take their own key.
		{"labels: {la: !reset null, lb: !reset null}", "labels", "lc=3"},
	} {
		t.Run(tc.over, func(t *testing.T) {
			p := loadTagged(t, map[string]string{"a.yaml": base, "b.yaml": "services:\n  web:\n    " + tc.over + "\n"}, "a.yaml", "b.yaml")
			if got := fieldOf(t, p.Services["web"], tc.field); got != tc.want {
				t.Errorf("web %s = %q, want %q", tc.field, got, tc.want)
			}
		})
	}
}

// The same tags in a file alone, in an extends (same file, another file, and
// the extended file's own), under an include, across three -f files, on a
// variable of an earlier mapping, on a whole service, and in a later -f file
// whose service extends — each as docker compose v5.5.0 reads it (measured).
func TestResetAndOverrideTagsAcrossExtendsIncludeAndFiles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		load  []string
		// service -> "tmpfs | environment"
		want map[string]string
	}{
		{"a file alone", map[string]string{
			"c.yaml": "name: s\nservices:\n  web:\n    image: alpine\n    tmpfs: !reset [/t]\n    environment: !override [A=1]\n"},
			[]string{"c.yaml"}, map[string]string{"web": "- | A=1"}},
		{"extends in the same file", map[string]string{
			"c.yaml": "name: e\nservices:\n  base:\n    image: alpine\n    tmpfs: [/t]\n    environment: [A=1]\n  web:\n    extends: base\n    tmpfs: !reset []\n    environment: !override [B=2]\n"},
			[]string{"c.yaml"}, map[string]string{"base": "/t | A=1", "web": "- | B=2"}},
		{"extends from another file", map[string]string{
			"base.yaml": "services:\n  base:\n    image: alpine\n    tmpfs: [/t]\n    environment: [A=1]\n",
			"c.yaml":    "name: x\nservices:\n  web:\n    extends: {file: base.yaml, service: base}\n    tmpfs: !reset []\n    environment: !override [B=2]\n"},
			[]string{"c.yaml"}, map[string]string{"web": "- | B=2"}},
		{"the extended file's own reset", map[string]string{
			"base.yaml": "services:\n  base:\n    image: alpine\n    tmpfs: !reset [/q]\n    environment: [A=1]\n",
			"c.yaml":    "name: x\nservices:\n  web:\n    extends: {file: base.yaml, service: base}\n"},
			[]string{"c.yaml"}, map[string]string{"web": "- | A=1"}},
		{"include", map[string]string{
			"inc.yaml": "services:\n  web:\n    image: alpine\n    tmpfs: [/t]\n    environment: [A=1]\n",
			"c.yaml":   "name: i\ninclude: [inc.yaml]\nservices:\n  web:\n    tmpfs: !reset []\n"},
			[]string{"c.yaml"}, map[string]string{"web": "- | A=1"}},
		{"three -f files: reset, then a value", map[string]string{
			"f1.yaml": "name: t\nservices:\n  web:\n    image: alpine\n    tmpfs: [/t]\n",
			"f2.yaml": "services:\n  web:\n    tmpfs: !reset []\n",
			"f3.yaml": "services:\n  web:\n    tmpfs: [/u]\n"},
			[]string{"f1.yaml", "f2.yaml", "f3.yaml"}, map[string]string{"web": "/u | -"}},
		{"a variable of an earlier mapping", map[string]string{
			"g1.yaml": "name: t\nservices:\n  web:\n    image: alpine\n    environment: {A: \"1\", B: \"2\"}\n",
			"g2.yaml": "services:\n  web:\n    environment:\n      A: !reset null\n"},
			[]string{"g1.yaml", "g2.yaml"}, map[string]string{"web": "- | B=2"}},
		{"a whole service", map[string]string{
			"h1.yaml": "name: t\nservices:\n  web:\n    image: alpine\n  db:\n    image: alpine\n    tmpfs: [/t]\n",
			"h2.yaml": "services:\n  db: !reset {}\n"},
			[]string{"h1.yaml", "h2.yaml"}, map[string]string{"web": "- | -"}},
		{"a later -f file whose service extends and overrides", map[string]string{
			"k1.yaml": "name: t\nservices:\n  web:\n    image: alpine\n    environment: [A=1]\n    tmpfs: [/t]\n",
			"k2.yaml": "services:\n  web:\n    extends: base\n    tmpfs: !override [/u]\n  base:\n    image: alpine\n    tmpfs: [/b]\n    environment: [B=2]\n"},
			[]string{"k1.yaml", "k2.yaml"}, map[string]string{"base": "/b | B=2", "web": "/u | A=1 B=2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := loadTagged(t, tc.files, tc.load...)
			var names []string
			for n := range p.Services {
				names = append(names, n)
			}
			sort.Strings(names)
			var wantNames []string
			for n := range tc.want {
				wantNames = append(wantNames, n)
			}
			sort.Strings(wantNames)
			if !slices.Equal(names, wantNames) {
				t.Fatalf("services = %v, want %v", names, wantNames)
			}
			for n, want := range tc.want {
				got := fieldOf(t, p.Services[n], "tmpfs") + " | " + fieldOf(t, p.Services[n], "environment")
				if got != want {
					t.Errorf("%s = %q, want %q", n, got, want)
				}
			}
		})
	}
}

// In a file alone the value is decoded straight from the document, so an
// `!override` value must read as its plain value there too: `init: !override
// true` is the bool true (docker compose v5.5.0, measured), not refused.
func TestAnOverrideScalarInAFileAloneReadsAsItsValue(t *testing.T) {
	p := loadTagged(t, map[string]string{"c.yaml": "name: s\nservices:\n  web:\n    image: alpine\n    init: !override true\n"}, "c.yaml")
	if !p.Services["web"].Init {
		t.Errorf("want init true, got %v", p.Services["web"].Init)
	}
}

// Review cases, each measured against docker compose v5.5.0 (`config`).
func TestResetAndOverrideTagsAtTheEdges(t *testing.T) {
	// A key written twice is refused whether or not one of the two is
	// tagged, as docker compose refuses it and as a file without tags is.
	for _, tc := range []struct{ name, file string }{
		{"a field twice, the second reset", "name: t\nservices:\n  web:\n    image: alpine\n    tmpfs: [/t]\n    tmpfs: !reset []\n"},
		{"a field twice, the first reset", "name: t\nservices:\n  web:\n    image: alpine\n    tmpfs: !reset []\n    tmpfs: [/t]\n"},
		{"a service twice, the second reset", "name: t\nservices:\n  web:\n    image: alpine\n  web: !reset {}\n"},
		{"control: a field twice, untagged", "name: t\nservices:\n  web:\n    image: alpine\n    tmpfs: [/t]\n    tmpfs: [/u]\n"},
	} {
		t.Run("twice: "+tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "c.yaml"), []byte(tc.file), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFiles([]string{filepath.Join(dir, "c.yaml")}, nil); err == nil || !strings.Contains(err.Error(), "same key twice") {
				t.Errorf("want the key written twice refused, got %v", err)
			}
		})
	}
	t.Run("twice in a later -f file", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("name: t\nservices:\n  web:\n    image: alpine\n    tmpfs: [/t]\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "b.yaml"), []byte("services:\n  web:\n    tmpfs: [/u]\n    tmpfs: !reset []\n"), 0o644)
		if _, err := LoadFiles([]string{filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")}, nil); err == nil || !strings.Contains(err.Error(), "same key twice") {
			t.Errorf("want the key written twice refused, got %v", err)
		}
	})
	// The project's name is not read for tags: `name: !reset t2` is t2.
	t.Run("name: !reset t2 is t2", func(t *testing.T) {
		p := loadTagged(t, map[string]string{"c.yaml": "name: !reset t2\nservices:\n  web:\n    image: alpine\n"}, "c.yaml")
		if p.Name != "t2" {
			t.Errorf("want name t2, got %q", p.Name)
		}
	})
	// A later path of one include entry tags keys over the earlier paths.
	for _, tc := range []struct{ i2, want string }{{"tmpfs: !reset []", "-"}, {"tmpfs: !override [/u]", "/u"}, {"tmpfs: [/u]", "/t /u"}} {
		t.Run("include path [i1, i2], i2 "+tc.i2, func(t *testing.T) {
			p := loadTagged(t, map[string]string{
				"i1.yaml": "services:\n  web:\n    image: alpine\n    tmpfs: [/t]\n",
				"i2.yaml": "services:\n  web:\n    " + tc.i2 + "\n",
				"c.yaml":  "name: i\ninclude:\n  - path: [i1.yaml, i2.yaml]\n",
			}, "c.yaml")
			if got := fieldOf(t, p.Services["web"], "tmpfs"); got != tc.want {
				t.Errorf("want tmpfs %q, got %q", tc.want, got)
			}
		})
	}
	// Inside a list item written as a mapping, a reset key is taken out: the
	// item reads as `{target: 80}` does (docker compose: no published port).
	t.Run("a long-form port's published: !reset", func(t *testing.T) {
		p := loadTagged(t, map[string]string{
			"c.yaml":   "name: t\nservices:\n  web:\n    image: alpine\n    ports: [{target: 80, published: !reset \"8080\"}]\n",
			"ctl.yaml": "name: t\nservices:\n  web:\n    image: alpine\n    ports: [{target: 80}]\n",
		}, "c.yaml")
		ctl := loadTagged(t, map[string]string{"ctl.yaml": "name: t\nservices:\n  web:\n    image: alpine\n    ports: [{target: 80}]\n"}, "ctl.yaml")
		if got, want := fieldOf(t, p.Services["web"], "ports"), fieldOf(t, ctl.Services["web"], "ports"); got != want || strings.Contains(got, "8080") {
			t.Errorf("want the port read as {target: 80} (%q), got %q", want, got)
		}
	})
	// An earlier networks or depends_on list is read as the mapping it
	// merges as, so a later entry's reset reaches it.
	t.Run("networks [n1, n2] then {n1: !reset null}", func(t *testing.T) {
		p := loadTagged(t, map[string]string{
			"a.yaml": "name: t\nservices:\n  web:\n    image: alpine\n    networks: [n1, n2]\nnetworks:\n  n1: {}\n  n2: {}\n",
			"b.yaml": "services:\n  web:\n    networks:\n      n1: !reset null\n",
		}, "a.yaml", "b.yaml")
		names := []string(p.Services["web"].Networks)
		if !slices.Equal(names, []string{"n2"}) {
			t.Errorf("want networks [n2], got %v", names)
		}
	})
	t.Run("depends_on [db, db2] then {db: !reset null}", func(t *testing.T) {
		p := loadTagged(t, map[string]string{
			"a.yaml": "name: t\nservices:\n  web:\n    image: alpine\n    depends_on: [db, db2]\n  db:\n    image: alpine\n  db2:\n    image: alpine\n",
			"b.yaml": "services:\n  web:\n    depends_on:\n      db: !reset null\n",
		}, "a.yaml", "b.yaml")
		if got := fieldOf(t, p.Services["web"], "depends_on"); got != "db2" {
			t.Errorf("want depends_on db2, got %q", got)
		}
	})
	// An override value reads as its plain value where the decoder reads
	// the resolved tag: a count is a count.
	for _, files := range [][]string{{"c.yaml"}, {"a.yaml", "b.yaml"}} {
		t.Run("healthcheck retries: !override 3 "+strings.Join(files, " "), func(t *testing.T) {
			p := loadTagged(t, map[string]string{
				"c.yaml": "name: t\nservices:\n  web:\n    image: alpine\n    healthcheck: {test: [CMD, \"true\"], retries: !override 3}\n",
				"a.yaml": "name: t\nservices:\n  web:\n    image: alpine\n    healthcheck: {test: [CMD, \"true\"], retries: 5}\n",
				"b.yaml": "services:\n  web:\n    healthcheck:\n      retries: !override 3\n",
			}, files...)
			if hc := p.Services["web"].Healthcheck; hc == nil || hc.Retries != 3 {
				t.Errorf("want retries 3, got %+v", hc)
			}
		})
	}
	// A `name:` below the top is read for tags: a volume's `name: !reset null`
	// takes the earlier name away (docker compose: the default name).
	t.Run("a volume's name: !reset null", func(t *testing.T) {
		p := loadTagged(t, map[string]string{
			"a.yaml": "name: t\nservices:\n  web:\n    image: alpine\n    volumes: [\"vv:/v\"]\nvolumes:\n  vv:\n    name: custom\n",
			"b.yaml": "volumes:\n  vv:\n    name: !reset null\n",
		}, "a.yaml", "b.yaml")
		if got := p.Volumes["vv"].Name; got != "" {
			t.Errorf("want the earlier name taken away (no name written), got %q", got)
		}
	})
	// A bool written `!override true` is the bool true (docker compose), in a
	// file alone too, where the value is decoded straight from the document.
	for _, files := range [][]string{{"a.yaml", "b.yaml"}, {"s.yaml"}} {
		t.Run("read_only: !override true "+strings.Join(files, " "), func(t *testing.T) {
			p := loadTagged(t, map[string]string{
				"a.yaml": "name: t\nservices:\n  web:\n    image: alpine\n    read_only: false\n",
				"b.yaml": "services:\n  web:\n    read_only: !override true\n",
				"s.yaml": "name: t\nservices:\n  web:\n    image: alpine\n    read_only: !override true\n",
			}, files...)
			if !p.Services["web"].ReadOnly {
				t.Error("want read_only true")
			}
		})
	}
	// Inside a list item `!override` means nothing and the value stays a
	// tagged string: docker compose refuses `mode: !override 0755` in a
	// long-form tmpfs item (`expected type 'uint32', got … 'string'`), and so
	// does opossum; the same mode untagged is read.
	t.Run("a long-form tmpfs's mode: !override 0755 is refused", func(t *testing.T) {
		dir := t.TempDir()
		item := "name: t\nservices:\n  web:\n    image: alpine\n    volumes:\n      - type: tmpfs\n        target: /t\n        tmpfs:\n          mode: %s\n"
		os.WriteFile(filepath.Join(dir, "tagged.yaml"), []byte(fmt.Sprintf(item, "!override 0755")), 0o644)
		os.WriteFile(filepath.Join(dir, "plain.yaml"), []byte(fmt.Sprintf(item, "0755")), 0o644)
		if _, err := LoadFiles([]string{filepath.Join(dir, "tagged.yaml")}, nil); err == nil || !strings.Contains(err.Error(), "is not a mode") {
			t.Errorf("want the tagged mode refused, got %v", err)
		}
		if _, err := LoadFiles([]string{filepath.Join(dir, "plain.yaml")}, nil); err != nil {
			t.Errorf("control: the plain mode is read, got %v", err)
		}
	})
	// An `!override` scalar is a string, as docker compose reads it, which
	// each field then reads as it reads a quoted value: a string field keeps
	// the characters (`user: !override 1000` is "1000", `08080` stays
	// "08080"), and a count is still a count (`retries`, above).
	t.Run("user: !override 1000 is the string 1000", func(t *testing.T) {
		p := loadTagged(t, map[string]string{"c.yaml": "name: t\nservices:\n  web:\n    image: alpine\n    user: !override 1000\n"}, "c.yaml")
		if got := p.Services["web"].User; got != "1000" {
			t.Errorf("want user 1000, got %q", got)
		}
	})
	for _, files := range [][]string{{"s.yaml"}, {"a.yaml", "b.yaml"}} {
		t.Run("environment P: !override 08080 stays 08080 "+strings.Join(files, " "), func(t *testing.T) {
			p := loadTagged(t, map[string]string{
				"s.yaml": "name: t\nservices:\n  web:\n    image: alpine\n    environment: {P: !override 08080}\n",
				"a.yaml": "name: t\nservices:\n  web:\n    image: alpine\n    environment: {P: \"1\"}\n",
				"b.yaml": "services:\n  web:\n    environment: {P: !override 08080}\n",
			}, files...)
			if got := fieldOf(t, p.Services["web"], "environment"); got != "P=08080" {
				t.Errorf("want P=08080, got %q", got)
			}
		})
	}
	// A later include entry's tags do not reach an earlier entry: the
	// entries merge plainly (docker compose v5.5.0 keeps i1's /t).
	t.Run("include [i1, i2] as two entries, i2 tmpfs: !reset []", func(t *testing.T) {
		p := loadTagged(t, map[string]string{
			"i1.yaml": "services:\n  web:\n    image: alpine\n    tmpfs: [/t]\n",
			"i2.yaml": "services:\n  web:\n    image: alpine\n    tmpfs: !reset []\n",
			"c.yaml":  "name: i\ninclude: [i1.yaml, i2.yaml]\n",
		}, "c.yaml")
		if got := fieldOf(t, p.Services["web"], "tmpfs"); got != "/t" {
			t.Errorf("want i1's /t kept, got %q", got)
		}
	})
	// In an extends, a list in the extended service is not read as a mapping:
	// docker compose keeps db within one file, and takes it out when the list
	// comes from another file — a known difference, kept so here.
	for _, tc := range []struct{ name, want string }{{"same file", "db db2"}, {"another file", "db db2"}} {
		t.Run("extends from "+tc.name+", base depends_on [db, db2], db: !reset null", func(t *testing.T) {
			files := map[string]string{
				"base.yaml": "services:\n  base:\n    image: alpine\n    depends_on: [db, db2]\n  db:\n    image: alpine\n  db2:\n    image: alpine\n",
			}
			if tc.name == "same file" {
				files["c.yaml"] = "name: t\nservices:\n  base:\n    image: alpine\n    depends_on: [db, db2]\n  web:\n    extends: base\n    depends_on:\n      db: !reset null\n  db:\n    image: alpine\n  db2:\n    image: alpine\n"
			} else {
				files["c.yaml"] = "name: t\nservices:\n  web:\n    extends: {file: base.yaml, service: base}\n    depends_on:\n      db: !reset null\n  db:\n    image: alpine\n  db2:\n    image: alpine\n"
			}
			p := loadTagged(t, files, "c.yaml")
			if got := fieldOf(t, p.Services["web"], "depends_on"); got != tc.want {
				t.Errorf("want depends_on %q, got %q", tc.want, got)
			}
		})
	}
	// The same for networks: in an extends a list is not read as a mapping
	// (docker compose keeps n1 within one file; from another file, the same
	// known difference).
	for _, name := range []string{"same file", "another file"} {
		t.Run("extends from "+name+", base networks [n1, n2], n1: !reset null", func(t *testing.T) {
			nets := "networks:\n  n1: {}\n  n2: {}\n"
			files := map[string]string{"base.yaml": "services:\n  base:\n    image: alpine\n    networks: [n1, n2]\n" + nets}
			if name == "same file" {
				files["c.yaml"] = "name: t\nservices:\n  base:\n    image: alpine\n    networks: [n1, n2]\n  web:\n    extends: base\n    networks:\n      n1: !reset null\n" + nets
			} else {
				files["c.yaml"] = "name: t\nservices:\n  web:\n    extends: {file: base.yaml, service: base}\n    networks:\n      n1: !reset null\n" + nets
			}
			p := loadTagged(t, files, "c.yaml")
			if got := []string(p.Services["web"].Networks); !slices.Equal(got, []string{"n1", "n2"}) {
				t.Errorf("want networks [n1 n2], got %v", got)
			}
		})
	}
	// Across an include, an earlier depends_on list is read as the mapping
	// it merges as, between the paths of one entry and from the including
	// file itself (docker compose v5.5.0 takes db out of both).
	for _, tc := range []struct {
		name  string
		files map[string]string
	}{
		{"between the paths of one entry", map[string]string{
			"i1.yaml": "services:\n  web:\n    image: alpine\n    depends_on: [db, db2]\n",
			"i2.yaml": "services:\n  web:\n    depends_on:\n      db: !reset null\n",
			"c.yaml":  "name: i\ninclude:\n  - path: [i1.yaml, i2.yaml]\nservices:\n  db:\n    image: alpine\n  db2:\n    image: alpine\n"}},
		{"from the including file", map[string]string{
			"i.yaml": "services:\n  web:\n    image: alpine\n    depends_on: [db, db2]\n",
			"c.yaml": "name: i\ninclude: [i.yaml]\nservices:\n  web:\n    depends_on:\n      db: !reset null\n  db:\n    image: alpine\n  db2:\n    image: alpine\n"}},
	} {
		t.Run("include "+tc.name+": depends_on [db, db2], db: !reset null", func(t *testing.T) {
			p := loadTagged(t, tc.files, "c.yaml")
			if got := fieldOf(t, p.Services["web"], "depends_on"); got != "db2" {
				t.Errorf("want depends_on db2, got %q", got)
			}
		})
	}
	// Inside a list item tagged `!override` the tags are not read: the
	// published port stays (docker compose v5.5.0).
	t.Run("ports: [!override {target: 80, published: !reset 8080}]", func(t *testing.T) {
		p := loadTagged(t, map[string]string{"c.yaml": "name: t\nservices:\n  web:\n    image: alpine\n    ports: [!override {target: 80, published: !reset \"8080\"}]\n"}, "c.yaml")
		if got := fieldOf(t, p.Services["web"], "ports"); !strings.Contains(got, "8080") {
			t.Errorf("want the published port 8080 kept, got %q", got)
		}
	})
	// The extended file's own extends reads its tags: b extends a in eb.yaml
	// and overrides tmpfs, and web extends b from there.
	t.Run("an extended file's own extends with an override", func(t *testing.T) {
		p := loadTagged(t, map[string]string{
			"eb.yaml": "services:\n  a:\n    image: alpine\n    tmpfs: [/a]\n  b:\n    extends: a\n    tmpfs: !override [/b]\n",
			"c.yaml":  "name: x\nservices:\n  web:\n    extends: {file: eb.yaml, service: b}\n",
		}, "c.yaml")
		if got := fieldOf(t, p.Services["web"], "tmpfs"); got != "/b" {
			t.Errorf("want tmpfs /b, got %q", got)
		}
	})
	// A whole service tagged `!override` that extends: the tag is on the
	// service, so it takes nothing out of what it extends.
	t.Run("web: !override {extends: base, tmpfs: [/u]}", func(t *testing.T) {
		p := loadTagged(t, map[string]string{
			"c.yaml": "name: t\nservices:\n  base:\n    image: alpine\n    tmpfs: [/b]\n  web: !override\n    extends: base\n    tmpfs: [/u]\n",
		}, "c.yaml")
		if got := fieldOf(t, p.Services["web"], "tmpfs"); got != "/b /u" {
			t.Errorf("want tmpfs /b /u, got %q", got)
		}
	})
}

func loadTagged(t *testing.T, files map[string]string, load ...string) *Project {
	t.Helper()
	dir := t.TempDir()
	for n, body := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var paths []string
	for _, n := range load {
		paths = append(paths, filepath.Join(dir, n))
	}
	p, err := LoadFiles(paths, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return p
}

// fieldOf is a service field as the rows write it: its values joined by a
// space, sorted where the field has no order of its own, "-" for none.
func fieldOf(t *testing.T, s *Service, field string) string {
	t.Helper()
	var vs []string
	switch field {
	case "tmpfs":
		vs = s.Tmpfs
	case "environment":
		env, err := s.ResolvedEnv()
		if err != nil {
			t.Fatalf("env: %v", err)
		}
		vs = append(vs, env...)
		sort.Strings(vs)
	case "ports":
		vs = s.Ports
	case "labels":
		vs = append(vs, s.Labels...)
		sort.Strings(vs)
	case "volumes":
		vs = s.Volumes
	case "depends_on":
		for _, d := range s.DependsOn {
			vs = append(vs, d.Name)
		}
	case "command":
		vs = s.Command
	case "init":
		return fmt.Sprint(s.Init)
	default:
		panic(fmt.Sprintf("no field %q", field))
	}
	if len(vs) == 0 {
		return "-"
	}
	return strings.Join(vs, " ")
}
