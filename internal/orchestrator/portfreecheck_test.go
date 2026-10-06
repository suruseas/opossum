package orchestrator_test

import (
	"fmt"
	"net"
	"testing"
)

// The helpers that draw a free port for a test check it where `up` checks it: on loopback, and on the wildcard
// address. A port that is free on loopback only — one a connection the machine made out from it holds, as on Linux
// the even ports are — failed the tests that used it with OPSM-201, from the pre-flight that binds the wildcard
// (#1753; 2 of the 2 CI failures of the tests of host ports in a row were this, not a port taken in between).
func TestAPortIsDrawnFreeOnLoopbackAndOnTheWildcardAddress(t *testing.T) {
	free := freePort(t)
	if !portFreeWhereItIsChecked(free) {
		t.Fatalf("freePort gave %d, which is not free where it is checked", free)
	}
	for name, hold := range map[string]struct{ network, address string }{
		"held on loopback":          {"tcp", "127.0.0.1:%d"},
		"held on the wildcard only": {"tcp4", ":%d"},
	} {
		t.Run(name, func(t *testing.T) {
			p := freePort(t)
			l, err := net.Listen(hold.network, fmt.Sprintf(hold.address, p))
			if err != nil {
				t.Fatalf("could not hold %d: %v", p, err)
			}
			defer l.Close()
			if portFreeWhereItIsChecked(p) {
				t.Errorf("port %d is held (%s) and was found free", p, name)
			}
		})
	}
}
