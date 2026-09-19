package compose

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// `group_add`, as docker compose v5.5.1 reads it (`config`, measured
// 2026-09-19): a list of numbers or strings — a scalar, a bool, a float or
// an item written twice refused. Kept as written, where docker compose
// reads a number to its decimal, and the same spelling twice refused
// whichever way round, where docker compose goes by the order (the rows
// marked "known difference", each with the answer this reads). What of it
// the runtime can take is the orchestrator's question, not this one's.
func TestGroupAddIsReadAsDockerComposeReadsIt(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       []string
		err        string
	}{
		{"a quoted number", "    group_add: [\"2000\"]\n", []string{"2000"}, ""},
		{"a number", "    group_add: [2000]\n", []string{"2000"}, ""},
		{"a name", "    group_add: [wheel]\n", []string{"wheel"}, ""},
		{"two", "    group_add: [\"2000\", \"3000\"]\n", []string{"2000", "3000"}, ""},
		{"empty list", "    group_add: []\n", nil, ""},
		{"an empty item", "    group_add: [\"\"]\n", []string{""}, ""},
		{"a negative number", "    group_add: [-1]\n", []string{"-1"}, ""},
		{"through an alias", "    group_add: [*g]\n", []string{"2000"}, ""},
		// Known difference: a number is kept as written, where docker
		// compose reads it to its decimal (`0x10` → `"16"`, `020` → `"16"`,
		// `0o17` → `"15"`, `0755` → `"493"`, `2_000` → `"2000"`, `+2000` →
		// `"2000"`, `-0` refused as a float there). Here `config` prints
		// what was written, and a spelling `--gid` does not take is refused
		// where the service starts.
		{"a hex number, kept as written", "    group_add: [0x10]\n", []string{"0x10"}, ""},
		// An unquoted leading-zero number is octal to YAML and to docker
		// compose (`0755` adds 493 there) but decimal to the runtime handed
		// the digits (755): refused, naming both ways to write it. Where the
		// two readings agree (`00`, `07`) it passes; quoted, it is a string.
		{"a mode-looking number is refused", "    group_add: [0755]\n", nil, "group_add entry 1 of 1 is 0755, which YAML reads as the octal 493 where the runtime would read it as decimal — write `- 493` for that group, or quote it (`- \"755\"`) for the decimal one"},
		{"a leading-zero number, 1024 in octal", "    group_add: [02000]\n", nil, "is 02000, which YAML reads as the octal 1024"},
		{"a signed leading-zero number", "    group_add: [+0755]\n", nil, "is +0755, which YAML reads as the octal 493 where the runtime would read it as decimal — write `- 493` for that group, or quote it (`- \"755\"`) for the decimal one"},
		// Three characters: 8 in octal, 10 in decimal.
		{"a three-character leading-zero number", "    group_add: [010]\n", nil, "is 010, which YAML reads as the octal 8 where the runtime would read it as decimal — write `- 8` for that group, or quote it (`- \"10\"`) for the decimal one"},
		{"another three-character one", "    group_add: [020]\n", nil, "is 020, which YAML reads as the octal 16"},
		{"a leading-zero number read the same both ways", "    group_add: [07]\n", []string{"07"}, ""},
		{"double zero", "    group_add: [00]\n", []string{"00"}, ""},
		{"a quoted leading-zero number is a string", "    group_add: [\"0755\"]\n", []string{"0755"}, ""},
		{"a negative leading-zero number is left to the start's check", "    group_add: [-010]\n", []string{"-010"}, ""},
		{"a signed number, kept as written", "    group_add: [+2000]\n", []string{"+2000"}, ""},
		{"minus zero, kept as written", "    group_add: [-0]\n", []string{"-0"}, ""},
		{"a quoted hex is a string", "    group_add: [\"0x10\"]\n", []string{"0x10"}, ""},
		{"a leading-zero non-octal is a float, refused", "    group_add: [08]\n", nil, "group_add entry 1 of 1 must be a number or a string"},
		{"an exponent is a float, refused", "    group_add: [1e3]\n", nil, "group_add entry 1 of 1 must be a number or a string"},
		{"a number too large for an integer is a float, refused", "    group_add: [99999999999999999999]\n", nil, "group_add entry 1 of 1 must be a number or a string"},
		{"a scalar is refused", "    group_add: 2000\n", nil, "group_add must be a list, got a single value"},
		{"a quoted scalar is refused", "    group_add: \"2000\"\n", nil, "group_add must be a list"},
		{"null is refused", "    group_add: null\n", nil, "group_add"},
		// An item written twice is refused, as docker compose refuses it —
		// by the spelling, whichever way round. Known difference: docker
		// compose goes by the order (`[2000, "2000"]` goes through there
		// with both kept, `["2000", 2000]` does not; `[16, 0x10]` is refused
		// there as one number twice, and is two spellings here).
		{"the same number twice is refused", "    group_add: [2000, 2000]\n", nil, "group_add items at 0 and 1 are equal"},
		{"the same string twice is refused", "    group_add: [\"2000\", \"2000\"]\n", nil, "group_add items at 0 and 1 are equal"},
		{"a string then its number is refused", "    group_add: [\"2000\", 2000]\n", nil, "group_add items at 0 and 1 are equal"},
		{"a number then its string is refused (docker compose: goes through)", "    group_add: [2000, \"2000\"]\n", nil, "group_add items at 0 and 1 are equal"},
		{"a number then the same in hex are two spellings (docker compose: refused)", "    group_add: [16, 0x10]\n", []string{"16", "0x10"}, ""},
		{"a string then a different number", "    group_add: [\"2000\", 3000]\n", []string{"2000", "3000"}, ""},
		{"three, the third repeating the first", "    group_add: [16, \"32\", 16]\n", nil, "group_add items at 0 and 2 are equal"},
		{"an int tag on digits reads the digits", "    group_add: [!!int \"16\"]\n", []string{"16"}, ""},
		{"a float is refused", "    group_add: [2000.5]\n", nil, "group_add entry 1 of 1 must be a number or a string"},
		{"a bool is refused", "    group_add: [true]\n", nil, "group_add entry 1 of 1 must be a number or a string"},
		{"a mapping item is refused", "    group_add: [{a: b}]\n", nil, "group_add entry 1 of 1 must be a number or a string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, "x-g: &g 2000\nservices:\n  app:\n    image: alpine:3.20\n"+tc.body))
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want a refusal saying %q, got %v", tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			svc := p.Services["app"]
			if !slices.Equal([]string(svc.GroupAdd), tc.want) {
				t.Errorf("group_add: got %q, want %q", svc.GroupAdd, tc.want)
			}
			if slices.Contains(svc.Unsupported, "group_add") {
				t.Errorf("group_add is read, yet listed as ignored: %v", svc.Unsupported)
			}
			out, err := RenderConfig(p)
			if err != nil {
				t.Fatal(err)
			}
			// Under its own key, each on a line of its own, as docker compose
			// prints it.
			if len(tc.want) > 0 {
				block := "        group_add:\n"
				for _, g := range tc.want {
					block += "            - " + quoteIfNumeric(g) + "\n"
				}
				if !strings.Contains(out, block) {
					t.Errorf("config should print\n%s\ngot\n%s", block, out)
				}
			} else if strings.Contains(out, "group_add") {
				t.Errorf("config should leave an empty group_add out:\n%s", out)
			}
		})
	}
}

