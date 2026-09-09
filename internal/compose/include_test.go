package compose

// A file's `include:` (docker compose v5.5.0, measured; the oracle fixtures
// and docker's output are kept with the project's dogfood): each entry
// names a file — or, in the long form, one or several under `path`, with
// `project_directory` and `env_file` — read as a project of its own,
// its relative paths counting from its project directory (the first path's
// directory when not given) and its variables from that directory's `.env`
// (or the entry's env_file) under the including project's shell and
// `.env`; the included files are merged, in order, under the including
// file, whose settings win where both define a service. An included file
// may include in turn (paths relative to it), and a service of the
// including file may extend an included one. A file that includes itself
// through any chain is refused, and so is a file that is not there.
// opossum refused every `include` by name (the files would have been left
// out of the project).

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// includeFixture lays out the oracle's tree: common.yml (db, a volume and a
// network, an env_file next to it) and sub/other.yml (cache, with relative
// paths of its own and an env_file next to it).
func includeFixture(t *testing.T) string {
	t.Helper()
	return includeFixtureIn(t, t.TempDir())
}

func includeFixtureIn(t *testing.T, dir string) string {
	t.Helper()
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("common.yml", "services:\n  db:\n    image: postgres:16\n    environment: {PGDATA: /data}\n    volumes: [dbdata:/data]\n    env_file: [.env.db]\nvolumes:\n  dbdata: {}\nnetworks:\n  back: {}\n")
	write(".env.db", "DB_PASS=secret\n")
	write("sub/other.yml", "services:\n  cache:\n    image: redis:7\n    volumes: [\"./conf:/conf\"]\n    env_file: .env.cache\n    build: ./img\n")
	write("sub/.env.cache", "C=1\n")
	return dir
}

