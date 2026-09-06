package repohygiene

// Public documents assert what Apple `container` does — refuses a symlinked
// socket, mounts a volume holding lost+found, exposes no aliases. Every such
// sentence was true of some version, measured in some shape; five times a
// sentence generalised past the shape that was measured, and three of those
// were in public text (#408). A version, or a condition that names the shape,
// is what keeps the sentence honest when the runtime moves.
//
// This is a ratchet: the sentences already there are listed in a baseline and
// tolerated; a new sentence that asserts the runtime's behaviour without a
// version or a condition on its line is refused. Fixing an old one shrinks the
// baseline (a listed line that is no longer unqualified is reported, so the
// baseline is kept true). The vocabulary is a starting point — subjects that
// name the runtime, verbs that assert — and is meant to be narrowed if it
// fires on a sentence about opossum's own behaviour, never bent around.

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// runtimeAssertion is a sentence whose subject is the runtime and whose verb
// asserts what it does.
var runtimeAssertion = regexp.MustCompile("(?i)(Apple `container`|the runtime|`container`)(?:'s)? (doesn'?t|does not|refuses|can'?t|cannot|mounts|exposes|requires|has no|won'?t|will not|leaves|reports|starts|creates|attaches|provides|hands|serves|resolves|reads|keeps|drops|ignores|takes)\\b")

// qualified is what lets an assertion through: a version, a date-ish anchor,
// or a word that limits the shape.
var qualified = regexp.MustCompile(`(?i)\b\d+\.\d+\.\d+\b|macOS \d+|\bas of\b|\bmeasured\b|\bobserved\b|\bwhen\b|\bonly\b|\bif\b|\bunless\b|\bwhere\b|\bsince\b|\bon \d{4}-\d{2}-\d{2}`)

const runtimeAssertionBaseline = "testdata/runtime-assertions-baseline.txt"

func unqualifiedRuntimeAssertions(t *testing.T, root string) map[string]bool {
	t.Helper()
	var files []string
	for _, pat := range []string{"docs/*.md", "README.md", "README.ja.md", "AGENTS.md", "changelog.d/*.md"} {
		m, _ := filepath.Glob(filepath.Join(root, pat))
		files = append(files, m...)
	}
	sort.Strings(files)
	found := map[string]bool{}
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, f)
		for _, line := range strings.Split(string(body), "\n") {
			if runtimeAssertion.MatchString(line) && !qualified.MatchString(line) {
				found[rel+": "+strings.TrimSpace(line)] = true
			}
		}
	}
	return found
}

func readBaseline(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open(runtimeAssertionBaseline)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]bool{}
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1<<20), 1<<20)
	for s.Scan() {
		line := s.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[line] = true
	}
	return out
}

func TestANewRuntimeAssertionInPublicDocsCarriesAVersionOrACondition(t *testing.T) {
	found := unqualifiedRuntimeAssertions(t, "../..")
	baseline := readBaseline(t)
	var fresh, gone []string
	for k := range found {
		if !baseline[k] {
			fresh = append(fresh, k)
		}
	}
	for k := range baseline {
		if !found[k] {
			gone = append(gone, k)
		}
	}
	sort.Strings(fresh)
	sort.Strings(gone)
	for _, k := range fresh {
		t.Errorf("a sentence asserts what the runtime does with no version or condition on its line — say which version it was measured on, or the shape it holds for:\n  %s", k)
	}
	for _, k := range gone {
		t.Errorf("listed in %s but no longer an unqualified assertion (fixed, moved, or reworded) — remove it from the baseline so the ratchet only tightens:\n  %s", runtimeAssertionBaseline, k)
	}
}

// The vocabulary, on sentences written for it: the subject and the verb both
// have to be there, a version or a condition lets it through, and a sentence
// about opossum's own behaviour is not the runtime's.
func TestWhatCountsAsARuntimeAssertion(t *testing.T) {
	for _, tc := range []struct {
		line string
		hit  bool // matched as an assertion
		ok   bool // and qualified
	}{
		{"Apple `container` doesn't provide it.", true, false},
		{"the runtime refuses any bind whose source is a symlink to a socket.", true, false},
		{"`container` mounts a volume holding `lost+found` (measured on 1.3.1).", true, true},
		{"Apple `container` requires a host port when publishing, so opossum picks one.", true, true},
		{"Apple `container` requires a host port, so opossum picks one.", true, false},
		{"On macOS 26 the runtime resolves names on the default network.", true, true},
		{"`container` reports no exit code only for a detached run.", true, true},
		{"opossum refuses a service with `extends:`.", false, false},
		{"opossum's `up` refuses before starting anything and names the port.", false, false},
		{"The `container` CLI's help lists `--publish`.", false, false},
	} {
		hit := runtimeAssertion.MatchString(tc.line)
		if hit != tc.hit {
			t.Errorf("%q: matched=%v, want %v", tc.line, hit, tc.hit)
		}
		if hit && qualified.MatchString(tc.line) != tc.ok {
			t.Errorf("%q: qualified=%v, want %v", tc.line, !tc.ok, tc.ok)
		}
	}
}
