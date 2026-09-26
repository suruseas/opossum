package orchestrator

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// boundAddress reports the address a held host port is actually bound to. The
// closer is the socket itself, so this reads the answer out of the kernel
// rather than out of the argument that was passed in — the whole question here
// is whether the two agree.
func boundAddress(t *testing.T, c io.Closer) (host, kind string) {
	t.Helper()
	var a net.Addr
	switch s := c.(type) {
	case *net.TCPListener:
		a, kind = s.Addr(), "tcp"
	case *net.UDPConn:
		a, kind = s.LocalAddr(), "udp"
	default:
		t.Fatalf("a held host port came back as %T, which this cannot read an address out of", c)
	}
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		t.Fatalf("a held host port is bound to %q, which is not an address and a port: %v", a, err)
	}
	return host, kind
}

// loopbackIsTwoAddresses says whether this machine's two loopback addresses can
// hold the same port at the same time. Every row below that names one of them
// rests on it: `127.0.0.1` and `::1` are separate addresses, so a port held on
// one is free on the other (measured on macOS 26 and in the Linux container the
// push-time gate runs in).
//
// Checked, rather than assumed, and reported as a failure rather than skipped:
// a machine where this does not hold makes the rows below say nothing, and a
// skip reads as a pass.
func loopbackIsTwoAddresses(t *testing.T) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("this machine would not bind 127.0.0.1: %v", err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	l6, err := net.Listen("tcp", fmt.Sprintf("[::1]:%d", port))
	if err != nil {
		t.Fatalf("host port %d is held on 127.0.0.1 and would not bind on ::1 (%v), so this "+
			"machine does not treat the two loopback addresses as separate — the rows below "+
			"cannot tell the two questions apart here.", port, err)
	}
	l6.Close()
}

// The probe binds the address the entry wrote. A host port free on one address
// can be held on another, so asking about the wrong one answers a question the
// file did not ask: the runtime will publish on the address in the entry, and
// that is the address that has to be free.
//
// Wildcards keep their own answer — `host` empty and the IPv4 family — which is
// what a bare or `0.0.0.0` entry is published on. `[::]` is published as an IPv6
// listener (measured on 1.4.1, and written down beside the duplicate-port check)
// and is probed on IPv4 all the same: narrower than the runtime, unchanged by
// this, and filed.
//
// The family comes from the address for anything that names one: Go reads it
// off the literal, so "tcp" with `[::1]` binds IPv6 while "tcp4" with the same
// address is refused outright ("no suitable address found", measured). Which is
// why the "4" below is confined to the wildcard branch.
func TestTheProbeAsksTheAddressTheEntryWrote(t *testing.T) {
	for _, tc := range []struct {
		name    string
		network string
		address string
		want    string
	}{
		{"a loopback address", "tcp", "127.0.0.1:0", "127.0.0.1"},
		{"the IPv6 loopback address", "tcp", "[::1]:0", "::1"},
		{"an entry with no address", "tcp", ":0", "0.0.0.0"},
		{"the wildcard spelled out", "tcp", "0.0.0.0:0", "0.0.0.0"},
		// The IPv6 wildcard is probed on IPv4 like the others, which is the
		// narrowness above rather than a claim about where it publishes.
		// Unchanged by asking the entry's address: `::` names no host.
		{"the IPv6 wildcard", "tcp", "[::]:0", "0.0.0.0"},
		{"a loopback address on udp", "udp", "127.0.0.1:0", "127.0.0.1"},
		{"the IPv6 loopback address on udp", "udp", "[::1]:0", "::1"},
		{"an entry with no address on udp", "udp", ":0", "0.0.0.0"},
		// Nothing in opossum reaches the probe with an address it did not build
		// out of a host and a port, so this row is about a caller that has yet
		// to exist: loopback is the narrowest thing to bind when the address
		// cannot be read, and binding every address would be the widest.
		{"an address that cannot be read", "tcp", "not-an-address", "127.0.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, c, err := holdHostPort(tc.network, tc.address, 0)
			if err != nil {
				t.Fatalf("holdHostPort(%q, %q, 0) = %v", tc.network, tc.address, err)
			}
			defer c.Close()
			if n == 0 {
				t.Errorf("holdHostPort(%q, %q, 0) answered with host port 0", tc.network, tc.address)
			}
			got, kind := boundAddress(t, c)
			if got != tc.want {
				t.Errorf("an entry writing %q was probed on %s, want %s — the probe has to bind "+
					"the address the entry will publish on.", tc.address, got, tc.want)
			}
			// And on the protocol the entry publishes: a port is in use per
			// protocol as well as per address, so a tcp answer about a udp entry
			// is an answer about a different port. The address alone cannot say
			// this — both sockets are bound to the same one.
			if want := strings.TrimSuffix(tc.network, "4"); kind != want {
				t.Errorf("an entry writing %q on %q was probed with a %s socket, want %s.",
					tc.address, tc.network, kind, want)
			}
		})
	}
}

