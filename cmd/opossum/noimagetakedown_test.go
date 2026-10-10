package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A service left with neither an `image` nor a `build` is refused by the commands that read the project, and the commands that take a project down name it on
// stderr and go on: an earlier opossum read an `!override` that comes by a plain anchor as a plain merge, so the image of the first file stayed, and a project it
// started from the two files is running (#1947; found on a real runtime: `down` from the project's directory was refused, with the container left).
func TestAServiceWithNoImageIsRefusedButDoesNotStopATakeDown(t *testing.T) {
	fakeShim(t)
	t.Setenv("COMPOSE_PROFILES", "")
	const base = "name: demo\nservices:\n  s:\n    image: alpine:latest\n    command: [\"sleep\", \"300\"]\n    ports: [\"38481:38481\"]\n    labels: {a: \"1\"}\n"
	const want = `service "s" must set either image or build`
	for _, tc := range []struct {
		name  string
		files []string
	}{
		{"an !override that comes by a plain anchor", []string{base, "x-c: &c !override {labels: {z: '9'}}\nx-b: &b {<<: *c}\nservices:\n  s: {<<: *b}\n"}},
		{"an !override on the service itself", []string{base, "services:\n  s: !override\n    labels: {z: '9'}\n"}},
		{"an image reset", []string{base, "services:\n  s:\n    image: !reset null\n"}},
		{"a single file with a service of neither", []string{"name: demo\nservices:\n  s:\n    command: [\"sleep\", \"300\"]\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var fs []string
			for i, text := range tc.files {
				p := filepath.Join(dir, "f"+string(rune('0'+i))+".yaml")
				if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
				fs = append(fs, "-f", p)
			}
			for _, args := range [][]string{{"config"}, {"ps"}, {"up"}, {"logs"}, {"restart"}, {"start"}, {"build"}} {
				t.Run("refuses "+strings.Join(args, " "), func(t *testing.T) {
					out, err := run(t, append(append([]string{}, fs...), args...)...)
					if err == nil || !strings.Contains(err.Error(), want) {
						t.Errorf("want %q refused, got err %v, out:\n%s", want, err, out)
					}
				})
			}
			for _, td := range []struct {
				args []string
				acts string // a line the fake runtime logs when the command acted
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
					if !strings.Contains(stderr, want+" — `up` refuses this compose file; going on, as an earlier opossum may have started it") {
						t.Errorf("want the refusal named on stderr, got:\n%s", stderr)
					}
					if strings.Contains(stdout, want) {
						t.Errorf("the note belongs on stderr, got stdout:\n%s", stdout)
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

// A service with no image is let go and the services after it are read all the same: what they are refused for stops a take-down, as it does for any file. A
// project is not taken down by a file that reads past its own mistakes silently (#1947).
func TestAServiceWithNoImageDoesNotLetThePartsAfterItGo(t *testing.T) {
	fakeShim(t)
	t.Setenv("COMPOSE_PROFILES", "")
	for _, tc := range []struct{ name, text, want string }{
		{"an undeclared network of the service itself", "name: demo\nservices:\n  a:\n    command: [\"true\"]\n    networks: [nope]\n", `references undefined network "nope"`},
		{"an unknown dependency of a service after it", "name: demo\nservices:\n  a:\n    command: [\"true\"]\n  d:\n    image: d\n    depends_on: [zz]\n", `zz`},
	} {
		for _, args := range [][]string{{"down"}, {"stop"}, {"kill"}, {"destroy", "--force"}} {
			t.Run(tc.name+": "+strings.Join(args, " "), func(t *testing.T) {
				t.Setenv("STATE_DIR", t.TempDir())
				t.Setenv("XDG_STATE_HOME", t.TempDir())
				compose := writeCompose(t, tc.text)
				_, stderr, err := runSplit(t, append([]string{"-f", compose}, args...)...)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("want %q refused, got err %v\nstderr:\n%s", tc.want, err, stderr)
				}
			})
		}
	}
}

// What is said of a file with two faults is the first one found: the missing image is the last the project is read for, so a value docker compose reads as another
// kind, found earlier, is the one the take-down names.
func TestAServiceWithNoImageDoesNotTakeTheFaultOfAnEarlierValue(t *testing.T) {
	fakeShim(t)
	t.Setenv("COMPOSE_PROFILES", "")
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	compose := writeCompose(t, "name: demo\nservices:\n  a:\n    image: a\n    scale: -1\n  z:\n    command: [\"true\"]\n")
	stdout, stderr, err := runSplit(t, "-f", compose, "down")
	if err != nil {
		t.Fatalf("want the command to go on, got %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stderr, "services.a.scale -1 is below the least, 0 — `up` refuses this compose file; going on") {
		t.Errorf("want the earlier value named, got:\n%s", stderr)
	}
	if strings.Contains(stderr, "must set either image or build") {
		t.Errorf("the missing image is named over the earlier value:\n%s", stderr)
	}
}
