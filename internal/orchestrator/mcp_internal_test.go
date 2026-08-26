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

// What a bad entry says, word for word.
//
// Asking only whether an error came back says nothing about which of the two
// names it put where. A mutation sweep exchanged the entry with the service name
// in both of these messages and left this repository green, and the message is
// the whole of what the user gets: `opossum up` stops, and this line is how they
// find out which tool reference is wrong.
//
// The entries with a port or a path on them are why this can see the exchange at
// all. In a bare "nope" the entry and the service name are the same string, and
// swapping one for the other prints the same message — a fixture that can only
// be written one way is a fixture that reads every way as correct.
func TestResolveMCPToolErrors(t *testing.T) {
	o := newMCPOrch(mcpProject())
	cases := map[string]string{
		"nope":          `MCP tool "nope" refers to unknown service "nope"`,
		"nope:8080":     `MCP tool "nope:8080" refers to unknown service "nope"`,
		"nope/mcp":      `MCP tool "nope/mcp" refers to unknown service "nope"`,
		"multi":         `MCP tool "multi": service publishes 2 ports, so the port is ambiguous — write multi:PORT to be explicit`,
		"multi/api":     `MCP tool "multi/api": service publishes 2 ports, so the port is ambiguous — write multi:PORT to be explicit`,
		"noport":        `MCP tool "noport": service publishes 0 ports, so the port is ambiguous — write noport:PORT to be explicit`,
		"=http://x/mcp": `invalid MCP tool "=http://x/mcp": expected name=url`,
		"name=":         `invalid MCP tool "name=": expected name=url`,
		":8080":         `invalid MCP tool ":8080": name is empty`,
	}
	// A table that can be made smaller can be made to pass: drop the rows that
	// carry a port or a path and the exchange this exists to catch goes back to
	// printing the same message. Counting the rows is not enough to say that —
	// nine entries with no port among them would pass a count and see nothing —
	// so what is counted is the property those rows have.
	if len(cases) != 9 {
		t.Fatalf("%d entries pinned, and there are nine", len(cases))
	}
	sep := 0
	for bad := range cases {
		if i := strings.IndexAny(bad, ":/"); i > 0 {
			sep++
		}
	}
	if sep < 3 {
		t.Fatalf("%d entries name a service and then say more about it, and it takes those for "+
			"the entry and the service name to be different strings at all", sep)
	}
	for bad, want := range cases {
		_, _, err := o.resolveMCPTool(bad)
		if err == nil {
			t.Errorf("entry %q should be an error", bad)
			continue
		}
		if err.Error() != want {
			t.Errorf("entry %q is not what this file says it should be\n got: %s\nwant: %s", bad, err, want)
		}
	}
}

// Two entries that resolve to the same tool name, in the words the user gets.
//
// `buildMCPConfig` refuses it — a map would otherwise keep whichever came last,
// silently, and the agent inside would talk to a server the compose file does
// not obviously name. Nothing read this message, so the name it reports could
// have been either entry's.
func TestBuildMCPConfigRefusesADuplicateName(t *testing.T) {
	o := newMCPOrch(mcpProject("terraform-http", "terraform-http:9999"))
	_, err := o.buildMCPConfig(o.Project.Services["agent"])
	if err == nil {
		t.Fatal("two entries resolving to one name should be refused")
	}
	if want := `duplicate MCP tool name "terraform-http"`; err.Error() != want {
		t.Errorf("the message is not what this file says it should be\n got: %s\nwant: %s", err, want)
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
