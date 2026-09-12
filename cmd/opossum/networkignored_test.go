package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The unit test reads Project.Unsupported; this one reads the two places a
// person sees it — `config`'s ignored list and the --verbose warning — since a
// field that is collected but never printed is still dropped in silence.
func TestDroppedNetworkFieldsReachTheReader(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	body := "name: p\nservices:\n  web:\n    image: app:1\n    networks:\n      back:\n        aliases: [db-alias]\nnetworks:\n  back:\n    ipam:\n      driver: default\n      config:\n        - subnet: 10.0.0.0/24\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := run(t, "config")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	for _, want := range []string{"networks.back.aliases", "networks.back.ipam.driver"} {
		if !strings.Contains(out, want) {
			t.Errorf("config should list %s among the ignored fields, got:\n%s", want, out)
		}
	}

	// The one-line note a plain up prints — the reader who never types
	// --verbose — names the dropped field too. This and the multi-file case
	// below are the two readers the first version of this test did not pass
	// through (declared as such in the PR; measured by its review).
	out, err = run(t, "up", "--no-build", "--no-supervisor", "--dry-run")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if !strings.Contains(out, "note:") || !strings.Contains(out, "networks.back.aliases") {
		t.Errorf("a plain up should note the dropped alias by name, got:\n%s", out)
	}

	over := filepath.Join(dir, "over.yaml")
	if err := os.WriteFile(over, []byte("services:\n  api:\n    image: app:1\n    networks:\n      back:\n        aliases: [api-alias]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, "-f", "compose.yaml", "-f", over, "config")
	if err != nil {
		t.Fatalf("config with two files: %v", err)
	}
	for _, want := range []string{"networks.back.aliases", "networks.back.ipam.driver"} {
		if !strings.Contains(out, want) {
			t.Errorf("the merged project should still list %s, got:\n%s", want, out)
		}
	}

	out, err = run(t, "--verbose", "up", "--no-build", "--no-supervisor", "--dry-run")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if !strings.Contains(out, "[OPSM-502]") || !strings.Contains(out, "networks.back.aliases") {
		t.Errorf("--verbose should warn per field with the dropped alias named, got:\n%s", out)
	}
	if !strings.Contains(out, "[OPSM-501]") || !strings.Contains(out, "networks.back.ipam.driver") {
		t.Errorf("--verbose should warn about the top-level ipam.driver by name, got:\n%s", out)
	}
}
