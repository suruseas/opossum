package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Which profiles a run has active: `--profile`, then COMPOSE_PROFILES in the
// shell, then COMPOSE_PROFILES in the `.env` (or the `--env-file` given) — the
// first of them that says anything, and not the sum of them (measured on docker
// compose v5.5.1, #1122). testdata/composeprofiles-matrix/docker.json is docker
// compose's answer to every cell (gen.py beside it builds the cells and asks);
// this builds the same cells and asks opossum.
func TestTheProfilesMatrixAgreesWithDockerCompose(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "composeprofiles-matrix", "docker.json"))
	if err != nil {
		t.Fatal(err)
	}
	type answer struct {
		RC       int      `json:"rc"`
		Services []string `json:"services"`
	}
	var golden struct {
		Cells []struct {
			Flag   bool   `json:"flag"`
			Shell  string `json:"shell"`
			Env    string `json:"env"`
			Source string `json:"source"`
			answer
		} `json:"cells"`
		Extras []struct {
			Kind   string   `json:"kind"`
			How    string   `json:"how"`
			Place  string   `json:"place"`
			Value  string   `json:"value"`
			Flags  []string `json:"flags"`
			Cwd    *string  `json:"cwd"`
			Sub    *string  `json:"sub"`
			One    *string  `json:"one"`
			Two    *string  `json:"two"`
			CwdEnv *string  `json:"cwdenv"`
			SubEnv *string  `json:"subenv"`
			answer
		} `json:"extras"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	if len(golden.Cells) != 36 || len(golden.Extras) != 47 {
		t.Fatalf("the matrix is 2 x 3 x 3 x 2 cells and 2 + 6 + 5 + 2 + 4 + 3 x 2 + 11 x 2 extras, got %d and %d", len(golden.Cells), len(golden.Extras))
	}
	compose := "services:\n  base:\n    image: alpine:3\n"
	for _, p := range []string{"a", "b", "c", "d"} {
		compose += "  s" + p + ":\n    image: alpine:3\n    profiles: [" + p + "]\n"
	}
	line := func(state, value string) string {
		switch state {
		case "value":
			return "COMPOSE_PROFILES=" + value + "\n"
		case "empty":
			return "COMPOSE_PROFILES=\n"
		}
		return ""
	}
	// knownDifferences are the cells where opossum is known not to give docker
	// compose's answer, with the answer it gives instead. They are held to that
	// answer, so a cell that comes to agree fails here and is taken off the list
	// — and the sentence in docs/compatibility.md that owns up to it with it.
	//
	// The one there is: a `--profile` value's surrounding white space, dropped
	// here and kept there.
	knownDifferences := map[string]string{
		`flagvalue//""[" a "]/-,-,-,-/-,-`: "base,sa",
	}
	seen := map[string]bool{}
	// ask runs `config --services` in dir and holds it to docker compose's answer.
	ask := func(t *testing.T, cell, dir string, args []string, shell, composeFile *string, want answer) {
		t.Helper()
		if want.RC != 0 {
			t.Fatalf("docker compose refused this cell (rc %d); the test has no refusal to compare", want.RC)
		}
		t.Chdir(dir)
		for _, n := range []string{"COMPOSE_PROJECT_NAME", "COMPOSE_PATH_SEPARATOR"} {
			setOrUnset(t, n, nil)
		}
		setOrUnset(t, "COMPOSE_FILE", composeFile)
		setOrUnset(t, "COMPOSE_PROFILES", shell)
		out, stderr, err := runSplit(t, append(append([]string{}, args...), "config", "--services")...)
		if err != nil {
			t.Fatalf("config --services: %v\n%s", err, stderr)
		}
		got := strings.Fields(out)
		sort.Strings(got)
		if known, ok := knownDifferences[cell]; ok {
			seen[cell] = true
			if known == strings.Join(want.Services, ",") {
				t.Fatalf("listed as a known difference, but the answer listed is docker compose's own: %s", known)
			}
			if strings.Join(got, ",") != known {
				t.Errorf("services = %v; this cell is a known difference, listed as %q (docker compose's are %v) — if it has come to agree, take it off the list and out of the docs", got, known, want.Services)
			}
			return
		}
		if strings.Join(got, ",") != strings.Join(want.Services, ",") {
			t.Errorf("services = %v, docker compose's are %v", got, want.Services)
		}
	}
	for _, c := range golden.Cells {
		cell := fmt.Sprintf("flag=%v/shell=%s/env=%s/%s", c.Flag, c.Shell, c.Env, c.Source)
		t.Run(cell, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "proj")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, "compose.yaml"), compose)
			var args []string
			switch c.Source {
			case "dotenv":
				if c.Env != "absent" {
					write(t, filepath.Join(dir, ".env"), line(c.Env, "c"))
				}
			case "envfile":
				write(t, filepath.Join(dir, ".env"), "COMPOSE_PROFILES=d\n")
				write(t, filepath.Join(dir, "cf.env"), line(c.Env, "c"))
				args = append(args, "--env-file", "cf.env")
			default:
				t.Fatalf("unknown source %q", c.Source)
			}
			if c.Flag {
				args = append(args, "--profile", "a")
			}
			var shell *string
			switch c.Shell {
			case "value":
				shell = ptr("b")
			case "empty":
				shell = ptr("")
			}
			ask(t, cell, dir, args, shell, nil, c.answer)
		})
	}
	for _, c := range golden.Extras {
		show := func(v *string) string {
			if v == nil {
				return "-"
			}
			return fmt.Sprintf("%q", *v)
		}
		cell := fmt.Sprintf("%s/%s%s/%q%q/%s,%s,%s,%s/%s,%s", c.Kind, c.How, c.Place, c.Value, c.Flags, show(c.Cwd), show(c.Sub), show(c.One), show(c.Two), show(c.CwdEnv), show(c.SubEnv))
		t.Run(cell, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "proj")
			if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			var args []string
			var shell *string
			switch c.Kind {
			case "where":
				write(t, filepath.Join(dir, "compose.yaml"), "services:\n  main:\n    image: alpine:3\n")
				write(t, filepath.Join(dir, "sub", "x.yaml"), compose)
				write(t, filepath.Join(dir, ".env"), "COMPOSE_PROFILES=c\n")
				write(t, filepath.Join(dir, "sub", ".env"), "COMPOSE_PROFILES=d\n")
				if c.How == "flag" {
					args = []string{"-f", "sub/x.yaml"}
				}
			case "second":
				// Only the `.env` files the cell names exist.
				write(t, filepath.Join(dir, "compose.yaml"), "services:\n  main:\n    image: alpine:3\n")
				write(t, filepath.Join(dir, "sub", "x.yaml"), compose)
				if c.Cwd != nil {
					write(t, filepath.Join(dir, ".env"), "COMPOSE_PROFILES="+*c.Cwd+"\n")
				}
				if c.Sub != nil {
					write(t, filepath.Join(dir, "sub", ".env"), "COMPOSE_PROFILES="+*c.Sub+"\n")
				}
				switch c.How {
				case "flag":
					args = []string{"-f", "sub/x.yaml"}
				case "composefile+envfile":
					write(t, filepath.Join(dir, "cf.env"), "OTHER=1\n")
					args = []string{"--env-file", "cf.env"}
				case "composefile":
				default:
					t.Fatalf("unknown how %q", c.How)
				}
			case "layered":
				write(t, filepath.Join(dir, "compose.yaml"), "services:\n  main:\n    image: alpine:3\n")
				write(t, filepath.Join(dir, "sub", "x.yaml"), compose)
				if c.CwdEnv != nil {
					write(t, filepath.Join(dir, ".env"), *c.CwdEnv)
				}
				if c.SubEnv != nil {
					write(t, filepath.Join(dir, "sub", ".env"), *c.SubEnv)
				}
				for _, f := range c.Flags {
					args = append(args, "--profile", f)
				}
			case "flagvalue":
				write(t, filepath.Join(dir, "compose.yaml"), compose)
				for _, f := range c.Flags {
					args = append(args, "--profile", f)
				}
			case "twoenvfiles":
				write(t, filepath.Join(dir, "compose.yaml"), compose)
				for name, v := range map[string]*string{"one.env": c.One, "two.env": c.Two} {
					body := "OTHER=1\n"
					if v != nil {
						body = "COMPOSE_PROFILES=" + *v + "\n"
					}
					write(t, filepath.Join(dir, name), body)
				}
				args = []string{"--env-file", "one.env", "--env-file", "two.env"}
			case "emptyflag":
				write(t, filepath.Join(dir, "compose.yaml"), compose)
				if c.Place == "dotenv" {
					write(t, filepath.Join(dir, ".env"), "COMPOSE_PROFILES=b\n")
				} else {
					shell = ptr("b")
				}
				for _, f := range c.Flags {
					args = append(args, "--profile", f)
				}
			case "spelling":
				write(t, filepath.Join(dir, "compose.yaml"), compose)
				if c.Place == "dotenv" {
					write(t, filepath.Join(dir, ".env"), "COMPOSE_PROFILES="+c.Value+"\n")
				} else {
					shell = &c.Value
				}
			default:
				t.Fatalf("unknown kind %q", c.Kind)
			}
			var composeFile *string
			if c.Kind == "layered" || (c.Kind == "where" || c.Kind == "second") && strings.HasPrefix(c.How, "composefile") {
				composeFile = ptr("sub/x.yaml")
			}
			ask(t, cell, dir, args, shell, composeFile, c.answer)
		})
	}
	for cell := range knownDifferences {
		if !seen[cell] {
			t.Errorf("a known difference names a cell the matrix does not have: %s", cell)
		}
	}
}

