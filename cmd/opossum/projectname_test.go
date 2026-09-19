package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// What a project is called decides what every command acts on, and docker
// compose reads it from more places than opossum did: `-p`, then the shell's
// COMPOSE_PROJECT_NAME, then the `.env`'s, then the file's `name:`, then the
// directory (measured on v5.5.1, #1122). opossum read the first, the fourth and
// the fifth, so a directory set up with COMPOSE_PROJECT_NAME got another
// project under opossum than under docker compose — without a word, and `down`
// then took down whatever had the directory's name.
//
// Each row is a row of that measurement; want is docker compose's answer.
func TestTheProjectNameIsReadWhereDockerComposeReadsIt(t *testing.T) {
	const services = "services:\n  web:\n    image: alpine:3\n"
	for _, tc := range []struct {
		name    string
		compose string
		dotenv  string
		// shell is COMPOSE_PROJECT_NAME in the environment; nil is unset, which
		// is not the same as set and empty.
		shell *string
		args  []string
		want  string
	}{
		{"A1 nothing says: the directory", services, "", nil, nil, "dirname"},
		{"A2 the file's name:", "name: topname\n" + services, "", nil, nil, "topname"},
		{"A3 the .env", services, "COMPOSE_PROJECT_NAME=envname\n", nil, nil, "envname"},
		{"A4 the .env over the file's name:", "name: topname\n" + services, "COMPOSE_PROJECT_NAME=envname\n", nil, nil, "envname"},
		{"A5 the shell over the .env", "name: topname\n" + services, "COMPOSE_PROJECT_NAME=envname\n", ptr("shellname"), nil, "shellname"},
		{"A6 -p over the shell", "name: topname\n" + services, "COMPOSE_PROJECT_NAME=envname\n", ptr("shellname"), []string{"-p", "flagname"}, "flagname"},
		{"A7 the shell over the file's name:", "name: topname\n" + services, "", ptr("shellname"), nil, "shellname"},
		{"A8 -p over the file's name:", "name: topname\n" + services, "", nil, []string{"-p", "flagname"}, "flagname"},
		// Set and empty is not unset: it takes the .env's value away, and the
		// name falls through to the file's.
		{"A9 an empty value in the shell hides the .env", "name: topname\n" + services, "COMPOSE_PROJECT_NAME=envname\n", ptr(""), nil, "topname"},
		{"an empty value in the shell, and no name: either", services, "COMPOSE_PROJECT_NAME=envname\n", ptr(""), nil, "dirname"},
		{"an empty value in the .env is no name", "name: topname\n" + services, "COMPOSE_PROJECT_NAME=\n", nil, nil, "topname"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "dirname")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, "compose.yaml"), tc.compose)
			if tc.dotenv != "" {
				write(t, filepath.Join(dir, ".env"), tc.dotenv)
			}
			t.Chdir(dir)
			setOrUnset(t, "COMPOSE_PROJECT_NAME", tc.shell)
			out, err := run(t, append(tc.args, "config")...)
			if err != nil {
				t.Fatalf("config: %v\n%s", err, out)
			}
			if got := configName(out); got != tc.want {
				t.Errorf("project name = %q, want %q (docker compose's)", got, tc.want)
			}
		})
	}
}

// `--env-file` takes the place of the `.env`, for the project's name as for
// everything else read from it (measured: D5).
func TestTheProjectNameIsReadFromTheEnvFileGiven(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dirname")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "compose.yaml"), "services:\n  web:\n    image: alpine:3\n")
	write(t, filepath.Join(dir, ".env"), "COMPOSE_PROJECT_NAME=envname\n")
	write(t, filepath.Join(dir, "other.env"), "COMPOSE_PROJECT_NAME=othername\n")
	t.Chdir(dir)
	setOrUnset(t, "COMPOSE_PROJECT_NAME", nil)
	out, err := run(t, "--env-file", "other.env", "config")
	if err != nil {
		t.Fatalf("config: %v\n%s", err, out)
	}
	if got := configName(out); got != "othername" {
		t.Errorf("project name = %q, want the one in the env file given", got)
	}
}

