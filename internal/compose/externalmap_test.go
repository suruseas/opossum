package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The older map form of `external` — `external: {name: x}` — is what a lot of
// docker-compose files still carry, and docker compose still reads it (with a
// deprecation notice; measured on v5.4.0, which canonicalizes it to `name: x`
// plus `external: true`). Reading it as "cannot unmarshal !!map into bool"
// pointed people at a type error instead of at their file. Both shapes now
// mean the same thing, on volumes and on networks alike, and — because a
// fixture that only ever says `true` cannot tell a reader of the value from a
// constant — one declaration says `false` on purpose.
func TestTheMapFormOfExternalMeansWhatDockerReadsIntoIt(t *testing.T) {
	p := writeProject(t, `
services:
  a:
    image: alpine:3
    volumes:
      - v:/data
    networks: [n, m]
volumes:
  v:
    external:
      name: foo
  same:
    name: agreed
    external:
      name: agreed
  quoted:
    external: "true"
  plain:
    external: false
networks:
  n:
    external:
      name: bar
  m:
    name: explicit
    external: true
`, "")
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for name, want := range map[string]VolumeDecl{
		"v":      {External: true, Name: "foo"},
		"same":   {External: true, Name: "agreed"}, // docker accepts the pair when they agree
		"quoted": {External: true},                 // a quoted "true" is a bool to docker too
		"plain":  {External: false},
	} {
		if got := proj.Volumes[name]; got != want {
			t.Errorf("volume %s = %+v, want %+v", name, got, want)
		}
	}
	for name, want := range map[string]NetworkDecl{
		"n": {External: true, Name: "bar"},
		"m": {External: true, Name: "explicit"},
	} {
		if got := proj.Networks[name]; got != want {
			t.Errorf("network %s = %+v, want %+v", name, got, want)
		}
	}
}

// Where docker compose refuses, so does this — a differing `name:` beside the
// map's name, and a key in the map that isn't `name` (a typo would otherwise
// turn into a nameless external volume). The refusal names the line.
func TestTheMapFormOfExternalRefusesWhatDockerRefuses(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		// Held with both names in their places: which one was `name:` and which
		// `external.name:` is the whole point of the sentence, and swapped they
		// would send the reader to fix the wrong key.
		"conflicting names": {"volumes:\n  v:\n    name: one\n    external:\n      name: other\n", `name "one" and external.name "other" conflict; only use name`},
		"an unknown key":    {"volumes:\n  v:\n    external:\n      name: x\n      extra: y\n", `unknown key "extra"`},
		"a network too":     {"networks:\n  n:\n    name: one\n    external:\n      name: two\n", `name "one" and external.name "two" conflict; only use name`},
		// The name is a string (docker compose: `external.name must be a
		// string`); the decode used to read `name: 42` as "42" and `name:`
		// as no name.
		"a name that is a number":                  {"networks:\n  n:\n    external:\n      name: 42\n", "line 7: external: name must be a string, got a number — quote it (`\"42\"`) if it is meant literally"},
		"a name that is a boolean":                 {"volumes:\n  v:\n    external:\n      name: true\n", "external: name must be a string, got true/false"},
		"a name that is a list":                    {"networks:\n  n:\n    external:\n      name: [a]\n", "line 7: external: name must be a string, got a list"},
		"a name with nothing after it":             {"networks:\n  n:\n    external:\n      name:\n", "line 7: external: name has nothing after it — write the name, or remove the key"},
		"a name through an alias, a number":        {"x-n: &n 42\nnetworks:\n  n:\n    external:\n      name: *n\n", "line 4: external: name must be a string, got a number"},
		"a name that is a mapping":                 {"networks:\n  n:\n    external:\n      name: {a: b}\n", "line 7: external: name must be a string, got a mapping"},
		"a name brought in by a merge key":         {"x-m: &m {name: 42}\nnetworks:\n  n:\n    external:\n      <<: *m\n", "line 4: external: name must be a string, got a number"},
		"an unknown key brought in by a merge key": {"x-m: &m {extra: y}\nnetworks:\n  n:\n    external:\n      name: x\n      <<: *m\n", `unknown key "extra"`},
		"the second of two merge keys":             {"x-a: &a {name: x}\nx-b: &b {name: [z]}\nnetworks:\n  n:\n    external:\n      <<: [*a, *b]\n", "external: name must be a string, got a list"},
	} {
		t.Run(name, func(t *testing.T) {
			p := writeProject(t, "services:\n  a:\n    image: alpine:3\n"+tc.body, "")
			_, err := Load(p)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "line ") {
				t.Errorf("should be refused by name with a line, got: %v", err)
			}
		})
	}
}