// A candidate host port is in use or free per address, and the answer the walk
// needs is the one for the entry's own address. The four rows are the two
// addresses a hold can be on against the two an entry can write: rows 1 and 2
// tell "ask the entry's address" apart from "ask loopback", and row 4 tells it
// apart from "ask both loopback addresses".
//
// Not from "ask every address": a hold on `::1` stops neither the IPv4 wildcard
// nor a dual-stack one (measured), so no row here would notice a probe that
// asked a wildcard instead. What notices that is the table above, which reads
// the address the probe actually bound.
func TestAHostPortHeldOnAnotherAddressIsFreeOnTheEntrysOwn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		holdOn   string // the address something else is listening on
		written  string // the address the entry writes
		answered bool   // whether the probe should hand the port over
	}{
		// The form this changes: the entry names an address of its own, and a
		// candidate port is held somewhere else. Probing loopback passed over a
		// candidate that is free on the address the entry publishes on. (Whether
		// the mirrored port is free is asked earlier, by askHostPort, and was
		// always asked on the entry's own address — this row is the candidate
		// search.)
		{"held on loopback, the entry writes ::1", "127.0.0.1", "[::1]", true},
		// And the same address really is asked: a hold there is seen.
		{"held on ::1, the entry writes ::1", "[::1]", "[::1]", false},
		{"held on loopback, the entry writes loopback", "127.0.0.1", "127.0.0.1", false},
		// Not "both loopback addresses": an entry on `127.0.0.1` is not
		// refused by a hold on `::1`.
		{"held on ::1, the entry writes loopback", "[::1]", "127.0.0.1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loopbackIsTwoAddresses(t)
			l, err := net.Listen("tcp", tc.holdOn+":0")
			if err != nil {
				t.Fatalf("this machine would not bind %s: %v", tc.holdOn, err)
			}
			defer l.Close()
			port := l.Addr().(*net.TCPAddr).Port
			got, c, err := holdHostPort("tcp", fmt.Sprintf("%s:%d", tc.written, port), port)
			if err == nil {
				defer c.Close()
			}
			if answered := err == nil; answered != tc.answered {
				verb := "was refused"
				if answered {
					verb = fmt.Sprintf("was answered with %d", got)
				}
				want := "refused: that address holds it"
				if tc.answered {
					want = "answered: nothing holds it on that address"
				}
				t.Errorf("host port %d is held on %s, and an entry writing %s asked for it and %s "+
					"(%v); want %s.", port, tc.holdOn, tc.written, verb, err, want)
			}
			if err == nil && got != port {
				t.Errorf("asked for host port %d on %s and got %d", port, tc.written, got)
			}
		})
	}
}

// An address this machine will not bind reaches the probe as an error rather
// than as an answer about some other address. A file naming one is refused
// before the walk — but not when the service's container is already running,
// which is the one way such an entry still gets this far. By then the walk has
// read the mirrored port as in use, since nothing binds on that address; an
// answer about loopback handed it a port to move to, and the entry went to a
// second port on the same unbindable address.
func TestAnAddressThisMachineWillNotBindIsAnErrorAndNotAnAnswer(t *testing.T) {
	const gone = "192.0.2.7" // TEST-NET-1: assigned to no interface here
	if l, err := net.Listen("tcp", gone+":0"); err == nil {
		l.Close()
		t.Fatalf("this machine binds %s, so it cannot stand for an address that has gone away.", gone)
	}
	n, c, err := holdHostPort("tcp", gone+":0", 0)
	if err == nil {
		c.Close()
		t.Fatalf("an entry writing %s was answered with host port %d, so the walk would move it "+
			"and say it was published somewhere it cannot be.", gone, n)
	}
	// The bind's own error, not one made up here: it names the address it was
	// refused for. The walk passes over this error rather than printing it, so
	// this guards what the error IS, not what a reader sees.
	if !strings.Contains(err.Error(), gone) {
		t.Errorf("the probe refused an entry on %s with %q, which does not name the address, so it "+
			"is not the bind's own answer about it.", gone, err)
	}
}

