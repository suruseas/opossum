package orchestrator_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/orchestrator"
)

// What a caller sets with SetAfterUpLocked runs as `up` finishes, with the error it returns, while the project's lock is still held —
// so a `down` asked from inside it is refused — and not for a dry run, which takes no lock (#1740).
func TestWhatRunsAsUpFinishesRunsUnderTheProjectLock(t *testing.T) {
	t.Run("it runs once, before the lock is let go, and the lock is free after", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		rt, _ := fakeShim(t)
		o := orchestrator.New(lockProject(), rt, "opossum", &bytes.Buffer{})
		calls := 0
		var downFromInside error
		o.SetAfterUpLocked(func(upErr error) {
			calls++
			other := orchestrator.New(lockProject(), rt, "opossum", &bytes.Buffer{})
			downFromInside = other.Down(false, "", false)
		})
		if err := o.Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if calls != 1 {
			t.Errorf("the hook ran %d times, want 1", calls)
		}
		if downFromInside == nil || !strings.Contains(downFromInside.Error(), "OPSM-208") {
			t.Errorf("a down from inside the hook: want it refused (OPSM-208), got %v", downFromInside)
		}
		if err := orchestrator.New(lockProject(), rt, "opossum", &bytes.Buffer{}).Down(false, "", false); err != nil {
			t.Errorf("a down after Up has returned: %v", err)
		}
	})
	t.Run("it is given the error Up returns", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		rt, _ := fakeShim(t)
		setShimEnv(rt, "NET_CREATE_FAIL=1")
		o := orchestrator.New(twoNetworkProject(), rt, "opossum", &bytes.Buffer{})
		var got error
		o.SetAfterUpLocked(func(upErr error) { got = upErr })
		err := o.Up(true)
		if err == nil || got == nil || !errors.Is(got, err) && got.Error() != err.Error() {
			t.Errorf("the hook was given %v, Up returned %v", got, err)
		}
	})
	t.Run("a dry run does not run it", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		rt, _ := fakeShim(t)
		o := orchestrator.New(lockProject(), rt, "opossum", &bytes.Buffer{})
		o.SetUpOptions(false, false, false, false, false)
		o.SetDryRun(true)
		ran := false
		o.SetAfterUpLocked(func(error) { ran = true })
		if err := o.Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if ran {
			t.Errorf("the hook ran in a dry run")
		}
	})
}
