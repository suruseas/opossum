package orchestrator

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
// The budget for drawing ports, and the ends of the range a row may be handed.
// Every one of them is read twice — once by the code that does the drawing, once
// by the refusal that says what was wanted — and each time these were written
// apart, one side was changed and the other went on naming the old number: the
// budget went from 64 to 512 while the message still said 512 on a tree where it
// was 64, and the top end went from 65534 to 65300 while the message said 65534.
const (
	freePortTries = 512
	freePortLow   = 1026  // the lowest number a row may be handed
	freePortHigh  = 65299 // the highest: a row builds the ports above one of these
)

func freePortsForSticky(t *testing.T, n int) []int {
	t.Helper()
	ports := make([]int, 0, n)
	// Away from both ends, because a row builds the port below and the ports
	// above one of these — a three-wide span starting here, and a number a
	// hundred above — and 65535 has nothing above it.
	//
	// The budget for walking past the ones over the cutoff is what it is because
	// the OS hands these out IN SEQUENCE, not at random. The numbers above 65299
	// are not a small chance on every draw; they are a stretch of about 235 that
	// the allocation walks through, and a run starting inside it has to walk all
	// the way out. Measured: with the allocation put at 65150, this helper failed
	// on the first 2 of 10 runs at a budget of 64, and every run after the wrap
	// passed — 64 could not cross 235. So the budget covers the whole stretch.
	//
	// Only the numbers kept hold their listener, until the last one is chosen; a
	// number passed over is let go again. Holding a few hundred open to walk the
	// stretch would run into a different limit (file descriptors), and letting
	// one go cannot hand it back within a run: the sequence has moved past it,
	// and it comes round again only after a wrap — 16,384 numbers away, measured,
	// which is 32 times the budget here. Holding them open is not what keeps
	// them from coming back: three hundred held open step by one just the same.
	for tries := 0; len(ports) < n; tries++ {
		if tries == freePortTries {
			t.Fatalf("the OS did not name %d ports between %d and %d in %d tries",
				n, freePortLow, freePortHigh, freePortTries)
		}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		if p := l.Addr().(*net.TCPAddr).Port; p >= freePortLow && p <= freePortHigh {
			defer l.Close()
			ports = append(ports, p)
			continue
		}
		l.Close()
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
	return shimHoldingSpan(t, service, hostPort, containerPort, proto, 1)
}

// shimHoldingTwo answers with TWO published entries for one container, in the
// order given — which is how the runtime answers a container run with two `-p`
// flags. The pair matters when both cover the same container port: `-p 47161:81
// -p 47150-47152:80-82` starts (measured on container 1.4.1) and then 81 is
// covered twice, once by the entry that names it and once by the range's second
// port. A fixture that could only hold one entry cannot pose that question.
func shimHoldingTwo(t *testing.T, service string, first, second runtime.PortMapping) *runtime.Runtime {
	t.Helper()
	return shimHoldingThese(t, service, first, second)
}

// shimHoldingThese is shimHoldingTwo for any number of published entries. Three
// is a shape one container port can really have — a line the file fixes, the
// port opossum handed a bare entry, and a range the file has since dropped — and
// a fixture that can only hold two answers cannot ask what the third does to the
// second.
func shimHoldingThese(t *testing.T, service string, pms ...runtime.PortMapping) *runtime.Runtime {
	t.Helper()
	dir := t.TempDir()
	shim := filepath.Join(dir, "c.sh")
	entries := make([]string, 0, len(pms))
	for _, p := range pms {
		entries = append(entries, fmt.Sprintf(
			`{"containerPort":%d,"count":%d,"hostAddress":"0.0.0.0","hostPort":%d,"proto":%q}`,
			p.ContainerPort, p.Count, p.HostPort, p.Proto))
	}
	body := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n  inspect)\n    case \"$2\" in\n      %s.demo.opossum) cat <<'J'\n"+
		`[{"status":{"state":"running"},"configuration":{"labels":{"opossum.project":"demo"},`+
		`"publishedPorts":[%s]}}]`+
		"\nJ\n      ;;\n      *) echo '[]' ;;\n    esac\n  ;;\n  system) echo 'status running' ;;\nesac\nexit 0\n",
		service, strings.Join(entries, ","))
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return &runtime.Runtime{Bin: shim}
}

// shimHoldingSpan is shimHolding for a published RANGE: the runtime answers one
// entry holding the first port of the span and a count of its width, so a
// container publishing three ports says so in one answer. A fixture that could
// only say "one entry, one port" cannot pose the question this is about.
func shimHoldingSpan(t *testing.T, service string, hostPort, containerPort int, proto string, count int) *runtime.Runtime {
	t.Helper()
	dir := t.TempDir()
	shim := filepath.Join(dir, "c.sh")
	body := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n  inspect)\n    case \"$2\" in\n      %s.demo.opossum) cat <<'J'\n"+
		`[{"status":{"state":"running"},"configuration":{"labels":{"opossum.project":"demo"},`+
		`"publishedPorts":[{"containerPort":%d,"count":%d,"hostAddress":"0.0.0.0","hostPort":%d,"proto":"%s"}]}}]`+
		"\nJ\n      ;;\n      *) echo '[]' ;;\n    esac\n  ;;\n  system) echo 'status running' ;;\nesac\nexit 0\n",
		service, containerPort, count, hostPort, proto)
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
// up. What this pins is the rule, not a run anyone will see.
//
// Two entries that name DIFFERENT addresses used to be the one real way in.
// They are not any more: a claim carries the address it was handed on, so two
// services on one number with different addresses are a pair the runtime starts,
// and both keep what they have. What is left here is the same address, which
// the pre-flight refuses with OPSM-213 once the walk has put both on it.
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