// What that error means for the reader, one level up: the entry stays where the
// file mirrored it and nothing is said. Probing loopback instead answered with
// a port that is free there, moved the entry to it, and printed `OPSM-206` —
// telling the reader a host port nothing is listening on was in use, and naming
// a second port on the same address the machine will not bind either. The
// runtime's own refusal follows in both cases; only one of them adds a sentence
// that is not true.
//
// Through the walk, because the address the probe is asked about is the walk's
// to pass on: the check that refuses such a file outright runs earlier and is
// skipped for a service whose container is already running.
func TestAnEntryOnAnAddressThatHasGoneAwayIsLeftWhereItIs(t *testing.T) {
	const gone = "192.0.2.7" // TEST-NET-1: assigned to no interface here
	if l, err := net.Listen("tcp", gone+":0"); err == nil {
		l.Close()
		t.Fatalf("this machine binds %s, so it cannot stand for an address that has gone away.", gone)
	}
	// Any number: nothing binds on this address, so which one it is cannot
	// change the answer.
	const port = 51234
	mirror := fmt.Sprintf("%s:%[2]d:%[2]d", gone, port)
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{mirror},
			AutoHostPort: map[string]bool{mirror: true}},
	}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	o.remapAutoHostPorts([]string{"a"})
	if got := p.Services["a"].Ports[0]; got != mirror {
		t.Errorf("a publishes %q, want the entry left on %q — there is no other port on an "+
			"address this machine will not bind.", got, mirror)
	}
	if said := out.String(); said != "" {
		t.Errorf("the walk said this about an address that cannot be bound at all:\n%s", said)
	}
}

// The walk's other question — is the host port the file mirrored free? — is
// asked about the entry's address as well, and the two questions being about the
// same address is what makes their answers comparable.
//
// Two rows, because the two wrong addresses are refused by different holds: a
// hold on `127.0.0.1` is what a loopback question would see, and a hold on the
// IPv4 wildcard is what a wildcard question would see. Neither is seen from
// `::1`, where these entries publish, so both rows leave the entry alone —
// and each row's own check says which hold it is that the wrong question would
// have tripped over. (A wildcard hold does not stop a loopback bind, nor the
// other way round: Go sets SO_REUSEADDR, so one row cannot stand for both.)
//
// This much was already so before the probe asked the entry's address: the
// guard is here because the sentence above is now a claim the probe rests on.
func TestTheMirroredPortIsAskedAboutOnTheEntrysOwnAddress(t *testing.T) {
	for _, tc := range []struct {
		name    string
		network string // the family the hold needs
		holdOn  string
	}{
		{"held on loopback", "tcp4", "127.0.0.1"},
		{"held on every IPv4 address", "tcp4", "0.0.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loopbackIsTwoAddresses(t)
			l, err := net.Listen(tc.network, tc.holdOn+":0")
			if err != nil {
				t.Fatalf("this machine would not bind %s: %v", tc.holdOn, err)
			}
			defer l.Close()
			port := l.Addr().(*net.TCPAddr).Port
			// Why this row is green has to be that `::1` is free, not that the
			// hold missed: on the address it is on, that port must read as taken.
			if again, err := net.Listen(tc.network, fmt.Sprintf("%s:%d", tc.holdOn, port)); err == nil {
				again.Close()
				t.Fatalf("host port %d is held on %s and still binds there, so this row would "+
					"pass with the address ignored.", port, tc.holdOn)
			}
			mirror := fmt.Sprintf("[::1]:%[1]d:%[1]d", port)
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: []string{mirror},
					AutoHostPort: map[string]bool{mirror: true}},
			}}
			var out bytes.Buffer
			o := New(p, quietShim(t), "opossum", &out)
			o.remapAutoHostPorts([]string{"a"})
			if got := p.Services["a"].Ports[0]; got != mirror {
				t.Errorf("a publishes %q, want the entry left on %q — host port %d is held on %s, "+
					"and the entry publishes on ::1.", got, mirror, port, tc.holdOn)
			}
			if said := out.String(); said != "" {
				t.Errorf("the walk said this about a host port nothing holds on the entry's "+
					"address:\n%s", said)
			}
		})
	}
}

