package compose

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A date written without quotes (`2024-01-01`, `2024-01-01T10:00:00Z`) is refused by docker compose for 31 keys of a service it takes (28 here, see datesRefused), whichever way the service is taken — in
// a file read on its own, written by a second file over a service of the first, or by the extender of an extends, or in the file an extends takes it from — and is read by a
// service nothing takes, and when it is quoted (measured, v5.5.1, `config -q`, every key of the service schema with the date; #1963). The keys it reads (`command`,
// `entrypoint`, `mem_limit`, `shm_size`) are not in the table, and are left as they were.
func TestADateIsRefusedForTheKeysDockerComposeRefusesItForInAServiceThatIsTaken(t *testing.T) {
	keys := make([]string, 0, len(datesRefused))
	for k := range datesRefused {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) != 28 {
		t.Fatalf("the table has %d keys, want the 28 docker compose refuses a date for", len(keys))
	}
	load := func(t *testing.T, files map[string]string, order ...string) error {
		t.Helper()
		dir := t.TempDir()
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		paths := make([]string, len(order))
		for i, f := range order {
			paths[i] = filepath.Join(dir, f)
		}
		_, err := LoadFiles(paths, nil)
		return err
	}
	for _, k := range keys {
		t.Run(k, func(t *testing.T) {
			if err := load(t, map[string]string{"compose.yaml": "services:\n  web:\n    image: wi\n    " + k + ": 2024-01-01\n"}, "compose.yaml"); err == nil {
				t.Errorf("a file read on its own: a date for %s is read", k)
			}
			if err := load(t, map[string]string{"compose.yaml": "services:\n  web:\n    image: wi\n    " + k + ": 2024-01-01T10:00:00Z\n"}, "compose.yaml"); err == nil {
				t.Errorf("a file read on its own: a date and a time for %s is read", k)
			}
			if err := load(t, map[string]string{"compose.yaml": "services:\n  web:\n    image: wi\n", "o.yaml": "services:\n  web:\n    " + k + ": 2024-01-01\n"}, "compose.yaml", "o.yaml"); err == nil {
				t.Errorf("a second file: a date for %s is read", k)
			}
			if err := load(t, map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: y}\n    " + k + ": 2024-01-01\n", "base.yaml": "services:\n  y:\n    image: yi\n"}, "compose.yaml"); err == nil {
				t.Errorf("the extender: a date for %s is read", k)
			}
			if err := load(t, map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: y}\n", "base.yaml": "services:\n  y:\n    image: yi\n    " + k + ": 2024-01-01\n"}, "compose.yaml"); err == nil {
				t.Errorf("the file an extends takes it from: a date for %s is read", k)
			}
			if err := load(t, map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: x}\n", "base.yaml": "services:\n  x:\n    image: xi\n  y:\n    image: yi\n    " + k + ": 2024-01-01\n"}, "compose.yaml"); err != nil {
				t.Errorf("a service nothing takes: a date for %s is refused: %v", k, err)
			}
		})
	}
}

// The keys outside the table where opossum and docker compose agreed on a date are not made different (measured, v5.5.1, the same sweep): `user`, `restart`, `working_dir`,
// `init`, `tty`, `read_only`, `platform` and `mac_address` refuse it; `mem_reservation`, `mem_swappiness`, `memswap_limit` and `pull_refresh_after` read it.
func TestADateForTheKeysOutsideTheTableIsAsItWas(t *testing.T) {
	for _, tc := range []struct {
		key     string
		refused bool
	}{
		{"user", true}, {"restart", true}, {"working_dir", true}, {"init", true}, {"tty", true}, {"read_only", true}, {"platform", true}, {"mac_address", true},
		{"mem_reservation", false}, {"mem_swappiness", false}, {"memswap_limit", false}, {"pull_refresh_after", false},
	} {
		t.Run(tc.key, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  web:\n    image: wi\n    "+tc.key+": 2024-01-01\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadFiles([]string{filepath.Join(dir, "compose.yaml")}, nil)
			if (err != nil) != tc.refused {
				t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
			}
		})
	}
}

