package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRenderConfigResolvesAndListsIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "db.env"), []byte("DBPASS=fromfile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(p, []byte(`
name: demo
services:
  db:
    image: postgres:${PG_TAG:-16}
    container_name: legacy
    env_file: db.env
    environment:
      EXTRA: "1"
    healthcheck:
      test: ["CMD", "pg_isready"]
  web:
    image: web
    depends_on:
      db:
        condition: service_healthy
`), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	out, err := RenderConfig(proj)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}

	// Interpolation resolved, env_file folded, condition shown, ignored listed.
	for _, want := range []string{
		"image: postgres:16",
		"DBPASS=fromfile",
		"EXTRA=1",
		"condition: service_healthy",
		"# fields opossum ignores",
		"db: container_name",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("config output missing %q:\n%s", want, out)
		}
	}

	// The YAML body (before the trailing comment block) must be valid YAML.
	body := out[:strings.Index(out, "\n# fields opossum ignores")]
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Errorf("rendered config body is not valid YAML: %v", err)
	}
}

// network_mode round-trips through `opossum config` (it's an applied field, so
// it belongs in the rendered body, not the ignored-fields comment).
func TestRenderConfigShowsNetworkMode(t *testing.T) {
	proj, err := Load(writeTemp(t, `
name: demo
services:
  agent:
    image: agent
    network_mode: none
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	out, err := RenderConfig(proj)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	if !strings.Contains(out, "network_mode: none") {
		t.Errorf("config output should render network_mode: none, got:\n%s", out)
	}
}

// Acted-on networks render in the config body (top-level decl + per-service
// attachment), not as an ignored-fields comment.
func TestRenderConfigShowsNetworks(t *testing.T) {
	proj, err := Load(writeTemp(t, `
name: demo
networks:
  caged:
    internal: true
services:
  agent:
    image: agent
    networks: [caged]
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	out, err := RenderConfig(proj)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	for _, want := range []string{"networks:", "caged:", "internal: true"} {
		if !strings.Contains(out, want) {
			t.Errorf("config output should render %q, got:\n%s", want, out)
		}
	}
}

func TestRenderConfigListsTopLevelIgnored(t *testing.T) {
	p := writeTemp(t, `
version: "3.9"
name: demo
networks:
  backend: {}
volumes:
  data: {}
x-custom: ignore-me
services:
  web:
    image: web
`)
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// version and x-* are not flagged; volumes is still ignored, but networks is
	// now acted on (namespaced/created), so it's no longer listed as ignored.
	if got := proj.Unsupported; len(got) != 1 || got[0] != "volumes" {
		t.Fatalf("top-level Unsupported = %v, want [volumes]", got)
	}
	out, err := RenderConfig(proj)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	if !strings.Contains(out, "(top-level): volumes") {
		t.Errorf("config should list top-level ignored keys, got:\n%s", out)
	}
}
