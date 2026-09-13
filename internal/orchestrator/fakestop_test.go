package orchestrator_test

import (
	"os/exec"
	"testing"

	"github.com/suruseas/opossum/internal/runtime"
)

// The fake models what `stop` and `delete` do to what a later `inspect` says,
// the way container 1.4.1 does (measured): after a stop the container is still
// there and its state is "stopped"; after a delete it is gone; stopping or
// deleting a name that is not there is the one failure the CLI reports (rc 1,
// `notFound`). Without this, "did the teardown work?" could not be asked of
// the fake at all — a stop that did nothing would look like one that worked.
func TestFakeStopAndDeleteChangeWhatInspectSays(t *testing.T) {
	const name = "one.demo.opossum"
	rt, _ := fakeShim(t)
	if err := rt.Run(runtime.RunOptions{Name: name, Image: "alpine"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if info := rt.Inspect(name); !info.Exists || info.State != "running" {
		t.Fatalf("a container just run is running, got %+v", info)
	}
	rt.Stop(name)
	if info := rt.Inspect(name); !info.Exists || info.State != "stopped" {
		t.Errorf("after a stop the container is there and stopped, got %+v", info)
	}
	// Running the name again makes it running again — a stop is not for ever.
	if err := rt.Run(runtime.RunOptions{Name: name, Image: "alpine"}); err != nil {
		t.Fatalf("run again: %v", err)
	}
	if info := rt.Inspect(name); !info.Exists || info.State != "running" {
		t.Errorf("after a stop and a run the container is running again, got %+v", info)
	}
	rt.Stop(name)
	if err := rt.Start(name); err != nil {
		t.Fatalf("start: %v", err)
	}
	if info := rt.Inspect(name); !info.Exists || info.State != "running" {
		t.Errorf("after a stop and a start the container is running again, got %+v", info)
	}
	rt.Delete(name)
	if info := rt.Inspect(name); info.Exists {
		t.Errorf("after a delete the container is gone, got %+v", info)
	}
	// A runtime that cannot answer is neither present nor gone.
	setShimEnv(rt, "INSPECT_FAIL="+name)
	if info := rt.Inspect(name); !info.Unknown || info.Exists {
		t.Errorf("an inspect the runtime cannot answer is Unknown, not gone, got %+v", info)
	}
	// The CLI's one reported failure: the name is not there.
	for _, verb := range []string{"stop", "delete"} {
		cmd := exec.Command(rt.Bin, verb, name)
		cmd.Env = append(cmd.Env, rt.Env...)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Errorf("%s of a container that is gone exits non-zero on the real CLI, the fake returned 0: %s", verb, out)
		}
		if want := `notFound: "container with ID ` + name + ` not found"`; !contains(string(out), want) {
			t.Errorf("%s of a gone container says %q, got: %s", verb, want, out)
		}
	}
}
