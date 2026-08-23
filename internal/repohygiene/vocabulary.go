package repohygiene

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
)

// Some words belong to how this project is run rather than to what it makes:
// they name documents that are not in the repository and a way of organising work
// that a person reading this code has no reason to meet. Tracked files should not
// carry them, and a check enforcing that used to hold the list in the open — which
// meant the one place in the repository still carrying those words was the check
// itself, gathered conveniently in one spot.
//
// So the list is kept as digests. That is enough for what this is for — someone
// reading the repository does not meet the words — and it is worth being exact
// about what it is not. These are unsalted digests of short words, and the mark
// below is a hash of a single rune: an independent reviewer recovered three of
// the words from this file in under twenty seconds, and the rune space is small
// enough that the marks are effectively the first characters in the open. The
// requirement was that the check stop being the one place that spells them, and
// that is met. Hiding them from someone who sets out to find them was never on
// offer.
//
// What this cannot do, unchanged from before: it knows these words and no others.
// Prose that says the same thing without them passes, and always will. The list
// grows when something gets past it.
type WordSum struct {
	Runes int    // length of the word, in runes
	Sum   string // hex SHA-256 of the word, lowercased
	// Mark is a non-cryptographic hash of the word's first rune, so a scan can
	// skip almost every position without hashing it. It travels with the digest
	// because a digest cannot be asked what it starts with — an earlier version
	// kept the marks beside the list instead, and then a caller looking for its
	// own word found nothing, silently.
	Mark uint32
}

// FindWord returns the first stretch of text whose digest is in words, or "" when
// there is none. Matching is on runes and case-folded, so a word is found wherever
// it sits — these files are prose, and prose has no token boundaries to lean on.
// What comes back is the lowercased form.
//
// The returned text is the word itself. That is deliberate: the point is to keep
// it out of files, not out of a failure message that tells someone which one to
// remove.
func FindWord(content []byte, words []WordSum) string {
	// Nothing to look for: the loop below would answer the same way, position by
	// position, so this is speed and not a rule. (Said plainly because the last
	// time a branch here was called redundant, it was load-bearing.)
	if len(words) == 0 {
		return ""
	}
	// Grouped by mark rather than pooled: a position that matches one word's first
	// rune is only worth hashing at that word's lengths, not at every length in the
	// table. Sorted, so a text that could match at two lengths reports the same one
	// every run.
	lengths := map[uint32][]int{}
	sums := map[string]bool{}
	for _, w := range words {
		if !slices.Contains(lengths[w.Mark], w.Runes) {
			lengths[w.Mark] = append(lengths[w.Mark], w.Runes)
		}
		sums[w.Sum] = true
	}
	for m := range lengths {
		slices.Sort(lengths[m])
	}
	// Case-folded once, and the match is returned from the folded text: a word
	// found in a shouting headline comes back lowercase, which is what it is.
	text := []rune(strings.ToLower(string(content)))
	for i := range text {
		// Hashing every window at every position would mean tens of millions of
		// digests for this repository. A cheap mark on the first rune cuts it to
		// the positions that could start one of these words — measured at about an
		// eighth of them, since three of the words begin with letters English uses
		// constantly, which is a large cut and not a small set.
		at, ok := lengths[fnv32(text[i])]
		if !ok {
			continue
		}
		for _, n := range at {
			if i+n > len(text) {
				continue
			}
			candidate := string(text[i : i+n])
			sum := sha256.Sum256([]byte(candidate))
			if sums[hex.EncodeToString(sum[:])] {
				return candidate
			}
		}
	}
	return ""
}

// FindInternalWord is FindWord over the words this repository does not want to
// carry.
func FindInternalWord(content []byte) string {
	return FindWord(content, internalVocabularySums)
}

// VocabularySize is how many words that is, so a check can say it is looking for
// something rather than passing on an empty list.
func VocabularySize() int { return len(internalVocabularySums) }

func fnv32(r rune) uint32 {
	h := uint32(2166136261)
	for _, b := range []byte(string(r)) {
		h = (h ^ uint32(b)) * 16777619
	}
	return h
}

// internalVocabularySums are the digests. They are transcribed, not computed —
// computing them would need the words — and that is this table's weak point: a
// wrong digit anywhere in a row drops that word silently, and nothing inside this
// repository can tell, because checking would need the words too.
//
// Two things push back on it. The shape of every row is checked below, which
// catches a mangled paste rather than a plausible one. And a check that has the
// words — run where they are, named by OPOSSUM_VOCABULARY_FILE — regenerates the
// table and compares. That check cannot run in CI, and says so out loud rather
// than passing.
var internalVocabularySums = []WordSum{
	{Runes: 17, Sum: "fd102a4c17b8ae81e89bb5403136a54adbdc7dd56aea5258d1350a41dfa9457e", Mark: 3758891744},
	{Runes: 7, Sum: "da16acc778e695cd0fa877b9e7c76ffbcad226b0c860c5ec87641c5c2c560709", Mark: 3909890315},
	{Runes: 13, Sum: "d5b4a6a108a574220b1816c0d2a8ab208b0c874daf303a61d7adb85c891f6da9", Mark: 4111221743},
	{Runes: 4, Sum: "5d1f781ec69e2c77f478d376880ada5c8c53b4bfbbfd3cf613a9ce400fabfef0", Mark: 1462011969},
	{Runes: 5, Sum: "61c93983d8856164cf0c761e42a6a351dc93317220d35e8194e3d5cc8cd8953a", Mark: 1160014827},
	{Runes: 6, Sum: "2aae2dd4b50441d66e32ba68bb409d9e049950a9ea70b533aea9c38a358af2f1", Mark: 3072810488},
	{Runes: 2, Sum: "1c9919455e8335db5f480108598f9c6ceee8a501b4e8c5b26d2f48a2e5df9660", Mark: 335588717},
	{Runes: 4, Sum: "7b5ddf32f439ff333c081d3ab97e4d501ff1112321d88b28de9359f3344fda0b", Mark: 968414704},
}

// GenerateSums builds the table from a list of words, in the form this file
// carries it. It exists so the table can be regenerated and compared where the
// words are available, rather than transcribed again by hand.
func GenerateSums(words []string) []WordSum {
	out := make([]WordSum, 0, len(words))
	for _, w := range words {
		lower := strings.ToLower(strings.TrimSpace(w))
		if lower == "" {
			continue
		}
		sum := sha256.Sum256([]byte(lower))
		out = append(out, WordSum{
			Runes: len([]rune(lower)),
			Sum:   hex.EncodeToString(sum[:]),
			Mark:  fnv32([]rune(lower)[0]),
		})
	}
	return out
}

// InternalVocabulary is the table itself, for a check that has the words and can
// say whether it is still right.
func InternalVocabulary() []WordSum { return internalVocabularySums }
