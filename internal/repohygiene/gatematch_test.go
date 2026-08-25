package repohygiene_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
//   - Flags handed over some other way. `GOFLAGS: -race -cover` in the
//     workflow's `env:` asks for them without writing them on the line, and
//     nothing here sees that. It counts too low, which would let CI drift ahead
//     of the local gate unnoticed.
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
func TestTheLocalGateAsksForAtLeastWhatCIAsksFor(t *testing.T) {
	root := repoRoot(t)
	// All three are read before anything is decided, so one file in an
	// unexpected shape does not hide what the other two say.
	local, err1 := flagsAfter(read(t, root, "Makefile"), gateCommand, recipeOf("test"), true)
	remote, err2 := flagsAfter(read(t, root, ".github/workflows/ci.yml"), "run: "+ciCommand, everyLine, false)
	shown, err3 := flagsAfter(read(t, root, "CONTRIBUTING.md"), gateCommand, everyLine, false)
	bad := false
	for path, err := range map[string]error{
		"Makefile":                 err1,
		".github/workflows/ci.yml": err2,
		"CONTRIBUTING.md":          err3,
	} {
		if err != nil {
			t.Errorf("%s: %v", path, err)
			bad = true
		}
	}
	if bad {
		t.FailNow()
	}

	var missing []string
	for f := range remote {
		if !local[f] {
			missing = append(missing, f)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("CI asks for %v and `make test` does not: a fault they catch is one that only "+
			"turns up after a push, and until then the local run reads as though it had been "+
			"checked for. Add them to the `test` recipe in the Makefile.", missing)
	}

	// CONTRIBUTING shows the recipe rather than pointing at it, so there are two
	// places to change. A change that exists to stop two places drifting apart
	// should not quietly leave a third behind.
	if !reflect.DeepEqual(sortedKeys(shown), sortedKeys(local)) {
		t.Errorf("CONTRIBUTING says the gate runs %v and the Makefile runs %v",
			sortedKeys(shown), sortedKeys(local))
	}
}

// The command the gate runs, named in full. The wrapper is part of it: an
// earlier version accepted `go run <anything> go test`, so pointing the recipe
// at a different program — one that runs no tests at all — left this green while
// CONTRIBUTING went on saying otherwise.
const (
	gateCommand = "go run ./cmd/noleftovers go test ./..."
	ciCommand   = "go test ./..."
)

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
	lines, err := scope(joinContinuations(strings.Split(body, "\n")))
	if err != nil {
		return nil, err
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
			return nil, fmt.Errorf("the gate runs with a leading `-`, so make reports success "+
				"whatever the tests do: %s", strings.TrimSpace(line))
		}
		found = append(found, rest)
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("looked for a line running `%s` and found %d of them. This "+
			"compares one line against another, so it will not pick; if the gate was rewritten, "+
			"rewrite this with it", command, len(found))
	}
	return flagsIn(found[0]), nil
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
		{"the recipe as it stands", "\tgo run ./cmd/noleftovers go test ./... -race -cover",
			gateCommand, " -race -cover", true},
		{"the workflow line", "        run: go test ./... -race -cover",
			"run: " + ciCommand, " -race -cover", true},
		{"a wrapped recipe is still this line", "\tgo run ./cmd/noleftovers go test ./... \\",
			gateCommand, " \\", true},
		{"the silence prefix is make's, not the command's", "\t@go run ./cmd/noleftovers go test ./... -race",
			gateCommand, " -race", true},
		// Everything below is prose about the gate, and none of it is the gate.
		{"a comment", "\t# go run ./cmd/noleftovers go test ./... -race -cover", gateCommand, "", false},
		{"an echo", "\t@echo go run ./cmd/noleftovers go test ./... -race -cover", gateCommand, "", false},
		{"an echo with a space after @", "\t@ echo the gate runs go test -race -cover", gateCommand, "", false},
		{"the null command", "\t: go test -race -cover", gateCommand, "", false},
		{"printf", "\t@printf '%s' 'go test -race -cover'", gateCommand, "", false},
		{"a sentence", "The gate is go run ./cmd/noleftovers go test ./... -race -cover", gateCommand, "", false},
		{"a different wrapper", "\tgo run ./cmd/busy go test ./... -race -cover", gateCommand, "", false},
		{"a longer package path", "\tgo run ./cmd/noleftovers go test ./...x -race", gateCommand, "", false},
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
		"\tgo run ./cmd/noleftovers go test ./... -race -cover",
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
	if _, err := flagsAfter(body, gateCommand, recipeOf("test"), true); err == nil {
		t.Error("a Makefile whose `make test` runs the bare command read as a gate with flags")
	}
	// The same body read without the scope is the mistake this guards, and it
	// finds the wrapper under `cover:` — which is what made it look fine.
	if _, err := flagsAfter(body, gateCommand, everyLine, true); err != nil {
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
