package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A network the runtime refuses for the key of a label is answered with the advice to fix the key; the advice about the runtime's health and
// a stale network is for the failures it does not fit (#1729). Every refusal the runtime makes of a key (measured, container 1.5.0: `-x`, an empty
// key, one with a space, one with a tab) reaches the same line, and a failure that is not about a label keeps the old advice.
func TestTheAdviceAfterAFailedNetworkCreateFollowsTheCause(t *testing.T) {
	const stale = "if a stale network with that name exists"
	const fixKey = "fix the label key it names"
	for _, tc := range []struct {
		name    string
		labels  compose.Labels
		env     []string
		says    string
		notSays string
	}{
		{"a key that starts with a dash", compose.Labels{"-x=1"}, nil, fixKey, stale},
		{"an empty key", compose.Labels{"=1"}, nil, fixKey, stale},
		{"a key with a space", compose.Labels{"a b=1"}, nil, fixKey, stale},
		{"a key with a tab", compose.Labels{"a\tb=1"}, nil, fixKey, stale},
		{"a key with a control character that is no space", compose.Labels{"a\x01b=1"}, nil, fixKey, stale},
		{"a failure that is not about a label", compose.Labels{"ok=1"}, []string{"NET_CREATE_FAIL=1"}, stale, fixKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := twoNetworkProject()
			p.Networks = map[string]compose.NetworkDecl{"back": {Labels: tc.labels}}
			rt, _ := fakeShim(t)
			setShimEnv(rt, tc.env...)
			var out bytes.Buffer
			err := orchestrator.New(p, rt, "opossum", &out).Up(true)
			if err == nil {
				t.Fatalf("Up read the network: %s", out.String())
			}
			if !strings.Contains(err.Error(), tc.says) || strings.Contains(err.Error(), tc.notSays) {
				t.Errorf("want the advice %q and not %q, got:\n%v", tc.says, tc.notSays, err)
			}
		})
	}
}

// The same advice follows a `run` that has to make the network.
func TestTheAdviceAfterAFailedNetworkCreateForARunFollowsTheCause(t *testing.T) {
	p := twoNetworkProject()
	p.Networks = map[string]compose.NetworkDecl{"back": {Labels: compose.Labels{"-x=1"}}}
	rt, _ := fakeShim(t)
	var out bytes.Buffer
	err := orchestrator.New(p, rt, "opossum", &out).RunOneOff("db", nil, orchestrator.RunOneOffOptions{})
	if err == nil || !strings.Contains(err.Error(), "fix the label key it names") || strings.Contains(err.Error(), "stale network") {
		t.Errorf("want the advice about the label key, got %v", err)
	}
}

// A network whose label has no value (`labels: {k: ""}`) is made: the runtime refuses `--label=k=` for a network (the fake does as the runtime does),
// so the label is given as the key alone (#1816).
func TestANetworkWhoseLabelHasNoValueIsMade(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, _ := fakeShim(t)
	p := twoNetworkProject()
	p.Networks = map[string]compose.NetworkDecl{"back": {Labels: compose.Labels{"k=", "tier=db"}}}
	var out bytes.Buffer
	if err := orchestrator.New(p, rt, "opossum", &out).Up(true); err != nil {
		t.Fatalf("Up: %v\n%s", err, out.String())
	}
	labels, ok := rt.NetworkLabels("demo-back")
	if !ok {
		t.Fatalf("the network was not made:\n%s", out.String())
	}
	if v, has := labels["k"]; !has || v != "" || labels["tier"] != "db" {
		t.Errorf("the network's labels are %v, want k with no value and tier=db", labels)
	}
}
