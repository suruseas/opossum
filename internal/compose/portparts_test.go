package compose

// A short-form port written with empty parts (#842, from the #784 sweep):
// `::80`, `80/`, `:8080:80`, `80:80/`. docker compose (v5.5.0) reads each
// as the ports that are there — the container port 80 alone, or `8080:80`
// and `80:80` — and Apple `container run -p` (1.3.1) refuses the spellings
// as written (`invalid publish value: ::80`, `invalid publish protocol:`
// for a trailing `/`). opossum passed them on as written, or as `:80:80`
// for `::80` (the mirroring of a lone container port kept the empty
// address), so the file loaded and `up` failed at the runtime. A real host
// address is kept; an IPv6 one written without brackets (`::1:8080:80`,
// the address `::1` to docker compose) is bracketed, the only spelling the
// runtime takes (#844) — the any address written as nothing but colons
// (`:::8080:80`, `::` to docker compose) included; a protocol that is
// there is kept. (A
// container port alone is mirrored to `80:80` at load, as it always was.)
// The empty parts are dropped after the spec is checked, so an address
// that is only colons (`:::80`, `::80:80`) is still refused as docker
// compose refuses it (`invalid IP address`), not folded into a port.

import (
	"strings"
	"testing"
)

func TestAPortWithEmptyPartsIsReadAsThePortsThatAreThere(t *testing.T) {
	for _, tc := range []struct{ name, spec, want string }{
		{"an empty address and host port", "::80", "80:80"},
		{"a trailing slash", "80/", "80:80"},
		{"an empty address", ":8080:80", "8080:80"},
		{"a trailing slash after both ports", "80:80/", "80:80"},
		{"an empty address and a trailing slash", "::80/", "80:80"},
		{"a protocol is kept", "80/udp", "80:80/udp"},
		{"a bracketed address is kept", "[::1]:8080:80", "[::1]:8080:80"},
		{"an unbracketed IPv6 address is bracketed", "::1:8080:80", "[::1]:8080:80"},
		{"a longer unbracketed IPv6 address is bracketed", "fe80::1:8080:80", "[fe80::1]:8080:80"},
		{"an IPv4 address is not bracketed", "127.0.0.1:8080:80", "127.0.0.1:8080:80"},
		{"an unbracketed IPv6 address with a protocol", "::1:8080:80/udp", "[::1]:8080:80/udp"},
		{"an unbracketed IPv6 address with the host port left out", "::1::80", "[::1]:80:80"},
		{"an unbracketed IPv6 address with port ranges", "::1:8000-8001:80-81", "[::1]:8000-8001:80-81"},
		{"the any address written as colons", ":::8080:80", "[::]:8080:80"},
		{"a plain pair is untouched", "8080:80", "8080:80"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    ports:\n      - \""+tc.spec+"\"\n"))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := strings.Join(p.Services["web"].Ports, ","); got != tc.want {
				t.Errorf("ports = %q, want %q", got, tc.want)
			}
		})
	}
	// Every entry, not just the first: the empty parts are dropped wherever
	// the port sits in the list.
	t.Run("the second and third entries too", func(t *testing.T) {
		p, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    ports:\n      - \"8080:80\"\n      - \"::443\"\n      - \"9090:90/\"\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if got := strings.Join(p.Services["web"].Ports, ","); got != "8080:80,443:443,9090:90" {
			t.Errorf("ports = %q, want 8080:80,443:443,9090:90", got)
		}
	})
	// Dropped after the check: an address that is only colons is refused,
	// not folded into a port.
	for _, tc := range []struct{ name, spec string }{
		{"three colons before the port", ":::80"},
		{"two colons before both ports", "::80:80"},
	} {
		t.Run(tc.name+" is still refused", func(t *testing.T) {
			got := loadErr(t, "services:\n  web:\n    image: alpine\n    ports:\n      - \""+tc.spec+"\"\n")
			if !strings.Contains(got, "must be an IP address") {
				t.Errorf("want the address refused, got:\n%s", got)
			}
		})
	}
	// The long form assembles its spec from the keys that are there, so an
	// empty host_ip and no protocol leave nothing behind without help.
	p, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    ports:\n      - {target: 80, published: 8080, host_ip: \"\"}\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := strings.Join(p.Services["web"].Ports, ","); got != "8080:80" {
		t.Errorf("long form = %q, want 8080:80", got)
	}
}
