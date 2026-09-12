package compose

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A network's `ipam.config` subnets are read — one IPv4 and one IPv6 — and
// the rest of `ipam` is read past and listed by its full name; a subnet
// comes in through an alias or a merge key like any value.
func TestIPAMSubnetsAreReadAndTheRestListed(t *testing.T) {
	p, err := Load(writeTemp(t, "x-cfg: &cfg {subnet: 10.7.0.0/24, gateway: 10.7.0.1}\n"+
		"services:\n  web:\n    image: a\n    networks: [back, front, plain]\n"+
		"networks:\n  back:\n    ipam:\n      driver: default\n      config:\n        - *cfg\n        - {subnet: \"fd00:7::/64\", ip_range: \"fd00:7::/80\"}\n"+
		"  front:\n    ipam:\n      config:\n        - <<: *cfg\n          subnet: 10.8.0.0/16\n"+
		"  plain: {}\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	back := p.Networks["back"].IPAM
	if back.Subnet != "10.7.0.0/24" || back.SubnetV6 != "fd00:7::/64" {
		t.Errorf("back subnets = %q / %q, want 10.7.0.0/24 / fd00:7::/64", back.Subnet, back.SubnetV6)
	}
	// The merge key's own subnet wins over the merged one, as YAML reads it.
	if front := p.Networks["front"].IPAM; front.Subnet != "10.8.0.0/16" || front.SubnetV6 != "" {
		t.Errorf("front subnets = %q / %q, want 10.8.0.0/16 and no IPv6", front.Subnet, front.SubnetV6)
	}
	if plain := p.Networks["plain"].IPAM; plain.Subnet != "" || plain.SubnetV6 != "" {
		t.Errorf("a network with no ipam has no subnets, got %q / %q", plain.Subnet, plain.SubnetV6)
	}
	for _, want := range []string{"networks.back.ipam.driver", "networks.back.ipam.config entry 1.gateway", "networks.back.ipam.config entry 2.ip_range", "networks.front.ipam.config entry 1.gateway"} {
		if !slices.Contains(p.Unsupported, want) {
			t.Errorf("%q must be listed among the ignored fields, got %v", want, p.Unsupported)
		}
	}
	for _, quiet := range []string{"networks.back.ipam", "networks.front.ipam", "networks.back.ipam.config"} {
		if slices.Contains(p.Unsupported, quiet) {
			t.Errorf("%q is acted on and must not be listed whole, got %v", quiet, p.Unsupported)
		}
	}
	cfg, err := RenderConfig(p)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	// The YAML body, without the trailer that lists the ignored fields (which
	// names the gateway on purpose).
	cfg, _, _ = strings.Cut(cfg, "\n# fields opossum ignores")
	for _, want := range []string{"subnet: 10.7.0.0/24", "subnet: fd00:7::/64", "subnet: 10.8.0.0/16"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config output should show %q, got:\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, "gateway") {
		t.Errorf("config output shows only what opossum acts on, got a gateway:\n%s", cfg)
	}
}

func TestIPAMRefusals(t *testing.T) {
	const svc = "services:\n  web:\n    image: a\n"
	for _, tc := range []struct{ name, body, want string }{
		{"a subnet that is not CIDR", svc + "networks:\n  back:\n    ipam:\n      config:\n        - subnet: 10.96.0.0\n", `a network's declaration: ipam.config entry 1: subnet "10.96.0.0" is not in CIDR form`},
		{"a subnet that is a number", svc + "networks:\n  back:\n    ipam:\n      config:\n        - subnet: 24\n", `ipam.config entry 1: subnet must be a string in CIDR form, got a number`},
		{"a second IPv4 subnet", svc + "networks:\n  back:\n    ipam:\n      config:\n        - subnet: 10.96.0.0/24\n        - subnet: 10.95.0.0/24\n", `ipam.config entry 2: a second IPv4 subnet ("10.95.0.0/24" after "10.96.0.0/24")`},
		{"a second IPv6 subnet", svc + "networks:\n  back:\n    ipam:\n      config:\n        - subnet: \"fd00:1::/64\"\n        - subnet: \"fd00:2::/64\"\n", `ipam.config entry 2: a second IPv6 subnet`},
		{"config that is a mapping", svc + "networks:\n  back:\n    ipam:\n      config: {subnet: 10.0.0.0/24}\n", `ipam.config must be a list, got a mapping`},
		{"an entry that is a string", svc + "networks:\n  back:\n    ipam:\n      config: [10.0.0.0/24]\n", `ipam.config entry 1 must be a mapping, got a single value`},
		{"ipam that is a list", svc + "networks:\n  back:\n    ipam: [a]\n", `ipam must be a mapping, got a list`},
		{"a bare ipam", svc + "networks:\n  back:\n    ipam:\n", `a network's declaration: ipam has nothing after it (line 6) — write the value or remove the key`},
		{"a key docker compose does not take under ipam", svc + "networks:\n  back:\n    ipam:\n      foo: 1\n", `ipam: "foo" is not a key docker compose takes`},
		{"a key docker compose does not take in an entry", svc + "networks:\n  back:\n    ipam:\n      config:\n        - {subnet: 10.0.0.0/24, foo: 1}\n", `ipam.config entry 1: "foo" is not a key docker compose takes`},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			_, err := Load(writeTemp(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want an error containing %q, got: %v", tc.want, err)
			}
		})
	}
	// Two mistakes name the first in name order, every time.
	for i := 0; i < 20; i++ {
		_, err := Load(writeTemp(t, svc+"networks:\n  back:\n    ipam:\n      zeta: 1\n      foo: 1\n"))
		if err == nil || !strings.Contains(err.Error(), `ipam: "foo" is not a key`) {
			t.Fatalf("run %d: want the first unknown key in name order, got: %v", i, err)
		}
	}
	// A bare `config:` and an entry without a subnet name nothing, as docker compose reads them.
	p, err := Load(writeTemp(t, svc+"networks:\n  back:\n    ipam:\n      config:\n  front:\n    ipam:\n      config:\n        - gateway: 10.0.0.1\n"))
	if err != nil {
		t.Fatalf("a bare config and an entry with no subnet are taken: %v", err)
	}
	if p.Networks["back"].IPAM.Subnet != "" || p.Networks["front"].IPAM.Subnet != "" {
		t.Errorf("no subnet was declared, got %q / %q", p.Networks["back"].IPAM.Subnet, p.Networks["front"].IPAM.Subnet)
	}
}

// A subnet is kept as the network it names, so a spelling with host bits
// set, or an uncompressed IPv6 one, is what the runtime is given and what
// `network inspect` shows back — not a change on the next `up`.
func TestIPAMSubnetIsKeptAsTheNetworkItNames(t *testing.T) {
	p, err := Load(writeTemp(t, "services:\n  web:\n    image: a\nnetworks:\n  back:\n    ipam:\n      config:\n        - subnet: 10.7.0.1/24\n        - subnet: \"fd00:0007:0000::/64\"\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := p.Networks["back"].IPAM; got.Subnet != "10.7.0.0/24" || got.SubnetV6 != "fd00:7::/64" {
		t.Errorf("subnets = %q / %q, want 10.7.0.0/24 / fd00:7::/64", got.Subnet, got.SubnetV6)
	}
}

// A later -f file's `ipam.config` replaces the earlier file's list, as docker
// compose merges it (v5.5.0: the merged network carries the later entries
// only) — appended, the two subnets would read as a second one of the same
// family and be refused.
func TestIPAMConfigOfALaterFileReplaces(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	over := filepath.Join(dir, "over.yaml")
	if err := os.WriteFile(base, []byte("services:\n  web:\n    image: a\nnetworks:\n  back:\n    ipam:\n      config:\n        - subnet: 10.1.0.0/24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(over, []byte("networks:\n  back:\n    ipam:\n      config:\n        - subnet: 10.2.0.0/24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("two files with one subnet each must merge, got: %v", err)
	}
	if got := p.Networks["back"].IPAM.Subnet; got != "10.2.0.0/24" {
		t.Errorf("the later file's subnet wins, got %q", got)
	}
}
