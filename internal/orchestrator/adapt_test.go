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
