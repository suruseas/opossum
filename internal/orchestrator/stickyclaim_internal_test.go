package orchestrator

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/runtime"

	"github.com/suruseas/opossum/internal/compose"
)

// Evals for what happens when a container already holds a host port and a line
// of the file starts asking for it.
//
// A port a running container of this project publishes is kept across re-ups:
// that is what makes the host port — and so the config hash — stable, and it is
// the point of the notice about moving a mirrored port. But the file is the
// project. Once a line fixes that port, it belongs to the service that wrote it
// down, and the container still holding it from before is the thing that moves.
// Keeping it would publish the port twice, and the pair would be refused after
// opossum had said nothing about it.
//
// docker compose has the same stickiness (`up` does not recreate an unchanged
// running container, so its host port does not move either, measured on v5.5.1)
// and does not reach this at all, because it never lets a bare entry take the
// number a line writes down. The end-to-end question — a is running, z is added
// asking for a's port — answers rc=0 there. So this is a difference to close,
// and which way to close it follows from the claims: a host port the file fixes
// is claimed wherever in the file it sits, whichever services this command
// starts.

// freePortsForSticky answers with n ports nothing is listening on, so the only
// thing deciding the answer is what the file claims. The listeners are all held
// open until the last number is chosen, so no two of them can be the same port:
// closing in between would leave that number free to be handed out again, and
// two numbers a test means to be different would quietly be one.
//
// It says nothing about their order. The OS hands these out however it likes —
// ascending on one kernel, not on another — so a test that needs "the port
// below" or "the port above" builds that number itself.
func freePortsForSticky(t *testing.T, n int) []int {
	t.Helper()
	ports := make([]int, 0, n)
	for tries := 0; len(ports) < n; tries++ {
		if tries == 64 {
			t.Fatalf("the OS did not name %d ports between 1026 and 65534 in 64 tries", n)
		}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		// Every listener stays open until the last number is chosen — the ones
		// passed over here too, so passing one over cannot make it come back.
		//
		// Away from both ends because a row builds the port below and the port
		// above one of these, and 65535 has no port above it. Ephemeral ports
		// are well inside, so this passes over nothing in practice.
		if p := l.Addr().(*net.TCPAddr).Port; p > 1025 && p < 65535 {
			ports = append(ports, p)
		}
	}
	return ports
}

func freePortForSticky(t *testing.T) int {
	t.Helper()
	return freePortsForSticky(t, 1)[0]
}

// shimHolding answers `inspect` with a running container publishing hostPort
// for containerPort — but only for the service named. Every other name gets
// nothing, which is what lets a row have one service running and another not.
// A shim that answered the same for every name would make "this run handed the
// port out" and "a container holds it" look alike.
func shimHolding(t *testing.T, service string, hostPort, containerPort int, proto string) *runtime.Runtime {
	t.Helper()
	dir := t.TempDir()
	shim := filepath.Join(dir, "c.sh")
	body := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n  inspect)\n    case \"$2\" in\n      %s.demo.opossum) cat <<'J'\n"+
		`[{"status":{"state":"running"},"configuration":{"labels":{"opossum.project":"demo"},`+
		`"publishedPorts":[{"containerPort":%d,"hostAddress":"0.0.0.0","hostPort":%d,"proto":"%s"}]}}]`+
		"\nJ\n      ;;\n      *) echo '[]' ;;\n    esac\n  ;;\n  system) echo 'status running' ;;\nesac\nexit 0\n",
		service, containerPort, hostPort, proto)
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return &runtime.Runtime{Bin: shim}
}

