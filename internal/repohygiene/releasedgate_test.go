package repohygiene_test

// The second changelog gate: a released `## [x.y.z]` section the base has,
// the head keeps, and a fragment this branch adds is not an entry a
// released section already carries. Both shapes happened once, by hand, in
// a CHANGELOG.md conflict resolution, and were caught by eye. The step's
// script is read from the workflow and run here against a stand-in git
// that serves two revisions' files, so the check measures the script CI
// runs and not a copy of it.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const releasedGateName = "released changelog sections stay released"

// readReleasedGate finds the step by name and returns its script, insisting
// on the same shape the fragment gate has: one step, run on every pull
// request, taking the two revisions from the event and nothing else.
func readReleasedGate(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := yaml.Unmarshal(b, &top); err != nil {
		t.Fatalf("reading the workflow: %v", err)
	}
	jobs, _ := top["jobs"].(map[string]any)
	job, _ := jobs["ci"].(map[string]any)
	steps, _ := job["steps"].([]any)
	var step map[string]any
	found := 0
	for _, s := range steps {
		m, _ := s.(map[string]any)
		if n, _ := m["name"].(string); n == releasedGateName {
			step = m
			found++
		}
	}
	if found != 1 {
		t.Fatalf("found %d steps named %q; this check reads exactly one", found, releasedGateName)
	}
	allowed(t, "the released-sections step", step, "name", "if", "env", "run")
	if cond, _ := step["if"].(string); cond != "${{ !cancelled() && github.event_name == 'pull_request' }}" {
		t.Fatalf("the step runs on %q; it should run on every pull request, even after an earlier failure", cond)
	}
	env, _ := step["env"].(map[string]any)
	if len(env) != 2 || env["BASE"] != "${{ github.event.pull_request.base.sha }}" || env["HEAD"] != "${{ github.event.pull_request.head.sha }}" {
		t.Fatalf("the step takes %v from the event; this check stands in for BASE and HEAD and nothing else", env)
	}
	script, _ := step["run"].(string)
	if script == "" || strings.Contains(script, "${{") {
		t.Fatal("the step runs nothing, or carries a GitHub expression this check cannot substitute")
	}
	return script
}

// runReleasedGate runs the script with a stand-in git: `show REV:path` reads
// the file from <dir>/<rev>/<path>, `diff --name-only --diff-filter=A
// BASE_SHA...HEAD_SHA` prints <dir>/added, and anything else is refused
// loudly — a quiet stand-in would turn a question the script started asking
// into "nothing to see".
func runReleasedGate(t *testing.T, script string, base, head map[string]string, added []string) (string, int) {
	t.Helper()
	dir := t.TempDir()
	for rev, files := range map[string]map[string]string{"BASE_SHA": base, "HEAD_SHA": head} {
		for name, body := range files {
			p := filepath.Join(dir, rev, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeAll(t, dir, map[string]string{
		"added": strings.Join(added, "\n") + "\n",
		"git": `#!/bin/sh
say() { echo "fake git: $*" >&2; exit 3; }
case "$1" in
  show)
    [ $# -eq 2 ] || say "show takes one REV:path, got: $*"
    rev="${2%%:*}"; path="${2#*:}"
    [ "$rev" = BASE_SHA ] || [ "$rev" = HEAD_SHA ] || say "unmodelled revision $rev"
    f="` + dir + `/$rev/$path"
    [ -f "$f" ] || { echo "fatal: path '$path' does not exist in '$rev'" >&2; exit 128; }
    cat "$f" ;;
  diff)
    [ "$2" = --name-only ] && [ "$3" = --diff-filter=A ] && [ "$4" = BASE_SHA...HEAD_SHA ] && [ $# -eq 4 ] || say "only 'diff --name-only --diff-filter=A BASE_SHA...HEAD_SHA' is modelled, got: $*"
    cat "` + dir + `/added" ;;
  *) say "only show and diff are modelled, got: $*" ;;
esac
`,
	})
	if err := os.Chmod(filepath.Join(dir, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "--noprofile", "--norc", "-e", "-s")
	cmd.Dir = repoRoot(t)
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = []string{"PATH=" + dir + string(os.PathListSeparator) + "/usr/bin:/bin", "BASE=BASE_SHA", "HEAD=HEAD_SHA"}
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running the gate: %v", err)
	}
	return string(out), code
}

func TestReleasedChangelogSectionsStayReleased(t *testing.T) {
	root := repoRoot(t)
	script := readReleasedGate(t, root)
	released := "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- Something new.\n\n## [0.24.5] - 2026-09-01\n\n### Fixed\n\n- The old entry that shipped.\n\n## [0.24.4] - 2026-08-30\n\n- Older.\n"
	for _, tc := range []struct {
		name       string
		base, head string
		added      []string
		fragments  map[string]string
		wantPass   bool
		wantSay    string
	}{
		{"nothing moved", released, released, nil, nil, true, "Released sections intact"},
		{"a released section is gone", released, strings.Replace(released, "## [0.24.5] - 2026-09-01\n\n### Fixed\n\n- The old entry that shipped.\n\n", "", 1), nil, nil, false, "## [0.24.5] - 2026-09-01"},
		{"a new fragment", released, released, []string{"changelog.d/999-new.fixed.md"}, map[string]string{"changelog.d/999-new.fixed.md": "- A brand new entry.\n"}, true, ""},
		{"a consumed fragment came back", released, released, []string{"changelog.d/510-old.fixed.md"}, map[string]string{"changelog.d/510-old.fixed.md": "- The old entry that shipped.\n"}, false, "changelog.d/510-old.fixed.md"},
		// An entry the base only has under Unreleased is not consumed: the
		// same branch may have added it there by regenerating.
		{"a fragment whose entry is only unreleased in the base", released, released, []string{"changelog.d/998-unrel.fixed.md"}, map[string]string{"changelog.d/998-unrel.fixed.md": "- Something new.\n"}, true, ""},
		{"the fragments' README is not a fragment", released, released, []string{"changelog.d/README.md"}, map[string]string{"changelog.d/README.md": "- The old entry that shipped.\n"}, true, ""},
		{"a section the head adds is fine", released, strings.Replace(released, "## [0.24.5]", "## [0.24.6] - 2026-09-07\n\n- Newer.\n\n## [0.24.5]", 1), nil, nil, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			head := map[string]string{"CHANGELOG.md": tc.head}
			for f, body := range tc.fragments {
				head[f] = body
			}
			out, code := runReleasedGate(t, script, map[string]string{"CHANGELOG.md": tc.base}, head, tc.added)
			if tc.wantPass && code != 0 {
				t.Errorf("should pass, exit %d:\n%s", code, out)
			}
			if !tc.wantPass && code == 0 {
				t.Errorf("should fail:\n%s", out)
			}
			if tc.wantSay != "" && !strings.Contains(out, tc.wantSay) {
				t.Errorf("should say %q:\n%s", tc.wantSay, out)
			}
		})
	}
}
