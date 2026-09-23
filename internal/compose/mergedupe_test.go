package compose_test

// Evals for what a merge folds when both files write the same entry. The fold
// is `appendNew`, shared by `ports`, `expose`, `volumes_from` and `group_add`,
// and it used to look only at entries that arrive as strings — so a number
// written in both files stayed twice over, and `group_add` then refused the
// merged file as the same entry twice. That refusal is raised as the file is
// read, so `up`, `ps`, `config` and `down` all stopped: a project started
// before the second file said the same thing could not be brought down
// (measured 2026-09-23 on
// container 1.4.1, with `compose.yaml` and `compose.override.yaml` — no `-f`
// given). docker compose folds them and goes on (measured on v5.5.1).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// loadTwo writes a base and an override and reads them as one project, the
// way opossum reads `compose.yaml` beside `compose.override.yaml`.
func loadTwo(t *testing.T, base, over string) (*compose.Project, error) {
	t.Helper()
	dir := t.TempDir()
	basePath := filepath.Join(dir, "compose.yaml")
	overPath := filepath.Join(dir, "compose.override.yaml")
	if err := os.WriteFile(basePath, []byte("name: demo\nservices:\n  app:\n    image: alpine:3.20\n"+base), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overPath, []byte("services:\n  app:\n"+over), 0o644); err != nil {
		t.Fatal(err)
	}
	return compose.LoadFiles([]string{basePath, overPath}, nil)
}

func TestANumberBothFilesWriteIsOneEntryAfterTheMerge(t *testing.T) {
	for _, tc := range []struct{ name, base, over, want string }{
		// The shape that stopped `down`: both files name the same group, as
		// a number.
		{"the same group as a number", "    group_add: [16]\n", "    group_add: [16]\n", "16"},
		// As a string, which folded before this too.
		{"the same group as a string", "    group_add: [\"16\"]\n", "    group_add: [\"16\"]\n", "16"},
		// One of each: the number arrives in its decimal, so the two are the
		// same entry and fold.
		{"a number over a string", "    group_add: [\"16\"]\n", "    group_add: [16]\n", "16"},
		{"a string over a number", "    group_add: [16]\n", "    group_add: [\"16\"]\n", "16"},
		// Both ways round, and inside a longer list: opossum folds either
		// way, where docker compose keeps both entries when the number comes
		// first and refuses the file when the string does (measured on
		// v5.5.1 — its repeat is found by the value it read, and only from
		// the second entry on). One `--gid` is handed over in the end, so
		// the two are one entry here whichever file wrote which.
		{"a number then its string, inside a list", "    group_add: [16, 24]\n", "    group_add: [\"24\"]\n", "16,24"},
		{"a string then its number, inside a list", "    group_add: [\"16\", \"24\"]\n", "    group_add: [24]\n", "16,24"},
		// The earlier file's entry is the one that stays, so the order the
		// two files are read in is the order the entries come out.
		{"the repeat is the later file's, and the earlier one keeps its place", "    group_add: [16, 24]\n", "    group_add: [16]\n", "16,24"},
		// A negative number folds like any other: it is refused a step later
		// for being negative, not here for being there twice.
		{"a negative number in both", "    group_add: [-5]\n", "    group_add: [-5]\n", "-5"},
		// A number past what an integer holds arrives as another kind, and
		// is folded by the same reading.
		{"a number past what an integer holds, in both", "    group_add: [18446744073709551615]\n", "    group_add: [18446744073709551615]\n", "18446744073709551615"},
		// The earlier file's own list is left as it wrote it: two spellings of
		// one group there are still that file's repeat, and the field that
		// reads them says so — whether or not the later file says anything
		// about the same key. Only what the later file adds is compared.
		// (Held as a refusal by TestAFilesOwnRepeatSurvivesTheMerge.)
		//
		// Each file adds its own: both are kept, in the order they are read.
		{"two groups, one from each file", "    group_add: [16]\n", "    group_add: [20]\n", "16,20"},
		// Part of the list repeats: the repeat folds, the rest is appended.
		{"a list with one entry in common", "    group_add: [16, 24]\n", "    group_add: [24, 20]\n", "16,24,20"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := loadTwo(t, tc.base, tc.over)
			if err != nil {
				t.Fatalf("want the files read, got %v", err)
			}
			if got := strings.Join(p.Services["app"].GroupAdd, ","); got != tc.want {
				t.Errorf("group_add = %s, want %s", got, tc.want)
			}
		})
	}
}

// A file's own repeat is not folded by the merge, whichever file it sits in
// and however the other file arrives: what each file wrote on its own is that
// file's to answer for.
//
// Known difference: docker compose answers for it only where the repeat
// survives its merge. A repeat in the first file is refused there as well; one
// a later file wrote is folded away and the project goes on — `[20]` read with
// an override of `[16, 16]` leaves `20 16` there (measured on v5.5.1,
// 2026-09-23), where the second row below refuses it.
func TestAFilesOwnRepeatSurvivesTheMerge(t *testing.T) {
	const repeat = "    group_add: [0x10, \"16\"]\n" // one group, two spellings
	const other = "    group_add: [0x10]\n"
	t.Run("in the earlier file, as docker compose refuses it too", func(t *testing.T) {
		if _, err := loadTwo(t, repeat, other); err == nil || !strings.Contains(err.Error(), "are equal — list the group once") {
			t.Errorf("want the repeat refused, got %v", err)
		}
	})
	t.Run("in the later file, where docker compose folds it away", func(t *testing.T) {
		if _, err := loadTwo(t, other, repeat); err == nil || !strings.Contains(err.Error(), "are equal — list the group once") {
			t.Errorf("want the repeat refused, got %v", err)
		}
	})
	t.Run("with the other file saying nothing about it", func(t *testing.T) {
		if _, err := loadTwo(t, repeat, "    environment:\n      X: \"1\"\n"); err == nil || !strings.Contains(err.Error(), "are equal — list the group once") {
			t.Errorf("want the repeat refused, got %v", err)
		}
	})
}

// Two spellings across files are one entry after the merge: it writes the
// tree back out, so the number arrives in its decimal beside the string and
// the later file's entry is one the earlier file already has.
func TestTwoSpellingsAcrossFilesAreOneEntryAfterTheMerge(t *testing.T) {
	p, err := loadTwo(t, "    group_add: [0x10]\n", "    group_add: [\"16\"]\n")
	if err != nil {
		t.Fatalf("want the files read, got %v", err)
	}
	// The merge writes the tree back out, so the number arrives in its
	// decimal and the two are one entry after all.
	if got := strings.Join(p.Services["app"].GroupAdd, ","); got != "16" {
		t.Errorf("group_add = %s, want 16", got)
	}
}

// One file naming a group twice, in the one spelling, is still refused as the
// file is read — in the base as docker compose refuses it (measured: `items at
// 0 and 1`), and in the override where docker compose folds the repeat away
// and prints `20 16` (measured on v5.5.1, 2026-09-23: the same known
// difference as TestAFilesOwnRepeatSurvivesTheMerge).
func TestOneFileNamingAGroupTwiceIsStillRefused(t *testing.T) {
	for _, tc := range []struct{ name, base, over string }{
		{"in the base, as docker compose refuses it too", "    group_add: [16, 16]\n", "    group_add: [20]\n"},
		{"in the override, where docker compose prints 20 16", "    group_add: [20]\n", "    group_add: [16, 16]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadTwo(t, tc.base, tc.over)
			if err == nil || !strings.Contains(err.Error(), "are equal — list the group once") {
				t.Errorf("want the repeat refused, got %v", err)
			}
		})
	}
}
