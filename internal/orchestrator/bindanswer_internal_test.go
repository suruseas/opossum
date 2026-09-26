package orchestrator

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// Evals for what a refused bind is taken to mean.
//
// The probe that asks whether a host port is free binds it, and a bind can be
// refused for more than one reason. "In use" is the one about somebody else's
// listener; the others are about this user or this address, and telling the reader
// "in use" for one of them sends them to look for a listener that is not there.
// The row that made this visible (measured, Apple `container` 1.4.1, macOS 26,
// uid 501): a specific address on a port below 1024 is refused to a user who is
// not root, the runtime refuses the same entry with a message that says so, and
// opossum said "host port 80 already in use" first.
//
// The answers a test needs are ones the machine will not give on demand — the
// suite can run as root, and the push-time gate's container does — so they are
// injected, in the shape a real refusal has. refusedBind builds it, and
// TestARefusalBuiltHereHasTheShapeOfARealOne holds it to a real one.

// refusedBind builds the error a bind gives when the operating system refuses it
// with errno: what net.Listen and net.ListenPacket return, an *net.OpError around
// a *os.SyscallError.
func refusedBind(t *testing.T, network, address string, errno syscall.Errno) error {
	t.Helper()
	var addr net.Addr
	var err error
	if strings.HasPrefix(network, "udp") {
		addr, err = net.ResolveUDPAddr(network, address)
	} else {
		addr, err = net.ResolveTCPAddr(network, address)
	}
	if err != nil {
		t.Fatalf("cannot build a refusal for %s %s: %v", network, address, err)
	}
	return &net.OpError{Op: "listen", Net: network, Addr: addr, Err: os.NewSyscallError("bind", errno)}
}

// answers is a probe with a fixed answer per "<network> <address>" and free for
// everything else. asked records what the probe was asked, in order, so a row can
// say the probe was reached at all.
type bindAnswers struct {
	by    map[string]error
	asked []string
}

func (a *bindAnswers) probe(network, address string) error {
	key := network + " " + address
	a.asked = append(a.asked, key)
	return a.by[key]
}

// The fixture is only worth what it resembles. Every row below injects an error
// built by refusedBind, so if it did not look like the one the standard library
// returns, the rows would pass against an error nothing produces. The address
// held here is a real one, refused by the real operating system.
func TestARefusalBuiltHereHasTheShapeOfARealOne(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	c, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, tc := range []struct {
		name, network, address string
	}{
		{"tcp", "tcp", l.Addr().String()},
		{"udp", "udp", c.LocalAddr().String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			real := bindHostPort(tc.network, tc.address)
			if real == nil {
				t.Fatalf("%s %s bound, and it is held: this row would compare against nothing",
					tc.network, tc.address)
			}
			built := refusedBind(t, tc.network, tc.address, syscall.EADDRINUSE)
			if real.Error() != built.Error() {
				t.Errorf("a refusal built by the test reads %q, the real one %q", built, real)
			}
			var realOp, builtOp *net.OpError
			if !errors.As(real, &realOp) || !errors.As(built, &builtOp) {
				t.Fatalf("both should be an *net.OpError: real %T, built %T", real, built)
			}
			if reflect.TypeOf(realOp.Err) != reflect.TypeOf(builtOp.Err) ||
				reflect.TypeOf(realOp.Addr) != reflect.TypeOf(builtOp.Addr) {
				t.Errorf("the refusal inside is %T at %T, the real one is %T at %T",
					builtOp.Err, builtOp.Addr, realOp.Err, realOp.Addr)
			}
			if !hostPortTaken(real) {
				t.Errorf("a port that is held reads as not taken: %v", real)
			}
		})
	}
}

