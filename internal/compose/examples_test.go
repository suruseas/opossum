package compose

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The bundled examples are documentation users copy from, so a broken one is a
// broken promise. This walks every compose file under examples/ and asserts it
// loads and validates — catching a rotted example (a field opossum stopped
// accepting, a typo, a newly-invalid combination) before it ships. It's the first
// regression guard the examples have had.
func TestExamplesLoad(t *testing.T) {
	root := filepath.Join("..", "..", "examples")
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Compose files only — skip .env, Dockerfiles, READMEs, .gitkeep, etc.
		// Override files aren't standalone projects, so they're not loaded here.
		base := d.Name()
		if (strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml")) &&
			!strings.Contains(base, ".override.") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking examples: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("found no example compose files — wrong path?")
	}
	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			if _, err := Load(f); err != nil {
				t.Errorf("example %s failed to load: %v", f, err)
			}
		})
	}
}

// The agent-sandbox "caged" variant makes a security claim — the agent's only
// egress is the allowlist proxy — and that claim rests entirely on a few compose
// facts holding together. A careless edit (flip the network off `internal`, drop
// the proxy env, put the proxy where it can't reach the internet) would quietly
// void the guarantee while everything still loads and runs. This pins those facts
// so such an edit fails CI instead of silently un-caging the agent.
func TestAgentSandboxCagedEgressIsFenced(t *testing.T) {
	p, err := Load(filepath.Join("..", "..", "examples", "agent-sandbox", "compose.yaml"))
	if err != nil {
		t.Fatalf("loading agent-sandbox example: %v", err)
	}

	// 1. The caged network must be host-only — remove `internal` and the agent gets
	//    direct internet, bypassing the allowlist entirely.
	caged, ok := p.Networks["caged"]
	if !ok {
		t.Fatal("caged network is gone — the whole egress fence rests on it")
	}
	if !caged.Internal {
		t.Error("caged network must be internal:true (host-only); without it the agent has direct internet")
	}

	// 2. The caged agent must sit ONLY on that host-only network — any other
	//    (non-internal) membership would be an unfenced route out.
	agent, ok := p.Services["agent-caged"]
	if !ok {
		t.Fatal("agent-caged service is gone")
	}
	if len(agent.Networks) != 1 || agent.Networks[0] != "caged" {
		t.Errorf("agent-caged must be on the caged network only, got %v", []string(agent.Networks))
	}

	// 3. Its egress must be pointed at the proxy's published port. If HTTPS_PROXY is
	//    dropped, the agent simply has no way out — but more importantly a stray edit
	//    that repoints it elsewhere should be caught.
	if proxy := envValue([]string(agent.Environment), "HTTPS_PROXY"); !strings.Contains(proxy, ":8080") {
		t.Errorf("agent-caged HTTPS_PROXY must route through the proxy port :8080, got %q", proxy)
	}

	// 4. It must bring the proxy up first.
	if deps := agent.DependsOn.Names(); !contains(deps, "proxy") {
		t.Errorf("agent-caged must depend_on proxy so the exit is up first, got %v", deps)
	}

	// 5. The proxy itself must NOT be on the caged (internal) network — it needs real
	//    internet to forward allowed requests — and must publish a port for the agent
	//    to reach via the host gateway.
	proxy, ok := p.Services["proxy"]
	if !ok {
		t.Fatal("proxy service is gone")
	}
	if contains([]string(proxy.Networks), "caged") {
		t.Error("proxy must stay off the internal network — on it, it couldn't reach the internet to forward allowed requests")
	}
	if len(proxy.Ports) == 0 {
		t.Error("proxy must publish a port so the caged agent can reach it via the host gateway")
	}

	// 6. The whole topology is moot if the proxy itself allows everything. The
	//    enforcement lives in tinyproxy.conf: `FilterDefaultDeny Yes` (not listed =
	//    denied) plus a `Filter` file. Flip the former to No, or drop the Filter
	//    line, and the allowlist silently inverts to allow-all — an un-caging that
	//    compose assertions can't see. Pin the enforcement, not just the plumbing.
	conf, err := os.ReadFile(filepath.Join("..", "..", "examples", "agent-sandbox", "proxy", "tinyproxy.conf"))
	if err != nil {
		t.Fatalf("reading proxy tinyproxy.conf: %v", err)
	}
	c := string(conf)
	if !regexp.MustCompile(`(?mi)^\s*FilterDefaultDeny\s+Yes\b`).MatchString(c) {
		t.Error("tinyproxy.conf must set `FilterDefaultDeny Yes` — without it the allowlist defaults to allow-all (un-caged)")
	}
	if !regexp.MustCompile(`(?mi)^\s*Filter\s+\S`).MatchString(c) {
		t.Error("tinyproxy.conf must reference a Filter (allowlist) file — without it there is no allowlist to enforce")
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
