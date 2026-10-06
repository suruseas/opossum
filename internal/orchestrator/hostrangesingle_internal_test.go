package orchestrator

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

// An entry whose host side is a range and whose container side is one port (`"7670-7671:80"`) is published on
// one host port of the range (#1254). docker compose starts it, on the first free port of the range (measured, v5.5.1:
// three containers on `7690-7692:80` got 7690, 7691, 7692, and a fourth was refused); the runtime refuses the
// spelling ("publish host and container port counts are not equal"), so opossum used to pass it on and fail the `up`.
// Each row is one thing the choice rests on.
func TestAHostRangeWithOneContainerPortIsPublishedOnOnePortOfTheRange(t *testing.T) {
	base := freeRunOf(t, 4)
	span := func(width int, rest string) string { return fmt.Sprintf("%d-%d:%s", base, base+width-1, rest) }
	one := func(offset int, rest string) string { return fmt.Sprintf("%d:%s", base+offset, rest) }
	type svcs map[string][]string
	for _, tc := range []struct {
		name      string
		services  svcs
		inUse     []int  // offsets from base the bind refuses
		holds     []int  // offsets from base the running container of a publishes on container port 80
		holdProto string // its protocol ("" is tcp)
		want      map[string][]string
	}{
		{"the first port of the range, when it is free", svcs{"a": {span(2, "80")}}, nil, nil, "",
			map[string][]string{"a": {one(0, "80")}}},
		{"the next one, when the first is in use", svcs{"a": {span(3, "80")}}, []int{0}, nil, "",
			map[string][]string{"a": {one(1, "80")}}},
		{"the one after, when two are in use", svcs{"a": {span(3, "80")}}, []int{0, 1}, nil, "",
			map[string][]string{"a": {one(2, "80")}}},
		{"the first port, when the whole range is in use (the pre-flight says so)", svcs{"a": {span(2, "80")}}, []int{0, 1}, nil, "",
			map[string][]string{"a": {one(0, "80")}}},
		{"a port another entry of the file writes is not taken", svcs{"a": {span(2, "80")}, "b": {one(0, "9000")}}, nil, nil, "",
			map[string][]string{"a": {one(1, "80")}, "b": {one(0, "9000")}}},
		{"a port a range of another entry of the file covers is not taken", svcs{"a": {span(3, "80")}, "b": {span(2, "9000-9001")}}, nil, nil, "",
			map[string][]string{"a": {one(2, "80")}, "b": {span(2, "9000-9001")}}},
		{"a range of another entry that covers some of the ports is not counted before it is settled", svcs{"a": {span(3, "80")}, "b": {span(2, "81")}}, nil, nil, "",
			map[string][]string{"a": {one(0, "80")}, "b": {one(1, "81")}}},
		{"a port another entry writes on the other protocol is taken", svcs{"a": {span(2, "80")}, "b": {one(0, "9000/udp")}}, nil, nil, "",
			map[string][]string{"a": {one(0, "80")}, "b": {one(0, "9000/udp")}}},
		{"two entries on one range take different ports", svcs{"a": {span(2, "80")}, "b": {span(2, "81")}}, nil, nil, "",
			map[string][]string{"a": {one(0, "80")}, "b": {one(1, "81")}}},
		{"the port the running container publishes is kept, though another is free", svcs{"a": {span(3, "80")}}, []int{1}, []int{1}, "",
			map[string][]string{"a": {one(1, "80")}}},
		{"a port the running container publishes outside the range is not taken", svcs{"a": {span(2, "80")}}, nil, []int{3}, "",
			map[string][]string{"a": {one(0, "80")}}},
		{"a port the running container publishes that another entry of the file writes is not kept", svcs{"a": {span(2, "80")}, "b": {one(1, "9000")}}, nil, []int{1}, "",
			map[string][]string{"a": {one(0, "80")}, "b": {one(1, "9000")}}},
		{"a port the running container publishes on the other protocol is not kept", svcs{"a": {fmt.Sprintf("%d-%d:80/udp", base, base+1)}}, nil, []int{1}, "tcp",
			map[string][]string{"a": {fmt.Sprintf("%d:80/udp", base)}}},
		{"the port the running container publishes at the first end of the range is kept (the bind refuses it: it is its own)", svcs{"a": {span(3, "80")}}, []int{0}, []int{0}, "",
			map[string][]string{"a": {one(0, "80")}}},
		{"the port the running container publishes at the last end of the range is kept", svcs{"a": {span(3, "80")}}, []int{2}, []int{2}, "",
			map[string][]string{"a": {one(2, "80")}}},
		{"the port in the range is kept when the container publishes the container port elsewhere too (one first)", svcs{"a": {span(2, "80")}}, []int{0}, []int{0, 50}, "",
			map[string][]string{"a": {one(0, "80")}}},
		{"the port in the range is kept when the container publishes the container port elsewhere too (one last)", svcs{"a": {span(2, "80")}}, []int{0}, []int{50, 0}, "",
			map[string][]string{"a": {one(0, "80")}}},
		{"the address and the protocol are kept", svcs{"a": {fmt.Sprintf("127.0.0.1:%d-%d:80/udp", base, base+1)}}, nil, nil, "",
			map[string][]string{"a": {fmt.Sprintf("127.0.0.1:%d:80/udp", base)}}},
		{"a range on both sides is left as it is", svcs{"a": {span(2, "80-81")}}, nil, nil, "",
			map[string][]string{"a": {span(2, "80-81")}}},
		{"a range of one port is left as it is", svcs{"a": {span(1, "80")}}, nil, nil, "",
			map[string][]string{"a": {span(1, "80")}}},
		{"a single port is left as it is", svcs{"a": {one(0, "80")}}, []int{0}, nil, "",
			map[string][]string{"a": {one(0, "80")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{}}
			var order []string
			for _, name := range []string{"a", "b"} {
				ports, ok := tc.services[name]
				if !ok {
					continue
				}
				p.Services[name] = &compose.Service{Image: "web:latest", Ports: append([]string(nil), ports...)}
				order = append(order, name)
			}
			shim := quietShim(t)
			if len(tc.holds) > 0 {
				proto := tc.holdProto
				if proto == "" {
					proto = "tcp"
				}
				var pms []runtime.PortMapping
				for _, off := range tc.holds {
					pms = append(pms, runtime.PortMapping{ContainerPort: 80, Count: 1, HostPort: base + off, Proto: proto})
				}
				shim = shimHoldingThese(t, "a", pms...)
			}
			var out bytes.Buffer
			o := New(p, shim, "opossum", &out)
			probe := &bindAnswers{by: map[string]error{}}
			for _, off := range tc.inUse {
				addr := ":" + strconv.Itoa(base+off)
				for _, n := range probeNetworks("tcp", addr) {
					probe.by[n+" "+addr] = refusedBind(t, n, addr, syscall.EADDRINUSE)
				}
			}
			o.probeHostPort = probe.probe
			o.settleHostPortRanges(order)
			for name, want := range tc.want {
				got := p.Services[name].Ports
				if fmt.Sprint(got) != fmt.Sprint(want) {
					t.Errorf("service %s: ports are %v, want %v\n%s", name, got, want, out.String())
				}
			}
			if out.Len() != 0 {
				t.Errorf("the entry is the file's own range, and nothing was said of it moving:\n%s", out.String())
			}
		})
	}
}

