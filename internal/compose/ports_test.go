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

// A spec that names no protocol names tcp, as docker compose reads it, so
// `8080` and `8080/tcp` are one published port. They used to be two: the
// comparison was made on the spec as written, and `/tcp` written in one of
// them was enough to make the pair look like two ports. The second was then
// published on a host port nobody asked for, and where both named the host
// port the runtime refused the pair outright (`host ports for different
// publish port specs may not overlap`) — a file docker compose starts.
//
// What is kept is the first spec as it was written: the question the key
// answers is whether two entries are the same port, not what to hand the
// runtime.
func TestLoadReadsAMissingProtocolAsTCP(t *testing.T) {
	for _, tc := range []struct {
		name  string
		specs []string
		want  []string
	}{
		{"the protocol written second", []string{"8080/tcp", "8080"}, []string{"8080:8080/tcp"}},
		{"the protocol written first", []string{"8080", "8080/tcp"}, []string{"8080:8080"}},
		{"both host ports named", []string{"8080:8080/tcp", "8080:8080"}, []string{"8080:8080/tcp"}},
		// A different protocol is a different port, and stays two.
		{"udp beside tcp", []string{"8080/udp", "8080/tcp"}, []string{"8080:8080/udp", "8080:8080/tcp"}},
		{"udp beside a spec with no protocol", []string{"8080/udp", "8080"}, []string{"8080:8080/udp", "8080:8080"}},
		// Unchanged: the same spelling twice, and two ports that differ.
		{"the same spelling twice", []string{"8080/tcp", "8080/tcp"}, []string{"8080:8080/tcp"}},
		{"two ports", []string{"8080", "9090"}, []string{"8080:8080", "9090:9090"}},
		// The host port is compared too: these are two published ports that
		// happen to share a container port.
		{"one container port on two host ports", []string{"8080:80/tcp", "9090:80"}, []string{"8080:80/tcp", "9090:80"}},
		// A protocol written in the short form is read without regard to
		// case and settled in lower case, where docker compose settles it
		// (v5.5.1 writes `8080/TCP` back as `protocol: tcp`) — so a pair
		// that differs only in how the protocol is spelled is one port.
		// Both host ports named is the shape that cannot fall back on a free
		// port: the runtime refused the pair outright.
		{"the protocol in upper case", []string{"8080/TCP", "8080/tcp"}, []string{"8080:8080/tcp"}},
		{"upper case beside no protocol at all", []string{"8080/TCP", "8080"}, []string{"8080:8080/tcp"}},
		{"upper case with both host ports named", []string{"8080:8080/TCP", "8080:8080/tcp"}, []string{"8080:8080/tcp"}},
		{"mixed case", []string{"8080/Tcp", "8080/tCP"}, []string{"8080:8080/tcp"}},
		// Not only tcp: the case is settled for whatever protocol is written.
		{"udp in upper case", []string{"8080/UDP", "8080/udp"}, []string{"8080:8080/udp"}},
		// And settling the case does not fold two protocols together.
		{"upper-case udp beside tcp", []string{"8080/UDP", "8080/tcp"}, []string{"8080:8080/udp", "8080:8080/tcp"}},
		// The protocol is the only part settled. A host address written in
		// another case is left as two entries here — whether two spellings
		// of one address are one published port is a question of its own,
		// and not one this answers (docker compose's answer to it is not
		// measured). The row is here to say where the settling stops.
		{"one host address in two cases", []string{"[::FFFF:1.2.3.4]:8080:80/tcp", "[::ffff:1.2.3.4]:8080:80/tcp"}, []string{"[::FFFF:1.2.3.4]:8080:80/tcp", "[::ffff:1.2.3.4]:8080:80/tcp"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "compose.yaml")
			body := "services:\n  web:\n    image: nginx\n    ports:\n"
			for _, spec := range tc.specs {
				body += "      - \"" + spec + "\"\n"
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			got := p.Services["web"].Ports
			if len(got) != len(tc.want) {
				t.Fatalf("ports = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("ports = %v, want %v", got, tc.want)
					break
				}
			}
		})
	}
}

