package repohygiene_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/repohygiene"
)

// Each refusal names the file and then what it is being refused for, and where
// two of those are the same kind of value — two paths, two directories, two
// numbers — the message still reads with them exchanged. The table above only
// asks for one word of each; these hold the pieces in their places (#559).
func TestTheOffenseMessagesKeepEachPieceInItsPlace(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		size int64
		head []byte
		want []string
	}{
		{
			"a binary names the file, the sniff size, the file again and where art belongs",
			"fakeshim", 2_700_000, []byte("\xcf\xfa\xed\xfe\x00\x00"),
			[]string{
				fmt.Sprintf("fakeshim looks like a binary file (a NUL byte in the first %d bytes).", repohygiene.SniffBytes),
				"then `git rm --cached fakeshim`.",
				"put it under docs/assets and give it an image",
			},
		},
		{
			"runtime state names the file and the directory, twice each",
			".opossum/state.json", 40, []byte("{}"),
			[]string{
				".opossum/state.json is under .opossum/, where opossum keeps",
				"Remove it (`git rm --cached .opossum/state.json`) and let opossum write its own. Add .opossum/ to",
			},
		},
		{
			"maintainer tooling names the file, the directory and the file again",
			"scripts/sync-public.sh", 4_000, []byte("#!/bin/bash\n"),
			[]string{
				"scripts/sync-public.sh is maintainer-only tooling, and maintainer-only tooling is not tracked.",
				"  scripts holds what runs the project",
				"Remove it from the index (`git rm --cached scripts/sync-public.sh`)",
			},
		},
		{
			"a large file names its size and then the limit",
			"docs/huge.md", repohygiene.MaxTrackedBytes + 1, []byte("# hello"),
			[]string{
				fmt.Sprintf("docs/huge.md is %d bytes, over the %d-byte limit for a tracked file.", repohygiene.MaxTrackedBytes+1, repohygiene.MaxTrackedBytes),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := repohygiene.Offense(tc.path, tc.size, tc.head)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("Offense(%q) should read %q in its place, got:\n%s", tc.path, want, got)
				}
			}
		})
	}
}

// The invisible-literal message carries the byte offset, the line, and the two
// line numbers of the od window: four ints on one call, and with the character
// on the second line at byte 3 every one of them is different.
func TestTheInvisibleLiteralMessageKeepsItsNumbersInPlace(t *testing.T) {
	got := repohygiene.InvisibleLiteral("fixture.txt", []byte("ab\n\ue000"))
	for _, want := range []string{
		"fixture.txt carries a literal U+E000 — the first at byte 3 (line 2) — and nothing can see it.",
		"`LC_ALL=C od -v -c fixture.txt | sed -n '1,2p'`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the message should read %q in its place, got:\n%s", want, got)
		}
	}
}