// Which errors are "somebody else's listener", one row each. Everything but
// EADDRINUSE is not: the port may well be free, and what is refused is this user
// or this address. The two denials are separate rows because EPERM is what a
// sandbox says where a plain user gets EACCES.
func TestOnlyAddressInUseIsTaken(t *testing.T) {
	for _, tc := range []struct {
		name          string
		errno         syscall.Errno
		taken, denied bool
	}{
		{"address in use", syscall.EADDRINUSE, true, false},
		{"permission denied", syscall.EACCES, false, true},
		{"operation not permitted", syscall.EPERM, false, true},
		{"address not available", syscall.EADDRNOTAVAIL, false, false},
		{"address family not supported", syscall.EAFNOSUPPORT, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := refusedBind(t, "tcp", "127.0.0.1:80", tc.errno)
			if got := hostPortTaken(err); got != tc.taken {
				t.Errorf("hostPortTaken(%v) = %v, want %v", err, got, tc.taken)
			}
			if got := hostPortDenied(err); got != tc.denied {
				t.Errorf("hostPortDenied(%v) = %v, want %v", err, got, tc.denied)
			}
		})
	}
	if hostPortTaken(nil) || hostPortDenied(nil) {
		t.Error("a bind that succeeded reads as refused")
	}
	// Wrapped further, as a caller adding context would: errors.Is has to see
	// through it, and a comparison of the error's text would not.
	wrapped := errors.Join(errors.New("context"), refusedBind(t, "tcp", "127.0.0.1:80", syscall.EACCES))
	if !hostPortDenied(wrapped) || hostPortTaken(wrapped) {
		t.Errorf("a refusal wrapped in another error is read wrongly: %v", wrapped)
	}
}

