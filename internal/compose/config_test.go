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
configs:
  appcfg: {}
x-custom: ignore-me
services:
  web:
    image: web
`)
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// version and x-* are not flagged, and neither are networks (namespaced and
	// created) nor volumes — a volume declaration drives `external: true` and a
	// volume's real `name:`, so calling it "not acted on" was wrong, and noisy
	// besides, since almost every real project declares named volumes. `configs`
	// genuinely is ignored, so it's the one that should be listed.
	if got := proj.Unsupported; len(got) != 1 || got[0] != "configs" {
		t.Fatalf("top-level Unsupported = %v, want [configs]", got)
	}
	out, err := RenderConfig(proj)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	if !strings.Contains(out, "(top-level): configs") {
		t.Errorf("config should list top-level ignored keys, got:\n%s", out)
	}
}

// `on-failure` is the one policy opossum can only approximate, because Apple
// `container` doesn't report exit codes. Someone reading their resolved config is
// exactly who needs to know their policy isn't being honoured literally.
func TestConfigNotesTheOnFailureApproximation(t *testing.T) {
	p := writeTemp(t, `
name: demo
services:
  db:
    image: postgres:16
    restart: on-failure:5
  web:
    image: nginx
    restart: always
`)
	proj, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderConfig(proj)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "restart: on-failure:5") || !strings.Contains(out, "restart: always") {
		t.Errorf("the resolved config should show each restart policy, got:\n%s", out)
	}
	if !strings.Contains(out, "can only approximate") || !strings.Contains(out, "exit code") {
		t.Errorf("config should explain the on-failure approximation, got:\n%s", out)
	}
	if !strings.Contains(out, "db uses") {
		t.Errorf("the note should name the service that uses it, got:\n%s", out)
	}
	// The services that ARE honoured exactly must not be implicated.
	if strings.Contains(out, "web uses") || strings.Contains(out, "db, web") {
		t.Errorf("only on-failure services should be named, got:\n%s", out)
	}
}

// A project without on-failure gets no note — the caveat must not become noise
// every user learns to skip.
func TestConfigNoRestartNoteWhenNotNeeded(t *testing.T) {
	p := writeTemp(t, "name: demo\nservices:\n  web:\n    image: nginx\n    restart: always\n")
	proj, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderConfig(proj)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "can only approximate") {
		t.Errorf("no on-failure service means no note, got:\n%s", out)
	}
}
