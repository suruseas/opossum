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
	"fmt"
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
	releasedLog   = "# Changelog\n\n## [Unreleased]\n\n[pending] note, not a link.\n\n## [0.24.6] - 2026-09-07\n\n### Fixed\n\n- Pending entry.\n\n" + olderSections
	olderSections = "## [0.24.5] - 2026-09-01\n\n### Fixed\n\n- The old entry that shipped.\n\n## [0.24.4] - 2026-08-30\n\n- Older.\n\n[bracketed] note, not a link.\n\n[Unreleased]: https://example.invalid/compare/v0.24.6...HEAD\n[0.24.5]: https://example.invalid/v0.24.5\n"
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
	// Blank lines added at the end of a released section: the text as the
	// reader sees it is the same, the file is not.
	r.branch(t, "trailingblank", "main", map[string]string{"CHANGELOG.md": strings.Replace(releasedLog, "\n## [0.24.5]", "\n\n\n## [0.24.5]", 1)})
	// A released heading a second time, far from the first, with other text.
	r.branch(t, "twice", "main", map[string]string{"CHANGELOG.md": strings.Replace(releasedLog, "## [0.24.4]", "## [0.24.6] - 2026-09-07\n\n- Not what shipped.\n\n## [0.24.4]", 1)})
	// The link reference definitions at the end of the file are not the last
	// section's text: a release adds one.
	r.branch(t, "linkdef", "main", map[string]string{"CHANGELOG.md": releasedLog + "[0.24.6]: https://example.invalid/v0.24.6\n"})
	// The base has a fragment; the head removes it and adds a consumed one of
	// the same text under another name, which git would report as a rename.
	r.branch(t, "renamebase", "main", map[string]string{"changelog.d/999-new.fixed.md": "- Pending entry.\n"})
	r.branch(t, "renameback", "renamebase", map[string]string{"changelog.d/999-new.fixed.md": "", "changelog.d/700-pending.fixed.md": "- Pending entry.\n"})
	// Fragment names with a space, with a glob character and with a character
	// git quotes are read as written: `[7]00-glob…` is its own file, not
	// `700-glob…`, which is there too and carries an innocent entry.
	r.branch(t, "spacename", "main", map[string]string{"changelog.d/998 spaced.fixed.md": "- A spaced entry.\n"})
	r.branch(t, "spaceback", "main", map[string]string{"changelog.d/998 spaced.fixed.md": "- Pending entry.\n"})
	r.branch(t, "globname", "main", map[string]string{"changelog.d/700-glob.fixed.md": "- A new one.\n", "changelog.d/[7]00-glob.fixed.md": "- Pending entry.\n"})
	r.branch(t, "quotedname", "main", map[string]string{"changelog.d/999-über.fixed.md": "- Pending entry.\n"})
	// The link reference lines of released versions are the record too.
	r.branch(t, "linkreword", "main", map[string]string{"CHANGELOG.md": strings.Replace(releasedLog, "[0.24.5]: https://example.invalid/v0.24.5", "[0.24.5]: https://example.invalid/v9", 1)})
	r.branch(t, "linkdrop", "main", map[string]string{"CHANGELOG.md": strings.Replace(releasedLog, "[0.24.5]: https://example.invalid/v0.24.5\n", "", 1)})
	r.branch(t, "linkappend", "main", map[string]string{"CHANGELOG.md": strings.Replace(releasedLog, "[0.24.5]: https://example.invalid/v0.24.5\n", "[0.24.5]: https://example.invalid/v0.24.5-evil\n", 1)})
	// `[Unreleased]:` is rewritten by every release: not a released line.
	r.branch(t, "unrellink", "main", map[string]string{"CHANGELOG.md": strings.Replace(releasedLog, "compare/v0.24.6...HEAD", "compare/v0.24.7...HEAD", 1)})
	// … but the line has to stay: a release rewrites it only when it is there.
	r.branch(t, "unrellinkdrop", "main", map[string]string{"CHANGELOG.md": strings.Replace(releasedLog, "[Unreleased]: https://example.invalid/compare/v0.24.6...HEAD\n", "", 1)})
	// A section appended after the oldest release, in a file with no link
	// block: the oldest section's text is what it was.
	r.branch(t, "nolinks", "main", map[string]string{"CHANGELOG.md": strings.Replace(strings.Replace(releasedLog, "[Unreleased]: https://example.invalid/compare/v0.24.6...HEAD\n", "", 1), "[0.24.5]: https://example.invalid/v0.24.5\n", "", 1)})
	r.branch(t, "appendsection", "nolinks", map[string]string{"CHANGELOG.md": strings.Replace(strings.Replace(releasedLog, "[Unreleased]: https://example.invalid/compare/v0.24.6...HEAD\n", "", 1), "[0.24.5]: https://example.invalid/v0.24.5\n", "## [0.24.3] - 2026-08-20\n\n- Oldest.\n", 1)})
	// A bracketed body line under Unreleased comes and goes freely.
	r.branch(t, "unrelbracket", "main", map[string]string{"CHANGELOG.md": strings.Replace(releasedLog, "[pending] note, not a link.\n\n", "", 1)})
	// A base whose file does not end in a newline, and a head that adds a link
	// reference line after the last one.
	r.branch(t, "nonewline", "main", map[string]string{"CHANGELOG.md": strings.TrimSuffix(releasedLog, "\n")})
	r.branch(t, "nonewlinelink", "nonewline", map[string]string{"CHANGELOG.md": releasedLog + "[0.24.6]: https://example.invalid/v0.24.6\n"})
	// Not fragments: a file under a subdirectory, and one that is not `.md`.
	r.branch(t, "subdirfile", "main", map[string]string{"changelog.d/sub/700-pending.fixed.md": "- Pending entry.\n"})
	r.branch(t, "notmd", "main", map[string]string{"changelog.d/700-pending.fixed.txt": "- Pending entry.\n"})
	// Blank lines at the very end of the file belong to the last section.
	r.branch(t, "fileendblank", "main", map[string]string{"CHANGELOG.md": releasedLog + "\n\n"})
	// A body line that quotes a heading is not a second heading; a body line
	// starting with `[` is text, not a link reference.
	r.branch(t, "headingquote", "main", map[string]string{"CHANGELOG.md": strings.Replace(releasedLog, "## [Unreleased]\n", "## [Unreleased]\n\n- See ## [0.24.6] - 2026-09-07 below.\n", 1)})
	r.branch(t, "bracketreword", "main", map[string]string{"CHANGELOG.md": strings.Replace(releasedLog, "[bracketed] note, not a link.", "[bracketed] note, reworded.", 1)})
	// A base that already has a released heading twice: not this PR's doing.
	r.branch(t, "basedup", "main", map[string]string{"CHANGELOG.md": strings.Replace(releasedLog, "## [0.24.4]", "## [0.24.6] - 2026-09-07\n\n- Not what shipped.\n\n## [0.24.4]", 1)})
	r.branch(t, "basedupcode", "basedup", map[string]string{"code.go": "package main\n"})
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
// everything else to the real git. When version is brokenShowBase, `git show`
// of the base's CHANGELOG.md fails the way an unreadable object does; when it
// is brokenShowMerged, `git show` of any other CHANGELOG.md (the merge's)
// fails that way. Everything else goes to the real git.
const (
	brokenShowBase   = "show of the base's CHANGELOG.md fails"
	brokenShowMerged = "show of the merge's CHANGELOG.md fails"
	brokenDiff       = "diff fails"
	brokenShowFrag   = "show of a fragment fails"
	// failing names a command other than git (`failing: grep`) that fails
	// with exit 2 whenever it runs, shadowing the real one.
	failing = "failing: "
)