// The other side of the walk: where the port IS held on the entry's own address,
// the entry still moves and still says so. Rows that only check "left alone"
// would all pass if the probe answered "in use" for everything, and this is the
// row that would not — the entry it moves to has to be on the same address.
func TestAnEntryWhosePortIsHeldOnItsOwnAddressStillMoves(t *testing.T) {
	loopbackIsTwoAddresses(t)
	l, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Fatalf("this machine would not bind ::1: %v", err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	mirror := fmt.Sprintf("[::1]:%[1]d:%[1]d", port)
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{mirror},
			AutoHostPort: map[string]bool{mirror: true}},
	}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	o.remapAutoHostPorts([]string{"a"})
	got := p.Services["a"].Ports[0]
	if got == mirror {
		t.Fatalf("a is still on %q, and host port %d is held on the address that entry publishes "+
			"on — it had to move.", mirror, port)
	}
	if !strings.HasPrefix(got, "[::1]:") {
		t.Errorf("a was moved to %q, which is not on the address the file wrote — a moved entry "+
			"keeps its own address.", got)
	}
	if said := out.String(); !strings.Contains(said, "[OPSM-206]") {
		t.Errorf("a moved from host port %d to %q and the reader was told this instead:\n%s",
			port, got, said)
	}
}

// The protocol is asked about too: a host port is in use per protocol as well as
// per address, and `8080/tcp` and `8080/udp` are two ports. Both rows are needed
// because dropping the protocol from the question goes wrong in both directions —
// a tcp hold reads as in use for a udp entry, and a udp hold reads as free.
//
// This pair guards the question that decides whether an entry moves at all
// (askHostPort). The protocol of the candidate search is guarded in the table
// above, which reads the kind of socket the probe came back with.
//
// Held on the entry's own address in both rows, so what is under test is the
// protocol and nothing else.
func TestTheProtocolOfTheEntryIsAskedAboutToo(t *testing.T) {
	for _, tc := range []struct {
		name    string
		holdUdp bool // whether the hold is on udp, the protocol the entry publishes
		moves   bool
	}{
		{"a tcp hold is not this entry's port", false, false},
		{"a udp hold is", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var port int
			if tc.holdUdp {
				c, err := net.ListenPacket("udp", "127.0.0.1:0")
				if err != nil {
					t.Fatalf("this machine would not bind udp on 127.0.0.1: %v", err)
				}
				defer c.Close()
				port = c.LocalAddr().(*net.UDPAddr).Port
			} else {
				l, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatalf("this machine would not bind tcp on 127.0.0.1: %v", err)
				}
				defer l.Close()
				port = l.Addr().(*net.TCPAddr).Port
			}
			// Why this row is what it is: the port has to be free on the OTHER
			// protocol, or "left alone" and "moved" would not separate the two.
			if c, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
				c.Close()
				if tc.holdUdp {
					t.Fatalf("host port %d is supposed to be held on udp and still binds there.", port)
				}
			} else if !tc.holdUdp {
				t.Fatalf("host port %d is held on tcp and would not bind on udp (%v), so this row "+
					"says nothing about the protocol.", port, err)
			}
			mirror := fmt.Sprintf("127.0.0.1:%[1]d:%[1]d/udp", port)
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: []string{mirror},
					AutoHostPort: map[string]bool{mirror: true}},
			}}
			var out bytes.Buffer
			o := New(p, quietShim(t), "opossum", &out)
			o.remapAutoHostPorts([]string{"a"})
			got := p.Services["a"].Ports[0]
			if moved := got != mirror; moved != tc.moves {
				held := "tcp"
				if tc.holdUdp {
					held = "udp"
				}
				t.Errorf("a publishes %q after the walk; host port %d is held on %s and the entry "+
					"publishes it on udp, so it should have %s. What the reader was told:\n%s",
					got, port, held, map[bool]string{true: "moved", false: "stayed"}[tc.moves],
					out.String())
			}
		})
	}
}