// The question put to the system is the one `up` puts: the address the entry writes, and the protocol it names; and
// a container that is not running holds nothing, so its port is asked about like any other.
func TestTheHostPortOfARangeIsAskedOnTheAddressAndProtocolOfTheEntry(t *testing.T) {
	base := freeRunOf(t, 2)
	refuse := func(t *testing.T, probe *bindAnswers, network, host string, n int) {
		addr := net.JoinHostPort(host, strconv.Itoa(n))
		for _, nw := range probeNetworks(network, addr) {
			probe.by[nw+" "+addr] = refusedBind(t, nw, addr, syscall.EADDRINUSE)
		}
	}
	for _, tc := range []struct {
		name, spec, want string
		refused          func(t *testing.T, probe *bindAnswers)
		stopped          bool
	}{
		{"an address that is free where the wildcard is not", fmt.Sprintf("127.0.0.1:%d-%d:80", base, base+1), fmt.Sprintf("127.0.0.1:%d:80", base),
			func(t *testing.T, probe *bindAnswers) { refuse(t, probe, "tcp", "", base) }, false},
		{"udp, with the port in use on udp only", fmt.Sprintf("%d-%d:80/udp", base, base+1), fmt.Sprintf("%d:80/udp", base+1),
			func(t *testing.T, probe *bindAnswers) { refuse(t, probe, "udp", "", base) }, false},
		{"a container that is not running holds nothing", fmt.Sprintf("%d-%d:80", base, base+1), fmt.Sprintf("%d:80", base+1),
			func(t *testing.T, probe *bindAnswers) { refuse(t, probe, "tcp", "", base) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"a": {Image: "web:latest", Ports: []string{tc.spec}},
			}}
			shim := quietShim(t)
			if tc.stopped {
				shim = shimHoldingThese(t, "a", runtime.PortMapping{ContainerPort: 80, Count: 1, HostPort: base, Proto: "tcp"})
				body, err := os.ReadFile(shim.Bin)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(body), `"state":"running"`) {
					t.Fatal("the shim does not say running, so there is nothing to turn into stopped")
				}
				if err := os.WriteFile(shim.Bin, []byte(strings.Replace(string(body), `"state":"running"`, `"state":"stopped"`, 1)), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			o := New(p, shim, "opossum", &bytes.Buffer{})
			probe := &bindAnswers{by: map[string]error{}}
			tc.refused(t, probe)
			o.probeHostPort = probe.probe
			o.settleHostPortRanges([]string{"a"})
			if got := p.Services["a"].Ports[0]; got != tc.want {
				t.Errorf("the entry is %q, want %q", got, tc.want)
			}
		})
	}
}
