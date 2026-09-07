package compose

// `build.args` in its list form (#784's leftover from #789's review).
// docker compose (v5.5.0) takes the variables as a mapping or as a list —
// `[A=1, B]`, `[A]`, `[]` load, `[42]` and a single value (`args: foo`)
// are refused (exit codes and full output read). opossum refused every
// list (`build.args must be a mapping, got a list`), and the messages the
// variables decoder gave for a bad item or a single value named
// `environment`, the other field it reads. Across several -f files the two
// forms merge by variable, as docker merges them: a list in one file and a
// mapping in the next keep every variable, the later file winning by name
// (before, a later mapping replaced a list whole, and two lists were
// appended). Alike in both: a duplicate variable in one list is kept twice
// (`--build-arg A=1 --build-arg A=2`; docker keeps the last), a bare
// `NAME` is passed on as written, and the orchestrator gives it the shell's
// value at build time (the builder does not read the shell itself).

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestBuildArgsAreTakenAsAListToo(t *testing.T) {
	build := "services:\n  web:\n    build:\n      context: .\n      args:"
	for _, tc := range []struct {
		name, args string
		want       []string
	}{
		{"a list of assignments", " [A=1, B=two]", []string{"A=1", "B=two"}},
		{"a bare name, taken from the shell", " [A]", []string{"A"}},
		{"an empty list", " []", nil},
		{"a mapping", " {A: 1}", []string{"A=1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeTemp(t, build+tc.args+"\n"))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			got := []string(p.Services["web"].Build.Args)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("args = %v, want %v", got, tc.want)
			}
		})
	}
	for _, tc := range []struct{ name, args, want string }{
		{"a number in the list", " [42]", "build.args entry 1 of 1 must be a string, got a number"},
		{"a single value", " foo", "expected a list or a mapping for build.args, got a single value"},
		{"an empty item", "\n        - A=1\n        -", "build.args entry 2 of 2 is empty"},
		{"bare", "", "build.args must be a mapping or list, got nothing"},
		// A variable that happens to be called `environment`, written
		// twice: the parser's words, with the key as written.
		{"a duplicate variable called environment", " {environment: x, environment: y}", "mapping key \"environment\" already defined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, build+tc.args+"\n")
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
			if strings.Contains(got, "environment") && !strings.Contains(tc.name, "environment") {
				t.Errorf("the message names environment, but the field is build.args:\n%s", got)
			}
			if strings.Contains(tc.name, "environment") && strings.Contains(got, "build.args") {
				t.Errorf("the message names a key that is not in the file:\n%s", got)
			}
		})
	}

	// Across files, by variable — in every pairing of the two forms.
	for _, tc := range []struct {
		name, base, over string
		want             []string
	}{
		{"a mapping over a list", "[A=1, B=base, C]", "{A: 2, D: over}", []string{"A=2", "B=base", "C", "D=over"}},
		{"a list over a mapping", "{A: 1, B: base}", "[A=2, D=over]", []string{"A=2", "B=base", "D=over"}},
		{"a list over a list", "[A=1, B=base, C]", "[A=3, B=over]", []string{"A=3", "B=over", "C"}},
		{"a mapping with an unset variable over a list", "[A=1, B=base]", "{A: }", []string{"A", "B=base"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, args := range map[string]string{"base.yaml": tc.base, "over.yaml": tc.over} {
				body := "services:\n  web:\n    build:\n      context: .\n      args: " + args + "\n"
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			p, err := LoadFiles([]string{filepath.Join(dir, "base.yaml"), filepath.Join(dir, "over.yaml")}, nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			got := append([]string(nil), p.Services["web"].Build.Args...)
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("args = %v, want %v", got, tc.want)
			}
		})
	}
}
