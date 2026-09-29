package orchestrator

import (
	"bytes"
	"fmt"
	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestHostPortBinding(t *testing.T) {
	cases := []struct {
		in, wantNet, wantAddr, wantPort string
		ok                              bool
	}{
		{"5000:5000", "tcp", ":5000", "5000", true},
		{"5000:5000/udp", "udp", ":5000", "5000", true},
		{"127.0.0.1:8080:80", "tcp", "127.0.0.1:8080", "8080", true},
		{"0.0.0.0:8080:80/tcp", "tcp", ":8080", "8080", true},
		{"[::1]:8080:80", "tcp", "[::1]:8080", "8080", true}, // IPv6 host preserved
		{"80", "", "", "", false},                            // container-only, host port unknown
		{"8000-8005:8000-8005", "", "", "", false},           // range — not probed
	}
	for _, c := range cases {
		nw, addr, port, ok := hostPortBinding(c.in)
		if ok != c.ok || nw != c.wantNet || addr != c.wantAddr || port != c.wantPort {
			t.Errorf("hostPortBinding(%q) = (%q,%q,%q,%v), want (%q,%q,%q,%v)",
				c.in, nw, addr, port, ok, c.wantNet, c.wantAddr, c.wantPort, c.ok)
		}
	}
}

func TestAirPlayHint(t *testing.T) {
	if !strings.Contains(airPlayHint(5000), "AirPlay") || !strings.Contains(airPlayHint(7000), "AirPlay") {
		t.Error("ports 5000/7000 should carry the AirPlay hint")
	}
	if airPlayHint(8080) != "" {
		t.Error("other ports should carry no hint")
	}
	// A host port that could not be read as a number reaches this as 0, which
	// is no port at all and carries no hint.
	if airPlayHint(0) != "" {
		t.Error("a port that is not a number should carry no hint")
	}
}

// #1272 C: the AirPlay hint is a guess at what a bare port number usually
// means, and naming this project's own service holding the port is not a
// guess — said together, they read as contradicting advice ("it's probably
// AirPlay" beside "it's this project's own service"). The hint is said only
// where there is no holder of this project's to name instead.
//
// The port under test is hardcoded to 5000 — the number airPlayHint keys
// off — rather than a listener this process holds (as the sibling tests in
// preflightownport_test.go do): probeHostPort is mocked below, so nothing
// ever really binds 5000, and a real net.Listen on it would risk colliding
// with an actual AirPlay Receiver on the machine running the test.
func TestAirPlayHintDoesNotAppearBesideOurOwnHolder(t *testing.T) {
	fakeProbe := func(network, address string) error { return syscall.EADDRINUSE }
	for _, tc := range []struct {
		name     string
		aPorts   string // "a"'s own compose entry; default is "5001:80"
		holder   []fakeContainer
		wantHeld bool
		wantAir  bool
	}{
		// "a" holds 5000 for real (fakeContainer) but its compose entry asks
		// for 5001, so "a" and "z" do not name the same host port in the
		// file — the in-file duplicate check (OPSM-213) stays out of the way
		// and this check is the one that finds the holder. Restarting frees
		// the port (takesItBack=false): "a"'s own entries don't publish 5000.
		{name: "our own service holds it, and lets the port go on restart",
			holder: []fakeContainer{{service: "a", hostPort: 5000}}, wantHeld: true, wantAir: false},
		{name: "nothing of ours holds it (control)", holder: nil,
			wantHeld: false, wantAir: true},
		// Same holder, but "a"'s own entries publish 5000 too (a range
		// covering it), so restarting takes the port straight back
		// (takesItBack=true) — a different branch through heldByUsHint,
		// and a guard of `holder == "" || takesItBack` would wrongly let
		// the AirPlay hint back in only here.
		{name: "our own service holds it and takes it straight back",
			aPorts: "5000-5001:80-81", holder: []fakeContainer{{service: "a", hostPort: 5000}},
			wantHeld: true, wantAir: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aPorts := tc.aPorts
			if aPorts == "" {
				aPorts = "5001:80"
			}
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: []string{aPorts}},
				"z": {Image: "web:latest", Ports: []string{"5000:90"}},
			}}
			o := New(p, shimFor(t, tc.holder...), "opossum", &bytes.Buffer{})
			o.probeHostPort = fakeProbe
			err := o.checkHostPorts([]string{"z", "a"})
			if err == nil {
				t.Fatal("checkHostPorts said nothing; wanted a refusal naming the port")
			}
			if got := strings.Contains(err.Error(), "held by this project's service"); got != tc.wantHeld {
				t.Errorf("held-by-us hint present = %v, want %v:\n%v", got, tc.wantHeld, err)
			}
			if got := strings.Contains(err.Error(), "AirPlay"); got != tc.wantAir {
				t.Errorf("AirPlay hint present = %v, want %v:\n%v", got, tc.wantAir, err)
			}
		})
	}
}

