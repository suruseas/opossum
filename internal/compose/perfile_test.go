package compose

// With several -f files each file is checked on its own before the merge,
// as docker compose (v5.5.0) validates each (#762; exit codes and full
// output read): a mistake in one file is refused naming that file, whether
// a later file writes over it (`command: [42]` then `command: [sh]`) or an
// earlier one had it right; a later file that carries only part of a
// service (no image), or only `networks:`, is fine on its own; a mistake
// in a declaration (`networks.back.internal: 42`) is named with its file
// too. What only the merged project can have — a dependency on a service
// no file declares — is still refused after the merge. opossum used to
// decode only the merged document, so a mistake a later file wrote over
// passed, and one it did not was named with every file and a line in a
// merged text nobody wrote.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEachFileIsCheckedOnItsOwnBeforeTheMerge(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("good.yml", "services:\n  web:\n    image: alpine\n    command: [sh]\n    networks: [back]\nnetworks:\n  back: {}\n")
	badCmd := write("bad-cmd.yml", "services:\n  web:\n    image: alpine\n    command: [42]\n")
	fixCmd := write("fix-cmd.yml", "services:\n  web:\n    command: [sh]\n")
	badDecl := write("bad-decl.yml", "networks:\n  back: {internal: 42}\n")
	for _, tc := range []struct {
		name  string
		files []string
		want  []string
		not   []string
	}{
		{"a mistake in the first file, written over by the second", []string{badCmd, fixCmd}, []string{"bad-cmd.yml", "command entry 1 of 1 must be a string"}, []string{"fix-cmd.yml", "merged document"}},
		{"a mistake in the second file", []string{good, badCmd}, []string{"bad-cmd.yml", "command entry 1 of 1 must be a string"}, []string{"good.yml", "merged document"}},
		{"a mistake in a declaration, in the second file", []string{good, badDecl}, []string{"bad-decl.yml", "line 2"}, []string{"good.yml", "merged document"}},
		{"a dependency no file declares is found after the merge", []string{good, write("dep.yml", "services:\n  web:\n    depends_on: [db]\n")}, []string{`"db"`}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadFiles(tc.files, nil)
			if err == nil {
				t.Fatal("loaded")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("want %q in:\n%v", w, err)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(err.Error(), n) {
					t.Errorf("%q does not belong in:\n%v", n, err)
				}
			}
		})
	}
	// Fine on its own: a later file with part of a service, or with only a
	// declaration, in either position.
	for _, tc := range []struct {
		name  string
		files []string
	}{
		{"a later file with no image", []string{good, write("frag.yml", "services:\n  web:\n    ports: [\"8080:80\"]\n")}},
		{"a later file with only networks", []string{good, write("nets.yml", "networks:\n  front: {}\n")}},
		{"a first file with only networks", []string{write("nets1.yml", "networks:\n  front: {}\n"), good}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadFiles(tc.files, nil); err != nil {
				t.Errorf("should load: %v", err)
			}
		})
	}
}

// In a later file a bare key reached through an alias — a `<<: *anchor`
// whose anchor carries `ports:` with nothing after it, or `ports: *nada` —
// is "not given" the same as one written in place: docker compose
// (v5.5.0) loads both, and so did opossum before each file was checked
// on its own. The pruned copy keeps the alias's target pruned too.
func TestABareKeyReachedThroughAnAliasInALaterFileIsNotGiven(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := write("base.yml", "services:\n  web:\n    image: alpine\n    ports: [\"8080:80\"]\n")
	for _, tc := range []struct{ name, over string }{
		{"through a merge key", "x-c: &c\n  user: fromanchor\n  ports:\nservices:\n  web:\n    <<: *c\n"},
		{"through an alias to nothing", "x-nada: &nada ~\nservices:\n  web:\n    ports: *nada\n"},
		{"through an alias to a mapping with a bare key", "x-w: &w\n  ports:\nservices:\n  web: *w\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := LoadFiles([]string{base, write("over.yml", tc.over)}, nil)
			if err != nil {
				t.Fatalf("a bare key behind an alias in the override is not given: %v", err)
			}
			if got := strings.Join(p.Services["web"].Ports, ","); got != "8080:80" {
				t.Errorf("ports = %q, want the base file's", got)
			}
		})
	}
	// The first file is read as written, through an alias too.
	_, err := LoadFiles([]string{write("first.yml", "x-nada: &nada ~\nservices:\n  web:\n    image: alpine\n    ports: *nada\n"), base}, nil)
	if err == nil || !strings.Contains(err.Error(), "first.yml") || !strings.Contains(err.Error(), "ports: expected a list, got nothing") {
		t.Errorf("the first file's alias to nothing is the bare key it is in one file, got: %v", err)
	}
}
