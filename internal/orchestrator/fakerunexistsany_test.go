package orchestrator_test

import (
	"os"
	"os/exec"
	"testing"
)

// TestFakeRunExistsAnyLeavesTheHolderThere guards what $RUN_EXISTS_ANY says (#1551): a run it refuses
// with `container with id X already exists` names a container the runtime has, so the strict inspect
// the default fakeShim answers finds X, as it does for the holder $RUN_EXISTS names. Without that
// the refusal and the next question about the same name would disagree.
func TestFakeRunExistsAnyLeavesTheHolderThere(t *testing.T) {
	const holder = "db.demo.opossum"
	t.Run("the holder of the refused name is a container", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "RUN_EXISTS_ANY="+holder)
		if out, err := runFake(rt.Env, "run", "-d", "--name", "web.demo.opossum", "alpine"); err == nil {
			t.Fatalf("a run under RUN_EXISTS_ANY should be refused, got %s", out)
		}
		if info := rt.Inspect(holder); !info.Exists {
			t.Fatalf("the holder %s the refusal names should be one the runtime has, got %+v", holder, info)
		}
	})
	// A holder deleted earlier is marked gone, and the refusal says it is there again: whoever took
	// the name since has un-gone it, as $RUN_EXISTS does.
	t.Run("a holder deleted before is there again once the refusal names it", func(t *testing.T) {
		rt, _ := fakeShim(t)
		strictContainers(t, rt, holder)
		if out, err := runFake(rt.Env, "delete", "--force", holder); err != nil {
			t.Fatalf("deleting the holder: %v\n%s", err, out)
		}
		if info := rt.Inspect(holder); info.Exists {
			t.Fatalf("the fixture is wrong: %s was deleted and should be gone, got %+v", holder, info)
		}
		setShimEnv(rt, "RUN_EXISTS_ANY="+holder)
		if out, err := runFake(rt.Env, "run", "-d", "--name", "web.demo.opossum", "alpine"); err == nil {
			t.Fatalf("a run under RUN_EXISTS_ANY should be refused, got %s", out)
		}
		if info := rt.Inspect(holder); !info.Exists {
			t.Fatalf("the refusal names %s, so it is one the runtime has again; got %+v", holder, info)
		}
	})
	t.Run("a name it does not name is still not there", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "RUN_EXISTS_ANY="+holder)
		_, _ = runFake(rt.Env, "run", "-d", "--name", "web.demo.opossum", "alpine")
		if info := rt.Inspect("web.demo.opossum"); info.Exists || info.Unknown {
			t.Fatalf("the name the run was for was refused, so nothing made it; got %+v", info)
		}
	})
}

// runFake runs the fake directly with the Runtime's per-child environment.
func runFake(env []string, args ...string) (string, error) {
	cmd := exec.Command(fakeShimBin, args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
