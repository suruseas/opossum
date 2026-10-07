package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// A block that merges itself (`x-a: &a {K: v, <<: *a}`) is refused as docker compose refuses it (v5.5.1 `config`, rc 1), with a word of its own, and does not
// take the process down: the walk of the names a merge key brings in is one stack deep for each block it goes through, and for a block that holds itself it
// has no end. A block that several merge keys bring in by different ways (a diamond, the same one twice, a chain) is no cycle, and a lattice of them (two
// ways to each block over twenty-eight levels, 2.4 KB) is walked once for each block: without that the walk goes through every way, takes twelve seconds at
// twenty-eight levels and four at twenty-six, and the file is refused for its aliases only after it (#1891).
func TestABlockThatMergesItselfIsRefusedAndDoesNotTakeTheProcessDown(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		refused    bool
	}{
		{"the environment uses a block that merges itself", "x-a: &a {K: v, <<: *a}\nservices:\n  s:\n    image: x\n    environment: *a\n", true},
		{"the labels use a block that merges itself", "x-a: &a {K: v, <<: *a}\nservices:\n  s:\n    image: x\n    labels: *a\n", true},
		{"a merge key of the environment brings in a block that merges itself", "x-a: &a {K: v, <<: *a}\nservices:\n  s:\n    image: x\n    environment:\n      <<: *a\n", true},
		{"a block that merges itself through a list", "x-a: &a {K: v, <<: [*a]}\nservices:\n  s:\n    image: x\n    environment: *a\n", true},
		{"a diamond of merges is no cycle", "x-a: &a {K: v}\nx-b: &b {J: w, <<: *a}\nx-c: &c {I: u, <<: *a}\nx-d: &d {H: t, <<: [*b, *c]}\nservices:\n  s:\n    image: x\n    environment: *d\n", false},
		{"the same block merged twice in a list is no cycle", "x-a: &a {K: v}\nx-b: &b {J: w, <<: [*a, *a]}\nservices:\n  s:\n    image: x\n    environment: *b\n", false},
		{"a chain of merges is no cycle", "x-a: &a {K: v}\nx-b: &b {J: w, <<: *a}\nx-c: &c {I: u, <<: *b}\nx-d: &d {H: t, <<: *c}\nx-e: &e {G: s, <<: *d}\nx-f: &f {F: r, <<: *e}\nservices:\n  s:\n    image: x\n    environment: *f\n", false},
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
			if tc.refused && !strings.Contains(err.Error(), "a merge key brings in the block that holds it") {
				t.Errorf("refused, but not for the block that merges itself: %v", err)
			}
		})
	}
}

// The walk of the names a merge key brings in goes through a lattice of merges (two ways to each block, twenty-eight levels) once for each block, and
// does not go through every way: it comes back at once, refused or not (the decoder refuses a file of that many aliases, but after the walk).
func TestALatticeOfMergesIsWalkedOnceForEachBlock(t *testing.T) {
	var b strings.Builder
	b.WriteString("x-l0: &l0 {K: v}\n")
	for i := 1; i <= 28; i++ {
		fmt.Fprintf(&b, "x-l%d: &l%d {a%d: 1, <<: [*l%d, *l%d]}\n", i, i, i, i-1, i-1)
	}
	b.WriteString("services:\n  s:\n    image: x\n    environment: *l28\n")
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
	case <-time.After(60 * time.Second):
		t.Fatal("the walk of the merges did not come back in a minute: it goes through every way to a block")
	}
}

// Used by `build.args`, a block that merges itself is refused as what it is, and not as the environment (as it once was, where the words of the refusal were
// kept for the environment): it is named by its line.
func TestABlockThatMergesItselfInBuildArgsIsNotRefusedAsTheEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	body := "x-a: &a {K: v, <<: *a}\nservices:\n  s:\n    image: x\n    build:\n      context: .\n      args: *a\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFiles([]string{path}, nil)
	if err == nil {
		t.Fatal("docker compose refuses it, and it was read")
	}
	if !strings.Contains(err.Error(), "a merge key brings in the block that holds it") || strings.Contains(err.Error(), "environment") {
		t.Errorf("want the block that merges itself refused, and not as the environment, got: %v", err)
	}
}

