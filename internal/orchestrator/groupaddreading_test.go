package orchestrator_test

// Evals for the reading a refusal names when a `group_add` entry is past the
// largest gid. The entry is quoted as the file spells it and the reading
// opossum made of it is left bare, so a reader can tell which is which — but
// the reading is only worth saying where it differs from the spelling.
//
// What differs is decided by what `--gid` reads, not by the loader: a quoted
// entry reaches the runtime as those characters and is read there in decimal,
// so `"02147483647"` is handed over as written and is the group 2147483647
// (`--gid` reads `+2000` and `0002000` as 2000, measured on container 1.4.1
// on 2026-09-19). Nothing in the refusal shows that opossum read the string
// as a number, which is what the reading says.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

func TestARefusalSaysWhatAGroupPastTheLargestGidReadsAs(t *testing.T) {
	// Each row is the whole of the refusal from the spelling on, so a reading
	// that appears where it should not fails as loudly as one that is missing.
	for _, tc := range []struct{ name, list, want string }{
		// Unquoted, where the loader writes the number back in its decimal:
		// the spelling in the file and the reading differ, and both are said.
		{"hex", `[0x80000000]`,
			`adds the group "0x80000000" (group_add), which reads as the group 2147483648 and is past 2147483647`},
		{"octal", `[0o20000000000]`,
			`adds the group "0o20000000000" (group_add), which reads as the group 2147483648 and is past 2147483647`},
		{"digits grouped with underscores", `[2_147_483_648]`,
			`adds the group "2_147_483_648" (group_add), which reads as the group 2147483648 and is past 2147483647`},
		{"a plus", `[+2147483648]`,
			`adds the group "+2147483648" (group_add), which reads as the group 2147483648 and is past 2147483647`},
		// Quoted, where the string reaches `--gid` as those characters: the
		// spelling is not the number the runtime reads there either, so the
		// reading is as much use as it is above. The pair `[+2147483648]`
		// and `["+2147483648"]` differ in the file alone — the refusal quotes
		// the same characters for both — so a reading said for one and not
		// the other is a difference the reader cannot see.
		{"a plus, quoted", `["+2147483648"]`,
			`adds the group "+2147483648" (group_add), which reads as the group 2147483648 and is past 2147483647`},
		{"a leading zero, quoted", `["02147483648"]`,
			`adds the group "02147483648" (group_add), which reads as the group 2147483648 and is past 2147483647`},
		{"leading zeros, quoted", `["0002147483648"]`,
			`adds the group "0002147483648" (group_add), which reads as the group 2147483648 and is past 2147483647`},
		// Both at once, so the two are dropped in either order and the row
		// fails if one of them is left on the number: `+02147483648` read
		// with the zeros dropped first still carries its `+`, and with the
		// `+` dropped first still carries a zero.
		{"a plus and a leading zero, quoted", `["+02147483648"]`,
			`adds the group "+02147483648" (group_add), which reads as the group 2147483648 and is past 2147483647`},
		// Where the spelling is the number: nothing to add, quoted or not.
		{"digits", `[2147483648]`,
			`adds the group "2147483648" (group_add), which is past 2147483647`},
		{"digits, quoted", `["2147483648"]`,
			`adds the group "2147483648" (group_add), which is past 2147483647`},
		// Past what an integer holds, so the loader leaves the spelling
		// alone: the reading is still the decimal the runtime would read.
		{"past what an integer holds, quoted with a leading zero", `["018446744073709551615"]`,
			`adds the group "018446744073709551615" (group_add), which reads as the group 18446744073709551615 and is past 2147483647`},
		{"past what an integer holds, quoted with a plus", `["+18446744073709551615"]`,
			`adds the group "+18446744073709551615" (group_add), which reads as the group 18446744073709551615 and is past 2147483647`},
		// The same number where the spelling is the number: a reading is said
		// where the two differ, not wherever the number is large. Held beside
		// the two rows above, which differ from it in the file alone.
		{"past what an integer holds, in digits", `[18446744073709551615]`,
			`adds the group "18446744073709551615" (group_add), which is past 2147483647`},
		{"past what an integer holds, in digits, quoted", `["18446744073709551615"]`,
			`adds the group "18446744073709551615" (group_add), which is past 2147483647`},
		// Past 32 bits and past an integer are the same rule here.
		{"past 32 bits", `[0x100000000]`,
			`adds the group "0x100000000" (group_add), which reads as the group 4294967296 and is past 2147483647`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			p, err := loadProject(t, "services:\n  app:\n    image: alpine:3.20\n    group_add: "+tc.list+"\n")
			if err != nil {
				t.Fatal(err)
			}
			err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
			if err == nil {
				t.Fatal("want a refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want %q in:\n%v", tc.want, err)
			}
		})
	}
}

