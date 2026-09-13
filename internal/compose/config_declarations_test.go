package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The declarations that decide what a run means travel with the config
// output: a volume's `external` and `name`, a secret's and a config's source,
// and each service's references. Without them the output run back made an
// external volume a namespaced, seeded volume of its own and dropped every
// secret and config mount (#939) — a change of meaning that the textual
// fixed point cannot see, because the second pass drops them the same way.
func TestConfigWritesTheDeclarationsThatDecideARun(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PROBE_CF", "value-from-env")
	for name, body := range map[string]string{
		"pg_password.txt": "secret\n",
		"api_key.txt":     "key\n",
		"cf.txt":          "cf\n",
		"compose.yaml": `name: decl
services:
  db:
    image: postgres:16
    volumes: ["bare:/a", "named:/b", "ext:/c"]
    secrets:
      - source: pg_password
        target: pw
      - pg_password
      - api_key
    configs:
      - source: app_cf
        target: /etc/app/cf.txt
      - app_cf
      - source: app_cf
        target: /x/app_cf
      - inline
      - fromenv
volumes:
  bare:
  named: {name: my-custom}
  ext: {external: true, name: real-name-outside}
secrets:
  pg_password: {file: ./pg_password.txt}
  api_key: {file: ./api_key.txt}
configs:
  app_cf: {file: ./cf.txt}
  inline: {content: "user=$$HOME\n"}
  fromenv: {environment: PROBE_CF}
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	// The renderer's indentation is four spaces per level. Two declarations of
	// each kind, references in long-then-short order, a target that contains
	// the source without being its default, and the content and environment
	// forms: each is a shape a rewrite that read the wrong declaration, decided
	// the form by position, or matched the target loosely would get wrong.
	for name, want := range map[string]string{
		"external volume, with the name up already mounts it by": "    ext:\n        external: true\n        name: real-name-outside\n",
		"secret file":        "    pg_password:\n        file: ./pg_password.txt\n",
		"second secret file": "    api_key:\n        file: ./api_key.txt\n",
		"config file":        "    app_cf:\n        file: ./cf.txt\n",
		"config content":     "    inline:\n        content: |\n            user=$$HOME\n",
		"config environment: the variable's name, not its value": "    fromenv:\n        environment: PROBE_CF\n",
		"secret ref, short form":                                 "            - pg_password\n            - api_key\n",
		"secret ref, own target":                                 "            - source: pg_password\n              target: pw\n",
		"config ref, short form":                                 "            - app_cf\n            - source: app_cf\n              target: /x/app_cf\n            - inline\n",
		"config ref, own target":                                 "            - source: app_cf\n              target: /etc/app/cf.txt\n",
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(out, want) {
				t.Errorf("want %q in the config, got:\n%s", want, out)
			}
		})
	}
	// A bare declaration stays bare (no `external: false` noise), so the
	// output reads as the input did.
	if strings.Contains(out, "external: false") {
		t.Errorf("a bare volume is written bare, got:\n%s", out)
	}
	// A project volume's `name` is the name `up` creates it under (#937), so the
	// output carries it — the assertion that used to keep it out, while the run
	// did not read it, turned around in the same change that made the run read it.
	if want := "    named:\n        name: my-custom\n"; !strings.Contains(out, want) {
		t.Errorf("a project volume's `name:` must be written, want %q in:\n%s", want, out)
	}
}
