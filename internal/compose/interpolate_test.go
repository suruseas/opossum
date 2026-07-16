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

// The colon-less required form `${VAR?}` errors only when the variable is truly
// unset — unlike `${VAR:?}`, a set-but-empty value satisfies it (mirroring the
// `-` vs `:-` distinction).
func TestInterpolateRequiredVarNoColon(t *testing.T) {
	// Unset -> error, with the default message when none is given.
	_, err := interpolate([]byte("image: ${NEEDED?}"), lk(nil))
	if err == nil || !strings.Contains(err.Error(), "NEEDED") {
		t.Fatalf("unset ${VAR?} should error naming the var, got: %v", err)
	}
	// Set-but-empty satisfies the no-colon form (returns empty, no error).
	got, err := interpolate([]byte("x: ${NEEDED?}"), lk(map[string]string{"NEEDED": ""}))
	if err != nil || string(got) != "x: " {
		t.Errorf("set-but-empty ${VAR?} should be accepted, got %q err %v", got, err)
	}
	// The colon form rejects the same empty value.
	if _, err := interpolate([]byte("x: ${NEEDED:?}"), lk(map[string]string{"NEEDED": ""})); err == nil {
		t.Error("${VAR:?} should reject a set-but-empty value")
	}
	// A real value satisfies both forms.
	if got, err := interpolate([]byte("image: ${NEEDED?}"), lk(map[string]string{"NEEDED": "x"})); err != nil || string(got) != "image: x" {
		t.Errorf("${VAR?} with value: got %q err %v", got, err)
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

// envValue returns the value of KEY from a normalized KEY=value list, or "".
func envValue(env []string, key string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// stubHostGateway overrides the built-in host-gateway resolver for a test.
func stubHostGateway(t *testing.T, addr string) {
	t.Helper()
	prev := hostGatewayFunc
	hostGatewayFunc = func() string { return addr }
	t.Cleanup(func() { hostGatewayFunc = prev })
}

// The built-in OPOSSUM_HOST_GATEWAY resolves to the host's reachable address so a
// compose file can point a container at a service running on the host.
func TestLoadInjectsHostGateway(t *testing.T) {
	stubHostGateway(t, "192.168.11.22")
	p := writeProject(t, `
services:
  app:
    image: app
    environment:
      OLLAMA_HOST: http://${OPOSSUM_HOST_GATEWAY}:11434
`, "")
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := envValue(proj.Services["app"].Environment, "OLLAMA_HOST"); got != "http://192.168.11.22:11434" {
		t.Errorf("OLLAMA_HOST = %q, want host gateway injected", got)
	}
}

// A shell env var of the same name overrides the built-in, so users keep control.
func TestLoadHostGatewayShellOverrides(t *testing.T) {
	stubHostGateway(t, "192.168.11.22")
	t.Setenv("OPOSSUM_HOST_GATEWAY", "10.0.0.5")
	p := writeProject(t, `
services:
  app:
    image: app
    environment:
      OLLAMA_HOST: http://${OPOSSUM_HOST_GATEWAY}:11434
`, "")
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := envValue(proj.Services["app"].Environment, "OLLAMA_HOST"); got != "http://10.0.0.5:11434" {
		t.Errorf("OLLAMA_HOST = %q, want shell env to override built-in", got)
	}
}

// A `.env` entry of the same name also overrides the built-in — this pins the
// third precedence tier (shell > .env > built-in), not just shell > built-in.
func TestLoadHostGatewayDotEnvOverrides(t *testing.T) {
	stubHostGateway(t, "192.168.11.22")
	p := writeProject(t, `
services:
  app:
    image: app
    environment:
      OLLAMA_HOST: http://${OPOSSUM_HOST_GATEWAY}:11434
`, "OPOSSUM_HOST_GATEWAY=10.1.2.3\n")
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := envValue(proj.Services["app"].Environment, "OLLAMA_HOST"); got != "http://10.1.2.3:11434" {
		t.Errorf("OLLAMA_HOST = %q, want .env to override built-in", got)
	}
}

// When the host address can't be determined the variable stays unset, so a `:-`
// default still applies (e.g. an offline host).
func TestLoadHostGatewayUnsetUsesDefault(t *testing.T) {
	stubHostGateway(t, "")
	p := writeProject(t, `
services:
  app:
    image: app
    environment:
      OLLAMA_HOST: http://${OPOSSUM_HOST_GATEWAY:-host.example}:11434
`, "")
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := envValue(proj.Services["app"].Environment, "OLLAMA_HOST"); got != "http://host.example:11434" {
		t.Errorf("OLLAMA_HOST = %q, want default applied when gateway unknown", got)
	}
}
