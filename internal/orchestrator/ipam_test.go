package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

func ipamProject(v4, v6 string) *compose.Project {
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"back"}},
	})
	p.Networks = map[string]compose.NetworkDecl{"back": {IPAM: compose.IPAM{Subnet: v4, SubnetV6: v6}}}
	return p
}

// A declared subnet goes to `network create --subnet` (an IPv6 one to
// `--subnet-v6`), from `up` and from a one-off run alike; a network with
// no subnet is created as before.
func TestUpAndRunCreateTheNetworkWithItsSubnets(t *testing.T) {
	for _, tc := range []struct{ name, v4, v6, want string }{
		{"an IPv4 subnet", "10.7.0.0/24", "", "network create --subnet 10.7.0.0/24 demo-back"},
		{"an IPv6 subnet", "", "fd00:7::/64", "network create --subnet-v6 fd00:7::/64 demo-back"},
		{"both", "10.7.0.0/24", "fd00:7::/64", "network create --subnet 10.7.0.0/24 --subnet-v6 fd00:7::/64 demo-back"},
		{"none", "", "", "network create demo-back"},
	} {
		t.Run(tc.name+" on up", func(t *testing.T) {
			rt, log := fakeShim(t)
			if err := orchestrator.New(ipamProject(tc.v4, tc.v6), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
				t.Fatalf("Up: %v", err)
			}
			if indexOf(log(), tc.want) < 0 {
				t.Errorf("want %q, got %v", tc.want, log())
			}
		})
		t.Run(tc.name+" on a one-off run", func(t *testing.T) {
			rt, log := fakeShim(t)
			if err := orchestrator.New(ipamProject(tc.v4, tc.v6), rt, "opossum", &bytes.Buffer{}).RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{}); err != nil {
				t.Fatalf("RunOneOff: %v", err)
			}
			if indexOf(log(), tc.want) < 0 {
				t.Errorf("want %q, got %v", tc.want, log())
			}
		})
	}
}

// A network that already exists is checked against the declared subnets:
// another subnet is refused with OPSM-207 and what to do; the same subnet,
// a network whose subnets cannot be read, and a declaration with no subnet
// all pass. The IPv6 one is read as the prefix, though the runtime writes
// the gateway's address in it.
func TestUpRefusesAnExistingNetworkWithAnotherSubnet(t *testing.T) {
	for _, tc := range []struct {
		name, v4, v6, existing string
		wantErr                string
	}{
		{"another IPv4 subnet", "10.7.0.0/24", "", "demo-back=10.9.0.0/24", "[OPSM-207] network \"demo-back\" exists with IPv4 subnet 10.9.0.0/24, and the compose file now declares 10.7.0.0/24"},
		{"another IPv6 subnet beside the same IPv4", "10.7.0.0/24", "fd00:7::/64", "demo-back=10.7.0.0/24,fd00:9::/64", "exists with IPv6 subnet fd00:9::/64, and the compose file now declares fd00:7::/64"},
		{"the same subnets", "10.7.0.0/24", "fd00:7::/64", "demo-back=10.7.0.0/24,fd00:7::/64", ""},
		{"the same IPv4 and no IPv6 declared", "10.7.0.0/24", "", "demo-back=10.7.0.0/24,fd00:9::/64", ""},
		{"subnets that cannot be read", "10.7.0.0/24", "", "", ""},
		{"no subnet declared over any network", "", "", "demo-back=10.9.0.0/24", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, "NET_EXISTS=1", "NETWORK_SUBNETS="+tc.existing)
			err := orchestrator.New(ipamProject(tc.v4, tc.v6), rt, "opossum", &bytes.Buffer{}).Up(true)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Up must pass, got: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want an error containing %q, got: %v", tc.wantErr, err)
			}
			if !strings.Contains(err.Error(), "run `opossum down`") {
				t.Errorf("the refusal must say what to do, got: %v", err)
			}
			// Not wrapped as a failure to create: the network is there, and
			// the advice about a stale one would contradict the refusal.
			if strings.Contains(err.Error(), "couldn't create network") || strings.Contains(err.Error(), "container network delete") {
				t.Errorf("OPSM-207 must not carry the stale-network advice, got: %v", err)
			}
			if indexOf(log(), "run -d") >= 0 {
				t.Errorf("nothing may start on a network with the wrong subnet, got %v", log())
			}
		})
	}
	// A one-off run refuses the same way, and unwrapped the same way.
	t.Run("on a one-off run", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "NET_EXISTS=1", "NETWORK_SUBNETS=demo-back=10.9.0.0/24")
		err := orchestrator.New(ipamProject("10.7.0.0/24", ""), rt, "opossum", &bytes.Buffer{}).RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{})
		if err == nil || !strings.Contains(err.Error(), "[OPSM-207] network \"demo-back\" exists with IPv4 subnet 10.9.0.0/24") {
			t.Fatalf("want OPSM-207 from a one-off run, got: %v", err)
		}
		if strings.Contains(err.Error(), "couldn't create network") || strings.Contains(err.Error(), "container network delete") {
			t.Errorf("OPSM-207 must not carry the stale-network advice on a one-off run either, got: %v", err)
		}
	})
	// With no subnet declared the network is not even inspected: a read
	// that has nothing to compare against is a call for nothing.
	rt, log := fakeShim(t)
	setShimEnv(rt, "NET_EXISTS=1", "NETWORK_SUBNETS=demo-back=10.9.0.0/24")
	if err := orchestrator.New(ipamProject("", ""), rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if indexOf(log(), "network inspect demo-back") >= 0 {
		t.Errorf("no subnet declared, so the network must not be inspected for one, got %v", log())
	}
}
