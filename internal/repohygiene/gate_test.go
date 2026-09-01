package repohygiene_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The changelog gate asks a PR that changes shipped code for a release note, and
// exempts the packages that serve this repository rather than ship. An exemption
// list is the cheapest place to make a red gate go green, so it is worth a check —
// and the check has been rebuilt six times, each of the first five abandoned after
// somebody stepped around it: quoting, `grep -Ev`, `sed`, an early exit, the shape
// of the file names it was given, a word in `env:`.
//
// Each of those was patched after the fact. This one starts from the other end.
//
// **What it runs.** The gate's step script, piped to `bash -e`. Every `$NAME` it
// spells is either one the workflow passes (three, each a GitHub expression this
// check knows by name) or one the script assigns *before* it reads — an
// assignment further down is a name the runner still fills in — and readGate
// fails on anything else. `$0` and the positional parameters are refused outright:
// they are not names, and what they hold differs between here and a runner. `PATH`
// is supplied, and points at the stand-in `git`, which models two calls —
// `diff --name-only [--diff-filter=A] BASE...HEAD` — and shouts at every other,
// with one exception: a revision range it does not recognise gets an empty diff
// rather than a shout, because that is what git answers for `A...A` or for the two
// the other way round. Every case is run twice, once with a dozen runner names
// set, and the two have to agree. The workflow is parsed as YAML: its top level
// may hold only `name`, `on` and `jobs`; `on` must carry an unnarrowed
// `pull_request`; the `ci` job may hold only `runs-on`, `env` and `steps`, its
// env pinned to GOTOOLCHAIN=local; the first step must be `actions/checkout@v7`
// with nothing but `fetch-depth: 0`; the gate step is found by its name,
// exactly once, may hold only `name`, `if`, `env` and `run`, and its one
// condition — every pull request, even after an earlier step failed — is
// pinned. The exit status is read, and so is what it printed.
//
// **The invariant.** Every package the released binary links — including any under
// a testdata/ directory, and the files chosen by the platform .goreleaser.yaml
// builds for — must be one the gate calls shipped, and a change to one of its
// files must be named in the refusal. And the gate must still pass what it should:
// a docs-only change, an added fragment, an opt-out in the body.
//
// **What it does not run, so a green run is not read as more than it is:**
//
//   - Whether GitHub agrees with any of this. The keys above are a reading of the
//     file, not a run of it; GitHub could still decide otherwise for reasons the
//     file does not contain, and branch protection lives outside it entirely.
//   - The runner's environment, beyond a dozen names. The pair of runs catches a
//     rule keyed on one of those names — asked about its presence. It catches
//     nothing about a name outside the sample (`env | grep -q '^RUNNER_TEMP='`),
//     and nothing about what a variable *contains*: the values here are made up,
//     and no set of made-up values covers the ones a real run could have.
//     `env | grep -q '^GITHUB_HEAD_REF=release/'` gets past this, and nothing
//     inside a test can close that.
//   - The machine, except by accident. This runs wherever the tests do — a
//     developer's macOS, ubuntu-latest on CI — and the script can ask directly:
//     `uname -s`, `[ -d /home/runner ]`. Both runs of the pair agree on that, so
//     nothing here is looking; a rule keyed on the host is caught only where the
//     host happens to disagree with the runner, which means a developer's machine
//     catches what CI cannot. `[ -d /home/runner ] && exit 0` is invisible to
//     both.
//   - How the script is started. GitHub writes `run:` to a file and runs
//     `bash -e <file>`; this pipes the same text to `bash -e -s`. A command that
//     reads stdin eats the rest of the script here and reads the step's stdin
//     there.
//   - The working directory is the repository root, because that is what a
//     checkout is — but it is *this* checkout, with whatever is untracked in it,
//     and in a linked worktree with `.git` as a file rather than a directory. A
//     rule that looks at the tree can still answer differently there.
//   - Any other job, and any step but the second of this one — which is not run,
//     only read.
//   - The runner otherwise: its git, its shell version, the actions it can reach.
//     Passing here is not passing there.
//   - Files the target platform does not build. `shippedFiles` asks for one
//     platform, so `clone_other.go` is not among the files tried — its package is,
//     which is what the gate's exclusions are written in terms of.
//   - Whether the gate's message is any good, beyond naming the file.
func TestTheChangelogGateAsksForANoteAboutEverythingThatShips(t *testing.T) {
	root := repoRoot(t)
	g := readGate(t, root)

	files, packages := shippedFiles(t, root)
	// The set, not a count: a floor lets whole packages fall out of the check
	// while it goes on passing, and this check's own definition of "shipped" is a
	// cheaper place to lose one than the gate's exclusion list is.
	want := []string{
		"cmd/opossum", "internal/compose", "internal/doctor",
		"internal/orchestrator", "internal/runtime", "internal/workspace",
	}
	if strings.Join(packages, " ") != strings.Join(want, " ") {
		t.Fatalf("the released binary links %v; this check was written for %v — if that changed, say so here rather than letting a package quietly stop being checked", packages, want)
	}

	// Every shipped file, one at a time: a change to it has to be called shipped.
	for _, f := range files {
		out, code := runGate(t, g, []string{f}, nil, "")
		if code == 0 {
			t.Errorf("a change to %s left the gate green: it ships, so it needs a release note\n%s", f, out)
			continue
		}
		if !strings.Contains(out, f) {
			t.Errorf("the gate refused a change to %s without naming it:\n%s", f, out)
		}
	}

	// …and the gate is not simply always red. A file that ships nothing passes.
	out, code := runGate(t, g, []string{"docs/networking.md"}, nil, "")
	if code != 0 {
		t.Errorf("a docs-only change needs no release note, but the gate refused it:\n%s", out)
	}

	// The ways out, and the things that look like a way out and are not. One file
	// at a time says nothing about a rule that reads the size of the change, or
	// one that takes a modified fragment for an added one, so the shapes are
	// spelled out here.
	ship := files[0]
	var many []string
	for i := 0; i < 30; i++ {
		many = append(many, "docs/page"+string(rune('a'+i%26))+".md")
	}
	for _, c := range []struct {
		name           string
		changed, added []string
		body           string
		wantPass       bool
	}{
		{name: "a fragment is added", changed: []string{ship, "changelog.d/999-x.fixed.md"},
			added: []string{"changelog.d/999-x.fixed.md"}, wantPass: true},
		{name: "the PR body opts out", changed: []string{ship}, body: "no note needed [skip changelog]", wantPass: true},
		// The body has to carry the token, not merely the subject.
		{name: "the body only talks about the changelog", changed: []string{ship}, body: "I updated the changelog earlier"},
		// An existing fragment edited is not a fragment added: the release note
		// for this change still does not exist.
		{name: "a fragment is edited, not added", changed: []string{ship, "changelog.d/999-x.fixed.md"}},
		// The README is not an entry, and a subdirectory is not read by the
		// assembler — an entry there is dropped from the release in silence.
		{name: "only the fragments' README is added", changed: []string{ship, "changelog.d/README.md"},
			added: []string{"changelog.d/README.md"}},
		{name: "the fragment is in a subdirectory", changed: []string{ship, "changelog.d/sub/999-x.fixed.md"},
			added: []string{"changelog.d/sub/999-x.fixed.md"}},
		// A big change is still a change: nothing about the count of files says
		// whether a release note is owed.
		{name: "one shipped file among thirty that ship nothing", changed: append([]string{ship}, many...)},
		{name: "…and the shipped one last", changed: append(append([]string{}, many...), ship)},
	} {
		out, code := runGate(t, g, c.changed, c.added, c.body)
		if c.wantPass && code != 0 {
			t.Errorf("%s: the gate should pass:\n%s", c.name, out)
		}
		if !c.wantPass && code == 0 {
			t.Errorf("%s: the release note is still owed, but the gate passed:\n%s", c.name, out)
		}
	}
}