// TestABareEntryInsideItsOwnRangeIsToldItMoved pins what `up` says when a line
// of the file asks for the host port the entry's container is on — here the
// range itself, still published. The port is spoken for, so the entry cannot
// keep it: it is moved, and `up` says so.
//
// The notice names the port the range was holding only because this fixture
// lets the entry land on its OWN number — the container port, mirrored. That
// is the one branch that has the range to talk about; where the entry has to
// be put somewhere else, the warning explains why its own number could not be
// had and never reaches the range. See
// TestAHeldPortGivenUpIsSaidEvenWhenTheMirrorIsFree.
//
// This is the other half of TestAContainerPortInsideAPublishedRangeKeepsItsHostPort
// (below). There no line asks for the port and the entry keeps it; here one
// does and it cannot. Which of the two happens turns on the compose file, not
// on the container, and a suite with only one of them reads as if it turned on
// the container.
//
// The container ports are taken from the OS, not written as low numbers, because the
// port an entry mirrors to is asked of the operating system: a fixture built on
// low numbers passes only on a machine where those happen to be free, and reads
// as a test of the range when it is a test of the machine.
func TestABareEntryInsideItsOwnRangeIsToldItMoved(t *testing.T) {
	for _, tc := range []struct {
		name   string
		offset int // which port of the three-wide span the file names, bare
	}{
		{"the first port of the range", 0},
		{"the middle of the range", 1},
		{"the last port of the range", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Two numbers from the OS: the range's first HOST port, and the
			// container port the file names — which is also the port that
			// entry would be mirrored to, so it has to be one this machine
			// will give out.
			//
			// Well apart, because the OS hands out neighbours often enough:
			// with the mirrored port INSIDE the range's host ports, the file
			// is publishing that number itself and a different rule answers
			// first, which is a row about the compose file, not about what the
			// container holds.
			lo, cp := 0, 0
			for _, p := range freePortsForSticky(t, 12) {
				switch {
				case lo == 0:
					lo = p
				case cp == 0 && (p < lo-8 || p > lo+8):
					cp = p
				}
			}
			if cp == 0 {
				t.Fatal("the OS never named a free port more than 8 away from the first")
			}
			first := cp - tc.offset // the range's first container port
			// The file publishes the range AND a bare entry for one container
			// port inside it. The container is already up on the range.
			mirror := fmt.Sprintf("%d:%d", cp, cp)
			span := fmt.Sprintf("%d-%d:%d-%d", lo, lo+2, first, first+2)
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: []string{mirror, span},
					AutoHostPort: map[string]bool{mirror: true}},
			}}
			var out bytes.Buffer
			o := New(p, shimHoldingSpan(t, "a", lo, first, "tcp", 3), "opossum", &out)
			o.remapAutoHostPorts([]string{"a"})
			said, held := out.String(), lo+tc.offset
			if !strings.Contains(said, "OPSM-206") {
				t.Errorf("up said nothing about moving the entry for %d, which the range is holding on %d:\n%s",
					cp, held, said)
			}
			if want := fmt.Sprintf("host port %d", held); !strings.Contains(said, want) {
				t.Errorf("the notice does not name %q — the port the range holds for %d:\n%s", want, cp, said)
			}
		})
	}
}

// The notice names the line that actually took the port, not the smallest name
// written on the number. With two lines on one number — one on another address,
// which does not stop this entry, and one on the same address, which does — the
// reader has to be sent to the second. Naming the first sends them to a line
// that can stay exactly as it is.
func TestTheNoticeNamesTheLineThatTookThePort(t *testing.T) {
	held, cp := 0, 0
	for _, p := range freePortsForSticky(t, 12) {
		switch {
		case held == 0:
			held = p
		case cp == 0 && (p < held-8 || p > held+8):
			cp = p
		}
	}
	if cp == 0 {
		t.Fatal("the OS never named a free port more than 8 away from the first")
	}
	mirror := fmt.Sprintf("%d:%d", cp, cp)
	// `m` sorts before `y`, so the smallest name on this number is the line that
	// does NOT collide. That is the whole point of the row.
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{mirror}, AutoHostPort: map[string]bool{mirror: true}},
		"m": {Image: "web:latest", Ports: []string{fmt.Sprintf("127.0.0.1:%d:9", held)}},
		"y": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:9", held)}},
	}}
	var out bytes.Buffer
	o := New(p, shimHolding(t, "a", held, cp, "tcp"), "opossum", &out)
	o.remapAutoHostPorts([]string{"a", "m", "y"})
	said := out.String()
	if !strings.Contains(said, `service "y"`) {
		t.Errorf("the notice does not name y, whose line asks for host port %d on the address a was on:\n%s",
			held, said)
	}
	if strings.Contains(said, `service "m"`) {
		t.Errorf("the notice names m, whose line asks for %d on 127.0.0.1 — a line that does not "+
			"stop a and does not have to change:\n%s", held, said)
	}
}

// What the walk hands out is a claim too, and it carries the address it was
// handed on. Two services that mirror to one number keep it only when they mean
// different addresses; one service's two entries never do, because a container
// whose published ports overlap is refused whatever it writes.
//
// The `mirrored` column is what the file writes in front of the container port.
// Before the claims carried an address, the second entry was always moved —
// which is what the runtime needs when the two are one container, and a port
// given away for nothing when they are two.
func TestWhatTheWalkHandsOutCarriesItsAddress(t *testing.T) {
	for _, tc := range []struct {
		name     string
		second   string // how the second bare entry writes the same container port
		sameSvc  bool
		wantBoth bool // both end up on the mirrored port
	}{
		{"two services, the second on 127.0.0.1", "127.0.0.1:%d:%d", false, true},
		{"two services, the second on the same address", "%d:%d", false, false},
		{"one service, the second on 127.0.0.1", "127.0.0.1:%d:%d", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp := freePortsForSticky(t, 1)[0]
			first := fmt.Sprintf("%d:%d", cp, cp)
			second := fmt.Sprintf(tc.second, cp, cp)
			a := &compose.Service{Image: "web:latest", Ports: []string{first},
				AutoHostPort: map[string]bool{first: true}}
			svcs := map[string]*compose.Service{"a": a}
			if tc.sameSvc {
				a.Ports = append(a.Ports, second)
				a.AutoHostPort[second] = true
			} else {
				svcs["b"] = &compose.Service{Image: "web:latest", Ports: []string{second},
					AutoHostPort: map[string]bool{second: true}}
			}
			p := &compose.Project{Name: "demo", Services: svcs}
			var out bytes.Buffer
			o := New(p, shimHolding(t, "nobody", cp, cp, "tcp"), "opossum", &out)
			o.remapAutoHostPorts([]string{"a", "b"})
			got := second
			if tc.sameSvc {
				got = p.Services["a"].Ports[1]
			} else {
				got = p.Services["b"].Ports[0]
			}
			if tc.wantBoth && got != second {
				t.Errorf("the second entry published %q, want %q — it names another address, "+
					"and the runtime starts both:\n%s", got, second, out.String())
			}
			if !tc.wantBoth && got == second {
				t.Errorf("the second entry kept %q, but it is asking for the same address as the "+
					"first. Two publish specs that overlap cannot both be had.", got)
			}
		})
	}
}