// The family a wildcard is probed with is the whole point of the fix, and it
// can't be observed through `up` on Linux — there a plain dual-stack bind already
// conflicts with an IPv4 listener, so the behavioural test passes even with the
// bug restored. Asserting the family choice directly guards it on every platform.
func TestProbeNetworks(t *testing.T) {
	cases := []struct {
		network, address string
		want             string
	}{
		// Wildcards are probed as IPv4: that's what Apple `container` publishes on.
		// An IPv4 probe already conflicts with a dual-stack listener, so adding IPv6
		// would only flag an IPv6-only listener the runtime binds alongside happily.
		{"tcp", ":8080", "tcp4"},
		{"tcp", "0.0.0.0:8080", "tcp4"},
		{"tcp", "[::]:8080", "tcp4"},
		{"udp", ":8080", "udp4"},
		// An address that names a host carries its own family; probe it as given.
		{"tcp", "127.0.0.1:8080", "tcp"},
		{"tcp", "[::1]:8080", "tcp"},
		{"udp", "127.0.0.1:8080", "udp"},
	}
	for _, c := range cases {
		got := probeNetworks(c.network, c.address)
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("probeNetworks(%q, %q) = %v, want [%s]", c.network, c.address, got, c.want)
		}
	}
}

func TestIsWildcardAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{":8080", true},           // every interface
		{"0.0.0.0:8080", true},    // same, spelled out
		{"[::]:8080", true},       // same, IPv6 spelling
		{"127.0.0.1:8080", false}, // a specific host
		{"[::1]:8080", false},
		{"", false}, // malformed: fall back to a single probe
		{"nonsense", false},
	}
	for _, c := range cases {
		if got := isWildcardAddr(c.addr); got != c.want {
			t.Errorf("isWildcardAddr(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

// shimWithPorts returns a runtime whose `inspect` reports a container in the
// given state publishing hostPort->containerPort, so the port-stickiness logic
// can be exercised without driving a whole `up`.
func shimWithPorts(t *testing.T, state string, hostPort, containerPort int) *runtime.Runtime {
	t.Helper()
	dir := t.TempDir()
	shim := filepath.Join(dir, "c.sh")
	body := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n  inspect) cat <<'J'\n"+
		`[{"status":{"state":"%s"},"configuration":{"labels":{"opossum.project":"demo"},`+
		`"publishedPorts":[{"containerPort":%d,"hostAddress":"0.0.0.0","hostPort":%d,"proto":"tcp"}]}}]`+
		"\nJ\n  ;;\n  system) echo 'status running' ;;\nesac\nexit 0\n", state, containerPort, hostPort)
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return &runtime.Runtime{Bin: shim}
}

// A STOPPED container doesn't hold its ports, so its recorded mapping is stale:
// reusing it would skip the busy check and hand back a port something else may
// have taken — the very failure this feature exists to prevent. Only a running
// container's ports are reused.
func TestRemapDoesNotReuseStoppedContainerPort(t *testing.T) {
	// Hold the port the "previous run" published on, so reusing it would be wrong.
	held, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	stale := held.Addr().(*net.TCPAddr).Port

	// And hold the mirror too, so the spec must move somewhere.
	mirror, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer mirror.Close()
	cport := mirror.Addr().(*net.TCPAddr).Port
	spec := fmt.Sprintf("%d:%d", cport, cport)

	for _, tc := range []struct {
		state     string
		wantStale bool
	}{
		{"running", true},  // it holds the port: reuse keeps the config hash stable
		{"stopped", false}, // it holds nothing: the stale port must be re-probed
	} {
		p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
			"web": {Name: "web", Image: "w", Ports: []string{spec}, AutoHostPort: map[string]bool{spec: true}},
		}}
		o := New(p, shimWithPorts(t, tc.state, stale, cport), "opossum", io.Discard)
		o.remapAutoHostPorts([]string{"web"})

		got := p.Services["web"].Ports[0]
		isStale := got == fmt.Sprintf("%d:%d", stale, cport)
		if isStale != tc.wantStale {
			t.Errorf("state=%s: got %q (stale=%v), want stale=%v", tc.state, got, isStale, tc.wantStale)
		}
		if tc.state == "stopped" && got == spec {
			t.Errorf("state=stopped: the taken mirror %q should have been moved", got)
		}
	}
}
