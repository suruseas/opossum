package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every command that reads the fragments — `make changelog` (sync), preview and
// the release — refuses one that renders differently from how it reads, naming
// the file, before it writes or deletes anything. The bad fragment sorts after a
// clean one, so a check that looks only at the first fragment is caught.
func TestEveryCommandRefusesAFragmentThatRendersWrong(t *testing.T) {
	const before = "# Changelog\n\n## [Unreleased]\n\n## [0.0.1] - 2020-01-01\n\n### Fixed\n\n- Older.\n"
	for _, cmd := range [][]string{{"sync"}, {"preview"}, {"release", "9.9.9", "2026-01-01"}} {
		for _, tc := range []struct{ name, body, want string }{
			{"a placeholder outside a code span", "- run opossum logs <svc>.\n", "<svc> outside a code span"},
			{"an escaped backtick", "- says `a \\`b\\` c`.\n", "\\` outside a code span"},
		} {
			t.Run(cmd[0]+", "+tc.name, func(t *testing.T) {
				t.Chdir(t.TempDir())
				if err := os.MkdirAll(fragmentDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(changelogPath, []byte(before), 0o644); err != nil {
					t.Fatal(err)
				}
				clean := filepath.Join(fragmentDir, "1-clean.fixed.md")
				bad := filepath.Join(fragmentDir, "2-bad.fixed.md")
				// Blank lines above the entry: the reported line is the file's.
				tc.body = "\n\n" + tc.body
				if err := os.WriteFile(clean, []byte("- run `opossum logs <svc>`.\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(bad, []byte(tc.body), 0o644); err != nil {
					t.Fatal(err)
				}
				err := run(cmd)
				if err == nil || !strings.Contains(err.Error(), "2-bad.fixed.md: line 3 has ") || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("want %s refused, naming the fragment and %q, got: %v", cmd[0], tc.want, err)
				}
				if b, _ := os.ReadFile(changelogPath); string(b) != before {
					t.Errorf("a refused %s must not write CHANGELOG.md, got:\n%s", cmd[0], b)
				}
				for _, f := range []string{clean, bad} {
					if _, err := os.Stat(f); err != nil {
						t.Errorf("a refused %s must not consume %s: %v", cmd[0], f, err)
					}
				}
			})
		}
	}
	// The correct spelling of the same entries goes through.
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(fragmentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(changelogPath, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragmentDir, "1-x.fixed.md"), []byte("- run `opossum logs <svc>`, which says `` a `b` c ``.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"sync"}); err != nil {
		t.Fatalf("a fragment written in code spans should sync, got: %v", err)
	}
	if b, _ := os.ReadFile(changelogPath); !strings.Contains(string(b), "`opossum logs <svc>`") {
		t.Errorf("the entry should be in [Unreleased], got:\n%s", b)
	}
}

// `changelog check` takes any Markdown file — the release notes are checked the
// same way as fragments — and says, per file, what renders wrong, with an exit
// code a workflow can stop on.
func TestCheckReportsEachFileAndExitsNonZero(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	clean := write("clean.md", "> [!WARNING]\n> Run `opossum logs <svc>` first.\n\n```sh\nopossum logs <svc>\n```\n")
	tag := write("tag.md", "> [!WARNING]\n> Run opossum logs <svc> first.\n")
	escaped := write("escaped.md", "Intro.\n\nIt says `a \\`b\\` c`.\n")

	var out strings.Builder
	if code := check([]string{clean}, &out); code != 0 || out.String() != "" {
		t.Errorf("a clean file: want exit 0 and no output, got %d and %q", code, out.String())
	}
	out.Reset()
	code := check([]string{tag, clean, escaped}, &out)
	if code != 1 {
		t.Errorf("want exit 1 when a file renders wrong, got %d", code)
	}
	for _, want := range []string{tag + ": line 2 has <svc> outside a code span", escaped + ": line 3 has \\` outside a code span"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("want %q in the report, got:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), clean+":") {
		t.Errorf("the clean file must not be reported, got:\n%s", out.String())
	}
	// The verdict is every file's, not the last one's: a broken file before a
	// clean one still fails the run.
	out.Reset()
	if code := check([]string{tag, clean}, &out); code != 1 {
		t.Errorf("a broken file followed by a clean one: want exit 1, got %d", code)
	}
	// An unreadable file is reported, and the files after it are still checked.
	out.Reset()
	missing := filepath.Join(dir, "missing.md")
	if code := check([]string{missing, tag}, &out); code != 1 || !strings.Contains(out.String(), "missing.md") || !strings.Contains(out.String(), tag+": line 2 has <svc>") {
		t.Errorf("an unreadable file then a broken one: want exit 1 naming both, got %d and %q", code, out.String())
	}
	out.Reset()
	if code := check(nil, &out); code != 2 {
		t.Errorf("no file named: want exit 2, got %d", code)
	}
}
