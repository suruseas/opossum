package compose

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Deep (non-env) nested mappings merge recursively: `build.args` from two files
// combine, and a key only the base sets (build.context) is preserved. This
// exercises mergeMap's map-in-map recursion, distinct from the env special case.
func TestLoadFilesMergeNestedMapping(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n"+
		"  web:\n"+
		"    build:\n"+
		"      context: .\n"+
		"      args: {A: \"1\", B: \"2\"}\n")
	mustWriteFile(t, over, "services:\n"+
		"  web:\n"+
		"    build:\n"+
		"      args: {B: \"20\", C: \"3\"}\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	b := p.Services["web"].Build
	if b == nil || b.Context != "." { // base-only nested key survives the merge
		t.Fatalf("build.context should be preserved, got %+v", b)
	}
	args := strings.Join(b.Args, ",") // nested map merged per key (later wins on B)
	for _, want := range []string{"A=1", "B=20", "C=3"} {
		if !strings.Contains(args, want) {
			t.Errorf("build.args should merge per key, missing %q in %q", want, args)
		}
	}
}

// Multiple -f files merge with docker compose semantics: scalars later-win,
// mappings merge by key, most sequences append, command/entrypoint replace, and a
// service only in the override is added.
func TestLoadFilesMerge(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n"+
		"  web:\n"+
		"    image: web:1\n"+
		"    ports: [\"8080:80\"]\n"+
		"    environment: {A: \"1\", B: \"2\"}\n"+
		"    command: [\"run\", \"base\"]\n")
	mustWriteFile(t, over, "services:\n"+
		"  web:\n"+
		"    image: web:2\n"+
		"    ports: [\"9090:90\"]\n"+
		"    environment: {B: \"20\", C: \"3\"}\n"+
		"    command: [\"run\", \"over\"]\n"+
		"  cache:\n"+
		"    image: cache:1\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	web := p.Services["web"]
	if web.Image != "web:2" { // scalar: later wins
		t.Errorf("image = %q, want web:2", web.Image)
	}
	if len(web.Ports) != 2 { // sequence: appended
		t.Errorf("ports should append (both), got %v", web.Ports)
	}
	if got := strings.Join(web.Command, " "); got != "run over" { // command: replaced
		t.Errorf("command should be replaced, got %q", got)
	}
	env := strings.Join(web.Environment, ",") // map: merged per key (sorted A,B,C)
	for _, want := range []string{"A=1", "B=20", "C=3"} {
		if !strings.Contains(env, want) {
			t.Errorf("environment should merge per key, missing %q in %q", want, env)
		}
	}
	if _, ok := p.Services["cache"]; !ok {
		t.Error("a service only in the override should be added")
	}
}

// environment merges by key across mixed forms (base map + override list): no base
// key is lost, and duplicate list keys collapse (docker compose parity).
func TestLoadFilesMergeEnvMixedForm(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n  web:\n    image: w\n    environment: {A: \"1\", B: \"2\"}\n")
	mustWriteFile(t, over, "services:\n  web:\n    image: w\n    environment:\n      - B=20\n      - C=3\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	// Environment is a sorted KEY=value slice.
	want := []string{"A=1", "B=20", "C=3"}
	got := []string(p.Services["web"].Environment)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("mixed-form env merge = %v, want %v (no key loss, later wins)", got, want)
	}
}

// A port restated identically in the override collapses to one entry.
// Volumes are deduped only at merge time (unlike ports, which are re-deduped
// during load), so this is the sole guard against an override restating a mount
// producing a doubled -v.
func TestLoadFilesDedupsVolumes(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n  web:\n    image: w\n    volumes: [\"data:/data\"]\n")
	mustWriteFile(t, over, "services:\n  web:\n    image: w\n    volumes: [\"data:/data\", \"logs:/logs\"]\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if vols := p.Services["web"].Volumes; len(vols) != 2 { // data:/data (deduped) + logs:/logs
		t.Errorf("an override restating a volume should dedup, got %v", vols)
	}
}

