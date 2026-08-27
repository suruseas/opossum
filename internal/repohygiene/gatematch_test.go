package repohygiene_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode"
)

// The gate people run before pushing has to ask for at least what CI asks for.
// It did not: CI ran `go test ./... -race -cover` and `make test` ran plain `go
// test ./...`, so a data race was something only CI could find — and "green
// here" meant less than anyone reading it thought.
//
// **How this reads the two files, and why it stopped reading them the other
// way.** The first version classified lines: find the words "go test" anywhere,
// then skip the ones that only mention it. Deciding what counts as a mention is
// a list of spellings, and four review rounds each walked past the current list
// — a flag inside a comment, the same comment behind a tab, prose behind `@echo`,
// then `@ echo` and `:` and `true` and `printf` and `/bin/echo`, then an
// ideographic space in a Japanese document, then a `~~~` fence. Every fix was a
// wider list, and every wider list had a next spelling.
//
// So it does not classify. It looks for one known line in each file and refuses
// to guess when it cannot find exactly one. There are few ways to write the gate
// and endless ways to write about it, and only the few are named here. Rewrite
// the recipe in a shape this does not know and it fails loudly rather than
// reading nothing and going green — which is the direction that matters, since
// every hole found so far was this check counting something the gate never
// asked for.
//
// **What it does not reach:**
//
//   - The step's surroundings. `continue-on-error: true` on the step, or
//     `MAKEFLAGS: -i` in `env:`, defeat the gate from YAML this never parses —
//     the line itself stays exactly the one looked for. What is written on the
//     line is read in full (see below); what stands around it is not.
//   - A `go test` inside a `run: |` block, or reached through `sh -c`. The
//     second-lane refusal reads single-line `run:` entries only, because that
//     is the shape a step running one command takes; a block hides one from
//     it. The gate line's own disappearance is still a refusal — "found 0" —
//     whatever shape replaced it.
//   - Anything but the flag names. `-count=3` and `-count=1` are the same ask,
//     and a `-timeout` that differs by an hour goes unremarked.
//   - Any workflow other than .github/workflows/ci.yml. pages.yml runs `go test
//     ./internal/site/` and is not read.
//   - Prefixes make accepts in orders this does not. `@@`, `+-`, `@- ` and `@`
//     followed by a tab are read; `@ - ` and `- @ ` are not, and land in the
//     "found none" branch. That is a refusal rather than a green, so a correct
//     recipe written that way fails with a message about the shape rather than
//     about the flags.
//   - A second identical step in the workflow — another lane running the same
//     command — is "found 2 of them", which refuses rather than picking. CI
//     getting stronger that way would have to be taught here.
//
// **The CI half changed shape.** The workflow used to restate the gate — `go
// test` with the flags written out again — and this test compared the two
// restatements. While they were two, they drifted in both directions: the
// leftovers check guarded only local runs, and the workflow's line answered
// out of a restored cache for packages it never ran. The workflow now runs
// `make test` itself, so "CI asks at least what the gate asks" holds by
// construction, and what is left to read from ci.yml is that it still says
// exactly that — once per compiler, so twice, each bare on its line, with no
// single-line `run: go test` beside them that could become a weaker lane.
// The count is two on purpose: the workflow's whole reason to run the gate
// twice is the two compilers, and a lane lost or gained without touching
// this number is the silent kind of change this file refuses.
func TestCIRunsTheGateItself(t *testing.T) {
	root := repoRoot(t)
	makefile := read(t, root, "Makefile")
	contributing := read(t, root, "CONTRIBUTING.md")
	ci := read(t, root, ".github/workflows/ci.yml")

	// Per command: found exactly once in the Makefile's recipe and once in
	// CONTRIBUTING's shown copy, with the same flags in both places, and with
	// nothing after it but flags — `|| true` turns a red gate green, and the
	// busy line is exactly the one a flaky week would tempt someone to quiet.
	// The CI line below has carried this refusal since the fold; the recipe
	// lines get the same one.
	perCommand := map[string]map[string]bool{}
	for _, cmd := range gateCommands {
		localRest, err1 := restAfter(makefile, cmd, recipeOf("test"), true)
		shownRest, err3 := restAfter(contributing, cmd, everyLine, false)
		bad := false
		for path, err := range map[string]error{"Makefile": err1, "CONTRIBUTING.md": err3} {
			if err != nil {
				t.Errorf("%s: %v", path, err)
				bad = true
			}
		}
		if bad {
			continue
		}
		for place, rest := range map[string]string{"Makefile": localRest, "CONTRIBUTING.md": shownRest} {
			for _, word := range strings.Fields(rest) {
				if strings.HasPrefix(word, "#") {
					break
				}
				if !strings.HasPrefix(word, "-") {
					t.Errorf("%s: `%s` runs with %q after it. A word that is not a flag changes "+
						"what the line does — `|| true` reports every red run green. Run the "+
						"command with its flags and nothing else, or teach this test the "+
						"addition on purpose.", place, cmd, word)
					break
				}
			}
		}
		local, shown := flagsIn(localRest), flagsIn(shownRest)
		// CONTRIBUTING shows the recipe rather than pointing at it, so there
		// are two places to change. A change that exists to stop two places
		// drifting apart should not quietly leave a third behind.
		if !reflect.DeepEqual(sortedKeys(shown), sortedKeys(local)) {
			t.Errorf("CONTRIBUTING says `%s` runs %v and the Makefile runs %v",
				cmd, sortedKeys(shown), sortedKeys(local))
		}
		perCommand[cmd] = local
	}
	// And the two commands ask for the same things as each other. The split
	// exists to move busy, not to weaken it: a `-race` dropped from one line
	// — consistently, in both files — would otherwise pass every check above.
	if len(perCommand) == 2 && !reflect.DeepEqual(
		sortedKeys(perCommand[gateSweep]), sortedKeys(perCommand[gateBusy])) {
		t.Errorf("the sweep runs with %v and busy with %v; the split moves busy to a quiet "+
			"machine, and a flag one line has that the other lacks is a weaker gate wearing "+
			"the same name", sortedKeys(perCommand[gateSweep]), sortedKeys(perCommand[gateBusy]))
	}

	// The order is part of the gate: busy measures the machine, so it runs on
	// the machine the sweep has finished with. Swapped, both lines are still
	// found and every flag still matches — only the quiet is gone.
	recipe, rerr := recipeOf("test")(joinContinuations(strings.Split(makefile, "\n")))
	if rerr != nil {
		t.Fatalf("Makefile: %v", rerr)
	}
	sweepAt, busyAt := -1, -1
	for i, line := range recipe {
		if _, ok := afterCommand(line, gateSweep); ok {
			sweepAt = i
		}
		if _, ok := afterCommand(line, gateBusy); ok {
			busyAt = i
		}
	}
	if sweepAt < 0 || busyAt < 0 || sweepAt > busyAt {
		t.Errorf("the recipe runs the sweep at line %d and busy at line %d; busy measures CPU, "+
			"so it goes last, alone, after the packages that would otherwise be its competitors",
			sweepAt, busyAt)
	}
	// Counted over the lines that run something: recipeOf keeps blank lines
	// (they end nothing in make), and a blank is not a command.
	commands := 0
	for _, line := range recipe {
		if strings.TrimSpace(line) != "" {
			commands++
		}
	}
	if commands != 2 {
		t.Errorf("the test recipe runs %d commands; this check knows the two gate commands and "+
			"nothing else — a third line is one it cannot vouch for", commands)
	}

	var gates []string
	for _, line := range joinContinuations(strings.Split(ci, "\n")) {
		if rest, ok := afterCommand(line, "run: "+ciCommand); ok {
			gates = append(gates, rest)
		}
	}
	if len(gates) != 2 {
		t.Fatalf("ci.yml: looked for lines running `run: %s` and found %d of them. The workflow "+
			"runs the gate once per compiler, and the compilers are two; a lane added or lost is "+
			"taught here on purpose, not discovered later.", ciCommand, len(gates))
	}
	// `make test` takes nothing, so anything after it on the line is there to
	// change what the line does — ` || true` reports every run green, one word
	// on the end of a line this test's name promises to have read in full.
	for _, after := range gates {
		for _, word := range strings.Fields(after) {
			if strings.HasPrefix(word, "#") {
				break
			}
			t.Errorf("ci.yml runs the gate with %q written after it. Whatever that does — "+
				"`|| true` turns every red run green — the step is no longer the bare gate. "+
				"Run `%s` alone, or teach this test the addition on purpose.",
				strings.TrimSpace(after), ciCommand)
			break
		}
	}

	// A direct `go test` line beside the gate would be a second lane, able to
	// drift weaker while the `make test` line above goes on being found.
	for _, line := range joinContinuations(strings.Split(ci, "\n")) {
		if _, ok := afterCommand(line, "run: go test"); ok {
			t.Errorf("ci.yml runs go test directly: %s\nThe workflow runs the gate (`%s`) so that "+
				"there is one definition of it; a second lane is one nothing here compares.",
				strings.TrimSpace(line), ciCommand)
		}
	}

}

