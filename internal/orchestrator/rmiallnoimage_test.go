package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A service the compose files as merged give neither an image nor a build (take-down reads such files: an earlier opossum, which did not read a later file's `!override` or `!reset`, started the
// service from the image an earlier file named) has no name `down --rmi all` can remove: it says so, for that service alone, and removes the images of the others (#1953). A service that builds
// (with or without an `image:`) has a name, and is not told it has none. `destroy` says nothing of this.
func TestDownRmiAllSaysWhenAServiceNamesNoImage(t *testing.T) {
	for _, tc := range []struct {
		name string
		rmi  string
		want bool
	}{
		{"all", "all", true},
		{"local", "local", false},
		{"none", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			var out bytes.Buffer
			p := project("demo", map[string]*compose.Service{
				"aaa":   {},
				"bare":  {},
				"named": {Image: "alpine:3.20"},
				"built": {Build: &compose.Build{Context: "."}},
				"both":  {Build: &compose.Build{Context: "."}, Image: "demo/both:1"},
			})
			if err := orchestrator.New(p, rt, "opossum", &out).Down(false, tc.rmi, false); err != nil {
				t.Fatal(err)
			}
			for _, bare := range []string{"aaa", "bare"} {
				said := strings.Count(out.String(), "Service "+bare+" names no image and no build in the compose files as merged") == 1
				if said != tc.want {
					t.Errorf("%s: said once = %v, want %v:\n%s", bare, said, tc.want, out.String())
				}
			}
			for _, has := range []string{"named", "built", "both"} {
				if strings.Contains(out.String(), "Service "+has+" names no image") {
					t.Errorf("%s: a service that names an image or builds one is not told it has none:\n%s", has, out.String())
				}
			}
			deletes := strings.Join(linesWith(log(), "image delete "), "\n")
			// The image a service names is removed by `--rmi all` and by nothing else; the images that were built are removed by `local` as well.
			if removed := strings.Contains(deletes, "alpine:3.20"); removed != (tc.rmi == "all") {
				t.Errorf("--rmi %q: the named image removed = %v:\n%s", tc.rmi, removed, deletes)
			}
			if tc.rmi == "" && deletes != "" {
				t.Errorf("no --rmi removed %v", deletes)
			}
		})
	}
}
