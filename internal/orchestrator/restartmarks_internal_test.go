package orchestrator

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
)

// The marks of one `restart` are taken in the order of the services' names,
// whatever order they were asked in: two restarts naming the same services the
// other way round would otherwise each hold one the other waits for.
func TestRestartMarksAreTakenInNameOrder(t *testing.T) {
	for _, tc := range []struct {
		name string
		ask  []string
		want string
	}{
		{"asked the other way round", []string{"web", "db", "cache"}, "cache,db,web"},
		{"asked in order", []string{"cache", "db", "web"}, "cache,db,web"},
		{"one service", []string{"web"}, "web"},
		{"the same name twice", []string{"web", "db", "web"}, "db,web"}, // must not wait for its own mark
		{"none", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			o := &Orchestrator{Project: &compose.Project{Name: "demo", BaseDir: t.TempDir()}}
			// Taken in a goroutine with a deadline of its own: a mark waited
			// for by the very call taking it would otherwise hold the package
			// until the whole run times out, taking every other result with it.
			type marks struct {
				taken   []string
				release func()
			}
			got := make(chan marks, 1)
			go func() {
				taken, release := o.markRestartingAll(tc.ask)
				got <- marks{taken, release}
			}()
			select {
			case m := <-got:
				defer m.release()
				if s := strings.Join(m.taken, ","); s != tc.want {
					t.Errorf("want %q taken, got %q", tc.want, s)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("want the marks taken; the call is waiting for a mark of its own")
			}
		})
	}
}

// A restart waiting for another restart of the same service holds it as
// restarting throughout: the mark is released, not removed, so the one waiting
// holds the same file, and a follow keeps reading the same file.
func TestRestartMarkIsHeldByTheNextRestartWaitingForIt(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	o := &Orchestrator{Project: &compose.Project{Name: "demo", BaseDir: t.TempDir()}}
	_, first := o.markRestartingAll([]string{"web"})
	if !o.isRestarting("web") {
		t.Fatal("want web restarting while the first holds the mark")
	}
	var wg sync.WaitGroup
	second := make(chan func(), 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, release := o.markRestartingAll([]string{"web"})
		second <- release
	}()
	time.Sleep(100 * time.Millisecond)
	select {
	case <-second:
		t.Fatal("want the second restart still waiting for the mark; it took it while the first held it")
	default:
	}
	first()
	release := <-second
	if !o.isRestarting("web") {
		t.Error("want web restarting while the second holds the mark")
	}
	release()
	wg.Wait()
	if o.isRestarting("web") {
		t.Error("want web not restarting once both are done")
	}
}

// Two restarts naming the same services in opposite orders finish: each takes
// the marks in the same order, so neither ends up holding one the other waits
// for. Asked in the order given, they deadlock — flock waits without end.
func TestRestartMarksOfOppositeOrdersDoNotWaitForEachOther(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	o := &Orchestrator{Project: &compose.Project{Name: "demo", BaseDir: t.TempDir()}}
	done := make(chan struct{})
	over := make(chan struct{}) // closed when the test gives up, so no round starts after it
	defer close(over)
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for _, ask := range [][]string{{"a", "b"}, {"b", "a"}} {
			wg.Add(1)
			go func(ask []string) {
				defer wg.Done()
				for i := 0; i < 200; i++ {
					select {
					case <-over:
						return
					default:
					}
					_, release := o.markRestartingAll(ask)
					time.Sleep(time.Millisecond)
					release()
				}
			}(ask)
		}
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("want both restarts to finish; each is holding a mark the other waits for")
	}
}
