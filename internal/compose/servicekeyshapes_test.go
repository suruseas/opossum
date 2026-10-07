package compose

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// docker compose refuses a service key whose value is not of the shape the
// schema gives it (`services.web.hostname must be a string`). opossum read the
// keys it acts on through its decoder and took every other key as it came, so a
// file docker compose refuses for `hostname: [1, 2]`, `dns: 7` or `sysctls: [1]`
// loaded here (232 of 497 forms in the first sweep).
//
// testdata/service-key-forms.json is what docker compose v5.5.1 (Docker 29.8.0)
// said to each of the 93 keys of its service schema written in 13 forms
// (testdata/tools/capture-service-key-forms.py makes it): accepted, refused for the
// shape of the value ("schema": a kind, a pattern, a choice, a bound, a key the
// schema does not know), or refused for another reason ("other": a name that is not
// defined, a number that does not read, a file that is not there).
//
// Two things hold of every row. What docker compose accepts loads here — a
// refusal of a file it takes is worse than a file it refuses being taken. And
// what docker compose refuses for its shape is refused here. The rows where that
// is not so are named below, each with what makes it so: they are pinned, so a
// change that moves one is seen and the list is written again.
func TestAServiceKeyIsHeldToTheShapeDockerComposeGivesIt(t *testing.T) {
	// Accepted by docker compose and refused here on purpose: opossum reads these
	// two keys itself and refuses a value it cannot act on (a restart policy it
	// does not have, an address that is not six hex pairs).
	stricter := map[string]string{}
	for _, f := range []string{"str", "sint", "sbool", "sfloat", "sstr"} {
		stricter["restart/"+f] = "opossum reads the policy and refuses one it does not have"
		stricter["mac_address/"+f] = "opossum reads the address and refuses one that is not six hex pairs"
	}
	// A key the schema does not give a mapping is refused there (`additional properties 'a' not allowed`) and, since #1644, here: no row is
	// left that docker compose refuses for its shape and opossum takes.
	laxer := map[string]string{}

	raw, err := os.ReadFile("testdata/service-key-forms.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct{ Key, Form, Value, Docker, Msg string }
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 93*13 {
		t.Fatalf("the capture has %d rows, want 93 keys × 13 forms", len(rows))
	}
	seenStricter, seenLaxer := map[string]bool{}, map[string]bool{}
	for _, r := range rows {
		id := r.Key + "/" + r.Form
		body := "services:\n  web:\n    image: alpine:3\n    " + r.Key + ": " + r.Value + "\n"
		if r.Key == "image" {
			body = "services:\n  web:\n    image: " + r.Value + "\n"
		}
		_, err := Load(writeTemp(t, body))
		switch r.Docker {
		case "accept":
			if _, ok := stricter[id]; ok {
				seenStricter[id] = true
				if err == nil {
					t.Errorf("%s: docker compose accepts it and opossum refuses it on purpose (%s), and it loaded", id, stricter[id])
				}
				continue
			}
			if err != nil {
				t.Errorf("%s: docker compose accepts `%s: %s`, and it was refused: %v", id, r.Key, r.Value, err)
			}
		case "schema":
			if _, ok := laxer[id]; ok {
				seenLaxer[id] = true
				if err != nil {
					t.Errorf("%s: this row is listed as one opossum accepts (%s), and it was refused: %v", id, laxer[id], err)
				}
				continue
			}
			if err == nil {
				t.Errorf("%s: docker compose refuses `%s: %s` (%s), and it loaded", id, r.Key, r.Value, r.Msg)
			}
		}
	}
	for id := range stricter {
		if !seenStricter[id] {
			t.Errorf("%s is listed as stricter than docker compose and is not a row that docker compose accepts", id)
		}
	}
	for id := range laxer {
		if !seenLaxer[id] {
			t.Errorf("%s is listed as laxer than docker compose and is not a row that docker compose refuses for the shape", id)
		}
	}
}

// What the refusal says: the key, and what it takes.
func TestTheRefusalOfAServiceKeyNamesTheKeyAndWhatItTakes(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"a string", "hostname: [1, 2]", "services.web.hostname must be a string"},
		{"a string or a list", "dns: 7", "services.web.dns must be a string or a list of strings"},
		{"a mapping or a list of strings", "sysctls: [1]", "services.web.sysctls[0] must be a string"},
		{"a boolean or a string", "privileged: 7", "services.web.privileged must be a boolean or a string"},
		{"a list", "expose: \"80\"", "services.web.expose must be a list"},
		{"an item of a list", "security_opt: [1]", "services.web.security_opt[0] must be a string"},
		{"a value of a mapping", "extra_hosts: {a: 7}", "services.web.extra_hosts.a must be"},
		{"a pattern", "container_name: x", "services.web.container_name \"x\" does not match the pattern"},
		{"a boolean that is not read from a string", "use_api_socket: \"true\"", "services.web.use_api_socket must be a boolean"},
		// The bounds and the choices the schema gives (each measured: docker compose
		// refuses the first of a pair and takes the second).
		{"above the most", "cpu_percent: 101", "services.web.cpu_percent 101 is above the most, 100"},
		{"below the least", "cpu_percent: -1", "services.web.cpu_percent -1 is below the least, 0"},
		{"below the least, as a count", "cpu_count: -1", "services.web.cpu_count -1 is below the least, 0"},
		{"above the most, with a negative least", "oom_score_adj: 1001", "services.web.oom_score_adj 1001 is above the most, 1000"},
		{"below a negative least", "oom_score_adj: -1001", "services.web.oom_score_adj -1001 is below the least, -1000"},
		{"a choice that is not one", "cgroup: x", "services.web.cgroup x is not one of"},
		{"an item of the wrong kind in a list of mappings", "gpus: [x]", "services.web.gpus"},
		// The second service is held to it too.
		{"the second service", "hostname: h\n  zz:\n    image: alpine\n    hostname: [1]", "services.zz.hostname must be a string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    "+tc.body+"\n"))
			if err == nil {
				t.Fatalf("docker compose refuses this and it loaded: %s", tc.body)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say %q:\n%v", tc.want, err)
			}
		})
	}
}

