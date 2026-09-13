package repohygiene_test

// The second changelog gate: merging a pull request keeps every released
// `## [x.y.z]` section the base has, heading and text, and brings back no
// fragment a released section already carries. Both shapes happened once, by
// hand, in a CHANGELOG.md conflict resolution, and were caught by eye. The
// step's script is read from the workflow and run here against a real git
// repository whose branches stand for the histories that matter — a branch
// cut before a release among them — so the check measures the script CI runs,
// and what git actually merges, and not a copy of either.

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

const (
	unreleasedLog = "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- Pending entry.\n\n" + olderSections
	releasedLog   = "# Changelog\n\n## [Unreleased]\n\n## [0.24.6] - 2026-09-07\n\n### Fixed\n\n- Pending entry.\n\n" + olderSections
	olderSections = "## [0.24.5] - 2026-09-01\n\n### Fixed\n\n- The old entry that shipped.\n\n## [0.24.4] - 2026-08-30\n\n- Older.\n\n[0.24.5]: https://example.invalid/v0.24.5\n"
)

// gitRepo is a throwaway repository with one branch per history the gate has
// to tell apart. `before` holds a fragment still to be released; `main` is the
// release that folded it into 0.24.6 and deleted the file. Branches cut from
// `main` are up to date; branches cut from `before` are not.
type gitRepo struct {
	dir  string
	refs map[string]string
}

func newGitRepo(t *testing.T) *gitRepo {
	t.Helper()
	r := &gitRepo{dir: t.TempDir(), refs: map[string]string{}}
	r.git(t, "init", "-q", "-b", "before")
	r.commit(t, "before", map[string]string{
		"CHANGELOG.md":                     unreleasedLog,
		"changelog.d/README.md":            "Fragments.\n",
		"changelog.d/700-pending.fixed.md": "- Pending entry.\n",
		"main.go":                          "package main\n",
	})
	r.branch(t, "main", "before", map[string]string{"CHANGELOG.md": releasedLog, "changelog.d/700-pending.fixed.md": ""})

	r.branch(t, "code", "main", map[string]string{"code.go": "package main\n"})
	r.branch(t, "drop", "main", map[string]string{"CHANGELOG.md": strings.Replace(releasedLog, "## [0.24.5] - 2026-09-01\n\n### Fixed\n\n- The old entry that shipped.\n\n", "", 1)})
	r.branch(t, "reword", "main", map[string]string{"CHANGELOG.md": strings.Replace(releasedLog, "- Older.", "- Older, reworded.", 1)})
	r.branch(t, "addsection", "main", map[string]string{"CHANGELOG.md": strings.Replace(releasedLog, "## [0.24.6]", "## [0.24.7] - 2026-09-08\n\n- Newer.\n\n## [0.24.6]", 1)})
	r.branch(t, "nolog", "main", map[string]string{"CHANGELOG.md": ""})
	r.branch(t, "newfrag", "main", map[string]string{"changelog.d/999-new.fixed.md": "- A brand new entry.\n"})
	r.branch(t, "consumedback", "main", map[string]string{"changelog.d/700-pending.fixed.md": "- Pending entry.\n"})
	// The base already carries that fragment again, and the head adds the same
	// file: merging changes nothing about it, so it is not this PR's to answer.
	r.branch(t, "basehasit", "main", map[string]string{"changelog.d/700-pending.fixed.md": "- Pending entry.\n"})
	r.branch(t, "olderback", "main", map[string]string{"changelog.d/510-old.fixed.md": "- The old entry that shipped.\n"})
	// The fragment reader skips leading blank lines, so the entry is the
	// first `- ` line, not the first line.
	r.branch(t, "blankfirst", "main", map[string]string{"changelog.d/511-blank.fixed.md": "\n- Pending entry.\n"})
	r.branch(t, "stale", "before", map[string]string{"code.go": "package main\n"})
	r.branch(t, "staleback", "before", map[string]string{"code.go": "package main\n"})
	r.merge(t, "staleback", "main", map[string]string{"changelog.d/700-pending.fixed.md": "- Pending entry.\n"})
	// An entry the base only has under Unreleased is not consumed: the same
	// branch may have added it there by regenerating.
	r.branch(t, "unrel", "before", map[string]string{"changelog.d/998-unrel.fixed.md": "- Pending entry.\n"})
	r.branch(t, "noreadme", "main", map[string]string{"changelog.d/README.md": ""})
	r.branch(t, "readmeback", "noreadme", map[string]string{"changelog.d/README.md": "- Pending entry.\n"})
	r.branch(t, "noentry", "main", map[string]string{"changelog.d/997-noentry.fixed.md": "Not an entry line.\n"})
	r.branch(t, "norelease", "main", map[string]string{"CHANGELOG.md": "# Changelog\n\n## [Unreleased]\n"})
	r.branch(t, "noreleasehead", "norelease", map[string]string{"CHANGELOG.md": "# Changelog\n\n## [Unreleased]\n\n- New.\n"})
	r.branch(t, "basemoved", "main", map[string]string{"CHANGELOG.md": strings.Replace(releasedLog, "## [Unreleased]\n", "## [Unreleased]\n\n- Base side.\n", 1)})
	r.branch(t, "conflict", "main", map[string]string{"CHANGELOG.md": strings.Replace(releasedLog, "## [Unreleased]\n", "## [Unreleased]\n\n- Head side.\n", 1)})
	return r
}