// An `--env-file` takes the `.env`'s place altogether: where it gives no name,
// the `.env` is not read for one behind its back (measured on docker compose:
// `.env` names the project, `--env-file other.env` does not → the directory).
func TestAnEnvFileWithNoNameDoesNotFallBackToTheDotEnv(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dirname")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "compose.yaml"), "services:\n  web:\n    image: alpine:3\n")
	write(t, filepath.Join(dir, ".env"), "COMPOSE_PROJECT_NAME=envname\n")
	write(t, filepath.Join(dir, "other.env"), "OTHER=1\n")
	t.Chdir(dir)
	setOrUnset(t, "COMPOSE_PROJECT_NAME", nil)
	setOrUnset(t, "COMPOSE_FILE", nil)
	out, err := run(t, "--env-file", "other.env", "config")
	if err != nil {
		t.Fatalf("config: %v\n%s", err, out)
	}
	if got := configName(out); got != "dirname" {
		t.Errorf("project name = %q, want the directory's: the env file given names none, and the .env is not read", got)
	}
}

func ptr(s string) *string { return &s }

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setOrUnset fixes a variable for the test either way: a test about what is
// read from the environment must not take its answer from the one it runs in.
func setOrUnset(t *testing.T, name string, value *string) {
	t.Helper()
	if value != nil {
		t.Setenv(name, *value)
		return
	}
	if old, ok := os.LookupEnv(name); ok {
		os.Unsetenv(name)
		t.Cleanup(func() { os.Setenv(name, old) })
	}
}

func configName(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(l, "name: "); ok {
			return strings.Trim(rest, `"`)
		}
	}
	return ""
}

// A directory that sets COMPOSE_PROJECT_NAME was, to the version before, the
// project its `name:` or its directory names; what that version made is still
// under that name, and under the new one no command reaches it. `up`, `down` and
// `ps` say so on stderr — and only there, only when something is left, and not
// when `-p` is what names the project.
func TestWhatIsLeftUnderTheFormerNameIsPointedAt(t *testing.T) {
	const left = `[{"status":{"state":"running"},"configuration":{"id":"web.dirname.opossum","labels":{"opossum.project":"dirname"}}}]`
	const services = "services:\n  web:\n    image: alpine:3\n    volumes: [\"data:/data\"]\nvolumes:\n  data: {}\n"
	for _, tc := range []struct {
		name   string
		dotenv string
		ls     string
		vols   string
		args   []string
		want   bool
	}{
		{"ps, with a container and a volume left", "COMPOSE_PROJECT_NAME=envname\n", left, "NAME\ndirname_data\n", []string{"ps"}, true},
		{"down", "COMPOSE_PROJECT_NAME=envname\n", left, "", []string{"down"}, true},
		{"up", "COMPOSE_PROJECT_NAME=envname\n", left, "", []string{"up"}, true},
		{"only a volume left", "COMPOSE_PROJECT_NAME=envname\n", "[]", "NAME\ndirname_data\n", []string{"ps"}, true},
		// The commonest way to arrive here: the machine was restarted, or the
		// project stopped, before the upgrade. Stopped is still there.
		{"a stopped container left", "COMPOSE_PROJECT_NAME=envname\n", strings.Replace(left, `"running"`, `"stopped"`, 1), "", []string{"ps"}, true},
		// `destroy` says it removes everything opossum made for the project,
		// which is where "and that is all of it" is most likely to be read.
		{"destroy --dry-run", "COMPOSE_PROJECT_NAME=envname\n", left, "NAME\ndirname_data\n", []string{"destroy", "--dry-run"}, true},
		{"nothing left", "COMPOSE_PROJECT_NAME=envname\n", "[]", "", []string{"ps"}, false},
		// The variable is not what names the project: nothing changed for it.
		{"no COMPOSE_PROJECT_NAME", "", left, "NAME\ndirname_data\n", []string{"ps"}, false},
		{"-p names the project", "COMPOSE_PROJECT_NAME=envname\n", left, "NAME\ndirname_data\n", []string{"-p", "other", "ps"}, false},
		// The variable names it what it was called anyway.
		{"COMPOSE_PROJECT_NAME is the directory's name", "COMPOSE_PROJECT_NAME=dirname\n", left, "NAME\ndirname_data\n", []string{"ps"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeShim(t)
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("CONTAINER_LS", tc.ls)
			t.Setenv("VOLUME_LS", tc.vols)
			dir := filepath.Join(t.TempDir(), "dirname")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, "compose.yaml"), services)
			if tc.dotenv != "" {
				write(t, filepath.Join(dir, ".env"), tc.dotenv)
			}
			t.Chdir(dir)
			setOrUnset(t, "COMPOSE_PROJECT_NAME", nil)
			stdout, stderr, err := runSplit(t, tc.args...)
			if err != nil {
				t.Fatalf("%v: %v\n%s", tc.args, err, stderr)
			}
			if got := strings.Contains(stderr, "opossum -p dirname down"); got != tc.want {
				t.Errorf("pointed at the former name: %v, want %v; stderr: %s", got, tc.want, stderr)
			}
			if strings.Contains(stdout, "COMPOSE_PROJECT_NAME") {
				t.Errorf("a note belongs on stderr, got it on stdout: %s", stdout)
			}
		})
	}
}

