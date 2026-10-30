package compose

import (
	"slices"
	"testing"
)

// A service's `aliases:` under a network is read: a list of one-label names is
// passed by `up` and not listed among the ignored fields; anything else on that
// network's aliases is listed, as every alias was before they were read.
func TestNetworkAliasesAreRead(t *testing.T) {
	for _, tc := range []struct {
		name, aliases string
		want          []string // NetworkAliases["back"]
		listed        bool     // networks.back.aliases among the ignored fields
	}{
		{"one-label names", "[cache, db-1]", []string{"cache", "db-1"}, false},
		{"capitals and an underscore", "[Cache, cache_1]", []string{"Cache", "cache_1"}, false},
		// A name with a dot is not passed, so the field is listed; the names are
		// still read, for `up` to say which it left out.
		{"a name with a dot", "[db.internal]", []string{"db.internal"}, true},
		{"one-label and dotted", "[cache, db.internal]", []string{"cache", "db.internal"}, true},
		{"an empty name", `[cache, ""]`, []string{"cache", ""}, true},
		// Not a list of strings: not read at all, and listed.
		{"a single name, not a list", "cache", nil, true},
		{"a number among the names", "[cache, 5]", nil, true},
		{"a null among the names", "[cache, ~]", nil, true},
		{"a mapping", "{cache: 1}", nil, true},
		// Nothing written: nothing to pass, nothing to list.
		{"an empty list", "[]", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := loadAliases(t, "services:\n  web:\n    image: app:1\n    networks:\n      back:\n        aliases: "+tc.aliases+"\nnetworks:\n  back: {}\n")
			web := p.Services["web"]
			if got := web.NetworkAliases["back"]; !slices.Equal(got, tc.want) {
				t.Errorf("aliases read: want %v, got %v", tc.want, got)
			}
			if got := slices.Contains(web.Unsupported, "networks.back.aliases"); got != tc.listed {
				t.Errorf("networks.back.aliases listed as ignored: want %v, got %v (%v)", tc.listed, got, web.Unsupported)
			}
		})
	}
}

// Each network's aliases are kept under that network's key, and a network
// with none has no entry.
func TestNetworkAliasesAreKeptPerNetwork(t *testing.T) {
	p := loadAliases(t, "services:\n  web:\n    image: app:1\n    networks:\n      back:\n        aliases: [cache]\n      front:\n        aliases: [www]\n      side: {}\nnetworks:\n  back: {}\n  front: {}\n  side: {}\n")
	got := p.Services["web"].NetworkAliases
	if len(got) != 2 || !slices.Equal(got["back"], []string{"cache"}) || !slices.Equal(got["front"], []string{"www"}) {
		t.Errorf("want back: [cache], front: [www] and nothing for side, got %v", got)
	}
}

// The list form of `networks:` writes no aliases.
func TestNetworkAliasesOfTheListForm(t *testing.T) {
	p := loadAliases(t, "services:\n  web:\n    image: app:1\n    networks: [back]\nnetworks:\n  back: {}\n")
	if got := p.Services["web"].NetworkAliases; got != nil {
		t.Errorf("want no aliases, got %v", got)
	}
}

// Written through YAML aliases — the networks block, the list, or one name —
// the aliases read are the ones written plainly.
func TestNetworkAliasesThroughYAMLAliases(t *testing.T) {
	p := loadAliases(t, "x-nets: &nets\n  back:\n    aliases: [cache]\nx-list: &list [cache]\nx-name: &name cache\nservices:\n"+
		"  a:\n    image: app:1\n    networks: *nets\n"+
		"  b:\n    image: app:1\n    networks:\n      back:\n        aliases: *list\n"+
		"  c:\n    image: app:1\n    networks:\n      back:\n        aliases: [*name]\n"+
		"networks:\n  back: {}\n")
	for _, svc := range []string{"a", "b", "c"} {
		if got := p.Services[svc].NetworkAliases["back"]; !slices.Equal(got, []string{"cache"}) {
			t.Errorf("service %s: want [cache], got %v", svc, got)
		}
		if slices.Contains(p.Services[svc].Unsupported, "networks.back.aliases") {
			t.Errorf("service %s: one-label aliases listed as ignored: %v", svc, p.Services[svc].Unsupported)
		}
	}
}

func loadAliases(t *testing.T, yaml string) *Project {
	t.Helper()
	p, err := Load(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return p
}
