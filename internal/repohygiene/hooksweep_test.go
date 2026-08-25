package repohygiene_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every way the one gofmt call can come back, against one rule: the hook may
// print nothing that is not registered below, every registered line carries the
// condition under which it is allowed, and most carry the condition under which
// they have to appear.
//
// This used to sweep two exit statuses against each other, a hundred rows of
// them, because the hook asked gofmt twice. Three defects came out of that shape
// and each was the same one: an arm asserting something about the call its own
// condition had not read. The hook asks once now, so the second status is gone
// and so are the rows that paired it — twenty are left, and none of them can
// ask which call is being talked about.
//
// The rule is inverted on purpose. Written the other way — a list of phrases,
// each checked for truth if it appeared — it caught almost nothing, for two
// reasons that are worth keeping written down:
//
//   - It matched the wording of the hook it was written against, so a defect
//     that phrased its lie slightly differently walked past.
//   - It could only ask whether what was said was true, never whether something
//     was said that should not have been, or whether something that had to be
//     said was missing. Adding one false sentence to an arm passed.
//
// **How this catches things, which is not how it first reads.** The per-line
// truth conditions look thin against a badly broken hook, because the same row
// has usually gone red already for unregistered wording or for going quiet.
// Against a mutation that breaks one thing, the truth condition is often the
// only net: printing a registered sentence from an arm whose condition never
// read it is caught by nothing else.
//
// So when adding a sentence, write the narrowest true condition, not one that
// returns true with a shrug, and anchor the pattern at both ends — a pattern
// that stops early lets any second half be appended, and a false second half is
// what the defect looked like every time.
//
// **What it does not reach:**
//
//   - The order lines come in. TestBothKindsOfGofmtTroubleAreReported pins the
//     one ordering that has a reason: the half that needs a person first.
//   - Whether a line that is allowed is the right line to print. A partial list
//     from a call that failed still comes out under "has something to say",
//     with `gofmt -w .` beside it, and this stays green. Real gofmt exits 2 on a
//     tree holding one file that does not parse and still lists the other file
//     that merely needs formatting, and TestBothKindsOfGofmtTroubleAreReported
//     requires that list to reach the reader — so this is not a bug to be
//     swatted. What is unresolved is narrower: nobody is told the list may be
//     short. This allows the line and does not require it (see owesList),
//     because a check that requires today's behaviour goes red the day somebody
//     improves it, and then the improvement looks like the mistake.
//   - What the list and the diagnosis look like. Each shim stream is one fixed
//     line, so nothing here notices a hook that mangles multi-line output.
//   - Which stream the hook keeps is reached now, which it was not before. The
//     shim writes both every row, so crossing the two redirections and merging
//     them into one both turn this red. Under two calls the merge was invisible
//     here, because each shim call only ever wrote the stream its caller kept.
//     What is left to the tests beside this one is the cut that separates them
//     again, and that is TestAFileNamedLikeASeparatorIsStillReportedWhole.
//   - Which shell runs the hook. Everything here starts it through its `#!`
//     line, so taking off the wrapping that makes ksh 93u+ agree leaves this
//     green. TestOtherShellsReadTheHookTheSameWay is what holds that, and only
//     on a machine that has another shell to compare against.
//   - Which stream the hook itself writes to. Everything here is read through
//     CombinedOutput, so a hook that sent its refusal to stdout would look the
//     same.
//   - Exit codes other than the two classes the hook can tell apart: below 126,
//     and 126 or above. 125 stands for the middle of the first class, which
//     nothing stood for until a mutation moving the line to 3 survived.
func TestEveryWayTheOneGofmtCallCanComeBack(t *testing.T) {
	hook := filepath.Join(repoRoot(t), ".githooks", "pre-commit")
	tree, shim := oneFixture(t)

	// Zero, the middle of the range, and the shell's own numbers.
	for _, status := range []int{0, 2, 125, 126, 127} {
		for _, listed := range []bool{true, false} {
			for _, explained := range []bool{true, false} {
				name := fmt.Sprintf("%d_list=%v_diag=%v", status, listed, explained)
				t.Run(name, func(t *testing.T) {
					said, code := runHook(t, hook, tree, shim, status, listed, explained)
					w := world{status: status, listed: listed, explained: explained}

					clean := status == 0 && !listed
					if clean && code != 0 {
						t.Errorf("nothing was wrong and it refused:\n%s", said)
					}
					if !clean && code != 1 {
						t.Errorf("exit = %d, want 1 (something was wrong):\n%s", code, said)
					}
					if code != 0 && strings.TrimSpace(said) == "" {
						t.Error("a refusal without a word is just a faster no")
					}

					lines := strings.Split(strings.TrimRight(said, "\n"), "\n")
					seen := map[string]bool{}
					spoke := map[int]bool{}
					for i, line := range lines {
						if strings.TrimSpace(line) == "" {
							// Trailing blanks are the shell's; one in the
							// middle means a heading printed a value that
							// still had its own newline on it.
							for _, rest := range lines[i+1:] {
								if strings.TrimSpace(rest) != "" {
									t.Errorf("a blank line in the middle of what it said:\n%s", said)
									break
								}
							}
							continue
						}
						if strings.HasSuffix(line, ":") {
							next := ""
							if i+1 < len(lines) {
								next = strings.TrimSpace(lines[i+1])
							}
							if next == "" {
								t.Errorf("a colon promises what comes next, and nothing does:\n%s", said)
							}
						}
						if seen[line] {
							t.Errorf("said this twice, so two arms answered:\n%s", said)
							break
						}
						seen[line] = true

						at, ok, why := lineIsAllowed(line, w)
						if !ok {
							t.Errorf("%s\nline: %q\nwhole output:\n%s", why, line, said)
						} else {
							spoke[at] = true
						}
					}

					// And the other direction: an arm may not go quiet about
					// the thing it exists to explain.
					for at, s := range vocabulary {
						if s.must != nil && s.must(w) && !spoke[at] {
							t.Errorf("this had to be said here and was not: %s\nwhole output:\n%s",
								s.pattern, said)
						}
					}
				})
			}
		}
	}
}

