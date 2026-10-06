package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A compose file this version refuses may be one an earlier version started the project from (a file of several YAML documents was
// read by its first document alone before 0.38.0, and a later document may hold what 0.38.0 refuses): `down` says the project can
// still be taken down by naming it, and `stop`, `kill` and `destroy` say it too, in the same words, since none of them has a way
// down by name of its own (#1481).
func TestTheCommandsThatTakeAProjectDownSayHowToNameItWhenTheFileIsRefused(t *testing.T) {
	fakeShim(t)
	setOrUnset(t, "COMPOSE_PROJECT_NAME", nil)
	dir := t.TempDir()
	// The second document is what this version refuses.
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("name: demo\nservices:\n  web:\n    image: web\n---\nservices:\n  web:\n    ports: 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	hint := "the project's containers can still be taken down without its file, by naming it: `opossum -p mine down`"
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{"stop", []string{"-p", "mine", "stop"}, true},
		{"kill", []string{"-p", "mine", "kill"}, true},
		{"destroy", []string{"-p", "mine", "destroy", "--force"}, true},
		{"logs refuses and does not take anything down", []string{"-p", "mine", "logs"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := run(t, tc.args...)
			if err == nil {
				t.Fatalf("%s: no refusal:\n%s", tc.name, out)
			}
			said := err.Error() + out
			if !strings.Contains(said, "ports must be a list") {
				t.Errorf("%s: the refusal of the file is not there:\n%s", tc.name, said)
			}
			if got := strings.Contains(said, hint); got != tc.want {
				t.Errorf("%s: says how to name the project: %v, want %v:\n%s", tc.name, got, tc.want, said)
			}
			if strings.Count(said, "-p mine down") > 1 {
				t.Errorf("%s: the way down is said more than once:\n%s", tc.name, said)
			}
		})
	}

	t.Run("down says it once, whatever the name", func(t *testing.T) {
		out, err := run(t, "down")
		if err == nil {
			t.Fatalf("down: no refusal:\n%s", out)
		}
		if got := strings.Count(err.Error()+out, "can still be taken down without its file"); got != 1 {
			t.Errorf("down says how to name the project %d times, want once:\n%s", got, err.Error()+out)
		}
	})

	t.Run("a project the documents name differently is asked for by name, with that and not this hint", func(t *testing.T) {
		two := t.TempDir()
		if err := os.WriteFile(filepath.Join(two, "compose.yaml"),
			[]byte("name: a\nservices:\n  web:\n    image: web\n---\nname: b\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(two)
		out, err := run(t, "stop")
		if err == nil {
			t.Fatalf("stop: no refusal:\n%s", out)
		}
		said := err.Error() + out
		if !strings.Contains(said, "name the project you mean") || strings.Contains(said, "without its file") {
			t.Errorf("the refusal is not the one that asks for the name, alone:\n%s", said)
		}
	})

	t.Run("a file that is read gets no hint", func(t *testing.T) {
		good := t.TempDir()
		if err := os.WriteFile(filepath.Join(good, "compose.yaml"), []byte("services:\n  web:\n    image: web\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(good)
		out, err := run(t, "-p", "mine", "stop")
		if err != nil {
			t.Fatalf("stop: %v\n%s", err, out)
		}
		if strings.Contains(out, "without its file") {
			t.Errorf("a file that was read comes with the way down without it:\n%s", out)
		}
	})
}
