package orchestrator_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// A start failure names a way out, and until #1103 it named the same one every
// time: `opossum logs <service>`. The rollback runs after the failure and tears
// down what this up started, so by the time anyone can type that command the
// container it reads is usually gone — `opossum logs` answers `container … not
// found` and the guidance reads as advice that was never written.
//
// Measured on the runtime (container 1.4.1, main 9d34234) over a service whose
// `container run` fails (`command: ["/nonexistent-binary"]`): `up --foreground`,
// detached `up`, an `up` whose dependency cannot start, and `run --rm` whose
// dependency cannot start all printed the logs pointer with no container left.
// Measured over five paths; those four are all of the ones that reach this
// message — the fifth, `run` where the one-off itself cannot start, does not.
//
// What the rollback could not remove is the other half: a container it failed to
// delete, or could not ask the runtime about, is still there, and its logs are
// worth reading. So the guidance follows what the teardown actually found rather
// than the path taken to it.
func upFailingService(t *testing.T, env ...string) error {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, _ := fakeShim(t)
	setShimEnv(rt, append([]string{"RUN_FAIL=db.demo.opossum"}, env...)...)
	p := project("demo", map[string]*compose.Service{"db": {Image: "alpine:3"}})
	err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
	if err == nil {
		t.Fatal("the run of db fails, so the up must fail")
	}
	return err
}

func TestTheWayOutFollowsWhatTheRollbackFound(t *testing.T) {
	const logsPointer = "opossum logs db"
	const noneLeft = "there is no container left to read logs from"
	for _, tc := range []struct {
		name string
		env  []string
		// wantLogs is whether a container is there to read afterwards. One
		// field, because the two wordings are the two sides of one question and
		// a row that wanted neither (or both) would be a message with no way out.
		wantLogs bool
	}{
		// The teardown removed it: nothing to read. This is the shape all four
		// measured paths land in.
		{"the rollback removed the container", nil, false},
		// The delete did not take. The rollback line says so in its own words
		// (`still there — opossum down removes it`), and the container is there
		// to be read.
		{"the rollback could not remove it", []string{"DELETE_STICKY=db.demo.opossum"}, true},
		// The runtime stopped answering. Not asked is not gone (#957): the
		// container may well be standing, so the logs stay the way out.
		{"the runtime could not be asked", []string{"INSPECT_FAIL_ONCE_GONE=db.demo.opossum"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := upFailingService(t, tc.env...).Error()
			if strings.Contains(got, logsPointer) != tc.wantLogs {
				if tc.wantLogs {
					t.Errorf("this container is still there, so its logs are the way out; got: %s", got)
				} else {
					t.Errorf("this container is gone, so `opossum logs` reads nothing; got: %s", got)
				}
			}
			// The other half, asked separately: a message that dropped the logs
			// pointer without saying why leaves the reader with no way out at all.
			if strings.Contains(got, noneLeft) == tc.wantLogs {
				t.Errorf("want the missing-container wording iff there is no container; got: %s", got)
			}
			// Whichever way it is worded, the half about the file stays — it is
			// the only advice that holds when the runtime said nothing useful.
			if !strings.Contains(got, "verify the image, command, and mounts in the compose file") {
				t.Errorf("the file is still worth checking in every shape; got: %s", got)
			}
		})
	}
}

// The guidance is about the service that failed, not about the rollback's
// lists. Two services are created and each row gives them different fates, so a
// decision that reads "was anything left standing?" instead of "what happened to
// this one?" gets the wrong answer in one direction or the other.
//
// Both directions are here because one alone is passed by the conditions a
// rollback already has to hand: the report line above says `nothing this `up`
// started is left running` when `len(left) == 0 && len(unknown) == 0`, and
// reusing that for this decision reproduces the bug #1103 is about — the failed
// service's container is gone, but another service's survival keeps pointing the
// reader at logs that read nothing.
func TestTheWayOutFollowsTheServiceThatFailed(t *testing.T) {
	for _, tc := range []struct {
		name string
		// env sets the fates. db is the service that fails; web is the other
		// one. wantRollback is the report line those fates produce — read back
		// rather than assumed, so a knob that stopped working shows as this row
		// not asking what it says it asks.
		env           []string
		wantRollback  string
		wantLogsForDB bool
	}{
		// db's own container survives the teardown: its logs are there to read,
		// whatever became of web.
		{"the failed service is the one still there",
			[]string{"DELETE_STICKY=db.demo.opossum"},
			"Rolled back web — stopped and removed; tried to roll back db, but the container is still there", true},
		// The other way round, and the one a decision made from the rollback's
		// lists gets wrong: db is gone, web is not, so `len(left) == 0` is false
		// while the container the message is about has been removed.
		{"another service is still there, the failed one is gone",
			[]string{"DELETE_STICKY=web.demo.opossum"},
			"Rolled back db — stopped and removed; tried to roll back web, but the container is still there", false},
		// Same shape with the third outcome: the runtime stopped answering about
		// web. db is still gone.
		{"the runtime could not be asked about another service",
			[]string{"INSPECT_FAIL_ONCE_GONE=web.demo.opossum"},
			"Rolled back db — stopped and removed; tried to roll back web, but the runtime could not be asked", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, _ := fakeShim(t)
			setShimEnv(rt, append([]string{"RUN_FAIL=db.demo.opossum"}, tc.env...)...)
			p := project("demo", map[string]*compose.Service{
				"web": {Image: "alpine:3"},
				"db":  {Image: "alpine:3", DependsOn: compose.DependsOn{{Name: "web"}}},
			})
			out := &bytes.Buffer{}
			err := orchestrator.New(p, rt, "opossum", out).Up(true)
			if err == nil {
				t.Fatal("the run of db fails, so the up must fail")
			}
			if !strings.Contains(out.String(), tc.wantRollback) {
				t.Fatalf("this row needs the two services to end differently — want %q; rollback said: %s", tc.wantRollback, out.String())
			}
			if got := strings.Contains(err.Error(), "opossum logs db"); got != tc.wantLogsForDB {
				if tc.wantLogsForDB {
					t.Errorf("db's own container is still there, so its logs are the way out; got: %s", err.Error())
				} else {
					t.Errorf("db's container is gone — what happened to web does not put it back; got: %s", err.Error())
				}
			}
			// Never another service's logs: the failure is db's.
			if strings.Contains(err.Error(), "opossum logs web") {
				t.Errorf("web did not fail, so its logs are not the way out of db's failure; got: %s", err.Error())
			}
		})
	}
}

