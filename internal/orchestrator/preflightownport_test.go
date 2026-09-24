package orchestrator

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

// Evals for whose hands a host port is in when the pre-flight check finds it
// taken.
//
// The check exists for a port something else is listening on: `up` would fail
// at bind time, far from the compose file, so it is refused first and the file's
// line is named. But a running container of this project is not something else.
// `up` deletes and recreates it, which frees the port.
//
// Two questions live here, and they were one while a host port stayed with the
// service that had it. An entry of the service whose container holds the port is
// not looking at a conflict — it is looking at itself. An entry of a DIFFERENT
// service is, unless this run has already let that port go by the time it gets
// there. Which containers have let go is decided by the order services start in,
// because that is the order they are recreated in; and a recreation only lets go
// of the ports the service's own entries no longer publish, because the ones
// they do publish it takes straight back.
//
// A container this run does not touch at all — a service outside the profile, or
// one this command was not asked to start — keeps its port for the whole run,
// and so does another project's container, and so does a stopped one (it is
// holding nothing; whatever holds the port is something else). Those stay
// refused.

// heldHostPort answers with a host port that really cannot be bound, by holding
// it for the length of the test. The probe the check uses names the address
// family, so the listener does too: a port "in use" only on the family nobody
// probes is not in use as far as the check is concerned.
func heldHostPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l.Addr().(*net.TCPAddr).Port
}

// heldUDPPort is heldHostPort for the other protocol. A row that means to be
// about udp has to be about udp on both sides: the container's published port
// and the listener the probe runs into.
func heldUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c.LocalAddr().(*net.UDPAddr).Port
}

// fakeContainer is one answer the shim gives to `inspect`. The zero values are
// the ordinary case — this project, running, tcp — so a row writes down only
// what it is varying, and a row varying nothing reads as the ordinary case.
type fakeContainer struct {
	service   string
	project   string // "" is the project under test
	state     string // "" is running
	proto     string // "" is tcp
	hostPort  int
	container int // 0 is 80
}

// shimFor answers `inspect` for the containers given and with nothing for every
// other name. Separate answers per name are the point: a shim that answered the
// same for all of them would make "the service holding it" and "some other
// service holding it" one fixture, and those are the two sides of this check.
func shimFor(t *testing.T, cs ...fakeContainer) *runtime.Runtime {
	t.Helper()
	var arms strings.Builder
	for _, c := range cs {
		project, state, proto, container := c.project, c.state, c.proto, c.container
		if project == "" {
			project = "demo"
		}
		if state == "" {
			state = "running"
		}
		if proto == "" {
			proto = "tcp"
		}
		if container == 0 {
			container = 80
		}
		fmt.Fprintf(&arms, "      %s.demo.opossum) cat <<'J'\n"+
			`[{"status":{"state":"%s"},"configuration":{"labels":{"opossum.project":"%s"},`+
			`"publishedPorts":[{"containerPort":%d,"hostAddress":"0.0.0.0","hostPort":%d,"proto":"%s"}]}}]`+
			"\nJ\n      ;;\n", c.service, state, project, container, c.hostPort, proto)
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "c.sh")
	body := "#!/bin/sh\ncase \"$1\" in\n  inspect)\n    case \"$2\" in\n" + arms.String() +
		"      *) echo '[]' ;;\n    esac\n  ;;\n  system) echo 'status running' ;;\nesac\nexit 0\n"
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return &runtime.Runtime{Bin: shim}
}

