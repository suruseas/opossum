package compose

// A port is checked at load the way docker compose (v5.5.0) validates one
// (exit codes and full output read, 60 fixtures): a container port that is a
// word (`80:a`), a fraction (`80.5`), 0, 65536, padded (`" 80"`, `"80: 80"`),
// hex, a boolean; a host port above 65535 or a range written high-low; a
// host address that is not an IP (`localhost`, `*`, `1.2.3`, a fourth `:`
// part); a protocol other than tcp/udp/sctp (`80/foo`, `80/tcp/udp`); a
// container range with a host range of another length, or with a single
// host port; no container port (`:`, `80:`). opossum used to pass every one
// of these to `container run -p` as written (`ports: [a]` became `a:a`).
//
// Stricter than docker, on purpose, and named here: `+80` (docker's integer
// parser takes a sign) and, in the long form, `target: 0`/`65536`, a
// `published` that is not a port or a range (`a`, `8080:80`), and a
// `protocol` docker does not check there — each key is checked by its own
// name, and each of those fails at `up` under docker anyway. A key the long
// form does not have is still read past (docker refuses it), and so is
// `host_ip: ""` (docker refuses; here it reads as left out).

import (
	"strings"
	"testing"
)

func TestAPortIsCheckedAtLoadTheWayDockerChecksIt(t *testing.T) {
	ctr := "the container port must be a number from 1 to 65535, or a range `low-high` of them"
	host := "the host port must be a number from 0 to 65535, or a range `low-high` of them"
	addr := "the host address before the ports must be an IP address, as in `127.0.0.1:8080:80`"
	proto := "the protocol after `/` must be tcp, udp or sctp"
	ranges := "a container port range needs a host port range of the same length, as in `8000-8010:80-90`"
	none := "no container port — write `host:container`, or the container port alone"
	ltarget := "must be one container port, a number from 1 to 65535 — a range, a host port or a protocol is written in the short form"
	lhost := "must be a number from 0 to 65535, or a range `low-high` of them"
	for _, tc := range []struct{ name, port, want string }{
		{"container port is a word", `"80:a"`, ctr},
		{"container port is a fraction", `80.5`, ctr},
		{"container port 0", `"0"`, ctr},
		{"container port 65536", `65536`, ctr},
		{"container port negative", `"-1"`, ctr},
		{"container port padded", `" 80"`, ctr},
		{"container port padded after the colon", `"80: 80"`, ctr},
		{"container port in hex", `"0x50"`, ctr},
		{"container port is a boolean", `true`, ctr},
		{"container port with a sign", `"+80"`, ctr},
		{"container range written high-low", `"8080:90-80"`, ctr},
		{"host port is a word", `"a:80"`, host},
		{"host port 65536", `"65536:80"`, host},
		{"host port padded", `"80 :80"`, host},
		{"host range written high-low", `"90-80:80"`, host},
		{"a fourth colon part", `"80:80:80:80"`, addr},
		{"a host name", `"localhost:80:80"`, addr},
		{"a star", `"*:80:80"`, addr},
		{"three octets", `"1.2.3:80:80"`, addr},
		{"protocol foo", `"80/foo"`, proto},
		{"two slashes", `"80:80/tcp/udp"`, proto},
		{"ranges of different length", `"80-90:80-91"`, ranges},
		{"single host port for a container range", `"80:80-90"`, ranges},
		{"only a colon", `":"`, none},
		{"host port only", `"80:"`, none},
		// The long form: each key named, before the spec is assembled.
		{"long target is a word", `{target: a}`, "target " + ltarget},
		{"long target 0", `{target: 0}`, "target " + ltarget},
		{"long target 65536", `{target: 65536}`, "target " + ltarget},
		{"long target negative", `{target: -1}`, "target " + ltarget},
		{"long target is a fraction", `{target: 80.5}`, "target " + ltarget},
		{"long target is a range", `{target: "80-90", published: "8080-8090"}`, "target " + ltarget},
		{"long target carries a host port", `{target: "8080:80"}`, "target " + ltarget},
		{"long target carries a protocol", `{target: "80/tcp"}`, "target " + ltarget},
		{"long published is a word", `{target: 80, published: a}`, "published " + lhost},
		{"long published 65536", `{target: 80, published: 65536}`, "published " + lhost},
		{"long published range with a word", `{target: 80, published: "8000-a"}`, "published " + lhost},
		{"long published carries a container port", `{target: 80, published: "8080:80"}`, "published " + lhost},
		{"long protocol foo", `{target: 80, protocol: foo}`, "protocol must be tcp, udp or sctp"},
		{"long host_ip is a word", `{target: 80, host_ip: bad}`, "host_ip must be an IP address, as in `127.0.0.1` or `::1`"},
		// The entry is numbered, so the second of two is named as such — in
		// either form.
		{"the second entry", `"8080:80", "80:a"`, "ports entry 2 of 2: " + ctr},
		{"the second entry, long form", `"8080:80", {target: a}`, "ports entry 2 of 2: target " + ltarget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, "services:\n  web:\n    image: alpine\n    ports: ["+tc.port+"]\n")
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
			if !strings.Contains(got, "ports entry") {
				t.Errorf("the entry should be numbered, got:\n%s", got)
			}
		})
	}
	// No message reads the port back: a `${VAR}` may hold anything, and
	// each branch that refuses is given the canary in its own position.
	const canary = "ghp-canary-do-not-print"
	t.Setenv("OPOSSUM_TEST_PORT", canary)
	for _, tc := range []struct{ name, port string }{
		{"container port", `"${OPOSSUM_TEST_PORT}"`},
		{"host port", `"${OPOSSUM_TEST_PORT}:80"`},
		{"host address", `"${OPOSSUM_TEST_PORT}:8080:80"`},
		{"protocol", `"80/${OPOSSUM_TEST_PORT}"`},
		{"no container port", `"${OPOSSUM_TEST_PORT}:"`},
		{"long target", `{target: "${OPOSSUM_TEST_PORT}"}`},
		{"long published", `{target: 80, published: "${OPOSSUM_TEST_PORT}"}`},
		{"long host_ip", `{target: 80, host_ip: "${OPOSSUM_TEST_PORT}"}`},
		{"long protocol", `{target: 80, protocol: "${OPOSSUM_TEST_PORT}"}`},
	} {
		t.Run("does not read back the "+tc.name, func(t *testing.T) {
			got := loadErr(t, "services:\n  web:\n    image: alpine\n    ports: ["+tc.port+"]\n")
			if strings.Contains(got, canary) {
				t.Errorf("the message reads the value back:\n%s", got)
			}
		})
	}

	// Taken, as docker takes them, and kept as written (normalization is
	// the loader's, measured elsewhere).
	for _, tc := range []struct{ name, port, want string }{
		{"host:container", `"8080:80"`, "8080:80"},
		{"container only", `"80"`, "80:80"},
		{"bare number", `80`, "80:80"},
		{"leading zero", `"080:80"`, "080:80"},
		{"host port 0", `"0:80"`, "0:80"},
		{"empty host port", `":80"`, "80:80"},
		{"ranges of one length", `"80-90:80-90"`, "80-90:80-90"},
		{"a host range for one container port", `"8000-8010:80"`, "8000-8010:80"},
		{"a bare container range", `"80-90"`, "80-90:80-90"},
		{"a range of one", `"8080-8080:80"`, "8080-8080:80"},
		{"udp", `"80:80/udp"`, "80:80/udp"},
		{"protocol in capitals", `"80:80/TCP"`, "80:80/TCP"},
		{"sctp", `"80:80/sctp"`, "80:80/sctp"},
		{"an empty protocol is dropped", `"80:80/"`, "80:80"},
		{"an IPv4 address", `"127.0.0.1:8080:80"`, "127.0.0.1:8080:80"},
		{"the any address", `"0.0.0.0:80:80"`, "0.0.0.0:80:80"},
		{"a bracketed IPv6 address", `"[::1]:80:80"`, "[::1]:80:80"},
		{"an unbracketed IPv6 address is bracketed", `"::1:80:80"`, "[::1]:80:80"},
		{"an address with the host port left out", `"127.0.0.1::80"`, "127.0.0.1:80:80"},
		{"long form", `{target: 80, published: 8080, protocol: tcp}`, "8080:80/tcp"},
		{"long form, published as a range", `{target: 80, published: "8000-8010"}`, "8000-8010:80"},
		{"long form, published 0", `{target: 80, published: 0}`, "0:80"},
		{"long form, an IPv6 host_ip", `{target: 80, host_ip: "::1"}`, "[::1]:80:80"},
		{"long form, protocol in capitals", `{target: 80, protocol: TCP}`, "80:80/TCP"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    ports: ["+tc.port+"]\n"))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := p.Services["web"].Ports; len(got) != 1 || got[0] != tc.want {
				t.Errorf("ports = %v, want [%s]", got, tc.want)
			}
		})
	}
}