func serviceNames(p *Project) []string {
	names := make([]string, 0, len(p.Services))
	for name := range p.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func writeIn(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAFileWithIncludeIsReadAsDockerReadsIt(t *testing.T) {
	dir := includeFixture(t)
	main := writeIn(t, dir, "compose.yml", "include:\n  - common.yml\n  - sub/other.yml\nservices:\n  web:\n    image: nginx\n    depends_on: [db, cache]\n    networks: [back]\n")
	p, err := Load(main)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	db, cache, web := p.Services["db"], p.Services["cache"], p.Services["web"]
	if db == nil || cache == nil || web == nil {
		t.Fatalf("want db, cache and web, got %v", serviceNames(p))
	}
	for _, tc := range []struct{ name, got, want string }{
		{"an included service's image", db.Image, "postgres:16"},
		{"an included file's relative env_file counts from its directory", strings.Join(db.Environment, ","), "DB_PASS=secret,PGDATA=/data"},
		{"a file in a subdirectory: its env_file counts from there", strings.Join(cache.Environment, ","), "C=1"},
		{"its bind mount too", strings.Join(cache.Volumes, ","), filepath.Join(dir, "sub", "conf") + ":/conf"},
		{"its build context too", cache.Build.Context, filepath.Join(dir, "sub", "img")},
		{"the including file's service sees the included ones", strings.Join(web.DependsOn.Names(), ","), "db,cache"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
	t.Run("an included file's declarations are included", func(t *testing.T) {
		if _, ok := p.Volumes["dbdata"]; !ok {
			t.Errorf("volume dbdata missing: %v", p.Volumes)
		}
		if _, ok := p.Networks["back"]; !ok {
			t.Errorf("network back missing: %v", p.Networks)
		}
	})
	t.Run("include is not listed among ignored fields", func(t *testing.T) {
		for _, f := range p.Unsupported {
			if strings.Contains(f, "include") {
				t.Errorf("include is read now, got ignored field %q", f)
			}
		}
	})
}

func TestTheLongFormOfIncludeIsReadAsDockerReadsIt(t *testing.T) {
	t.Run("project_directory: the included file's relative paths count from it", func(t *testing.T) {
		dir := includeFixture(t)
		writeIn(t, dir, "sub/.env.db", "DB_PASS=from-sub\n")
		p, err := Load(writeIn(t, dir, "compose.yml", "include:\n  - path: common.yml\n    project_directory: sub\nservices:\n  web:\n    image: nginx\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if got := strings.Join(p.Services["db"].Environment, ","); got != "DB_PASS=from-sub,PGDATA=/data" {
			t.Errorf("environment = %q, want the env_file under sub/", got)
		}
	})
	t.Run("path as a list: the first path's directory is the project directory of all", func(t *testing.T) {
		dir := includeFixture(t)
		writeIn(t, dir, ".env.cache", "C=from-root\n")
		p, err := Load(writeIn(t, dir, "compose.yml", "include:\n  - path:\n      - common.yml\n      - sub/other.yml\nservices:\n  web:\n    image: nginx\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		cache := p.Services["cache"]
		if got := strings.Join(cache.Volumes, ","); got != filepath.Join(dir, "conf")+":/conf" {
			t.Errorf("volumes = %q, want ./conf counted from the first path's directory", got)
		}
		if got := strings.Join(cache.Environment, ","); got != "C=from-root" {
			t.Errorf("environment = %q, want the env_file counted from the first path's directory", got)
		}
	})
	t.Run("env_file: the included file's variables come from it", func(t *testing.T) {
		dir := includeFixture(t)
		writeIn(t, dir, "common2.yml", "services:\n  db:\n    image: ${DB_IMG:-postgres:15}\n")
		writeIn(t, dir, ".env.inc", "DB_IMG=postgres:99\n")
		p, err := Load(writeIn(t, dir, "compose.yml", "include:\n  - path: common2.yml\n    env_file: .env.inc\nservices:\n  web:\n    image: nginx\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if got := p.Services["db"].Image; got != "postgres:99" {
			t.Errorf("image = %q, want the entry's env_file read", got)
		}
	})
	t.Run("an absolute project_directory and env_file are taken as they are", func(t *testing.T) {
		dir := includeFixture(t)
		writeIn(t, dir, "common2.yml", "services:\n  db:\n    image: ${DB_IMG:-postgres:15}\n    env_file: .env.db\n")
		writeIn(t, dir, "sub/.env.db", "DB_PASS=from-sub\n")
		writeIn(t, dir, ".env.inc", "DB_IMG=postgres:99\n")
		p, err := Load(writeIn(t, dir, "compose.yml", "include:\n  - path: common2.yml\n    project_directory: "+filepath.Join(dir, "sub")+"\n    env_file: "+filepath.Join(dir, ".env.inc")+"\nservices:\n  web:\n    image: nginx\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		db := p.Services["db"]
		if got := db.Image + " " + strings.Join(db.Environment, ","); got != "postgres:99 DB_PASS=from-sub" {
			t.Errorf("db = %q, want the absolute env_file read and the env_file under the absolute project_directory", got)
		}
	})
	t.Run("an absolute include path is taken as it is", func(t *testing.T) {
		dir := includeFixture(t)
		p, err := Load(writeIn(t, dir, "compose.yml", "include:\n  - "+filepath.Join(dir, "common.yml")+"\nservices:\n  web:\n    image: nginx\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if p.Services["db"] == nil {
			t.Errorf("want db through the absolute path, got %v", serviceNames(p))
		}
	})
	t.Run("a relative -f: paths still count from the file's directory, once", func(t *testing.T) {
		parent := t.TempDir()
		dir := includeFixtureIn(t, filepath.Join(parent, "proj"))
		writeIn(t, dir, "compose.yml", "include:\n  - common.yml\n  - sub/other.yml\nservices:\n  web:\n    image: nginx\n")
		t.Chdir(parent)
		p, err := Load(filepath.Join("proj", "compose.yml"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if got := strings.Join(p.Services["db"].Environment, ","); got != "DB_PASS=secret,PGDATA=/data" {
			t.Errorf("db's environment = %q, want the env_file found under proj/ once", got)
		}
		if got := strings.Join(p.Services["cache"].Volumes, ","); got != filepath.Join(dir, "sub", "conf")+":/conf" {
			t.Errorf("cache's volumes = %q, want an absolute source under proj/sub", got)
		}
	})
	t.Run("a nested include counts from the project directory, not from the file that names it", func(t *testing.T) {
		dir := includeFixture(t)
		writeIn(t, dir, "other.yml", "services:\n  cache:\n    image: rootother\n")
		writeIn(t, dir, "sub/inc.yml", "include:\n  - path: other.yml\n    env_file: .env.x\nservices:\n  mid:\n    image: ${MID:-none}\n")
		writeIn(t, dir, ".env.x", "MID=from-root\n")
		writeIn(t, dir, "sub/.env.x", "MID=from-sub\n")
		p, err := Load(writeIn(t, dir, "compose.yml", "include:\n  - path: sub/inc.yml\n    project_directory: .\nservices:\n  web:\n    image: nginx\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if got := p.Services["cache"].Image; got != "rootother" {
			t.Errorf("cache = %q, want other.yml found from the project directory", got)
		}
		// The nested entry's env_file counts from there too; it feeds the
		// file that entry includes, not mid itself, so check through a
		// service of that file.
		writeIn(t, dir, "other.yml", "services:\n  cache:\n    image: ${MID:-none}\n")
		p, err = Load(filepath.Join(dir, "compose.yml"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if got := p.Services["cache"].Image; got != "from-root" {
			t.Errorf("cache = %q, want the nested entry's env_file found from the project directory", got)
		}
	})
	t.Run("env_file as a list: every file is read, the later winning", func(t *testing.T) {
		dir := includeFixture(t)
		writeIn(t, dir, "common2.yml", "services:\n  db:\n    image: ${DB_IMG:-none}\n    environment: {A: \"${A:-none}\", B: \"${B:-none}\"}\n")
		writeIn(t, dir, ".env.a", "DB_IMG=froma\nA=a\n")
		writeIn(t, dir, ".env.b", "DB_IMG=fromb\nB=b\n")
		p, err := Load(writeIn(t, dir, "compose.yml", "include:\n  - path: common2.yml\n    env_file: [.env.a, .env.b]\nservices:\n  web:\n    image: nginx\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		db := p.Services["db"]
		if got := db.Image + " " + strings.Join(db.Environment, ","); got != "fromb A=a,B=b" {
			t.Errorf("db = %q, want both files read and the later winning", got)
		}
	})
	t.Run("a nested entry's relative project_directory counts from the project directory too", func(t *testing.T) {
		dir := includeFixture(t)
		writeIn(t, dir, "deep/d.yml", "services:\n  leaf:\n    image: ${L:-none}\n")
		writeIn(t, dir, "deep/.env", "L=rootdeep\n")
		writeIn(t, dir, "sub/deep/d.yml", "services:\n  leaf:\n    image: ${L:-none}\n")
		writeIn(t, dir, "sub/deep/.env", "L=subdeep\n")
		writeIn(t, dir, "sub/inc.yml", "include:\n  - path: deep/d.yml\n    project_directory: deep\nservices:\n  mid:\n    image: alpine\n")
		p, err := Load(writeIn(t, dir, "compose.yml", "include:\n  - path: sub/inc.yml\n    project_directory: .\nservices:\n  web:\n    image: nginx\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if got := p.Services["leaf"].Image; got != "rootdeep" {
			t.Errorf("leaf = %q, want deep/ found from the project directory, not from sub/inc.yml", got)
		}
	})
	t.Run("a mapping without path names nothing, as docker compose takes it", func(t *testing.T) {
		dir := includeFixture(t)
		p, err := Load(writeIn(t, dir, "compose.yml", "include:\n  - {project_directory: sub}\nservices:\n  web:\n    image: nginx\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if len(p.Services) != 1 {
			t.Errorf("want web alone, got %v", serviceNames(p))
		}
	})
}

// The included project's variables sit under the including project's: the
// shell, then the including project's `.env`, then the entry's env_file —
// which replaces the included directory's own `.env`, read when no
// env_file is given.
func TestAnIncludedFileSeesTheIncludingProjectsVariablesFirst(t *testing.T) {
	dir := includeFixture(t)
	writeIn(t, dir, "sub/common3.yml", "services:\n  db:\n    image: ${DB_IMG:-default}\n    environment: {S: \"${SUB_ONLY:-none}\"}\n")
	writeIn(t, dir, ".env", "DB_IMG=fromroot\n")
	writeIn(t, dir, "sub/.env", "DB_IMG=fromsub\nSUB_ONLY=fromsub\n")
	writeIn(t, dir, ".env.inc", "DB_IMG=fromincenv\nSUB_ONLY=fromincenv\n")
	main := writeIn(t, dir, "compose.yml", "include:\n  - sub/common3.yml\nservices:\n  web:\n    image: nginx\n")
	withEnv := writeIn(t, dir, "compose-env.yml", "include:\n  - path: sub/common3.yml\n    env_file: .env.inc\nservices:\n  web:\n    image: nginx\n")
	load := func(t *testing.T, path string) *Service {
		t.Helper()
		p, err := Load(path)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		return p.Services["db"]
	}
	t.Run("the including project's .env wins over the included directory's", func(t *testing.T) {
		if got := load(t, main).Image; got != "fromroot" {
			t.Errorf("image = %q, want fromroot", got)
		}
	})
	t.Run("the included directory's .env is read for what the including project does not set", func(t *testing.T) {
		if got := strings.Join(load(t, main).Environment, ","); got != "S=fromsub" {
			t.Errorf("environment = %q, want S=fromsub", got)
		}
	})
	t.Run("the entry's env_file replaces the included directory's .env", func(t *testing.T) {
		if got := strings.Join(load(t, withEnv).Environment, ","); got != "S=fromincenv" {
			t.Errorf("environment = %q, want S=fromincenv", got)
		}
	})
	t.Run("the shell wins over both", func(t *testing.T) {
		t.Setenv("DB_IMG", "fromshell")
		if got := load(t, withEnv).Image; got != "fromshell" {
			t.Errorf("image = %q, want fromshell", got)
		}
	})
	t.Run("an include path is interpolated", func(t *testing.T) {
		t.Setenv("OPOSSUM_TEST_INC", "sub/common3.yml")
		p, err := Load(writeIn(t, dir, "compose-var.yml", "include:\n  - ${OPOSSUM_TEST_INC}\nservices:\n  web:\n    image: nginx\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if p.Services["db"] == nil {
			t.Errorf("want db from the interpolated path, got %v", serviceNames(p))
		}
	})
}

func TestIncludedFilesMergeUnderTheIncludingFile(t *testing.T) {
	t.Run("the including file's service goes over the included one", func(t *testing.T) {
		dir := includeFixture(t)
		p, err := Load(writeIn(t, dir, "compose.yml", "include:\n  - common.yml\nservices:\n  db:\n    image: mysql\n    ports: [\"3306:3306\"]\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		db := p.Services["db"]
		if got := db.Image + " " + strings.Join(db.Environment, ",") + " " + strings.Join(db.Ports, ","); got != "mysql DB_PASS=secret,PGDATA=/data 3306:3306" {
			t.Errorf("db = %q, want the including file's image and ports over the included settings", got)
		}
	})
	t.Run("a later include goes over an earlier one", func(t *testing.T) {
		dir := includeFixture(t)
		writeIn(t, dir, "common2.yml", "services:\n  db:\n    image: postgres:17\n    environment: {X: 1}\n")
		p, err := Load(writeIn(t, dir, "compose.yml", "include:\n  - common.yml\n  - common2.yml\nservices:\n  web:\n    image: nginx\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		db := p.Services["db"]
		if got := db.Image + " " + strings.Join(db.Environment, ","); got != "postgres:17 DB_PASS=secret,PGDATA=/data,X=1" {
			t.Errorf("db = %q, want common2 over common", got)
		}
	})
	t.Run("an included file includes in turn, relative to itself, with its own paths", func(t *testing.T) {
		dir := includeFixture(t)
		writeIn(t, dir, "sub/inc.yml", "include:\n  - other.yml\nservices:\n  mid:\n    image: alpine\n    volumes: [\"./mid:/mid\"]\n")
		p, err := Load(writeIn(t, dir, "compose.yml", "include:\n  - sub/inc.yml\nservices:\n  web:\n    image: nginx\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if p.Services["cache"] == nil {
			t.Fatalf("want cache through the nested include, got %v", serviceNames(p))
		}
		if got := strings.Join(p.Services["mid"].Volumes, ","); got != filepath.Join(dir, "sub", "mid")+":/mid" {
			t.Errorf("mid's volumes = %q, want ./mid counted from sub/", got)
		}
		if got := strings.Join(p.Services["cache"].Volumes, ","); got != filepath.Join(dir, "sub", "conf")+":/conf" {
			t.Errorf("cache's volumes = %q, want ./conf counted from sub/, once", got)
		}
	})
	t.Run("a service of the including file may extend an included one", func(t *testing.T) {
		dir := includeFixture(t)
		p, err := Load(writeIn(t, dir, "compose.yml", "include:\n  - common.yml\nservices:\n  web:\n    extends: db\n    image: nginx\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		web := p.Services["web"]
		if got := web.Image + " " + strings.Join(web.Environment, ","); got != "nginx DB_PASS=secret,PGDATA=/data" {
			t.Errorf("web = %q, want the included db's settings under its own image", got)
		}
	})
	t.Run("an included file's name does not become the project's", func(t *testing.T) {
		dir := includeFixture(t)
		writeIn(t, dir, "named.yml", "name: incname\nservices:\n  db:\n    image: postgres:16\n")
		p, err := Load(writeIn(t, dir, "compose.yml", "include:\n  - named.yml\nservices:\n  web:\n    image: nginx\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if p.Name == "incname" {
			t.Errorf("the project took the included file's name; want the directory's, got %q", p.Name)
		}
	})
	t.Run("an included file's secret file counts from its project directory, not from the file's", func(t *testing.T) {
		dir := includeFixture(t)
		writeIn(t, dir, "secret.txt", "root\n")
		writeIn(t, dir, "sub/secret.txt", "sub\n")
		writeIn(t, dir, "sub/sec.yml", "services:\n  db:\n    image: postgres:16\n    secrets: [s]\nsecrets:\n  s:\n    file: ./secret.txt\n")
		p, err := Load(writeIn(t, dir, "compose.yml", "include:\n  - path: sub/sec.yml\n    project_directory: .\nservices:\n  web:\n    image: nginx\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if got := p.Secrets["s"].File; got != filepath.Join(dir, "secret.txt") {
			t.Errorf("secret file = %q, want it under the project directory", got)
		}
	})
	t.Run("with -f, a later file's include merges like the rest", func(t *testing.T) {
		dir := includeFixture(t)
		first := writeIn(t, dir, "first.yml", "services:\n  web:\n    image: nginx\n")
		over := writeIn(t, dir, "over.yml", "include:\n  - common.yml\nservices:\n  db:\n    ports: [\"5432:5432\"]\n")
		p, err := LoadFiles([]string{first, over}, nil)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		db := p.Services["db"]
		if got := db.Image + " " + strings.Join(db.Ports, ","); got != "postgres:16 5432:5432" {
			t.Errorf("db = %q, want the included db with the later file's ports", got)
		}
	})
}

func TestIncludeIsRefusedWhereItNamesNothingReadable(t *testing.T) {
	dir := includeFixture(t)
	refused := func(t *testing.T, body string) string {
		t.Helper()
		_, err := Load(writeIn(t, dir, "compose.yml", body))
		if err == nil {
			t.Fatal("want a refusal, got nil")
		}
		return err.Error()
	}
	t.Run("a file that is not there, naming the including file and the path", func(t *testing.T) {
		got := refused(t, "include:\n  - nope.yml\nservices:\n  web:\n    image: nginx\n")
		if !strings.Contains(got, "compose.yml: include names "+filepath.Join(dir, "nope.yml")+", which cannot be read") || !strings.Contains(got, "resolved from the project directory") {
			t.Errorf("want the including file and the path, got:\n%s", got)
		}
	})
	t.Run("a cycle through files", func(t *testing.T) {
		writeIn(t, dir, "cycle2.yml", "include:\n  - compose.yml\nservices:\n  x:\n    image: alpine\n")
		got := refused(t, "include:\n  - cycle2.yml\nservices:\n  web:\n    image: nginx\n")
		if !strings.Contains(got, "include forms a cycle (compose.yml → cycle2.yml → compose.yml)") {
			t.Errorf("want the cycle shown, got:\n%s", got)
		}
	})
	t.Run("a refusal of the whole names the included files first, then the including", func(t *testing.T) {
		writeIn(t, dir, "empty.yml", "services: {}\n")
		got := refused(t, "include:\n  - empty.yml\nservices: {}\n")
		if !strings.HasPrefix(got, filepath.Join(dir, "empty.yml")+" + "+filepath.Join(dir, "compose.yml")+" defines no services") {
			t.Errorf("want both files named, included first and nothing else, got:\n%s", got)
		}
	})
	t.Run("an entry's env_file that is not there", func(t *testing.T) {
		got := refused(t, "include:\n  - path: common.yml\n    env_file: .env.nope\nservices:\n  web:\n    image: nginx\n")
		if !strings.Contains(got, "compose.yml: include: env file") || !strings.Contains(got, ".env.nope") {
			t.Errorf("want the entry's env_file named, got:\n%s", got)
		}
	})
	t.Run("an included file is checked as a project of its own, even after an earlier -f file", func(t *testing.T) {
		first := writeIn(t, dir, "first.yml", "services:\n  db:\n    image: mysql\n")
		writeIn(t, dir, "bare-inc.yml", "services:\n  db:\n")
		second := writeIn(t, dir, "second.yml", "include:\n  - bare-inc.yml\nservices:\n  web:\n    image: nginx\n")
		_, err := LoadFiles([]string{first, second}, nil)
		if err == nil || !strings.Contains(err.Error(), "bare-inc.yml") || !strings.Contains(err.Error(), `service "db" must be a mapping`) {
			t.Errorf("want the bare service refused naming bare-inc.yml, got: %v", err)
		}
	})
	t.Run("a file missing two levels down is named by the file that includes it", func(t *testing.T) {
		writeIn(t, dir, "sub/deep.yml", "include:\n  - gone.yml\nservices:\n  mid:\n    image: alpine\n")
		got := refused(t, "include:\n  - sub/deep.yml\nservices:\n  web:\n    image: nginx\n")
		if !strings.Contains(got, filepath.Join(dir, "sub", "deep.yml")+": include names "+filepath.Join(dir, "sub", "gone.yml")) {
			t.Errorf("want deep.yml naming gone.yml, got:\n%s", got)
		}
	})
	t.Run("a refusal of the whole names a nested file too, in reading order", func(t *testing.T) {
		writeIn(t, dir, "sub/deep2.yml", "include:\n  - ../empty2.yml\nservices: {}\n")
		writeIn(t, dir, "empty2.yml", "services: {}\n")
		got := refused(t, "include:\n  - sub/deep2.yml\nservices: {}\n")
		want := filepath.Join(dir, "empty2.yml") + " + " + filepath.Join(dir, "sub", "deep2.yml") + " + " + filepath.Join(dir, "compose.yml") + " defines no services"
		if !strings.HasPrefix(got, want) {
			t.Errorf("want %q, got:\n%s", want, got)
		}
	})
	t.Run("a shape mistake in an included file names that file and its line", func(t *testing.T) {
		writeIn(t, dir, "bad.yml", "services:\n  db:\n    image: postgres:16\n    ports: 5432\n")
		got := refused(t, "include:\n  - bad.yml\nservices:\n  web:\n    image: nginx\n")
		if !strings.Contains(got, "bad.yml") || !strings.Contains(got, "ports must be a list") {
			t.Errorf("want bad.yml and its ports refusal, got:\n%s", got)
		}
		if strings.Contains(got, "compose.yml") {
			t.Errorf("the mistake is in bad.yml alone, got:\n%s", got)
		}
	})
	for _, tc := range []struct{ name, body, want string }{
		{"an entry that is a number", "include:\n  - 42\n", "include entry 1 must be a path or a mapping with path, got a single value"},
		{"an entry with nothing in it", "include:\n  -\n", "include entry 1 must be a path or a mapping with path, got nothing"},
		{"a path that is a mapping", "include:\n  - path: {a: b}\n", "include entry 1: path must be a path or a list of paths, got a mapping"},
		{"a path list with a number in it", "include:\n  - path: [common.yml, 42]\n", "include entry 1: path must be a path or a list of paths — got a single value in the list"},
		{"a project_directory that is a list", "include:\n  - path: common.yml\n    project_directory: [sub]\n", "include entry 1: project_directory must be a path, got a list"},
		{"an env_file that is a mapping", "include:\n  - path: common.yml\n    env_file: {a: b}\n", "include entry 1: env_file must be a path or a list of paths, got a mapping"},
		{"the second entry at fault is the second", "include:\n  - common.yml\n  - 42\n", "include entry 2 must be"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := refused(t, tc.body+"services:\n  web:\n    image: nginx\n")
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
		})
	}
}

// A service key the including file writes with nothing under it is "not
// given", as in a later -f file: the included service stands (docker
// compose, measured), and what no file gave a value to is refused after
// the merge.
func TestABareKeyInTheIncludingFileIsNotGiven(t *testing.T) {
	t.Run("a bare service key is the included service", func(t *testing.T) {
		dir := includeFixture(t)
		p, err := Load(writeIn(t, dir, "compose.yml", "include:\n  - common.yml\nservices:\n  db:\n"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if got := p.Services["db"].Image; got != "postgres:16" {
			t.Errorf("db = %q, want the included one", got)
		}
	})
	t.Run("a bare service key no include defines is still refused, naming this file", func(t *testing.T) {
		dir := includeFixture(t)
		_, err := Load(writeIn(t, dir, "compose.yml", "include:\n  - common.yml\nservices:\n  web:\n"))
		if err == nil || !strings.Contains(err.Error(), "compose file "+filepath.Join(dir, "compose.yml")+`: service "web" must be a mapping`) {
			t.Errorf("want the bare-service refusal naming compose.yml, got: %v", err)
		}
	})
	t.Run("a bare field the include gave a value to is that value", func(t *testing.T) {
		dir := includeFixture(t)
		writeIn(t, dir, "ported.yml", "services:\n  db:\n    image: postgres:16\n    ports: [\"5432:5432\"]\n")
		p, err := Load(writeIn(t, dir, "compose.yml", "include:\n  - ported.yml\nservices:\n  db:\n    ports:\n"))
		if err != nil {
			t.Fatalf("a bare ports: the include gave a value to is not given: %v", err)
		}
		if got := strings.Join(p.Services["db"].Ports, ","); got != "5432:5432" {
			t.Errorf("ports = %q, want the include's", got)
		}
	})
	t.Run("with -f, a bare service key only an earlier file defines is that service", func(t *testing.T) {
		dir := includeFixture(t)
		first := writeIn(t, dir, "first.yml", "services:\n  web:\n    image: nginx\n    ports: [\"8080:80\"]\n")
		writeIn(t, dir, "x.yml", "services:\n  other:\n    image: alpine\n")
		second := writeIn(t, dir, "second.yml", "include:\n  - x.yml\nservices:\n  web:\n")
		p, err := LoadFiles([]string{first, second}, nil)
		if err != nil {
			t.Fatalf("a bare web: the earlier file defined is not given: %v", err)
		}
		if got := p.Services["web"].Image + " " + strings.Join(p.Services["web"].Ports, ","); got != "nginx 8080:80" {
			t.Errorf("web = %q, want the earlier file's", got)
		}
	})
	t.Run("with -f, a bare field on a service only an earlier file defines is that value", func(t *testing.T) {
		dir := includeFixture(t)
		first := writeIn(t, dir, "first.yml", "services:\n  web:\n    image: nginx\n    ports: [\"8080:80\"]\n")
		writeIn(t, dir, "x.yml", "services:\n  other:\n    image: alpine\n")
		second := writeIn(t, dir, "second.yml", "include:\n  - x.yml\nservices:\n  web:\n    ports:\n")
		p, err := LoadFiles([]string{first, second}, nil)
		if err != nil {
			t.Fatalf("a bare ports: the earlier file gave a value to is not given: %v", err)
		}
		if got := strings.Join(p.Services["web"].Ports, ","); got != "8080:80" {
			t.Errorf("ports = %q, want the earlier file's", got)
		}
	})
	t.Run("a bare field no include gave a value to is refused after the merge, naming this file", func(t *testing.T) {
		dir := includeFixture(t)
		_, err := Load(writeIn(t, dir, "compose.yml", "include:\n  - common.yml\nservices:\n  web:\n    image: nginx\n    ports:\n"))
		if err == nil || !strings.Contains(err.Error(), "ports: expected a list, got nothing") {
			t.Fatalf("want the bare-key refusal, got: %v", err)
		}
		// The key is this file's; the included file has no part in it.
		if !strings.Contains(err.Error(), "compose file "+filepath.Join(dir, "compose.yml")+" parsed") || strings.Contains(err.Error(), "common.yml") {
			t.Errorf("the refusal should name the including file alone, got: %v", err)
		}
	})
	t.Run("an external name that conflicts with the name an include gave is refused", func(t *testing.T) {
		dir := includeFixture(t)
		writeIn(t, dir, "named.yml", "services:\n  db:\n    image: alpine\n    volumes: [data:/d]\nvolumes:\n  data:\n    name: n1\n")
		_, err := Load(writeIn(t, dir, "compose.yml", "include:\n  - named.yml\nvolumes:\n  data:\n    external:\n      name: n2\n"))
		if err == nil || !strings.Contains(err.Error(), `volumes.data: name "n1" and external.name "n2" conflict`) {
			t.Errorf("want the conflict refused as docker compose refuses it, got: %v", err)
		}
	})
	t.Run("with -f, what an earlier file gave counts too", func(t *testing.T) {
		dir := includeFixture(t)
		first := writeIn(t, dir, "first.yml", "name: proj\nservices:\n  db:\n    image: mysql\n    ports: [\"1:1\"]\n")
		writeIn(t, dir, "ported.yml", "services:\n  db:\n    image: postgres:16\n    ports: [\"2:2\"]\n  cache:\n    image: redis\n    cap_add: [NET_ADMIN]\n")
		second := writeIn(t, dir, "second.yml", "include:\n  - ported.yml\nname:\n")
		p, err := LoadFiles([]string{first, second}, nil)
		if err != nil {
			t.Fatalf("a bare name: the earlier file gave a value to is not given: %v", err)
		}
		if p.Name != "proj" {
			t.Errorf("name = %q, want the earlier file's", p.Name)
		}
		// The earlier files' tree is read, not written to: the included
		// db goes over the earlier once, not twice.
		if got := strings.Join(p.Services["db"].Ports, ","); got != "1:1,2:2" {
			t.Errorf("db's ports = %q, want the earlier file's then the included, once", got)
		}
		if got := strings.Join(p.Services["cache"].CapAdd, ","); got != "NET_ADMIN" {
			t.Errorf("cache's cap_add = %q, want the included list once — twice means the earlier files' tree was written to", got)
		}
	})
}

// A later -f file's include path counts from the project directory — the
// first file's — not from the later file's own (docker compose, measured).
func TestALaterFilesIncludeIsFoundFromTheFirstFilesDirectory(t *testing.T) {
	dir := includeFixture(t)
	a := writeIn(t, dir, "a.yml", "services:\n  web:\n    image: nginx\n")
	writeIn(t, dir, "other.yml", "services:\n  other:\n    image: alpine:root\n")
	writeIn(t, dir, "sub/other.yml", "services:\n  other:\n    image: alpine:sub\n")
	inc := writeIn(t, dir, "sub/inc.yml", "include:\n  - other.yml\nservices:\n  x:\n    image: busybox\n")
	p, err := LoadFiles([]string{a, inc}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := p.Services["other"].Image; got != "alpine:root" {
		t.Errorf("other = %q, want the other.yml next to a.yml", got)
	}
}

// An included file's `extends: {file: …}` is found from the entry's project
// directory, not from the included file's own (docker compose, measured).
func TestAnIncludedFilesExtendsIsFoundFromItsProjectDirectory(t *testing.T) {
	dir := includeFixture(t)
	writeIn(t, dir, "base.yml", "services:\n  base:\n    image: alpine:root\n")
	writeIn(t, dir, "sub/base.yml", "services:\n  base:\n    image: alpine:sub\n")
	writeIn(t, dir, "sub/ext.yml", "services:\n  web:\n    extends: {file: base.yml, service: base}\n")
	for _, tc := range []struct{ name, entry, want string }{
		{"the entry's project_directory", "  - path: sub/ext.yml\n    project_directory: .\n", "alpine:root"},
		{"the path's directory when not given", "  - sub/ext.yml\n", "alpine:sub"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(writeIn(t, dir, "compose.yml", "include:\n"+tc.entry+"services:\n  x:\n    image: nginx\n"))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := p.Services["web"].Image; got != tc.want {
				t.Errorf("web = %q, want %q", got, tc.want)
			}
		})
	}
}
