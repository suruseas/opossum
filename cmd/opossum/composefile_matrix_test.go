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

// What chose the compose file × where the file is × what the working
// directory's `.env` says of the project's name × whether the file's own
// directory's `.env` names it: every cell, held to what docker compose made of
// the same cell. testdata/composefile-matrix/docker.json is docker compose's
// answer to each (gen.py beside it builds the cells and asks); this builds the
// same cells and asks opossum.
//
// The cell the rows of the other tests had not reached: a name that is set and
// empty in the working directory's `.env` is no name, and ends the search — the
// file's directory's `.env` is not asked next (docker compose names the project
// after the directory).
func TestTheComposeFileMatrixAgreesWithDockerCompose(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "composefile-matrix", "docker.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Cells []struct {
			Source   string   `json:"source"`
			Where    string   `json:"where"`
			CwdName  string   `json:"cwdname"`
			SubName  bool     `json:"subname"`
			RC       int      `json:"rc"`
			Name     string   `json:"name"`
			Services []string `json:"services"`
			V        string   `json:"V"`
		} `json:"cells"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	if len(golden.Cells) != 48 {
		t.Fatalf("the matrix is 4 sources x 2 places x 3 states x 2, got %d cells", len(golden.Cells))
	}
	const target = "services:\n  target:\n    image: alpine:3\n    environment: {V: \"${VAR:-unset}\"}\n"
	nameLine := func(state, value string) string {
		switch state {
		case "value":
			return "COMPOSE_PROJECT_NAME=" + value + "\n"
		case "empty":
			return "COMPOSE_PROJECT_NAME=\n"
		}
		return ""
	}
	for _, c := range golden.Cells {
		t.Run(fmt.Sprintf("%s/%s/cwdname=%s/subname=%v", c.Source, c.Where, c.CwdName, c.SubName), func(t *testing.T) {
			if c.RC != 0 {
				t.Fatalf("docker compose refused this cell (rc %d); the test has no refusal to compare", c.RC)
			}
			dir := filepath.Join(t.TempDir(), "proj")
			if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, "compose.yaml"), "services:\n  main:\n    image: alpine:3\n")
			write(t, filepath.Join(dir, "x.yaml"), target)
			write(t, filepath.Join(dir, "sub", "x.yaml"), target)
			path := "x.yaml"
			if c.Where == "other" {
				path = "sub/x.yaml"
			}
			dotenv := "VAR=from-cwd-env\n"
			var args []string
			var shell *string
			switch c.Source {
			case "envfile":
				dotenv += "COMPOSE_PROJECT_NAME=dotenvname\n"
				write(t, filepath.Join(dir, "cf.env"), "VAR=from-envfile\nCOMPOSE_FILE="+path+"\n"+nameLine(c.CwdName, "envfilename"))
				args = []string{"--env-file", "cf.env"}
			case "dotenv":
				dotenv += nameLine(c.CwdName, "cwdname") + "COMPOSE_FILE=" + path + "\n"
			case "shell":
				dotenv += nameLine(c.CwdName, "cwdname")
				shell = &path
			case "flag":
				dotenv += nameLine(c.CwdName, "cwdname")
				args = []string{"-f", path}
			default:
				t.Fatalf("unknown source %q", c.Source)
			}
			write(t, filepath.Join(dir, ".env"), dotenv)
			subenv := "VAR=from-sub-env\n"
			if c.SubName {
				subenv += "COMPOSE_PROJECT_NAME=subname\n"
			}
			write(t, filepath.Join(dir, "sub", ".env"), subenv)
			t.Chdir(dir)
			for _, n := range []string{"COMPOSE_PROJECT_NAME", "COMPOSE_PATH_SEPARATOR", "VAR"} {
				setOrUnset(t, n, nil)
			}
			setOrUnset(t, "COMPOSE_FILE", shell)

			out, err := run(t, append(append([]string{}, args...), "config")...)
			if err != nil {
				t.Fatalf("config: %v\n%s", err, out)
			}
			if got := configName(out); got != c.Name {
				t.Errorf("project name = %q, docker compose's is %q", got, c.Name)
			}
			if !strings.Contains(out, "V="+c.V+"\n") {
				t.Errorf("docker compose expands V to %q, got:\n%s", c.V, out)
			}
			svcs, err := run(t, append(append([]string{}, args...), "config", "--services")...)
			if err != nil {
				t.Fatalf("config --services: %v\n%s", err, svcs)
			}
			got := strings.Fields(svcs)
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(c.Services, ",") {
				t.Errorf("services = %v, docker compose's are %v", got, c.Services)
			}
		})
	}
}
