package orchestrator

import "strings"

// lineStarting returns the line of s that begins with prefix, and how many there
// were. The count comes back rather than being folded into an empty string: a
// message that lost the line, one that grew a second, and one whose wording
// changed are three different faults, and a check that answers "" to all three
// makes the reader guess which.
//
// It lives here rather than beside its first caller, because it has two.
func lineStarting(s, prefix string) (string, int) {
	found, n := "", 0
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, prefix) {
			found, n = line, n+1
		}
	}
	return found, n
}
