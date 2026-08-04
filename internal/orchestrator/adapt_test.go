package orchestrator

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// planFor builds an orchestrator over a compose project and returns its overlay.
func planFor(t *testing.T, body string) (string, []Adaptation) {
	t.Helper()
	p := loadProject(t, body)
	o := New(p, nil, "opossum", io.Discard)
	return o.PlanOverlay()
}

func loadProject(t *testing.T, body string) *compose.Project {
	t.Helper()
	path := writeFile(t, t.TempDir(), "compose.yaml", body)
	p, err := compose.Load(path)
	if err != nil {
		t.Fatalf("loading test compose: %v", err)
	}
	return p
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The commonest migration snag: a named volume mounted straight at Postgres's
// data directory. The overlay redirects PGDATA into a subdirectory of that same
// volume — additive, and the data stays in the volume.
func TestPlanOverlayPGDATA(t *testing.T) {
	body, changes := planFor(t, `
name: demo
services:
  db:
    image: postgres:16
    volumes:
      - dbdata:/var/lib/postgresql/data
volumes:
  dbdata: {}
`)
	if len(changes) != 1 || changes[0].Code != "OPSM-101" || changes[0].Service != "db" {
		t.Fatalf("expected one OPSM-101 change for db, got %+v", changes)
	}
	if !strings.Contains(body, "PGDATA: /var/lib/postgresql/data/pgdata") {
		t.Errorf("overlay should set PGDATA to a subdirectory, got:\n%s", body)
	}
	// It must not invent a volume swap — the mount is already a named volume.
	if strings.Contains(body, "volumes:\n      -") {
		t.Errorf("overlay should not touch volumes for an already-named mount, got:\n%s", body)
	}
}

// A bind-mounted DB data directory is swapped for a named volume: Apple
// `container` bind mounts are host-owned and can't be chowned, which every
// official DB image does at startup.
func TestPlanOverlayBindMountedDataDir(t *testing.T) {
	body, changes := planFor(t, `
name: demo
services:
  cache:
    image: mysql:8
    volumes:
      - ./mysql:/var/lib/mysql
`)
	if len(changes) != 1 || changes[0].Code != "OPSM-105" {
		t.Fatalf("expected one OPSM-105 change, got %+v", changes)
	}
	if !strings.Contains(body, "- cache-data:/var/lib/mysql") {
		t.Errorf("overlay should mount a named volume at the data dir, got:\n%s", body)
	}
	// The named volume has to be declared, or the project won't load.
	if !strings.Contains(body, "volumes:\n  cache-data: {}") {
		t.Errorf("overlay should declare the named volume it introduces, got:\n%s", body)
	}
	// The comment must warn that data moves — the one surprising consequence.
	if !strings.Contains(body, "changes where the data lives") {
		t.Errorf("overlay must say the data location changes, got:\n%s", body)
	}
}

// A bind-mounted *Postgres* data dir needs BOTH fixes: swapping it for a named
// volume alone would just move the failure to initdb (a volume mount point isn't
// empty), so PGDATA must be redirected in the same pass.
func TestPlanOverlayBindMountedPostgresGetsBothFixes(t *testing.T) {
	body, changes := planFor(t, `
name: demo
services:
  db:
    image: postgres:16
    volumes:
      - ./pgdata:/var/lib/postgresql/data
`)
	codes := map[string]bool{}
	for _, c := range changes {
		codes[c.Code] = true
	}
	if !codes["OPSM-105"] || !codes["OPSM-101"] {
		t.Fatalf("a bind-mounted postgres data dir needs both fixes, got %+v", changes)
	}
	if !strings.Contains(body, "- db-data:/var/lib/postgresql/data") ||
		!strings.Contains(body, "PGDATA: /var/lib/postgresql/data/pgdata") {
		t.Errorf("overlay should both swap the mount and redirect PGDATA, got:\n%s", body)
	}
}

// Detection runs on the RESOLVED project, so a fix the user already applied (by
// hand or in a compose.override.yaml) must not be applied again.
func TestPlanOverlaySkipsAlreadyFixed(t *testing.T) {
	// PGDATA already points at a subdirectory.
	if body, changes := planFor(t, `
name: demo
services:
  db:
    image: postgres:16
    environment:
      PGDATA: /var/lib/postgresql/data/pgdata
    volumes:
      - dbdata:/var/lib/postgresql/data
volumes:
  dbdata: {}
`); body != "" || len(changes) != 0 {
		t.Errorf("an existing PGDATA subdirectory should need no overlay, got %d change(s):\n%s", len(changes), body)
	}

	// A DB whose data dir is already a named volume needs no swap.
	if body, changes := planFor(t, `
name: demo
services:
  cache:
    image: mysql:8
    volumes:
      - cachedata:/var/lib/mysql
volumes:
  cachedata: {}
`); body != "" || len(changes) != 0 {
		t.Errorf("a named-volume mysql data dir should need no overlay, got %d change(s):\n%s", len(changes), body)
	}
}

// Nothing to adapt -> no file. The overlay must never be written speculatively.
func TestPlanOverlayNoFalsePositives(t *testing.T) {
	body, changes := planFor(t, `
name: demo
services:
  web:
    image: nginx
    volumes:
      - ./site:/usr/share/nginx/html
      - ./conf:/etc/nginx/conf.d
  worker:
    image: busybox
    volumes:
      - shared:/data
volumes:
  shared: {}
`)
	if body != "" || len(changes) != 0 {
		t.Errorf("ordinary bind mounts and volumes must not be adapted, got %d change(s):\n%s", len(changes), body)
	}
}

// An external volume is the user's to manage — opossum must not redirect into it.
func TestPlanOverlaySkipsExternalVolume(t *testing.T) {
	body, changes := planFor(t, `
name: demo
services:
  db:
    image: postgres:16
    volumes:
      - dbdata:/var/lib/postgresql/data
volumes:
  dbdata:
    external: true
`)
	if body != "" || len(changes) != 0 {
		t.Errorf("an external volume must be left alone, got %d change(s):\n%s", len(changes), body)
	}
}

// The generated comments are a contract: the reader is often an agent deciding
// what to do when the fix didn't work. Every entry must carry all five parts, and
// a stable marker it can grep for. This ratchets the contract so it can't erode.
func TestPlanOverlayCommentContract(t *testing.T) {
	body, changes := planFor(t, `
name: demo
services:
  db:
    image: postgres:16
    volumes:
      - ./pgdata:/var/lib/postgresql/data
  cache:
    image: mysql:8
    volumes:
      - ./mysql:/var/lib/mysql
`)
	if len(changes) == 0 {
		t.Fatal("expected adaptations to check the comment contract against")
	}
	required := []string{
		"# [opossum --from-docker-compose] ", // What, with the stable marker
		"# Why: ",
		"# Verify: ",
		"# If this still fails: ",
		"# To undo: ",
	}
	for _, part := range required {
		// Each part must appear once per entry, not just once in the file.
		if got := strings.Count(body, part); got != len(changes) {
			t.Errorf("comment contract %q appears %d time(s), want %d (one per entry), body:\n%s",
				part, got, len(changes), body)
		}
	}
	// The Why must cite a diagnostic code, so a reader can cross-reference AGENTS.md.
	if !strings.Contains(body, "Diagnostic: OPSM-101") || !strings.Contains(body, "Diagnostic: OPSM-105") {
		t.Errorf("every Why must name its diagnostic code, got:\n%s", body)
	}
	// Undo must state the user's own file is untouched.
	if !strings.Contains(body, "original compose file was") {
		t.Errorf("the undo note must say the original compose file is unmodified, got:\n%s", body)
	}
}

// The overlay must parse as compose and actually resolve the problem: after
// merging it, the data dir is mounted from a named volume exactly once and PGDATA
// points below it. This is the end the whole feature exists for.
func TestPlanOverlayResolvesTheProblemWhenMerged(t *testing.T) {
	dir := t.TempDir()
	basePath := writeFile(t, dir, "compose.yaml", `
name: demo
services:
  db:
    image: postgres:16
    volumes:
      - ./pgdata:/var/lib/postgresql/data
`)
	body, _ := planFor(t, readFile(t, basePath))
	overlayPath := writeFile(t, dir, compose.SanitizeName("compose")+".opossum.yaml", body)

	p, err := compose.LoadFiles([]string{basePath, overlayPath}, nil)
	if err != nil {
		t.Fatalf("the generated overlay must be valid compose: %v\n%s", err, body)
	}
	db := p.Services["db"]
	if len(db.Volumes) != 1 || db.Volumes[0] != "db-data:/var/lib/postgresql/data" {
		t.Errorf("after merging, the data dir should be one named-volume mount, got %v", db.Volumes)
	}
	var pgdata string
	for _, e := range db.Environment {
		if v, ok := strings.CutPrefix(e, "PGDATA="); ok {
			pgdata = v
		}
	}
	if pgdata != "/var/lib/postgresql/data/pgdata" {
		t.Errorf("after merging, PGDATA should point below the data dir, got %q", pgdata)
	}
	// And the adapted project no longer needs adapting (the fix is complete).
	o := New(p, nil, "opossum", io.Discard)
	if b, changes := o.PlanOverlay(); b != "" || len(changes) != 0 {
		t.Errorf("the adapted project should need no further changes, got %d:\n%s", len(changes), b)
	}
}

// A service that mounts a database's data directory but is NOT that database — a
// backup sidecar, an inspector — must be left alone. Rewriting it would swap its
// real data for an empty volume, silently. Keying on the path alone is not enough
// evidence; the image has to look like the database that owns it.
func TestPlanOverlaySkipsNonDatabaseService(t *testing.T) {
	body, changes := planFor(t, `
name: demo
services:
  inspector:
    image: alpine:3
    volumes:
      - ./snapshot:/var/lib/postgresql/data
  backup:
    image: busybox
    volumes:
      - ./dump:/var/lib/mysql
`)
	if body != "" || len(changes) != 0 {
		t.Errorf("a non-database service mounting a data dir must not be adapted, got %d:\n%s", len(changes), body)
	}
}

// A read-only mount is proof the service isn't the database that chowns the
// directory (a database can't run on one), so it is never adapted — and its `:ro`
// is never silently dropped.
func TestPlanOverlaySkipsReadOnlyMount(t *testing.T) {
	body, changes := planFor(t, `
name: demo
services:
  db:
    image: postgres:16
    volumes:
      - ./snapshot:/var/lib/postgresql/data:ro
`)
	if body != "" || len(changes) != 0 {
		t.Errorf("a read-only data-dir mount must not be adapted, got %d:\n%s", len(changes), body)
	}
}

// Vendor and variant images still count as the database that owns the path.
func TestPlanOverlayRecognizesVendorImages(t *testing.T) {
	for _, img := range []string{"bitnami/postgresql:16", "postgis/postgis:16-3.4", "mariadb:11", "percona:8"} {
		dir := "/var/lib/postgresql/data"
		if strings.Contains(img, "maria") || strings.Contains(img, "percona") {
			dir = "/var/lib/mysql"
		}
		_, changes := planFor(t, "name: demo\nservices:\n  db:\n    image: "+img+
			"\n    volumes:\n      - ./data:"+dir+"\n")
		if len(changes) == 0 {
			t.Errorf("image %q mounting %s should be recognized as the owning database", img, dir)
		}
	}
}

// Two services whose sanitized names collide ("a_b" and "a-b" both sanitize to
// "a-b") must not be given the same volume name: the overlay would declare a
// duplicate YAML key (invalid, and it would brick every later command) and point
// two databases at one volume.
func TestPlanOverlayVolumeNamesDoNotCollide(t *testing.T) {
	const src = `
name: demo
services:
  a_b:
    image: mysql:8
    volumes:
      - ./one:/var/lib/mysql
  a-b:
    image: mysql:8
    volumes:
      - ./two:/var/lib/mysql
`
	body, changes := planFor(t, src)
	if len(changes) != 2 {
		t.Fatalf("expected both services adapted, got %+v", changes)
	}
	if _, err := overlayServiceKeys(t, src, body); err != nil {
		t.Fatalf("colliding names produced an unloadable overlay (%v):\n%s", err, body)
	}
	if strings.Count(body, "a-b-data:") < 1 || !strings.Contains(body, "a-b-data-2") {
		t.Errorf("colliding volume names should be disambiguated, got:\n%s", body)
	}
}

// A volume name the project already declares must never be reused — merging into
// an existing declaration would inherit it, including an `external: true` one,
// pointing a fresh database at data the user manages elsewhere.
func TestPlanOverlayAvoidsExistingVolumeName(t *testing.T) {
	body, _ := planFor(t, `
name: demo
services:
  db:
    image: postgres:16
    volumes:
      - ./pgdata:/var/lib/postgresql/data
  other:
    image: busybox
    volumes:
      - db-data:/archive
volumes:
  db-data:
    external: true
    name: production_pgdata
`)
	if strings.Contains(body, "- db-data:/var/lib/postgresql/data") {
		t.Errorf("the overlay must not reuse a declared (external) volume name, got:\n%s", body)
	}
	if !strings.Contains(body, "db-data-2") {
		t.Errorf("expected a disambiguated volume name, got:\n%s", body)
	}
}

// Service names that aren't safe as bare YAML keys (a reserved word, a number)
// must be quoted — otherwise the merged tree renames or drops the service, and the
// generated file is never overwritten, so the breakage is permanent.
func TestPlanOverlayQuotesUnsafeServiceNames(t *testing.T) {
	for _, name := range []string{"true", "no", "1.0"} {
		src := "name: demo\nservices:\n  \"" + name +
			"\":\n    image: postgres:16\n    volumes:\n      - ./d:/var/lib/postgresql/data\n"
		body, changes := planFor(t, src)
		if len(changes) == 0 {
			t.Fatalf("service %q should be adapted", name)
		}
		keys, err := overlayServiceKeys(t, src, body)
		if err != nil {
			t.Errorf("service name %q produced an unloadable overlay (%v):\n%s", name, err, body)
			continue
		}
		if !keys[name] {
			t.Errorf("service name %q did not survive a YAML round-trip (got keys %v):\n%s", name, keys, body)
		}
	}
}

// A `$` in a user-supplied path must be escaped: opossum interpolates ${VAR} over
// the raw file text, comments included, so an unescaped one is either expanded
// (corrupting the record of where the data was) or fails the load outright.
func TestPlanOverlayEscapesInterpolation(t *testing.T) {
	body, _ := planFor(t, `
name: demo
services:
  db:
    image: postgres:16
    volumes:
      - ./pg$$data:/var/lib/postgresql/data
`)
	if !strings.Contains(body, "pg$$data") {
		t.Errorf("a $ in a host path must be escaped as $$ in the overlay, got:\n%s", body)
	}
	// And the overlay must survive a real load (interpolation included).
	dir := t.TempDir()
	base := writeFile(t, dir, "compose.yaml",
		"name: demo\nservices:\n  db:\n    image: postgres:16\n    volumes:\n      - ./pg$$data:/var/lib/postgresql/data\n")
	ov := writeFile(t, dir, OverlayFileName, body)
	if _, err := compose.LoadFiles([]string{base, ov}, nil); err != nil {
		t.Errorf("the generated overlay must load with interpolation on: %v\n%s", err, body)
	}
}

// overlayServiceKeys merges the generated overlay through the REAL load path and
// returns the resulting service keys. A local yaml.Unmarshal is not good enough:
// yaml.v3 coerces a bare `true:` or `1.0:` into a string when decoding into
// map[string]any, so such a test passes even with the quoting removed — while the
// merge path decodes nested mappings into `any`, where a non-string key makes the
// overlay replace the base services map instead of merging into it.
func overlayServiceKeys(t *testing.T, srcBody, overlayBody string) (map[string]bool, error) {
	t.Helper()
	dir := t.TempDir()
	base := writeFile(t, dir, "compose.yaml", srcBody)
	ov := writeFile(t, dir, OverlayFileName, overlayBody)
	p, err := compose.LoadFiles([]string{base, ov}, nil)
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for k := range p.Services {
		keys[k] = true
	}
	return keys, nil
}

// A PGDATA the user set is theirs, wherever it points. Treating "not under the
// default data directory" as unfixed would overwrite a deliberate PGDATA on
// another volume — Postgres would initdb an empty cluster and the real one would
// sit unreachable, looking to the user like the database came up empty.
func TestPlanOverlayNeverOverridesUserPGDATA(t *testing.T) {
	body, changes := planFor(t, `
name: demo
services:
  db:
    image: postgres:16
    environment:
      PGDATA: /custom/pg
    volumes:
      - dbdata:/var/lib/postgresql/data
      - pgcustom:/custom
volumes:
  dbdata: {}
  pgcustom: {}
`)
	if body != "" || len(changes) != 0 {
		t.Errorf("a PGDATA the user set must never be overridden, got %d:\n%s", len(changes), body)
	}
}

// Sidecars whose image merely CONTAINS a database's name — an exporter, a dump
// cron, a backup tool — are not that database. Matching the image reference by
// substring would adapt exactly the services this is meant to protect, swapping
// their real data for an empty volume.
func TestPlanOverlaySkipsDatabaseNamedSidecars(t *testing.T) {
	cases := []struct{ image, dir string }{
		{"quay.io/prometheuscommunity/postgres-exporter:v0.15", "/var/lib/postgresql/data"},
		{"schickling/mysqldump-cron", "/var/lib/mysql"},
		{"myapp/mysql-backup-tool:1", "/var/lib/mysql"},
		{"myorg/postgres-backup:2", "/var/lib/postgresql/data"},
	}
	for _, c := range cases {
		body, changes := planFor(t, "name: demo\nservices:\n  side:\n    image: "+c.image+
			"\n    volumes:\n      - ./d:"+c.dir+"\n")
		if body != "" || len(changes) != 0 {
			t.Errorf("image %q is not the database that owns %s; got %d change(s):\n%s",
				c.image, c.dir, len(changes), body)
		}
	}
}

// …while the real databases, including registry-qualified and digest-pinned refs,
// are still recognized. A false negative is only a missed fix, but a whole class of
// missed fixes would make the feature pointless.
func TestImageNameRecognition(t *testing.T) {
	owns := map[string]string{
		"postgres:16":                   "/var/lib/postgresql/data",
		"docker.io/library/postgres:16": "/var/lib/postgresql/data",
		"bitnami/postgresql":            "/var/lib/postgresql/data",
		"postgis/postgis:16-3.4":        "/var/lib/postgresql/data",
		"mysql:8":                       "/var/lib/mysql",
		"mysql/mysql-server:8.0":        "/var/lib/mysql",
		"mariadb:11":                    "/var/lib/mysql",
		"percona:8":                     "/var/lib/mysql",
		"localhost:5000/postgres:16":    "/var/lib/postgresql/data",
	}
	for img, dir := range owns {
		svc := &compose.Service{Image: img}
		if !ownsDataDir(svc, dir) {
			t.Errorf("image %q should be recognized as owning %s (imageName=%q)", img, dir, imageName(img))
		}
	}
}

// A named volume a service mounts without declaring it top-level is still in use.
// Claiming that name would hand a database another service's data — and trip the
// exclusive-attach failure (OPSM-102) on the real runtime.
func TestPlanOverlayAvoidsUndeclaredButUsedVolume(t *testing.T) {
	body, _ := planFor(t, `
name: demo
services:
  db:
    image: postgres:16
    volumes:
      - ./pgdata:/var/lib/postgresql/data
  archiver:
    image: busybox
    volumes:
      - db-data:/archive
`)
	if strings.Contains(body, "- db-data:/var/lib/postgresql/data") {
		t.Errorf("the overlay must not claim a volume another service already mounts, got:\n%s", body)
	}
	if !strings.Contains(body, "db-data-2") {
		t.Errorf("expected a disambiguated volume name, got:\n%s", body)
	}
}

// A `$` in a SERVICE NAME must be escaped in the emitted key too, not just in
// comments: unescaped, interpolation eats it on reload and the service key becomes
// something else, breaking every later command against a file opossum won't rewrite.
func TestPlanOverlayEscapesServiceNameInKey(t *testing.T) {
	src := "name: demo\nservices:\n  \"pg$$db\":\n    image: postgres:16\n    volumes:\n      - ./d:/var/lib/postgresql/data\n"
	body, changes := planFor(t, src)
	if len(changes) == 0 {
		t.Fatal("expected the service to be adapted")
	}
	keys, err := overlayServiceKeys(t, src, body)
	if err != nil {
		t.Fatalf("a $ in a service name must not break the overlay: %v\n%s", err, body)
	}
	if !keys["pg$db"] {
		t.Errorf("the service key must survive interpolation, got keys %v:\n%s", keys, body)
	}
}

// PGDATA can be passed through from the host environment (`- PGDATA`, or `PGDATA:`
// with no value), which is stored as a bare name with no `=`. Missing that form
// would overwrite a PGDATA the user supplies at run time — the exact damage the
// guard exists to prevent.
func TestPlanOverlayRespectsPassThroughPGDATA(t *testing.T) {
	for _, env := range []string{"    environment:\n      - PGDATA\n", "    environment:\n      PGDATA:\n"} {
		body, changes := planFor(t, "name: demo\nservices:\n  db:\n    image: postgres:16\n"+env+
			"    volumes:\n      - dbdata:/var/lib/postgresql/data\nvolumes:\n  dbdata: {}\n")
		if body != "" || len(changes) != 0 {
			t.Errorf("a pass-through PGDATA must be respected, got %d change(s):\n%s", len(changes), body)
		}
	}
}

// Two services on the SAME host data directory are sharing it deliberately — a
// database and a backup/inspection sidecar running the database's own image (which
// defeats the image gate, since it needs matching binaries). Giving each its own
// named volume would sever that silently: the sidecar would keep working against an
// empty volume and look healthy forever.
func TestPlanOverlaySkipsSharedHostDataDir(t *testing.T) {
	body, changes := planFor(t, `
name: demo
services:
  db:
    image: postgres:16
    volumes:
      - ./pgdata:/var/lib/postgresql/data
  backup:
    image: postgres:16
    entrypoint: ["/bin/sh", "-c", "tar czf /backup/pg.tgz /var/lib/postgresql/data"]
    volumes:
      - ./pgdata:/var/lib/postgresql/data
      - ./backups:/backup
`)
	for _, c := range changes {
		if c.Code == "OPSM-105" {
			t.Errorf("a host data dir shared by two services must not be split, got %+v:\n%s", changes, body)
			break
		}
	}
}

// Moving PGDATA down a level strands anything mounted below the data directory —
// an injected postgresql.conf stops being read, a dedicated pg_wal goes unused —
// so the fix is declined rather than applied silently.
func TestPlanOverlaySkipsPGDATAWithNestedMount(t *testing.T) {
	for _, nested := range []string{
		"      - ./postgresql.conf:/var/lib/postgresql/data/postgresql.conf:ro\n",
		"      - ./pgwal:/var/lib/postgresql/data/pg_wal\n",
	} {
		body, changes := planFor(t, "name: demo\nservices:\n  db:\n    image: postgres:16\n"+
			"    volumes:\n      - dbdata:/var/lib/postgresql/data\n"+nested+
			"volumes:\n  dbdata: {}\n")
		for _, c := range changes {
			if c.Code == "OPSM-101" {
				t.Errorf("PGDATA must not move when a mount sits below the data dir, got %+v:\n%s", changes, body)
				break
			}
		}
	}
}

// PGDATA is redirected because a NAMED VOLUME's mount point isn't empty. When the
// data dir stays a bind mount — because the swap was declined, e.g. two services
// share it — the directory is the user's and initdb is happy with it, so moving
// PGDATA would relocate their data a level down for nothing.
func TestPlanOverlayNoPGDATAForUnswappedBindMount(t *testing.T) {
	_, changes := planFor(t, `
name: demo
services:
  db:
    image: postgres:16
    volumes:
      - ./pgdata:/var/lib/postgresql/data
  backup:
    image: postgres:16
    entrypoint: ["/bin/sh", "-c", "true"]
    volumes:
      - ./pgdata:/var/lib/postgresql/data
`)
	if len(changes) != 0 {
		t.Errorf("a shared bind mount should get neither fix, got %+v", changes)
	}
}

// The data directories below were each confirmed on the real runtime to fail the
// same way as Postgres/MySQL — the image chowns its data directory and exits.
// Redis's `/data` is generic, so the image name is doing all the work: the exact
// match is what keeps an unrelated service mounting `/data` from being rewritten.
func TestPlanOverlayCoversMoreDatabases(t *testing.T) {
	cases := []struct{ image, dir string }{
		{"clickhouse/clickhouse-server:25.3-alpine", "/var/lib/clickhouse"},
		{"mongo:7", "/data/db"},
		{"redis:7-alpine", "/data"},
		{"valkey/valkey:8", "/data"},
	}
	for _, c := range cases {
		_, changes := planFor(t, "name: demo\nservices:\n  db:\n    image: "+c.image+
			"\n    volumes:\n      - ./d:"+c.dir+"\n")
		if len(changes) == 0 {
			t.Errorf("%s mounting %s should be adapted", c.image, c.dir)
			continue
		}
		if changes[0].Code != "OPSM-105" {
			t.Errorf("%s: expected OPSM-105, got %+v", c.image, changes)
		}
	}
}

// …and a service that merely mounts one of those paths without being that
// database is still left alone. `/data` in particular is mounted by all sorts of
// images, so a wrong match here would swap real data for an empty volume.
func TestPlanOverlayGenericDataDirNeedsMatchingImage(t *testing.T) {
	for _, img := range []string{"busybox", "alpine:3", "myapp/data-loader:1", "redis-exporter:1"} {
		body, changes := planFor(t, "name: demo\nservices:\n  svc:\n    image: "+img+
			"\n    volumes:\n      - ./d:/data\n")
		if body != "" || len(changes) != 0 {
			t.Errorf("%s mounting /data is not a database; got %d change(s):\n%s", img, len(changes), body)
		}
	}
}

// mongo's entrypoint chowns BOTH /data/db and /data/configdb. Fixing only the
// first is the worst possible outcome: the change is announced as the fix, the
// user's directory is detached from the container, and it still dies on the
// second. Every chowned directory on a service has to be adapted in one pass.
func TestPlanOverlayAdaptsEveryDataDirOnAService(t *testing.T) {
	const src = `
name: demo
services:
  db:
    image: mongo:7
    volumes:
      - ./db:/data/db
      - ./cfg:/data/configdb
`
	body, changes := planFor(t, src)
	if len(changes) != 2 {
		t.Fatalf("both chowned directories must be adapted, got %+v:\n%s", changes, body)
	}
	for _, want := range []string{":/data/db", ":/data/configdb"} {
		if !strings.Contains(body, want) {
			t.Errorf("overlay should mount a named volume at %s, got:\n%s", want, body)
		}
	}
	// Two adaptations on one service both write under `volumes:`. Emitting that
	// key twice is invalid YAML and the overlay would not load at all, so check
	// the merged result through the real loader rather than by substring.
	p := mergeOverlay(t, src, body)
	vols := p.Services["db"].Volumes
	if len(vols) != 2 {
		t.Fatalf("expected two named-volume mounts after merging, got %v", vols)
	}
	seen := map[string]bool{}
	for _, v := range vols {
		src, _, _, ok := splitMount(v)
		if !ok || isHostPath(src) {
			t.Errorf("mount %q should be a named volume", v)
		}
		if seen[src] {
			t.Errorf("both data dirs got the same volume %q: %v", src, vols)
		}
		seen[src] = true
	}
}

// mergeOverlay loads the source compose with the generated overlay on top, the
// way a real run does, and fails if the result doesn't resolve.
func mergeOverlay(t *testing.T, srcBody, overlayBody string) *compose.Project {
	t.Helper()
	dir := t.TempDir()
	base := writeFile(t, dir, "compose.yaml", srcBody)
	ov := writeFile(t, dir, OverlayFileName, overlayBody)
	p, err := compose.LoadFiles([]string{base, ov}, nil)
	if err != nil {
		t.Fatalf("the generated overlay must load: %v\n%s", err, overlayBody)
	}
	return p
}

// A service built on a database image but running a client/dump command is not
// the server: the official entrypoints only chown when started as the server, so
// a bind mount works fine there and rewriting it would swap real data for an
// empty volume.
func TestPlanOverlaySkipsNonServerCommand(t *testing.T) {
	cases := []string{
		"name: demo\nservices:\n  dump:\n    image: redis:7-alpine\n    command: [\"redis-cli\",\"--pipe\"]\n    volumes:\n      - ./dumps:/data\n",
		"name: demo\nservices:\n  restore:\n    image: mongo:7\n    entrypoint: [\"mongorestore\"]\n    volumes:\n      - ./dump:/data/db\n",
		"name: demo\nservices:\n  shell:\n    image: mongo:7\n    command: [\"sh\",\"-c\",\"mongodump\"]\n    volumes:\n      - ./dump:/data/db\n",
	}
	for _, src := range cases {
		body, changes := planFor(t, src)
		if body != "" || len(changes) != 0 {
			t.Errorf("a non-server command must not be adapted, got %d change(s):\n%s\nfor:\n%s", len(changes), body, src)
		}
	}
}

// …but the server itself, with flags, still adapts.
func TestPlanOverlayAdaptsServerWithFlags(t *testing.T) {
	_, changes := planFor(t, `
name: demo
services:
  cache:
    image: redis:7-alpine
    command: ["redis-server", "--appendonly", "yes"]
    volumes:
      - ./data:/data
`)
	if len(changes) != 1 {
		t.Errorf("a server invoked with flags should still be adapted, got %+v", changes)
	}
}

// A named volume shared by several services can't work on Apple container (one
// attachment at a time), and the way out — a host directory — changes what the
// project means. So it's written out and left commented: proposed, not done.
func TestPlanOverlaySuggestsSharedVolumeFix(t *testing.T) {
	const src = `
name: demo
services:
  web:
    image: nginx
    volumes:
      - shared:/srv
  worker:
    image: busybox
    volumes:
      - shared:/srv
volumes:
  shared: {}
`
	body, changes := planFor(t, src)
	if len(changes) != 2 {
		t.Fatalf("both sharers should get a suggestion, got %+v", changes)
	}
	for _, c := range changes {
		if c.Code != "OPSM-102" {
			t.Errorf("expected OPSM-102, got %+v", c)
		}
	}
	if !strings.Contains(body, suggestionMarker) {
		t.Errorf("a suggestion must carry its marker, got:\n%s", body)
	}
	// The proposal must be INERT: merging the overlay must not change the mounts.
	p := mergeOverlay(t, src, body)
	for _, svc := range []string{"web", "worker"} {
		vols := p.Services[svc].Volumes
		if len(vols) != 1 || vols[0] != "shared:/srv" {
			t.Errorf("a suggestion must not take effect; %s has %v", svc, vols)
		}
	}
}

// Uncommenting a suggestion has to actually work — that's the whole promise. Strip
// the comment prefix from the suggestion block and the result must merge cleanly
// and produce the proposed mounts.
func TestPlanOverlaySuggestionAppliesWhenUncommented(t *testing.T) {
	const src = `
name: demo
services:
  web:
    image: nginx
    volumes:
      - shared:/srv
  worker:
    image: busybox
    volumes:
      - shared:/srv
volumes:
  shared: {}
`
	body, _ := planFor(t, src)
	uncommented := uncommentSuggestions(body)
	p := mergeOverlay(t, src, uncommented)
	for _, svc := range []string{"web", "worker"} {
		vols := p.Services[svc].Volumes
		if len(vols) != 1 || !strings.HasPrefix(vols[0], "./shared:") {
			t.Errorf("uncommenting should swap %s to a host directory, got %v", svc, vols)
		}
	}
}

// uncommentSuggestions strips the leading "# " from the suggestion section, the
// way a user would when accepting the proposal.
func uncommentSuggestions(body string) string {
	var out []string
	inSuggestions := false
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "# ── Suggestions"):
			inSuggestions = true
			continue
		case strings.HasPrefix(line, "# ── Notes"):
			inSuggestions = false
			continue
		}
		if inSuggestions {
			if strings.HasPrefix(line, "# opossum did NOT") || strings.HasPrefix(line, "# the call is") ||
				strings.HasPrefix(line, "# line included") {
				continue // the section preamble, not part of the YAML
			}
			out = append(out, strings.TrimPrefix(strings.TrimPrefix(line, "# "), "#"))
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// Problems opossum can't fix are recorded rather than left for the user to
// rediscover — and they carry no YAML, so nothing can be uncommented into effect.
func TestPlanOverlayNotesUnfixable(t *testing.T) {
	body, changes := planFor(t, `
name: demo
services:
  ci:
    image: someci
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
  media:
    image: player
    volumes:
      - /dev/dri:/dev/dri
`)
	codes := map[string]bool{}
	for _, c := range changes {
		codes[c.Code] = true
	}
	if !codes["OPSM-204"] {
		t.Errorf("a docker.sock mount should be noted, got %+v", changes)
	}
	if !strings.Contains(body, noteMarker) {
		t.Errorf("a note must carry its marker, got:\n%s", body)
	}
	if !strings.Contains(body, "/dev/dri") {
		t.Errorf("a host device mount should be noted, got:\n%s", body)
	}
	// Notes are prose only: there is no services: block to accidentally apply.
	if strings.Contains(body, "\nservices:") {
		t.Errorf("a notes-only overlay should carry no YAML, got:\n%s", body)
	}
}

// The three classes are a contract an agent relies on: each entry says which it
// is, and the markers are stable. This ratchets that so it can't erode.
func TestPlanOverlayEntryClassContract(t *testing.T) {
	body, _ := planFor(t, `
name: demo
services:
  db:
    image: postgres:16
    volumes:
      - ./pg:/var/lib/postgresql/data
  web:
    image: nginx
    volumes:
      - shared:/srv
  worker:
    image: busybox
    volumes:
      - shared:/srv
  ci:
    image: someci
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
volumes:
  shared: {}
`)
	for _, marker := range []string{overlayMarker, suggestionMarker, noteMarker} {
		if !strings.Contains(body, marker) {
			t.Errorf("every class must be present and marked; missing %q in:\n%s", marker, body)
		}
	}
	// The header must teach the reader what the three markers mean.
	for _, want := range []string{"applied. This is live", "uncomment to apply", "nothing to change"} {
		if !strings.Contains(body, want) {
			t.Errorf("the header should explain the classes; missing %q", want)
		}
	}
}

// planForIn is planFor with a compose file written into a directory the test
// controls, so bind-mount sources can be created and populated.
func planForIn(t *testing.T, dir, body string) (string, []Adaptation) {
	t.Helper()
	path := writeFile(t, dir, "compose.yaml", body)
	p, err := compose.Load(path)
	if err != nil {
		t.Fatalf("loading test compose: %v", err)
	}
	return New(p, nil, "opossum", io.Discard).PlanOverlay()
}

// A directory two services share is class C, not this: swapping it for a named
// volume would break the sharing outright (OPSM-102), so it must not be suggested
// as an app data dir.
func TestPlanOverlaySharedEmptyDirIsNotAnAppDataDirSuggestion(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "media"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, changes := planForIn(t, dir, `
name: demo
services:
  app:
    image: papermerge/papermerge
    volumes:
      - ./media:/opt/media
  worker:
    image: papermerge/papermerge
    volumes:
      - ./media:/opt/media
`)
	for _, c := range changes {
		if c.Code == string(codeBindDataDirChown) {
			t.Errorf("a shared directory must not be suggested as an app data dir, got %+v", changes)
		}
	}
}

// The self-containment promise has to survive MORE THAN ONE suggestion. Emitting
// a `volumes:` key between services would either duplicate a top-level key
// (invalid YAML) or swallow the services after it as volume declarations — in
// which case uncommenting silently applies only the first suggestion while the
// user believes they applied them all.
func TestPlanOverlayMultipleSuggestionsUncommentTogether(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"content", "wiki"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const src = `
name: demo
services:
  blog:
    image: ghost:6-alpine
    volumes:
      - ./content:/var/lib/ghost/content
  wiki:
    image: wikijs
    volumes:
      - ./wiki:/var/wiki/data
  web:
    image: nginx
    volumes:
      - shared:/srv
  worker:
    image: busybox
    volumes:
      - shared:/srv
volumes:
  shared: {}
`
	writeFile(t, dir, "compose.yaml", src)
	p0, err := compose.Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	o := New(p0, nil, "opossum", io.Discard)
	// Two services that actually died chowning their mount, plus the pair sharing a
	// named volume: enough suggestions from two different sources to catch a render
	// that only makes the first one uncommentable.
	o.crashHint("blog", "chown: changing ownership of '/var/lib/ghost/content': Operation not permitted")
	o.crashHint("wiki", "chown: /var/wiki/data: Operation not permitted")
	body, changes := o.PlanOverlay()
	if len(changes) < 4 {
		t.Fatalf("expected suggestions for all four services, got %+v", changes)
	}

	// Uncommented, EVERY suggestion must take effect — not just the first.
	merged := mergeOverlay(t, src, uncommentSuggestions(body))
	for _, c := range []struct{ svc, wantPrefix string }{
		{"blog", "blog-content:"},
		{"wiki", "wiki-data:"},
		{"web", "./shared:"},
		{"worker", "./shared:"},
	} {
		vols := merged.Services[c.svc].Volumes
		if len(vols) != 1 || !strings.HasPrefix(vols[0], c.wantPrefix) {
			t.Errorf("uncommenting should give %s a %s mount, got %v", c.svc, c.wantPrefix, vols)
		}
	}
	// And the volumes the suggestions introduce must be declared exactly once.
	if n := strings.Count(uncommentSuggestions(body), "\nvolumes:\n"); n != 1 {
		t.Errorf("the uncommented block should carry exactly one volumes: section, got %d", n)
	}
}

// Two empty bind dirs on ONE service must not be handed the same volume name —
// that would mount one volume at two paths.
func TestPlanOverlayTwoDirsOnOneServiceGetDistinctVolumes(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"d1", "d2"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	body, _ := planForIn(t, dir, `
name: demo
services:
  app:
    image: someapp
    volumes:
      - ./d1:/var/data
      - ./d2:/opt/data
`)
	names := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		if i := strings.Index(line, "      - "); i >= 0 && strings.Contains(line, ":/") {
			spec := strings.TrimSpace(line[i+8:])
			if src, _, _, ok := splitMount(spec); ok {
				if names[src] {
					t.Errorf("volume %q suggested for two mount points:\n%s", src, body)
				}
				names[src] = true
			}
		}
	}
}