// An entry that writes its host port is a contract, so a refused bind fails the
// run before anything starts. The row is what the reader is told: "already in
// use" only for a listener, and the bind's own reason for anything else.
func TestAnExplicitEntryIsOnlyCalledInUseWhenSomethingHoldsIt(t *testing.T) {
	const (
		inUse  = "host port already in use"
		refuse = "[OPSM-215]"
		advice = "below 1024"
		wider  = "wider door"
	)
	for _, tc := range []struct {
		name    string
		port    string // as written in the file, host side first
		network string
		address string // what the probe is asked
		errno   syscall.Errno
		// wantIn: the entry is refused as in use; wantRefused: as not bindable.
		wantIn, wantRefused bool
		// wantAdvice: the way out for a port below 1024 is offered. noDrop: without
		// the second half of it, dropping the address, which is offered for an IPv4
		// address only.
		wantAdvice bool
		noDrop     bool
		wantWords  []string
	}{
		{name: "a listener holds it (tcp)", port: "127.0.0.1:8080:80", network: "tcp", address: "127.0.0.1:8080",
			errno: syscall.EADDRINUSE, wantIn: true},
		{name: "a listener holds it (udp)", port: "127.0.0.1:8080:80/udp", network: "udp", address: "127.0.0.1:8080",
			errno: syscall.EADDRINUSE, wantIn: true},
		// The same errno on a port below 1024: still a listener, and the advice for a
		// refusal does not apply — nobody is asking for root.
		{name: "a listener holds a port below 1024", port: "127.0.0.1:80:80", network: "tcp", address: "127.0.0.1:80",
			errno: syscall.EADDRINUSE, wantIn: true},

		{name: "a port below 1024 is denied (tcp)", port: "127.0.0.1:80:80", network: "tcp", address: "127.0.0.1:80",
			errno: syscall.EACCES, wantRefused: true, wantAdvice: true, wantWords: []string{"permission denied"}},
		{name: "a port below 1024 is denied (udp)", port: "127.0.0.1:80:80/udp", network: "udp", address: "127.0.0.1:80",
			errno: syscall.EACCES, wantRefused: true, wantAdvice: true, wantWords: []string{"listen udp", "permission denied"}},
		{name: "a port below 1024 is not permitted", port: "127.0.0.1:80:80", network: "tcp", address: "127.0.0.1:80",
			errno: syscall.EPERM, wantRefused: true, wantAdvice: true, wantWords: []string{"operation not permitted"}},
		// Dropping `[::1]` moves the entry to IPv4 as well as to every address, and
		// that has not been measured, so the way out offered is the port alone.
		{name: "the IPv6 loopback below 1024", port: "[::1]:80:80", network: "tcp", address: "[::1]:80",
			errno: syscall.EACCES, wantRefused: true, wantAdvice: true, noDrop: true, wantWords: []string{"[::1]:80"}},
		// A wildcard is not what takes root, so a refusal there (a sandbox saying
		// EPERM for every bind) is not told it needs a specific address's advice.
		{name: "every address below 1024 is refused too", port: "80:80", network: "tcp4", address: ":80",
			errno: syscall.EPERM, wantRefused: true, wantWords: []string{"operation not permitted"}},

		// The advice is for a port below 1024, and the boundary is 1024: 1023 is on
		// the side that takes root, 1024 is not.
		{name: "the highest port that takes root", port: "127.0.0.1:1023:80", network: "tcp", address: "127.0.0.1:1023",
			errno: syscall.EACCES, wantRefused: true, wantAdvice: true},
		{name: "the lowest port that does not", port: "127.0.0.1:1024:80", network: "tcp", address: "127.0.0.1:1024",
			errno: syscall.EACCES, wantRefused: true},
		// Denied on a port that takes no root: still refused, still not "in use",
		// and there is nothing to say about 1024.
		{name: "a port above 1024 is denied", port: "127.0.0.1:8080:80", network: "tcp", address: "127.0.0.1:8080",
			errno: syscall.EACCES, wantRefused: true, wantWords: []string{"permission denied"}},

		// Not a listener and not a denial: the reason is passed through and no advice
		// is guessed at. Port 80 so that only the errno differs from the denied rows.
		{name: "an address that is not available", port: "127.0.0.1:80:80", network: "tcp", address: "127.0.0.1:80",
			errno: syscall.EADDRNOTAVAIL, wantRefused: true, wantWords: []string{syscall.EADDRNOTAVAIL.Error()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: []string{tc.port}},
			}}
			var out bytes.Buffer
			o := New(p, quietShim(t), "opossum", &out)
			a := &bindAnswers{by: map[string]error{
				tc.network + " " + tc.address: refusedBind(t, tc.network, tc.address, tc.errno)}}
			o.probeHostPort = a.probe
			err := o.checkHostPorts([]string{"a"})
			if err == nil {
				t.Fatalf("nothing refused %q, and the bind said %v", tc.port, tc.errno)
			}
			msg := err.Error()
			has := func(s string) bool { return strings.Contains(msg, s) }
			if has(inUse) != tc.wantIn {
				t.Errorf("says %q: %v, want %v — %s:\n%s", inUse, has(inUse), tc.wantIn, tc.errno, msg)
			}
			if has(string(codeHostPortInUse)) != tc.wantIn {
				t.Errorf("carries %s: %v, want %v:\n%s", codeHostPortInUse, has(string(codeHostPortInUse)), tc.wantIn, msg)
			}
			if has(refuse) != tc.wantRefused {
				t.Errorf("carries %s: %v, want %v:\n%s", refuse, has(refuse), tc.wantRefused, msg)
			}
			if has(advice) != tc.wantAdvice {
				t.Errorf("offers the way out for a port below 1024: %v, want %v:\n%s", has(advice), tc.wantAdvice, msg)
			}
			if has(wider) != (tc.wantAdvice && !tc.noDrop) {
				t.Errorf("offers dropping the address: %v, want %v:\n%s", has(wider), tc.wantAdvice && !tc.noDrop, msg)
			}
			for _, w := range tc.wantWords {
				if !has(w) {
					t.Errorf("does not say %q, so the reader is not told why:\n%s", w, msg)
				}
			}
			if tc.wantRefused && !has(`service "a"`) {
				t.Errorf("does not name the service:\n%s", msg)
			}
		})
	}
}

