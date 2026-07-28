package orchestrator

import (
	"fmt"
	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
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
	if !strings.Contains(airPlayHint("5000"), "AirPlay") || !strings.Contains(airPlayHint("7000"), "AirPlay") {
		t.Error("ports 5000/7000 should carry the AirPlay hint")
	}
	if airPlayHint("8080") != "" {
		t.Error("other ports should carry no hint")
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