// A suggested volume name must not land on one the project already declares —
// re-declaring an external volume would hand a service someone else's data.
func TestPlanOverlaySuggestedVolumeAvoidsDeclaredNames(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "content"), 0o755); err != nil {
		t.Fatal(err)
	}
	body, _ := planForIn(t, dir, `
name: demo
services:
  blog:
    image: ghost:6-alpine
    volumes:
      - ./content:/var/lib/ghost/content
  other:
    image: busybox
    volumes:
      - blog-content:/mnt
volumes:
  blog-content:
    external: true
    name: real-content
`)
	if strings.Contains(body, "- blog-content:/var/lib/ghost/content") {
		t.Errorf("a suggestion must not reuse a declared volume name, got:\n%s", body)
	}
}

// A read-only mount is not a data directory the service takes ownership of — the
// host is supplying it, and nothing chowns it. Suggesting a named volume there
// would be advice to replace supplied content with an empty volume.
func TestPlanOverlayNoAppDataSuggestionForReadOnlyMount(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "content"), 0o755); err != nil {
		t.Fatal(err)
	}
	body, changes := planForIn(t, dir, `
name: demo
services:
  blog:
    image: ghost:6-alpine
    volumes:
      - ./content:/var/lib/ghost/content:ro
`)
	if body != "" || len(changes) != 0 {
		t.Errorf("a read-only mount must not be suggested away, got %d:\n%s", len(changes), body)
	}
}