// A cached "ok" is a report about a previous run: sound about the code, silent
// about the environment, and printed in the same green as a run that happened.
// Before -count=1 a one-file change left 12 packages unrun locally, and CI
// answered for 2 packages out of a cache frozen weeks earlier. The comparison
// above cannot hold this line: it reads flag names, not values — for every
// other flag the name is the ask, but `-count=3` is not this ask — so the value
// is pinned here, in both places that write the recipe out.
func TestTheGateRunsColdEveryTime(t *testing.T) {
	root := repoRoot(t)
	for _, place := range []struct {
		path  string
		scope where
	}{
		{"Makefile", recipeOf("test")},
		{"CONTRIBUTING.md", everyLine},
	} {
		for _, cmd := range gateCommands {
			rest, err := restAfter(read(t, root, place.path), cmd, place.scope, place.path == "Makefile")
			if err != nil {
				t.Errorf("%s: %v", place.path, err)
				continue
			}
			cold := false
			for _, word := range strings.Fields(rest) {
				if strings.HasPrefix(word, "#") {
					break
				}
				if word == "-count=1" {
					cold = true
				}
			}
			if !cold {
				t.Errorf("%s: `%s` runs without -count=1, so a package unchanged since its last "+
					"run is reported from the cache instead of being run — green that means \"passed "+
					"then\", read as \"passed now\".", place.path, cmd)
			}
		}
	}
}