// The other walks of the names a merge key brings in — the ulimits of a service and the declaration of a volume, a network, a secret and a config — refuse
// a block that merges itself as well, and do not take the process down (docker compose v5.5.1, rc 1 for each; #1893). A block brought in by several ways is no
// cycle: the ulimits are merged again for each place it is brought in at, and only the way down is kept.
func TestTheOtherWalksOfAMergeRefuseABlockThatMergesItself(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		refused    bool
	}{
		{"the ulimits are a block that merges itself", "x-a: &a {K: v, <<: *a}\nservices:\n  s:\n    image: x\n    ulimits: *a\n", true},
		{"a merge key of the ulimits brings in a block that merges itself", "x-a: &a {K: v, <<: *a}\nservices:\n  s:\n    image: x\n    ulimits:\n      <<: *a\n", true},
		{"the declaration of a volume merges a block that merges itself", "x-a: &a {K: v, <<: *a}\nservices:\n  s:\n    image: x\nvolumes:\n  v:\n    <<: *a\n", true},
		{"a network declaration merges a block that merges itself", "x-a: &a {K: v, <<: *a}\nservices:\n  s:\n    image: x\nnetworks:\n  n:\n    <<: *a\n", true},
		{"a secret declaration merges a block that merges itself", "x-a: &a {K: v, <<: *a}\nservices:\n  s:\n    image: x\nsecrets:\n  z:\n    <<: *a\n", true},
		{"a config declaration merges a block that merges itself", "x-a: &a {K: v, <<: *a}\nservices:\n  s:\n    image: x\nconfigs:\n  z:\n    <<: *a\n", true},
		{"a diamond of merges in the ulimits is no cycle", "x-a: &a {nofile: 1024}\nx-b: &b {nproc: 2048, <<: *a}\nx-c: &c {core: 0, <<: *a}\nservices:\n  s:\n    image: x\n    ulimits:\n      <<: [*b, *c]\n", false},
		{"a diamond of merges in a volume declaration is no cycle", "x-a: &a {driver: local}\nx-b: &b {name: n, <<: *a}\nx-c: &c {external: false, <<: *a}\nservices:\n  s:\n    image: x\nvolumes:\n  v:\n    <<: [*b, *c]\n", false},
		{"the same block merged twice into the ulimits is no cycle", "x-a: &a {nofile: 1024}\nservices:\n  s:\n    image: x\n    ulimits:\n      <<: [*a, *a]\n", false},
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
			if tc.refused && !strings.Contains(err.Error(), "a merge key brings in the block that holds it") {
				t.Errorf("refused, but not for the block that merges itself: %v", err)
			}
		})
	}
}

// A lattice of merges (two ways to each block, twenty-eight levels) is merged, and its names read, once for each block in the ulimits and in a volume's
// declaration, and does not take every way down to a block: each comes back at once, refused or not (#1893).
func TestALatticeOfMergesInTheOtherWalksIsWalkedOnceForEachBlock(t *testing.T) {
	for _, tc := range []struct{ name, tail string }{
		{"the ulimits", "services:\n  s:\n    image: x\n    ulimits:\n      <<: *l28\n"},
		{"the declaration of a volume", "services:\n  s:\n    image: x\nvolumes:\n  v:\n    <<: *l28\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			b.WriteString("x-l0: &l0 {nofile: 1}\n")
			for i := 1; i <= 28; i++ {
				fmt.Fprintf(&b, "x-l%d: &l%d {a%d: 1, <<: [*l%d, *l%d]}\n", i, i, i, i-1, i-1)
			}
			b.WriteString(tc.tail)
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
			case <-time.After(60 * time.Second):
				t.Fatal("the walk of the merges did not come back in a minute: it goes through every way to a block")
			}
		})
	}
}