// Where the file has a `name:`, that is what the project used to be called —
// not the directory — so that is the name looked under and the `-p` named.
func TestTheFormerNameIsTheFilesNameWhereItHasOne(t *testing.T) {
	fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CONTAINER_LS", `[{"status":{"state":"running"},"configuration":{"id":"web.topname.opossum","labels":{"opossum.project":"topname"}}},`+
		`{"status":{"state":"running"},"configuration":{"id":"web.dirname.opossum","labels":{"opossum.project":"dirname"}}}]`)
	dir := filepath.Join(t.TempDir(), "dirname")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "compose.yaml"), "name: topname\nservices:\n  web:\n    image: alpine:3\n")
	write(t, filepath.Join(dir, ".env"), "COMPOSE_PROJECT_NAME=envname\n")
	t.Chdir(dir)
	setOrUnset(t, "COMPOSE_PROJECT_NAME", nil)
	_, stderr, err := runSplit(t, "ps")
	if err != nil {
		t.Fatalf("ps: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "opossum -p topname down") || strings.Contains(stderr, "-p dirname") {
		t.Errorf("the former name is the file's `name:`, not the directory; stderr: %s", stderr)
	}
}

// `down` stops the restart supervisor before it reads the compose file, so that
// a file that has gone — a rename, a branch switch — still leaves a way to stop
// it. That lookup has no file to ask, and has to come to the same name the `up`
// that started the supervisor came to: COMPOSE_PROJECT_NAME from the `.env`, or
// from the `--env-file` given, ahead of the directory. Read the old way it
// looks for the directory's supervisor, finds none, and leaves this one running
// (measured on the runtime).
func TestDownFindsTheSupervisorUnderTheEnvName(t *testing.T) {
	for _, tc := range []struct {
		name    string
		envFile string
		args    []string
	}{
		{"from the .env", ".env", nil},
		{"from the --env-file given", "other.env", []string{"--env-file", "other.env"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeShim(t)
			state := t.TempDir()
			t.Setenv("XDG_STATE_HOME", state)
			t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
			dir := filepath.Join(t.TempDir(), "dirname")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, "compose.yaml"), "services:\n  web:\n    image: web\n    restart: always\n")
			write(t, filepath.Join(dir, tc.envFile), "COMPOSE_PROJECT_NAME=envname\n")
			t.Chdir(dir)
			setOrUnset(t, "COMPOSE_PROJECT_NAME", nil)

			if _, err := run(t, append(append([]string{}, tc.args...), "up", "--no-build")...); err != nil {
				t.Fatalf("up: %v", err)
			}
			var pid int
			waitFor(t, "the supervisor, under the env's name", func() bool {
				pid = supervisorPID(t, state, "envname")
				return pid != 0
			})
			t.Cleanup(func() {
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Signal(syscall.SIGKILL)
				}
			})
			if other := supervisorPID(t, state, "dirname"); other != 0 {
				t.Fatalf("a supervisor under the directory's name as well (pid %d): the two lookups disagree", other)
			}
			if err := os.Remove(filepath.Join(dir, "compose.yaml")); err != nil {
				t.Fatal(err)
			}
			out, _ := run(t, append(append([]string{}, tc.args...), "down")...) // the load fails, as it should
			if !strings.Contains(out, "stopped the restart supervisor") {
				t.Errorf("down should find the supervisor under the env's name, got:\n%s", out)
			}
			waitFor(t, "the supervisor to exit", func() bool { return !processIsAlive(pid) })
		})
	}
}

