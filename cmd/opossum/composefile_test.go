package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// docker compose takes the compose files from `-f`, then COMPOSE_FILE in the
// shell, then COMPOSE_FILE in the `.env` (or the `--env-file` given), and only
// then looks for one (measured on v5.5.1, #1122). opossum read `-f` and looked,
// so a directory set up with COMPOSE_FILE ran a different file under opossum
// than under docker compose — without a word.
//
// Each row is a row of that measurement; want is docker compose's answer.
func TestTheComposeFilesAreReadWhereDockerComposeReadsThem(t *testing.T) {
	svc := func(name string) string { return "services:\n  " + name + ":\n    image: alpine:3\n" }
	for _, tc := range []struct {
		name   string
		dotenv string
		// extra is another file written beside the rest: name and body.
		extra [2]string
		shell map[string]string
		args  []string
		want  string
		// wantErr, when set, is what the refusal has to say instead.
		wantErr []string
	}{
		{name: "B1 nothing says: the one that is found", want: "main"},
		{name: "B2 the shell", shell: map[string]string{"COMPOSE_FILE": "a.yaml"}, want: "a"},
		{name: "B3 two of them, by a colon", shell: map[string]string{"COMPOSE_FILE": "a.yaml:b.yaml"}, want: "a,b"},
		// A comma is not a separator: it is one path, and there is no such file.
		{name: "B4 a comma is part of the name", shell: map[string]string{"COMPOSE_FILE": "a.yaml,b.yaml"},
			wantErr: []string{"COMPOSE_FILE", "a.yaml,b.yaml"}},
		{name: "B5 unless it is made the separator", shell: map[string]string{"COMPOSE_FILE": "a.yaml,b.yaml", "COMPOSE_PATH_SEPARATOR": ","}, want: "a,b"},
		{name: "B6 -f over the shell", shell: map[string]string{"COMPOSE_FILE": "a.yaml"}, args: []string{"-f", "c.yaml"}, want: "c"},
		{name: "B7 the .env", dotenv: "COMPOSE_FILE=a.yaml\n", want: "a"},
		{name: "B8 the shell over the .env", dotenv: "COMPOSE_FILE=a.yaml\n", shell: map[string]string{"COMPOSE_FILE": "b.yaml"}, want: "b"},
		{name: "B9 -f over the .env", dotenv: "COMPOSE_FILE=a.yaml\n", args: []string{"-f", "c.yaml"}, want: "c"},
		{name: "B10 the separator from the .env too", dotenv: "COMPOSE_FILE=a.yaml,b.yaml\nCOMPOSE_PATH_SEPARATOR=,\n", want: "a,b"},
		{name: "the separator from the --env-file given", extra: [2]string{"other.env", "COMPOSE_FILE=a.yaml,b.yaml\nCOMPOSE_PATH_SEPARATOR=,\n"},
			args: []string{"--env-file", "other.env"}, want: "a,b"},
		{name: "the --env-file given, in the .env's place", dotenv: "COMPOSE_FILE=b.yaml\n", extra: [2]string{"other.env", "COMPOSE_FILE=a.yaml\n"},
			args: []string{"--env-file", "other.env"}, want: "a"},
		// Chosen by COMPOSE_FILE is chosen, as by `-f`: the override that is
		// merged into a file that was found is not merged into these.
		{name: "no override is merged into a file COMPOSE_FILE names", extra: [2]string{"compose.override.yaml", svc("ov")},
			shell: map[string]string{"COMPOSE_FILE": "a.yaml"}, want: "a"},
		{name: "the override is merged into the file that is found", extra: [2]string{"compose.override.yaml", svc("ov")}, want: "main,ov"},
		// …and the same for opossum's own overlay, which is merged into a file
		// that was found and into no file that was chosen, by either means.
		{name: "no opossum overlay is merged into a file COMPOSE_FILE names", extra: [2]string{"compose.opossum.yaml", svc("overlaid")},
			shell: map[string]string{"COMPOSE_FILE": "a.yaml"}, want: "a"},
		{name: "nor into a file -f names", extra: [2]string{"compose.opossum.yaml", svc("overlaid")}, args: []string{"-f", "a.yaml"}, want: "a"},
		{name: "the opossum overlay is merged into the file that is found", extra: [2]string{"compose.opossum.yaml", svc("overlaid")}, want: "main,overlaid"},
		// …and a COMPOSE_FILE that is refused has merged nothing either.
		{name: "no opossum overlay is announced over a COMPOSE_FILE that is refused", extra: [2]string{"compose.opossum.yaml", svc("overlaid")},
			shell: map[string]string{"COMPOSE_FILE": "nosuch.yaml"}, wantErr: []string{"nosuch.yaml"}},
		{name: "a file that is not there", shell: map[string]string{"COMPOSE_FILE": "nosuch.yaml"}, wantErr: []string{"COMPOSE_FILE", "nosuch.yaml"}},
		// An empty element is a path docker compose refuses (measured: `a.yaml::b.yaml`,
		// `a.yaml:`, `:a.yaml`, `:`). Dropping it would let the last of these fall
		// through to the file that is found — the very thing reading COMPOSE_FILE is
		// there to stop.
		{name: "an empty element in the middle", shell: map[string]string{"COMPOSE_FILE": "a.yaml::b.yaml"}, wantErr: []string{"COMPOSE_FILE", "a.yaml::b.yaml", "empty"}},
		{name: "an empty element at the end", shell: map[string]string{"COMPOSE_FILE": "a.yaml:"}, wantErr: []string{"COMPOSE_FILE", "empty"}},
		{name: "an empty element at the start", shell: map[string]string{"COMPOSE_FILE": ":a.yaml"}, wantErr: []string{"COMPOSE_FILE", "empty"}},
		{name: "nothing but the separator", shell: map[string]string{"COMPOSE_FILE": ":"}, wantErr: []string{"COMPOSE_FILE", "empty"}},
		{name: "a directory", shell: map[string]string{"COMPOSE_FILE": "adir"}, wantErr: []string{"COMPOSE_FILE", "adir", "is a directory"}},
		// Anything else that is not a regular file is refused before it is opened
		// (docker compose: "is not a regular file"). Opened, a fifo is a read
		// that never ends.
		{name: "a fifo", shell: map[string]string{"COMPOSE_FILE": "afifo"}, wantErr: []string{"COMPOSE_FILE", "afifo", "not a regular file"}},
		{name: "a device", shell: map[string]string{"COMPOSE_FILE": "/dev/null"}, wantErr: []string{"COMPOSE_FILE", "/dev/null", "not a regular file"}},
		{name: "a fifo after a file", shell: map[string]string{"COMPOSE_FILE": "a.yaml:afifo"}, wantErr: []string{"COMPOSE_FILE", "afifo", "not a regular file"}},
		// A link is what it points at, either way.
		{name: "a link to a directory", shell: map[string]string{"COMPOSE_FILE": "adirlink"}, wantErr: []string{"adirlink", "is a directory"}},
		{name: "a link to a file", shell: map[string]string{"COMPOSE_FILE": "alink"}, want: "a"},
		// The refusal says where the value came from, which with --env-file is
		// not the `.env`.
		{name: "a file that is not there, from the .env", dotenv: "COMPOSE_FILE=nosuch.yaml\n", wantErr: []string{"nosuch.yaml", "the shell or the working directory's `.env`"}},
		{name: "a file that is not there, from the --env-file", extra: [2]string{"other.env", "COMPOSE_FILE=nosuch.yaml\n"},
			args: []string{"--env-file", "other.env"}, wantErr: []string{"nosuch.yaml", "the shell or the --env-file given"}},
		// The separator the refusal quotes is the one in force.
		{name: "the refusal names the separator in force", shell: map[string]string{"COMPOSE_FILE": "a.yaml,,b.yaml", "COMPOSE_PATH_SEPARATOR": ","},
			wantErr: []string{"empty", `separated by ","`}},
		{name: "and the default one", shell: map[string]string{"COMPOSE_FILE": "a.yaml::b.yaml"}, wantErr: []string{"empty", `separated by ":"`}},
		// docker compose refuses a COMPOSE_FILE that is set and empty. Here it
		// reads as not set, which is what it did before it was read at all.
		{name: "set and empty in the shell reads as not set", dotenv: "COMPOSE_FILE=a.yaml\n", shell: map[string]string{"COMPOSE_FILE": ""}, want: "main"},
		{name: "set and empty in the .env reads as not set", dotenv: "COMPOSE_FILE=\n", want: "main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "proj")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, "compose.yaml"), svc("main"))
			for _, n := range []string{"a", "b", "c"} {
				write(t, filepath.Join(dir, n+".yaml"), svc(n))
			}
			if err := os.Mkdir(filepath.Join(dir, "adir"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("a.yaml", filepath.Join(dir, "alink")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("adir", filepath.Join(dir, "adirlink")); err != nil {
				t.Fatal(err)
			}
			// The fifo keeps getting a writer that says nothing and leaves, so that
			// a read of it — what this guards against — comes to an end and fails
			// the row instead of hanging the run.
			if err := syscall.Mkfifo(filepath.Join(dir, "afifo"), 0o644); err != nil {
				t.Fatal(err)
			}
			stop := make(chan struct{})
			t.Cleanup(func() { close(stop) })
			go func(path string) {
				for {
					select {
					case <-stop:
						return
					default:
					}
					if w, err := os.OpenFile(path, os.O_RDWR, 0); err == nil {
						time.Sleep(20 * time.Millisecond)
						w.Close()
					}
					time.Sleep(5 * time.Millisecond)
				}
			}(filepath.Join(dir, "afifo"))
			if tc.dotenv != "" {
				write(t, filepath.Join(dir, ".env"), tc.dotenv)
			}
			if tc.extra[0] != "" {
				write(t, filepath.Join(dir, tc.extra[0]), tc.extra[1])
			}
			t.Chdir(dir)
			for _, name := range []string{"COMPOSE_FILE", "COMPOSE_PATH_SEPARATOR", "COMPOSE_PROJECT_NAME"} {
				var v *string
				if s, ok := tc.shell[name]; ok {
					v = &s
				}
				setOrUnset(t, name, v)
			}
			out, stderr, err := runSplit(t, append(append([]string{}, tc.args...), "config", "--services")...)
			// The overlay is announced exactly when it is merged: said of a file
			// that was chosen, it would be describing a merge that did not happen.
			if tc.extra[0] == "compose.opossum.yaml" {
				if announced := strings.Contains(stderr, "merging compose.opossum.yaml"); announced != strings.Contains(tc.want, "overlaid") {
					t.Errorf("overlay announced: %v, merged: %v; stderr: %s", announced, strings.Contains(tc.want, "overlaid"), stderr)
				}
			}
			out += stderr
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("want it refused, got services:\n%s", out)
				}
				for _, want := range tc.wantErr {
					if !strings.Contains(err.Error()+out, want) {
						t.Errorf("the refusal should name %q, got: %v\n%s", want, err, out)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("config --services: %v\n%s", err, out)
			}
			got := strings.Fields(strings.TrimSuffix(out, stderr))
			sort.Strings(got)
			if strings.Join(got, ",") != tc.want {
				t.Errorf("services = %q, want %q (docker compose's)", strings.Join(got, ","), tc.want)
			}
		})
	}
}

// A file COMPOSE_FILE names in another directory is read there — its relative
// paths are that directory's, as with `-f` — but the `.env` its variables come
// from is the working directory's first, which is where COMPOSE_FILE itself was
// read from, and the file's directory's for what that one does not set; with
// `-f` it is the file's directory's alone (measured). The project's name
// comes from the working directory's `.env`, then the file's directory's, then
// that directory's name (measured: B16, B11, B17).
func TestAFileNamedByComposeFileInAnotherDirectory(t *testing.T) {
	const sub = "services:\n  subsvc:\n    image: alpine:3\n    volumes: [\"./d:/d\"]\n    environment: {V: \"${VAR:-unset}\"}\n"
	for _, tc := range []struct {
		name     string
		cwdEnv   string
		subEnv   string
		shell    string
		args     []string
		wantV    string
		wantName string
		// envFile is the body of cf.env, written when there is one.
		envFile string
	}{
		{"B17 COMPOSE_FILE: the working directory's .env, the file's directory's name",
			"VAR=from-cwd-env\n", "VAR=from-sub-env\n", "sub/x.yaml", nil, "from-cwd-env", "sub", ""},
		{"B11 the name from the file's directory's .env",
			"VAR=from-cwd-env\n", "VAR=from-sub-env\nCOMPOSE_PROJECT_NAME=subenvname\n", "sub/x.yaml", nil, "from-cwd-env", "subenvname", ""},
		{"B16 the working directory's .env names it first",
			"VAR=from-cwd-env\nCOMPOSE_PROJECT_NAME=cwdenvname\n", "VAR=from-sub-env\nCOMPOSE_PROJECT_NAME=subenvname\n", "sub/x.yaml", nil, "from-cwd-env", "cwdenvname", ""},
		{"B14 COMPOSE_FILE from the working directory's .env",
			"VAR=from-cwd-env\nCOMPOSE_FILE=sub/x.yaml\n", "VAR=from-sub-env\nCOMPOSE_PROJECT_NAME=subenvname\n", "", nil, "from-cwd-env", "subenvname", ""},
		// docker compose reads the file's directory's `.env` under the working
		// directory's, for what that one does not set (measured; the whole of it
		// is TestTheEnvLayersMatrixAgreesWithDockerCompose). This row used to say
		// "unset": that was opossum's answer, not docker compose's (#1140).
		{"no .env in the working directory: the file's directory's is read",
			"", "VAR=from-sub-env\n", "sub/x.yaml", nil, "from-sub-env", "sub", ""},
		// An --env-file is the one env file there is: where it names no project,
		// neither `.env` is asked — not the working directory's, and not the
		// file's directory's, though it has a name to give.
		{name: "--env-file with no name: the file's directory's .env is not asked",
			cwdEnv: "VAR=from-cwd-env\n", subEnv: "VAR=from-sub-env\nCOMPOSE_PROJECT_NAME=subenvname\n",
			args: []string{"--env-file", "cf.env"}, wantV: "from-envfile", wantName: "sub",
			envFile: "VAR=from-envfile\nCOMPOSE_FILE=sub/x.yaml\n"},
		// The control: with `-f` everything is the file's directory's.
		{"B12 -f: the file's directory's .env for both",
			"VAR=from-cwd-env\nCOMPOSE_PROJECT_NAME=cwdenvname\n", "VAR=from-sub-env\nCOMPOSE_PROJECT_NAME=subenvname\n", "", []string{"-f", "sub/x.yaml"}, "from-sub-env", "subenvname", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeShim(t)
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			dir := filepath.Join(t.TempDir(), "proj")
			if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, "compose.yaml"), "services:\n  main:\n    image: alpine:3\n")
			write(t, filepath.Join(dir, "sub", "x.yaml"), sub)
			if tc.cwdEnv != "" {
				write(t, filepath.Join(dir, ".env"), tc.cwdEnv)
			}
			write(t, filepath.Join(dir, "sub", ".env"), tc.subEnv)
			if tc.envFile != "" {
				write(t, filepath.Join(dir, "cf.env"), tc.envFile)
			}
			t.Chdir(dir)
			setOrUnset(t, "COMPOSE_PROJECT_NAME", nil)
			setOrUnset(t, "COMPOSE_PATH_SEPARATOR", nil)
			setOrUnset(t, "VAR", nil)
			var shell *string
			if tc.shell != "" {
				shell = &tc.shell
			}
			setOrUnset(t, "COMPOSE_FILE", shell)
			out, err := run(t, append(append([]string{}, tc.args...), "config")...)
			if err != nil {
				t.Fatalf("config: %v\n%s", err, out)
			}
			if got := configName(out); got != tc.wantName {
				t.Errorf("project name = %q, want %q", got, tc.wantName)
			}
			if !strings.Contains(out, "V="+tc.wantV+"\n") {
				t.Errorf("want V expanded to %q, got:\n%s", tc.wantV, out)
			}
			// Either way the file's relative paths are its own directory's: what
			// `up` would hand the runtime for the bind.
			plan, err := run(t, append(append([]string{}, tc.args...), "up", "--dry-run")...)
			if err != nil {
				t.Fatalf("up --dry-run: %v\n%s", err, plan)
			}
			real, _ := filepath.EvalSymlinks(dir)
			if !strings.Contains(plan, filepath.Join(real, "sub", "d")+":/d") && !strings.Contains(plan, filepath.Join(dir, "sub", "d")+":/d") {
				t.Errorf("the bind's source should resolve under the file's directory, got:\n%s", plan)
			}
		})
	}
}