// A list that holds a block that merges it (`x-l: &l [{K: v, <<: *l}]`) is refused wherever it is, used or not, as docker compose refuses it (rc 1), and does
// not take the process down: the list is put where the merge key holds the alias to it, which makes a cycle with no alias in it (#1898). A list of mappings
// merged by several keys, or by a block that is itself in a list, is no cycle.
func TestAListThatHoldsABlockThatMergesItIsRefusedAndDoesNotTakeTheProcessDown(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		refused    bool
		want       string // what the refusal says, where it is said
	}{
		{"a list that holds a block that merges it, used by nothing", "x-l: &l [{K: v, <<: *l}]\nservices:\n  s:\n    image: x\n", true, "a merge key brings in the block that holds it"},
		{"the environment merges a list that holds a block that merges it", "x-l: &l [{K: v, <<: *l}]\nservices:\n  s:\n    image: x\n    environment: {J: w, <<: *l}\n", true, "a merge key brings in the block that holds it"},
		{"the ulimits merge a list that holds a block that merges it", "x-l: &l [{K: v, <<: *l}]\nservices:\n  s:\n    image: x\n    ulimits: {<<: *l}\n", true, "a merge key brings in the block that holds it"},
		{"the labels are a list that holds a block that merges it", "x-l: &l [{K: v, <<: *l}]\nservices:\n  s:\n    image: x\n    labels: *l\n", true, ""},
		{"a block of the list is reached again by an alias, after the list was walked: the list is not put where its own block is", "x-l: &l [&m {K: v, <<: *l}]\nx-y: *m\nservices:\n  s:\n    image: x\n", true, "a merge key brings in the block that holds it"},
		{"the same, used by the environment", "x-l: &l [&m {K: v, <<: *l}]\nservices:\n  s:\n    image: x\n    environment: *m\n", true, ""},
		{"a list of mappings merged by two services is no cycle", "x-e: &e [{A: '1'}, {B: '2'}]\nservices:\n  s:\n    image: x\n    environment: {<<: *e}\n  t:\n    image: y\n    labels: {<<: *e}\n", false, ""},
		{"a list that merges a list of mappings is no cycle", "x-e: &e [{A: '1'}]\nx-l: &l [{K: v, <<: *e}]\nservices:\n  s:\n    image: x\n    environment: {J: w, <<: *l}\n", false, ""},
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
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want %q in the refusal, got: %v", tc.want, err)
			}
		})
	}
}

// The same list is refused wherever the decode reads it — an override file, an included file, an extended file, a service, a declaration, a healthcheck —
// and not only in the fields opossum looks at before it: the list is not put where its own block is, so the decode is not given a cycle (#1898, docker compose
// v5.5.1: rc 1 for each). Every file of a row is written in a directory of its own, the first one (c.yml) is the file asked for and the second, if any, is
// given after it with -f.
func TestAListThatHoldsABlockThatMergesItIsRefusedInEveryFileTheDecodeReads(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []struct{ name, body string }
	}{
		{"an override file holds the list, used by nothing", []struct{ name, body string }{{"c.yml", "services:\n  s:\n    image: x\n"}, {"o.yml", "x-l: &l [{K: v, <<: *l}]\nservices:\n  s:\n    image: x\n"}}},
		{"an override file uses the list in the environment", []struct{ name, body string }{{"c.yml", "services:\n  s:\n    image: x\n"}, {"o.yml", "x-l: &l [{K: v, <<: *l}]\nservices:\n  s:\n    environment: {J: w, <<: *l}\n"}}},
		{"an included file holds the list, used by nothing", []struct{ name, body string }{{"c.yml", "include:\n  - i.yml\nservices:\n  s:\n    image: x\n"}, {"i.yml", "x-l: &l [{K: v, <<: *l}]\nservices:\n  s:\n    image: x\n"}}},
		{"an extended file uses the list in the environment", []struct{ name, body string }{{"c.yml", "services:\n  s:\n    image: x\n    extends: {file: i.yml, service: t}\n"}, {"i.yml", "x-l: &l [{K: v, <<: *l}]\nservices:\n  t:\n    image: y\n    environment: {J: w, <<: *l}\n"}}},
		{"a service is the merge of the list", []struct{ name, body string }{{"c.yml", "x-l: &l [{image: x, <<: *l}]\nservices:\n  s: {<<: *l}\n"}}},
		{"a volume declaration is the merge of the list", []struct{ name, body string }{{"c.yml", "x-l: &l [{K: v, <<: *l}]\nservices:\n  s:\n    image: x\nvolumes:\n  v: {<<: *l}\n"}}},
		{"a network declaration is the merge of the list", []struct{ name, body string }{{"c.yml", "x-l: &l [{K: v, <<: *l}]\nservices:\n  s:\n    image: x\nnetworks:\n  n: {<<: *l}\n"}}},
		{"a secret declaration is the merge of the list", []struct{ name, body string }{{"c.yml", "x-l: &l [{K: v, <<: *l}]\nservices:\n  s:\n    image: x\nsecrets:\n  z: {<<: *l}\n"}}},
		{"the healthcheck is the merge of the list", []struct{ name, body string }{{"c.yml", "x-l: &l [{K: v, <<: *l}]\nservices:\n  s:\n    image: x\n    healthcheck: {<<: *l}\n"}}},
		{"an override file reaches a block of the list again by an alias, after the list was walked", []struct{ name, body string }{{"c.yml", "services:\n  s:\n    image: x\n"}, {"o.yml", "x-l: &l [&m {K: v, <<: *l}]\nx-y: *m\nservices:\n  s:\n    image: x\n"}}},
		{"an override file uses the block of the list that an alias reaches again", []struct{ name, body string }{{"c.yml", "services:\n  s:\n    image: x\n"}, {"o.yml", "x-l: &l [&m {K: v, <<: *l}]\nservices:\n  s:\n    environment: *m\n"}}},
		{"an included file reaches a block of the list again by an alias, after the list was walked", []struct{ name, body string }{{"c.yml", "include:\n  - i.yml\nservices:\n  s:\n    image: x\n"}, {"i.yml", "x-l: &l [&m {K: v, <<: *l}]\nx-y: *m\nservices:\n  s:\n    image: x\n"}}},
		{"an included file uses the block of the list that an alias reaches again", []struct{ name, body string }{{"c.yml", "include:\n  - i.yml\nservices:\n  s:\n    image: x\n"}, {"i.yml", "x-l: &l [&m {K: v, <<: *l}]\nservices:\n  s:\n    image: x\n    environment: *m\n"}}},
		{"the depends_on is the merge of the list", []struct{ name, body string }{{"c.yml", "x-l: &l [{K: v, <<: *l}]\nservices:\n  s:\n    image: x\n    depends_on: {<<: *l}\n"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var paths []string
			for _, f := range tc.files {
				path := filepath.Join(dir, f.name)
				if err := os.WriteFile(path, []byte(f.body), 0o644); err != nil {
					t.Fatal(err)
				}
				if f.name == "c.yml" || f.name == "o.yml" {
					paths = append(paths, path)
				}
			}
			if _, err := LoadFiles(paths, nil); err == nil {
				t.Fatal("docker compose refuses it, and it was read")
			}
		})
	}
}

