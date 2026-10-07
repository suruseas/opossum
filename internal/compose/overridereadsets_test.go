package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A block that holds a merge key to a list of mappings (`<<: *e`) is refused when it is read inside an `!override` and nowhere outside one (docker compose
// v5.5.1 `config`, every row measured, rc 1 or 0): where it is written (plainly, under `!override`, under `!reset`) against where an alias uses it (outside
// every tag, inside an `!override`, inside a `!reset`), each of the eight sets of uses. A block read plainly is read whatever else reads it under an
// `!override`; one written under a `!reset` is read only where an alias outside the `!reset` reads it (#1860).
func TestABlockWithAListMergeIsRefusedWhereAnOverrideReadsItAndNothingOutsideOneDoes(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		refused    bool
	}{
		{"a block written plainly, used by nothing", "x-e: &e [{A: '1'}]\nx-d: &in {<<: *e}\nservices:\n  s:\n    image: x\n", false},
		{"a block written plainly, used by a service outside every tag", "x-e: &e [{A: '1'}]\nx-d: &in {<<: *e}\nservices:\n  s:\n    image: x\n    labels: *in\n", false},
		{"a block written plainly, used inside an !override", "x-e: &e [{A: '1'}]\nx-d: &in {<<: *e}\nx-o: !override {Y: *in}\nservices:\n  s:\n    image: x\n", false},
		{"a block written plainly, used inside a !reset", "x-e: &e [{A: '1'}]\nx-d: &in {<<: *e}\nx-p: !reset {Y: *in}\nservices:\n  s:\n    image: x\n", false},
		{"a block written plainly, used by a service outside every tag and inside an !override", "x-e: &e [{A: '1'}]\nx-d: &in {<<: *e}\nx-o: !override {Y: *in}\nservices:\n  s:\n    image: x\n    labels: *in\n", false},
		{"a block written plainly, used by a service outside every tag and inside a !reset", "x-e: &e [{A: '1'}]\nx-d: &in {<<: *e}\nx-p: !reset {Y: *in}\nservices:\n  s:\n    image: x\n    labels: *in\n", false},
		{"a block written plainly, used inside an !override and inside a !reset", "x-e: &e [{A: '1'}]\nx-d: &in {<<: *e}\nx-o: !override {Y: *in}\nx-p: !reset {Y: *in}\nservices:\n  s:\n    image: x\n", false},
		{"a block written plainly, used by a service outside every tag and inside an !override and inside a !reset", "x-e: &e [{A: '1'}]\nx-d: &in {<<: *e}\nx-o: !override {Y: *in}\nx-p: !reset {Y: *in}\nservices:\n  s:\n    image: x\n    labels: *in\n", false},
		{"a block written under !override, used by nothing", "x-e: &e [{A: '1'}]\nx-z: !override {X: &in {<<: *e}}\nservices:\n  s:\n    image: x\n", true},
		{"a block written under !override, used by a service outside every tag", "x-e: &e [{A: '1'}]\nx-z: !override {X: &in {<<: *e}}\nservices:\n  s:\n    image: x\n    labels: *in\n", false},
		{"a block written under !override, used inside an !override", "x-e: &e [{A: '1'}]\nx-z: !override {X: &in {<<: *e}}\nx-o: !override {Y: *in}\nservices:\n  s:\n    image: x\n", true},
		{"a block written under !override, used inside a !reset", "x-e: &e [{A: '1'}]\nx-z: !override {X: &in {<<: *e}}\nx-p: !reset {Y: *in}\nservices:\n  s:\n    image: x\n", true},
		{"a block written under !override, used by a service outside every tag and inside an !override", "x-e: &e [{A: '1'}]\nx-z: !override {X: &in {<<: *e}}\nx-o: !override {Y: *in}\nservices:\n  s:\n    image: x\n    labels: *in\n", false},
		{"a block written under !override, used by a service outside every tag and inside a !reset", "x-e: &e [{A: '1'}]\nx-z: !override {X: &in {<<: *e}}\nx-p: !reset {Y: *in}\nservices:\n  s:\n    image: x\n    labels: *in\n", false},
		{"a block written under !override, used inside an !override and inside a !reset", "x-e: &e [{A: '1'}]\nx-z: !override {X: &in {<<: *e}}\nx-o: !override {Y: *in}\nx-p: !reset {Y: *in}\nservices:\n  s:\n    image: x\n", true},
		{"a block written under !override, used by a service outside every tag and inside an !override and inside a !reset", "x-e: &e [{A: '1'}]\nx-z: !override {X: &in {<<: *e}}\nx-o: !override {Y: *in}\nx-p: !reset {Y: *in}\nservices:\n  s:\n    image: x\n    labels: *in\n", false},
		{"a block written under !reset, used by nothing", "x-e: &e [{A: '1'}]\nx-r: !reset {a: &in {<<: *e}}\nservices:\n  s:\n    image: x\n", false},
		{"a block written under !reset, used by a service outside every tag", "x-e: &e [{A: '1'}]\nx-r: !reset {a: &in {<<: *e}}\nservices:\n  s:\n    image: x\n    labels: *in\n", false},
		{"a block written under !reset, used inside an !override", "x-e: &e [{A: '1'}]\nx-r: !reset {a: &in {<<: *e}}\nx-o: !override {Y: *in}\nservices:\n  s:\n    image: x\n", true},
		{"a block written under !reset, used inside a !reset", "x-e: &e [{A: '1'}]\nx-r: !reset {a: &in {<<: *e}}\nx-p: !reset {Y: *in}\nservices:\n  s:\n    image: x\n", false},
		{"a block written under !reset, used by a service outside every tag and inside an !override", "x-e: &e [{A: '1'}]\nx-r: !reset {a: &in {<<: *e}}\nx-o: !override {Y: *in}\nservices:\n  s:\n    image: x\n    labels: *in\n", false},
		{"a block written under !reset, used by a service outside every tag and inside a !reset", "x-e: &e [{A: '1'}]\nx-r: !reset {a: &in {<<: *e}}\nx-p: !reset {Y: *in}\nservices:\n  s:\n    image: x\n    labels: *in\n", false},
		{"a block written under !reset, used inside an !override and inside a !reset", "x-e: &e [{A: '1'}]\nx-r: !reset {a: &in {<<: *e}}\nx-o: !override {Y: *in}\nx-p: !reset {Y: *in}\nservices:\n  s:\n    image: x\n", true},
		{"a block written under !reset, used by a service outside every tag and inside an !override and inside a !reset", "x-e: &e [{A: '1'}]\nx-r: !reset {a: &in {<<: *e}}\nx-o: !override {Y: *in}\nx-p: !reset {Y: *in}\nservices:\n  s:\n    image: x\n    labels: *in\n", false},
		{"a block read outside every tag does not hold its !reset child for an !override that reads it afterwards", "x-e: &e [{A: '1'}]\nx-d: &in {a: !reset {<<: *e}}\nx-o: !override {Y: *in}\nservices:\n  s:\n    image: x\n", false},
		{"the same, the block written under an !override and read by an alias outside every tag", "x-e: &e [{A: '1'}]\nx-z: !override {X: &in {a: !reset {<<: *e}}}\nx-y: *in\nservices:\n  s:\n    image: x\n", false},
		{"a list read outside every tag does not hold its !reset item for an !override", "x-e: &e [{A: '1'}]\nx-d: &in [!reset {<<: *e}]\nx-o: !override {Y: *in}\nservices:\n  s:\n    image: x\n", false},
		{"the same, the !override written first and the alias outside every tag after it", "x-e: &e [{A: '1'}]\nx-o: !override {Y: &in {a: !reset {<<: *e}}}\nx-y: *in\nservices:\n  s:\n    image: x\n", false},
		{"a !reset inside an !override that nothing outside every tag reads is looked through", "x-e: &e [{A: '1'}]\nx-o: !override {Y: {a: !reset {<<: *e}}}\nservices:\n  s:\n    image: x\n", true},
		{"an alias inside a !reset inside an !override is read", "x-e: &e [{A: '1'}]\nx-r: !reset {a: &in {<<: *e}}\nx-o: !override {a: !reset {Y: *in}}\nservices:\n  s:\n    image: x\n", true},
		{"a lattice of five levels written under an !override", "x-e: &e [{A: '1'}]\nx-o: !override\n  l0: &l0 {<<: *e}\n  l1: &l1 {a1: *l0, b1: *l0}\n  l2: &l2 {a2: *l1, b2: *l1}\n  l3: &l3 {a3: *l2, b3: *l2}\n  l4: &l4 {a4: *l3, b4: *l3}\n  l5: &l5 {a5: *l4, b5: *l4}\nservices:\n  s:\n    image: x\n", true},
		{"an alias to a !reset node is taken out of a block read outside every tag, as the node is: a mapping value", "x-e: &e [{A: '1'}]\nx-r: &r !reset {<<: *e}\nx-d: &in {a: *r}\nx-o: !override {Y: *in}\nservices:\n  s:\n    image: x\n", false},
		{"the same as an item of a list", "x-e: &e [{A: '1'}]\nx-r: &r !reset {<<: *e}\nx-d: &in [*r]\nx-o: !override {Y: *in}\nservices:\n  s:\n    image: x\n", false},
		{"the same, the !reset node a list", "x-e: &e [{A: '1'}]\nx-r: &r !reset [{<<: *e}]\nx-d: &in {a: *r}\nx-o: !override {Y: *in}\nservices:\n  s:\n    image: x\n", false},
		{"the same as the value of a merge key", "x-e: &e [{A: '1'}]\nx-r: &r !reset {<<: *e}\nx-d: &in {<<: *r}\nx-o: !override {Y: *in}\nservices:\n  s:\n    image: x\n", false},
		{"the same in the labels of one service, read by the environment of another", "x-e: &e [{A: '1'}]\nx-r: &r !reset {<<: *e}\nservices:\n  t:\n    image: y\n    labels: &in {a: *r}\n  s:\n    image: x\n    environment: !override {<<: *in}\n", false},
		{"the same, the block read by the !override directly from a service", "x-e: &e [{A: '1'}]\nx-r: &r !reset {<<: *e}\nx-d: &in {a: *r}\nservices:\n  s:\n    image: x\n    environment: !override {<<: *in}\n", false},
		{"the same through a chain of two aliases", "x-e: &e [{A: '1'}]\nx-r: &r !reset {<<: *e}\nx-c: &c {b: *r}\nx-d: &in {a: *c}\nx-o: !override {Y: *in}\nservices:\n  s:\n    image: x\n", false},
		{"an alias to an !override node from a block read outside every tag is read", "x-e: &e [{A: '1'}]\nx-q: &q !override {<<: *e}\nx-d: &in {a: *q}\nx-o: !override {Y: *in}\nservices:\n  s:\n    image: x\n", true},
		{"an alias to a !reset node written inside the !override itself is read", "x-e: &e [{A: '1'}]\nx-r: &r !reset {<<: *e}\nx-o: !override {Y: *r}\nservices:\n  s:\n    image: x\n", true},
		{"a service written !override that holds the anchor and the alias that uses it", "x-e: &e [{A: '1'}]\nservices:\n  s: !override {image: x, environment: &in {<<: *e}, labels: *in}\n", true},
		{"an !override anchored in a block that is also read by an alias outside every tag", "x-e: &e [{A: '1'}]\nx-z: !override {X: &in {<<: *e}}\nx-y: *in\nservices:\n  s:\n    image: x\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadFiles([]string{path}, nil)
			if (err != nil) != tc.refused {
				t.Fatalf("err %v, docker compose refuses it: %v", err, tc.refused)
			}
			if tc.refused && !strings.Contains(err.Error(), "stands for a list, inside a value written with `!override`") {
				t.Errorf("refused, but not for the merge key under !override: %v", err)
			}
		})
	}
}

