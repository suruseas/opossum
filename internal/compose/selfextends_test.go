package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A block that holds itself (`x-a: &a {name: n, x: *a}`, or one whose inner block merges the outer one) is refused as the cycle it is wherever `extends` reaches it — as an alias
// for the whole of `extends`, merged into it, or as the service it names — and the load comes back with that refusal instead of expanding the alias for ever (docker compose
// v5.5.1: rc 1, `cycle detected`; opossum died with a stack overflow, rc 2). Every row is docker compose's answer (#1909).
func TestABlockThatHoldsItselfReachedThroughExtendsIsACycle(t *testing.T) {
	selfBlocks := map[string]string{
		"an inner block that merges the outer one": "&a {name: n, x: {<<: *a}}",
		"an alias to the block it is in":           "&a {name: n, x: *a}",
		"a block three deep":                       "&a {p: {q: {r: *a}}}",
	}
	uses := map[string]string{
		"extends is the alias":                  "extends: *a",
		"extends merges it":                     "extends: {<<: *a}",
		"extends names a file and merges it":    "extends: {file: base.yaml, service: y, <<: *a}",
		"extends names the service by an alias": "extends: {file: base.yaml, service: *a}",
	}
	for sn, sb := range selfBlocks {
		for un, ub := range uses {
			t.Run(sn+"/"+un, func(t *testing.T) {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "base.yaml"), []byte("services:\n  y:\n    image: yi\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				text := "x-a: " + sb + "\nservices:\n  web:\n    image: wi\n    " + ub + "\n"
				if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
				if _, err := LoadFiles([]string{filepath.Join(dir, "compose.yaml")}, nil); err == nil || !strings.Contains(err.Error(), "cycle") {
					t.Errorf("want the block that holds itself refused as a cycle, got %v", err)
				}
			})
		}
		t.Run(sn+"/the service that is extended from holds it", func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "anchor.yaml"), []byte("x-a: "+sb+"\nservices:\n  z:\n    image: zi\n    extends: *a\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  web:\n    extends: {file: anchor.yaml, service: z}\n    image: wi\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFiles([]string{filepath.Join(dir, "compose.yaml")}, nil); err == nil || !strings.Contains(err.Error(), "cycle") {
				t.Errorf("want the block that holds itself refused as a cycle, got %v", err)
			}
		})
	}
}