// stickyRun sets up `a` with one bare entry mirroring `port`, a running
// container of a already publishing it, and `z` holding whatever the row says.
// It gives back what a ended up publishing and what was said.
func stickyRun(t *testing.T, port int, zPorts []string, zAuto bool) (published, said string) {
	t.Helper()
	mirror := fmt.Sprintf("%[1]d:%[1]d", port)
	z := &compose.Service{Image: "web:latest", Ports: zPorts}
	if zAuto {
		z.AutoHostPort = map[string]bool{}
		for _, p := range zPorts {
			z.AutoHostPort[p] = true
		}
	}
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{mirror}, AutoHostPort: map[string]bool{mirror: true}},
		"z": z,
	}}
	var out bytes.Buffer
	// a's container is running and publishes the mirrored port, which is the
	// state the stickiness is about — and a's alone. A shim that answered the
	// same for z would make z's own entry look like a port a container holds,
	// and a row about what the FILE asks for would be measuring something else.
	o := New(p, shimHolding(t, "a", port, port, "tcp"), "opossum", &out)
	o.remapAutoHostPorts([]string{"a", "z"})
	return p.Services["a"].Ports[0], out.String()
}

func TestAPortAContainerHoldsIsKeptUntilALineOfTheFileAsksForIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		// zPorts is what z publishes; %[1]d is the port a holds, %[2]d another,
		// %[3]d-%[4]d a span with the held port in the middle of it.
		zPorts   []string
		zAuto    bool
		wantKept bool
	}{
		// Nobody asks for it. The container keeps it, which is what makes a
		// re-up leave the host port — and the config hash — alone.
		{"no line asks for it", []string{"%[2]d:80"}, false, true},
		// z's line fixes it. The file says the port is z's, so a moves: this
		// is the case docker compose starts and opossum did not.
		{"a line of another service fixes it", []string{"%[1]d:80"}, false, false},
		// A range holding it is the same claim, read as the ports it covers.
		// The held port is the MIDDLE of the range: neither end of the span is
		// the number a holds, so the row is about what the range covers and not
		// about either endpoint matching.
		{"a range of another service holds it", []string{"%[3]d-%[4]d:80-82"}, false, false},
		// A bare entry of z's is not a claim: it is a number opossum chose and
		// may move, so it does not turn the held port into z's.
		//
		// Written the way the loader writes it. A bare entry reaches `ports` as
		// "N:N" — spelled "N", neither hostPortBinding nor hostPortSpan reads
		// it, the entry is skipped in both passes, and the row becomes a second
		// copy of "no line asks for it".
		{"another service mirrors it with a bare entry", []string{"%[1]d:%[1]d"}, true, true},
		// A bare RANGE is a claim, though: withHostPort replaces one number and
		// a range has two, so opossum cannot move it. The held port has to go
		// instead — keeping it would publish one host port twice.
		{"a bare range of another service covers it", []string{"%[3]d-%[4]d:%[3]d-%[4]d"}, true, false},
		// A claim is a host port AND a protocol. z asking for it on udp leaves
		// the tcp port alone.
		{"another service fixes it on the other protocol", []string{"%[1]d:80/udp"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ports := freePortsForSticky(t, 2)
			port, other := ports[0], ports[1]
			zPorts := make([]string, len(tc.zPorts))
			for i, f := range tc.zPorts {
				// %[1]d is the port a holds, %[2]d another one, and
				// %[3]d-%[4]d a span straddling the held port.
				zPorts[i] = fmt.Sprintf(f, port, other, port-1, port+1)
			}
			published, said := stickyRun(t, port, zPorts, tc.zAuto)
			mirror := fmt.Sprintf("%[1]d:%[1]d", port)
			if kept := published == mirror; kept != tc.wantKept {
				t.Errorf("a published %q; wanted the port it already holds %s. z publishes %v.",
					published, map[bool]string{true: "kept", false: "given up"}[tc.wantKept], zPorts)
			}
			// The move is said, and the keep is not: a notice about a port that
			// did not move would send the reader looking for a change.
			//
			// About a. Some rows move z instead, and a notice about z's port
			// would answer "was anything said" with the wrong entry's move.
			noticeForA := fmt.Sprintf("[%s] service %q", codeHostPortRemapped, "a")
			if said := strings.Contains(said, noticeForA); said == tc.wantKept {
				t.Errorf("the notice %s while the port %s",
					map[bool]string{true: "was printed", false: "was not printed"}[said],
					map[bool]string{true: "was kept", false: "was given up"}[tc.wantKept])
			}
			if tc.wantKept {
				return
			}
			// And it names the line that took it — the reader's next move is to
			// look at that line, or to accept the new number from `opossum ps`.
			if want := fmt.Sprintf("service %q asks for host port %d in the compose file", "z", port); tc.zPorts[0] == "%[1]d:80" && !strings.Contains(said, want) {
				t.Errorf("the notice does not name the line that took the port (%q):\n%s", want, said)
			}
		})
	}
}