// A service behind a profile that `up` enabled is started, and where it has a
// `restart:` policy it is put under the restart supervisor like any other: the
// `up` decides which services are watched and hands the watcher their names
// (the watcher does not work the set out from the profiles for itself). Here
// the only service with a policy is behind a profile that one thing alone
// enables, so a supervisor left running is one that was handed it. The `.env`
// row is the one this guards — before COMPOSE_PROFILES was read there, `up`
// never started the service; the `--profile` row is the control beside it.
func TestAServiceAProfileEnablesIsSupervised(t *testing.T) {
	for _, tc := range []struct {
		name   string
		dotenv string
		args   []string
	}{
		{"from the .env", "COMPOSE_PROFILES=extras\n", nil},
		{"from --profile", "", []string{"--profile", "extras"}},
	} {
		t.Run(tc.name, func(t *testing.T) { aServiceAProfileEnablesIsSupervised(t, tc.dotenv, tc.args) })
	}
}

func aServiceAProfileEnablesIsSupervised(t *testing.T, dotenv string, args []string) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	dir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "compose.yaml"), "services:\n  base:\n    image: web\n  extra:\n    image: web\n    restart: always\n    profiles: [extras]\n")
	if dotenv != "" {
		write(t, filepath.Join(dir, ".env"), dotenv)
	}
	t.Chdir(dir)
	for _, name := range []string{"COMPOSE_PROJECT_NAME", "COMPOSE_PATH_SEPARATOR", "COMPOSE_FILE", "COMPOSE_PROFILES"} {
		setOrUnset(t, name, nil)
	}
	out, err := run(t, append(append([]string{}, args...), "up", "--no-build")...)
	if err != nil {
		t.Fatalf("up: %v\n%s", err, out)
	}
	if !strings.Contains(out, "extra") {
		t.Fatalf("`extras` is enabled, so `up` starts extra; got:\n%s", out)
	}
	const gone = "no supervisor is left running: the service the profile enables was not handed to one"
	var pid int
	for deadline := time.Now().Add(5 * time.Second); pid == 0; time.Sleep(50 * time.Millisecond) {
		if pid = supervisorPID(t, state, "proj"); pid == 0 && time.Now().After(deadline) {
			t.Fatal(gone)
		}
	}
	t.Cleanup(func() {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Signal(syscall.SIGKILL)
		}
	})
	for deadline := time.Now().Add(1500 * time.Millisecond); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if !processIsAlive(pid) {
			t.Fatal(gone)
		}
	}
	if out, err := run(t, append(append([]string{}, args...), "down")...); err != nil {
		t.Fatalf("down: %v\n%s", err, out)
	}
	waitFor(t, "the supervisor to exit", func() bool { return !processIsAlive(pid) })
}

