package compose

// A YAML alias as a list item (#745). The decoder resolves an alias before it
// hands a whole value to a custom decoder, but the items of a list arrive as
// raw nodes, so the four decoders that walk their items by kind — volumes,
// ports, secrets, env_file — saw an alias to a *string* as not-a-scalar,
// sent it down the mapping branch, and failed the load. An alias to a
// mapping already worked (that branch decodes the item, which resolves it);
// those are kept below so the two spellings stay side by side, but only the
// string aliases guard the change. docker compose (v5.5.0) accepts every
// shape below.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnAliasAsAListItemIsReadAsWhatItStandsFor(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{".env", "secret.txt"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("A=1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	body := `x-vol: &v ./a:/a
x-lf: &lf
  type: bind
  source: ./b
  target: /b
x-port: &p "8080:80"
x-plf: &plf
  target: 90
  published: 9090
x-sec: &s db_password
x-secl: &secl
  source: db_password
  target: pw
x-ef: &ef ./.env
x-efl: &efl
  path: ./.env
  required: false
secrets:
  db_password:
    file: ./secret.txt
services:
  web:
    image: alpine
    volumes: [*v, *lf]
    ports: [*p, *plf]
    secrets: [*s, *secl]
    env_file: [*ef, *efl]
`
	path := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	web := p.Services["web"]
	if got := strings.Join(web.Volumes, " "); got != "./a:/a ./b:/b" {
		t.Errorf("volumes = %q", got)
	}
	if got := strings.Join(web.Ports, " "); got != "8080:80 9090:90" {
		t.Errorf("ports = %q", got)
	}
	if len(web.Secrets) != 2 || web.Secrets[0].Source != "db_password" || web.Secrets[1].Target != "pw" {
		t.Errorf("secrets = %+v", web.Secrets)
	}
	if len(web.EnvFile) != 2 || web.EnvFile[0].Path != "./.env" || web.EnvFile[1].Required {
		t.Errorf("env_file = %+v", web.EnvFile)
	}
}

// An alias to nothing is an empty item, and the empty-item refusal (#735)
// must see through the alias to say so — not fall into the long-form branch.
func TestAnAliasToNothingIsAnEmptyItem(t *testing.T) {
	for _, tc := range []struct{ field, want string }{
		{"volumes", "volumes entry 2 of 2 is empty"},
		{"ports", "ports entry 2 of 2 is empty"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			got := loadErr(t, "x-n: &n ~\nservices:\n  web:\n    image: alpine\n    "+tc.field+":\n      - \"8080:80\"\n      - *n\n")
			if !strings.Contains(got, tc.want) {
				t.Errorf("want the empty-entry refusal, got:\n%s", got)
			}
		})
	}
}

// `networks: *nets` used to make the ignored-field report go quiet: the
// second decoding pass keeps the value as a yaml.Node, in which an alias
// stays an alias, so the map-form entries' aliases and addresses were never
// listed. Written plainly beside it, the same entries are.
func TestIgnoredNetworkFieldsAreReportedThroughAnAlias(t *testing.T) {
	p, err := Load(writeTemp(t, "x-nets: &nets\n  back:\n    aliases: [db-alias]\nservices:\n  web:\n    image: alpine\n    networks: *nets\n  web2:\n    image: alpine\n    networks:\n      back:\n        aliases: [db-alias]\nnetworks:\n  back: {}\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	plain := strings.Join(p.Services["web2"].Unsupported, ",")
	if plain == "" {
		t.Fatalf("the plain spelling should report the ignored aliases field")
	}
	if got := strings.Join(p.Services["web"].Unsupported, ","); got != plain {
		t.Errorf("through the alias the report is %q, plainly it is %q", got, plain)
	}
}
