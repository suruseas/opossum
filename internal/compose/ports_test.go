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
		if got, _ := normalizePort(in); got != want {
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

// A port whose host side opossum supplied is marked, so `up` may move it if the
// mirrored host port is taken. A port the file names explicitly is NOT marked —
// that's the user's declared contract and must fail loudly instead.
func TestLoadMarksAutoHostPorts(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yaml")
	mustWrite(t, p, `
name: demo
services:
  web:
    image: w
    ports:
      - "3000"
      - "8080:80"
      - "9000/udp"
      - ":7000"
`)
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	svc := proj.Services["web"]
	auto := svc.AutoHostPort
	for _, spec := range []string{"3000:3000", "9000:9000/udp", "7000:7000"} {
		if !auto[spec] {
			t.Errorf("%q came from a container-only entry and should be movable, got %v", spec, auto)
		}
	}
	if auto["8080:80"] {
		t.Errorf("an explicit mapping must never be marked movable, got %v", auto)
	}
}

// If any declaration of the same resolved spec named the host port, the user did
// choose it — so it must not be treated as opossum's to move.
func TestLoadExplicitDeclarationWinsOverBare(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yaml")
	mustWrite(t, p, `
name: demo
services:
  web:
    image: w
    ports:
      - "3000"
      - "3000:3000"
`)
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	svc := proj.Services["web"]
	if len(svc.Ports) != 1 || svc.Ports[0] != "3000:3000" {
		t.Fatalf("expected the two forms to collapse to one spec, got %v", svc.Ports)
	}
	if svc.AutoHostPort["3000:3000"] {
		t.Errorf("an explicitly declared host port must not be movable, got %v", svc.AutoHostPort)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The "explicit wins" rule has to hold in BOTH declaration orders. Written the
// other way round the naive `auto[n] = mirrored` would happen to be right, so
// only this order proves the accumulation is doing anything.
func TestLoadExplicitFirstThenBare(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yaml")
	mustWrite(t, p, `
name: demo
services:
  web:
    image: w
    ports:
      - "3000:3000"
      - "3000"
`)
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if svc := proj.Services["web"]; svc.AutoHostPort["3000:3000"] {
		t.Errorf("an explicit declaration first must still win, got %v", svc.AutoHostPort)
	}
}

// `ip::80` is compose's "bind this interface, engine picks the host port". It
// used to reach the runtime with an empty host port; now it mirrors like `:80`
// and is opossum's to move. Same for the long form that omits `published`.
func TestLoadHostIPWithoutPublishedPort(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yaml")
	mustWrite(t, p, `
name: demo
services:
  web:
    image: w
    ports:
      - "127.0.0.1::80"
      - target: 90
        host_ip: 127.0.0.1
`)
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	svc := proj.Services["web"]
	for _, want := range []string{"127.0.0.1:80:80", "127.0.0.1:90:90"} {
		found := false
		for _, got := range svc.Ports {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("expected %q among %v", want, svc.Ports)
		}
		if !svc.AutoHostPort[want] {
			t.Errorf("%q left the host port to the engine and should be movable, got %v", want, svc.AutoHostPort)
		}
	}
}