const (
	listedName    = "LISTED.go"
	diagnosisText = "DIAGNOSIS-TEXT"
)

// world is what the one call actually did, which is what every line has to be
// consistent with.
type world struct {
	status            int
	listed, explained bool
}

// The two arms, and what each is standing on.
func cannotRun(w world) bool    { return w.status >= 126 }
func ordinary(w world) bool     { return w.status < 126 }
func hasDiagnosis(w world) bool { return ordinary(w) && w.status != 0 && w.explained }
func hasList(w world) bool      { return ordinary(w) && w.listed }
func nothingLeft(w world) bool {
	return ordinary(w) && w.status != 0 && !hasDiagnosis(w) && !hasList(w)
}

// What the hook owes, which is narrower than what it may say. The gap is the
// list from a call that failed: allowed, because real gofmt lists usefully even
// when it exits 2, and not required, because requiring today's behaviour turns
// the check red on the day somebody improves it.
func owesList(w world) bool   { return w.listed && (w.status == 0 || cannotRun(w)) }
func owesAdvice(w world) bool { return ordinary(w) && w.listed && w.status == 0 }
func owesNamed(w world) bool  { return cannotRun(w) && w.listed }
func owesDiagnosis(w world) bool {
	return w.explained && (hasDiagnosis(w) || cannotRun(w))
}

// sentence is one line the hook is allowed to print, and when.
type sentence struct {
	pattern *regexp.Regexp
	// when says whether this line may be printed in this world. n is the number
	// the line carried, or -1 when it carried none.
	when func(w world, n int) (bool, string)
	// must, when set, says this line has to be printed in this world. Without
	// it the check is one-sided: dropping a sentence leaves every remaining
	// sentence true, which is how an arm can go quiet about the very thing it
	// was added to explain.
	must func(w world) bool
}

func onlyIn(where func(world) bool, what string) func(world, int) (bool, string) {
	return func(w world, n int) (bool, string) {
		if !where(w) {
			return false, "this is only true when " + what
		}
		return true, ""
	}
}

