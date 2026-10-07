package compose

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// A key with nothing after it (`user: ~`, `image:`) in a service of a file that is only extended from, and that nothing takes,
// is read by docker compose: the file is checked as the model of the service taken, and the others are not asked. All
// but six keys of the schema pass there (measured, v5.5.1: every key of a service, with docker compose config -q); the six that are
// refused there are refused as in a service taken. In the service taken itself, the key is refused whichever it is (a control).
func TestAKeyWithNothingAfterItInAServiceNothingTakesIsRead(t *testing.T) {
	var spec struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(serviceSpecJSON, &spec); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(spec.Properties))
	for k := range spec.Properties {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) < 90 {
		t.Fatalf("the schema names %d keys of a service, fewer than the 93 measured: the sweep would not cover them", len(keys))
	}
	refusedThere := map[string]bool{"build": true, "depends_on": true, "env_file": true, "gpus": true, "ports": true}
	load := func(t *testing.T, key, taking string) error {
		dir := t.TempDir()
		image := "    image: alpine\n"
		if key == "image" {
			image = ""
		}
		write := func(name, body string) {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("base.yaml", "services:\n  ok:\n    image: alpine\n  other:\n"+image+"    "+key+": ~\n")
		write("compose.yaml", "services:\n  web:\n    extends: {file: base.yaml, service: "+taking+"}\n")
		_, err := Load(filepath.Join(dir, "compose.yaml"))
		return err
	}
	for _, key := range keys {
		if key == "extends" {
			continue // docker compose refuses it there; opossum reads it, as it did (a known difference, left alone)
		}
		t.Run(key, func(t *testing.T) {
			if err := load(t, key, "ok"); (err != nil) != refusedThere[key] {
				t.Errorf("%s with nothing after it in a service nothing takes: refused = %v, want %v (%v)", key, err != nil, refusedThere[key], err)
			}
		})
	}
	// Written other ways (measured, v5.5.1): through an alias, a merge key or a list of them, or the service itself an alias, it is read like the key
	// itself, and a merge key with nothing after it is refused.
	forms := func(t *testing.T, prelude, other string) error {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "base.yaml"), []byte(prelude+"services:\n  ok:\n    image: alpine\n"+other), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  web:\n    extends: {file: base.yaml, service: ok}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(filepath.Join(dir, "compose.yaml"))
		return err
	}
	for _, tc := range []struct {
		name, prelude, other string
		refused              bool
	}{
		{"through an alias", "x-n: &n ~\n", "  other:\n    image: a\n    user: *n\n", false},
		{"brought in by a merge key", "x-a: &a {user: ~, restart: ~}\n", "  other:\n    <<: *a\n    image: alpine\n", false},
		{"brought in by a list of merge keys", "x-a: &a {user: ~}\n", "  other:\n    <<: [*a]\n    image: alpine\n", false},
		{"the service is an alias", "x-a: &a {image: alpine, user: ~}\n", "  other: *a\n", false},
		{"a merge key with nothing after it", "", "  other:\n    image: a\n    <<: ~\n", true},
		{"a key docker compose refuses there, brought in by a merge key", "x-a: &a {ports: ~}\n", "  other:\n    <<: *a\n    image: alpine\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := forms(t, tc.prelude, tc.other); (err != nil) != tc.refused {
				t.Errorf("refused = %v, want %v (%v)", err != nil, tc.refused, err)
			}
		})
	}
	// The service taken is not stripped: a key with nothing after it that stays is refused, and `models` is refused even where the extender writes over it.
	t.Run("the service taken still refuses it where it stays", func(t *testing.T) {
		for _, key := range []string{"user", "image", "restart", "labels", "ports"} {
			if err := load(t, key, "other"); err == nil {
				t.Errorf("%s with nothing after it in the service taken is read", key)
			}
		}
	})
	t.Run("the service taken refuses a models of nothing even where the extender writes over it", func(t *testing.T) {
		dir := t.TempDir()
		for name, body := range map[string]string{
			"base.yaml":    "services:\n  other:\n    image: alpine\n    models: ~\n",
			"compose.yaml": "services:\n  web:\n    extends: {file: base.yaml, service: other}\n    models: [m]\nmodels:\n  m: {model: x}\n",
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := Load(filepath.Join(dir, "compose.yaml")); err == nil {
			t.Error("a `models: ~` of the service taken, written over by the extender, is read (docker compose v5.5.1 refuses it, and it is not stripped here)")
		}
	})
}
