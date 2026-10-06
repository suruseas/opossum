package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// `cpus` is read as docker compose v5.5.1 reads it, in both places a count of CPUs is written
// (`cpus` and `deploy.resources.limits.cpus`; the same answers, measured with `docker compose
// config` for every row, the count being what docker compose gives, rounded up as the runtime
// takes a whole number). A number written bare is the number YAML reads it as (`010` is eight,
// `0x10` sixteen, `0o10` eight, `0b11` three: opossum read the digits as text, so `010` was
// ten and `0x10` was refused); a string is cast as `strconv.ParseFloat` casts it to a 32-bit float,
// with no blank trimmed around it, and what is not a finite number refused (`inf` and `1e39` gave the
// largest integer as the count of CPUs, and `NaN` no limit, with no word of either). A negative count is
// read by docker compose and refused by the engine at start; it is refused here at load, on
// purpose: the file cannot be run either way, and read, it would set no limit in silence.
func TestCPUsAreReadAsDockerComposeReadsThem(t *testing.T) {
	for _, tc := range []struct{ name, yaml, want string }{
		{"0x10", "0x10", "16"},
		{"0X10", "0X10", "16"},
		{"010", "010", "8"},
		{"0o10", "0o10", "8"},
		{"0b11", "0b11", "3"},
		{"1_000", "1_000", "1000"},
		{"1_", "1_", "1"},
		{"0x_10", "0x_10", "16"},
		{"1_0.5", "1_0.5", "11"},
		{"+2", "+2", "2"},
		{".5", ".5", "1"},
		{"5.", "5.", "5"},
		{"1e2", "1e2", "100"},
		{"2.0000000001", "2.0000000001", "2"},
		{"0.1", "0.1", "1"},
		{"0x1p3", "0x1p3", "8"},
		{"2", "2", "2"},
		{"1.5", "1.5", "2"},
		{"0", "0", ""},
		{"(review) a float on the middle of two 32-bit floats", "1.0000000596046448", "1"},
		{"(review) a float on the middle of two 32-bit floats, two", "2.00000011920928955078125", "2"},
		{"(review) bare 1_.5 (YAML reads a float)", "1_.5", "2"},
		{"(review) bare 1_e2 (YAML reads a float)", "1_e2", "100"},
		{"(review) bare 1__0.5", "1__0.5", "11"},
		{"(review) bare 1.5_", "1.5_", "2"},
		{"(review) a quoted !!float tag on hex", "!!float \"0x10\"", "16"},
		{"(review) a quoted !!int tag on octal", "!!int \"010\"", "8"},
		{"(review) minus zero", "-0", ""},
		{"(review) minus zero, quoted", "\"-0\"", ""},
		{"(review) minus zero point zero", "-0.0", ""},
		{"(review) an integer past 24 bits (32-bit rounds it)", "16777217", "16777216"},
		{"(review) 0.1 in 32 bits", "0.1", "1"},
		{"-2", "-2", "REFUSE"},         // docker reads it (the engine refuses it at start); refused here at load, on purpose
		{"\"-1\"", "\"-1\"", "REFUSE"}, // docker reads it (the engine refuses it at start); refused here at load, on purpose
		{"\"010\"", "\"010\"", "10"},
		{"\"0x10\"", "\"0x10\"", "REFUSE"},
		{"\"2.0000000001\"", "\"2.0000000001\"", "2"},
		{"\"1_0\"", "\"1_0\"", "10"},
		{"\"1_.5\"", "\"1_.5\"", "REFUSE"},
		{"\"0x1p3\"", "\"0x1p3\"", "8"},
		{"\"2.0\"", "\"2.0\"", "2"},
		{"\"+2\"", "\"+2\"", "2"},
		{"\".5\"", "\".5\"", "1"},
		{"\"5.\"", "\"5.\"", "5"},
		{"\"1e2\"", "\"1e2\"", "100"},
		{"\"1.5\"", "\"1.5\"", "2"},
		{"\" 2\"", "\" 2\"", "REFUSE"},
		{"\"2 \"", "\"2 \"", "REFUSE"},
		{"\" \"", "\" \"", "REFUSE"},
		{"\"inf\"", "\"inf\"", "REFUSE"},
		{"\"NaN\"", "\"NaN\"", "REFUSE"},
		{"\"+Inf\"", "\"+Inf\"", "REFUSE"},
		{"\"-inf\"", "\"-inf\"", "REFUSE"},
		{"\"Infinity\"", "\"Infinity\"", "REFUSE"},
		{"\"+nan\"", "\"+nan\"", "REFUSE"},
		{"\"1e39\"", "\"1e39\"", "REFUSE"},
		{"\".inf\"", "\".inf\"", "REFUSE"},
		{"inf-native", ".inf", "REFUSE"},
		{"nan-native", ".nan", "REFUSE"},
		{"\"abc\"", "\"abc\"", "REFUSE"},
		{"\"1,5\"", "\"1,5\"", "REFUSE"},
		{"\"2\\n\"", "\"2\\n\"", "REFUSE"},
	} {
		for place, doc := range map[string]string{
			"cpus":   "services:\n  a:\n    image: x\n    cpus: %s\n",
			"deploy": "services:\n  a:\n    image: x\n    deploy:\n      resources:\n        limits:\n          cpus: %s\n",
		} {
			t.Run(place+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "compose.yaml")
				if err := os.WriteFile(path, []byte(fmt.Sprintf(doc, tc.yaml)), 0o644); err != nil {
					t.Fatal(err)
				}
				p, err := Load(path)
				got := "REFUSE"
				if err == nil {
					_, cpu, rerr := p.Services["a"].Resources()
					if rerr != nil {
						t.Fatalf("Resources after a load that passed: %v", rerr)
					}
					got = cpu
				}
				if got != tc.want {
					t.Errorf("cpus: %s: %q, want %q (err %v)", tc.yaml, got, tc.want, err)
				}
			})
		}
	}
}