// What is not refused, the way real files write it: docker compose takes each of
// these (v5.5.1, `config`; the awesome-compose and docker/compose e2e files, 89 that
// docker compose accepts, all load).
func TestServiceKeysAreNotRefusedForTheWaysRealFilesWriteThem(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"a command as a string, a list and empty", "web:\n  image: a\n  command: sleep 1\napi:\n  image: a\n  command: [sleep, \"1\"]\ndb:\n  image: a\n  command:\n"},
		{"an environment as a list and as a mapping", "web:\n  image: a\n  environment: [A=1, B]\napi:\n  image: a\n  environment: {A: 1, B: true, C: null}\n"},
		{"ports of every kind", "web:\n  image: a\n  ports: [80, \"8080:80\", {target: 80, published: 8081}]\n"},
		{"numbers written as strings where a number is taken", "web:\n  image: a\n  cpu_shares: \"512\"\n  mem_limit: 1g\n  pids_limit: \"100\"\n  oom_score_adj: -500\n"},
		{"booleans written as words", "web:\n  image: a\n  stdin_open: true\n  tty: \"true\"\n  privileged: yes\n  init: true\n"},
		{"a list and a mapping where either is taken", "web:\n  image: a\n  sysctls: {net.core.somaxconn: 1024}\n  labels: [a=b]\napi:\n  image: a\n  sysctls: [net.core.somaxconn=1024]\n  labels: {a: b}\n"},
		{"a string and a list where either is taken", "web:\n  image: a\n  dns: 1.1.1.1\n  dns_search: [a.b, c.d]\n  env_file: .env\n"},
		{"depends_on as a list and as a mapping", "db:\n  image: a\nweb:\n  image: a\n  depends_on: [db]\napi:\n  image: a\n  depends_on: {db: {condition: service_started}}\n"},
		{"extra_hosts as a list and as a mapping", "web:\n  image: a\n  extra_hosts: [\"h:1.1.1.1\"]\napi:\n  image: a\n  extra_hosts: {h: 1.1.1.1}\n"},
		// docker compose takes a repeat in these lists (it refuses one in `security_opt`).
		{"a list that repeats an item", "web:\n  image: a\n  dns: [a, a]\n  expose: [80, 80]\n  cap_add: [NET_ADMIN, NET_ADMIN]\n  dns_search: [a, a]\n"},
		{"an extension key of any shape", "web:\n  image: a\n  x-note: [1, {a: 2}]\n"},
		{"a value through an alias and a merge key", "x-base: &base {image: a, dns: [1.1.1.1]}\nweb:\n  <<: *base\n  hostname: h\n"},
		// A date is a string to docker compose (`config` prints it as one), a timestamp to
		// the YAML reader: an OCI annotation is written so.
		{"a date without quotes where a string is taken", "web:\n  image: a\n  annotations: {org.opencontainers.image.created: 2024-01-15}\n  sysctls: {a: 2026-01-01}\n  logging: {driver: json-file, options: {a: 2026-01-01}}\n  pull_refresh_after: 2026-01-01\n"},
		// A name that starts with a dash: the pattern is asked as a search, as there.
		{"a container_name the pattern finds a match in", "web:\n  image: a\n  container_name: \"-ab\"\n"},
		{"null values in a mapping", "web:\n  image: a\n  logging: {driver: json-file, options: {max-size: }}\n  annotations: {c: }\n  sysctls: {a: }\n"},
		{"the bounds and the choices at their edge", "web:\n  image: a\n  cpu_percent: 100\n  cpu_count: 0\n  oom_score_adj: -1000\n  cgroup: host\n  gpus: all\n"},
		{"gpus as a list of devices", "web:\n  image: a\n  gpus: [{driver: nvidia, count: 1}]\n"},
		{"logging", "web:\n  image: a\n  logging: {driver: json-file, options: {max-size: 1m}}\n"},
		{"ulimits", "web:\n  image: a\n  ulimits: {nofile: {soft: 1, hard: 2}, nproc: 3}\n"},
		{"healthcheck", "web:\n  image: a\n  healthcheck: {test: [CMD, \"true\"], interval: 5s, retries: 3}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(writeTemp(t, "services:\n"+indentAll(tc.body))); err != nil {
				t.Fatalf("docker compose takes this file, and it was refused: %v", err)
			}
		})
	}
}

