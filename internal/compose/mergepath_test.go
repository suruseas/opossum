package compose

import (
	"path/filepath"
	"testing"
)

// The merge's field rules (environment/labels by key, networks by name) are
// keyed on a name — and a service, network or volume is named by whoever
// wrote the file. A service called `networks` is a service, not the field:
// an override that blanks its `command:` blanks it (docker compose v5.4.0,
// measured), instead of the "an empty entry keeps the other side" rule that
// belongs to a service's networks. Before, the rule fired on the key alone.
func TestAServiceNamedNetworksIsMergedAsAService(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n"+
		"  networks:\n"+
		"    image: alpine\n"+
		"    command: [sleep, \"1\"]\n"+
		"    environment:\n"+
		"      A: \"1\"\n")
	mustWriteFile(t, over, "services:\n"+
		"  networks:\n"+
		"    command:\n"+
		"    environment:\n"+
		"      B: \"2\"\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	svc := p.Services["networks"]
	if svc == nil {
		t.Fatalf("the service should still be there, got %v", p.Services)
	}
	if len(svc.Command) != 0 {
		t.Errorf("a null override clears command for a service, whatever its name; got %v", svc.Command)
	}
	// The field rules still apply one level down: this service's own
	// environment merges by key like any other service's.
	if got := len(svc.Environment); got != 2 {
		t.Errorf("environment should merge by key (A and B), got %v", svc.Environment)
	}
}

// A declaration named `networks` — a network, a volume, a secret — is not a
// service: for declarations, an override that writes a key with nothing after
// it (`internal:`, `external:`, `file:`) leaves the base's value, as docker
// compose does (v5.5.0, measured). The `networks` rule happens to read that
// way, so these must keep going through it; listing the other collections
// beside `services` would send them down "null wins" instead — the shape an
// earlier draft of this fix had, and #725's review caught on all three.
func TestADeclarationNamedNetworksKeepsItsValuesAcrossANullOverride(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n"+
		"  web:\n"+
		"    image: alpine\n"+
		"    networks: [networks]\n"+
		"    volumes: [\"networks:/d\"]\n"+
		"    secrets: [networks]\n"+
		"networks:\n"+
		"  networks:\n"+
		"    internal: true\n"+
		"volumes:\n"+
		"  networks:\n"+
		"    external: true\n"+
		"secrets:\n"+
		"  networks:\n"+
		"    file: ./s.txt\n")
	mustWriteFile(t, over, "networks:\n"+
		"  networks:\n"+
		"    internal:\n"+
		"volumes:\n"+
		"  networks:\n"+
		"    external:\n"+
		"secrets:\n"+
		"  networks:\n"+
		"    file:\n")
	mustWriteFile(t, filepath.Join(dir, "s.txt"), "x\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("a secret whose override writes `file:` with nothing after it still has the base's file; LoadFiles: %v", err)
	}
	if !p.Networks["networks"].Internal {
		t.Errorf("network declaration: internal: true should survive `internal:` null, got %+v", p.Networks["networks"])
	}
	if !p.Volumes["networks"].External {
		t.Errorf("volume declaration: external: true should survive `external:` null, got %+v", p.Volumes["networks"])
	}
}

// The path, not the last name, decides: a service that happens to be called
// `volumes` is an element of `services`, and its own `networks:` field one
// level down still merges by network name. A merge that only remembered the
// nearest name would read `volumes` as the collection and skip the rule.
func TestAServiceNamedAfterACollectionKeepsItsOwnFieldRules(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n"+
		"  volumes:\n"+
		"    image: alpine\n"+
		"    networks:\n"+
		"      back:\n"+
		"        aliases: [db-alias]\n"+
		"networks:\n"+
		"  back: {}\n")
	mustWriteFile(t, over, "services:\n"+
		"  volumes:\n"+
		"    networks: [back]\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	svc := p.Services["volumes"]
	if svc == nil {
		t.Fatalf("service missing: %v", p.Services)
	}
	found := false
	for _, u := range svc.Unsupported {
		if u == "networks.back.aliases" {
			found = true
		}
	}
	if !found {
		t.Errorf("the service's own networks field should merge by name (aliases kept), got %v", svc.Unsupported)
	}
}
