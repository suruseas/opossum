package compose

// Where a `.env` value ends (#827). docker compose (v5.5.0) reads an
// unquoted value up to the first ` #` — a space, then a hash — and drops
// the comment: `H=with # hash` is `with`. `a#b` is whole, a tab before the
// hash does not count, and a value that is only `# …` after the `=` is
// kept. A quoted value is what stands between its quotes, and whatever
// follows the closing quote — a comment or anything else — is dropped:
// `"q" # note` is `q`; the closing quote is the first one not preceded by a
// backslash, in either style. (The contents are kept as written — docker
// compose turns `\"` into `"` and `\\` into `\`, and opossum does not; that
// predates this.) opossum kept the whole line after the `=`: `with #
// hash`, and `"q" # note` with its quotes still on. The difference showed
// once expansions were read as text (#824); before that the YAML comment
// happened to eat ` # hash` in the unquoted case and, wrongly, in the
// quoted one too.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestADotEnvValueEndsAtItsCommentOrClosingQuote(t *testing.T) {
	// Measured on docker compose v5.5.0, one line per case.
	cases := []struct{ name, line, want string }{
		{"a space and a hash start a comment", "OPOSSUM_E_A=with # hash", "with"},
		{"inside double quotes a hash is text", `OPOSSUM_E_B="with # hash"`, "with # hash"},
		{"inside single quotes a hash is text", `OPOSSUM_E_C='with # hash'`, "with # hash"},
		{"a hash with no space before it is text", "OPOSSUM_E_D=a#b", "a#b"},
		{"the comment need not have a space after the hash", "OPOSSUM_E_E=a #b", "a"},
		{"a tab before the hash does not start a comment", "OPOSSUM_E_F=a\t# tab", "a\t# tab"},
		{"a value that starts with a hash is the value", "OPOSSUM_E_G=#lead", "#lead"},
		{"a hash first after the equals is the value", "OPOSSUM_E_H= # only", "# only"},
		{"after a closing double quote the rest is dropped", `OPOSSUM_E_I="q" # after quote`, "q"},
		{"only the first comment mark counts", "OPOSSUM_E_J=x # c1 # c2", "x"},
		{"text right after a closing quote is dropped too", `OPOSSUM_E_K="q"x`, "q"},
		{"after a closing single quote the rest is dropped", `OPOSSUM_E_L='a' # c`, "a"},
		{"the spaces before the hash are trimmed", "OPOSSUM_E_M=a   #  b", "a"},
		{"a quote inside the dropped comment is not the closing one", `OPOSSUM_E_N="a" # "b"`, "a"},
		{"quotes inside an unquoted value are text", `OPOSSUM_E_O=a "b" # c`, `a "b"`},
		{"an empty quoted value is empty", `OPOSSUM_E_P=""`, ""},
		{"a single-quoted value is still not expanded", `OPOSSUM_E_Q='${OPOSSUM_E_A}' # c`, "${OPOSSUM_E_A}"},
		{"an escaped double quote does not close the value", `OPOSSUM_E_R="a\"b" # c`, `a\"b`},
		{"an escaped single quote does not close the value", `OPOSSUM_E_S='a\'b'`, `a\'b`},
		{"an escaped backslash before the closing quote", `OPOSSUM_E_T="a\\" # c`, `a\\`},
		{"a tab before the comment is trimmed too", "OPOSSUM_E_U=a\t #b", "a"},
		{"an empty single-quoted value is empty", `OPOSSUM_E_V=''`, ""},
	}
	if len(cases) != 22 {
		t.Fatalf("the table has %d cases, want 22", len(cases))
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
}