func runReleasedGate(t *testing.T, r *gitRepo, script, base, head, version string) (string, int) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	shim := "#!/bin/sh\nexec " + realGit + " \"$@\"\n"
	fail := "echo 'fatal: unable to read object' >&2; exit 128"
	if strings.HasPrefix(version, failing) {
		// `failing: <command>|<arg>|<arg>…` fails only the calls whose leading
		// arguments are those, as written (`awk|-v` is the section reader,
		// `grep|-E|^## \[[0-9]` the heading reader, `grep|-q|-F` the fragment
		// match in an `if`), passing the rest to the real command.
		parts := strings.Split(strings.TrimPrefix(version, failing), "|")
		name, args := parts[0], parts[1:]
		body := "#!/bin/sh\necho '" + name + ": simulated failure' >&2; exit 2\n"
		if len(args) > 0 {
			real, err := exec.LookPath(name)
			if err != nil {
				t.Fatal(err)
			}
			cond := ""
			for i, a := range args {
				cond += fmt.Sprintf(" [ \"$%d\" = '%s' ] &&", i+1, strings.ReplaceAll(a, "'", "'\\''"))
			}
			body = "#!/bin/sh\nif" + cond + " :; then echo '" + name + ": simulated failure' >&2; exit 2; fi\nexec " + real + " \"$@\"\n"
		}
		writeAll(t, bin, map[string]string{name: body})
		if err := os.Chmod(filepath.Join(bin, name), 0o755); err != nil {
			t.Fatal(err)
		}
	} else if version == brokenShowFrag {
		shim = "#!/bin/sh\nif [ \"$1\" = show ]; then case \"$2\" in *:changelog.d/*) " + fail + " ;; esac; fi\nexec " + realGit + " \"$@\"\n"
	} else if version == brokenShowBase {
		shim = "#!/bin/sh\nif [ \"$1\" = show ] && [ \"$2\" = '" + base + ":CHANGELOG.md' ]; then " + fail + "; fi\nexec " + realGit + " \"$@\"\n"
	} else if version == brokenDiff {
		shim = "#!/bin/sh\nif [ \"$1\" = diff ]; then " + fail + "; fi\nexec " + realGit + " \"$@\"\n"
	} else if version == brokenShowMerged {
		shim = "#!/bin/sh\nif [ \"$1\" = show ] && [ \"$2\" != '" + base + ":CHANGELOG.md' ]; then case \"$2\" in *:CHANGELOG.md) " + fail + " ;; esac; fi\nexec " + realGit + " \"$@\"\n"
	} else if version != "" {
		shim = "#!/bin/sh\n[ \"$1\" = version ] && { echo '" + version + "'; exit 0; }\nexec " + realGit + " \"$@\"\n"
	}
	// mktemp records what it was asked for, then does it.
	realMktemp, err := exec.LookPath("mktemp")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"git": shim}
	if version != failing+"mktemp" {
		files["mktemp"] = "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$HOME/mktemp.args\"\nexec " + realMktemp + " \"$@\"\n"
	}
	writeAll(t, bin, files)
	if err := os.Chmod(filepath.Join(bin, "mktemp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "--noprofile", "--norc", "-e", "-s")
	cmd.Dir = r.dir
	cmd.Stdin = strings.NewReader(script)
	// TMPDIR is this test's own: the step's temporary file has to be gone
	// afterwards, whichever way the step ended.
	tmp := t.TempDir()
	home := t.TempDir()
	cmd.Env = append(gitEnv(home), "BASE="+base, "HEAD="+head, "TMPDIR="+tmp)
	cmd.Env[0] = "PATH=" + bin + string(os.PathListSeparator) + "/usr/bin:/bin"
	out, err := cmd.CombinedOutput()
	left, readErr := os.ReadDir(tmp)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(left) != 0 {
		t.Errorf("the step left %d temporary file(s) behind: %v", len(left), left)
	}
	// The temporary file is asked for under TMPDIR by name (macOS's mktemp
	// does not read TMPDIR on its own), or the check above sees nothing.
	if b, err := os.ReadFile(filepath.Join(home, "mktemp.args")); err == nil && !strings.HasPrefix(strings.TrimSpace(string(b)), tmp+"/") {
		t.Errorf("mktemp was asked for %q, not for a file under TMPDIR %q", strings.TrimSpace(string(b)), tmp)
	}
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
		{"blank lines added to a released section", "main", "trailingblank", "", false, []string{"changes released", "  ## [0.24.6] - 2026-09-07"}, nil},
		{"a released heading a second time", "main", "twice", "", false, []string{"more than once", "  ## [0.24.6] - 2026-09-07 (2 times)"}, []string{"changes released"}},
		{"a link reference definition the head adds", "main", "linkdef", "", true, []string{"Released sections intact"}, nil},
		{"a consumed fragment back under a name git reads as a rename", "renamebase", "renameback", "", false, []string{"changelog.d/700-pending.fixed.md", "- Pending entry."}, nil},
		{"a fragment name with a space", "main", "spacename", "", true, []string{"Released sections intact"}, nil},
		{"a consumed fragment back under a name with a glob character", "main", "globname", "", false, []string{"changelog.d/[7]00-glob.fixed.md", "- Pending entry."}, nil},
		{"a consumed fragment back under a name with a space", "main", "spaceback", "", false, []string{"changelog.d/998 spaced.fixed.md", "- Pending entry."}, nil},
		{"a consumed fragment back under a name git quotes", "main", "quotedname", "", false, []string{"changelog.d/999-über.fixed.md", "- Pending entry."}, nil},
		{"a released version's link reference reworded", "main", "linkreword", "", false, []string{"link reference lines", "  [0.24.5]: https://example.invalid/v0.24.5"}, []string{"changes released"}},
		{"a released version's link reference dropped", "main", "linkdrop", "", false, []string{"link reference lines", "  [0.24.5]: https://example.invalid/v0.24.5"}, nil},
		{"a released version's link reference with text appended", "main", "linkappend", "", false, []string{"link reference lines", "  [0.24.5]: https://example.invalid/v0.24.5\n"}, nil},
		{"the Unreleased link reference rewritten", "main", "unrellink", "", true, []string{"Released sections intact"}, []string{"link reference lines"}},
		{"the Unreleased link reference dropped", "main", "unrellinkdrop", "", false, []string{"link reference lines", "  [Unreleased]: (the line itself"}, []string{"changes released"}},
		{"a section appended after the oldest release", "nolinks", "appendsection", "", true, []string{"Released sections intact"}, nil},
		{"a bracketed line under Unreleased removed", "main", "unrelbracket", "", true, []string{"Released sections intact"}, []string{"link reference lines"}},
		{"a base without a final newline, a link reference added", "nonewline", "nonewlinelink", "", true, []string{"Released sections intact"}, []string{"v0.24.5## x", "v0.24.5x"}},
		{"a file under a subdirectory is not a fragment", "main", "subdirfile", "", true, []string{"Released sections intact"}, nil},
		{"a file that is not .md is not a fragment", "main", "notmd", "", true, []string{"Released sections intact"}, nil},
		{"blank lines added at the end of the file", "main", "fileendblank", "", false, []string{"changes released", "  ## [0.24.4] - 2026-08-30"}, nil},
		{"a body line quoting a released heading is not a second heading", "main", "headingquote", "", true, []string{"Released sections intact"}, []string{"more than once"}},
		{"a bracketed body line of a released section reworded", "main", "bracketreword", "", false, []string{"changes released", "  ## [0.24.4] - 2026-08-30"}, nil},
		{"a base that already has a heading twice is not this PR's doing", "basedup", "basedupcode", "", true, []string{"The base has ## [0.24.6] - 2026-09-07 more than once (2 times)", "Released sections intact"}, []string{"Merging this PR leaves", "not compared.\nThe base has"}},
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
		// A CHANGELOG.md git cannot show is not one whose sections are fine:
		// the step ends on the failure, saying nothing about being intact.
		{"a base CHANGELOG.md git cannot show", "main", "code", brokenShowBase, false, []string{"fatal: unable to read object"}, []string{"Released sections intact"}},
		// Not read as "every section dropped": the step ends on the failure.
		{"a merged CHANGELOG.md git cannot show", "main", "code", brokenShowMerged, false, []string{"fatal: unable to read object"}, []string{"Released sections intact", "drops released"}},
		// The names of the added files come from git too: a `git diff` that
		// fails is not "no fragment came back".
		{"a git diff that fails", "main", "code", brokenDiff, false, []string{"fatal: unable to read object"}, []string{"Released sections intact"}},
		// A grep on the left of a pipe, or one in an `if`, that fails: neither
		// reads as "nothing there" either. Each shim fails one call only, so
		// each row reaches the call it names.
		{"the grep reading headings that fails, with a section dropped", "main", "drop", failing + `grep|-E|^## \[[0-9]`, false, []string{"grep: simulated failure"}, []string{"Released sections intact", "drops released"}},
		{"the grep reading link lines that fails, with a link line dropped", "main", "linkdrop", failing + `grep|-E|^\[[^]]+\]: `, false, []string{"grep: simulated failure"}, []string{"Released sections intact", "link reference lines"}},
		{"the grep looking for the Unreleased line that fails", "main", "unrellinkdrop", failing + "grep|-q|-E", false, []string{"grep: simulated failure"}, []string{"Released sections intact", "link reference lines"}},
		{"the grep matching a fragment's entry that fails", "main", "consumedback", failing + "grep|-q|-F", false, []string{"grep: simulated failure"}, []string{"Released sections intact", "already in a released"}},
		{"the grep matching a link line that fails", "main", "code", failing + "grep|-q|-xF", false, []string{"grep: simulated failure"}, []string{"Released sections intact", "link reference lines"}},
		// Every other command the step leans on, failing: none of them reads as
		// "nothing there" — the step ends without saying the sections are intact.
		{"a git show of a fragment that fails", "main", "newfrag", brokenShowFrag, false, []string{"fatal: unable to read object"}, []string{"Released sections intact"}},
		{"a grep that fails", "main", "code", failing + "grep", false, []string{"grep: simulated failure"}, []string{"Released sections intact"}},
		{"an awk that fails", "main", "code", failing + "awk", false, []string{"awk: simulated failure"}, []string{"Released sections intact"}},
		{"the awk reading a section that fails", "main", "code", failing + "awk|-v", false, []string{"awk: simulated failure"}, []string{"Released sections intact", "drops released"}},
		{"a sed that fails", "main", "newfrag", failing + "sed", false, []string{"sed: simulated failure"}, []string{"Released sections intact"}},
		{"a mktemp that fails", "main", "code", failing + "mktemp", false, []string{"mktemp: simulated failure"}, []string{"Released sections intact"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rev := func(name string) string {
				if sha, ok := r.refs[name]; ok {
					return sha
				}
				return name
			}
			// The working directory holds the head, as the CI checkout does: a
			// name the script globbed would expand against the head's files —
			// the glob row is only a row while this checkout is here.
			if _, ok := r.refs[tc.head]; ok {
				r.git(t, "checkout", "-q", tc.head)
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