func (r *gitRepo) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = r.dir
	cmd.Env = gitEnv(r.dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes files (an empty body deletes the file) on the checked-out
// branch and records the result under that branch's name.
func (r *gitRepo) commit(t *testing.T, name string, files map[string]string) {
	t.Helper()
	for f, body := range files {
		p := filepath.Join(r.dir, filepath.FromSlash(f))
		if body == "" {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r.git(t, "add", "-A")
	r.git(t, "commit", "-q", "-m", name)
	r.refs[name] = r.git(t, "rev-parse", "HEAD")
}

func (r *gitRepo) branch(t *testing.T, name, from string, files map[string]string) {
	t.Helper()
	r.git(t, "checkout", "-q", "-b", name, from)
	r.commit(t, name, files)
}

// merge brings from into the branch name, then applies files on top: a
// conflict resolved by hand, the way a consumed fragment came back once.
func (r *gitRepo) merge(t *testing.T, name, from string, files map[string]string) {
	t.Helper()
	r.git(t, "checkout", "-q", name)
	r.git(t, "merge", "-q", "--no-edit", from)
	r.commit(t, name, files)
}

func gitEnv(home string) []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "LC_ALL=C"}
}

// runReleasedGate runs the script in the repository with BASE and HEAD set.
// When version is not empty, git answers `git version` with it and passes
// everything else to the real git.
func runReleasedGate(t *testing.T, r *gitRepo, script, base, head, version string) (string, int) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	shim := "#!/bin/sh\nexec " + realGit + " \"$@\"\n"
	if version != "" {
		shim = "#!/bin/sh\n[ \"$1\" = version ] && { echo '" + version + "'; exit 0; }\nexec " + realGit + " \"$@\"\n"
	}
	writeAll(t, bin, map[string]string{"git": shim})
	if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "--noprofile", "--norc", "-e", "-s")
	cmd.Dir = r.dir
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = append(gitEnv(t.TempDir()), "BASE="+base, "HEAD="+head)
	cmd.Env[0] = "PATH=" + bin + string(os.PathListSeparator) + "/usr/bin:/bin"
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
	script := readReleasedGate(t, repoRoot(t))
	r := newGitRepo(t)
	const missing = "0000000000000000000000000000000000000001"
	for _, tc := range []struct {
		name, base, head, version string
		wantPass                  bool
		wantSay, wantNot          []string
	}{
		{"nothing moved", "main", "code", "", true, []string{"Released sections intact"}, nil},
		{"a released section is gone", "main", "drop", "", false, []string{"drops released", "  ## [0.24.5] - 2026-09-01"}, nil},
		{"a released section's text changed", "main", "reword", "", false, []string{"changes released", "  ## [0.24.4] - 2026-08-30"}, []string{"  ## [0.24.5]", "drops released"}},
		{"CHANGELOG.md itself is gone", "main", "nolog", "", false, []string{"drops released", "  ## [0.24.6] - 2026-09-07", "  ## [0.24.4] - 2026-08-30"}, nil},
		{"a section the head adds is fine", "main", "addsection", "", true, nil, nil},
		// Cut before the release: the head has neither 0.24.6 nor the
		// deletion of the fragment the release consumed, and drops nothing.
		{"a branch cut before a release", "main", "stale", "", true, []string{"Released sections intact"}, nil},
		{"a new fragment", "main", "newfrag", "", true, nil, nil},
		{"a fragment without an entry line is not taken for a consumed one", "main", "noentry", "", true, nil, nil},
		{"a base with no released section yet", "norelease", "noreleasehead", "", true, nil, nil},
		{"a consumed fragment came back", "main", "consumedback", "", false, []string{"changelog.d/700-pending.fixed.md", "- Pending entry."}, nil},
		{"a fragment the base already carries is not this PR's", "basehasit", "consumedback", "", true, nil, nil},
		{"a fragment an older release consumed came back", "main", "olderback", "", false, []string{"changelog.d/510-old.fixed.md", "- The old entry that shipped."}, nil},
		{"a consumed fragment with a blank first line came back", "main", "blankfirst", "", false, []string{"changelog.d/511-blank.fixed.md"}, nil},
		{"a consumed fragment came back through a merge", "main", "staleback", "", false, []string{"changelog.d/700-pending.fixed.md"}, nil},
		{"a fragment whose entry is only unreleased in the base", "before", "unrel", "", true, nil, nil},
		{"the fragments' README is not a fragment", "noreadme", "readmeback", "", true, nil, nil},
		{"a merge with conflicts cannot be judged", "basemoved", "conflict", "", false, []string{"cannot be judged", "CHANGELOG.md"}, nil},
		{"a base git cannot read", missing, "code", "", false, []string{"unknown is not the same as fine"}, nil},
		{"an empty base", "", "code", "", false, []string{"unknown is not the same as fine"}, nil},
		{"a head git cannot read", "main", missing, "", false, []string{"unknown is not the same as fine"}, nil},
		{"an empty head", "main", "", "", false, []string{"unknown is not the same as fine"}, nil},
		{"git older than 2.38", "main", "code", "git version 2.37.9", false, []string{"2.38 or later"}, nil},
		{"git 1.x", "main", "code", "git version 1.99.0", false, []string{"2.38 or later"}, nil},
		{"git 2.38 itself", "main", "code", "git version 2.38.0", true, nil, nil},
		{"git 3", "main", "code", "git version 3.0.1 (Apple Git-200)", true, nil, nil},
		{"a git version that does not parse", "main", "code", "git version unknown", false, []string{"Cannot tell which git"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rev := func(name string) string {
				if sha, ok := r.refs[name]; ok {
					return sha
				}
				return name
			}
			out, code := runReleasedGate(t, r, script, rev(tc.base), rev(tc.head), tc.version)
			if tc.wantPass && code != 0 {
				t.Errorf("should pass, exit %d:\n%s", code, out)
			}
			if !tc.wantPass && code == 0 {
				t.Errorf("should fail:\n%s", out)
			}
			for _, say := range tc.wantSay {
				if !strings.Contains(out, say) {
					t.Errorf("should say %q:\n%s", say, out)
				}
			}
			for _, say := range tc.wantNot {
				if strings.Contains(out, say) {
					t.Errorf("should not say %q:\n%s", say, out)
				}
			}
		})
	}
}
