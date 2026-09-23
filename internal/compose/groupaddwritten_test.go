package compose

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The spellings `group_add` was written in are kept beside the loader's
// reading of them, entry by entry and in the order written, so a refusal can
// name what the reader wrote (`0x10`, not the group 16 it reads as). A file
// read with a second one arrives here already rewritten, so the spelling
// recorded there is the merge's, as the reading is.
func TestTheGroupAddSpellingsAreKeptBesideTheReading(t *testing.T) {
	for _, tc := range []struct {
		name, body    string
		read, written []string
	}{
		{"a number", "    group_add: [0x10]\n", []string{"16"}, []string{"0x10"}},
		{"two entries", "    group_add: [0o20, 16]\n", []string{"16", "16"}, []string{"0o20", "16"}},
		{"a quoted entry", "    group_add: [\"0002000\"]\n", []string{"0002000"}, []string{"0002000"}},
		{"a name", "    group_add: [wheel]\n", []string{"wheel"}, []string{"wheel"}},
		{"a zero with a minus", "    group_add: [-00]\n", []string{"-0"}, []string{"-00"}},
		{"through an alias", "    group_add: [*g]\n", []string{"16"}, []string{"0x10"}},
		{"through a list alias", "    group_add: *gl\n", []string{"16"}, []string{"0x10"}},
		{"no group_add", "    user: \"1000\"\n", nil, nil},
		{"an empty list", "    group_add: []\n", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "compose.yaml")
			body := "x-g: &g 0x10\nx-gl: &gl [0x10]\nservices:\n  app:\n    image: alpine:3.20\n" + tc.body
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := LoadFiles([]string{path}, nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			svc := p.Services["app"]
			if got := []string(svc.GroupAdd); !slices.Equal(got, tc.read) {
				t.Errorf("group_add = %q, want %q", got, tc.read)
			}
			if !slices.Equal(svc.GroupAddWritten, tc.written) {
				t.Errorf("spellings = %q, want %q", svc.GroupAddWritten, tc.written)
			}
			// Nothing written, nothing recorded: an empty list keeps no
			// spellings at all (`slices.Equal` reads an empty slice and nil
			// alike, so the length is asked for by name).
			if tc.written == nil && len(svc.GroupAddWritten) != 0 {
				t.Errorf("spellings = %q, want none", svc.GroupAddWritten)
			}
		})
	}
}

// Read with a second file, the spelling recorded is the one that reaches the
// decoder — the merge writes the number back out in decimal, as it did
// before the spellings were kept.
func TestTheSpellingsRecordedWithASecondFileAreTheMergedOnes(t *testing.T) {
	dir := t.TempDir()
	one := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(one, []byte("services:\n  app:\n    image: alpine:3.20\n    group_add: [0x10]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "over.yaml")
	if err := os.WriteFile(other, []byte("services:\n  app:\n    environment: {Z: z}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadFiles([]string{one, other}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := p.Services["app"].GroupAddWritten; !slices.Equal(got, []string{"16"}) {
		t.Errorf("spellings = %q, want [16]", got)
	}
}

// Anything that merges the file before it is read settles the spelling as it
// settles the reading: resolving an `extends`, or taking an `include:` in,
// writes the tree back out, and the number comes back in decimal. That
// reaches every service in the file, the one that wrote the entry included —
// `extends:` on another service, or an `include:` of a file that says nothing
// about it, is enough (measured 2026-09-20).
func TestTheSpellingsAreRecordedThroughExtendsAndInclude(t *testing.T) {
	for _, tc := range []struct {
		name, base, top string
		// svc is the service the row reads, which is not always the one that
		// extends: a row may look at the service that wrote the entry.
		svc           string
		read, written []string
	}{
		{
			"extends from another file",
			"services:\n  base:\n    image: alpine:3.20\n    group_add: [0x10]\n",
			"services:\n  app:\n    extends: {file: base.yaml, service: base}\n",
			"app",
			[]string{"16"}, []string{"16"},
		},
		{
			"include",
			"services:\n  app:\n    image: alpine:3.20\n    group_add: [0x10]\n",
			"include:\n  - base.yaml\n",
			"app",
			[]string{"16"}, []string{"16"},
		},
		{
			"extends within one file",
			"",
			"services:\n  base:\n    image: alpine:3.20\n    group_add: [0x10]\n  app:\n    extends: {service: base}\n",
			"app",
			[]string{"16"}, []string{"16"},
		},
		// The service that wrote the entry, in a file another service
		// extends: its spelling is settled too.
		{
			// The one that extends writes its own entry, so the two
			// services read different groups: this row is the service that
			// wrote the entry the other extends.
			"the service extended, read by its own name",
			"",
			"services:\n  base:\n    image: alpine:3.20\n    group_add: [0x10]\n  app:\n    extends: {service: base}\n    group_add: [0o21]\n",
			"base",
			[]string{"16"}, []string{"16"},
		},
		// Nothing to merge in: an `include:` with no file under it, and an
		// `extends:` that is not on a service, leave the spelling alone.
		{
			"an include with nothing under it",
			"",
			"include:\nservices:\n  app:\n    image: alpine:3.20\n    group_add: [0x10]\n",
			"app",
			[]string{"16"}, []string{"0x10"},
		},
		{
			"an extends outside services",
			"",
			"x-t: {q: {extends: nope}}\nservices:\n  app:\n    image: alpine:3.20\n    group_add: [0x10]\n",
			"app",
			[]string{"16"}, []string{"0x10"},
		},
		// An `include:` of a file that says nothing about this service
		// settles it as well.
		{
			"beside an include of another file",
			"services:\n  other:\n    image: alpine:3.20\n",
			"include:\n  - base.yaml\nservices:\n  app:\n    image: alpine:3.20\n    group_add: [0x10]\n",
			"app",
			[]string{"16"}, []string{"16"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.base != "" {
				if err := os.WriteFile(filepath.Join(dir, "base.yaml"), []byte(tc.base), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			top := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(top, []byte(tc.top), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := LoadFiles([]string{top}, nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			svc := p.Services[tc.svc]
			if got := []string(svc.GroupAdd); !slices.Equal(got, tc.read) {
				t.Errorf("group_add = %q, want %q", got, tc.read)
			}
			if !slices.Equal(svc.GroupAddWritten, tc.written) {
				t.Errorf("spellings = %q, want %q", svc.GroupAddWritten, tc.written)
			}
		})
	}
}

// Two entries that read as one group only once the file is merged are
// refused as the same entry twice, where docker compose takes them (v5.5.1,
// `config` exits 0; measured 2026-09-20). The refusal is the repeat check's:
// the merge settles `0x10` to `16`, which the second entry already spells.
func TestEntriesThatReadAlikeOnlyAfterAMergeAreRefusedAsARepeat(t *testing.T) {
	dir := t.TempDir()
	one := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(one, []byte("services:\n  app:\n    image: alpine:3.20\n    group_add: [0x10, \"16\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "over.yaml")
	if err := os.WriteFile(other, []byte("services:\n  app:\n    environment: {Z: z}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Read alone the two are two spellings, and the file loads.
	if _, err := LoadFiles([]string{one}, nil); err != nil {
		t.Fatalf("read alone, the file should load: %v", err)
	}
	_, err := LoadFiles([]string{one, other}, nil)
	if err == nil || !strings.Contains(err.Error(), "group_add items at 0 and 1 are equal") {
		t.Fatalf("want the repeat refused after the merge, got %v", err)
	}
}
