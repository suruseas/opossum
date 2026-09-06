package compose

// Shapes docker compose refuses at validation and opossum used to pass through
// or crash on (#735): a service key with nothing under it, and an empty item in
// a `volumes:` or `ports:` list. Each expectation below was checked against
// `docker compose config` (v5.5.0): the null service is "services.web must be a
// mapping", the empty items are "services.web.volumes.1 must be a string" /
// "services.web.ports.0 must be a number", and the quoted empty string is
// "invalid empty volume spec" / "no port specified: <empty>" — all exit 1.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAServiceKeyWithNothingUnderItIsRefusedNotCrashedOn(t *testing.T) {
	for _, tc := range []struct{ name, body, names string }{
		{"the only service, bare key", "services:\n  web:\n", `service "web" must be a mapping`},
		{"the only service, explicit null", "services:\n  web: ~\n", `service "web" must be a mapping`},
		{"a null service next to a whole one", "services:\n  web:\n    image: alpine\n  db: null\n", `service "db" must be a mapping`},
		{"two null services: the first by name", "services:\n  zed:\n  api:\n", `service "api" must be a mapping`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.names) {
				t.Errorf("want %q in the error, got:\n%s", tc.names, got)
			}
			if !strings.Contains(got, "image:") {
				t.Errorf("the error should say what to give the service, got:\n%s", got)
			}
		})
	}
}

// A scalar under the service key is a different mistake: the decoder already
// refuses it as a value of the wrong shape, and that message is kept — the
// mapping check must not swallow it.
func TestAScalarServiceIsStillAShapeError(t *testing.T) {
	got := loadErr(t, "services:\n  web: 42\n")
	if !strings.Contains(got, "not the shape that field takes") {
		t.Errorf("want the shape-error wording, got:\n%s", got)
	}
}

// The bare key across several -f files is the other reading (#732): an override
// that writes `web:` keeps the earlier file's service. The refusal fires only
// when no file gave the service a body.
func TestABareServiceKeyInAnOverrideKeepsTheBaseButTwoBareKeysAreRefused(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := write("base.yml", "services:\n  web:\n    image: alpine\n")
	bare := write("bare.yml", "services:\n  web:\n")
	p, err := LoadFiles([]string{base, bare}, nil)
	if err != nil {
		t.Fatalf("a bare key in the override should keep the base service: %v", err)
	}
	if p.Services["web"].Image != "alpine" {
		t.Errorf("image = %q, want the base file's", p.Services["web"].Image)
	}
	_, err = LoadFiles([]string{bare, write("bare2.yml", "services:\n  web: ~\n")}, nil)
	if err == nil || !strings.Contains(err.Error(), `service "web" must be a mapping`) {
		t.Errorf("two bare keys leave no service body; want the mapping refusal, got: %v", err)
	}
}

func TestAnEmptyItemInVolumesOrPortsIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"volumes: a dash with nothing after it", "services:\n  web:\n    image: alpine\n    volumes:\n      - ./a:/a\n      - \n", "volumes entry 2 of 2 is empty"},
		// An empty item that is not the last one: "1 of 2" reads the other way
		// round as "2 of 1", which "2 of 2" and "1 of 1" above never could — the
		// position and the count have to be told apart, not just both printed.
		{"volumes: a dash with nothing after it, followed by a mount", "services:\n  web:\n    image: alpine\n    volumes:\n      - \n      - ./b:/b\n", "volumes entry 1 of 2 is empty"},
		{"volumes: the word null", "services:\n  web:\n    image: alpine\n    volumes:\n      - null\n", "volumes entry 1 of 1 is empty"},
		{"volumes: a quoted empty string", "services:\n  web:\n    image: alpine\n    volumes:\n      - \"\"\n", "volumes entry 1 of 1 is empty"},
		{"volumes: a reference that expanded to nothing", "services:\n  web:\n    image: alpine\n    volumes:\n      - ${OPOSSUM_TEST_UNSET_VOLUME}\n", "volumes entry 1 of 1 is empty"},
		{"ports: a dash with nothing after it", "services:\n  web:\n    image: alpine\n    ports:\n      - \"8080:80\"\n      - \n      - \"9090:90\"\n", "ports entry 2 of 3 is empty"},
		{"ports: the word null", "services:\n  web:\n    image: alpine\n    ports:\n      - null\n", "ports entry 1 of 1 is empty"},
		{"ports: a quoted empty string", "services:\n  web:\n    image: alpine\n    ports:\n      - ''\n", "ports entry 1 of 1 is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q in the error, got:\n%s", tc.want, got)
			}
			if !strings.Contains(got, `service "web"`) {
				t.Errorf("the error should name the service, got:\n%s", got)
			}
			if !strings.Contains(got, "remove the `- `") {
				t.Errorf("the error should say how to fix it, got:\n%s", got)
			}
		})
	}
}

// After a merge the tree is re-encoded, so the empty item arrives as the word
// `null` rather than as nothing — the same refusal must fire on that spelling.
// One list per case: the merged document is decoded in key order, so an
// override that empties both lists would report ports and leave the volumes
// guard unmeasured (the first version of this test did exactly that).
func TestAnEmptyItemIsRefusedAfterAMergeToo(t *testing.T) {
	for _, tc := range []struct{ name, override, want string }{
		{"volumes", "services:\n  web:\n    volumes:\n      - ./a:/a\n      - \n", "volumes entry 2 of 2 is empty"},
		{"ports", "services:\n  web:\n    ports:\n      - \n", "ports entry 1 of 1 is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			base := filepath.Join(dir, "base.yml")
			over := filepath.Join(dir, "over.yml")
			if err := os.WriteFile(base, []byte("services:\n  web:\n    image: alpine\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(over, []byte(tc.override), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadFiles([]string{base, over}, nil)
			if err == nil {
				t.Fatal("expected the empty item to be refused after the merge")
			}
			if got := err.Error(); !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
		})
	}
}

// The neighbours that must keep loading: a whole item beside the shapes above,
// including a bare number in ports (a scalar, not empty).
func TestWholeVolumeAndPortItemsStillLoad(t *testing.T) {
	p, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    volumes:\n      - ./a:/a\n      - data:/data\n    ports:\n      - 3000\n      - \"8080:80\"\nvolumes:\n  data:\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := len(p.Services["web"].Volumes); got != 2 {
		t.Errorf("volumes = %d, want 2", got)
	}
	if got := len(p.Services["web"].Ports); got != 2 {
		t.Errorf("ports = %d, want 2", got)
	}
}
