package compose

import (
	"path/filepath"
	"strings"
	"testing"
)

// Multiple -f files merge with docker compose semantics: scalars later-win,
// mappings merge by key, most sequences append, command/entrypoint replace, and a
// service only in the override is added.
func TestLoadFilesMerge(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n"+
		"  web:\n"+
		"    image: web:1\n"+
		"    ports: [\"8080:80\"]\n"+
		"    environment: {A: \"1\", B: \"2\"}\n"+
		"    command: [\"run\", \"base\"]\n")
	mustWriteFile(t, over, "services:\n"+
		"  web:\n"+
		"    image: web:2\n"+
		"    ports: [\"9090:90\"]\n"+
		"    environment: {B: \"20\", C: \"3\"}\n"+
		"    command: [\"run\", \"over\"]\n"+
		"  cache:\n"+
		"    image: cache:1\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	web := p.Services["web"]
	if web.Image != "web:2" { // scalar: later wins
		t.Errorf("image = %q, want web:2", web.Image)
	}
	if len(web.Ports) != 2 { // sequence: appended
		t.Errorf("ports should append (both), got %v", web.Ports)
	}
	if got := strings.Join(web.Command, " "); got != "run over" { // command: replaced
		t.Errorf("command should be replaced, got %q", got)
	}
	env := strings.Join(web.Environment, ",") // map: merged per key (sorted A,B,C)
	for _, want := range []string{"A=1", "B=20", "C=3"} {
		if !strings.Contains(env, want) {
			t.Errorf("environment should merge per key, missing %q in %q", want, env)
		}
	}
	if _, ok := p.Services["cache"]; !ok {
		t.Error("a service only in the override should be added")
	}
}

// environment merges by key across mixed forms (base map + override list): no base
// key is lost, and duplicate list keys collapse (docker compose parity).
func TestLoadFilesMergeEnvMixedForm(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n  web:\n    image: w\n    environment: {A: \"1\", B: \"2\"}\n")
	mustWriteFile(t, over, "services:\n  web:\n    image: w\n    environment:\n      - B=20\n      - C=3\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	// Environment is a sorted KEY=value slice.
	want := []string{"A=1", "B=20", "C=3"}
	got := []string(p.Services["web"].Environment)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("mixed-form env merge = %v, want %v (no key loss, later wins)", got, want)
	}
}

// A port restated identically in the override collapses to one entry.
// Volumes are deduped only at merge time (unlike ports, which are re-deduped
// during load), so this is the sole guard against an override restating a mount
// producing a doubled -v.
func TestLoadFilesDedupsVolumes(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n  web:\n    image: w\n    volumes: [\"data:/data\"]\n")
	mustWriteFile(t, over, "services:\n  web:\n    image: w\n    volumes: [\"data:/data\", \"logs:/logs\"]\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if vols := p.Services["web"].Volumes; len(vols) != 2 { // data:/data (deduped) + logs:/logs
		t.Errorf("an override restating a volume should dedup, got %v", vols)
	}
}

// entrypoint replaces (not appends) across files — it's a single value, not a
// list to accumulate. Previously only `command` replacement was tested.
func TestLoadFilesReplacesEntrypoint(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n  web:\n    image: w\n    entrypoint: [\"/base\", \"--old\"]\n")
	mustWriteFile(t, over, "services:\n  web:\n    image: w\n    entrypoint: [\"/override\"]\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if ep := []string(p.Services["web"].Entrypoint); len(ep) != 1 || ep[0] != "/override" {
		t.Errorf("entrypoint should be replaced by the override, got %v", ep)
	}
}

func TestLoadFilesDedupsPorts(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n  web:\n    image: w\n    ports: [\"8080:80\"]\n")
	mustWriteFile(t, over, "services:\n  web:\n    image: w\n    ports: [\"8080:80\", \"9090:90\"]\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	ports := p.Services["web"].Ports
	if len(ports) != 2 { // 8080:80 (deduped) + 9090:90
		t.Errorf("identical ports should dedup, got %v", ports)
	}
}

func TestDiscoverOverride(t *testing.T) {
	dir := t.TempDir()
	if got := DiscoverOverride(dir); got != "" {
		t.Errorf("no override present, got %q", got)
	}
	mustWriteFile(t, filepath.Join(dir, "compose.override.yaml"), "services: {}\n")
	if got := DiscoverOverride(dir); got == "" {
		t.Error("should discover compose.override.yaml")
	}
}
