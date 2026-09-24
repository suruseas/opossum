package orchestrator

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

// Evals for where a moved port lands.
//
// The host port for a bare entry is asked of the operating system, which
// answers about listeners. The claims this run is avoiding are not listeners:
// a line in the compose file that fixes a port has nothing on it until that
// service starts. So the two can disagree, and the answer can be a port the
// file has already taken — including a port in the very range that caused the
// move, which leaves the notice saying a port was moved to somewhere the file
// already publishes, and leaves the runtime to refuse the pair.
//
// Asked here rather than through `up` on purpose. Whether the machine hands
// back a claimed port depends on where the kernel's ephemeral cursor happens
// to be: measured one way it happens every time, measured an hour later it
// happens in none of twelve tries. A test built on that measures the cursor,
// not opossum — and would go green when the defect is still there.

// tracked is a closer that remembers whether it has been closed, so a test
// can ask whether a refused answer was still being held.
type tracked struct{ closed bool }

func (t *tracked) Close() error { t.closed = true; return nil }

// answers stands in for the operating system. Asked for port 0 it answers with
// `any`; asked for a number it says whether that number can be bound, which is
// the only thing the system knows that opossum does not. `busy` lists the
// numbers it refuses.
//
// It records what each ask asked for — the network, the address and the number
// — because that is where this went wrong before: a seam that threw the
// arguments away let a udp entry be handed a port free only on tcp, and a walk
// that asked for the next number rather than the number past the claims cost
// one ask per claimed port.
type answers struct {
	any           int
	then          int // what a second "any port" answers, when a row needs one
	busy          []int
	asked         int
	given         []*tracked
	askedFor      []string
	askedPort     []int
	openWhenAsked []int
	t             *testing.T
}

func (a *answers) hold(network, address string, port int) (int, io.Closer, error) {
	a.askedFor = append(a.askedFor, network+" "+address)
	a.askedPort = append(a.askedPort, port)
	open := 0
	for _, c := range a.given {
		if !c.closed {
			open++
		}
	}
	a.openWhenAsked = append(a.openWhenAsked, open)
	a.asked++
	n := port
	if port == 0 {
		n = a.any
		if a.asked > 1 && a.then != 0 {
			n = a.then
		}
	}
	for _, b := range a.busy {
		if b == n {
			return 0, nil, errors.New("address already in use")
		}
	}
	c := &tracked{}
	a.given = append(a.given, c)
	return n, c, nil
}

// mustBeClosed says every socket this allocator handed out has been closed.
// Holding is a means, not an end: the answer that was refused is held only
// while a clear one is looked for, and the one chosen is handed to the runtime,
// which cannot bind a port opossum is still sitting on.
func (a *answers) mustBeClosed(t *testing.T) {
	t.Helper()
	for i, c := range a.given {
		if !c.closed {
			t.Errorf("the socket from ask %d was still held when the remap finished. Whatever "+
				"opossum is holding, nothing else on this machine can bind — including the "+
				"runtime, and including the service whose line claimed it.", i)
		}
	}
}

func (a *answers) heldWhenAsked() string { return fmt.Sprint(a.openWhenAsked) }

// askedPorts reads back the numbers the asks named, so a row can say that the
// walk stepped past a whole claim in one go rather than asking for the next
// number.
func (a *answers) askedPorts() string { return fmt.Sprint(a.askedPort) }

func (a *answers) askedOn() string { return fmt.Sprint(a.askedFor) }

// freeRun answers with the first of n consecutive ports nothing is listening
// on, and with room above for the widest fixture that uses it. It fails rather
// than skips: a row that does not run says nothing, and a skipped row reads as
// a passing one in the sweep that decides whether these tests guard anything.
func freeRunOf(t *testing.T, n int) int {
	t.Helper()
	for try := 0; try < 40; try++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		base := l.Addr().(*net.TCPAddr).Port
		l.Close()
		if base < 1024 || base > 65535-n-1 {
			continue
		}
		free := true
		for i := 1; i < n; i++ {
			c, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", base+i))
			if err != nil {
				free = false
				break
			}
			c.Close()
		}
		if free {
			return base
		}
	}
	t.Fatalf("no run of %d free ports on this machine after 40 tries", n)
	return 0
}