func TestAHostPortThisRunFreesItselfIsNotAConflict(t *testing.T) {
	for _, tc := range []struct {
		name string
		// holder is the service whose container publishes the port under test;
		// "" for a port held by nothing of ours.
		holder string
		// what varies about that container.
		project, state, proto string
		// holderPort shifts the port that container publishes off the one under
		// test, which is how a row says "running, but holding something else".
		holderPort int
		// aPorts is what the holding service's own entries publish: %[1]d is
		// the held port, %[2]d two above it, %[3]d one below, %[4]d one above
		// and %[5]d two below. Empty means "some other port", the state a bare
		// entry is left in once it has been moved off.
		aPorts string
		// zPorts is what the asking service publishes, %[1]d being the held
		// port. Empty is "<held>:90".
		zPorts string
		// udp puts the whole row on the other protocol: the listener the probe
		// finds, and the port the container publishes.
		udp         bool
		order       []string
		wantRefused bool
		// wantNamed is whether the refusal names one of our services as the
		// holder.
		wantNamed bool
	}{
		// The shape a re-up reaches once a host port has moved between
		// services: a's container still holds the number, a's own entry has
		// been moved off it, and z's line asks for it. a is recreated first.
		{name: "a container this run recreates, ahead of the entry", holder: "a", order: []string{"a", "z"}},
		// The same container, the same number — and this run never touches it,
		// so it holds the port while z tries to bind it.
		{name: "a container this run does not start", holder: "a", order: []string{"z"}, wantRefused: true},
		// Recreated, but after the entry that wants its port: nothing has freed
		// it by the time z binds. Refusing here says so before the runtime does.
		{name: "a container this run recreates, behind the entry", holder: "a", order: []string{"z", "a"}, wantRefused: true, wantNamed: true},
		// Named like ours, labelled another project's. Recreating our services
		// does not touch it.
		{name: "a container of another project", holder: "a", project: "other", order: []string{"a", "z"}, wantRefused: true},
		// A stopped container holds nothing, so whatever the probe found is
		// something else — and recreating this one frees none of it.
		{name: "a stopped container of ours", holder: "a", state: "exited", order: []string{"a", "z"}, wantRefused: true},
		// The port it holds is the same number on the other protocol, which is
		// a different published port: letting it go frees nothing here.
		{name: "our container holds that number on the other protocol", holder: "a", proto: "udp", order: []string{"a", "z"}, wantRefused: true},
		// Recreated ahead of the entry, but its own entries still publish the
		// port — as a range does, and as any container `up` leaves alone does.
		// It takes the port straight back, so it was never free.
		//
		// The held port is in the MIDDLE of the range: neither end of the span
		// is the number the container holds, so the row is about what the range
		// covers rather than about an endpoint matching.
		{name: "a container whose own entries still publish it", holder: "a", aPorts: "%[3]d-%[4]d:80-82", order: []string{"a", "z"}, wantRefused: true, wantNamed: true},
		// The same, with the held port at the TOP of the range rather than
		// inside it. A span read with its upper end left out would call this
		// one released; the container was made on a single port and the file
		// grew a range around it afterwards.
		{name: "a container holding the top of the range it publishes", holder: "a", aPorts: "%[5]d-%[1]d:80-82", order: []string{"a", "z"}, wantRefused: true, wantNamed: true},
		// Ahead of the entry, holding that number — but its own entries publish
		// that number on the OTHER protocol, so what it is holding is let go.
		{name: "a container whose entries publish that number on the other protocol", holder: "a", aPorts: "%[1]d:80/udp", order: []string{"a", "z"}},
		// The whole row on udp: the service's own container holds the port its
		// own entry asks for. Same shape as the ordinary re-up, and it must not
		// come out differently for being udp.
		{name: "the container of the very service asking for it, on udp", holder: "z", udp: true, zPorts: "%[1]d:90/udp", order: []string{"a", "z"}},
		// An address this machine will not assign reads as in use, because the
		// probe cannot tell the two apart — this is the case the leniency below
		// is written for, spelled out.
		{name: "a running service, over an address this machine will not assign", holder: "z", holderPort: -1, zPorts: "[::2]:%[1]d:90", order: []string{"a", "z"}},
		// The same number again, written with two leading zeros, and asked of
		// the set the walk fills rather than the service's own: a is ahead and
		// has moved off it. A reader that stripped one leading zero and then
		// read the rest would pass the row below, which has one zero, and fail
		// this one.
		{name: "a container ahead has let it go, and the asking line is padded", holder: "a", zPorts: "00%[1]d:90", order: []string{"a", "z"}},
		// The same, written with a leading zero. The loader takes that spelling
		// and leaves it as written, and the runtime reads it as the same port —
		// so the container reports the number while the file says "0" and the
		// number. Compared as spellings, a service's own port looks like
		// somebody else's and its own re-up is refused against itself.
		{name: "the service's own container holds it, written with a leading zero", holder: "z", zPorts: "0%[1]d:90", order: []string{"a", "z"}},
		// The ordinary re-up: the service asking for the port is the one whose
		// container is holding it. It is not looking at a conflict, it is
		// looking at itself — and this is the case that made skipping a running
		// service's entries wholesale look right for so long.
		{name: "the container of the very service asking for it", holder: "z", order: []string{"a", "z"}},
		// The control: a port in use by something that is not a container of
		// this project at all. Nothing here is exempt.
		{name: "a port nothing of ours holds", order: []string{"a", "z"}, wantRefused: true},
		// Unchanged, and deliberately: a service whose own container is running,
		// over a port none of this project's containers is holding. The probe
		// cannot tell an occupant apart from an address this machine will not
		// assign, and a re-up has been given the benefit of the doubt here for
		// as long as the check has existed. This row is what says that half did
		// not move: what moved is the case where one of OUR containers is the
		// holder, which is a question that has an answer.
		{name: "a running service, over a port nothing of ours holds", holder: "z", holderPort: -1, order: []string{"a", "z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held := heldHostPort(t)
			// The protocol the asking entry is on, which is what the refusal
			// names — as against the one the holding container publishes on,
			// which a row varies to say "the same number, a different port".
			proto := "tcp"
			if tc.udp {
				held, proto = heldUDPPort(t), "udp"
			}
			holderProto := tc.proto
			if holderProto == "" {
				holderProto = proto
			}
			aPorts := fmt.Sprintf("%d:80", held-1)
			if tc.aPorts != "" {
				aPorts = fmt.Sprintf(tc.aPorts, held, held+2, held-1, held+1, held-2)
			}
			zPorts := fmt.Sprintf("%d:90", held)
			if tc.zPorts != "" {
				zPorts = fmt.Sprintf(tc.zPorts, held)
			}
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: []string{aPorts}},
				"z": {Image: "web:latest", Ports: []string{zPorts}},
			}}
			rt := quietShim(t)
			if tc.holder != "" {
				rt = shimFor(t, fakeContainer{
					service: tc.holder, project: tc.project, state: tc.state,
					proto: holderProto, hostPort: held + tc.holderPort,
				})
			}
			var out bytes.Buffer
			o := New(p, rt, "opossum", &out)
			err := o.checkHostPorts(tc.order)
			if refused := err != nil; refused != tc.wantRefused {
				t.Fatalf("checkHostPorts(%v) err = %v; wanted refused = %v. %d is held by %s, "+
					"and a publishes %q.", tc.order, err, tc.wantRefused, held,
					map[bool]string{true: "a container of ours", false: "nothing of ours"}[tc.holder != ""], aPorts)
			}
			if !tc.wantRefused {
				return
			}
			// The refusal names the port and the entry that asked for it: the
			// reader's next move is that line of the file.
			for _, want := range []string{fmt.Sprintf("%d/%s", held, proto), `service "z"`} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not name %q:\n%v", want, err)
				}
			}
			// And it names a holder only where there is one to name. A holder
			// this run cannot speak for — outside the order, stopped, another
			// project's, or holding a different published port — is not one of
			// ours as far as this refusal goes, and naming it would send the
			// reader to a service that is not the problem.
			if named := strings.Contains(err.Error(), "held by this project's service"); named != tc.wantNamed {
				t.Errorf("the refusal %s a holder; wanted it %s:\n%v",
					map[bool]string{true: "names", false: "does not name"}[named],
					map[bool]string{true: "named", false: "unnamed"}[tc.wantNamed], err)
			}
		})
	}
}

