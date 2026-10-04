package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestFakeShimIsStrictByDefault guards the default fakeShim hands out (#1551): a name nothing ran is
// not a container the runtime has, so a test that forgets to make the container it stands on fails
// at once instead of passing on one that was never there. strictContainers sets the knob again, which
// is why no other test notices the default being dropped. The fake looks only when it has a
// STATE_DIR, so the two are guarded one each.
func TestFakeShimIsStrictByDefault(t *testing.T) {
	inspect := func(t *testing.T, name string) (string, error) {
		t.Helper()
		out, err := exec.Command(fakeShimBin, "inspect", name).CombinedOutput()
		return string(out), err
	}
	t.Run("a name nothing ran is not a container", func(t *testing.T) {
		fakeShim(t)
		out, err := inspect(t, "web.demo.opossum")
		if err == nil || !strings.Contains(out, "not found") {
			t.Fatalf("inspect of a name nothing ran = err %v, %q; want rc 1 and `not found`", err, out)
		}
	})
	t.Run("a name that was run is one (the knob is not just refusing everything)", func(t *testing.T) {
		fakeShim(t)
		strictContainers(t, "web.demo.opossum")
		if out, err := inspect(t, "web.demo.opossum"); err != nil {
			t.Fatalf("inspect of a container that was run: %v\n%s", err, out)
		}
	})
}
