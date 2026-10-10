package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// The name the files write at their top level is known without loading them where it can be known, and is not guessed where it cannot (#1953).
func TestTheNameTheFilesWriteIsKnownWhereItCanBe(t *testing.T) {
	write := func(t *testing.T, dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		files map[string]string // by name, in the directory
		paths []string          // as given; none: the names docker compose looks for
		want  string
	}{
		{"the name of the one file", map[string]string{"a.yaml": "name: xb\nservices: {s: {image: a}}\n"}, []string{"a.yaml"}, "xb"},
		{"a quoted name", map[string]string{"a.yaml": "name: \"xb\"\n"}, []string{"a.yaml"}, "xb"},
		{"the later file's name wins", map[string]string{"a.yaml": "name: one\n", "b.yaml": "name: two\n"}, []string{"a.yaml", "b.yaml"}, "two"},
		{"a later file that writes none leaves the earlier one's", map[string]string{"a.yaml": "name: one\n", "b.yaml": "services: {}\n"}, []string{"a.yaml", "b.yaml"}, "one"},
		{"no name", map[string]string{"a.yaml": "services: {s: {image: a}}\n"}, []string{"a.yaml"}, ""},
		{"a name with a variable is not known", map[string]string{"a.yaml": "name: ${P:-x}\n"}, []string{"a.yaml"}, ""},
		{"a name that is a number is not a string", map[string]string{"a.yaml": "name: 12\n"}, []string{"a.yaml"}, ""},
		{"a file that is not there", nil, []string{"a.yaml"}, ""},
		{"a file that is not YAML", map[string]string{"a.yaml": "name: [\n"}, []string{"a.yaml"}, ""},
		{"documents that agree", map[string]string{"a.yaml": "name: xb\n---\nservices: {}\n---\nname: xb\n"}, []string{"a.yaml"}, "xb"},
		{"documents that name it differently are not known", map[string]string{"a.yaml": "name: a\n---\nname: b\n"}, []string{"a.yaml"}, ""},
		{"a name in a nested mapping is not the project's", map[string]string{"a.yaml": "services:\n  s:\n    name: inner\n"}, []string{"a.yaml"}, ""},
		{"the base file found by its name", map[string]string{"compose.yaml": "name: found\n"}, nil, "found"},
		{"docker-compose.yml when there is no compose.yaml", map[string]string{"docker-compose.yml": "name: old\n"}, nil, "old"},
		{"the first of the names docker compose looks for", map[string]string{"compose.yaml": "name: first\n", "docker-compose.yml": "name: second\n"}, nil, "first"},
		{"the override found beside it wins", map[string]string{"compose.yaml": "name: base\n", "compose.override.yaml": "name: over\n"}, nil, "over"},
		{"the overlay of opossum wins last", map[string]string{"compose.yaml": "name: base\n", "compose.override.yaml": "name: over\n", "compose.opossum.yaml": "name: overlay\n"}, nil, "overlay"},
		{"a later file's name with a variable is not known", map[string]string{"a.yaml": "name: one\n", "b.yaml": "name: ${P}\n"}, []string{"a.yaml", "b.yaml"}, ""},
		{"an alias as the name", map[string]string{"a.yaml": "x-n: &n xb\nname: *n\n"}, []string{"a.yaml"}, "xb"},
		{"a document that is not YAML after a name", map[string]string{"a.yaml": "name: xb\n---\n[\n"}, []string{"a.yaml"}, ""},
		{"a file that is not there before one that is", map[string]string{"b.yaml": "name: two\n"}, []string{"gone.yaml", "b.yaml"}, "two"},
		{"an override with no base file", map[string]string{"compose.override.yaml": "name: over\n"}, nil, ""},
		{"a later file's empty name is not known", map[string]string{"a.yaml": "name: base\n", "b.yaml": "name: \"\"\n"}, []string{"a.yaml", "b.yaml"}, ""},
		{"a later document's empty name is not known", map[string]string{"a.yaml": "name: xb\n---\nname: \"\"\n"}, []string{"a.yaml"}, ""},
		{"no file found", map[string]string{"other.yaml": "name: x\n"}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				write(t, dir, name, body)
			}
			paths := make([]string, len(tc.paths))
			for i, p := range tc.paths {
				paths[i] = filepath.Join(dir, p)
			}
			if got := WrittenProjectName(dir, paths); got != tc.want {
				t.Errorf("WrittenProjectName = %q, want %q", got, tc.want)
			}
		})
	}
}
