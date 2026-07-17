package doctor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// mock is a fake Runner returning canned output per top-level `container` command.
type mock struct {
	status, builder, probe string
	statusErr              bool
	dns                    bool
}

func (m mock) Output(args ...string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	switch args[0] {
	case "system":
		if m.statusErr {
			return "", errors.New("container not available")
		}
		return m.status, nil
	case "builder":
		return m.builder, nil
	case "run":
		return m.probe, nil
	}
	return "", nil
}

func (m mock) DNSDomainExists(string) bool { return m.dns }

func TestDoctorAllHealthy(t *testing.T) {
	m := mock{status: "status running\n", dns: true, probe: "DNS-OK\nIP-OK\n", builder: "buildkit img running 4 8192 MB\n"}
	var b bytes.Buffer
	if !Run(&b, m, "opossum", nil, 16384) {
		t.Fatalf("expected all checks to pass; got:\n%s", b.String())
	}
	for _, want := range []string{"✅ runtime", "✅ dns", "✅ network", "✅ builder"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q in:\n%s", want, b.String())
		}
	}
}

func TestDoctorRuntimeDownSkipsRest(t *testing.T) {
	var b bytes.Buffer
	if Run(&b, mock{statusErr: true}, "opossum", nil, 0) {
		t.Error("expected failure when the runtime is down")
	}
	s := b.String()
	if !strings.Contains(s, "❌ runtime") {
		t.Errorf("expected a runtime failure; got:\n%s", s)
	}
	if strings.Contains(s, "network") || strings.Contains(s, "dns") {
		t.Errorf("further checks should be skipped when the runtime is down; got:\n%s", s)
	}
}

// The middle network branch: the internet is reachable (IP-OK) but DNS fails.
// Previously only both-OK and both-FAIL were tested, so a misclassification of
// the DNS-only case would ship green.
func TestDoctorNetworkDNSOnlyFailure(t *testing.T) {
	m := mock{status: "status running\n", dns: true, probe: "DNS-FAIL\nIP-OK\n", builder: "buildkit img running 4 8192 MB\n"}
	var b bytes.Buffer
	if Run(&b, m, "opossum", nil, 16384) {
		t.Error("a DNS-only network failure should fail the checks")
	}
	if s := b.String(); !strings.Contains(s, "❌ network") || !strings.Contains(s, "DNS resolution is failing") {
		t.Errorf("expected a DNS-specific network failure, got:\n%s", s)
	}
}

func TestDoctorDNSNetworkBuilderProblems(t *testing.T) {
	m := mock{status: "status running\n", dns: false, probe: "DNS-FAIL\nIP-FAIL\n", builder: "buildkit img running 2 2048 MB\n"}
	var b bytes.Buffer
	if Run(&b, m, "opossum", nil, 0) {
		t.Error("expected failure (dns + network)")
	}
	s := b.String()
	checks := map[string]string{
		"❌ dns":     "sudo container system dns create opossum",
		"❌ network": "container system stop && container system start",
	}
	for status, fix := range checks {
		if !strings.Contains(s, status) || !strings.Contains(s, fix) {
			t.Errorf("expected %q with fix %q; got:\n%s", status, fix, s)
		}
	}
	if !strings.Contains(s, "⚠️") || !strings.Contains(s, "builder") {
		t.Errorf("expected a builder warning; got:\n%s", s)
	}
}

// A wedge that stops the probe from even running (empty output) still reads as a
// network failure.
func TestDoctorNetworkProbeCantRun(t *testing.T) {
	m := mock{status: "status running\n", dns: true, probe: "", builder: "buildkit img running 4 8192 MB\n"}
	var b bytes.Buffer
	Run(&b, m, "opossum", nil, 0)
	if !strings.Contains(b.String(), "❌ network") {
		t.Errorf("a probe that produced no output should read as a network failure; got:\n%s", b.String())
	}
}

func TestCheckMemoryEstimate(t *testing.T) {
	p := &compose.Project{Services: map[string]*compose.Service{
		"a": {Image: "x"},
		"b": {Image: "x", MemLimit: "4g"}, // 290 + 4096 = 4386 MB ≈ 4.3 GB
	}}
	c := checkMemory(p, 16384) // well under 70% of 16 GB
	if !strings.Contains(c.detail, "4.3 GB") {
		t.Errorf("estimate should account for mem_limit; got: %s", c.detail)
	}
	if c.status != ok {
		t.Errorf("a small stack on a big host should be ok; got status %d", c.status)
	}
}

// A present-but-not-running system status is a runtime failure, and the rest is
// skipped.
func TestDoctorRuntimeStopped(t *testing.T) {
	var b bytes.Buffer
	if Run(&b, mock{status: "status stopped\n"}, "opossum", nil, 0) {
		t.Error("a stopped system should fail")
	}
	if !strings.Contains(b.String(), "❌ runtime") || strings.Contains(b.String(), "network") {
		t.Errorf("expected runtime fail and rest skipped; got:\n%s", b.String())
	}
}

