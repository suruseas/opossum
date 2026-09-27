package compose

import (
	"strings"
	"testing"
)

// `default` is a network every file has, declared or not: a service may list it
// with no declaration (docker compose v5.5.1), and a name that is not there is
// still refused.
func TestAServiceMayListTheDefaultNetworkWithoutDeclaringIt(t *testing.T) {
	body := func(networks string) string {
		return "services:\n  web:\n    image: alpine:3\n    networks: " + networks + "\n"
	}
	for _, tc := range []struct{ name, body, want string }{
		{"default alone", body("[default]"), ""},
		{"default and a declared network", body("[default, back]") + "networks:\n  back: {}\n", ""},
		{"default, declared", body("[default]") + "networks:\n  default: {name: shared}\n", ""},
		{"a network that is not declared", body("[nosuch]"), `service "web" references undefined network "nosuch"`},
		{"default beside one that is not declared", body("[default, nosuch]"), `service "web" references undefined network "nosuch"`},
		{"external default with a name the runtime cannot create, listed", "services:\n  web:\n    image: alpine:3\n    networks: [default]\nnetworks:\n  default: {external: true, name: Bad_Name}\n", "external network \"default\": name \"Bad_Name\""},
		{"external default with a name the runtime cannot create, nothing lists it", "services:\n  web:\n    image: alpine:3\nnetworks:\n  default: {external: true, name: Bad_Name}\n", "external network \"default\": name \"Bad_Name\""},
		{"external default with a name the runtime cannot create, on an isolated service only", "services:\n  web:\n    image: alpine:3\n    network_mode: none\nnetworks:\n  default: {external: true, name: Bad_Name}\n", ""},
		{"a name that only starts like it", body("[defaults]"), `references undefined network "defaults"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, tc.body))
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("want it loaded, got %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("\n got %v\nwant %s", err, tc.want)
			}
		})
	}
}

// The default network is one network with one declaration, so what it shares
// a name with is asked of that declaration.
func TestCheckNetworkKeysReadsTheDefaultNetworksDeclaration(t *testing.T) {
	onDefault := &Service{}
	on := func(keys ...string) *Service { return &Service{Networks: ServiceNetworks(keys)} }
	for _, tc := range []struct {
		name     string
		services map[string]*Service
		networks map[string]NetworkDecl
		want     string // "" = accepted
	}{
		{"a service listing default and one listing nothing", map[string]*Service{"a": onDefault, "b": on("default")}, nil, ""},
		{"declared with a name, both on it", map[string]*Service{"a": onDefault, "b": on("default")},
			map[string]NetworkDecl{"default": {Name: "shared"}}, ""},
		{"external is somebody else's, whatever it is called", map[string]*Service{"a": onDefault, "b": on("x")},
			map[string]NetworkDecl{"default": {External: true, Name: "demo-x"}, "x": {}}, ""},
		{"named, and another key with the same name and declaration", map[string]*Service{"a": onDefault, "b": on("x")},
			map[string]NetworkDecl{"default": {Name: "shared"}, "x": {Name: "shared"}}, ""},
		{"named, and another key with the same name declared differently", map[string]*Service{"a": onDefault, "b": on("x")},
			map[string]NetworkDecl{"default": {Name: "shared", Internal: true}, "x": {Name: "shared"}},
			`networks "x" and "default" both have ` + "`name: shared`" + ` and are declared differently`},
		{"named, and another key's runtime name", map[string]*Service{"a": onDefault, "b": on("x")},
			map[string]NetworkDecl{"default": {Name: "demo-x"}, "x": {}},
			"network \"default\" has `name: demo-x`, which is the runtime name of network \"x\" (`<project>-x`)"},
		{"host-only, and a network that is called demo-net", map[string]*Service{"a": onDefault, "b": on("x")},
			map[string]NetworkDecl{"default": {Internal: true}, "x": {Name: "demo-net"}},
			"network \"x\" has `name: demo-net`, which is the network services without `networks:` join, and that network is declared differently"},
		{"plain, and a network that is called demo-net", map[string]*Service{"a": onDefault, "b": on("x")},
			map[string]NetworkDecl{"x": {Name: "demo-net"}}, ""},
		{"a key net beside the default network", map[string]*Service{"a": onDefault, "b": on("net")},
			map[string]NetworkDecl{"net": {}}, "network \"net\" becomes the runtime network `<project>-net`"},
		{"default listed by a service, and a key net", map[string]*Service{"a": on("default"), "b": on("net")},
			map[string]NetworkDecl{"net": {}}, "network \"net\" becomes the runtime network `<project>-net`"},
		{"an external default is not made, so a key net is not its collision", map[string]*Service{"a": onDefault, "b": on("net")},
			map[string]NetworkDecl{"default": {External: true, Name: "shared"}, "net": {}}, ""},
		{"external and left alone by the check", map[string]*Service{"a": onDefault},
			map[string]NetworkDecl{"default": {External: true, Name: "Shared"}}, ""},
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
