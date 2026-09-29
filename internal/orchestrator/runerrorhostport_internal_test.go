package orchestrator

// Evals for #1272 A: the post-failure `[OPSM-201]` line (the `→` row under a
// failed start) used to always say "which opossum's pre-flight did not see.
// Remap the port in the compose file" — even when the holder is a container
// of this very project, where remapping the compose file changes nothing
// (the file already says what it says) and the actual fix is to bring the
// project down and up again, or, if the holder's own entries still publish
// the number, to change one of the two lines.
//
// The reachable case is a host-port RANGE: two entries naming the exact same
// host port are refused earlier, by refuseDuplicateHostPorts reading the
// compose file itself, and the pre-flight's own per-entry probe does not
// read a range either (hostPortBinding refuses to probe one). So a range
// overlapping another entry passes both checks — the pre-flight sees no
// conflict because it never asked about the range's own ports — and the
// collision only exists once the range's service has actually bound.

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// hostPortHolderInOrder asks the same question checkHostPorts asks before
// anything starts, asked again after a start fails. It should agree with the
// pre-flight in every case the two share: named only for a running container
// of THIS project that is in order, on the same host address, and
// takesItBack computed from that holder's own compose entries — not from the
// failing service's.
func TestHostPortHolderInOrder(t *testing.T) {
	held := heldHostPort(t)
	for _, tc := range []struct {
		name       string
		order      []string
		aSpec      string // "a"'s own compose entry
		bSpec      string // "b"'s own compose entry (the failing service); default is "<held>:90"
		containers []fakeContainer
		otherLabel string // non-"" makes a's container another project's
		wantHolder string
		wantBack   bool
	}{
		{
			name:       "a sibling this run already started holds it, and lets it go on restart",
			order:      []string{"a", "b"},
			aSpec:      fmt.Sprintf("%d:81", held+1), // a's own entry no longer names `held`
			containers: []fakeContainer{{service: "a", hostPort: held}},
			wantHolder: "a", wantBack: false,
		},
		{
			name:  "the same sibling, but its own entry still publishes the number",
			order: []string{"a", "b"},
			// a's range covers `held` too, so recreating it takes the port straight back.
			aSpec:      fmt.Sprintf("%d-%d:80-81", held, held+1),
			containers: []fakeContainer{{service: "a", hostPort: held}},
			wantHolder: "a", wantBack: true,
		},
		{
			name:       "nothing of this project's is running at all",
			order:      []string{"a", "b"},
			aSpec:      fmt.Sprintf("%d:80", held+1),
			containers: nil,
			wantHolder: "",
		},
		{
			name:       "a's container holds the port but on a different protocol",
			order:      []string{"a", "b"},
			aSpec:      fmt.Sprintf("%d:80", held+1),
			containers: []fakeContainer{{service: "a", hostPort: held, proto: "udp"}},
			wantHolder: "",
		},
		{
			// b's own spec (below) is a bare number — the wildcard address. a
			// really holds `held`, just not on an address b's entry asks of the
			// host: two services publishing the same number on different
			// addresses can both be running, and neither is the other's problem
			// (the same distinction refuseDuplicateHostPorts and hostPortsCollide
			// draw for the pre-flight's own duplicate check).
			name:       "a's container holds the number, but on a different address than b's entry asks for",
			order:      []string{"a", "b"},
			aSpec:      fmt.Sprintf("127.0.0.1:%d:80", held+1),
			containers: []fakeContainer{{service: "a", hostPort: held, address: "127.0.0.1"}},
			wantHolder: "",
		},
		{
			// b's own entry is a range and the holder sits in the MIDDLE of it —
			// neither endpoint. A boundary check that quietly turned into an
			// equality (hi mistaken for lo) would still pass every other row
			// here, because every other row's spec is a single port where lo and
			// hi are the same number.
			name:       "b's own entry is a range, held in the middle rather than at either end",
			order:      []string{"a", "b"},
			aSpec:      fmt.Sprintf("%d:80", held+5), // clear of both b's range and a's own held port
			bSpec:      fmt.Sprintf("%d-%d:90-92", held-1, held+1),
			containers: []fakeContainer{{service: "a", hostPort: held}},
			wantHolder: "a", wantBack: false,
		},
		{
			name:       "the holder is another project's container, not this one's",
			order:      []string{"a", "b"},
			aSpec:      fmt.Sprintf("%d:80", held+1),
			containers: []fakeContainer{{service: "a", hostPort: held}},
			otherLabel: "someone-else",
			wantHolder: "",
		},
		{
			name:       "the holder exists but is outside order (a profile this run does not start)",
			order:      []string{"b"}, // "a" is deliberately left out of order
			aSpec:      fmt.Sprintf("%d:80", held+1),
			containers: []fakeContainer{{service: "a", hostPort: held}},
			wantHolder: "",
		},
		{
			name:       "no order at all (a one-off has no run to scope the lookup to)",
			order:      nil,
			aSpec:      fmt.Sprintf("%d:80", held+1),
			containers: []fakeContainer{{service: "a", hostPort: held}},
			wantHolder: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bSpec := tc.bSpec
			if bSpec == "" {
				bSpec = fmt.Sprintf("%d:90", held)
			}
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: []string{tc.aSpec}},
				"b": {Image: "web:latest", Ports: []string{bSpec}},
			}}
			cs := tc.containers
			if tc.otherLabel != "" {
				cs = make([]fakeContainer, len(tc.containers))
				copy(cs, tc.containers)
				for i := range cs {
					cs[i].project = tc.otherLabel
				}
			}
			o := New(p, shimFor(t, cs...), "opossum", &bytes.Buffer{})
			holder, takesItBack := o.hostPortHolderInOrder(p.Services["b"], tc.order)
			if holder != tc.wantHolder {
				t.Errorf("holder = %q, want %q", holder, tc.wantHolder)
			}
			if holder != "" && takesItBack != tc.wantBack {
				t.Errorf("takesItBack = %v, want %v", takesItBack, tc.wantBack)
			}
		})
	}
}

