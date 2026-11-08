package orchestrator_test

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// probed is a healthcheck a test never waits the real 30 s interval of.
func probed(retries int) *compose.Healthcheck {
	return &compose.Healthcheck{Interval: time.Millisecond, Test: []string{"probe"}, Retries: retries, Timeout: time.Second}
}

// upWaiting runs a detached `up --wait` over p with the fake steered by env, and
// returns what it returned, what it said, and the runtime calls it made.
func upWaiting(t *testing.T, p *compose.Project, wait bool, timeout time.Duration, env ...string) (error, string, []string) {
	t.Helper()
	rt, log := fakeShim(t)
	setShimEnv(rt, env...)
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	o.SetWait(wait, timeout)
	err := o.Up(true)
	return err, out.String(), log()
}

// What `up --wait` waits on, and what it says when the wait does not end well.
// docker compose v5.6.1 is the reference (measured): a service with a healthcheck
// that passes is rc 0, one that fails it or exits is rc 1, a --wait-timeout that
// runs out is rc 1, and in each failure the containers are left as they are.
func TestUpWaitWaitsForTheStartedServicesHealthchecks(t *testing.T) {
	one := func(hc *compose.Healthcheck) *compose.Project {
		return project("demo", map[string]*compose.Service{"web": {Image: "web:latest", Healthcheck: hc}})
	}

	t.Run("a healthcheck that passes on the third probe is waited for", func(t *testing.T) {
		err, out, lines := upWaiting(t, one(probed(5)), true, 0, "HEALTH_OK_AT=3")
		if err != nil {
			t.Fatalf("Up: %v", err)
		}
		if n := countLines(lines, "exec web.demo.opossum probe"); n != 3 {
			t.Errorf("want 3 probes, got %d in %v", n, lines)
		}
		if run, probe := indexOf(lines, "run -d --name web.demo.opossum"), indexOf(lines, "exec web.demo.opossum probe"); run < 0 || probe < run {
			t.Errorf("the probes come after the service is started: run %d, first probe %d", run, probe)
		}
		if !strings.Contains(out, "Waiting for web to be healthy") {
			t.Errorf("the wait is not said:\n%s", out)
		}
	})

	t.Run("without --wait nothing is probed", func(t *testing.T) {
		err, _, lines := upWaiting(t, one(probed(5)), false, 0, "HEALTH_OK_AT=3")
		if err != nil {
			t.Fatalf("Up: %v", err)
		}
		if n := countLines(lines, "exec "); n != 0 {
			t.Errorf("an up without --wait probed %d times: %v", n, lines)
		}
	})

	t.Run("a healthcheck that never passes fails the up and leaves the service up", func(t *testing.T) {
		err, _, lines := upWaiting(t, one(probed(2)), true, 0, "HEALTH_OK_AT=100000")
		if err == nil {
			t.Fatal("an unhealthy service passed `up --wait`")
		}
		for _, want := range []string{"[OPSM-417]", `service "web" did not become healthy`, "healthcheck did not pass after 2 attempt(s)", "the services stay up"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the failure does not say %q:\n%v", want, err)
			}
		}
		if n := countLines(lines, "exec web.demo.opossum probe"); n != 2 {
			t.Errorf("want the 2 attempts the healthcheck allows, got %d", n)
		}
		run := indexOf(lines, "run -d --name web.demo.opossum")
		for i, l := range lines[run+1:] {
			if strings.HasPrefix(l, "stop ") || strings.HasPrefix(l, "delete ") || strings.HasPrefix(l, "network delete") {
				t.Errorf("the service was taken down after it was started (line %d): %q", run+1+i, l)
			}
		}
	})

	t.Run("a service with no healthcheck is not probed", func(t *testing.T) {
		err, _, lines := upWaiting(t, one(nil), true, 0)
		if err != nil {
			t.Fatalf("Up: %v", err)
		}
		if n := countLines(lines, "exec "); n != 0 {
			t.Errorf("probed %d times: %v", n, lines)
		}
	})

	t.Run("a healthcheck switched off is not probed", func(t *testing.T) {
		hc := probed(5)
		hc.Disabled = true
		err, _, lines := upWaiting(t, one(hc), true, 0, "HEALTH_OK_AT=100000")
		if err != nil {
			t.Fatalf("Up: %v", err)
		}
		if n := countLines(lines, "exec "); n != 0 {
			t.Errorf("probed %d times: %v", n, lines)
		}
	})

	t.Run("a healthcheck with no test is not probed", func(t *testing.T) {
		hc := probed(5)
		hc.Test = nil
		err, _, lines := upWaiting(t, one(hc), true, 0, "HEALTH_OK_AT=100000")
		if err != nil {
			t.Fatalf("Up: %v", err)
		}
		if n := countLines(lines, "exec "); n != 0 {
			t.Errorf("probed %d times: %v", n, lines)
		}
	})

	t.Run("a one-shot that completed is not probed", func(t *testing.T) {
		p := project("demo", map[string]*compose.Service{
			"init": {Image: "init:latest", Healthcheck: probed(5)},
			"web":  {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "init", Condition: compose.ConditionCompleted}}},
		})
		err, _, lines := upWaiting(t, p, true, 0, "HEALTH_OK_AT=100000")
		if err != nil {
			t.Fatalf("Up: %v", err)
		}
		if n := countLines(lines, "exec "); n != 0 {
			t.Errorf("probed %d times: %v", n, lines)
		}
	})

	t.Run("the first service that does not pass ends the wait", func(t *testing.T) {
		p := project("demo", map[string]*compose.Service{
			"a": {Image: "a:latest", Healthcheck: probed(2)},
			"b": {Image: "b:latest", Healthcheck: probed(2)},
		})
		err, _, lines := upWaiting(t, p, true, 0, "HEALTH_OK_AT=100000")
		if err == nil || !strings.Contains(err.Error(), `service "a"`) {
			t.Fatalf("want a failure naming a, got %v", err)
		}
		if n := countLines(lines, "exec b.demo.opossum"); n != 0 {
			t.Errorf("b was probed after a failed: %d", n)
		}
	})

	t.Run("every service that passes is waited for, the second after the first", func(t *testing.T) {
		p := project("demo", map[string]*compose.Service{
			"a": {Image: "a:latest", Healthcheck: probed(5)},
			"b": {Image: "b:latest", Healthcheck: probed(5)},
		})
		// The fake's count is the project's: a passes on the 2nd probe, and b's first probe is the 3rd.
		err, _, lines := upWaiting(t, p, true, 0, "HEALTH_OK_AT=2")
		if err != nil {
			t.Fatalf("Up: %v", err)
		}
		if a, b := countLines(lines, "exec a.demo.opossum"), countLines(lines, "exec b.demo.opossum"); a != 2 || b != 1 {
			t.Errorf("want a probed twice and b once, got %d and %d in %v", a, b, lines)
		}
	})
}