// The push-time sieve is a third place the gate runs — a clean container, per
// push — and its promises live in three files nothing else connects: the hook
// runs the sieve and nothing else, the sieve reads the committed tree as a
// bundle of HEAD and mounts nothing but its named caches, and what runs
// inside is the bare gate rather than a restatement of it. Read with the same
// machinery as the gate lines above — a known line, found exactly once, with
// nothing after it — because the first version of this test used substring
// checks, and a reviewer walked four one-word mutations straight through
// them: `exec make test -n` (a dry run that runs nothing), a bundle of
// HEAD~1, an extra bind mount beside the caches, and the hook's run line
// living on inside a comment.
func TestThePushSieveRunsTheGateOnTheCommittedTree(t *testing.T) {
	root := repoRoot(t)
	hookPath := filepath.Join(root, ".githooks", "pre-push")
	info, err := os.Stat(hookPath)
	if err != nil {
		t.Fatalf("no pre-push hook, so nothing runs the sieve at push time: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("the pre-push hook is not executable — git skips it silently, which is a sieve "+
			"that never runs reading as one that always passes (mode %v)", info.Mode())
	}
	bareOnce := func(file, command string, lines []string) {
		t.Helper()
		var found []string
		for _, line := range lines {
			if rest, ok := afterCommand(line, command); ok {
				found = append(found, rest)
			}
		}
		if len(found) != 1 {
			t.Errorf("%s: looked for a line running `%s` and found %d of them; if it was "+
				"rewritten, rewrite this with it", file, command, len(found))
			return
		}
		for _, word := range strings.Fields(found[0]) {
			if strings.HasPrefix(word, "#") {
				break
			}
			t.Errorf("%s: `%s` runs with %q written after it — `-n` alone turns the gate into "+
				"a dry run that runs nothing. Run it bare, or teach this test the addition.",
				file, command, strings.TrimSpace(found[0]))
			break
		}
	}
	bareOnce(".githooks/pre-push", "exec make sieve",
		joinContinuations(strings.Split(read(t, root, ".githooks/pre-push"), "\n")))
	bareOnce("sieve/run.sh", "exec make test",
		joinContinuations(strings.Split(read(t, root, "sieve/run.sh"), "\n")))

	recipe, err := recipeOf("sieve")(joinContinuations(strings.Split(read(t, root, "Makefile"), "\n")))
	if err != nil {
		t.Fatalf("Makefile: %v", err)
	}
	joined := strings.Join(recipe, "\n")
	// The bundle line, as a command with a known head: `HEAD~1` fails to match
	// (the command must be followed by a space or the pipe), so a sieve of the
	// wrong commit is a "found 0", not a quiet pass.
	bundles := 0
	for _, line := range recipe {
		if rest, ok := afterCommand(line, "git bundle create - HEAD"); ok {
			bundles++
			if !strings.HasPrefix(rest, " | docker run ") {
				t.Errorf("the bundle does not flow straight into docker run: %q — whatever sits "+
					"between could hand the container a different tree", strings.TrimSpace(line))
			}
		}
	}
	if bundles != 1 {
		t.Errorf("found %d lines bundling HEAD into the sieve, want exactly one — the sieve "+
			"reads the committed tree a push carries, and nothing else", bundles)
	}
	// Every mount is a named cache volume. A path from the host — bind mounts
	// generally — is both a way for state to leak in and a file-sharing layer
	// that has already lied to this Makefile once.
	for _, m := range regexp.MustCompile(`-v[ 	]+([^: 	]+):`).FindAllStringSubmatch(joined, -1) {
		if !strings.HasPrefix(m[1], "opossum-sieve-") {
			t.Errorf("the sieve mounts %q; only its own named cache volumes (opossum-sieve-*) "+
				"belong in the container — anything else carries this machine's state into a "+
				"run whose point is not having any", m[1])
		}
	}
	if !strings.Contains(joined, "sh sieve/run.sh") {
		t.Error("the sieve recipe does not run sieve/run.sh; the version and user assertions " +
			"and the gate live there")
	}
}

