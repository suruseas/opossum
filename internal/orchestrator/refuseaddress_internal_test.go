package orchestrator

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/runtime"

	"github.com/suruseas/opossum/internal/compose"
)

// An entry naming a host address this machine will not bind cannot publish
// anywhere, so nothing starts and the first line names the address. Both engines
// refuse such a file; what opossum adds is a message that says which address and
// which port (Apple `container` returns the same errno four levels down a chain
// of causes that names neither, measured on 1.4.1).
//
// The rows below decide, for each shape of entry, whether the address is asked
// about at all — and the ones that are asked also pin WHICH address is asked,
// since asking about the wrong one is how this went wrong before: the walk that
// picks host ports asks loopback for every entry, whatever address it writes.
func TestAnEntryNamingAnAddressThisHostWillNotBindIsRefused(t *testing.T) {
	const absent = "192.0.2.7" // TEST-NET-1: no machine routes it
	for _, tc := range []struct {
		name       string
		ports      []string
		notStarted bool     // the service is not one this run starts
		wantAsked  []string // network and host, in the order they were asked
		wantRefuse string   // the spec the refusal has to name, or "" for none
	}{
		// Nothing to ask about: no address is written down, or the one written is
		// every address.
		{"a bare entry", []string{"8080"}, false, nil, ""},
		{"the wildcard spelled out", []string{"0.0.0.0:8080:80"}, false, nil, ""},
		{"the IPv6 wildcard", []string{"[::]:8080:80"}, false, nil, ""},
		// A spec the loader never hands over: `normalizePort` mirrors
		// `127.0.0.1::80` into `127.0.0.1:80:80`, which IS asked about (see
		// TestAMirroredEntryIsAskedAboutBeforeItIsMoved). The row is here for the
		// reading of a spec with no host side — hostSide drops the address with
		// it — and not as a claim about what a file like that does.
		{"a spec with no host side, which the loader does not produce",
			[]string{"127.0.0.1::80"}, false, nil, ""},
		// A wildcard BESIDE an address. The rows above hold services whose entries
		// are all of one kind, and for those the question never gets past the
		// first guard — "does this service write any address at all". Only a
		// service that writes one and also writes an entry without one reaches the
		// reading of each entry in turn, which is where the wildcard is passed
		// over.
		{"a wildcard beside an address", []string{"0.0.0.0:8080:80", "127.0.0.1:8081:81"}, false,
			[]string{"tcp 127.0.0.1"}, ""},
		{"a bare entry beside an address", []string{"9090", "127.0.0.1:8081:81"}, false,
			[]string{"tcp 127.0.0.1"}, ""},
		// The same pair the other way round. The rows above all put the entry
		// with the address LAST, and a first guard that read only the last entry
		// would pass every one of them: a service whose address comes first would
		// then never be asked about at all.
		{"an address before a bare entry", []string{"127.0.0.1:8080:80", "9090"}, false,
			[]string{"tcp 127.0.0.1"}, ""},
		// (A wildcard-after-address row was here and came out: every mutation that
		// turned it red turned the bare one red too, and two mutations of the
		// first guard were caught by the bare one alone.)
		// Written down, and this machine has it.
		{"loopback", []string{"127.0.0.1:8080:80"}, false, []string{"tcp 127.0.0.1"}, ""},
		{"loopback on udp", []string{"127.0.0.1:8080:80/udp"}, false, []string{"udp 127.0.0.1"}, ""},
		// Written down, and it will not bind.
		{"an address nothing routes", []string{"192.0.2.7:8080:80"}, false,
			[]string{"tcp " + absent}, "192.0.2.7:8080:80"},
		// A RANGE has no one host port to probe, and the address is still one
		// this machine will not bind. Reading the address out of the port-probing
		// helper would leave every range unasked.
		{"a range on that address", []string{"192.0.2.7:8080-8082:80-82"}, false,
			[]string{"tcp " + absent}, "192.0.2.7:8080-8082:80-82"},
		// Two entries, one address: asked once. A file publishing twenty ports on
		// one address asks the operating system once, and every probe steps the
		// port the system hands out next.
		//
		// On an address that BINDS, because that is the pair that can tell: with a
		// bad one the first entry refuses and the loop is over, so one probe is
		// all there ever was, memo or no memo. (A row for the bad pair was here
		// and came out; no mutation turned it red that this one does not.)
		{"two entries on one good address", []string{"127.0.0.1:8080:80", "127.0.0.1:8081:81"}, false,
			[]string{"tcp 127.0.0.1"}, ""},
		// The good one is asked first and passes; the bad one is what refuses, and
		// the message names IT, not the entry that came before it.
		{"a good address then a bad one", []string{"127.0.0.1:8080:80", "192.0.2.7:8081:81"}, false,
			[]string{"tcp 127.0.0.1", "tcp " + absent}, "192.0.2.7:8081:81"},
		// An entry on udp is asked about on udp: the probe names the protocol,
		// because an interface can carry one and not the other. (The pair of
		// protocols on ONE address is the row below; this one is a single udp
		// entry, which is a different question — whether the protocol is carried
		// at all.)
		{"an entry written on udp", []string{"192.0.2.7:8080:80/udp"}, false,
			[]string{"udp " + absent}, "192.0.2.7:8080:80/udp"},
		// A service this run does not start is left alone, the same reading as the
		// duplicate-port check: a file naming an address for a service a profile
		// leaves out starts today, and turning that into a refusal is a separate
		// decision.
		{"a service this run does not start", []string{"192.0.2.7:8080:80"}, true, nil, ""},
		// The same address on both protocols is two questions, not one: an
		// interface can carry one and not the other, so the memo that keeps a
		// file publishing twenty ports from asking twenty times is keyed on the
		// protocol as well.
		{"one address, both protocols", []string{"127.0.0.1:8080:80", "127.0.0.1:8081:81/udp"}, false,
			[]string{"tcp 127.0.0.1", "udp 127.0.0.1"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: tc.ports},
			}}
			var out bytes.Buffer
			o := New(p, quietShim(t), "opossum", &out)
			var asked []string
			o.bindHostAddress = func(network, host string) error {
				asked = append(asked, network+" "+host)
				if host == absent {
					return errors.New("bind: can't assign requested address")
				}
				return nil
			}
			order := []string{"a"}
			if tc.notStarted {
				order = nil
			}
			err := o.refuseUnbindableHostAddresses(order)
			if tc.wantRefuse == "" {
				if err != nil {
					t.Errorf("refused %v, want the file to start: %q names no address this host "+
						"will not bind", err, tc.ports)
				}
			} else {
				if err == nil {
					t.Fatalf("nothing refused %q, and %s is an address this host will not bind — "+
						"the runtime's own refusal names neither the address nor the port",
						tc.wantRefuse, absent)
				}
				// The address is looked for in the SENTENCE, not just anywhere: the
				// spec it came from holds it as a substring, so a message that
				// dropped the address from its own words would still contain it.
				// Through the colon that ends the clause: "will not bind 192.0.2.7"
				// is a prefix of "will not bind 192.0.2.7:8080:80:", so a message
				// naming the ENTRY where the address belongs would pass a check
				// that stopped at the number. The advice was lengthened for the
				// same reason one round earlier; this is the same trap in the
				// first sentence.
				for _, want := range []string{string(codeHostAddressUnbindable), tc.wantRefuse,
					"will not bind " + absent + ":\n", "bind: can't assign requested address",
					`service "a"`} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("the refusal does not say %q, so a reader cannot act on it:\n%v", want, err)
					}
				}
			}
			if strings.Join(asked, ",") != strings.Join(tc.wantAsked, ",") {
				t.Errorf("asked about %v, want %v — %q", asked, tc.wantAsked, tc.ports)
			}
		})
	}
}

