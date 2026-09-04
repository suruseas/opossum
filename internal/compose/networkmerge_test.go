package compose

import (
	"path/filepath"
	"slices"
	"testing"
)

// A service's `networks:` has two spellings, and two `-f` files may use one
// each. docker compose (v5.4.0, measured) reads the list form as a map with
// empty entries before merging, so an override that lists `[back]` over a base
// that wrote `back: {aliases: [...]}` keeps the aliases, and a name both files
// carry is one network. Before this, a list on top of a map replaced it — the
// aliases vanished at merge time, so not even the per-field report saw them.
func TestNetworksMergeAcrossListAndMapForms(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n"+
		"  web:\n"+
		"    image: nginx\n"+
		"    networks:\n"+
		"      back:\n"+
		"        aliases: [db-alias]\n"+
		"networks:\n"+
		"  back: {}\n")
	mustWriteFile(t, over, "services:\n"+
		"  web:\n"+
		"    networks: [back, front]\n"+
		"networks:\n"+
		"  front: {}\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	web := p.Services["web"]
	if got := []string(web.Networks); !slices.Equal(got, []string{"back", "front"}) {
		t.Fatalf("the merged service should join both networks once each, got %v", got)
	}
	// The map's entry survived the merge, so the reader who wrote aliases hears
	// (as before, per field) that opossum does not act on them — rather than
	// nothing, which read as "applied".
	if !slices.Contains(web.Unsupported, "networks.back.aliases") {
		t.Errorf("the base file's aliases should still be reported after the override's list form, got %v", web.Unsupported)
	}
}

// The other way round — a map form over a list form — carries the map's
// entries too, and a name only the list had stays joined.
func TestNetworksMapOverListKeepsBothSides(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n"+
		"  web:\n"+
		"    image: nginx\n"+
		"    networks: [front, back]\n"+
		"networks:\n"+
		"  back: {}\n"+
		"  front: {}\n")
	mustWriteFile(t, over, "services:\n"+
		"  web:\n"+
		"    networks:\n"+
		"      back:\n"+
		"        ipv4_address: 10.0.0.9\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	web := p.Services["web"]
	if got := []string(web.Networks); !slices.Equal(got, []string{"back", "front"}) {
		t.Fatalf("front (list only) and back (both) should both be joined, got %v", got)
	}
	if !slices.Contains(web.Unsupported, "networks.back.ipv4_address") {
		t.Errorf("the override's map entry should be reported per field, got %v", web.Unsupported)
	}
}

// Two list forms that name the same network are one network, as docker
// compose reads them — not the name twice (which became two --network flags).
func TestNetworksListedTwiceIsJoinedOnce(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n"+
		"  web:\n"+
		"    image: nginx\n"+
		"    networks: [back]\n"+
		"networks:\n"+
		"  back: {}\n")
	mustWriteFile(t, over, "services:\n"+
		"  web:\n"+
		"    networks: [back]\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if got := []string(p.Services["web"].Networks); !slices.Equal(got, []string{"back"}) {
		t.Fatalf("a network both files list should be joined once, got %v", got)
	}
}

// The map form can name a network with nothing under it (`back:`), which
// docker compose reads like the list form: the name, and no change to what
// another file set for it. Before, that empty entry replaced the base's map —
// the aliases went the same way as with the list form.
func TestNetworksEmptyMapEntryKeepsTheOtherFilesSettings(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n"+
		"  web:\n"+
		"    image: nginx\n"+
		"    networks:\n"+
		"      back:\n"+
		"        aliases: [db-alias]\n"+
		"networks:\n"+
		"  back: {}\n")
	mustWriteFile(t, over, "services:\n"+
		"  web:\n"+
		"    networks:\n"+
		"      back:\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	web := p.Services["web"]
	if got := []string(web.Networks); !slices.Equal(got, []string{"back"}) {
		t.Fatalf("got %v", got)
	}
	if !slices.Contains(web.Unsupported, "networks.back.aliases") {
		t.Errorf("an empty map entry must not erase the base file's aliases, got %v", web.Unsupported)
	}
}

