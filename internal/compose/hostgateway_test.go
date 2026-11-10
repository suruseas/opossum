package compose

import (
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestHostGatewayNamesReadEveryWayOfWritingTheEntry(t *testing.T) {
	const pre = "x-h: &h {viaanchor: host-gateway}\nx-a: &a viaalias:host-gateway\nx-g: &g host-gateway\nx-m: &m {one: host-gateway, same: host-gateway}\nx-n: &n {two: host-gateway, same: 10.0.0.9}\nservices:\n  web:\n    image: alpine\n"
	for _, tc := range []struct {
		name, body string
		want       []string
	}{
		{"no extra_hosts", "", nil},
		{"list, colon", "    extra_hosts: [\"host.docker.internal:host-gateway\"]\n", []string{"host.docker.internal"}},
		{"list, equals", "    extra_hosts: [\"host.docker.internal=host-gateway\"]\n", []string{"host.docker.internal"}},
		{"mapping", "    extra_hosts: {gw: host-gateway}\n", []string{"gw"}},
		{"mapping to a list", "    extra_hosts: {gw: [10.0.0.2, host-gateway]}\n", []string{"gw"}},
		{"only the host-gateway entries", "    extra_hosts: [\"a:10.0.0.1\", \"b:host-gateway\"]\n", []string{"b"}},
		{"an address is not one", "    extra_hosts: [\"a:10.0.0.1\", \"b=10.0.0.2\"]\n", nil},
		{"mapping to an address", "    extra_hosts: {a: 10.0.0.1, c: [10.0.0.2]}\n", nil},
		{"the word in another case", "    extra_hosts: [\"a:HOST-GATEWAY\"]\n", nil},
		{"the word with more after it", "    extra_hosts: [\"a:host-gateway:x\"]\n", nil},
		{"equals decides over colon", "    extra_hosts: [\"a=b:host-gateway\"]\n", nil},
		{"a space before the word", "    extra_hosts: [\"a: host-gateway\"]\n", nil},
		{"after a comma, colon", "    extra_hosts: [\"a:10.0.0.1,host-gateway\"]\n", []string{"a"}},
		{"before a comma, equals", "    extra_hosts: [\"a=host-gateway,10.0.0.1\"]\n", []string{"a"}},
		{"in brackets", "    extra_hosts: [\"a:[host-gateway]\"]\n", []string{"a"}},
		{"in brackets, equals", "    extra_hosts: [\"a=[host-gateway]\"]\n", []string{"a"}},
		{"a bracket pair across a comma is no pair", "    extra_hosts: [\"a:[10.0.0.1,host-gateway]\"]\n", nil},
		{"a bracket pair across a comma, the other way", "    extra_hosts: [\"a:[host-gateway,10.0.0.1]\"]\n", nil},
		{"only an opening bracket", "    extra_hosts: [\"a:[host-gateway\"]\n", nil},
		{"only a closing bracket", "    extra_hosts: [\"a:host-gateway]\"]\n", nil},
		{"two pairs of brackets", "    extra_hosts: [\"a:[[host-gateway]]\"]\n", nil},
		{"the middle of three addresses", "    extra_hosts: [\"a:1.1.1.1,host-gateway,2.2.2.2\"]\n", []string{"a"}},
		{"a second colon stays in the address", "    extra_hosts: [\"a:b:host-gateway\"]\n", nil},
		{"a second equals stays in the address", "    extra_hosts: [\"a=b=host-gateway\"]\n", nil},
		{"a mapping's value in brackets", "    extra_hosts: {a: \"[host-gateway]\"}\n", []string{"a"}},
		{"a mapping's list item in brackets", "    extra_hosts: {a: [10.0.0.1, \"[host-gateway]\"]}\n", []string{"a"}},
		{"a mapping's value with two pairs", "    extra_hosts: {a: \"[[host-gateway]]\"}\n", nil},
		{"a mapping's value with only an opening bracket", "    extra_hosts: {a: \"[host-gateway\"}\n", nil},
		{"first in a mapping's list", "    extra_hosts: {a: [host-gateway, 10.0.0.1]}\n", []string{"a"}},
		{"in the middle of a mapping's list", "    extra_hosts: {a: [1.1.1.1, host-gateway, 2.2.2.2]}\n", []string{"a"}},
		{"a mapping's list item's comma does not separate", "    extra_hosts: {a: [\"10.0.0.1,host-gateway\"]}\n", nil},
		{"a mapping's list item's comma, the other way", "    extra_hosts: {a: [\"host-gateway,10.0.0.1\"]}\n", nil},
		{"an empty address", "    extra_hosts: [\"a:\"]\n", nil},
		{"a mapping's empty address", "    extra_hosts: {a: \"\"}\n", nil},
		{"after an empty address", "    extra_hosts: [\"a:,host-gateway\"]\n", []string{"a"}},
		{"an opening bracket and a longer word", "    extra_hosts: [\"a:[host-gatewayx\"]\n", nil},
		{"a closing bracket and a longer word", "    extra_hosts: [\"a:xhost-gateway]\"]\n", nil},
		{"a mapping's comma does not separate", "    extra_hosts: {a: \"10.0.0.1,host-gateway\"}\n", nil},
		{"mapping, another case", "    extra_hosts: {a: HOST-GATEWAY}\n", nil},
		{"mapping, more after it", "    extra_hosts: {a: host-gateway2}\n", nil},
		{"mapping to a list, another case", "    extra_hosts: {a: [10.0.0.1, HOST-GATEWAY]}\n", nil},
		{"mapping to a list, more after it", "    extra_hosts: {a: [10.0.0.1, host-gateway2]}\n", nil},
		{"a build's extra_hosts is not the service's", "    build: {context: ., extra_hosts: [\"a:host-gateway\"]}\n", nil},
		{"a written key wins over a merged one, merge first", "    extra_hosts: {<<: *m, one: 10.0.0.1}\n", []string{"same"}},
		{"a written key wins over a merged one, merge last", "    extra_hosts: {one: 10.0.0.1, <<: *m}\n", []string{"same"}},
		{"a list of merges, first wins", "    extra_hosts: {<<: [*m, *n]}\n", []string{"one", "same", "two"}},
		{"a list of merges, the other order", "    extra_hosts: {<<: [*n, *m]}\n", []string{"one", "two"}},
		{"sorted", "    extra_hosts: [\"z:host-gateway\", \"a:host-gateway\"]\n", []string{"a", "z"}},
		{"once each", "    extra_hosts: [\"a:host-gateway\", \"a=host-gateway\"]\n", []string{"a"}},
		{"through an alias on the item", "    extra_hosts: [*a]\n", []string{"viaalias"}},
		{"through an alias on the mapping", "    extra_hosts: *h\n", []string{"viaanchor"}},
		{"through an alias on the mapping's value", "    extra_hosts: {a: *g}\n", []string{"a"}},
		{"through an alias in the mapping's list", "    extra_hosts: {a: [*g]}\n", []string{"a"}},
		{"through a merge key", "    extra_hosts: {<<: *h, b: host-gateway}\n", []string{"b", "viaanchor"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, pre+tc.body))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := p.Services["web"].HostGatewayNames; !slices.Equal(got, tc.want) {
				t.Errorf("HostGatewayNames = %v, want %v", got, tc.want)
			}
		})
	}
}

