package repohygiene_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// The documentation site is published with a release, not ahead of it: the
// workflow runs on a version tag, as release.yml does, and by hand. A push to
// main used to publish it, so the site described behaviour no released version
// had. Both triggers are pinned, and nothing else may join them.
func TestTheSiteIsPublishedWithARelease(t *testing.T) {
	read := func(name string) map[string]any {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		var top struct {
			On map[string]any `yaml:"on"`
		}
		if err := yaml.Unmarshal(b, &top); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return top.On
	}
	pages, release := read("pages.yml"), read("release.yml")

	allowed(t, "pages.yml's triggers", pages, "push", "workflow_dispatch")
	if _, ok := pages["workflow_dispatch"]; !ok {
		t.Error("pages.yml has no workflow_dispatch trigger; a site published wrongly has no way to be published again without a new tag")
	}
	push, ok := pages["push"].(map[string]any)
	if !ok {
		t.Fatalf("pages.yml has no push trigger: %v", pages)
	}
	allowed(t, "pages.yml's push trigger", push, "tags")
	if got, want := fmt.Sprint(push["tags"]), fmt.Sprint(release["push"].(map[string]any)["tags"]); got != want {
		t.Errorf("pages.yml runs on tags %s and release.yml on %s; the site is published by the push that publishes the release", got, want)
	}
	if got := fmt.Sprint(push["tags"]); got != "[v*]" {
		t.Errorf("pages.yml runs on tags %s; this knows [v*], the version tags", got)
	}
}
