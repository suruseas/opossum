package orchestrator

import (
	"testing"

	"github.com/suruseas/opossum/internal/runtime"
)

// A published port is two numbers that are not interchangeable, and the pieces
// around them are not either.
//
// Both of the functions below were found by a sweep that exchanges arguments of
// the same kind and runs this package's tests: swapping the host port with the
// container port, or the host IP with the protocol, changed nothing those tests
// could see. The fixtures were the reason — every one of them used a mapping
// like 8080:8080, where the two numbers are the same, and left the IP and the
// protocol empty, where the two strings are the same. The format was never
// wrong; the questions asked of it could not have told.
//
// One of the two was caught elsewhere: exchanging formatPorts' two ports fails
// TestPsCLI over in cmd/opossum, which had an asymmetric mapping all along. So
// the sweep's answer is about the package it ran, not about the suite — worth
// remembering when reading any of its survivors. A pin here is still the right
// place for it: this is where the function lives, and the failure it gives is
// about the function rather than about a table three layers up.
//
// Every case here is asymmetric on purpose: the host port differs from the
// container port, the IP is present, and the protocol is present.
func TestAPublishedPortSaysWhichSideIsWhich(t *testing.T) {
	t.Run("rewriting the host port", func(t *testing.T) {
		for _, c := range []struct {
			name, spec string
			host       int
			want       string
			ok         bool
		}{
			{"a bare mapping", "8080:80", 9090, "9090:80", true},
			{"with a host address", "127.0.0.1:8080:80", 9090, "127.0.0.1:9090:80", true},
			{"with a protocol", "8080:80/udp", 9090, "9090:80/udp", true},
			{"with both", "127.0.0.1:8080:80/udp", 9090, "127.0.0.1:9090:80/udp", true},
			// An IPv6 host address, which is the only shape where the host part
			// holds more than one colon — and so the only one that can tell
			// "the last colon" from "the first". compose normalizes `[::1]::80`
			// into this form and marks it for auto-assignment, so it reaches
			// here.
			{"an IPv6 host address", "[::1]:8080:80/udp", 9090, "[::1]:9090:80/udp", true},
			// A container-side range cannot be remapped onto one host port, and
			// saying so is the difference between leaving the spec alone and
			// inventing one.
			{"a container range", "8080:80-82", 9090, "", false},
			// A host-side range is answered rather than refused, which is safe
			// only because the caller never gets here with one: hostPortBinding
			// rejects a range before this is reached. Pinned so that a change on
			// either side has to face the other.
			{"a host range, which the caller filters first", "8000-8002:80", 9090, "9090:80", true},
		} {
			got, ok := withHostPort(c.spec, c.host)
			if ok != c.ok {
				t.Errorf("%s: withHostPort(%q, %d) ok = %v, want %v", c.name, c.spec, c.host, ok, c.ok)
				continue
			}
			if got != c.want {
				t.Errorf("%s: withHostPort(%q, %d) = %q, want %q", c.name, c.spec, c.host, got, c.want)
			}
		}
	})

	t.Run("rendering what is published", func(t *testing.T) {
		got := formatPorts([]runtime.PortMapping{
			{HostAddress: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Proto: "tcp"},
			{HostAddress: "127.0.0.1", HostPort: 5433, ContainerPort: 5432, Proto: "udp"},
		})
		want := "0.0.0.0:8080->80/tcp, 127.0.0.1:5433->5432/udp"
		if got != want {
			t.Errorf("formatPorts =\n %q\nwant\n %q", got, want)
		}
	})
}
