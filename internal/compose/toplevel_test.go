package compose

// The shapes of a file's top-level keys (#846, from the top-level-key
// sweep). docker compose (v5.5.0) refuses a `name:` or `version:` that is
// not a string (`name must be a string` — a number, a boolean, a list, a
// mapping, or nothing), a bare `networks:`/`volumes:`/`secrets:`/`configs:`
// (`must be a mapping`, as it refuses a bare `services:`), `configs` that is
// not a mapping — and so is `networks`/`volumes`/`secrets` that is not
// one, which opossum refused too but in YAML's words, naming a Go type
// (`cannot unmarshal !!seq into map[string]compose.NetworkDecl`) — and an
// `include` that is not a list (a list it reads and includes). opossum read `name: 42` as the project "42", a bare `name:`
// as the directory's name, a bare declaration mapping as none, any shape
// of `configs` as ignored, and `include` — which it does not read — as an
// ignored field, leaving the services of the named files out in silence.
// (An empty or bare `include:` names nothing, and docker compose takes it.)
// With several files, docker compose reads a later file's bare
// `networks:`/`name:` as "not given" only where an earlier file gave the
// key a value, refuses it where none did, and refuses a later bare
// `version:` whatever came before.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestATopLevelKeyOfTheWrongShapeIsRefused(t *testing.T) {
	svc := "services:\n  web:\n    image: alpine\n"
	for _, tc := range []struct{ name, body, want string }{
		{"a name that is a number", svc + "name: 42\n", "name must be a string, got a number (line 4) — quote it (`\"42\"`) if it is meant literally"},
		{"a name that is a boolean", svc + "name: true\n", "name must be a string, got true/false"},
		{"a name that is a list", svc + "name: [a]\n", "name must be a string, got a list (line 4)"},
		{"a name with nothing after it", svc + "name:\n", "name must be a string — the key has nothing after it; write the value or remove the key"},
		{"a version that is a number", svc + "version: 3\n", "version must be a string, got a number (line 4) — quote it (`\"3\"`)"},
		{"a version that is a mapping", svc + "version: {a: b}\n", "version must be a string, got a mapping"},
		{"a version with nothing after it", svc + "version:\n", "version must be a string — the key has nothing after it"},
		{"a bare networks", svc + "networks:\n", "networks must be a mapping — the key has nothing under it; write the declarations or remove the key"},
		{"a bare volumes", svc + "volumes:\n", "volumes must be a mapping — the key has nothing under it"},
		{"a bare secrets", svc + "secrets:\n", "secrets must be a mapping — the key has nothing under it"},
		{"a bare configs", svc + "configs:\n", "configs must be a mapping — the key has nothing under it"},
		{"configs that is a list", svc + "configs: [a]\n", "configs must be a mapping, got a list (line 4)"},
		{"networks that is a list", svc + "networks: [a]\n", "networks must be a mapping, got a list (line 4) — write the declarations as `name: {…}`"},
		{"volumes that is a string", svc + "volumes: x\n", "volumes must be a mapping, got a single value (line 4)"},
		{"secrets that is a number", svc + "secrets: 42\n", "secrets must be a mapping, got a single value"},
		{"networks that is an empty list", svc + "networks: []\n", "networks must be a mapping, got a list"},
		{"networks that is a block list", svc + "networks:\n  - a\n", "networks must be a mapping, got a list (line 4)"},
		{"configs that is a string", svc + "configs: x\n", "configs must be a mapping, got a single value"},
		{"an include", svc + "include:\n  - other.yml\n", "include is not read — the files it names would be left out of the project; pass them with -f instead"},
		{"an include that is not a list", svc + "include: other.yml\n", "include is not read"},
		{"a name through an alias, a number", "x-n: &n 42\n" + svc + "name: *n\n", "name must be a string, got a number (line 1)"},
		{"a name through an alias, nothing", "x-n: &n ~\n" + svc + "name: *n\n", "name must be a string — the key has nothing after it"},
		{"networks through an alias, nothing", "x-n: &n ~\n" + svc + "networks: *n\n", "networks must be a mapping — the key has nothing under it"},
		{"a bad key with keys after it", "name: 42\n" + svc + "version: \"3\"\n", "name must be a string, got a number (line 1)"},
		{"a bad key brought in by a merge key", "x-c: &c {name: 42}\n<<: *c\n" + svc, "name must be a string, got a number"},
		{"a bad key in a file with a reference", "x-a: ${OPOSSUM_T_UNSET_846}\n" + svc + "name: 42\n", "name must be a string, got a number"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
		})
	}
	// Taken: a quoted number is a string; `configs` as a mapping is ignored
	// as before; a version that is a string is read past; an empty or bare
	// `include:` names nothing; an empty declaration mapping is a mapping.
	p, err := Load(writeTemp(t, svc+"name: \"42\"\nversion: \"3.9\"\nconfigs: {c: {file: ./c}}\ninclude: []\nnetworks: {}\nvolumes: {}\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if p.Name != "42" {
		t.Errorf("name = %q, want the quoted text", p.Name)
	}
	if got := strings.Join(p.Unsupported, ","); !strings.Contains(got, "configs") {
		t.Errorf("ignored fields = %q, want configs listed", got)
	}
	// With several -f files a later file's bare key is "not given" and the
	// earlier file's value stands; a wrong shape in a later file is refused
	// naming that file.
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := write("base.yml", svc+"name: proj\nversion: \"3\"\nnetworks:\n  back: {}\n")
	if p, err := LoadFiles([]string{base, write("over.yml", "name:\nnetworks:\ninclude:\n")}, nil); err != nil || p.Name != "proj" {
		t.Errorf("a later file's bare name:/networks: should be not given, got name=%q err=%v", p.Name, err)
	}
	for _, tc := range []struct{ name, over, want string }{
		{"a later name: 42", "name: 42\n", "name must be a string"},
		{"a later version: 3", "version: 3\n", "version must be a string, got a number"},
		{"a later bare version:", "version:\n", "version must be a string — the key has nothing after it"},
		{"a later bare volumes: no earlier file gave", "volumes:\n", "volumes must be a mapping — the key has nothing under it"},
		{"a later include", "include: [x.yml]\n", "include is not read"},
		{"a later networks: [a]", "networks: [a]\n", "networks must be a mapping, got a list (line 1) — write the declarations as"},
	} {
		t.Run(tc.name+" is refused naming that file", func(t *testing.T) {
			_, err := LoadFiles([]string{base, write("bad.yml", tc.over)}, nil)
			if err == nil || !strings.Contains(err.Error(), "bad.yml") || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want bad.yml and %q, got: %v", tc.want, err)
			}
		})
	}
	// The first of several files is checked as a first file.
	if _, err := LoadFiles([]string{write("first-bad.yml", svc+"name: 42\n"), write("second-ok.yml", "services:\n  web:\n    image: busybox\n")}, nil); err == nil || !strings.Contains(err.Error(), "first-bad.yml") {
		t.Errorf("the first file's name: 42 should be refused naming it, got: %v", err)
	}
}