func freeRun(t *testing.T) int { return freeRunOf(t, 3) }

// quietShim answers `inspect` with nothing running, so the sticky path in
// remapAutoHostPorts finds no container of this project holding a port.
func quietShim(t *testing.T) *runtime.Runtime {
	t.Helper()
	dir := t.TempDir()
	shim := filepath.Join(dir, "c.sh")
	body := "#!/bin/sh\ncase \"$1\" in\n  inspect) echo '[]' ;;\n  system) echo 'status running' ;;\nesac\nexit 0\n"
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return &runtime.Runtime{Bin: shim}
}

// remapWith runs the remap over one bare entry that mirrors `mirror`, with `z`
// holding the claim written in `zPorts`, and `a` standing in for the operating
// system. It gives back what the bare entry ended up publishing and what was
// said about it.
func remapWith(t *testing.T, mirror string, zPorts []string, zAuto bool, alloc *answers) (published, said string) {
	t.Helper()
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
	o := New(p, quietShim(t), "opossum", &out)
	o.holdPort = alloc.hold
	o.remapAutoHostPorts([]string{"a", "z"})
	alloc.mustBeClosed(t)
	return p.Services["a"].Ports[0], out.String()
}

func TestAMovedPortIsNotOneTheFileHasAlreadySpokenFor(t *testing.T) {
	for _, tc := range []struct {
		name string
		// width is how many ports z's range covers, starting at base.
		width int
		// anyOff is what the operating system answers to "any port", as an
		// offset from base; busyOff are numbers it refuses by name.
		anyOff   int
		busyOff  []int
		proto    string
		wantOff  int    // the port the entry ends up on, as an offset from base
		askPorts string // the numbers the asks named ("PORT+n" for base+n)
		held     string
	}{
		// The system's first answer is clear of the claims. One ask, and
		// nothing about the walk comes into it.
		{"the first answer is clear of the claims", 3, 9, nil, "", 9, "[0]", "[0]"},
		// The answer is inside the range. The walk steps to the first number
		// past the range and asks for THAT — not for the next number, and not
		// for "any port" again.
		{"the answer is inside the range that caused the move", 3, 1, nil, "", 3, "[0 PORT+3]", "[0 1]"},
		// A range wider than the bound on the tries. Asking the system again
		// would cost one answer per claimed port and run out; stepping past
		// the range costs one.
		{"the range is wider than the number of tries", 60, 30, nil, "", 60, "[0 PORT+60]", "[0 1]"},
		// The number past the claims is itself in use. Then the walk does what
		// the bound is for: it steps on. A refused bind hands back no socket,
		// so the count of held answers does not grow at that ask.
		{"the port past the claims is in use", 3, 1, []int{3}, "", 4, "[0 PORT+3 PORT+4]", "[0 1 1]"},
		// The same question asked of udp. A claim is a host port AND a
		// protocol, and the number asked for has to be asked on the entry's
		// own protocol.
		{"a udp range", 3, 1, nil, "/udp", 3, "[0 PORT+3]", "[0 1]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := freeRunOf(t, tc.width+12)
			// The entry being moved mirrors a port inside the claim; what the
			// system answers is a separate axis, and rolling the two into one
			// number is why the first row here once did nothing at all.
			mirror := fmt.Sprintf("%[1]d:%[1]d%[2]s", base+1, tc.proto)
			claim := []string{fmt.Sprintf("%d-%d:80-%d", base, base+tc.width-1, 80+tc.width-1) + tc.proto}
			alloc := &answers{any: base + tc.anyOff}
			for _, off := range tc.busyOff {
				alloc.busy = append(alloc.busy, base+off)
			}
			published, said := remapWith(t, mirror, claim, false, alloc)
			want := fmt.Sprintf("%d:%d%s", base+tc.wantOff, base+1, tc.proto)
			if published != want {
				t.Errorf("a published %q, want %q. z's line fixes host ports %d-%d, so the entry "+
					"has to land outside them.", published, want, base, base+tc.width-1)
			}
			// What the asks asked for. The first is "any port"; the rest name
			// the number the walk chose, which is what makes a wide claim cost
			// one ask instead of one per port.
			gotPorts := alloc.askedPorts()
			for off := tc.width + 4; off >= 0; off-- {
				gotPorts = strings.ReplaceAll(gotPorts, fmt.Sprint(base+off), fmt.Sprintf("PORT+%d", off))
			}
			if gotPorts != tc.askPorts {
				t.Errorf("the asks named %s, want %s", gotPorts, tc.askPorts)
			}
			if got := alloc.heldWhenAsked(); got != tc.held {
				t.Errorf("the answers still open at each ask were %s, want %s — the refused answer "+
					"must be held while a clear one is looked for.", got, tc.held)
			}
			// Every ask went to the entry's own network AND its own address.
			// A candidate asked about on loopback says nothing about a
			// wildcard publish, and one asked about on tcp says nothing about
			// a udp entry.
			proto := "tcp"
			if tc.proto == "/udp" {
				proto = "udp"
			}
			wantOn := fmt.Sprintf("%s :%d", proto, base+1)
			for i, on := range alloc.askedFor {
				if on != wantOn {
					t.Errorf("ask %d went to %q, but the entry publishes on %q", i, on, wantOn)
				}
			}
			if !strings.Contains(said, "[OPSM-206]") {
				t.Errorf("no notice about the move:\n%s", said)
			}
			if got := strings.SplitN(published, ":", 2)[0]; !strings.Contains(said, "published it on "+got) {
				t.Errorf("the notice does not say it published on %s:\n%s", got, said)
			}
		})
	}
}

