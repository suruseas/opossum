package runtime_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The error-wording table says how far anyone actually checked, and this holds it
// to that.
//
// Each row's two verdict cells begin with one of three words: `raw:<file>` for a
// saved capture, `path-tried:<file>` for a route someone tried, `unverified` for
// one nobody did. For `raw:`, every wording the row lists has to appear in the
// capture it names.
//
// Every one of those was added after the version before it was shown to pass on
// something it should not have: a capture that showed nothing of the kind, prose
// in place of a verdict, one wording out of three matching by accident on a word
// as short as `MB`, a path climbing out of the capture directory to quote the
// table itself. The rule of thumb was that each version prevented citing nothing,
// not claiming more than was seen.
//
// What it still does not check, so that nobody reads more into a green run:
//
//   - Whether a capture is what the machine actually printed. Nothing signs them,
//     and they carry redactions already, so an edited one and a fabricated one
//     look the same from here.
//   - Whether a wording matched the line it was supposed to. A word as short as
//     `in use` appears in captures about other things, and any of them will do.
//   - What the row says about where the wording is matched, or which diagnostic
//     it belongs to. Those columns are read by people only.
//   - Whether a signature added to the code got a row. The count below notices a
//     row that goes missing; nothing here reads the code, so a new one is nobody's
//     job to add.
//   - What `path-tried:` files show. Only that they exist.
//   - Whether the wordings a row lists are still the ones the code matches on.
//     Nothing here reads the code, so a row can be thinned down to one short
//     word and stay green — from here that edit looks the same as the ones that
//     removed `RECLAIMABLE` and `builder status` for cause.
//   - Whether a capture's matching line came from upstream or from opossum.
//     Dropping `#` and `$ ` lines removes recipes and typed commands, not the
//     warnings opossum printed into the capture itself, so a row can cite
//     opossum's own sentence as the upstream wording.
//   - Whether the file a row cites is a capture. `.txt` is a name, not a
//     property: hand-written lines sit inside these files too, and an index
//     saved under that extension would be citable like any other.
//   - What a narrowing note claims. Its shape is checked; the claim inside it is
//     not, so a note in that shape can widen what the verdict says.
//   - Whether a row was deleted along with the code it described. The count below
//     catches a bare deletion; a deletion with the count edited to match is a
//     deliberate act, and this is not a defence against those.
//
// The table's own prose counts the captures kept for the image-side signatures,
// which are recorded but deliberately not table rows (their upstream is an image,
// so what has to be written down is a version, and that shape is still being
// settled). A count written by hand went wrong twice in one afternoon, so it is
// read back from the directory here.
func TestTheCaptureCountsInTheProseAreTheOnesOnDisk(t *testing.T) {
	const table = "../../testdata/real-cli-output.md"
	const captures = "../../testdata/error-wordings"
	md, err := os.ReadFile(table)
	if err != nil {
		t.Fatal(err)
	}
	claim := regexp.MustCompile("`(pg[0-9]+)-\\*\\.txt` ([0-9]+)件")
	counted := map[string]string{}
	for _, m := range claim.FindAllStringSubmatch(string(md), -1) {
		counted[m[1]] = m[2]
	}
	// Every prefix on disk has to be counted, not just the ones still written in the
	// shape this looks for. Checking only what matched let one of the two be
	// reworded out of the pattern while the other kept the test green.
	all, err := filepath.Glob(filepath.Join(captures, "pg*-*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	onDisk := map[string]int{}
	prefix := regexp.MustCompile(`^(pg[0-9]+)-`)
	for _, f := range all {
		if m := prefix.FindStringSubmatch(filepath.Base(f)); m != nil {
			onDisk[m[1]]++
		}
	}
	if len(onDisk) == 0 {
		t.Fatal("no image-side captures on disk; delete this test or restore them")
	}
	// And the other way: a count for a prefix nothing on disk starts with is a
	// sentence about captures that do not exist.
	for p := range counted {
		if _, ok := onDisk[p]; !ok {
			t.Errorf("the table's prose counts captures starting with %q; the directory holds none", p+"-")
		}
	}
	for p, want := range onDisk {
		got, ok := counted[p]
		if !ok {
			t.Errorf("the directory holds %d captures starting with %q and the table's prose "+
				"counts none of them — a count that is not written cannot go stale, and cannot be read either",
				want, p+"-")
			continue
		}
		if got != strconv.Itoa(want) {
			t.Errorf("the prose says %s captures start with %q, the directory holds %d", got, p+"-", want)
		}
	}
}

func TestTheWordingTableSaysOnlyWhatWasChecked(t *testing.T) {
	const table = "../../testdata/real-cli-output.md"
	const captures = "../../testdata/error-wordings"
	// Every signature in the census has a row. Changing this number is how you say
	// the census changed; a row that goes missing on its own trips it.
	const wantRows = 12

	md, err := os.ReadFile(table)
	if err != nil {
		t.Fatal(err)
	}
	rows, malformed := wordingRows(string(md))
	for _, line := range malformed {
		// Dropped rows are the quiet failure this whole table is about: a stray `|`
		// in a cell would leave the row looking checked and checking nothing.
		t.Errorf("this row has the wrong number of cells, so it would be skipped: %s", line)
	}
	if len(rows) != wantRows {
		t.Fatalf("the table has %d rows, expected %d — a row that disappeared takes its "+
			"signature out of the census without anything else noticing", len(rows), wantRows)
	}
	quoted := regexp.MustCompile("`([^`]+)`")
	// A capture, not any file that happens to sit beside them. The index in that
	// directory quotes every wording in the table, so `raw:README.md` would let any
	// row cite it and pass — the same self-quoting that reaching `../` allowed.
	cite := regexp.MustCompile(`^(raw|path-tried):([^/\s（(]+\.txt)$`)
	// The note may only narrow, in one shape, so that a reader cannot be told the
	// opposite of what the verdict says.
	narrows := regexp.MustCompile(`^[^。]+のみ。[^。]+は unverified$`)
	for _, cells := range rows {
		// | # | 診断 | 場所 | 文言 | 経路 | 上流の文言 |
		wording, verdicts := cells[3], cells[4:]
		for i, cell := range verdicts {
			t.Run(cells[0]+"-"+[]string{"path", "upstream"}[i], func(t *testing.T) {
				norm := strings.NewReplacer("(", "（", ")", "）").Replace(cell)
				head, note, hasNote := strings.Cut(norm, "（")
				v := strings.TrimSpace(strings.Trim(head, "`* "))
				if hasNote {
					// The note may only narrow. Anything a reader could take as more
					// coverage has to be a row of its own, or it is a claim nobody
					// checked riding along with one they did.
					note = strings.TrimSuffix(strings.TrimSpace(note), "）")
					if !narrows.MatchString(note) {
						t.Fatalf("the note on %q has to say what is left out, in the form "+
							"`（X のみ。Y は unverified）`. A note that reads as more coverage "+
							"is the thing this column exists to stop: %q", v, note)
					}
				}
				if v == "unverified" {
					return
				}
				m := cite.FindStringSubmatch(v)
				if m == nil {
					t.Fatalf("verdict %q is not one of raw:<file>, path-tried:<file>, unverified "+
						"— and a file here names one in %s, nothing further away", cell, captures)
				}
				b, err := os.ReadFile(filepath.Join(captures, m[2]))
				if err != nil {
					t.Fatalf("%s names a capture that is not there: %v", v, err)
				}
				if m[1] != "raw" {
					return
				}
				// Only what the machine printed. The captures carry a header naming
				// the recipe and the command, written by hand — a wording matched
				// there would be this table quoting itself back.
				var printed []string
				for _, line := range strings.Split(string(b), "\n") {
					if t := strings.TrimSpace(line); strings.HasPrefix(t, "#") || strings.HasPrefix(t, "$ ") {
						continue
					}
					printed = append(printed, line)
				}
				body := strings.Join(printed, "\n")
				want := quoted.FindAllStringSubmatch(wording, -1)
				if len(want) == 0 {
					t.Fatalf("row lists no wording, so there is nothing to look for")
				}
				for _, w := range want {
					if !strings.Contains(body, w[1]) {
						t.Errorf("%s does not show %q — a capture cited for a wording it does "+
							"not contain is a claim with nothing behind it", v, w[1])
					}
				}
			})
		}
	}
}

// wordingRows returns the table's data rows split into cells, and separately any
// row it could not split. It finds the table by its header rather than by
// position, so moving the section does not leave this checking nothing.
func wordingRows(md string) (rows [][]string, malformed []string) {
	inTable := false
	for _, line := range strings.Split(md, "\n") {
		switch {
		case strings.HasPrefix(line, "| # | 診断 |"):
			inTable = true
		case inTable && !strings.HasPrefix(line, "|"):
			inTable = false
		case inTable && strings.HasPrefix(line, "|---"):
		case inTable:
			cells := strings.Split(strings.Trim(line, "|"), "|")
			if len(cells) != 6 {
				malformed = append(malformed, line)
				continue
			}
			for i := range cells {
				cells[i] = strings.TrimSpace(cells[i])
			}
			rows = append(rows, cells)
		}
	}
	return rows, malformed
}
