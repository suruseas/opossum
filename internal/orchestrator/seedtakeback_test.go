package orchestrator_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// A Ctrl-C while a new volume is being filled takes the seed back: the throwaway
// container is stopped and removed and the half-filled volume deleted. `stop` and
// `delete` report nothing that says the thing is gone, so each is asked about
// afterwards and the verdict says what is still there — the way `run`'s own
// cleanup does — instead of saying the fill was taken back because the commands
// were sent (#955).
//
// The rows are the runtime's answers to that question, for a fill under `up` and
// under `run`: everything gone (the wording stays as it was), the container left,
// the volume left, both, and a runtime that cannot be asked. The "left" worlds are
// made by the shim's sticky knobs, since the machine will not leave them on demand.
func TestASeedTakeBackSaysWhatItLeftBehind(t *testing.T) {
	seeded := func() *compose.Project {
		return project("demo", map[string]*compose.Service{"web": {Image: "node:20-alpine", Volumes: []string{"data:/var/data"}}})
	}
	seedName := runtime.SeedContainerName("demo_data")
	for _, tc := range []struct {
		name string
		env  []string
		// want are the phrases the verdict has to carry, in it; wantNot are ones it
		// must not — every row that is not clean says nothing about being taken back.
		want, wantNot []string
	}{
		{name: "everything is gone", env: nil,
			wantNot: []string{"still there", "could not be asked"}},
		{name: "the container is still there", env: []string{"DELETE_STICKY=" + seedName},
			want:    []string{"the container " + seedName + " that was filling the volume demo_data is still there", "container delete --force " + seedName},
			wantNot: []string{"half-filled volume"}},
		{name: "the volume is still there", env: []string{"VOLUME_DELETE_STICKY=demo_data"},
			want:    []string{"the half-filled volume demo_data is still there", "container volume delete demo_data"},
			wantNot: []string{"that was filling the volume"}},
		{name: "both are still there", env: []string{"DELETE_STICKY=" + seedName, "VOLUME_DELETE_STICKY=demo_data"},
			want: []string{"the container " + seedName + " that was filling the volume demo_data is still there", "the half-filled volume demo_data is still there"}},
		{name: "the runtime cannot be asked about the container", env: []string{"INSPECT_FAIL=" + seedName},
			want:    []string{"could not be asked whether the container " + seedName + " that was filling the volume demo_data is gone", "container ls -a", "container delete --force " + seedName},
			wantNot: []string{"is still there"}},
		{name: "the runtime cannot be asked about the volume", env: []string{"VOLUME_LS_FAIL_FROM=2"},
			want:    []string{"could not be asked whether the half-filled volume demo_data is gone", "container volume ls", "container volume delete demo_data"},
			wantNot: []string{"that was filling the volume"}},
	} {
		for _, via := range []string{"up", "run"} {
			t.Run(tc.name+" ("+via+")", func(t *testing.T) {
				rt, log := fakeShim(t)
				setShimEnv(rt, append([]string{"RUN_HANG=" + seedName}, tc.env...)...)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				o := orchestrator.New(seeded(), rt, "opossum", &bytes.Buffer{})
				o.OnSignal(ctx)
				done := make(chan error, 1)
				go func() {
					if via == "up" {
						done <- o.Up(true)
					} else {
						done <- o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: true})
					}
				}()
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) && indexOf(log(), "--name "+seedName+" ") < 0 {
					time.Sleep(2 * time.Millisecond)
				}
				if indexOf(log(), "--name "+seedName+" ") < 0 {
					t.Fatalf("the fill never started, got %v", log())
				}
				// The shim logs a run as it starts and records the volume that run
				// mounts a moment later (the real runtime makes it as a side effect of
				// the run). A cancel between the two kills a run that made nothing, and
				// the verdict is "nothing left" for a reason that is not the row's.
				// Wait until the volume is there to be found, as it is once the fill
				// is really under way.
				for time.Now().Before(deadline) && !rt.VolumeExists("demo_data") {
					time.Sleep(2 * time.Millisecond)
				}
				if !rt.VolumeExists("demo_data") {
					t.Fatalf("the fill started but the volume was never made, got %v", log())
				}
				cancel()
				var err error
				select {
				case err = <-done:
				case <-time.After(8 * time.Second):
					t.Fatal("did not return")
				}
				if err == nil {
					t.Fatal("an interrupted fill came back with no error")
				}
				msg := err.Error()
				// A world with nothing left is the wording each path had already.
				clean := len(tc.want) == 0
				switch {
				case clean && via == "up" && msg != "interrupted — rolling back":
					t.Errorf("want up's own wording, got %q", msg)
				case clean && via == "run" && msg != "interrupted — the fill of a new volume for web was taken back; the one-off did not start":
					t.Errorf("want run's own wording, got %q", msg)
				}
				for _, w := range tc.want {
					if !strings.Contains(msg, w) {
						t.Errorf("the verdict does not say %q:\n%s", w, msg)
					}
				}
				for _, w := range tc.wantNot {
					if strings.Contains(msg, w) {
						t.Errorf("the verdict says %q, which is not what happened:\n%s", w, msg)
					}
				}
				if !clean && via == "run" && strings.Contains(msg, "was taken back") {
					t.Errorf("run says the fill was taken back with something left:\n%s", msg)
				}
			})
		}
	}
}

// The sentences come in the order the things are taken back — the container, then
// the volume it held — so the reader who runs the commands as written removes them
// in an order the runtime accepts.
func TestASeedTakeBackNamesTheContainerBeforeTheVolume(t *testing.T) {
	seedName := runtime.SeedContainerName("demo_data")
	rt, log := fakeShim(t)
	setShimEnv(rt, "RUN_HANG="+seedName, "DELETE_STICKY="+seedName, "VOLUME_DELETE_STICKY=demo_data")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := project("demo", map[string]*compose.Service{"web": {Image: "node:20-alpine", Volumes: []string{"data:/var/data"}}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	o.OnSignal(ctx)
	done := make(chan error, 1)
	go func() { done <- o.Up(true) }()
	for i := 0; i < 1500 && indexOf(log(), "--name "+seedName+" ") < 0; i++ {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	err := <-done
	if err == nil {
		t.Fatal("no error")
	}
	c, v := strings.Index(err.Error(), "container "+seedName), strings.Index(err.Error(), "half-filled volume")
	if c < 0 || v < 0 || c > v {
		t.Errorf("want the container before the volume, got:\n%s", err)
	}
}

// A volume that is only cleared (`nocopy`) is taken back through the other branch
// of the seeding, and says the same.
func TestANocopyVolumesTakeBackSaysWhatItLeftBehindToo(t *testing.T) {
	seedName := runtime.SeedContainerName("demo_data")
	rt, log := fakeShim(t)
	setShimEnv(rt, "RUN_HANG="+seedName, "DELETE_STICKY="+seedName)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := project("demo", map[string]*compose.Service{"web": {Image: "node:20-alpine",
		Volumes: []string{"data:/var/data"}, NoCopy: []string{"/var/data"}}})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	o.OnSignal(ctx)
	done := make(chan error, 1)
	go func() { done <- o.Up(true) }()
	for i := 0; i < 1500 && indexOf(log(), "--name "+seedName+" ") < 0; i++ {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "container delete --force "+seedName) {
		t.Errorf("the take-back of a nocopy volume does not say what it left:\n%v", err)
	}
}
