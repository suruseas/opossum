package repohygiene_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The push-time sieve is wired per clone, by a git config that nobody sets
// twice — and a clone where nobody set it once pushes in silence: no hook
// runs, the push succeeds, and from outside that is the same as a sieve that
// passed. It happened: a clone pushed unwired for a day while its owner
// reported "sieve green", and a red tree reached the remote without touching
// any net (#646). Nothing in the repository can set the config for a clone it
// cannot see, so what the repository does is say so, at the top of the one
// command everyone runs.
//
// The script is run, not read: it is a handful of shell whose whole value is
// which cases it speaks in and which it keeps quiet in, and both directions
// matter — a check that speaks in CI is noise nobody wired, and one that is
// quiet for a clone wired somewhere else is the silence this exists to end.
func TestAnUnwiredCloneIsNamedAtTheTopOfTheGate(t *testing.T) {
	script := filepath.Join(repoRoot(t), "sieve", "wired.sh")

	// A fresh repository carrying the hook file, wired (or not) as asked.
	clone := func(t *testing.T, initGit bool, hooksPath string) string {
		t.Helper()
		dir := t.TempDir()
		if initGit {
			gitIn(t, dir, "init", "-q")
		}
		if err := os.MkdirAll(filepath.Join(dir, ".githooks"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".githooks", "pre-push"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if hooksPath != "" {
			gitIn(t, dir, "config", "core.hooksPath", hooksPath)
		}
		return dir
	}
	// Run with the surroundings scrubbed, so that the machine the suite runs on
	// does not decide the case: CI (this very test runs under it), the global
	// git config (a developer's own core.hooksPath), and any repository the
	// temp dir happens to sit under (git looks upward; the ceiling stops it
	// at the case's own directory).
	say := func(t *testing.T, dir string, extra ...string) string {
		t.Helper()
		cmd := exec.Command("sh", script)
		cmd.Dir = dir
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "CI=") && !strings.HasPrefix(kv, "GIT_CONFIG_GLOBAL=") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		cmd.Env = append(cmd.Env,
			"GIT_CONFIG_GLOBAL="+filepath.Join(t.TempDir(), "no-global-gitconfig"),
			"GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
		cmd.Env = append(cmd.Env, extra...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("the check must never fail the gate, but exited: %v\n%s", err, out)
		}
		return string(out)
	}

	unwired := clone(t, true, "")
	if got := say(t, unwired); !strings.Contains(got, "not wired") || !strings.Contains(got, "make hooks") {
		t.Errorf("an unwired clone should be named, with the way out; said:\n%s", got)
	}
	// Wired elsewhere is not wired: a hooks directory of one's own runs its
	// own hooks, not this repository's pre-push — and the usual name for such
	// a directory is .githooks, so the name must not be what is compared.
	elsewhere := clone(t, true, filepath.Join(t.TempDir(), ".githooks"))
	if got := say(t, elsewhere); !strings.Contains(got, "not wired") {
		t.Errorf("a clone whose hooks live elsewhere should be named; said:\n%s", got)
	}
	// The same, set the way people actually set it: globally. The clone
	// itself says nothing, and git resolves the global setting.
	home := t.TempDir()
	global := filepath.Join(home, "gitconfig")
	gitIn(t, home, "config", "--file", global, "core.hooksPath", filepath.Join(home, ".githooks"))
	if got := say(t, clone(t, true, ""), "GIT_CONFIG_GLOBAL="+global); !strings.Contains(got, "not wired") {
		t.Errorf("a clone under a global hooks directory should be named; said:\n%s", got)
	}

	// And every case in which it must keep quiet.
	for name, c := range map[string]struct {
		dir string
		env []string
	}{
		"wired by relative path":          {clone(t, true, ".githooks"), nil},
		"wired with a trailing slash":     {clone(t, true, ".githooks/"), nil},
		"wired by a dotted relative path": {clone(t, true, "./.githooks"), nil},
		"wired by absolute path": {func() string {
			d := clone(t, true, "")
			gitIn(t, d, "config", "core.hooksPath", filepath.Join(d, ".githooks"))
			return d
		}(), nil},
		"under CI, which pushes nothing": {clone(t, true, ""), []string{"CI=true"}},
		"outside any git checkout":       {clone(t, false, ""), nil},
		"a checkout with no hook to wire": {func() string {
			d := clone(t, true, "")
			if err := os.Remove(filepath.Join(d, ".githooks", "pre-push")); err != nil {
				t.Fatal(err)
			}
			return d
		}(), nil},
	} {
		if got := say(t, c.dir, c.env...); got != "" {
			t.Errorf("%s: the check should be silent, said:\n%s", name, got)
		}
	}
}

// Speaking is only worth anything from the place people look. The script is
// reached through `make test` — as a prerequisite, so the recipe the other
// tests read stays what it was — and the way out it names is a target that
// runs exactly the documented command. The sieve's own clone is wired by its
// runner, or every push would print the line from inside the container.
func TestTheGateAsksWhetherThisCloneIsWired(t *testing.T) {
	root := repoRoot(t)
	makefile := read(t, root, "Makefile")

	var testLine string
	for _, line := range strings.Split(makefile, "\n") {
		if strings.HasPrefix(line, "test:") {
			testLine = line
		}
	}
	prereqs := strings.Fields(strings.SplitN(strings.TrimPrefix(testLine, "test:"), "##", 2)[0])
	if !contains(prereqs, "wired") {
		t.Errorf("`test` should have `wired` as a prerequisite, has %v: without it the check runs "+
			"only for whoever knows to run it, which is nobody who needs it", prereqs)
	}
	lines := joinContinuations(strings.Split(makefile, "\n"))
	for target, want := range map[string]string{
		"wired": "sieve/wired.sh",
		"hooks": "git config core.hooksPath .githooks",
	} {
		recipe, err := recipeOf(target)(lines)
		if err != nil {
			t.Errorf("Makefile: %v", err)
			continue
		}
		if !strings.Contains(strings.Join(recipe, "\n"), want) {
			t.Errorf("`%s` should run %q, runs:\n%s", target, want, strings.Join(recipe, "\n"))
		}
	}
	if !strings.Contains(read(t, root, "CONTRIBUTING.md"), "make hooks") {
		t.Errorf("CONTRIBUTING.md should name `make hooks`")
	}
	// Wired before the gate runs, not merely somewhere in the file: after
	// `exec make test` nothing runs at all.
	runsh := read(t, root, "sieve/run.sh")
	wire, gate := strings.Index(runsh, "git config core.hooksPath .githooks"), strings.Index(runsh, "exec make test")
	if wire < 0 || gate < 0 || wire > gate {
		t.Errorf("sieve/run.sh should wire its clone before `exec make test` (wire at %d, gate at %d)", wire, gate)
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// Reaching the script is not the same as being heard: a recipe that runs it
// with its output thrown away (`@sh sieve/wired.sh >/dev/null 2>&1`) still
// satisfies every test above, which read the script directly and read the
// Makefile as text. So `make wired` itself is run — the repository's Makefile
// and script, copied into an unwired clone with the surroundings scrubbed —
// and the way out has to come back on the terminal (#838). A wired clone,
// through the same recipe, has to say nothing.
func TestTheNoticeReachesTheTerminalThroughMake(t *testing.T) {
	root := repoRoot(t)
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not on PATH")
	}
	clone := func(t *testing.T, wired bool) string {
		t.Helper()
		dir := t.TempDir()
		gitIn(t, dir, "init", "-q")
		for _, rel := range []string{"Makefile", "sieve/wired.sh"} {
			src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil {
				t.Fatal(err)
			}
			dst := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dst, src, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.MkdirAll(filepath.Join(dir, ".githooks"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".githooks", "pre-push"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if wired {
			gitIn(t, dir, "config", "core.hooksPath", ".githooks")
		}
		return dir
	}
	makeWired := func(t *testing.T, dir string) string {
		t.Helper()
		// Run as a person types it, not as a sub-make: under a parent make (the
		// sieve runs this suite from `make test`) GNU make announces
		// "Entering directory" around the recipe, which is make talking, not
		// the notice — so the flags and level a parent would hand down are
		// dropped and the directory chatter is switched off.
		cmd := exec.Command("make", "--no-print-directory", "wired")
		cmd.Dir = dir
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "CI=") && !strings.HasPrefix(kv, "GIT_CONFIG_GLOBAL=") &&
				!strings.HasPrefix(kv, "MAKEFLAGS=") && !strings.HasPrefix(kv, "MAKELEVEL=") && !strings.HasPrefix(kv, "MFLAGS=") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		cmd.Env = append(cmd.Env,
			"GIT_CONFIG_GLOBAL="+filepath.Join(t.TempDir(), "no-global-gitconfig"),
			"GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("`make wired` must never fail, but exited: %v\n%s", err, out)
		}
		return string(out)
	}

	if got := makeWired(t, clone(t, false)); !strings.Contains(got, "not wired") || !strings.Contains(got, "make hooks") {
		t.Errorf("`make wired` in an unwired clone should put the notice and the way out on the terminal; printed:\n%q", got)
	}
	if got := makeWired(t, clone(t, true)); got != "" {
		t.Errorf("`make wired` in a wired clone should print nothing; printed:\n%q", got)
	}
}
