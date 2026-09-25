package orchestrator_test

// Evals for a compose file that publishes one host port twice.
//
// The pre-flight walks the published ports to probe each one for a foreign
// listener, and it skips an address it has already probed — a saving, not a
// judgement. So two entries naming the same host port were walked past in
// silence, and the run reached the runtime, which refused the pair with a
// message about publish specs. Nothing said which two lines were the problem.
//
// What counts as a collision was measured rather than reasoned about, and the
// two runtimes differ:
//
//	                                        container 1.4.1   docker v5.5.1
//	two services, same address              second bind fails  refused
//	two services, 127.0.0.1 and the LAN ip   both listen        both listen
//	two services, 127.0.0.1 and a wildcard   both listen        refused
//	one service, two addresses               refused (overlap)  both listen
//
// opossum refuses what its own runtime refuses. The wildcard pair is left to
// start, because turning a project that runs today into one that does not is
// the more expensive mistake; that it differs from docker compose is written
// down rather than fixed here.

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

func TestAFileThatPublishesOneHostPortTwiceIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		// ports maps a service name to its entries. %[1]d is the port under
		// test, %[2]d another free one.
		ports       map[string][]string
		only        []string // the services this run starts; nil means all
		wantRefused bool
	}{
		// Two services, plainly the same port.
		{"two services name the same host port", map[string][]string{
			"a": {"%[1]d:80"}, "z": {"%[1]d:81"}}, nil, true},
		// One service, two of its own entries. The runtime says "publish
		// specs may not overlap" here, which is about what opossum handed
		// it rather than about the file.
		{"one service names it twice", map[string][]string{
			"a": {"%[1]d:80", "%[1]d:81"}}, nil, true},
		// The same address twice, written out rather than left implicit.
		{"the same address twice", map[string][]string{
			"a": {"127.0.0.1:%[1]d:80"}, "z": {"127.0.0.1:%[1]d:81"}}, nil, true},
		// Two services, two different addresses. Both bind: the runtime
		// starts them, so refusing would break a project that works.
		{"two services on different addresses", map[string][]string{
			"a": {"127.0.0.1:%[1]d:80"}, "z": {"[::1]:%[1]d:81"}}, nil, false},
		// An address and a wildcard, across services. The runtime starts
		// both here too — docker compose refuses this one, and that
		// difference is written down rather than closed.
		{"an address-bound entry and a wildcard one, across services", map[string][]string{
			"a": {"127.0.0.1:%[1]d:80"}, "z": {"%[1]d:81"}}, nil, false},
		// The same two entries inside ONE service. The runtime refuses two
		// publish specs of one container that overlap, whatever addresses
		// they name — so the address no longer rescues the pair.
		{"one service on two different addresses", map[string][]string{
			"a": {"127.0.0.1:%[1]d:80", "[::1]:%[1]d:81"}}, nil, true},
		// The same number written with a leading zero is the same port.
		{"the same port written with a leading zero", map[string][]string{
			"a": {"0%[1]d:80"}, "z": {"%[1]d:81"}}, nil, true},
		// Two services publishing the identical entry.
		{"two services publish the identical entry", map[string][]string{
			"a": {"%[1]d:80"}, "z": {"%[1]d:80"}}, nil, true},
		// A service that runs to completion before its dependent starts is
		// gone by the time the other publishes, so the two never overlap.
		{"a one-shot dependency and its dependent", map[string][]string{
			"seed": {"%[1]d:80"}, "app": {"%[1]d:81"}}, nil, false},
		// The two wildcards. `0.0.0.0` gets an IPv4 listener and `[::]` an
		// IPv6 one, and both start — the probe treats them as one question,
		// which is where this went wrong the first time.
		{"the two wildcards, across services", map[string][]string{
			"a": {"0.0.0.0:%[1]d:80"}, "z": {"[::]:%[1]d:81"}}, nil, false},
		// A bare host port is the IPv4 wildcard written the short way.
		{"a bare host port and the IPv6 wildcard", map[string][]string{
			"a": {"%[1]d:80"}, "z": {"[::]:%[1]d:81"}}, nil, false},
		// And the IPv6 wildcard twice does collide.
		{"the IPv6 wildcard twice", map[string][]string{
			"a": {"[::]:%[1]d:80"}, "z": {"[::]:%[1]d:81"}}, nil, true},
		// A bare host port and `0.0.0.0` are the same wildcard.
		{"a bare host port and 0.0.0.0", map[string][]string{
			"a": {"%[1]d:80"}, "z": {"0.0.0.0:%[1]d:81"}}, nil, true},
		// Two IPv6 addresses that differ only after the first colon are asked
		// about one level down instead — see
		// TestTwoAddressesThatDifferAfterTheFirstColonAreTwoAddresses. The pair
		// that poses the question needs a second IPv6 address, and this machine
		// binds only ::1: any other literal is refused before the comparison is
		// reached, by the check that an address can be bound at all. Asking the
		// comparison directly is the way to ask it without that in the way.
		// A dependency gated on health keeps running while its dependent
		// publishes, so the two do collide. Only a run-to-completion
		// dependency is out of the way.
		{"a healthy dependency and its dependent", map[string][]string{
			"db": {"%[1]d:80"}, "web": {"%[1]d:81"}}, nil, true},
		// A run-to-completion dependency that is optional still runs to
		// completion, so it is still out of the way.
		{"an optional one-shot dependency and its dependent", map[string][]string{
			"seed": {"%[1]d:80"}, "opt": {"%[1]d:81"}}, nil, false},
		// Three entries on one port, where the middle one is on another
		// address. The clash is between the first and the third, so a check
		// that only remembers the entry it saw last never sees it.
		{"three entries, and the clash skips the middle one", map[string][]string{
			"a": {"127.0.0.1:%[1]d:80"},
			"b": {"[::1]:%[1]d:81"},
			"c": {"127.0.0.1:%[1]d:82"}}, nil, true},
		// A different protocol is a different port. udp/8080 and tcp/8080
		// are published side by side, here as on the runtime.
		{"the same number on different protocols", map[string][]string{
			"a": {"%[1]d:80/tcp"}, "z": {"%[1]d:81/udp"}}, nil, false},
		// Different numbers, the plain case.
		{"different host ports", map[string][]string{
			"a": {"%[1]d:80"}, "z": {"%[2]d:81"}}, nil, false},
		// The other service is not started by this command. Nothing
		// collides, so nothing is refused — the file is only contradictory
		// when both are asked to run.
		{"the other entry belongs to a service this run does not start", map[string][]string{
			"a": {"%[1]d:80"}, "z": {"%[1]d:81"}}, []string{"a"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port, other := freePort(t), freePort(t)
			services := map[string]*compose.Service{}
			for name, specs := range tc.ports {
				s := &compose.Service{Image: "web:latest"}
				for _, f := range specs {
					s.Ports = append(s.Ports, fmt.Sprintf(f, port, other))
				}
				services[name] = s
			}
			if app, ok := services["app"]; ok {
				app.DependsOn = compose.DependsOn{{Name: "seed", Condition: compose.ConditionCompleted}}
			}
			if opt, ok := services["opt"]; ok {
				opt.DependsOn = compose.DependsOn{{Name: "seed", Condition: compose.ConditionCompleted, Optional: true}}
			}
			if web, ok := services["web"]; ok {
				web.DependsOn = compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}}
				services["db"].Healthcheck = &compose.Healthcheck{Test: []string{"CMD", "true"}}
			}
			rt, log := fakeShim(t)
			var out bytes.Buffer
			err := orchestrator.New(project("demo", services), rt, "opossum", &out).Up(true, tc.only...)
			if refused := err != nil; refused != tc.wantRefused {
				t.Fatalf("up err = %v; wanted it %s.\ncalls:\n%s", err,
					map[bool]string{true: "refused", false: "accepted"}[tc.wantRefused],
					strings.Join(log(), "\n"))
			}
			if !tc.wantRefused {
				return
			}
			if !strings.Contains(err.Error(), "[OPSM-213]") {
				t.Errorf("the refusal is not the one about two entries on one host port: %v", err)
			}
			// Nothing was started. A refusal that arrives after the first
			// container is up leaves the user to clean up.
			for _, c := range log() {
				if strings.HasPrefix(c, "run ") {
					t.Errorf("a container was started before the refusal:\n%s", strings.Join(log(), "\n"))
					break
				}
			}
		})
	}
}