// The folded job's two standing promises, read from the YAML. First: no step
// may quietly ignore its own failure — `continue-on-error` on a checking step
// turns its red into the job's green, and with everything in one job there is
// no second check mark to disagree. The one exception is named: the goreleaser
// validation has carried it since before the fold, deliberately, for a
// deprecation notice. Second: the stable gate runs even when the go.mod
// section failed. "One red, one green" says the two compilers disagree; "one
// red, one skipped" says nothing — the contrast is the reason the second
// section exists, and the `if` that preserves it is one word to delete.
func TestTheFoldedJobKeepsItsPromises(t *testing.T) {
	root := repoRoot(t)
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
	if len(steps) == 0 {
		t.Fatal("no `ci` job steps; the promises this reads live there")
	}
	var gateIfs []string
	for i, s := range steps {
		m, _ := s.(map[string]any)
		name, _ := m["name"].(string)
		if _, has := m["continue-on-error"]; has && name != "validate .goreleaser.yaml" {
			t.Errorf("step %d (%q) carries continue-on-error: its red would read as the job's "+
				"green, and this job is the only check mark there is", i, name)
		}
		if run, _ := m["run"].(string); strings.TrimSpace(run) == "make test" {
			cond, _ := m["if"].(string)
			gateIfs = append(gateIfs, cond)
		}
	}
	// The count is gatematch's question; the conditions are this one's.
	if len(gateIfs) == 2 {
		if gateIfs[0] != "" {
			t.Errorf("the first gate step runs on %q; the go.mod section leads, and a condition "+
				"there is a way for the leading compiler to be skipped", gateIfs[0])
		}
		if gateIfs[1] != "${{ !cancelled() }}" {
			t.Errorf("the second gate step runs on %q, not %q — without it, a red first section "+
				"turns \"one red, one green\" into \"one red, one skipped\", and the contrast the "+
				"section exists for is gone", gateIfs[1], "${{ !cancelled() }}")
		}
	}
}