// A port this service KEEPS goes into the claims under its own name too. The
// name is what makes a second entry of the same service see the first: without
// it, two of one service's bare entries land on one number and the runtime
// refuses the container.
//
// Three places hand a port to a service — the one it keeps, the mirror when it
// is free, and the number it is moved to — and each writes the claim itself.
// The row below covers the FIRST of them; the mirror is covered by
// TestWhatTheWalkHandsOutCarriesItsAddress.
func TestAPortAServiceKeepsIsClaimedUnderItsName(t *testing.T) {
	held, cp := 0, 0
	for _, p := range freePortsForSticky(t, 12) {
		switch {
		case held == 0:
			held = p
		case cp == 0 && (p < held-8 || p > held+8):
			cp = p
		}
	}
	if cp == 0 {
		t.Fatal("the OS never named a free port more than 8 away from the first")
	}
	// `a`'s container is already on held for cp, so its first entry keeps held.
	// The second entry mirrors to held as well, on another address — which is a
	// different claim between services, but the same one within a service.
	first := fmt.Sprintf("%d:%d", cp, cp)
	second := fmt.Sprintf("127.0.0.1:%d:%d", held, held)
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{first, second},
			AutoHostPort: map[string]bool{first: true, second: true}},
	}}
	var out bytes.Buffer
	o := New(p, shimHolding(t, "a", held, cp, "tcp"), "opossum", &out)
	o.remapAutoHostPorts([]string{"a"})
	if got := p.Services["a"].Ports[1]; got == second {
		t.Errorf("the second entry kept %q, but this service is already publishing host port %d "+
			"for %d — one container cannot publish a port twice, whatever addresses the two "+
			"entries name:\n%s", got, held, cp, out.String())
	}
}

// The port an entry is MOVED to goes into the claims under its name as well —
// the third of the three places that hand a port out. Without the name, the
// service's next bare entry can be mirrored onto the very port the first one was
// just moved to.
//
// The OS decides where the first entry lands, so the test decides for it:
// holdPort answers with the number the second entry will mirror to, which is the
// one collision that has to be seen.
func TestAPortAnEntryIsMovedToIsClaimedUnderItsName(t *testing.T) {
	ports := freePortsForSticky(t, 2)
	taken, landing := ports[0], ports[1]
	// `a` writes two bare entries. The first mirrors to `taken`, which another
	// service's line has spoken for, so it is moved — and the OS is made to
	// answer with `landing`. The second entry mirrors to `landing`.
	// The second entry writes ANOTHER address, so only the same-service half of
	// the rule can stop it. Written as a wildcard it would collide on the address
	// instead, and the name the claim carries would not matter — the row would
	// pass with or without it.
	first := fmt.Sprintf("%[1]d:%[1]d", taken)
	second := fmt.Sprintf("127.0.0.1:%[1]d:%[1]d", landing)
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{first, second},
			AutoHostPort: map[string]bool{first: true, second: true}},
		"z": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:9", taken)}},
	}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	o.holdPort = func(network, address string, port int) (int, io.Closer, error) {
		if port != 0 {
			return port, io.NopCloser(nil), nil // the caller named it: say it is free
		}
		return landing, io.NopCloser(nil), nil // the OS hands out the landing port
	}
	o.remapAutoHostPorts([]string{"a", "z"})
	moved, next := p.Services["a"].Ports[0], p.Services["a"].Ports[1]
	if moved != fmt.Sprintf("%d:%d", landing, taken) {
		// Fatal, not Skip: holdPort decides where the first entry goes, so this
		// cannot drift on its own. A regression that stops the first entry
		// moving would make a Skip here read as a pass in CI.
		t.Fatalf("the first entry went to %q, want %d:%d — holdPort was made to answer with "+
			"the landing port, so the row about the second entry cannot be reached", moved, landing, taken)
	}
	if next == second {
		t.Errorf("the second entry kept %q, the port the first one was just moved to. "+
			"One container cannot publish a host port twice:\n%s", next, out.String())
	}
}

