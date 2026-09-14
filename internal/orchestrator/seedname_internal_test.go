package orchestrator

import (
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/runtime"
)

// The seed of a volume whose `seed-<volume>.opossum` would pass 63 characters
// runs under a shortened name (#1002); the check that tells "being filled" from
// "held by someone else" has to know it by that name, not by the long spelling
// the runtime would have refused.
func TestSeedIsFillingKnowsTheSeedOfALongVolumeByItsShortenedName(t *testing.T) {
	vol := "demo_" + strings.Repeat("d", 60)
	if !seedIsFilling(vol, []string{runtime.SeedContainerName(vol)}) {
		t.Errorf("the seed %q of %q should read as filling", runtime.SeedContainerName(vol), vol)
	}
	if seedIsFilling(vol, []string{"seed-" + vol + ".opossum"}) {
		t.Errorf("the unshortened spelling is not a seed opossum runs; it should read as another holder")
	}
}
