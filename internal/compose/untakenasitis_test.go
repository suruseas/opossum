package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// The keys docker compose asks nothing of in a service it does not take (readAsItIsWhereNotTaken), `develop`, `gpus`, `healthcheck` and the lists of mappings of `configs` and
// `secrets` are read there whatever a list or a mapping of them holds, what holds nothing too (measured, v5.5.1, `config -q`: each key below with a list of nothing, a mapping
// with a key of nothing and a list of a mapping with a key of nothing, in a service of an extended file that the extending service does not name; #1965). Each row is the
// values docker compose refuses. The forms where opossum answers otherwise than docker compose are the ones this change does not make: left out and named.
var nothingShapesInAService = map[string]string{"listnull": "[~]", "mapnull": "{a: ~}", "listmapnull": "[{a: ~}]"}

var knownDifferencesInAServiceNothingTakes = map[string]bool{
	"configs/listnull": true,
	"secrets/listnull": true,
}

func TestWhatHoldsNothingInTheKeysDockerComposeAsksNothingOfInAServiceNothingTakesIsRead(t *testing.T) {
	for _, tc := range []struct {
		key     string
		refused []string
	}{
		{"attach", []string{}},
		{"blkio_config", []string{}},
		{"cgroup", []string{}},
		{"cgroup_parent", []string{}},
		{"command", []string{}},
		{"configs", []string{"listnull", "mapnull"}},
		{"container_name", []string{}},
		{"cpuset", []string{}},
		{"credential_spec", []string{}},
		{"develop", []string{}},
		{"device_cgroup_rules", []string{}},
		{"domainname", []string{}},
		{"entrypoint", []string{}},
		{"external_links", []string{}},
		{"extra_hosts", []string{}},
		{"gpus", []string{"mapnull"}},
		{"group_add", []string{}},
		{"healthcheck", []string{}},
		{"hostname", []string{}},
		{"image", []string{}},
		{"ipc", []string{}},
		{"isolation", []string{}},
		{"logging", []string{}},
		{"mac_address", []string{}},
		{"mem_limit", []string{}},
		{"mem_reservation", []string{}},
		{"mem_swappiness", []string{}},
		{"memswap_limit", []string{}},
		{"network_mode", []string{}},
		{"pid", []string{}},
		{"platform", []string{}},
		{"post_start", []string{}},
		{"pre_start", []string{}},
		{"pre_stop", []string{}},
		{"provider", []string{}},
		{"pull_policy", []string{}},
		{"pull_refresh_after", []string{}},
		{"restart", []string{}},
		{"runtime", []string{}},
		{"secrets", []string{"listnull", "mapnull"}},
		{"security_opt", []string{}},
		{"shm_size", []string{}},
		{"stop_grace_period", []string{}},
		{"stop_signal", []string{}},
		{"storage_opt", []string{}},
		{"use_api_socket", []string{}},
		{"user", []string{}},
		{"userns_mode", []string{}},
		{"uts", []string{}},
		{"volumes_from", []string{}},
		{"working_dir", []string{}},
	} {
		t.Run(tc.key, func(t *testing.T) {
			for shape, value := range nothingShapesInAService {
				if knownDifferencesInAServiceNothingTakes[tc.key+"/"+shape] {
					continue
				}
				dir := t.TempDir()
				image := "    image: y\n"
				if tc.key == "image" {
					image = ""
				}
				for name, body := range map[string]string{
					"base.yaml":    "services:\n  s: {image: x}\n  other:\n" + image + "    " + tc.key + ": " + value + "\n",
					"compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: s}\n",
				} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				_, err := Load(filepath.Join(dir, "compose.yaml"))
				want := false
				for _, r := range tc.refused {
					want = want || r == shape
				}
				if (err != nil) != want {
					t.Errorf("not taken: %s = %s: refused = %v, want %v (%v)", tc.key, value, err != nil, want, err)
				}
			}
		})
	}
}

// What holds nothing is asked of the file of the service taken, though the extender writes the key over (measured, v5.5.1: rc 1): the early read is for a service nothing
// takes only.
func TestWhatHoldsNothingInTheServiceTakenIsAskedEvenWhereTheExtenderWritesOverIt(t *testing.T) {
	for _, tc := range []struct{ key, base, over string }{
		{"user", "[~]", "root"},
		{"user", "{a: ~}", "root"},
		{"group_add", "{a: ~}", "[a]"},
		{"working_dir", "{a: ~}", "/x"},
	} {
		t.Run(tc.key+" = "+tc.base, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range map[string]string{
				"base.yaml":    "services:\n  other:\n    image: y\n    " + tc.key + ": " + tc.base + "\n",
				"compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: other}\n    " + tc.key + ": " + tc.over + "\n",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Load(filepath.Join(dir, "compose.yaml")); err == nil {
				t.Errorf("%s: %s of the service taken, written over with %s, is read (docker compose refuses it)", tc.key, tc.base, tc.over)
			}
		})
	}
}

// The `retries` and the `disable` of a `healthcheck` are cast as the file is read, whether they are written in it or come by a merge key or are keys that are aliases, and the
// rest of it holds nothing that is asked in a service nothing takes (measured, v5.5.1: rc 1 for the first three, rc 0 for the others, #1965).
func TestAHealthcheckThatHoldsNothingKeepsItsCastsThroughAMergeKeyAndAnAliasedKey(t *testing.T) {
	for _, tc := range []struct {
		name, healthcheck string
		refused           bool
	}{
		{"a retries through a merge key beside a key of nothing", "{<<: {retries: abc}, a: ~}", true},
		{"a retries through an alias of a merge key", "{<<: *r, a: ~}", true},
		{"a disable through a list of merge keys", "{<<: [{disable: abc}], a: ~}", true},
		{"a retries that is an aliased key", "{*rk : abc, a: ~}", true},
		{"a disable that is an aliased key", "{*dk : abc, a: ~}", true},
		{"a merge key that brings nothing that is cast", "{<<: {interval: abc}, a: ~}", false},
		{"a key of nothing beside the ones that are not cast", "{interval: abc, a: ~}", false},
		{"a list that holds nothing", "[~]", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range map[string]string{
				"base.yaml":    "x-r: &r {retries: abc}\nx-rk: &rk retries\nx-dk: &dk disable\nservices:\n  s: {image: x}\n  other:\n    image: y\n    healthcheck: " + tc.healthcheck + "\n",
				"compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: s}\n",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Load(filepath.Join(dir, "compose.yaml")); (err != nil) != tc.refused {
				t.Errorf("healthcheck: %s: refused = %v, want %v (%v)", tc.healthcheck, err != nil, tc.refused, err)
			}
		})
	}
}
