package orchestrator

// Internal tests for MCP tool resolution / .mcp.json generation (#258).

import (
	"io"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

func mcpProject(agentTools ...string) *compose.Project {
	return &compose.Project{
		Name: "demo",
		Services: map[string]*compose.Service{
			"agent":          {Image: "agent", MCPTools: agentTools},
			"terraform-http": {Image: "tf", Ports: compose.Ports{"8080:8080"}},
			"multi":          {Image: "m", Ports: compose.Ports{"8080:8080", "9000:9090"}},
			"noport":         {Image: "n"},
		},
	}
}

func newMCPOrch(p *compose.Project) *Orchestrator {
	return New(p, nil, "opossum", io.Discard)
}

func TestBuildMCPConfigResolvesServiceRef(t *testing.T) {
	o := newMCPOrch(mcpProject("terraform-http"))
	data, err := o.buildMCPConfig(o.Project.Services["agent"])
	if err != nil {
		t.Fatalf("buildMCPConfig: %v", err)
	}
	got := string(data)
	// A service ref becomes an HTTP entry reached by bare name on the shared net,
	// port taken from its single published port, path defaulting to /mcp.
	for _, want := range []string{
		`"mcpServers"`, `"terraform-http"`, `"type": "http"`, `"url": "http://terraform-http:8080/mcp"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("generated config missing %q, got:\n%s", want, got)
		}
	}
}

func TestBuildMCPConfigNoToolsIsNil(t *testing.T) {
	o := newMCPOrch(mcpProject()) // agent declares no tools
	data, err := o.buildMCPConfig(o.Project.Services["agent"])
	if err != nil || data != nil {
		t.Errorf("a service with no MCP tools must produce (nil, nil), got (%q, %v)", data, err)
	}
}

func TestResolveMCPToolForms(t *testing.T) {
	o := newMCPOrch(mcpProject())
	cases := []struct{ entry, name, url string }{
		{"terraform-http", "terraform-http", "http://terraform-http:8080/mcp"},
		{"terraform-http:9999", "terraform-http", "http://terraform-http:9999/mcp"},
		{"terraform-http:8080/api", "terraform-http", "http://terraform-http:8080/api"},
		{"tf=http://192.168.11.22:8090/mcp", "tf", "http://192.168.11.22:8090/mcp"},
	}
	for _, c := range cases {
		name, url, err := o.resolveMCPTool(c.entry)
		if err != nil {
			t.Errorf("%q: unexpected error %v", c.entry, err)
			continue
		}
		if name != c.name || url != c.url {
			t.Errorf("%q → (%q, %q), want (%q, %q)", c.entry, name, url, c.name, c.url)
		}
	}
}

func TestResolveMCPToolErrors(t *testing.T) {
	o := newMCPOrch(mcpProject())
	for _, bad := range []string{
		"nope",          // unknown service
		"multi",         // ambiguous port (2 published)
		"noport",        // no published port
		"=http://x/mcp", // empty name
		"name=",         // empty url
	} {
		if _, _, err := o.resolveMCPTool(bad); err == nil {
			t.Errorf("entry %q should be an error", bad)
		}
	}
}

// Only the declaring service's tools appear — other services in the project don't
// leak into the config.
func TestBuildMCPConfigOnlyDeclaredTools(t *testing.T) {
	o := newMCPOrch(mcpProject("terraform-http"))
	data, _ := o.buildMCPConfig(o.Project.Services["agent"])
	if got := string(data); strings.Contains(got, "multi") || strings.Contains(got, "noport") {
		t.Errorf("undeclared services must not appear in the config, got:\n%s", got)
	}
}