// `on-failure` is the one policy opossum can only approximate. Someone migrating
// a project meets the compatibility surprises in the overlay, so it belongs there
// too — not only in `config` and the docs, which they may never open.
func TestPlanOverlayNotesTheOnFailureApproximation(t *testing.T) {
	body, changes := planFor(t, `
name: demo
services:
  worker:
    image: busybox
    restart: on-failure
`)
	found := false
	for _, c := range changes {
		if c.Kind == "note" && strings.Contains(c.Summary, "on-failure") {
			found = true
		}
	}
	if !found {
		t.Fatalf("an on-failure policy should be noted, got %+v:\n%s", changes, body)
	}
	if !strings.Contains(body, "exit code") {
		t.Errorf("the note should say why it can't be honoured, got:\n%s", body)
	}
	// It must be a note, not a change: nothing here is applied.
	if strings.Contains(body, "\nservices:") {
		t.Errorf("a notes-only finding should carry no YAML, got:\n%s", body)
	}
}

// The policies opossum honours exactly must not be flagged — a note on every
// project with `restart:` would be noise, and `always` genuinely works.
func TestPlanOverlayNoNoteForHonouredPolicies(t *testing.T) {
	for _, p := range []string{"always", "unless-stopped", "no", ""} {
		src := "name: demo\nservices:\n  web:\n    image: nginx\n"
		if p != "" {
			src += "    restart: " + p + "\n"
		}
		body, changes := planFor(t, src)
		if body != "" || len(changes) != 0 {
			t.Errorf("restart: %q is honoured exactly and needs no note, got %d:\n%s", p, len(changes), body)
		}
	}
}