var vocabulary = []sentence{
	{pattern: regexp.MustCompile(`^gofmt could not run at all \(exit (\d+)\)$`),
		when: func(w world, n int) (bool, string) {
			if w.status < 126 {
				return false, fmt.Sprintf("it ran and exited %d", w.status)
			}
			if n != w.status {
				return false, fmt.Sprintf("it exited %d, not %d", w.status, n)
			}
			return true, ""
		},
		must: cannotRun},

	{pattern: regexp.MustCompile(`^it named these before it stopped:$`),
		when: onlyIn(owesNamed, "it could not run and had named something"),
		must: owesNamed},

	{pattern: regexp.MustCompile(`^nothing here was checked$`),
		when: onlyIn(cannotRun, "it could not be run at all"),
		must: cannotRun},

	{pattern: regexp.MustCompile(`^gofmt stopped on this tree:$`),
		when: onlyIn(hasDiagnosis, "there is a diagnosis to introduce"),
		must: hasDiagnosis},

	{pattern: regexp.MustCompile(`^formatting is not the fix here — this has to parse, and be readable, first$`),
		when: onlyIn(hasDiagnosis, "there is a diagnosis to stand behind"),
		must: hasDiagnosis},

	{pattern: regexp.MustCompile(`^gofmt has something to say about this tree:$`),
		when: onlyIn(hasList, "it named something"),
		must: owesAdvice},

	{pattern: regexp.MustCompile("^run `gofmt -w \\.`, or `git commit --no-verify` if you meant it$"),
		when: onlyIn(hasList, "there is a list to act on"),
		must: owesAdvice},

	{pattern: regexp.MustCompile(`^gofmt failed with nothing to say \(exit (\d+)\), so nothing here was checked$`),
		when: func(w world, n int) (bool, string) {
			if w.status == 0 {
				return false, "it worked"
			}
			if n != w.status {
				return false, fmt.Sprintf("it exited %d, not %d", w.status, n)
			}
			if w.explained {
				return false, "it had something to say, and this line claims it did not"
			}
			if w.listed {
				return false, "there was a list, so something was checked"
			}
			if cannotRun(w) {
				return false, "an earlier arm answers this world"
			}
			return true, ""
		},
		must: nothingLeft},

	// The two lines that are not sentences but contents. Their conditions name
	// the arm as well: a listing printed under a heading that never promised one
	// is the same defect as a false sentence.
	{pattern: regexp.MustCompile(`^` + regexp.QuoteMeta(listedName) + `$`),
		when: func(w world, n int) (bool, string) {
			if !w.listed {
				return false, "nothing was listed"
			}
			return true, ""
		},
		must: owesList},

	{pattern: regexp.MustCompile(`^` + regexp.QuoteMeta(diagnosisText) + `$`),
		when: func(w world, n int) (bool, string) {
			if !w.explained {
				return false, "nothing was said"
			}
			if ordinary(w) && w.status == 0 {
				return false, "it worked, so there is nothing to quote"
			}
			return true, ""
		},
		must: owesDiagnosis},
}

func lineIsAllowed(line string, w world) (int, bool, string) {
	for i, s := range vocabulary {
		m := s.pattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n := -1
		if len(m) > 1 {
			fmt.Sscanf(m[1], "%d", &n)
		}
		if ok, why := s.when(w, n); !ok {
			return i, false, "this line is not true here: " + why
		}
		return i, true, ""
	}
	return -1, false, "the hook printed a line nothing here knows about. If it is new, add it to " +
		"the vocabulary above with the narrowest condition under which it is true, anchored at " +
		"both ends — that is the whole check. (A `go vet cannot make sense of this tree` line " +
		"means the go on PATH could not run, which is this test's fixture and not the hook.)"
}

// oneFixture builds the tree, the shim and the stand-in `go` once. Built per row
// it was thirty-seven seconds for two seconds of hook: the rest was making and
// removing directories.
func oneFixture(t *testing.T) (tree, shim string) {
	t.Helper()
	tree = t.TempDir()
	write(t, filepath.Join(tree, "go.mod"), "module probe\n\ngo 1.24\n")
	// A real package: the rows where gofmt says nothing fall through to the vet
	// arms, and a tree with no packages fails there — which this sweep found on
	// its first run, in its own fixture.
	write(t, filepath.Join(tree, "probe.go"), "package probe\n\n// Fine is here so the tree has a package.\nfunc Fine() int { return 1 }\n")

	shim = t.TempDir()
	// One call now, so the shim has no call count to keep and rows share
	// nothing but the answers in their environment.
	write(t, filepath.Join(shim, "gofmt"), "#!/bin/sh\n"+
		"[ -n \"$SWEEP_LISTED\" ] && echo '"+listedName+"'\n"+
		"[ -n \"$SWEEP_EXPLAINED\" ] && echo '"+diagnosisText+"' >&2\n"+
		"exit ${SWEEP_STATUS}\n")
	if err := os.Chmod(filepath.Join(shim, "gofmt"), 0o755); err != nil {
		t.Fatal(err)
	}
	real, err := exec.LookPath("go")
	if err != nil {
		t.Fatal("go is not on PATH")
	}
	if err := os.Symlink(real, filepath.Join(shim, "go")); err != nil {
		t.Fatal(err)
	}
	return tree, shim
}

func runHook(t *testing.T, hook, tree, shim string, status int, listed, explained bool) (string, int) {
	t.Helper()
	env := []string{
		"PATH=" + shim + string(os.PathListSeparator) + "/usr/bin:/bin",
		"HOME=" + tree,
		"GOOS=",
		fmt.Sprintf("SWEEP_STATUS=%d", status),
	}
	if listed {
		env = append(env, "SWEEP_LISTED=1")
	}
	if explained {
		env = append(env, "SWEEP_EXPLAINED=1")
	}
	cmd := exec.Command(hook)
	cmd.Dir = tree
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running the hook: %v\n%s", err, out)
	}
	return string(out), code
}