// Through `Up`, because what matters is which of two refusals the reader gets —
// and calling the parts in order inside a test would only show that the test
// called them in that order.
//
// Before this, an address this machine will not bind came back as
// `[OPSM-201] host port already in use`: the probe binds the address and the port
// together, so the address failing reads as the port being held. The port is not
// held — nothing is listening on it — and the advice that comes with that code is
// to free it or move it, neither of which changes anything. So the row asserts
// both halves: the refusal that arrives, and the one that must not.
func TestUpSaysTheAddressIsTheProblemAndNotThePort(t *testing.T) {
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{"192.0.2.7:8080:80"}},
	}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	asked := 0
	o.bindHostAddress = func(network, host string) error {
		asked++
		return errors.New("bind: can't assign requested address")
	}
	err := o.Up(true)
	if err == nil {
		t.Fatal("Up started a file whose only entry names an address this host will not bind")
	}
	if !strings.Contains(err.Error(), string(codeHostAddressUnbindable)) {
		t.Errorf("Up refused with %v, want %s — the address is what cannot be had",
			err, codeHostAddressUnbindable)
	}
	if strings.Contains(err.Error(), string(codeHostPortInUse)) {
		t.Errorf("Up still blames the host port:\n%v\nNothing is listening on it, and the advice "+
			"that comes with %s (free it, or move it) changes nothing here", err, codeHostPortInUse)
	}
	if asked != 1 {
		t.Errorf("the address was asked about %d times, want once", asked)
	}
}