// A key that is not a name is refused when the file is loaded, as docker compose
// refuses it, so this is asked of the function: such a mapping names nothing.
func TestHostGatewayNamesOfAMappingThatDoesNotDecodeAreNone(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("{? [x] : host-gateway, a: host-gateway}\n"), &doc); err != nil {
		t.Fatal(err)
	}
	if got := hostGatewayNames(doc.Content[0]); got != nil {
		t.Errorf("HostGatewayNames = %v, want none", got)
	}
}

func TestHostGatewayNamesAreReadAfterInterpolationAndExtends(t *testing.T) {
	t.Run("interpolated", func(t *testing.T) {
		t.Setenv("GW_WORD", "host-gateway")
		p, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    extra_hosts: [\"a:${GW_WORD}\"]\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if got := p.Services["web"].HostGatewayNames; !slices.Equal(got, []string{"a"}) {
			t.Errorf("HostGatewayNames = %v", got)
		}
	})
	t.Run("extended", func(t *testing.T) {
		p, err := Load(writeTemp(t, "services:\n  base:\n    image: alpine\n    extra_hosts: [\"a:host-gateway\"]\n  web:\n    extends: {service: base}\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if got := p.Services["web"].HostGatewayNames; !slices.Equal(got, []string{"a"}) {
			t.Errorf("HostGatewayNames of the extending service = %v", got)
		}
	})
}