// Every number from the system's answer up to the last host port is spoken
// for. The walk has to stop, and what it says then has to be about the file.
func TestWhenEveryNumberAboveTheAnswerIsClaimedTheWalkStopsAndSaysSo(t *testing.T) {
	base := freeRunOf(t, 6)
	mirror := fmt.Sprintf("%[1]d:%[1]d", base+1)
	// A range from base to the last host port there is: nothing above the
	// answer is free for this project.
	claim := []string{fmt.Sprintf("%d-65535:%d-65535", base, base)}
	alloc := &answers{any: base + 1}
	published, said := remapWith(t, mirror, claim, true, alloc)
	if published != mirror {
		t.Errorf("a published %q, want the entry left where the file mirrored it (%q): there was "+
			"nowhere to move it to.", published, mirror)
	}
	if want := fmt.Sprintf("left the entry on %d", base+1); !strings.Contains(said, want) {
		t.Errorf("the notice does not say %q — the number it names has to be the one still "+
			"published:\n%s", want, said)
	}
	if !strings.Contains(said, "[OPSM-212]") {
		t.Errorf("nothing was said about why the entry did not move:\n%s", said)
	}
	if strings.Contains(said, "[OPSM-206]") {
		t.Errorf("the notice says the port was moved, and it was not:\n%s", said)
	}
	// The line that took the port is still named. That is the line the reader
	// has to change, and it is the whole reason this notice exists rather than
	// leaving the runtime to complain about publish specs.
	if want := fmt.Sprintf("publishes container ports %d-65535", base); !strings.Contains(said, want) {
		t.Errorf("the notice does not name the line that took the port (%q):\n%s", want, said)
	}
	// And it says what was tried, rather than picking a reason. The two
	// reasons come mixed — a walk steps over a claim and then meets a
	// listener — so naming one would be wrong half the time.
	if want := "tried 32 other host ports"; !strings.Contains(said, want) {
		t.Errorf("the notice does not say what was tried (%q):\n%s", want, said)
	}
}