// The same question through `Up`, with the address one this machine really will
// not bind — no stub in the way, so the probe that runs is the one users get. This is the row that used to live in the
// table for "a host port this run frees itself", where it asserted that the run
// went ON: the probe binds the address and the port together, so an address that
// cannot be bound read as a port in use, and a leniency let it through.
//
// The address is ::2. IPv6 gives LOOPBACK the single address ::1/128, where v4
// gives it all of 127/8 — a machine can of course hold global or link-local IPv6
// addresses, but ::2 is not one anything assigns. That difference from v4 is why
// this row does not reach for another number in 127/8 — macOS assigns only 127.0.0.1
// and Linux assigns the whole block, so `127.0.0.2` is absent on this machine and
// present in the container the push-time gate runs in. Measured in both: ::1
// binds, ::2 and 192.0.2.7 do not, 127.0.0.2 splits.
func TestUpRefusesAnAddressTheMachineReallyWillNotBind(t *testing.T) {
	port := freePortForSticky(t)
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:80", port)}},
		"z": {Image: "web:latest", Ports: []string{fmt.Sprintf("[::2]:%d:90", port)}},
	}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	err := o.Up(true)
	if err == nil {
		t.Fatalf("Up accepted a file publishing on [::2], which this machine will not "+
			"bind. Before the address was asked about, it read as host port %d being in use — of a "+
			"port nothing is listening on", port)
	}
	if !strings.Contains(err.Error(), string(codeHostAddressUnbindable)) {
		t.Errorf("refused with %v, want %s", err, codeHostAddressUnbindable)
	}
	if strings.Contains(err.Error(), string(codeHostPortInUse)) {
		t.Errorf("still blames the host port:\n%v", err)
	}
}

// A service that runs to completion before its dependent starts IS asked about.
// The duplicate check leaves those out because two entries collide only if they
// publish at the same time, and this one does not overlap its dependent — but it
// does publish while it runs, and an address it cannot bind stops it then. The
// reason does not carry over, so the guard does not either.
func TestAServiceThatRunsToCompletionIsAskedAboutItsAddress(t *testing.T) {
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{"192.0.2.7:8080:80"}},
		"b": {Image: "web:latest", DependsOn: compose.DependsOn{
			{Name: "a", Condition: compose.ConditionCompleted},
		}},
	}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	var asked []string
	o.bindHostAddress = func(network, host string) error {
		asked = append(asked, host)
		return errors.New("bind: can't assign requested address")
	}
	err := o.refuseUnbindableHostAddresses([]string{"a", "b"})
	if err == nil {
		t.Errorf("accepted a one-shot service on an address it cannot bind — it publishes while it " +
			"runs, and its dependent waits for it to finish")
	}
	if len(asked) != 1 {
		t.Errorf("asked about %v, want the one address once", asked)
	}
}