// The commands the gate runs, named in full, in the order it runs them. The
// wrapper is part of each: an earlier version accepted `go run <anything> go
// test`, so pointing the recipe at a different program — one that runs no
// tests at all — left this green while CONTRIBUTING went on saying otherwise.
//
// Two commands, not one, and the order is load-bearing. cmd/busy measures
// how much CPU the machine can give, and `go test ./...` runs packages in
// parallel — so the measuring package ran beside its own competitors, and on
// a two-core runner the arithmetic left the rest of the suite a 20%
// allowance it could not keep. The first command runs everything else; the
// second runs cmd/busy on the machine the first has just finished with.
// Busy last, alone: swapped, the quiet it measures is gone again.
const (
	gateSweep = "go run ./cmd/noleftovers go test $$(go list ./... | grep -v '/cmd/busy$$')"
	gateBusy  = "go run ./cmd/noleftovers go test ./cmd/busy"
	ciCommand = "make test"
)

// gateCommands is the gate, in running order. Every check below that asks
// "does this file run the gate" asks it per command: each present exactly
// once, each with the same flags, busy's line after the sweep's.
var gateCommands = []string{gateSweep, gateBusy}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("cannot read %s, so nothing can be concluded: %v", rel, err)
	}
	return string(body)
}

// where narrows a file to the lines that can hold the gate. everyLine is for
// files with no such structure; recipeOf is for the Makefile, where a line only
// runs as part of `make test` if it sits under that target — an earlier version
// read the whole file, so a wrapper line under `cover:` stood in for the gate
// while `make test` ran the bare command this change exists to stop.
type where func(lines []string) ([]string, error)

func everyLine(lines []string) ([]string, error) { return lines, nil }

func recipeOf(target string) where {
	return func(lines []string) ([]string, error) {
		var recipe []string
		in := false
		for _, line := range lines {
			switch {
			case strings.HasPrefix(line, target+":"):
				in = true
			case in && !strings.HasPrefix(line, "\t") && strings.TrimSpace(line) != "":
				in = false
			case in:
				recipe = append(recipe, line)
			}
		}
		if len(recipe) == 0 {
			return nil, fmt.Errorf("no recipe under `%s:`, so nothing was read. If the target "+
				"was renamed, rename it here too", target)
		}
		return recipe, nil
	}
}

