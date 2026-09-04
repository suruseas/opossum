package orchestrator

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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

// The data directories below were each watched failing on the real runtime the
// same way Postgres and MySQL do — the image chowns its data directory and exits.
func TestPlanOverlayCoversMoreDatabases(t *testing.T) {
	cases := []struct{ image, dir string }{
		{"clickhouse/clickhouse-server:25.3-alpine", "/var/lib/clickhouse"},
		{"mongo:7", "/data/db"},
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
// database is still left alone. `/data/db` is mounted by all sorts of images, so
// a wrong match here would swap real data for an empty volume.
func TestPlanOverlayGenericDataDirNeedsMatchingImage(t *testing.T) {
	for _, img := range []string{"busybox", "alpine:3", "myapp/data-loader:1", "mongo-exporter:1"} {
		body, changes := planFor(t, "name: demo\nservices:\n  svc:\n    image: "+img+
			"\n    volumes:\n      - ./d:/data/db\n")
		if body != "" || len(changes) != 0 {
			t.Errorf("%s mounting /data/db is not a database; got %d change(s):\n%s", img, len(changes), body)
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
		"name: demo\nservices:\n  dump:\n    image: mysql:8\n    command: [\"mysql\",\"-e\",\"select 1\"]\n    volumes:\n      - ./dumps:/var/lib/mysql\n",
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
  db:
    image: mongo:7
    command: ["mongod", "--auth"]
    volumes:
      - ./data:/data/db
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

// Problems opossum records rather than leaving the user to rediscover them —
// and they carry no YAML, so nothing can be uncommented into effect.
func TestPlanOverlayNotes(t *testing.T) {
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
	// Spelled out rather than read from the constants the header is built from:
	// a want that reads the same value the code writes agrees with any wording,
	// including a wrong one. These are the lines a reader meets.
	for _, want := range []string{
		"— applied. This is live.",
		"— written out but commented; uncomment to apply.",
		"— things opossum writes no YAML for; recorded so",
	} {
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

// Redis and its relatives are not rewritten on sight any more, and the reason is
// that the images disagree with each other. Run on the runtime (container 1.2.2)
// and kept in testdata/error-wordings/redis-family-chown-split.txt:
// `redis:7-alpine` exits with `chown: .: Operation not permitted`;
// `redis:8-alpine` starts and writes to the host directory; `valkey/valkey:8-alpine`
// ships the same entrypoint as Valkey 7 and exits like redis 7. `redis-stack-server`
// was read rather than run: its entrypoint has no chown in it. Four images, three
// behaviours, and no version to sort them by — and the Debian-based tags were not
// run at all, which is the other half of why the name cannot decide this.
//
// Rewriting on the image's name meant a project that works today gets its host
// directory quietly swapped for a volume, and the output reads like success. The
// other mistake — leaving a mount that does need swapping — ends in a container
// that exits saying so, which is decoded and then proposed for. One is silent and
// one is loud, and the loud one already has somewhere to land.
func TestPlanOverlayLeavesRedisFamilyBindMountsAlone(t *testing.T) {
	for _, img := range []string{"redis:7-alpine", "redis:8", "valkey/valkey:8-alpine", "redis/redis-stack-server:latest"} {
		body, changes := planFor(t, "name: demo\nservices:\n  cache:\n    image: "+img+
			"\n    volumes:\n      - ./d:/data\n")
		if len(changes) != 0 {
			t.Errorf("%s: nothing has failed, so the mount stays as written; got %+v", img, changes)
		}
		if body != "" {
			t.Errorf("%s: no overlay should be written:\n%s", img, body)
		}
	}
}

// What replaces the rewrite: the same container, watched failing. Redis says
// nothing about which path it could not chown (`chown: .:`), so this is also the
// case where the mount has to be identified from the service rather than the log.
func TestPlanOverlayProposesForRedisOnceItHasActuallyDied(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "rdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "compose.yaml", `
name: demo
services:
  cache:
    image: redis:7-alpine
    volumes:
      - ./rdata:/data
`)
	p0, err := compose.Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	o := New(p0, nil, "opossum", io.Discard)
	if _, changes := o.PlanOverlay(); len(changes) != 0 {
		t.Fatalf("before any crash there is nothing to propose, got %+v", changes)
	}

	// The line the image actually prints, from the run in this repository.
	if h := o.crashHint("cache", "chown: .: Operation not permitted"); h == "" {
		t.Fatal("the crash should be decoded")
	}
	body, changes := o.PlanOverlay()
	var suggested []string
	for _, c := range changes {
		if c.Kind == "suggestion" {
			suggested = append(suggested, c.Summary)
		}
	}
	if len(suggested) != 1 || !strings.Contains(suggested[0], "/data") {
		t.Fatalf("the mount that died should be proposed, got %v", suggested)
	}
	if !strings.Contains(body, suggestionMarker) {
		t.Errorf("the entry should be a suggestion, not applied:\n%s", body)
	}
}

// The crash guidance is read by whoever is staring at a container that just died,
// and everything it says has to be true of every project that reaches it. Five
// versions of it have been wrong about something: what the next command writes,
// how it writes it, whether it offers or applies it, and once about whether
// opossum knew which mount had died at all.
//
// So the three forms are written out here word for word rather than checked for
// phrases they must not contain. A list of banned words only catches the wordings
// someone already thought of — a sixth version can be just as false without using
// any of them. Changing any of these sentences fails this test, which is the
// point: the words are the interface, and a new one should be a decision rather
// than a diff nobody reads.
//
// They take the mount and the service as arguments for the same reason. A flat
// constant with /data written into it stopped anyone noticing that the guidance
// had stopped using the mount it was given — a mutation that hardcoded /data
// passed the whole suite, which meant a Mongo container that died on /data/db
// would be told to change /data, a path it does not mount.
func crashWhyText() string {
	return "Apple `container` bind mounts are host-owned and can't be chowned from inside the container, so an " +
		"image that takes ownership of its data directory at startup fails here. A named volume can be chowned"
}

// The mount is known and the note about it survived.
func crashKnownText(target string) string {
	return "\n  → [OPSM-105] " + crashWhyText() + ". This container died on " + target + ", and opossum has that on " +
		"record: `opossum up --from-docker-compose` reads it. Changing " + target + " to a named volume is what gets " +
		"past this; what is in the host directory now stays there, and the service stops seeing it."
}

// The mount is known, but nothing could be written down about it.
func crashUnrecordedText(target string) string {
	return "\n  → [OPSM-105] " + crashWhyText() + ". This container died on " + target + ", and opossum could not " +
		"keep a note of that in this project directory — change " + target + " to a named volume yourself; what is " +
		"in the host directory now stays there, and the service stops seeing it."
}

// Which mount died cannot be told apart from the ones that are working.
func crashUnknownText(service string) string {
	return "\n  → [OPSM-105] " + crashWhyText() + ". opossum could not work out which of " + strconv.Quote(service) +
		"'s mounts this container died on — change the mount holding its data to a named volume yourself; what is " +
		"in the host directory now stays there, and the service stops seeing it."
}

func TestTheCrashGuidanceSaysOnlyWhatItKnows(t *testing.T) {
	const saysNothing = "chown: .: Operation not permitted"
	at := func(t *testing.T, dir string) *Orchestrator {
		t.Helper()
		return New(&compose.Project{Name: "demo", BaseDir: dir, Services: map[string]*compose.Service{}}, nil, "opossum", io.Discard)
	}
	unwritable := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		return dir
	}
	// Two shapes reach each of the first two forms, and they name different
	// mounts: one where nothing else could have been it, and one where the
	// container said which directory it could not take.
	saidNothing := &compose.Service{Image: "redis:7-alpine", Volumes: []string{"./rdata:/data"}}
	namedIt := &compose.Service{Image: "mongo:7", Volumes: []string{"./m:/data/db", "./conf:/etc/mongo:ro"}}
	const namedItLogs = "chown: changing ownership of '/data/db': Operation not permitted"

	t.Run("the mount is known and the note is kept", func(t *testing.T) {
		for _, c := range []struct {
			svc          *compose.Service
			logs, target string
		}{
			{saidNothing, saysNothing, "/data"},
			{namedIt, namedItLogs, "/data/db"},
		} {
			want := crashKnownText(c.target)
			if got := at(t, t.TempDir()).chownCrashHint("cache", c.svc, c.logs); got != want {
				t.Errorf("guidance changed:\n got: %q\nwant: %q", got, want)
			}
		}
	})

	t.Run("the mount is known but the note cannot be kept", func(t *testing.T) {
		for _, c := range []struct {
			svc          *compose.Service
			logs, target string
		}{
			{saidNothing, saysNothing, "/data"},
			{namedIt, namedItLogs, "/data/db"},
		} {
			want := crashUnrecordedText(c.target)
			if got := at(t, unwritable(t)).chownCrashHint("cache", c.svc, c.logs); got != want {
				t.Errorf("guidance changed:\n got: %q\nwant: %q", got, want)
			}
		}
	})

	t.Run("there is nowhere to keep the note at all", func(t *testing.T) {
		// Without a project directory the record has no home, and the path it
		// would otherwise use is relative — which lands in whatever directory the
		// process happens to be in. This runs somewhere disposable and checks
		// nothing was left there. (Defensive: every loaded project has a BaseDir.)
		dir := t.TempDir()
		t.Chdir(dir)
		want := crashUnrecordedText("/data")
		if got := at(t, "").chownCrashHint("cache", saidNothing, saysNothing); got != want {
			t.Errorf("guidance changed:\n got: %q\nwant: %q", got, want)
		}
		if _, err := os.Stat(filepath.Join(dir, ".opossum")); err == nil {
			t.Error("a project with no directory wrote its record into the working directory")
		}
	})

	// Which shapes land on the unknown form. All of them get the same words, and
	// those words state no reason — saying which would be a guess about the
	// reader's own file. The service is named, so it is passed rather than fixed.
	for _, c := range []struct {
		name, service, logs string
		svc                 *compose.Service
	}{
		{name: "several binds and a log naming none", service: "cache", logs: saysNothing,
			svc: &compose.Service{Image: "redis:7-alpine", Volumes: []string{"./rdata:/data", "./conf:/usr/local/etc/redis"}}},
		{name: "a named volume beside the bind", service: "store", logs: saysNothing,
			svc: &compose.Service{Image: "redis:7-alpine", Volumes: []string{"cachedata:/data", "./conf:/usr/local/etc/redis"}}},
		{name: "an anonymous volume beside the bind", service: "cache", logs: saysNothing,
			svc: &compose.Service{Image: "redis:7-alpine", Volumes: []string{"/data", "./conf:/usr/local/etc/redis"}}},
		{name: "a log naming a path nothing mounts", service: "db", logs: "chown: changing ownership of '/var/lib/postgresql/data': Operation not permitted",
			svc: &compose.Service{Image: "postgres:17-alpine", Volumes: []string{"./init:/docker-entrypoint-initdb.d"}}},
		{name: "mounts nested inside each other, so which one owns the path is a guess", service: "db",
			logs: "chown: changing ownership of '/var/lib/postgresql/data/pg_wal': Operation not permitted",
			svc:  &compose.Service{Image: "postgres:17-alpine", Volumes: []string{"./pg:/var/lib/postgresql/data", "./pgwal:/var/lib/postgresql/data/pg_wal"}}},
	} {
		want := crashUnknownText(c.service)
		if got := at(t, t.TempDir()).chownCrashHint(c.service, c.svc, c.logs); got != want {
			t.Errorf("%s:\n got: %q\nwant: %q", c.name, got, want)
		}
	}

	// …and the shape that must reach the known form, because it is how Redis is
	// usually written: a config file bound read-only is not a candidate and not a
	// rival, since a database cannot run on one.
	withConf := &compose.Service{Image: "redis:7-alpine", Volumes: []string{"./rdata:/data", "./redis.conf:/usr/local/etc/redis/redis.conf:ro"}}
	want := crashKnownText("/data")
	if got := at(t, t.TempDir()).chownCrashHint("cache", withConf, saysNothing); got != want {
		t.Errorf("a read-only config bind should not take the suggestion away:\n got: %q\nwant: %q", got, want)
	}
}

// A named volume beside a bind is the ordinary way to write Redis, and the volume
// is the likelier thing the image chowns. Blaming the bind would propose
// detaching a directory that is working — and say a container died on it.
func TestNothingIsBlamedWhenAVolumeCouldHaveBeenIt(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "compose.yaml", `
name: demo
services:
  cache:
    image: redis:7-alpine
    volumes:
      - cachedata:/data
      - ./conf:/usr/local/etc/redis
volumes:
  cachedata: {}
`)
	p0, err := compose.Load(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	o := New(p0, nil, "opossum", io.Discard)
	o.crashHint("cache", "chown: .: Operation not permitted")
	body, changes := o.PlanOverlay()
	for _, c := range changes {
		if c.Kind == "suggestion" {
			t.Errorf("the container never named a directory and a volume could have been it; nothing should be proposed, got %q", c.Summary)
		}
	}
	if strings.Contains(body, "/usr/local/etc/redis") {
		t.Errorf("the config bind is working; it must not appear in the overlay:\n%s", body)
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

// The other three notes' bodies, word for word — the same treatment the PGDATA
// note has had since the eleven defects that made it necessary. Until this test
// there was a note held to the word and a note held to a phrase or two, and the
// difference did not follow from anything: truncating either of these bodies to
// its first line left every package in this repository green, and left a reader
// with "Why: Apple container runs these, and it has no socket to share. If a Docker"
// and nothing after it. What the note says is now a decision, not a clause that
// can go missing.
//
// One project, one golden: the whole Notes section is compared, so a note added
// here without its own line in this file fails, and so does a line added between
// two of them.
func TestTheNotesAreWordForWordWhatWeMeanToSay(t *testing.T) {
	body := `services:
  app:
    image: alpine:3
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
  sup:
    image: alpine:3
    restart: on-failure
  usb:
    image: alpine:3
    volumes:
      - /dev/ttyUSB0:/dev/ttyUSB0
`
	overlay, changes := planWithImage(t, body, "")
	want := strings.Join([]string{
		`# ── Notes ───────────────────────────────────────────────────────────────`,
		`# These are things opossum writes no YAML for.`,
		`# [opossum note] service "app": mounts the Docker socket, which does not answer for the containers here`,
		"# Why: Apple container runs these, and it has no socket to share. If a Docker",
		`#   daemon answers on that path it is a different one, and it knows a`,
		`#   different set: a service that wants this socket in order to watch its`,
		`#   neighbours will be told about theirs.`,
		"# What to expect: if you meant that daemon, keep the mount — though a bind whose source",
		`#   is a symlink to a socket is refused before anything starts, and the`,
		`#   refusal says what to write instead.`,
		`#   If you meant these containers, nothing here answers for them.`,
		"# [opossum note] service \"sup\": uses `restart: on-failure`, which opossum can only approximate",
		"# Why: `on-failure` means restart only if the service failed. Apple container",
		`#   does not report a container's exit code, so a crash and a clean exit look`,
		`#   the same from outside. opossum treats any exit as a failure, retries a few`,
		"#   times and then stops — rather than restarting a service that may have",
		"#   finished on purpose. `always` and `unless-stopped` are honoured exactly.",
		"# What to expect: if this service is meant to keep running, `restart: always` says so",
		`#   exactly. If it is meant to finish, another service can wait for it with`,
		"#   `depends_on: {condition: service_completed_successfully}`, which also",
		`#   excludes it from supervision.`,
		`# [opossum note] service "usb": mounts the host path /dev/ttyUSB0, which is a device or session socket`,
		`# Why: Each container is its own VM. A device node cannot be handed to one:`,
		`#   /dev/dri and the like arrive as a path with nothing behind them. A`,
		`#   session socket (X11, PulseAudio) is mounted as a path too, and what`,
		`#   would answer on it is the host's own session; whether anything useful`,
		`#   comes through here has not been measured.`,
		`# What to expect: expect this service's device-dependent features not to work; no compose`,
		`#   change grants a VM access to the host's devices.`,
	}, "\n")
	if got := noteBlockOf(t, overlay); got != want {
		t.Errorf("the notes are not what this file says they should be\n got:\n%s\nwant:\n%s", got, want)
	}

	// The heading above them says opossum writes no YAML for these. Three kinds
	// of note stand under it here, and the heading is only true if none of them
	// put a services: block in the file — the sentence has to be checked against
	// the file, not only against the words it was written from.
	if strings.Contains(overlay, "\nservices:") {
		t.Errorf("the notes were introduced as things opossum writes no YAML for, and one of them wrote some:\n%s", overlay)
	}

	// The summary lines too: when the notes are all there is, no overlay is
	// written and these are what stands next to the body on the terminal.
	wantSummaries := map[string]string{
		"app": `mounts the Docker socket, which does not answer for the containers here`,
		"sup": "uses `restart: on-failure`, which opossum can only approximate",
		"usb": `mounts the host path /dev/ttyUSB0, which is a device or session socket`,
	}
	if len(changes) != len(wantSummaries) {
		t.Fatalf("three notes, %d planned: %+v", len(changes), changes)
	}
	for _, c := range changes {
		if want := wantSummaries[c.Service]; c.Summary != want {
			t.Errorf("service %q: the summary is not what this file says it should be\n got: %s\nwant: %s", c.Service, c.Summary, want)
		}
	}
}

// suggestionBlockOf returns the Suggestions section, whole — the same shape as
// noteBlockOf, and for the same reason. From the section header rather than from
// the first marker, because a line added just above a suggestion reaches the same
// reader.
func suggestionBlockOf(t *testing.T, overlay string) string {
	t.Helper()
	i := strings.Index(overlay, "# \u2500\u2500 Suggestions")
	if i < 0 {
		t.Fatalf("no suggestion in this overlay:\n%s", overlay)
	}
	var out []string
	for _, line := range strings.Split(overlay[i:], "\n") {
		if !strings.HasPrefix(line, "#") {
			break
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// The Suggestions section, word for word.
//
// The Notes section has had this since the day a note's body could go missing
// without anything saying so. The Suggestions section did not, and a mutation
// sweep of adapt.go found what that cost: thirteen ways to exchange two of the
// strings a suggestion is built from — a service name for a volume name, a host
// path for a container path, a section label for the sentence written under it —
// and every one of them left this repository green. Eleven of the thirteen build
// the commented block; the other two build the summary that goes to the terminal,
// which is why both are read here.
//
// A suggestion that names the wrong path is worse than no suggestion: it is
// written into the user's compose.opossum.yaml and read later, away from
// whatever made it.
//
// One project, one golden, and both kinds of suggestion inside it: a line added
// between two of these fails, and so does a suggestion that grows a sentence.
//
// Thirteen more exchanges of the same kind survive in adapt.go, in the summaries
// and comments for the changes opossum did apply. Nothing here reads those.
func TestTheSuggestionsAreWordForWordWhatWeMeanToSay(t *testing.T) {
	dir := t.TempDir()
	// Both kinds of suggestion in one project, because the section is what this
	// pins: a service that died chowning a bind mount, and a pair that cannot both
	// attach the same named volume. A golden over only one of them leaves the other
	// half of the section with nothing reading it.
	path := writeFile(t, dir, "compose.yaml", `services:
  blog:
    image: ghost:6-alpine
    volumes:
      - ./content:/var/lib/ghost/content
  web:
    image: busybox
    volumes:
      - shared:/srv
  worker:
    image: busybox
    volumes:
      - shared:/srv
volumes:
  shared: {}
`)
	p, err := compose.Load(path)
	if err != nil {
		t.Fatalf("loading test compose: %v", err)
	}
	o := New(p, nil, "opossum", io.Discard)
	o.crashHint("blog", "chown: changing ownership of '/var/lib/ghost/content': Operation not permitted")
	overlay, changes := o.PlanOverlay()

	// The summaries too, in the same test and from the same run: they are what
	// stands on the terminal, and they are built from the same names by their own
	// format call. The Notes test does this for its own three; this one had
	// nothing reading it at all.
	wantSummaries := map[string]string{
		"blog":   `service "blog" died taking ownership of /var/lib/ghost/content; a named volume can be chowned, a bind mount cannot`,
		"web":    `service "web" shares named volume "shared" with 1 other service(s); a bind mount would let them all run`,
		"worker": `service "worker" shares named volume "shared" with 1 other service(s); a bind mount would let them all run`,
	}
	if len(changes) != len(wantSummaries) {
		t.Fatalf("three suggestions, %d planned: %+v", len(changes), changes)
	}
	for _, c := range changes {
		want, ok := wantSummaries[c.Service]
		if !ok {
			t.Errorf("a suggestion for %q, which this file does not expect: %s", c.Service, c.Summary)
			continue
		}
		if c.Summary != want {
			t.Errorf("service %q: the summary is not what this file says it should be\n got: %s\nwant: %s",
				c.Service, c.Summary, want)
		}
	}

	want := strings.Join([]string{
		`# ── Suggestions ─────────────────────────────────────────────────────────`,
		`# opossum did NOT make these changes: each alters what the project means, so`,
		"# the call is yours. Uncommenting a whole block applies it (the `services:`",
		`# line included — this file is merged like any compose file).`,
		`# services:`,
		`#   # [opossum suggestion — NOT APPLIED] service "web": mount the shared data from a host directory instead of the named volume "shared".`,
		`#   # Why: Apple container attaches a named volume to one container at a time, so`,
		`#   #   "web" and "worker" cannot all run while they share "shared" — whichever starts`,
		`#   #   first gets it and the rest fail to attach. Diagnostic: OPSM-102.`,
		`#   #   A host directory is shareable, so a bind mount lets them all mount it.`,
		`#   #   NOT APPLIED, because it changes what the project means: the data moves out`,
		`#   #   of the runtime's storage onto the Mac, and that directory's contents and`,
		`#   #   permissions become yours to manage. A database's data directory in`,
		`#   #   particular should NOT be moved this way — it needs to be chownable.`,
		`#   # To apply: uncomment the block for every service sharing "shared" (all of them,`,
		"#   #   or none), create the directory, then `opossum up`. `opossum ps`",
		`#   #   should show them all running.`,
		`#   # To ignore: delete this block. Nothing here is in effect until you uncomment it.`,
		`#   web:`,
		`#     volumes:`,
		`#       - ./shared:/srv`,
		`#   # [opossum suggestion — NOT APPLIED] service "worker": mount the shared data from a host directory instead of the named volume "shared".`,
		`#   # Why: Apple container attaches a named volume to one container at a time, so`,
		`#   #   "web" and "worker" cannot all run while they share "shared" — whichever starts`,
		`#   #   first gets it and the rest fail to attach. Diagnostic: OPSM-102.`,
		`#   #   A host directory is shareable, so a bind mount lets them all mount it.`,
		`#   #   NOT APPLIED, because it changes what the project means: the data moves out`,
		`#   #   of the runtime's storage onto the Mac, and that directory's contents and`,
		`#   #   permissions become yours to manage. A database's data directory in`,
		`#   #   particular should NOT be moved this way — it needs to be chownable.`,
		`#   # To apply: uncomment the block for every service sharing "shared" (all of them,`,
		"#   #   or none), create the directory, then `opossum up`. `opossum ps`",
		`#   #   should show them all running.`,
		`#   # To ignore: delete this block. Nothing here is in effect until you uncomment it.`,
		`#   worker:`,
		`#     volumes:`,
		`#       - ./shared:/srv`,
		`#   # [opossum suggestion — NOT APPLIED] service "blog": use a named volume for /var/lib/ghost/content instead of the host path "./content".`,
		`#   # Why: This is not a guess. That container started, tried to take ownership of`,
		`#   #   /var/lib/ghost/content, and exited: Apple container bind mounts are`,
		`#   #   host-owned and cannot be chowned from inside. A named volume can.`,
		`#   #   Diagnostic: OPSM-105.`,
		`#   #   NOT APPLIED, because it moves where the data lives. Today it is on the`,
		`#   #   Mac at ./content and you can open it; afterwards it`,
		`#   #   is inside a volume that only the runtime manages. Anything already in`,
		`#   #   that directory stays where it is and the service stops seeing it.`,
		"#   # To apply: uncomment this block, then `opossum up` again. To keep the data on the",
		`#   #   Mac instead, run the service as the user that owns the directory`,
		"#   #   (`user:` in the compose file) so it has nothing to chown.",
		`#   # To ignore: delete this block. Nothing here is in effect until you uncomment it.`,
		`#   blog:`,
		`#     volumes:`,
		`#       - blog-content:/var/lib/ghost/content`,
		`# volumes:`,
		`#   blog-content: {}`,
	}, "\n")
	if got := suggestionBlockOf(t, overlay); got != want {
		t.Errorf("the suggestions are not what this file says they should be\n got:\n%s\nwant:\n%s", got, want)
	}
}

// A service whose env_file could not be read gets no adaptation drawn from its
// environment. The questions adaptService asks are all answered out of it, and
// an unreadable env_file looks exactly like a variable nobody set — so PGDATA
// reads as absent and the overlay sets it, over the value the user was
// supplying at run time. That is the damage the PGDATA guard exists to
// prevent, and the overlay outlives the run that wrote it: `up` stopping does
// not take the file back (#413).
//
// What this does not say is that the service goes quiet. The notes and the
// shared-volume suggester read volumes rather than the environment, so they
// still speak about this service, and the two cases at the end ask them to.
// (The chown-failure suggester reads recorded failures and is the same shape;
// producing one takes a run, so it is not asked here.)
func TestPlanOverlaySaysNothingAboutAServiceWhoseEnvCouldNotBeRead(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pg.env", "PGDATA=${MY_PGDATA:?set MY_PGDATA}\n")
	body, changes := planForIn(t, dir, `
name: demo
services:
  db:
    image: postgres:16
    env_file: [pg.env]
    volumes:
      - dbdata:/var/lib/postgresql/data
volumes:
  dbdata: {}
`)
	if len(changes) != 0 {
		t.Errorf("the planner cannot see this service's environment, so nothing it "+
			"concludes from one applies here; got %+v", changes)
	}
	if strings.Contains(body, "PGDATA") {
		t.Errorf("the overlay would set PGDATA over one the env_file is supplying, got:\n%s", body)
	}
	// The same project without the unreadable file does produce the change, so
	// this is measuring the environment being unreadable and not the shape of
	// the compose file.
	if _, sane := planFor(t, `
name: demo
services:
  db:
    image: postgres:16
    volumes:
      - dbdata:/var/lib/postgresql/data
volumes:
  dbdata: {}
`); len(sane) != 1 {
		t.Fatalf("the control case should still plan one change, got %+v", sane)
	}

	// And the bind-mounted road, which is the other half of the same fix and a
	// worse thing to get wrong: the swap moves where the data lives, so an
	// overlay written about a service nobody could read leaves the host
	// directory behind. Asking only about PGDATA would leave a guard that
	// covers half of what the planner does and looks complete.
	dir2 := t.TempDir()
	writeFile(t, dir2, "pg.env", "PGDATA=${MY_PGDATA:?set MY_PGDATA}\n")
	body2, changes2 := planForIn(t, dir2, `
name: demo
services:
  db:
    image: postgres:16
    env_file: [pg.env]
    volumes:
      - ./pgdata:/var/lib/postgresql/data
`)
	if len(changes2) != 0 {
		t.Errorf("a bind-mounted data dir on a service whose environment is unreadable is "+
			"still a service nothing can be concluded about; got %+v", changes2)
	}
	if strings.Contains(body2, "db-data") {
		t.Errorf("the overlay would move this service's data to a named volume on the "+
			"strength of an environment nobody read, got:\n%s", body2)
	}

	// And the part that does not depend on the environment still comes out. A
	// note about `restart:` or a device mount is not a conclusion drawn from
	// anything; withholding it would make an unreadable env_file the reason a
	// different thing went unmentioned, which is the mystery those notes exist
	// to prevent.
	dir3 := t.TempDir()
	writeFile(t, dir3, "pg.env", "PGDATA=${MY_PGDATA:?set MY_PGDATA}\n")
	_, notes := planForIn(t, dir3, `
name: demo
services:
  app:
    image: app
    restart: on-failure
    env_file: [pg.env]
`)
	var codes []string
	for _, n := range notes {
		codes = append(codes, n.Code)
	}
	if !slices.Contains(codes, "OPSM-409") {
		t.Errorf("the restart note reads no environment, so an unreadable env_file is not "+
			"a reason to withhold it; got %v", codes)
	}
	for _, n := range notes {
		if n.Kind != "note" {
			t.Errorf("only what reads no environment may come from a service whose "+
				"environment is unreadable, got %+v", n)
		}
	}

	// And the shared-volume suggester, which runs outside adaptService, still
	// names the service. It reads volumes; an unreadable env_file tells it
	// nothing, so withholding its advice would be withholding it for no reason.
	dir4 := t.TempDir()
	writeFile(t, dir4, "pg.env", "PGDATA=${MY_PGDATA:?set MY_PGDATA}\n")
	_, shared := planForIn(t, dir4, `
name: demo
services:
  a:
    image: app
    env_file: [pg.env]
    volumes:
      - shared:/data
  b:
    image: app
    volumes:
      - shared:/data
volumes:
  shared: {}
`)
	var named bool
	for _, n := range shared {
		if n.Service == "a" {
			named = true
		}
	}
	if !named {
		t.Errorf("the shared-volume suggester reads volumes, not the environment, so an "+
			"unreadable env_file is not a reason to drop this service from it; got %+v", shared)
	}
}

// A service gated behind a profile that is not active is left out of the
// plan. It is not started and not shown by `config` — docker compose leaves
// it out there too — and an entry about it would ask the reader to judge
// whether a change to a service that is not running is theirs to care about.
// What enables it enables the plan's eye on it as well: the profile, or naming
// the service, as under docker compose. (A service with no profile is looked
// at whether or not this run starts it — that is unchanged.) What was left
// out comes back by name, in order, so the caller can say so (#492).
func TestPlanOverlayLeavesOutServicesGatedByAnInactiveProfile(t *testing.T) {
	const body = `name: prof
services:
  web:
    image: alpine:3
  db:
    image: postgres:16
    profiles: [debug]
    volumes:
      - pgdata:/var/lib/postgresql/data
volumes:
  pgdata: {}
`
	o := New(loadProject(t, body), nil, "opossum", io.Discard)
	text, changes, gated := o.PlanOverlayFor(nil)
	if len(changes) != 0 || text != "" {
		t.Errorf("db is gated and not enabled, so nothing should be planned; got %d change(s):\n%s", len(changes), text)
	}
	if len(gated) != 1 || gated[0] != "db" {
		t.Errorf("the service left out should be named, got %v", gated)
	}
	// Two left out come back in name order, whatever order the map gave them.
	two := loadProject(t, strings.Replace(body, "volumes:\n  pgdata: {}\n",
		"  zeta:\n    image: alpine:3\n    profiles: [debug]\n  alpha:\n    image: alpine:3\n    profiles: [debug]\nvolumes:\n  pgdata: {}\n", 1))
	// Asked more than once: a map walk happens to come out sorted often enough
	// that a single ask would let the sort go missing unnoticed.
	for i := 0; i < 8; i++ {
		_, _, gated = New(two, nil, "opossum", io.Discard).PlanOverlayFor(nil)
		if got, want := strings.Join(gated, ","), "alpha,db,zeta"; got != want {
			t.Fatalf("gated = %v, want %v", got, want)
		}
	}

	o = New(loadProject(t, body), nil, "opossum", io.Discard)
	o.EnableProfiles([]string{"debug"})
	_, changes, gated = o.PlanOverlayFor(nil)
	if len(changes) == 0 || len(gated) != 0 {
		t.Errorf("with the profile enabled db is looked at: changes=%d gated=%v", len(changes), gated)
	}

	o = New(loadProject(t, body), nil, "opossum", io.Discard)
	_, changes, gated = o.PlanOverlayFor([]string{"db"})
	if len(changes) == 0 || len(gated) != 0 {
		t.Errorf("naming db enables it, as under docker compose: changes=%d gated=%v", len(changes), gated)
	}
}