// An entry of a service whose container is running is checked like any other
// against a port that container is NOT holding.
//
// Skipping such a service wholesale answers "is this my own port" with "is this
// my own entry", and the two part company as soon as a host port moves: here a's
// line asks for a port b's container holds, and b is recreated after a. Nothing
// frees it in time, and passing it over leaves the runtime to fail on the bind
// with a message about publish specs rather than about the file.
func TestAnEntryOfARunningServiceIsCheckedAgainstAPortItsOwnContainerDoesNotHold(t *testing.T) {
	held := heldHostPort(t)
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		// a is running on some other port, and its line asks for the one b holds.
		"a": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:80", held)}},
		// b held that port as a bare entry and has been moved off it.
		"b": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:80", held-1)}},
	}}
	rt := shimFor(t,
		fakeContainer{service: "a", hostPort: held - 2},
		fakeContainer{service: "b", hostPort: held},
	)
	var out bytes.Buffer
	o := New(p, rt, "opossum", &out)
	err := o.checkHostPorts([]string{"a", "b"})
	if err == nil {
		t.Fatalf("checkHostPorts said nothing. a asks for %d, which b's container holds and "+
			"this run does not recreate until after a has bound it.", held)
	}
	if want := fmt.Sprintf("%d/tcp", held); !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal does not name %q:\n%v", want, err)
	}
}

