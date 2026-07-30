package orchestrator

import (
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
)

func pol(t *testing.T, v string) compose.RestartPolicy {
	t.Helper()
	p, err := compose.ParseRestart(v)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The policy is the whole contract of the feature, so each mode is pinned.
func TestDecidePerPolicy(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name   string
		policy string
		state  serviceState
		want   restartDecision
	}{
		{"no policy is not supervised", "", serviceState{}, leaveIt},
		{"explicit no", "no", serviceState{}, leaveIt},
		{"always restarts", "always", serviceState{}, restartIt},
		{"always restarts even after many tries", "always", serviceState{restarts: 20}, restartIt},
		{"unless-stopped restarts a crash", "unless-stopped", serviceState{}, restartIt},
		{"unless-stopped honours our own stop", "unless-stopped", serviceState{stoppedByUs: true}, leaveIt},
		// Docker documents `always` as restarting a manually stopped container only
		// when the daemon restarts — so within one supervisor lifetime a stop stands
		// for this policy too. Undoing `opossum stop` within one poll would make the
		// command useless for the most common policy there is.
		{"always also honours our own stop", "always", serviceState{stoppedByUs: true}, leaveIt},
		{"on-failure retries", "on-failure", serviceState{restarts: 1}, restartIt},
		{"on-failure gives up at the default bound", "on-failure", serviceState{restarts: maxOnFailureRetries}, gaveUp},
		{"on-failure:1 gives up after one", "on-failure:1", serviceState{restarts: 1}, gaveUp},
		{"on-failure:5 keeps going", "on-failure:5", serviceState{restarts: 4}, restartIt},
		{"a service already given up on is left alone", "always", serviceState{gaveUp: true}, leaveIt},
	}
	for _, c := range cases {
		got, _ := decide(pol(t, c.policy), c.state, now)
		if got != c.want {
			t.Errorf("%s: decide = %v, want %v", c.name, got, c.want)
		}
	}
}

// The first restart is immediate: this feature exists for a service that lost a
// startup race and will succeed on the next try, and delaying that is pure
// latency. Only a repeating failure should be slowed down, and it must be capped
// so a crash loop can't spin.
func TestBackoffGrowsAndIsCapped(t *testing.T) {
	if d := backoffFor(0); d != 0 {
		t.Errorf("the first restart should be immediate, got %v", d)
	}
	prev := time.Duration(-1)
	for i := 0; i < len(backoffSteps); i++ {
		d := backoffFor(i)
		if d < prev {
			t.Errorf("backoff should not shrink: step %d = %v after %v", i, d, prev)
		}
		prev = d
	}
	capped := backoffFor(len(backoffSteps) + 50)
	if capped != backoffSteps[len(backoffSteps)-1] {
		t.Errorf("backoff must cap, got %v for a large n", capped)
	}
}

// A service that recovers and then stays up has not been "failing repeatedly" —
// resetting its count is what stops a service that crashes once an hour from
// eventually being given up on.
func TestStableRunResetsTheCount(t *testing.T) {
	now := time.Now()
	st := serviceState{restarts: 3, lastAction: now.Add(-stableRun - time.Second), gaveUp: true}
	got := observeRunning(st, now, pol(t, "always"))
	if got.restarts != 0 || got.gaveUp {
		t.Errorf("a stable run should reset the count, got %+v", got)
	}
	// Still inside the window: nothing is proven yet.
	fresh := serviceState{restarts: 3, lastAction: now.Add(-time.Second)}
	if observeRunning(fresh, now, pol(t, "always")).restarts != 3 {
		t.Error("a brief run must not reset the count")
	}
}

// Seeing the container running again clears a stop opossum recorded — otherwise
// an `unless-stopped` service that the user later started by hand would never be
// supervised again.
func TestRunningClearsTheRecordedStop(t *testing.T) {
	got := observeRunning(serviceState{stoppedByUs: true}, time.Now(), pol(t, "always"))
	if got.stoppedByUs {
		t.Error("a running container should clear the recorded stop")
	}
}

// The on-failure bound must survive a long-running job. A service that runs for
// longer than stableRun before exiting cleanly would otherwise have its counter
// reset every cycle and be restarted forever — the exact outcome the bound, the
// README and the `opossum config` note all promise won't happen.
func TestOnFailureBoundSurvivesLongRuns(t *testing.T) {
	now := time.Now()
	st := serviceState{restarts: 2, lastAction: now.Add(-stableRun - time.Minute)}
	got := observeRunning(st, now, pol(t, "on-failure"))
	if got.restarts != 2 {
		t.Errorf("on-failure must not reset its count on a long run, got %d", got.restarts)
	}
	// …and it still gives up once the bound is reached.
	if d, _ := decide(pol(t, "on-failure"), serviceState{restarts: 3}, now); d != gaveUp {
		t.Errorf("on-failure should give up at the bound, got %v", d)
	}
	// A long-running `always` service DOES reset — its promise is to keep running.
	if observeRunning(st, now, pol(t, "always")).restarts != 0 {
		t.Error("always should reset after a stable run")
	}
}

// The bound is the whole on-failure story, so pin the number rather than the
// constant — comparing the constant to itself passes for any value.
func TestOnFailureDefaultBoundIsSmall(t *testing.T) {
	now := time.Now()
	if d, _ := decide(pol(t, "on-failure"), serviceState{restarts: 3}, now); d != gaveUp {
		t.Errorf("the default on-failure bound should be 3 restarts, got %v at 3", d)
	}
	if d, _ := decide(pol(t, "on-failure"), serviceState{restarts: 2}, now); d != restartIt {
		t.Errorf("two restarts should still be within the bound, got %v", d)
	}
}
