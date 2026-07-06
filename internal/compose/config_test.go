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
	// version and x-* are not flagged; networks and volumes are.
	if got := proj.Unsupported; len(got) != 2 || got[0] != "networks" || got[1] != "volumes" {
		t.Fatalf("top-level Unsupported = %v, want [networks volumes]", got)
	}
	out, err := RenderConfig(proj)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	if !strings.Contains(out, "(top-level): networks, volumes") {
		t.Errorf("config should list top-level ignored keys, got:\n%s", out)
	}
}