// A line that fixes a host port on ONE address does not speak for that number
// on another one. The runtime starts `0.0.0.0:N` beside `127.0.0.1:N` when the
// two belong to different services (measured on 1.4.1), so moving a bare entry
// off N because another service wrote `127.0.0.1:N` buys nothing and costs the
// service its port — and the config hash with it — on the `up` after the file
// changed.
//
// Within ONE service the addresses do not divide the number: the runtime
// refuses a container whose publish specs overlap, whatever addresses they
// name. That row is the control here, and it has to keep moving.
func TestALineOnAnotherAddressDoesNotSpeakForTheNumber(t *testing.T) {
	// Two ports are asked about, and they are asked in different places. The
	// port `a`'s container is ALREADY ON goes through the sticky path, which
	// reads the claims to decide whether it may keep it. The port `a` would be
	// MIRRORED to goes through the check just after, which reads the same
	// claims about a different number. A row on one of them says nothing about
	// the other: with the address written one way on the asking side and
	// another way on the claim, the sticky rows still pass.
	for _, tc := range []struct {
		name     string
		zAddr    string // what the other line writes in front of the port
		sameSvc  bool   // the other line belongs to `a` itself
		onMirror bool   // the other line names the port `a` mirrors to, not the held one
		asRange  bool   // the other line writes a RANGE over the number
		wantKeep bool
	}{
		{"another service fixes it on 127.0.0.1", "127.0.0.1:", false, false, false, true},
		{"another service fixes it on the same address", "", false, false, false, false},
		{"the same service fixes it on 127.0.0.1", "127.0.0.1:", true, false, false, false},
		{"another service fixes the mirrored port on 127.0.0.1", "127.0.0.1:", false, true, false, true},
		{"another service fixes the mirrored port on the same address", "", false, true, false, false},
		// A RANGE of another service's, over the number, on another address.
		// The claims a range makes are kept as spans and asked one by one, and
		// that is a second place the rule has to be read: a row on single ports
		// leaves the span side free to go on speaking for every address.
		{"another service's range covers it on 127.0.0.1", "127.0.0.1:", false, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Two numbers from the OS: the port `a`'s container already holds
			// for its container port, and the container port itself — which is
			// also where a bare entry would be mirrored to, so it has to be a
			// port this machine will give out.
			held, cp := 0, 0
			for _, p := range freePortsForSticky(t, 12) {
				switch {
				case held == 0:
					held = p
				case cp == 0 && (p < held-8 || p > held+8):
					cp = p
				}
			}
			if cp == 0 {
				t.Fatal("the OS never named a free port more than 8 away from the first")
			}
			mirror := fmt.Sprintf("%d:%d", cp, cp)
			// On the mirror rows nothing is held, so the sticky path has no
			// answer and the mirror is what gets asked about.
			at := held
			if tc.onMirror {
				at = cp
			}
			fixed := fmt.Sprintf("%s%d:9", tc.zAddr, at)
			if tc.asRange {
				fixed = fmt.Sprintf("%s%d-%d:9-11", tc.zAddr, at-1, at+1)
			}
			a := &compose.Service{Image: "web:latest", Ports: []string{mirror},
				AutoHostPort: map[string]bool{mirror: true}}
			svcs := map[string]*compose.Service{"a": a}
			if tc.sameSvc {
				a.Ports = append(a.Ports, fixed)
			} else {
				svcs["z"] = &compose.Service{Image: "web:latest", Ports: []string{fixed}}
			}
			p := &compose.Project{Name: "demo", Services: svcs}
			var out bytes.Buffer
			holder := "a"
			if tc.onMirror {
				holder = "nobody" // no container of a's: the mirror is what is asked about
			}
			o := New(p, shimHolding(t, holder, held, cp, "tcp"), "opossum", &out)
			o.remapAutoHostPorts([]string{"a", "z"})
			keep := fmt.Sprintf("%d:%d", held, cp)
			if tc.onMirror {
				keep = mirror
			}
			got, want := p.Services["a"].Ports[0], keep
			if tc.wantKeep && got != want {
				t.Errorf("a published %q, want %q — the port it should have been left on. "+
					"%q fixes that number on another address, and the runtime starts both.",
					got, want, fixed)
			}
			if !tc.wantKeep && got == want {
				t.Errorf("a kept %q, but %q asks for that number where it counts. "+
					"Two publish specs that overlap cannot both be had.", got, fixed)
			}
		})
	}
}

// A bare entry whose container port sits inside a published range keeps the host
// port that range covers — as long as no line of the compose file asks for that
// host port. Where one does, the entry is moved instead: see
// TestABareEntryInsideItsOwnRangeIsToldItMoved (above), which is the same
// container answering the same span, with the range left in the file.
//
// The container answers with one entry — the first port of the span and a count
// — so "which host port is this container publishing container port C on" is a
// question about the span, not about the entry's own numbers. Reading only what
// the entry names would hand this service a fresh port and leave the one it is
// already listening on to somebody else.
func TestAContainerPortInsideAPublishedRangeKeepsItsHostPort(t *testing.T) {
	// Which port of the span the file asks about. The MIDDLE is the one the
	// answer only covers by walking the span. The FIRST is the one the answer
	// names outright, and reading the span must not lose it: a walk that starts
	// at the second port keeps every other row here green while putting that
	// entry back on the mirrored port, which is what happened before any of
	// this. One value cannot tell "the span is read" from "the span is read
	// from its second port on".
	for _, tc := range []struct {
		name string
		cp   int // the container port the file names, bare
		// asked puts a line of ANOTHER service on the host port the span holds
		// for cp. What decides this is whether the file asks for that port —
		// not whether the file still publishes the range — and with the range
		// gone from the file, another service's line is the only way left to
		// ask for it.
		asked bool
	}{
		{"the middle of the span", 81, false},
		{"the first port of the span", 80, false},
		{"another service asks for the port the span holds", 81, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lo := freePortsForSticky(t, 1)[0]
			// The container publishes lo..lo+2 for container ports 80..82, and
			// the file has one bare entry.
			mirror := fmt.Sprintf("%d:%d", tc.cp, tc.cp)
			host := lo + tc.cp - 80
			svcs := map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: []string{mirror}, AutoHostPort: map[string]bool{mirror: true}},
			}
			if tc.asked {
				svcs["z"] = &compose.Service{Image: "web:latest", Ports: []string{fmt.Sprintf("%d:9", host)}}
			}
			p := &compose.Project{Name: "demo", Services: svcs}
			var out bytes.Buffer
			o := New(p, shimHoldingSpan(t, "a", lo, 80, "tcp", 3), "opossum", &out)
			o.remapAutoHostPorts([]string{"a", "z"})
			got := p.Services["a"].Ports[0]
			if tc.asked {
				if want := fmt.Sprintf("%d:%d", host, tc.cp); got == want {
					t.Errorf("a kept %q, but service z asks for host port %d in the file. "+
						"Holding it is not enough: a port the file speaks for is not this entry's to keep.",
						got, host)
				}
				return
			}
			if want := fmt.Sprintf("%d:%d", host, tc.cp); got != want {
				t.Errorf("a published %q, want the port its container already holds for %d (%q). "+
					"The container answers lo=%d count=3, so %d is on %d.",
					got, tc.cp, want, lo, tc.cp, host)
			}
		})
	}
}