// Only a container of THIS project that is actually running is a reason to leave
// the address alone. Every other answer the runtime can give is a service that is
// about to be started, and an address it cannot bind stops it.
//
// The values below are what `Inspect` can really answer, and they are here because
// four mutations of this guard lived without them: a fixture that only ever said
// "the runtime did not answer" cannot tell not-found from running, stopped from
// running, or created from running. The first `up` of a project is the not-found
// one, so a guard that read it as running would switch this check off exactly
// where it matters most.
func TestOnlyAContainerOfOursThatIsRunningLeavesTheAddressAlone(t *testing.T) {
	for _, tc := range []struct {
		name     string
		state    string // what the runtime says, "" for not found
		project  string // the project label on it, "" for ours
		notFound bool
		unknown  bool // the runtime could not be asked at all
		wantAsk  bool
	}{
		{name: "a container of ours is running", state: "running"},
		// The first `up` of a project: nothing has been made yet. `Inspect` says
		// not found, which is Exists false AND Unknown false — the pair no other
		// row here produces.
		{name: "nothing has been made yet", notFound: true, wantAsk: true},
		// The runtime answered with an empty list, which is not the same as "not
		// found": it is "could not be read". Left as a reason to ask, because an
		// answer nobody can read is not a container we are up on.
		{name: "the runtime did not answer", unknown: true, wantAsk: true},
		{name: "a container of ours has stopped", state: "stopped", wantAsk: true},
		{name: "a container of ours was created and not started", state: "created", wantAsk: true},
		{name: "a container of another project is running", state: "running",
			project: "somebody-else", wantAsk: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: []string{"192.0.2.7:8080:80"}},
			}}
			var out bytes.Buffer
			rt := quietShim(t) // answers [], which reads as "could not be read"
			if !tc.unknown {
				project := tc.project
				if project == "" {
					project = "demo"
				}
				rt = shimInState(t, "a", project, tc.state, tc.notFound)
			}
			o := New(p, rt, "opossum", &out)
			asked := 0
			o.bindHostAddress = func(network, host string) error {
				asked++
				return errors.New("bind: can't assign requested address")
			}
			err := o.refuseUnbindableHostAddresses([]string{"a"})
			if !tc.wantAsk {
				if err != nil {
					t.Errorf("refused %v — a container of ours is up on it, and a re-up publishes "+
						"nothing new", err)
				}
				if asked != 0 {
					t.Errorf("asked about the address %d times, want none", asked)
				}
				return
			}
			if err == nil {
				t.Errorf("accepted it: this service is about to be started, and the address it " +
					"names cannot be bound")
			}
			if asked != 1 {
				t.Errorf("asked about the address %d times, want once", asked)
			}
		})
	}
}

// A running service does not stop the walk: the service behind it is still asked
// about. Leaving the address alone is per service, and a guard that returned from
// the whole check on the first running one would let every service after it
// through.
func TestARunningServiceDoesNotEndTheWalk(t *testing.T) {
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{"127.0.0.1:8080:80"}},
		"z": {Image: "web:latest", Ports: []string{"192.0.2.7:8081:81"}},
	}}
	var out bytes.Buffer
	// `a` is up on its address; `z` has never been made.
	o := New(p, shimInState(t, "a", "demo", "running", false), "opossum", &out)
	asked := 0
	o.bindHostAddress = func(network, host string) error {
		asked++
		if host == "192.0.2.7" {
			return errors.New("bind: can't assign requested address")
		}
		return nil
	}
	err := o.refuseUnbindableHostAddresses([]string{"a", "z"})
	if err == nil {
		t.Fatalf("accepted the file: %q is behind a running service, and its address cannot be "+
			"bound. %d addresses were asked about", "z", asked)
	}
	if !strings.Contains(err.Error(), `service "z"`) {
		t.Errorf("the refusal does not name z:\n%v", err)
	}
	if asked != 1 {
		t.Errorf("asked about %d addresses, want one — a's is left alone and z's is asked", asked)
	}
}

