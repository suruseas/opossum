package orchestrator

import (
	"slices"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// The names reach the build, where a refused image that is one of them is
// told as the context it was (runtime.additionalContextHint).
func TestBuildOptionsCarryTheAdditionalContextNames(t *testing.T) {
	o := &Orchestrator{Project: &compose.Project{Name: "demo"}}
	b := &compose.Build{Context: ".", AdditionalContexts: compose.AdditionalContexts{"lib", "sharedlib"}}
	if got := o.buildOptions("x:1", b, "opossum up").AdditionalContexts; !slices.Equal(got, []string{"lib", "sharedlib"}) {
		t.Errorf("got %q", got)
	}
}