// The restart supervisor is a child with another working directory, and it
// reads the project again for itself. What it is handed has to bring it to the
// project this `up` started: the files by `-f`, absolute — a COMPOSE_FILE it
// inherits would be read from where it stands — and the directory whose `.env`
// they were read with first, since files handed over by `-f` would otherwise be
// read with their own directory's alone. It is handed over as a directory and
// not as an `--env-file`: with an env file given, the second `.env` — the
// files' own directory's — is not read, and the child would be reading another
// project than its parent did.
func TestTheSupervisorReadsTheFilesComposeFileNamedTheSameWay(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cwdEnv bool
		// byFlag names the file with `-f` instead: the control, where the file's
		// own directory's `.env` is the one read and nothing is handed over.
		byFlag  bool
		envFile bool
		// wantEnvDir is whether the child is told the directory; wantEnvFile is
		// the env file it is handed, "" for none.
		wantEnvDir  bool
		wantEnvFile func(dir string) string
	}{
		{"with a .env in the working directory", true, false, false, true, nil},
		{"with none", false, false, false, true, nil},
		{"named by -f: nothing is handed over", true, true, false, false, nil},
		// An --env-file is the one env file there is: it is handed over, and
		// no directory with it.
		{"with an --env-file: that one, and no directory", true, false, true, false, func(dir string) string { return filepath.Join(dir, "other.env") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeShim(t)
			state := t.TempDir()
			t.Setenv("XDG_STATE_HOME", state)
			t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
			dir := filepath.Join(t.TempDir(), "proj")
			if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			dir, _ = filepath.EvalSymlinks(dir)
			write(t, filepath.Join(dir, "compose.yaml"), "services:\n  main:\n    image: web\n")
			write(t, filepath.Join(dir, "sub", "x.yaml"), "services:\n  web:\n    image: web\n    restart: always\n")
			write(t, filepath.Join(dir, "sub", ".env"), "VAR=from-sub-env\n")
			if tc.cwdEnv {
				write(t, filepath.Join(dir, ".env"), "VAR=from-cwd-env\n")
			}
			t.Chdir(dir)
			setOrUnset(t, "COMPOSE_PROJECT_NAME", nil)
			setOrUnset(t, "COMPOSE_PATH_SEPARATOR", nil)
			args := []string{"up", "--no-build"}
			var choice []string
			if tc.byFlag {
				setOrUnset(t, "COMPOSE_FILE", nil)
				choice = []string{"-f", "sub/x.yaml"}
			} else {
				t.Setenv("COMPOSE_FILE", "sub/x.yaml")
			}
			if tc.envFile {
				write(t, filepath.Join(dir, "other.env"), "VAR=from-other-env\n")
				choice = append(choice, "--env-file", "other.env")
			}
			args = append(append([]string{}, choice...), args...)
			if out, err := run(t, args...); err != nil {
				t.Fatalf("up: %v\n%s", err, out)
			}
			var pid int
			waitFor(t, "the supervisor", func() bool {
				pid = supervisorPID(t, state, "sub")
				return pid != 0
			})
			t.Cleanup(func() {
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Signal(syscall.SIGKILL)
				}
			})
			argv, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
			if err != nil {
				t.Fatalf("reading the supervisor's command line: %v", err)
			}
			line := " " + strings.TrimSpace(string(argv)) + " "
			if want := " -f " + filepath.Join(dir, "sub", "x.yaml") + " "; !strings.Contains(line, want) {
				t.Errorf("the child should be handed the file by -f, absolute; want %q in: %s", want, line)
			}
			if want := " --env-dir " + dir + " "; tc.wantEnvDir && (!strings.Contains(line, want) || strings.Count(line, " --env-dir ") != 1) {
				t.Errorf("the child should be told the directory whose .env was read first; want %q, once, in: %s", want, line)
			} else if !tc.wantEnvDir && strings.Contains(line, " --env-dir") {
				t.Errorf("no directory is handed over where the files' own is the one read; got: %s", line)
			}
			if tc.wantEnvFile == nil {
				if strings.Contains(line, " --env-file ") {
					t.Errorf("no env file was given, so none is handed over — it would keep the second .env from being read; got: %s", line)
				}
			} else if want := " --env-file " + tc.wantEnvFile(dir) + " "; !strings.Contains(line, want) || strings.Count(line, " --env-file ") != 1 {
				t.Errorf("the child should be handed the env file the files were read with, and no other; want %q in: %s", want, line)
			}
			downArgs := append(append([]string{}, choice...), "down")
			if out, err := run(t, downArgs...); err != nil {
				t.Fatalf("down: %v\n%s", err, out)
			}
			waitFor(t, "the supervisor to exit", func() bool { return !processIsAlive(pid) })
		})
	}
}