// A service can publish more than one host port, and nothing here can tell
// which one the failed bind was actually for. When the entries that match
// anything at all point at two DIFFERENT services, naming either one would
// be a guess dressed up as an answer — no holder is named, and the generic
// wording (honest about not knowing) is what the reader gets instead of a
// wrong specific one.
func TestHostPortHolderInOrderNamesNobodyWhenTwoEntriesPointAtTwoDifferentHolders(t *testing.T) {
	p := heldHostPort(t)
	q := heldHostPort(t)
	proj := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:80", p+1)}}, // a's own entry: not p
		"c": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:80", q+1)}}, // c's own entry: not q
		"b": {Image: "web:latest", Ports: []string{
			fmt.Sprintf("%d:90", p), // matches a
			fmt.Sprintf("%d:91", q), // matches c
		}},
	}}
	rt := shimFor(t,
		fakeContainer{service: "a", hostPort: p},
		fakeContainer{service: "c", hostPort: q},
	)
	o := New(proj, rt, "opossum", &bytes.Buffer{})
	holder, _ := o.hostPortHolderInOrder(proj.Services["b"], []string{"a", "c", "b"})
	if holder != "" {
		t.Errorf("holder = %q, want none — a and c each hold one of b's two ports, and nothing here "+
			"knows which one actually failed to bind", holder)
	}
}

// The `→` line reuses the pre-flight's own wording once it can name a holder,
// rather than sending the reader to remap a file that is not the problem —
// and, like the pre-flight's AirPlay hint (#1272 C), does not pair a named
// holder with the DNS/AirPlay guess.
func TestRunErrorHintNamesHostPortHolder(t *testing.T) {
	svc := &compose.Service{Ports: []string{"53:53/udp"}} // would normally trigger the DNS culprit note
	stderr := "Error: failed to bootstrap container (cause: bind(descriptor:ptr:bytes:): Address already in use) (errno: 48)"
	for _, tc := range []struct {
		name        string
		holder      string
		takesItBack bool
		wantContain []string
		wantAbsent  []string
	}{
		{
			name:   "no holder found: the pre-flight-blind wording, with the DNS culprit",
			holder: "",
			wantContain: []string{"which opossum's pre-flight did not see", "Remap the port in the compose file",
				"on macOS, 53 is the runtime's built-in DNS"},
			wantAbsent: []string{"held by this project's service"},
		},
		{
			name:        "held by a service this run starts after this entry",
			holder:      "a",
			takesItBack: false,
			wantContain: []string{`held by this project's service "a"`,
				"take the project down and bring it up again to place both"},
			wantAbsent: []string{"which opossum's pre-flight did not see", "Remap the port in the compose file",
				"on macOS,", "runtime's built-in DNS"},
		},
		{
			name:        "held by a service whose own entries take the port straight back",
			holder:      "a",
			takesItBack: true,
			wantContain: []string{`held by this project's service "a"`,
				"whose own `ports` entries publish that number too, so it takes it straight back"},
			wantAbsent: []string{"which opossum's pre-flight did not see", "Remap the port in the compose file",
				"on macOS,", "runtime's built-in DNS"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := runErrorHint(svc, tc.holder, tc.takesItBack, runErr(stderr))
			for _, want := range tc.wantContain {
				if !strings.Contains(h, want) {
					t.Errorf("hint does not contain %q:\n%s", want, h)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(h, absent) {
					t.Errorf("hint should not contain %q:\n%s", absent, h)
				}
			}
		})
	}
}

// End to end: specificStartError, given the order an `up` is running, names a
// sibling's container as the holder instead of falling back to the generic
// pre-flight-blind wording — wired through decodeStartError's real plumbing
// rather than asserted against runErrorHint's arguments directly. "a"
// publishes a RANGE covering the port "b" asks for as a single number: the
// shape that actually reaches this code (see the file's header comment for
// why a plain duplicate host port does not).
func TestSpecificStartErrorNamesASiblingAlreadyRunning(t *testing.T) {
	held := heldHostPort(t)
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Name: "a", Image: "web:latest", Ports: []string{fmt.Sprintf("%d-%d:80-81", held, held+1)}},
		"b": {Name: "b", Image: "web:latest", Ports: []string{fmt.Sprintf("%d:90", held)}},
	}}
	o := New(p, shimFor(t, fakeContainer{service: "a", hostPort: held}), "opossum", &bytes.Buffer{})
	stderr := "Error: failed to bootstrap container (cause: bind(descriptor:ptr:bytes:): Address already in use) (errno: 48)"
	err := o.specificStartError("b", o.containerName("b"), []string{"a", "b"}, runErr(stderr))
	if err == nil {
		t.Fatal("specificStartError said nothing; wanted the decoded host-port hint")
	}
	if want := `held by this project's service "a", whose own`; !strings.Contains(err.Error(), want) {
		t.Errorf("did not name the running sibling as the holder:\n%v", err)
	}
	// Without order (a one-off, or any caller that has none to give), the same
	// failure falls back to the pre-flight-blind wording rather than silently
	// finding no holder in a way that looks the same as never having looked.
	err = o.specificStartError("b", o.containerName("b"), nil, runErr(stderr))
	if err == nil || !strings.Contains(err.Error(), "which opossum's pre-flight did not see") {
		t.Errorf("with no order to scope the lookup to, expected the generic wording, got: %v", err)
	}
}
