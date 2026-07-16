package compose

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDevelopWatch(t *testing.T) {
	p, err := Load(writeTemp(t, `
name: demo
services:
  app:
    image: app
    develop:
      watch:
        - action: sync
          path: ./src
          target: /app/src
          ignore:
            - node_modules/
        - action: rebuild
          path: ./go.mod
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	d := p.Services["app"].Develop
	if d == nil || len(d.Watch) != 2 {
		t.Fatalf("Develop.Watch = %+v, want 2 rules", d)
	}
	if w := d.Watch[0]; w.Action != "sync" || w.Path != "./src" || w.Target != "/app/src" || len(w.Ignore) != 1 || w.Ignore[0] != "node_modules/" {
		t.Errorf("watch[0] = %+v", w)
	}
	if w := d.Watch[1]; w.Action != "rebuild" || w.Path != "./go.mod" {
		t.Errorf("watch[1] = %+v", w)
	}
	// `develop` is a known key, so it must not surface as an unsupported field.
	for _, u := range p.Services["app"].Unsupported {
		if u == "develop" {
			t.Errorf("develop should be a supported key, got it in Unsupported")
		}
	}
}

// The thin run-option passthroughs parse into the right fields (and are known
// keys, so they don't surface as unsupported).
func TestLoadRunOptions(t *testing.T) {
	p, err := Load(writeTemp(t, `
services:
  app:
    image: app
    user: "1000:1000"
    working_dir: /srv
    init: true
    read_only: true
    cap_add: [NET_ADMIN, SYS_TIME]
    cap_drop: [ALL]
    network_mode: none
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s := p.Services["app"]
	if s.User != "1000:1000" || s.WorkingDir != "/srv" || !s.Init || !s.ReadOnly {
		t.Errorf("user/working_dir/init/read_only = %q/%q/%v/%v", s.User, s.WorkingDir, s.Init, s.ReadOnly)
	}
	if s.NetworkMode != NetworkModeNone {
		t.Errorf("network_mode = %q, want %q", s.NetworkMode, NetworkModeNone)
	}
	if !reflect.DeepEqual([]string(s.CapAdd), []string{"NET_ADMIN", "SYS_TIME"}) {
		t.Errorf("cap_add = %v", s.CapAdd)
	}
	if !reflect.DeepEqual([]string(s.CapDrop), []string{"ALL"}) {
		t.Errorf("cap_drop = %v", s.CapDrop)
	}
	for _, u := range s.Unsupported {
		switch u {
		case "user", "working_dir", "init", "read_only", "cap_add", "cap_drop", "network_mode":
			t.Errorf("%q should be a supported key, not in Unsupported", u)
		}
	}
}

// Only network_mode: none is acted on; any other value (e.g. host) is ignored —
// the service loads and joins the project network, and network_mode is reported
// as an ignored field — rather than failing the whole file. Rejecting it would
// break real-world compose files (e.g. plex uses network_mode: host).
func TestLoadIgnoresUnsupportedNetworkMode(t *testing.T) {
	p, err := Load(writeTemp(t, `
services:
  app:
    image: app
    network_mode: host
`))
	if err != nil {
		t.Fatalf("an unsupported network_mode should load (ignored), got error: %v", err)
	}
	s := p.Services["app"]
	if s.NetworkMode != "" {
		t.Errorf("unsupported network_mode should be cleared so it doesn't reach the orchestrator, got %q", s.NetworkMode)
	}
	found := false
	for _, u := range s.Unsupported {
		if u == "network_mode" {
			found = true
		}
	}
	if !found {
		t.Errorf("network_mode should be reported as ignored, got Unsupported=%v", s.Unsupported)
	}
	// network_mode: none is still acted on (not reported as ignored).
	pn, err := Load(writeTemp(t, "services:\n  app:\n    image: app\n    network_mode: none\n"))
	if err != nil {
		t.Fatalf("network_mode: none should load: %v", err)
	}
	if pn.Services["app"].NetworkMode != NetworkModeNone {
		t.Errorf("network_mode: none should be preserved, got %q", pn.Services["app"].NetworkMode)
	}
}

// Top-level networks: (internal/external/name) and per-service networks: (list
// and map forms) parse; a declared network is not listed as an ignored key.
func TestLoadNetworks(t *testing.T) {
	p, err := Load(writeTemp(t, `
name: demo
networks:
  caged:
    internal: true
  shared:
    external: true
    name: prod-shared
services:
  agent:
    image: agent
    networks: [caged]
  gw:
    image: gw
    networks:
      shared:
        aliases: [edge]
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if d := p.Networks["caged"]; !d.Internal || d.External {
		t.Errorf("caged decl = %+v, want internal", d)
	}
	if d := p.Networks["shared"]; !d.External || d.Name != "prod-shared" {
		t.Errorf("shared decl = %+v, want external name=prod-shared", d)
	}
	if got := []string(p.Services["agent"].Networks); len(got) != 1 || got[0] != "caged" {
		t.Errorf("agent networks (list form) = %v, want [caged]", got)
	}
	if got := []string(p.Services["gw"].Networks); len(got) != 1 || got[0] != "shared" {
		t.Errorf("gw networks (map form) = %v, want [shared]", got)
	}
	for _, u := range p.Unsupported {
		if u == "networks" {
			t.Errorf("networks should be acted on, not listed as ignored")
		}
	}
}

func TestLoadRejectsUndefinedNetwork(t *testing.T) {
	_, err := Load(writeTemp(t, `
services:
  app:
    image: app
    networks: [missing]
`))
	if err == nil || !strings.Contains(err.Error(), "undefined network") {
		t.Fatalf("expected an undefined-network error, got %v", err)
	}
}

// A service may join several declared networks; all must be declared top-level.
func TestLoadMultipleNetworks(t *testing.T) {
	p, err := Load(writeTemp(t, `
networks:
  a: {}
  b: {}
services:
  app:
    image: app
    networks: [a, b]
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := []string(p.Services["app"].Networks); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("app networks = %v, want [a b]", got)
	}
}

// Every named network — even in a multi-entry list — must be declared top-level.
func TestLoadRejectsUndefinedNetworkInList(t *testing.T) {
	_, err := Load(writeTemp(t, `
networks:
  a: {}
services:
  app:
    image: app
    networks: [a, missing]
`))
	if err == nil || !strings.Contains(err.Error(), "undefined network") {
		t.Fatalf("expected an undefined-network error, got %v", err)
	}
}

// internal + external on one network is contradictory (external is used as-is,
// so internal — and its egress guarantee — would be silently dropped).
func TestLoadRejectsInternalAndExternalNetwork(t *testing.T) {
	_, err := Load(writeTemp(t, `
networks:
  bad:
    internal: true
    external: true
services:
  app:
    image: app
    networks: [bad]
`))
	if err == nil || !strings.Contains(err.Error(), "internal and external") {
		t.Fatalf("expected an internal+external conflict error, got %v", err)
	}
}

// A null/empty `networks:` value is benign (no networks), not a parse error.
func TestLoadNullNetworksIsEmpty(t *testing.T) {
	p, err := Load(writeTemp(t, `
services:
  app:
    image: app
    networks:
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := p.Services["app"].Networks; len(got) != 0 {
		t.Errorf("null networks should be empty, got %v", got)
	}
}

func TestLoadRejectsNetworkModeNoneWithNetworks(t *testing.T) {
	_, err := Load(writeTemp(t, `
networks:
  a: {}
services:
  app:
    image: app
    network_mode: none
    networks: [a]
`))
	if err == nil || !strings.Contains(err.Error(), "network_mode: none and networks") {
		t.Fatalf("expected a none+networks conflict error, got %v", err)
	}
}

func TestLoadAndOrder(t *testing.T) {
	p, err := Load(writeTemp(t, `
name: demo
services:
  web:
    build: ./web
    ports: ["8080:8080"]
    environment:
      DATABASE_URL: postgres://db:5432/app
    depends_on: [db, cache]
  db:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: secret
  cache:
    image: redis:7
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if p.Name != "demo" {
		t.Errorf("name = %q, want demo", p.Name)
	}
	order, err := p.StartupOrder()
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	// web must come last; its deps (cache, db) before it.
	if order[len(order)-1] != "web" {
		t.Errorf("web should start last, got order %v", order)
	}
	if !before(order, "db", "web") || !before(order, "cache", "web") {
		t.Errorf("dependencies not ordered before web: %v", order)
	}

	// environment map normalized to KEY=value
	want := []string{"POSTGRES_PASSWORD=secret"}
	if !reflect.DeepEqual([]string(p.Services["db"].Environment), want) {
		t.Errorf("db env = %v, want %v", p.Services["db"].Environment, want)
	}
}

func TestVolumesLongFormNormalized(t *testing.T) {
	// A real docker-compose file mixes the short string form with the long
	// mapping form (type/source/target/read_only). Both must normalize to the
	// short `source:target[:ro]` string the orchestrator understands (#74).
	p, err := Load(writeTemp(t, `
name: demo
services:
  web:
    image: nginx
    volumes:
      - "shortvol:/short"
      - type: bind
        source: ./proxy/nginx.conf
        target: /etc/nginx/conf.d/default.conf
        read_only: true
      - type: volume
        source: datavol
        target: /var/lib/data
      - type: volume
        target: /anon
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := []string(p.Services["web"].Volumes)
	want := []string{
		"shortvol:/short",
		"./proxy/nginx.conf:/etc/nginx/conf.d/default.conf:ro",
		"datavol:/var/lib/data",
		"/anon",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("volumes = %v, want %v", got, want)
	}
}

func TestVolumesLongFormMissingTarget(t *testing.T) {
	// A long-form entry with no target can't be normalized — reject clearly
	// rather than mount something wrong.
	_, err := Load(writeTemp(t, `
name: demo
services:
  web:
    image: nginx
    volumes:
      - type: volume
        source: datavol
`))
	if err == nil {
		t.Fatal("expected an error for a long-form volume missing target")
	}
}

func TestTopLevelExternalVolumeParsed(t *testing.T) {
	// A top-level `volumes:` block parses; `external: true` is recorded while a
	// plain declaration (null value) is not flagged external (#64).
	p, err := Load(writeTemp(t, `
name: demo
services:
  db:
    image: postgres:16
    volumes:
      - shared:/ext
      - pgdata:/data
volumes:
  shared:
    external: true
  pgdata:
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !p.Volumes["shared"].External {
		t.Errorf("shared should be external, got %+v", p.Volumes["shared"])
	}
	if p.Volumes["pgdata"].External {
		t.Errorf("pgdata should not be external, got %+v", p.Volumes["pgdata"])
	}
}

func TestSecretsParsedShortAndLong(t *testing.T) {
	// Top-level file secrets plus service refs in both short (name) and long
	// (source/target) form parse into the project (#76).
	p, err := Load(writeTemp(t, `
name: demo
secrets:
  db-password:
    file: ./pw.txt
  api-key:
    file: ./api.txt
services:
  db:
    image: postgres:16
    secrets:
      - db-password
      - source: api-key
        target: api_key
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := p.Secrets["db-password"].File; got != "./pw.txt" {
		t.Errorf("top-level secret file = %q, want ./pw.txt", got)
	}
	refs := p.Services["db"].Secrets
	if len(refs) != 2 || refs[0] != (SecretRef{Source: "db-password", Target: "db-password"}) ||
		refs[1] != (SecretRef{Source: "api-key", Target: "api_key"}) {
		t.Errorf("service secret refs = %+v", refs)
	}
	// secrets must not be flagged as an ignored/unsupported field.
	if indexOfStr(p.Unsupported, "secrets") >= 0 || indexOfStr(p.Services["db"].Unsupported, "secrets") >= 0 {
		t.Errorf("secrets should be supported, not flagged: top=%v svc=%v", p.Unsupported, p.Services["db"].Unsupported)
	}
}

func TestSecretUndefinedRejected(t *testing.T) {
	_, err := Load(writeTemp(t, `
name: demo
services:
  db:
    image: postgres:16
    secrets:
      - missing
`))
	if err == nil {
		t.Fatal("expected an error for a service referencing an undefined secret")
	}
}

func TestSecretExternalRejected(t *testing.T) {
	_, err := Load(writeTemp(t, `
name: demo
secrets:
  db-password:
    external: true
services:
  db:
    image: postgres:16
    secrets:
      - db-password
`))
	if err == nil {
		t.Fatal("expected an error for an external (non-file) secret")
	}
}

func TestSecretTargetPathRejected(t *testing.T) {
	_, err := Load(writeTemp(t, `
name: demo
secrets:
  s:
    file: ./s.txt
services:
  db:
    image: postgres:16
    secrets:
      - source: s
        target: ../escape
`))
	if err == nil {
		t.Fatal("expected an error for a secret target with a path separator")
	}
}

func indexOfStr(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return -1
}

func TestPlatformParsed(t *testing.T) {
	// A service `platform:` is parsed (not flagged unsupported) and surfaced in
	// the resolved config; the orchestrator maps it to `container run --platform`.
	p, err := Load(writeTemp(t, `
name: demo
services:
  cache:
    image: redislabs/redismod
    platform: linux/amd64
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	svc := p.Services["cache"]
	if svc.Platform != "linux/amd64" {
		t.Errorf("Platform = %q, want linux/amd64", svc.Platform)
	}
	for _, u := range svc.Unsupported {
		if u == "platform" {
			t.Errorf("platform must not be reported as unsupported, got %v", svc.Unsupported)
		}
	}
	out, err := RenderConfig(p)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	if !strings.Contains(out, "platform: linux/amd64") {
		t.Errorf("config should surface the platform, got:\n%s", out)
	}
}

func TestBuildTargetParsed(t *testing.T) {
	// A multi-stage build target is parsed and surfaced in the resolved config
	// (it reaches `container build --target` in the orchestrator layer, #75).
	p, err := Load(writeTemp(t, `
name: demo
services:
  api:
    build:
      context: ./api
      target: builder
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := p.Services["api"].Build.Target; got != "builder" {
		t.Errorf("Build.Target = %q, want builder", got)
	}
	out, err := RenderConfig(p)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	if !strings.Contains(out, "target: builder") {
		t.Errorf("config should surface the build target, got:\n%s", out)
	}
}

func TestVolumesLongFormUnsupportedType(t *testing.T) {
	// An unsupported long-form type must error clearly rather than be silently
	// normalized into a host bind mount (#74). bind/volume/tmpfs are supported.
	_, err := Load(writeTemp(t, `
name: demo
services:
  web:
    image: nginx
    volumes:
      - type: npipe
        target: /x
`))
	if err == nil {
		t.Fatal("expected an error for an unsupported volume type")
	}
}

func TestServiceLevelTmpfs(t *testing.T) {
	// The service-level `tmpfs:` field (string or list) is accepted and folded
	// together with any volume-form `type: tmpfs` entries (#93/#79).
	p, err := Load(writeTemp(t, `
name: demo
services:
  web:
    image: nginx
    tmpfs:
      - /run
    volumes:
      - type: tmpfs
        target: /cache
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	web := p.Services["web"]
	got := []string(web.Tmpfs)
	has := func(x string) bool {
		for _, v := range got {
			if v == x {
				return true
			}
		}
		return false
	}
	if !has("/run") || !has("/cache") {
		t.Errorf("Tmpfs should hold both service-level (/run) and volume-form (/cache) targets, got %v", got)
	}
	if indexOfStr(web.Unsupported, "tmpfs") >= 0 {
		t.Errorf("tmpfs should be a supported field, not flagged: %v", web.Unsupported)
	}
}

func TestServiceLevelTmpfsScalar(t *testing.T) {
	// The scalar form (`tmpfs: /run`) is also accepted, matching the documented
	// "string or list" contract (#93).
	p, err := Load(writeTemp(t, `
name: demo
services:
  web:
    image: nginx
    tmpfs: /run
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := []string(p.Services["web"].Tmpfs); len(got) != 1 || got[0] != "/run" {
		t.Errorf("scalar tmpfs should parse to [/run], got %v", got)
	}
}

func TestVolumesTmpfsSplitFromMounts(t *testing.T) {
	// A `type: tmpfs` entry is moved out of Volumes into Tmpfs (it becomes a
	// `--tmpfs` mount, not a `-v` one); bind/named entries in the same list stay
	// in Volumes (#79).
	p, err := Load(writeTemp(t, `
name: demo
services:
  web:
    image: nginx
    volumes:
      - pgdata:/data
      - type: tmpfs
        target: /tmp
      - ./host:/mnt
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	web := p.Services["web"]
	if got := []string(web.Volumes); len(got) != 2 || got[0] != "pgdata:/data" || got[1] != "./host:/mnt" {
		t.Errorf("Volumes should keep only bind/named mounts, got %v", got)
	}
	if len(web.Tmpfs) != 1 || web.Tmpfs[0] != "/tmp" {
		t.Errorf("Tmpfs should hold /tmp, got %v", web.Tmpfs)
	}
	// The marker must never leak into the mount list.
	for _, m := range web.Volumes {
		if strings.Contains(m, "tmpfs") {
			t.Errorf("tmpfs marker leaked into Volumes: %q", m)
		}
	}
}

func TestCycleDetected(t *testing.T) {
	p, err := Load(writeTemp(t, `
services:
  a: {image: x, depends_on: [b]}
  b: {image: y, depends_on: [a]}
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := p.StartupOrder(); err == nil {
		t.Fatal("expected cycle error, got nil")
	}
}

func TestUnknownDependency(t *testing.T) {
	_, err := Load(writeTemp(t, `
services:
  a: {image: x, depends_on: [missing]}
`))
	if err == nil {
		t.Fatal("expected error for unknown dependency")
	}
}

func TestServiceNeedsImageOrBuild(t *testing.T) {
	_, err := Load(writeTemp(t, `
services:
  a: {ports: ["1:1"]}
`))
	if err == nil {
		t.Fatal("expected error for service with neither image nor build")
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"My App":  "my-app",
		"opossum": "opossum",
		"--x--":   "x",
		"":        "opossum",
		"a/b c.d": "a-b-c-d",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func before(order []string, a, b string) bool {
	ai, bi := -1, -1
	for i, n := range order {
		if n == a {
			ai = i
		}
		if n == b {
			bi = i
		}
	}
	return ai >= 0 && bi >= 0 && ai < bi
}

func condOf(deps DependsOn, name string) string {
	for _, d := range deps {
		if d.Name == name {
			return d.Condition
		}
	}
	return ""
}

func TestDependsOnConditionParsing(t *testing.T) {
	// Long map form carries per-target conditions; the short list form implies
	// service_started.
	p, err := Load(writeTemp(t, `
services:
  web:
    image: web
    depends_on:
      db:
        condition: service_healthy
      cache:
        condition: service_started
  worker:
    image: worker
    depends_on: [db]
  db:
    image: postgres
    healthcheck:
      test: ["CMD", "pg_isready"]
  cache:
    image: redis
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := condOf(p.Services["web"].DependsOn, "db"); got != ConditionHealthy {
		t.Errorf("web->db condition = %q, want %q", got, ConditionHealthy)
	}
	if got := condOf(p.Services["web"].DependsOn, "cache"); got != ConditionStarted {
		t.Errorf("web->cache condition = %q, want %q", got, ConditionStarted)
	}
	if got := condOf(p.Services["worker"].DependsOn, "db"); got != ConditionStarted {
		t.Errorf("short-form worker->db condition = %q, want %q", got, ConditionStarted)
	}
	// Ordering still works through the new shape.
	order, err := p.StartupOrder()
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	if !before(order, "db", "web") || !before(order, "cache", "web") {
		t.Errorf("deps should precede web: %v", order)
	}
}

// NONE disables the healthcheck (which gates whether a service_healthy
// dependency is even valid), and CMD-SHELL wraps the command in `sh -c`.
// Previously only CMD and the bare-string forms were tested.
func TestHealthcheckNoneAndCmdShell(t *testing.T) {
	p, err := Load(writeTemp(t, `
services:
  off:
    image: x
    healthcheck:
      test: ["NONE"]
  shell:
    image: y
    healthcheck:
      test: ["CMD-SHELL", "curl -f http://localhost || exit 1"]
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if hc := p.Services["off"].Healthcheck; hc == nil || !hc.Disabled {
		t.Errorf("NONE should disable the healthcheck, got %+v", hc)
	}
	if hc := p.Services["shell"].Healthcheck; hc == nil ||
		!reflect.DeepEqual(hc.Test, []string{"sh", "-c", "curl -f http://localhost || exit 1"}) {
		t.Errorf("CMD-SHELL should wrap in sh -c, got %v", hc)
	}
}

func TestHealthcheckParsing(t *testing.T) {
	p, err := Load(writeTemp(t, `
services:
  db:
    image: postgres
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "postgres"]
      interval: 5s
      timeout: 3s
      retries: 7
      start_period: 2s
  cache:
    image: redis
    healthcheck:
      test: redis-cli ping
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	db := p.Services["db"].Healthcheck
	if db == nil {
		t.Fatal("db healthcheck not parsed")
	}
	// CMD form drops the "CMD" directive and passes argv straight through.
	if !reflect.DeepEqual(db.Test, []string{"pg_isready", "-U", "postgres"}) {
		t.Errorf("db test argv = %v", db.Test)
	}
	if db.Interval != 5*time.Second || db.Timeout != 3*time.Second || db.StartPeriod != 2*time.Second {
		t.Errorf("db durations = interval %v, timeout %v, start_period %v", db.Interval, db.Timeout, db.StartPeriod)
	}
	if db.Retries != 7 {
		t.Errorf("db retries = %d, want 7", db.Retries)
	}

	// A bare string test runs through a shell; unset fields take compose defaults.
	cache := p.Services["cache"].Healthcheck
	if !reflect.DeepEqual(cache.Test, []string{"sh", "-c", "redis-cli ping"}) {
		t.Errorf("cache test argv = %v, want sh -c form", cache.Test)
	}
	if cache.Interval != 30*time.Second || cache.Retries != 3 {
		t.Errorf("cache defaults = interval %v, retries %d; want 30s, 3", cache.Interval, cache.Retries)
	}
}

func TestServiceHealthyRequiresHealthcheck(t *testing.T) {
	_, err := Load(writeTemp(t, `
services:
  web:
    image: web
    depends_on:
      db:
        condition: service_healthy
  db:
    image: postgres
`))
	if err == nil {
		t.Fatal("expected error: service_healthy on a dependency with no healthcheck")
	}
}

func TestUnsupportedConditionRejected(t *testing.T) {
	_, err := Load(writeTemp(t, `
services:
  web:
    image: web
    depends_on:
      db:
        condition: service_bogus
  db:
    image: postgres
`))
	if err == nil {
		t.Fatal("expected error for an unsupported depends_on condition")
	}
}

func TestCompletedConditionParsing(t *testing.T) {
	// service_completed_successfully needs no healthcheck on the target and still
	// orders the one-shot before its dependent.
	p, err := Load(writeTemp(t, `
services:
  web:
    image: web
    depends_on:
      migrate:
        condition: service_completed_successfully
  migrate:
    image: migrate
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := condOf(p.Services["web"].DependsOn, "migrate"); got != ConditionCompleted {
		t.Errorf("web->migrate condition = %q, want %q", got, ConditionCompleted)
	}
	order, err := p.StartupOrder()
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	if !before(order, "migrate", "web") {
		t.Errorf("migrate should precede web: %v", order)
	}
}

func TestHealthyOfCompletedTargetRejected(t *testing.T) {
	// A run-to-completion service stops when it finishes, so it can't also be
	// required to stay healthy — that contradiction is rejected at load time.
	_, err := Load(writeTemp(t, `
services:
  a:
    image: a
    depends_on:
      job:
        condition: service_completed_successfully
  b:
    image: b
    depends_on:
      job:
        condition: service_healthy
  job:
    image: job
    healthcheck:
      test: ["CMD", "true"]
`))
	if err == nil {
		t.Fatal("expected error: a service can't be both run-to-completion and required healthy")
	}
}
