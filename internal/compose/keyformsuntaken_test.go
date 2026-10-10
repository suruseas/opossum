package compose

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A key of a service comes to be read the same way whether it is written in the service, brought in by a merge key (`<<: *o`), written in a service that is an alias
// (`other: *o`), given as an alias of its own (`key: *v`) or written with a key that is an alias (`*k : v`): docker compose reads the file once, with the aliases and the merge keys taken in (measured, v5.5.1,
// `config -q`: every key of the service schema with a number, a word, a list and a mapping, in a service taken and in one nothing takes — 2976 files, none of which
// docker compose reads differently by the way the key is written; #1936, #1934). So the answer for a way of writing is the answer for the key written in the service.
func TestAKeyIsReadTheSameWhateverWayItIsWritten(t *testing.T) {
	var spec struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(serviceSpecJSON, &spec); err != nil {
		t.Fatal(err)
	}
	if len(spec.Properties) < 90 {
		t.Fatalf("the schema names %d keys of a service, fewer than the 93 measured", len(spec.Properties))
	}
	shapes := map[string]string{"a number": "1", "a word": "abc", "a list": "[1]", "a mapping": "{a: 1}"}
	base := func(key, value, form string) string {
		image := "    image: y\n"
		if key == "image" {
			image = ""
		}
		switch form {
		case "merge key":
			return "x-o: &o {" + key + ": " + value + "}\nservices:\n  s: {image: x}\n  other:\n    <<: *o\n" + image
		case "service alias":
			inline := "image: y, "
			if key == "image" {
				inline = ""
			}
			return "x-o: &o {" + inline + key + ": " + value + "}\nservices:\n  s: {image: x}\n  other: *o\n"
		case "key alias":
			return "x-k: &k " + key + "\nservices:\n  s: {image: x}\n  other:\n" + image + "    *k : " + value + "\n"
		case "value alias":
			return "x-v: &v " + value + "\nservices:\n  s: {image: x}\n  other:\n" + image + "    " + key + ": *v\n"
		}
		return "services:\n  s: {image: x}\n  other:\n" + image + "    " + key + ": " + value + "\n"
	}
	refused := func(t *testing.T, key, value, form, extends string) bool {
		dir := t.TempDir()
		for name, body := range map[string]string{
			"base.yaml":    base(key, value, form),
			"compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: " + extends + "}\n",
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		_, err := Load(filepath.Join(dir, "compose.yaml"))
		return err != nil
	}
	for key := range spec.Properties {
		if key == "extends" || key == "pid" {
			continue // `extends` is resolved before the aliases are; `pid` is one docker compose panics on in a service taken
		}
		t.Run(key, func(t *testing.T) {
			for shape, value := range shapes {
				for _, role := range []struct{ name, extends string }{{"not taken", "s"}, {"taken", "other"}} {
					written := refused(t, key, value, "in the service", role.extends)
					for _, form := range []string{"merge key", "service alias", "value alias", "key alias"} {
						if got := refused(t, key, value, form, role.extends); got != written {
							t.Errorf("%s %s = %s, %s: refused = %v by a %s, and %v written in the service", key, "as", shape, role.name, got, form, written)
						}
					}
				}
			}
		})
	}
}

// What is kept of a key when the file is asked is read from the value it stands for, not from the alias that stands for it (measured, v5.5.1, `config -q`): the
// count of retries of a healthcheck is cast as the file is read, in a service taken or not; a long entry of `ports` whose `target` is a fraction is left in a service
// nothing takes.
func TestWhatIsKeptOfAKeyIsReadFromTheValueAnAliasStandsFor(t *testing.T) {
	for _, tc := range []struct {
		name, base, extends string
		refused             bool
	}{
		{"a healthcheck written in the service", "services:\n  s: {image: x}\n  other:\n    image: y\n    healthcheck: {retries: abc}\n", "s", true},
		{"a healthcheck that is an alias", "x-v: &v {retries: abc}\nservices:\n  s: {image: x}\n  other:\n    image: y\n    healthcheck: *v\n", "s", true},
		{"a healthcheck that is an alias, in the service taken", "x-v: &v {retries: abc}\nservices:\n  other:\n    image: y\n    healthcheck: *v\n", "other", true},
		{"a port with a fraction for a target, written in the service", "services:\n  s: {image: x}\n  other:\n    image: y\n    ports: [{target: 1.5, published: 80}]\n", "s", false},
		{"a port with a fraction for a target, in a list that is an alias", "x-v: &v [{target: 1.5, published: 80}]\nservices:\n  s: {image: x}\n  other:\n    image: y\n    ports: *v\n", "s", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range map[string]string{
				"base.yaml":    tc.base,
				"compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: " + tc.extends + "}\n",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Load(filepath.Join(dir, "compose.yaml")); (err != nil) != tc.refused {
				t.Errorf("refused = %v, want %v (%v)", err != nil, tc.refused, err)
			}
		})
	}
}

// A key written as an alias is read as the key it names where the value is one that is asked of the file only in some places: a word for a duration or a size, a number for
// `deploy`, written over by the extender in the service taken, and a long entry of `ports` in a service nothing takes (measured, v5.5.1: rc 0 for each, #1946).
func TestAKeyThatIsAnAliasIsReadAsTheKeyItNamesWhereTheValueIsAskedInSomePlacesOnly(t *testing.T) {
	for _, tc := range []struct {
		name, key, value, extends, over string
	}{
		{"a word for stop_grace_period, written over", "stop_grace_period", "abc", "other", "    stop_grace_period: 5s\n"},
		{"a word for shm_size, written over", "shm_size", "abc", "other", "    shm_size: 1g\n"},
		{"a number for deploy, written over", "deploy", "1", "other", "    deploy: {replicas: 2}\n"},
		{"a target that is a fraction in a long entry of ports", "ports", "[{target: 1.5, published: 80}]", "s", ""},
		{"a published port that is a word in a long entry of ports", "ports", "[{target: 80, published: abc}]", "s", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range map[string]string{
				"base.yaml":    "x-k: &k " + tc.key + "\nservices:\n  s: {image: x}\n  other:\n    image: y\n    *k : " + tc.value + "\n",
				"compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: " + tc.extends + "}\n" + tc.over,
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Load(filepath.Join(dir, "compose.yaml")); err != nil {
				t.Errorf("%s: %v", tc.name, err)
			}
		})
	}
}
