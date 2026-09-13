package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The CLI-level fake keeps the same contract as the orchestrator's: a stop is
// visible to a later inspect as "stopped", a run of the same name clears it,
// and stopping or deleting a name that is gone is the one failure the real
// CLI reports (rc 1, `notFound`; measured on container 1.4.1).
func TestCLIFakeStopAndDeleteChangeWhatInspectSays(t *testing.T) {
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	const name = "one.demo.opossum"
	shim := func(args ...string) (string, error) {
		out, err := exec.Command(fakeShimBin, args...).CombinedOutput()
		return string(out), err
	}
	if _, err := shim("run", "-d", "--name", name, "alpine"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := shim("stop", name); err != nil {
		t.Fatalf("stop of a present container succeeds, got: %v", err)
	}
	if out, _ := shim("inspect", name); !strings.Contains(out, `"state":"stopped"`) {
		t.Errorf("after a stop, inspect says stopped, got: %s", out)
	}
	if _, err := shim("run", "-d", "--name", name, "alpine"); err != nil {
		t.Fatalf("run again: %v", err)
	}
	if out, _ := shim("inspect", name); !strings.Contains(out, `"state":"running"`) {
		t.Errorf("after a run, inspect says running again, got: %s", out)
	}
	if _, err := shim("stop", name); err != nil {
		t.Fatalf("stop again: %v", err)
	}
	if _, err := shim("start", name); err != nil {
		t.Fatalf("start: %v", err)
	}
	if out, _ := shim("inspect", name); !strings.Contains(out, `"state":"running"`) {
		t.Errorf("after a start, inspect says running again, got: %s", out)
	}
	if _, err := shim("delete", "--force", name); err != nil {
		t.Fatalf("delete of a present container succeeds, got: %v", err)
	}
	for _, verb := range []string{"stop", "delete"} {
		out, err := shim(verb, name)
		if err == nil {
			t.Errorf("%s of a gone container exits non-zero on the real CLI, the fake returned 0: %s", verb, out)
		}
		if want := `notFound: "container with ID ` + name + ` not found"`; !strings.Contains(out, want) {
			t.Errorf("%s of a gone container says %q, got: %s", verb, want, out)
		}
	}
}
