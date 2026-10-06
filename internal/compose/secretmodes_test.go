package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The `mode` of a service's `secrets` and `configs` entries is read as docker compose v5.5.1 reads it (#1544;
// `docker compose config -q`, every row measured, for `secrets` and for `configs`). A null, a bool, a list and a mapping
// are refused by its schema in the file that writes them, whatever a later file writes over them. A float (`1.5`, `1.0`,
// `!!float 1`), a number past int64 and a string that is no octal number (`host`, `""`, `"08"`, `"0o440"`, `"0x1ff"`, `"1.5"`)
// are refused once the files are merged: a later file that writes the entry again (with another mode, `!override`, `!reset`, or
// none) takes the refusal away. A whole number up to int64 and an octal string (`"0440"`, `"+1"`, `"-1"`) are read. A service whose
// profile is not enabled is asked too. known marks the one difference left open: a bare `-0`, which docker compose reads as a float and the
// merged tree can no longer tell from `0`.
func TestTheModeOfASecretOrConfigEntryIsReadAsDockerComposeReadsIt(t *testing.T) {
	entry := func(kind, mode string) string {
		return "services:\n  a:\n    image: x\n    " + kind + ":\n      - source: k\n        mode: " + mode + "\n" + kind + ":\n  k:\n    file: ./s.txt\n"
	}
	later := map[string]string{
		"later 0400":      "        mode: 0400\n",
		"later !override": "        mode: !override 0400\n",
		"later !reset":    "        mode: !reset null\n",
		"later nothing":   "",
	}
	for _, tc := range []struct {
		kind, mode, scenario string
		refused, known       bool
	}{
		{"secrets", "1.5", "alone", true, false},
		{"secrets", "1.5", "later 0400", false, false},
		{"secrets", "1.5", "later !override", false, false},
		{"secrets", "1.5", "later !reset", false, false},
		{"secrets", "1.5", "later nothing", false, false},
		{"secrets", "~", "alone", true, false},
		{"secrets", "~", "later 0400", true, false},
		{"secrets", "~", "later !override", true, false},
		{"secrets", "~", "later !reset", true, false},
		{"secrets", "~", "later nothing", true, false},
		{"secrets", "true", "alone", true, false},
		{"secrets", "true", "later 0400", true, false},
		{"secrets", "true", "later !override", true, false},
		{"secrets", "true", "later !reset", true, false},
		{"secrets", "true", "later nothing", true, false},
		{"secrets", "[1]", "alone", true, false},
		{"secrets", "[1]", "later 0400", true, false},
		{"secrets", "[1]", "later !override", true, false},
		{"secrets", "[1]", "later !reset", true, false},
		{"secrets", "[1]", "later nothing", true, false},
		{"secrets", "host", "alone", true, false},
		{"secrets", "host", "later 0400", false, false},
		{"secrets", "host", "later !override", false, false},
		{"secrets", "host", "later !reset", false, false},
		{"secrets", "host", "later nothing", false, false},
		{"secrets", "\"\"", "alone", true, false},
		{"secrets", "\"\"", "later 0400", false, false},
		{"secrets", "\"\"", "later !override", false, false},
		{"secrets", "\"\"", "later !reset", false, false},
		{"secrets", "\"\"", "later nothing", false, false},
		{"secrets", "\"08\"", "alone", true, false},
		{"secrets", "\"08\"", "later 0400", false, false},
		{"secrets", "\"08\"", "later !override", false, false},
		{"secrets", "\"08\"", "later !reset", false, false},
		{"secrets", "\"08\"", "later nothing", false, false},
		{"secrets", "\"0o440\"", "alone", true, false},
		{"secrets", "\"0o440\"", "later 0400", false, false},
		{"secrets", "\"0o440\"", "later !override", false, false},
		{"secrets", "\"0o440\"", "later !reset", false, false},
		{"secrets", "\"0o440\"", "later nothing", false, false},
		{"secrets", "-0", "alone", true, true},
		{"secrets", "-0", "later 0400", false, false},
		{"secrets", "-0", "later !override", false, false},
		{"secrets", "-0", "later !reset", false, false},
		{"secrets", "-0", "later nothing", false, false},
		{"secrets", "18446744073709551616", "alone", true, false},
		{"secrets", "18446744073709551616", "later 0400", false, false},
		{"secrets", "18446744073709551616", "later !override", false, false},
		{"secrets", "18446744073709551616", "later !reset", false, false},
		{"secrets", "18446744073709551616", "later nothing", false, false},
		{"secrets", "!!float 1", "alone", true, false},
		{"secrets", "!!float 1", "later 0400", false, false},
		{"secrets", "!!float 1", "later !override", false, false},
		{"secrets", "!!float 1", "later !reset", false, false},
		{"secrets", "!!float 1", "later nothing", false, false},
		{"secrets", "{a: 1}", "alone", true, false},
		{"secrets", "{a: 1}", "later 0400", true, false},
		{"secrets", "{a: 1}", "later !override", true, false},
		{"secrets", "{a: 1}", "later !reset", true, false},
		{"secrets", "{a: 1}", "later nothing", true, false},
		{"secrets", "0440", "alone", false, false},
		{"secrets", "0440", "later 0400", false, false},
		{"secrets", "0440", "later !override", false, false},
		{"secrets", "0440", "later !reset", false, false},
		{"secrets", "0440", "later nothing", false, false},
		{"secrets", "\"0440\"", "alone", false, false},
		{"secrets", "\"0440\"", "later 0400", false, false},
		{"secrets", "\"0440\"", "later !override", false, false},
		{"secrets", "\"0440\"", "later !reset", false, false},
		{"secrets", "\"0440\"", "later nothing", false, false},
		{"secrets", "0o440", "alone", false, false},
		{"secrets", "0o440", "later 0400", false, false},
		{"secrets", "0o440", "later !override", false, false},
		{"secrets", "0o440", "later !reset", false, false},
		{"secrets", "0o440", "later nothing", false, false},
		{"secrets", "440", "alone", false, false},
		{"secrets", "440", "later 0400", false, false},
		{"secrets", "440", "later !override", false, false},
		{"secrets", "440", "later !reset", false, false},
		{"secrets", "440", "later nothing", false, false},
		{"secrets", "\"+1\"", "alone", false, false},
		{"secrets", "\"+1\"", "later 0400", false, false},
		{"secrets", "\"+1\"", "later !override", false, false},
		{"secrets", "\"+1\"", "later !reset", false, false},
		{"secrets", "\"+1\"", "later nothing", false, false},
		{"secrets", "\"-1\"", "alone", false, false},
		{"secrets", "\"-1\"", "later 0400", false, false},
		{"secrets", "\"-1\"", "later !override", false, false},
		{"secrets", "\"-1\"", "later !reset", false, false},
		{"secrets", "\"-1\"", "later nothing", false, false},
		{"secrets", "-1", "alone", false, false},
		{"secrets", "-1", "later 0400", false, false},
		{"secrets", "-1", "later !override", false, false},
		{"secrets", "-1", "later !reset", false, false},
		{"secrets", "-1", "later nothing", false, false},
		{"secrets", "\"37777777777\"", "alone", false, false},
		{"secrets", "\"37777777777\"", "later 0400", false, false},
		{"secrets", "\"37777777777\"", "later !override", false, false},
		{"secrets", "\"37777777777\"", "later !reset", false, false},
		{"secrets", "\"37777777777\"", "later nothing", false, false},
		{"secrets", "9223372036854775807", "alone", false, false},
		{"secrets", "9223372036854775807", "later 0400", false, false},
		{"secrets", "9223372036854775807", "later !override", false, false},
		{"secrets", "9223372036854775807", "later !reset", false, false},
		{"secrets", "9223372036854775807", "later nothing", false, false},
		{"secrets", "9223372036854775808", "alone", true, false},
		{"secrets", "9223372036854775808", "later 0400", false, false},
		{"secrets", "9223372036854775808", "later !override", false, false},
		{"secrets", "9223372036854775808", "later !reset", false, false},
		{"secrets", "9223372036854775808", "later nothing", false, false},
		{"secrets", "18446744073709551615", "alone", true, false},
		{"secrets", "18446744073709551615", "later 0400", false, false},
		{"secrets", "18446744073709551615", "later !override", false, false},
		{"secrets", "18446744073709551615", "later !reset", false, false},
		{"secrets", "18446744073709551615", "later nothing", false, false},
		{"secrets", "4294967296", "alone", false, false},
		{"secrets", "4294967296", "later 0400", false, false},
		{"secrets", "4294967296", "later !override", false, false},
		{"secrets", "4294967296", "later !reset", false, false},
		{"secrets", "4294967296", "later nothing", false, false},
		{"secrets", "1.0", "alone", true, false},
		{"secrets", "1.0", "later 0400", false, false},
		{"secrets", "1.0", "later !override", false, false},
		{"secrets", "1.0", "later !reset", false, false},
		{"secrets", "1.0", "later nothing", false, false},
		{"secrets", "\"1.5\"", "alone", true, false},
		{"secrets", "\"1.5\"", "later 0400", false, false},
		{"secrets", "\"1.5\"", "later !override", false, false},
		{"secrets", "\"1.5\"", "later !reset", false, false},
		{"secrets", "\"1.5\"", "later nothing", false, false},
		{"secrets", "010", "alone", false, false},
		{"secrets", "010", "later 0400", false, false},
		{"secrets", "010", "later !override", false, false},
		{"secrets", "010", "later !reset", false, false},
		{"secrets", "010", "later nothing", false, false},
		{"secrets", "\"010\"", "alone", false, false},
		{"secrets", "\"010\"", "later 0400", false, false},
		{"secrets", "\"010\"", "later !override", false, false},
		{"secrets", "\"010\"", "later !reset", false, false},
		{"secrets", "\"010\"", "later nothing", false, false},
		{"secrets", "0x1ff", "alone", false, false},
		{"secrets", "0x1ff", "later 0400", false, false},
		{"secrets", "0x1ff", "later !override", false, false},
		{"secrets", "0x1ff", "later !reset", false, false},
		{"secrets", "0x1ff", "later nothing", false, false},
		{"secrets", "\"0x1ff\"", "alone", true, false},
		{"secrets", "\"0x1ff\"", "later 0400", false, false},
		{"secrets", "\"0x1ff\"", "later !override", false, false},
		{"secrets", "\"0x1ff\"", "later !reset", false, false},
		{"secrets", "\"0x1ff\"", "later nothing", false, false},
		{"secrets", "1_0", "alone", false, false},
		{"secrets", "1_0", "later 0400", false, false},
		{"secrets", "1_0", "later !override", false, false},
		{"secrets", "1_0", "later !reset", false, false},
		{"secrets", "1_0", "later nothing", false, false},
		{"configs", "1.5", "alone", true, false},
		{"configs", "1.5", "later 0400", false, false},
		{"configs", "1.5", "later !override", false, false},
		{"configs", "1.5", "later !reset", false, false},
		{"configs", "1.5", "later nothing", false, false},
		{"configs", "~", "alone", true, false},
		{"configs", "~", "later 0400", true, false},
		{"configs", "~", "later !override", true, false},
		{"configs", "~", "later !reset", true, false},
		{"configs", "~", "later nothing", true, false},
		{"configs", "true", "alone", true, false},
		{"configs", "true", "later 0400", true, false},
		{"configs", "true", "later !override", true, false},
		{"configs", "true", "later !reset", true, false},
		{"configs", "true", "later nothing", true, false},
		{"configs", "[1]", "alone", true, false},
		{"configs", "[1]", "later 0400", true, false},
		{"configs", "[1]", "later !override", true, false},
		{"configs", "[1]", "later !reset", true, false},
		{"configs", "[1]", "later nothing", true, false},
		{"configs", "host", "alone", true, false},
		{"configs", "host", "later 0400", false, false},
		{"configs", "host", "later !override", false, false},
		{"configs", "host", "later !reset", false, false},
		{"configs", "host", "later nothing", false, false},
		{"configs", "\"\"", "alone", true, false},
		{"configs", "\"\"", "later 0400", false, false},
		{"configs", "\"\"", "later !override", false, false},
		{"configs", "\"\"", "later !reset", false, false},
		{"configs", "\"\"", "later nothing", false, false},
		{"configs", "\"08\"", "alone", true, false},
		{"configs", "\"08\"", "later 0400", false, false},
		{"configs", "\"08\"", "later !override", false, false},
		{"configs", "\"08\"", "later !reset", false, false},
		{"configs", "\"08\"", "later nothing", false, false},
		{"configs", "\"0o440\"", "alone", true, false},
		{"configs", "\"0o440\"", "later 0400", false, false},
		{"configs", "\"0o440\"", "later !override", false, false},
		{"configs", "\"0o440\"", "later !reset", false, false},
		{"configs", "\"0o440\"", "later nothing", false, false},
		{"configs", "-0", "alone", true, true},
		{"configs", "-0", "later 0400", false, false},
		{"configs", "-0", "later !override", false, false},
		{"configs", "-0", "later !reset", false, false},
		{"configs", "-0", "later nothing", false, false},
		{"configs", "18446744073709551616", "alone", true, false},
		{"configs", "18446744073709551616", "later 0400", false, false},
		{"configs", "18446744073709551616", "later !override", false, false},
		{"configs", "18446744073709551616", "later !reset", false, false},
		{"configs", "18446744073709551616", "later nothing", false, false},
		{"configs", "!!float 1", "alone", true, false},
		{"configs", "!!float 1", "later 0400", false, false},
		{"configs", "!!float 1", "later !override", false, false},
		{"configs", "!!float 1", "later !reset", false, false},
		{"configs", "!!float 1", "later nothing", false, false},
		{"configs", "{a: 1}", "alone", true, false},
		{"configs", "{a: 1}", "later 0400", true, false},
		{"configs", "{a: 1}", "later !override", true, false},
		{"configs", "{a: 1}", "later !reset", true, false},
		{"configs", "{a: 1}", "later nothing", true, false},
		{"configs", "0440", "alone", false, false},
		{"configs", "0440", "later 0400", false, false},
		{"configs", "0440", "later !override", false, false},
		{"configs", "0440", "later !reset", false, false},
		{"configs", "0440", "later nothing", false, false},
		{"configs", "\"0440\"", "alone", false, false},
		{"configs", "\"0440\"", "later 0400", false, false},
		{"configs", "\"0440\"", "later !override", false, false},
		{"configs", "\"0440\"", "later !reset", false, false},
		{"configs", "\"0440\"", "later nothing", false, false},
		{"configs", "0o440", "alone", false, false},
		{"configs", "0o440", "later 0400", false, false},
		{"configs", "0o440", "later !override", false, false},
		{"configs", "0o440", "later !reset", false, false},
		{"configs", "0o440", "later nothing", false, false},
		{"configs", "440", "alone", false, false},
		{"configs", "440", "later 0400", false, false},
		{"configs", "440", "later !override", false, false},
		{"configs", "440", "later !reset", false, false},
		{"configs", "440", "later nothing", false, false},
		{"configs", "\"+1\"", "alone", false, false},
		{"configs", "\"+1\"", "later 0400", false, false},
		{"configs", "\"+1\"", "later !override", false, false},
		{"configs", "\"+1\"", "later !reset", false, false},
		{"configs", "\"+1\"", "later nothing", false, false},
		{"configs", "\"-1\"", "alone", false, false},
		{"configs", "\"-1\"", "later 0400", false, false},
		{"configs", "\"-1\"", "later !override", false, false},
		{"configs", "\"-1\"", "later !reset", false, false},
		{"configs", "\"-1\"", "later nothing", false, false},
		{"configs", "-1", "alone", false, false},
		{"configs", "-1", "later 0400", false, false},
		{"configs", "-1", "later !override", false, false},
		{"configs", "-1", "later !reset", false, false},
		{"configs", "-1", "later nothing", false, false},
		{"configs", "\"37777777777\"", "alone", false, false},
		{"configs", "\"37777777777\"", "later 0400", false, false},
		{"configs", "\"37777777777\"", "later !override", false, false},
		{"configs", "\"37777777777\"", "later !reset", false, false},
		{"configs", "\"37777777777\"", "later nothing", false, false},
		{"configs", "9223372036854775807", "alone", false, false},
		{"configs", "9223372036854775807", "later 0400", false, false},
		{"configs", "9223372036854775807", "later !override", false, false},
		{"configs", "9223372036854775807", "later !reset", false, false},
		{"configs", "9223372036854775807", "later nothing", false, false},
		{"configs", "9223372036854775808", "alone", true, false},
		{"configs", "9223372036854775808", "later 0400", false, false},
		{"configs", "9223372036854775808", "later !override", false, false},
		{"configs", "9223372036854775808", "later !reset", false, false},
		{"configs", "9223372036854775808", "later nothing", false, false},
		{"configs", "18446744073709551615", "alone", true, false},
		{"configs", "18446744073709551615", "later 0400", false, false},
		{"configs", "18446744073709551615", "later !override", false, false},
		{"configs", "18446744073709551615", "later !reset", false, false},
		{"configs", "18446744073709551615", "later nothing", false, false},
		{"configs", "4294967296", "alone", false, false},
		{"configs", "4294967296", "later 0400", false, false},
		{"configs", "4294967296", "later !override", false, false},
		{"configs", "4294967296", "later !reset", false, false},
		{"configs", "4294967296", "later nothing", false, false},
		{"configs", "1.0", "alone", true, false},
		{"configs", "1.0", "later 0400", false, false},
		{"configs", "1.0", "later !override", false, false},
		{"configs", "1.0", "later !reset", false, false},
		{"configs", "1.0", "later nothing", false, false},
		{"configs", "\"1.5\"", "alone", true, false},
		{"configs", "\"1.5\"", "later 0400", false, false},
		{"configs", "\"1.5\"", "later !override", false, false},
		{"configs", "\"1.5\"", "later !reset", false, false},
		{"configs", "\"1.5\"", "later nothing", false, false},
		{"configs", "010", "alone", false, false},
		{"configs", "010", "later 0400", false, false},
		{"configs", "010", "later !override", false, false},
		{"configs", "010", "later !reset", false, false},
		{"configs", "010", "later nothing", false, false},
		{"configs", "\"010\"", "alone", false, false},
		{"configs", "\"010\"", "later 0400", false, false},
		{"configs", "\"010\"", "later !override", false, false},
		{"configs", "\"010\"", "later !reset", false, false},
		{"configs", "\"010\"", "later nothing", false, false},
		{"configs", "0x1ff", "alone", false, false},
		{"configs", "0x1ff", "later 0400", false, false},
		{"configs", "0x1ff", "later !override", false, false},
		{"configs", "0x1ff", "later !reset", false, false},
		{"configs", "0x1ff", "later nothing", false, false},
		{"configs", "\"0x1ff\"", "alone", true, false},
		{"configs", "\"0x1ff\"", "later 0400", false, false},
		{"configs", "\"0x1ff\"", "later !override", false, false},
		{"configs", "\"0x1ff\"", "later !reset", false, false},
		{"configs", "\"0x1ff\"", "later nothing", false, false},
		{"configs", "1_0", "alone", false, false},
		{"configs", "1_0", "later 0400", false, false},
		{"configs", "1_0", "later !override", false, false},
		{"configs", "1_0", "later !reset", false, false},
		{"configs", "1_0", "later nothing", false, false},
	} {
		t.Run(tc.kind+" / "+tc.mode+" / "+tc.scenario, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "s.txt"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			files := []string{filepath.Join(dir, "a.yaml")}
			if err := os.WriteFile(files[0], []byte(entry(tc.kind, tc.mode)), 0o644); err != nil {
				t.Fatal(err)
			}
			if extra, ok := later[tc.scenario]; ok {
				b := filepath.Join(dir, "b.yaml")
				if err := os.WriteFile(b, []byte("services:\n  a:\n    "+tc.kind+":\n      - source: k\n"+extra), 0o644); err != nil {
					t.Fatal(err)
				}
				files = append(files, b)
			}
			_, err := LoadFiles(files, nil)
			if tc.known {
				if err != nil {
					t.Logf("a difference left open: docker compose refuses %s mode %s where it stands, and this reads it", tc.kind, tc.mode)
				}
				return
			}
			if (err != nil) != tc.refused {
				t.Errorf("%s mode %s (%s): refused is %v, want %v (err %v)", tc.kind, tc.mode, tc.scenario, err != nil, tc.refused, err)
			}
		})
	}
}

