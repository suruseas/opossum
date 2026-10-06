package orchestrator

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// A host port found in use is not where the entry is moved to, whoever hands the number out (#1753). The walk
// for a free port starts by asking the system for one, and a system that gives back the number just found in
// use — a port that was only in use as far as the answer went, as the stand-in for one in these tests is,
// or one freed in the moment between — placed the entry on the very port it was being moved off, with a notice
// that said "publishes it on N instead" for the N it was on. Linux hands out port 0 at random, so a number freed
// a moment ago comes back about once in 8,000 asks (measured, 200,000 asks, on tcp and udp, on loopback and the
// wildcard address alike, with and without load); macOS hands them out in order and never does. The rows of
// the tests of the notices (five forms each) failed 7 times in 60,000 at six at once, on the unchanged code.
// The entry is a bare one (host and container port the same) or one with another container port, which the
// host port of the entry is what is excluded for.
func TestAPortFoundInUseIsNotWhereTheEntryIsMovedWhenTheSystemHandsItOutAgain(t *testing.T) {
	for name, specOf := range map[string]func(port int) string{
		"a bare entry":                         func(port int) string { return fmt.Sprintf("%[1]d:%[1]d", port) },
		"an entry with another container port": func(port int) string { return fmt.Sprintf("%d:3000", port) },
	} {
		t.Run(name, func(t *testing.T) {
			base := freeRunOf(t, 6)
			mirrored := base + 1
			mirror := specOf(mirrored)
			network, address, _, _ := hostPortBinding(mirror)
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: []string{mirror}, AutoHostPort: map[string]bool{mirror: true}},
			}}
			var out bytes.Buffer
			o := New(p, quietShim(t), "opossum", &out)
			probe := &bindAnswers{by: map[string]error{}}
			for _, n := range probeNetworks(network, address) {
				probe.by[n+" "+address] = refusedBind(t, n, address, syscall.EADDRINUSE)
			}
			o.probeHostPort = probe.probe
			alloc := &answers{any: mirrored} // the system answers with the number that was found in use
			o.holdPort = alloc.hold
			o.remapAutoHostPorts([]string{"a"})
			alloc.mustBeClosed(t)
			got := p.Services["a"].Ports[0]
			if got == mirror {
				t.Fatalf("the entry %q was left on the port it was moved off:\n%s", mirror, out.String())
			}
			host, _, _ := strings.Cut(got, ":")
			if n, err := strconv.Atoi(host); err != nil || n == mirrored {
				t.Errorf("the entry is %q, want a host port other than %d", got, mirrored)
			}
			if said := out.String(); !strings.Contains(said, fmt.Sprintf("publishes it on %s instead", host)) {
				t.Errorf("the notice does not name the port the entry went to (%s):\n%s", host, said)
			}
		})
	}
}
