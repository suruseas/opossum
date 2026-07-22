package orchestrator

// MCP tool wiring (#258): a service can declare the MCP servers an agent inside it
// should use (`x-opossum-mcp-tools`), and opossum generates a `.mcp.json` and
// mounts it in — the "tools" face of a compose-as-capability-manifest. MVP is
// HTTP-transport MCP servers only (stdio has no cross-container stdin path).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/suruseas/opossum/internal/compose"
)

// mcpMountTarget is the in-container path a generated MCP config is mounted at. A
// stable, documented location outside the workspace, so an agent command can point
// at it (e.g. `claude --mcp-config /run/opossum/mcp.json`) without it landing in
// the bind-mounted `./work` or tripping Claude Code's project-scope approval gate.
const mcpMountTarget = "/run/opossum/mcp.json"

// mcpServer is one entry in the generated .mcp.json (Claude Code's HTTP shape).
type mcpServer struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// buildMCPConfig renders the `.mcp.json` for a service's declared MCP tools, or
// (nil, nil) when it declares none. Deterministic (json.Marshal sorts map keys),
// so it's golden-testable.
func (o *Orchestrator) buildMCPConfig(svc *compose.Service) ([]byte, error) {
	if len(svc.MCPTools) == 0 {
		return nil, nil
	}
	servers := map[string]mcpServer{}
	for _, entry := range svc.MCPTools {
		name, url, err := o.resolveMCPTool(entry)
		if err != nil {
			return nil, err
		}
		if _, dup := servers[name]; dup {
			return nil, fmt.Errorf("duplicate MCP tool name %q", name)
		}
		servers[name] = mcpServer{Type: "http", URL: url}
	}
	data, err := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// resolveMCPTool turns one `x-opossum-mcp-tools` entry into a (tool name, URL).
// Two forms:
//   - "name=url": an explicit HTTP URL (any ${OPOSSUM_HOST_GATEWAY} is already
//     interpolated at load) — for a tool reached some other way, e.g. a caged
//     agent going out through the host gateway.
//   - "svc" / "svc:port" / "svc:port/path": another compose service, reached by
//     name on the shared network. The port is taken from the service's single
//     published port when omitted; the path defaults to /mcp.
func (o *Orchestrator) resolveMCPTool(entry string) (name, url string, err error) {
	entry = strings.TrimSpace(entry)
	if i := strings.IndexByte(entry, '='); i >= 0 {
		name, url = strings.TrimSpace(entry[:i]), strings.TrimSpace(entry[i+1:])
		if name == "" || url == "" {
			return "", "", fmt.Errorf("invalid MCP tool %q: expected name=url", entry)
		}
		return name, url, nil
	}

	ref, path := entry, "/mcp"
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		ref, path = ref[:i], ref[i:]
	}
	svcName, port := ref, ""
	if i := strings.IndexByte(ref, ':'); i >= 0 {
		svcName, port = ref[:i], ref[i+1:]
	}
	if svcName == "" {
		return "", "", fmt.Errorf("invalid MCP tool %q: name is empty", entry)
	}
	target, ok := o.Project.Services[svcName]
	if !ok {
		return "", "", fmt.Errorf("MCP tool %q refers to unknown service %q", entry, svcName)
	}
	if port == "" {
		port, err = singleContainerPort(target)
		if err != nil {
			return "", "", fmt.Errorf("MCP tool %q: %w — write %s:PORT to be explicit", entry, err, svcName)
		}
	}
	// Bare service name resolves on the shared network via the DNS search domain.
	return svcName, fmt.Sprintf("http://%s:%s%s", svcName, port, path), nil
}

// singleContainerPort returns a service's container port when it publishes exactly
// one, so a tool reference can omit it. Ambiguous (0 or >1) is an error.
func singleContainerPort(svc *compose.Service) (string, error) {
	if len(svc.Ports) != 1 {
		return "", fmt.Errorf("service publishes %d ports, so the port is ambiguous", len(svc.Ports))
	}
	return containerPortOf(svc.Ports[0]), nil
}

// containerPortOf extracts the container-side port from a normalized port spec
// ("8080:8080", "127.0.0.1:9000:9090/tcp" → "8080", "9090").
func containerPortOf(spec string) string {
	if i := strings.IndexByte(spec, '/'); i >= 0 {
		spec = spec[:i]
	}
	parts := strings.Split(spec, ":")
	return parts[len(parts)-1]
}

// mcpConfigMount returns the read-only bind mount for a service's generated
// `.mcp.json`, or ("", nil) when it declares no tools. When write is true it also
// (re)writes the file under the project's `.opossum/mcp/` directory — stable, so a
// detached `up` container keeps a live mount. write is false under `up --dry-run`:
// the plan still shows the mount, but a dry run touches no host files.
func (o *Orchestrator) mcpConfigMount(serviceName string, svc *compose.Service, write bool) (mount string, err error) {
	data, err := o.buildMCPConfig(svc)
	if err != nil {
		return "", err
	}
	if data == nil {
		return "", nil
	}
	host := filepath.Join(o.Project.BaseDir, ".opossum", "mcp", serviceName+".json")
	if write {
		if err := os.MkdirAll(filepath.Dir(host), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(host, data, 0o644); err != nil {
			return "", err
		}
	}
	return host + ":" + mcpMountTarget + ":ro", nil
}
