package orchestrator_test

// #1272 A wiring check: a unit test against specificStartError/runErrorHint
// directly supplies `order` itself, so it cannot see either of Up's two call
// sites failing to pass `order` through. Driving a real Up() with the
// compiled fake shim is what actually exercises that wiring.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/orchestrator"
)

// "a" is given a host-port RANGE that "b" is asked for as a single number — the
// pre-flight's own per-entry probe does not read a's range (hostPortBinding
// refuses to probe one) and refuseDuplicateHostPorts does not either, so both
// entries pass pre-flight. "a" starts first (alphabetically, no dependency
// between them) and really binds the range; "b"'s run then fails against it.
func TestUpNamesASiblingAlreadyRunningOnAHostPortConflict(t *testing.T) {
	rt, _ := fakeShim(t)
	setShimEnv(rt, "RUN_FAIL=b.demo.opossum", "RUN_FAIL_STDERR="+portConflictStderr, "INSPECT_ABSENT=b.demo.opossum")
	proj, err := loadProject(t, "services:\n"+
		"  a:\n    image: alpine:3\n    ports: [\"47180-47181:80-81\"]\n"+
		"  b:\n    image: alpine:3\n    ports: [\"47181:90\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = orchestrator.New(proj, rt, "opossum", &out).Up(true)
	if err == nil {
		t.Fatal("want the up refused")
	}
	if want := `held by this project's service "a", whose own`; !strings.Contains(err.Error(), want) {
		t.Errorf("did not name the already-running sibling as the holder, got: %v", err)
	}
}

// The same wiring, exercised at Up's OTHER call site into specificStartError:
// a run-to-completion dependency (service_completed_successfully) whose own
// failure a required dependent waits on, rather than a plain long-running
// service's run. Both call sites read the same local `order`, but a mistake
// at either one is a mistake this unit test alone (which supplies `order`
// itself) cannot see.
func TestUpNamesASiblingAlreadyRunningOnAHostPortConflictForARunToCompletionDependency(t *testing.T) {
	rt, _ := fakeShim(t)
	setShimEnv(rt, "RUN_FAIL=b.demo.opossum", "RUN_FAIL_STDERR="+portConflictStderr, "INSPECT_ABSENT=b.demo.opossum")
	proj, err := loadProject(t, "services:\n"+
		"  a:\n    image: alpine:3\n    ports: [\"47180-47181:80-81\"]\n"+
		"  b:\n    image: alpine:3\n    ports: [\"47181:90\"]\n"+
		"  c:\n    image: alpine:3\n    depends_on:\n      b:\n        condition: service_completed_successfully\n")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = orchestrator.New(proj, rt, "opossum", &out).Up(true)
	if err == nil {
		t.Fatal("want the up refused")
	}
	if want := `held by this project's service "a", whose own`; !strings.Contains(err.Error(), want) {
		t.Errorf("did not name the already-running sibling as the holder, got: %v", err)
	}
}

const portConflictStderr = "Error: failed to bootstrap container (cause: bind(descriptor:ptr:bytes:): Address already in use) (errno: 48)\n"
