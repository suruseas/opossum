package compose

import (
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
