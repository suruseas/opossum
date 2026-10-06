package orchestrator

import (
	"bytes"
	"fmt"
	"strings"
	"syscall"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// `down` and `destroy` over a supervisor `ps` would not vouch for (#1759): it was not signalled, so the notice does not say it was
// asked to stop. It says nothing was asked, names the process, and gives the command to look with. A supervisor that was asked and
// would not go keeps its own wording (the OPSM-414 notice that says it was asked), and so does one that is not there.
func TestTheStopNoticeSaysWhatWasDoneToTheSupervisor(t *testing.T) {
	t.Run("a process ps will not vouch for: not asked, the process named", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		pid := aLiveOtherProcess(t)
		useReadPs(t, pid, 1<<30, "")
		pidFileOf(t, "demo", pid, readPsMarker)
		var out bytes.Buffer
		o := New(&compose.Project{Name: "demo"}, nil, "opossum", &out)
		if left := o.stopSupervisorAndReport(); !left {
			t.Errorf("stopSupervisorAndReport said no supervisor is left")
		}
		said := out.String()
		for _, want := range []string{"[OPSM-414]", "was not asked to stop", fmt.Sprintf("process %d", pid), fmt.Sprintf("ps -p %d", pid)} {
			if !strings.Contains(said, want) {
				t.Errorf("the notice lacks %q:\n%s", want, said)
			}
		}
		if strings.Contains(said, "asked the restart supervisor to stop") {
			t.Errorf("the notice says the supervisor was asked, and it was not:\n%s", said)
		}
		if err := syscall.Kill(pid, 0); err != nil {
			t.Errorf("the process was signalled: %v", err)
		}
	})

	t.Run("asked and not confirmed, and ps will not answer by the time the notice is chosen: it still says it was asked", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		pid := aLiveOtherProcess(t)
		useReadPs(t, pid, 1<<30, "")
		var out bytes.Buffer
		o := New(&compose.Project{Name: "demo"}, nil, "opossum", &out)
		// Nothing was unchecked when the stop began (no pid file). The stop asked, and what it left behind is a file
		// `ps` will not vouch for: a notice that reads the state after the stop would say it was never asked.
		o.stopSupervisor = func(string) (bool, bool) {
			pidFileOf(t, "demo", pid, readPsMarker)
			return false, true
		}
		o.stopSupervisorAndReport()
		said := out.String()
		if !strings.Contains(said, "asked the restart supervisor to stop") || strings.Contains(said, "was not asked to stop") {
			t.Errorf("the notice does not say the supervisor was asked:\n%s", said)
		}
	})

	t.Run("the notice is for what was read before the stop", func(t *testing.T) {
		if got := NoticeSupervisorStopFailedFor(0); got != NoticeSupervisorStopFailed() {
			t.Errorf("with nothing unchecked the notice is %q, want %q", got, NoticeSupervisorStopFailed())
		}
		if got := NoticeSupervisorStopFailedFor(77); got != NoticeSupervisorUnchecked(77) {
			t.Errorf("with 77 unchecked the notice is %q, want %q", got, NoticeSupervisorUnchecked(77))
		}
		if !strings.Contains(NoticeSupervisorStopFailed(), "asked the restart supervisor to stop") {
			t.Errorf("the notice for a supervisor that was asked no longer says so: %s", NoticeSupervisorStopFailed())
		}
	})
}
