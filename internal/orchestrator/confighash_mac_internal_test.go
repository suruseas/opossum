package orchestrator

import (
	"testing"

	"github.com/suruseas/opossum/internal/runtime"
)

// The config hash tracks the emitted `container run` command, so a changed
// mac_address recreates the container; an unset one keeps the hash
// existing containers were made with.
func TestConfigHashSeesTheMacAddress(t *testing.T) {
	base := runtime.RunOptions{Name: "web", Image: "alpine:3.20", Networks: []string{"demo-net"}}
	withMac := base
	withMac.MacAddress = "02:42:ac:11:00:77"
	other := base
	other.MacAddress = "02:42:ac:11:00:78"
	if configHash(base) == configHash(withMac) {
		t.Errorf("a mac_address must change the hash")
	}
	if configHash(withMac) == configHash(other) {
		t.Errorf("a different mac_address must change the hash")
	}
	if configHash(base) != configHash(runtime.RunOptions{Name: "web", Image: "alpine:3.20", Networks: []string{"demo-net"}}) {
		t.Errorf("with no mac_address the hash must be what it was")
	}
}

// Labels are part of the fingerprint: a changed value recreates the
// container, and the same set in another order does not.
func TestConfigHashSeesTheLabels(t *testing.T) {
	base := runtime.RunOptions{Name: "web", Image: "alpine:3.20", Networks: []string{"demo-net"}, Labels: []string{"app.tier=front", "opossum.project=demo"}}
	changed := base
	changed.Labels = []string{"app.tier=back", "opossum.project=demo"}
	reordered := base
	reordered.Labels = []string{"opossum.project=demo", "app.tier=front"}
	if configHash(base) == configHash(changed) {
		t.Errorf("a changed label value must change the hash")
	}
	if configHash(base) != configHash(reordered) {
		t.Errorf("the same labels in another order must not change the hash")
	}
}