// shimInState answers about one container in a state the caller picks. notFound
// makes it fail the way the runtime does when nothing of that name exists, which
// `Inspect` reads as Exists false and Unknown false — the shape the first `up` of
// a project sees, and the one an empty list does NOT produce.
func shimInState(t *testing.T, service, project, state string, notFound bool) *runtime.Runtime {
	t.Helper()
	dir := t.TempDir()
	shim := filepath.Join(dir, "c.sh")
	answer := fmt.Sprintf(`[{"status":{"state":%q},"configuration":{"labels":{"opossum.project":%q},`+
		`"publishedPorts":[{"containerPort":80,"count":1,"hostAddress":"0.0.0.0","hostPort":8080,`+
		`"proto":"tcp"}]}}]`, state, project)
	inspect := "cat <<'J'\n" + answer + "\nJ"
	if notFound {
		inspect = "echo 'Error: container not found' >&2; exit 1"
	}
	// The `;;` goes on a line of its own: with a heredoc above it, `J ;;` is not
	// the terminator the shell is looking for, and the document never ends.
	body := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n  inspect)\n    case \"$2\" in\n"+
		"      %s.demo.opossum)\n%s\n      ;;\n      *) echo '[]' ;;\n    esac\n  ;;\n"+
		"  system) echo 'status running' ;;\nesac\nexit 0\n", service, inspect)
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return &runtime.Runtime{Bin: shim}
}

// The udp branch of the probe, run for real. Every row above answers through a
// stub, so the two lines that build a udp probe were never executed: a mutation
// that made them refuse everything, and one that swallowed their error, both
// lived. Asked of bindHostAddress directly, because what is being checked is that
// those two lines work at all.
func TestTheProbeBindsUdpForReal(t *testing.T) {
	if err := bindHostAddress("udp", "127.0.0.1"); err != nil {
		t.Errorf("udp on loopback: %v, want it to bind — a machine that cannot would fail every "+
			"udp entry it publishes", err)
	}
	if err := bindHostAddress("udp", "::2"); err == nil {
		t.Error("udp on ::2 bound, and nothing assigns that address — the probe is answering yes " +
			"to an address it cannot have")
	}
	// The tcp side beside it, so a mutation that swaps the two branches has a row
	// on both sides of the swap.
	if err := bindHostAddress("tcp", "127.0.0.1"); err != nil {
		t.Errorf("tcp on loopback: %v, want it to bind", err)
	}
	if err := bindHostAddress("tcp", "::2"); err == nil {
		t.Error("tcp on ::2 bound, and nothing assigns that address")
	}
}

// A file with BOTH faults — two entries on one host port, and an address this
// machine will not bind — hears about the address. Either order costs the reader
// two passes, since the two faults are independent; the choice is which one to
// hear, and an entry that cannot publish anywhere comes before a pair that cannot
// both be had.
func TestTheAddressIsSaidBeforeTheDuplicatePair(t *testing.T) {
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{"192.0.2.7:8080:80"}},
		"z": {Image: "web:latest", Ports: []string{"192.0.2.7:8080:81"}},
	}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	err := o.Up(true)
	if err == nil {
		t.Fatal("Up started a file with two entries on one host port, on an address this host will " +
			"not bind")
	}
	if !strings.Contains(err.Error(), string(codeHostAddressUnbindable)) {
		t.Errorf("Up said %v, want %s first — changing the port would leave the address as it is",
			err, codeHostAddressUnbindable)
	}
	if strings.Contains(err.Error(), string(codeHostPortTwice)) {
		t.Errorf("Up said %s as well:\n%v", codeHostPortTwice, err)
	}
}

// The port the file wrote is in the advice, so a reader who drops the address
// knows which port goes onto every address. Nothing else in the message carries
// it — the spec does, but a message that lost the number from its own words would
// still hold the spec.
func TestTheAdviceNamesTheHostPortTheEntryAsksFor(t *testing.T) {
	const port = "48081"
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{"192.0.2.7:" + port + ":80"}},
	}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	o.bindHostAddress = func(network, host string) error { return errors.New("no") }
	err := o.refuseUnbindableHostAddresses([]string{"a"})
	if err == nil {
		t.Fatal("nothing refused an address the probe said no to")
	}
	if !strings.Contains(err.Error(), "host port "+port+" on EVERY") {
		t.Errorf("the advice does not say which port goes onto every address:\n%v", err)
	}
	// And which address to look for on the machine. The sentence before it names
	// the address as the thing that will not bind; this one is what to check.
	// To the end of the clause: "has 192.0.2.7:8080:80 and" starts with
	// "has 192.0.2.7", so a message naming the ENTRY where the address belongs
	// would pass a check that stopped at the number.
	if !strings.Contains(err.Error(), "check that the machine has 192.0.2.7 and that its interface") {
		t.Errorf("the advice does not say which address to check for:\n%v", err)
	}
}