// What the child does with the `.env` it is handed, asked by what it does rather
// than by what it was handed. The supervisor never recreates a container, so the
// values inside one say nothing about which `.env` the child read; what the
// child decides from the project is which services to watch, and under what
// `restart:` policy. Here the policy is a variable of the `.env` files, and a
// child that read them another way than its parent finds nothing to watch and
// ends at once (measured on the runtime). Two rows, for the two files: the
// working directory's says `always` over the file's directory's `no`; and the
// working directory's says nothing of it, so the file's directory's `always`
// is what counts — which a child handed the first `.env` as an `--env-file`
// would not read.
func TestTheSupervisorDecidesThePolicyFromTheSameEnv(t *testing.T) {
	for _, tc := range []struct{ name, cwdEnv, subEnv string }{
		{"the working directory's .env over the file's directory's", "POLICY=always\n", "POLICY=no\n"},
		{"the file's directory's .env, where the working directory's does not say", "OTHER=1\n", "POLICY=always\n"},
		{"the file's directory's .env, where the working directory has none", "", "POLICY=always\n"},
	} {
		t.Run(tc.name, func(t *testing.T) { theSupervisorDecidesThePolicy(t, tc.cwdEnv, tc.subEnv) })
	}
}

func theSupervisorDecidesThePolicy(t *testing.T, cwdEnv, subEnv string) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	dir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "compose.yaml"), "services:\n  main:\n    image: web\n")
	write(t, filepath.Join(dir, "sub", "x.yaml"), "services:\n  web:\n    image: web\n    restart: ${POLICY:-no}\n")
	if cwdEnv != "" {
		write(t, filepath.Join(dir, ".env"), cwdEnv)
	}
	write(t, filepath.Join(dir, "sub", ".env"), subEnv)
	t.Chdir(dir)
	for _, name := range []string{"COMPOSE_PROJECT_NAME", "COMPOSE_PATH_SEPARATOR", "POLICY"} {
		setOrUnset(t, name, nil)
	}
	t.Setenv("COMPOSE_FILE", "sub/x.yaml")
	if out, err := run(t, "up", "--no-build"); err != nil {
		t.Fatalf("up: %v\n%s", err, out)
	}
	// A child that resolved `restart: no` has nothing to watch and is gone —
	// often before it can be seen at all, so not seeing one is that same answer
	// and not a wait that ran out. One that resolved `always` is there, and is
	// still there a while later.
	const gone = "no supervisor is left running: it did not read `restart:` as the `up` that started it did"
	var pid int
	for deadline := time.Now().Add(5 * time.Second); pid == 0; time.Sleep(50 * time.Millisecond) {
		if pid = supervisorPID(t, state, "sub"); pid == 0 && time.Now().After(deadline) {
			t.Fatal(gone)
		}
	}
	t.Cleanup(func() {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Signal(syscall.SIGKILL)
		}
	})
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !processIsAlive(pid) {
			t.Fatal(gone)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if out, err := run(t, "down"); err != nil {
		t.Fatalf("down: %v\n%s", err, out)
	}
	waitFor(t, "the supervisor to exit", func() bool { return !processIsAlive(pid) })
}

