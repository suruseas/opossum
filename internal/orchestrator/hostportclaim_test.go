package orchestrator_test

// Evals for what counts as a host port this project has already spoken for.
//
// A published port is claimed by its host port and its protocol, and by
// nothing else. The key used to be the host address the entry named, which
// was wrong in both directions at once: it left out the protocol, so a
// service publishing 8080/udp made opossum hand the next bare 8080/tcp entry
// some other port — a port the file never asked for and the runtime would
// have given it; and it kept the address, so a `127.0.0.1:8080` entry did not
// stop opossum handing out the wildcard 8080, which overlaps it. The runtime
// then refused the pair opossum had assembled (`host ports for different
// publish port specs may not overlap`), and the project did not start at all.

import (
	"bytes"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// freePort answers with a port nothing is listening on, so that the only
// thing standing between the two entries is what this project claimed.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestAClaimedHostPortIsTheOneProtocolItNames(t *testing.T) {
	port := freePort(t)
	for _, tc := range []struct {
		name     string
		specs    []string
		auto     string // the entry opossum may move
		wantKept bool   // whether that entry keeps the mirrored port
	}{
		// Each row writes the claim as an entry of its own — a host port the
		// file named, which opossum never moves — and then a mirrored entry
		// on the same host port. What differs between the rows is only what
		// the claim says about protocol and address.
		//
		// The claim is udp's; the tcp entry is free to mirror.
		{"udp claimed, tcp mirrored", []string{"%[1]d:80/udp", "%[1]d:%[1]d/tcp"}, "%[1]d:%[1]d/tcp", true},
		// Same protocol: the claim stands and the mirror moves.
		{"tcp claimed, tcp mirrored", []string{"%[1]d:80/tcp", "%[1]d:%[1]d/tcp"}, "%[1]d:%[1]d/tcp", false},
		// A claim on one address covers the wildcard: the two overlap, and
		// the runtime refuses the pair if both are published.
		{"an address-bound claim covers the wildcard", []string{"127.0.0.1:%[1]d:80/tcp", "%[1]d:%[1]d/tcp"}, "%[1]d:%[1]d/tcp", false},
		// And the other way round.
		{"a wildcard claim covers an address", []string{"%[1]d:80/tcp", "127.0.0.1:%[1]d:%[1]d/tcp"}, "127.0.0.1:%[1]d:%[1]d/tcp", false},
		// A different host port is a different claim.
		{"another host port", []string{"%[2]d:80/tcp", "%[1]d:%[1]d/tcp"}, "%[1]d:%[1]d/tcp", true},
		// No protocol written is tcp, on both sides of the question.
		{"a claim with no protocol covers a tcp mirror", []string{"%[1]d:80", "%[1]d:%[1]d/tcp"}, "%[1]d:%[1]d/tcp", false},
		{"a udp claim leaves a mirror with no protocol alone", []string{"%[1]d:80/udp", "%[1]d:%[1]d"}, "%[1]d:%[1]d", true},
		// The side being moved is asked about its own protocol too. Every row
		// above mirrors a tcp entry (or one with no protocol, which is tcp),
		// so a check that read the mirror as tcp whatever it says would pass
		// them all; and every udp claim above answers "leave it alone", so
		// one that threw udp claims away would pass them all as well. These
		// two rows are the other value of each.
		{"a tcp claim leaves a udp mirror alone", []string{"%[1]d:80/tcp", "%[1]d:%[1]d/udp"}, "%[1]d:%[1]d/udp", true},
		{"a udp claim covers a udp mirror", []string{"%[1]d:80/udp", "%[1]d:%[1]d/udp"}, "%[1]d:%[1]d/udp", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other := freePort(t)
			specs := make([]string, len(tc.specs))
			for i, f := range tc.specs {
				specs[i] = fmt.Sprintf(f, port, other)
			}
			auto := fmt.Sprintf(tc.auto, port, other)
			rt, log := fakeShim(t)
			p := project("demo", map[string]*compose.Service{
				"web": {Image: "web:latest", Ports: specs, AutoHostPort: map[string]bool{auto: true}},
			})
			var out bytes.Buffer
			if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
				t.Fatalf("up: %v", err)
			}
			calls := strings.Join(log(), "\n")
			kept := strings.Contains(calls, "-p "+auto)
			if kept != tc.wantKept {
				t.Errorf("the entry opossum may move %s; wanted it %s.\ncalls:\n%s",
					map[bool]string{true: "kept its port", false: "was moved"}[kept],
					map[bool]string{true: "kept", false: "moved"}[tc.wantKept], calls)
			}
			// The notice and the move say the same thing, so a run that
			// moved the port without saying so — or said so without moving
			// it — fails here too.
			if said := strings.Contains(out.String(), "[OPSM-206]"); said == tc.wantKept {
				t.Errorf("the notice %s while the port %s:\n%s",
					map[bool]string{true: "was printed", false: "was not printed"}[said],
					map[bool]string{true: "was kept", false: "was moved"}[kept], out.String())
			}
		})
	}
}
