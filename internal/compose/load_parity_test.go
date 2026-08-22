package compose

import (
	"testing"

	"github.com/suruseas/opossum/internal/repohygiene"
)

// Every compose file name opossum reads is one the repository gate refuses at the
// top of the tree.
//
// One of these arrived there for real — written by hand while measuring a message,
// swept in by `git add -A` — and `opossum up` in a fresh clone reads exactly these
// names, so a tracked one is not a stray file but a live one.
//
// The gate keeps its own copy of the list, because a package about what may be
// tracked has no business depending on the compose reader. This is the test that
// makes the copy safe, and it lives here rather than there because two of the
// three lists are unexported: from the gate's own package only four of the ten
// names are visible, which is how a parity test can look complete and cover less
// than half.
func TestEveryNameOpossumReadsIsRefusedAtTheTop(t *testing.T) {
	var names []string
	names = append(names, DefaultFileNames...)
	names = append(names, overrideFileNames...)
	names = append(names, opossumOverlayFileNames...)
	if len(names) < 10 {
		t.Fatalf("only %d names were gathered; the lists this reads have moved", len(names))
	}
	for _, name := range names {
		if repohygiene.Offense(name, 40, []byte("services:\n")) == "" {
			t.Errorf("opossum reads %q at the top of a directory, but the gate would let it be tracked", name)
		}
	}
}
