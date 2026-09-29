package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// The docker oracle's web service: `3000` (host side chosen at run time),
// `8080:80` and `9090:90/udp`, plus one bound to a specific address so the
// address is seen to be read rather than assumed. Host and container ports
// differ in every entry, so a line that prints the wrong side cannot pass.
const portInspect = `[{"status":{"state":"running","networks":[{"network":"demo-net","ipv4Address":"192.168.64.10/24"}]},"configuration":{"labels":{"opossum.project":"demo"},"publishedPorts":[` +
	`{"containerPort":3000,"hostAddress":"0.0.0.0","hostPort":65345,"proto":"tcp"},` +
	`{"containerPort":80,"hostAddress":"0.0.0.0","hostPort":8080,"proto":"tcp"},` +
	`{"containerPort":90,"hostAddress":"0.0.0.0","hostPort":9090,"proto":"udp"},` +
	`{"containerPort":443,"hostAddress":"127.0.0.1","hostPort":8443,"proto":"tcp"}]}}]`

func portProject() *compose.Project {
	return project("demo", map[string]*compose.Service{
		"web": {Image: "web:latest"},
	})
}

// #1272 E: a published range arrives from `inspect` as one entry with a
// count, not as one entry per port (measured on container 1.4.1: a
// 47210-47212:80-82 range reports one publishedPorts entry, containerPort
// 80, count 3 — and all three of 47210/47211/47212 are really bound). `port`
// used to read only that first entry, so asking for 81 or 82 — bound and
// real — was refused as though nothing there was published at all.
const rangeInspect = `[{"status":{"state":"running","networks":[{"network":"demo-net","ipv4Address":"192.168.64.10/24"}]},"configuration":{"labels":{"opossum.project":"demo"},"publishedPorts":[` +
	`{"containerPort":80,"count":3,"hostAddress":"0.0.0.0","hostPort":47210,"proto":"tcp"}]}}]`

func TestPortFindsAContainerPortInTheMiddleOfAPublishedRange(t *testing.T) {
	rt := fakeShimInspect(t, rangeInspect, 0)
	for _, tc := range []struct {
		name string
		port int
		want string
	}{
		{"the bottom of the range, the one the entry names outright", 80, "0.0.0.0:47210\n"},
		{"the middle of the range", 81, "0.0.0.0:47211\n"},
		{"the top of the range", 82, "0.0.0.0:47212\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := orchestrator.New(portProject(), rt, "opossum", &out).Port("web", tc.port, "tcp"); err != nil {
				t.Fatalf("Port: %v", err)
			}
			if out.String() != tc.want {
				t.Errorf("port %d/tcp printed %q, want %q", tc.port, out.String(), tc.want)
			}
		})
	}
	// A container port outside the range is refused, and the list of what IS
	// published names every port the range actually covers — not just the
	// entry's own first number, which would send the reader looking for 81
	// and 82 among ports that were never there instead of telling them those
	// two are taken already.
	t.Run("outside the range, still refused, naming the whole span", func(t *testing.T) {
		var out bytes.Buffer
		err := orchestrator.New(portProject(), rt, "opossum", &out).Port("web", 83, "tcp")
		want := `no port 83/tcp for container web.demo.opossum: 80/tcp, 81/tcp, 82/tcp`
		if err == nil || err.Error() != want {
			t.Errorf("want error %q, got: %v", want, err)
		}
		if out.Len() != 0 {
			t.Errorf("nothing may be printed when the port is refused, got %q", out.String())
		}
	})
}

// A single port BEFORE the range, both on the same container — a version of
// the fix that only spans info.Ports[0] (plausible, since a range is
// commonly the only entry) would still collapse THIS range: every row above
// puts it first.
const singleThenRangeInspect = `[{"status":{"state":"running","networks":[{"network":"demo-net","ipv4Address":"192.168.64.10/24"}]},"configuration":{"labels":{"opossum.project":"demo"},"publishedPorts":[` +
	`{"containerPort":3000,"hostAddress":"0.0.0.0","hostPort":65345,"proto":"tcp"},` +
	`{"containerPort":80,"count":3,"hostAddress":"0.0.0.0","hostPort":47210,"proto":"tcp"}]}}]`

func TestPortFindsAPublishedRangeThatIsNotTheFirstEntry(t *testing.T) {
	rt := fakeShimInspect(t, singleThenRangeInspect, 0)
	var out bytes.Buffer
	if err := orchestrator.New(portProject(), rt, "opossum", &out).Port("web", 82, "tcp"); err != nil {
		t.Fatalf("Port: %v", err)
	}
	if want := "0.0.0.0:47212\n"; out.String() != want {
		t.Errorf("port 82/tcp printed %q, want %q", out.String(), want)
	}
	var refused bytes.Buffer
	err := orchestrator.New(portProject(), rt, "opossum", &refused).Port("web", 84, "tcp")
	want := `no port 84/tcp for container web.demo.opossum: 3000/tcp, 80/tcp, 81/tcp, 82/tcp`
	if err == nil || err.Error() != want {
		t.Errorf("want error %q, got: %v", want, err)
	}
}

