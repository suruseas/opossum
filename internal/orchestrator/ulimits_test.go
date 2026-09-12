package orchestrator_test

import (
	"bytes"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// shm_size reaches `container run` as --shm-size <bytes> and each ulimit as
// --ulimit name=soft[:hard], on `up` and on a one-off run (#885).
func TestUpAndRunPassShmSizeAndUlimits(t *testing.T) {
	svc := func() *compose.Service {
		return &compose.Service{Image: "alpine:3.20", ShmSize: "1073741824", Ulimits: compose.Ulimits{"nofile": {Soft: 65536, Hard: 65536}, "nproc": {Soft: 100, Hard: 200}}}
	}
	t.Run("up", func(t *testing.T) {
		rt, log := fakeShim(t)
		o := orchestrator.New(project("demo", map[string]*compose.Service{"web": svc()}), rt, "opossum", &bytes.Buffer{})
		if err := o.Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if indexOf(log(), "--shm-size 1073741824 --ulimit nofile=65536 --ulimit nproc=100:200 ") < 0 {
			t.Errorf("expected --shm-size and the two --ulimit flags in name order, got %v", log())
		}
	})
	t.Run("one-off run", func(t *testing.T) {
		rt, log := fakeShim(t)
		o := orchestrator.New(project("demo", map[string]*compose.Service{"web": svc()}), rt, "opossum", &bytes.Buffer{})
		if err := o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{}); err != nil {
			t.Fatalf("RunOneOff: %v", err)
		}
		if indexOf(log(), "--shm-size 1073741824 --ulimit nofile=65536 --ulimit nproc=100:200 ") < 0 {
			t.Errorf("expected the same flags on the one-off run, got %v", log())
		}
	})
	t.Run("nothing set sends nothing", func(t *testing.T) {
		rt, log := fakeShim(t)
		o := orchestrator.New(project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20"}}), rt, "opossum", &bytes.Buffer{})
		if err := o.Up(true); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if indexOf(log(), "--shm-size") >= 0 || indexOf(log(), "--ulimit") >= 0 {
			t.Errorf("no shm_size/ulimits must send no flag, got %v", log())
		}
	})
}

// A changed value — not only a first setting — recreates the container: the
// hash carries the values, not just that something is set.
func TestUpRecreatesWhenShmSizeOrAUlimitValueChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*compose.Service)
	}{
		{"shm_size value", func(s *compose.Service) { s.ShmSize = "1073741824" }},
		{"a limit's value", func(s *compose.Service) {
			s.Ulimits = compose.Ulimits{"nofile": {Soft: 2048, Hard: 2048}, "nproc": {Soft: 100, Hard: 200}}
		}},
		{"a limit removed", func(s *compose.Service) { s.Ulimits = compose.Ulimits{"nofile": {Soft: 1024, Hard: 1024}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			svc := &compose.Service{Image: "alpine:3.20", ShmSize: "67108864", Ulimits: compose.Ulimits{"nofile": {Soft: 1024, Hard: 1024}, "nproc": {Soft: 100, Hard: 200}}}
			o := orchestrator.New(project("demo", map[string]*compose.Service{"web": svc}), rt, "opossum", &bytes.Buffer{})
			if err := o.Up(true); err != nil {
				t.Fatalf("first up: %v", err)
			}
			if err := o.Up(true); err != nil {
				t.Fatalf("second up (same): %v", err)
			}
			if n := countLines(log(), "--name web.demo.opossum"); n != 1 {
				t.Fatalf("the same values must not recreate, want 1 run got %d", n)
			}
			tc.change(svc)
			if err := o.Up(true); err != nil {
				t.Fatalf("third up (changed): %v", err)
			}
			if n := countLines(log(), "--name web.demo.opossum"); n != 2 {
				t.Errorf("changing %s must recreate the container, want 2 runs got %d", tc.name, n)
			}
		})
	}
}
