package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// docker compose refuses a key it does not take at every level of a file
// (`services.web additional properties 'foo' not allowed`, measured on
// v5.5.0 at each of these places), and allows an `x-` key anywhere. A
// typo used to be read past and listed among the ignored fields, so the
// service started without its variables and the mistake surfaced later.
func TestUnknownKeysAreRefusedAsDockerComposeRefusesThem(t *testing.T) {
	const svc = "services:\n  web:\n    image: a\n"
	for _, tc := range []struct{ name, body, want string }{
		{"a service key (a typo)", svc + "    enviroment: {A: 1}\n", `service "web": "enviroment" is not a key docker compose takes`},
		{"a key under build", svc + "    build: {context: ., foo: 1}\n", `service "web": build: "foo" is not a key docker compose takes`},
		{"a key under healthcheck", svc + "    healthcheck: {test: [\"CMD\", \"true\"], foo: 1}\n", `service "web": healthcheck: "foo" is not a key`},
		{"a key under deploy", svc + "    deploy: {foo: 1}\n", `service "web": deploy: "foo" is not a key`},
		{"a key under deploy.resources", svc + "    deploy: {resources: {foo: 1}}\n", `service "web": deploy.resources: "foo" is not a key`},
		{"a key under deploy.resources.limits", svc + "    deploy: {resources: {limits: {foo: 1}}}\n", `service "web": deploy.resources.limits: "foo" is not a key`},
		{"a key in a watch rule", svc + "    develop: {watch: [{action: sync, path: ., target: /app, foo: 1}]}\n", `service "web": develop.watch entry 1: "foo" is not a key`},
		{"a key in a long-form port", svc + "    ports: [{target: 80, published: 8080, foo: 1}]\n", `service "web": ports entry 1: "foo" is not a key`},
		{"a key in a long-form mount", svc + "    volumes: [{type: volume, source: d, target: /d, foo: 1}]\n", `service "web": volumes entry 1: "foo" is not a key`},
		{"a key in a mount's volume options", svc + "    volumes: [{type: volume, source: d, target: /d, volume: {foo: 1}}]\nvolumes:\n  d:\n", `service "web": volumes entry 1.volume: "foo" is not a key`},
		{"a key in a long-form secret", svc + "    secrets: [{source: s, foo: 1}]\nsecrets:\n  s: {file: ./s}\n", `service "web": secrets entry 1: "foo" is not a key`},
		{"a key in a long-form env_file", svc + "    env_file: [{path: .env, foo: 1}]\n", `service "web": env_file entry 1: "foo" is not a key`},
		{"a key in a dependency's mapping", svc + "    depends_on: {db: {condition: service_started, foo: 1}}\n  db:\n    image: b\n", `service "web": depends_on.db: "foo" is not a key`},
		{"a key in a service's network entry (a typo)", svc + "    networks: {back: {ipv4_adress: 10.0.0.9}}\nnetworks:\n  back: {}\n", `service "web": networks.back: "ipv4_adress" is not a key`},
		{"a top-level key (a typo)", "servcies:\n  web:\n    image: a\n", `"servcies" is not a top-level key docker compose takes (line 1)`},
		{"a key in a volume declaration", svc + "volumes:\n  data:\n    foo: 1\n", `volumes.data: "foo" is not a key docker compose takes (line 6)`},
		{"a key in a network declaration", svc + "networks:\n  back:\n    foo: 1\n", `networks.back: "foo" is not a key`},
		{"a key in a secret declaration", svc + "secrets:\n  s:\n    file: ./s\n    foo: 1\n", `secrets.s: "foo" is not a key`},
		{"a key in a config declaration", svc + "configs:\n  c:\n    file: ./c\n    foo: 1\n", `configs.c: "foo" is not a key`},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			_, err := Load(writeTemp(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want an error containing %q, got: %v", tc.want, err)
			}
		})
	}
}

// A key docker compose takes that opossum does not act on is still read
// past and listed among the ignored fields — refusing it would refuse a
// file docker compose runs. And an `x-` key is a note, taken anywhere and
// listed nowhere.
func TestKeysDockerComposeTakesAreListedNotRefused(t *testing.T) {
	body := "x-note: 1\nservices:\n  web:\n    image: a\n    x-note: 1\n    sysctls: {net.core.somaxconn: 1024}\n" +
		"    build: {context: ., labels: {a: b}, x-note: 1}\n" +
		"    deploy: {replicas: 2, resources: {limits: {pids: 10}}}\n" +
		"    ports: [{target: 80, published: 8080, mode: host, x-note: 1}]\n" +
		"    volumes: [{type: volume, source: d, target: /d, volume: {subpath: x}}]\n" +
		"    depends_on: {db: {condition: service_started, restart: true, x-note: 1}}\n" +
		"    networks: {back: {aliases: [w], x-note: 1}}\n" +
		"  db:\n    image: b\n" +
		"volumes:\n  d:\n    driver: local\n    x-note: 1\n" +
		"networks:\n  back:\n    ipam: {driver: default, config: [{subnet: 10.0.0.0/24}]}\n" +
		"secrets:\n  s:\n    file: ./s\n    labels: {a: b}\n" +
		"configs:\n  c:\n    file: ./c\n    labels: {a: b}\n" +
		"models:\n  m: {model: ai/x}\n"
	p, err := Load(writeTemp(t, body))
	if err != nil {
		t.Fatalf("a file docker compose takes must load, got: %v", err)
	}
	got := strings.Join(p.Services["web"].Unsupported, ",")
	for _, want := range []string{"sysctls", "build.labels", "deploy.replicas", "deploy.resources.limits.pids", "ports entry 1.mode", "volumes entry 1.volume.subpath", "depends_on.db.restart", "networks.back.aliases"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q must still be listed among the ignored fields, got %q", want, got)
		}
	}
	if strings.Contains(got, "x-note") {
		t.Errorf("an x- key is a note, not an ignored field, got %q", got)
	}
	top := strings.Join(p.Unsupported, ",")
	for _, want := range []string{"models", "volumes.d.driver", "networks.back.ipam.driver", "secrets.s.labels", "configs.c.labels"} {
		if !strings.Contains(top, want) {
			t.Errorf("%q must still be listed among the top-level ignored fields, got %q", want, top)
		}
	}
	if strings.Contains(top, "x-note") {
		t.Errorf("a top-level x- key is a note, not an ignored field, got %q", top)
	}
}

