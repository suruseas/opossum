package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A secret or a config named outside letters, digits, `.`, `_` and `-` is
// refused by docker compose for every command, and here by every command that
// starts, prints or lists — but not by the ones that take a project down: an
// earlier opossum passed such a name on as the mount path and ran the project,
// and refusing `down` would leave it with no way down but `container delete`.
// The service's own name is refused by all of them, an earlier opossum having
// refused it before it made anything.
func TestASecretOrConfigNameOutsideTheRule(t *testing.T) {
	bodies := map[string]string{
		"secret": "name: demo\nservices:\n  web:\n    image: alpine:3.20\n    secrets: [\"a b\"]\nsecrets:\n  \"a b\": {file: ./s.txt}\n",
		"config": "name: demo\nservices:\n  web:\n    image: alpine:3.20\n    configs: [\"c/d\"]\nconfigs:\n  \"c/d\": {content: hi}\n",
	}
	want := map[string]string{"secret": `secret name "a b"`, "config": `config name "c/d"`}
	refuses := [][]string{{"up", "--dry-run"}, {"config"}, {"ps"}, {"run", "--no-deps", "web", "true"}, {"logs", "--tail", "1"}, {"images"},
		{"exec", "web", "true"}, {"cp", "web:/etc/hostname", "./here"}, {"stats", "--no-stream"}, {"restart"}, {"start"}, {"pull"},
		{"volumes"}, {"port", "web", "80"}, {"build"}, {"import"}}
	goesOn := [][]string{{"down"}, {"stop"}, {"kill"}, {"destroy", "--dry-run"}}
	for kind, body := range bodies {
		for _, args := range refuses {
			t.Run(kind+" "+strings.Join(args, " ")+" refuses", func(t *testing.T) {
				t.Setenv("XDG_STATE_HOME", t.TempDir())
				fakeShim(t)
				out, err := run(t, append([]string{"-f", writeCompose(t, body)}, args...)...)
				if err == nil || !strings.Contains(err.Error(), want[kind]) {
					t.Fatalf("want a refusal saying %q, got %v\n%s", want[kind], err, out)
				}
			})
		}
		for _, args := range goesOn {
			t.Run(kind+" "+strings.Join(args, " ")+" goes on", func(t *testing.T) {
				t.Setenv("XDG_STATE_HOME", t.TempDir())
				fakeShim(t)
				_, stderr, err := runSplit(t, append([]string{"-f", writeCompose(t, body)}, args...)...)
				if err != nil {
					t.Fatalf("want %s to take the project down as it always did, got %v", args[0], err)
				}
				if !strings.Contains(stderr, want[kind]) || !strings.Contains(stderr, "going on") {
					t.Errorf("want the name said on stderr and the command to go on, got:\n%s", stderr)
				}
			})
		}
	}
}

func TestAServiceNameOutsideTheRuleIsRefusedByEveryCommand(t *testing.T) {
	body := "name: demo\nservices:\n  \"w b\":\n    image: alpine:3.20\n"
	for _, args := range [][]string{{"up", "--dry-run"}, {"config"}, {"ps"}, {"down"}, {"stop"}, {"kill"}, {"destroy", "--dry-run"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			fakeShim(t)
			out, err := run(t, append([]string{"-f", writeCompose(t, body)}, args...)...)
			if err == nil || !strings.Contains(err.Error(), `service name "w b"`) {
				t.Fatalf("want the service name refused, got %v\n%s", err, out)
			}
		})
	}
}

// The same for a variable name in the project's `.env`, which docker compose
// refuses in every command and an earlier opossum passed on: a project started on
// `A/B=1` (measured on container 1.4.1: the runtime takes it) is one `down` has to
// be able to take down. Read past by the load, kept for the commands that start,
// print or list something (which refuse it), and named on stderr by the four that
// take a project down (which go on).
func TestAnEnvVariableNameOutsideTheRuleInTheProjectDotEnv(t *testing.T) {
	const compose = "name: demo\nservices:\n  web:\n    image: alpine:3.20\n"
	for _, tc := range []struct{ name, dotenv, want string }{
		{"a symbol", "A/B=1\nOK=1\n", `unexpected character "/" in variable name`},
		{"a space", "A B=1\nOK=1\n", "key cannot contain a space"},
		{"a symbol after a value that spans lines", "P=\"one\ntwo\"\nA$B=1\n", `unexpected character "$" in variable name`},
	} {
		for _, args := range [][]string{{"up", "--dry-run"}, {"config"}, {"ps"}, {"logs", "--tail", "1"}, {"images"}} {
			t.Run(tc.name+" "+strings.Join(args, " ")+" refuses", func(t *testing.T) {
				t.Setenv("XDG_STATE_HOME", t.TempDir())
				fakeShim(t)
				file := writeCompose(t, compose)
				if err := os.WriteFile(filepath.Join(filepath.Dir(file), ".env"), []byte(tc.dotenv), 0o644); err != nil {
					t.Fatal(err)
				}
				out, err := run(t, append([]string{"-f", file}, args...)...)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("want a refusal saying %q, got %v\n%s", tc.want, err, out)
				}
			})
		}
		for _, args := range [][]string{{"down"}, {"stop"}, {"kill"}, {"destroy", "--dry-run"}} {
			t.Run(tc.name+" "+strings.Join(args, " ")+" goes on", func(t *testing.T) {
				t.Setenv("XDG_STATE_HOME", t.TempDir())
				fakeShim(t)
				file := writeCompose(t, compose)
				if err := os.WriteFile(filepath.Join(filepath.Dir(file), ".env"), []byte(tc.dotenv), 0o644); err != nil {
					t.Fatal(err)
				}
				_, stderr, err := runSplit(t, append([]string{"-f", file}, args...)...)
				if err != nil {
					t.Fatalf("want %s to take the project down as it always did, got %v", args[0], err)
				}
				if !strings.Contains(stderr, tc.want) || !strings.Contains(stderr, "going on") {
					t.Errorf("want the name said on stderr and the command to go on, got:\n%s", stderr)
				}
			})
		}
	}
}

// A `.env` of an included project holds the same: a project started on it is taken
// down by the four commands, which say so and go on, and refused by the rest.
func TestAnIncludedProjectsDotEnvNameOutsideTheRule(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		goesOn bool
	}{
		{[]string{"config"}, false}, {[]string{"ps"}, false},
		{[]string{"down"}, true}, {[]string{"stop"}, true}, {[]string{"kill"}, true},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			fakeShim(t)
			file := writeCompose(t, "name: demo\ninclude:\n  - sub/compose.yaml\nservices:\n  web:\n    image: alpine:3.20\n")
			dir := filepath.Dir(file)
			if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			for name, body := range map[string]string{"sub/compose.yaml": "services:\n  db:\n    image: alpine:3.20\n", "sub/.env": "A/B=1\n"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, stderr, err := runSplit(t, append([]string{"-f", file}, tc.args...)...)
			if tc.goesOn {
				if err != nil || !strings.Contains(stderr, `unexpected character "/"`) || !strings.Contains(stderr, "going on") {
					t.Fatalf("want the name said and the command to go on, got %v\n%s", err, stderr)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), `unexpected character "/"`) {
				t.Fatalf("want a refusal, got %v", err)
			}
		})
	}
}
