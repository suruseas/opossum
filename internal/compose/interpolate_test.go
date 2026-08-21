package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// withoutHostVar removes name from the environment for the duration of the test
// and puts it back afterwards. Tests about "which level wins" are meaningless if
// the host happens to define the key: the shell outranks every level under test,
// so a name as ordinary as K turns the whole assertion into a check of the
// developer's own environment.
func withoutHostVar(t *testing.T, name string) {
	t.Helper()
	// Go has no t.Unsetenv, so the removal is by hand — but t.Setenv first, for
	// two things it brings. It records the value and restores it at the end of
	// the test, including when the removal below leaves the variable unset
	// (measured, not assumed). And it panics if the test has called t.Parallel:
	// touching the process environment from a parallel test is the bug that guard
	// exists to catch, and doing this entirely by hand would have dropped it.
	t.Setenv(name, "")
	os.Unsetenv(name)
}

// unsetHostVars is withoutHostVar for the several names a fixture resolves. Most
// of these tests turn on a name as ordinary as A or URL, so "is this name set on
// the machine running the suite" decides whether they measure anything.
func unsetHostVars(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		withoutHostVar(t, n)
	}
}

// noScope is an empty outer scope and no built-in: nothing is set anywhere but
// the file itself.
func noScope() envScope {
	empty := func(string) (string, bool) { return "", false }
	return envScope{outer: empty, level: map[string]string{}, builtin: empty}
}

// lk builds a varLookup from a map; a key present with an empty string counts as
// "set" (matching an env var exported as empty).
func lk(m map[string]string) varLookup {
	return func(name string) (string, bool) {
		v, ok := m[name]
		return v, ok
	}
}

// interpolate is the plain expander, used on text that is not a YAML document:
// env-file values and `${VAR:-default}` arguments. It leaves `x: ` alone — the
// repair that turns that into `x: ""` belongs to the document path only, because
// a value like `LIB=${PREFIX}/lib:` is not a mapping and must not be "repaired".
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
	m, err := parseDotEnv(filepath.Join(dir, ".env"), noScope())
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
	// A `${…}` sits inside the key so the quoting rule is checked here too, not
	// only on single-line values: single quotes suppress expansion, double quotes
	// do not, and a multi-line value goes down a separate branch that would
	// otherwise be unguarded.
	pem := "-----BEGIN PUBLIC KEY-----\nMIIB${WHO}\nMIIBLine2\n-----END PUBLIC KEY-----"
	body := "" +
		"WHO=alice\n" +
		"DQUOTE=\"" + pem + "\"\n" + // double-quoted, `=` separator
		"SQUOTE: '" + pem + "'\n" + // single-quoted, `:` separator (the reported case)
		"COLON: plain\n" + // `:` separator, single line
		"AFTER=tail\n" // a normal line after a multi-line value still parses
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := parseDotEnv(filepath.Join(dir, ".env"), noScope())
	if err != nil {
		t.Fatalf("parseDotEnv: %v", err)
	}
	want := map[string]string{
		"WHO":    "alice",
		"DQUOTE": strings.Replace(pem, "${WHO}", "alice", 1),
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
	if _, err := parseDotEnv(filepath.Join(dir, ".env"), noScope()); err == nil {
		t.Fatal("expected an error for an unterminated quoted value")
	}
}

