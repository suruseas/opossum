package compose

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCorpusLoads is the compat regression gate. testdata/corpus holds compose
// files modeled on real-world projects (awesome-compose and the like), each
// exercising patterns that trip up a parser or validator — network_mode: host,
// depends_on conditions, CMD-SHELL healthchecks with $$, named volumes on data
// dirs, multiple/internal networks, static IPs + ipam, cap_add/drop, profiles,
// secrets, build targets, legacy ignored fields. Every one must LOAD without
// error (ignored fields are fine; a hard failure is not).
//
// This is what catches the class of regression manual dogfooding finds — e.g. a
// validation change that turns an ignored field into a load error and breaks a
// real compose file. When dogfooding surfaces a new pattern that broke, add a
// representative file here so it can never regress again.
func TestCorpusLoads(t *testing.T) {
	root := filepath.Join("testdata", "corpus")
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && (strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking corpus: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no corpus files found — wrong path?")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			p, err := Load(f)
			if err != nil {
				t.Fatalf("real-world compose %s should load without error: %v", f, err)
			}
			// `config` is a fixed point: feeding its output back through `config`
			// yields the same document. The trailing comments (fields opossum
			// ignores, the restart caveat) are about the input file, not the
			// document, so the comparison stops where they start. This is what
			// caught `$$` being written back as `$` (#935): a shape-level check
			// of the output cannot see a value whose meaning changed.
			first, err := RenderConfig(p)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			again := filepath.Join(t.TempDir(), "compose.yaml")
			if err := os.WriteFile(again, []byte(first), 0o644); err != nil {
				t.Fatal(err)
			}
			p2, err := Load(again)
			if err != nil {
				t.Fatalf("the rendered config should load back: %v\n%s", err, first)
			}
			second, err := RenderConfig(p2)
			if err != nil {
				t.Fatalf("render again: %v", err)
			}
			a, b := configDocument(first), configDocument(second)
			if !strings.Contains(a, "services:") {
				t.Fatalf("the compared document is not the config itself: %q", a)
			}
			if a != b {
				t.Errorf("config is not a fixed point for %s:\n--- first\n%s\n--- second\n%s", f, a, b)
			}
		})
	}
}

// configDocument is the rendered config up to its first trailing comment
// block: the YAML document itself, without the notes about the input file.
func configDocument(rendered string) string {
	if i := strings.Index(rendered, "\n#"); i >= 0 {
		return rendered[:i]
	}
	return rendered
}
