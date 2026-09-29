package orchestrator

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// Evals for the spelling the port notices ask the reader to write.
//
// `OPSM-206` (a port was moved) and `OPSM-212` (it could not be moved) end with
// "to pin one, write it in the compose file as ...". That spelling is copied
// into a file, so the test that matters is not what the sentence says but what
// the file it produces publishes: the same address, the same protocol and the
// same container port as the entry the notice is about, on the host port the
// reader chose. The spelling used to keep the container port and nothing else,
// which turned a udp entry into a tcp one and a loopback entry into one on every
// interface — and `up` succeeded either way.
//
// Compared as what is published, not as text: a bare tcp entry and one spelled
// `/tcp` are the same entry, and a comparison of spellings would call the
// control different.

// pinnedForms are the entries a notice can be about, as the loader hands them to
// the walk (the mirrored host port already written in). %[1]d is that port.
var pinnedForms = []struct{ name, spec string }{
	{"a bare tcp entry", "%[1]d:%[1]d"},
	{"a udp entry", "%[1]d:%[1]d/udp"},
	{"a loopback address", "127.0.0.1:%[1]d:%[1]d"},
	{"the IPv6 loopback address", "[::1]:%[1]d:%[1]d"},
	{"a loopback address on udp", "127.0.0.1:%[1]d:%[1]d/udp"},
}

// published says what an entry publishes apart from its host port: the protocol,
// the address it names as the loader writes it ("" for a bare entry, and
// `0.0.0.0` or `[::]` where the file spelled a wildcard out), and the container
// port.
func published(spec string) string {
	network, _, _, _ := hostSide(spec)
	return fmt.Sprintf("%s %q %d", network, hostTextOf(spec), specContainerPort(spec))
}

// writeBack is what a reader does with the notice: put the spelling in a compose
// file with a host port of their choosing, and have opossum read it.
func writeBack(t *testing.T, spelling string, host int) string {
	t.Helper()
	if !strings.Contains(spelling, "<host>") {
		t.Fatalf("the spelling %q has no <host> for the reader to fill in", spelling)
	}
	entry := strings.Replace(spelling, "<host>", fmt.Sprint(host), 1)
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	body := fmt.Sprintf("services:\n  a:\n    image: web:latest\n    ports:\n      - %q\n", entry)
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := compose.Load(file)
	if err != nil {
		t.Fatalf("a file written from the notice's spelling %q does not load: %v", spelling, err)
	}
	ports := p.Services["a"].Ports
	if len(ports) != 1 {
		t.Fatalf("the spelling %q came back as %d entries, want 1: %v", spelling, len(ports), ports)
	}
	return ports[0]
}

// The spelling in a notice is quoted after the words that introduce it.
var pinnedSpelling = regexp.MustCompile(`as "([^"]*<host>[^"]*)"`)

func spellingIn(t *testing.T, said string) string {
	t.Helper()
	m := pinnedSpelling.FindStringSubmatch(said)
	if m == nil {
		t.Fatalf("the notice carries no spelling to write:\n%s", said)
	}
	return m[1]
}

// The function on its own, against the spellings a reader is owed. One row per
// part the spelling has to keep, and the control that has nothing to keep.
func TestPinSpellingKeepsWhatTheEntryPublishes(t *testing.T) {
	for _, tc := range []struct{ spec, want string }{
		{"3000:3000", "<host>:3000"},
		{"3000:3000/udp", "<host>:3000/udp"},
		{"127.0.0.1:3000:3000", "127.0.0.1:<host>:3000"},
		{"[::1]:3000:3000", "[::1]:<host>:3000"},
		{"127.0.0.1:3000:3000/udp", "127.0.0.1:<host>:3000/udp"},
		// A host port that differs from the container port: the container side is
		// what the spelling keeps, and the host side is what the reader replaces.
		{"51864:3000", "<host>:3000"},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			if got := pinSpelling(tc.spec); got != tc.want {
				t.Errorf("pinSpelling(%q) = %q, want %q", tc.spec, got, tc.want)
			}
		})
	}
}