func TestParseDotEnvMissingIsEmpty(t *testing.T) {
	m, err := parseDotEnv(filepath.Join(t.TempDir(), "nope.env"), noScope())
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
	unsetHostVars(t, "FOO", "BAR", "BAZ", "DB_IMAGE", "DB_PORT", "REDIS_TAG")
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
	unsetHostVars(t, "BAR")
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
	unsetHostVars(t, "FOO", "BAR", "BAZ")
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
	unsetHostVars(t, "FOO", "BAR", "BAZ")
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
	unsetHostVars(t, "FOO")
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
	// The whole point is that it is unset; a host that exports it makes this pass
	// while testing nothing.
	unsetHostVars(t, "DB_IMAGE")
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
	// The shell outranks the built-in by design, so a host that exports this name
	// makes the stub invisible and every test below reads the developer's own
	// value instead. Seven tests depended on that not being the case.
	withoutHostVar(t, hostGatewayVar)
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

// A `.env` value that references another variable is expanded, which is what
// docker compose does and what opossum did not. Measured against compose v5.3.1
// case by case, because several of these rules are not the obvious ones and
// guessing them wrong is how the bug got written in the first place.
//
// The corpus case that surfaced it: Compose-Examples' mattermost writes
// `POSTGRES_DATA_PATH=${DOCKER_VOLUME_STORAGE:-/mnt/docker-volumes}/mattermost/psql`
// in `.env`. Left unexpanded, the `:` inside the default split the mount spec, so
// `${DOCKER_VOLUME_STORAGE` reached the runtime as a volume NAME and `up` died on
// `invalid volume name`. That is why the expected values here are written out
// rather than derived — each line pins one rule, and a rule that quietly changes
// has to break a named subtest.
func TestDotEnvValuesExpandTheWayComposeDoes(t *testing.T) {
	// Both the keys the fixture defines and the keys those keys resolve from: the
	// shell outranks `.env`, so a host copy of either side silently replaces what
	// this table is reading. Derived mechanically from the fixture, not listed by
	// hand — a hand list of these missed seven of them.
	unsetHostVars(t, "BASE", "LATER", "NOPE", "SELFREF", "SQ", "VOLROOT",
		"FORWARD", "BACKWARD", "UNSET_NODEF", "FROM_SHELL", "DQ", "MOUNT", "VIA_SQ")
	withoutHostVar(t, "SHELLVAR")
	cases := []struct {
		name string
		key  string
		want string
	}{
		{"a key defined above is visible", "FORWARD", "/base/fwd"},
		// Not "/later/bwd": only what is above the line is in scope. A file that
		// resolved both ways would be order-independent, and compose's is not.
		{"a key defined below is not", "BACKWARD", "/bwd"},
		{"a self-reference resolves to empty, not a loop", "SELFREF", "x"},
		{"an undefined reference with no default is empty", "UNSET_NODEF", "/tail"},
		{"a default applies when the shell has not set it", "FROM_SHELL", "fallback"},
		// Single quotes suppress expansion the way a shell's do; double quotes and
		// no quotes do not. Getting this backwards would silently expand values a
		// user wrote to be literal.
		{"a single-quoted value is left alone", "SQ", "${BASE}/sq"},
		{"a double-quoted value is expanded", "DQ", "/base/dq"},
		// The corpus shape, whole: a `:` inside a default must not split the mount.
		{"a default containing a colon survives as one path", "MOUNT", "/mnt/vol/psql"},
		// Expansion is a single pass: what SQ produced is not expanded again when
		// another value references it. Expanding twice would quietly resolve text
		// the single quotes were there to protect.
		{"the result of an expansion is not expanded again", "VIA_SQ", "<${BASE}/sq>"},
	}
	// A table that runs nothing passes every assertion in it. Pinning the count
	// means a case lost to a bad edit is a failure, not a quieter suite.
	if len(cases) != 9 {
		t.Fatalf("the table has %d cases, want 9 — add the count here when you add a rule", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := writeProject(t, `
services:
  app:
    image: app
    environment:
      OUT: ${`+tc.key+`}
`, "BASE=/base\n"+
				"FORWARD=${BASE}/fwd\n"+
				"BACKWARD=${LATER}/bwd\n"+
				"LATER=/later\n"+
				"SELFREF=${SELFREF}x\n"+
				"UNSET_NODEF=${NOPE}/tail\n"+
				"FROM_SHELL=${SHELLVAR:-fallback}\n"+
				"SQ='${BASE}/sq'\n"+
				`DQ="${BASE}/dq"`+"\n"+
				"MOUNT=${VOLROOT:-/mnt/vol}/psql\n"+
				"VIA_SQ=<${SQ}>\n")
			proj, err := Load(p)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := envValue(proj.Services["app"].Environment, "OUT"); got != tc.want {
				t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

// The shell still wins over `.env`, and a later `.env` line that references the
// same key sees the shell's value — so the override reaches derived values too,
// not just direct references. Pinning this separately because the expansion above
// is exactly where an "outer scope wins" rule is easy to lose.
func TestShellOverrideReachesDerivedDotEnvValues(t *testing.T) {
	unsetHostVars(t, "DERIVED")
	t.Setenv("BASE", "/from-shell")
	p := writeProject(t, `
services:
  app:
    image: app
    environment:
      DIRECT: ${BASE}
      DERIVED: ${DERIVED}
`, "BASE=/from-env\nDERIVED=${BASE}/x\n")
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	env := proj.Services["app"].Environment
	if got := envValue(env, "DIRECT"); got != "/from-shell" {
		t.Errorf("DIRECT = %q, want the shell to win", got)
	}
	if got := envValue(env, "DERIVED"); got != "/from-shell/x" {
		t.Errorf("DERIVED = %q, want the shell's value to reach the derived entry too", got)
	}
}

// A service's `env_file:` values are expanded the same way — compose treats them
// as interpolated too. The precedence here is the surprising one and is measured:
// the project `.env` beats a key defined on the line ABOVE in the same env_file,
// because the project scope is the outer one.
func TestEnvFileValuesExpandAgainstTheProjectScope(t *testing.T) {
	unsetHostVars(t, "A", "OVER")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "svc.env"),
		[]byte("A=/a\nSAME_FILE=${A}/b\nOVER=from-svcfile\nOUTER_WINS=${OVER}/z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"),
		[]byte("OVER=from-dotenv\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(p, []byte(`
services:
  app:
    image: app
    env_file: svc.env
`), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	env := proj.Services["app"].Environment
	if got := envValue(env, "SAME_FILE"); got != "/a/b" {
		t.Errorf("SAME_FILE = %q, want a key from the same file to be visible", got)
	}
	if got := envValue(env, "OUTER_WINS"); got != "from-dotenv/z" {
		t.Errorf("OUTER_WINS = %q, want the project `.env` to beat the same file's own line", got)
	}
}

// A later --env-file sees an earlier one, so the files compose rather than each
// starting from the shell alone.
func TestLaterEnvFileSeesTheEarlierOne(t *testing.T) {
	unsetHostVars(t, "A", "B")
	dir := t.TempDir()
	for name, body := range map[string]string{"one.env": "A=/a\n", "two.env": "B=${A}/b\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(p, []byte(`
services:
  app:
    image: app
    environment:
      OUT: ${B}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := LoadFiles([]string{p}, []string{filepath.Join(dir, "one.env"), filepath.Join(dir, "two.env")})
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if got := envValue(proj.Services["app"].Environment, "OUT"); got != "/a/b" {
		t.Errorf("OUT = %q, want the second env file to see the first", got)
	}
}

// A required-variable reference inside a `.env` value fails the load, naming the
// file and line — the only place that context is attached. docker compose fails
// here too. Without this the error path can be swallowed and nothing notices.
func TestDotEnvRequiredVariableFailsWithFileAndLine(t *testing.T) {
	// A host copy makes the required variable satisfied, so the load succeeds and
	// this test asserts nothing.
	unsetHostVars(t, "MISSING_ON_PURPOSE", "OK", "BAD")
	p := writeProject(t, `
services:
  app:
    image: app
`, "OK=fine\nBAD=${MISSING_ON_PURPOSE:?boom}\n")
	_, err := Load(p)
	if err == nil {
		t.Fatal("a required variable that is unset must fail the load")
	}
	for _, want := range []string{".env:2", "MISSING_ON_PURPOSE", "boom"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should say %q so the line can be found, got: %v", want, err)
		}
	}
}

// The built-in host-gateway ranks below a `.env` entry — and that has to hold for
// values DERIVED from it inside the same file, not just for a direct reference.
// Ranking the built-in above the file's own entries made one file give the same
// variable two different values, which is what this pins.
func TestDotEnvOverrideOfTheBuiltInReachesDerivedValues(t *testing.T) {
	unsetHostVars(t, "URL")
	stubHostGateway(t, "192.168.11.22")
	p := writeProject(t, `
services:
  app:
    image: app
    environment:
      DIRECT: ${OPOSSUM_HOST_GATEWAY}
      DERIVED: ${URL}
`, "OPOSSUM_HOST_GATEWAY=1.2.3.4\nURL=http://${OPOSSUM_HOST_GATEWAY}:11434\n")
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	env := proj.Services["app"].Environment
	if got := envValue(env, "DIRECT"); got != "1.2.3.4" {
		t.Errorf("DIRECT = %q, want the .env entry to override the built-in", got)
	}
	if got := envValue(env, "DERIVED"); got != "http://1.2.3.4:11434" {
		t.Errorf("DERIVED = %q, want the same override to reach a value derived from it "+
			"in the same file (got the auto-detected address instead)", got)
	}
}

// A service with two env_file entries: the second sees the first, the same way a
// second --env-file does. Two paths read env files and they have to agree; the
// rule was implemented on one of them only.
func TestLaterServiceEnvFileSeesTheEarlierOne(t *testing.T) {
	unsetHostVars(t, "A", "B")
	dir := t.TempDir()
	for name, body := range map[string]string{"one.env": "A=/a\n", "two.env": "B=${A}/b\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(p, []byte(`
services:
  app:
    image: app
    env_file:
      - one.env
      - two.env
`), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := envValue(proj.Services["app"].Environment, "B"); got != "/a/b" {
		t.Errorf("B = %q, want the second env_file to see the first", got)
	}
}

// Which definition wins depends on whether the other one is at the same level,
// and the two answers are opposite. A strictly outer level always wins; within
// one level the files are a single map filled top to bottom, so the LAST
// assignment wins and a file's own line beats a file read before it.
//
// Collapsing those two into "outer wins" is a mistake that reads as correct —
// the first attempt at this made an earlier file beat the current file's own
// line, so splitting a `.env` into a base plus an override quietly kept the base
// value. Each case here was measured against docker compose v5.3.1.
func TestWhichEnvFileDefinitionWins(t *testing.T) {
	unsetHostVars(t, "A", "B")
	write := func(t *testing.T, dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a later env_file overrides an earlier one, and sees its own value", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "one.env", "A=first\n")
		write(t, dir, "two.env", "A=second\nB=${A}\n")
		write(t, dir, "compose.yaml", "services:\n  app:\n    image: app\n    env_file: [one.env, two.env]\n")
		proj, err := Load(filepath.Join(dir, "compose.yaml"))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		env := proj.Services["app"].Environment
		if got := envValue(env, "A"); got != "second" {
			t.Errorf("A = %q, want the later file to win", got)
		}
		if got := envValue(env, "B"); got != "second" {
			t.Errorf("B = %q, want the file's own line to beat the earlier file "+
				"(it is one map, not two ranked scopes)", got)
		}
	})

	t.Run("the same holds for --env-file", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "e1.env", "A=first\n")
		write(t, dir, "e2.env", "A=second\nB=${A}\n")
		write(t, dir, "compose.yaml",
			"services:\n  app:\n    image: app\n    environment:\n      OUT: ${B}\n")
		proj, err := LoadFiles([]string{filepath.Join(dir, "compose.yaml")},
			[]string{filepath.Join(dir, "e1.env"), filepath.Join(dir, "e2.env")})
		if err != nil {
			t.Fatalf("LoadFiles: %v", err)
		}
		if got := envValue(proj.Services["app"].Environment, "OUT"); got != "second" {
			t.Errorf("OUT = %q, want the same rule on the --env-file path", got)
		}
	})

	// The other direction, which is what makes this two rules and not one: a
	// value read BEFORE the override still holds the old value.
	t.Run("a value read before the override keeps the old one", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "e1.env", "A=first\nB=${A}\n")
		write(t, dir, "e2.env", "A=second\n")
		write(t, dir, "compose.yaml",
			"services:\n  app:\n    image: app\n    environment:\n      OUT_A: ${A}\n      OUT_B: ${B}\n")
		proj, err := LoadFiles([]string{filepath.Join(dir, "compose.yaml")},
			[]string{filepath.Join(dir, "e1.env"), filepath.Join(dir, "e2.env")})
		if err != nil {
			t.Fatalf("LoadFiles: %v", err)
		}
		env := proj.Services["app"].Environment
		if got := envValue(env, "OUT_A"); got != "second" {
			t.Errorf("OUT_A = %q, want the later assignment", got)
		}
		if got := envValue(env, "OUT_B"); got != "first" {
			t.Errorf("OUT_B = %q, want the value as it stood when that line was read", got)
		}
	})

	// The outer level, by contrast, is not part of that map at all: the project's
	// `.env` wins over the whole env_file chain, however late the redefinition.
	t.Run("the project .env beats the whole env_file chain", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, ".env", "A=dot\n")
		write(t, dir, "f1.env", "A=one\n")
		write(t, dir, "f2.env", "A=two\nB=${A}\n")
		write(t, dir, "compose.yaml", "services:\n  app:\n    image: app\n    env_file: [f1.env, f2.env]\n")
		proj, err := Load(filepath.Join(dir, "compose.yaml"))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		env := proj.Services["app"].Environment
		if got := envValue(env, "A"); got != "two" {
			t.Errorf("A = %q, want the env_file value to reach the container", got)
		}
		if got := envValue(env, "B"); got != "dot" {
			t.Errorf("B = %q, want the project `.env` to win while expanding, "+
				"even against a redefinition on the line above", got)
		}
	})
}

// A multi-line value is visible to the lines after it, the same as a single-line
// one. It goes down its own branch of the parser, so "the level is written
// through" has to be asserted on that branch too — one side being wired is no
// evidence about the other.
func TestAMultiLineValueIsVisibleToLaterLines(t *testing.T) {
	dir := t.TempDir()
	// The value carries a `${…}` of its own, so this pins WHAT becomes visible and
	// not merely that something does: writing the raw text through instead of the
	// expanded one reads the same to a check with no variable in it.
	if err := os.WriteFile(filepath.Join(dir, ".env"),
		[]byte("A=x\nPEM=\"p${A}\nq\"\nUSES=[${PEM}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := parseDotEnv(filepath.Join(dir, ".env"), noScope())
	if err != nil {
		t.Fatalf("parseDotEnv: %v", err)
	}
	if got, want := m["USES"], "[px\nq]"; got != want {
		t.Errorf("USES = %q, want %q — a multi-line value must reach the lines below it, expanded", got, want)
	}
}

// The built-in is reachable FROM an env-file value, not just from the compose
// file. The existing tests all override it, so the plain path — nobody set it,
// a `.env` value refers to it — was guarded by nothing.
func TestEnvFileValueCanUseTheBuiltIn(t *testing.T) {
	unsetHostVars(t, "OLLAMA", "URL")
	stubHostGateway(t, "10.9.8.7")
	p := writeProject(t, `
services:
  app:
    image: app
    environment:
      URL: ${OLLAMA}
`, "OLLAMA=http://${OPOSSUM_HOST_GATEWAY}:11434\n")
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := envValue(proj.Services["app"].Environment, "URL"); got != "http://10.9.8.7:11434" {
		t.Errorf("URL = %q, want the built-in to be visible from a `.env` value", got)
	}
}

// The built-in ranks last at EVERY level, not just the project's. A service's
// `env_file:` that sets OPOSSUM_HOST_GATEWAY must be able to use its own value on
// the next line.
//
// This is the same defect as TestDotEnvOverrideOfTheBuiltInReachesDerivedValues,
// one level down: it was fixed for `.env` and then reintroduced for `env_file:`
// by a later refactor, because the scope handed to the inner level had folded the
// built-in into it. Both levels are asserted so neither can be fixed alone.
func TestEnvFileOverrideOfTheBuiltInReachesDerivedValues(t *testing.T) {
	unsetHostVars(t, "URL")
	stubHostGateway(t, "192.168.11.22")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "svc.env"),
		[]byte("OPOSSUM_HOST_GATEWAY=1.2.3.4\nURL=http://${OPOSSUM_HOST_GATEWAY}:11434\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(p, []byte(`
services:
  app:
    image: app
    env_file: svc.env
`), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := envValue(proj.Services["app"].Environment, "URL"); got != "http://1.2.3.4:11434" {
		t.Errorf("URL = %q, want the env_file's own override to reach a value derived from "+
			"it in the same file (got the auto-detected address instead)", got)
	}
}

// A scope built by hand, without going through loadEnv or inner, has no level
// map — and parseDotEnv writes through to it. Failing with a sentence beats
// panicking with `assignment to entry in nil map` from inside the parse loop.
func TestParseDotEnvRejectsAScopeWithNoLevel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("A=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := parseDotEnv(filepath.Join(dir, ".env"), envScope{})
	if err == nil {
		t.Fatal("a scope with no level map must be refused, not written to")
	}
	if !strings.Contains(err.Error(), "level") {
		t.Errorf("the error should say what is missing, got: %v", err)
	}
}

// A service's `environment:` is a level of its own, between the project's `.env`
// and the `env_file:` chain. Missing it is not a detail: an env file's value can
// name a key that exists only in `environment:`, and where both define one, the
// file's values see `environment:`, not their own.
//
// The whole level was absent until it was measured — the rules had been written
// up as a complete set with a level missing from them. Each case here is one
// boundary of the order, measured against docker compose v5.3.1.
func TestServiceEnvironmentIsALevelBetween(t *testing.T) {
	unsetHostVars(t, "ONLY", "OTHER")
	// Every case below turns on K resolving from the level under test, so the
	// host must not answer for it. Done here rather than per case: doing it in one
	// subtest and not its neighbours is how two of these came to pass only on a
	// machine where K happened to be unset. A case that brings its own name says
	// so at its own top.
	withoutHostVar(t, "K")
	load := func(t *testing.T, dotenv, envfile, environment string) []string {
		t.Helper()
		dir := t.TempDir()
		if dotenv != "" {
			if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(dotenv), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "f.env"), []byte(envfile), 0o644); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "compose.yaml")
		if err := os.WriteFile(p, []byte("services:\n  app:\n    image: app\n"+
			"    env_file: [f.env]\n    environment:\n      "+environment+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		proj, err := Load(p)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return proj.Services["app"].Environment
	}

	t.Run("an env_file value can name a key only environment: has", func(t *testing.T) {
		env := load(t, "", "OTHER=[${ONLY}]\n", "ONLY: yes-it-is")
		if got := envValue(env, "OTHER"); got != "[yes-it-is]" {
			t.Errorf("OTHER = %q, want the `environment:` key to be in scope", got)
		}
	})

	t.Run("environment: beats the env_file's own line", func(t *testing.T) {
		env := load(t, "", "K=from-file\nOTHER=[${K}]\n", "K: from-environment")
		if got := envValue(env, "OTHER"); got != "[from-environment]" {
			t.Errorf("OTHER = %q, want `environment:` to win over the file's own key", got)
		}
		// The value that reaches the container is still `environment:`'s, which is
		// a separate rule (mergeEnv) and would hide a broken expansion if the two
		// were checked together.
		if got := envValue(env, "K"); got != "from-environment" {
			t.Errorf("K = %q, want the explicit `environment:` value", got)
		}
	})

	t.Run("the project .env beats environment:", func(t *testing.T) {
		env := load(t, "K=dot\n", "K=from-file\nOTHER=[${K}]\n", "K: from-environment")
		if got := envValue(env, "OTHER"); got != "[dot]" {
			t.Errorf("OTHER = %q, want the outer level to win", got)
		}
	})

	t.Run("the shell beats all of them", func(t *testing.T) {
		t.Setenv("K", "shell")
		env := load(t, "K=dot\n", "K=from-file\nOTHER=[${K}]\n", "K: from-environment")
		if got := envValue(env, "OTHER"); got != "[shell]" {
			t.Errorf("OTHER = %q, want the shell to win", got)
		}
	})

	// The twin of the bare-key case: two VALUES for one key. Last wins, and the
	// env file's expansion has to see the same winner the container gets. Only
	// the bare-key half was pinned, so `explicitEnv` could be made first-wins
	// with nothing going red.
	t.Run("the last value for a key at this level wins", func(t *testing.T) {
		env := load(t, "", "OTHER=[${K}]\n", "- K=first\n      - K=second")
		if got := envValue(env, "OTHER"); got != "[second]" {
			t.Errorf("OTHER = %q, want the last value to win while expanding", got)
		}
		if got := envValue(env, "K"); got != "second" {
			t.Errorf("K = %q, want the container to get the same winner", got)
		}
	})

	// A bare key must undo its own key and nothing else. Wiping the level would
	// pass every case above, since each of those has only the one key in play.
	t.Run("a bare key leaves the other keys at its level alone", func(t *testing.T) {
		withoutHostVar(t, "KEEP")
		env := load(t, "", "KEEP=from-file\nOTHER=[${KEEP}]\n", "- KEEP=kept\n      - K")
		if got := envValue(env, "OTHER"); got != "[kept]" {
			t.Errorf("OTHER = %q, want the bare K to leave KEEP standing", got)
		}
	})

	// A bare `- KEY` under environment: means "take it from the host", so it
	// defines nothing at this level. Reading it as a key set to the empty string
	// would put an empty value where the file's own is meant to show through.
	//
	// The host must NOT have the key for this to test anything: with it set, the
	// shell answers from a level above and an empty entry here is never reached —
	// which is how the first version of this case passed against both behaviours.
	t.Run("a bare key under environment: defines nothing", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "f.env"),
			[]byte("K=file-value\nOTHER=[${K}]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "compose.yaml")
		if err := os.WriteFile(p, []byte("services:\n  app:\n    image: app\n"+
			"    env_file: [f.env]\n    environment:\n      - K\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		proj, err := Load(p)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got := envValue(proj.Services["app"].Environment, "OTHER"); got != "[file-value]" {
			t.Errorf("OTHER = %q, want the bare key to define nothing, leaving the "+
				"env_file's own value visible", got)
		}
	})
}

// The other half of the built-in contract at the env_file level: it can be READ
// from there, not only overridden. The override case was pinned first, so the
// line that keeps the built-in reachable from an inner level had nothing holding
// it — and it reads like an unused field.
func TestServiceEnvFileValueCanReadTheBuiltIn(t *testing.T) {
	unsetHostVars(t, "URL")
	stubHostGateway(t, "172.16.0.1")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "svc.env"),
		[]byte("URL=http://${OPOSSUM_HOST_GATEWAY}:11434\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(p, []byte("services:\n  app:\n    image: app\n    env_file: svc.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := envValue(proj.Services["app"].Environment, "URL"); got != "http://172.16.0.1:11434" {
		t.Errorf("URL = %q, want the built-in to be reachable from an `env_file:` value", got)
	}
}

// A bare `KEY` after `KEY=value` in the same `environment:` list undoes the
// value: the container is sent nothing for it, so an env file expanding ${KEY}
// must not see the value either. Skipping the bare entry instead of undoing it
// left one key reading two ways inside one service — the container's view and
// the expansion's view disagreeing.
func TestABareKeyUndoesAnEarlierValueInTheSameList(t *testing.T) {
	unsetHostVars(t, "OTHER")
	withoutHostVar(t, "K")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.env"),
		[]byte("K=file-value\nOTHER=[${K}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(p, []byte("services:\n  app:\n    image: app\n"+
		"    env_file: [f.env]\n    environment:\n      - K=value\n      - K\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	env := proj.Services["app"].Environment
	if got := envValue(env, "OTHER"); got != "[file-value]" {
		t.Errorf("OTHER = %q, want the bare key to undo `K=value`, leaving the env "+
			"file's own value visible", got)
	}
	// The container's view, which is what makes the two disagree if only one side
	// is fixed. Asserted as "the bare key is present" rather than "K=value is
	// absent": `-e K` inherits the host's value and `-e K=` sends an empty one
	// (runtime.go passes each entry through verbatim), so a check that only
	// rejects the old value cannot tell those two apart.
	if !slices.Contains(env, "K") {
		t.Errorf("the container should get a bare K, got %v — a bare key and K= are "+
			"different arguments to the runtime", env)
	}
}

// withoutHostVar is what keeps the level tests from silently becoming checks of
// the developer's own shell, so it gets its own guard. Nothing else can provide
// one: on a clean machine a helper that does nothing looks exactly like a helper
// that works.
func TestWithoutHostVarRemovesAndRestores(t *testing.T) {
	const name = "OPOSSUM_TEST_HOST_VAR"
	t.Setenv(name, "outer-value")

	t.Run("removed inside", func(t *testing.T) {
		withoutHostVar(t, name)
		if v, ok := os.LookupEnv(name); ok {
			t.Errorf("%s is still set to %q — the level tests would be reading the host", name, v)
		}
	})

	// The subtest's cleanup has run by now, so the outer value must be back.
	// Without this, one test's protection would leak into every test after it.
	if v, ok := os.LookupEnv(name); !ok || v != "outer-value" {
		t.Errorf("%s = %q (set=%v), want it restored after the subtest", name, v, ok)
	}

	// The plural form is what 13 of these names actually go through, and a loop
	// body that does nothing is invisible on a clean machine for the same reason
	// the singular one is.
	t.Run("the plural form removes every name", func(t *testing.T) {
		const a, b = "OPOSSUM_TEST_MULTI_A", "OPOSSUM_TEST_MULTI_B"
		t.Setenv(a, "one")
		t.Setenv(b, "two")
		unsetHostVars(t, a, b)
		for _, n := range []string{a, b} {
			if v, ok := os.LookupEnv(n); ok {
				t.Errorf("%s is still set to %q", n, v)
			}
		}
	})

	t.Run("absent stays absent", func(t *testing.T) {
		const gone = "OPOSSUM_TEST_HOST_VAR_UNSET"
		os.Unsetenv(gone)
		withoutHostVar(t, gone)
		if _, ok := os.LookupEnv(gone); ok {
			t.Errorf("%s should still be unset", gone)
		}
	})
}

// stubHostGateway has to clear the host's own OPOSSUM_HOST_GATEWAY as well as
// swap the resolver. The shell outranks the built-in by design, so on a machine
// that exports it the stub is simply never consulted and every built-in test
// silently reads the developer's value instead.
//
// Nothing else can catch that: on a clean machine, a stub that clears the host
// variable and one that does not behave identically. So the host variable is set
// here on purpose.
func TestStubHostGatewayBeatsAHostValue(t *testing.T) {
	t.Setenv(hostGatewayVar, "203.0.113.9")
	stubHostGateway(t, "10.0.0.1")
	p := writeProject(t, `
services:
  app:
    image: app
    environment:
      GW: ${OPOSSUM_HOST_GATEWAY}
`, "")
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := envValue(proj.Services["app"].Environment, "GW"); got != "10.0.0.1" {
		t.Errorf("GW = %q, want the stub — the host's own value must not show through", got)
	}
}

// A value that expands to nothing must reach the parser as an empty string, not
// as null. For `environment:` the two are opposites: null is a bare key, which
// tells the runtime to take the variable FROM THE HOST — so a compose file asking
// for an empty value handed the container whatever the developer happened to have
// exported.
//
// Each expectation was measured against docker compose v5.3.1. The pairs matter
// more than the individual values: a fix that empties everything would also break
// the hand-written null, and one that touches nothing keeps the leak.
func TestAValueEmptiedByExpansionIsAnEmptyString(t *testing.T) {
	unsetHostVars(t, "NOSUCHVAR", "A", "B", "LEAKY", "INHERITS")
	p := writeProject(t, `
services:
  app:
    image: app
    environment:
      FROM_UNSET: ${NOSUCHVAR}
      BRACELESS: $NOSUCHVAR
      FROM_DEFAULT: ${NOSUCHVAR:-}
      TWO_ADJACENT: ${A}${B}
      TWO_SPACED: ${A} ${B}
      HASH_AFTER: ${A}#tail
      QUOTED: "${A}"
      PREFIXED: x${A}
      SUFFIXED: ${A}x
      HAND_WRITTEN_NULL:
      HAND_WRITTEN_EMPTY: ""
`, "")
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	env := proj.Services["app"].Environment
	for _, tc := range []struct{ key, want string }{
		{"FROM_UNSET", "FROM_UNSET="},
		// `$NAME` goes down its own branch of the expander, so it needs its own case:
		// wiring one form and not the other leaves half the leak open.
		{"BRACELESS", "BRACELESS="},
		{"FROM_DEFAULT", "FROM_DEFAULT="},
		{"TWO_ADJACENT", "TWO_ADJACENT="},
		// The space between two references is text the author wrote, so it stays —
		// the value is one space, not nothing. It comes out right because the mark
		// holds each reference's place while the parser reads the line; with the
		// references simply gone, the space sat at the end of `TWO_SPACED:` and was
		// stripped as indentation.
		{"TWO_SPACED", "TWO_SPACED= "},
		// `#` starts a comment only after a space, and here it follows a reference.
		// The mark keeps it that way: with the reference gone the `#` would follow
		// the space after the colon, and everything from it would be thrown away as
		// a comment — which is how this used to lose the whole value.
		{"HASH_AFTER", "HASH_AFTER=#tail"},
		{"QUOTED", "QUOTED="},
		{"PREFIXED", "PREFIXED=x"},
		{"SUFFIXED", "SUFFIXED=x"},
		// The one shape that must stay a bare key: nobody wrote a reference here,
		// so "inherit from the host" is what the file actually says.
		{"HAND_WRITTEN_NULL", "HAND_WRITTEN_NULL"},
		{"HAND_WRITTEN_EMPTY", "HAND_WRITTEN_EMPTY="},
	} {
		if !slices.Contains(env, tc.want) {
			t.Errorf("%s: want %q in the environment, got %v", tc.key, tc.want, env)
		}
	}
}

// The leak itself, end to end: with the host exporting the same name, the value
// the service gets must come from the compose file. Asserting on the environment
// list alone would pass on a build that reads the host, because the list would
// still name the key.
func TestAnEmptiedValueDoesNotInheritFromTheHost(t *testing.T) {
	unsetHostVars(t, "NOSUCHVAR")
	t.Setenv("LEAKY", "secret-from-host")
	p := writeProject(t, `
services:
  app:
    image: app
    environment:
      LEAKY: ${NOSUCHVAR}
`, "")
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	env := proj.Services["app"].Environment
	if slices.Contains(env, "LEAKY") {
		t.Errorf("LEAKY arrives as a bare key, which makes the runtime pass the host's "+
			"value through: %v", env)
	}
	if !slices.Contains(env, "LEAKY=") {
		t.Errorf("LEAKY should be set to nothing, got %v", env)
	}
}

// The document repair must not reach an env-file value. Those are expanded by the
// same code but are not YAML, and plenty of ordinary ones end in a colon: a PATH
// being extended, a namespace prefix. Repairing `LIB=/usr/local/lib:` into
// `/usr/local/lib: ""` broke compose files that had always worked — silently when
// the value came from `env_file:`, and as a YAML parse error when it was
// referenced from the document.
func TestTheDocumentRepairDoesNotReachEnvFileValues(t *testing.T) {
	unsetHostVars(t, "PREFIX", "LIB", "TENANT", "NS")

	t.Run("through env_file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "app.env"),
			[]byte("PREFIX=/usr/local\nLIB=${PREFIX}/lib:\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "compose.yaml")
		if err := os.WriteFile(p, []byte("services:\n  app:\n    image: app\n    env_file: app.env\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		proj, err := Load(p)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got := envValue(proj.Services["app"].Environment, "LIB"); got != "/usr/local/lib:" {
			t.Errorf("LIB = %q, want the trailing colon left alone", got)
		}
	})

	// Referenced from the document, the same value used to produce
	// `LIB: "/usr/local/lib: """ — not valid YAML, so the project stopped loading.
	t.Run("referenced from the document", func(t *testing.T) {
		p := writeProject(t, `
services:
  app:
    image: app
    environment:
      LIB: "${LIB}"
`, "PREFIX=/usr/local\nLIB=${PREFIX}/lib:\n")
		proj, err := Load(p)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got := envValue(proj.Services["app"].Environment, "LIB"); got != "/usr/local/lib:" {
			t.Errorf("LIB = %q, want the trailing colon left alone", got)
		}
	})

	// The other fragment path: the default inside `${VAR:-default}` goes through
	// the same expander. Checked at the expander rather than through Load, because
	// a document that expands to `NS: acme:` is not valid YAML — that is the
	// separate, older consequence of expanding before parsing (#411), and it fails
	// the same way on the commit before this change.
	t.Run("inside a default argument", func(t *testing.T) {
		got, err := interpolateStr("${NOPE:-${TENANT}:}", lk(map[string]string{"TENANT": "acme"}))
		if err != nil {
			t.Fatal(err)
		}
		if got != "acme:" {
			t.Errorf("got %q, want the default's trailing colon left alone", got)
		}
	})
}

// What the repair must and must not touch, decided by the parser rather than by
// reading the text. Each case is a whole document through Load, because the
// question — "is this a value that vanished, or the inside of a string?" — only
// has an answer once the document is parsed.
//
// The shapes below are the ones a text-level rule got wrong, one per review
// round: block scalars, then anchors and tags before the indicator, then
// multi-line quoted scalars. Flow style and sequence items are here too; those
// the text rule never reached at all.
func TestOnlyAVanishedValueBecomesAnEmptyString(t *testing.T) {
	unsetHostVars(t, "NOPE", "ONCALL", "HOSTONLY")
	for _, tc := range []struct {
		name    string
		compose string
		key     string
		want    string
	}{
		{
			name:    "a value that is only a reference",
			compose: "    environment:\n      K: ${NOPE}\n",
			key:     "K", want: "K=",
		},
		{
			name:    "a value the author left empty",
			compose: "    environment:\n      K:\n",
			key:     "K", want: "K", // a bare key: inherit from the host, as written
		},
		{
			name:    "a value with text around the reference",
			compose: "    environment:\n      K: x${NOPE}\n",
			key:     "K", want: "K=x",
		},
		{
			name:    "flow style",
			compose: "    environment: {K: ${NOPE}}\n",
			key:     "K", want: "K=",
		},
		{
			name:    "a braceless reference in flow style",
			compose: "    environment: {K: $NOPE}\n",
			key:     "K", want: "K=",
		},
		{
			// The reference expands — opossum expands the text, comments included —
			// but the value beside it is the author's own bare key. Matching on the
			// line alone read this as a vanished value and rewrote it.
			name:    "a reference in a comment, beside a key the author left empty",
			compose: "    environment:\n      K:  # ${NOPE}\n",
			key:     "K", want: "K", // still: inherit from the host
		},
		{
			name:    "a reference in a comment, beside one that did vanish",
			compose: "    environment:\n      K: ${NOPE}  # ${NOPE}\n",
			key:     "K", want: "K=",
		},
		{
			// The parser reports a null carrying an anchor at the `&`, so the anchor
			// itself stands between the position it gives and the value's place.
			name:    "an anchor on a value that vanished",
			compose: "    environment:\n      K: &anc ${NOPE}\n      J: *anc\n",
			key:     "K", want: "K=",
		},
		{
			name:    "an alias to a value that vanished",
			compose: "    environment:\n      J: &anc ${NOPE}\n      K: *anc\n",
			key:     "K", want: "K=",
		},
		{
			name:    "an anchor in flow style",
			compose: "    environment: {K: &anc ${NOPE}, J: one}\n",
			key:     "K", want: "K=",
		},
		{
			// The document is written back out only because J needed repairing. K
			// must survive that round trip as the null the author wrote, which it
			// does not in flow style: the marshaller renders a flow null as `''`.
			name:    "a key the author left empty, beside one that vanished, in flow style",
			compose: "    environment: {K: , J: ${NOPE}}\n",
			key:     "K", want: "K", // still: inherit from the host
		},
		{
			// A control: the document is written back out, and a value that was not
			// repaired has to come back as itself. It does — the marshaller re-quotes
			// whatever the tag requires — so this pins the round trip rather than any
			// choice made here.
			name:    "a quoted value beside one that vanished",
			compose: "    environment: {K: \"yes\", J: ${NOPE}}\n",
			key:     "K", want: "K=yes",
		},
		{
			// A tab is separation space after a colon, so the parser accepts this and
			// the gap between the value's place and the reference is not a space.
			name:    "a tab between the colon and the reference",
			compose: "    environment:\n      K:\t${NOPE}\n",
			key:     "K", want: "K=",
		},
		{
			// The reference before it writes two characters, so the second reference
			// does not start where it would have on an untouched line. A cursor that
			// does not move over what it wrote points back into `K:` and the value is
			// left as the author's own null.
			name:    "a second reference on a line another one already widened",
			compose: "    environment: {A: ${NOPE:-xy}, K: ${NOPE}}\n",
			key:     "K", want: "K=",
		},
		{
			// The parser reports this null at the comma, which is PAST where the
			// reference was: the value's place and the reference that emptied it
			// have to be recognised as adjacent from either side.
			name:    "flow style with a space before the comma",
			compose: "    environment: {K: ${NOPE} , J: one}\n",
			key:     "K", want: "K=",
		},
		// Inside a string, a `key:` is text. The parser knows that; a line-based
		// rule cannot, and got each of these wrong in turn.
		{
			name:    "a literal block holding what looks like a key",
			compose: "    environment:\n      K: |\n        listen: ${NOPE}\n",
			key:     "K", want: "K=listen: \n",
		},
		{
			name:    "an anchor before the block indicator",
			compose: "    environment:\n      K: &k |\n        listen: ${NOPE}\n",
			key:     "K", want: "K=listen: \n",
		},
		// Folding joins the lines with a space. The mark stands where the reference
		// was, so the space before it is not at the end of a line and folding does
		// not eat it — `oncall:  end`, with two spaces, which is what Docker
		// Compose reads it as (measured against v5.3.1). Expanding raw text used to
		// lose that space; it no longer does.
		{
			name:    "a multi-line single-quoted scalar",
			compose: "    environment:\n      K: 'runbook\n        oncall: ${NOPE}\n        end'\n",
			key:     "K", want: "K=runbook oncall:  end",
		},
		{
			name:    "a multi-line double-quoted scalar",
			compose: "    environment:\n      K: \"runbook\n        oncall: ${NOPE}\n        end\"\n",
			key:     "K", want: "K=runbook oncall:  end",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := writeProject(t, "services:\n  app:\n    image: app\n"+tc.compose, "")
			proj, err := Load(p)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			env := proj.Services["app"].Environment
			if !slices.Contains(env, tc.want) {
				t.Errorf("want %q in the environment, got %v", tc.want, env)
			}
		})
	}
}

// A sequence item that vanishes is an empty string too — the parser reports a
// null scalar there just as it does for a mapping value, so the repair reaches it
// without knowing anything about sequences.
func TestAVanishedSequenceItemBecomesAnEmptyString(t *testing.T) {
	unsetHostVars(t, "NOPE")
	p := writeProject(t, `
services:
  app:
    image: app
    command:
      - echo
      - ${NOPE}
`, "")
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := proj.Services["app"].Command
	if len(got) != 2 || got[1] != "" {
		t.Errorf("command = %#v, want [echo \"\"] rather than a null second item", got)
	}
}

// A value carrying an explicit tag. Emptying it has to leave a string behind:
// `!!int` with nothing under it is not a number, and the document would come back
// out unreadable.
func TestAnEmptiedValueWithAnExplicitTagBecomesAString(t *testing.T) {
	unsetHostVars(t, "NOPE")
	for _, tag := range []string{"!!str", "!!int"} {
		t.Run(tag, func(t *testing.T) {
			p := writeProject(t, "services:\n  app:\n    image: app\n    environment:\n      K: "+tag+" ${NOPE}\n", "")
			proj, err := Load(p)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if env := proj.Services["app"].Environment; !slices.Contains(env, "K=") {
				t.Errorf("want K= in the environment, got %v", env)
			}
		})
	}
}

// The mark is a character, and a compose file could contain it. Reading such a
// file would conflate what the author wrote with what expansion produced, so it is
// refused rather than guessed at.
func TestAComposeFileHoldingTheMarkIsRefused(t *testing.T) {
	unsetHostVars(t, "NOPE")
	p := writeProject(t, "services:\n  app:\n    image: app\n    environment:\n      K: \"a\ue000b\"\n", "")
	_, err := Load(p)
	if err == nil {
		t.Fatal("a file carrying the mark was read as if it were ordinary text")
	}
	if !strings.Contains(err.Error(), "U+E000") {
		t.Errorf("the error should name the character, got: %v", err)
	}
}

// A reference that expands to nothing where the result is no longer YAML.
//
// The mark stands between what was on either side of it, so taking it back out of
// the text JOINS them: `c: *x${NOPE}y` would become `c: *xy`, which is valid YAML,
// points at a different anchor, and loads without a word. The bytes go back
// untouched instead, so they fail to parse for the caller exactly as they failed
// here — and the caller says so in its own words rather than this blaming a
// variable for what may be an indentation mistake.
func TestTextExpansionBrokeIsNotQuietlyRepaired(t *testing.T) {
	unsetHostVars(t, "NOPE")
	// The alias case: `*x<mark>y` is not a name, so nothing parses it — and nothing
	// silently turns it into `*xy` either.
	raw := "a: &x 1\nxy: &xy 2\nc: *x${NOPE}y\n"
	out, err := interpolateDocument([]byte(raw), lk(nil))
	if err != nil {
		t.Fatalf("interpolateDocument: %v", err)
	}
	var got map[string]any
	if out.into(&got) == nil {
		t.Errorf("the alias was quietly repointed instead of left broken: %v", got)
	}

	// An emptied value followed by a colon stops being a value at all: `NS: :` is
	// a mapping where a scalar belongs, which is a different YAML failure from the
	// one below and goes through the same door. (It is not the next line being
	// swallowed — there is no next line here; the line breaks on its own.)
	brokenLine := writeProject(t, "services:\n  app:\n    image: app\n    environment:\n      NS: ${NOPE}:\n", "")
	if _, err := Load(brokenLine); err == nil {
		t.Error("a document with a mapping where a value belongs was loaded")
	} else if !strings.Contains(err.Error(), "is not valid YAML") {
		t.Errorf("want the loader's own message, got: %v", err)
	}

	// And with more than one file, where a different piece of code does the
	// reporting: the failure has to name the file it is actually in.
	dir := t.TempDir()
	good := filepath.Join(dir, "compose.yaml")
	bad := filepath.Join(dir, "override.yaml")
	if err := os.WriteFile(good, []byte("services:\n  app:\n    image: app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("services:\n  app:\n    environment:\n      NS: ${NOPE}:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFiles([]string{good, bad}, nil); err == nil {
		t.Error("a broken override was merged in anyway")
	} else {
		if !strings.Contains(err.Error(), "override.yaml") {
			t.Errorf("the failure should name the file it is in, got: %v", err)
		}
		// Naming the right file only means something if it is not naming both:
		// "compose.yaml and override.yaml" would contain the string above and
		// still send the reader to the wrong file.
		if strings.Contains(err.Error(), "compose.yaml") {
			t.Errorf("the failure names a file the problem is not in, got: %v", err)
		}
		// And the same message as the single-file road, so the two do not drift.
		if !strings.Contains(err.Error(), "is not valid YAML") {
			t.Errorf("want the loader's own message, got: %v", err)
		}
		if strings.Contains(err.Error(), "\ue000") {
			t.Errorf("the mark reached the message: %v", err)
		}
	}

	// And through Load, where the message matters: a file whose own indentation is
	// wrong must be told about its indentation, not about its variables.
	p := writeProject(t, "services:\n  app:\n    image: ${NOPE}\n   bad: 1\n", "")
	_, err = Load(p)
	if err == nil {
		t.Fatal("a file that is not YAML was loaded")
	}
	if !strings.Contains(err.Error(), "is not valid YAML") ||
		!strings.Contains(err.Error(), "check the indentation") {
		t.Errorf("the loader's own message should survive, got: %v", err)
	}
	if strings.Contains(err.Error(), "expanded to nothing") {
		t.Errorf("an indentation mistake was blamed on a variable: %v", err)
	}
	if strings.Contains(err.Error(), "\ue000") {
		t.Errorf("the mark reached the message: %v", err)
	}
}

// The mark is this package's bookkeeping and must not ride out into the document
// opossum hands on — including through a comment, which is not a scalar and is not
// where anyone would think to look for one.
func TestTheMarkNeverLeavesThisPackage(t *testing.T) {
	unsetHostVars(t, "NOPE")
	for _, raw := range []string{
		"a: 1 # ${NOPE}\n",
		"# head ${NOPE}\na: 1\n",
		"a: 1\n# foot ${NOPE}\n",
		"a: ${NOPE}  # and here ${NOPE}\n",
	} {
		out, err := interpolateDocument([]byte(raw), lk(nil))
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		var got map[string]any
		if err := out.into(&got); err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if v := fmt.Sprint(got); strings.Contains(v, "\ue000") {
			t.Errorf("%q came out still carrying the mark: %s", raw, v)
		}
	}
}

// The space in front of a reference belongs to the author, and stays whether or
// not the variable was set. Losing it only when the variable is unset would make
// the value depend on the environment — which is the whole of what this exists to
// stop, arriving through the door marked "tidy up the comments".
func TestWhitespaceAroundAnEmptiedReferenceIsKept(t *testing.T) {
	unsetHostVars(t, "NOPE")
	for _, tc := range []struct{ name, raw, want string }{
		{"a space before it", "a: \"one ${NOPE}\"\n", "one "},
		{"spaces after it too", "a: \"hello ${NOPE}  \"\n", "hello   "},
		{"a tab on each side", "a: \"tab\\t${NOPE}\\t\"\n", "tab\t\t"},
		{"folded over two lines", "a: \"one\n  ${NOPE} two \"\n", "one  two "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := interpolateDocument([]byte(tc.raw), lk(nil))
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := out.into(&got); err != nil {
				t.Fatal(err)
			}
			if got["a"] != tc.want {
				t.Errorf("a = %q, want %q", got["a"], tc.want)
			}
		})
	}
	// The comments this used to check are gone from here on purpose. They were
	// about the document being written back out, and it no longer is — the tree is
	// handed on, and nothing that reads it can see a comment. Two rounds of review
	// went on where a comment's trailing whitespace should be trimmed; that whole
	// question stopped existing when the rebuild did.

	// And the same value with the variable SET, which is the comparison that makes
	// the point: these two must not differ except in what the variable held.
	set, err := interpolateDocument([]byte("a: \"one ${V}\"\n"), lk(map[string]string{"V": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := set.into(&got); err != nil {
		t.Fatal(err)
	}
	if got["a"] != "one x" {
		t.Errorf("a = %q, want %q", got["a"], "one x")
	}
}

// A byte-order mark at the start of an env file belongs to the file, not to the
// first key. Editors on Windows write one without being asked, and the failure it
// used to cause is the quiet kind: the first key became `\ufeffA`, so `${A}` found
// nothing and expanded to empty, with no error anywhere to say why.
//
// Anywhere else the mark lands inside a name, which has the same quiet ending, so
// that is refused rather than carried. A mark inside a VALUE is a character
// somebody put there and is kept.
//
// Every expectation below was measured against docker compose v5.3.1 on
// 2026-08-21: it drops a leading mark, keeps one inside a value, and refuses one
// in a name (its message quotes the whole line back, which this does not — the
// line is in an env file).
//
// Both roads are here. The fix lives in the function both of them call, and a
// version that only handled `.env` passed every case when this covered one road,
// so the road is part of what is being checked.
//
// The two roads are read the same way but used differently, and the assertions
// follow that rather than papering over it: the project `.env` supplies the
// variables the compose file itself refers to, while an `env_file:` hands its
// entries to the container and takes no part in expanding the compose file. So
// one road is read through `${A}` and the other by looking for `A` in what the
// service ends up with.
func TestAByteOrderMarkAtTheStartOfAnEnvFileIsNotPartOfTheFirstKey(t *testing.T) {
	unsetHostVars(t, "A", "B", "OUT_A", "OUT_B")
	const bom = "\ufeff"
	// Stands in for whatever is on a line an env file refuses: it is a file that
	// holds secrets, so a refusal names the place and not the line.
	const canary = "do-not-print-me"
	const compose = "services:\n  app:\n    image: app\n    environment:\n" +
		"      OUT_A: ${A}\n      OUT_B: ${B}\n"

	for _, tc := range []struct {
		name    string
		envfile string
		wantA   string
		wantB   string
		wantErr string
	}{
		{name: "no mark at all", envfile: "A=1\nB=${A}\n", wantA: "1", wantB: "1"},
		// The shape from the report: the mark ate the first key, and the value that
		// referred to it went empty in silence.
		{name: "a mark before the first key", envfile: bom + "A=1\nB=${A}\n", wantA: "1", wantB: "1"},
		// Windows writes the mark and Windows writes CRLF, so the two arrive
		// together more often than either arrives alone. These two are here for
		// the mark's sake, not CRLF's: dropping the CRLF normalisation entirely
		// leaves every case here green, because the trailing `\r` is eaten when
		// the value is trimmed. What they hold is that the mark is still found
		// and removed when the file is written the Windows way.
		{name: "a mark before the first key, CRLF", envfile: bom + "A=1\r\nB=${A}\r\n", wantA: "1", wantB: "1"},
		{name: "CRLF with no mark", envfile: "A=1\r\nB=${A}\r\n", wantA: "1", wantB: "1"},
		// The first line being a comment is the other way the mark reaches the
		// front of the file.
		{name: "a mark before a leading comment", envfile: bom + "# note\nA=1\nB=${A}\n", wantA: "1", wantB: "1"},
		// The boundary. A mark inside a value is somebody's character and stays
		// exactly where they put it.
		{name: "a mark inside a value", envfile: "A=x" + bom + "y\nB=${A}\n", wantA: "x" + bom + "y", wantB: "x" + bom + "y"},
		// And the refusals. Only the file's first mark is the file's; the rest are
		// in names, where they cannot be typed and cannot be reported.
		// The values here are recognisable ASCII so the check below can see a
		// message that read the line back. Looking for the mark itself would not:
		// `%q` writes it as the six characters `\ufeff`, so a message built that
		// way contains the whole line and none of the character.
		{name: "a mark on a later line", envfile: "A=1\n" + bom + "B=" + canary + "\n", wantErr: "byte-order mark"},
		{name: "two marks at the start", envfile: bom + bom + "A=" + canary + "\nB=${A}\n", wantErr: "byte-order mark"},
		{name: "a mark at the end of a name", envfile: "A" + bom + "=" + canary + "\n", wantErr: "byte-order mark"},
		// No separator on the line, so there is no name to look inside — the whole
		// line is the name. "no `=`" would be true and useless here: what has to go
		// is a character the reader cannot see, so the message has to say so.
		{name: "a mark in front of a comment", envfile: "A=1\n" + bom + "# " + canary + "\n", wantErr: "byte-order mark"},
		{name: "a mark alone on a line", envfile: "A=1\n" + bom + "\n", wantErr: "byte-order mark"},
	} {
		for _, road := range []string{"the project .env", "a service env_file"} {
			t.Run(tc.name+", through "+road, func(t *testing.T) {
				dir := t.TempDir()
				var composeText string
				if road == "the project .env" {
					composeText = compose
					mustWriteFile(t, filepath.Join(dir, ".env"), tc.envfile)
				} else {
					composeText = strings.Replace(compose, "    image: app\n",
						"    image: app\n    env_file: svc.env\n", 1)
					mustWriteFile(t, filepath.Join(dir, "svc.env"), tc.envfile)
				}
				cf := filepath.Join(dir, "compose.yaml")
				mustWriteFile(t, cf, composeText)

				keyA, keyB := "OUT_A", "OUT_B"
				if road != "the project .env" {
					keyA, keyB = "A", "B"
				}

				proj, err := Load(cf)
				if tc.wantErr != "" {
					if err == nil {
						t.Fatalf("want a failure saying %q, got none", tc.wantErr)
					}
					if !strings.Contains(err.Error(), tc.wantErr) {
						t.Errorf("a different failure got there first: %v", err)
					}
					if strings.Contains(err.Error(), canary) {
						t.Errorf("the message reads the line back: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
				got := proj.Services["app"].Environment
				for _, want := range []string{keyA + "=" + tc.wantA, keyB + "=" + tc.wantB} {
					if !slices.Contains(got, want) {
						t.Errorf("want %q in the environment, got %q", want, got)
					}
				}
			})
		}
	}
}

// What an env file's errors about the SHAPE of a line are allowed to say. The
// file is where passwords and tokens live, so: name the place, name what is
// wrong with it, and leave the contents alone. The reader can open their own
// file at the line named — they do not need it read back to them.
//
// The shape of the line is as far as this reaches. Expansion of a value can also
// fail, and an unterminated `${` still quotes the rest of the value back — that
// road is shared with the compose file, where the quoted text is the only clue
// to where the problem is, so silencing it needs a decision rather than a patch.
// Tracked separately; deliberately not covered here, because a sweep that
// implied it did would be worse than one that says where it stops.
//
// The line with no separator is the one that used to break the rule: it quoted
// the whole line, and a token pasted onto a line of its own is exactly the shape
// that lands there.
//
// Each case says which failure it expects, so a change that makes some other
// error come first cannot leave this passing on an error it never meant to look
// at — an assertion about a message is worth nothing if it is a different
// message.
func TestAnEnvFileErrorNamesThePlaceNotTheContents(t *testing.T) {
	// The host wins over an env file, so a `K` exported in the shell running the
	// tests would replace what the file says and the second half of this would
	// measure the host instead.
	unsetHostVars(t, "K", "OUT")
	const secret = "ghp-canary-do-not-print"
	for _, tc := range []struct{ name, dotenv, wantIn string }{
		{"a line with no separator", secret + "\n", "expected KEY=VALUE"},
		{"a quoted value that never closes", "K=\"" + secret + "\n", "unterminated quoted value"},
		{"no name in front of the separator", "=" + secret + "\n", "empty variable name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := writeProject(t, "services:\n  app:\n    image: app\n", tc.dotenv)
			_, err := Load(p)
			if err == nil {
				t.Fatal("the env file was accepted")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("a different failure got there first, so this checked nothing: %v", err)
			}
			if !strings.Contains(err.Error(), ".env:1") {
				t.Errorf("the error should name the file and line to open: %v", err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("the error read the file's contents back:\n%v", err)
			}
		})
	}

	// The other direction. A rule that refuses more than it should would empty
	// this whole surface and still pass everything above, because everything above
	// is about failures.
	for _, tc := range []struct{ name, dotenv, want string }{
		{"an ordinary line", "K=" + secret + "\n", secret},
		{"a colon instead of an equals", "K:" + secret + "\n", secret},
		{"an exported line", "export K=" + secret + "\n", secret},
		{"a comment that has no separator", "# " + secret + "\nK=v\n", "v"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := writeProject(t, "services:\n  app:\n    image: app\n    environment:\n      OUT: ${K}\n", tc.dotenv)
			proj, err := Load(p)
			if err != nil {
				t.Fatalf("a line that is fine was refused: %v", err)
			}
			if got := proj.Services["app"].Environment; !slices.Contains(got, "OUT="+tc.want) {
				t.Errorf("want OUT=%q in the environment, got %v", tc.want, got)
			}
		})
	}
}

// A variable's value must not turn up in an error. Variables are where passwords
// and tokens live, and an error goes to a terminal, a CI log, and an issue; what
// the reader needs is which variable to go and fix, not what is in it.
//
// Text the FILE holds is a different matter and is not covered here: an
// unterminated reference has to quote the file back, and the reader can already
// read their own file.
//
// The premise is the whole test. The first version of this swept six shapes of
// failure and expanded a secret-bearing variable in none of them, so the secret
// could not have appeared whatever the code did: a refuseMark rewritten to print
// `name + " = " + val` went through it green. What it asserted instead — that
// four of the six still failed — was true and beside the point. So every case
// here fails *with a value containing the secret in the code's hands*, and says
// how it knows.
//
// Knowing it takes care, because two different refusals name U+E000 and only one
// of them has ever held a value: a compose file that contains the mark itself is
// refused up front, before a single variable is looked up. Recognising the
// character alone would accept that one, and then a fixture with a stray mark in
// it would sail through this whole sweep measuring nothing — the same shape of
// hole in a new place. So the test recognises the wording only the refusal that
// holds a value uses, and checks that the other one does not pass for it.
//
// Note the fixture is built with an escape rather than a literal character. A
// stray mark pasted into a fixture is invisible in the source and turns "the
// value was reached" into an accident — which is how this was measured the first
// time.
func TestAVariablesValueNeverAppearsInAnError(t *testing.T) {
	unsetHostVars(t, "V", "NOSUCH")
	const secret = "hunter2-canary-do-not-print"
	// Carries the secret and is refused, so the refusal is raised with the whole
	// value in hand — which is the only moment the value could leak.
	held := secret + "\ue000"

	// notHolding says why err is not the refusal that has a value in its hands, or
	// "" if it is.
	notHolding := func(err error) string {
		switch {
		case err == nil:
			return "it did not fail at all"
		case !strings.Contains(err.Error(), "the value of "):
			return "a different failure got there first: " + err.Error()
		}
		return ""
	}
	reached := func(t *testing.T, err error) {
		t.Helper()
		if why := notHolding(err); why != "" {
			t.Fatalf("nothing was measured about what a failure says — %s", why)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the error carries a variable's value:\n%v", err)
		}
	}

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"a reference to it", "a: ${V}\n"},
		{"a reference to it, unbraced", "a: $V\n"},
		{"a default it does not need", "a: ${V:-fallback}\n"},
		{"a reference that requires it", "a: ${V:?set this first}\n"},
		{"standing in as another variable's default", "a: ${NOSUCH:-${V}}\n"},
		{"in a key", "${V}: 1\n"},
		{"in a comment", "# note ${V}\na: 1\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := interpolateDocument([]byte(tc.raw), lk(map[string]string{"V": held}))
			reached(t, err)
		})
	}

	// The other refusal that names U+E000: the file itself holds the mark, and it
	// is turned away before any variable is read. It must not pass for the one
	// that holds a value — that is what keeps this sweep from going hollow if a
	// mark ever gets into a fixture, which is exactly how it was measured wrong
	// the first time.
	t.Run("a file holding the mark is not mistaken for a value holding it", func(t *testing.T) {
		_, err := interpolateDocument([]byte("a: \ue000\n"), lk(map[string]string{"V": held}))
		if err == nil {
			t.Fatal("a compose file holding the mark was accepted")
		}
		if !strings.Contains(err.Error(), "U+E000") {
			t.Fatalf("the wrong failure got there: %v", err)
		}
		if notHolding(err) == "" {
			t.Errorf("the up-front refusal passed as the one that holds a value: %v", err)
		}
	})

	// And through Load, with the secret where secrets actually live: an `.env`
	// file beside the compose file. A different set of code wraps the error on
	// the way out, and wrapping is where a value gets picked up.
	t.Run("through a .env file", func(t *testing.T) {
		p := writeProject(t, "services:\n  app:\n    image: app\n    environment:\n      K: ${V}\n",
			"V="+held+"\n")
		_, err := Load(p)
		reached(t, err)
	})
}

// A value carrying the mark, arriving through the shell or an env file rather
// than through the file itself. The file is checked before anything starts; this
// is the same problem by another road, and letting it through would conflate what
// somebody set with what expansion produced.
func TestAValueCarryingTheMarkIsRefused(t *testing.T) {
	// The recognisable part is plain ASCII on purpose. Asserting on a value that
	// contains the mark misses a message built with %q, which escapes the mark and
	// so never contains the value as written — the leak happens and the check
	// passes.
	const val = "canary" + "\ue000" + "canary"
	// Both spellings of a reference write the value, so both have to check it, and
	// each names itself as it was written rather than as the other one.
	for _, tc := range []struct{ raw, wantRef string }{
		{"a: ${V}\n", "${V}"},
		{"a: $V\n", "$V"},
	} {
		_, err := interpolateDocument([]byte(tc.raw), lk(map[string]string{"V": val}))
		if err == nil {
			t.Fatalf("%q: a value carrying the mark was written into the document", tc.raw)
		}
		if !strings.Contains(err.Error(), "U+E000") {
			t.Errorf("%q: the error should name the character, got: %v", tc.raw, err)
		}
		// The variable, so the reader knows which one to go and fix — and not the
		// value, which is where a password lives.
		if !strings.Contains(err.Error(), tc.wantRef) {
			t.Errorf("%q: the error should name the reference %s, got: %v", tc.raw, tc.wantRef, err)
		}
		if strings.Contains(err.Error(), "canary") {
			t.Errorf("%q: the error printed the value itself: %v", tc.raw, err)
		}
	}
}

// The two shapes a rule based on positions in the text could not reach, and what
// they cost: an item of a flow sequence disappeared entirely, so a command lost an
// argument without saying so; a value written under its key was left inheriting
// from the host. Neither needed a rule of its own once the mark carried the answer
// through the parser — which is the whole argument for doing it this way.
func TestTheShapesPositionsCouldNotReach(t *testing.T) {
	unsetHostVars(t, "NOPE", "PREFIX")
	for _, tc := range []struct {
		name    string
		compose string
		want    []string // every entry the service must end up with
	}{
		{
			name:    "an item of a flow sequence, last",
			compose: "    command: [echo, ${NOPE}]\n",
			want:    []string{"echo", ""},
		},
		{
			// First and middle used to fail the load outright: `[, a]` is not a list.
			name:    "an item of a flow sequence, first",
			compose: "    command: [${NOPE}, echo]\n",
			want:    []string{"", "echo"},
		},
		{
			name:    "an item of a flow sequence, middle",
			compose: "    command: [echo, ${NOPE}, done]\n",
			want:    []string{"echo", "", "done"},
		},
		{
			name:    "a block sequence item",
			compose: "    command:\n      - echo\n      - ${NOPE}\n",
			want:    []string{"echo", ""},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := writeProject(t, "services:\n  app:\n    image: app\n"+tc.compose, "")
			proj, err := Load(p)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			got := proj.Services["app"].Command
			if len(got) != len(tc.want) {
				t.Fatalf("command = %#v, want %#v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("command[%d] = %q, want %q (all: %#v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

// A value written under its key rather than beside it. The parser reports its
// empty value up on the key's line, which is why a rule that compared positions
// in the text could not reach it — and left the key inheriting from the host.
func TestAValueOnTheLineBelowItsKeyIsEmptiedToo(t *testing.T) {
	unsetHostVars(t, "NOPE", "PREFIX")
	for _, tc := range []struct {
		name    string
		compose string
		want    string
	}{
		{
			name:    "the value is a line below its key",
			compose: "    environment:\n      K:\n        ${NOPE}\n",
			want:    "K=",
		},
		{
			// The line below begins with a reference of its own, and the key above it
			// is the author's own null. Comparing positions could not tell these two
			// apart — nothing but whitespace separates either — so this one had to be
			// left alone by leaving both alone.
			name:    "the line below begins with a reference of its own",
			compose: "    environment:\n      K:\n      ${PREFIX}FOO: 1\n",
			want:    "K", // still: inherit from the host
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := writeProject(t, "services:\n  app:\n    image: app\n"+tc.compose, "")
			proj, err := Load(p)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if env := proj.Services["app"].Environment; !slices.Contains(env, tc.want) {
				t.Errorf("want %q in the environment, got %v", tc.want, env)
			}
		})
	}
}

// Expansion can leave text that is no longer YAML — a value that ends in a colon
// reads as a mapping key, so the line is a mapping inside a mapping value. The
// repair cannot run on that, and what it does
// instead is a decision with a user-visible consequence: it hands the bytes back
// so the loader reports the syntax error in its own words, naming the file and
// pointing at the line, rather than reporting "interpolating <file>" for
// something interpolation did not fail at.
func TestTextThatExpansionBrokeIsReportedAsASyntaxError(t *testing.T) {
	unsetHostVars(t, "TENANT")
	p := writeProject(t, "services:\n  app:\n    image: app\n    environment:\n      NS: ${TENANT}:\n", "TENANT=acme\n")
	_, err := Load(p)
	if err == nil {
		t.Fatal("Load succeeded on a document expansion had broken")
	}
	for _, want := range []string{"is not valid YAML", "check the indentation and quoting"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should carry %q, got: %v", want, err)
		}
	}
}

// A sweep of the shapes a line can take — the line endings YAML recognises, a
// byte-order mark, multi-byte text, a file that does not end in a newline. It was
// written when the repair worked out positions in the text itself and had to count
// them exactly as the parser does; that machinery is gone, and this is kept as
// what it always measured underneath: whatever the shape of the file, a value that
// expanded to nothing arrives empty rather than inherited from the host.
func TestTheShapeOfTheFileDoesNotChangeWhatAnEmptiedValueBecomes(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  string
	}{
		{"plain ASCII", "a: 1\nK: ${NOPE}\n"},
		// The reference is the last thing in the file: it wrote at the end of the
		// output, past every character there is to walk over.
		{"no trailing newline", "a: 1\nK: ${NOPE}"},
		// Columns restart at each line, so a rune on an EARLIER line cannot move a
		// later one — kept as the control that says so, and not as evidence about
		// counting bytes, which it is not.
		{"a multi-byte value on an earlier line", "a: 日本語\nK: ${NOPE}\n"},
		// These two are the ones a byte count gets wrong.
		{"a multi-byte key on the same line", "{日本語: 1, K: ${NOPE}}\n"},
		{"a two-byte rune on the same line", "{caf\u00e9: 1, K: ${NOPE}}\n"},
		// Each of these starts a line as far as the parser is concerned, so a
		// reference below one of them is on a line the repair has to agree about.
		{"CRLF line endings", "a: 1\r\nK: ${NOPE}\r\n"},
		{"CR line endings", "a: 1\rK: ${NOPE}\r"},
		{"a NEL line ending", "a: 1\u0085K: ${NOPE}\n"},
		{"an LS line ending", "a: 1\u2028K: ${NOPE}\n"},
		{"a PS line ending", "a: 1\u2029K: ${NOPE}\n"},
		// The parser skips a byte-order mark; counting it as a column shifts the
		// whole of the first line.
		{"a byte-order mark", "\ufeffK: ${NOPE}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := interpolateDocument([]byte(tc.doc), lk(nil))
			if err != nil {
				t.Fatalf("interpolateDocument: %v", err)
			}
			var got map[string]any
			if err := out.into(&got); err != nil {
				t.Fatalf("the repaired document does not decode: %v", err)
			}
			v, ok := got["K"]
			if !ok {
				t.Fatalf("K is missing from %v", got)
			}
			if v != "" {
				t.Errorf("K = %#v, want the empty string — nil is YAML null, which under `environment:` means inherit from the host", v)
			}
		})
	}
}

// The same repair when the compose file has been split in two. Merging reads each
// file on its own before combining them, which is a second road into the same
// code, and the marks have to be gone on that one too.
//
// This is here because a mutation found it: reading the per-file bytes instead of
// the per-file tree left every test green while a marker went into the merged
// values. The single-file road was covered and this one was not — the same shape
// of gap that let a byte-order mark through on one of two roads.
func TestSplittingTheFileDoesNotBringTheMarkBack(t *testing.T) {
	unsetHostVars(t, "NOPE", "X", "Y")
	dir := t.TempDir()
	base := filepath.Join(dir, "compose.yaml")
	over := filepath.Join(dir, "override.yaml")
	mustWriteFile(t, base, "services:\n  app:\n    image: app\n    environment:\n      X: ${NOPE}\n")
	mustWriteFile(t, over, "services:\n  app:\n    environment:\n      Y: ${NOPE}\n")

	proj, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	got := proj.Services["app"].Environment
	for _, want := range []string{"X=", "Y="} {
		if !slices.Contains(got, want) {
			t.Errorf("want %q in the environment, got %q", want, got)
		}
	}
	// And explicitly: not the marker, and not a bare key either. A bare key is the
	// defect this whole mechanism exists to stop, and it is what an emptied value
	// becomes when nothing repairs it.
	for _, e := range got {
		if strings.Contains(e, "\ue000") {
			t.Errorf("a marker reached the environment: %q", got)
		}
		if e == "X" || e == "Y" {
			t.Errorf("%q came out as a bare key, which means inherit from the host: %q", e, got)
		}
	}
}

// When expansion leaves text that will not parse, what comes back has to be the
// EXPANDED text, not the file as it was written.
//
// The difference only shows when the original parses and the expansion does not
// — a variable whose value is itself broken YAML. Hand back the original and it
// parses cleanly with `${V}` still in it, so the service is handed the reference
// as a literal string and nothing anywhere says so. Hand back the expanded text
// and it fails, which is the truth.
//
// A mutation returning the original survived the whole suite: the case that was
// there (`c: *x${NOPE}y`) is broken before expansion too, so it could not tell
// the two apart.
func TestWhatComesBackFromAnUnparsableDocumentIsTheExpandedText(t *testing.T) {
	unsetHostVars(t, "NOPE")
	// The original parses: `a: ${V}` is an ordinary scalar. Expanded, V's value
	// turns the line into an unterminated flow sequence.
	raw := "a: ${V}\nb: ${NOPE}\n"
	out, err := interpolateDocument([]byte(raw), lk(map[string]string{"V": "x: ["}))
	if err != nil {
		t.Fatalf("interpolateDocument: %v", err)
	}
	if out.node != nil {
		t.Fatal("the document parsed after expansion, so this case no longer measures anything")
	}
	var got map[string]any
	if err := out.into(&got); err == nil {
		t.Errorf("the broken expansion was accepted, and `a` came back as %#v — "+
			"handing back the file as written would do exactly this", got["a"])
	}
}
