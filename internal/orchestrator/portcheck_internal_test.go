package orchestrator

import (
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
