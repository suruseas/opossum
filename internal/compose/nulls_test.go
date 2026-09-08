package compose

// Keys written with nothing after them that docker compose (v5.5.0)
// refuses and opossum used to read past (#808; exit codes and full output
// read, 26 fixtures). Three shapes: a bare top-level `services:` in any of
// several files (`services must be a mapping` — one file's was already
// refused as "no services"); a key in a long-form item — a mount's
// `source:` or `type:`, a port's `published:`, a dependency's
// `condition:` — which the struct decode read as left out; and, across
// files, a bare key a later file writes that no earlier file gave a value
// to (a new network's `internal:`, a `build.context:` no base has), which
// docker compose refuses naming that file — where the same key over an
// earlier value is "not given" and keeps it.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAKeyWithNothingAfterItInALongFormItemIsRefused(t *testing.T) {
	svc := "services:\n  web:\n    image: alpine\n"
	for _, tc := range []struct{ name, body, want string }{
		{"a mount's source", svc + "    volumes: [{type: bind, source: , target: /x}]\n", "volumes entry 1 of 1: source has nothing after it (line 4) — write the value or remove the key"},
		{"a mount's type", svc + "    volumes: [{type: , source: ., target: /x}]\n", "volumes entry 1 of 1: type has nothing after it"},
		{"the second mount's source", svc + "    volumes: [./a:/a, {type: bind, source: ~, target: /x}]\n", "volumes entry 2 of 2: source has nothing after it"},
		{"a port's published", svc + "    ports: [{target: 80, published: }]\n", "ports entry 1 of 1: published has nothing after it"},
		{"a dependency's condition", svc + "    depends_on: {db: {condition: }}\n  db:\n    image: alpine\n", "depends_on.db: condition has nothing after it"},
		{"through an alias", "x-n: &n ~\n" + svc + "    volumes: [{type: bind, source: *n, target: /x}]\n", "volumes entry 1 of 1: source has nothing after it"},
		// Declarations too: docker compose refuses `networks.back.internal
		// must be a boolean or string`, `volumes.data.name must be a string`.
		{"a network's internal", svc + "networks:\n  back: {internal: }\n", "a network's declaration: internal has nothing after it"},
		{"a network's name", svc + "networks:\n  back: {name: }\n", "a network's declaration: name has nothing after it"},
		{"a volume's name", svc + "volumes:\n  data: {name: }\n", "a volume's declaration: name has nothing after it"},
		{"a secret's file", svc + "secrets:\n  s: {file: }\n", "a secret's declaration: file has nothing after it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
		})
	}
	// Left out is not bare: a mount without `source`, a port without
	// `published`, a dependency without `condition` load as before. (A
	// mount without `type` is refused on its own account — docker compose
	// needs one in the long form; see mounttype_test.go.)
	p, err := Load(writeTemp(t, svc+"    volumes: [{type: volume, target: /x}]\n    ports: [{target: 80}]\n    depends_on: {db: {}}\n  db:\n    image: alpine\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := p.Services["web"].DependsOn[0].Condition; got != ConditionStarted {
		t.Errorf("condition = %q, want the default", got)
	}
}

func TestABareTopLevelServicesIsRefusedInAnyFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("good.yml", "services:\n  web:\n    image: alpine\n")
	bare := write("bare.yml", "services:\nnetworks:\n  front: {}\n")
	for _, tc := range []struct {
		name  string
		files []string
	}{
		{"first", []string{bare, good}},
		{"later", []string{good, bare}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadFiles(tc.files, nil)
			if err == nil || !strings.Contains(err.Error(), "services must be a mapping") || !strings.Contains(err.Error(), "bare.yml") {
				t.Fatalf("want the refusal naming bare.yml, got: %v", err)
			}
		})
	}
}

func TestABareKeyNoEarlierFileGaveAValueToIsRefusedNamingTheLaterFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := write("base.yml", "services:\n  web:\n    image: alpine\nnetworks:\n  back: {internal: true}\n")
	for _, tc := range []struct {
		name, over, want string
		namesALine       bool
	}{
		{"a new network's internal", "networks:\n  other: {internal: }\n", "internal", false},
		{"a build.context no base has", "services:\n  web:\n    build: {context: }\n", "build.context must be a string, got nothing", false},
		{"a ports no base has", "services:\n  web:\n    ports:\n", "ports: expected a list, got nothing", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadFiles([]string{base, write("over.yml", tc.over)}, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "over.yml") {
				t.Fatalf("want the refusal naming over.yml, got: %v", err)
			}
			// The file that wrote the key, not every file: the base has no
			// part in it.
			if strings.Contains(err.Error(), "base.yml") {
				t.Errorf("the refusal should name the later file alone, got: %v", err)
			}
			// Where the decoder names a line, it is one of the merged
			// document, and the message says so (the other two refusals
			// come from checks that name the key and no line).
			if tc.namesALine && !strings.Contains(err.Error(), "counts in the merged document") {
				t.Errorf("a line of the merged document should be said to be one, got: %v", err)
			}
		})
	}
	// A dependency the base listed by name has its condition: a later
	// file's `condition:` with nothing after it keeps it (docker compose
	// reads the list as `{db: {condition: service_started}}` before
	// merging).
	p, err := LoadFiles([]string{write("deps.yml", "services:\n  web:\n    image: alpine\n    depends_on: [db]\n  db:\n    image: alpine\n"), write("cond.yml", "services:\n  web:\n    depends_on: {db: {condition: }}\n")}, nil)
	if err != nil {
		t.Fatalf("a bare condition over a listed dependency keeps service_started: %v", err)
	}
	if got := p.Services["web"].DependsOn[0].Condition; got != ConditionStarted {
		t.Errorf("condition = %q, want %q", got, ConditionStarted)
	}
	// The same key over an earlier value is "not given": the value stands.
	p, err = LoadFiles([]string{base, write("keep.yml", "networks:\n  back: {internal: }\n")}, nil)
	if err != nil {
		t.Fatalf("a bare field over an earlier value keeps it: %v", err)
	}
	if !p.Networks["back"].Internal {
		t.Errorf("internal = false, want the base file's true")
	}
}
