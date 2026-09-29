package main

// These pin the two call sites in this package that ask orchestrator.StopSupervisor
// to stop a project's supervisor — downCmd's early stop (before the compose file is
// even read) and startSupervisorFor's replace path — against the exact #1401 bug
// shape: silently treating "asked, but couldn't confirm within budget" the same as
// "nothing was running". reportSupervisorStop and supervisorReplacedSafely are
// pinned as pure functions elsewhere (reportsupervisorstop_test.go); what those
// tests cannot prove is that either call site actually reads stopSupervisorFn's
// second return value at all, which is what these drive through the real CLI.

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// downCmd's early stop runs unconditionally once a project name is known, before
// the compose file is even loaded — so nothing needs to actually be running for
// this test: stopSupervisorFn is stubbed straight to "attempted but not
// confirmed", and the assertion is just that the line reaches the user. fakeShim
// is still needed, though: `down`'s own job past the early stop touches the
// runtime, so the root command's preflight (PersistentPreRunE) refuses it before
// RunE runs at all on a machine without the real `container` CLI on PATH — which
// this test's own dev machine may have and CI never does (a real gap this test
// tripped on the pre-push sieve, not merely a style nit).
func TestDownWarnsWhenTheEarlyStopCannotConfirm(t *testing.T) {
	fakeShim(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	saved := stopSupervisorFn
	stopSupervisorFn = func(string) (bool, bool) { return false, true }
	t.Cleanup(func() { stopSupervisorFn = saved })

	out, _ := run(t, "-p", "downearly", "down")
	if !strings.Contains(out, "OPSM-414") {
		t.Errorf("down's early stop must warn with OPSM-414 when it cannot confirm the supervisor stopped, got:\n%s", out)
	}
}

// startSupervisorFor's replace path must not start a new supervisor (or announce
// one) when it could not confirm the old one stopped — the old pid file is still
// there in that case, so StartSupervisor would silently no-op against it while a
// misleading "watching <new services>" notice went out regardless (round-1
// review's blocker 2). This drives it with a REAL first supervisor (built the
// same way TestUpReplacesASupervisorWhoseSetChanged confirms a replacement
// actually happens), stubbing only the second up's stop attempt to "not
// confirmed" — so if this test ever regresses to starting a second supervisor
// anyway, pgrep-style process assertions would need it, which this avoids by
// checking the one fact that matters: the notice text and pid file.
func TestUpDoesNotReplaceASupervisorItCannotConfirmStopped(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	dir := t.TempDir()
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("name: notconf\nservices:\n  web:\n    image: web\n    restart: always\n")
	t.Chdir(dir)

	t.Cleanup(func() { run(t, "down") })
	if _, err := run(t, "up", "--no-build"); err != nil {
		t.Fatalf("first up: %v", err)
	}
	var first int
	waitFor(t, "the first supervisor", func() bool {
		first = supervisorPID(t, state, "notconf")
		return first != 0
	})
	// Registered as soon as its pid is known, not at the end of the test: a
	// Fatal from any assertion below must still not leave a real process
	// running on the machine after the test binary exits (the exact failure
	// this whole change exists to stop being silent about).
	t.Cleanup(func() {
		if p, err := os.FindProcess(first); err == nil {
			_ = p.Signal(syscall.SIGKILL)
		}
	})
	watched := filepath.Join(state, "opossum", "notconf", "supervised")
	waitFor(t, "the watched set to be recorded", func() bool {
		_, err := os.Stat(watched)
		return err == nil
	})
	before, err := os.ReadFile(watched)
	if err != nil {
		t.Fatal(err)
	}

	// A second service gains a policy — the same trigger
	// TestUpReplacesASupervisorWhoseSetChanged uses to force a replace attempt.
	// Named alone (cache, not "up" bare) so Started() is [cache], not
	// [cache web]: what the #1414 addendum must name is whichever of the
	// merged services set oldWatched does not already know about — this
	// distinguishes that from "whatever this up itself started", which the
	// paired test below (…OnlyAServiceWasDropped) is what actually catches a
	// mutation confusing the two (independent review, round 2).
	write("name: notconf\nservices:\n  web:\n    image: web\n    restart: always\n  cache:\n    image: c\n    restart: always\n")
	saved := stopSupervisorFn
	stopSupervisorFn = func(string) (bool, bool) { return false, true }
	t.Cleanup(func() { stopSupervisorFn = saved })
	out, err := run(t, "up", "--no-build", "cache")
	if err != nil {
		t.Fatalf("second up: %v", err)
	}

	if !strings.Contains(out, "OPSM-414") {
		t.Errorf("the second up must warn with OPSM-414, got:\n%s", out)
	}
	if strings.Contains(out, "a small supervisor is now running") {
		t.Errorf("the second up must not announce a new supervisor when it could not confirm the old one stopped, got:\n%s", out)
	}
	// #1414: the generic OPSM-414 line says the old supervisor may still be
	// running, but not up's own consequence — cache, newly given
	// restart: always by this very compose file edit, is watched by nobody
	// (the old supervisor never heard of it, and this up isn't starting a new
	// one over it). "cache" alone would also match the unrelated
	// "Starting cache" line above, so the distinctive phrase is checked whole.
	if !strings.Contains(out, "cache — new to `restart:` watching in this run — will not be watched by anyone") {
		t.Errorf("the second up must say cache is watched by nobody, got:\n%s", out)
	}
	// web is carried over from the first up (already in oldWatched), not new
	// — the old supervisor may still be alive and watching it, so the notice
	// must not claim it too (independent review of #1414: the first version
	// of this fix said the whole merged set, web included, "will not be
	// watched", contradicting the OPSM-414 line above it).
	if strings.Contains(out, "cache, web") {
		t.Errorf("the second up must not lump web (carried over, not new) in with cache as unwatched, got:\n%s", out)
	}
	// The old supervisor's pid file must still be there — stopSupervisorFn was
	// stubbed, so nothing was really signalled, and StopSupervisor only clears it
	// on a confirmed stop.
	after := supervisorPID(t, state, "notconf")
	if after != first {
		t.Errorf("the original supervisor's pid file should be unchanged (still %d), got %d", first, after)
	}
	if !processIsAlive(first) {
		t.Errorf("the original supervisor must still be running — nothing really signalled it")
	}
	// The watched set must be unchanged too: no replacement happened, so nothing
	// should have re-recorded it.
	if afterBytes, rerr := os.ReadFile(watched); rerr != nil || string(afterBytes) != string(before) {
		t.Errorf("the watched set must be unchanged when no replacement happened, before:\n%s\nafter:\n%s", before, afterBytes)
	}
}

// The opposite shape from the test above: a service LOSES its restart:
// policy, so the merged set after this up is a subset of what the old
// supervisor was already watching — nothing new was added, only something
// dropped. There is then nothing this up can honestly say is "watched by
// nobody" (the dropped service is neither in the merged set nor a lie to
// call unwatched, and the rest was already the old supervisor's to begin
// with) — the #1414 addendum line must not print at all here (independent
// review: an earlier version of the fix printed the whole merged set
// regardless, which would have said "web will not be watched by anyone"
// here even though web was never new).
func TestUpSaysNothingIsNewlyUnwatchedWhenOnlyAServiceWasDropped(t *testing.T) {
	fakeShim(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPOSSUM_SELF_BIN", opossumBin)
	dir := t.TempDir()
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("name: notconf2\nservices:\n  web:\n    image: web\n    restart: always\n  db:\n    image: db\n    restart: always\n")
	t.Chdir(dir)

	t.Cleanup(func() { run(t, "down") })
	if _, err := run(t, "up", "--no-build"); err != nil {
		t.Fatalf("first up: %v", err)
	}
	var first int
	waitFor(t, "the first supervisor", func() bool {
		first = supervisorPID(t, state, "notconf2")
		return first != 0
	})
	t.Cleanup(func() {
		if p, err := os.FindProcess(first); err == nil {
			_ = p.Signal(syscall.SIGKILL)
		}
	})
	watched := filepath.Join(state, "opossum", "notconf2", "supervised")
	waitFor(t, "the watched set to be recorded", func() bool {
		_, err := os.Stat(watched)
		return err == nil
	})

	// db loses its restart: policy — Started() (from `up web`, named so db's
	// container is left alone) is [web], a strict subset of the old
	// supervisor's [db web], forcing a replace attempt with nothing new in
	// the merged set.
	write("name: notconf2\nservices:\n  web:\n    image: web\n    restart: always\n  db:\n    image: db\n")
	saved := stopSupervisorFn
	stopSupervisorFn = func(string) (bool, bool) { return false, true }
	t.Cleanup(func() { stopSupervisorFn = saved })
	out, err := run(t, "up", "--no-build", "web")
	if err != nil {
		t.Fatalf("second up: %v", err)
	}

	if !strings.Contains(out, "OPSM-414") {
		t.Errorf("the second up must warn with OPSM-414, got:\n%s", out)
	}
	// The #1414 addendum's fixed opening clause — checked instead of its
	// tail ("will not be watched by anyone"), which an over-claiming
	// version of the message might not share verbatim but would still be
	// wrong to print here.
	if strings.Contains(out, "this up is not starting a new supervisor over it") {
		t.Errorf("the second up must not claim anything is newly unwatched when only a service was dropped, got:\n%s", out)
	}
}
