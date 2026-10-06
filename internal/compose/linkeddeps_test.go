package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What `links:` and `network_mode: service:<name>` name is a dependency of the service, as docker compose reads it (v5.5.1 `config`:
// `depends_on: {b: {condition: service_started, required: true}}`, the condition of a `depends_on` the file writes for the same service kept):
// the alias after the `:` of a link is not a service, one `network_mode` names one service, and a value that names none (`host`, `container:`)
// is no dependency. A target the file does not define is refused for a service with no `profiles:`, and left to the command that
// finds the service active for one that has them (#1802).
func TestLinksAndNetworkModeServiceAreDependenciesOfTheService(t *testing.T) {
	const bc = "  b:\n    image: y\n  c:\n    image: z\n"
	for _, tc := range []struct {
		name, body, want string // want: the services a depends on with their conditions, or "error", or "loads" with the linked names
	}{
		{"links with an alias", "services:\n  a:\n    image: x\n    links: [\"b:alias\", c]\n" + bc, "b=service_started c=service_started"},
		{"network_mode service", "services:\n  a:\n    image: x\n    network_mode: service:b\n" + bc, "b=service_started"},
		{"the condition a depends_on writes is kept", "services:\n  a:\n    image: x\n    links: [b]\n    depends_on:\n      b: {condition: service_healthy}\n  b:\n    image: y\n    healthcheck: {test: [CMD, \"true\"]}\n", "b=service_healthy"},
		{"one service named three ways is one dependency", "services:\n  a:\n    image: x\n    links: [b]\n    network_mode: service:b\n    volumes_from: [b]\n  b:\n    image: y\n", "b=service_started"},
		{"network_mode host is no dependency", "services:\n  a:\n    image: x\n    network_mode: host\n" + bc, ""},
		{"network_mode container is no dependency", "services:\n  a:\n    image: x\n    network_mode: container:other\n" + bc, ""},
		{"a link with two colons is one name", "services:\n  a:\n    image: x\n    links: [\"b:c:d\"]\n" + bc, "error"},
		{"a link with no name", "services:\n  a:\n    image: x\n    links: [\"\"]\n", "error"},
		{"a network_mode service with no name", "services:\n  a:\n    image: x\n    network_mode: \"service:\"\n", "error"},
		{"a link to an undefined service", "services:\n  a:\n    image: x\n    links: [nosuch]\n", "error"},
		{"a network_mode service that is undefined", "services:\n  a:\n    image: x\n    network_mode: service:nosuch\n", "error"},
		{"a link to an undefined service of a gated service", "services:\n  a:\n    image: x\n    profiles: [g]\n    links: [nosuch]\n", "loads"},
		{"a link to a gated service of a service that has none", "services:\n  a:\n    image: x\n    links: [b]\n  b:\n    image: y\n    profiles: [g]\n", "b=service_started"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "c.yaml")
			if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			project, err := LoadFiles([]string{p}, nil)
			if tc.want == "error" {
				if err == nil || !strings.Contains(err.Error(), "depends on undefined service") {
					t.Fatalf("want it refused as an undefined dependency, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if tc.want == "loads" {
				return
			}
			var got []string
			for _, d := range project.Services["a"].DependsOn {
				got = append(got, d.Name+"="+d.Condition)
			}
			if strings.Join(got, " ") != tc.want {
				t.Errorf("a depends on %q, want %q", strings.Join(got, " "), tc.want)
			}
		})
	}
}