// Which of the pair opossum may move is the pair's answer, not each spec's: a
// host port named in either of them is a host port the file chose, however the
// protocol was spelled.
func TestAProtocolSpellingDoesNotMakeAHostPortOpossumsToMove(t *testing.T) {
	for _, tc := range []struct {
		name  string
		specs []string
		kept  string
		auto  bool
	}{
		{"both bare", []string{"8080/tcp", "8080"}, "8080:8080/tcp", true},
		{"the host port named in the second", []string{"8080/tcp", "8080:8080"}, "8080:8080/tcp", false},
		{"the host port named in the first", []string{"8080:8080/tcp", "8080"}, "8080:8080/tcp", false},
		// Three entries, the named one last: what the pair answered has to
		// survive the third. Whichever spec is kept, it is the first — an
		// answer that moved to a later spelling would leave the file's own
		// host port marked as opossum's to move.
		{"two bare and then the host port named", []string{"8080", "8080/tcp", "8080:8080"}, "8080:8080", false},
		{"the host port named first, then two bare", []string{"8080:8080", "8080/tcp", "8080"}, "8080:8080", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "compose.yaml")
			body := "services:\n  web:\n    image: nginx\n    ports:\n"
			for _, spec := range tc.specs {
				body += "      - \"" + spec + "\"\n"
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			svc := p.Services["web"]
			if len(svc.Ports) != 1 || svc.Ports[0] != tc.kept {
				t.Fatalf("ports = %v, want [%s]", svc.Ports, tc.kept)
			}
			if got := svc.AutoHostPort[svc.Ports[0]]; got != tc.auto {
				t.Errorf("AutoHostPort[%q] = %v, want %v (AutoHostPort = %v)", svc.Ports[0], got, tc.auto, svc.AutoHostPort)
			}
			// And no answer left behind for a spec that is not published:
			// a key that moved would mark a port nobody asked about.
			for spec := range svc.AutoHostPort {
				if spec != svc.Ports[0] {
					t.Errorf("AutoHostPort holds %q, which is not among the published ports %v", spec, svc.Ports)
				}
			}
		})
	}
}

// Where the case of a protocol is settled decides one shape, and only one:
// a long-form entry writing `protocol: TCP` beside a short-form `8080/tcp`.
// docker compose settles the case in the parser for the short form and not
// for the long form's own key (measured on v5.5.1: a file with one of each
// keeps two entries and `up` fails to bind the second, where `protocol: tcp`
// beside the same short form is one entry and starts), so opossum settles it
// in the same place. Settling it where two entries are compared instead would
// fold this pair and start a file docker compose does not.
func TestTheLongFormProtocolKeepsItsCase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	body := "services:\n  web:\n    image: nginx\n    ports:\n" +
		"      - {target: 8080, published: \"8080\", protocol: TCP}\n" +
		"      - \"8080:8080/tcp\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := p.Services["web"].Ports
	want := []string{"8080:8080/TCP", "8080:8080/tcp"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("ports = %v, want %v", got, want)
	}
}

// The same pair with the short form written in capitals too. This one used to
// be a single published port — both entries said `/TCP`, so they matched as
// written — and is two now that the short form settles in lower case while
// the long form's key does not. The file stops starting, and docker compose
// does not start it either (measured on v5.5.1: `config` returns both and
// `up` fails to bind the second), which is why it is left this way.
func TestTheLongFormProtocolNoLongerMatchesAShortFormInCapitals(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	body := "services:\n  web:\n    image: nginx\n    ports:\n" +
		"      - {target: 8080, published: \"8080\", protocol: TCP}\n" +
		"      - \"8080:8080/TCP\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := p.Services["web"].Ports
	want := []string{"8080:8080/TCP", "8080:8080/tcp"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("ports = %v, want %v", got, want)
	}
}
