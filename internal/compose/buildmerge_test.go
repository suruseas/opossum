package compose

// A service's `build:` across several -f files, in its two forms (#790).
// docker compose (v5.5.0) reads the short form (`build: .`) as
// `{context: .}` before merging, so a later file's path changes only the
// context and a later file's mapping keeps the earlier path (`config`
// output read in full). opossum replaced the mapping with the path — the
// dockerfile, args and target vanished in silence — and replaced the path
// with the mapping — the context vanished, and a later `{labels: …}` left
// `build: {}`. A service that happens to be called `build` is not a build
// block: a scalar there is refused as it is for any service. Nor is a
// variable called `build` inside environment or build.args: two strings
// there merge as strings (the later wins), not as contexts. The rows a
// path or a mapping alone produces ("a mapping with a context over a
// path", "a path over a path", "a bare build") read the same before this
// change and pin docker's reading rather than guard it. With the args in
// list form, a path written over the mapping used to drop them; now they
// survive and the existing shape check refuses the list form — the same
// refusal one file gets.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestABuildPathAndABuildMappingMergeAsOneMapping(t *testing.T) {
	long := "services:\n  web:\n    build:\n      context: .\n      dockerfile: Dockerfile.dev\n      args:\n        A: \"1\"\n      target: dev\n"
	short := "services:\n  web:\n    build: ./other\n"
	for _, tc := range []struct {
		name, base, over            string
		context, dockerfile, target string
	}{
		{"a path over a mapping changes only the context", long, short, "./other", "Dockerfile.dev", "dev"},
		{"a mapping over a path keeps the path", short, "services:\n  web:\n    build:\n      dockerfile: Dockerfile.dev\n      target: dev\n", "./other", "Dockerfile.dev", "dev"},
		{"a mapping with a context over a path", short, "services:\n  web:\n    build:\n      context: ./third\n", "./third", "", ""},
		{"a path over a path", short, "services:\n  web:\n    build: ./third\n", "./third", "", ""},
		{"an empty path over a mapping empties the context", long, "services:\n  web:\n    build: \"\"\n", "", "Dockerfile.dev", "dev"},
		// A bare `build:` is "not given": the base stands (as for any key).
		{"a bare build over a mapping keeps it", long, "services:\n  web:\n    build:\n", ".", "Dockerfile.dev", "dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range map[string]string{"base.yaml": tc.base, "over.yaml": tc.over} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			p, err := LoadFiles([]string{filepath.Join(dir, "base.yaml"), filepath.Join(dir, "over.yaml")}, nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			b := p.Services["web"].Build
			if b == nil {
				t.Fatal("no build block after the merge")
			}
			if b.Context != tc.context || b.Dockerfile != tc.dockerfile || b.Target != tc.target {
				t.Errorf("build = {context %q, dockerfile %q, target %q}, want {%q, %q, %q}", b.Context, b.Dockerfile, b.Target, tc.context, tc.dockerfile, tc.target)
			}
		})
	}

	// The args come along too: the mapping's `A` survives a path written
	// over it.
	t.Run("the args survive a path written over the mapping", func(t *testing.T) {
		dir := t.TempDir()
		for name, body := range map[string]string{"base.yaml": long, "over.yaml": short} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		p, err := LoadFiles([]string{filepath.Join(dir, "base.yaml"), filepath.Join(dir, "over.yaml")}, nil)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if got := p.Services["web"].Build.Args; len(got) != 1 || got[0] != "A=1" {
			t.Errorf("args = %v, want [A=1]", got)
		}
	})

	// A variable called `build` is a variable: the later file's string
	// wins, in the mapping form of environment, in its list form, and in
	// build.args.
	for _, tc := range []struct{ name, base, over, field string }{
		{"a variable called build in environment", "services:\n  web:\n    image: x\n    environment:\n      build: one\n", "services:\n  web:\n    environment:\n      build: two\n", "environment"},
		{"a variable called build in environment's list form", "services:\n  web:\n    image: x\n    environment:\n      - build=one\n", "services:\n  web:\n    environment:\n      - build=two\n", "environment"},
		{"a variable called build in build.args", "services:\n  web:\n    build:\n      context: .\n      args:\n        build: one\n", "services:\n  web:\n    build:\n      args:\n        build: two\n", "args"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range map[string]string{"base.yaml": tc.base, "over.yaml": tc.over} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			p, err := LoadFiles([]string{filepath.Join(dir, "base.yaml"), filepath.Join(dir, "over.yaml")}, nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			var got []string
			if tc.field == "args" {
				got = p.Services["web"].Build.Args
			} else {
				got = p.Services["web"].Environment
			}
			if len(got) != 1 || got[0] != "build=two" {
				t.Errorf("%s = %v, want [build=two]", tc.field, got)
			}
		})
	}

	// A service called `build`, overridden by a scalar, is a service that is
	// not a mapping — refused, not read as a build block's context.
	dir := t.TempDir()
	for name, body := range map[string]string{"base.yaml": "services:\n  build:\n    image: x\n", "over.yaml": "services:\n  build: hello\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err := LoadFiles([]string{filepath.Join(dir, "base.yaml"), filepath.Join(dir, "over.yaml")}, nil)
	if err == nil {
		t.Fatal("a service written as a scalar should be refused")
	}
	if strings.Contains(err.Error(), "context") {
		t.Errorf("the scalar was read as a build context, not as a broken service:\n%v", err)
	}
}
