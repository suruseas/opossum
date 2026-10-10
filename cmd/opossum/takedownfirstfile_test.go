package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A service that an extends takes from another file, and that keeps a value the types refuse until a later `-f` file writes over it, is refused by the commands that read the
// project (docker compose refuses it as the first file is read), and the commands that take a project down go on: an earlier opossum read the two files together and may have
// started the project from them (#2002).
func TestAFirstFileTheTypesRefuseAfterItsExtendsDoesNotStopATakeDown(t *testing.T) {
	fakeShim(t)
	t.Setenv("COMPOSE_PROFILES", "")
	const base = "services:\n  y:\n    image: alpine:latest\n    command: [\"sleep\", \"300\"]\n"
	for _, tc := range []struct {
		name, baseExtra, over string
	}{
		{"a deploy that is a word", "    deploy: abc\n", "services:\n  s:\n    deploy: {replicas: 1}\n"},
		{"an environment with nothing after it", "    environment:\n", "services:\n  s:\n    environment: {A: b}\n"},
		{"volumes that are a word", "    volumes: abc\n", "services:\n  s:\n    volumes: []\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{
				"base.yaml": base + tc.baseExtra,
				"a.yaml":    "name: demo\nservices:\n  s:\n    extends: {file: base.yaml, service: y}\n",
				"b.yaml":    tc.over,
			}
			for name, text := range files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			fs := []string{"-f", filepath.Join(dir, "a.yaml"), "-f", filepath.Join(dir, "b.yaml")}
			t.Run("config refuses", func(t *testing.T) {
				if out, err := run(t, append(append([]string{}, fs...), "config")...); err == nil {
					t.Errorf("want the project refused, got:\n%s", out)
				}
			})
			for _, td := range []struct {
				args []string
				acts string
			}{
				{[]string{"down"}, "delete"},
				{[]string{"destroy", "--force"}, "delete"},
				{[]string{"stop"}, "stop"},
				{[]string{"kill"}, "kill"},
			} {
				t.Run("takes down: "+strings.Join(td.args, " "), func(t *testing.T) {
					readLog := fakeShim(t)
					t.Setenv("STATE_DIR", t.TempDir())
					t.Setenv("XDG_STATE_HOME", t.TempDir())
					strictContainers(t, "s.demo.opossum")
					stdout, stderr, err := runSplit(t, append(append([]string{}, fs...), td.args...)...)
					if err != nil {
						t.Fatalf("want the command to go on, got %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
					}
					acted := false
					for _, l := range readLog() {
						if strings.HasPrefix(l, td.acts+" ") && strings.Contains(l, "s.demo.opossum") {
							acted = true
						}
					}
					if !acted {
						t.Errorf("want %q sent for s.demo.opossum, got log:\n%s", td.acts, strings.Join(readLog(), "\n"))
					}
				})
			}
		})
	}
}