func TestHandsThroughAFile(t *testing.T) {
	for _, tc := range []struct {
		src, target string
		want        bool
	}{
		// The same name on both sides with an extension: a single file passed in.
		{"./mongodb-init-replica-set.js", "/docker-entrypoint-initdb.d/mongodb-init-replica-set.js", true},
		{"./nginx.conf", "/etc/nginx/nginx.conf", true},
		// A directory handed over, which is what the suggestion is for.
		{"./appdata", "/opt/app/data", false},
		{"./volume-data/influxdb/data", "/var/lib/influxdb", false},
		// Same name, no extension — a directory, not a file.
		{"./data", "/data", false},
		// An extension but different names: not the pass-a-file shape.
		{"./conf", "/etc/nginx/nginx.conf", false},
	} {
		if got := handsThroughAFile(tc.src, tc.target); got != tc.want {
			t.Errorf("handsThroughAFile(%q, %q) = %v, want %v", tc.src, tc.target, got, tc.want)
		}
	}
}

// The predicate this replaced proposed a named volume for any empty read-write
// bind directory. Measured over 156 real projects it made 193 proposals and about
// half were wrong: `/config` files people edit, `/downloads` and `/media` they
// open, `/logs` they read. These are those exact shapes, taken from the ledger at
// ~/opossum-dogfood/results/df321-mounts.tsv, and none of them may draw a
// suggestion now — the only thing that earns one is a crash.
func TestPlanOverlayProposesNothingForBindDirsThatNeverFailed(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"config", "downloads", "media", "logs", "uploads", "data"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Empty on purpose: emptiness is what used to be read as "the app will fill it".
	writeFile(t, dir, "compose.yaml", `
name: demo
services:
  jellyfin:
    image: lscr.io/linuxserver/jellyfin
    volumes:
      - ./config:/config
      - ./media:/media
  transmission:
    image: lscr.io/linuxserver/transmission
    volumes:
      - ./downloads:/downloads
  nginx:
    image: nginx
    volumes:
      - ./logs:/var/log/nginx
  app:
    image: someapp
    volumes:
      - ./uploads:/uploads
      - ./data:/app/data
`)
	p0, err := compose.Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	body, changes := New(p0, nil, "opossum", io.Discard).PlanOverlay()
	for _, c := range changes {
		if c.Kind == "suggestion" {
			t.Errorf("nothing here has failed, so nothing should be proposed — got %+v", c)
		}
	}
	if strings.Contains(body, suggestionMarker) {
		t.Errorf("the overlay should carry no suggestion, got:\n%s", body)
	}
}

