package compose

// The remaining list fields (#735's family, after #758): `profiles`,
// `cap_add`, `cap_drop`, `tmpfs`, `command`, `entrypoint` are read straight
// into strings, so a number became a name — `profiles: [42]` a profile "42"
// that is never active, which made the service vanish from `config` without
// a word. docker compose (v5.5.0) refuses a number or a boolean in each
// (`services.web.profiles.[0]: unexpected type int`, `services.web.command.0
// must be a string`). A date it handles worse — a list item crashes it, and
// a scalar `command: 2024-01-01` is taken and the command silently lost —
// so refusing the date too is the stricter reading, not a match. `expose:
// [42]` docker takes, and so does this.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestANonStringInAStringListFieldIsRefused(t *testing.T) {
	for _, tc := range []struct{ field, first, item, word string }{
		{"profiles", "dev", "42", "a number"},
		{"cap_add", "NET_ADMIN", "true", "true/false"},
		{"cap_drop", "ALL", "1.5", "a number"},
		{"tmpfs", "/run", "42", "a number"},
		{"command", "sh", "42", "a number"},
		{"entrypoint", "sh", "2024-01-01", "a date"},
	} {
		t.Run(tc.field+" "+tc.item, func(t *testing.T) {
			got := loadErr(t, "services:\n  web:\n    image: alpine\n    "+tc.field+":\n      - "+tc.first+"\n      - "+tc.item+"\n")
			want := tc.field + " entry 2 of 2 must be a string, got " + tc.word
			if !strings.Contains(got, want) {
				t.Errorf("want %q, got:\n%s", want, got)
			}
		})
	}
}

// `healthcheck.test` is the same kind of field one level down — written
// plainly, through a `<<:` merge key, or as an alias.
func TestANonStringInHealthcheckTestIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"plain", "services:\n  web:\n    image: alpine\n    healthcheck:\n      test: [CMD, 42]\n"},
		{"through a merge key", "x-hc: &hc\n  test: [CMD, 42]\nservices:\n  web:\n    image: alpine\n    healthcheck:\n      <<: *hc\n      interval: 5s\n"},
		{"through an alias", "x-t: &t [CMD, 42]\nservices:\n  web:\n    image: alpine\n    healthcheck:\n      test: *t\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, "healthcheck.test entry 2 of 2 must be a string, got a number") {
				t.Errorf("want the healthcheck.test refusal, got:\n%s", got)
			}
		})
	}
}

// An item with nothing in it under these fields is refused too (docker
// compose: `unexpected type <nil>`); it used to be dropped.
func TestAnEmptyItemInAStringListFieldIsRefused(t *testing.T) {
	got := loadErr(t, "services:\n  web:\n    image: alpine\n    cap_add:\n      - NET_ADMIN\n      - \n")
	if !strings.Contains(got, "cap_add entry 2 of 2 is empty") {
		t.Errorf("want the empty-entry refusal, got:\n%s", got)
	}
}

// Across -f files a number in a later file's list is refused naming that
// file, before the merge (each file is checked on its own); the entry is
// counted in that file's own list.
func TestANonStringInAStringListFieldIsRefusedAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	if err := os.WriteFile(base, []byte("services:\n  web:\n    image: alpine\n    profiles: [dev]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(over, []byte("services:\n  web:\n    profiles: [42]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFiles([]string{base, over}, nil); err == nil || !strings.Contains(err.Error(), "profiles entry 1 of 1 must be a string, got a number") || !strings.Contains(err.Error(), "over.yml") {
		t.Errorf("want the refusal naming over.yml and its own entry (1 of 1), got: %v", err)
	}
}

// The scalar form of the same fields, and an alias to a number.
func TestANonStringScalarOrAliasInAStringFieldIsRefused(t *testing.T) {
	got := loadErr(t, "services:\n  web:\n    image: alpine\n    command: 42\n")
	if !strings.Contains(got, "command must be a string, got a number") {
		t.Errorf("want the scalar-form refusal, got:\n%s", got)
	}
	got = loadErr(t, "x-n: &n 42\nservices:\n  web:\n    image: alpine\n    profiles: [dev, *n]\n")
	if !strings.Contains(got, "profiles entry 2 of 2 must be a string, got a number") {
		t.Errorf("want the refusal through the alias, got:\n%s", got)
	}
}

// Quoted they load, and `expose` keeps taking a bare number as docker does.
func TestQuotedStringListItemsAndExposeNumbersStillLoad(t *testing.T) {
	p, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    profiles: [\"42\"]\n    cap_add: [\"42\"]\n    command: [\"42\"]\n    expose: [8080]\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	web := p.Services["web"]
	if strings.Join(web.Profiles, ",") != "42" || strings.Join(web.CapAdd, ",") != "42" || strings.Join(web.Command, ",") != "42" {
		t.Errorf("quoted items should load as written: profiles=%v cap_add=%v command=%v", web.Profiles, web.CapAdd, web.Command)
	}
}
