package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A short mount with an empty first section or an empty last one is refused by docker compose as
// an empty section between colons (#1637). Every spec below was measured against docker compose
// v5.5.1 (`config -q`, with a top-level volume `a`). A bare `:` and a one-letter section, which
// docker compose takes for a Windows drive, are read there and are read here. A middle section of
// one letter is any Unicode letter (`é`, `Ω`, `字`), and one digit or symbol is refused (#1647, #1648).
func TestAShortMountWithAnEmptyFirstOrLastSectionIsRefused(t *testing.T) {
	for _, tc := range []struct {
		spec    string
		refused bool
	}{
		{":/b", true},
		{":/b:ro", true},
		{":/b:rw", true},
		{":ro", true},
		{":/b:", true},
		{"/a:/b:", true},
		{"/a:ro:", true},
		{"a:/b:", true},
		{"/a:1:", true},
		{"/a:_:", true},
		{"/a:.:", true},
		{"/a:-:", true},
		{"/a: :", true},
		{"/a:ab:", true},
		{"/a:éé:", true},
		{"/a:e\u0301:", true},
		{"/a:€:", true},
		{"/a:😀:", true},
		{"/a:٣:", true},
		// One character that is not a letter although it looks like one or like nothing: a
		// combining mark (Mn), a zero-width space (Cf), a Roman numeral (Nl), a ring numeral (Nl).
		{"/a:\u0301:", true},
		{"/a:\u200b:", true},
		{"/a:\ufeff:", true},
		{"/a:Ⅻ:", true},
		{"/a:〇:", true},
		{"/a:²:", true},
		{":é", true},
		{":a:", true},
		{":é:", true},
		{":1:", true},
		{":ab:", true},
		{"a::", true},
		// What is read.
		{":", false},
		{"a:/b", false},
		{"/a:/b", false},
		{"/a:/b:ro", false},
		{"/:/", false},
		{":a", false},
		{"/a:a:", false},
		{"/a:Z:", false},
		{"/a:é:", false},
		{"/a:Ω:", false},
		{"/a:字:", false},
		{"/a:ß:", false},
		{"/a:ａ:", false},
		{"/a:ー:", false},
		{":1", false},
		{":_", false},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			dir := t.TempDir()
			body := "services:\n  web:\n    image: alpine\n    volumes: [\"" + tc.spec + "\"]\nvolumes:\n  a: {}\n"
			p := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(p)
			if tc.refused && (err == nil || !strings.Contains(err.Error(), "volumes entry 1 of 1")) {
				t.Errorf("docker compose refuses %q, and it was read: %v", tc.spec, err)
			}
			if !tc.refused && err != nil {
				t.Errorf("docker compose reads %q, and it was refused: %v", tc.spec, err)
			}
		})
	}
}

// The refusal names the entry as it was written, the service it is in and which side of the
// mount is empty — "before its first colon" or "after its second colon" — in the words a person
// fixes the file by (#1650). The service is not called `web`, the name every other fixture here
// has, so that a message that holds a fixed name instead of the service's own is seen.
func TestTheRefusalNamesTheEntryTheServiceAndTheEmptySide(t *testing.T) {
	const before, after = "has nothing before its first colon", "has nothing after its second colon"
	for _, tc := range []struct{ spec, side string }{
		{":/b", before},
		{":/b:ro", before},
		{":ro", before},
		{":a:", before},
		{"/a:/b:", after},
		{"/a:1:", after},
		{"a:/b:", after},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			_, err := Load(writeTemp(t, "services:\n  cache:\n    image: alpine\n    volumes: [\""+tc.spec+"\"]\n"))
			want := "services.cache.volumes entry 1 of 1: \"" + tc.spec + "\" " + tc.side
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("want a refusal holding %q, got: %v", want, err)
			}
		})
	}
}

