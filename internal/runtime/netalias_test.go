package runtime

import (
	"strings"
	"testing"
)

// Each alias rides on its own network as `,alias=`, after the MAC, keyed by the
// network's name; a network with none stays as it was.
func TestRunPutsEachAliasOnItsNetwork(t *testing.T) {
	rt, read := loggingShim(t)
	if err := rt.Run(RunOptions{Name: "w", Image: "a", Networks: []string{"demo-net", "back"}, MacAddress: "02:42:ac:11:00:77",
		NetworkAliases: map[string][]string{"back": {"cache.demo.opossum", "kv.demo.opossum"}}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := lastLine(t, read); !strings.Contains(got, "--network=demo-net,mac=02:42:ac:11:00:77 --network=back,alias=cache.demo.opossum,alias=kv.demo.opossum ") {
		t.Errorf("want the aliases on back alone, got: %s", got)
	}
	if err := rt.Run(RunOptions{Name: "w", Image: "a", Networks: []string{"demo-net"}, MacAddress: "02:42:ac:11:00:77",
		NetworkAliases: map[string][]string{"demo-net": {"cache.demo.opossum"}}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := lastLine(t, read); !strings.Contains(got, "--network=demo-net,mac=02:42:ac:11:00:77,alias=cache.demo.opossum ") {
		t.Errorf("want the alias after the MAC on the first network, got: %s", got)
	}
}

// 1.6.0 added `--network <name>,alias=`; a version that cannot be read is one
// that does not take it.
func TestAcceptsNetworkAliases(t *testing.T) {
	for v, want := range map[string]bool{
		"1.6.0": true, "1.6.1": true, "1.10.0": true, "2.0.0": true, "1.6": true,
		"1.5.0": false, "1.4.1": false, "0.9.0": false, "": false, "1": false,
		"x.6.0": false, "1.x.0": false, "2.x.0": false, "unspecified": false,
	} {
		if got := AcceptsNetworkAliases(v); got != want {
			t.Errorf("AcceptsNetworkAliases(%q) = %v, want %v", v, got, want)
		}
	}
}
