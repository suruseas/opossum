package compose

import (
	"io/fs"
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
			if _, err := Load(f); err != nil {
				t.Errorf("real-world compose %s should load without error: %v", f, err)
			}
		})
	}
}
