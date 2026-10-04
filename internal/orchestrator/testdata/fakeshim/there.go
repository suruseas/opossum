package main

import (
	"os"
	"strings"
)

// there says whether a container of this name is there as far as this fake is
// concerned: one named in $INSPECT_ABSENT was never made, one the `delete`
// case marked gone is no longer there, and with $INSPECT_STRICT one nothing here ran is not. The real CLI answers `start` and `logs`
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
	// $INSPECT_STRICT: a name nothing here ran is not a container the runtime has, for `start`,
	// `logs`, `exec`, `kill` and `stats` as for `inspect` (#1551).
	if os.Getenv("INSPECT_STRICT") != "" {
		if _, err := os.Stat(createdPath(dir, name)); err != nil {
			return false
		}
	}
	return true
}

// execTarget is the container an `exec` is for: the first argument after the verb that is not a
// flag (`exec -i -t NAME cmd` — the runtime puts -i and -t before the name). "" when there is none.
// A flag that takes a value (`-u x`) is not read: the runtime does not make one, and `x` would be taken for the name.
func execTarget(args []string) string {
	for _, a := range args[1:] {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}