func TestCheckMemoryOvercommitWarns(t *testing.T) {
	svcs := map[string]*compose.Service{}
	for i := 0; i < 40; i++ {
		svcs[fmt.Sprintf("s%d", i)] = &compose.Service{Image: "x"} // 40 * 290 = 11600 MB
	}
	if c := checkMemory(&compose.Project{Services: svcs}, 8192); c.status != warn {
		t.Errorf("40 services on 8 GB should warn; got status %d, detail %s", c.status, c.detail)
	}
}

// TestRunJSONShape pins the machine-readable contract: the top-level object has
// `healthy` and `checks`, each check carries exactly {id, status, detail, fix}
// with the ok/warn/fail vocabulary, and the ids are the stable slugs agents key
// on. A healthy run reports healthy:true with no "fail" statuses.
func TestRunJSONShape(t *testing.T) {
	m := mock{status: "status running\n", dns: true, probe: "DNS-OK\nIP-OK\n", builder: "buildkit img running 4 8192 MB\n"}
	p := &compose.Project{Services: map[string]*compose.Service{"a": {Image: "x"}}}
	var b bytes.Buffer
	healthy, err := RunJSON(&b, m, "opossum", p, 16384)
	if err != nil {
		t.Fatalf("RunJSON: %v", err)
	}
	if !healthy {
		t.Fatalf("expected healthy run; got:\n%s", b.String())
	}

	var rep Report
	if err := json.Unmarshal(b.Bytes(), &rep); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, b.String())
	}
	if !rep.Healthy {
		t.Errorf("healthy field should be true; got %+v", rep)
	}
	wantIDs := []string{"runtime", "dns", "network", "builder", "memory"}
	if len(rep.Checks) != len(wantIDs) {
		t.Fatalf("got %d checks, want %d: %+v", len(rep.Checks), len(wantIDs), rep.Checks)
	}
	for i, c := range rep.Checks {
		if c.ID != wantIDs[i] {
			t.Errorf("check %d: id = %q, want %q", i, c.ID, wantIDs[i])
		}
		switch c.Status {
		case "ok", "warn", "fail":
		default:
			t.Errorf("check %q: status = %q, want ok/warn/fail", c.ID, c.Status)
		}
		if c.Status == "fail" {
			t.Errorf("healthy run should have no fail; %q is fail", c.ID)
		}
	}

	// Assert the exact key set per check (no extra/renamed keys leak into the
	// contract) by round-tripping through a generic map.
	var raw struct {
		Checks []map[string]any `json:"checks"`
	}
	if err := json.Unmarshal(b.Bytes(), &raw); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	for _, c := range raw.Checks {
		if len(c) != 4 {
			t.Errorf("check has %d keys, want 4 {id,status,detail,fix}: %v", len(c), c)
		}
		for _, k := range []string{"id", "status", "detail", "fix"} {
			if _, ok := c[k]; !ok {
				t.Errorf("check missing key %q: %v", k, c)
			}
		}
	}
}

// TestRunJSONStatusMapping is the mutation-style guard: each status value must
// land in the JSON as the right string, a failing check must carry its fix and
// flip healthy:false, and a passing check must have an empty fix.
func TestRunJSONStatusMapping(t *testing.T) {
	// dns fails, builder warns (2048 MB), runtime/network ok.
	m := mock{status: "status running\n", dns: false, probe: "DNS-OK\nIP-OK\n", builder: "buildkit img running 2 2048 MB\n"}
	var b bytes.Buffer
	healthy, err := RunJSON(&b, m, "opossum", nil, 0)
	if err != nil {
		t.Fatalf("RunJSON: %v", err)
	}
	if healthy {
		t.Error("a failing dns check must make the report unhealthy")
	}

	var rep Report
	if err := json.Unmarshal(b.Bytes(), &rep); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, b.String())
	}
	if rep.Healthy {
		t.Error("healthy field should be false when a check fails")
	}
	byID := map[string]CheckResult{}
	for _, c := range rep.Checks {
		byID[c.ID] = c
	}

	if c := byID["runtime"]; c.Status != "ok" || c.Fix != "" {
		t.Errorf("runtime should be ok with empty fix; got %+v", c)
	}
	if c := byID["dns"]; c.Status != "fail" || !strings.Contains(c.Fix, "dns create opossum") {
		t.Errorf("dns should be fail with its create fix; got %+v", c)
	}
	if c := byID["builder"]; c.Status != "warn" || c.Fix == "" {
		t.Errorf("builder should be warn with a fix; got %+v", c)
	}
}

func TestParseBuilder(t *testing.T) {
	if mb, st := parseBuilder("ID IMAGE STATE IP CPUS MEMORY\nbuildkit img stopped 2 2048 MB\n"); mb != 2048 || st != "stopped" {
		t.Errorf("got mb=%d state=%q, want 2048/stopped", mb, st)
	}
	if mb, _ := parseBuilder("buildkit img running 4 8 GB\n"); mb != 8192 {
		t.Errorf("GB should convert to MB; got %d", mb)
	}
}
