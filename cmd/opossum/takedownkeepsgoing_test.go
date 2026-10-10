package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A project an earlier opossum started comes down by a plain `down` (and `destroy`, `stop`, `kill`) when its files are refused for a key the take-down does not need: a key a declaration of
// `networks`, `volumes` or `models` should not hold that comes in by a merge key, the `deploy` of a service an extends takes when the file gives it a list, and a `healthcheck` of a service in the
// extended file that nothing takes whose `retries` is a word (found on a real runtime, 0.44.0 against the next release: the take-down was refused, the containers and the declared network left; #2042).
// The refusal is named on stderr and the command goes on, as it does for the other values `up` refuses.
func TestAProjectIsTakenDownWhateverKeyTheRefusalIsAbout(t *testing.T) {
	for _, tc := range []struct {
		name      string
		files     map[string]string // the first is compose.yaml
		container string
		want      string // the refusal, as named on stderr
		network   string // a declared network the take-down removes, when the file declares one
	}{
		{
			"a declared network holds a key that came in by a merge key",
			map[string]string{"compose.yaml": "name: demo\nx-a: &a {restart: always}\nservices:\n  s: {image: alpine, command: sleep 300, networks: [n]}\nnetworks:\n  n:\n    <<: [*a]\n"},
			"s.demo.opossum", `networks.n: "restart" is not a key docker compose takes`, "demo-n",
		},
		{
			"a declared volume holds a key that came in by a merge key",
			map[string]string{"compose.yaml": "name: demo\nx-a: &a {bogus: 1}\nservices:\n  s: {image: alpine, command: sleep 300}\nvolumes:\n  v:\n    <<: *a\n"},
			"s.demo.opossum", `volumes.v: "bogus" is not a key docker compose takes`, "",
		},
		{
			"a declared secret holds a key that came in by a merge key",
			map[string]string{"compose.yaml": "name: demo\nx-a: &a {bogus: 1}\nservices:\n  s: {image: alpine, command: sleep 300}\nsecrets:\n  k:\n    <<: *a\n    file: ./k.txt\n"},
			"s.demo.opossum", `secrets.k: "bogus" is not a key docker compose takes`, "",
		},
		{
			"a declared config holds a key that came in by a merge key",
			map[string]string{"compose.yaml": "name: demo\nx-a: &a {bogus: 1}\nservices:\n  s: {image: alpine, command: sleep 300}\nconfigs:\n  c:\n    <<: *a\n    file: ./k.txt\n"},
			"s.demo.opossum", `configs.c: "bogus" is not a key docker compose takes`, "",
		},
		{
			"a declared model holds a key that is not one",
			map[string]string{"compose.yaml": "name: demo\nservices:\n  s: {image: alpine, command: sleep 300}\nmodels:\n  m:\n    model: ai/x\n    bogus: 1\n"},
			"s.demo.opossum", `models.m: "bogus" is not a key docker compose takes`, "",
		},
		{
			"a service the extends takes gives deploy as a list, the extender writes resources",
			map[string]string{"compose.yaml": "name: demo\nservices:\n  s:\n    extends: {file: base.yaml, service: b}\n    deploy: {resources: {limits: {cpus: \"1\"}}}\n", "base.yaml": "services:\n  b:\n    image: alpine\n    deploy: [a]\n"},
			"s.demo.opossum", `cannot unmarshal !!seq into compose.Deploy`, "",
		},
		{
			"a service nothing takes in the extended file has a healthcheck disable that is a word, written by an alias key",
			map[string]string{"compose.yaml": "name: demo\nservices:\n  s:\n    extends: {file: base.yaml, service: b}\n", "base.yaml": "x-d: &d disable\nservices:\n  b: {image: alpine:latest}\n  u:\n    image: alpine:latest\n    healthcheck:\n      *d : abc\n"},
			"s.demo.opossum", `cannot unmarshal !!str into bool`, "",
		},
		{
			"a service the extends takes gives deploy as a list",
			map[string]string{"compose.yaml": "name: demo\nservices:\n  s:\n    extends: {file: base.yaml, service: b}\n    deploy: {replicas: 1}\n", "base.yaml": "services:\n  b:\n    image: alpine\n    deploy: [a]\n"},
			"s.demo.opossum", `cannot unmarshal !!seq into compose.Deploy`, "",
		},
		{
			"a service nothing takes in the extended file has a healthcheck retries that is a word, written by an alias key",
			map[string]string{"compose.yaml": "name: demo\nservices:\n  s:\n    extends: {file: base.yaml, service: b}\n", "base.yaml": "x-r: &r retries\nservices:\n  b: {image: alpine:latest}\n  u:\n    image: alpine:latest\n    healthcheck:\n      *r : abc\n"},
			"s.demo.opossum", `healthcheck retries: not a count`, "",
		},
		{
			"a service nothing takes in the extended file has a healthcheck retries that is a word, written by a merge key",
			map[string]string{"compose.yaml": "name: demo\nservices:\n  s:\n    extends: {file: base.yaml, service: b}\n", "base.yaml": "x-h: &h {retries: abc}\nservices:\n  b: {image: alpine:latest}\n  u:\n    image: alpine:latest\n    healthcheck:\n      <<: *h\n"},
			"s.demo.opossum", `healthcheck retries: not a count`, "",
		},
	} {
		for _, td := range []struct {
			args []string
			acts string // a line the fake runtime logs when the command acted
		}{
			{[]string{"down"}, "delete"},
			{[]string{"destroy", "--force"}, "delete"},
			{[]string{"stop"}, "stop"},
			{[]string{"kill"}, "kill"},
		} {
			t.Run(tc.name+"/"+strings.Join(td.args, " "), func(t *testing.T) {
				readLog := fakeShim(t)
				t.Setenv("COMPOSE_PROFILES", "")
				t.Setenv("STATE_DIR", t.TempDir())
				t.Setenv("XDG_STATE_HOME", t.TempDir())
				strictContainers(t, tc.container)
				dir := t.TempDir()
				for name, text := range tc.files {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				stdout, stderr, err := runSplit(t, append([]string{"-f", filepath.Join(dir, "compose.yaml")}, td.args...)...)
				if err != nil {
					t.Fatalf("want the command to go on, got %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
				}
				if !strings.Contains(stderr, tc.want) || !strings.Contains(stderr, "`up` refuses this compose file; going on, as an earlier opossum may have started it") {
					t.Errorf("want the refusal %q named on stderr, got:\n%s", tc.want, stderr)
				}
				acted, network := false, false
				for _, l := range readLog() {
					if strings.HasPrefix(l, td.acts+" ") && strings.Contains(l, tc.container) {
						acted = true
					}
					if tc.network != "" && l == "network delete "+tc.network {
						network = true
					}
				}
				if !acted {
					t.Errorf("want %q sent for %s, got log:\n%s", td.acts, tc.container, strings.Join(readLog(), "\n"))
				}
				if tc.network != "" && td.args[0] == "down" && !network {
					t.Errorf("want the declared network %s removed by down, got log:\n%s", tc.network, strings.Join(readLog(), "\n"))
				}
			})
		}
	}
}

// A take-down goes on past the refusals above and only those: a project whose name cannot be read (a `name` that is not a string, written after a declaration that holds an unknown key), and a file that fails to
// read on a key the take-down does need beside one it does not (`ports`, `depends_on` with a `healthcheck` that is a word; an `image`, a `container_name` or `volumes` of the wrong kind are read already), are still refused, and nothing is sent to the runtime
// (found by the review of the change that goes on; #2042).
func TestATakeDownIsStillRefusedWhereTheProjectCannotBeRead(t *testing.T) {
	const decl = "x-a: &a {bogus: 1}\nnetworks:\n  n:\n    <<: *a\n"
	for _, tc := range []struct {
		name string
		file string
		base string // base.yaml, when the file extends one
	}{
		{"a name with nothing after it, after the declaration", decl + "name:\nservices:\n  s: {image: alpine, command: sleep 300}\n", ""},
		{"a name that is a number, after the declaration", decl + "name: 123\nservices:\n  s: {image: alpine, command: sleep 300}\n", ""},
		{"a top-level key docker compose does not take, after the declaration", decl + "servcies: {}\nservices:\n  s: {image: alpine, command: sleep 300}\n", ""},
		{"a service nothing takes in the extended file has ports that are a word beside a healthcheck word", "name: demo\nservices:\n  s:\n    extends: {file: base.yaml, service: b}\n", "services:\n  b: {image: alpine}\n  u: {image: alpine, ports: abc, healthcheck: {retries: abc}}\n"},
		{"a service nothing takes in the extended file has a depends_on that is a number beside a healthcheck word", "name: demo\nservices:\n  s:\n    extends: {file: base.yaml, service: b}\n", "services:\n  b: {image: alpine}\n  u: {image: alpine, depends_on: 5, healthcheck: {retries: abc}}\n"},
		{"the extended service has a deploy list and a depends_on that is a number", "name: demo\nservices:\n  s:\n    extends: {file: base.yaml, service: b}\n", "services:\n  b: {image: alpine, depends_on: 5, deploy: [a]}\n"},
	} {
		for _, args := range [][]string{{"down"}, {"destroy", "--force"}, {"stop"}, {"kill"}} {
			t.Run(tc.name+"/"+strings.Join(args, " "), func(t *testing.T) {
				readLog := fakeShim(t)
				t.Setenv("COMPOSE_PROFILES", "")
				t.Setenv("STATE_DIR", t.TempDir())
				t.Setenv("XDG_STATE_HOME", t.TempDir())
				dir := t.TempDir()
				p := filepath.Join(dir, "compose.yaml")
				if err := os.WriteFile(p, []byte(tc.file), 0o644); err != nil {
					t.Fatal(err)
				}
				if tc.base != "" {
					if err := os.WriteFile(filepath.Join(dir, "base.yaml"), []byte(tc.base), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				stdout, stderr, err := runSplit(t, append([]string{"-f", p}, args...)...)
				if err == nil {
					t.Fatalf("want the take-down refused, got success\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
				}
				for _, l := range readLog() {
					if strings.HasPrefix(l, "stop ") || strings.HasPrefix(l, "kill ") || strings.HasPrefix(l, "delete ") || strings.HasPrefix(l, "network delete ") {
						t.Errorf("want nothing sent to the runtime, got %q in:\n%s", l, strings.Join(readLog(), "\n"))
					}
				}
			})
		}
	}
}
