package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// Apple's `container` requires a host port, so a bare container port gets one
// (mirroring the container port); fully-specified mappings pass through.
func TestNormalizePort(t *testing.T) {
	cases := map[string]string{
		"3000":              "3000:3000",
		"3000/udp":          "3000:3000/udp",
		"3000-3005":         "3000-3005:3000-3005",
		"3000-3005/udp":     "3000-3005:3000-3005/udp",
		":80":               "80:80", // empty host (docker: random) -> mirror
		"8080:80":           "8080:80",
		"8080:80/udp":       "8080:80/udp",
		"127.0.0.1:8080:80": "127.0.0.1:8080:80",
		"0.0.0.0:5432:5432": "0.0.0.0:5432:5432",
		"[::1]:8080:80":     "[::1]:8080:80",
		"[::1]:8080:80/udp": "[::1]:8080:80/udp",
		"":                  "",
	}
	for in, want := range cases {
		if got := normalizePort(in); got != want {
			t.Errorf("normalizePort(%q) = %q, want %q", in, got, want)
		}
	}
}

// Load applies the normalization, so a compose file with a bare port yields a
// runnable host:container spec.
func TestLoadNormalizesBarePorts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	body := "services:\n  web:\n    image: nginx\n    ports:\n      - \"3000\"\n      - \"8080:80\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := p.Services["web"].Ports
	want := []string{"3000:3000", "8080:80"}
	if len(got) != len(want) {
		t.Fatalf("ports = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ports[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// Ports that collapse to the same spec only after normalization ("3000" and
// "3000:3000") are deduped, so the runtime doesn't get a doubled -p.
func TestLoadDedupsNormalizedPorts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	body := "services:\n  web:\n    image: nginx\n    ports:\n      - \"3000\"\n      - \"3000:3000\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Services["web"].Ports; len(got) != 1 || got[0] != "3000:3000" {
		t.Errorf("ports = %v, want [3000:3000]", got)
	}
}

// The long mapping form of `ports:` ({target, published, protocol, host_ip}) is
// normalized to the same short spec as the string form — including a numeric
// (unquoted) target, a target-only entry (host port mirrored), and a host_ip.
func TestLoadLongFormPorts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	body := `services:
  web:
    image: nginx
    ports:
      - target: 80
        published: 8080
        protocol: tcp
      - target: 5432          # no published -> host port mirrors it
      - target: 90
        published: 9090
        host_ip: 127.0.0.1
      - target: 100
        published: 9100
        host_ip: "::1"        # IPv6 host must be bracketed
      - "7000:70"             # short form still works alongside
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatalf("long-form ports should load: %v", err)
	}
	got := []string(p.Services["web"].Ports)
	want := []string{"8080:80/tcp", "5432:5432", "127.0.0.1:9090:90", "[::1]:9100:100", "7000:70"}
	if len(got) != len(want) {
		t.Fatalf("ports = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ports[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// A long-form port entry missing a target is rejected (rather than producing a
// bogus spec).
func TestLoadLongFormPortMissingTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	body := "services:\n  web:\n    image: nginx\n    ports:\n      - published: 8080\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("a long-form port with no target should error")
	}
}