// Each of the three places that word a notice, and each of the entries a notice
// can be about. The row names the path by its own words before it looks at the
// spelling: two of the paths end on the same number, so a spelling alone would
// not say which one it read.
func TestTheSpellingAReaderIsToldToWriteKeepsTheEntry(t *testing.T) {
	type path struct {
		name  string
		words string // what only this path says
		run   func(t *testing.T, spec string) (said string)
		// racy is true for a path whose run does a real bind check (rather
		// than stubbing probeHostPort) on the port drawn below. freePortsForSticky
		// only verifies the number free on tcp/127.0.0.1; a form that binds a
		// different protocol or address (udp, a wildcard, [::1]) is checked
		// there for real instead, and something outside this test entirely —
		// not another goroutine of this run, and not a race — can already
		// hold that number on that other (protocol, address) pair (#1376: CI
		// run 36290611668 failed this way on a udp entry at port 41641, which
		// tailscaled listens on by default on the runner; confirmed
		// deterministically with TestAMirrorOnADifferentProtocolIsThe1376Failure,
		// not assumed from the traceback). Retrying draws a different number,
		// which this mismatch does not reliably collide with twice.
		racy bool
	}
	paths := []path{
		{"the mirrored port was taken and the entry moved", "picks a free port here too", movedNotice, false},
		{"nothing could be found to move it to", "could not place it", unplacedNotice, false},
		{"a port the service was on was given up", "was published on host port", gaveUpNotice, true},
	}
	for _, pa := range paths {
		for _, form := range pinnedForms {
			t.Run(pa.name+"/"+form.name, func(t *testing.T) {
				attempts := 1
				if pa.racy {
					attempts = 3
				}
				var spec, said string
				for attempt := 1; attempt <= attempts; attempt++ {
					port := freePortsForSticky(t, 1)[0]
					spec = fmt.Sprintf(form.spec, port)
					said = pa.run(t, spec)
					if strings.Contains(said, pa.words) {
						break
					}
					if attempt < attempts {
						t.Logf("attempt %d: this row's port may already be held on this form's own protocol/address by something %s's real bind check reaches and freePortsForSticky's tcp/127.0.0.1 check does not; retrying with a fresh one. The reader was told:\n%s", attempt, pa.name, said)
					}
				}
				if !strings.Contains(said, pa.words) {
					t.Fatalf("this row was meant to reach the notice that says %q, and the reader was told:\n%s", pa.words, said)
				}
				got := writeBack(t, spellingIn(t, said), 8123)
				if published(got) != published(spec) {
					t.Errorf("the entry %q was told to be written as %q, which loads as %q and publishes %s — the entry "+
						"published %s.\n%s", spec, spellingIn(t, said), got, published(got), published(spec), said)
				}
			})
		}
	}
}

// decoy is an entry of the same service that is not the one a notice is about,
// and differs from every form in pinnedForms in its protocol, its address and its
// container port. It stands in front of the entry under test: a notice that read
// the wrong entry of the service — the first, say — would then spell the decoy,
// where with one entry alone the first and the only are the same thing.
const decoy = "[::1]:59999:59998/udp"

// movedNotice: the mirrored port is in use, so the entry is moved to a free one
// on its own address.
func movedNotice(t *testing.T, spec string) string {
	t.Helper()
	network, address, _, _ := hostPortBinding(spec)
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{decoy, spec}, AutoHostPort: map[string]bool{spec: true}},
	}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	probe := &bindAnswers{by: map[string]error{}}
	for _, n := range probeNetworks(network, address) {
		probe.by[n+" "+address] = refusedBind(t, n, address, syscall.EADDRINUSE)
	}
	o.probeHostPort = probe.probe
	o.remapAutoHostPorts([]string{"a"})
	if p.Services["a"].Ports[1] == spec {
		t.Fatalf("%q was not moved though its mirror is in use:\n%s", spec, out.String())
	}
	return out.String()
}

