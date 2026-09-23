package compose_test

// Evals for what "the same entry twice" means in `group_add`. The repeat is
// looked for by the spelling the file uses, which is right where two spellings
// read alike — but the same spelling can read two ways (`020` is YAML's octal,
// the string "020" is the digits `--gid` reads as 20), and those are two
// groups, not one entry twice.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

func loadGroupAdd(t *testing.T, list string) (*compose.Project, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	body := "name: demo\nservices:\n  app:\n    image: alpine:3.20\n    group_add: " + list + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return compose.Load(path)
}

func TestTheSameSpellingReadingTwoWaysIsNotOneEntryTwice(t *testing.T) {
	for _, tc := range []struct {
		name, list string
		refused    bool
	}{
		// The spelling is the same and so is the reading: one entry twice,
		// refused as it was — `down` and the rest are refused with it, which
		// is why this check stays narrow.
		{"the same number twice", `[2000, 2000]`, true},
		{"the same string twice", `["2000", "2000"]`, true},
		{"a string then its number", `["2000", 2000]`, true},
		{"a number then its string", `[2000, "2000"]`, true},
		// The spelling is not a decimal, and what is handed over for it is
		// the same both times: still one entry twice.
		{"the same hex twice", `[0x10, 0x10]`, true},
		{"the same octal twice", `[020, 020]`, true},
		{"the same spaced-out number twice", `[2_000, 2_000]`, true},
		{"the same unreadable string twice", `["0x10", "0x10"]`, true},
		// The spelling is the same and the reading is not: an integer `020`
		// is YAML's octal (the group 16) and the string is the digits `--gid`
		// reads as 20. Two groups, and the check where the service starts is
		// the one that says so.
		{"an octal integer beside its string", `[020, "020"]`, false},
		{"another octal integer beside its string", `[016, "016"]`, false},
		{"a hex integer beside its string", `[0x10, "0x10"]`, false},
		{"a spaced-out integer beside its string", `[2_000, "2_000"]`, false},
		// The reading compared here is the one opossum would hand over, not
		// the one `--gid` makes of it: an integer `+16` arrives as `16` and
		// the string keeps its plus, so these two are not one entry twice —
		// although the runtime reads both as the group 16. The check the
		// services are started by folds them and says so.
		{"a signed integer beside its string", `[+16, "+16"]`, false},
		// Different spellings are two entries, as they were.
		{"two spellings of one group", `[0x10, "16"]`, false},
		{"two groups", `[16, 20]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadGroupAdd(t, tc.list)
			if tc.refused {
				if err == nil || !strings.Contains(err.Error(), "group_add items at 0 and 1 are equal — list the group once") {
					t.Errorf("want the repeat refused, got %v", err)
				}
				return
			}
			if err != nil {
				t.Errorf("want the file read, got %v", err)
			}
		})
	}
}

// The two the refusal names are the two that are the same entry, not the first
// two that are spelled alike: one spelling can be two groups, and then the
// repeat is the one further along.
func TestTheRefusalNamesTheTwoThatAreTheSameEntry(t *testing.T) {
	for _, tc := range []struct{ name, list, want string }{
		{"the repeat is the third entry", `[020, "020", "020"]`, "group_add items at 1 and 2 are equal"},
		{"the repeat is the first and the third", `["020", 020, "020"]`, "group_add items at 0 and 2 are equal"},
		{"three alike", `["020", "020", "020"]`, "group_add items at 0 and 1 are equal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadGroupAdd(t, tc.list)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want %q, got %v", tc.want, err)
			}
		})
	}
}

// The narrowing reaches every reading of the file: a repeat written twice in
// one file arrives here whole, and stays refused — where a repeat split across
// two files is folded by the merge before this sees it (mergedupe_test.go).
func TestARepeatInOneFileIsStillOneEntryTwice(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "compose.yaml")
	overlay := filepath.Join(dir, "over.yaml")
	if err := os.WriteFile(base, []byte("name: demo\nservices:\n  app:\n    image: alpine:3.20\n    group_add: [0x10, \"16\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, []byte("services:\n  app:\n    environment:\n      X: \"1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The merge writes the tree back out, so `0x10` arrives as `16` beside
	// the string `"16"`: one entry twice, in one file's list.
	_, err := compose.LoadFiles([]string{base, overlay}, nil)
	if err == nil || !strings.Contains(err.Error(), "are equal — list the group once") {
		t.Errorf("want the repeat refused, got %v", err)
	}
}

// What the entries read as is unchanged by the narrowing: the pair that is no
// longer one entry twice still reads as the two groups it names.
func TestTheEntriesStillReadAsTheirGroups(t *testing.T) {
	for _, tc := range []struct{ list, want string }{
		{`[020, "020"]`, "16,020"},
		{`[+16, "+16"]`, "16,+16"},
	} {
		t.Run(tc.list, func(t *testing.T) {
			p, err := loadGroupAdd(t, tc.list)
			if err != nil {
				t.Fatalf("want it read, got %v", err)
			}
			if got := strings.Join(p.Services["app"].GroupAdd, ","); got != tc.want {
				t.Errorf("group_add = %s, want %s", got, tc.want)
			}
		})
	}
}
