package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A value with a line break in it used to break the whole file: expansion runs
// on the raw text, so the value's second line landed in the document as
// structure and the parse failed — while docker, which expands after parsing,
// loads the same project fine (measured on Docker Compose v5.4.0: the value
// comes through as a block scalar). Now the expansion writes a one-line marker
// and the repair pass puts the value back into the parsed tree, so downstream
// sees it whole — line breaks and all — the way a post-parse expansion would
// have handed it over (#411).
func TestAMultiLineValueLoadsLikeDockerLoadsIt(t *testing.T) {
	unsetHostVars(t, "PEM")
	pem := "line1\nline2\nline3"
	p := writeProject(t, `
services:
  app:
    image: app:1
    environment:
      KEY: ${PEM}
      BOTH: pre-${PEM}-post
`, "PEM=\"line1\nline2\nline3\"\n")
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("a multi-line value must not break the load: %v", err)
	}
	env := proj.Services["app"].Environment
	found := map[string]bool{}
	for _, e := range env {
		switch e {
		case "KEY=" + pem:
			found["KEY"] = true
		case "BOTH=pre-" + pem + "-post":
			found["BOTH"] = true
		}
	}
	// Both shapes: the value alone, and the value embedded in a longer scalar.
	for _, k := range []string{"KEY", "BOTH"} {
		if !found[k] {
			t.Errorf("%s should carry the value whole, got environment: %q", k, env)
		}
	}
}

// A carriage return is a line break to YAML too, and rides the same marker.
func TestACarriageReturnValueLoads(t *testing.T) {
	unsetHostVars(t, "CRV")
	p := writeProject(t, `
services:
  app:
    image: app:1
    environment:
      K: ${CRV}
`, "CRV=\"a\rb\"\n")
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := proj.Services["app"].Environment; len(got) != 1 || got[0] != "K=a\rb" {
		t.Errorf("the CR should survive into the value, got %q", got)
	}
}

// The marker character arriving already written is refused, the same way the
// empty-mark is: a marker that can be forged is an index into someone else's
// table. Both roads — the file itself, and a variable's value.
func TestTheHeldMarkIsRefusedWhereverItArrives(t *testing.T) {
	unsetHostVars(t, "EVIL")
	inFile := writeProject(t, "services:\n  a:\n    image: \uE001x\n", "")
	if _, err := Load(inFile); err == nil || !strings.Contains(err.Error(), "U+E001") {
		t.Errorf("a file carrying U+E001 should be refused by name, got: %v", err)
	}
	inVar := writeProject(t, `
services:
  a:
    image: ${EVIL}
`, "EVIL=\"x\uE001y\"\n")
	if _, err := Load(inVar); err == nil || !strings.Contains(err.Error(), "${EVIL}") {
		t.Errorf("a value carrying U+E001 should be refused naming the reference, got: %v", err)
	}
}

// A file whose own YAML is broken still fails as itself: the multi-line value
// elsewhere must not change whose mistake the error names.
func TestAMultiLineValueDoesNotMaskASyntaxError(t *testing.T) {
	unsetHostVars(t, "PEM")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("PEM=\"l1\nl2\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(bad, []byte("services:\n  a:\n    image: ${PEM}\n   badindent: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), "YAML") {
		t.Errorf("the file's own syntax error should surface, got: %v", err)
	}
}

// Two DIFFERENT multi-line values, apart and side by side in one scalar. The
// first version of this file referenced one value twice, so vals[0] and
// vals[1] were equal and the index wiring — the mechanism's heart — was
// unguarded: a writeVal that always writes index 0, and a restore that
// replaces only the first marker in a scalar, both stayed green (this PR's
// independent review ran exactly those two mutations). One exemplar was
// serving two rules. The expected strings are docker's answers, measured on
// Docker Compose v5.4.0.
func TestTwoDifferentMultiLineValuesKeepTheirIndexes(t *testing.T) {
	unsetHostVars(t, "A", "B")
	p := writeProject(t, `
services:
  app:
    image: app:1
    environment:
      K1: ${A}
      K2: ${B}
      K3: ${A}-${B}
`, "A=\"x\ny\"\nB=\"p\nq\"\n")
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]bool{
		"K1=x\ny":      false,
		"K2=p\nq":      false,
		"K3=x\ny-p\nq": false,
	}
	for _, e := range proj.Services["app"].Environment {
		if _, ok := want[e]; ok {
			want[e] = true
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("missing %q — the wrong value answered for an index; got %q", k, proj.Services["app"].Environment)
		}
	}
}
