package compose

// Keys written with nothing after them that docker compose (v5.5.0)
// refuses and opossum used to read past (#808; exit codes and full output
// read, 26 fixtures). Three shapes: a bare top-level `services:` in any of
// several files (`services must be a mapping` — one file's was already
// refused as "no services"); a key in a long-form item — a mount's
// `source:` or `type:`, a port's `published:`, a dependency's
// `condition:` — which the struct decode read as left out; and, across
// files, a bare key a later file writes that no earlier file gave a value
// to (a new network's `internal:`, a `build.context:` no base has), which
// docker compose refuses naming that file — where the same key over an
// earlier value is "not given" and keeps it.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAKeyWithNothingAfterItInALongFormItemIsRefused(t *testing.T) {
	svc := "services:\n  web:\n    image: alpine\n"
	for _, tc := range []struct{ name, body, want string }{
		{"a mount's source", svc + "    volumes: [{type: bind, source: , target: /x}]\n", "volumes entry 1 of 1: source has nothing after it (line 4) — write the value or remove the key"},
		{"a mount's type", svc + "    volumes: [{type: , source: ., target: /x}]\n", "volumes entry 1 of 1: type has nothing after it"},
		{"the second mount's source", svc + "    volumes: [./a:/a, {type: bind, source: ~, target: /x}]\n", "volumes entry 2 of 2: source has nothing after it"},
		{"a port's published", svc + "    ports: [{target: 80, published: }]\n", "ports entry 1 of 1: published has nothing after it"},
		{"a dependency's condition", svc + "    depends_on: {db: {condition: }}\n  db:\n    image: alpine\n", "depends_on.db: condition has nothing after it"},
		{"through an alias", "x-n: &n ~\n" + svc + "    volumes: [{type: bind, source: *n, target: /x}]\n", "volumes entry 1 of 1: source has nothing after it"},
		// Declarations too: docker compose refuses `networks.back.internal
		// must be a boolean or string`, `volumes.data.name must be a string`.
		{"a network's internal", svc + "networks:\n  back: {internal: }\n", "a network's declaration: internal has nothing after it"},
		{"a network's name", svc + "networks:\n  back: {name: }\n", "a network's declaration: name has nothing after it"},
		{"a volume's name", svc + "volumes:\n  data: {name: }\n", "a volume's declaration: name has nothing after it"},
		{"a secret's file", svc + "secrets:\n  s: {file: }\n", "a secret's declaration: file has nothing after it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
		})
	}
	// Left out is not bare: a mount without `source`, a port without
	// `published`, a dependency without `condition` load as before. (A
	// mount without `type` is refused on its own account — docker compose
	// needs one in the long form; see mounttype_test.go.)
	p, err := Load(writeTemp(t, svc+"    volumes: [{type: volume, target: /x}]\n    ports: [{target: 80}]\n    depends_on: {db: {}}\n  db:\n    image: alpine\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := p.Services["web"].DependsOn[0].Condition; got != ConditionStarted {
		t.Errorf("condition = %q, want the default", got)
	}
}

func TestABareTopLevelServicesIsRefusedInAnyFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("good.yml", "services:\n  web:\n    image: alpine\n")
	bare := write("bare.yml", "services:\nnetworks:\n  front: {}\n")
	for _, tc := range []struct {
		name  string
		files []string
	}{
		{"first", []string{bare, good}},
		{"later", []string{good, bare}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadFiles(tc.files, nil)
			if err == nil || !strings.Contains(err.Error(), "services must be a mapping") || !strings.Contains(err.Error(), "bare.yml") {
				t.Fatalf("want the refusal naming bare.yml, got: %v", err)
			}
		})
	}
}

