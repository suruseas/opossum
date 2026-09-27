package compose

import (
	"strings"
	"testing"
)

// A network with a `name:` of its own is the network of that name (docker
// compose creates it as written), so which declarations are one network, and
// which names come to the same one, are settled here, before anything is made.
func TestCheckNetworkKeysReadsANetworkNameOfItsOwn(t *testing.T) {
	onDefault := &Service{}
	on := func(keys ...string) *Service { return &Service{Networks: ServiceNetworks(keys)} }
	sub := func(d NetworkDecl, v4, v6 string) NetworkDecl {
		d.IPAM.Subnet, d.IPAM.SubnetV6 = v4, v6
		return d
	}
	for _, tc := range []struct {
		name     string
		services map[string]*Service
		networks map[string]NetworkDecl
		want     string // "" = accepted
	}{
		{"one network under a name", map[string]*Service{"a": on("x")},
			map[string]NetworkDecl{"x": {Name: "shared"}}, ""},
		// What the runtime takes of a name is asked where the network is about to
		// be made (a service that is not started makes none), not here, where every
		// service counts.
		{"a name the runtime cannot create is not this check's", map[string]*Service{"a": on("x")},
			map[string]NetworkDecl{"x": {Name: "Shared"}}, ""},

		{"two keys, one name, one declaration", map[string]*Service{"a": on("x"), "b": on("y")},
			map[string]NetworkDecl{"x": {Name: "shared"}, "y": {Name: "shared"}}, ""},
		{"the same labels written in another order", map[string]*Service{"a": on("x"), "b": on("y")},
			map[string]NetworkDecl{"x": {Name: "shared", Labels: Labels{"a=1", "b=2"}}, "y": {Name: "shared", Labels: Labels{"b=2", "a=1"}}}, ""},
		{"a label written twice reads as the later one", map[string]*Service{"a": on("x"), "b": on("y")},
			map[string]NetworkDecl{"x": {Name: "shared", Labels: Labels{"a=1", "a=2"}}, "y": {Name: "shared", Labels: Labels{"a=2"}}}, ""},
		{"a label written twice, the earlier one is not the network's", map[string]*Service{"a": on("x"), "b": on("y")},
			map[string]NetworkDecl{"x": {Name: "shared", Labels: Labels{"a=1", "a=2"}}, "y": {Name: "shared", Labels: Labels{"a=1"}}}, "declared differently"},
		{"one host-only", map[string]*Service{"a": on("x"), "b": on("y")},
			map[string]NetworkDecl{"x": {Name: "shared", Internal: true}, "y": {Name: "shared"}}, `networks "x" and "y" both have ` + "`name: shared`" + ` and are declared differently`},
		{"other labels", map[string]*Service{"a": on("x"), "b": on("y")},
			map[string]NetworkDecl{"x": {Name: "shared", Labels: Labels{"a=1"}}, "y": {Name: "shared", Labels: Labels{"a=2"}}}, "declared differently"},
		{"a label one has and the other has not", map[string]*Service{"a": on("x"), "b": on("y")},
			map[string]NetworkDecl{"x": {Name: "shared", Labels: Labels{"a=1"}}, "y": {Name: "shared"}}, "declared differently"},
		{"another IPv4 subnet", map[string]*Service{"a": on("x"), "b": on("y")},
			map[string]NetworkDecl{"x": sub(NetworkDecl{Name: "shared"}, "10.1.0.0/24", ""), "y": sub(NetworkDecl{Name: "shared"}, "10.2.0.0/24", "")}, "declared differently"},
		{"another IPv6 subnet", map[string]*Service{"a": on("x"), "b": on("y")},
			map[string]NetworkDecl{"x": sub(NetworkDecl{Name: "shared"}, "", "fd00:1::/64"), "y": sub(NetworkDecl{Name: "shared"}, "", "fd00:2::/64")}, "declared differently"},
		{"two keys, one name, and one of them is not joined", map[string]*Service{"a": on("x")},
			map[string]NetworkDecl{"x": {Name: "shared"}, "y": {Name: "shared", Internal: true}}, ""},

		{"a name that is the default network's, plainly declared", map[string]*Service{"a": onDefault, "b": on("x")},
			map[string]NetworkDecl{"x": {Name: "demo-net"}}, ""},
		{"a name that is the default network's, host-only", map[string]*Service{"a": onDefault, "b": on("x")},
			map[string]NetworkDecl{"x": {Name: "demo-net", Internal: true}}, "network \"x\" has `name: demo-net`, which is the network services without `networks:` join"},

		{"a name that is another key's runtime name", map[string]*Service{"a": on("x", "y")},
			map[string]NetworkDecl{"x": {Name: "demo-y"}, "y": {}}, "network \"x\" has `name: demo-y`, which is the runtime name of network \"y\" (`<project>-y`)"},
		{"a name that is another key's runtime name, the keys the other way round", map[string]*Service{"a": on("x", "y")},
			map[string]NetworkDecl{"x": {}, "y": {Name: "demo-x"}}, "network \"y\" has `name: demo-x`, which is the runtime name of network \"x\" (`<project>-x`)"},
		{"a name that is a folded key's runtime name", map[string]*Service{"a": on("x", "Y")},
			map[string]NetworkDecl{"x": {Name: "demo-y"}, "Y": {}}, "which is the runtime name of network \"Y\" (`<project>-y`)"},
		{"a name that is another project's key's name", map[string]*Service{"a": on("x", "y")},
			map[string]NetworkDecl{"x": {Name: "other-y"}, "y": {}}, ""},

		{"keys folding to one name are refused as before", map[string]*Service{"a": on("backEnd", "backend")},
			map[string]NetworkDecl{"backEnd": {}, "backend": {}}, `networks "backEnd" and "backend" both become the runtime network ` + "`<project>-backend`"},
		{"a key with a name is not folded with the key it folds to", map[string]*Service{"a": on("backEnd", "backend")},
			map[string]NetworkDecl{"backEnd": {Name: "elsewhere"}, "backend": {}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Project{Name: "demo", Services: tc.services, Networks: tc.networks}
			err := p.CheckNetworkKeys()
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("want it accepted, got %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("\n got %v\nwant %s", err, tc.want)
			}
		})
	}
}