// flagsAfter finds the one line that runs the given command and returns the
// flags written after it. Not "the first such line": if there are two, which one
// is the gate is a guess, and guessing is how a sentence of prose came to stand
// in for the block people actually type.
func flagsAfter(body, command string, scope where, isMakefile bool) (map[string]bool, error) {
	rest, err := restAfter(body, command, scope, isMakefile)
	if err != nil {
		return nil, err
	}
	return flagsIn(rest), nil
}

// restAfter is the finding half: the one line, and what follows the command on
// it, before any reading of flags.
func restAfter(body, command string, scope where, isMakefile bool) (string, error) {
	lines, err := scope(joinContinuations(strings.Split(body, "\n")))
	if err != nil {
		return "", err
	}
	var found []string
	for _, line := range lines {
		rest, ok := afterCommand(line, command)
		if !ok {
			continue
		}
		// `-` in front of a recipe line tells make to carry on whatever
		// happens. It means nothing in YAML or markdown, where the same
		// character starts a list — and pages.yml already writes `- run:`.
		if isMakefile && ignoresFailure(line) {
			return "", fmt.Errorf("the gate runs with a leading `-`, so make reports success "+
				"whatever the tests do: %s", strings.TrimSpace(line))
		}
		found = append(found, rest)
	}
	if len(found) != 1 {
		return "", fmt.Errorf("looked for a line running `%s` and found %d of them. This "+
			"compares one line against another, so it will not pick; if the gate was rewritten, "+
			"rewrite this with it", command, len(found))
	}
	return found[0], nil
}

// joinContinuations puts a wrapped recipe back together. make does, and an
// earlier version did not — so writing the flags on the next line reported them
// as missing, which is a true "not on that line" and a false "not asked for".
func joinContinuations(lines []string) []string {
	var out []string
	pending := ""
	for _, line := range lines {
		if strings.HasSuffix(line, "\\") {
			pending += strings.TrimSuffix(line, "\\") + " "
			continue
		}
		out = append(out, pending+line)
		pending = ""
	}
	if pending != "" {
		out = append(out, pending)
	}
	return out
}

// afterCommand says whether the line runs command, and hands back what follows.
// Leading whitespace and make's recipe prefixes are dropped; nothing else is.
func afterCommand(line, command string) (string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	trimmed = strings.TrimLeft(trimmed, "@-+")
	trimmed = strings.TrimLeft(trimmed, " \t")
	if !strings.HasPrefix(trimmed, command) {
		return "", false
	}
	rest := trimmed[len(command):]
	if rest != "" && !strings.HasPrefix(rest, " ") && !strings.HasPrefix(rest, "\t") {
		return "", false // `go test ./...x`, which is a different command
	}
	return rest, true
}

func ignoresFailure(line string) bool {
	return strings.HasPrefix(strings.TrimLeft(line, " \t"), "-")
}

// flagsIn reads what was written after the command. It stops at a comment: the
// shell does, and an earlier version of this simplification forgot to, which put
// `# -cover is for make cover` back into the count while make went on not
// passing it. Splitting on fields first means every kind of space separates the
// comment, including the ideographic one that walked past a byte comparison.
func flagsIn(rest string) map[string]bool {
	flags := map[string]bool{}
	for _, word := range strings.Fields(rest) {
		if strings.HasPrefix(word, "#") {
			break
		}
		if !strings.HasPrefix(word, "-") {
			continue
		}
		// -cover and -coverprofile=x are different asks; the name is what is
		// compared, so a value does not make them differ.
		if e := strings.Index(word, "="); e >= 0 {
			word = word[:e]
		}
		flags[word] = true
	}
	return flags
}

