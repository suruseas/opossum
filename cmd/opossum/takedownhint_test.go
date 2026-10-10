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

// The way down a refusal says is the project the file writes (`name: xb`), and the directory's name only where the file writes none: the order docker compose names a project
// in (-p, COMPOSE_PROJECT_NAME, `name:`, the directory). A name that cannot be known (a variable in it) is the directory's (#1953).
func TestTheWayDownIsTheNameTheFileWritesBeforeTheDirectorys(t *testing.T) {
	fakeShim(t)
	for _, tc := range []struct {
		name, compose string
		env           map[string]string
		args          []string
		want, notWant string
	}{
		{"the name the file writes", "name: xb\nservices:\n  s:\n    image: a\n    networks: [nope]\n", nil, []string{"stop"}, "`opossum -p xb down`", ""},
		{"no name: the directory's", "services:\n  s:\n    image: a\n    networks: [nope]\n", nil, []string{"stop"}, "`opossum -p wherever down`", ""},
		{"a name with a variable: the directory's", "name: ${P:-x}\nservices:\n  s:\n    image: a\n    networks: [nope]\n", nil, []string{"stop"}, "`opossum -p wherever down`", ""},
		{"COMPOSE_PROJECT_NAME is before the name the file writes", "name: xb\nservices:\n  s:\n    image: a\n    networks: [nope]\n", map[string]string{"COMPOSE_PROJECT_NAME": "fromenv"}, []string{"stop"}, "`opossum -p fromenv down`", "-p xb"},
		{"-p is before the name the file writes", "name: xb\nservices:\n  s:\n    image: a\n    networks: [nope]\n", nil, []string{"-p", "given", "stop"}, "`opossum -p given down`", "-p xb"},
		{"a name with capitals is written the way a project is named", "name: My_App\nservices:\n  s:\n    image: a\n    networks: [nope]\n", nil, []string{"stop"}, "`opossum -p my-app down`", ""},
		{"-f names the file in the directory: its name, not the other file's", "name: one\nservices:\n  s:\n    image: a\n    networks: [nope]\n", nil, []string{"-f", "alt.yaml", "stop"}, "`opossum -p two down`", "-p one"},
		{"a file in another directory: its name", "name: one\nservices:\n  s:\n    image: a\n    networks: [nope]\n", nil, []string{"-f", "sub/c.yaml", "stop"}, "`opossum -p three down`", "-p one"},
		{"the .env's COMPOSE_PROJECT_NAME is before the name the file writes", "name: xb\nservices:\n  s:\n    image: a\n    networks: [nope]\n", nil, []string{"stop"}, "`opossum -p fromdotenv down`", "-p xb"},
		{"down says it too", "name: xb\nservices:\n  s:\n    image: a\n    networks: [nope]\n", nil, []string{"down"}, "`opossum -p xb down`", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setOrUnset(t, "COMPOSE_PROJECT_NAME", nil)
			for k, v := range tc.env {
				v := v
				setOrUnset(t, k, &v)
			}
			dir := filepath.Join(t.TempDir(), "wherever")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(tc.compose), 0o644); err != nil {
				t.Fatal(err)
			}
			bad := "services:\n  s:\n    image: a\n    networks: [nope]\n"
			os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
			os.WriteFile(filepath.Join(dir, "alt.yaml"), []byte("name: two\n"+bad), 0o644)
			os.WriteFile(filepath.Join(dir, "sub", "c.yaml"), []byte("name: three\n"+bad), 0o644)
			if strings.Contains(tc.name, ".env") {
				os.WriteFile(filepath.Join(dir, ".env"), []byte("COMPOSE_PROJECT_NAME=fromdotenv\n"), 0o644)
			}
			t.Chdir(dir)
			out, err := run(t, tc.args...)
			if err == nil {
				t.Fatalf("no refusal:\n%s", out)
			}
			said := err.Error() + out
			if !strings.Contains(said, tc.want) {
				t.Errorf("want %s in:\n%s", tc.want, said)
			}
			if tc.notWant != "" && strings.Contains(said, tc.notWant) {
				t.Errorf("did not want %s in:\n%s", tc.notWant, said)
			}
		})
	}
}