// With several -f files the map form is read the way docker compose reads
// it before merging — `external: true` with the name lifted to the
// declaration's `name:` — so a later file's `external: true` keeps the
// earlier file's name instead of replacing the whole map (#836; measured
// against docker compose v5.5.0). A later `external: false` keeps the name
// too, and a later `name:` wins, as there; a later map wins only where no
// earlier file named the declaration — where one did, and the names
// differ, docker compose refuses the pair as a conflict, and so does this.
func TestTheMapFormOfExternalSurvivesALaterFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	svc := "services:\n  a:\n    image: alpine:3\n    volumes: [data:/data, more:/more]\n    networks: [back]\n"
	// Two volumes in the map form: the lift must reach every declaration of
	// a kind, not just the first.
	baseMap := write("base-map.yml", svc+"volumes:\n  data:\n    external:\n      name: base-name\n  more:\n    external:\n      name: more-name\nnetworks:\n  back:\n    external:\n      name: base-net\n")
	baseBool := write("base-bool.yml", svc+"volumes:\n  data:\n    external: true\n  more:\n    external: true\nnetworks:\n  back:\n    external: true\n")
	baseBoolName := write("base-bool-name.yml", svc+"volumes:\n  data:\n    name: base-name\n    external: true\n  more:\n    external: true\nnetworks:\n  back:\n    name: base-net\n    external: true\n")
	// A map with no name, and a `name:` beside an empty one: the lift leaves
	// what it cannot read alone.
	baseEmpty := write("base-empty.yml", svc+"volumes:\n  data:\n    name: real\n    external: {}\n  more:\n    name: keep\n    external: {name: \"\"}\nnetworks:\n  back:\n    external: {}\n")
	for _, tc := range []struct {
		name, base, over           string
		external                   bool
		volName, moreName, netName string
	}{
		{"a later external: true keeps the map's name", baseMap, "volumes:\n  data:\n    external: true\n  more:\n    external: true\nnetworks:\n  back:\n    external: true\n", true, "base-name", "more-name", "base-net"},
		{"a later external: false keeps the name and drops external", baseMap, "volumes:\n  data:\n    external: false\n  more:\n    external: false\nnetworks:\n  back:\n    external: false\n", false, "base-name", "more-name", "base-net"},
		{"a later map with the same name", baseMap, "volumes:\n  data:\n    external: {name: base-name}\nnetworks:\n  back:\n    external: {name: base-net}\n", true, "base-name", "more-name", "base-net"},
		{"a later name: wins", baseMap, "volumes:\n  data:\n    name: top\nnetworks:\n  back:\n    name: top-net\n", true, "top", "more-name", "top-net"},
		{"a later bare name: is not given", baseMap, "volumes:\n  data:\n    name:\nnetworks:\n  back:\n    name:\n", true, "base-name", "more-name", "base-net"},
		{"a map over a bool", baseBool, "volumes:\n  data:\n    external: {name: later}\nnetworks:\n  back:\n    external: {name: later-net}\n", true, "later", "", "later-net"},
		{"a bare external: in a later file is not given", baseMap, "volumes:\n  data:\n    external:\nnetworks:\n  back:\n    external:\n", true, "base-name", "more-name", "base-net"},
		{"an empty map, and a name beside one, survive a later true", baseEmpty, "volumes:\n  data:\n    external: true\n  more:\n    external: true\nnetworks:\n  back:\n    external: true\n", true, "real", "keep", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := LoadFiles([]string{tc.base, write("over.yml", tc.over)}, nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if v := p.Volumes["data"]; v.External != tc.external || v.Name != tc.volName {
				t.Errorf("volume = %+v, want external=%v name=%q", v, tc.external, tc.volName)
			}
			if v := p.Volumes["more"]; v.Name != tc.moreName {
				t.Errorf("second volume = %+v, want name=%q", v, tc.moreName)
			}
			if n := p.Networks["back"]; n.External != tc.external || n.Name != tc.netName {
				t.Errorf("network = %+v, want external=%v name=%q", n, tc.external, tc.netName)
			}
		})
	}
	// A secret's map is left as it is: nothing reads its name, and lifting
	// it would list `secrets.s.name` among the ignored fields for a key the
	// file never wrote.
	t.Run("a secret's map is not lifted", func(t *testing.T) {
		base := write("base-secret.yml", "services:\n  a:\n    image: alpine:3\nsecrets:\n  s:\n    external:\n      name: sec-name\n")
		p, err := LoadFiles([]string{base, write("over-secret.yml", "secrets:\n  s:\n    external: true\n")}, nil)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if got := strings.Join(p.Unsupported, ","); strings.Contains(got, "secrets.s.name") {
			t.Errorf("ignored fields = %q: a lifted name the file never wrote", got)
		}
	})
	// A later map whose name differs from the name an earlier file gave —
	// in its map form or beside `external: true` — is a conflict, as docker
	// compose reads it (`name and external.name conflict`), named by file.
	for _, tc := range []struct{ name, base string }{
		{"over a map", baseMap},
		{"over a bool with a name", baseBoolName},
	} {
		t.Run("a later map with another name conflicts "+tc.name, func(t *testing.T) {
			_, err := LoadFiles([]string{tc.base, write("over-conflict.yml", "volumes:\n  data:\n    external: {name: other}\n")}, nil)
			want := `over-conflict.yml: volumes.data: name "base-name" and external.name "other" conflict; only use name`
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("want %q, got: %v", want, err)
			}
		})
	}
}