// The port this run has already handed to an earlier service is not a line of
// the file, so it must not reach into the sticky path: a container holding a
// port keeps it whatever this run has done elsewhere. Two services mirroring
// one port are settled by the walk, not here.
//
// The state is easier to write than to reach. b's container holds the port with
// nothing listening on it, so a's probe finds it free and takes it; on a machine
// the listener would be there and a would have moved before the question came
// up. What this pins is the rule, not a run anyone will see — the one real way
// in is a probe that misses the listener because the two name different
// addresses.
func TestAPortThisRunHandedOutDoesNotTakeAHeldPortAway(t *testing.T) {
	port := freePortForSticky(t)
	mirror := fmt.Sprintf("%[1]d:%[1]d", port)
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{mirror}, AutoHostPort: map[string]bool{mirror: true}},
		"b": {Image: "web:latest", Ports: []string{mirror}, AutoHostPort: map[string]bool{mirror: true}},
	}}
	var out bytes.Buffer
	// a is not running and is walked first, so it takes the mirrored port and
	// this run now holds it. b IS running on that port: the question is whether
	// what this run just did counts as a line of the file. It does not.
	o := New(p, shimHolding(t, "b", port, port, "tcp"), "opossum", &out)
	o.remapAutoHostPorts([]string{"a", "b"})
	if got := p.Services["b"].Ports[0]; got != mirror {
		t.Errorf("b published %q, want the port its container holds (%q). Nothing in the file "+
			"asks for it — only this run's own placement does, and that is not a line.",
			got, mirror)
	}
}

// A claim is a host port AND a protocol. The held port here is udp, and the
// line of the file fixes the same NUMBER on tcp: two different published ports,
// so the container keeps what it has.
//
// The held port is not the number the entry mirrors, which is what makes the
// answer visible: giving up the held port does not fail, it just publishes
// somewhere else. With both numbers the same, a check that asked about the
// wrong protocol would fall through to the same port and look right.
func TestAHeldUDPPortIsNotGivenUpForATCPClaim(t *testing.T) {
	ports := freePortsForSticky(t, 2)
	held, container := ports[0], ports[1]
	mirror := fmt.Sprintf("%[1]d:%[1]d/udp", container)
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{mirror}, AutoHostPort: map[string]bool{mirror: true}},
		// z fixes the number a HOLDS, but on tcp.
		"z": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:80/tcp", held)}},
	}}
	var out bytes.Buffer
	o := New(p, shimHolding(t, "a", held, container, "udp"), "opossum", &out)
	o.remapAutoHostPorts([]string{"a", "z"})
	if want := fmt.Sprintf("%d:%d/udp", held, container); p.Services["a"].Ports[0] != want {
		t.Errorf("a published %q, want the udp port its container holds (%q). z's line fixes %d on "+
			"tcp, which is a different published port from %d on udp.",
			p.Services["a"].Ports[0], want, held, held)
	}
}

// What is asked about is the port the container HOLDS, not the container port
// the entry names. They are the same number for a bare entry the first time
// round — and different ever after, once opossum has moved it.
func TestTheQuestionIsAboutTheHeldPortNotTheContainerPort(t *testing.T) {
	ports := freePortsForSticky(t, 2)
	held, container := ports[0], ports[1]
	mirror := fmt.Sprintf("%[1]d:%[1]d", container)
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{mirror}, AutoHostPort: map[string]bool{mirror: true}},
		// z fixes the CONTAINER port's number, which is not the number a holds.
		"z": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:80", container)}},
	}}
	var out bytes.Buffer
	o := New(p, shimHolding(t, "a", held, container, "tcp"), "opossum", &out)
	o.remapAutoHostPorts([]string{"a", "z"})
	if want := fmt.Sprintf("%d:%d", held, container); p.Services["a"].Ports[0] != want {
		t.Errorf("a published %q, want the port its container holds (%q). z's line fixes %d, "+
			"which a is not on — a asked about the wrong number.",
			p.Services["a"].Ports[0], want, container)
	}
}