// A later file's entry replaces an earlier one that is mounted at the same target, and one for another target is added:
// that is what decides whether a bad `mode` is still in the merged project (#1544; measured, v5.5.1). An entry that
// writes no target is mounted at the default of its kind, so the short form and the long form of one name are one entry.
func TestSecretAndConfigEntriesMergeByTheirTarget(t *testing.T) {
	for _, tc := range []struct {
		name, kind, a, b string
		refused          bool
	}{
		{"another target is added, the bad one stays", "secrets", "      - source: k\n        target: one\n        mode: 1.5\n", "      - source: k\n        target: two\n", true},
		{"the same target replaces, another source", "secrets", "      - source: k\n        target: same\n        mode: 1.5\n", "      - source: j\n        target: same\n", false},
		{"the short form replaces the default", "secrets", "      - source: k\n        mode: 1.5\n", "      - k\n", false},
		{"a relative target does not replace the default", "secrets", "      - source: k\n        mode: 1.5\n", "      - source: k\n        target: k\n", true},
		{"the long form with a target over the short form", "secrets", "      - k\n", "      - source: k\n        target: t\n        mode: 1.5\n", true},
		{"another target is added, the bad one stays", "configs", "      - source: k\n        target: one\n        mode: 1.5\n", "      - source: k\n        target: two\n", true},
		{"the same target replaces, another source", "configs", "      - source: k\n        target: same\n        mode: 1.5\n", "      - source: j\n        target: same\n", false},
		{"the short form replaces the default", "configs", "      - source: k\n        mode: 1.5\n", "      - k\n", false},
		{"the default written out replaces the default", "configs", "      - source: k\n        mode: 1.5\n", "      - source: k\n        target: /k\n", false},
		{"a relative target does not replace the default", "configs", "      - source: k\n        mode: 1.5\n", "      - source: k\n        target: k\n", true},
		{"the long form with a target over the short form", "configs", "      - k\n", "      - source: k\n        target: t\n        mode: 1.5\n", true},
	} {
		t.Run(tc.kind+" / "+tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "s.txt"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			a := "services:\n  a:\n    image: x\n    " + tc.kind + ":\n" + tc.a + tc.kind + ":\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"
			b := "services:\n  a:\n    " + tc.kind + ":\n" + tc.b
			pa, pb := filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")
			for p, body := range map[string]string{pa: a, pb: b} {
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, err := LoadFiles([]string{pa, pb}, nil)
			if (err != nil) != tc.refused {
				t.Errorf("refused is %v, want %v (err %v)", err != nil, tc.refused, err)
			}
		})
	}
}

