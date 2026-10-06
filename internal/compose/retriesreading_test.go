package compose

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// `healthcheck.retries` is read as docker compose v5.5.1 reads it (`docker compose config -q`, every row measured:
// the refusals are the rows docker compose refuses, the rest it reads: -0.5 is refused as a negative number is, #1774). It reads the count as an unsigned
// 64-bit number, so a whole number up to 2^64-1 is one it takes (in decimal, a larger one is read as a float), and a float is one of any size (1e10
// and 1e300 are read); opossum refused everything from 2^63 up and every float past 2^31. A count past what an int holds is kept as the largest: the
// wait is as long as it takes, and the file says so. A count of nothing (0, 0.5, -0.0) is the default of 3.
func TestHealthcheckRetriesAreReadAsDockerComposeReadsThem(t *testing.T) {
	max := strconv.Itoa(math.MaxInt)
	for _, tc := range []struct{ yaml, want string }{
		{"0", "3"},
		{"1", "1"},
		{"2147483648", "2147483648"},
		{"9223372036854775807", max},
		{"9223372036854775808", max},
		{"18446744073709551615", max},
		{"18446744073709551616", max},
		{"-1", "REFUSE"},
		{"1.5", "1"},
		{"\"3\"", "3"},
		{"\"abc\"", "REFUSE"},
		{"1e3", "1000"},
		{"0x10", "16"},
		{".inf", "REFUSE"},
		{"1e10", "10000000000"},
		{"1e19", max},
		{"1e30", max},
		{"1e300", max},
		{"2147483647.5", "2147483647"},
		{"4294967296.0", "4294967296"},
		{"18446744073709551616.0", max},
		{"-1.5", "REFUSE"},
		{"-1e30", "REFUSE"},
		{"0.5", "3"},
		{".nan", "REFUSE"},
		{"-.inf", "REFUSE"},
		{"-9223372036854775809", "REFUSE"},
		{"-0.0", "3"},
		{"-0.5", "REFUSE"}, // docker compose refuses it where it stands, as a negative number is, and reads it where a later file writes the count over it (#1774: asked once the files are merged)
		{"-1e-5", "REFUSE"},
		{"!!int abc", "REFUSE"}, // a tag that says whole number over what is not one: no count
		{"!!int 0xFFFFFFFFFFFFFFFFFF", "REFUSE"},
		{"!!int 18446744073709551616", "REFUSE"},
		{"9223372036854775807.0", max},
		{"9223372036854775808.0", max},
		{"1e-5", "3"},
		{"1.0e+400", "REFUSE"},
		{"0xFFFFFFFFFFFFFFFF", max},
		{"0xFFFFFFFFFFFFFFFFFF", "REFUSE"},
		{"0o1777777777777777777777", max},
		{"0o2000000000000000000000", "REFUSE"},
		{"0b1111111111111111111111111111111111111111111111111111111111111111", max},
		{"0b11111111111111111111111111111111111111111111111111111111111111111", "REFUSE"},
		{"1_000_000_000_000_000_000_000", max},
		{"9_223_372_036_854_775_808", max},
		{"-0x8000000000000001", "REFUSE"},
		{"\"9223372036854775808\"", "REFUSE"},
	} {
		t.Run(tc.yaml, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "compose.yaml")
			doc := fmt.Sprintf("services:\n  a:\n    image: x\n    healthcheck:\n      test: [CMD, \"true\"]\n      retries: %s\n", tc.yaml)
			if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := Load(path)
			got := "REFUSE"
			if err == nil {
				got = strconv.Itoa(p.Services["a"].Healthcheck.Retries)
			}
			if got != tc.want {
				t.Errorf("retries: %s: %s, want %s (err %v)", tc.yaml, got, tc.want, err)
			}
			// A count the tag calls a whole number and the number is not: the refusal is this field's own,
			// not the generic one about a tag over a value it does not fit.
			if strings.HasPrefix(tc.yaml, "!!int") && tc.want == "REFUSE" && err != nil && !strings.Contains(err.Error(), "healthcheck retries") {
				t.Errorf("retries: %s: refused, but not by the count's own check: %v", tc.yaml, err)
			}
		})
	}
}
