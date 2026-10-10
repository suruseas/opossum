package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// An unquoted date as an item of the `labels`, `secrets`, `ssh` or `tags` of a `build` is refused wherever the service is written — in a file on its own, in a second file, in
// the extender and in the file an extends takes the service from — and by a service nothing takes, and a word or a number there is read (measured, v5.5.1, `config -q`, #1977).
func TestADateInAListOfABuildIsRefusedWhereverItIsWritten(t *testing.T) {
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
	for _, key := range []string{"labels", "secrets", "ssh", "tags"} {
		for _, item := range []string{"2024-01-01", "x, 2024-01-01", "2024-01-01 10:00:00"} {
			spec := "    build: {context: ., " + key + ": [" + item + "]}\n"
			t.Run(key+"/"+item+"/a file on its own", func(t *testing.T) {
				if err := load(t, map[string]string{"compose.yaml": "services:\n  web:\n    image: wi\n" + spec}, "compose.yaml"); err == nil {
					t.Errorf("read")
				}
			})
			t.Run(key+"/"+item+"/a second file", func(t *testing.T) {
				if err := load(t, map[string]string{"compose.yaml": "services:\n  web:\n    image: wi\n", "o.yaml": "services:\n  web:\n" + spec}, "compose.yaml", "o.yaml"); err == nil {
					t.Errorf("read")
				}
			})
			t.Run(key+"/"+item+"/the file an extends takes it from", func(t *testing.T) {
				if err := load(t, map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: y}\n", "base.yaml": "services:\n  y:\n    image: yi\n" + spec}, "compose.yaml"); err == nil {
					t.Errorf("read")
				}
			})
			t.Run(key+"/"+item+"/a service nothing takes", func(t *testing.T) {
				if err := load(t, map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n", "base.yaml": "services:\n  x:\n    image: xi\n  y:\n    image: yi\n" + spec}, "compose.yaml"); err == nil {
					t.Errorf("read")
				}
			})
		}
	}
}

// …and a word or a quoted date is read, and an `ssh` that is a list of what it takes in a service nothing takes is not refused for having something after it.
func TestAListOfAWordInABuildIsStillRead(t *testing.T) {
	for _, tc := range []struct{ name, spec string }{
		{"labels a word", "labels: [a=b]"},
		{"tags a word", "tags: [x]"},
		{"secrets a word", "secrets: [x]"},
		{"ssh default", "ssh: [default]"},
		{"ssh an id and a path", "ssh: [a=b]"},
		{"labels a quoted date", `labels: ["2024-01-01"]`},
		{"ulimits a limit and a limit", "ulimits: {a: 1, b: 2}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := loadWithUntaken(t, "x", "    build: {context: ., "+tc.spec+"}\n"); err != nil {
				t.Errorf("refused in a service nothing takes: %v", err)
			}
		})
	}
	t.Run("a null in the second limit of ulimits is refused where nothing takes the service", func(t *testing.T) {
		if err := loadWithUntaken(t, "x", "    build: {context: ., ulimits: {a: 1, b: ~}}\n"); err == nil {
			t.Errorf("read")
		}
	})
	t.Run("a null after ssh is refused where nothing takes the service", func(t *testing.T) {
		if err := loadWithUntaken(t, "x", "    build: {context: ., ssh: ~}\n"); err == nil {
			t.Errorf("read")
		}
	})
}