// The refusal has to name both lines: the fix is to change one of them, and
// which one is the user's choice. Naming only the second — the one the walk
// happened to reach later — leaves them looking for the first.
func TestTheRefusalNamesBothEntries(t *testing.T) {
	port := freePort(t)
	services := map[string]*compose.Service{
		"a": {Image: "web:latest", Ports: []string{fmt.Sprintf("127.0.0.1:%d:80", port)}},
		"z": {Image: "web:latest", Ports: []string{fmt.Sprintf("127.0.0.1:%d:81", port)}},
	}
	rt, _ := fakeShim(t)
	var out bytes.Buffer
	err := orchestrator.New(project("demo", services), rt, "opossum", &out).Up(true)
	if err == nil {
		t.Fatal("the file publishes one host port twice and was accepted")
	}
	for _, want := range []string{
		fmt.Sprintf("host port %d/tcp", port),
		fmt.Sprintf(`service "a" (127.0.0.1:%d:80)`, port),
		fmt.Sprintf(`service "z" (127.0.0.1:%d:81)`, port),
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}
}

// Two services start in the order the file's dependencies put them in, not in
// name order. A check that kept whichever entry it saw later — or that decided
// which to name by comparing the names — reads the pair back the other way
// round when the order is reversed, and then the refusal points at the line
// the user did not write second.
func TestTheEarlierServiceInTheStartOrderIsNamedFirst(t *testing.T) {
	port := freePort(t)
	// z starts first: a depends on it. The names are the other way round from
	// the start order, so a check that fell back on the name is caught here.
	services := map[string]*compose.Service{
		"z": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:80", port)}},
		"a": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:81", port)},
			DependsOn: compose.DependsOn{{Name: "z", Condition: compose.ConditionStarted}}},
	}
	rt, _ := fakeShim(t)
	var out bytes.Buffer
	err := orchestrator.New(project("demo", services), rt, "opossum", &out).Up(true)
	if err == nil {
		t.Fatal("the file publishes one host port twice and was accepted")
	}
	first := fmt.Sprintf("  - service %q (%d:80)\n  - service %q (%d:81)", "z", port, "a", port)
	if !strings.Contains(err.Error(), first) {
		t.Errorf("the refusal does not name z before a, which is the order they start in:\n%v", err)
	}
}

// The services are walked in the order they start, so the two lines are
// named in the same order every time. Reading the same file twice and being
// told to change a different line each time is its own bug.
func TestTheRefusalReadsTheSameWayEveryTime(t *testing.T) {
	port := freePort(t)
	first := ""
	for i := 0; i < 20; i++ {
		services := map[string]*compose.Service{
			"a": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:80", port)}},
			"m": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:81", port)}},
			"z": {Image: "web:latest", Ports: []string{fmt.Sprintf("%d:82", port)}},
		}
		rt, _ := fakeShim(t)
		var out bytes.Buffer
		err := orchestrator.New(project("demo", services), rt, "opossum", &out).Up(true)
		if err == nil {
			t.Fatalf("reading %d: the file publishes one host port three times and was accepted", i)
		}
		if first == "" {
			first = err.Error()
			continue
		}
		if err.Error() != first {
			t.Fatalf("reading %d gave a different refusal.\nfirst:\n%s\nnow:\n%s", i, first, err)
		}
	}
}
