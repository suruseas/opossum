package orchestrator_test

import "testing"

// TestFakeShimIsStrictByDefault guards the default fakeShim hands out (#1551): a name nothing ran is
// not a container the runtime has, so a test that forgets to make the container it stands on fails
// at once instead of passing on one that was never there. strictContainers sets the same knob again,
// which is why no other test notices the default being dropped.
func TestFakeShimIsStrictByDefault(t *testing.T) {
	t.Run("a name nothing ran is not there, and the answer is not Unknown", func(t *testing.T) {
		rt, _ := fakeShim(t)
		info := rt.Inspect("web.demo.test")
		if info.Exists || info.Unknown {
			t.Fatalf("Inspect of a name nothing ran = %+v, want not found (Exists false, Unknown false)", info)
		}
	})
	t.Run("a name that was run is there (the knob is not just refusing everything)", func(t *testing.T) {
		rt, _ := fakeShim(t)
		strictContainers(t, rt, "web.demo.test")
		if info := rt.Inspect("web.demo.test"); !info.Exists {
			t.Fatalf("Inspect of a container that was run = %+v, want Exists", info)
		}
	})
}