// A quoted number is a string, and the name.
func TestTheMapFormOfExternalTakesAQuotedNumberAsTheName(t *testing.T) {
	p, err := Load(writeProject(t, "services:\n  a:\n    image: alpine:3\n    networks: [n]\nnetworks:\n  n:\n    external:\n      name: \"42\"\n", ""))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := p.Networks["n"]; !got.External || got.Name != "42" {
		t.Errorf("network = %+v, want external with the name 42", got)
	}
}

// A secret in the map form is an external secret, which opossum does not
// support — and says so as that, not as a type error.
func TestAnExternalSecretInMapFormIsRefusedAsExternal(t *testing.T) {
	p := writeProject(t, `
services:
  a:
    image: alpine:3
    secrets: [s]
secrets:
  s:
    external:
      name: baz
`, "")
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "external") || strings.Contains(err.Error(), "unmarshal") {
		t.Errorf("the map form should be refused as an external secret, got: %v", err)
	}
}

// A shape that is neither a bool nor a mapping is refused by name and line —
// the old decoder message named a Go type, which is not something a compose
// author can act on. And a declaration that isn't a mapping at all still
// fails the way it always did, naming the declaration (VolumeDecl), not the
// internal shape it is read through.
func TestAnExternalOfTheWrongShapeIsRefusedByName(t *testing.T) {
	for name, body := range map[string]string{
		"a string": "volumes:\n  v:\n    external: maybe\n",
		"a list":   "volumes:\n  v:\n    external: [true]\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := writeProject(t, "services:\n  a:\n    image: alpine:3\n"+body, "")
			_, err := Load(p)
			if err == nil || !strings.Contains(err.Error(), "expected true/false or a mapping with name") || !strings.Contains(err.Error(), "line 6") {
				t.Errorf("should be refused by shape with its line, got: %v", err)
			}
		})
	}
	p := writeProject(t, "services:\n  a:\n    image: alpine:3\nvolumes:\n  v: hello\n", "")
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "compose.VolumeDecl") || strings.Contains(err.Error(), "raw") {
		t.Errorf("a non-mapping declaration should still name VolumeDecl and no internal type, got: %v", err)
	}
}
