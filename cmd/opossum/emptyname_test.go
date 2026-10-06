package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A project whose `name:` is written with a reference that comes to nothing (`name: ${P:-}`, P unset or empty) is refused as docker compose
// refuses it (`project name must not be empty`, v5.5.1 `config -q`, every row measured) unless `-p` or COMPOSE_PROJECT_NAME names the project;
// `name: ""` is the directory's, as it is there. Of several files, a reference that came to nothing in
// any of them stays the refusal until a later file writes a name with something in it (`name: ""` does not take it away), and an included
// file's `name:` is not the project's. The commands that take a project
// down name it and go on (an earlier opossum started it under the directory's name), and are asked by TestAnEmptiedNameDoesNotStopATakeDown.
func TestAProjectNameThatComesToNothingByAVariable(t *testing.T) {
	const svc = "services:\n  a:\n    image: alpine:3.20\n"
	type file struct{ name, body string }
	for _, tc := range []struct {
		name    string
		files   []file
		flags   []string
		env     map[string]string
		refused bool
	}{
		{"a default that is empty", []file{{"c.yaml", "name: ${P:-}\n" + svc}}, nil, nil, true},
		{"a variable that is not set", []file{{"c.yaml", "name: ${P}\n" + svc}}, nil, nil, true},
		{"a quoted reference", []file{{"c.yaml", "name: \"${P:-}\"\n" + svc}}, nil, nil, true},
		{"two references that are both empty", []file{{"c.yaml", "name: ${P:-}${Q:-}\n" + svc}}, nil, nil, true},
		{"the other side of a conditional", []file{{"c.yaml", "name: ${P:+x}\n" + svc}}, nil, nil, true},
		{"an alias to a reference that is empty", []file{{"c.yaml", "x-n: &n ${P:-}\nname: *n\n" + svc}}, nil, nil, true},
		{"the variable is set", []file{{"c.yaml", "name: ${P:-}\n" + svc}}, nil, map[string]string{"P": "ok"}, false},
		{"a default that is not empty", []file{{"c.yaml", "name: ${P:-dflt}\n" + svc}}, nil, nil, false},
		{"a name written as an empty string", []file{{"c.yaml", "name: \"\"\n" + svc}}, nil, nil, false},
		{"-p names the project", []file{{"c.yaml", "name: ${P:-}\n" + svc}}, []string{"-p", "x"}, nil, false},
		{"COMPOSE_PROJECT_NAME names the project", []file{{"c.yaml", "name: ${P:-}\n" + svc}}, nil, map[string]string{"COMPOSE_PROJECT_NAME": "e"}, false},
		{"a name, then another file that empties it", []file{{"a.yaml", "name: n\n" + svc}, {"b.yaml", "name: ${P:-}\n"}}, nil, nil, true},
		{"an empty one, then another file that names it", []file{{"a.yaml", "name: ${P:-}\n" + svc}, {"b.yaml", "name: n\n"}}, nil, nil, false},
		{"an empty one, then a file with no name", []file{{"a.yaml", "name: ${P:-}\n" + svc}, {"b.yaml", "services:\n  a:\n    hostname: h\n"}}, nil, nil, true},
		{"an empty one, then a name written as an empty string", []file{{"a.yaml", "name: ${P:-}\n" + svc}, {"b.yaml", "name: \"\"\n"}}, nil, nil, true},
		{"an empty string, then an empty one", []file{{"a.yaml", "name: \"\"\n" + svc}, {"b.yaml", "name: ${P:-}\n"}}, nil, nil, true},
		{"a name, an empty one, then an empty string", []file{{"a.yaml", "name: n\n" + svc}, {"b.yaml", "name: ${P:-}\n"}, {"c.yaml", "name: \"\"\n"}}, nil, nil, true},
		{"an empty one, then an empty string", []file{{"a.yaml", "name: ${P:-}\n" + svc}, {"b.yaml", "name: \"\"\n"}}, nil, nil, true},
		{"a name, an empty one, then another name", []file{{"a.yaml", "name: n\n" + svc}, {"b.yaml", "name: ${P:-}\n"}, {"c.yaml", "name: m\n"}}, nil, nil, false},
		{"an empty one, a name, then an empty string", []file{{"a.yaml", "name: ${P:-}\n" + svc}, {"b.yaml", "name: n\n"}, {"c.yaml", "name: \"\"\n"}}, nil, nil, false},
		{"an empty one, a name from a variable, then an empty string", []file{{"a.yaml", "name: ${P:-}\n" + svc}, {"b.yaml", "name: ${T}\n"}, {"c.yaml", "name: \"\"\n"}}, nil, map[string]string{"T": "t"}, false},
		{"an empty one, a name through an alias, then an empty string", []file{{"a.yaml", "name: ${P:-}\n" + svc}, {"b.yaml", "x-n: &n m\nname: *n\n"}, {"c.yaml", "name: \"\"\n"}}, nil, nil, false},
		{"an empty string and a reference elsewhere", []file{{"c.yaml", "name: \"\"\nservices:\n  a:\n    image: ${I:-alpine:3.20}\n"}}, nil, nil, false},
		{"no name and a reference elsewhere", []file{{"c.yaml", "services:\n  a:\n    image: ${I:-alpine:3.20}\n"}}, nil, nil, false},
		{"the name written after the services", []file{{"c.yaml", svc + "name: ${P:-}\n"}}, nil, nil, true},
		{"both empty", []file{{"a.yaml", "name: ${P:-}\n" + svc}, {"b.yaml", "name: ${Q:-}\n"}}, nil, nil, true},
		{"a name from a variable, then an empty string", []file{{"a.yaml", "name: ${T}\n" + svc}, {"b.yaml", "name: \"\"\n"}}, nil, map[string]string{"T": "ok"}, false},
		{"a name from a variable with a colon in it, then an empty string", []file{{"a.yaml", "name: ${T}\n" + svc}, {"b.yaml", "name: \"\"\n"}}, nil, map[string]string{"T": "a: b"}, false},
		{"a name, then an empty string", []file{{"a.yaml", "name: n\n" + svc}, {"b.yaml", "name: \"\"\n"}}, nil, nil, false},
		{"an included file's name is not the project's", []file{{"c.yaml", "include:\n  - i.yaml\n" + svc}, {"i.yaml", "name: ${P:-}\nservices:\n  b:\n    image: alpine:3.20\n"}}, nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			fakeShim(t)
			for _, k := range []string{"P", "Q", "COMPOSE_PROJECT_NAME"} {
				t.Setenv(k, "")
				os.Unsetenv(k)
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			dir := t.TempDir()
			args := append([]string{}, tc.flags...)
			for _, f := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.body), 0o644); err != nil {
					t.Fatal(err)
				}
				if f.name != "i.yaml" {
					args = append(args, "-f", filepath.Join(dir, f.name))
				}
			}
			out, err := run(t, append(args, "config")...)
			if tc.refused != (err != nil && strings.Contains(err.Error(), "project name must not be empty")) {
				t.Errorf("config: err %v, docker compose refuses it: %v\n%s", err, tc.refused, out)
			}
		})
	}
}