// TestTheRestOfASpanIsKeptAfterANamedPortIsSkipped pins what happens AFTER the
// rule that lets a named port win steps over a port: the span keeps going. Stopping there instead
// loses every port past the overlap, and the entries sitting on them are handed
// fresh host ports on the next `up` — the symptom this whole change is about,
// one position further along. The two ports this row reads are on either side
// of the skipped one, so a fixture that only ever overlapped the last port
// could not tell "kept going" from "stopped".
func TestTheRestOfASpanIsKeptAfterANamedPortIsSkipped(t *testing.T) {
	lo := freePortsForSticky(t, 1)[0]
	named := lo + 100
	// The container publishes 80-82 as a range, and 81 a second time on its
	// own. The file mirrors 81 and 82 bare, so both have to come back from the
	// running container: 81 from the entry that names it, 82 from the range's
	// LAST port — the one past the skip.
	eightyOne, eightyTwo := "81:81", "82:82"
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{eightyOne, eightyTwo},
			AutoHostPort: map[string]bool{eightyOne: true, eightyTwo: true}},
	}}
	one := runtime.PortMapping{HostAddress: "0.0.0.0", HostPort: named, ContainerPort: 81, Proto: "tcp", Count: 1}
	many := runtime.PortMapping{HostAddress: "0.0.0.0", HostPort: lo, ContainerPort: 80, Proto: "tcp", Count: 3}
	var out bytes.Buffer
	o := New(p, shimHoldingTwo(t, "a", one, many), "opossum", &out)
	o.remapAutoHostPorts([]string{"a"})
	if want := fmt.Sprintf("%d:81", named); p.Services["a"].Ports[0] != want {
		t.Errorf("the entry for 81 published %q, want %q — the port it names.",
			p.Services["a"].Ports[0], want)
	}
	if want := fmt.Sprintf("%d:82", lo+2); p.Services["a"].Ports[1] != want {
		t.Errorf("the entry for 82 published %q, want %q — the range's last port. "+
			"Stepping over 81 must not end the span: 82 is behind it.",
			p.Services["a"].Ports[1], want)
	}
	if out.Len() != 0 {
		t.Errorf("nothing moved, so nothing should have been said:\n%s", out.String())
	}
}

// A container port an entry NAMES keeps that entry's host port, even when a
// range of the same container also covers it.
//
// The runtime takes the same container port twice — `-p 47161:81` beside
// `-p 47150-47152:80-82` starts — and then two answers cover 81: the one
// published on 47161, and the range's second port. The first is what that entry
// is actually listening on. Letting the range's port win hands the service a
// host port its container is not on, and the next `up` finds that port held by
// its own container and moves it: the published port, and the config hash with
// it, then move on every re-up.
//
// The table below says what each row varies and why.
func TestAnEntryThatNamesAContainerPortKeepsItsOwnHostPort(t *testing.T) {
	// The axes below are the ones the rule can be got wrong along. Two rows are
	// the only ones that catch a mutation of their own — the range that ENDS on
	// the named port, and the count of nothing with the range answered first.
	// The rest overlap, and are kept because they name what the rule is for; if
	// that stops being worth their run time, measure again before cutting, not
	// from the shape of the table.
	//
	// ORDER: which entry the runtime answers with first. Keying the rule on the
	// position in the span made the answer depend on it, and the answers come
	// back in the order the run gave the flags, so a rule that worked in only
	// one order would be settled by where the line sits in the file.
	//
	// POSITION: where in the range the named container port falls. A rule
	// reading "everything after the first" walks past a range that BEGINS on
	// it; one reading "everything but the last" walks past a range that ENDS on
	// it. The middle alone tells neither.
	//
	// WIDTH: two ports is still a range. A rule asking "more than two" passes a
	// two-wide range off as an entry naming its port.
	//
	// COUNT: an answer from before the field existed names no count. It is one
	// port, so it is an entry naming that port and it wins; read as a range it
	// would give way — which only shows when the range is answered first.
	for _, tc := range []struct {
		name       string
		namedFirst bool
		rangeStart bool // the named container port is the range's FIRST port
		width      int  // ports the range publishes; 0 means the usual three
		namedCount int  // what the single entry says its count is; 0 means one
		fileRange  bool // the file writes the range down as well
	}{
		{"the entry that names it comes first", true, false, 0, 1, false},
		{"the range comes first", false, false, 0, 1, false},
		{"the range starts on the named port, which comes first", true, true, 0, 1, false},
		// A range only two ports wide is still a range. Every other row here is
		// three wide, and a rule that asked "more than two" would pass this one
		// off as an entry naming its port — the width standing in for what the
		// entry is.
		{"a range two ports wide still yields", true, true, 2, 1, false},
		// The same width, with the named port at the range's END. A rule that
		// let the last port of a span through — "everything but the last" — is
		// invisible to every other row here: they put the named port first or
		// in the middle, and the three-wide rows never overlap on the last one.
		{"a range two ports wide that ends on the named port", true, false, 2, 1, false},
		// An answer from before the count existed says nothing about width. It
		// is one port, so it is an entry naming that port, and it wins — read
		// as a range instead, it would be the one to give way.
		//
		// With the range answered FIRST, because that is the order that can
		// tell: answered first itself, this entry writes into an empty map
		// either way and the reading makes no difference.
		{"an entry that names no count at all still wins, the range first", false, false, 0, 0, false},
		// A control for the situation this test was written for: the file
		// writing the range as well as the bare entry. The answer is the same,
		// but it is reached before the width is looked at — a host port the
		// file writes belongs to the line that wrote it — so no mutation of the
		// WIDTH rule below (count, position, two-wide) turns this row red.
		// Mutations of the preference do reach it, and other rows catch those
		// too, so it guards nothing of its own. It is here because this is the
		// arrangement a reader of this test will have in mind, and leaving it
		// out would mean nothing runs it at all.
		{"the file writes the range as well", true, false, 0, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lo := freePortsForSticky(t, 1)[0]
			// Well clear of the range. Two ports asked of the OS come back
			// next to each other often enough that `named` landed ON the
			// range's second port, and then the two values this row is about —
			// "where the entry says it is" and "where the range covers it" —
			// were one number. Nothing binds this one, so it only has to be a
			// number the container can report.
			named := lo + 100
			// Where the named container port falls in the range: its middle,
			// or its first port.
			cp := 81
			if tc.rangeStart {
				cp = 80
			}
			width := tc.width
			if width == 0 {
				width = 3
			}
			mirror := fmt.Sprintf("%d:%d", cp, cp)
			// The file writes the bare entry only — except in the one row that
			// says it writes the range as well, which is a control. The range is
			// one the container still publishes and the file no longer asks for,
			// which is what leaves these axes to the rule this test is about: a
			// range written down in the file is a host port the file pins, and then
			// the answer is settled by whose port it is before the width, the
			// position or the count is ever looked at. That question has its own
			// test; this one keeps the range out of the file so the rule below
			// is the one being measured.
			specs := []string{mirror}
			if tc.fileRange {
				specs = append(specs, fmt.Sprintf("%d-%d:80-%d", lo, lo+width-1, 80+width-1))
			}
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: specs,
					AutoHostPort: map[string]bool{mirror: true}},
			}}
			one := runtime.PortMapping{HostAddress: "0.0.0.0", HostPort: named, ContainerPort: cp, Proto: "tcp", Count: tc.namedCount}
			many := runtime.PortMapping{HostAddress: "0.0.0.0", HostPort: lo, ContainerPort: 80, Proto: "tcp", Count: width}
			first, second := one, many
			if !tc.namedFirst {
				first, second = many, one
			}
			var out bytes.Buffer
			o := New(p, shimHoldingTwo(t, "a", first, second), "opossum", &out)
			mustAnswerWith(t, o, "a", 2)
			o.remapAutoHostPorts([]string{"a"})
			if want := fmt.Sprintf("%d:%d", named, cp); p.Services["a"].Ports[0] != want {
				t.Errorf("a published %q, want the port the entry naming %d is on (%q). "+
					"The range covers %d as well, on %d, but that is not where this entry is listening.",
					p.Services["a"].Ports[0], cp, want, cp, lo+cp-80)
			}
			if out.Len() != 0 {
				t.Errorf("nothing moved, so nothing should have been said:\n%s", out.String())
			}
		})
	}
}