// --wait-timeout bounds the wait after starting, and only that.
func TestUpWaitTimeout(t *testing.T) {
	t.Run("a wait that outlasts it fails, saying so", func(t *testing.T) {
		p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest", Healthcheck: probed(1_000_000)}})
		start := time.Now()
		err, _, _ := upWaiting(t, p, true, 300*time.Millisecond, "HEALTH_OK_AT=100000")
		if err == nil {
			t.Fatal("a wait past --wait-timeout passed")
		}
		for _, want := range []string{"[OPSM-417]", `service "web" was not healthy within the --wait-timeout of 300ms`, "the services stay up"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the failure does not say %q:\n%v", want, err)
			}
		}
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("took %s", d)
		}
	})

	t.Run("an interval longer than it does not outlast it", func(t *testing.T) {
		hc := probed(1_000_000)
		hc.Interval = time.Hour
		p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest", Healthcheck: hc}})
		done := make(chan error, 1)
		go func() {
			err, _, _ := upWaiting(t, p, true, 300*time.Millisecond, "HEALTH_OK_AT=100000")
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "--wait-timeout") {
				t.Errorf("want the timeout, got %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("the wait slept an hour's interval past a 300ms --wait-timeout")
		}
	})

	t.Run("zero is no limit", func(t *testing.T) {
		p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest", Healthcheck: probed(50)}})
		err, _, lines := upWaiting(t, p, true, 0, "HEALTH_OK_AT=20")
		if err != nil {
			t.Fatalf("Up: %v", err)
		}
		if n := countLines(lines, "exec web.demo.opossum probe"); n != 20 {
			t.Errorf("want 20 probes, got %d", n)
		}
	})

	t.Run("the dependency wait before the services start is not bounded by it", func(t *testing.T) {
		p := project("demo", map[string]*compose.Service{
			"db":  {Image: "db:latest", Healthcheck: probed(5)},
			"web": {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}}},
		})
		// db passes on its 3rd probe, waited on before web starts; a 1ns
		// --wait-timeout reaching back there would fail at the first sleep. After
		// the start, db's first probe is the 4th and passes before any sleep.
		err, _, lines := upWaiting(t, p, true, time.Nanosecond, "HEALTH_OK_AT=3")
		if err != nil {
			t.Fatalf("Up: %v", err)
		}
		if n := countLines(lines, "exec db.demo.opossum probe"); n != 4 {
			t.Errorf("want 3 probes before web starts and 1 after, got %d in %v", n, lines)
		}
	})
}