// `--profile` and COMPOSE_PROFILES used to add up, so a directory with both may
// have a service running that an `up` no longer starts. `ps` and `down` do not
// go by the active profiles — they act on every service of the project — so
// they reach it, with the same flag and variable as before. That was so before
// this change too (this is green without it): it is here so that reading the
// profiles in a new order does not come to narrow those two. That a container
// an earlier version started is in fact reached was measured on the runtime.
func TestPsAndDownDoNotGoByTheActiveProfiles(t *testing.T) {
	log := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "compose.yaml"), "services:\n  sa:\n    image: web\n    profiles: [a]\n  sb:\n    image: web\n    profiles: [b]\n")
	t.Chdir(dir)
	for _, name := range []string{"COMPOSE_PROJECT_NAME", "COMPOSE_PATH_SEPARATOR", "COMPOSE_FILE"} {
		setOrUnset(t, name, nil)
	}
	// The same directory as its user has it: the flag and the variable.
	t.Setenv("COMPOSE_PROFILES", "b")
	out, err := run(t, "--profile", "a", "ps")
	if err != nil {
		t.Fatalf("ps: %v\n%s", err, out)
	}
	if !strings.Contains(out, "sb.proj.opossum") {
		t.Errorf("sb is a service of this project, and ps lists it whatever the profiles say; got:\n%s", out)
	}
	before := len(log())
	if out, err := run(t, "--profile", "a", "down"); err != nil {
		t.Fatalf("down: %v\n%s", err, out)
	}
	removed := false
	for _, l := range log()[before:] {
		if strings.Contains(l, "delete") && strings.Contains(l, "sb.proj.opossum") {
			removed = true
		}
	}
	if !removed {
		t.Errorf("down removes sb with the rest; shim saw:\n%s", strings.Join(log()[before:], "\n"))
	}
}