// The last host port there is. A walk that stopped one short of it would call a
// file unplaceable while 65535 was free, and a walk that ran one past would ask
// the operating system for a port that does not exist.
func TestTheWalkReachesTheLastHostPort(t *testing.T) {
	base := freeRunOf(t, 6)
	mirror := fmt.Sprintf("%[1]d:%[1]d", base+1)
	// Everything from base to 65534 is the file's; only 65535 is left.
	claim := []string{fmt.Sprintf("%d-65534:%d-65534", base, base)}
	alloc := &answers{any: base + 1}
	published, said := remapWith(t, mirror, claim, true, alloc)
	if want := fmt.Sprintf("65535:%d", base+1); published != want {
		t.Errorf("a published %q, want %q — 65535 is the one host port left, and it is free.",
			published, want)
	}
	if strings.Contains(said, "[OPSM-212]") {
		t.Errorf("the notice says there was nowhere to move it, and there was:\n%s", said)
	}
	if want := fmt.Sprint(65535); !strings.Contains(said, "published it on "+want) {
		t.Errorf("the notice does not say it published on %s:\n%s", want, said)
	}
}

// And when the numbers past the claims are all held by something else, the
// tries run out rather than the walk running off the end. The bound is what
// stops that, and it counts listeners met — not claimed ports stepped over.
func TestWhenEveryCandidateIsHeldTheTriesRunOut(t *testing.T) {
	base := freeRunOf(t, 40)
	mirror := fmt.Sprintf("%[1]d:%[1]d", base+1)
	claim := []string{fmt.Sprintf("%d-%d:80-82", base, base+2)}
	alloc := &answers{any: base + 1}
	// Every candidate past the claim refuses to bind. The answer to "any
	// port" is not among them: a system that will not answer at all is a
	// different case, and a different test.
	for i := 3; i <= 40; i++ {
		alloc.busy = append(alloc.busy, base+i)
	}
	published, said := remapWith(t, mirror, claim, false, alloc)
	if asked := alloc.asked; asked != 33 {
		t.Errorf("the operating system was asked %d times, want 33 — one for \"any port\" and "+
			"32 candidates. The bound is written here rather than read from the constant, "+
			"because a test that reads the same constant it is checking compares a value "+
			"with itself.", asked)
	}
	if published != mirror {
		t.Errorf("a published %q, want the entry left on its mirror (%q)", published, mirror)
	}
	if !strings.Contains(said, "[OPSM-212]") {
		t.Errorf("nothing was said about why the entry did not move:\n%s", said)
	}
	if want := "tried 32 other host ports"; !strings.Contains(said, want) {
		t.Errorf("the notice does not say what was tried (%q):\n%s", want, said)
	}
	// It does not blame the file: these ports are held by something else, and
	// a reader sent to a line would find nothing wrong with it.
	if bad := "already publishes"; strings.Contains(said, bad) {
		t.Errorf("the notice blames the compose file for ports something else is holding:\n%s", said)
	}
	// The line that took the MIRRORED port is still named, and this fixture
	// writes it explicitly — the other row uses a mirrored range, so the two
	// wordings of `whyUnavailable` are each guarded by one row.
	if want := fmt.Sprintf("service %q asks for host port %d in the compose file", "z", base+1); !strings.Contains(said, want) {
		t.Errorf("the notice does not name the line that took the port (%q):\n%s", want, said)
	}
}

// The system's answer can be the last host port there is, or close to it. The
// walk goes upward, so from there it has almost nowhere to go — and the space
// below, which is most of the range, is not reachable by stepping up. Asking
// the system again is what covers it: the next answer is somewhere else.
//
// This is a real answer to get, not a contrived one: the answers are ephemeral
// ports, and the top of that range is one of them.
func TestAnAnswerAtTheTopOfTheRangeStillFindsRoom(t *testing.T) {
	base := freeRunOf(t, 6)
	mirror := fmt.Sprintf("%[1]d:%[1]d", base+1)
	// z fixes the port the entry mirrors, so the entry has to move; the system
	// answers with the very last host port, which is also spoken for.
	claim := []string{fmt.Sprintf("%d:80", base+1), "65535:81"}
	alloc := &answers{any: 65535, then: base + 4}
	published, said := remapWith(t, mirror, claim, false, alloc)
	if want := fmt.Sprintf("%d:%d", base+4, base+1); published != want {
		t.Errorf("a published %q, want %q — the walk ran off the top and the second answer is "+
			"where the room is.", published, want)
	}
	if strings.Contains(said, "[OPSM-212]") {
		t.Errorf("the notice says there was nowhere to move it, and there was:\n%s", said)
	}
	if got := alloc.askedPorts(); got != "[0 0]" {
		t.Errorf("the asks named %s, want [0 0] — off the top there is nothing to ask for by "+
			"number, so the system is asked to choose again.", got)
	}
}

