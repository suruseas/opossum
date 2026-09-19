package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// When COMPOSE_FILE names a compose file in another directory, docker compose
// reads the working directory's `.env` first and that directory's for what the
// first does not set — one rule, for a `${VAR}` the file refers to, for the
// project's name and for COMPOSE_PROFILES alike (measured on v5.5.1, #1140).
// With `-f` it is the file's directory's alone; with an `--env-file`, neither.
//
// testdata/envlayers-matrix/docker.json is docker compose's answer to every
// cell (gen.py beside it builds the cells and asks); this builds the same cells
// and asks opossum: the name, `${V}` as expanded, and the services enabled.
func TestTheEnvLayersMatrixAgreesWithDockerCompose(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "envlayers-matrix", "docker.json"))
	if err != nil {
		t.Fatal(err)
	}
	type answer struct {
		RC       int      `json:"rc"`
		Warned   bool     `json:"warned"`
		Name     string   `json:"name"`
		V        string   `json:"V"`
		Services []string `json:"services"`
	}
	var golden struct {
		Cells []struct {
			Kind, How, Cwd, Sub string
			answer
		} `json:"cells"`
		Extras []struct {
			Group, Kind, Label string
			CwdEnv             *string           `json:"cwdenv"`
			SubEnv             *string           `json:"subenv"`
			Shell              map[string]string `json:"shell"`
			answer
		} `json:"extras"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	if len(golden.Cells) != 144 || len(golden.Extras) != 27 {
		t.Fatalf("the matrix is 3 x 4 x 4 x 3 cells and 3 x (4 + 3 + 2) extras, got %d and %d", len(golden.Cells), len(golden.Extras))
	}
	compose := "services:\n  base:\n    image: \"alpine:${V:-unset}\"\n"
	for _, p := range []string{"c", "d"} {
		compose += "  s" + p + ":\n    image: alpine:3\n    profiles: [" + p + "]\n"
	}
	key := map[string]string{"var": "V", "name": "COMPOSE_PROJECT_NAME", "profiles": "COMPOSE_PROFILES"}
	value := map[string][2]string{"var": {"cwdv", "subv"}, "name": {"cwdname", "subname"}, "profiles": {"c", "d"}}
	body := func(kind, state string, which int) *string {
		switch state {
		case "nofile":
			return nil
		case "noline":
			return ptr("OTHER=1\n")
		case "value":
			return ptr(key[kind] + "=" + value[kind][which] + "\n")
		case "empty":
			return ptr(key[kind] + "=\n")
		}
		t.Fatalf("unknown state %q", state)
		return nil
	}
	ask := func(t *testing.T, how string, cwdenv, subenv *string, shell map[string]string, want answer) {
		t.Helper()
		if want.RC != 0 {
			t.Fatalf("docker compose refused this cell (rc %d); the test has no refusal to compare", want.RC)
		}
		dir := filepath.Join(t.TempDir(), "proj")
		if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, "compose.yaml"), "services:\n  main:\n    image: alpine:3\n")
		write(t, filepath.Join(dir, "sub", "x.yaml"), compose)
		var args []string
		var composeFile *string
		switch how {
		case "dotenv":
			b := "COMPOSE_FILE=sub/x.yaml\n"
			if cwdenv != nil {
				b = *cwdenv + b
			}
			cwdenv = &b
		case "flag":
			args = []string{"-f", "sub/x.yaml"}
		case "composefile", "envfile":
			composeFile = ptr("sub/x.yaml")
		default:
			t.Fatalf("unknown how %q", how)
		}
		if how == "envfile" {
			write(t, filepath.Join(dir, "cf.env"), "OTHER=1\n")
			args = []string{"--env-file", "cf.env"}
		}
		if cwdenv != nil {
			write(t, filepath.Join(dir, ".env"), *cwdenv)
		}
		if subenv != nil {
			write(t, filepath.Join(dir, "sub", ".env"), *subenv)
		}
		t.Chdir(dir)
		for _, n := range []string{"COMPOSE_PROJECT_NAME", "COMPOSE_PROFILES", "COMPOSE_PATH_SEPARATOR", "V", "X"} {
			var v *string
			if s, ok := shell[n]; ok {
				v = &s
			}
			setOrUnset(t, n, v)
		}
		setOrUnset(t, "COMPOSE_FILE", composeFile)
		out, stderr, err := runSplit(t, append(append([]string{}, args...), "config")...)
		if err != nil {
			t.Fatalf("config: %v\n%s", err, stderr)
		}
		// In 6 cells docker compose warns that a variable is not set (a value
		// refers to one only the other file sets, or a line below). opossum
		// warns of no unset variable, anywhere (#1143): a known difference, held
		// to here on the command's own stderr — the one opossum's messages go to
		// — so that a warning landing there is noticed and compared instead.
		if strings.Contains(stderr, "variable is not set") {
			t.Errorf("opossum warned of a variable that is not set — docker compose warns in this cell: %v. If that is #1143 landing, compare the two instead of this; got:\n%s", want.Warned, stderr)
		}
		if got := configName(out); got != want.Name {
			t.Errorf("project name = %q, docker compose's is %q", got, want.Name)
		}
		if !strings.Contains(out, "image: alpine:"+want.V+"\n") {
			t.Errorf("docker compose expands ${V} to %q, got:\n%s", want.V, out)
		}
		svcs, stderr, err := runSplit(t, append(append([]string{}, args...), "config", "--services")...)
		if err != nil {
			t.Fatalf("config --services: %v\n%s", err, stderr)
		}
		got := strings.Fields(svcs)
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(want.Services, ",") {
			t.Errorf("services = %v, docker compose's are %v", got, want.Services)
		}
	}
	for _, c := range golden.Cells {
		t.Run(fmt.Sprintf("%s/%s/cwd=%s/sub=%s", c.Kind, c.How, c.Cwd, c.Sub), func(t *testing.T) {
			ask(t, c.How, body(c.Kind, c.Cwd, 0), body(c.Kind, c.Sub, 1), nil, c.answer)
		})
	}
	for _, c := range golden.Extras {
		t.Run(fmt.Sprintf("%s/%s/%s", c.Group, c.Kind, c.Label), func(t *testing.T) {
			ask(t, "composefile", c.CwdEnv, c.SubEnv, c.Shell, c.answer)
		})
	}
}