// A block that merges itself (`x-a: &a {name: n, <<: *a}`) is refused wherever it is put — a service's `depends_on`, `env_file`, `extends` or `volumes`, the
// `external:` of a volume, a network, a secret or a config, a merge key of the document itself — and not only in the fields that were found one at a time, by the
// process going round for ever in the walk of a field that nothing had asked about (docker compose v5.5.1: rc 1 for each; #1900, #1902). One row for each place
// and shape that did, found by putting the block in every field of a service, of the document and of each of their parts in five shapes (`*a`, `{<<: *a}`,
// `[*a]`, `[{<<: *a}]`, `{<<: [*a]}`), in a directory of its own for each. The rows after them are blocks that several merge keys bring in, which is no cycle.
func TestABlockThatMergesItselfIsRefusedWhereverItIsPut(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		refused    bool
	}{
		{"svc.env_file item", "x-a: &a {name: n, <<: *a}\nservices:\n  t:\n    image: y\n  s:\n    image: x\n    env_file: [*a]\n", true},
		{"svc.env_file mergeitem", "x-a: &a {name: n, <<: *a}\nservices:\n  t:\n    image: y\n  s:\n    image: x\n    env_file: [{<<: *a}]\n", true},
		{"svc.volumes item", "x-a: &a {name: n, <<: *a}\nservices:\n  t:\n    image: y\n  s:\n    image: x\n    volumes: [*a]\n", true},
		{"svc.volumes mergeitem", "x-a: &a {name: n, <<: *a}\nservices:\n  t:\n    image: y\n  s:\n    image: x\n    volumes: [{<<: *a}]\n", true},
		{"svc.extends alias", "x-a: &a {name: n, <<: *a}\nservices:\n  t:\n    image: y\n  s:\n    image: x\n    extends: *a\n", true},
		{"svc.extends merge", "x-a: &a {name: n, <<: *a}\nservices:\n  t:\n    image: y\n  s:\n    image: x\n    extends: {<<: *a}\n", true},
		{"svc.depends_on.t alias", "x-a: &a {name: n, <<: *a}\nservices:\n  t:\n    image: y\n  s:\n    image: x\n    depends_on:\n      t: *a\n", true},
		{"svc.depends_on.t merge", "x-a: &a {name: n, <<: *a}\nservices:\n  t:\n    image: y\n  s:\n    image: x\n    depends_on:\n      t: {<<: *a}\n", true},
		{"svc.depends_on.t mergeseq", "x-a: &a {name: n, <<: *a}\nservices:\n  t:\n    image: y\n  s:\n    image: x\n    depends_on:\n      t: {<<: [*a]}\n", true},
		{"svc.extends.service alias", "x-a: &a {name: n, <<: *a}\nservices:\n  t:\n    image: y\n  s:\n    image: x\n    extends:\n      file: x.yml\n      service: *a\n", true},
		{"svc.extends.service merge", "x-a: &a {name: n, <<: *a}\nservices:\n  t:\n    image: y\n  s:\n    image: x\n    extends:\n      file: x.yml\n      service: {<<: *a}\n", true},
		{"svc.extends.file alias", "x-a: &a {name: n, <<: *a}\nservices:\n  t:\n    image: y\n  s:\n    image: x\n    extends:\n      file: x.yml\n      file: *a\n", true},
		{"svc.extends.file merge", "x-a: &a {name: n, <<: *a}\nservices:\n  t:\n    image: y\n  s:\n    image: x\n    extends:\n      file: x.yml\n      file: {<<: *a}\n", true},
		{"top.volumes.n.external alias", "x-a: &a {name: n, <<: *a}\nservices:\n  s:\n    image: x\nvolumes:\n  n:\n    external: *a\n", true},
		{"top.volumes.n.external merge", "x-a: &a {name: n, <<: *a}\nservices:\n  s:\n    image: x\nvolumes:\n  n:\n    external: {<<: *a}\n", true},
		{"top.volumes.n.external mergeseq", "x-a: &a {name: n, <<: *a}\nservices:\n  s:\n    image: x\nvolumes:\n  n:\n    external: {<<: [*a]}\n", true},
		{"top.networks.n.external alias", "x-a: &a {name: n, <<: *a}\nservices:\n  s:\n    image: x\nnetworks:\n  n:\n    external: *a\n", true},
		{"top.networks.n.external merge", "x-a: &a {name: n, <<: *a}\nservices:\n  s:\n    image: x\nnetworks:\n  n:\n    external: {<<: *a}\n", true},
		{"top.networks.n.external mergeseq", "x-a: &a {name: n, <<: *a}\nservices:\n  s:\n    image: x\nnetworks:\n  n:\n    external: {<<: [*a]}\n", true},
		{"top.secrets.n.external alias", "x-a: &a {name: n, <<: *a}\nservices:\n  s:\n    image: x\nsecrets:\n  n:\n    external: *a\n", true},
		{"top.secrets.n.external merge", "x-a: &a {name: n, <<: *a}\nservices:\n  s:\n    image: x\nsecrets:\n  n:\n    external: {<<: *a}\n", true},
		{"top.secrets.n.external mergeseq", "x-a: &a {name: n, <<: *a}\nservices:\n  s:\n    image: x\nsecrets:\n  n:\n    external: {<<: [*a]}\n", true},
		{"top.configs.n.external alias", "x-a: &a {name: n, <<: *a}\nservices:\n  s:\n    image: x\nconfigs:\n  n:\n    external: *a\n", true},
		{"top.configs.n.external merge", "x-a: &a {name: n, <<: *a}\nservices:\n  s:\n    image: x\nconfigs:\n  n:\n    external: {<<: *a}\n", true},
		{"top.configs.n.external mergeseq", "x-a: &a {name: n, <<: *a}\nservices:\n  s:\n    image: x\nconfigs:\n  n:\n    external: {<<: [*a]}\n", true},
		{"ext name map", "x-a: &a {name: n, <<: *a}\nservices:\n  s:\n    image: x\nvolumes:\n  n:\n    external: {name: n, <<: *a}\n", true},
		{"root merge", "x-a: &a {name: n, <<: *a}\n<<: *a\nservices:\n  s:\n    image: x\n", true},
		{"root merge after", "x-a: &a {name: n, <<: *a}\nservices:\n  s:\n    image: x\n<<: *a\n", true},
		{"root merge seq", "x-a: &a {name: n, <<: *a}\n<<: [*a]\nservices:\n  s:\n    image: x\n", true},
		{"root name map", "x-a: &a {name: n, <<: *a}\nname: p\n<<: *a\nservices:\n  s:\n    image: x\n", true},
		{"a diamond of merges at the root is no cycle", "x-m: &m {name: n}\nx-a: &a {name: n, <<: *m}\nx-b: &b {name: n, <<: *m}\nx-c: &c {name: n, <<: [*a, *b]}\nname: p\nservices:\n  s:\n    image: x\n", false},
		{"a merge of a block held by an extension is no cycle", "x-m: &m {name: n}\nx-a: &a {name: n, <<: *m}\nx-b: &b {name: n, <<: *m}\nservices:\n  s:\n    image: x\nvolumes:\n  v:\n    external: *a\n", false},
		{"a depends_on that merges a good block is no cycle", "x-d: &d {condition: service_started}\nservices:\n  t:\n    image: y\n  s:\n    image: x\n    depends_on:\n      t:\n        <<: *d\n", false},
		{"an env_file that merges a good block is no cycle", "x-f: &f {path: ./e.env, required: false}\nservices:\n  s:\n    image: x\n    env_file:\n      - <<: *f\n", false},
		{"two blocks that merge themselves: the first one is named", "x-a: &a {A: '1', <<: *a}\nx-b: &b {B: '2', <<: *b}\nservices:\n  s:\n    image: x\n", true},
		{"the line named is the block's, not the first line of the file", "# a comment\n\nx-a: &a {name: n, <<: *a}\nservices:\n  s:\n    image: x\n    depends_on:\n      t:\n        <<: *a\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "c.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadFiles([]string{path}, nil)
			if (err != nil) != tc.refused {
				t.Fatalf("err %v, docker compose refuses it: %v", err, tc.refused)
			}
			if tc.refused && !strings.Contains(err.Error(), "a merge key brings in the block that holds it") {
				t.Errorf("refused, but not for the block that merges itself: %v", err)
			}
			if strings.HasPrefix(tc.name, "the line named") && !strings.Contains(err.Error(), "(line 3)") {
				t.Errorf("want the line of the block (line 3), got: %v", err)
			}
			if strings.HasPrefix(tc.name, "two blocks") && !strings.Contains(err.Error(), "(line 1)") {
				t.Errorf("want the line of the first block (line 1), got: %v", err)
			}
		})
	}
}

