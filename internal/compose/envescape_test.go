package compose

// Escapes inside a quoted `.env` value (#829). docker compose (v5.5.0),
// measured one line per run: between double quotes `\"`, `\\` and `\$`
// stand for the character after the backslash and `\n`, `\t`, `\r` for the
// control character (so do `\a` and `\b`, which opossum does not read),
// while any other pair (`\x`, `\'`) is kept as written;
// between single quotes only `\'` is read and everything else is kept;
// an unquoted value keeps every backslash. opossum kept every backslash as
// written in all three (`"say \"hi\""` was `say \"hi\"`).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnEscapeInsideAQuotedDotEnvValueIsReadAsDockerComposeReadsIt(t *testing.T) {
	cases := []struct{ name, line, want string }{
		{"a quote between double quotes", `OPOSSUM_X_A="a\"b"`, `a"b`},
		{"a backslash between double quotes", `OPOSSUM_X_B="a\\b"`, `a\b`},
		{"a newline between double quotes", `OPOSSUM_X_C="a\nb"`, "a\nb"},
		{"a tab between double quotes", `OPOSSUM_X_D="a\tb"`, "a\tb"},
		{"a carriage return between double quotes", `OPOSSUM_X_E="a\rb"`, "a\rb"},
		{"a dollar between double quotes", `OPOSSUM_X_F="a\$b"`, `a$b`},
		{"an unknown pair between double quotes is kept", `OPOSSUM_X_G="a\xb"`, `a\xb`},
		{"a single quote between double quotes is kept", `OPOSSUM_X_H="a\'b"`, `a\'b`},
		{"a single quote between single quotes", `OPOSSUM_X_I='a\'b'`, `a'b`},
		{"a backslash between single quotes is kept", `OPOSSUM_X_J='a\\b'`, `a\\b`},
		{"a newline between single quotes is kept", `OPOSSUM_X_K='a\nb'`, `a\nb`},
		{"a double quote between single quotes is kept", `OPOSSUM_X_L='a\"b'`, `a\"b`},
		{"an unquoted value keeps its backslashes", `OPOSSUM_X_M=a\nb`, `a\nb`},
		{"a backslash at the end between double quotes", `OPOSSUM_X_N="a\\"`, `a\`},
		{"two backslashes between double quotes", `OPOSSUM_X_O="\\\\"`, `\\`},
		{"a dollar between single quotes is kept, and not expanded", `OPOSSUM_X_P='a\$b'`, `a\$b`},
		{"two escaped dollars between double quotes", `OPOSSUM_X_Q="\$\$"`, `$$`},
	}
	if len(cases) != 17 {
		t.Fatalf("the table has %d cases, want 17", len(cases))
	}
	var lines, refs []string
	for _, tc := range cases {
		key := strings.SplitN(tc.line, "=", 2)[0]
		unsetHostVars(t, key)
		lines = append(lines, tc.line)
		refs = append(refs, "      "+key+": ${"+key+"}")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	compose := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(compose, []byte("services:\n  web:\n    image: alpine\n    environment:\n"+strings.Join(refs, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(compose)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := map[string]string{}
	for _, kv := range p.Services["web"].Environment {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := strings.SplitN(tc.line, "=", 2)[0]
			if got[key] != tc.want {
				t.Errorf("%s = %q, want %q", tc.line, got[key], tc.want)
			}
		})
	}
	// A quoted value that spans lines is read the same way: an escaped
	// quote on the first line, on a continuation line or on the closing
	// line does not close it, and between single quotes `\\` stays as
	// written (measured on docker compose v5.5.0).
	for _, tc := range []struct{ name, file, want string }{
		{"an escaped quote on the first line", "OPOSSUM_X_ML=\"one \\\"two\\\"\nthree\"\n", "one \"two\"\nthree"},
		{"an escaped quote on a continuation line", "OPOSSUM_X_ML=\"one\ntwo \\\"x\\\"\nthree\"\n", "one\ntwo \"x\"\nthree"},
		{"an escaped quote on the closing line", "OPOSSUM_X_ML=\"one\ntwo \\\"x\\\" end\"\n", "one\ntwo \"x\" end"},
		{"between single quotes a backslash pair stays", "OPOSSUM_X_ML='one\\\\\ntwo \\'x\\''\n", "one\\\\\ntwo 'x'"},
	} {
		t.Run("multi-line, "+tc.name, func(t *testing.T) {
			unsetHostVars(t, "OPOSSUM_X_ML")
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(tc.file), 0o644); err != nil {
				t.Fatal(err)
			}
			compose := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(compose, []byte("services:\n  web:\n    image: alpine\n    environment:\n      OPOSSUM_X_ML: ${OPOSSUM_X_ML}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := Load(compose)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := p.Services["web"].Environment; len(got) != 1 || got[0] != "OPOSSUM_X_ML="+tc.want {
				t.Errorf("got %q, want %q", got, "OPOSSUM_X_ML="+tc.want)
			}
		})
	}
}
