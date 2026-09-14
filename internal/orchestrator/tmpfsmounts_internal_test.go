package orchestrator

import (
	"slices"
	"strings"
	"testing"
)

// An option that holds the word with a space around it is not `defaults`: the
// docker engine 29.7.2 and container 1.4.1 both refuse it, so it is passed on
// as written rather than dropped into a mount that starts.
func TestTmpfsMountsKeepAnOptionThatOnlyHoldsDefaults(t *testing.T) {
	for _, m := range []string{"/t: defaults", "/t:defaults ", "/t:exec, defaults"} {
		_, opts, _ := strings.Cut(m, ":")
		want := []string{"/t:nosuid,nodev,noexec," + opts}
		if got := tmpfsMounts([]string{m}); !slices.Equal(got, want) {
			t.Errorf("tmpfsMounts(%q) = %q, want %q", m, got, want)
		}
	}
}