// Two map forms that both write settings under one name keep both sides'
// settings (docker compose merges the entry field by field): the base's
// aliases and the override's address are both still there to report.
func TestNetworksMapEntriesMergeFieldByField(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n"+
		"  web:\n"+
		"    image: nginx\n"+
		"    networks:\n"+
		"      back:\n"+
		"        aliases: [db-alias]\n"+
		"networks:\n"+
		"  back: {}\n")
	mustWriteFile(t, over, "services:\n"+
		"  web:\n"+
		"    networks:\n"+
		"      back:\n"+
		"        ipv4_address: 10.0.0.9\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	web := p.Services["web"]
	for _, want := range []string{"networks.back.aliases", "networks.back.ipv4_address"} {
		if !slices.Contains(web.Unsupported, want) {
			t.Errorf("both files' settings for back should survive the merge, missing %s in %v", want, web.Unsupported)
		}
	}
}

// An empty list (`networks: []`) in the override names nothing, and so changes
// nothing: the base's networks and their settings stand, as docker compose
// reads it. (Before, the empty list replaced the map and the service left its
// networks.)
func TestNetworksEmptyListLeavesTheOtherFileAlone(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n"+
		"  web:\n"+
		"    image: nginx\n"+
		"    networks:\n"+
		"      back:\n"+
		"        aliases: [db-alias]\n"+
		"networks:\n"+
		"  back: {}\n")
	mustWriteFile(t, over, "services:\n"+
		"  web:\n"+
		"    networks: []\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	web := p.Services["web"]
	if got := []string(web.Networks); !slices.Equal(got, []string{"back"}) {
		t.Fatalf("an empty override list should leave the base's networks in place, got %v", got)
	}
	if !slices.Contains(web.Unsupported, "networks.back.aliases") {
		t.Errorf("the base's aliases should still be reported, got %v", web.Unsupported)
	}
}

// The top-level `networks:` declarations go the same road: an override that
// mentions a network with nothing under it (`back:`) leaves the base's
// declaration — what opossum acts on (internal) and what it reports (driver)
// alike — as docker compose does.
func TestTopLevelNetworkDeclarationSurvivesAnEmptyOverrideEntry(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n"+
		"  web:\n"+
		"    image: nginx\n"+
		"    networks: [back]\n"+
		"networks:\n"+
		"  back:\n"+
		"    driver: bridge\n"+
		"    internal: true\n")
	mustWriteFile(t, over, "networks:\n"+
		"  back:\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if !p.Networks["back"].Internal {
		t.Errorf("internal: true from the base declaration should survive an empty override entry, got %+v", p.Networks["back"])
	}
	if !slices.Contains(p.Unsupported, "networks.back.driver") {
		t.Errorf("the base declaration's driver should still be reported, got %v", p.Unsupported)
	}
}

// Both files writing aliases for the same network: the lists append (docker
// compose concatenates them), so neither file's aliases are lost from the
// report. A reader who believes "the override's entry wins whole" would drop
// the base's list here.
func TestNetworksAliasesFromBothFilesAreBothKept(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n"+
		"  web:\n"+
		"    image: nginx\n"+
		"    networks:\n"+
		"      back:\n"+
		"        aliases: [a-alias]\n"+
		"networks:\n"+
		"  back: {}\n")
	mustWriteFile(t, over, "services:\n"+
		"  web:\n"+
		"    networks:\n"+
		"      back:\n"+
		"        aliases: [b-alias]\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	web := p.Services["web"]
	if got := []string(web.Networks); !slices.Equal(got, []string{"back"}) {
		t.Fatalf("got %v", got)
	}
	if !slices.Contains(web.Unsupported, "networks.back.aliases") {
		t.Errorf("aliases should still be reported, got %v", web.Unsupported)
	}
}

// An explicit empty map (`back: {}`) is "the name and nothing more" too — not
// the same shape as the list's null entry, but the same meaning, and docker
// compose keeps the other file's aliases for it.
func TestNetworksEmptyMapValueKeepsTheOtherFilesSettings(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n"+
		"  web:\n"+
		"    image: nginx\n"+
		"    networks:\n"+
		"      back:\n"+
		"        aliases: [db-alias]\n"+
		"networks:\n"+
		"  back: {}\n")
	mustWriteFile(t, over, "services:\n"+
		"  web:\n"+
		"    networks:\n"+
		"      back: {}\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if !slices.Contains(p.Services["web"].Unsupported, "networks.back.aliases") {
		t.Errorf("an empty map value must not erase the base file's aliases, got %v", p.Services["web"].Unsupported)
	}
}