// The gate has to be one that can turn a build red. This part reads the file
// rather than running anything, and knows only the two ways of neutering a job
// that live in it.
func TestTheChangelogGateCanStillFailTheBuild(t *testing.T) {
	// readGate is where this lives now: the job may hold `runs-on`, `env` and
	// `steps` and nothing else, and the one condition the gate step may carry
	// is pinned. A `continue-on-error`, a `needs`, a job-level `if`, an extra
	// key on the gate step — each is a key that is not on the list, and the
	// list is what fails.
	readGate(t, repoRoot(t))
}

// The stand-in git is part of this check, so it is checked. Breaking it — making
// it answer nothing instead of shouting, or dropping the test that the two
// revisions are the right way round — used to leave every case green, which meant
// the whole thing rested on a few lines nobody looked at.
func TestTheStandInGitAnswersOnlyWhatItModels(t *testing.T) {
	dir := t.TempDir()
	writeAll(t, dir, map[string]string{"git": fakeGit(dir), "changed": "a.go\n", "added": "b.go\n"})
	if err := os.Chmod(filepath.Join(dir, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, int) {
		cmd := exec.Command(filepath.Join(dir, "git"), args...)
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return string(out), code
	}
	for _, c := range []struct {
		name string
		args []string
		want string
		code int
	}{
		{name: "the call it models", args: []string{"diff", "--name-only", "BASE_SHA...HEAD_SHA"}, want: "a.go\n"},
		{name: "…and the added-only form", args: []string{"diff", "--name-only", "--diff-filter=A", "BASE_SHA...HEAD_SHA"}, want: "b.go\n"},
		// git's A...B is merge-base(A,B)..B, so the order carries the meaning: the
		// same revision twice, or the two the other way round, is an empty diff.
		{name: "the same revision twice", args: []string{"diff", "--name-only", "BASE_SHA...BASE_SHA"}, want: ""},
		{name: "the other way round", args: []string{"diff", "--name-only", "HEAD_SHA...BASE_SHA"}, want: ""},
		// Both sides are checked, and each needs a case of its own: the two above
		// leave with the right-hand test alone, so without this the left-hand one
		// could be deleted unnoticed.
		{name: "the head against itself", args: []string{"diff", "--name-only", "HEAD_SHA...HEAD_SHA"}, want: ""},
		// Everything else has to be loud. A stand-in that answers nothing for a
		// call it does not model reads as "there were no changes".
		{name: "a patch instead of names", args: []string{"diff", "BASE_SHA...HEAD_SHA"}, code: 3},
		{name: "a pathspec", args: []string{"diff", "--name-only", "BASE_SHA...HEAD_SHA", "--", "."}, code: 3},
		{name: "a global option", args: []string{"-C", "/repo", "diff", "--name-only", "BASE_SHA...HEAD_SHA"}, code: 3},
		{name: "another filter", args: []string{"diff", "--name-only", "--diff-filter=AM", "BASE_SHA...HEAD_SHA"}, code: 3},
		{name: "two ranges", args: []string{"diff", "--name-only", "BASE_SHA...HEAD_SHA", "X...Y"}, code: 3},
		{name: "no range at all", args: []string{"diff", "--name-only"}, code: 3},
		{name: "another subcommand", args: []string{"rev-parse", "HEAD"}, code: 3},
	} {
		out, code := run(c.args...)
		if code != c.code {
			t.Errorf("%s: git %v exited %d, want %d (%s)", c.name, c.args, code, c.code, strings.TrimSpace(out))
		}
		if c.code == 0 && out != c.want {
			t.Errorf("%s: git %v printed %q, want %q", c.name, c.args, out, c.want)
		}
	}
}

// gate is the changelog job, read as YAML rather than as text.
//
// Every earlier version of this check read the workflow with string matching and
// was stepped around by a spelling: `grep -Ev` for `grep -v`, `- if:` for `if:`,
// a job id with an underscore that made the "next job" pattern miss and swallow
// the rest of the file. So this parses, and then says which keys a job and a step
// are allowed to have. A key nobody anticipated is a failure here rather than a
// silence — which is the same move as listing what is not run, applied to the
// file instead of the prose.
type gate struct {
	condition string
	env       map[string]string
	script    string
}

func readGate(t *testing.T, root string) gate {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := yaml.Unmarshal(b, &top); err != nil {
		t.Fatalf("reading the workflow: %v", err)
	}
	// The file's own top level can decide whether any of this runs, and can hand
	// every script an environment or a different shell. Reading only `jobs` left
	// all of that outside — so the same rule applies here: these keys and no
	// others.
	allowed(t, "the workflow", top, "name", "on", "jobs")
	on, _ := top["on"].(map[string]any)
	// Two triggers, both pinned. The pull_request types are the billing
	// decision written as structure: a draft's opening and its pushes are
	// free, marking it ready is the request for attestation, and
	// `synchronize` is absent on purpose — its presence billed every review
	// round. A type added or removed here changes when the gate runs, which
	// is exactly the kind of change that must be taught, not discovered.
	// The dispatch trigger is the release's one manual main-tree run.
	allowed(t, "the triggers", on, "pull_request", "workflow_dispatch")
	pr, ok := on["pull_request"].(map[string]any)
	if !ok {
		t.Fatalf("the workflow's triggers are %v; this check knows the gate as something that runs on a pull request", on)
	}
	allowed(t, "the pull_request trigger", pr, "types")
	types, _ := pr["types"].([]any)
	if len(types) != 3 || types[0] != "opened" || types[1] != "ready_for_review" || types[2] != "synchronize" {
		t.Fatalf("the pull_request types are %v; this check knows [opened, ready_for_review, "+
			"synchronize] — opened for a pull request born ready, ready_for_review as the "+
			"request for attestation, and synchronize so the attestation follows the head "+
			"(the draft skip is what keeps a draft's pushes free; drop synchronize and a "+
			"push after the ready mark leaves the green pointing at some other tree)", types)
	}
	if _, ok := on["workflow_dispatch"]; !ok {
		t.Fatal("no workflow_dispatch trigger; the release's final main-tree run has no way in")
	}
	if sub, ok := on["workflow_dispatch"].(map[string]any); ok && len(sub) > 0 {
		t.Fatalf("the workflow_dispatch trigger carries %v; inputs are a way to make the manual "+
			"run something other than the workflow as written", sub)
	}
	jobs, _ := top["jobs"].(map[string]any)
	job, ok := jobs["ci"].(map[string]any)
	if !ok {
		t.Fatal("no `ci` job in the workflow; the gate lives among its steps and this check no longer finds it")
	}
	// What the job may say, and the one skip it may make. The job-level `if`
	// exists to make drafts free, and its exact text is pinned because every
	// other condition is a way for the gate to not run on something that can
	// merge — a draft cannot, GitHub refuses that natively, so skipping it
	// skips nothing the gate is for. On workflow_dispatch the event has no
	// pull_request, the expression is empty, !empty is true, and the manual
	// run goes ahead. The environment the job hands every script is pinned
	// for the same reason the keys are: a variable added here reaches the
	// gate's script too, and this check would be running that script without
	// it.
	allowed(t, "the job", job, "if", "runs-on", "env", "steps")
	if jobIf, _ := job["if"].(string); jobIf != "${{ !github.event.pull_request.draft }}" {
		t.Fatalf("the job runs on %q; this check knows one skip — drafts, which cannot merge — "+
			"and a different condition may skip something that can", jobIf)
	}
	jobEnv, _ := job["env"].(map[string]any)
	if len(jobEnv) != 1 || jobEnv["GOTOOLCHAIN"] != "local" {
		t.Fatalf("the job hands every script env %v; this check knows GOTOOLCHAIN=local and nothing else", jobEnv)
	}

	steps, _ := job["steps"].([]any)
	if len(steps) == 0 {
		t.Fatal("the job has no steps")
	}
	checkout, _ := steps[0].(map[string]any)
	allowed(t, "the checkout step", checkout, "uses", "with")
	// What that step is, not just which keys it has: `uses:` is a place to run
	// anything at all, and a step this check never executes is a step it cannot
	// otherwise say anything about.
	if u, _ := checkout["uses"].(string); u != "actions/checkout@v7" {
		t.Fatalf("the first step uses %q; this check treats it as a plain checkout and does not run it, so anything else there is code it never sees", u)
	}
	// And what it is told to check out. `repository:` or `ref:` would put a
	// different tree under the script, which makes the range it diffs meaningless
	// — and neither shows up in anything this check runs.
	with, _ := checkout["with"].(map[string]any)
	allowed(t, "the checkout's settings", with, "fetch-depth")
	if d, ok := with["fetch-depth"].(int); !ok || d != 0 {
		t.Fatalf("the checkout asks for fetch-depth %v; the gate diffs two revisions, and a shallow clone is one that may not have them", with["fetch-depth"])
	}
	// The gate is found by its name, exactly once. Among the many steps of a
	// single job, position cannot locate it, and a second step under the same
	// name would leave this check reading whichever one it happened to take.
	const gateName = "a shipped change needs a changelog.d fragment"
	var step map[string]any
	found := 0
	for _, s := range steps[1:] {
		m, _ := s.(map[string]any)
		if n, _ := m["name"].(string); n == gateName {
			step = m
			found++
		}
	}
	if found != 1 {
		t.Fatalf("found %d steps named %q; the gate is one step, and this check reads exactly it", found, gateName)
	}
	allowed(t, "the gate step", step, "name", "if", "env", "run")
	cond, _ := step["if"].(string)
	if cond != "${{ !cancelled() && github.event_name == 'pull_request' }}" {
		t.Fatalf("the gate step runs on %q; this check knows one condition — on every pull request, "+
			"even when an earlier step failed — and a different one may mean it never runs", cond)
	}

	g := gate{condition: cond, env: map[string]string{}}
	// The expressions this check knows how to stand in for. A fourth stops it:
	// running the script with a value the workflow never gives is how a check comes
	// to be measuring something else.
	known := map[string]string{
		"${{ github.event.pull_request.base.sha }}": "BASE_SHA",
		"${{ github.event.pull_request.head.sha }}": "HEAD_SHA",
		"${{ github.event.pull_request.body }}":     "",
	}
	rawEnv, _ := step["env"].(map[string]any)
	for name, v := range rawEnv {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("the step passes %s as %T; this check only knows strings", name, v)
		}
		stand, ok := known[strings.TrimSpace(s)]
		if !ok {
			t.Fatalf("the step passes %s=%q — not an expression this check knows, and not something it can stand in for; teach it that one rather than running the script with a value the workflow never gives", name, s)
		}
		g.env[name] = stand
	}
	if len(g.env) != 3 {
		t.Fatalf("expected the step to take three values from the event, found %d: %v", len(g.env), g.env)
	}
	g.script, _ = step["run"].(string)
	if g.script == "" {
		t.Fatal("the gate step runs nothing")
	}
	if strings.Contains(g.script, "${{") {
		t.Fatal("the script carries a GitHub expression, which is substituted before bash sees it — this check hands bash the text as written and would be running a different script")
	}
	// Every name the script reads has to be one this check supplies or one the
	// script sets itself. A name from the runner's environment is empty here and
	// full there, which is a difference this check cannot see from the inside.
	if m := regexp.MustCompile(`\$[0-9@#*]`).FindString(g.script); m != "" {
		t.Fatalf("the script reads %s — not a name but how it was invoked, and that differs: GitHub writes the script to a file under _temp/ and runs it, this pipes the text to bash", m)
	}
	for _, at := range regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)`).FindAllStringSubmatchIndex(g.script, -1) {
		name := g.script[at[2]:at[3]]
		if _, fromUs := g.env[name]; fromUs {
			continue
		}
		// Set *before* it is read. An assignment further down is a name the runner
		// still fills in — the script reads what the environment had, and only then
		// overwrites it, which is a difference this check would otherwise miss.
		set := regexp.MustCompile(`(?m)^\s*` + name + `=`).FindStringIndex(g.script)
		if set != nil && set[0] < at[0] {
			continue
		}
		t.Fatalf("the script reads $%s before anything here sets it — in CI that name may hold something, and here it is always empty", name)
	}
	return g
}

// allowed fails unless the mapping has exactly the keys named. Listing what may
// be there, rather than what may not, is what makes a key nobody thought of a
// failure instead of a gap.
func allowed(t *testing.T, what string, m map[string]any, keys ...string) {
	t.Helper()
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	for k := range m {
		if !want[k] {
			t.Fatalf("%s has a %q, which this check does not model — it may change whether or how the gate runs", what, k)
		}
	}
}

// runGate runs the gate's script with a stand-in git, and answers with what it
// printed and how it ended.
// runGate runs the gate twice — once with nothing beyond what the workflow
// passes, once with a dozen runner names also set — and requires the same answer
// before returning it.
//
// Reading the script for `$NAME` cannot close this on its own: `env | grep`,
// `printenv`, `${!x}` all read the environment without spelling a name. Running
// every case twice can, for the names in the sample. It is every case rather than
// a couple, because a rule that only fires on a shape the pair never takes — a
// large change, an added fragment — would otherwise never be asked twice.
func runGate(t *testing.T, g gate, changed, added []string, body string) (string, int) {
	t.Helper()
	bare, bareCode := runGateWith(t, g, changed, added, body, nil)
	filled, filledCode := runGateWith(t, g, changed, added, body, runnerNames)
	if bare != filled || bareCode != filledCode {
		t.Errorf("the gate answers differently when the runner's variables are set:\n"+
			"  with nothing: exit %d %q\n  on a runner:  exit %d %q\n"+
			"something in the script reads where it is running, which is a rule this check can only see one side of",
			bareCode, bare, filledCode, filled)
	}
	return bare, bareCode
}

func runGateWith(t *testing.T, g gate, changed, added []string, body string, extra []string) (string, int) {
	t.Helper()
	dir := t.TempDir()
	writeAll(t, dir, map[string]string{
		"git":     fakeGit(dir),
		"changed": strings.Join(changed, "\n") + "\n",
		"added":   strings.Join(added, "\n") + "\n",
	})
	if err := os.Chmod(filepath.Join(dir, "git"), 0o755); err != nil {
		t.Fatal(err)
	}

	// GitHub's default for `run:` is bash with -e. The environment is built from
	// nothing rather than inherited: a name the script reads is either one the
	// workflow passes or one it sets itself (readGate insists on that), and
	// inheriting this machine's environment would quietly answer for names that
	// are empty here and full on a runner.
	cmd := exec.Command("bash", "--noprofile", "--norc", "-e", "-s")
	// The repository root, because that is what a runner's checkout is. Leaving it
	// wherever the test happens to run — the package directory — lets a rule that
	// looks for a file answer differently here than there: `[ -f CHANGELOG.md ] &&
	// exit 0` is false in this package and true in CI, and the check would have
	// nothing to say about it. Nothing here writes.
	cmd.Dir = repoRoot(t)
	cmd.Stdin = strings.NewReader(g.script)
	cmd.Env = []string{"PATH=" + dir + string(os.PathListSeparator) + "/usr/bin:/bin"}
	for name, stand := range g.env {
		if stand == "" {
			stand = body
		}
		cmd.Env = append(cmd.Env, name+"="+stand)
	}
	for _, name := range extra {
		cmd.Env = append(cmd.Env, name+"=set-by-the-runner")
	}
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running the gate: %v", err)
	}
	return string(out), code
}

