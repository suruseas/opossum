package orchestrator

import (
	"testing"

	"github.com/suruseas/opossum/internal/runtime"
)

// The hash a service that does not clear its entrypoint had before the mark existed is the hash it
// has now (#1620): a change that fed the mark into every hash would recreate every container
// running on the day it was upgraded. 9387ecf4717144bb is what main answered, taken before the
// mark was added.
func TestTheMarkOfAClearedEntrypointDoesNotMoveTheHashOfOneThatIsNot(t *testing.T) {
	opts := runtime.RunOptions{
		Name: "web", Image: "alpine:3.20", Networks: []string{"demo-net"},
		Command: []string{"serve"}, Entrypoint: []string{"/app/run", "--serve"},
	}
	if got := configHash(opts); got != "9387ecf4717144bb" {
		t.Errorf("configHash = %s, want 9387ecf4717144bb (the hash before the mark)", got)
	}
	cleared := opts
	cleared.Entrypoint, cleared.EntrypointCleared = nil, true
	notCleared := cleared
	notCleared.EntrypointCleared = false
	if configHash(cleared) == configHash(notCleared) {
		t.Errorf("clearing the entrypoint should change the hash")
	}
}
