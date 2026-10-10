package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// In a service that is taken, a date as an item of the `cache_from`, `cache_to`, `platforms` and `entitlements` of a `build` is refused in each file that writes it (a second file
// that writes the key over does not take the refusal away, and neither does the extender), while a date for `network`, `pull`, `no_cache`, `isolation` or `privileged` is asked of the
// project the files make, so a value written over it is read; a service nothing takes reads all of them (measured, v5.5.1, `config -q`, #1989).
func TestADateInTheBuildOfAServiceThatIsTaken(t *testing.T) {
	load := func(t *testing.T, files map[string]string, order ...string) error {
		t.Helper()
		dir := t.TempDir()
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		paths := make([]string, len(order))
		for i, f := range order {
			paths[i] = filepath.Join(dir, f)
		}
		_, err := LoadFiles(paths, nil)
		return err
	}
	type place struct {
		name  string
		files func(base, over string) map[string]string
		order []string
	}
	places := []place{
		{"a file on its own", func(b, o string) map[string]string {
			return map[string]string{"compose.yaml": "services:\n  web:\n    image: wi\n    build: " + b + "\n"}
		}, []string{"compose.yaml"}},
		{"a second file writes the key over it", func(b, o string) map[string]string {
			return map[string]string{"compose.yaml": "services:\n  web:\n    image: wi\n    build: " + b + "\n", "o.yaml": "services:\n  web:\n    build: " + o + "\n"}
		}, []string{"compose.yaml", "o.yaml"}},
		{"the extender writes the key over it", func(b, o string) map[string]string {
			return map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: y}\n    build: " + o + "\n", "base.yaml": "services:\n  y:\n    image: yi\n    build: " + b + "\n"}
		}, []string{"compose.yaml"}},
	}
	for _, tc := range []struct {
		key, bad, good string
		fileLevel      bool // false: asked of the project
	}{
		{"cache_from", "[2024-01-01]", "[x]", true},
		{"cache_from", "[x, 2024-01-01]", "[x]", true},
		{"cache_to", "[2024-01-01]", "[x]", true},
		{"platforms", "[2024-01-01]", "[linux/amd64]", true},
		{"platforms", "[2024-1-1]", "[linux/amd64]", true},
		{"entitlements", "[2024-01-01 10:00:00]", "[x]", true},
		{"network", "2024-01-01", "host", false},
		{"pull", "2024-01-01", "true", false},
		{"no_cache", "2024-01-01", "true", false},
		{"isolation", "2024-01-01", "default", false},
		{"privileged", "2024-01-01", "true", false},
	} {
		bad := "{context: ., " + tc.key + ": " + tc.bad + "}"
		good := "{context: ., " + tc.key + ": " + tc.good + "}"
		for _, pl := range places {
			t.Run(tc.key+"/"+tc.bad+"/"+pl.name, func(t *testing.T) {
				wantRefused := tc.fileLevel || pl.name == "a file on its own"
				err := load(t, pl.files(bad, good), pl.order...)
				if (err != nil) != wantRefused {
					t.Errorf("refused = %v (%v), want %v", err != nil, err, wantRefused)
				}
				if err != nil && !strings.Contains(err.Error(), tc.key) {
					t.Errorf("the refusal does not name %s: %v", tc.key, err)
				}
			})
		}
		t.Run(tc.key+"/"+tc.bad+"/a service nothing takes", func(t *testing.T) {
			files := map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n", "base.yaml": "services:\n  x:\n    image: xi\n  y:\n    image: yi\n    build: " + bad + "\n"}
			if err := load(t, files, "compose.yaml"); err != nil {
				t.Errorf("a service nothing takes: refused: %v", err)
			}
		})
		t.Run(tc.key+"/"+tc.bad+"/in the second service by name", func(t *testing.T) {
			files := map[string]string{"compose.yaml": "services:\n  a:\n    image: ai\n  z:\n    image: zi\n    build: " + bad + "\n"}
			if err := load(t, files, "compose.yaml"); err == nil {
				t.Errorf("a date for %s in the second service is read", tc.key)
			}
		})
		t.Run(tc.key+"/"+tc.good+"/a word is read", func(t *testing.T) {
			if err := load(t, map[string]string{"compose.yaml": "services:\n  web:\n    image: wi\n    build: " + good + "\n"}, "compose.yaml"); err != nil {
				t.Errorf("refused: %v", err)
			}
		})
	}
}