// `--from-docker-compose` writes its findings into an overlay only beside a
// file it found; over files that were chosen it says what the overlay would
// hold and how to use it. For files COMPOSE_FILE chose, that way has to keep
// them chosen by COMPOSE_FILE: named with `-f` instead they would be read with
// their own directory's `.env`, which is another project than the one the
// reader is looking at.
//
// The advice is a command to type, so it is held to that: each row does what
// it says — writes the text where it says, hands the command to a shell as
// printed, and runs what the shell made of it. It has to load, and the overlay
// has to be merged, and the run it starts has to read the project this one
// read — the command carries the rest of the run's root flags (an `--env-file`,
// a `-p`) and the services it named.
func TestTheMigrationsAdviceCanBeFollowed(t *testing.T) {
	const pg = "services:\n  db:\n    image: postgres:16\n    environment: {V: \"${VAR:-unset}\"}\n    volumes: [\"pgdata:/var/lib/postgresql/data\"]\nvolumes:\n  pgdata: {}\n"
	const other = "services:\n  other:\n    image: alpine:3\n"
	for _, tc := range []struct {
		name string
		// files are written under the working directory: path and body.
		files map[string]string
		args  []string
		shell map[string]string
		// wantCommand is a part of the command line as printed, where the
		// spelling is the point; wantOverlay is where the overlay has to go,
		// which is beside the first file.
		wantCommand, wantOverlay string
		// byEnv is whether COMPOSE_FILE has to go on choosing the files.
		byEnv bool
		// named are the services the run names.
		named []string
	}{
		{name: "COMPOSE_FILE, the separator it has by default",
			files: map[string]string{"a.yaml": pg}, shell: map[string]string{"COMPOSE_FILE": "a.yaml"},
			wantCommand: "COMPOSE_FILE=a.yaml:compose.opossum.yaml ", wantOverlay: "compose.opossum.yaml", byEnv: true},
		// The overlay is added with the separator the value is read with, or
		// the advice names one file called `a.yaml:compose.opossum.yaml`.
		{name: "COMPOSE_FILE, the separator it was given",
			files: map[string]string{"a.yaml": pg}, shell: map[string]string{"COMPOSE_FILE": "a.yaml", "COMPOSE_PATH_SEPARATOR": ","},
			wantCommand: "COMPOSE_FILE=a.yaml,compose.opossum.yaml ", wantOverlay: "compose.opossum.yaml", byEnv: true},
		// The file is somewhere else: the overlay is written there, and the
		// command has to name it there.
		{name: "COMPOSE_FILE, a file in another directory",
			files: map[string]string{"sub/x.yaml": pg}, shell: map[string]string{"COMPOSE_FILE": "sub/x.yaml"},
			wantOverlay: "sub/compose.opossum.yaml", byEnv: true},
		{name: "-f, a file in another directory",
			files: map[string]string{"sub/x.yaml": pg}, args: []string{"-f", "sub/x.yaml"},
			wantOverlay: "sub/compose.opossum.yaml"},
		// What a shell would take apart is quoted.
		{name: "COMPOSE_FILE, a space in the path",
			files: map[string]string{"sub dir/x.yaml": pg}, shell: map[string]string{"COMPOSE_FILE": "sub dir/x.yaml"},
			wantOverlay: "sub dir/compose.opossum.yaml", byEnv: true},
		{name: "-f, a space in the path",
			files: map[string]string{"sub dir/x.yaml": pg}, args: []string{"-f", "sub dir/x.yaml"},
			wantOverlay: "sub dir/compose.opossum.yaml"},
		{name: "-f, a quote in the path",
			files: map[string]string{"it's/x.yaml": pg}, args: []string{"-f", "it's/x.yaml"},
			wantOverlay: "it's/compose.opossum.yaml"},
		{name: "COMPOSE_FILE, a separator the shell reads as its own",
			files: map[string]string{"a.yaml": pg, "b.yaml": other}, shell: map[string]string{"COMPOSE_FILE": "a.yaml;b.yaml", "COMPOSE_PATH_SEPARATOR": ";"},
			wantOverlay: "compose.opossum.yaml", byEnv: true},
		// Several files: beside the first, which is the project's directory.
		{name: "COMPOSE_FILE, several files, the first somewhere else",
			files: map[string]string{"sub/x.yaml": pg, "b.yaml": other}, shell: map[string]string{"COMPOSE_FILE": "sub/x.yaml:b.yaml"},
			wantOverlay: "sub/compose.opossum.yaml", byEnv: true},
		{name: "-f, several files, the first somewhere else",
			files: map[string]string{"sub/x.yaml": pg, "b.yaml": other}, args: []string{"-f", "sub/x.yaml", "-f", "b.yaml"},
			wantCommand: " -f sub/x.yaml -f b.yaml -f sub/compose.opossum.yaml", wantOverlay: "sub/compose.opossum.yaml"},
		// COMPOSE_FILE and its separator from the working directory's `.env`:
		// the command puts COMPOSE_FILE in the shell, over the `.env`'s, and the
		// separator stays the `.env`'s.
		{name: "COMPOSE_FILE and its separator from the .env",
			files:       map[string]string{"sub/x.yaml": pg, "b.yaml": other, ".env": "COMPOSE_FILE=sub/x.yaml;b.yaml\nCOMPOSE_PATH_SEPARATOR=;\n"},
			wantCommand: "COMPOSE_FILE='sub/x.yaml;b.yaml;sub/compose.opossum.yaml' opossum up --from-docker-compose", wantOverlay: "sub/compose.opossum.yaml", byEnv: true},
		// The rest of the run's root flags come along: the project's name and
		// the DNS domain are in the names of what the run made, and an
		// --env-file is where its variables (a profile among them) came from.
		{name: "-f, with the project's name and the DNS domain",
			files: map[string]string{"sub/x.yaml": pg}, args: []string{"-p", "my proj", "--dns-domain", "foo", "-f", "sub/x.yaml"},
			wantCommand: " -f sub/x.yaml -p 'my proj' --dns-domain foo -f sub/compose.opossum.yaml", wantOverlay: "sub/compose.opossum.yaml"},
		// The --env-file is where the variables came from: without it the
		// rewrite reads the `.env`, and V, which the two set differently,
		// comes out the other way.
		{name: "COMPOSE_FILE from an --env-file, with the project's name",
			files: map[string]string{"sub/x.yaml": pg, ".env": "VAR=from-dot-env\n", "other.env": "COMPOSE_FILE=sub/x.yaml\nVAR=from-other-env\n"}, args: []string{"--env-file", "other.env", "-p", "myproj"},
			wantCommand: "COMPOSE_FILE=sub/x.yaml:sub/compose.opossum.yaml opossum up --from-docker-compose --env-file other.env -p myproj", wantOverlay: "sub/compose.opossum.yaml", byEnv: true},
		// The services the run named are named again, by either means.
		{name: "-f, with the service it named",
			files: map[string]string{"sub/x.yaml": pg}, args: []string{"-f", "sub/x.yaml"}, named: []string{"db"},
			wantCommand: " -f sub/x.yaml -f sub/compose.opossum.yaml db", wantOverlay: "sub/compose.opossum.yaml"},
		{name: "COMPOSE_FILE, with the service it named",
			files: map[string]string{"sub/x.yaml": pg}, shell: map[string]string{"COMPOSE_FILE": "sub/x.yaml"}, named: []string{"db"},
			wantCommand: "COMPOSE_FILE=sub/x.yaml:sub/compose.opossum.yaml opossum up --from-docker-compose db", wantOverlay: "sub/compose.opossum.yaml", byEnv: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeShim(t)
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			root := t.TempDir()
			dir := filepath.Join(root, "proj")
			for path, body := range tc.files {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755); err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(dir, path), body)
			}
			t.Chdir(dir)
			for _, name := range []string{"COMPOSE_PROJECT_NAME", "COMPOSE_FILE", "COMPOSE_PATH_SEPARATOR"} {
				var v *string
				if s, ok := tc.shell[name]; ok {
					v = &s
				}
				setOrUnset(t, name, v)
			}
			_, stderr, err := runSplit(t, append(append(append([]string{}, tc.args...), "up", "--from-docker-compose", "--no-build", "--dry-run"), tc.named...)...)
			if err != nil {
				t.Fatalf("up --from-docker-compose --dry-run: %v\n%s", err, stderr)
			}
			if !strings.Contains(stderr, "and name both:\n") {
				t.Fatalf("this fixture gave the migration nothing to say, so the advice was not printed:\n%s", stderr)
			}
			if how := "COMPOSE_FILE names the compose file"; strings.Contains(stderr, how) != tc.byEnv {
				t.Errorf("the advice should say what chose the files (COMPOSE_FILE: %v); got:\n%s", tc.byEnv, stderr)
			}
			if !strings.Contains(stderr, "write it as "+tc.wantOverlay+" ") {
				t.Errorf("the advice should say to write the overlay as %q; got:\n%s", tc.wantOverlay, stderr)
			}
			// The command is the line after "name both:", two spaces in.
			_, rest, _ := strings.Cut(stderr, "and name both:\n")
			command, _, _ := strings.Cut(rest, "\n")
			command = strings.TrimPrefix(command, "  ")
			if tc.wantCommand != "" && !strings.Contains(command, tc.wantCommand) {
				t.Errorf("the command should hold %q, got: %s", tc.wantCommand, command)
			}
			// The overlay's text is the block printed two spaces in, after the
			// first blank line.
			_, block, ok := strings.Cut(rest, "\n\n")
			if !ok {
				t.Fatalf("no overlay text in:\n%s", stderr)
			}
			var overlay strings.Builder
			for _, line := range strings.Split(block, "\n") {
				if line != "" && !strings.HasPrefix(line, "  ") {
					break
				}
				overlay.WriteString(strings.TrimPrefix(line, "  ") + "\n")
			}
			write(t, filepath.Join(dir, tc.wantOverlay), overlay.String())

			composeFile, argv := shellReads(t, root, command)
			if tc.byEnv == (composeFile == "<unset>") {
				t.Errorf("COMPOSE_FILE chose the files: %v, but the command runs with COMPOSE_FILE=%s", tc.byEnv, composeFile)
			}
			if composeFile != "<unset>" {
				t.Setenv("COMPOSE_FILE", composeFile)
			}
			// Files COMPOSE_FILE chose are not named with -f as well: -f wins,
			// and reads them with their own directory's `.env`.
			for _, a := range argv {
				if tc.byEnv && a == "-f" {
					t.Errorf("the command names files with -f beside COMPOSE_FILE, which -f overrides: %s", command)
				}
			}
			out, stderr2, err := runSplit(t, append(argv, "--no-build", "--dry-run")...)
			if err != nil {
				t.Fatalf("following the advice (%s) failed: %v\n%s", command, err, stderr2)
			}
			if !strings.Contains(out+stderr2, "PGDATA=/var/lib/postgresql/data/pgdata") {
				t.Errorf("following the advice (%s) should merge the overlay; got:\n%s%s", command, out, stderr2)
			}
			if strings.Contains(stderr2, "and name both:") {
				t.Errorf("following the advice should leave nothing to advise; got:\n%s", stderr2)
			}
			// …and the run it starts is the same project as this one: the name,
			// and what its variables come to. The services named come last and
			// are left off `config`.
			var flags []string
			for i := 0; i < len(argv)-len(tc.named); i++ {
				if argv[i] == "up" || argv[i] == "--from-docker-compose" {
					continue
				}
				flags = append(flags, argv[i])
			}
			for i, n := range tc.named {
				if argv[len(argv)-len(tc.named)+i] != n {
					t.Errorf("the command should end with the services named (%v), got argv %v", tc.named, argv)
				}
			}
			after, err := run(t, append(flags, "config")...)
			if err != nil {
				t.Fatalf("config with the command's flags (%v): %v", flags, err)
			}
			setOrUnset(t, "COMPOSE_FILE", nil)
			if v, ok := tc.shell["COMPOSE_FILE"]; ok {
				t.Setenv("COMPOSE_FILE", v)
			}
			before, err := run(t, append(append([]string{}, tc.args...), "config")...)
			if err != nil {
				t.Fatal(err)
			}
			if configName(after) != configName(before) {
				t.Errorf("following the advice reads project %q where this run read %q", configName(after), configName(before))
			}
			vLine := func(out string) string {
				_, rest, _ := strings.Cut(out, "V=")
				line, _, _ := strings.Cut(rest, "\n")
				return line
			}
			if vLine(after) != vLine(before) {
				t.Errorf("following the advice expands V to %q where this run had %q", vLine(after), vLine(before))
			}
			// …and the same services: a file dropped from the command would
			// leave the name and V as they were.
			services := func(args []string) string {
				out, err := run(t, append(append([]string{}, args...), "config", "--services")...)
				if err != nil {
					t.Fatal(err)
				}
				s := strings.Fields(out)
				sort.Strings(s)
				return strings.Join(s, ",")
			}
			if got, want := services(flags), services(tc.args); got != want {
				t.Errorf("following the advice reads services %q where this run read %q", got, want)
			}
		})
	}
}