// mustAnswerWith fails the row unless the shim answers about this service the
// way the sticky path needs: a running container of THIS project, publishing
// exactly n entries. Every row below poses its question through that answer, and
// with the answer thrown away the sticky path has nothing to read — the bare
// entry falls to the port it mirrors, and a row expecting a port KEPT goes red
// with a message about the wrong port, which reads exactly like the rule being
// broken. Asking the same four things first says "the fixture did not hold"
// instead (`Unknown` being the runtime declining to answer, not a container
// that is gone).
//
// What it does NOT do is tell a flaky exec apart from a broken rule. It asks
// once, and the walk asks again: an answer that arrives here and not there
// still comes out as a wrong port. A fixture whose SHAPE is wrong is what this
// catches, and a shim that answers differently twice is not.
func mustAnswerWith(t *testing.T, o *Orchestrator, service string, n int) {
	t.Helper()
	name := o.containerName(service)
	info := o.rt.Inspect(name)
	if info.Exists && !info.Unknown && info.State == "running" &&
		info.Labels[projectLabel] == o.Project.Name && len(info.Ports) == n {
		return
	}
	t.Fatalf("the fixture did not hold, so this row measured nothing: the shim answered "+
		"exists=%t unknown=%t state=%q project=%q with %d published entries for %q, want a "+
		"running container of %q with %d", info.Exists, info.Unknown, info.State,
		info.Labels[projectLabel], len(info.Ports), name, o.Project.Name, n)
}

