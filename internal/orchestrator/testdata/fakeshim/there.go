package main

import (
	"os"
	"strings"
)

// there says whether a container of this name is there as far as this fake is
// concerned: one named in $INSPECT_ABSENT was never made, and one the `delete`
// case marked gone is no longer there. The real CLI answers `start` and `logs`
// for a name that is not there with rc 1 and `notFound` (measured on 1.4.1),
// and a fake that answered them silently would let a command that should have
// passed the service by look as though it worked.
func there(name string) bool {
	for _, m := range strings.Fields(os.Getenv("INSPECT_ABSENT")) {
		if name == m {
			return false
		}
	}
	dir := os.Getenv("STATE_DIR")
	if dir == "" {
		return true
	}
	if _, err := os.Stat(gonePath(dir, name)); err == nil {
		return false
	}
	return true
}
