package compose

// `opossum config` shows a bare variable name the way docker compose shows
// it (#801): with the shell's value (`A=x`); left unset by the shell, an
// `environment` name stays bare (docker compose shows `A: null` — the
// runtime is still told the name) and a `build.args` name is left out
// (what `up --build` passes: the builder does not read the shell). An
// empty shell value shows as `A=`. Before, `config` showed every bare
// name as written, which was not what `up` and `build` passed. The value
// is the shell's: docker compose also reads `.env` for it, which neither
// `config` nor `up` does here (named in the docs).

import (
	"os"
	"strings"
	"testing"
)

func TestConfigShowsBareNamesTheWayDockerShowsThem(t *testing.T) {
	t.Setenv("OPOSSUM_TEST_CFG_ESET", "fromenv")
	t.Setenv("OPOSSUM_TEST_CFG_ASET", "fromargs")
	t.Setenv("OPOSSUM_TEST_CFG_EMPTY", "")
	for _, name := range []string{"OPOSSUM_TEST_CFG_EUNSET", "OPOSSUM_TEST_CFG_AUNSET"} {
		if v, ok := os.LookupEnv(name); ok {
			t.Setenv(name, v) // restores it after the test
			os.Unsetenv(name)
		}
	}
	p, err := Load(writeTemp(t, "services:\n  web:\n    build:\n      context: .\n      args:\n        - OPOSSUM_TEST_CFG_ASET\n        - OPOSSUM_TEST_CFG_AUNSET\n        - B=fromfile\n"+
		"    environment:\n      - OPOSSUM_TEST_CFG_ESET\n      - OPOSSUM_TEST_CFG_EUNSET\n      - OPOSSUM_TEST_CFG_EMPTY\n      - C=fromfile\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	out, err := RenderConfig(p)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, tc := range []struct{ name, want string }{
		{"environment: set takes the value", "- OPOSSUM_TEST_CFG_ESET=fromenv"},
		{"environment: unset stays bare", "- OPOSSUM_TEST_CFG_EUNSET\n"},
		{"environment: empty shows as empty", "- OPOSSUM_TEST_CFG_EMPTY=\n"},
		{"environment: written value passes through", "- C=fromfile"},
		{"build.args: set takes the value", "- OPOSSUM_TEST_CFG_ASET=fromargs"},
		{"build.args: written value passes through", "- B=fromfile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(out, tc.want) {
				t.Errorf("want %q in:\n%s", tc.want, out)
			}
		})
	}
	// build.args: unset is left out — the name appears nowhere.
	t.Run("build.args: unset is left out", func(t *testing.T) {
		if strings.Contains(out, "OPOSSUM_TEST_CFG_AUNSET") {
			t.Errorf("an unset build arg should be left out, got:\n%s", out)
		}
	})
	// The loaded project is not rewritten: both declarations keep the bare
	// names (other readers — the adapt overlay's PGDATA check — read them).
	if got := p.Services["web"].Build.Args; len(got) != 3 || got[0] != "OPOSSUM_TEST_CFG_ASET" || got[1] != "OPOSSUM_TEST_CFG_AUNSET" {
		t.Errorf("config should render, not rewrite, the build.args declaration: %v", got)
	}
	if got := p.Services["web"].Environment; len(got) != 4 || got[0] != "OPOSSUM_TEST_CFG_ESET" || got[1] != "OPOSSUM_TEST_CFG_EUNSET" {
		t.Errorf("config should render, not rewrite, the environment declaration: %v", got)
	}
}