// Both kinds in one file: each entry is told what is true of it, and neither
// hides the other.
func TestAFileWithBothKindsOfRefusalHearsBoth(t *testing.T) {
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{"127.0.0.1:8080:80"}},
		"b": {Image: "web:latest", Ports: []string{"127.0.0.1:80:80"}},
		// A second refusal after the first, so that a message which keeps only the
		// last one of them says nothing about the other.
		"c": {Image: "web:latest", Ports: []string{"127.0.0.1:443:80"}},
	}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	a := &bindAnswers{by: map[string]error{
		"tcp 127.0.0.1:8080": refusedBind(t, "tcp", "127.0.0.1:8080", syscall.EADDRINUSE),
		"tcp 127.0.0.1:80":   refusedBind(t, "tcp", "127.0.0.1:80", syscall.EACCES),
		"tcp 127.0.0.1:443":  refusedBind(t, "tcp", "127.0.0.1:443", syscall.EACCES),
	}}
	o.probeHostPort = a.probe
	err := o.checkHostPorts([]string{"a", "b", "c"})
	if err == nil {
		t.Fatal("nothing refused a file whose two entries are both refused")
	}
	msg := err.Error()
	inUse := strings.Index(msg, string(codeHostPortInUse))
	refused := strings.Index(msg, string(codeHostPortNotBindable))
	if inUse < 0 || refused < 0 {
		t.Fatalf("one of the two is missing (in use at %d, not bindable at %d):\n%s", inUse, refused, msg)
	}
	// Each sentence carries its own service: "a" is the listener's, "b" the denial's.
	inUseText, refusedText := msg[inUse:], msg[refused:]
	if inUse > refused {
		inUseText, refusedText = msg[inUse:], msg[refused:inUse]
	} else {
		inUseText, refusedText = msg[inUse:refused], msg[refused:]
	}
	if !strings.Contains(inUseText, `service "a"`) || strings.Contains(inUseText, `service "b"`) {
		t.Errorf("the in-use refusal is not about service a alone:\n%s", inUseText)
	}
	if !strings.Contains(refusedText, `service "b"`) || !strings.Contains(refusedText, `service "c"`) ||
		strings.Contains(refusedText, `service "a"`) {
		t.Errorf("the not-bindable refusal is not about services b and c alone:\n%s", refusedText)
	}
}

// Nothing is refused where the bind answers: a wildcard below 1024 (which a
// plain user can bind, measured) and a free port on a specific address.
func TestAnEntryTheBindAnswersIsNotRefused(t *testing.T) {
	for _, port := range []string{"80:80", "127.0.0.1:80:80", "127.0.0.1:8080:80", "127.0.0.1:80:80/udp"} {
		t.Run(port, func(t *testing.T) {
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: []string{port}},
			}}
			var out bytes.Buffer
			o := New(p, quietShim(t), "opossum", &out)
			a := &bindAnswers{}
			o.probeHostPort = a.probe
			if err := o.checkHostPorts([]string{"a"}); err != nil {
				t.Errorf("refused %q, which the bind answers: %v", port, err)
			}
			if len(a.asked) == 0 {
				t.Errorf("the bind was never asked about %q, so this row says nothing", port)
			}
		})
	}
}

// A service whose container is already running is left alone: a re-up that
// changes nothing publishes nothing new, and an address that has gone away since
// must not turn every `up` into a refusal. The container here holds a DIFFERENT
// port from the one refused, so the entry is not passed over as its own.
func TestARunningServiceIsNotRefusedForABindItDoesNotNeed(t *testing.T) {
	for _, tc := range []struct {
		name        string
		state       string
		wantRefused bool
	}{
		{"running", "", false},
		{"stopped", "exited", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: []string{"127.0.0.1:80:80"}},
			}}
			var out bytes.Buffer
			o := New(p, shimFor(t, fakeContainer{service: "a", hostPort: 9999, state: tc.state}), "opossum", &out)
			a := &bindAnswers{by: map[string]error{
				"tcp 127.0.0.1:80": refusedBind(t, "tcp", "127.0.0.1:80", syscall.EACCES)}}
			o.probeHostPort = a.probe
			err := o.checkHostPorts([]string{"a"})
			if (err != nil) != tc.wantRefused {
				t.Errorf("a %s service with a refused bind: err = %v, want refused: %v", tc.name, err, tc.wantRefused)
			}
		})
	}
}

