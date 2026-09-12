package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A service's labels go on the container before opossum's own, so on a
// clash opossum's value is the one the runtime keeps (it keeps the last
// `-l` for a key, measured on 1.4.1) — as docker compose's own labels win.
func TestUpPutsServiceLabelsBeforeItsOwn(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "alpine:3.20", Labels: compose.Labels{"app.tier=front", "opossum.project=other", "flagonly="}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	var runLine string
	for _, line := range log() {
		if strings.HasPrefix(line, "run ") && strings.Contains(line, "--name web.demo.opossum") {
			runLine = line
		}
	}
	if runLine == "" {
		t.Fatalf("no run for web, got %v", log())
	}
	user := strings.Index(runLine, "-l app.tier=front -l opossum.project=other -l flagonly= ")
	own := strings.LastIndex(runLine, "-l opossum.project=demo")
	if user < 0 || own < 0 || own < user {
		t.Errorf("want the service's labels first and opossum's project label after them, got: %s", runLine)
	}
}

// A network declaration's labels are given to `network create --label`.
func TestUpCreatesTheNetworkWithItsLabels(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"back"}},
	})
	p.Networks = map[string]compose.NetworkDecl{"back": {Internal: true, Labels: compose.Labels{"net.tier=back", "owner=ops"}}}
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if indexOf(log(), "network create --internal --label net.tier=back --label owner=ops demo-back") < 0 {
		t.Errorf("expected the network created with its labels, got %v", log())
	}
}

// A one-off run puts the labels in the same order, and creates its network
// with the declaration's labels, as `up` does.
func TestRunOneOffPutsServiceLabelsBeforeItsOwnAndLabelsTheNetwork(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "alpine:3.20", Labels: compose.Labels{"app.tier=front", "opossum.project=other"}, Networks: compose.ServiceNetworks{"back"}},
	})
	p.Networks = map[string]compose.NetworkDecl{"back": {Labels: compose.Labels{"net.tier=back"}}}
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{}); err != nil {
		t.Fatalf("RunOneOff: %v", err)
	}
	if indexOf(log(), "network create --label net.tier=back demo-back") < 0 {
		t.Errorf("expected the one-off run's network created with its labels, got %v", log())
	}
	var runLine string
	for _, line := range log() {
		if strings.HasPrefix(line, "run ") && strings.Contains(line, "web-run.demo.opossum") {
			runLine = line
		}
	}
	user := strings.Index(runLine, "-l app.tier=front -l opossum.project=other ")
	own := strings.LastIndex(runLine, "-l opossum.project=demo")
	if runLine == "" || user < 0 || own < 0 || own < user {
		t.Errorf("want the service's labels first and opossum's project label after them on the one-off run, got: %s", runLine)
	}
}

// The labels are in the fingerprint `up` stamps on the container: the same
// labels leave it alone, a changed value recreates it (the wiring, not just
// the hash function: the hash is taken after the labels are on the run).
func TestUpRecreatesOnlyWhenALabelChanges(t *testing.T) {
	rt, log := fakeShim(t)
	svc := &compose.Service{Image: "alpine:3.20", Labels: compose.Labels{"app.tier=front"}}
	p := project("demo", map[string]*compose.Service{"web": svc})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("first up: %v", err)
	}
	if err := o.Up(true); err != nil {
		t.Fatalf("second up (same labels): %v", err)
	}
	if n := countLines(log(), "--name web.demo.opossum"); n != 1 {
		t.Errorf("the same labels must not recreate the container, want 1 run got %d", n)
	}
	svc.Labels = compose.Labels{"app.tier=back"}
	if err := o.Up(true); err != nil {
		t.Fatalf("third up (changed label): %v", err)
	}
	if n := countLines(log(), "--name web.demo.opossum"); n != 2 {
		t.Errorf("a changed label value must recreate the container, want 2 runs got %d", n)
	}
}
