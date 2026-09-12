package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `configs` (#872), read as docker compose (v5.5.0) reads it: a top-level
// config is a file placed in the container — from a host file, from text in
// the compose file, or from a variable — and a service names the ones it
// takes, at `/<name>` or at a target of its own. The oracle fixtures and
// docker's output are with the dogfood (sweeps/configs-oracle).
func TestConfigsParsedShortAndLong(t *testing.T) {
	t.Setenv("CFG_X", "fromenv")
	t.Setenv("CFG_ENV", "fromshell")
	p, err := Load(writeTemp(t, `
name: demo
configs:
  app_conf:
    file: ./app.conf
  inline:
    content: |
      inline content ${CFG_X:-defaultx}
  fromenv:
    environment: CFG_ENV
services:
  web:
    image: alpine:3.20
    configs:
      - app_conf
      - source: app_conf
        target: /etc/renamed.conf
        uid: "0"
        gid: "0"
        mode: 0440
      - source: inline
        target: relative/inline.txt
      - fromenv
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := p.Configs["app_conf"].File; got != "./app.conf" {
		t.Errorf("file config = %q, want ./app.conf", got)
	}
	if c := p.Configs["inline"].Content; c == nil || *c != "inline content fromenv\n" {
		t.Errorf("content config = %v, want the interpolated text", c)
	}
	if c := p.Configs["fromenv"]; c.EnvVar != "CFG_ENV" || !c.EnvSet || c.EnvValue != "fromshell" {
		t.Errorf("environment config = %+v, want CFG_ENV read from the shell as fromshell", c)
	}
	refs := p.Services["web"].Configs
	want := ConfigRefs{
		{Source: "app_conf", Target: "/app_conf"},
		{Source: "app_conf", Target: "/etc/renamed.conf"},
		{Source: "inline", Target: "/relative/inline.txt"},
		{Source: "fromenv", Target: "/fromenv"},
	}
	if len(refs) != len(want) {
		t.Fatalf("refs = %+v, want %+v", refs, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Errorf("ref %d = %+v, want %+v", i, refs[i], want[i])
		}
	}
	// Read, so not listed as ignored — but uid/gid/mode are, by entry, as
	// docker compose warns it ignores them.
	if indexOfStr(p.Unsupported, "configs") >= 0 || indexOfStr(p.Services["web"].Unsupported, "configs") >= 0 {
		t.Errorf("configs should be read, not flagged: top=%v svc=%v", p.Unsupported, p.Services["web"].Unsupported)
	}
	for _, k := range []string{"configs entry 2.uid", "configs entry 2.gid", "configs entry 2.mode"} {
		if indexOfStr(p.Services["web"].Unsupported, k) < 0 {
			t.Errorf("%s should be listed as ignored, got %v", k, p.Services["web"].Unsupported)
		}
	}
	for _, k := range []string{"configs entry 2.source", "configs entry 2.target"} {
		if indexOfStr(p.Services["web"].Unsupported, k) >= 0 {
			t.Errorf("%s is read and must not be listed as ignored, got %v", k, p.Services["web"].Unsupported)
		}
	}
}

// An `environment:` config reads the project's environment: `.env` next to
// the compose file is a source, and the shell wins over it (measured).
func TestAnEnvironmentConfigReadsDotEnvUnderTheShell(t *testing.T) {
	t.Setenv("OPOSSUM_T_CFG_SHELL", "fromshell")
	os.Unsetenv("OPOSSUM_T_CFG_ONLYDOTENV")
	os.Unsetenv("OPOSSUM_T_CFG_UNSET")
	p := writeTemp(t, "configs:\n  a: {environment: OPOSSUM_T_CFG_ONLYDOTENV}\n  b: {environment: OPOSSUM_T_CFG_SHELL}\n  c: {environment: OPOSSUM_T_CFG_UNSET}\nservices:\n  web:\n    image: a\n    configs: [a, b, c]\n")
	if err := os.WriteFile(filepath.Join(filepath.Dir(p), ".env"), []byte("OPOSSUM_T_CFG_ONLYDOTENV=fromdotenv\nOPOSSUM_T_CFG_SHELL=fromdotenv\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c := proj.Configs["a"]; !c.EnvSet || c.EnvValue != "fromdotenv" {
		t.Errorf("a variable only in .env should be read from it, got %+v", c)
	}
	if c := proj.Configs["b"]; !c.EnvSet || c.EnvValue != "fromshell" {
		t.Errorf("the shell should win over .env, got %+v", c)
	}
	if c := proj.Configs["c"]; c.EnvSet || c.EnvValue != "" {
		t.Errorf("an unset variable should be marked unset at load (refused when the container is made), got %+v", c)
	}
}

func TestConfigsRefusedTheWayDockerRefusesThem(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"undefined reference", "services:\n  web:\n    image: a\n    configs: [nope]\n", `service "web" refers to undefined config "nope"`},
		{"external", "configs:\n  ext: {external: true}\nservices:\n  web:\n    image: a\n    configs: [ext]\n", `external config "ext" is not supported`},
		{"external in map form", "configs:\n  ext: {external: {name: x}}\nservices:\n  web:\n    image: a\n    configs: [ext]\n", `external config "ext" is not supported`},
		{"none of the three forms", "configs:\n  e: {}\nservices:\n  web:\n    image: a\n    configs: [e]\n", "must set one of file, content or environment"},
		{"two forms at once", "configs:\n  two: {file: ./a, content: x}\nservices:\n  web:\n    image: a\n    configs: [two]\n", "mutually exclusive"},
		{"not a list", "configs:\n  c: {file: ./a}\nservices:\n  web:\n    image: a\n    configs: notalist\n", "configs must be a list"},
		{"a number in the list", "configs:\n  c: {file: ./a}\nservices:\n  web:\n    image: a\n    configs: [42]\n", "configs"},
		{"long form without source", "configs:\n  c: {file: ./a}\nservices:\n  web:\n    image: a\n    configs:\n      - target: /x\n", "has no source"},
		{"a bare file: key", "configs:\n  c: {file: }\nservices:\n  web:\n    image: a\n    configs: [c]\n", "file"},
		{"target that climbs", "configs:\n  c: {file: ./a}\nservices:\n  web:\n    image: a\n    configs:\n      - source: c\n        target: /etc/../x\n", "must not contain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want an error containing %q, got: %v", tc.want, err)
			}
		})
	}
	// A declared config no service takes is fine, as docker compose takes it,
	// and an unread key inside a declaration is named among the ignored.
	p, err := Load(writeTemp(t, "configs:\n  unused: {file: ./a, labels: {x: y}}\nservices:\n  web:\n    image: a\n"))
	if err != nil {
		t.Fatalf("an unused config should load: %v", err)
	}
	if indexOfStr(p.Unsupported, "configs.unused.labels") < 0 {
		t.Errorf("configs.unused.labels should be listed as ignored, got %v", p.Unsupported)
	}
	// content: "" is refused as no form set: docker compose drops the empty
	// content and places an empty directory at the target (measured), which
	// is not what anyone writes `content: ""` for.
	_, err = Load(writeTemp(t, "configs:\n  empty: {content: \"\"}\nservices:\n  web:\n    image: a\n    configs: [empty]\n"))
	if err == nil || !strings.Contains(err.Error(), "must set one of file, content or environment") {
		t.Errorf("an empty content config should be refused as no form, got: %v", err)
	}
	// Whitespace-only content is content (docker compose places it).
	p, err = Load(writeTemp(t, "configs:\n  blank: {content: \"  \"}\nservices:\n  web:\n    image: a\n    configs: [blank]\n"))
	if err != nil {
		t.Fatalf("a whitespace content config should load: %v", err)
	}
	if c := p.Configs["blank"].Content; c == nil || *c != "  " {
		t.Errorf("whitespace content = %v, want the two spaces kept", c)
	}
	// The long form without a target is the short form: placed at /<source>.
	p, err = Load(writeTemp(t, "configs:\n  c: {file: ./a}\nservices:\n  web:\n    image: a\n    configs:\n      - source: c\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if refs := p.Services["web"].Configs; len(refs) != 1 || refs[0] != (ConfigRef{Source: "c", Target: "/c"}) {
		t.Errorf("long form without target = %+v, want /c", refs)
	}
}