func TestABareKeyNoEarlierFileGaveAValueToIsRefusedNamingTheLaterFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := write("base.yml", "services:\n  web:\n    image: alpine\nnetworks:\n  back: {internal: true}\n")
	for _, tc := range []struct {
		name, over, want string
		namesALine       bool
	}{
		{"a new network's internal", "networks:\n  other: {internal: }\n", "internal", false},
		{"a build.context no base has", "services:\n  web:\n    build: {context: }\n", "build.context must be a string, got nothing", false},
		{"a ports no base has", "services:\n  web:\n    ports:\n", "ports: expected a list, got nothing", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadFiles([]string{base, write("over.yml", tc.over)}, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "over.yml") {
				t.Fatalf("want the refusal naming over.yml, got: %v", err)
			}
			// The file that wrote the key, not every file: the base has no
			// part in it.
			if strings.Contains(err.Error(), "base.yml") {
				t.Errorf("the refusal should name the later file alone, got: %v", err)
			}
			// A line the refusal names is one of the later file, which is
			// the one that was read: it is not said to be a line of a merged
			// document.
			if strings.Contains(err.Error(), "merged document") {
				t.Errorf("the refusal reads the later file by itself, got: %v", err)
			}
		})
	}
	// A dependency the base listed by name has its condition: a later
	// file's `condition:` with nothing after it keeps it (docker compose
	// reads the list as `{db: {condition: service_started}}` before
	// merging).
	p, err := LoadFiles([]string{write("deps.yml", "services:\n  web:\n    image: alpine\n    depends_on: [db]\n  db:\n    image: alpine\n"), write("cond.yml", "services:\n  web:\n    depends_on: {db: {condition: }}\n")}, nil)
	if err != nil {
		t.Fatalf("a bare condition over a listed dependency keeps service_started: %v", err)
	}
	if got := p.Services["web"].DependsOn[0].Condition; got != ConditionStarted {
		t.Errorf("condition = %q, want %q", got, ConditionStarted)
	}
	// The same key over an earlier value is "not given": the value stands.
	p, err = LoadFiles([]string{base, write("keep.yml", "networks:\n  back: {internal: }\n")}, nil)
	if err != nil {
		t.Fatalf("a bare field over an earlier value keeps it: %v", err)
	}
	if !p.Networks["back"].Internal {
		t.Errorf("internal = false, want the base file's true")
	}
}

