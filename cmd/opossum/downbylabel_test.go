package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A `down` whose compose file cannot be read takes the project down by its label,
// as docker compose does for `down -p <name>`: the containers the runtime holds
// for it and its default network, and nothing of another project's. It is what
// keeps a project an earlier opossum started from a file the current one refuses
// from being stranded — whatever the file is refused for, and for whatever
// refusal is added next.
func TestDownTakesAProjectDownByLabelWhenItsFileCannotBeRead(t *testing.T) {
	const ours = `{"status":{"state":"running"},"configuration":{"id":"web.demo.opossum","labels":{"opossum.project":"demo"}}}`
	const theirs = `{"status":{"state":"running"},"configuration":{"id":"db.other.opossum","labels":{"opossum.project":"other"}}}`
	// A file the loader refuses, for a reason that has nothing to do with `down`.
	const broken = "services:\n  web:\n    image: alpine:3\n    volumes: [\"\"]\n"
	for _, tc := range []struct {
		name    string
		files   map[string]string // relative to the project's directory, which is named "demo"
		args    []string
		env     map[string]string
		wantRun bool // takes ours down
	}{
		{"-p names the project", map[string]string{"compose.yaml": broken}, []string{"-p", "demo", "down"}, nil, true},
		{"-p is written the way a project is named", map[string]string{"compose.yaml": broken}, []string{"-p", "Demo", "down"}, nil, true},
		{"COMPOSE_PROJECT_NAME in the shell names it", map[string]string{"compose.yaml": broken}, []string{"down"}, map[string]string{"COMPOSE_PROJECT_NAME": "demo"}, true},
		{"COMPOSE_PROJECT_NAME in the .env names it", map[string]string{"compose.yaml": broken, ".env": "COMPOSE_PROJECT_NAME=demo\n"}, []string{"down"}, nil, true},
		{"-p and no file at all, as docker compose reads it", nil, []string{"-p", "demo", "down"}, nil, true},
		{"COMPOSE_PROJECT_NAME and no file at all", nil, []string{"down"}, map[string]string{"COMPOSE_PROJECT_NAME": "demo"}, true},
		{"-p and a file named that is not there", nil, []string{"-p", "demo", "-f", "nosuch.yaml", "down"}, nil, true},
		// The controls. Nothing here is named, so nothing is taken down: the folder's
		// name is the project's unless the file says otherwise, and the file is what
		// could not be read — a guess that removes what belongs to somebody else.
		{"the directory's name is not a name", map[string]string{"compose.yaml": broken}, []string{"down"}, nil, false},
		{"a file that names another project, unreadable", map[string]string{"compose.yaml": "name: other\n" + broken}, []string{"down"}, nil, false},
		{"a -f with a typo", map[string]string{"compose.yaml": "name: demo\nservices:\n  web:\n    image: alpine:3\n"}, []string{"-f", "compose.yml", "down"}, nil, false},
		{"an --env-file that is not there", map[string]string{"compose.yaml": broken}, []string{"--env-file", ".env.prod", "down"}, nil, false},
		{"no file and no name is not a guess", nil, []string{"down"}, nil, false},
		// Another project's name reaches that project alone.
		{"another project's name reaches only that project", map[string]string{"compose.yaml": broken}, []string{"-p", "other", "down"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			log := fakeShim(t)
			for _, n := range []string{"COMPOSE_PROJECT_NAME", "COMPOSE_FILE", "COMPOSE_PATH_SEPARATOR", "COMPOSE_PROFILES"} {
				t.Setenv(n, "")
				os.Unsetenv(n)
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			dir := filepath.Join(t.TempDir(), "demo")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(dir)
			t.Setenv("CONTAINER_LS", "["+ours+","+theirs+"]")
			_, stderr, err := runSplit(t, tc.args...)
			deleted := func(name string) bool {
				for _, l := range log() {
					if strings.Contains(l, "delete --force "+name) {
						return true
					}
				}
				return false
			}
			if deleted("db.other.opossum") && !strings.Contains(strings.Join(tc.args, " "), "other") {
				t.Errorf("another project's container was taken down:\n%v", log())
			}
			if tc.wantRun {
				if err != nil {
					t.Fatalf("want the project taken down, got %v\n%s", err, stderr)
				}
				if !deleted("web.demo.opossum") {
					t.Errorf("the project's container was not taken down: %v", log())
				}
				if !strings.Contains(stderr, "without a compose file to read") || !strings.Contains(stderr, `"demo"`) {
					t.Errorf("want it said which project is taken down and that there is no file, got:\n%s", stderr)
				}
				return
			}
			if deleted("web.demo.opossum") {
				t.Errorf("ours was taken down where the project was not named: %v", log())
			}
			if !strings.Contains(strings.Join(tc.args, " "), "other") {
				if err == nil {
					t.Errorf("a guess was refused with no error at all:\n%s", stderr)
				} else {
					// The way down names the project the file writes (`name: other`) where it writes one, the directory's ("demo") where not (#1953).
					want := "`opossum -p demo down`"
					if strings.Contains(tc.files["compose.yaml"], "name: other") {
						want = "`opossum -p other down`"
					}
					if !strings.Contains(err.Error(), want) {
						t.Errorf("the refusal should say how to name the project (%s), got: %v", want, err)
					}
				}
			}
		})
	}
}

// A name that is not one the runtime holds is not made to sound like a project
// taken down.
func TestDownByLabelSaysWhenThereIsNothingUnderTheName(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	log := fakeShim(t)
	t.Setenv("CONTAINER_LS", `[{"status":{"state":"running"},"configuration":{"id":"web.demo.opossum","labels":{"opossum.project":"demo"}}}]`)
	dir := filepath.Join(t.TempDir(), "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	_, stderr, err := runSplit(t, "-p", "nothere", "down")
	if err != nil {
		t.Fatalf("down: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, `holds no container of project "nothere"`) {
		t.Errorf("want it said that nothing is held under that name, got:\n%s", stderr)
	}
	for _, l := range log() {
		if strings.Contains(l, "delete") {
			t.Errorf("something was deleted under a name nothing is held for: %s", l)
		}
	}
}

// What the file would have named is left, and said to be: a volume, an image and a
// network it declares.
func TestDownByLabelSaysWhatItLeaves(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	fakeShim(t)
	dir := filepath.Join(t.TempDir(), "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  web:\n    image: alpine:3\n    volumes: [\"\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("CONTAINER_LS", `[{"status":{"state":"running"},"configuration":{"id":"web.demo.opossum","labels":{"opossum.project":"demo"}}}]`)
	for _, flags := range [][]string{{"--volumes"}, {"--rmi", "local"}, {"--volumes", "--rmi", "all"}} {
		_, stderr, err := runSplit(t, append([]string{"-p", "demo", "down"}, flags...)...)
		if err != nil {
			t.Fatalf("down %v: %v\n%s", flags, err, stderr)
		}
		for _, want := range []string{"Volumes, images and networks the file declares are named by it and are left", "--volumes and --rmi need the compose file"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("down %v: want %q said, got:\n%s", flags, want, stderr)
			}
		}
	}
}

// A file that reads is read, and `down` says nothing about a file it could not
// read: the fallback is for the file it cannot.
func TestDownReadsAFileThatReads(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	fakeShim(t)
	file := writeCompose(t, "name: demo\nservices:\n  web:\n    image: alpine:3\n")
	_, stderr, err := runSplit(t, "-f", file, "down")
	if err != nil {
		t.Fatalf("down: %v\n%s", err, stderr)
	}
	if strings.Contains(stderr, "could not be read") {
		t.Errorf("a file that reads was reported unreadable:\n%s", stderr)
	}
}