// A held port given up because the file asks for it is said out loud, even when
// the entry lands on the very number the file mirrors.
//
// That landing reads as "nothing happened": the spec after the walk is the one
// the file wrote. For a container that had been published somewhere else it is
// a move, and the only trace of the old number is a reader noticing the port
// they were using is gone. The mirrored port being free is what makes this the
// quiet path — the noisy one already explains itself.
func TestAHeldPortGivenUpIsSaidEvenWhenTheMirrorIsFree(t *testing.T) {
	ports := freePortsForSticky(t, 2)
	held, container := ports[0], ports[1]
	mirror := fmt.Sprintf("%[1]d:%[1]d", container)
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{mirror}, AutoHostPort: map[string]bool{mirror: true}},
		// z fixes the port a is PUBLISHED on, which is not the number a's entry
		// mirrors. So a gives up the held port and lands on its mirror, free.
		"z": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:80", held)}},
	}}
	var out bytes.Buffer
	o := New(p, shimHolding(t, "a", held, container, "tcp"), "opossum", &out)
	o.remapAutoHostPorts([]string{"a", "z"})
	if got := p.Services["a"].Ports[0]; got != mirror {
		t.Fatalf("a published %q, want the mirror (%q): z fixes %d, which a was published on, "+
			"so a gives that up — and the port it mirrors is free.", got, mirror, held)
	}
	said := out.String()
	for _, tc := range []struct {
		what string
		want string
	}{
		{"the code", fmt.Sprintf("[%s] service %q", codeHostPortRemapped, "a")},
		{"the port given up", fmt.Sprintf("was published on host port %d", held)},
		{"the line that took it", fmt.Sprintf("service %q asks for host port %d in the compose file", "z", held)},
		{"where it went", fmt.Sprintf("published it on %d instead", container)},
	} {
		t.Run(tc.what, func(t *testing.T) {
			if !strings.Contains(said, tc.want) {
				t.Errorf("the notice does not say %s (%q):\n%s", tc.what, tc.want, said)
			}
		})
	}
}

// What one entry gave up is not said about the next one.
//
// The notice is assembled from what this entry was published on and the line
// that took it. Both belong to the entry, not to the service: a service with two
// bare entries has one of them moved off a held port and the other not, and a
// notice carried over would tell the reader a port moved that never did — with
// a number that is not this entry's.
func TestWhatOneEntryGaveUpIsNotSaidAboutTheNext(t *testing.T) {
	ports := freePortsForSticky(t, 4)
	held, first, second := ports[0], ports[1], ports[2]
	mirrors := []string{fmt.Sprintf("%[1]d:%[1]d", first), fmt.Sprintf("%[1]d:%[1]d", second)}
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: mirrors,
			AutoHostPort: map[string]bool{mirrors[0]: true, mirrors[1]: true}},
		// z fixes the port a's FIRST entry is published on. The second entry's
		// container port is not one a's container publishes at all.
		"z": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:80", held)}},
	}}
	var out bytes.Buffer
	o := New(p, shimHolding(t, "a", held, first, "tcp"), "opossum", &out)
	o.remapAutoHostPorts([]string{"a", "z"})
	said := out.String()
	if got := strings.Count(said, fmt.Sprintf("was published on host port %d", held)); got != 1 {
		t.Errorf("the port given up is named %d times, want once — only the first entry was on it:\n%s",
			got, said)
	}
	if want := fmt.Sprintf("%[1]d:%[1]d", second); p.Services["a"].Ports[1] != want {
		t.Errorf("the second entry published %q, want its mirror (%q): nothing asked for it.",
			p.Services["a"].Ports[1], want)
	}
}
