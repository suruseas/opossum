package compose

// The fields that take one string (#735's family, after #759): `image: 42`
// was read as the text "42", `cap_add: NET_ADMIN` as a one-item list, and
// `deploy.resources.limits.memory: 512` as "512". docker compose (v5.5.0)
// refuses each — `services.web.image must be a string`, `services.web.cap_add
// must be a array`, `…limits.memory must be a string` (exit codes and full
// output read, not grepped for) — and takes a bare number for `mem_limit:`
// and `cpus:`, and one string for `tmpfs:`, as this still does.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestANonStringInAStringFieldIsRefused(t *testing.T) {
	for _, tc := range []struct{ field, item, word string }{
		{"image", "42", "a number"},
		{"user", "1000", "a number"},
		{"working_dir", "42", "a number"},
		{"platform", "42", "a number"},
		{"network_mode", "true", "true/false"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			body := "services:\n  web:\n    image: alpine\n    " + tc.field + ": " + tc.item + "\n"
			if tc.field == "image" {
				body = "services:\n  web:\n    image: " + tc.item + "\n"
			}
			got := loadErr(t, body)
			if !strings.Contains(got, tc.field+" must be a string, got "+tc.word) {
				t.Errorf("want the refusal, got:\n%s", got)
			}
		})
	}
}

func TestAListOnlyFieldWrittenAsOneValueIsRefused(t *testing.T) {
	// `profiles` is one of these too (docker compose: `services.web.profiles
	// must be a array`); its decode into []string refused it as well, but in
	// the decoder's words, naming the Go type.
	for _, field := range []string{"cap_add", "cap_drop", "profiles"} {
		t.Run(field, func(t *testing.T) {
			got := loadErr(t, "services:\n  web:\n    image: alpine\n    "+field+": NET_ADMIN\n")
			if strings.Contains(got, "[]string") || strings.Contains(got, "unmarshal") {
				t.Errorf("the refusal should not name a Go type or the decoder, got:\n%s", got)
			}
			if !strings.Contains(got, field+" must be a list, got a single value — write it as `- NET_ADMIN`") {
				t.Errorf("want the list refusal, got:\n%s", got)
			}
		})
	}
	// A bare number there is told "a list" too — not "a string" and then,
	// once quoted, "a list" (docker compose: `must be a array` either way).
	for _, item := range []string{"42", "true"} {
		t.Run("cap_add "+item, func(t *testing.T) {
			got := loadErr(t, "services:\n  web:\n    image: alpine\n    cap_add: "+item+"\n")
			if !strings.Contains(got, "cap_add must be a list, got a single value") {
				t.Errorf("want the list refusal first, got:\n%s", got)
			}
		})
	}
}

func TestANumberAsDeployMemoryLimitIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"plain", "services:\n  web:\n    image: alpine\n    deploy:\n      resources:\n        limits:\n          memory: 512\n"},
		{"through a merge key", "x-lim: &lim\n  memory: 512\nservices:\n  web:\n    image: alpine\n    deploy:\n      resources:\n        limits:\n          <<: *lim\n"},
		{"through an alias on the way down", "x-lim: &lim\n  memory: 512\nservices:\n  web:\n    image: alpine\n    deploy:\n      resources:\n        limits: *lim\n"},
		{"through an alias on the value", "x-m: &m 512\nservices:\n  web:\n    image: alpine\n    deploy:\n      resources:\n        limits:\n          memory: *m\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, "deploy.resources.limits.memory must be a string, got a number") {
				t.Errorf("want the memory refusal, got:\n%s", got)
			}
		})
	}
}

// What docker compose takes, and so does this: a quoted number in a string
// field, a bare number for mem_limit and cpus, one string for tmpfs.
func TestWhatDockerTakesInScalarFieldsIsStillTaken(t *testing.T) {
	p, err := Load(writeTemp(t, "services:\n  web:\n    image: \"42\"\n    user: \"1000\"\n    mem_limit: 512m\n    cpus: 0.5\n    tmpfs: /run\n    deploy:\n      resources:\n        limits:\n          memory: 512m\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	web := p.Services["web"]
	if web.Image != "42" || web.User != "1000" || strings.Join(web.Tmpfs, ",") != "/run" {
		t.Errorf("quoted and scalar forms should load as written: image=%q user=%q tmpfs=%v", web.Image, web.User, web.Tmpfs)
	}
}

// Across -f files the override's number reaches the merged document with its
// kind intact, and is refused there.
func TestANonStringInAStringFieldIsRefusedAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	if err := os.WriteFile(base, []byte("services:\n  web:\n    image: alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(over, []byte("services:\n  web:\n    user: 1000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFiles([]string{base, over}, nil); err == nil || !strings.Contains(err.Error(), "user must be a string, got a number") {
		t.Errorf("want the refusal after the merge, got: %v", err)
	}
}
