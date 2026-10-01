package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A key of `deploy`, `build`, `healthcheck`, a service's `networks` entry or its
// `depends_on` written with nothing after it is refused where the schema gives the key
// no null (#1583, #1592, #1598; measured, docker compose v5.5.1, `config -q`: 92 keys
// taken from the schema, one at a time in a single file — 28 of them refused here before,
// 91 now, the one left being the `additional_contexts` read as it was). The keys that take
// a null, and the two reads opossum keeps, are read.
func TestAKeyWithNothingAfterItIsRefusedWhereTheSchemaGivesItNoNull(t *testing.T) {
	const svc = "services:\n  web:\n    image: alpine\n"
	const db = "  db:\n    image: alpine\n"
	for _, tc := range []struct {
		name, body string
		refused    bool
	}{
		{"deploy.labels|services.web.deploy.labels", svc + "    deploy: {labels: ~}\n", true},
		{"deploy.update_config.parallelism|services.web.deploy.update_config.parallelism", svc + "    deploy: {update_config: {parallelism: ~}}\n", true},
		{"deploy.restart_policy|services.web.deploy.restart_policy", svc + "    deploy: {restart_policy: ~}\n", true},
		{"deploy.placement.constraints|services.web.deploy.placement.constraints", svc + "    deploy: {placement: {constraints: ~}}\n", true},
		{"deploy.endpoint_mode|services.web.deploy.endpoint_mode", svc + "    deploy: {endpoint_mode: ~}\n", true},
		{"build.network|services.web.build.network", svc + "    build: {context: ., network: ~}\n", true},
		{"build.no_cache|services.web.build.no_cache", svc + "    build: {context: ., no_cache: ~}\n", true},
		{"build.labels|services.web.build.labels", svc + "    build: {context: ., labels: ~}\n", true},
		{"build.extra_hosts, an entry|services.web.build.extra_hosts.a", svc + "    build: {context: ., extra_hosts: {a: ~}}\n", true},
		{"build.ulimits.nofile|services.web.build.ulimits.nofile", svc + "    build: {context: ., ulimits: {nofile: ~}}\n", true},
		{"build.additional_contexts, an entry|services.web.build.additional_contexts.a", svc + "    build: {context: ., additional_contexts: {a: ~}}\n", true},
		{"build.provenance|services.web.build.provenance", svc + "    build: {context: ., provenance: ~}\n", true},
		{"healthcheck.disable|services.web.healthcheck.disable", svc + "    healthcheck: {disable: ~}\n", true},
		{"healthcheck.start_interval|services.web.healthcheck.start_interval", svc + "    healthcheck: {start_interval: ~}\n", true},
		{"networks.n.aliases|services.web.networks.n.aliases", svc + "    networks: {n: {aliases: ~}}\nnetworks:\n  n: {}\n", true},
		{"networks.n.ipv4_address|services.web.networks.n.ipv4_address", svc + "    networks: {n: {ipv4_address: ~}}\nnetworks:\n  n: {}\n", true},
		{"networks.n.driver_opts, an entry|services.web.networks.n.driver_opts.a", svc + "    networks: {n: {driver_opts: {a: ~}}}\nnetworks:\n  n: {}\n", true},
		{"depends_on.db.restart|services.web.depends_on.db.restart", svc + "    depends_on: {db: {restart: ~}}\n" + db, true},
		// A name the exceptions are spelt with is only a name.
		{"a network named depends_on|services.web.networks.depends_on.aliases", svc + "    networks: {depends_on: {aliases: ~}}\nnetworks:\n  depends_on: {}\n", true},
		{"a service named depends_on, its restart|services.depends_on.depends_on.depends_on.restart", "services:\n  depends_on:\n    image: alpine\n    depends_on: {depends_on: {restart: ~}}\n", true},
		// Two keys with nothing after them: the first in the order of the names is the one said.
		{"deploy.endpoint_mode, of two|services.web.deploy.endpoint_mode", svc + "    deploy: {update_config: ~, labels: ~, endpoint_mode: ~, restart_policy: ~}\n", true},
		// What takes a null.
		{"a network entry with nothing in it", svc + "    networks: {n: ~}\nnetworks:\n  n: {}\n", false},
		{"deploy.labels, a label with nothing after it", svc + "    deploy: {labels: {a: ~}}\n", false},
		{"build.args, an argument with nothing after it", svc + "    build: {context: ., args: {A: ~}}\n", false},
		{"deploy with nothing in it", svc + "    deploy: ~\n", false},
		{"an x- key of a network named additional_contexts", svc + "    networks: {additional_contexts: {x-a: ~}}\nnetworks:\n  additional_contexts: {}\n", false},
		{"an x- key of a dependency named additional_contexts", svc + "    depends_on: {additional_contexts: {condition: service_started, x-a: ~}}\n  additional_contexts:\n    image: alpine\n", false},
		// The exception is where the node is (an entry of a build's additional_contexts), not how the path is spelt.
		{"an x- key of a network named x.build.additional_contexts", svc + "    networks: {x.build.additional_contexts: {x-a: ~}}\nnetworks:\n  x.build.additional_contexts: {}\n", false},
		{"an x- key of a dependency named a.build.additional_contexts", svc + "    depends_on: {a.build.additional_contexts: {condition: service_started, x-a: ~}}\n  a.build.additional_contexts:\n    image: alpine\n", false},
		{"an aliases of a network named x.build.additional_contexts, still refused|services.web.networks.x.build.additional_contexts.aliases", svc + "    networks: {x.build.additional_contexts: {aliases: ~}}\nnetworks:\n  x.build.additional_contexts: {}\n", true},
		{"an x- key of each block", svc + "    deploy: {x-a: ~}\n    build: {context: ., x-a: ~}\n    healthcheck: {x-a: ~}\n    networks: {n: {x-a: ~}}\nnetworks:\n  n: {}\n", false},
		// Read as they were, as docker compose's refusal is a known difference.
		{"a dependency with nothing after its name", svc + "    depends_on: {db: ~}\n" + db, false},
		{"a dependency with nothing after its name, in a service whose name has a dot", "services:\n  web.1:\n    image: alpine\n    depends_on: {db: ~}\n" + db, false},
		{"additional_contexts with nothing after it", svc + "    build: {context: ., additional_contexts: ~}\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(p)
			want := "services.web."
			if _, path, ok := strings.Cut(tc.name, "|"); ok {
				want = path
			}
			if tc.refused && (err == nil || !strings.Contains(err.Error(), want+" has nothing after it")) {
				t.Errorf("docker compose refuses this, want %q has nothing after it, got: %v", want, err)
			}
			if !tc.refused && err != nil {
				t.Errorf("docker compose reads this (or it is read as it was), and it was refused: %v", err)
			}
			// What a take-down reads is named and gone on past: an earlier opossum passed it on.
			proj, err := LoadFilesEnvDirSoft([]string{p}, nil, "")
			if err != nil {
				t.Fatalf("taking down reads the file and goes on, got: %v", err)
			}
			if fault := proj.CheckValueFaults(); tc.refused && fault == nil {
				t.Errorf("taking down should name the key with nothing after it and go on")
			}
		})
	}
}