// What does earn one: the container started, tried to take ownership of a bind
// mount, and exited. The suggestion then names that mount and only that mount —
// the service's other bind mounts are working and detaching them would be the
// mis-proposal this design exists to stop making.
func TestPlanOverlayProposesTheMountThatActuallyDied(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"db", "config", "backups"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, dir, "compose.yaml", `
name: demo
services:
  ghost:
    image: ghost:6-alpine
    volumes:
      # The failing mount is deliberately NOT first: an implementation that
      # blames whichever bind mount it happens to see first would pass with it
      # at the top and be wrong about every project that lists them otherwise.
      - ./config:/etc/ghost
      - ./backups:/backups
      - ./db:/var/lib/ghost/content
`)
	p0, err := compose.Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	o := New(p0, nil, "opossum", io.Discard)

	// Nothing has failed yet.
	if _, changes := o.PlanOverlay(); len(changes) != 0 {
		t.Fatalf("before any crash there is nothing to propose, got %+v", changes)
	}

	// The crash decode runs on the real log shape and records what it saw.
	logs := "[entrypoint] starting\nchown: changing ownership of '/var/lib/ghost/content': Operation not permitted"
	if h := o.crashHint("ghost", logs); h == "" {
		t.Fatal("the chown signature should still produce the crash hint")
	}

	body, changes := o.PlanOverlay()
	var suggested []string
	for _, c := range changes {
		if c.Kind == "suggestion" {
			suggested = append(suggested, c.Summary)
		}
	}
	if len(suggested) != 1 || !strings.Contains(suggested[0], "/var/lib/ghost/content") {
		t.Fatalf("exactly the mount that died should be proposed, got %v", suggested)
	}
	// The other two mounts of the same service never failed and are not touched.
	for _, safe := range []string{"/etc/ghost", "/backups"} {
		if strings.Contains(body, safe) {
			t.Errorf("%s did not fail, so it must not appear in the overlay:\n%s", safe, body)
		}
	}
	if !strings.Contains(body, suggestionMarker) {
		t.Errorf("the entry should be a suggestion, not applied:\n%s", body)
	}
}