// The refusal says which file holds the merge key, and the line in it: one file, a later `-f` file, an extended file and an included file each name their own
// (the file a reader has to open is the one that is named, not the first one given).
func TestAnOverrideListMergeRefusalNamesTheFileThatHoldsIt(t *testing.T) {
	const head = "x-e: &e [{A: '1'}]\n"
	const svc = "services:\n  s:\n    image: x\n"
	const bad = "    environment: !override {<<: *e}\n"
	for _, tc := range []struct {
		name  string
		files map[string]string
		order []string
		file  string // the file the refusal names
	}{
		{"one file", map[string]string{"c.yaml": head + svc + bad}, []string{"c.yaml"}, "c.yaml"},
		{"the second of two -f files", map[string]string{"c.yaml": svc, "o.yaml": head + "services:\n  s:\n" + bad}, []string{"c.yaml", "o.yaml"}, "o.yaml"},
		{"the first of two -f files", map[string]string{"c.yaml": head + svc + bad, "o.yaml": "services:\n  s:\n    working_dir: /w\n"}, []string{"c.yaml", "o.yaml"}, "c.yaml"},
		{"an extended file", map[string]string{"base.yaml": head + "services:\n  b:\n    image: y\n" + bad, "c.yaml": "services:\n  s:\n    extends: {file: base.yaml, service: b}\n"}, []string{"c.yaml"}, "base.yaml"},
		{"an included file", map[string]string{"sub.yaml": head + svc + bad, "c.yaml": "include:\n  - sub.yaml\n"}, []string{"c.yaml"}, "sub.yaml"},
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
			if err == nil {
				t.Fatal("docker compose refuses it, and it was read")
			}
			if want := "compose file " + filepath.Join(dir, tc.file) + ": line "; !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal should name the file and a line (%q):\n%v", want, err)
			}
		})
	}
}