// unplacedNotice: the mirrored port is in use and every other port the search
// turns up is one the file has spoken for or the system refuses.
func unplacedNotice(t *testing.T, spec string) string {
	t.Helper()
	network, address, _, _ := hostPortBinding(spec)
	// z is a line of the file that fixes the port the system hands out first, on
	// the same address and protocol, so that answer is one the file has spoken for.
	z, _ := withHostPort(spec, 40000)
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{decoy, spec}, AutoHostPort: map[string]bool{spec: true}},
		"z": {Image: "web:latest", Ports: []string{z}},
	}}
	var out bytes.Buffer
	o := New(p, quietShim(t), "opossum", &out)
	probe := &bindAnswers{by: map[string]error{}}
	for _, n := range probeNetworks(network, address) {
		probe.by[n+" "+address] = refusedBind(t, n, address, syscall.EADDRINUSE)
	}
	o.probeHostPort = probe.probe
	o.holdPort = func(network, address string, port int) (int, io.Closer, error) {
		if port == 0 {
			return 40000, &tracked{}, nil
		}
		return 0, nil, errors.New("busy")
	}
	o.remapAutoHostPorts([]string{"a", "z"})
	return out.String()
}

// gaveUpNotice: the service's container is on a port the file now fixes for
// another service, so it gives that up and lands on its own mirror, which is
// free — real, not stubbed (unlike movedNotice/unplacedNotice below): this
// row's whole point is that a's own bind check finds spec's mirror free.
// TestTheSpellingAReaderIsToldToWriteKeepsTheEntry retries this path (#1376)
// rather than stubbing it here too, because that real check is on spec's own
// protocol and address, which freePortsForSticky's draw does not verify.
func gaveUpNotice(t *testing.T, spec string) string {
	t.Helper()
	proto := "tcp"
	if strings.HasSuffix(spec, "/udp") {
		proto = "udp"
	}
	ports := freePortsForSticky(t, 1)
	held := ports[0]
	container := specContainerPort(spec)
	z, _ := withHostPort(spec, held)
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{decoy, spec}, AutoHostPort: map[string]bool{spec: true}},
		"z": {Image: "web:latest", Ports: []string{z}},
	}}
	var out bytes.Buffer
	o := New(p, shimHolding(t, "a", held, container, proto), "opossum", &out)
	o.remapAutoHostPorts([]string{"a", "z"})
	return out.String()
}

// TestAMirrorOnADifferentProtocolIsThe1376Failure confirms, deterministically,
// what the retry in TestTheSpellingAReaderIsToldToWriteKeepsTheEntry is a
// mitigation for: a's own real bind check on its mirror (askHostPort, inside
// remapAutoHostPorts) finding that (protocol, address) pair held by something
// outside this test entirely produces the "moved" notice's wording instead of
// "was published on host port", in the exact words CI showed (#1376, run
// 36290611668). That run's row was a udp entry at port 41641 — tailscaled's
// default UDP port on the runner (a self-hosted Linux box) — while
// freePortsForSticky had verified only that port free on tcp/127.0.0.1: two
// different sockets, not a race between two listeners of the same run. held
// (z's port, and the one a's container is shimmed to already be on) is
// untouched; only the mirror's own bind is forced busy, with probeHostPort
// stubbed (as bindanswer_internal_test.go's other rows already do) rather
// than holding a real socket, which needs no coordination with anything
// external to reproduce reliably.
func TestAMirrorOnADifferentProtocolIsThe1376Failure(t *testing.T) {
	const held = 39620   // z's port, and where a's container already is
	const mirror = 39621 // a's own entry: the number its own bind check finds held
	spec := fmt.Sprintf("%d:%d", mirror, mirror)
	z, _ := withHostPort(spec, held)
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{decoy, spec}, AutoHostPort: map[string]bool{spec: true}},
		"z": {Image: "web:latest", Ports: []string{z}},
	}}
	var out bytes.Buffer
	o := New(p, shimHolding(t, "a", held, mirror, "tcp"), "opossum", &out)
	probe := &bindAnswers{by: map[string]error{}}
	probe.by[fmt.Sprintf("tcp4 :%d", mirror)] = refusedBind(t, "tcp4", fmt.Sprintf(":%d", mirror), syscall.EADDRINUSE)
	o.probeHostPort = probe.probe
	o.remapAutoHostPorts([]string{"a", "z"})
	said := out.String()
	for _, want := range []string{"picks a free port here too", fmt.Sprintf("host port %d is in use", mirror)} {
		if !strings.Contains(said, want) {
			t.Fatalf("got %q\nwant it to hold %q — the exact CI wording (#1376)", said, want)
		}
	}
}
