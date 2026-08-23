package repohygiene_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/repohygiene"
)

// The words this check looks for are not written here, or anywhere else in the
// repository: the package holds their digests. What is written here is a word
// invented for the purpose, so the machinery can be shown to work on something
// nobody minds reading.
func sumOf(t *testing.T, word string) repohygiene.WordSum {
	t.Helper()
	lower := strings.ToLower(word)
	sum := sha256.Sum256([]byte(lower))
	mark := uint32(2166136261)
	for _, b := range []byte(string([]rune(lower)[0])) {
		mark = (mark ^ uint32(b)) * 16777619
	}
	return repohygiene.WordSum{Runes: len([]rune(lower)), Sum: hex.EncodeToString(sum[:]), Mark: mark}
}

func TestFindWordFindsAWordWhereverItSits(t *testing.T) {
	const made = "zzquux"
	words := []repohygiene.WordSum{sumOf(t, made)}
	for _, c := range []struct{ name, text, want string }{
		{"on its own", made, made},
		{"inside a sentence", "the " + made + " is here", made},
		{"with no spaces around it, the way prose in another script runs", "あ" + made + "い", made},
		{"shouted, and it comes back the way it is looked for", strings.ToUpper(made), made},
		{"absent", "nothing to see", ""},
		{"a prefix of it only", made[:3], ""},
	} {
		if got := repohygiene.FindWord([]byte(c.text), words); got != c.want {
			t.Errorf("%s: FindWord(%q) = %q, want %q", c.name, c.text, got, c.want)
		}
	}
	// A caller with nothing to look for finds nothing, rather than everything.
	if got := repohygiene.FindWord([]byte(made), nil); got != "" {
		t.Errorf("with no words to look for, nothing should be found, got %q", got)
	}
}

// The ratchet. Every tracked text file is read whole, and none of them may carry
// one of the words. It is green today with nothing exempt from it — including
// this check, which used to be the one file in the repository still spelling
// them.
func TestTrackedFilesAreWrittenForTheirReaders(t *testing.T) {
	root := repoRoot(t)
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("listing tracked files: %v", err)
	}
	names := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	if len(names) == 0 {
		t.Fatal("no tracked files; this check would pass on nothing")
	}
	if n := repohygiene.VocabularySize(); n < 5 {
		t.Fatalf("the check is looking for %d words; it has been emptied out", n)
	}
	looked := 0
	for _, name := range names {
		if !isText(name) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		looked++
		if w := repohygiene.FindInternalWord(raw); w != "" {
			t.Errorf("%s contains %q, which is about how this project is run rather than what it does; say it in the terms a reader of this repository has", name, w)
		}
	}
	if looked == 0 {
		t.Fatal("no tracked text files were read; this check is passing on nothing")
	}
}

// The table is transcribed, so the shape of every row is worth checking: a
// mangled paste is the failure this can see from inside the repository. What it
// cannot see is a plausible wrong digit, which drops a word and says nothing —
// that is what the check below is for, where the words are available.
func TestEveryRowOfTheTableHasTheShapeOfOne(t *testing.T) {
	table := repohygiene.InternalVocabulary()
	if len(table) < 8 {
		t.Fatalf("the table has %d rows; it has been emptied out", len(table))
	}
	seen := map[string]bool{}
	for i, w := range table {
		if w.Runes < 2 {
			t.Errorf("row %d claims a word of %d rune(s)", i, w.Runes)
		}
		if len(w.Sum) != 64 {
			t.Errorf("row %d has a digest of %d characters, not 64", i, len(w.Sum))
		}
		if _, err := hex.DecodeString(w.Sum); err != nil {
			t.Errorf("row %d has a digest that is not hex: %v", i, err)
		}
		if w.Mark == 0 {
			t.Errorf("row %d has no mark, so nothing will ever be compared against it", i)
		}
		if seen[w.Sum] {
			t.Errorf("row %d repeats a digest; one of the words is not in the table at all", i)
		}
		seen[w.Sum] = true
	}
}

// The one check that can tell whether the table is still right, and it needs the
// words. Point OPOSSUM_VOCABULARY_FILE at a file that has them, one per line, and
// this regenerates the table and compares. Without it there is nothing to compare
// against — so it says that, rather than passing.
func TestTheTableMatchesTheWordsWhereTheyAreAvailable(t *testing.T) {
	path := os.Getenv("OPOSSUM_VOCABULARY_FILE")
	if path == "" {
		t.Skip("no OPOSSUM_VOCABULARY_FILE: the table cannot be checked against the words here, only its shape")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the words named by OPOSSUM_VOCABULARY_FILE: %v", err)
	}
	var words []string
	for _, line := range strings.Split(string(b), "\n") {
		if w := strings.TrimSpace(line); w != "" && !strings.HasPrefix(w, "#") {
			words = append(words, w)
		}
	}
	want := repohygiene.GenerateSums(words)
	got := repohygiene.InternalVocabulary()
	if len(want) != len(got) {
		t.Fatalf("the table has %d rows and the words give %d", len(got), len(want))
	}
	index := map[string]repohygiene.WordSum{}
	for _, w := range got {
		index[w.Sum] = w
	}
	for i, w := range want {
		have, ok := index[w.Sum]
		if !ok {
			t.Errorf("word %d is not in the table at all", i)
			continue
		}
		if have != w {
			t.Errorf("word %d is in the table with the wrong shape: %+v, want %+v", i, have, w)
		}
	}
}

// isText keeps the check to the files a reader reads. A binary fixture cannot be
// rewritten for its audience, and reading one in full is wasted work.
func isText(name string) bool {
	switch filepath.Ext(name) {
	case ".md", ".go", ".yml", ".yaml", ".sh", ".txt", ".json", ".toml":
		return true
	}
	return false
}