// A key a later `-f` writes with nothing after it is "not given" only where an earlier
// file gave the key a value (#1562; measured, docker compose v5.5.1, `config -q`): with
// none to fall back on, docker compose refuses the file — for `dns: ~`, for a field
// inside a mapping (`deploy: {mode: ~}`) and for some forty other keys — and with one, it
// reads the key as not written. A short form stands for its long form: `depends_on: [db]`
// gives `db` a condition, and `build: .` gives the build a context.
func TestABareKeyInALaterFileIsRefusedWhereNoEarlierFileGaveTheKeyAValue(t *testing.T) {
	const svc = "services:\n  web:\n    image: alpine\n"
	const db = "  db:\n    image: alpine\n"
	for _, tc := range []struct {
		name, base, over string
		refused          bool
	}{
		{"dns, no base has it", svc, "services:\n  web:\n    dns: ~\n", true},
		{"dns, the base has it", svc + "    dns: [1.1.1.1]\n", "services:\n  web:\n    dns: ~\n", false},
		{"extra_hosts, no base has it", svc, "services:\n  web:\n    extra_hosts:\n", true},
		{"extra_hosts, the base has it", svc + "    extra_hosts: ['a:1.2.3.4']\n", "services:\n  web:\n    extra_hosts:\n", false},
		{"hostname, no base has it", svc, "services:\n  web:\n    hostname: ~\n", true},
		{"hostname, the base has it", svc + "    hostname: h\n", "services:\n  web:\n    hostname: ~\n", false},
		{"privileged, no base has it", svc, "services:\n  web:\n    privileged: ~\n", true},
		{"deploy.mode, the base has deploy without a mode", svc + "    deploy: {replicas: 2}\n", "services:\n  web:\n    deploy: {mode: ~}\n", true},
		{"deploy.mode, the base has a mode", svc + "    deploy: {mode: global}\n", "services:\n  web:\n    deploy: {mode: ~}\n", false},
		{"healthcheck.test, the base has healthcheck without it", svc + "    healthcheck: {interval: 1s}\n", "services:\n  web:\n    healthcheck: {test: ~}\n", true},
		{"a condition over a dependency the base listed by name", svc + "    depends_on: [db]\n" + db, "services:\n  web:\n    depends_on: {db: {condition: ~}}\n", false},
		{"a context over a build the base gave as a path", svc + "    build: .\n", "services:\n  web:\n    build: {context: ~}\n", false},
		{"a dockerfile over a build the base gave as a path", svc + "    build: .\n", "services:\n  web:\n    build: {dockerfile: ~}\n", true},
		{"deploy.mode, the base has no deploy at all", svc, "services:\n  web:\n    deploy: {mode: ~}\n", true},
		{"logging.driver, the base has no logging", svc, "services:\n  web:\n    logging: {driver: ~}\n", true},
		{"dns of a service that extends one in the file, which has it", svc, "services:\n  web:\n    extends: {service: common}\n    dns: ~\n  common:\n    image: alpine\n    dns: [1.1.1.1]\n", false},
		{"deploy.mode of a service that extends one in the file, which has it", svc, "services:\n  web:\n    extends: {service: common}\n    deploy: {mode: ~}\n  common:\n    image: alpine\n    deploy: {mode: global}\n", false},
		{"dns of a service that extends one in the file by name, which has it", svc, "services:\n  web:\n    extends: common\n    dns: ~\n  common:\n    image: alpine\n    dns: [1.1.1.1]\n", false},
		{"dns of a new service that extends one in the file, which has it", svc, "services:\n  api:\n    extends: {service: common}\n    dns: ~\n  common:\n    image: alpine\n    dns: [1.1.1.1]\n", false},
		{"dns of a service whose extends comes in by a merge key", svc, "x-e: &e\n  extends: {service: common}\nservices:\n  web:\n    <<: *e\n    dns: ~\n  common:\n    image: alpine\n    dns: [1.1.1.1]\n", false},
		{"a wrong value of a service that extends is still refused", svc, "services:\n  web:\n    extends: {service: common}\n    privileged: abc\n  common:\n    image: alpine\n", true},
		{"logging, the base has it", svc + "    logging: {driver: json-file}\n", "services:\n  web:\n    logging: ~\n", true},
		{"networks, the base has it", svc + "    networks: [n]\n", "services:\n  web:\n    networks: ~\nnetworks:\n  n: {}\n", true},
		{"depends_on, the base has it", svc + "    depends_on: {db: {condition: service_started}}\n" + db, "services:\n  web:\n    depends_on: ~\n", true},
		{"logging.driver, the base has it", svc + "    logging: {driver: json-file, options: {a: \"1\"}}\n", "services:\n  web:\n    logging: {driver: ~}\n", true},
		{"logging.options, the base has it", svc + "    logging: {driver: json-file, options: {a: \"1\"}}\n", "services:\n  web:\n    logging: {options: ~}\n", false},
		{"healthcheck.test, the base has it", svc + "    healthcheck: {test: [CMD, \"true\"]}\n", "services:\n  web:\n    healthcheck: {test: ~}\n", true},
		{"healthcheck.interval, the base has it", svc + "    healthcheck: {test: [CMD, \"true\"], interval: 1s}\n", "services:\n  web:\n    healthcheck: {interval: ~}\n", false},
		{"ulimits.nofile, the base has it", svc + "    ulimits: {nofile: 5, nproc: 6}\n", "services:\n  web:\n    ulimits: {nofile: ~}\n", true},
		{"ulimits.nproc, the base has it as a pair", svc + "    ulimits: {nproc: {soft: 1, hard: 2}}\n", "services:\n  web:\n    ulimits: {nproc: ~}\n", true},
		{"ulimits as a whole, the base has it", svc + "    ulimits: {nofile: 5}\n", "services:\n  web:\n    ulimits: ~\n", false},
		{"healthcheck as a whole, the base has it", svc + "    healthcheck: {test: [CMD, \"true\"]}\n", "services:\n  web:\n    healthcheck: ~\n", false},
		{"a key of another service the base has", svc + "    dns: [1.1.1.1]\n  other:\n    image: alpine\n", "services:\n  other:\n    dns: ~\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			base, over := filepath.Join(dir, "base.yml"), filepath.Join(dir, "over.yml")
			for p, b := range map[string]string{base: tc.base, over: tc.over} {
				if err := os.WriteFile(p, []byte(b), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, err := LoadFiles([]string{base, over}, nil)
			if tc.refused && (err == nil || !strings.Contains(err.Error(), "over.yml")) {
				t.Fatalf("want the refusal naming over.yml, got: %v", err)
			}
			if !tc.refused && err != nil {
				t.Fatalf("a key an earlier file gave a value stands: %v", err)
			}
			// The commands that take a project down read the same files and go on:
			// a value that is not the shape it takes is asked, not refused, there.
			// (Two rows are refused by the read of the merged document, which taking down
			// does not go round: healthcheck.test and build.dockerfile, #1582.)
			if hard := strings.Contains(tc.name, "healthcheck.test") || strings.Contains(tc.name, "a dockerfile"); hard && !strings.Contains(tc.name, "the base has it") {
				return
			}
			proj, err := LoadFilesEnvDirSoft([]string{base, over}, nil, "")
			if err != nil {
				t.Fatalf("taking down reads the files and goes on, got: %v", err)
			}
			// What is refused is named for the commands that take a project down, to
			// print and go on; a refusal dropped on the way is a take-down that says nothing.
			if fault := proj.CheckValueFaults(); tc.refused && (fault == nil || !strings.Contains(fault.Error(), "over.yml")) {
				t.Errorf("taking down should name the refusal of over.yml and go on, got fault: %v", fault)
			}
		})
	}
}

// A value an earlier file gave stands through the files after it, as far as a later file's
// nothing is concerned (#1588; measured, docker compose v5.5.1): the earlier files are read
// together, not the one just before. Three files: a value, then a file that does not name the
// key (or names the service not at all) or writes nothing after it, then nothing after it again.
func TestABareKeyInAThirdFileIsNotGivenWhereAnyEarlierFileGaveAValue(t *testing.T) {
	const svc = "services:\n  web:\n    image: alpine\n"
	for _, tc := range []struct {
		name, first, second, third string
		refused                    bool
	}{
		{"hostname: value, no key, nothing", svc + "    hostname: h\n", svc, "services:\n  web:\n    hostname: ~\n", false},
		{"dns: value, nothing, nothing", svc + "    dns: [1.1.1.1]\n", "services:\n  web:\n    dns: ~\n", "services:\n  web:\n    dns: ~\n", false},
		{"deploy.mode: value, no service, nothing", svc + "    deploy: {mode: global}\n", "services:\n  other:\n    image: alpine\n", "services:\n  web:\n    deploy: {mode: ~}\n", false},
		{"hostname: no value anywhere", svc, svc, "services:\n  web:\n    hostname: ~\n", true},
		{"hostname: nothing in the second, none in the first", svc, "services:\n  web:\n    hostname: ~\n", "services:\n  web:\n    hostname: ~\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var files []string
			for i, b := range []string{tc.first, tc.second, tc.third} {
				p := filepath.Join(dir, fmt.Sprintf("f%d.yml", i+1))
				if err := os.WriteFile(p, []byte(b), 0o644); err != nil {
					t.Fatal(err)
				}
				files = append(files, p)
			}
			_, err := LoadFiles(files, nil)
			if tc.refused && err == nil {
				t.Fatalf("want the refusal, none was given")
			}
			if !tc.refused && err != nil {
				t.Fatalf("a value an earlier file gave stands: %v", err)
			}
		})
	}
}