// Across files a repeated group written as a string is folded, as docker
// compose folds it (v5.5.1, measured 2026-09-19: `compose.yaml` with
// `["2000"]` and an override with `["2000"]` print one `"2000"`; `["2000",
// "3000"]` then `["3000"]` print two). Known difference: a number written in
// both files is the same item twice here and refused — docker compose folds
// `[2000]` and `[2000]`, prints both of `[2000]` and `["2000"]`, and refuses
// `["2000"]` then `[2000]` — the answer here is one: list the group once, or
// write it the same way in both.
func TestGroupAddIsFoldedAcrossFiles(t *testing.T) {
	for _, tc := range []struct {
		name, base, over string
		want             []string
		err              string
	}{
		{"the same group in both", "[\"2000\"]", "[\"2000\"]", []string{"2000"}, ""},
		{"one of two repeated", "[\"2000\", \"3000\"]", "[\"3000\"]", []string{"2000", "3000"}, ""},
		{"a new one added", "[\"2000\"]", "[\"3000\"]", []string{"2000", "3000"}, ""},
		{"a number in both (docker compose: folded)", "[2000]", "[2000]", nil, "group_add items at 0 and 1 are equal"},
		{"a number then its string (docker compose: both printed)", "[2000]", "[\"2000\"]", nil, "group_add items at 0 and 1 are equal"},
		{"a string then its number (docker compose: refused too)", "[\"2000\"]", "[2000]", nil, "group_add items at 0 and 1 are equal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			base := writeTemp(t, "services:\n  app:\n    image: alpine:3.20\n    group_add: "+tc.base+"\n")
			over := filepath.Join(dir, "over.yaml")
			if err := os.WriteFile(over, []byte("services:\n  app:\n    group_add: "+tc.over+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := LoadFiles([]string{base, over}, nil)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want a refusal saying %q, got %v", tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := []string(p.Services["app"].GroupAdd); !slices.Equal(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// quoteIfNumeric is how the YAML printer spells a string that looks like a
// number: in quotes, as docker compose's `config` prints `- "2000"`.
func quoteIfNumeric(s string) string {
	if s == "" {
		return `""`
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0o") {
		return `"` + s + `"`
	}
	for _, c := range s {
		if (c < '0' || c > '9') && c != '-' && c != '+' {
			return s
		}
	}
	return `"` + s + `"`
}