// The --wait deadline is the wait's own: an orchestrator that runs `up` again
// (as a watch does) does not carry a deadline that has passed into the next
// one's dependency wait.
func TestUpWaitDeadlineEndsWithTheWait(t *testing.T) {
	rt, log := fakeShim(t)
	setShimEnv(rt, "HEALTH_OK_AT=1")
	p := project("demo", map[string]*compose.Service{
		"db":  {Image: "db:latest", Healthcheck: probed(5)},
		"web": {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy}}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	o.SetWait(true, time.Nanosecond)
	if err := o.Up(true); err != nil {
		t.Fatalf("first Up: %v", err)
	}
	before := countLines(log(), "exec db.demo.opossum probe")
	// The next up, without --wait, waits for db again and fails its first probe.
	// SetWait and not SetUpOptions, which would clear the deadline itself.
	o.SetWait(false, 0)
	setShimEnv(rt, "HEALTH_OK_AT="+strconv.Itoa(before+2))
	if err := o.Up(true); err != nil {
		t.Fatalf("an up after a --wait was bounded by that wait's deadline: %v", err)
	}
	if n := countLines(log(), "exec db.demo.opossum probe") - before; n != 2 {
		t.Errorf("want db probed twice in the second up, got %d", n)
	}
}

// A Ctrl-C during the wait stops waiting; the services are up, and it says so
// rather than that it is rolling back, which it is not.
func TestUpWaitInterruptedLeavesTheServicesUp(t *testing.T) {
	rt, log := fakeShim(t)
	setShimEnv(rt, "HEALTH_OK_AT=100000")
	p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest", Healthcheck: probed(1_000_000)}})
	ctx, cancel := context.WithCancel(context.Background())
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	o.OnSignal(ctx)
	o.SetWait(true, 0)
	done := make(chan error, 1)
	go func() { done <- o.Up(true) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && indexOf(log(), "exec web.demo.opossum probe") < 0 {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("up --wait did not stop on an interrupt")
	}
	if err == nil || !strings.Contains(err.Error(), `interrupted while waiting for service "web" to be healthy; the services stay up`) {
		t.Errorf("want the interrupted wait said, got %v", err)
	}
	lines := log()
	run := indexOf(lines, "run -d --name web.demo.opossum")
	for _, l := range lines[run+1:] {
		if strings.HasPrefix(l, "stop ") || strings.HasPrefix(l, "delete ") {
			t.Errorf("an interrupted wait took the service down: %q", l)
		}
	}
}

// A dry run starts nothing, so there is nothing to wait for.
func TestUpWaitDryRunProbesNothing(t *testing.T) {
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest", Healthcheck: probed(5)}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	o.SetDryRun(true)
	o.SetWait(true, 0)
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if n := countLines(log(), "exec "); n != 0 {
		t.Errorf("a dry run probed %d times", n)
	}
}
