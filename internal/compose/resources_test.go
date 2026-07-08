package compose

import (
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

// `deploy` is a known key (resources.limits is applied), but content opossum
// drops (replicas, reservations…) is still surfaced as ignored.
func TestDeployExtraSurfaced(t *testing.T) {
	unsupported := func(body string) []string {
		var s Service
		if err := yaml.Unmarshal([]byte(body), &s); err != nil {
			t.Fatal(err)
		}
		return s.Unsupported
	}
	if got := unsupported("image: x\ndeploy:\n  resources:\n    limits:\n      memory: 1g\n      cpus: \"1\"\n"); slices.Contains(got, "deploy") {
		t.Errorf("deploy with only resources.limits should not be flagged, got %v", got)
	}
	if got := unsupported("image: x\ndeploy:\n  replicas: 3\n  resources:\n    limits:\n      memory: 1g\n"); !slices.Contains(got, "deploy") {
		t.Errorf("deploy.replicas should be surfaced as ignored, got %v", got)
	}
	if got := unsupported("image: x\ndeploy:\n  resources:\n    reservations:\n      memory: 1g\n"); !slices.Contains(got, "deploy") {
		t.Errorf("deploy.resources.reservations should be surfaced as ignored, got %v", got)
	}
}

func deployLimits(mem, cpus string) *Deploy {
	return &Deploy{Resources: &DeployResources{Limits: &DeployLimits{Memory: scalarStr(mem), CPUs: scalarStr(cpus)}}}
}

// Resource limits resolve to Apple `container` -m/-c args: memory in MiB with an
// uppercase suffix, CPUs as an integer rounded up. Legacy (mem_limit/cpus) and
// modern (deploy.resources.limits) both work.
func TestResourcesResolve(t *testing.T) {
	cases := []struct {
		name             string
		svc              Service
		wantMem, wantCPU string
	}{
		{"legacy", Service{MemLimit: "512m", CPUs: "1.5"}, "512M", "2"},                 // 512 MiB, 1.5→ceil 2
		{"gigs", Service{MemLimit: "2g"}, "2048M", ""},                                  //
		{"uppercase + MiB", Service{MemLimit: "512MiB"}, "512M", ""},                    //
		{"bytes", Service{MemLimit: "536870912"}, "512M", ""},                           // 512 MiB in bytes
		{"deploy", Service{Deploy: deployLimits("256m", "0.5")}, "256M", "1"},           // 0.5→ceil 1
		{"deploy int cpu", Service{Deploy: deployLimits("", "2")}, "", "2"},             //
		{"agree", Service{MemLimit: "1g", Deploy: deployLimits("1g", "")}, "1024M", ""}, // both, equal → ok
		{"non-MiB bytes ceil", Service{MemLimit: "1500000000"}, "1431M", ""},            // 1.5e9 B → ceil to MiB
		{"none", Service{}, "", ""},                                                     //
	}
	for _, c := range cases {
		mem, cpu, err := c.svc.Resources()
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
			continue
		}
		if mem != c.wantMem || cpu != c.wantCPU {
			t.Errorf("%s: got (-m %q, -c %q), want (-m %q, -c %q)", c.name, mem, cpu, c.wantMem, c.wantCPU)
		}
	}
}

// docker compose rejects a compose that sets the legacy and deploy forms to
// different values.
func TestResourcesConflictErrors(t *testing.T) {
	for _, svc := range []Service{
		{MemLimit: "1g", Deploy: deployLimits("2g", "")}, // memory conflict
		{CPUs: "1", Deploy: deployLimits("", "2")},       // cpus conflict
	} {
		if _, _, err := svc.Resources(); err == nil {
			t.Errorf("expected a conflict error for %+v", svc)
		}
	}
}

func TestResourcesBadValueErrors(t *testing.T) {
	for _, svc := range []Service{
		{MemLimit: "notmem"},
		{MemLimit: "512x"}, // bad unit
		{CPUs: "abc"},
		{CPUs: "-1"}, // negative
	} {
		if _, _, err := svc.Resources(); err == nil {
			t.Errorf("expected an error for %+v", svc)
		}
	}
}