// A log that names no usable path (redis reports `chown: .:`) still identifies the
// mount when the service has only one. With more than one it does not, and
// guessing between them is what this design refuses to do.
func TestChownFailureIsOnlyRecordedWhenTheMountIsCertain(t *testing.T) {
	for _, tc := range []struct {
		name    string
		volumes []string
		logs    string
		want    string // "" = nothing recorded
	}{
		{"path names one of several", []string{"./a:/data", "./b:/config"},
			"chown: /data: Operation not permitted", "/data"},
		{"unusable path, single mount", []string{"./a:/data"},
			"chown: .: Operation not permitted", "/data"},
		{"unusable path, several mounts", []string{"./a:/data", "./b:/config"},
			"chown: .: Operation not permitted", ""},
		{"read-only mounts are not candidates", []string{"./a:/data:ro"},
			"chown: /data: Operation not permitted", ""},
		// The log names a directory inside the image that this service does not
		// mount at all — an entrypoint running as a non-root user chowning its own
		// install. Falling back to "there is only one bind mount" would name a
		// directory that is working and tell the reader it is the one that died.
		{"path names something that is not mounted", []string{"./a:/config"},
			"chown: /usr/local/lib/app: Operation not permitted", ""},
		// A log naming a parent of the mount is not evidence about the mount: the
		// parent's own chown was refused, which a named volume for a child does not
		// fix. A recursive chown that dies on a mounted child names that child.
		{"log names a parent of the mount", []string{"./a:/data/db", "./b:/config"},
			"chown: /data: Operation not permitted", ""},
		// The shape that made an earlier version of this fire on everything.
		{"log names the filesystem root", []string{"./a:/config"},
			"chown: /: Operation not permitted", ""},
		{"log names a parent, single mount", []string{"./a:/var/lib/mysql"},
			"chown: /var: Operation not permitted", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &compose.Project{Name: "demo", BaseDir: t.TempDir(),
				Services: map[string]*compose.Service{"s": {Image: "x", Volumes: tc.volumes}}}
			o := New(p, nil, "opossum", io.Discard)
			o.crashHint("s", tc.logs)
			got := o.recordedChownFailures()
			if tc.want == "" {
				if len(got) != 0 {
					t.Errorf("the mount is not certain, so nothing should be recorded, got %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Target != tc.want {
				t.Errorf("recorded %+v, want the mount at %s", got, tc.want)
			}
		})
	}
}

// The record says what happened once, not what to keep proposing. When the
// project moves on — the suggestion was taken, the mount was renamed, the service
// was deleted — the entry has to stop appearing, or the overlay accumulates
// proposals about mounts that no longer exist.
func TestPlanOverlayDropsRecordsTheProjectHasMovedPast(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	const before = `
name: demo
services:
  ghost:
    image: ghost:6-alpine
    volumes:
      - ./db:/var/lib/ghost/content
`
	writeFile(t, dir, "compose.yaml", before)
	p0, err := compose.Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	o := New(p0, nil, "opossum", io.Discard)
	o.crashHint("ghost", "chown: /var/lib/ghost/content: Operation not permitted")
	if _, changes := o.PlanOverlay(); len(changes) != 1 {
		t.Fatalf("the crash should leave exactly one suggestion, got %+v", changes)
	}

	// The suggestion is taken: that mount is now a named volume.
	writeFile(t, dir, "compose.yaml", `
name: demo
services:
  ghost:
    image: ghost:6-alpine
    volumes:
      - ghost-content:/var/lib/ghost/content
volumes:
  ghost-content: {}
`)
	p1, err := compose.Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// Same directory, so the record from the first crash is still on disk.
	if got := New(p1, nil, "opossum", io.Discard).recordedChownFailures(); len(got) != 1 {
		t.Fatalf("the record should still be there, got %+v", got)
	}
	body, changes := New(p1, nil, "opossum", io.Discard).PlanOverlay()
	for _, c := range changes {
		if c.Kind == "suggestion" {
			t.Errorf("the mount is already a named volume, so nothing is left to propose: %+v", c)
		}
	}
	if strings.Contains(body, suggestionMarker) {
		t.Errorf("no suggestion should survive the fix being applied:\n%s", body)
	}
}

// Detaching a directory another service is reading ends the sharing, silently.
// The applied path refuses outright for that reason. Here the crash is real, so
// the entry stays — but it has to say who else is looking, or the reader
// uncomments it and finds out afterwards.
func TestPlanOverlayNamesTheServicesSharingTheDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "content"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "compose.yaml", `
name: demo
services:
  blog:
    image: ghost:6-alpine
    volumes:
      - ./content:/var/lib/ghost/content
  backup:
    image: alpine
    volumes:
      - ./content:/backup-src
`)
	p0, err := compose.Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	o := New(p0, nil, "opossum", io.Discard)
	o.crashHint("blog", "chown: /var/lib/ghost/content: Operation not permitted")
	body, _ := o.PlanOverlay()
	if !strings.Contains(body, "backup") {
		t.Errorf("the suggestion detaches a directory %q also reads, and must say so:\n%s", "backup", body)
	}
}