func TestPortPrintsTheHostSideOfAPublishedPort(t *testing.T) {
	rt := fakeShimInspect(t, portInspect, 0)
	for _, tc := range []struct {
		name  string
		port  int
		proto string
		want  string
	}{
		{"a host port chosen at run time", 3000, "tcp", "0.0.0.0:65345\n"},
		{"a host port written in the file", 80, "tcp", "0.0.0.0:8080\n"},
		{"a udp port under --protocol udp", 90, "udp", "0.0.0.0:9090\n"},
		{"a port bound to one address", 443, "tcp", "127.0.0.1:8443\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := orchestrator.New(portProject(), rt, "opossum", &out).Port("web", tc.port, tc.proto); err != nil {
				t.Fatalf("Port: %v", err)
			}
			if out.String() != tc.want {
				t.Errorf("port %d/%s printed %q, want %q", tc.port, tc.proto, out.String(), tc.want)
			}
		})
	}
}

func TestPortRefusesWhatIsNotPublished(t *testing.T) {
	for _, tc := range []struct {
		name    string
		inspect string
		port    int
		proto   string
		want    string
	}{
		// The list is what the reader needs: 90 is there, under udp.
		{"the right port under the wrong protocol", portInspect, 90, "tcp",
			`no port 90/tcp for container web.demo.opossum: 3000/tcp, 80/tcp, 90/udp, 443/tcp`},
		{"a port never published", portInspect, 22, "tcp",
			`no port 22/tcp for container web.demo.opossum: 3000/tcp, 80/tcp, 90/udp, 443/tcp`},
		{"a container publishing nothing",
			`[{"status":{"state":"running"},"configuration":{"labels":{"opossum.project":"demo"},"publishedPorts":[]}}]`, 80, "tcp",
			`no port 80/tcp for container web.demo.opossum: (none published)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := fakeShimInspect(t, tc.inspect, 0)
			var out bytes.Buffer
			err := orchestrator.New(portProject(), rt, "opossum", &out).Port("web", tc.port, tc.proto)
			if err == nil || err.Error() != tc.want {
				t.Errorf("want error %q, got: %v", tc.want, err)
			}
			if out.Len() != 0 {
				t.Errorf("nothing may be printed when the port is refused, got %q", out.String())
			}
		})
	}
}

func TestPortNeedsARunningContainer(t *testing.T) {
	t.Run("a container that was never created or was removed", func(t *testing.T) {
		rt := fakeShimInspect(t, "Error: container not found: web.demo.opossum", 1)
		var out bytes.Buffer
		err := orchestrator.New(portProject(), rt, "opossum", &out).Port("web", 80, "tcp")
		if err == nil || err.Error() != `service "web" is not running` {
			t.Errorf("want the not-running error, got: %v", err)
		}
	})
	t.Run("a container that exists but is stopped", func(t *testing.T) {
		rt := fakeShimInspect(t, strings.Replace(portInspect, `"state":"running"`, `"state":"stopped"`, 1), 0)
		var out bytes.Buffer
		err := orchestrator.New(portProject(), rt, "opossum", &out).Port("web", 80, "tcp")
		if err == nil || err.Error() != `service "web" is not running` {
			t.Errorf("want the not-running error, got: %v", err)
		}
		if out.Len() != 0 {
			t.Errorf("a stopped container's old mapping must not be printed, got %q", out.String())
		}
	})
	t.Run("a service the project does not define", func(t *testing.T) {
		rt := fakeShimInspect(t, portInspect, 0)
		var out bytes.Buffer
		err := orchestrator.New(portProject(), rt, "opossum", &out).Port("nope", 80, "tcp")
		if err == nil || !strings.Contains(err.Error(), `unknown service "nope"`) || !strings.Contains(err.Error(), "web") {
			t.Errorf("want the unknown-service error naming the defined services, got: %v", err)
		}
	})
	t.Run("a stopped runtime is reported, not read as a stopped service", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "SYSTEM_STOPPED=1")
		var out bytes.Buffer
		err := orchestrator.New(portProject(), rt, "opossum", &out).Port("web", 8080, "tcp")
		if err == nil || !strings.Contains(err.Error(), "OPSM-405") {
			t.Errorf("want the runtime-stopped error (OPSM-405), got: %v", err)
		}
	})
}