// A lattice (two ways to each block over twenty-six levels) written under an `!override` is walked once for every block, not once for every way to it: the
// refusal comes at once (at twenty-six levels a walk that goes through every way takes seventeen seconds, and at thirty nearly five minutes). docker compose
// refuses the same shape at five levels (rc 1); at twenty-six it does not come to an end itself, measured (#1860).
func TestALatticeUnderAnOverrideIsWalkedOnce(t *testing.T) {
	var b strings.Builder
	b.WriteString("x-e: &e [{A: '1'}]\nx-o: !override\n  l0: &l0 {<<: *e}\n")
	for i := 1; i <= 26; i++ {
		fmt.Fprintf(&b, "  l%d: &l%d {a%d: *l%d, b%d: *l%d}\n", i, i, i, i-1, i, i-1)
	}
	b.WriteString("services:\n  s:\n    image: x\n")
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := LoadFiles([]string{path}, nil)
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("took %v: the lattice is walked once for every way to a block", took)
	}
	if err == nil {
		t.Error("docker compose refuses it, and it was read")
	}
}

// Every `!override` met outside a tag is read, not only the first: a file with two of them is refused for the second one's merge key as it is for the first's
// (#1896; docker compose v5.5.1: rc 1 for each).
func TestEveryOverrideRootIsReadForAListMerge(t *testing.T) {
	const svc = "services:\n  s:\n    image: x\n"
	for _, tc := range []struct {
		name, body string
		refused    bool
	}{
		{"the first !override holds it", "x-e: &e [{A: '1'}]\nx-o: !override {Y: {<<: *e}}\nx-a: !override {B: '1'}\n" + svc, true},
		{"the second !override holds it", "x-e: &e [{A: '1'}]\nx-a: !override {B: '1'}\nx-o: !override {Y: {<<: *e}}\n" + svc, true},
		{"the third of three holds it", "x-e: &e [{A: '1'}]\nx-a: !override {B: '1'}\nx-b: !override {C: '2'}\nx-o: !override {Y: {<<: *e}}\n" + svc, true},
		{"none holds it", "x-e: &e [{A: '1'}]\nx-a: !override {B: '1'}\nx-b: !override {C: '2'}\n" + svc, false},
		{"the second holds it, and the block is read outside every tag as well", "x-e: &e [{A: '1'}]\nx-d: &in {<<: *e}\nx-a: !override {B: '1'}\nx-o: !override {Y: *in}\nservices:\n  s:\n    image: x\n    labels: *in\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadFiles([]string{path}, nil)
			if (err != nil) != tc.refused {
				t.Fatalf("err %v, docker compose refuses it: %v", err, tc.refused)
			}
			if tc.refused && !strings.Contains(err.Error(), "stands for a list") {
				t.Errorf("refused, but not for the list that a merge key stands for: %v", err)
			}
		})
	}
}

