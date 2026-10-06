package orchestrator_test

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// `up` hands the runtime one host port for an entry whose host side is a range and whose container side is one
// port (#1254). The choice itself is in the rows of TestAHostRangeWithOneContainerPortIsPublishedOnOnePortOfTheRange;
// this is that `up` makes it, and that what reaches the runtime is the single spelling the runtime accepts and
// not the range it refuses ("publish host and container port counts are not equal").
func TestUpPublishesAHostRangeWithOneContainerPortOnOnePort(t *testing.T) {
	base := twoFreePorts(t)
	for _, tc := range []struct{ name, spec, want string }{
		{"tcp", fmt.Sprintf("%d-%d:80", base, base+1), fmt.Sprintf("%d:80", base)},
		{"an address and udp", fmt.Sprintf("127.0.0.1:%d-%d:80/udp", base, base+1), fmt.Sprintf("127.0.0.1:%d:80/udp", base)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			svc := &compose.Service{Image: "web:latest", Ports: []string{tc.spec}, AutoHostPort: map[string]bool{}}
			var out bytes.Buffer
			if err := orchestrator.New(project("demo", map[string]*compose.Service{"a": svc}), rt, "opossum", &out).Up(true); err != nil {
				t.Fatalf("up: %v", err)
			}
			var published []string
			for _, c := range log() {
				published = append(published, publishedSpecs(c)...)
			}
			if got := strings.Join(published, " "); got != tc.want {
				t.Errorf("the runtime was given %q, want %q (the file wrote %q)", got, tc.want, tc.spec)
			}
		})
	}
}

// The range is settled before a bare entry is decided, so a bare entry whose mirror is a port of the range keeps it and
// the range takes the next (#1254): settled after, the bare entry would be moved off the range as if the range
// were fixed, with a notice about a port the file wrote.
func TestUpSettlesAHostRangeBeforeItDecidesABareEntry(t *testing.T) {
	base := twoFreePorts(t)
	bare := fmt.Sprintf("%[1]d:%[1]d", base)
	rt, log := fakeShim(t)
	services := map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d-%d:80", base, base+1)}, AutoHostPort: map[string]bool{}},
		"b": {Image: "web:latest", Ports: []string{bare}, AutoHostPort: map[string]bool{bare: true}},
	}
	var out bytes.Buffer
	if err := orchestrator.New(project("demo", services), rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("up: %v", err)
	}
	var published []string
	for _, c := range log() {
		published = append(published, publishedSpecs(c)...)
	}
	got := strings.Join(published, " ")
	if !strings.Contains(got, fmt.Sprintf("%d:80", base+1)) || !strings.Contains(got, bare) {
		t.Errorf("published %q, want the range on %d and the bare entry on %s", got, base+1, bare)
	}
	if strings.Contains(out.String(), "instead") {
		t.Errorf("something was said of a port moving:\n%s", out.String())
	}
}

// twoFreePorts is a port and the one after it, both free to bind on every address. It scans for them from a point of its own, and
// does not take them from the system's choice of an ephemeral port: macOS hands those out in sequence, and a run that starts in the
// last few hundred numbers has no two consecutive ones below the cutoff a test wants (this failed every run for as long as the
// sequence stood there).
func twoFreePorts(t *testing.T) int {
	t.Helper()
	start := 30000 + (os.Getpid()*37)%20000
	for n := 0; n < 4000; n++ {
		base := start + n
		l, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", base))
		if err != nil {
			continue
		}
		l1, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", base+1))
		l.Close()
		if err != nil {
			continue
		}
		l1.Close()
		return base
	}
	t.Fatal("no two consecutive free ports found")
	return 0
}