// Every command that starts, prints or lists refuses the name; the ones that take a project down say so on stderr and go on.
func TestAnEmptiedNameIsRefusedByTheCommandsThatStartAndNotTheOnesThatTakeDown(t *testing.T) {
	body := "name: ${P:-}\nservices:\n  web:\n    image: alpine:3.20\n"
	refuses := [][]string{{"up", "--dry-run"}, {"config"}, {"ps"}, {"logs", "--tail", "1"}, {"images"}, {"restart"}, {"start"}, {"pull"}, {"volumes"}}
	goesOn := [][]string{{"down"}, {"stop"}, {"kill"}, {"destroy", "--dry-run"}}
	for _, args := range refuses {
		t.Run(strings.Join(args, " ")+" refuses", func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			fakeShim(t)
			t.Setenv("P", "")
			os.Unsetenv("P")
			out, err := run(t, append([]string{"-f", writeCompose(t, body)}, args...)...)
			if err == nil || !strings.Contains(err.Error(), "project name must not be empty") {
				t.Fatalf("want a refusal saying the name is empty, got %v\n%s", err, out)
			}
		})
	}
	t.Run("down with -p says nothing of the name", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		fakeShim(t)
		t.Setenv("P", "")
		os.Unsetenv("P")
		_, stderr, err := runSplit(t, "-p", "x", "-f", writeCompose(t, body), "down")
		if err != nil || strings.Contains(stderr, "project name must not be empty") {
			t.Errorf("want -p to name the project and nothing said of its file's name, got %v\n%s", err, stderr)
		}
	})
	for _, args := range goesOn {
		t.Run(strings.Join(args, " ")+" goes on", func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			fakeShim(t)
			t.Setenv("P", "")
			os.Unsetenv("P")
			_, stderr, err := runSplit(t, append([]string{"-f", writeCompose(t, body)}, args...)...)
			if err != nil {
				t.Fatalf("want %s to take the project down as it always did, got %v", args[0], err)
			}
			if !strings.Contains(stderr, "project name must not be empty") || !strings.Contains(stderr, "going on") {
				t.Errorf("want the name said on stderr and the command to go on, got:\n%s", stderr)
			}
		})
	}
}
