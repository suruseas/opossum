package orchestrator

import "testing"

// disownLast takes a container off the rollback list only when it is the
// list's last entry — the one the caller just appended. A list whose last
// entry is some other container is left as it is (that would be someone
// else's entry to drop), while the service is still marked as not created.
func TestDisownLastPopsOnlyTheMatchingLastEntry(t *testing.T) {
	t.Run("the last entry is this container: popped", func(t *testing.T) {
		started, created := disownLast([]string{"a.p.opossum", "b.p.opossum"}, map[string]bool{"a": true, "b": true}, "b.p.opossum", "b")
		if len(started) != 1 || started[0] != "a.p.opossum" {
			t.Errorf("want [a.p.opossum], got %v", started)
		}
		if created["b"] || !created["a"] {
			t.Errorf("b is no longer this up's, a still is; got %v", created)
		}
	})
	t.Run("the last entry is another container: the list is left alone", func(t *testing.T) {
		started, created := disownLast([]string{"a.p.opossum", "b.p.opossum"}, map[string]bool{"a": true, "b": true}, "a.p.opossum", "a")
		if len(started) != 2 {
			t.Errorf("b's entry is not a's to drop; want the list unchanged, got %v", started)
		}
		if created["a"] {
			t.Errorf("a is still marked as created: %v", created)
		}
	})
	t.Run("an empty list stays empty", func(t *testing.T) {
		started, _ := disownLast(nil, map[string]bool{}, "a.p.opossum", "a")
		if len(started) != 0 {
			t.Errorf("got %v", started)
		}
	})
}
