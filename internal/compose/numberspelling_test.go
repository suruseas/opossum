package compose

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// `group_add` reads a number to its decimal — the group docker compose adds —
// whether the file is read on its own or with a second one (docker compose
// v5.5.1, `config`, measured 2026-09-20: `0x10` is `"16"`, `0755` is `"493"`,
// `2_000` is `"2000"`). Before this, a file read on its own kept what was
// written and handed the runtime `--gid 0x10`, which it does not take, while
// the same file read with an override that says nothing about `group_add`
// reached the runtime as 16.
func TestGroupAddReadsANumberTheSameInOneFileAsInTwo(t *testing.T) {
	for _, tc := range []struct {
		name, written, want string
		// wantTwo, when set, is what a second file's reading gives instead:
		// the merge writes the tree back out before this decoder sees it, and
		// a zero written with a minus no longer carries the sign by then (as
		// it did not before this change either).
		wantTwo string
	}{
		{"hexadecimal", "0x10", "16", ""},
		{"octal with a leading zero", "0755", "493", ""},
		{"octal written as 0o", "0o17", "15", ""},
		{"binary", "0b101", "5", ""},
		{"underscores", "2_000", "2000", ""},
		{"a plus", "+2000", "2000", ""},
		{"two zeros", "00", "0", ""},
		{"decimal", "16", "16", ""},
		{"a negative number", "-1", "-1", ""},
		{"a negative leading-zero number", "-010", "-8", ""},
		// A zero written with a minus keeps its sign, so the check where the
		// service starts still refuses it for being negative; docker compose
		// refuses the value there as a float.
		{"minus zero", "-0", "-0", "0"},
		{"minus zero with more zeros", "-00", "-0", "0"},
		// Quoted, it is a string: what was written, untouched.
		{"quoted hexadecimal", `"0x10"`, "0x10", ""},
		{"quoted leading zeros", `"0002000"`, "0002000", ""},
		{"a name", "wheel", "wheel", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			one := filepath.Join(dir, "compose.yaml")
			write(t, one, "services:\n  app:\n    image: alpine:3.20\n    group_add: ["+tc.written+"]\n")
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
					if got := strings.Join(p.Services["app"].GroupAdd, ","); got != want {
						t.Errorf("group_add = %q, want %q", got, want)
					}
				})
			}
		})
	}
}

// A repeat is found by the spelling, as it was before this: two spellings of
// one group are two entries, read to the same decimal, and it is the check
// where the service starts that refuses them — folding them by what would be
// handed to `--gid`, as one group named twice. Reading the
// decimal to find the repeat would refuse files docker compose v5.5.1 and
// every earlier opossum take — `[0x10, "16"]`, `[2_000, "2000"]`, `[07,
// "7"]` — and refuse them as the file is read, which stops `down` as well
// (measured 2026-09-20).
func TestARepeatedGroupIsFoundByTheSpelling(t *testing.T) {
	for _, tc := range []struct {
		name, list string
		want       []string
		err        string
	}{
		{"one spelling twice", `[16, 16]`, nil, "group_add items at 0 and 1 are equal"},
		// The repeat is the second and third entries: the refusal names
		// those, not the first entry that reads as the same group (docker
		// compose names 1 and 2 here too).
		{"another spelling first, then one twice", `[0x10, "16", "16"]`, nil, "group_add items at 1 and 2 are equal"},
		{"a number then the same as a string", `[0x10, "16"]`, []string{"16", "16"}, ""},
		{"underscores then the same as a string", `[2_000, "2000"]`, []string{"2000", "2000"}, ""},
		{"an octal then the same as a string", `[07, "7"]`, []string{"7", "7"}, ""},
		{"a number then the same in hex", `[16, 0x10]`, []string{"16", "16"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			one := filepath.Join(dir, "compose.yaml")
			write(t, one, "services:\n  app:\n    image: alpine:3.20\n    group_add: "+tc.list+"\n")
			p, err := LoadFiles([]string{one}, nil)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want %q, got %v", tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := p.Services["app"].GroupAdd; !slices.Equal(got, tc.want) {
				t.Errorf("group_add = %q, want %q", got, tc.want)
			}
		})
	}
}

// A number too large to fit an integer stays as written: the check where the
// service starts refuses one written another way (`0xFFFFFFFFFFFFFFFF`) for
// not being the digits of a gid, and one written in digits for being past the
// largest gid the engine takes. Docker compose refuses such a file outright
// (`unconvertible type`).
func TestAGroupTooLargeForAnIntegerStaysAsWritten(t *testing.T) {
	for _, written := range []string{"18446744073709551615", "0xFFFFFFFFFFFFFFFF"} {
		t.Run(written, func(t *testing.T) {
			dir := t.TempDir()
			one := filepath.Join(dir, "compose.yaml")
			write(t, one, "services:\n  app:\n    image: alpine:3.20\n    group_add: ["+written+"]\n")
			p, err := LoadFiles([]string{one}, nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := strings.Join(p.Services["app"].GroupAdd, ","); got != written {
				t.Errorf("group_add = %q, want %q", got, written)
			}
		})
	}
}

// Nothing else in the file is read differently: a label keeps the spelling it
// had, and an environment value keeps the decimal it already had (this
// changes `group_add` alone).
func TestReadingTheGroupDoesNotChangeTheRestOfTheFile(t *testing.T) {
	dir := t.TempDir()
	one := filepath.Join(dir, "compose.yaml")
	write(t, one, "services:\n  app:\n    image: alpine:3.20\n    group_add: [0x10]\n    labels: {a: 0x10}\n    environment: {A: 0x10}\n")
	p, err := LoadFiles([]string{one}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	svc := p.Services["app"]
	if got := strings.Join(svc.GroupAdd, ","); got != "16" {
		t.Errorf("group_add = %q, want 16", got)
	}
	if got := labelValue(t, svc.Labels, "a"); got != "0x10" {
		t.Errorf("label a = %q, want 0x10 (untouched)", got)
	}
	// `environment` reads its value into a string itself, in decimal, as it
	// did before this change.
	if got := labelValue(t, Labels(svc.Environment), "A"); got != "16" {
		t.Errorf("environment A = %q, want 16 (unchanged by this)", got)
	}
}

// labelValue is the value of the named entry in a list the loader keeps as
// `key=value` strings (`labels` and `environment` are both that shape).
func labelValue(t *testing.T, labels Labels, key string) string {
	t.Helper()
	for _, l := range labels {
		if k, v, ok := strings.Cut(l, "="); ok && k == key {
			return v
		}
	}
	t.Fatalf("no label %q in %q", key, labels)
	return ""
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