// Said as "none is left" rather than "the rollback removed it". The teardown
// cannot tell a container it deleted from one that was never made — an image
// that would not come down leaves the service on the created list with nothing
// behind it — and for a reader who wants to read logs the two are the same.
// Claiming a removal that did not happen would be a sentence about opossum's
// bookkeeping rather than about what is there.
// The shape #1103 was reported from: `up --foreground`, where the container
// runs attached and its own output is above the failure. The decision is made
// in the same place for both (detach only reaches `runOpts.Detach`), so this is
// here because the reported shape should have a row of its own — a later change
// that made the guidance depend on the mode would otherwise be caught only by
// hand.
func TestTheForegroundShapeItWasReportedFromGetsTheSameAnswer(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, _ := fakeShim(t)
	setShimEnv(rt, "RUN_FAIL=db.demo.opossum")
	p := project("demo", map[string]*compose.Service{"db": {Image: "alpine:3"}})
	err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(false)
	if err == nil {
		t.Fatal("the run of db fails, so the up must fail")
	}
	if !strings.Contains(err.Error(), "there is no container left to read logs from") {
		t.Errorf("a foreground up rolls back too, so there is nothing to read; got: %s", err.Error())
	}
	if strings.Contains(err.Error(), "opossum logs db") {
		t.Errorf("the container is gone here as well; got: %s", err.Error())
	}
}

// The failure the runtime reported is still reachable through the wrapper. It
// was reachable while this was a `%w` string, and a reader of the error — a
// caller deciding on the exit code, a future decoding of a new signature —
// should not lose it because the wording moved into a type.
func TestTheRuntimesOwnFailureIsStillReachable(t *testing.T) {
	err := upFailingService(t, "RUN_FAIL_STDERR=Error: failed to find target executable /nonexistent-binary")
	var re *runtime.RunError
	if !errors.As(err, &re) {
		t.Fatalf("the runtime's failure should still be reachable through the start failure; got %T: %v", err, err)
	}
	if !strings.Contains(re.Stderr, "failed to find target executable") {
		t.Errorf("and it should still carry what the runtime said, got: %q", re.Stderr)
	}
}

func TestTheWayOutDoesNotClaimARemovalItCannotSee(t *testing.T) {
	got := upFailingService(t).Error()
	if lower := strings.ToLower(got); strings.Contains(lower, "rollback removed") || strings.Contains(lower, "rolled back") {
		t.Errorf("the message must not narrate a removal it cannot confirm; got: %s", got)
	}
	if !strings.Contains(got, "the failure above is what there is") {
		t.Errorf("with no logs to read, what is left is what was already printed; got: %s", got)
	}
}

// The wording is settled after the failure is made — the rollback runs in
// between — so a caller that formats the message earlier would carry the stale
// half. `run` starts its dependencies through the same bring-up and wraps what
// comes back (`starting dependencies: …`), which is where that would show. The
// real path, not a string joined here: wrapping with `%w` renders the inner
// message at the moment of the wrap, and this pins that moment as the later one.
func TestRunsDependenciesCarryTheChosenWayOut(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, _ := fakeShim(t)
	setShimEnv(rt, "RUN_FAIL=db.demo.opossum")
	p := project("demo", map[string]*compose.Service{
		"db":  {Image: "alpine:3"},
		"web": {Image: "alpine:3", DependsOn: compose.DependsOn{{Name: "db"}}},
	})
	err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{Rm: true})
	if err == nil {
		t.Fatal("db cannot start, so the run must fail")
	}
	if !strings.Contains(err.Error(), "starting dependencies: ") {
		t.Fatalf("this row needs the dependency path; got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "there is no container left to read logs from") {
		t.Errorf("the wrapper must carry the wording the rollback settled on; got: %s", err.Error())
	}
	if strings.Contains(err.Error(), "opossum logs db") {
		t.Errorf("db's container was rolled back, so its logs read nothing; got: %s", err.Error())
	}
}
