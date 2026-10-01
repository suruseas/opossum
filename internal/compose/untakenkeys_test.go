package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// In a file that is only extended from, a service docker compose does not take is asked nothing
// of these keys (measured, v5.5.1, `config -q`, each at every place the schema gives it and with
// eight kinds of value: taken, the schema refuses what it does not take; not taken, nothing is
// refused). Each row is a value of the wrong kind for its key — refused in the service taken, read
// in one that is not.
//
// The keys are written out here rather than read from readAsItIsWhereNotTaken: a key taken out of
// the map would take its own subtest with it, and a gap would go unnoticed.
func TestWhatDockerComposeAsksNothingOfInAServiceItDoesNotTake(t *testing.T) {
	for _, tc := range []struct{ key, wrong string }{
		{"attach", `1`},
		{"blkio_config", `1`},
		{"cgroup", `1`},
		{"cgroup_parent", `1`},
		{"container_name", `1`},
		{"cpuset", `1`},
		{"credential_spec", `1`},
		{"device_cgroup_rules", `1`},
		{"domainname", `1`},
		{"external_links", `1`},
		{"extra_hosts", `1`},
		{"hostname", `1`},
		{"ipc", `1`},
		{"isolation", `1`},
		{"logging", `1`},
		{"mem_reservation", `[a]`},
		{"mem_swappiness", `[a]`},
		{"memswap_limit", `[a]`},
		{"pid", `1`},
		{"post_start", `1`},
		{"pre_start", `1`},
		{"pre_stop", `1`},
		{"provider", `1`},
		{"pull_policy", `1`},
		{"pull_refresh_after", `1`},
		{"runtime", `1`},
		{"security_opt", `1`},
		{"stop_grace_period", `1`},
		{"stop_signal", `1`},
		{"storage_opt", `1`},
		{"use_api_socket", `1`},
		{"userns_mode", `1`},
		{"uts", `1`},
		// A key the loader also reads is refused there for some values (its decode answers before
		// this does), and read for the rest: shm_size as a list or a mapping is read, as a null it is not.
		{"shm_size", `[1]`},
	} {
		t.Run(tc.key, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, body string) string {
				p := filepath.Join(dir, name)
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
				return p
			}
			line := "    " + tc.key + ": " + tc.wrong + "\n"
			// Taken: the named service itself.
			write("base.yaml", "services:\n  ok:\n    image: alpine\n"+line)
			main := write("compose.yaml", "services:\n  web:\n    extends: {file: base.yaml, service: ok}\n")
			if _, err := Load(main); err == nil {
				t.Errorf("%s: %s in the service that is taken should be refused", tc.key, tc.wrong)
			}
			// Not taken: another service of the same file.
			write("base.yaml", "services:\n  ok:\n    image: alpine\n  other:\n    image: alpine\n"+line)
			if _, err := Load(main); err != nil {
				t.Errorf("%s: %s in a service that is not taken is read by docker compose, and it was refused: %v", tc.key, tc.wrong, err)
			}
		})
	}
}

// The keys docker compose reads into a type before it knows whether a service is taken are asked
// of a service that is not (measured, v5.5.1: each refused there, with the value below). A key put
// among those it is asked nothing of by mistake is caught here where the loader's own decode does
// not refuse the value first (for `build`, `configs`, `depends_on`, `env_file`, `init`, `read_only`,
// `secrets`, `tty`, `ulimits` and `volumes` it does, so a key put among them wrongly changes nothing
// a file can show).
func TestWhatDockerComposeReadsIntoATypeIsStillAskedInAServiceItDoesNotTake(t *testing.T) {
	for _, tc := range []struct{ key, wrong string }{
		{"build", `1`},
		{"configs", `{a: 1}`},
		{"cpu_count", `abc`},
		{"cpu_percent", `abc`},
		{"cpu_period", `abc`},
		{"cpu_quota", `abc`},
		{"cpu_rt_period", `abc`},
		{"cpu_rt_runtime", `abc`},
		{"cpu_shares", `abc`},
		{"cpus", `abc`},
		{"depends_on", `1`},
		{"devices", `{a: 1}`},
		{"env_file", `1`},
		{"gpus", `1`},
		{"init", `abc`},
		{"label_file", `{a: 1}`},
		{"oom_kill_disable", `abc`},
		{"oom_score_adj", `abc`},
		{"pids_limit", `abc`},
		{"ports", `1`},
		{"privileged", `abc`},
		{"read_only", `abc`},
		{"scale", `abc`},
		{"secrets", `{a: 1}`},
		{"stdin_open", `abc`},
		{"tty", `abc`},
		{"ulimits", `[a]`},
		{"volumes", `{a: 1}`},
	} {
		t.Run(tc.key, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "base.yaml"),
				[]byte("services:\n  ok:\n    image: alpine\n  other:\n    image: alpine\n    "+tc.key+": "+tc.wrong+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			main := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(main, []byte("services:\n  web:\n    extends: {file: base.yaml, service: ok}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(main); err == nil {
				t.Errorf("%s: %s in a service that is not taken is refused by docker compose, and it was read", tc.key, tc.wrong)
			}
		})
	}
}