// A file that is only extended from is not asked for these: docker compose merges the
// extending service over the one it takes before it asks, so a key with nothing after it that
// the extender (or a service of the chain) writes a value over is read, in the service taken as
// in the others (measured, v5.5.1: the same null refused where nothing writes over it).
func TestAKeyWithNothingAfterItInAFileOnlyExtendedFromIsReadWhereTheExtenderWritesOverIt(t *testing.T) {
	for _, tc := range []struct{ name, main, base string }{
		{"deploy.labels", "    deploy: {labels: {a: b}}\n", "    deploy: {labels: ~}\n"},
		{"healthcheck.disable", "    healthcheck: {disable: true}\n", "    healthcheck: {disable: ~}\n"},
		{"build.network", "    build: {context: ., network: host}\n", "    build: {context: ., network: ~}\n"},
		{"deploy.restart_policy.condition", "    deploy: {restart_policy: {condition: any}}\n", "    deploy: {restart_policy: {condition: ~}}\n"},
		{"depends_on.db.restart", "    depends_on: {db: {condition: service_started, restart: true}}\n", "    depends_on: {db: {restart: ~}}\n"},
		{"build.provenance, nothing written over it", "", "    build: {context: ., provenance: ~}\n"},
		{"not taken, deploy.labels", "", "    image: alpine\n  other:\n    image: alpine\n    deploy: {labels: ~}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			base := "services:\n  b:\n    image: alpine\n" + tc.base
			if strings.Contains(tc.base, "  other:") {
				base = "services:\n  b:\n" + tc.base
			}
			base += "  db:\n    image: alpine\n"
			if err := os.WriteFile(filepath.Join(dir, "base.yaml"), []byte(base), 0o644); err != nil {
				t.Fatal(err)
			}
			main := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(main, []byte("services:\n  web:\n    extends: {file: base.yaml, service: b}\n"+tc.main+"  db:\n    image: alpine\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(main); err != nil {
				t.Errorf("docker compose reads this, and it was refused: %v", err)
			}
		})
	}
}
