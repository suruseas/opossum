package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// A key a merge key brings into a declaration of the top-level `secrets`, `configs`, `networks` or `volumes` is a key written there: docker compose refuses one it does not
// take (`additional properties 'mode' not allowed`), through any depth of merge keys, an alias and a list of them, and reads a key it takes and an `x-` note (measured, v5.5.1,
// `config`, #1866).
func TestAKeyAMergeKeyBringsIntoATopLevelDeclarationIsAskedAsOneWrittenThere(t *testing.T) {
	known := map[string]string{"secrets": "file: ./f.txt", "configs": "file: ./f.txt", "networks": "driver: bridge", "volumes": "driver: local"}
	uses := map[string]string{"secrets": "    secrets: [s]\n", "configs": "    configs: [s]\n", "networks": "    networks: [s]\n", "volumes": "    volumes: [s:/v]\n"}
	for _, kind := range []string{"secrets", "configs", "networks", "volumes"} {
		for _, tc := range []struct {
			name, extra string
			refused     bool
		}{
			{"an unknown key written there", "zzz: 1", true},
			{"an unknown key through a merge key", "<<: {zzz: 1}", true},
			{"an unknown key through an alias", "<<: *a", true},
			{"an unknown key through a list of aliases", "<<: [*a]", true},
			{"an unknown key through a list of mappings", "<<: [{zzz: 1}]", true},
			{"an unknown key through a merge in a merge", "<<: {<<: {zzz: 1}}", true},
			{"mode through a merge key", "<<: {mode: -0}", true},
			{"a note written there", "x-n: 1", false},
			{"a note through a merge key", "<<: {x-n: 1}", false},
			{"a key the declaration takes through a merge key", "<<: {labels: {a: b}}", false},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
				body := "x-a: &a {zzz: 1}\nservices:\n  w:\n    image: wi\n" + uses[kind] + kind + ":\n  s: {" + known[kind] + ", " + tc.extra + "}\n"
				if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
				if _, err := LoadFiles([]string{filepath.Join(dir, "compose.yaml")}, nil); (err != nil) != tc.refused {
					t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
				}
			})
		}
	}
}