// What is refused says where: the entry of the merged project and the mode as written.
func TestTheRefusalOfASecretModeNamesTheEntryAndTheMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "compose.yaml")
	body := "services:\n  a:\n    image: x\n    secrets:\n      - k\n      - source: k\n        target: t\n        mode: host\nsecrets:\n  k:\n    file: ./s.txt\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFiles([]string{p}, nil)
	if err == nil {
		t.Fatal("a mode that reads as no octal number was read")
	}
	for _, want := range []string{"services.a.secrets[1].mode", `"host"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal is %q, want it to say %q", err, want)
		}
	}
}

// What is asked of the project as docker compose asks it, not of each file alone (#1544; `config -q`, every row measured, for
// `secrets` and for `configs`): a service an extends takes from another file is put to the schema once it is merged with the
// service that extends it, so a null, bool, list or mapping `mode` that the extender writes the entry over, or that is in a service of that file which is
// not taken, passes, and one that stays is refused; a file that writes a target twice has the last entry only, before the kind
// of its `mode` is asked; a service whose profile is not enabled, and a second service, are asked as the first; a target written
// empty is no default one.
func TestTheModeOfASecretOrConfigEntryIsAskedOfTheProjectAsDockerComposeAsksIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   map[string]string
		order   []string
		refused bool
	}{
		{"secrets: extends, the extender writes the entry again, mode ~", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    secrets:\n      - source: k\n        mode: ~\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n    secrets:\n      - source: k\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"secrets: extends, the extender does not, mode ~", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    secrets:\n      - source: k\n        mode: ~\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, true},
		{"secrets: a service of the extended file that is not taken, mode ~", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n  other:\n    image: x\n    secrets:\n      - source: k\n        mode: ~\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"secrets: a service with a profile, mode ~", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    profiles: [p]\n    secrets:\n      - source: k\n        mode: ~\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: the second service, mode ~", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  b:\n    image: x\n    secrets:\n      - source: k\n        mode: ~\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: one file, the entry again after it, mode ~", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: k\n        mode: ~\n      - k\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, false},
		{"secrets: one file, the entry after another one, mode ~", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - k\n      - source: k\n        mode: ~\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: one file, a second entry with a bad mode behind a good one, mode ~", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: j\n        mode: 0440\n      - source: k\n        mode: ~\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: extends, the extender writes the entry again, mode true", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    secrets:\n      - source: k\n        mode: true\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n    secrets:\n      - source: k\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"secrets: extends, the extender does not, mode true", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    secrets:\n      - source: k\n        mode: true\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, true},
		{"secrets: a service of the extended file that is not taken, mode true", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n  other:\n    image: x\n    secrets:\n      - source: k\n        mode: true\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"secrets: a service with a profile, mode true", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    profiles: [p]\n    secrets:\n      - source: k\n        mode: true\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: the second service, mode true", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  b:\n    image: x\n    secrets:\n      - source: k\n        mode: true\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: one file, the entry again after it, mode true", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: k\n        mode: true\n      - k\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, false},
		{"secrets: one file, the entry after another one, mode true", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - k\n      - source: k\n        mode: true\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: one file, a second entry with a bad mode behind a good one, mode true", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: j\n        mode: 0440\n      - source: k\n        mode: true\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: extends, the extender writes the entry again, mode [1]", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    secrets:\n      - source: k\n        mode: [1]\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n    secrets:\n      - source: k\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"secrets: extends, the extender does not, mode [1]", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    secrets:\n      - source: k\n        mode: [1]\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, true},
		{"secrets: a service of the extended file that is not taken, mode [1]", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n  other:\n    image: x\n    secrets:\n      - source: k\n        mode: [1]\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"secrets: a service with a profile, mode [1]", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    profiles: [p]\n    secrets:\n      - source: k\n        mode: [1]\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: the second service, mode [1]", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  b:\n    image: x\n    secrets:\n      - source: k\n        mode: [1]\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: one file, the entry again after it, mode [1]", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: k\n        mode: [1]\n      - k\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, false},
		{"secrets: one file, the entry after another one, mode [1]", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - k\n      - source: k\n        mode: [1]\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: one file, a second entry with a bad mode behind a good one, mode [1]", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: j\n        mode: 0440\n      - source: k\n        mode: [1]\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: extends, the extender writes the entry again, mode {a: 1}", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    secrets:\n      - source: k\n        mode: {a: 1}\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n    secrets:\n      - source: k\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"secrets: extends, the extender does not, mode {a: 1}", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    secrets:\n      - source: k\n        mode: {a: 1}\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, true},
		{"secrets: a service of the extended file that is not taken, mode {a: 1}", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n  other:\n    image: x\n    secrets:\n      - source: k\n        mode: {a: 1}\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"secrets: a service with a profile, mode {a: 1}", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    profiles: [p]\n    secrets:\n      - source: k\n        mode: {a: 1}\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: the second service, mode {a: 1}", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  b:\n    image: x\n    secrets:\n      - source: k\n        mode: {a: 1}\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: one file, the entry again after it, mode {a: 1}", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: k\n        mode: {a: 1}\n      - k\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, false},
		{"secrets: one file, the entry after another one, mode {a: 1}", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - k\n      - source: k\n        mode: {a: 1}\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: one file, a second entry with a bad mode behind a good one, mode {a: 1}", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: j\n        mode: 0440\n      - source: k\n        mode: {a: 1}\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: extends, the extender writes the entry again, mode 1.5", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    secrets:\n      - source: k\n        mode: 1.5\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n    secrets:\n      - source: k\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"secrets: extends, the extender does not, mode 1.5", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    secrets:\n      - source: k\n        mode: 1.5\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, true},
		{"secrets: a service of the extended file that is not taken, mode 1.5", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n  other:\n    image: x\n    secrets:\n      - source: k\n        mode: 1.5\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"secrets: a service with a profile, mode 1.5", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    profiles: [p]\n    secrets:\n      - source: k\n        mode: 1.5\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: the second service, mode 1.5", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  b:\n    image: x\n    secrets:\n      - source: k\n        mode: 1.5\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: one file, the entry again after it, mode 1.5", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: k\n        mode: 1.5\n      - k\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, false},
		{"secrets: one file, the entry after another one, mode 1.5", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - k\n      - source: k\n        mode: 1.5\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: one file, a second entry with a bad mode behind a good one, mode 1.5", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: j\n        mode: 0440\n      - source: k\n        mode: 1.5\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: extends, the extender writes the entry again, mode 2001-01-01", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    secrets:\n      - source: k\n        mode: 2001-01-01\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n    secrets:\n      - source: k\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"secrets: extends, the extender does not, mode 2001-01-01", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    secrets:\n      - source: k\n        mode: 2001-01-01\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, true},
		{"secrets: a service of the extended file that is not taken, mode 2001-01-01", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n  other:\n    image: x\n    secrets:\n      - source: k\n        mode: 2001-01-01\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"secrets: a service with a profile, mode 2001-01-01", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    profiles: [p]\n    secrets:\n      - source: k\n        mode: 2001-01-01\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: the second service, mode 2001-01-01", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  b:\n    image: x\n    secrets:\n      - source: k\n        mode: 2001-01-01\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: one file, the entry again after it, mode 2001-01-01", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: k\n        mode: 2001-01-01\n      - k\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, false},
		{"secrets: one file, the entry after another one, mode 2001-01-01", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - k\n      - source: k\n        mode: 2001-01-01\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: one file, a second entry with a bad mode behind a good one, mode 2001-01-01", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: j\n        mode: 0440\n      - source: k\n        mode: 2001-01-01\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: extends, the extender writes the entry again, mode host", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    secrets:\n      - source: k\n        mode: host\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n    secrets:\n      - source: k\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"secrets: extends, the extender does not, mode host", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    secrets:\n      - source: k\n        mode: host\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, true},
		{"secrets: a service of the extended file that is not taken, mode host", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n  other:\n    image: x\n    secrets:\n      - source: k\n        mode: host\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"secrets: a service with a profile, mode host", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    profiles: [p]\n    secrets:\n      - source: k\n        mode: host\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: the second service, mode host", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  b:\n    image: x\n    secrets:\n      - source: k\n        mode: host\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: one file, the entry again after it, mode host", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: k\n        mode: host\n      - k\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, false},
		{"secrets: one file, the entry after another one, mode host", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - k\n      - source: k\n        mode: host\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: one file, a second entry with a bad mode behind a good one, mode host", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: j\n        mode: 0440\n      - source: k\n        mode: host\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"secrets: one file, same explicit target, another source", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: k\n        target: t\n        mode: 1.5\n      - source: j\n        target: t\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, false},
		{"secrets: an empty target is not the default", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    secrets:\n      - source: k\n        target: ''\n        mode: 1.5\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "b.yaml": "services:\n  a:\n    secrets:\n      - k\n"}, []string{"a.yaml", "b.yaml"}, true},
		{"configs: extends, the extender writes the entry again, mode ~", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    configs:\n      - source: k\n        mode: ~\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n    configs:\n      - source: k\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"configs: extends, the extender does not, mode ~", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    configs:\n      - source: k\n        mode: ~\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, true},
		{"configs: a service of the extended file that is not taken, mode ~", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n  other:\n    image: x\n    configs:\n      - source: k\n        mode: ~\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"configs: a service with a profile, mode ~", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    profiles: [p]\n    configs:\n      - source: k\n        mode: ~\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: the second service, mode ~", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  b:\n    image: x\n    configs:\n      - source: k\n        mode: ~\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: one file, the entry again after it, mode ~", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: k\n        mode: ~\n      - k\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, false},
		{"configs: one file, the entry after another one, mode ~", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - k\n      - source: k\n        mode: ~\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: one file, a second entry with a bad mode behind a good one, mode ~", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: j\n        mode: 0440\n      - source: k\n        mode: ~\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: extends, the extender writes the entry again, mode true", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    configs:\n      - source: k\n        mode: true\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n    configs:\n      - source: k\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"configs: extends, the extender does not, mode true", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    configs:\n      - source: k\n        mode: true\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, true},
		{"configs: a service of the extended file that is not taken, mode true", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n  other:\n    image: x\n    configs:\n      - source: k\n        mode: true\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"configs: a service with a profile, mode true", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    profiles: [p]\n    configs:\n      - source: k\n        mode: true\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: the second service, mode true", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  b:\n    image: x\n    configs:\n      - source: k\n        mode: true\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: one file, the entry again after it, mode true", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: k\n        mode: true\n      - k\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, false},
		{"configs: one file, the entry after another one, mode true", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - k\n      - source: k\n        mode: true\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: one file, a second entry with a bad mode behind a good one, mode true", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: j\n        mode: 0440\n      - source: k\n        mode: true\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: extends, the extender writes the entry again, mode [1]", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    configs:\n      - source: k\n        mode: [1]\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n    configs:\n      - source: k\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"configs: extends, the extender does not, mode [1]", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    configs:\n      - source: k\n        mode: [1]\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, true},
		{"configs: a service of the extended file that is not taken, mode [1]", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n  other:\n    image: x\n    configs:\n      - source: k\n        mode: [1]\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"configs: a service with a profile, mode [1]", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    profiles: [p]\n    configs:\n      - source: k\n        mode: [1]\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: the second service, mode [1]", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  b:\n    image: x\n    configs:\n      - source: k\n        mode: [1]\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: one file, the entry again after it, mode [1]", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: k\n        mode: [1]\n      - k\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, false},
		{"configs: one file, the entry after another one, mode [1]", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - k\n      - source: k\n        mode: [1]\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: one file, a second entry with a bad mode behind a good one, mode [1]", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: j\n        mode: 0440\n      - source: k\n        mode: [1]\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: extends, the extender writes the entry again, mode {a: 1}", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    configs:\n      - source: k\n        mode: {a: 1}\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n    configs:\n      - source: k\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"configs: extends, the extender does not, mode {a: 1}", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    configs:\n      - source: k\n        mode: {a: 1}\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, true},
		{"configs: a service of the extended file that is not taken, mode {a: 1}", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n  other:\n    image: x\n    configs:\n      - source: k\n        mode: {a: 1}\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"configs: a service with a profile, mode {a: 1}", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    profiles: [p]\n    configs:\n      - source: k\n        mode: {a: 1}\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: the second service, mode {a: 1}", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  b:\n    image: x\n    configs:\n      - source: k\n        mode: {a: 1}\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: one file, the entry again after it, mode {a: 1}", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: k\n        mode: {a: 1}\n      - k\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, false},
		{"configs: one file, the entry after another one, mode {a: 1}", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - k\n      - source: k\n        mode: {a: 1}\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: one file, a second entry with a bad mode behind a good one, mode {a: 1}", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: j\n        mode: 0440\n      - source: k\n        mode: {a: 1}\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: extends, the extender writes the entry again, mode 1.5", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    configs:\n      - source: k\n        mode: 1.5\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n    configs:\n      - source: k\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"configs: extends, the extender does not, mode 1.5", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    configs:\n      - source: k\n        mode: 1.5\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, true},
		{"configs: a service of the extended file that is not taken, mode 1.5", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n  other:\n    image: x\n    configs:\n      - source: k\n        mode: 1.5\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"configs: a service with a profile, mode 1.5", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    profiles: [p]\n    configs:\n      - source: k\n        mode: 1.5\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: the second service, mode 1.5", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  b:\n    image: x\n    configs:\n      - source: k\n        mode: 1.5\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: one file, the entry again after it, mode 1.5", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: k\n        mode: 1.5\n      - k\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, false},
		{"configs: one file, the entry after another one, mode 1.5", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - k\n      - source: k\n        mode: 1.5\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: one file, a second entry with a bad mode behind a good one, mode 1.5", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: j\n        mode: 0440\n      - source: k\n        mode: 1.5\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: extends, the extender writes the entry again, mode 2001-01-01", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    configs:\n      - source: k\n        mode: 2001-01-01\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n    configs:\n      - source: k\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"configs: extends, the extender does not, mode 2001-01-01", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    configs:\n      - source: k\n        mode: 2001-01-01\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, true},
		{"configs: a service of the extended file that is not taken, mode 2001-01-01", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n  other:\n    image: x\n    configs:\n      - source: k\n        mode: 2001-01-01\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"configs: a service with a profile, mode 2001-01-01", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    profiles: [p]\n    configs:\n      - source: k\n        mode: 2001-01-01\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: the second service, mode 2001-01-01", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  b:\n    image: x\n    configs:\n      - source: k\n        mode: 2001-01-01\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: one file, the entry again after it, mode 2001-01-01", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: k\n        mode: 2001-01-01\n      - k\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, false},
		{"configs: one file, the entry after another one, mode 2001-01-01", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - k\n      - source: k\n        mode: 2001-01-01\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: one file, a second entry with a bad mode behind a good one, mode 2001-01-01", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: j\n        mode: 0440\n      - source: k\n        mode: 2001-01-01\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: extends, the extender writes the entry again, mode host", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    configs:\n      - source: k\n        mode: host\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\n    configs:\n      - source: k\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"configs: extends, the extender does not, mode host", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n    configs:\n      - source: k\n        mode: host\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, true},
		{"configs: a service of the extended file that is not taken, mode host", map[string]string{"base.yaml": "services:\n  base:\n    image: x\n  other:\n    image: x\n    configs:\n      - source: k\n        mode: host\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: base}\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"compose.yaml"}, false},
		{"configs: a service with a profile, mode host", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    profiles: [p]\n    configs:\n      - source: k\n        mode: host\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: the second service, mode host", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n  b:\n    image: x\n    configs:\n      - source: k\n        mode: host\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: one file, the entry again after it, mode host", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: k\n        mode: host\n      - k\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, false},
		{"configs: one file, the entry after another one, mode host", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - k\n      - source: k\n        mode: host\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: one file, a second entry with a bad mode behind a good one, mode host", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: j\n        mode: 0440\n      - source: k\n        mode: host\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, true},
		{"configs: one file, same explicit target, another source", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: k\n        target: t\n        mode: 1.5\n      - source: j\n        target: t\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"}, []string{"a.yaml"}, false},
		{"configs: an empty target is not the default", map[string]string{"a.yaml": "services:\n  a:\n    image: x\n    configs:\n      - source: k\n        target: ''\n        mode: 1.5\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n", "b.yaml": "services:\n  a:\n    configs:\n      - k\n"}, []string{"a.yaml", "b.yaml"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "s.txt"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var paths []string
			for _, name := range tc.order {
				paths = append(paths, filepath.Join(dir, name))
			}
			_, err := LoadFiles(paths, nil)
			if (err != nil) != tc.refused {
				t.Errorf("refused is %v, want %v (err %v)", err != nil, tc.refused, err)
			}
		})
	}
}

// A load that goes on past a fault (the commands that take a project down) keeps the refusal of a mode that stays, as a fault of
// the project, and does not stop; and a file that writes a target twice reads as the one entry, in a file of its own as in two.
func TestTheRefusalOfASecretModeIsKeptWhereTheLoadGoesOn(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "compose.yaml")
	body := "services:\n  a:\n    image: x\n    secrets:\n      - source: k\n        mode: 1.5\nsecrets:\n  k:\n    file: ./s.txt\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := LoadFilesEnvDirSoft([]string{p}, nil, dir)
	if err != nil {
		t.Fatalf("the load stopped: %v", err)
	}
	if err := proj.CheckValueFaults(); err == nil || !strings.Contains(err.Error(), "services.a.secrets[0].mode") {
		t.Fatalf("the fault kept is %v, want the refusal of the mode", err)
	}
}

func TestEntriesOfOneTargetAreOneEntryInAFileOfItsOwn(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "compose.yaml")
	body := "services:\n  a:\n    image: x\n    secrets:\n      - k\n      - source: j\n        target: k\n    configs:\n      - k\n      - source: j\n        target: k\nsecrets:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\nconfigs:\n  k:\n    file: ./s.txt\n  j:\n    file: ./s.txt\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := LoadFiles([]string{p}, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := proj.Services["a"]
	if len(svc.Secrets) != 1 || svc.Secrets[0].Source != "j" {
		t.Errorf("secrets are %v, want the one entry for target k, the last written (source j)", svc.Secrets)
	}
	if len(svc.Configs) != 1 || svc.Configs[0].Source != "j" {
		t.Errorf("configs are %v, want the one entry for target /k, the last written (source j)", svc.Configs)
	}
}
