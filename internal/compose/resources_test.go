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
	if got := unsupported("image: x\ndeploy:\n  replicas: 3\n  resources:\n    limits:\n      memory: 1g\n"); !slices.Contains(got, "deploy.replicas") {
		t.Errorf("deploy.replicas should be surfaced as ignored, by name, got %v", got)
	}
	if got := unsupported("image: x\ndeploy:\n  resources:\n    reservations:\n      memory: 1g\n"); !slices.Contains(got, "deploy.resources.reservations") {
		t.Errorf("deploy.resources.reservations should be surfaced as ignored, by name, got %v", got)
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
//
// Word for word, both of them. Asking only whether an error came back leaves
// the sentence unread: the message here names two keys, and a version of it
// that named them the other way round — or named the wrong pair entirely —
// passed every check this file used to make. The reader of this error is
// holding a compose file with two places to look; the message is what tells
// them which two.
func TestResourcesConflictErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		svc  Service
		want string
	}{
		{
			"memory set both ways",
			Service{Name: "web", MemLimit: "1g", Deploy: deployLimits("2g", "")},
			`service "web": mem_limit and deploy.resources.limits.memory are set to different values`,
		},
		{
			"cpus set both ways",
			Service{Name: "web", CPUs: "1", Deploy: deployLimits("", "2")},
			`service "web": cpus and deploy.resources.limits.cpus are set to different values`,
		},
	} {
		_, _, err := c.svc.Resources()
		if err == nil {
			t.Errorf("%s: expected a conflict error", c.name)
			continue
		}
		if err.Error() != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, err, c.want)
		}
	}
}

// A value that will not parse says which key held it and what would parse.
//
// The key first, because two keys can hold this number and the reader has to
// know which one to open. The value is deliberately not repeated: these come
// from a compose file where a `${...}` can put a password in, and this text
// goes to a terminal and a CI log.
func TestResourcesBadValueErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		svc  Service
		want string
	}{
		{
			"mem_limit is not a size",
			Service{Name: "web", MemLimit: "notmem"},
			`service "web": mem_limit: not a memory size — use a number with an optional unit, e.g. "512m" or "2g"`,
		},
		{
			"mem_limit has a unit nobody uses",
			Service{Name: "web", MemLimit: "512x"},
			`service "web": mem_limit: not a memory unit — use k, m, g, or t (e.g. "512m")`,
		},
		{
			"cpus is not a number",
			Service{Name: "web", CPUs: "abc"},
			`service "web": cpus: not a number of CPUs — use a number, e.g. "1.5" or "2"`,
		},
		{
			"cpus is negative",
			Service{Name: "web", CPUs: "-1"},
			`service "web": cpus: must not be negative`,
		},
		// The deploy form of each, so that the key in the message is read from
		// the argument rather than being the same word every time: with only the
		// legacy cases, every want above holds the same two words and a version
		// that printed a constant would satisfy them all. Naming the wrong key
		// is caught elsewhere too (TestAFieldThatChecksItsOwnValueDoesNotReadItBack
		// covers it, measured), so this is a second reader of the same rule and
		// not the only one.
		{
			"deploy memory is not a size",
			Service{Name: "web", Deploy: deployLimits("nope", "")},
			`service "web": deploy.resources.limits.memory: not a memory size — use a number with an optional unit, e.g. "512m" or "2g"`,
		},
		{
			"deploy cpus is not a number",
			Service{Name: "web", Deploy: deployLimits("", "xyz")},
			`service "web": deploy.resources.limits.cpus: not a number of CPUs — use a number, e.g. "1.5" or "2"`,
		},
	} {
		_, _, err := c.svc.Resources()
		if err == nil {
			t.Errorf("%s: expected an error", c.name)
			continue
		}
		if err.Error() != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, err, c.want)
		}
	}
}