// The overlay must not carry two blocks for one mount, and must not go silent
// about a crash it has no applied fix for. Both directions run through
// appliedSameMount, and neither was guarded until this test.
func TestPlanOverlaySuppressesOnlyWhenItReallyAppliedTheSameMount(t *testing.T) {
	for _, tc := range []struct {
		name, compose string
		wantSuggested bool
	}{
		{
			// Postgres from the known-image list: the swap is applied, so a suggestion
			// for the same mount would be a second block saying the same thing.
			name: "applied covers it",
			compose: `
name: demo
services:
  db:
    image: postgres:16
    volumes:
      - ./pgdata:/var/lib/postgresql/data
`,
			wantSuggested: false,
		},
		{
			// Same image and mount, but the entrypoint is overridden, so it no longer
			// runs the server that chowns — the applied path declines. Asking
			// `ownsDataDir` instead of "did we apply it" silenced this case too, and a
			// crash that really happened left no trace in the overlay at all.
			name: "applied declined, so the crash still has to be reported",
			compose: `
name: demo
services:
  db:
    image: postgres:16
    entrypoint: ["/usr/local/bin/custom-init"]
    volumes:
      - ./pgdata:/var/lib/postgresql/data
`,
			wantSuggested: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "pgdata"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, dir, "compose.yaml", tc.compose)
			p0, err := compose.Load(filepath.Join(dir, "compose.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			o := New(p0, nil, "opossum", io.Discard)
			o.crashHint("db", "chown: /var/lib/postgresql/data: Operation not permitted")
			_, changes := o.PlanOverlay()
			var suggested int
			for _, c := range changes {
				if c.Kind == "suggestion" {
					suggested++
				}
			}
			if got := suggested > 0; got != tc.wantSuggested {
				t.Errorf("suggested=%v want %v; entries were %+v", got, tc.wantSuggested, changes)
			}
		})
	}
}