// A quoted `"<<"` is a key like any other, and the block that holds itself through it is refused as every alias that holds its own block is, not as a merge
// (docker compose: rc 1 for both).
func TestAQuotedMergeKeyIsNoMergeKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	body := "x-a: &a {name: n, \"<<\": *a}\nservices:\n  s:\n    image: x\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFiles([]string{path}, nil)
	if err == nil || !strings.Contains(err.Error(), "cycle detected") || strings.Contains(err.Error(), "a merge key brings in") {
		t.Fatalf("want it refused as an alias that holds its own block, got: %v", err)
	}
}

// The commands that take a project down read a file whose block merges itself, as they read one whose alias holds its own block (#1475): the refusal is kept on
// the project and the read goes on, where the load that starts a project stops on it. Where a place was read before the check was asked for (a service's `dns`,
// its `labels`) it still is, and where the walks went round in it (`depends_on`, `env_file`, the `external:` of a volume, a merge key of the document) it is
// now.
func TestACommandThatTakesAProjectDownReadsAFileWhoseBlockMergesItself(t *testing.T) {
	const svc = "services:\n  t:\n    image: y\n  s:\n    image: x\n"
	for _, tc := range []struct{ name, block, rest string }{
		{"the dns is the block", "x-k: v", svc + "    dns: *a\n"},
		{"a depends_on merges the block", "condition: service_started", svc + "    depends_on:\n      t:\n        <<: *a\n"},
		{"an env_file item merges the block", "path: ./e.env", svc + "    env_file:\n      - <<: *a\n"},
		{"the ulimits merge the block", "nofile: 1", svc + "    ulimits:\n      <<: *a\n"},
		{"a volume is external by the block", "name: n", svc + "volumes:\n  v:\n    external: *a\n"},
		{"a network is external by the block", "name: n", svc + "networks:\n  v:\n    external: *a\n"},
		{"the document merges the block", "name: p", "<<: *a\n" + svc},
		{"a list that holds itself is merged", "", "x-l: &l [*l]\n" + svc + "    environment:\n      <<: *l\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.yaml")
			body := "x-a: &a {" + tc.block + ", <<: *a}\n" + tc.rest
			if tc.block == "" {
				body = tc.rest
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFiles([]string{path}, nil); err == nil {
				t.Fatal("the load that starts a project read it, and docker compose refuses it")
			}
			p, err := LoadFilesEnvDirSoft([]string{path}, nil, "")
			if err != nil {
				t.Fatalf("the load that takes a project down stopped: %v", err)
			}
			if len(p.Services) != 2 {
				t.Fatalf("the project was not read: %d services", len(p.Services))
			}
			if fault := p.CheckValueFaults(); fault == nil || !strings.Contains(fault.Error(), "a merge key brings in the block that holds it") {
				t.Errorf("the refusal was not kept on the project: %v", fault)
			}
		})
	}
}

