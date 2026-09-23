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
// an item written twice refused. A number that fits an integer is read to
// its decimal as there, whether the file is read alone or with another
// (measured 2026-09-20); a repeat is found by the spelling together with what
// opossum hands over for it, where docker compose goes by the value it read (the rows marked "known difference",
// each with the answer this reads). What of it the runtime can take is the
// orchestrator's question, not this one's.
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
		// A number that fits an integer is read to its decimal, as docker
		// compose reads it (`0x10` → `"16"`, `020` → `"16"`, `0o17` →
		// `"15"`, `0755` → `"493"`, `2_000` → `"2000"`, `+2000` →
		// `"2000"`), in one file as in two. `config` prints the decimal,
		// and `--gid` is given the same group docker compose adds.
		{"a hex number", "    group_add: [0x10]\n", []string{"16"}, ""},
		{"an octal number", "    group_add: [0o17]\n", []string{"15"}, ""},
		{"a mode-looking number is its octal", "    group_add: [0755]\n", []string{"493"}, ""},
		{"a leading-zero number, 1024 in octal", "    group_add: [02000]\n", []string{"1024"}, ""},
		{"a signed leading-zero number", "    group_add: [+0755]\n", []string{"493"}, ""},
		{"a three-character leading-zero number", "    group_add: [010]\n", []string{"8"}, ""},
		{"a leading-zero number read the same both ways", "    group_add: [07]\n", []string{"7"}, ""},
		{"double zero", "    group_add: [00]\n", []string{"0"}, ""},
		{"an underscored number", "    group_add: [2_000]\n", []string{"2000"}, ""},
		{"a quoted leading-zero number is a string", "    group_add: [\"0755\"]\n", []string{"0755"}, ""},
		{"a negative leading-zero number is left to the start's check", "    group_add: [-010]\n", []string{"-8"}, ""},
		{"a signed number", "    group_add: [+2000]\n", []string{"2000"}, ""},
		// Minus zero keeps its sign, so the check where the service starts
		// still refuses it for being negative; docker compose refuses the
		// value as a float there.
		{"minus zero keeps its sign", "    group_add: [-0]\n", []string{"-0"}, ""},
		{"minus zero written with more zeros", "    group_add: [-00]\n", []string{"-0"}, ""},
		// Known difference: a repeat is found by the spelling and what is
		// handed over for it, so two
		// spellings of one number are two entries here — the check where the
		// service starts folds them by what would be handed to `--gid` and
		// refuses them as one group named twice — where docker compose
		// refuses the file for naming one group twice as it reads it.
		{"a number then the same in hex", "    group_add: [16, 0x10]\n", []string{"16", "16"}, ""},
		// The other way round: `[0x10, "16"]` is two entries on docker
		// compose too, and both read the same group here.
		{"a number then the same as a string", "    group_add: [0x10, \"16\"]\n", []string{"16", "16"}, ""},
		{"a quoted hex is a string", "    group_add: [\"0x10\"]\n", []string{"0x10"}, ""},
		{"a leading-zero non-octal is a float, refused", "    group_add: [08]\n", nil, "group_add entry 1 of 1 must be a number or a string"},
		{"an exponent is a float, refused", "    group_add: [1e3]\n", nil, "group_add entry 1 of 1 must be a number or a string"},
		{"a number too large for an unsigned integer is a float, refused", "    group_add: [99999999999999999999]\n", nil, "group_add entry 1 of 1 must be a number or a string"},
		// Between the two: YAML reads it as an unsigned number, which does
		// not fit the integer this reads, so it stays as written and the
		// check where the service starts refuses it.
		{"a number too large for an integer stays as written", "    group_add: [18446744073709551615]\n", []string{"18446744073709551615"}, ""},
		{"the same in hex", "    group_add: [0xFFFFFFFFFFFFFFFF]\n", []string{"0xFFFFFFFFFFFFFFFF"}, ""},
		{"a scalar is refused", "    group_add: 2000\n", nil, "group_add must be a list, got a single value"},
		{"a quoted scalar is refused", "    group_add: \"2000\"\n", nil, "group_add must be a list"},
		{"null is refused", "    group_add: null\n", nil, "group_add"},
		// An item written twice is refused, as docker compose refuses it —
		// by the spelling, whichever way round. Known difference: docker
		// compose answers by the order the two are written in (`[2000,
		// "2000"]` goes through there with both kept, `["2000", 2000]` does
		// not; `[16, 0x10]` is refused there as one group named twice, and is
		// two entries here, which the check where the service starts refuses
		// as one group named twice, asking for it to be listed once).
		{"the same number twice is refused", "    group_add: [2000, 2000]\n", nil, "group_add items at 0 and 1 are equal"},
		{"the same string twice is refused", "    group_add: [\"2000\", \"2000\"]\n", nil, "group_add items at 0 and 1 are equal"},
		{"a string then its number is refused", "    group_add: [\"2000\", 2000]\n", nil, "group_add items at 0 and 1 are equal"},
		{"a number then its string is refused (docker compose: goes through)", "    group_add: [2000, \"2000\"]\n", nil, "group_add items at 0 and 1 are equal"},
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

// Across files a repeated group is folded by what it reads as, whether the
// files write it as a string or as a number (v5.5.1, measured 2026-09-19 and
// 2026-09-23: `compose.yaml` with `["2000"]` and an override with `["2000"]`
// print one `"2000"`; `["2000", "3000"]` then `["3000"]` print two; `[2000]`
// and `[2000]` print one there too).
//
// Known difference, in the rows that say so: docker compose compares an entry
// by the value it read and only from the second entry on, so it prints both
// of `[2000]` and `["2000"]` and refuses `["2000"]` then `[2000]`. Here the
// two are one entry whichever file wrote which, since one `--gid` is handed
// over in the end.
func TestGroupAddIsFoldedAcrossFiles(t *testing.T) {
	for _, tc := range []struct {
		name, base, over string
		want             []string
		err              string
	}{
		{"the same group in both", "[\"2000\"]", "[\"2000\"]", []string{"2000"}, ""},
		{"one of two repeated", "[\"2000\", \"3000\"]", "[\"3000\"]", []string{"2000", "3000"}, ""},
		{"a new one added", "[\"2000\"]", "[\"3000\"]", []string{"2000", "3000"}, ""},
		// The merge folds a scalar by what it reads as, so a number written
		// in both files is one entry — as docker compose folds it. A number
		// beside its string folds here either way round, where docker
		// compose keeps both when the number comes first and refuses the
		// file when the string does (measured on v5.5.1): one `--gid` is
		// handed over in the end, so the two are one entry here whichever
		// file wrote which.
		{"a number in both (docker compose: folded)", "[2000]", "[2000]", []string{"2000"}, ""},
		{"a number then its string (docker compose: both printed)", "[2000]", "[\"2000\"]", []string{"2000"}, ""},
		{"a string then its number (docker compose: refused)", "[\"2000\"]", "[2000]", []string{"2000"}, ""},
		// Not one entry: the integer is YAML's octal and the string is the
		// digits, so they read as two groups and both are kept (docker
		// compose keeps them too).
		{"an octal integer beside its string", "[020]", "[\"020\"]", []string{"16", "020"}, ""},
		// A file naming a group twice on its own is still refused, wherever
		// it sits.
		{"twice in the base", "[2000, 2000]", "[3000]", nil, "group_add items at 0 and 1 are equal"},
		{"twice in the override", "[3000]", "[2000, 2000]", nil, "group_add items at 0 and 1 are equal"},
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