// docker compose asks it of the merged project, so a date that is written over or taken out by a later file or by the extender is read, and one a later file writes where the
// earlier ones gave a word is refused (measured, v5.5.1: rc 0 for the first four, rc 1 for the rest, each for `hostname`, `cpu_count`, `privileged` and `scale`).
func TestADateIsAskedOfTheMergedProject(t *testing.T) {
	for _, k := range []string{"hostname", "cpu_count", "privileged", "scale"} {
		word := "\"x\""
		if k != "hostname" {
			word = map[string]string{"cpu_count": "2", "privileged": "true", "scale": "2"}[k]
		}
		for _, tc := range []struct {
			name    string
			files   map[string]string
			order   []string
			refused bool
		}{
			{"a second file writes a word over a date", map[string]string{"compose.yaml": "services:\n  web:\n    image: wi\n    " + k + ": 2024-01-01\n", "o.yaml": "services:\n  web:\n    " + k + ": " + word + "\n"}, []string{"compose.yaml", "o.yaml"}, false},
			{"a second file resets a date", map[string]string{"compose.yaml": "services:\n  web:\n    image: wi\n    " + k + ": 2024-01-01\n", "o.yaml": "services:\n  web:\n    " + k + ": !reset null\n"}, []string{"compose.yaml", "o.yaml"}, false},
			{"a second file overrides a date", map[string]string{"compose.yaml": "services:\n  web:\n    image: wi\n    " + k + ": 2024-01-01\n", "o.yaml": "services:\n  web:\n    " + k + ": !override " + word + "\n"}, []string{"compose.yaml", "o.yaml"}, false},
			{"the extender writes a word over a date the extended file has", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: y}\n    " + k + ": " + word + "\n", "base.yaml": "services:\n  y:\n    image: yi\n    " + k + ": 2024-01-01\n"}, []string{"compose.yaml"}, false},
			{"the extender resets a date the extended file has", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: y}\n    " + k + ": !reset null\n", "base.yaml": "services:\n  y:\n    image: yi\n    " + k + ": 2024-01-01\n"}, []string{"compose.yaml"}, false},
			{"a second file writes a date where the first gave a word", map[string]string{"compose.yaml": "services:\n  web:\n    image: wi\n    " + k + ": " + word + "\n", "o.yaml": "services:\n  web:\n    " + k + ": 2024-01-01\n"}, []string{"compose.yaml", "o.yaml"}, true},
			{"a word is read", map[string]string{"compose.yaml": "services:\n  web:\n    image: wi\n    " + k + ": " + word + "\n"}, []string{"compose.yaml"}, false},
		} {
			t.Run(k+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				for name, body := range tc.files {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				paths := make([]string, len(tc.order))
				for i, f := range tc.order {
					paths[i] = filepath.Join(dir, f)
				}
				if _, err := LoadFiles(paths, nil); (err != nil) != tc.refused {
					t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
				}
			})
		}
	}
}

// `pull_policy` is an enum, which docker compose asks of each file it reads: a date in a file read on its own is refused whatever a later file writes over it or takes it out
// for, where the extender of an extends writes over the date of the file it takes the service from first, and is read (measured, v5.5.1, #1982).
func TestADateForPullPolicyIsAskedOfEachFile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   map[string]string
		order   []string
		refused bool
	}{
		{"a file on its own", map[string]string{"a.yaml": "services:\n  web:\n    image: wi\n    pull_policy: 2024-01-01\n"}, []string{"a.yaml"}, true},
		{"a later file writes a word over it", map[string]string{"a.yaml": "services:\n  web:\n    image: wi\n    pull_policy: 2024-01-01\n", "b.yaml": "services:\n  web:\n    pull_policy: always\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"a later file overrides it", map[string]string{"a.yaml": "services:\n  web:\n    image: wi\n    pull_policy: 2024-01-01\n", "b.yaml": "services:\n  web:\n    pull_policy: !override always\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"a later file takes it out", map[string]string{"a.yaml": "services:\n  web:\n    image: wi\n    pull_policy: 2024-01-01\n", "b.yaml": "services:\n  web:\n    pull_policy: !reset null\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"the first file gives a word and a later one a date", map[string]string{"a.yaml": "services:\n  web:\n    image: wi\n    pull_policy: always\n", "b.yaml": "services:\n  web:\n    pull_policy: 2024-01-01\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"the extender writes a word over the date of the file it extends", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: y}\n    pull_policy: always\n", "base.yaml": "services:\n  y:\n    image: yi\n    pull_policy: 2024-01-01\n"}, []string{"compose.yaml"}, false},
		{"the extender keeps the date of the file it extends", map[string]string{"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: y}\n", "base.yaml": "services:\n  y:\n    image: yi\n    pull_policy: 2024-01-01\n"}, []string{"compose.yaml"}, true},
		{"a word is read", map[string]string{"a.yaml": "services:\n  web:\n    image: wi\n    pull_policy: always\n"}, []string{"a.yaml"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			paths := make([]string, len(tc.order))
			for i, f := range tc.order {
				paths[i] = filepath.Join(dir, f)
			}
			if _, err := LoadFiles(paths, nil); (err != nil) != tc.refused {
				t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
			}
		})
	}
}

// The refusal of a date for `pull_policy` does not tell to quote it: a quoted date is no pull policy either.
func TestTheRefusalOfADateForPullPolicyDoesNotSayToQuoteIt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("services:\n  web:\n    image: wi\n    pull_policy: 2024-01-01\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFiles([]string{filepath.Join(dir, "a.yaml")}, nil)
	if err == nil || strings.Contains(err.Error(), "quotes") || !strings.Contains(err.Error(), "no pull policy") {
		t.Errorf("want a refusal that names the policies and does not say to quote, got: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.yaml"), []byte("services:\n  web:\n    extends: {file: base.yaml, service: y}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "base.yaml"), []byte("services:\n  y:\n    image: yi\n    pull_policy: 2024-01-01\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadFiles([]string{filepath.Join(dir, "b.yaml")}, nil)
	if err == nil || strings.Contains(err.Error(), "quotes") || !strings.Contains(err.Error(), "no pull policy") {
		t.Errorf("the date an extends brings: want the same refusal, got: %v", err)
	}
}