// An entry that never reaches the range at all is refused by the branch above
// it, and says nothing about a reading: the reading is part of the answer to
// "that number is too large", and no part of "that is not a number".
func TestAnEntryRefusedBeforeTheRangeSaysNothingAboutAReading(t *testing.T) {
	for _, tc := range []struct{ name, list, want string }{
		{"a hex string, which --gid cannot read", `["0x80000000"]`,
			`adds the group "0x80000000" (group_add), which is not the digits of a gid`},
		{"a negative number", `[-0x10]`,
			`adds the group "-0x10" (group_add); a gid is not negative`},
		{"a negative number, quoted", `["-0x10"]`,
			`adds the group "-0x10" (group_add); a gid is not negative`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			p, err := loadProject(t, "services:\n  app:\n    image: alpine:3.20\n    group_add: "+tc.list+"\n")
			if err != nil {
				t.Fatal(err)
			}
			err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
			if err == nil {
				t.Fatal("want a refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want %q in:\n%v", tc.want, err)
			}
			if strings.Contains(err.Error(), "reads as the group") {
				t.Errorf("want no reading named, got:\n%v", err)
			}
		})
	}
}

// A group inside the range starts, and the spelling the file wrote is what
// `--gid` is handed: opossum does not write the reading back in its place.
// This is why the reading is worth saying above — the characters handed over
// are the file's, and the number opossum read out of them appears nowhere
// else.
func TestAGroupInsideTheRangeIsHandedOverAsTheFileSpellsIt(t *testing.T) {
	for _, tc := range []struct{ name, list, want string }{
		{"digits", `[2147483647]`, "--gid 2147483647"},
		{"a leading zero, quoted", `["02147483647"]`, "--gid 02147483647"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, calls := fakeShim(t)
			p, err := loadProject(t, "services:\n  app:\n    image: alpine:3.20\n    group_add: "+tc.list+"\n")
			if err != nil {
				t.Fatal(err)
			}
			if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
				t.Fatalf("want the service started, got %v", err)
			}
			got := strings.Join(calls(), "\n")
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q in the runtime's arguments:\n%s", tc.want, got)
			}
		})
	}
}

// A second file settles an unquoted number as its decimal, so the spelling
// the refusal names is the reading and there is nothing to add
// (TestASecondFileSettlesWhatIsQuoted holds that). A quoted entry is a string
// on both sides of the merge, so it arrives as the file wrote it and the
// reading is worth as much as it is in a file read on its own.
func TestAQuotedEntryStillNamesItsReadingAfterAMerge(t *testing.T) {
	rt, _ := fakeShim(t)
	dir := t.TempDir()
	base := filepath.Join(dir, "compose.yaml")
	overlay := filepath.Join(dir, "over.yaml")
	if err := os.WriteFile(base, []byte("name: demo\nservices:\n  app:\n    image: alpine:3.20\n    group_add: [\"+2147483648\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, []byte("services:\n  app:\n    environment:\n      X: \"1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := compose.LoadFiles([]string{base, overlay}, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
	if err == nil {
		t.Fatal("want a refusal")
	}
	if want := `adds the group "+2147483648" (group_add), which reads as the group 2147483648 and is past 2147483647`; !strings.Contains(err.Error(), want) {
		t.Errorf("want %q in:\n%v", want, err)
	}
}
