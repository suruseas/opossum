package compose

// A list item YAML read as something other than a string (#724's neighbour,
// #735's family). `volumes: [42]` used to become a mount named "42",
// `networks: [back, 123]` a network named "123", `secrets: [42]` a secret
// named "42", `environment: [42]` a variable "42", and a bare `- ` under
// `environment:` vanished. docker compose (v5.5.0) refuses all of these
// (`services.web.volumes.0 must be a string`, `services.web.secrets.[0]:
// unsupported expose value`, `services.web.environment.[0]: unexpected type
// int`) and keeps taking a bare number under `ports:` — so do these decoders.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAListItemThatIsNotAStringIsRefused(t *testing.T) {
	for _, tc := range []struct{ field, item, word string }{
		{"volumes", "42", "a number"},
		{"volumes", "true", "true/false"},
		{"volumes", "1.5", "a number"},
		{"volumes", "2024-01-01", "a date"},
		{"networks", "123", "a number"},
		{"secrets", "42", "a number"},
		{"secrets", "true", "true/false"},
		{"env_file", "42", "a number"},
		{"environment", "42", "a number"},
		{"environment", "false", "true/false"},
	} {
		t.Run(tc.field+" "+tc.item, func(t *testing.T) {
			first := map[string]string{"volumes": "./a:/a", "networks": "back", "secrets": "tok", "env_file": "./.env", "environment": "A=1"}[tc.field]
			body := "services:\n  web:\n    image: alpine\n    " + tc.field + ":\n      - " + first + "\n      - " + tc.item + "\n"
			body += "networks:\n  back: {}\n  \"" + tc.item + "\": {}\nsecrets:\n  tok:\n    file: ./s.txt\n  \"" + tc.item + "\":\n    file: ./s.txt\n"
			got := loadErr(t, body)
			want := tc.field + " entry 2 of 2 must be a string, got " + tc.word
			if !strings.Contains(got, want) {
				t.Errorf("want %q, got:\n%s", want, got)
			}
			if !strings.Contains(got, "quote it") {
				t.Errorf("the refusal should say how to fix it, got:\n%s", got)
			}
		})
	}
}

// The item can be an alias: it is followed before its tag is read.
func TestANonStringItemThroughAnAliasIsRefused(t *testing.T) {
	for _, tc := range []struct{ field, body string }{
		{"networks", "x-n: &n 123\nservices:\n  web:\n    image: alpine\n    networks: [back, *n]\nnetworks:\n  back: {}\n  \"123\": {}\n"},
		{"environment", "x-n: &n 42\nservices:\n  web:\n    image: alpine\n    environment:\n      - A=1\n      - *n\n"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.field+" entry 2 of 2 must be a string, got a number") {
				t.Errorf("want the refusal through the alias, got:\n%s", got)
			}
		})
	}
}

// The neighbouring branches of the same decoders: the map form's keys and the
// scalar form of env_file.
func TestANonStringNetworkKeyOrEnvFileScalarIsRefused(t *testing.T) {
	got := loadErr(t, "services:\n  web:\n    image: alpine\n    networks:\n      123: {}\nnetworks:\n  \"123\": {}\n")
	if !strings.Contains(got, "networks entry 1 of 1 must be a string, got a number") {
		t.Errorf("want the map-key refusal, got:\n%s", got)
	}
	got = loadErr(t, "services:\n  web:\n    image: alpine\n    env_file: 42\n")
	if !strings.Contains(got, "env_file must be a string, got a number") {
		t.Errorf("want the scalar-form refusal, got:\n%s", got)
	}
}

// Quoted, the same text is a string and loads.
func TestQuotedItemsLoadAsWritten(t *testing.T) {
	p, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    volumes: [\"42:/n\"]\n    networks: [\"123\"]\n    environment: [\"42\"]\n    secrets: [\"42\"]\nnetworks:\n  \"123\": {}\nsecrets:\n  \"42\":\n    file: ./s.txt\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	web := p.Services["web"]
	if got := strings.Join(web.Volumes, ","); got != "42:/n" {
		t.Errorf("volumes = %q", got)
	}
	if got := strings.Join(web.Networks, ","); got != "123" {
		t.Errorf("networks = %q", got)
	}
	if got := strings.Join(web.Environment, ","); got != "42" {
		t.Errorf("environment = %q", got)
	}
	if len(web.Secrets) != 1 || web.Secrets[0].Source != "42" {
		t.Errorf("secrets = %+v", web.Secrets)
	}
}

// What docker compose keeps taking, and so does this: a bare number under
// ports, a YAML-1.1 word (`yes`) that YAML 1.2 reads as a string, a tag it
// takes as text (`!custom`), and a number as a map-form variable's value.
func TestWhatDockerTakesIsStillTaken(t *testing.T) {
	p, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    ports: [3000]\n    networks: [yes]\n    volumes: [!custom ./a:/a]\n    environment:\n      A: 42\nnetworks:\n  \"yes\": {}\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	web := p.Services["web"]
	if got := strings.Join(web.Ports, ","); !strings.Contains(got, "3000") {
		t.Errorf("ports = %q", got)
	}
	if got := strings.Join(web.Networks, ","); got != "yes" {
		t.Errorf("networks = %q", got)
	}
	if got := strings.Join(web.Volumes, ","); got != "./a:/a" {
		t.Errorf("volumes = %q", got)
	}
	if got := strings.Join(web.Environment, ","); got != "A=42" {
		t.Errorf("environment = %q", got)
	}
}

// A bare `- ` under environment: is an empty item, refused — not dropped.
func TestAnEmptyEnvironmentItemIsRefused(t *testing.T) {
	got := loadErr(t, "services:\n  web:\n    image: alpine\n    environment:\n      - \n      - A=1\n")
	if !strings.Contains(got, "environment entry 1 of 2 is empty") {
		t.Errorf("want the empty-entry refusal, got:\n%s", got)
	}
}

// Across -f files the environment lists are merged by variable name, and an
// item with no name used to fall out of that merge unseen — so the pair of
// files passed where either alone is refused. Now the unclean side is handed
// on as it is, for the merged document's decode to refuse.
func TestANonStringEnvironmentItemIsRefusedAcrossFilesToo(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	clean := write("clean.yml", "services:\n  web:\n    image: alpine\n    environment:\n      - A=1\n")
	number := write("number.yml", "services:\n  web:\n    environment:\n      - 42\n")
	bare := write("bare.yml", "services:\n  web:\n    environment:\n      - \n")
	for _, tc := range []struct {
		name  string
		files []string
		want  string
	}{
		{"number in the override", []string{clean, number}, "must be a string, got a number"},
		{"number in the base", []string{number, clean}, "must be a string, got a number"},
		{"bare item in the override", []string{clean, bare}, "environment entry 1 of 1 is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadFiles(tc.files, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want %q across files, got: %v", tc.want, err)
			}
		})
	}
	// And two clean lists still merge by name.
	p, err := LoadFiles([]string{clean, write("more.yml", "services:\n  web:\n    environment:\n      - A=2\n      - B=1\n")}, nil)
	if err != nil {
		t.Fatalf("clean lists: %v", err)
	}
	if got := strings.Join(p.Services["web"].Environment, ","); got != "A=2,B=1" {
		t.Errorf("merged environment = %q, want A=2,B=1", got)
	}
}