func sortedKeys(m map[string]bool) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// And the reader itself, on lines written out here rather than on the repo's own
// files — the repo has one shape, and every way of writing the same thing that
// it does not happen to use is a way of walking past this. Several of these rows
// are shapes that an earlier version read wrongly.
func TestWhatCountsAsRunningTheGate(t *testing.T) {
	for _, tc := range []struct {
		name, line, command, wantRest string
		wantOK                        bool
	}{
		{"the sweep line as it stands", "\tgo run ./cmd/noleftovers go test $$(go list ./... | grep -v '/cmd/busy$$') -race -cover -count=1",
			gateSweep, " -race -cover -count=1", true},
		{"the busy line as it stands", "\tgo run ./cmd/noleftovers go test ./cmd/busy -race -cover -count=1",
			gateBusy, " -race -cover -count=1", true},
		{"the workflow line", "        run: make test",
			"run: " + ciCommand, "", true},
		{"a wrapped recipe is still this line", "\tgo run ./cmd/noleftovers go test ./cmd/busy \\",
			gateBusy, " \\", true},
		{"the silence prefix is make's, not the command's", "\t@go run ./cmd/noleftovers go test ./cmd/busy -race",
			gateBusy, " -race", true},
		// busy's package path must not satisfy the sweep's command, or one
		// line could stand in for both.
		{"the busy line is not the sweep line", "\tgo run ./cmd/noleftovers go test ./cmd/busy -race",
			gateSweep, "", false},
		// Everything below is prose about the gate, and none of it is the gate.
		{"a comment", "\t# go run ./cmd/noleftovers go test ./... -race -cover", gateBusy, "", false},
		{"an echo", "\t@echo go run ./cmd/noleftovers go test ./... -race -cover", gateBusy, "", false},
		{"an echo with a space after @", "\t@ echo the gate runs go test -race -cover", gateBusy, "", false},
		{"the null command", "\t: go test -race -cover", gateBusy, "", false},
		{"printf", "\t@printf '%s' 'go test -race -cover'", gateBusy, "", false},
		{"a sentence", "The gate is go run ./cmd/noleftovers go test ./... -race -cover", gateBusy, "", false},
		{"a different wrapper", "\tgo run ./cmd/busy go test ./... -race -cover", gateBusy, "", false},
		{"a longer package path", "\tgo run ./cmd/noleftovers go test ./...x -race", gateBusy, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rest, ok := afterCommand(tc.line, tc.command)
			if ok != tc.wantOK {
				t.Fatalf("runs the gate = %v, want %v (line %q)", ok, tc.wantOK, tc.line)
			}
			if ok && rest != tc.wantRest {
				t.Errorf("what follows = %q, want %q", rest, tc.wantRest)
			}
		})
	}
}

// The gate is also a thing people are told to run, and telling them the old one
// leaves them running the weaker check no matter what the Makefile says. An
// earlier version of this change moved the Makefile and left four documents
// saying `go test ./...` under headings like "Before pushing".
//
// Written as a refusal rather than a search for one spelling: any tracked
// markdown line that starts by running `go test` is refused, whatever follows
// it. The version before this looked for the exact three words inside a ```
// fence, and was walked past by a trailing comment, by an ideographic space
// before that comment, by a `~~~` fence, by a four-space indented block, and by
// `go test ./... -cover` — which is a weaker gate spelled differently.
//
// **What it does not reach:** a line that runs the weaker gate through
// something else (`sh -c "go test ./..."`), which nothing in the tree does; and
// prose that mentions the command without a line starting with it, which
// several places do — CONTRIBUTING talks about `go test` at lines 98, 142, 148
// and 300 without telling anyone to type it, and none of that is read here.
func TestNoDocumentTellsPeopleToRunTheGateDirectly(t *testing.T) {
	root := repoRoot(t)
	out, err := exec.Command("git", "-C", root, "ls-files", "-z", "*.md").Output()
	if err != nil {
		t.Skipf("git ls-files unavailable (not a checkout?): %v", err)
	}
	paths := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	looked := 0
	for _, p := range paths {
		if p == "" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			continue // tracked but absent; not this test's business
		}
		looked++
		for n, line := range strings.Split(string(body), "\n") {
			if startsByRunningGoTest(line) {
				t.Errorf("%s:%d has someone run `go test` directly: %s\n`make test` is the gate — "+
					"it adds -race and -cover, which CI runs anyway, and the check for what the "+
					"run leaves behind.", p, n+1, strings.TrimSpace(line))
			}
		}
	}
	if looked == 0 {
		t.Fatal("no tracked markdown was read, so nothing was checked")
	}
}