// The walks that refuse a block that merges itself where they are asked about it, called by themselves: the load asks before them, so nothing it is given reaches
// them (#1900), and what is kept there is what keeps them from going round for ever on a tree some other road gives them.
func TestTheWalksOfAMergeRefuseABlockThatMergesItselfWhenCalledByThemselves(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("x-a: &a {name: n, <<: *a}\n"), &doc); err != nil {
		t.Fatal(err)
	}
	block := doc.Content[0].Content[1] // the mapping the anchor is on
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"the names of an environment", func() error { return refuseNonStringKeys("environment variable", block) }},
		{"the pairs of a mapping", func() error { _, err := mergedPairs(block); return err }},
		{"the keys of a declaration", func() error { return refuseNonStringDeclKeys("a volume's declaration", block, "name") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil || !strings.Contains(err.Error(), "a merge key brings in the block that holds it") {
				t.Fatalf("want the block that merges itself refused, got: %v", err)
			}
		})
	}
}

// What the cycle did not hold is read as it was written. The merge key (or the item of a list) that brings a block in by itself is the one that is taken out, and
// not the block it is in: a list that holds a block that merges it keeps the block, and the names of the block stay (#1900).
func TestACommandThatTakesAProjectDownKeepsWhatTheCycleDidNotHold(t *testing.T) {
	const svc = "services:\n  s:\n    image: x\n"
	for _, tc := range []struct {
		name, body string
		env        []string // what the environment of s is
	}{
		{"a list that holds a block that merges it keeps the block", "x-a: &a [{name: p, <<: *a}]\n" + svc + "    environment: {<<: *a}\n", []string{"name=p"}},
		{"items that hold the list in a row go and the others stay", "x-l: &l [*l, *l, {A: '1'}]\n" + svc + "    environment: {<<: *l}\n", []string{"A=1"}},
		{"a block that is merged in a row keeps what it holds", "x-a: &a {name: n, <<: *a}\n" + svc + "    environment: {<<: [*a, *a, {B: '2'}, *a]}\n", []string{"B=2", "name=n"}},
		{"a list of two blocks that each merge it keeps both", "x-a: &a [{name: p, <<: *a}, {B: '2', <<: *a}]\n" + svc + "    environment: {<<: *a}\n", []string{"B=2", "name=p"}},
		{"an item of a list written there that merges the block is cut where it merges it, and keeps what it holds", "x-a: &a {A: '1', <<: [*a, {C: '3', <<: *a}]}\n" + svc + "    environment: {<<: *a}\n", []string{"A=1", "C=3"}},
		{"a merge key written twice in a block that merges itself: both are cut", svc + "    environment: &e {B: '2', <<: *e, <<: *e}\n", []string{"B=2"}},
		{"a mapping written there that merges the block is cut where it merges it, and keeps what it holds", "x-a: &a {A: '1', <<: {C: '3', <<: *a}}\n" + svc + "    environment: {<<: *a}\n", []string{"A=1", "C=3"}},
		{"a mapping in a mapping in a list written there is cut the same way", "x-a: &a {A: '1', <<: [{D: '4', <<: {C: '3', <<: *a}}]}\n" + svc + "    environment: {<<: *a}\n", []string{"A=1", "C=3", "D=4"}},
		{"two blocks that merge themselves are both read", "x-a: &a {A: '1', <<: *a}\nx-b: &b {B: '2', <<: *b}\n" + svc + "    environment: {<<: [*a, *b]}\n", []string{"A=1", "B=2"}},
		{"a block that is merged by a block that merges itself keeps the names of both", "x-c: &c {C: '3'}\nx-a: &a {A: '1', <<: [*c, *a]}\n" + svc + "    environment: {<<: *a}\n", []string{"A=1", "C=3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := LoadFilesEnvDirSoft([]string{path}, nil, "")
			if err != nil {
				t.Fatalf("the load that takes a project down stopped: %v", err)
			}
			got := append([]string(nil), p.Services["s"].Environment...)
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(tc.env, ",") {
				t.Errorf("environment %v, want %v", got, tc.env)
			}
		})
	}
}