// Through Up, because what matters is what the reader gets from the command:
// the refusal arrives before anything is created, under the code that says what
// is true, and not under the one that sends them to free a port nobody holds.
func TestUpRefusesADeniedPortInItsOwnWords(t *testing.T) {
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{"127.0.0.1:80:80"}},
	}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	o.bindHostAddress = func(network, host string) error { return nil }
	a := &bindAnswers{by: map[string]error{
		"tcp 127.0.0.1:80": refusedBind(t, "tcp", "127.0.0.1:80", syscall.EACCES)}}
	o.probeHostPort = a.probe
	err := o.Up(true)
	if err == nil {
		t.Fatal("Up started an entry whose bind is denied")
	}
	if !strings.Contains(err.Error(), string(codeHostPortNotBindable)) {
		t.Errorf("Up refused with %v, want %s", err, codeHostPortNotBindable)
	}
	if strings.Contains(err.Error(), "already in use") || strings.Contains(err.Error(), string(codeHostPortInUse)) {
		t.Errorf("Up says a port is in use that nothing holds:\n%v", err)
	}
}

// A container-only entry whose mirrored port cannot be had is moved, and the
// reader is told why. What is being fixed is the reason: the move itself is what
// the walk has always done, and a port from 1024 up on the same address is one
// the runtime does start.
func TestAMirroredPortIsMovedForTheReasonItWasRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		spec    string
		network string
		errno   syscall.Errno
		suffix  string
		want    string // the reason the reader is given
		notWant string // the reason it must not be
		// bindsOwn is the bind's own words, which the reader has to be given too:
		// the sentence about opossum being refused is the same for every errno.
		bindsOwn string
	}{
		{"a listener holds it", "127.0.0.1:80:80", "tcp", syscall.EADDRINUSE, "", "host port 80 is in use", "will not let opossum bind", ""},
		{"a listener holds it (udp)", "127.0.0.1:80:80/udp", "udp", syscall.EADDRINUSE, "/udp", "host port 80 is in use", "will not let opossum bind", ""},
		{"denied", "127.0.0.1:80:80", "tcp", syscall.EACCES, "", "will not let opossum bind host port 80", "is in use", "permission denied"},
		{"denied (udp)", "127.0.0.1:80:80/udp", "udp", syscall.EACCES, "/udp", "will not let opossum bind host port 80", "is in use", "listen udp 127.0.0.1:80: bind: permission denied"},
		{"not permitted", "127.0.0.1:80:80", "tcp", syscall.EPERM, "", "will not let opossum bind host port 80", "is in use", "operation not permitted"},
		{"not available", "127.0.0.1:80:80", "tcp", syscall.EADDRNOTAVAIL, "", "will not let opossum bind host port 80", "is in use", syscall.EADDRNOTAVAIL.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: []string{tc.spec},
					AutoHostPort: map[string]bool{tc.spec: true}},
			}}
			var out bytes.Buffer
			o := New(p, quietShim(t), "opossum", &out)
			a := &bindAnswers{by: map[string]error{
				tc.network + " 127.0.0.1:80": refusedBind(t, tc.network, "127.0.0.1:80", tc.errno)}}
			o.probeHostPort = a.probe
			o.remapAutoHostPorts([]string{"a"})
			got := p.Services["a"].Ports[0]
			if got == tc.spec {
				t.Fatalf("a is still on %q, and the bind of its mirrored port said %v — it had to move", tc.spec, tc.errno)
			}
			if !strings.HasPrefix(got, "127.0.0.1:") || !strings.HasSuffix(got, ":80"+tc.suffix) {
				t.Errorf("a was moved to %q, which is not the same address and container port", got)
			}
			said := out.String()
			if !strings.Contains(said, "[OPSM-206]") {
				t.Errorf("a moved and the reader was told this instead:\n%s", said)
			}
			if !strings.Contains(said, tc.want) {
				t.Errorf("the move does not say %q:\n%s", tc.want, said)
			}
			if strings.Contains(said, tc.notWant) {
				t.Errorf("the move says %q, which is not what happened:\n%s", tc.notWant, said)
			}
			if !strings.Contains(said, tc.bindsOwn) {
				t.Errorf("the move does not give the bind's own words %q:\n%s", tc.bindsOwn, said)
			}
		})
	}
}

