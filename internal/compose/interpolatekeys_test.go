package compose

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// Evals for what a mapping's key is read as.
//
// docker compose expands the values of a mapping and never its keys (measured on
// v5.5.1 with `config`): under `environment:` or `labels:`, `E${SFX}: 1` names a
// variable `E${SFX}`, and a `$$` in a key stays two characters. Expansion here
// runs on the file's text before it is parsed, so a key was expanded with the
// rest, and a file docker reads one way was read as another — same exit code, a
// different name in the container.
//
// The rows read the document the expansion hands on, as the loader does, and
// compare it as data. Each is a pair written on the same line, the key and the
// value both carrying the reference, so a key restored with the value, and a
// value restored as a key, each show up as the one that is wrong.

func keyLookup(name string) (string, bool) {
	switch name {
	case "SFX":
		return "1", true
	case "EMPTY":
		return "", true
	}
	return "", false
}

func expandedAs(t *testing.T, doc string) map[string]any {
	t.Helper()
	d, err := interpolateDocument([]byte(doc), keyLookup)
	if err != nil {
		t.Fatalf("the document did not expand: %v\n%s", err, doc)
	}
	var got map[string]any
	if err := d.into(&got); err != nil {
		t.Fatalf("the document did not read back: %v\n%s", err, doc)
	}
	return got
}

func TestAKeyIsNotExpandedAndItsValueIs(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  string
		want map[string]any
	}{
		// Green in the expansion: the row's key is the only place a reference is
		// written as `${`, `$` and `:-`, and each spelling takes its own path.
		{"a braced reference", "E${SFX}: v${SFX}\n", map[string]any{"E${SFX}": "v1"}},
		{"a bare reference", "E$SFX: v$SFX\n", map[string]any{"E$SFX": "v1"}},
		{"a reference with a default that is not used", "E${SFX:-d}: v${SFX:-d}\n", map[string]any{"E${SFX:-d}": "v1"}},
		{"a reference with a default that is", "E${NOPE:-d}: v${NOPE:-d}\n", map[string]any{"E${NOPE:-d}": "vd"}},
		{"a required reference that is set", "E${SFX:?need}: v${SFX:?need}\n", map[string]any{"E${SFX:?need}": "v1"}},
		// A key that would have expanded to nothing keeps the reference too, and its
		// value is the empty string it always was — not the null a key with nothing
		// after it reads as.
		{"a reference that is not set", "E${NOPE}: v${NOPE}\n", map[string]any{"E${NOPE}": "v"}},
		{"a reference that is empty", "${EMPTY}: ${EMPTY}\n", map[string]any{"${EMPTY}": ""}},
		{"a key that is only a reference", "${SFX}: ${SFX}\n", map[string]any{"${SFX}": "1"}},
		// `$$` is one `$` in a value and two characters in a key.
		{"an escaped dollar", "A$$B: A$$B\n", map[string]any{"A$$B": "A$B"}},
		{"a key of only the escape", "$$: $$\n", map[string]any{"$$": "$"}},
		// Control: a `$` that is not a reference is what it says in both places.
		{"a lone dollar", "\"a$ b\": \"a$ b\"\n", map[string]any{"a$ b": "a$ b"}},
		// Where the pair sits.
		{"one level down", "top:\n  E${SFX}: v${SFX}\n", map[string]any{"top": map[string]any{"E${SFX}": "v1"}}},
		{"in a list of mappings", "top:\n  - E${SFX}: v${SFX}\n  - E$$: v$$\n",
			map[string]any{"top": []any{map[string]any{"E${SFX}": "v1"}, map[string]any{"E$$": "v$"}}}},
		{"in a flow mapping used as a value", "top: {\"E${SFX}\": \"v${SFX}\"}\n",
			map[string]any{"top": map[string]any{"E${SFX}": "v1"}}},
		// A scalar in a sequence sits at an even index too, and is a value: the
		// question is asked of a mapping's content and of nothing else.
		{"the first item of a sequence", "top:\n  - v${SFX}\n  - w${SFX}\n", map[string]any{"top": []any{"v1", "w1"}}},
		// The keys of a mapping merged in from an anchor are the anchor's own.
		{"a merged mapping", "base: &b\n  E${SFX}: v${SFX}\nuse:\n  <<: *b\n  F${SFX}: w\n",
			map[string]any{"base": map[string]any{"E${SFX}": "v1"}, "use": map[string]any{"E${SFX}": "v1", "F${SFX}": "w"}}},
		// A value that is a block: what it holds is text, expanded, and a key that
		// comes after it is still a key.
		{"a block value before a key", "run: |\n  echo $$HOME ${SFX}\nE${SFX}: v\n",
			map[string]any{"run": "echo $HOME 1\n", "E${SFX}": "v"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := expandedAs(t, tc.doc)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("the document\n%s was read as %#v, want %#v", tc.doc, got, tc.want)
			}
		})
	}
}

// The same rule through the loader, for the two places a key becomes a name in a
// container: a variable and a label. A file that spells the name with a reference
// used to get `E1` here and `E${SFX}` from docker compose.
func TestTheLoaderKeepsAReferenceInAnEnvironmentOrLabelKey(t *testing.T) {
	t.Setenv("SFX", "1")
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	body := `services:
  a:
    image: web:latest
    environment:
      E${SFX}: v${SFX}
      A$$B: x
    labels:
      l${SFX}: v${SFX}
`
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	svc := p.Services["a"]
	got := append(append([]string{}, svc.Environment...), svc.Labels...)
	slices.Sort(got)
	want := []string{"A$$B=x", "E${SFX}=v1", "l${SFX}=v1"}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("environment and labels = %q, want %q", got, want)
	}
}