// The same in every file the load that takes a project down reads — one given after the first with -f, an included one, an extended one: the tree each of them is
// read from has the cycle taken out before it is decoded, where it is first decoded.
func TestACommandThatTakesAProjectDownReadsEveryFileWhoseBlockMergesItself(t *testing.T) {
	const svc = "services:\n  s:\n    image: x\n"
	const cyc = "x-a: &a {name: n, <<: *a}\n"
	for _, tc := range []struct {
		name  string
		files [][2]string // name and body; the first is asked for, o.yml is given after it
	}{
		{"an override file", [][2]string{{"c.yml", svc}, {"o.yml", cyc + "services:\n  s:\n    environment: {<<: *a}\n"}}},
		{"an override file that is asked for first", [][2]string{{"o.yml", cyc + svc + "    environment: {<<: *a}\n"}, {"c.yml", svc}}},
		{"an included file", [][2]string{{"c.yml", "include:\n  - i.yml\n" + svc}, {"i.yml", cyc + "services:\n  t:\n    image: y\n    environment: {<<: *a}\n"}}},
		{"an extended file", [][2]string{{"c.yml", svc + "    extends: {file: i.yml, service: t}\n"}, {"i.yml", cyc + "services:\n  t:\n    image: y\n    environment: {<<: *a}\n"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var paths []string
			for _, f := range tc.files {
				path := filepath.Join(dir, f[0])
				if err := os.WriteFile(path, []byte(f[1]), 0o644); err != nil {
					t.Fatal(err)
				}
				if f[0] == "c.yml" || f[0] == "o.yml" {
					paths = append(paths, path)
				}
			}
			if tc.name == "an override file that is asked for first" {
				paths = []string{filepath.Join(dir, "o.yml"), filepath.Join(dir, "c.yml")}
			}
			if _, err := LoadFiles(paths, nil); err == nil {
				t.Fatal("the load that starts a project read it, and docker compose refuses it")
			}
			if _, err := LoadFilesEnvDirSoft(paths, nil, ""); err != nil {
				t.Fatalf("the load that takes a project down stopped: %v", err)
			}
		})
	}
}

// What the file calls what it starts is read from what a cut left, as it is read from the same file without the cycle: a `name` held by the mapping a merge key
// brings in is still the project's, and the network's, so that a command that takes a project down is not asked about another project's containers and
// networks (#1911).
func TestACommandThatTakesAProjectDownKeepsTheNamesWhatTheCycleDidNotHold(t *testing.T) {
	const svc = "services:\n  s:\n    image: x\n"
	for _, tc := range []struct {
		name, body   string
		project, net string
	}{
		{"the project's name is in a mapping written in a merge key of the document", "x-a: &a {x-k: v, <<: {name: pj, <<: *a}}\n<<: *a\n" + svc, "pj", ""},
		{"the project's name is in a mapping two merge keys down", "x-a: &a {x-k: v, <<: {x-m: 1, <<: {name: pj, <<: *a}}}\n<<: *a\n" + svc, "pj", ""},
		{"the network's name is in a mapping written in a merge key of its block", svc + "networks:\n  n1: &n {x-k: v, <<: {name: realnet, <<: *n}}\n", "", "realnet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "dirname")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "c.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := LoadFilesEnvDirSoft([]string{path}, nil, "")
			if err != nil {
				t.Fatalf("the load that takes a project down stopped: %v", err)
			}
			if tc.project != "" && p.Name != tc.project {
				t.Errorf("the project is %q, want %q (not the name of the directory)", p.Name, tc.project)
			}
			if tc.net != "" && p.Networks["n1"].Name != tc.net {
				t.Errorf("the network is %q, want %q", p.Networks["n1"].Name, tc.net)
			}
		})
	}
}