// Where there are two merge keys to refuse, the one named is the first the file writes, by its line and its alias: not the last, and not one that comes first
// only in the order the tags were met (#1896).
func TestTheFirstListMergeInAnOverrideIsTheOneNamed(t *testing.T) {
	const svc = "services:\n  s:\n    image: x\n"
	for _, tc := range []struct {
		name, body string
		wantLine   string
		wantAlias  string
		notAlias   string
	}{
		{"two in one !override, written on two lines", "x-e: &e [{A: '1'}]\nx-f: &f [{B: '2'}]\nx-o: !override {Y: {<<: *e}, Z: {<<: *f}}\n" + svc, "line 3", "`<<: *e`", "`<<: *f`"},
		{"two in one !override, the other way round", "x-e: &e [{A: '1'}]\nx-f: &f [{B: '2'}]\nx-o: !override {Y: {<<: *f}, Z: {<<: *e}}\n" + svc, "line 3", "`<<: *f`", "`<<: *e`"},
		{"one in each of two !override, the earlier first", "x-e: &e [{A: '1'}]\nx-f: &f [{B: '2'}]\nx-a: !override {Y: {<<: *e}}\nx-b: !override {Z: {<<: *f}}\n" + svc, "line 3", "`<<: *e`", "`<<: *f`"},
		{"one in each of two !override, the later one's alias was met first", "x-e: &e [{A: '1'}]\nx-f: &f [{B: '2'}]\nx-a: !override {Y: {<<: *f}}\nx-b: !override {Z: {<<: *e}}\n" + svc, "line 3", "`<<: *f`", "`<<: *e`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadFiles([]string{path}, nil)
			if err == nil {
				t.Fatal("docker compose refuses it, and it was read")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.wantLine) || !strings.Contains(msg, tc.wantAlias) || strings.Contains(msg, tc.notAlias) {
				t.Errorf("want the first merge key named (%s, %s, and not %s), got: %v", tc.wantLine, tc.wantAlias, tc.notAlias, err)
			}
		})
	}
}
