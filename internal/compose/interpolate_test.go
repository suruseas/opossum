package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lk builds a varLookup from a map; a key present with an empty string counts as
// "set" (matching an env var exported as empty).
func lk(m map[string]string) varLookup {
	return func(name string) (string, bool) {
		v, ok := m[name]
		return v, ok
	}
}

func TestInterpolateForms(t *testing.T) {
	env := lk(map[string]string{
		"IMAGE": "postgres:16",
		"EMPTY": "",
		"PORT":  "5432",
	})
	cases := []struct{ in, want string }{
		{"image: ${IMAGE}", "image: postgres:16"},
		{"image: $IMAGE", "image: postgres:16"},   // braceless
		{"port: ${MISSING:-9000}", "port: 9000"},  // default when unset
		{"port: ${PORT:-9000}", "port: 5432"},     // set wins over default
		{"x: ${EMPTY:-fallback}", "x: fallback"},  // :- treats empty as missing
		{"x: ${EMPTY-fallback}", "x: "},           // - keeps a set-but-empty value
		{"x: ${MISSING-fallback}", "x: fallback"}, // - defaults only when truly unset
		{"x: ${MISSING}", "x: "},                  // undefined, no default -> empty
		{"pw: a$$b", "pw: a$b"},                   // $$ escape
		{"cost: 5$ each", "cost: 5$ each"},        // lone $ is literal
	}
	for _, c := range cases {
		got, err := interpolate([]byte(c.in), env)
		if err != nil {
			t.Errorf("interpolate(%q) error: %v", c.in, err)
			continue
		}
		if string(got) != c.want {
			t.Errorf("interpolate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestInterpolateRequiredVar(t *testing.T) {
	_, err := interpolate([]byte("image: ${NEEDED:?must be set}"), lk(nil))
	if err == nil {
		t.Fatal("expected an error for an unset required variable")
	}
	if !strings.Contains(err.Error(), "NEEDED") || !strings.Contains(err.Error(), "must be set") {
		t.Errorf("error should name the variable and message, got: %v", err)
	}
	// A provided value satisfies the requirement.
	got, err := interpolate([]byte("image: ${NEEDED:?must be set}"), lk(map[string]string{"NEEDED": "x"}))
	if err != nil || string(got) != "image: x" {
		t.Errorf("required var with value: got %q, err %v", got, err)
	}
}

func TestInterpolateUnterminated(t *testing.T) {
	if _, err := interpolate([]byte("image: ${OOPS"), lk(nil)); err == nil {
		t.Fatal("expected an error for an unterminated ${ reference")
	}
}

func TestParseDotEnv(t *testing.T) {
	dir := t.TempDir()
	body := "" +
		"# a comment\n" +
		"\n" +
		"IMAGE=postgres:16\n" +
		"  SPACED  =  value  \n" +
		"QUOTED=\"quoted value\"\n" +
		"SQUOTED='single'\n" +
		"EMPTY=\n" +
		"export EXPORTED=yes\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := parseDotEnv(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatalf("parseDotEnv: %v", err)
	}
	want := map[string]string{
		"IMAGE":    "postgres:16",
		"SPACED":   "value",
		"QUOTED":   "quoted value",
		"SQUOTED":  "single",
		"EMPTY":    "",
		"EXPORTED": "yes",
	}
	for k, v := range want {
		if got, ok := m[k]; !ok || got != v {
			t.Errorf(".env[%q] = %q (ok=%v), want %q", k, got, ok, v)
		}
	}
}

// A multi-line quoted value (e.g. a PEM key) spans several lines until its
// closing quote — the same as docker compose's env_file handling. This also
// covers the `KEY: value` (colon) separator docker compose accepts.
func TestParseDotEnvMultiline(t *testing.T) {
	dir := t.TempDir()
	pem := "-----BEGIN PUBLIC KEY-----\nMIIBLine1\nMIIBLine2\n-----END PUBLIC KEY-----"
	body := "" +
		"DQUOTE=\"" + pem + "\"\n" + // double-quoted, `=` separator
		"SQUOTE: '" + pem + "'\n" + // single-quoted, `:` separator (the reported case)
		"COLON: plain\n" + // `:` separator, single line
		"AFTER=tail\n" // a normal line after a multi-line value still parses
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := parseDotEnv(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatalf("parseDotEnv: %v", err)
	}
	want := map[string]string{
		"DQUOTE": pem,
		"SQUOTE": pem,
		"COLON":  "plain",
		"AFTER":  "tail",
	}
	for k, v := range want {
		if got, ok := m[k]; !ok || got != v {
			t.Errorf(".env[%q] = %q (ok=%v), want %q", k, got, ok, v)
		}
	}
}

// An opening quote with no closing quote is an error, matching docker compose
// (a truncated PEM key should fail loudly, not silently pass a wrong value).
func TestParseDotEnvUnterminatedQuoteErrors(t *testing.T) {
	dir := t.TempDir()
	body := "GOOD=ok\nBAD=\"-----BEGIN PUBLIC KEY-----\nMIIBLine1\n" // no closing quote
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := parseDotEnv(filepath.Join(dir, ".env")); err == nil {
		t.Fatal("expected an error for an unterminated quoted value")
	}
}

func TestParseDotEnvMissingIsEmpty(t *testing.T) {
	m, err := parseDotEnv(filepath.Join(t.TempDir(), "nope.env"))
	if err != nil || len(m) != 0 {
		t.Errorf("missing .env should yield empty map, no error; got %v, %v", m, err)
	}
}

// writeProject writes a compose file and (optionally) a .env alongside it, then
// returns the compose path — exercising the full Load interpolation path.
func writeProject(t *testing.T, compose, dotenv string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(p, []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	if dotenv != "" {
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(dotenv), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestLoadInterpolatesFromDotEnv(t *testing.T) {
	p := writeProject(t, `
services:
  db:
    image: ${DB_IMAGE}
    ports:
      - "${DB_PORT:-5432}:5432"
  cache:
    image: redis:${REDIS_TAG:-7}
`, "DB_IMAGE=postgres:16\nDB_PORT=6000\n")
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := proj.Services["db"].Image; got != "postgres:16" {
		t.Errorf("db image = %q, want interpolated from .env", got)
	}
	if got := proj.Services["db"].Ports[0]; got != "6000:5432" {
		t.Errorf("db port = %q, want .env value applied", got)
	}
	if got := proj.Services["cache"].Image; got != "redis:7" {
		t.Errorf("cache image = %q, want default tag applied", got)
	}
}

func TestLoadShellEnvOverridesDotEnv(t *testing.T) {
	t.Setenv("DB_IMAGE", "postgres:17") // shell wins over .env
	p := writeProject(t, `
services:
  db:
    image: ${DB_IMAGE}
`, "DB_IMAGE=postgres:16\n")
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := proj.Services["db"].Image; got != "postgres:17" {
		t.Errorf("db image = %q, want shell env to override .env", got)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// An explicit --env-file replaces the default .env (docker compose): values from
// .env that the env-file doesn't set are gone.
func TestLoadEnvFileReplacesDotEnv(t *testing.T) {
	dir := t.TempDir()
	cfile := filepath.Join(dir, "compose.yaml")
	mustWriteFile(t, cfile, "services:\n  web:\n    image: \"i-${FOO:-none}-${BAR:-none}\"\n")
	mustWriteFile(t, filepath.Join(dir, ".env"), "FOO=dot\nBAR=dot\n")
	custom := filepath.Join(dir, "custom.env")
	mustWriteFile(t, custom, "FOO=custom\n")

	proj, err := Load(cfile, custom)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := proj.Services["web"].Image; got != "i-custom-none" {
		t.Errorf("env-file should replace .env, got %q", got)
	}
}

func TestLoadEnvFilesLaterWins(t *testing.T) {
	dir := t.TempDir()
	cfile := filepath.Join(dir, "compose.yaml")
	mustWriteFile(t, cfile, "services:\n  web:\n    image: \"i-${FOO:-none}-${BAZ:-none}\"\n")
	a := filepath.Join(dir, "a.env")
	mustWriteFile(t, a, "FOO=a\n")
	b := filepath.Join(dir, "b.env")
	mustWriteFile(t, b, "FOO=b\nBAZ=b\n")

	proj, err := Load(cfile, a, b)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := proj.Services["web"].Image; got != "i-b-b" {
		t.Errorf("later env-file should win, got %q", got)
	}
}

func TestLoadEnvFileShellStillOverrides(t *testing.T) {
	t.Setenv("FOO", "shell")
	dir := t.TempDir()
	cfile := filepath.Join(dir, "compose.yaml")
	mustWriteFile(t, cfile, "services:\n  web:\n    image: \"i-${FOO}\"\n")
	custom := filepath.Join(dir, "custom.env")
	mustWriteFile(t, custom, "FOO=custom\n")

	proj, err := Load(cfile, custom)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := proj.Services["web"].Image; got != "i-shell" {
		t.Errorf("shell env should override an --env-file value, got %q", got)
	}
}

func TestLoadEnvFileFlagMissingErrors(t *testing.T) {
	dir := t.TempDir()
	cfile := filepath.Join(dir, "compose.yaml")
	mustWriteFile(t, cfile, "services:\n  web:\n    image: x\n")
	if _, err := Load(cfile, filepath.Join(dir, "nope.env")); err == nil {
		t.Fatal("a missing --env-file should be an error")
	}
}

func TestLoadRequiredVarUnsetFails(t *testing.T) {
	p := writeProject(t, `
services:
  db:
    image: ${DB_IMAGE:?set DB_IMAGE first}
`, "")
	if _, err := Load(p); err == nil {
		t.Fatal("expected Load to fail when a required variable is unset")
	}
}
