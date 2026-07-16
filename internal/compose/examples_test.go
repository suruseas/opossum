package compose

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// The bundled examples are documentation users copy from, so a broken one is a
// broken promise. This walks every compose file under examples/ and asserts it
// loads and validates — catching a rotted example (a field opossum stopped
// accepting, a typo, a newly-invalid combination) before it ships. It's the first
// regression guard the examples have had.
func TestExamplesLoad(t *testing.T) {
	root := filepath.Join("..", "..", "examples")
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Compose files only — skip .env, Dockerfiles, READMEs, .gitkeep, etc.
		// Override files aren't standalone projects, so they're not loaded here.
		base := d.Name()
		if (strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml")) &&
			!strings.Contains(base, ".override.") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking examples: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("found no example compose files — wrong path?")
	}
	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			if _, err := Load(f); err != nil {
				t.Errorf("example %s failed to load: %v", f, err)
			}
		})
	}
}
