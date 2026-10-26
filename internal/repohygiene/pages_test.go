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
// had. Both triggers are pinned, and nothing else may join them; and each job's
// condition is the repository and nothing else, and no step has one, since a
// condition on the ref would skip a tag's run and leave the site silently
// unpublished.
func TestTheSiteIsPublishedWithARelease(t *testing.T) {
	type workflow struct {
		On   map[string]any            `yaml:"on"`
		Jobs map[string]map[string]any `yaml:"jobs"`
	}
	read := func(name string) workflow {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		var w workflow
		if err := yaml.Unmarshal(b, &w); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return w
	}
	site, rel := read("pages.yml"), read("release.yml")
	pages := site.On
	releasePush, _ := rel.On["push"].(map[string]any)

	allowed(t, "pages.yml's triggers", pages, "push", "workflow_dispatch")
	if _, ok := pages["workflow_dispatch"]; !ok {
		t.Error("pages.yml has no workflow_dispatch trigger; a site published wrongly has no way to be published again without a new tag")
	}
	push, ok := pages["push"].(map[string]any)
	if !ok {
		t.Fatalf("pages.yml has no push trigger: %v", pages)
	}
	allowed(t, "pages.yml's push trigger", push, "tags")
	if got, want := fmt.Sprint(push["tags"]), fmt.Sprint(releasePush["tags"]); got != want {
		t.Errorf("pages.yml runs on tags %s and release.yml on %s; the site is published by the push that publishes the release", got, want)
	}
	if got := fmt.Sprint(push["tags"]); got != "[v*]" {
		t.Errorf("pages.yml runs on tags %s; this knows [v*], the version tags", got)
	}

	if len(site.Jobs) == 0 {
		t.Fatal("pages.yml has no jobs")
	}
	const onlyHere = "github.repository == 'suruseas/opossum'"
	for name, job := range site.Jobs {
		if got := fmt.Sprint(job["if"]); got != onlyHere {
			t.Errorf("pages.yml's %s job runs if %q; this knows %q and nothing else — a condition on the ref skips a tag's run, and the site is not published without a word", name, got, onlyHere)
		}
		// One level down, the same: a skipped step leaves the job green and the
		// site unpublished. None of the steps has a condition, and none may.
		steps, _ := job["steps"].([]any)
		for i, st := range steps {
			m, _ := st.(map[string]any)
			if c, has := m["if"]; has {
				t.Errorf("pages.yml's %s job, step %d (%v), runs if %v; a step here runs on every run of its job, or a tag's run can skip it with every check green", name, i, m["uses"], c)
			}
		}
	}
}
