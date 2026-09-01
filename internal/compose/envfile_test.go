package compose

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadEnvFileFoldedIntoEnvironment(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "svc.env"),
		[]byte("FROM_FILE=a\nSHARED=file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(p, []byte(`
services:
  web:
    image: web
    env_file: svc.env
    environment:
      SHARED: env
      ONLY_ENV: x
`), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := []string(proj.Services["web"].Environment)
	// env_file provides FROM_FILE; environment overrides SHARED and adds ONLY_ENV.
	want := []string{"FROM_FILE=a", "SHARED=env", "ONLY_ENV=x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("merged env = %#v, want %#v", got, want)
	}
}

// mustLoadWithEnvFileError loads p, which is expected to load cleanly, and
// returns the error the named service's env_file resolution recorded. It fails
// the test when there is none: the point of these cases is that the error is
// still produced, and only the moment it reaches the caller has moved.
func mustLoadWithEnvFileError(t *testing.T, p, service string) error {
	t.Helper()
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("load should not fail on an env_file a caller has not asked for: %v", err)
	}
	svc, ok := proj.Services[service]
	if !ok {
		t.Fatalf("no service %q in the loaded project", service)
	}
	env, err := svc.ResolvedEnv()
	if err == nil {
		t.Fatalf("service %q resolved to %v, want the env_file failure", service, env)
	}
	// The failure arrives further from the load than it used to, so which
	// service it belongs to is worth more than it was and is easier to drop.
	if !strings.Contains(err.Error(), service) {
		t.Errorf("the failure should name the service it belongs to (%s), got: %v", service, err)
	}
	return err
}

func TestLoadEnvFileMissingErrors(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(p, []byte("services:\n  web:\n    image: web\n    env_file: nope.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The load itself succeeds — nothing has asked for this service yet — and
	// the failure is waiting for whoever renders or starts it.
	if err := mustLoadWithEnvFileError(t, p, "web"); !strings.Contains(err.Error(), "nope.env") {
		t.Errorf("the error should name the file that is missing, got: %v", err)
	}
}

func TestLoadEnvFileOptionalMissingSkipped(t *testing.T) {
	// A long-form entry with required: false is skipped when the file is absent,
	// while a required entry (default) in the same list still errors (#85).
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yaml")
	body := "services:\n  web:\n    image: web\n    env_file:\n" +
		"      - path: absent.env\n        required: false\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("optional missing env_file should not error: %v", err)
	}
	if refs := proj.Services["web"].EnvFile; len(refs) != 1 || refs[0].Required {
		t.Errorf("expected one optional env_file ref, got %+v", refs)
	}

	// Same file marked required (long form) — must error when absent.
	body2 := "services:\n  web:\n    image: web\n    env_file:\n" +
		"      - path: absent.env\n        required: true\n"
	if err := os.WriteFile(p, []byte(body2), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mustLoadWithEnvFileError(t, p, "web"); !strings.Contains(err.Error(), "absent.env") {
		t.Errorf("the error should name the required file that is missing, got: %v", err)
	}
}

func TestLoadEnvFileLongFormRequiredDefaultsTrue(t *testing.T) {
	// A long-form entry with `required` omitted defaults to required=true, so an
	// absent file still errors (guards the default, #85).
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yaml")
	body := "services:\n  web:\n    image: web\n    env_file:\n" +
		"      - path: absent.env\n" // no `required:` key
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mustLoadWithEnvFileError(t, p, "web"); !strings.Contains(err.Error(), "absent.env") {
		t.Errorf("a long-form env_file without `required` defaults to required=true, so an "+
			"absent file has to be an error naming it; got: %v", err)
	}
}

func TestLoadEnvFileMixedOptionalKeepsPresent(t *testing.T) {
	// An optional-missing entry is skipped, but a present file in the same list
	// is still folded into the environment (the skip must not abort the loop).
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "present.env"), []byte("FROM_PRESENT=yes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "compose.yaml")
	body := "services:\n  web:\n    image: web\n    env_file:\n" +
		"      - path: absent.env\n        required: false\n" +
		"      - present.env\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("mixed env_file list should load: %v", err)
	}
	env := []string(proj.Services["web"].Environment)
	found := false
	for _, e := range env {
		if e == "FROM_PRESENT=yes" {
			found = true
		}
	}
	if !found {
		t.Errorf("present env_file value should survive the skipped optional one, got %v", env)
	}
}

func TestMergeEnvPrecedenceAndOrder(t *testing.T) {
	got := mergeEnv([]string{"A=1", "B=1"}, []string{"B=2", "C=3"})
	want := []string{"A=1", "B=2", "C=3"} // B overridden, first-seen order kept
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mergeEnv = %#v, want %#v", got, want)
	}
}

func TestUnsupportedFieldsRecorded(t *testing.T) {
	p := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(p, []byte(`
services:
  web:
    image: web
    container_name: my-web
    restart: unless-stopped
    ports: ["80:80"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// container_name is unsupported; image/ports/restart are acted on and not flagged
	// (restart drives the per-project supervisor).
	if got := proj.Services["web"].Unsupported; !reflect.DeepEqual(got, []string{"container_name"}) {
		t.Errorf("Unsupported = %#v, want [container_name]", got)
	}
	if got := proj.Services["web"].Restart; got != "unless-stopped" {
		t.Errorf("Restart = %q, want unless-stopped", got)
	}
}