// A half-written record parses as nothing, and reading nothing is
// indistinguishable from never having failed — the whole history would vanish on
// one bad moment. The write goes through a temporary file for that reason.
//
// So the test has to interrupt one. Blocking the temporary path is the only way
// to make the write fail after the record already has something in it: with the
// rename, the existing record is untouched; with a direct write, the second
// record lands and the point of the temporary file is lost. An earlier version of
// this test asserted only that the file parses and no .tmp is left over — both
// true of a direct write, so it guarded nothing.
func TestChownRecordSurvivesAnInterruptedWrite(t *testing.T) {
	dir := t.TempDir()
	p := &compose.Project{Name: "demo", BaseDir: dir, Services: map[string]*compose.Service{
		"a": {Image: "x", Volumes: []string{"./a:/data"}},
		"b": {Image: "y", Volumes: []string{"./b:/other"}},
	}}
	o := New(p, nil, "opossum", io.Discard)
	o.crashHint("a", "chown: /data: Operation not permitted")
	if got := o.recordedChownFailures(); len(got) != 1 {
		t.Fatalf("the first failure should have been recorded, got %+v", got)
	}

	// Make the temporary path unusable, so the write cannot complete.
	if err := os.MkdirAll(o.chownFailurePath()+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	o.crashHint("b", "chown: /other: Operation not permitted")

	got := o.recordedChownFailures()
	if len(got) != 1 || got[0].Service != "a" {
		t.Errorf("a write that could not complete must leave the record as it was, got %+v", got)
	}
}