func indentAll(body string) string {
	var out strings.Builder
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		out.WriteString("  " + line + "\n")
	}
	return out.String()
}

// A value written as a `${...}` reference is read after it is expanded, as docker
// compose reads it, and the refusal does not depend on what it expands to when
// the schema takes a string there.
func TestAServiceKeyIsHeldToItsShapeAfterExpansion(t *testing.T) {
	t.Setenv("HOST", "h")
	t.Setenv("PORTNUM", "80")
	if _, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    hostname: ${HOST}\n    cpu_shares: ${PORTNUM}\n")); err != nil {
		t.Fatalf("values that expand to what the keys take were refused: %v", err)
	}
}

// Each file is held to it on its own: an overlay that writes a key of the wrong shape is
// refused naming that file.
func TestAServiceKeyOfALaterFileIsHeldToItsShape(t *testing.T) {
	base := writeTemp(t, "services:\n  web:\n    image: alpine\n")
	overlay := writeTemp(t, "services:\n  web:\n    hostname: [1]\n")
	_, err := LoadFiles([]string{base, overlay}, nil)
	if err == nil {
		t.Fatal("the overlay gives hostname a list and loaded")
	}
	if !strings.Contains(err.Error(), overlay) || !strings.Contains(err.Error(), "services.web.hostname must be a string") {
		t.Errorf("the refusal does not name the overlay and the key:\n%v", err)
	}
}