// `run` activates the profiles as `up` does, so a one-off whose dependency is
// behind a profile the `.env` enables starts it and runs — where, before
// COMPOSE_PROFILES was read there, it was refused for a dependency whose
// profile is not active. The matrix asks `config` alone; this is the same
// reading reached through `run`.
func TestRunEnablesTheProfilesTheDotEnvNames(t *testing.T) {
	log := fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "compose.yaml"), "services:\n  base:\n    image: web\n    depends_on: [sa]\n  sa:\n    image: web\n    profiles: [a]\n")
	write(t, filepath.Join(dir, ".env"), "COMPOSE_PROFILES=a\n")
	t.Chdir(dir)
	for _, name := range []string{"COMPOSE_PROJECT_NAME", "COMPOSE_PATH_SEPARATOR", "COMPOSE_FILE", "COMPOSE_PROFILES"} {
		setOrUnset(t, name, nil)
	}
	out, err := run(t, "run", "--rm", "base", "true")
	if err != nil {
		t.Fatalf("the .env enables sa's profile, so base's dependency is there to start; got: %v\n%s", err, out)
	}
	if indexOfLine(log(), "--name sa.proj.opossum") < 0 {
		t.Errorf("run should have started sa, the dependency the profile enables; shim saw:\n%s", strings.Join(log(), "\n"))
	}
}