// The refusal names the service of ours that is holding the port, and says what
// to do about it — which is not the same thing in both cases.
//
// "Free the port or remap it in the compose file" is the wrong thing to ask for
// when what holds it is a container of the very project being started: there is
// nothing for the reader to free that is not the thing they are starting. What
// to do instead turns on whether that holder gives the port up. One whose own
// entries no longer publish it is holding it only until this run recreates it,
// so starting from nothing running places both. One whose entries publish that
// number takes it straight back however often the project is restarted, and
// there a line has to change.
//
// Not where the holder sits in the order, which is the near-enough answer: a
// holder ahead of the entry always takes the port back, so the two agree there
// and part company only further down. The rows below are chosen to part them.
func TestTheRefusalNamesOurOwnServiceHoldingThePort(t *testing.T) {
	for _, tc := range []struct {
		name string
		// aPorts is what the holding service publishes, %[1]d being the port
		// under test and %[2]d the one above it.
		aPorts string
		// udp puts the whole row on the other protocol — the listener the probe
		// finds, the port the container publishes, and the entry asking for it.
		// A row on tcp alone cannot tell "the protocol being asked about" from
		// "tcp", because those are the same thing there.
		udp bool
		// asks is how the asking service writes the port, %d being the number.
		// Empty is "<port>:90".
		asks  string
		order []string
		want  string
	}{
		{
			name:   "the holder starts after the entry",
			aPorts: "%[2]d:80",
			order:  []string{"z", "a"},
			want:   `held by this project's service "a", which this run starts after this entry; take the project down and bring it up again to place both`,
		},
		{
			// Already passed, and still publishing that number — a range over
			// it, which `refuseDuplicateHostPorts` does not read, so nothing
			// else has said the file publishes one port twice.
			name:   "the holder started before the entry",
			aPorts: "%[1]d-%[2]d:80-81",
			order:  []string{"a", "z"},
			want:   "held by this project's service \"a\", whose own `ports` entries publish that number too, so it takes it straight back; change one of the two lines",
		},
		{
			// Starts after the entry AND still publishes the number. Where the
			// holder sits says "restart and both will fit"; what it publishes
			// says it takes the port straight back, and a restart walks into
			// the same failure. The second one is the true answer, so this row
			// is the one that says which question the sentence is about.
			name:   "the holder starts after the entry and publishes it too",
			aPorts: "%[1]d-%[2]d:80-81",
			order:  []string{"z", "a"},
			want:   "held by this project's service \"a\", whose own `ports` entries publish that number too, so it takes it straight back; change one of the two lines",
		},
		{
			// The asking line is padded. The holder is found by the number, so
			// the name and the sentence are the same as they would be without
			// the zeros — a refusal that fell back to the generic "free the
			// port" here would send the reader looking for another process.
			name:   "the asking line is written with a leading zero",
			aPorts: "%[2]d:80",
			asks:   "0%d:90",
			order:  []string{"z", "a"},
			want:   `held by this project's service "a", which this run starts after this entry; take the project down and bring it up again to place both`,
		},
		{
			// Publishing that NUMBER is not publishing that port. The holder's
			// entry is on the other protocol, so it does not take this one
			// back, and a restart does place both.
			name:   "the holder publishes that number on the other protocol",
			aPorts: "%[1]d:80/udp",
			order:  []string{"z", "a"},
			want:   `held by this project's service "a", which this run starts after this entry; take the project down and bring it up again to place both`,
		},
		{
			// The same row read the other way round: everything on udp, and the
			// holder does take it back. A check that asked about tcp whatever
			// was being published would answer "lets go" here and send the
			// reader to restart, which changes nothing.
			//
			// A range again, for the same reason as above: two entries on one
			// host port are refused before this check is reached, and a range
			// is the shape that reaches it.
			name:   "the holder takes it back on udp",
			aPorts: "%[1]d-%[2]d:80-81/udp",
			udp:    true,
			order:  []string{"z", "a"},
			want:   "held by this project's service \"a\", whose own `ports` entries publish that number too, so it takes it straight back; change one of the two lines",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held, proto, asks := heldHostPort(t), "tcp", "%d:90"
			if tc.udp {
				held, proto, asks = heldUDPPort(t), "udp", "%d:90/udp"
			}
			if tc.asks != "" {
				asks = tc.asks
			}
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: []string{fmt.Sprintf(tc.aPorts, held, held+1)}},
				"z": {Image: "web:latest", Ports: []string{fmt.Sprintf(asks, held)}},
			}}
			o := New(p, shimFor(t, fakeContainer{service: "a", hostPort: held, proto: proto}), "opossum", &bytes.Buffer{})
			err := o.checkHostPorts(tc.order)
			if err == nil {
				t.Fatalf("checkHostPorts(%v) said nothing; wanted a refusal naming a", tc.order)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say %q:\n%v", tc.want, err)
			}
		})
	}
}