// startsByRunningGoTest says whether a markdown line has someone run `go test`.
//
// Two lists were tried and both were wrong. Stripping a prompt meant listing
// prompts, and `$` and `>` left `%`, `❯`, a `-` bullet, a `|` table cell and a
// pair of backticks all reading as prose. And requiring nothing after `go test`
// meant an English sentence starting "go test is slower than it looks" read as
// an instruction.
//
// So: drop leading decoration — anything that is not a letter or a digit, plus
// any NAME=value in front — and then ask whether what follows has the shape of
// running it: a flag or a path where the packages go. A sentence has a verb
// there instead. `#` is the exception, because it changes what the line is
// rather than how it is dressed.
func startsByRunningGoTest(line string) bool {
	// A `#` at the front is a heading or a comment, never a thing to type. It is
	// the one piece of decoration that changes what the line means.
	if strings.HasPrefix(strings.TrimSpace(line), "#") {
		return false
	}
	f := strings.Fields(strings.TrimLeftFunc(line, func(c rune) bool {
		return !unicode.IsLetter(c) && !unicode.IsDigit(c)
	}))
	for len(f) > 0 && isAssignment(f[0]) {
		f = f[1:]
	}
	if len(f) < 3 || f[0] != "go" || f[1] != "test" {
		return false
	}
	arg := f[2]
	return strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, ".") || strings.HasPrefix(arg, "/")
}

func isAssignment(word string) bool {
	i := strings.Index(word, "=")
	if i <= 0 {
		return false
	}
	for _, c := range word[:i] {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' {
			return false
		}
	}
	return true
}

// recipeOf is what ties the gate line to `make test`. Without it the check reads
// the whole Makefile, and a wrapper line under any other target stands in for
// the gate — which is how a tree whose `make test` ran the bare command passed a
// version of this. The constraint was checked by hand when it went in and by
// nothing afterwards, so it is checked here.
func TestARecipeIsOnlyTheLinesUnderItsOwnTarget(t *testing.T) {
	body := strings.Join([]string{
		"test: ## the gate",
		"\tgo test ./...",
		"",
		"cover: ## coverage",
		"\tgo run ./cmd/noleftovers go test ./cmd/busy -race -cover",
	}, "\n")

	lines, err := recipeOf("test")(strings.Split(body, "\n"))
	if err != nil {
		t.Fatalf("reading the `test` recipe: %v", err)
	}
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "go test ./...") {
		t.Errorf("the `test` recipe is missing its own line:\n%s", got)
	}
	if strings.Contains(got, "cmd/noleftovers") {
		t.Errorf("a line under `cover:` was read as part of `test`, so the gate could be "+
			"anywhere in the file:\n%s", got)
	}

	if _, err := recipeOf("nosuchtarget")(strings.Split(body, "\n")); err == nil {
		t.Error("a target that is not there read as an empty recipe rather than as a question")
	}

	// And the whole read, on that same Makefile: `make test` there runs the bare
	// command, so the gate has to come back as "not found" rather than as the
	// flags on the line under `cover:`.
	if _, err := flagsAfter(body, gateBusy, recipeOf("test"), true); err == nil {
		t.Error("a Makefile whose `make test` runs the bare command read as a gate with flags")
	}
	// The same body read without the scope is the mistake this guards, and it
	// finds the wrapper under `cover:` — which is what made it look fine.
	if _, err := flagsAfter(body, gateBusy, everyLine, true); err != nil {
		t.Errorf("this is the reading that used to pass; if it no longer finds anything, the "+
			"fixture stopped standing for the mistake: %v", err)
	}
}

// The spellings that were actually in the tree, and the ones near them.
func TestWhatCountsAsTellingSomeoneToRunItDirectly(t *testing.T) {
	for _, tc := range []struct {
		line string
		want bool
	}{
		{"go test ./...     # the regression gate", true},
		{"go test ./...        # 回帰ゲート", true},
		{"go test ./...", true},
		{"go test ./...　# 回帰ゲート", true},
		{"    go test ./...", true},
		{"$ go test ./...", true},
		{"go test ./... -race -cover", true},
		{"go test ./internal/site/", true},
		{"% go test ./...", true},
		{"❯ go test ./...", true},
		{"- go test ./...", true},
		{"| go test ./... |", true},
		{"`go test ./...`", true},
		{"GOFLAGS=-count=1 go test ./...", true},
		{"make test", false},
		{"go run ./cmd/noleftovers go test ./... -race -cover", false},
		{"# go test ./...", false},
		{"- **The suite.** `go test ./...` is minutes", false},
		{"go test is slower than it looks, so we wrap it.", false},
	} {
		t.Run(tc.line, func(t *testing.T) {
			if got := startsByRunningGoTest(tc.line); got != tc.want {
				t.Errorf("startsByRunningGoTest(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}