// runnerNames are variables a GitHub runner sets that a script could read. The
// list is short and certainly incomplete, which is the point: it is not a
// promise, it is a sample.
var runnerNames = []string{
	"GITHUB_ACTIONS", "CI", "GITHUB_HEAD_REF", "GITHUB_BASE_REF", "GITHUB_REF",
	"GITHUB_EVENT_NAME", "GITHUB_ACTOR", "GITHUB_REPOSITORY", "GITHUB_SHA",
	"GITHUB_WORKSPACE", "RUNNER_OS", "GITHUB_RUN_ID",
}

// fakeGit answers `diff --name-only [--diff-filter=A] BASE_SHA...HEAD_SHA` and
// shouts at everything else.
//
// Shouting is the point. A stand-in that quietly returns nothing for a call it
// does not model turns "the script asked something new" into "the script found no
// changes", which reads as a green gate — and the check would be measuring a
// script that never ran. Every departure below was reachable: a pathspec after
// `--`, a global option before `diff`, the two revisions the other way round, a
// different --diff-filter. Each one is a way to narrow what the gate sees without
// touching the exclusion list at all.
func fakeGit(dir string) string {
	return `#!/bin/sh
say() { echo "fake git: $*" >&2; exit 3; }
[ "$1" = diff ] || say "only 'diff' is modelled, got: $*"
shift
filter=no
names=no
range=""
for a in "$@"; do
  case "$a" in
    --name-only) names=yes ;;
    --diff-filter=A) filter=yes ;;
    --diff-filter=*) say "only --diff-filter=A is modelled, got $a" ;;
    --) say "a pathspec narrows the diff, and this stand-in does not model one" ;;
    -*) say "unmodelled option: $a" ;;
    *...*) [ -z "$range" ] || say "more than one revision range: $*" ; range="$a" ;;
    *) say "unmodelled argument: $a" ;;
  esac
done
[ "$names" = yes ] || say "without --name-only this prints a patch, which is a different thing to filter"
[ -n "$range" ] || say "no A...B range in: $*"
left="${range%%...*}"
right="${range#*...}"
# A...B is merge-base(A,B)..B, so the order is the whole meaning: the same
# revision twice, or the two the other way round, is an empty diff in real git.
[ "$left" = BASE_SHA ] || exit 0
[ "$right" = HEAD_SHA ] || exit 0
if [ "$filter" = yes ]; then cat "` + filepath.Join(dir, "added") + `"; else cat "` + filepath.Join(dir, "changed") + `"; fi
`
}