// The other side of the walk: where the bind answers, the entry stays and nothing
// is said. Every row above would pass if the walk moved everything.
func TestAMirroredPortTheBindAnswersStaysPut(t *testing.T) {
	spec := "127.0.0.1:80:80"
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{spec}, AutoHostPort: map[string]bool{spec: true}},
	}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	a := &bindAnswers{}
	o.probeHostPort = a.probe
	o.remapAutoHostPorts([]string{"a"})
	if got := p.Services["a"].Ports[0]; got != spec {
		t.Errorf("a was moved to %q though the bind of %q was answered", got, spec)
	}
	if out.Len() != 0 {
		t.Errorf("the walk said this about a port the bind answers:\n%s", out.String())
	}
	if len(a.asked) != 1 || a.asked[0] != "tcp 127.0.0.1:80" {
		t.Errorf("the probe was asked %v, want the entry's own address once", a.asked)
	}
}

// The move can fail as well: the mirrored port is refused, and the search for a
// port to move to comes back empty. That is a different message (`OPSM-212`), and
// it gives the reason the mirrored port was unavailable, so it has to be the
// bind's reason there too. The search is made to come back empty by a system that
// answers with a port the file has already spoken for and refuses every other.
func TestAnEntryThatCannotBeMovedSaysWhyItsMirrorWasRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		errno   syscall.Errno
		want    string
		notWant string
		// bindsOwn: the bind's own words, given whatever the errno.
		bindsOwn string
	}{
		{"a listener holds it", syscall.EADDRINUSE, "host port 80 is in use", "will not let opossum bind", ""},
		{"denied", syscall.EACCES, "will not let opossum bind host port 80", "is in use", "listen tcp 127.0.0.1:80: bind: permission denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := "127.0.0.1:80:80"
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: []string{spec}, AutoHostPort: map[string]bool{spec: true}},
				"z": {Image: "web:latest", Ports: []string{"127.0.0.1:40000:81"}},
			}}
			var out bytes.Buffer
			o := New(p, quietShim(t), "opossum", &out)
			a := &bindAnswers{by: map[string]error{
				"tcp 127.0.0.1:80": refusedBind(t, "tcp", "127.0.0.1:80", tc.errno)}}
			o.probeHostPort = a.probe
			o.holdPort = func(network, address string, port int) (int, io.Closer, error) {
				if port == 0 {
					return 40000, &tracked{}, nil // a port z's line has spoken for
				}
				return 0, nil, errors.New("busy")
			}
			o.remapAutoHostPorts([]string{"a", "z"})
			said := out.String()
			if !strings.Contains(said, "[OPSM-212]") {
				t.Fatalf("the search was meant to come back empty, and the reader was told this:\n%s", said)
			}
			if !strings.Contains(said, tc.want) {
				t.Errorf("does not say %q:\n%s", tc.want, said)
			}
			if strings.Contains(said, tc.notWant) {
				t.Errorf("says %q, which is not what happened:\n%s", tc.notWant, said)
			}
			if !strings.Contains(said, tc.bindsOwn) {
				t.Errorf("does not give the bind's own words %q:\n%s", tc.bindsOwn, said)
			}
			if got := p.Services["a"].Ports[0]; got != spec {
				t.Errorf("a was moved to %q though there was nowhere to move it", got)
			}
		})
	}
}