// shellWord is what makes a printed command safe to paste: a word a shell reads
// back as it was, whatever is in it.
func TestShellWord(t *testing.T) {
	for in, want := range map[string]string{
		"a.yaml:sub/compose.opossum.yaml": "a.yaml:sub/compose.opossum.yaml",
		"":                                "''",
		"sub dir/x.yaml":                  "'sub dir/x.yaml'",
		"a.yaml;b.yaml":                   "'a.yaml;b.yaml'",
		"$HOME/x.yaml":                    "'$HOME/x.yaml'",
		"it's/x.yaml":                     `'it'\''s/x.yaml'`,
		"~/x.yaml":                        "'~/x.yaml'",
		"*":                               "'*'",
		"a*b":                             "'a*b'",
		"a?b":                             "'a?b'",
		"[ab]":                            "'[ab]'",
	} {
		if got := shellWord(in); got != want {
			t.Errorf("shellWord(%q) = %s, want %s", in, got, want)
		}
		out, err := exec.Command("/bin/sh", "-c", "printf %s "+shellWord(in)).Output()
		if err != nil || string(out) != in {
			t.Errorf("a shell reads %s back as %q (%v), want %q", shellWord(in), out, err, in)
		}
	}
}

// shellReads hands a command line opossum printed to a shell, as printed, and
// returns what the shell would have run as `opossum`: its arguments, and the
// COMPOSE_FILE it would have run with ("<unset>" when none). A stand-in of that
// name, first on the PATH, writes them down; whatever else the line may do, it
// really does, in the working directory.
func shellReads(t *testing.T, root, command string) (composeFile string, argv []string) {
	t.Helper()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(root, "record")
	write(t, filepath.Join(bin, "opossum"), "#!/bin/sh\n{ printf '%s\\n' \"${COMPOSE_FILE-<unset>}\"; for a in \"$@\"; do printf '%s\\n' \"$a\"; done; } > \""+record+"\"\n")
	if err := os.Chmod(filepath.Join(bin, "opossum"), 0o755); err != nil {
		t.Fatal(err)
	}
	sh := exec.Command("/bin/sh", "-c", command)
	sh.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
	if out, err := sh.CombinedOutput(); err != nil {
		t.Fatalf("a shell could not run the command as printed (%s): %v\n%s", command, err, out)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the shell ran no `opossum` from %q: %v", command, err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	return lines[0], lines[1:]
}