// With several -f files each file is checked on its own, and the refusal
// names the file the key is in.
func TestUnknownKeyInALaterFileNamesThatFile(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	over := filepath.Join(dir, "over.yaml")
	if err := os.WriteFile(base, []byte("services:\n  web:\n    image: a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(over, []byte("services:\n  web:\n    enviroment: {A: 1}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFiles([]string{base, over}, nil)
	if err == nil || !strings.Contains(err.Error(), "over.yaml") || !strings.Contains(err.Error(), `"enviroment" is not a key`) {
		t.Errorf("want the refusal naming over.yaml, got: %v", err)
	}
}

// With two typos the refusal names the same one every time — the first in
// name order — and keeps the service's name: the loader finds the service
// to blame by decoding again and matching the error, so a refusal that
// depended on the map's order lost the name on the runs where the second
// decode found the other typo first. One row per walk that refuses.
func TestTwoUnknownKeysAreRefusedInNameOrderNamingTheService(t *testing.T) {
	const svc = "services:\n  web:\n    image: a\n"
	for _, tc := range []struct{ name, body, want string }{
		{"service keys", svc + "    prots: [\"80\"]\n    enviroment: {A: 1}\n", `service "web": "enviroment" is not a key`},
		{"keys under build", svc + "    build: {context: ., foo: 1, bar: 1}\n", `service "web": build: "bar" is not a key`},
		{"keys in a long-form port", svc + "    ports: [{target: 80, foo: 1, bar: 1}]\n", `service "web": ports entry 1: "bar" is not a key`},
		{"keys in a mount's volume options", svc + "    volumes: [{type: volume, source: d, target: /d, volume: {foo: 1, bar: 1}}]\nvolumes:\n  d:\n", `service "web": volumes entry 1.volume: "bar" is not a key`},
		{"keys in two dependencies' mappings", svc + "    depends_on: {db: {condition: service_started, foo: 1}, cache: {condition: service_started, bar: 1}}\n  db:\n    image: b\n  cache:\n    image: c\n", `service "web": depends_on.cache: "bar" is not a key`},
		{"keys in a watch rule", svc + "    develop: {watch: [{action: sync, path: ., target: /app, foo: 1, bar: 1}]}\n", `service "web": develop.watch entry 1: "bar" is not a key`},
		{"keys in two network entries", svc + "    networks: {front: {foo: 1}, back: {bar: 1}}\nnetworks:\n  front: {}\n  back: {}\n", `service "web": networks.back: "bar" is not a key`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < 20; i++ {
				_, err := Load(writeTemp(t, tc.body))
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("run %d: want the first typo in name order, named with the service (%q), got: %v", i, tc.want, err)
				}
			}
		})
	}
}

// The places a refusal can hide from a test that looks only at the first
// of everything: the second declaration, a typo that is not the
// declaration's first key (the line is the key's, not the declaration's),
// a key that begins with `x` but is not an `x-` note, and a top-level typo
// in a later file.
func TestUnknownKeysBeyondTheFirstOfEverything(t *testing.T) {
	const svc = "services:\n  web:\n    image: a\n"
	for _, tc := range []struct{ name, body, want string }{
		{"the second declaration", svc + "volumes:\n  a: {}\n  b:\n    foo: 1\n", `volumes.b: "foo" is not a key docker compose takes (line 7)`},
		{"a typo after the declaration's first key", svc + "volumes:\n  a:\n    driver: local\n    foo: 1\n", `volumes.a: "foo" is not a key docker compose takes (line 7)`},
		{"a key beginning with x but not x-", svc + "    xfoo: 1\n", `service "web": "xfoo" is not a key`},
		{"a top-level typo after another key", svc + "volumes: {}\nservcies: {}\n", `"servcies" is not a top-level key docker compose takes (line 5)`},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			_, err := Load(writeTemp(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want an error containing %q, got: %v", tc.want, err)
			}
		})
	}
	t.Run("a top-level typo in a later file names that file", func(t *testing.T) {
		dir := t.TempDir()
		base := filepath.Join(dir, "base.yaml")
		over := filepath.Join(dir, "over.yaml")
		if err := os.WriteFile(base, []byte("services:\n  web:\n    image: a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(over, []byte("servcies:\n  web:\n    image: b\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadFiles([]string{base, over}, nil)
		if err == nil || !strings.Contains(err.Error(), "over.yaml") || !strings.Contains(err.Error(), `"servcies" is not a top-level key`) {
			t.Errorf("want the refusal naming over.yaml, got: %v", err)
		}
	})
}
