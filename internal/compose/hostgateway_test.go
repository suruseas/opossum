package compose

import (
	"slices"
	"testing"
)

func TestHostGatewayNamesReadEveryWayOfWritingTheEntry(t *testing.T) {
	const pre = "x-h: &h {viaanchor: host-gateway}\nx-a: &a viaalias:host-gateway\nx-g: &g host-gateway\nservices:\n  web:\n    image: alpine\n"
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