// Two published entries can cover one container port, and then only one of them
// is the bare entry's. The one on a host port another line of the same service
// writes down belongs to that line, so the other is what the bare entry is on —
// whichever order the runtime lists them in, which is the order the file wrote
// them WHEN THE CONTAINER WAS CREATED (argument order, measured on 1.4.1; the
// file can have been rearranged since). Reading the fixed line's port as the
// bare entry's makes the entry give it up (the file asks for it) and tells the
// reader it was published on a port it was never on.
//
// One row is a control rather than a guard: "one answer nobody wrote down"
// catches no mutation of this rule. It is here so that the keeping, silent path
// is exercised with nothing else going on.
func TestTwoAnswersForOneContainerPortAreToldApartByWhoseTheyAre(t *testing.T) {
	for _, tc := range []struct {
		name string
		// what the container publishes for the one container port
		answers   []int // host ports, in the order inspect gives them
		fileLine  bool  // another line of this service fixes answers[0]
		bareLast  bool  // the file writes the bare entry after that line
		onMirror  bool  // the second answer is the port the bare entry mirrors to
		udpLine   bool  // that other line names the number on udp, not tcp
		udpAnswer bool  // the published entries, and the bare line, are udp too
		bareRange bool  // that other line is a bare RANGE, not a fixed port
		addrLine  bool  // that other line writes an address in front of the port
		wantKeep  int   // 0 = the port the file fixes, 1 = the port the bare entry is on;
		//                     -1 for "gives it up and moves"
	}{
		// Two answers, and one of them is a host port the file writes down.
		// Both orders, because the order is the only thing that used to decide.
		{"the file's own port is listed first", []int{0, 1}, true, true, false, false, false, false, false, 1},
		// The same pair answered the other way round, with the bare entry
		// written first in the file. A row with the file order and the answer
		// order pulled apart is left out: nothing here reads `svc.Ports` in
		// order, so such a row catches nothing these two do not, and a fixture
		// that writes one order and answers in another has to say why.
		{"the bare entry comes first in the file", []int{1, 0}, true, false, false, false, false, false, false, 1},
		// The bare entry's own line writes a host port too — the one it mirrors
		// to — and that one is not somebody else's: it is the number being
		// decided. Counting it among the ports the file pins down would make
		// both answers pinned and hand the question back to the order, so this
		// row lists the mirror FIRST, where the order gives the wrong one.
		{"one answer is the port the entry mirrors to", []int{1, 0}, true, true, true, false, false, false, false, 1},
		// One answer, on a port no line of the file writes: kept, as before.
		// A control: no mutation of this rule turns it red.
		{"one answer nobody wrote down", []int{1}, false, true, false, false, false, false, false, 1},
		// One answer, on the port this service's other line fixes. This is the
		// container that held a port before the file fixed it: it has to move,
		// and the reader has to be told. A preference between two answers must
		// not turn into "record nothing".
		{"one answer, and the file fixed that port", []int{0}, true, true, false, false, false, false, false, -1},
		// A line on udp does not pin the number down for tcp: the two do not
		// overlap on the host, here as on the runtime. Neither answer is pinned
		// then, so which one wins goes back to the order the file writes — the
		// question this rule does not answer — and the row pins that, since it
		// is what tells a protocol-blind reading apart.
		{"the other line names the number on udp", []int{1, 0}, true, true, false, true, false, false, false, 0},
		// The other half of the same question: when the published entries are
		// udp AS WELL, the udp line does pin the number, and the answer on it is
		// that line's. Without this row the protocol could be dropped on the way
		// in — asked about tcp whatever the entry publishes — and only the row
		// above would notice, which reads as "udp pins nothing".
		{"the answers and the line are both udp", []int{1, 0}, true, true, false, true, true, false, false, 1},
		// A bare RANGE is pinned too. It cannot be moved — one number is what
		// gets replaced — so its ports are as good as ones the file fixed, and
		// an answer on one of them is that line's, not the bare single's.
		{"the other line is a bare range", []int{1, 0}, true, true, false, false, false, true, false, 1},
		// The other line writes an ADDRESS in front of its port, and still pins
		// the number. Two lines of ONE service cannot share a host port at all —
		// the runtime refuses two publish specs of one container that overlap
		// whatever addresses they name, and opossum says so first with
		// `OPSM-213` — so there is no pair here for an address to tell apart,
		// and a published answer on that number is that line's. (Between two
		// SERVICES the address does count and both publishes start; that is a
		// different question, asked of a different set of claims.)
		{"the other line writes an address", []int{1, 0}, true, true, false, false, false, false, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Three numbers the OS will hand out: the port the file fixes, the
			// port the bare entry is published on, and the container port —
			// which is also where a bare entry mirrors to, so it has to be one
			// this machine would give out.
			three := freePortsForSticky(t, 12)[:3]
			// Smallest first, so the row that asks whether a pinned span has an
			// upper end cannot be decided by which number the OS handed out
			// first: the port the file writes has to be BELOW the other two, or
			// a span read as open-ended covers nothing and the row passes for a
			// reason that has nothing to do with the rule.
			sort.Ints(three)
			fixed, bareOn, cp := three[0], three[1], three[2]
			if fixed == bareOn || bareOn == cp {
				t.Fatalf("the OS named the same port twice (%v) — the rows need three", three)
			}
			ports := []int{fixed, bareOn}
			if tc.onMirror {
				ports[1] = cp
			}
			proto := "tcp"
			bare := fmt.Sprintf("%d:%d", cp, cp)
			line := fmt.Sprintf("%d:%d", fixed, cp)
			if tc.udpAnswer {
				proto = "udp"
				bare = fmt.Sprintf("%d:%d/udp", cp, cp)
			}
			if tc.udpLine {
				line = fmt.Sprintf("%d:%d/udp", fixed, cp)
			}
			if tc.addrLine {
				line = fmt.Sprintf("127.0.0.1:%d:%d", fixed, cp)
			}
			if tc.bareRange {
				// Downwards from the port the row is about, so the range cannot
				// reach the OTHER answer: the ports the OS hands out come back
				// in sequence, and a range upwards from here would cover it
				// about as often as not — which would pin both answers and hand
				// the row back to the order.
				line = fmt.Sprintf("%d-%d:%d-%d", fixed-1, fixed, cp-1, cp)
			}
			specs := []string{bare}
			if tc.fileLine {
				specs = []string{line, bare}
				if !tc.bareLast {
					specs = []string{bare, line}
				}
			}
			auto := map[string]bool{bare: true}
			if tc.bareRange {
				auto[line] = true
			}
			a := &compose.Service{Image: "web:latest", Ports: specs, AutoHostPort: auto}
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{"a": a}}
			pm := func(i int) runtime.PortMapping {
				return runtime.PortMapping{HostAddress: "0.0.0.0", HostPort: ports[i],
					ContainerPort: cp, Proto: proto, Count: 1}
			}
			var rt *runtime.Runtime
			switch len(tc.answers) {
			case 1:
				rt = shimHolding(t, "a", ports[tc.answers[0]], cp, proto)
			default:
				rt = shimHoldingTwo(t, "a", pm(tc.answers[0]), pm(tc.answers[1]))
			}
			var out bytes.Buffer
			o := New(p, rt, "opossum", &out)
			mustAnswerWith(t, o, "a", len(tc.answers))
			o.remapAutoHostPorts([]string{"a"})
			got := ""
			for _, spec := range p.Services["a"].Ports {
				if p.Services["a"].AutoHostPort[spec] {
					got = spec
				}
			}
			suffix := ""
			if tc.udpAnswer {
				suffix = "/udp"
			}
			if tc.wantKeep < 0 {
				if got == fmt.Sprintf("%d:%d%s", fixed, cp, suffix) {
					t.Errorf("the bare entry kept %q, the host port %q fixes. It has to give it "+
						"up: the file asks for that port, and publishing it twice is refused", got, line)
				}
				if !strings.Contains(out.String(), strconv.Itoa(fixed)) {
					t.Errorf("nothing said %d, the host port this service was published on and "+
						"has just given up. A reader who is not told has to find it gone:\n%s",
						fixed, out.String())
				}
				return
			}
			want := fmt.Sprintf("%d:%d%s", ports[tc.wantKeep], cp, suffix)
			if got != want {
				t.Errorf("the bare entry published %q, want %q — the port its own container is on. "+
					"%q is the other answer, and %q is what the file fixes:\n%s",
					got, want, fmt.Sprintf("%d:%d%s", ports[1-tc.wantKeep], cp, suffix), line, out.String())
			}
			if out.Len() != 0 {
				t.Errorf("the entry stayed where it was, so nothing should have been said:\n%s",
					out.String())
			}
		})
	}
}

