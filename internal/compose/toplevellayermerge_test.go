package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The declarations a merge key brings into the top-level `secrets`, `configs`, `networks` and `volumes` themselves (`volumes: {<<: *d}`, `<<: {s: {…}}`, a list of aliases, a merge
// in a merge) have their keys asked as those written there are, and so do the declarations of the top-level `models`: docker compose refuses a key it does not take, reads a key
// it takes and an `x-` note (measured, v5.5.1, `config -q`, #1996).
func TestTheDeclarationsAMergeKeyBringsIntoATopLevelSectionAreAskedTheirKeys(t *testing.T) {
	known := map[string]string{"secrets": "file: ./f.txt", "configs": "file: ./f.txt", "networks": "driver: bridge", "volumes": "driver: local", "models": "model: ai/x"}
	uses := map[string]string{"secrets": "    secrets: [s]\n", "configs": "    configs: [s]\n", "networks": "    networks: [s]\n", "volumes": "    volumes: [s:/v]\n", "models": "    models: [s]\n"}
	for _, kind := range []string{"secrets", "configs", "networks", "volumes", "models"} {
		kn := known[kind]
		for _, tc := range []struct {
			name, body string
			refused    bool
		}{
			{"an unknown key written in the declaration", kind + ":\n  s: {" + kn + ", zzz: 1}\n", true},
			{"a layer merged in place, with an unknown key", kind + ":\n  <<: {s: {" + kn + ", zzz: 1}}\n", true},
			{"a layer merged in place, with keys it takes", kind + ":\n  <<: {s: {" + kn + "}}\n", false},
			{"a layer merged through an alias, with an unknown key", "x-d: &d {s: {" + kn + ", zzz: 1}}\n" + kind + ":\n  <<: *d\n", true},
			{"a layer merged through an alias, with keys it takes", "x-d: &d {s: {" + kn + "}}\n" + kind + ":\n  <<: *d\n", false},
			{"a layer merged through a list of aliases", "x-d: &d {s: {" + kn + ", zzz: 1}}\n" + kind + ":\n  <<: [*d]\n", true},
			{"a layer merged through a merge in a merge", "x-d: &d {s: {" + kn + ", zzz: 1}}\nx-e: &e {<<: *d}\n" + kind + ":\n  <<: *e\n", true},
			{"a layer merged beside a declaration of its own", "x-d: &d {t: {" + kn + ", zzz: 1}}\n" + kind + ":\n  s: {" + kn + "}\n  <<: *d\n", true},
			{"a note written beside the declarations is a declaration to docker compose too", kind + ":\n  x-n: 1\n  s: {" + kn + "}\n", true},
			{"a note merged into the layer is a declaration, which docker compose refuses", kind + ":\n  <<: {x-n: 1}\n  s: {" + kn + "}\n", true},
			{"a declaration with a note of its own", kind + ":\n  s: {" + kn + ", x-n: 1}\n", false},
			{"an unknown key in a declaration the layer's own declaration stands over", "x-d: &d {s: {" + kn + ", zzz: 1}}\n" + kind + ":\n  s: {" + kn + "}\n  <<: *d\n", false},
			{"an unknown key in a declaration an earlier source of a list stands over", "x-g: &g {s: {" + kn + "}}\nx-d: &d {s: {" + kn + ", zzz: 1}}\n" + kind + ":\n  <<: [*g, *d]\n", false},
			{"an unknown key in the earlier source of a list", "x-g: &g {s: {" + kn + ", zzz: 1}}\nx-d: &d {s: {" + kn + "}}\n" + kind + ":\n  <<: [*g, *d]\n", true},
			{"an unknown key in a later source of a list, under another name", "x-g: &g {s: {" + kn + "}}\nx-d: &d {t: {" + kn + ", zzz: 1}}\n" + kind + ":\n  <<: [*g, *d]\n", true},
			{"an unknown key in a declaration a merge in a merge stands over", "x-d: &d {s: {" + kn + ", zzz: 1}}\nx-e: &e {s: {" + kn + "}, <<: *d}\n" + kind + ":\n  <<: *e\n", false},
			{"a declaration that merges an unknown key", kind + ":\n  s: {" + kn + ", <<: {zzz: 1}}\n", true},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
				body := "services:\n  w:\n    image: wi\n" + uses[kind] + "\n" + tc.body
				if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
				want := tc.refused
				// Known, as it was: docker compose refuses a note that stands as a declaration of the top-level `models` (`models.x-n must be a mapping`), and it is read here.
				if kind == "models" && strings.Contains(tc.name, "a declaration to docker compose too") || kind == "models" && strings.Contains(tc.name, "is a declaration, which docker compose refuses") {
					want = false
				}
				if _, err := LoadFiles([]string{filepath.Join(dir, "compose.yaml")}, nil); (err != nil) != want {
					t.Errorf("refused = %v (%v), want %v", err != nil, err, want)
				}
			})
		}
	}
}
