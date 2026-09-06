package compose

// `external:` written as a YAML alias (#745). The declaration's `external`
// field is read into a yaml.Node, in which an alias stays an alias, so
// `external: *shared` was refused as "got something else" — the shape error
// for a value that, once followed, is a plain `true` or a `{name: …}`
// mapping. docker compose (v5.5.0) reads the aliased forms as the plain ones.

import (
	"strings"
	"testing"
)

func TestAnAliasedExternalIsReadAsWhatItStandsFor(t *testing.T) {
	p, err := Load(writeTemp(t, "x-ext: &e true\nx-extm: &em\n  name: shared-net\nservices:\n  web:\n    image: alpine\n    volumes:\n      - data:/data\n    networks: [back]\nvolumes:\n  data:\n    external: *e\nnetworks:\n  back:\n    external: *em\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if v := p.Volumes["data"]; !v.External {
		t.Errorf("volume data: external = %v, want true through the alias", v.External)
	}
	if n := p.Networks["back"]; !n.External || n.Name != "shared-net" {
		t.Errorf("network back: external=%v name=%q, want external with name shared-net through the alias", n.External, n.Name)
	}
}

// An external secret is refused as external whatever the spelling; through an
// alias it must still be that refusal, not the shape error.
func TestAnAliasedExternalSecretIsRefusedAsExternal(t *testing.T) {
	got := loadErr(t, "x-ext: &e true\nsecrets:\n  tok:\n    external: *e\nservices:\n  web:\n    image: alpine\n    secrets: [tok]\n")
	if !strings.Contains(got, `external secret "tok" is not supported`) {
		t.Errorf("want the external-secret refusal, got:\n%s", got)
	}
}

// Inside the mapping an alias stands for, an unknown key is refused on the
// key's own line (that is where it is written and fixed), not the alias's.
func TestAnUnknownKeyInsideAnAliasedExternalIsRefusedOnItsOwnLine(t *testing.T) {
	got := loadErr(t, "x-em: &em\n  nam: shared-net\nservices:\n  web:\n    image: alpine\n    networks: [back]\nnetworks:\n  back:\n    external: *em\n")
	if !strings.Contains(got, `line 2: external: unknown key "nam" (only name is allowed here)`) {
		t.Errorf("want the unknown-key refusal at line 2, got:\n%s", got)
	}
}

// The key side can be an alias or a merge key too; both decode to `name`.
func TestAnAliasedOrMergedKeyInsideExternalIsReadAsName(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"alias key", "x-k: &k name\nservices:\n  web:\n    image: alpine\n    volumes:\n      - data:/data\nvolumes:\n  data:\n    external:\n      *k : shared-vol\n"},
		{"merge key", "x-b: &b\n  name: shared-vol\nservices:\n  web:\n    image: alpine\n    volumes:\n      - data:/data\nvolumes:\n  data:\n    external:\n      <<: *b\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, tc.body))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if v := p.Volumes["data"]; !v.External || v.Name != "shared-vol" {
				t.Errorf("volume data: external=%v name=%q, want external with name shared-vol", v.External, v.Name)
			}
		})
	}
}

// An alias to the wrong shape is the wrong shape, named on the line the alias
// was written — the refusal does not point at the anchor.
func TestAnAliasedExternalOfTheWrongShapeIsStillRefusedAtTheAliasLine(t *testing.T) {
	got := loadErr(t, "x-ext: &e [true]\nservices:\n  web:\n    image: alpine\n    volumes:\n      - data:/data\nvolumes:\n  data:\n    external: *e\n")
	if !strings.Contains(got, "line 9: external: expected true/false or a mapping with name, got a list") {
		t.Errorf("want the shape refusal at line 9 (the alias), got:\n%s", got)
	}
}