// The same question for two RANGES that both cover one container port. The
// runtime takes the pair (`-p 62934-62936:80-82 -p 62950-62952:81-83` starts and
// six listeners answer, measured on 1.4.1), so a container can publish one
// container port twice this way, and the file keeping only one of the ranges
// leaves the other as the bare entry's.
//
// The published spans here are that measured pair: two three-wide ranges whose
// container ports overlap by two. A fixture where the two cover exactly the same
// container ports would be a shape nobody has run on the runtime.
//
// The file either writes the range it kept AS PUBLISHED, or writes a NARROWED
// one — the same range minus its first port. Both have to come out the same way,
// and only the narrowed one can tell where a rule looks: narrowed, the answer
// this row is about sits on a port the file writes while the entry carrying it
// STARTS one below, on a port the file does not, so a rule asking where the
// entry starts gets the opposite answer. Written as published, both numbers are
// inside the line and the two questions collapse.
func TestARangeTheFileNoLongerWritesIsStillWhereTheEntryIs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		bFirst   bool
		narrowed bool
	}{
		{"the file's own range is listed first", true, false},
		{"the file's own range is listed second", false, false},
		{"the file has narrowed its range, listed first", true, true},
		{"the file has narrowed its range, listed second", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Two three-wide spans, far enough apart not to touch, and the
			// container ports they cover overlap by two. The second span is a
			// hundred above the first rather than a second number from the OS:
			// the ports it hands out come back in sequence, so asking for two
			// far apart is asking it to do something it does not do. Nothing
			// binds either span here — the entry keeps a port its own container
			// holds, so no probe is reached — and a row that stopped keeping it
			// fails on the port it published, not quietly.
			aLo := freePortForSticky(t)
			bLo := aLo + 100
			const cpLo = 48080 // container ports, not host ports: any number will do
			// a publishes cpLo..cpLo+2 from aLo and cpLo+1..cpLo+3 from bLo, so
			// cpLo+1 and cpLo+2 each have two answers. The row is about cpLo+2,
			// which b carries on its SECOND port — one above where b starts.
			aSpan := runtime.PortMapping{HostAddress: "0.0.0.0", HostPort: aLo,
				ContainerPort: cpLo, Proto: "tcp", Count: 3}
			bSpan := runtime.PortMapping{HostAddress: "0.0.0.0", HostPort: bLo,
				ContainerPort: cpLo + 1, Proto: "tcp", Count: 3}
			bLine := fmt.Sprintf("%d-%d:%d-%d", bLo, bLo+2, cpLo+1, cpLo+3)
			if tc.narrowed {
				bLine = fmt.Sprintf("%d-%d:%d-%d", bLo+1, bLo+2, cpLo+2, cpLo+3)
			}
			bare := fmt.Sprintf("%d:%d", cpLo+2, cpLo+2)
			a := &compose.Service{Image: "web:latest", Ports: []string{bLine, bare},
				AutoHostPort: map[string]bool{bare: true}}
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{"a": a}}
			first, second := aSpan, bSpan
			if tc.bFirst {
				first, second = bSpan, aSpan
			}
			var out bytes.Buffer
			o := New(p, shimHoldingTwo(t, "a", first, second), "opossum", &out)
			mustAnswerWith(t, o, "a", 2)
			o.remapAutoHostPorts([]string{"a"})
			got, want := p.Services["a"].Ports[1], fmt.Sprintf("%d:%d", aLo+2, cpLo+2)
			if got != want {
				t.Errorf("the bare entry published %q, want %q — the port the range the file no "+
					"longer writes has it on. %q is the range the file does write, and its ports "+
					"belong to that line:\n%s", got, want, bLine, out.String())
			}
			if out.Len() != 0 {
				t.Errorf("the entry stayed where it was, so nothing should have been said:\n%s",
					out.String())
			}
		})
	}
}

// Once an answer has been written over because the file pins the one that was
// there, the port that replaced it is the bare entry's — and a THIRD answer the
// file does not pin must not take its place. Keeping "the answer here is on a
// pinned port" set after it has been replaced does exactly that: the next
// unpinned answer reads as better than what is already recorded, and a range the
// file dropped hands the service a port its container is not on.
//
// Two answers cannot ask this. The question is what the third does to the
// second, so the shim answers three times about one container port: the line the
// file fixes, the port a bare entry was given, and a range the file no longer
// writes.
func TestAThirdAnswerDoesNotReplaceThePortThatReplacedTheFirst(t *testing.T) {
	// Three numbers from the OS: the port the file fixes, the port the bare entry
	// is on, and the container port (also where it would mirror to). The range's
	// host ports are built above the last of them, not asked for — sorted, so
	// there is a "last of them" to build above.
	three := freePortsForSticky(t, 12)[:3]
	sort.Ints(three)
	fixed, bareOn, cp := three[0], three[1], three[2]
	if fixed == bareOn || bareOn == cp {
		t.Fatalf("the OS named the same port twice (%v) — this row needs three", three)
	}
	rangeLo := cp + 10 // its second port is what covers cp, and it is nobody's
	bare := fmt.Sprintf("%d:%d", cp, cp)
	line := fmt.Sprintf("%d:%d", fixed, cp)
	a := &compose.Service{Image: "web:latest", Ports: []string{line, bare},
		AutoHostPort: map[string]bool{bare: true}}
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{"a": a}}
	single := func(hp int) runtime.PortMapping {
		return runtime.PortMapping{HostAddress: "0.0.0.0", HostPort: hp, ContainerPort: cp,
			Proto: "tcp", Count: 1}
	}
	// The range starts one container port below, so its SECOND port is the one
	// covering cp — a number no line of the file writes.
	span := runtime.PortMapping{HostAddress: "0.0.0.0", HostPort: rangeLo, ContainerPort: cp - 1,
		Proto: "tcp", Count: 3}
	var out bytes.Buffer
	o := New(p, shimHoldingThese(t, "a", single(fixed), single(bareOn), span), "opossum", &out)
	mustAnswerWith(t, o, "a", 3)
	o.remapAutoHostPorts([]string{"a"})
	got, want := p.Services["a"].Ports[1], fmt.Sprintf("%d:%d", bareOn, cp)
	if got != want {
		t.Errorf("the bare entry published %q, want %q — the port that replaced the one %q pins. "+
			"%d is the range's second port, and a range the file does not write is not where this "+
			"entry is listening:\n%s", got, want, line, rangeLo+1, out.String())
	}
	if out.Len() != 0 {
		t.Errorf("the entry stayed where it was, so nothing should have been said:\n%s", out.String())
	}
}
