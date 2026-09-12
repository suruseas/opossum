package orchestrator

import (
	"strings"
	"testing"
)

// Two takes of the same project's lock through the real code path — what
// two opossum processes do — conflict: the second is refused at once, and
// the first's release frees it. A shared lock would let both through.
func TestProjectLockConflictsWithItself(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	first, err := lockProject("p")
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if _, err := lockProject("p"); err == nil || !strings.Contains(err.Error(), "[OPSM-208]") {
		t.Fatalf("second lock must be refused with OPSM-208, got: %v", err)
	}
	// Another project is not in the way.
	other, err := lockProject("q")
	if err != nil {
		t.Fatalf("a different project's lock must be free: %v", err)
	}
	other.release()
	first.release()
	again, err := lockProject("p")
	if err != nil {
		t.Fatalf("after release the lock must be free: %v", err)
	}
	again.release()
}