// A range is ports, plural. The advice offers to drop the address, and a reader
// dropping it puts every port of the range onto every address — saying "host port
// 8080-8082" would read as one.
func TestTheAdviceSaysPortsForARange(t *testing.T) {
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{"192.0.2.7:48081-48083:80-82"}},
	}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	o.bindHostAddress = func(network, host string) error { return errors.New("no") }
	err := o.refuseUnbindableHostAddresses([]string{"a"})
	if err == nil {
		t.Fatal("nothing refused a range on an address the probe said no to")
	}
	if !strings.Contains(err.Error(), "host ports 48081-48083 on EVERY") {
		t.Errorf("the advice speaks of one port where the file names three:\n%v", err)
	}
}

// `--dry-run` resolves the plan and prints it, and it refuses what `up` would
// refuse: a plan that cannot run is not a plan. The port check does the same, and
// a reader asking what WOULD happen is told the same thing they would be told.
func TestDryRunRefusesItToo(t *testing.T) {
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{"192.0.2.7:8080:80"}},
	}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	o.SetDryRun(true)
	err := o.Up(true)
	if err == nil || !strings.Contains(err.Error(), string(codeHostAddressUnbindable)) {
		t.Errorf("dry run said %v, want the %s refusal — the plan it would print cannot run",
			err, codeHostAddressUnbindable)
	}
}

// An entry the file wrote WITHOUT a host port — `<address>::<container port>` — is
// asked about too, and this is the shape that made the check's position matter.
// The loader turns it into `<address>:<container port>:<container port>` and marks
// it as one opossum may move; the walk that moves it probes the address and the
// port together, reads "in use" of a port nothing is listening on, moves the entry
// to a second port on that same unbindable address, and says so with `OPSM-206`.
// Asked before the walk, none of that happens.
//
// The number in the message is the container port the loader mirrored to, not one
// opossum chose: the file's own `192.0.2.7::80` becomes `192.0.2.7:80:80`. That
// spelling is not in the file either — a grep for it finds nothing — but a reader
// can tell it is their line, because the address and the container port are both
// theirs. A port in the sixty-thousands could not be told that way.
func TestAMirroredEntryIsAskedAboutBeforeItIsMoved(t *testing.T) {
	const cp = "48080"
	spec := "192.0.2.7:" + cp + ":" + cp // what the loader makes of "192.0.2.7::48080"
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{spec}, AutoHostPort: map[string]bool{spec: true}},
	}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	err := o.Up(true)
	if err == nil {
		t.Fatal("Up started a mirrored entry on an address this host will not bind")
	}
	if !strings.Contains(err.Error(), string(codeHostAddressUnbindable)) {
		t.Errorf("Up said %v, want %s", err, codeHostAddressUnbindable)
	}
	if !strings.Contains(err.Error(), spec) {
		t.Errorf("the refusal does not name %q, the entry as the file's own line becomes:\n%v",
			spec, err)
	}
	if strings.Contains(out.String(), string(codeHostPortRemapped)) {
		t.Errorf("the entry was moved first, and told so:\n%s\nThere is nowhere to move it to: "+
			"every port on that address is as unbindable as the first", out.String())
	}
	if strings.Contains(err.Error(), string(codeHostPortInUse)) {
		t.Errorf("the refusal still blames a port:\n%v", err)
	}
}

// The runtime is not asked about a service that writes no address. A `up` of
// twenty services that name none must not cost twenty inspects for a question
// none of them pose — and a test elsewhere counts those calls, which is how the
// cost was noticed.
func TestTheRuntimeIsNotAskedAboutAServiceThatWritesNoAddress(t *testing.T) {
	for _, tc := range []struct {
		name         string
		ports        []string
		wantInspects int // how many times the runtime is asked whether our container is up
	}{
		{"no address anywhere", []string{"8080:80", "0.0.0.0:8081:81", "9090"}, 0},
		{"one address among them", []string{"8080:80", "127.0.0.1:8081:81"}, 1},
		{"three addresses in one service", []string{"127.0.0.1:8080:80", "127.0.0.1:8081:81",
			"127.0.0.1:8082:82"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: tc.ports},
			}}
			var out bytes.Buffer
			rt, inspects := inspectTallyShim(t)
			o := New(p, rt, "opossum", &out)
			o.bindHostAddress = func(network, host string) error { return nil }
			if err := o.refuseUnbindableHostAddresses([]string{"a"}); err != nil {
				t.Fatalf("refused %v, want it to pass", err)
			}
			// The exact count, not "more than none": asking twice about one
			// service is the shape of the cost this is here to hold down.
			if got := inspects(); got != tc.wantInspects {
				t.Errorf("the runtime was asked %d times, want %d — %q", got, tc.wantInspects, tc.ports)
			}
		})
	}
}

