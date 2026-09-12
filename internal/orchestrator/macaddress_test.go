package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A service's mac_address rides on its first --network as `,mac=…` (the
// runtime takes a MAC per network); other networks are left alone (#879).
func TestUpGivesTheMacAddressToTheFirstNetwork(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web":   {Image: "alpine:3.20", MacAddress: "02:42:ac:11:00:77", Networks: compose.ServiceNetworks{"front", "back"}},
		"plain": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"front"}},
		"solo":  {Image: "alpine:3.20", MacAddress: "02:42:ac:11:00:79"},
	})
	p.Networks = map[string]compose.NetworkDecl{"front": {}, "back": {}}
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if indexOf(log(), "--network demo-front,mac=02:42:ac:11:00:77 --network demo-back ") < 0 {
		t.Errorf("expected the MAC on the first network only, got %v", log())
	}
	// With no networks: the project's default network is the first one.
	if indexOf(log(), "--name solo.demo.opossum --network demo-net,mac=02:42:ac:11:00:79") < 0 {
		t.Errorf("expected the MAC on the default network for a service with no networks:, got %v", log())
	}
	if indexOf(log(), "--name plain.demo.opossum") < 0 || indexOf(log(), "plain.demo.opossum --network demo-front,mac") >= 0 {
		t.Errorf("a service without mac_address must not get one, got %v", log())
	}
	for _, line := range log() {
		if strings.Contains(line, "--name plain.demo.opossum") && strings.Contains(line, "mac=") {
			t.Errorf("plain got a mac: %s", line)
		}
	}
}

func TestRunOneOffGivesTheMacAddressToo(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "alpine:3.20", MacAddress: "02:42:ac:11:00:78"},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{}); err != nil {
		t.Fatalf("RunOneOff: %v", err)
	}
	if indexOf(log(), "--network demo-net,mac=02:42:ac:11:00:78") < 0 {
		t.Errorf("expected the MAC on the one-off run's network, got %v", log())
	}
}
