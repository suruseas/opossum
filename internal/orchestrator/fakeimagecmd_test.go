package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// `entrypoint: []` with no command runs the image's CMD, so `up` reads it with `image inspect`.
// The fake answers that when it is given the image's CMD ($IMAGE_CMD, #1635), so the path runs
// through `up` here and not only in the runtime's own tests, which have a shim of their own.
func TestUpRunsTheCmdOfAnImageWhoseEntrypointIsCleared(t *testing.T) {
	rt, log := fakeShim(t)
	setShimEnv(rt, "IMAGE_CMD=img:1=postgres,-c,x")
	svc := compose.Service{Image: "img:1", EntrypointCleared: true}
	o := orchestrator.New(project("demo", map[string]*compose.Service{"w": &svc}), rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("up: %v\n%s", err, strings.Join(log(), "\n"))
	}
	runs := runLinesOf(log(), "")
	if len(runs) != 1 || !strings.Contains(runs[0], "--entrypoint=postgres img:1 -c x") {
		t.Errorf("want the image's CMD run in place of its entrypoint, got:\n%s", strings.Join(runs, "\n"))
	}
}

// Without the CMD given, the fake answers as it always did (the image is here and says nothing),
// and `up` refuses the service as it refuses one whose image has no CMD.
func TestUpRefusesAClearedEntrypointWhenTheFakeImageHasNoCmd(t *testing.T) {
	rt, _ := fakeShim(t)
	svc := compose.Service{Image: "img:1", EntrypointCleared: true}
	o := orchestrator.New(project("demo", map[string]*compose.Service{"w": &svc}), rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err == nil || !strings.Contains(err.Error(), "command") {
		t.Errorf("want a refusal that names the command, got: %v", err)
	}
}