// inspectTallyShim answers like quietShim and counts the inspects. (There is a
// countingShim in this package already, with a different shape.)
func inspectTallyShim(t *testing.T) (*runtime.Runtime, func() int) {
	t.Helper()
	dir := t.TempDir()
	shim := filepath.Join(dir, "c.sh")
	tally := filepath.Join(dir, "n")
	body := "#!/bin/sh\ncase \"$1\" in\n  inspect) echo x >> " + tally + "; echo '[]' ;;\n" +
		"  system) echo 'status running' ;;\nesac\nexit 0\n"
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return &runtime.Runtime{Bin: shim}, func() int {
		b, err := os.ReadFile(tally)
		if err != nil {
			return 0
		}
		return bytes.Count(b, []byte("\n"))
	}
}

// The memo spans the whole walk, not one service: a project where three services
// publish on one address asks the operating system once. The comment on it says
// "a file publishing twenty ports on one address asks once", and a memo made
// inside the loop over services would ask once PER SERVICE while every row about
// one service stayed green.
//
// On an address that binds, because a bad one refuses on the first service and
// the walk is over — one probe is all there ever was then, memo or no memo.
func TestTheMemoSpansTheWholeWalkAndNotOneService(t *testing.T) {
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{"127.0.0.1:8080:80"}},
		"b": {Image: "web:latest", Ports: []string{"127.0.0.1:8081:81"}},
		"c": {Image: "web:latest", Ports: []string{"127.0.0.1:8082:82"}},
	}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	var asked []string
	o.bindHostAddress = func(network, host string) error {
		asked = append(asked, network+" "+host)
		return nil
	}
	if err := o.refuseUnbindableHostAddresses([]string{"a", "b", "c"}); err != nil {
		t.Fatalf("refused %v, want it to pass", err)
	}
	if len(asked) != 1 {
		t.Errorf("asked %v — three services on one address, want the address asked about once", asked)
	}
}

// The service a running container is left alone for is left alone whatever the
// file now says. That is wider than "nothing has changed", and the changelog says
// so: the entry can have been edited to an address that cannot be bound, and this
// check still passes it to the walk, where the container is replaced and the
// replacement fails with the runtime's own error.
//
// ★This row guards nothing of today's code. No mutation of the guards as they
// stand turns only it red: its only difference from the rows above is the port the
// file names, and nothing here reads the port. What it holds down is the SENTENCE
// in the public note — that the leniency does not read the file — so that a later
// guard which did read it (the config hash, say) could not be added without this
// row being looked at. The fixture cannot pose the hash question either way: the
// shim carries no hash label, so every row above is "edited" on that axis too.
//
// Kept as a declaration, not as a guard, and said so here because a row that
// looks like a guard and is not is worse than no row.
func TestARunningServiceIsLeftAloneEvenWhenTheEntryHasChanged(t *testing.T) {
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		// The container answers about port 8080; the file now says something else
		// entirely, on an address nothing routes.
		"a": {Image: "web:latest", Ports: []string{"192.0.2.7:9999:99"}},
	}}
	var out bytes.Buffer
	o := New(p, shimInState(t, "a", "demo", "running", false), "opossum", &out)
	asked := 0
	o.bindHostAddress = func(network, host string) error {
		asked++
		return errors.New("bind: can't assign requested address")
	}
	if err := o.refuseUnbindableHostAddresses([]string{"a"}); err != nil {
		t.Errorf("refused %v — the leniency does not read the file, and the note says as much", err)
	}
	if asked != 0 {
		t.Errorf("asked about the address %d times, want none", asked)
	}
}