// A name from COMPOSE_PROJECT_NAME is made into a project name the way a `-p`
// one is: lowercased, and anything else turned into `-`. docker compose refuses
// such a name outright (measured: `invalid project name`), so this is a known
// difference and written down as one — what matters here is that the name is
// the same whichever way it arrives, and that the former name is made the same
// way, or the two would never compare equal.
func TestANameFromTheEnvironmentIsMadeLikeAnyOther(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Dir_Name")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "compose.yaml"), "services:\n  web:\n    image: alpine:3\n")
	t.Chdir(dir)
	for _, tc := range []struct {
		name  string
		shell *string
		args  []string
		want  string
	}{
		{"from the shell", ptr("My_App"), nil, "my-app"},
		{"from -p, for comparison", nil, []string{"-p", "My_App"}, "my-app"},
		{"the directory, for comparison", nil, nil, "dir-name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setOrUnset(t, "COMPOSE_PROJECT_NAME", tc.shell)
			out, err := run(t, append(append([]string{}, tc.args...), "config")...)
			if err != nil {
				t.Fatalf("config: %v\n%s", err, out)
			}
			if got := configName(out); got != tc.want {
				t.Errorf("project name = %q, want %q", got, tc.want)
			}
		})
	}
}

// `destroy --force` refuses a `-p` that names another project than this
// directory's, because it would take this directory's files with it. Which
// project is this directory's now includes COMPOSE_PROJECT_NAME: naming that
// same project with `-p` is not naming another one, and naming the directory's
// old name is.
func TestDestroyKnowsThisDirectorysProjectByItsEnvName(t *testing.T) {
	for _, tc := range []struct {
		name    string
		p       string
		refused bool
	}{
		{"-p is the env's name", "envname", false},
		{"-p is what the directory used to be called", "dirname", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeShim(t)
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			dir := filepath.Join(t.TempDir(), "dirname")
			if err := os.MkdirAll(filepath.Join(dir, ".opossum"), 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, "compose.yaml"), "services:\n  web:\n    image: alpine:3\n")
			write(t, filepath.Join(dir, ".env"), "COMPOSE_PROJECT_NAME=envname\n")
			t.Chdir(dir)
			setOrUnset(t, "COMPOSE_PROJECT_NAME", nil)
			out, err := run(t, "-p", tc.p, "destroy", "--force")
			if refused := err != nil && strings.Contains(out+err.Error(), "refusing to destroy"); refused != tc.refused {
				t.Errorf("refused: %v, want %v (err %v)\n%s", refused, tc.refused, err, out)
			}
			if _, statErr := os.Stat(filepath.Join(dir, ".opossum")); (statErr == nil) != tc.refused {
				t.Errorf(".opossum still there: %v, want %v", statErr == nil, tc.refused)
			}
		})
	}
}
