package orchestrator

import (
	"fmt"
	"net"
	goruntime "runtime"
	"testing"
)

// The helpers of the internal tests that draw free ports check them where `up` checks them: on loopback and on
// the IPv4 wildcard, for the number and the ones a row builds beside it (#1758). The check is read here on its own;
// which port a helper draws cannot be made to land on one a machine's outgoing connection holds.
func TestPortsAreCheckedFreeOnLoopbackAndOnTheWildcardAddress(t *testing.T) {
	base := freeRunOf(t, 3)
	if !portsFreeWhereUpAsks(base, base+2) {
		t.Fatalf("freeRunOf gave a run from %d that is not free where it is checked", base)
	}
	for name, hold := range map[string]struct{ network, address string }{
		"held on loopback":          {"tcp", "127.0.0.1:%d"},
		"held on the wildcard only": {"tcp4", ":%d"},
	} {
		for pos, offset := range map[string]int{"the first": 0, "the middle": 1, "the last": 2} {
			t.Run(name+"/"+pos, func(t *testing.T) {
				base := freeRunOf(t, 3)
				l, err := net.Listen(hold.network, fmt.Sprintf(hold.address, base+offset))
				if err != nil {
					t.Fatalf("could not hold %d: %v", base+offset, err)
				}
				defer l.Close()
				if portsFreeWhereUpAsks(base, base+2) {
					t.Errorf("the run %d-%d has a port held (%s, %s) and was found free", base, base+2, name, pos)
				}
			})
		}
	}
}

// The helper that draws ports for the sticky tests never gives the same number twice (each is held until the last
// is chosen). That the numbers, and the ones a row builds beside them, are free where `up` asks is the check read
// above, called from the helper; which port a draw lands on cannot be made to be one a machine's outgoing connection
// holds, so the call itself is not guarded by a row.
func TestFreePortsForStickyAreNeverTheSameNumber(t *testing.T) {
	ports := freePortsForSticky(t, 12)
	seen := map[int]bool{}
	for _, p := range ports {
		if seen[p] {
			t.Errorf("the number %d was given twice", p)
		}
		seen[p] = true
	}
}

// A port an outgoing connection uses as its source port can be listened on at 127.0.0.1 and cannot be bound at the
// wildcard (the connection's socket was made without SO_REUSEADDR, and `0.0.0.0` overlaps its address): the case
// that failed the tests of host ports with OPSM-201 (#1753), and the one the wildcard half of the check is for. On Linux
// a wildcard that is *held* is seen from the loopback half as well, so the rows of the test above do not need the
// wildcard half there; this one does. Linux only (127.0.0.2 is a loopback address there alone), as the same row of #1719.
func TestAPortAnOutgoingConnectionHoldsIsFoundNotFreeAtTheWildcardAddress(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("127.0.0.2 is a loopback address on Linux only")
	}
	server, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go func() {
		for {
			c, err := server.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	for pos, offset := range map[string]int{"the first": 0, "the middle": 1, "the last": 2} {
		t.Run(pos, func(t *testing.T) {
			base := freeRunOf(t, 3)
			held := base + offset
			conn, err := (&net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP("127.0.0.2"), Port: held}}).Dial("tcp", server.Addr().String())
			if err != nil {
				t.Skipf("cannot make a connection from 127.0.0.2:%d: %v", held, err)
			}
			defer conn.Close()
			if l, err := net.Listen("tcp4", fmt.Sprintf(":%d", held)); err == nil {
				l.Close()
				t.Skipf("the wildcard binds %d though a connection uses it: this kernel does not make the difference", held)
			}
			if l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", held)); err != nil {
				t.Skipf("loopback does not bind %d either (%v): the row is about a port only the wildcard refuses", held, err)
			} else {
				l.Close()
			}
			if portsFreeWhereUpAsks(base, base+2) {
				t.Errorf("the run %d-%d has %d held by an outgoing connection (loopback binds it, the wildcard does not) and was found free", base, base+2, held)
			}
		})
	}
}