// A host port that is not a number has no side of 1024 to be on. The loader does
// not let one through, so this is the check asked of the function directly: the
// advice is about a number, and a bind refused for permission on something that
// is not one gets the reason and nothing else.
func TestAHostPortThatIsNotANumberGetsNoAdviceAboutOne(t *testing.T) {
	msg := unbindableEntry("http", "127.0.0.1:http", "tcp", "a", 0, false,
		refusedBind(t, "tcp", "127.0.0.1:http", syscall.EACCES))
	if strings.Contains(msg, "below 1024") {
		t.Errorf("advice about a number for a port that is not one:\n%s", msg)
	}
	if !strings.Contains(msg, "permission denied") {
		t.Errorf("the reason is gone:\n%s", msg)
	}
}

// Two rows about which running service is left alone, both of which the reader
// meets on a re-up that changes nothing.
//
// First: a running service is passed over even where a running container of this
// project holds the port it asks for. Before this, that came out as `OPSM-201`
// "held by this project's service b" — a listener that the bind never reported.
// The bind said the entry cannot be had at all, whoever holds the port, and a
// re-up that publishes nothing new is not refused for it.
//
// Second: a service that is NOT running is refused for a bind the operating
// system denies even when another service lets go of that very port first. The
// port b lets go of is a port b's container stops holding; it does nothing about
// this user being denied the bind, so the entry is refused before anything starts
// and not by the runtime after b has been recreated.
func TestARefusedBindIsAnsweredWhoeverElseHoldsThePort(t *testing.T) {
	for _, tc := range []struct {
		name        string
		order       []string
		aRunning    bool
		wantRefused bool
	}{
		{"a running service whose port another running service holds", []string{"a", "b"}, true, false},
		{"a stopped service, the port another service is about to let go of", []string{"b", "a"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: []string{"127.0.0.1:80:80"}},
				// b holds host port 80 now and asks for 8081 in the file, so recreating
				// it lets go of 80.
				"b": {Image: "web:latest", Ports: []string{"8081:80"}},
			}}
			cs := []fakeContainer{{service: "b", hostPort: 80}}
			if tc.aRunning {
				cs = append(cs, fakeContainer{service: "a", hostPort: 9999})
			}
			var out bytes.Buffer
			o := New(p, shimFor(t, cs...), "opossum", &out)
			a := &bindAnswers{by: map[string]error{
				"tcp 127.0.0.1:80": refusedBind(t, "tcp", "127.0.0.1:80", syscall.EACCES)}}
			o.probeHostPort = a.probe
			err := o.checkHostPorts(tc.order)
			if (err != nil) != tc.wantRefused {
				t.Fatalf("err = %v, want refused: %v", err, tc.wantRefused)
			}
			if tc.wantRefused {
				if !strings.Contains(err.Error(), string(codeHostPortNotBindable)) || strings.Contains(err.Error(), "already in use") {
					t.Errorf("refused, but not as a bind this user is denied:\n%v", err)
				}
			}
		})
	}
}

// The port in the reason is the port the entry mirrors, not a constant. Every
// row above is about 80, which a reason that always said 80 would satisfy.
func TestTheReasonNamesTheMirroredPortItWasAbout(t *testing.T) {
	spec := "127.0.0.1:443:443"
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{spec}, AutoHostPort: map[string]bool{spec: true}},
	}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	a := &bindAnswers{by: map[string]error{
		"tcp 127.0.0.1:443": refusedBind(t, "tcp", "127.0.0.1:443", syscall.EACCES)}}
	o.probeHostPort = a.probe
	o.remapAutoHostPorts([]string{"a"})
	said := out.String()
	if !strings.Contains(said, "will not let opossum bind host port 443 ") || strings.Contains(said, "host port 80") {
		t.Errorf("the reason does not name port 443:\n%s", said)
	}
}
