package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// `up` says a network is being created only for one it created. The runtime
// answers "already exists" for one an earlier `up` made, and the line used to
// be written before it was asked, so every `up` after the first said it was
// creating a network that was there (measured on container 1.4.1: the same
// project, up twice, `Creating network` both times, and `up to date` under it).
//
// The project joins two networks — the default one (web) and a declared one
// (db) — so that a line said for the wrong one of the two shows.
func twoNetworkProject() *compose.Project {
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "alpine:3.20"},
		"db":  {Image: "alpine:3.20", Networks: compose.ServiceNetworks{"back"}},
	})
	p.Networks = map[string]compose.NetworkDecl{"back": {}}
	return p
}

func TestUpSaysCreatingOnlyForANetworkItCreated(t *testing.T) {
	for _, tc := range []struct {
		name, exists  string
		dryRun        bool
		want, notWant []string
	}{
		{"neither is there", "", false,
			[]string{"Creating network demo-net\n", "Creating network demo-back\n"}, nil},
		{"both are there", "1", false,
			nil, []string{"Creating network"}},
		{"only the default one is there", "demo-net", false,
			[]string{"Creating network demo-back\n"}, []string{"Creating network demo-net"}},
		{"only the declared one is there", "demo-back", false,
			[]string{"Creating network demo-net\n"}, []string{"Creating network demo-back"}},
		// A dry run asks the runtime nothing that changes it: it cannot tell a
		// network that is there from one that is not, and says what it would do.
		{"a dry run over networks that are there", "1", true,
			[]string{"Creating network demo-net\n", "Creating network demo-back\n"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			setShimEnv(rt, "NET_EXISTS="+tc.exists)
			var out bytes.Buffer
			o := orchestrator.New(twoNetworkProject(), rt, "opossum", &out)
			o.SetDryRun(tc.dryRun)
			if err := o.Up(true); err != nil {
				t.Fatalf("Up: %v\n%s", err, out.String())
			}
			for _, w := range tc.want {
				if strings.Count(out.String(), w) != 1 {
					t.Errorf("want %q said once, got:\n%s", w, out.String())
				}
			}
			for _, nw := range tc.notWant {
				if strings.Contains(out.String(), nw) {
					t.Errorf("want %q not said, got:\n%s", nw, out.String())
				}
			}
		})
	}
}

// The rows the two above cannot reach, each one a way the answer to "did it
// create the network" can come out differently: a network that is there with
// the subnets the file declares (the runtime is asked what it has, and the answer
// is still "not created"), one whose subnets differ (refused, and not by a
// network that was never being made), one the runtime cannot create, and the
// host-only kind, whose warning follows the line when it is said and stands
// alone when it is not.
func TestTheCreatingLineAndTheAnswerToWhetherItWasCreated(t *testing.T) {
	const creating = "Creating network demo-back"
	const warning = "network demo-back is internal"
	internal := func() *compose.Project {
		p := twoNetworkProject()
		p.Networks = map[string]compose.NetworkDecl{"back": {Internal: true}}
		return p
	}
	for _, tc := range []struct {
		name    string
		project *compose.Project
		env     []string
		says    []string // in this order
		notSays []string
		wantErr string
	}{
		{"a network there with the subnet the file declares", ipamProject("10.7.0.0/24", ""),
			[]string{"NET_EXISTS=1", "NETWORK_SUBNETS=demo-back=10.7.0.0/24"}, nil, []string{"Creating network"}, ""},
		{"a network there with another subnet", ipamProject("10.7.0.0/24", ""),
			[]string{"NET_EXISTS=1", "NETWORK_SUBNETS=demo-back=10.9.0.0/24"}, nil, []string{"Creating network"}, "OPSM-207"},
		{"a network the runtime cannot create", twoNetworkProject(),
			[]string{"NET_CREATE_FAIL=1"}, nil, []string{"Creating network"}, "couldn't create network"},
		{"a host-only network that is made: the line, then the warning", internal(),
			nil, []string{creating, warning}, nil, ""},
		{"a host-only network that is there: the warning alone", internal(),
			[]string{"NET_EXISTS=demo-back"}, []string{warning}, []string{creating}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			setShimEnv(rt, tc.env...)
			var out bytes.Buffer
			err := orchestrator.New(tc.project, rt, "opossum", &out).Up(true)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("Up: %v\n%s", err, out.String())
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("want an error saying %q, got %v\n%s", tc.wantErr, err, out.String())
			}
			at := -1
			for _, s := range tc.says {
				i := strings.Index(out.String(), s)
				if i < 0 || i < at {
					t.Fatalf("want %q said, after what came before it, got:\n%s", s, out.String())
				}
				at = i
			}
			for _, s := range tc.notSays {
				if strings.Contains(out.String(), s) {
					t.Errorf("want %q not said, got:\n%s", s, out.String())
				}
			}
		})
	}
}