// The entry is looked at wherever it is in the list: after a long-form mount (which is not a
// short one and is passed over) and after a good short one, and the message says which it is.
func TestAnEmptyMountSectionIsFoundWhereverTheEntryIs(t *testing.T) {
	for _, tc := range []struct {
		name, volumes, want, entry string
	}{
		{"after a long-form mount", "[{type: volume, target: /x}, \":/b\"]", "volumes entry 2 of 2", ":/b"},
		{"after a good short mount", "[\"/a:/b\", \":/b\"]", "volumes entry 2 of 2", ":/b"},
		{"the last one of three", "[\"/a:/b\", \"/c:/d\", \"/e:/f:\"]", "volumes entry 3 of 3", "/e:/f:"},
		{"the first of two", "[\":/b\", \"/a:/b\"]", "volumes entry 1 of 2", ":/b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "services:\n  api:\n    image: alpine\n    volumes: " + tc.volumes + "\n"
			_, err := Load(writeTemp(t, body))
			want := "services.api." + tc.want + ": \"" + tc.entry + "\""
			if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "docker compose refuses an empty section between colons") {
				t.Errorf("want a refusal holding %q, got: %v", want, err)
			}
		})
	}
}

// docker compose refuses it in the file that writes it: a later file that writes the same mount
// over it, or resets the list, does not make it fine; and it refuses it in a service that is not
// taken as well, `volumes` being read into a type before it is known whether the service is taken.
func TestAnEmptyMountSectionIsRefusedInTheFileThatWritesIt(t *testing.T) {
	write := func(t *testing.T, dir, name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Run("written over by a later file", func(t *testing.T) {
		dir := t.TempDir()
		a := write(t, dir, "a.yaml", "services:\n  web:\n    image: alpine\n    volumes: [\":/b\"]\n")
		b := write(t, dir, "b.yaml", "services:\n  web:\n    volumes: !override [\"/x:/b\"]\n")
		if _, err := LoadFiles([]string{a, b}, nil); err == nil || !strings.Contains(err.Error(), "a.yaml") || !strings.Contains(err.Error(), `":/b" has nothing before its first colon`) {
			t.Errorf("docker compose refuses it in the file that wrote it, and it was read or the refusal does not name a.yaml and the entry: %v", err)
		}
	})
	t.Run("reset by a later file", func(t *testing.T) {
		dir := t.TempDir()
		a := write(t, dir, "a.yaml", "services:\n  web:\n    image: alpine\n    volumes: [\":/b\"]\n")
		b := write(t, dir, "b.yaml", "services:\n  web:\n    volumes: !reset []\n")
		if _, err := LoadFiles([]string{a, b}, nil); err == nil || !strings.Contains(err.Error(), "a.yaml") || !strings.Contains(err.Error(), `":/b" has nothing before its first colon`) {
			t.Errorf("docker compose refuses it in the file that wrote it, and it was read or the refusal does not name a.yaml and the entry: %v", err)
		}
	})
	t.Run("in a service that is not taken", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "base.yaml", "services:\n  ok:\n    image: alpine\n  other:\n    image: alpine\n    volumes: [\":/b\"]\n")
		main := write(t, dir, "compose.yaml", "services:\n  web:\n    extends: {file: base.yaml, service: ok}\n")
		if _, err := Load(main); err == nil || !strings.Contains(err.Error(), "base.yaml") || !strings.Contains(err.Error(), "services.other.volumes entry 1 of 1") {
			t.Errorf("docker compose refuses it in a service that is not taken, and it was read or the refusal does not name base.yaml and the service: %v", err)
		}
	})
}

// An earlier opossum started a project whose file had one (the runtime takes the empty source as
// an anonymous volume), so taking it down warns and goes on rather than refusing the file.
func TestTakingDownAProjectWhoseMountHasAnEmptySectionWarnsAndGoesOn(t *testing.T) {
	for _, spec := range []string{":/b", "/a:/b:", "/a:1:"} {
		t.Run(spec, func(t *testing.T) {
			p := writeTemp(t, "services:\n  web:\n    image: alpine\n    volumes: [\""+spec+"\"]\n")
			proj, err := LoadFilesEnvDirSoft([]string{p}, nil, "")
			if err != nil {
				t.Fatalf("taking down reads the file and goes on, got: %v", err)
			}
			err = proj.CheckValueFaults()
			if err == nil || !strings.Contains(err.Error(), `"`+spec+`"`) {
				t.Errorf("taking down should name %q and go on, got: %v", spec, err)
			}
		})
	}
}