// The second answer can be spoken for too. It is held while the walk carries on
// from it, for the same reason the first one is: a number opossum is still
// sitting on is one nothing else can take, and letting it go would put it back
// in the pool the very next ask draws from.
func TestASecondAnswerThatIsAlsoClaimedIsHeldWhileTheWalkGoesOn(t *testing.T) {
	base := freeRunOf(t, 6)
	mirror := fmt.Sprintf("%[1]d:%[1]d", base+1)
	claim := []string{fmt.Sprintf("%d:80", base+1), "65535:81"}
	// Both answers are ones the file publishes: the last host port, and then
	// the one the entry mirrors.
	alloc := &answers{any: 65535, then: base + 1}
	published, _ := remapWith(t, mirror, claim, false, alloc)
	if want := fmt.Sprintf("%d:%d", base+2, base+1); published != want {
		t.Errorf("a published %q, want %q", published, want)
	}
	if got, want := alloc.askedPorts(), fmt.Sprintf("[0 0 %d]", base+2); got != want {
		t.Errorf("the asks named %s, want %s", got, want)
	}
	if got := alloc.heldWhenAsked(); got != "[0 1 2]" {
		t.Errorf("the answers still open at each ask were %s, want [0 1 2] — a claimed answer is "+
			"held while the walk goes on from it.", got)
	}
}

// A held host port is one nobody else can take, on the family the entry will
// publish on. Holding is what lets the walk keep the refused answer out of the
// way while it looks, and what makes "this number binds" mean anything.
func TestAHeldHostPortIsOneNobodyElseCanTake(t *testing.T) {
	for _, tc := range []struct {
		name    string
		network string
		bind    func(port int) (io.Closer, error)
	}{
		{"tcp", "tcp", func(port int) (io.Closer, error) {
			return net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", port))
		}},
		// udp goes through ListenPacket, a second socket call with its own
		// close. A tcp-only row says nothing about it, and the entries this
		// whole area is about can be udp.
		{"udp", "udp", func(port int) (io.Closer, error) {
			return net.ListenPacket("udp4", fmt.Sprintf("0.0.0.0:%d", port))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, c, err := holdHostPort(tc.network, ":0", 0)
			if err != nil {
				t.Fatal(err)
			}
			// While the closer is open, the port is not free. Asserting "the
			// next ask answers with a different number" would not say this: on
			// macOS the kernel's cursor advances whether or not the last answer
			// is held, so that reading passes with the holding removed.
			if l, err := tc.bind(n); err == nil {
				l.Close()
				c.Close()
				t.Fatalf("host port %d could be bound while it was supposed to be held.", n)
			}
			// And asking for that very number, while it is held, is refused —
			// which is how the walk finds out that a candidate is in use.
			if _, c2, err := holdHostPort(tc.network, ":0", n); err == nil {
				c2.Close()
				c.Close()
				t.Fatalf("asking for host port %d while it was held was answered, so a candidate "+
					"in use would read as free.", n)
			}
			c.Close()
			// Once closed it is free again, and asking for it by name works —
			// otherwise the walk could never take the number it chose.
			got, c3, err := holdHostPort(tc.network, ":0", n)
			if err != nil {
				t.Fatalf("host port %d is still held after the closer ran: %v", n, err)
			}
			c3.Close()
			if got != n {
				t.Errorf("asked for host port %d and got %d", n, got)
			}
		})
	}
}

