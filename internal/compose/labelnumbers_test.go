package compose

import (
	"path/filepath"
	"testing"
)

// A label's value is read as YAML reads it and written as its text, the way
// docker compose prints it (v5.5.1, `config`, measured 2026-09-26): `0x10` is
// `16`, `1.50` is `1.5`, `True` is `true`, a date is the time it names. A file
// read on its own used to keep what was written, while the same file read with a
// second one — which writes the tree out and reads it back — gave the reading
// above, so one file and two disagreed on the label the container carried; the
// single-file side was the one docker did not agree with.
//
// Each row is read twice, on its own and with an override that says nothing
// about labels, and asserts the same value both times. Rows are one spelling
// each, so a reading that took hexadecimal and left the octal would show.
func TestALabelValueIsTheSameInOneFileAsInTwo(t *testing.T) {
	for _, tc := range []struct {
		name, written, want string
		// wantTwo, when set, is what a second file's reading gives instead: the
		// merge writes the tree out first, and a zero written with a minus and
		// nothing else no longer carries its sign by then.
		wantTwo string
	}{
		{"hexadecimal", "0x10", "16", ""},
		{"hexadecimal in capitals", "0xFF", "255", ""},
		{"octal with a leading zero", "0755", "493", ""},
		{"octal written as 0o", "0o17", "15", ""},
		{"binary", "0b11", "3", ""},
		{"underscores", "2_000", "2000", ""},
		{"a plus", "+2000", "2000", ""},
		{"a negative hexadecimal", "-0x10", "-16", ""},
		{"a leading zero that is not octal", "08", "8", ""},
		{"a float with an exponent", "1e3", "1000", ""},
		{"a float with a trailing zero", "1.50", "1.5", ""},
		{"a float with no integer part", ".5", "0.5", ""},
		{"a float that stays an exponent", "1e+06", "1e+06", ""},
		{"a boolean word", "True", "true", ""},
		{"a date", "2026-09-20", "2026-09-20 00:00:00 +0000 UTC", ""},
		{"a date with a time", "2026-09-20T00:00:00Z", "2026-09-20 00:00:00 +0000 UTC", ""},
		{"a null", "null", "", ""},
		// Minus zero: docker keeps the sign of a float's, and of `-0`, and drops it
		// from `-00`. The second file's reading loses it from `-0` (as it always
		// did), which is a difference this leaves where it was.
		{"minus zero", "-0", "-0", "0"},
		// Green in the second file's reading because the tree is written out with
		// `-0` and read back as an integer, and the hand-kept sign restores it.
		{"minus zero as a float", "-0.0", "-0", ""},
		{"minus zero with more zeros", "-00", "0", ""},
		// Text stays text: quoted, it is a string, and a word YAML 1.2 reads as
		// one is one.
		{"quoted hexadecimal", `"0x10"`, "0x10", ""},
		{"a tagged string", `!!str 0x10`, "0x10", ""},
		{"a word docker reads as a boolean", "yes", "yes", ""},
		{"a time of day", "1:30", "1:30", ""},
		{"a name", "web", "web", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			one := filepath.Join(dir, "compose.yaml")
			write(t, one, "services:\n  app:\n    image: alpine:3.20\n    labels: {a: "+tc.written+"}\n")
			other := filepath.Join(dir, "over.yaml")
			write(t, other, "services:\n  app:\n    environment: {Z: z}\n")
			for _, read := range []struct {
				name  string
				paths []string
			}{
				{"one file", []string{one}},
				{"two files", []string{one, other}},
			} {
				t.Run(read.name, func(t *testing.T) {
					p, err := LoadFiles(read.paths, nil)
					if err != nil {
						t.Fatalf("load: %v", err)
					}
					want := tc.want
					if read.name == "two files" && tc.wantTwo != "" {
						want = tc.wantTwo
					}
					if got := labelValue(t, p.Services["app"].Labels, "a"); got != want {
						t.Errorf("label a = %q, want %q", got, want)
					}
				})
			}
		})
	}
}

// The same reading wherever a label is kept: a network's goes through the same
// decoder, and a file that spells one `0x10` is read to 16 there too (a
// volume's labels are read past, so there is nothing to look at). And what was never a mapping's value keeps what it was: the list
// form is text, `a=0x10` is the label `a` with the value `0x10`.
func TestEveryPlaceALabelIsWrittenReadsItTheSame(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	write(t, file, `services:
  app:
    image: alpine:3.20
    labels: ["l=0x10"]
networks:
  n:
    labels: {a: 0x10}
`)
	p, err := LoadFiles([]string{file}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := labelValue(t, p.Networks["n"].Labels, "a"); got != "16" {
		t.Errorf("network label a = %q, want 16", got)
	}
	if got := labelValue(t, p.Services["app"].Labels, "l"); got != "0x10" {
		t.Errorf("list-form label l = %q, want 0x10 (text as written)", got)
	}
}

// A value written as an alias is its target's, in the spelling that carries the
// sign of a zero as much as in the one that does not. The decoder looks through
// the alias for both the reading and the sign, and each is a branch of its own.
func TestALabelValueThroughAnAliasReadsAsItsTarget(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	write(t, file, `x-hex: &hex 0x10
x-zero: &zero -0
services:
  app:
    image: alpine:3.20
    labels: {a: *hex, b: *zero}
`)
	p, err := LoadFiles([]string{file}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for key, want := range map[string]string{"a": "16", "b": "-0"} {
		if got := labelValue(t, p.Services["app"].Labels, key); got != want {
			t.Errorf("label %s = %q, want %q", key, got, want)
		}
	}
}
