package orchestrator_test

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A `restart` that waits for another restart of a service it names has not
// started yet: an earlier `stop` of the services it also names still stands
// until it holds every mark, because it is this restart that undoes the stop
// and it has not begun. A restart that forgot the stop first would leave a
// service stopped on purpose with nothing recording it, which the supervisor
// reads as a crash.
func TestARestartWaitingForAnotherLeavesAnEarlierStopStanding(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, _ := fakeShim(t)
	setShimEnv(rt, "STOP_THEN_SLEEP_MS=2000")
	proj := project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20"}, "db": {Image: "alpine:3.20"}})
	newO := func() *orchestrator.Orchestrator { return orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}) }
	stops := func() int {
		deep, _ := filepath.Glob(filepath.Join(os.Getenv("XDG_STATE_HOME"), "*", "*", "stopped-*"))
		flat, _ := filepath.Glob(filepath.Join(os.Getenv("XDG_STATE_HOME"), "*", "stopped-*"))
		return len(deep) + len(flat)
	}
	newO().MarkStopped("web")
	if stops() != 1 {
		t.Fatalf("want the stop of web recorded, got %d markers", stops())
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // holds db's mark for the length of its stop
		defer wg.Done()
		if err := newO().Restart([]string{"db"}); err != nil {
			t.Errorf("restart db: %v", err)
		}
	}()
	time.Sleep(300 * time.Millisecond) // the first restart holds db's mark
	waiting := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(waiting)
		// web first: a restart that forgot the stops in the order it was asked
		// would forget web's before it waits for db's mark.
		if err := newO().Restart([]string{"web", "db"}); err != nil {
			t.Errorf("restart web db: %v", err)
		}
	}()
	<-waiting
	time.Sleep(300 * time.Millisecond) // the second restart is waiting for db's mark
	if got := stops(); got != 1 {
		t.Errorf("want the stop of web still recorded while the restart waits, got %d markers", got)
	}
	wg.Wait()
	if got := stops(); got != 0 {
		t.Errorf("want the stop of web forgotten once the restart ran, got %d markers", got)
	}
}