// An Orchestrator built without New has no allocator, and the remap must still
// ask the operating system rather than dereference nothing. Several tests build
// one as a struct literal; none of them reaches the remap today, and the day one
// does is not the day to find this out.
func TestAnOrchestratorBuiltWithoutNewStillAsksTheOperatingSystem(t *testing.T) {
	o := &Orchestrator{Project: &compose.Project{Name: "demo"}, out: io.Discard}
	n, err := o.freeHostPortClearOf("tcp", ":0", func(int) bool { return false })
	if err != nil || n == 0 {
		t.Fatalf("freeHostPortClearOf with no allocator = %d, %v; wanted a port from the "+
			"operating system", n, err)
	}
}

// When the operating system will not answer at all — out of descriptors, a
// sandbox that forbids binding — nothing is said and nothing moves. That is
// what happened before this change, and it is a different thing from "every
// answer was one this file publishes": no compose file can be edited to fix it.
func TestWhenTheOperatingSystemWillNotAnswerNothingIsSaid(t *testing.T) {
	base := freeRun(t)
	mirror := fmt.Sprintf("%[1]d:%[1]d", base+1)
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{mirror}, AutoHostPort: map[string]bool{mirror: true}},
		"z": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d-%d:80-82", base, base+2)}},
	}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	o.holdPort = func(network, address string, port int) (int, io.Closer, error) {
		return 0, nil, errors.New("too many open files")
	}
	o.remapAutoHostPorts([]string{"a", "z"})
	if got := p.Services["a"].Ports[0]; got != mirror {
		t.Errorf("a published %q, want the entry left where the file mirrored it (%q)", got, mirror)
	}
	if said := out.String(); strings.Contains(said, "[OPSM-212]") {
		t.Errorf("the notice blames the compose file for a refusal that has nothing to do with "+
			"it:\n%s", said)
	}
}

// The entry left behind is still opossum's to move on the next run: nothing
// about running out of room makes it a host port the user chose.
func TestAnEntryLeftBehindIsStillTheOneOpossumMayMove(t *testing.T) {
	base := freeRunOf(t, 6)
	mirror := fmt.Sprintf("%[1]d:%[1]d", base+1)
	claim := []string{fmt.Sprintf("%d-65535:%d-65535", base, base)}
	alloc := &answers{any: base + 1}
	z := &compose.Service{Image: "web:latest", Ports: claim,
		AutoHostPort: map[string]bool{claim[0]: true}}
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{mirror}, AutoHostPort: map[string]bool{mirror: true}},
		"z": z,
	}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	o.holdPort = alloc.hold
	o.remapAutoHostPorts([]string{"a", "z"})
	alloc.mustBeClosed(t)
	if !p.Services["a"].AutoHostPort[mirror] {
		t.Errorf("the entry %q is no longer marked as opossum's to move: %v. Running out of room "+
			"is not the user writing a host port down.", mirror, p.Services["a"].AutoHostPort)
	}
}

// The claims include ports this run itself handed out a moment ago, not only
// the ones the file writes down. Two bare entries on the same container port is
// the shape: the first takes a number, and the second must not be given it.
func TestAPortThisRunHasJustHandedOutIsAlsoAClaim(t *testing.T) {
	base := freeRunOf(t, 6)
	mirror := fmt.Sprintf("%[1]d:%[1]d", base)
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{mirror}, AutoHostPort: map[string]bool{mirror: true}},
		"b": {Image: "web:latest", Ports: []string{mirror}, AutoHostPort: map[string]bool{mirror: true}},
	}}
	// Nothing in the file fixes the port, so a keeps it. Then the system
	// offers b the number a has just been handed.
	alloc := &answers{any: base}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	o.holdPort = alloc.hold
	o.remapAutoHostPorts([]string{"a", "b"})
	alloc.mustBeClosed(t)
	got := []string{p.Services["a"].Ports[0], p.Services["b"].Ports[0]}
	if got[0] == got[1] {
		t.Fatalf("both services published %q. The second was given the host port the first had "+
			"just been handed, and nothing in the file writes that number down — so only this "+
			"run knows it is taken.", got[0])
	}
	if want := fmt.Sprintf("[%d:%d %d:%d]", base, base, base+1, base); fmt.Sprint(got) != want {
		t.Errorf("published %v, want %v (asked %d times)", got, want, alloc.asked)
	}
}
