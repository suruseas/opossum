package orchestrator

import (
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
)

// A non-positive healthcheck timeout (unset, or an explicit `timeout: 0s`) must
// fall back to the default rather than run unbounded — otherwise a hung probe
// could still block `up` forever (#139).
func TestProbeTimeoutClampsNonPositive(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"unset", 0, defaultProbeTimeout},
		{"explicit zero", 0 * time.Second, defaultProbeTimeout},
		{"negative", -5 * time.Second, defaultProbeTimeout},
		{"positive kept", 3 * time.Second, 3 * time.Second},
	}
	for _, c := range cases {
		if got := probeTimeout(&compose.Healthcheck{Timeout: c.in}); got != c.want {
			t.Errorf("%s: probeTimeout(%s) = %s, want %s", c.name, c.in, got, c.want)
		}
	}
}