// docker compose merges volumes by MOUNT POINT: a later file mounting a path an
// earlier file already mounts replaces it, rather than adding a second mount at
// the same path. Verified against docker compose (v5.1.4), which renders exactly one
// mount for this input. Without it, an override can't swap a bind mount for a
// named volume — the container would get both sources at one path.
func TestLoadFilesVolumesMergeByTarget(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n  db:\n    image: postgres\n    volumes: [\"./data:/var/lib/postgresql/data\", \"./cfg:/etc/cfg\"]\n")
	mustWriteFile(t, over, "services:\n  db:\n    image: postgres\n    volumes: [\"dbdata:/var/lib/postgresql/data\"]\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	vols := p.Services["db"].Volumes
	// The data dir is mounted exactly once, by the override's named volume; the
	// unrelated mount survives, and the swap keeps the base's position.
	want := []string{"dbdata:/var/lib/postgresql/data", "./cfg:/etc/cfg"}
	if strings.Join(vols, ",") != strings.Join(want, ",") {
		t.Errorf("volumes merge by target = %v, want %v", vols, want)
	}
}

// The same rule applies to the long mapping form and to a `:ro` mode suffix —
// the target is what identifies the mount, not the entry's spelling.
func TestLoadFilesVolumesMergeByTargetForms(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	// Long form in the base, short form (with a mode) in the override.
	mustWriteFile(t, base, "services:\n  web:\n    image: w\n    volumes:\n      - type: bind\n        source: ./a\n        target: /data\n")
	mustWriteFile(t, over, "services:\n  web:\n    image: w\n    volumes: [\"vol:/data:ro\"]\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if vols := p.Services["web"].Volumes; len(vols) != 1 || vols[0] != "vol:/data:ro" {
		t.Errorf("a short-form override should replace a long-form mount at the same target, got %v", vols)
	}
}

// A mount at a path nobody else mounts is kept — merging by target must not
// collapse unrelated mounts.
func TestLoadFilesVolumesDistinctTargetsKept(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n  web:\n    image: w\n    volumes: [\"a:/one\"]\n")
	mustWriteFile(t, over, "services:\n  web:\n    image: w\n    volumes: [\"b:/two\", \"/anon\"]\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	// Assert the contents, not just the count: a count survives a bug that stops
	// keying an entry (it's appended unmerged, so the total is unchanged).
	want := []string{"a:/one", "b:/two", "/anon"}
	if vols := p.Services["web"].Volumes; strings.Join(vols, ",") != strings.Join(want, ",") {
		t.Errorf("distinct targets should all survive in order = %v, want %v", vols, want)
	}
}

// An anonymous volume ("/data", no source) names its target directly, so a later
// file mounting something at that path replaces it. docker compose does the same.
func TestLoadFilesVolumesAnonymousReplacedByTarget(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n  web:\n    image: w\n    volumes: [\"/data\"]\n")
	mustWriteFile(t, over, "services:\n  web:\n    image: w\n    volumes: [\"./a:/data\"]\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if vols := p.Services["web"].Volumes; len(vols) != 1 || vols[0] != "./a:/data" {
		t.Errorf("a named mount should replace an anonymous volume at the same target, got %v", vols)
	}
}

// A trailing slash doesn't make it a different mount point ("/data/" is "/data").
// Confirmed against docker compose, which collapses these too.
func TestLoadFilesVolumesTrailingSlashTarget(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n  web:\n    image: w\n    volumes: [\"./a:/data/\"]\n")
	mustWriteFile(t, over, "services:\n  web:\n    image: w\n    volumes: [\"./b:/data\"]\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if vols := p.Services["web"].Volumes; len(vols) != 1 || vols[0] != "./b:/data" {
		t.Errorf("a trailing slash should not make a distinct mount point, got %v", vols)
	}
}

// The collapse must also apply within a SINGLE file: the merge only runs when
// files are combined, but docker compose collapses duplicate targets regardless
// of how many files were given. Same for a merge where the override never
// restates `volumes` (that key is then copied through, never merged).
func TestLoadVolumesCollapseWithoutMerge(t *testing.T) {
	dir := t.TempDir()

	// One file, two mounts at the same target: the last wins.
	single := filepath.Join(dir, "single.yml")
	mustWriteFile(t, single, "services:\n  db:\n    image: p\n    volumes: [\"./a:/data\", \"./b:/data\"]\n")
	p, err := Load(single)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if vols := p.Services["db"].Volumes; len(vols) != 1 || vols[0] != "./b:/data" {
		t.Errorf("a single file should collapse duplicate targets, got %v", vols)
	}

	// Two files, but the override doesn't touch `volumes` — the base's list is
	// copied through without ever reaching the merge.
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n  db:\n    image: p\n    volumes: [\"./a:/data\", \"./b:/data\"]\n")
	mustWriteFile(t, over, "services:\n  db:\n    image: p\n    environment:\n      X: \"1\"\n")
	p2, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if vols := p2.Services["db"].Volumes; len(vols) != 1 || vols[0] != "./b:/data" {
		t.Errorf("an untouched volumes list should still collapse, got %v", vols)
	}
}

// entrypoint replaces (not appends) across files — it's a single value, not a
// list to accumulate. Previously only `command` replacement was tested.
func TestLoadFilesReplacesEntrypoint(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n  web:\n    image: w\n    entrypoint: [\"/base\", \"--old\"]\n")
	mustWriteFile(t, over, "services:\n  web:\n    image: w\n    entrypoint: [\"/override\"]\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if ep := []string(p.Services["web"].Entrypoint); len(ep) != 1 || ep[0] != "/override" {
		t.Errorf("entrypoint should be replaced by the override, got %v", ep)
	}
}

func TestLoadFilesDedupsPorts(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	mustWriteFile(t, base, "services:\n  web:\n    image: w\n    ports: [\"8080:80\"]\n")
	mustWriteFile(t, over, "services:\n  web:\n    image: w\n    ports: [\"8080:80\", \"9090:90\"]\n")

	p, err := LoadFiles([]string{base, over}, nil)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	ports := p.Services["web"].Ports
	if len(ports) != 2 { // 8080:80 (deduped) + 9090:90
		t.Errorf("identical ports should dedup, got %v", ports)
	}
}

func TestDiscoverOverride(t *testing.T) {
	if got := DiscoverOverride(t.TempDir()); got != "" {
		t.Errorf("no override present, got %q", got)
	}
	// Each recognized override filename is auto-discovered (each in a fresh dir so
	// they don't shadow one another).
	for _, name := range []string{
		"compose.override.yaml",
		"compose.override.yml",
		"docker-compose.override.yaml",
		"docker-compose.override.yml",
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, name)
		mustWriteFile(t, path, "services: {}\n")
		if got := DiscoverOverride(dir); got != path {
			t.Errorf("should discover %s, got %q", name, got)
		}
	}
}

// A volume declaration's `external:`/`name:` are acted on, so `volumes` itself is
// not an ignored key — but `driver`/`driver_opts`/`labels` ARE dropped, and a
// project asking for (say) an NFS driver must be told it won't get one.
func TestIgnoredVolumeDeclarationFields(t *testing.T) {
	p := writeTemp(t, `
name: demo
volumes:
  nfsdata:
    driver: local
    driver_opts:
      type: nfs
      device: ":/exports"
  plain:
    external: true
    name: real-name
services:
  web:
    image: web
`)
	proj, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := strings.Join(proj.Unsupported, ",")
	for _, want := range []string{"volumes.nfsdata.driver", "volumes.nfsdata.driver_opts"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q to be reported as ignored, got %v", want, proj.Unsupported)
		}
	}
	// The acted-on keys are not flagged, and neither is `volumes` wholesale.
	for _, unwanted := range []string{"volumes.plain.external", "volumes.plain.name"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("%q is acted on and must not be reported as ignored, got %v", unwanted, proj.Unsupported)
		}
	}
	if slices.Contains(proj.Unsupported, "volumes") {
		t.Errorf("`volumes` itself is acted on and must not be flagged, got %v", proj.Unsupported)
	}
}
