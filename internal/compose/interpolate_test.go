package compose

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
		// Folding joins the lines with a space, so the value here reads
		// `oncall:  end` for Compose and `oncall: end` for opossum: expanding
		// before the parser folds means the space the reference left behind is at
		// the end of a line, where folding eats it. That difference is on main too
		// — it belongs to expanding raw text (#419), not to this change. What
		// matters here is that no `""` is injected into the middle of the string,
		// which is what a line-based repair did.
		{
			name:    "a multi-line single-quoted scalar",
			compose: "    environment:\n      K: 'runbook\n        oncall: ${NOPE}\n        end'\n",
			key:     "K", want: "K=runbook oncall: end",
		},
		{
			name:    "a multi-line double-quoted scalar",
			compose: "    environment:\n      K: \"runbook\n        oncall: ${NOPE}\n        end\"\n",
			key:     "K", want: "K=runbook oncall: end",
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

// A value written under its key rather than beside it is one mapping entry, and
// its reference is a line below the null the parser reports. It is not repaired.
//
// This records a limit, and the second case is the reason for it. Matching across
// the line would fix the first — and would break the second, because the next
// line's own indent is whitespace too, so a key that begins with a reference
// (Compose interpolates keys) reads as adjacent to the null above it. Rewriting a
// null the author wrote is the one thing this repair must never do, so the shape
// that is merely unrepaired is the better side to be wrong on.
func TestAValueOnTheLineBelowItsKeyIsNotRepaired(t *testing.T) {
	unsetHostVars(t, "NOPE", "PREFIX")
	for _, tc := range []struct {
		name    string
		compose string
		want    string
	}{
		{
			name:    "the value is a line below its key",
			compose: "    environment:\n      K:\n        ${NOPE}\n",
			want:    "K", // not "K=" — the shape this does not reach
		},
		{
			name:    "the line below begins with a reference of its own",
			compose: "    environment:\n      K:\n      ${PREFIX}FOO: 1\n",
			want:    "K", // K is the author's own null and must stay one
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

// Expansion can leave text that is no longer YAML — a value ending in a colon
// swallows the next line's key. The repair cannot run on that, and what it does
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

// The repair compares a position it works out itself against one the parser
// reports, so the two have to count the same way. They did not: columns were
// counted in bytes and the parser counts runes, and only `\n` started a line
// where the parser starts one at six different characters. Every case below
// leaked a value the compose file never mentions — a service was handed the
// host's variable of that name — and did it silently.
//
// Written as a matrix of what the parser can be asked to count, rather than as
// the four bugs that were found, because the failure is one disagreement wearing
// several faces.
func TestPositionsAreCountedTheWayTheParserCountsThem(t *testing.T) {
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
			if err := yaml.Unmarshal(out, &got); err != nil {
				t.Fatalf("the repaired document does not parse: %v\n%s", err, out)
			}
			v, ok := got["K"]
			if !ok {
				t.Fatalf("K is missing from %v (output %q)", got, out)
			}
			if v != "" {
				t.Errorf("K = %#v, want the empty string — nil is YAML null, which under `environment:` means inherit from the host", v)
			}
		})
	}
}

// marksOf expands src and returns where each reference wrote, as the parser would
// count the position. It is the pair the repair actually runs on.
func marksOf(src string, env map[string]string) ([]refMark, error) {
	out, offsets, err := expand([]byte(src), lk(env))
	if err != nil {
		return nil, err
	}
	_, marks := locate(out, offsets)
	return marks, nil
}

// A value that expands to several lines pushes everything below it down. The
// repair matches on position, so the newlines a value writes have to be counted —
// otherwise the first multi-line value in a file silently moves every reference
// below it out of alignment, and the repair stops finding them.
//
// Asserted on expand's own bookkeeping rather than through Load: a multi-line
// value expanded into a document breaks the YAML anyway (#411), so a document
// fixture would fail for that reason and prove nothing about the counting.
func TestExpandRecordsWhereItWrote(t *testing.T) {
	// Both spellings of a reference write the value, so both have to count it.
	for _, src := range []string{"a: ${MULTI}\nb: ${NOPE}\n", "a: $MULTI\nb: $NOPE\n"} {
		checkExpandMarks(t, src)
	}
}

func checkExpandMarks(t *testing.T, src string) {
	t.Helper()
	marks, err := marksOf(src, map[string]string{"MULTI": "one\ntwo\nthree"})
	if err != nil {
		t.Fatal(err)
	}
	// The output is:
	//
	//	1  a: one
	//	2  two
	//	3  three
	//	4  b:
	//
	// so the first reference starts at line 1 column 4, and the second — the
	// second line of the SOURCE — is on line 4, not line 2: the three lines the
	// first value wrote are the middle of a value, with no reference on them.
	want := []refMark{{1, 4}, {4, 4}}
	if len(marks) != len(want) {
		t.Fatalf("marks = %v, want %v", marks, want)
	}
	for i, w := range want {
		if marks[i] != w {
			t.Errorf("mark %d = %v, want %v (all: %v)", i, marks[i], w, marks)
		}
	}
}

// A position also has to come back to the left margin. A value carrying newlines
// ends wherever its last line ends, so a reference that follows it ON THAT LINE
// starts near the margin and not far along the line the value began on.
func TestAReferenceAfterAMultiLineValueStartsWhereThatValueEnded(t *testing.T) {
	marks, err := marksOf("a: ${MULTI} b: ${NOPE}\n", map[string]string{"MULTI": "one\ntwo\nthree"})
	if err != nil {
		t.Fatal(err)
	}
	// The output is one line short of the source:
	//
	//	1  a: one
	//	2  two
	//	3  three b:
	//
	// `three` ends at column 5, ` b: ` takes four more, so the second reference
	// starts at line 3 column 10.
	want := []refMark{{1, 4}, {3, 10}}
	if len(marks) != len(want) || marks[0] != want[0] || marks[1] != want[1] {
		t.Errorf("marks = %v, want %v", marks, want)
	}
}
