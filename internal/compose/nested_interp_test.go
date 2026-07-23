package compose

// Evals for #290: nested `${…}` (a reference inside a default) and YAML
// double-quoted line continuations inside a reference — both used by real composes
// (rocketchat's mongodb replica-set URL) and both supported by docker compose.

import (
	"strings"
	"testing"
)

func TestInterpolateNested(t *testing.T) {
	env := lk(map[string]string{"SET": "yes", "PORT": "5432"})
	cases := []struct{ in, want string }{
		// Outer unset -> its default is used, and the nested ref inside it resolves.
		{"${OUTER:-pre-${INNER:-mid}-post}", "pre-mid-post"},
		// Inner set wins inside the outer's default.
		{"${OUTER:-host:${PORT:-9999}}", "host:5432"},
		// Outer set -> default (and its nested ref) is ignored entirely.
		{"${SET:-${MISSING:?should not be evaluated}}", "yes"},
		// Three levels deep, all unset -> innermost default.
		{"${A:-${B:-${C:-deep}}}", "deep"},
		// Nested colon-less default.
		{"${A-${B-x}}", "x"},
		// A nested ref plus trailing text, all in one default.
		{"url=${U:-mongodb://${H:-localhost}:${P:-27017}/db}", "url=mongodb://localhost:27017/db"},
		// A default is fully interpolated (matching docker compose): `$$` escapes to
		// `$`, and a braceless `$VAR` in the default expands too.
		{"${U:-a$$b}", "a$b"},
		{"${U:-host=${HOST:-x}:$PORT}", "host=x:5432"},
	}
	for _, c := range cases {
		got, err := interpolate([]byte(c.in), env)
		if err != nil {
			t.Errorf("interpolate(%q) error: %v", c.in, err)
			continue
		}
		if string(got) != c.want {
			t.Errorf("interpolate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestInterpolateLineContinuation(t *testing.T) {
	// A reference written across lines with YAML double-quoted `\` continuations
	// (backslash + newline + indent), as rocketchat writes its mongo URL.
	in := "URL: \"${MONGO_URL:\\\n        -mongodb://${HOST:-mongodb}:${PORT:-27017}/\\\n        local}\""
	got, err := interpolate([]byte(in), lk(nil))
	if err != nil {
		t.Fatalf("interpolate with line continuation errored: %v", err)
	}
	want := `URL: "mongodb://mongodb:27017/local"`
	if string(got) != want {
		t.Errorf("line-continuation interpolate = %q, want %q", got, want)
	}
}

// A literal backslash that is NOT a line continuation must survive untouched.
func TestInterpolateLiteralBackslash(t *testing.T) {
	got, err := interpolate([]byte(`p: ${WIN:-C:\Program Files}`), lk(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `C:\Program Files`) {
		t.Errorf("a literal backslash should be preserved, got: %q", got)
	}
}