func writeAll(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// shippedFiles are the non-test files of every package the released binary links,
// as paths relative to the module root. `go list -deps` is asked about the binary
// .goreleaser.yaml builds, for the platform it builds it for — so a file chosen by
// build tag is included, and so is a package that happens to live under a
// testdata/ directory.
func shippedFiles(t *testing.T, root string) (files, packages []string) {
	t.Helper()
	main, goos, goarch := releaseTarget(t, root)
	cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}\t{{.Dir}}\t{{range .GoFiles}}{{.}} {{end}}", main)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("listing what the released binary links: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) != 3 || !strings.HasPrefix(parts[0], "github.com/suruseas/opossum") {
			continue
		}
		rel, err := filepath.Rel(root, parts[1])
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		names := strings.Fields(parts[2])
		if len(names) == 0 {
			continue
		}
		packages = append(packages, filepath.ToSlash(rel))
		for _, n := range names {
			files = append(files, filepath.ToSlash(filepath.Join(rel, n)))
		}
	}
	sort.Strings(packages)
	sort.Strings(files)
	return files, packages
}

// releaseTarget is what .goreleaser.yaml builds, and for what.
func releaseTarget(t *testing.T, root string) (main, goos, goarch string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	one := func(pattern, what string) string {
		m := regexp.MustCompile(pattern).FindAllStringSubmatch(string(b), -1)
		if len(m) != 1 {
			t.Fatalf("expected one %s in .goreleaser.yaml, found %d; this check does not know which to hold the gate to", what, len(m))
		}
		return m[0][1]
	}
	return one(`(?m)^\s+main:\s+(\S+)`, "built binary"),
		one(`(?m)^\s+goos:\n\s+- (\S+)`, "target OS"),
		one(`(?m)^\s+goarch:\n\s+- (\S+)`, "target architecture")
}
