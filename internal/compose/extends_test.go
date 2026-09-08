package compose

// `extends:` naming a service of the same file (#853). docker compose
// (v5.5.0) reads the named service's settings first and the extending
// service's own over them, merged as a later -f file merges: lists are the
// other's then its own, mappings merge by key with its own winning, a
// scalar is its own where it has one, healthcheck merges by sub-key, and a
// key with nothing after it is "not given" except where a null is a value
// (`command:`, a variable in `environment:`); a chain resolves the named
// service first; a cycle and a service the file does not define are
// refused. With several -f files each file's extends is resolved before
// the merge, against the services that file defines. `extends: {file: …}`
// is still refused by name (#415): until it was, a service that extended
// another usually failed as "must set either image or build", which reads
// as a typo in the wrong file. The oracle fixtures and docker's output are
// kept with the dogfood.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAServiceThatExtendsAnotherInTheSameFileIsReadAsDockerReadsIt(t *testing.T) {
	body := "services:\n  common:\n    image: alpine:3\n    environment: {A: base, B: base}\n    ports: [\"8080:80\"]\n    volumes: [\"./data:/data\"]\n    command: [\"sh\", \"-c\", \"base\"]\n    cap_add: [NET_ADMIN]\n    healthcheck: {test: [\"CMD\", \"true\"], interval: 10s}\n    depends_on: [dep]\n  web:\n    extends: common\n    environment: {B: web, C: web}\n    ports: [\"9090:90\"]\n    volumes: [\"./logs:/logs\"]\n    cap_add: [SYS_TIME]\n    healthcheck: {interval: 5s}\n  mid:\n    extends: {service: common}\n    environment: {B: mid}\n  leaf:\n    extends: mid\n    environment: {C: leaf}\n  dep:\n    image: alpine:3\n"
	p, err := Load(writeTemp(t, body))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	web := p.Services["web"]
	for _, tc := range []struct{ name, got, want string }{
		{"image is inherited", web.Image, "alpine:3"},
		{"a mapping merges by key, its own winning", strings.Join(web.Environment, ","), "A=base,B=web,C=web"},
		{"a list is the other's then its own", strings.Join(web.Ports, ","), "8080:80,9090:90"},
		{"volumes too", strings.Join(web.Volumes, ","), "./data:/data,./logs:/logs"},
		{"cap_add too", strings.Join(web.CapAdd, ","), "NET_ADMIN,SYS_TIME"},
		{"command is inherited", strings.Join(web.Command, " "), "sh -c base"},
		{"a healthcheck merges by sub-key", strings.Join(web.Healthcheck.Test, " ") + " " + web.Healthcheck.Interval.String(), "true 5s"},
		{"depends_on is inherited", web.DependsOn.Names()[0], "dep"},
		{"a chain resolves the named service first", strings.Join(p.Services["leaf"].Environment, ","), "A=base,B=mid,C=leaf"},
		{"the named service is left as it was", strings.Join(p.Services["common"].Environment, ","), "A=base,B=base"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
	if got := strings.Join(web.Unsupported, ","); strings.Contains(got, "extends") {
		t.Errorf("extends should not be listed as ignored once read, got %q", got)
	}
}

// The merge is the -f merge, path rules and all: a key with nothing after
// it in the extending service is "not given" — except `command:` (docker
// compose: `command: null`, no command) and a variable in `environment:`
// (unset); `depends_on` in either form merges into the long form; `build`
// in either form merges by sub-key.
func TestExtendsMergesByTheSameRulesAsALaterFile(t *testing.T) {
	load := func(t *testing.T, body string) *Service {
		t.Helper()
		p, err := Load(writeTemp(t, body))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		return p.Services["web"]
	}
	t.Run("a bare command is no command", func(t *testing.T) {
		web := load(t, "services:\n  base:\n    image: alpine:3\n    command: [\"sh\", \"-c\", \"base\"]\n  web:\n    extends: base\n    command:\n")
		if len(web.Command) != 0 {
			t.Errorf("command = %q, want none", web.Command)
		}
	})
	t.Run("a bare variable is unset, the others inherited", func(t *testing.T) {
		web := load(t, "services:\n  base:\n    image: alpine:3\n    environment: {A: base, B: base}\n  web:\n    extends: base\n    environment: {A: }\n")
		if got := strings.Join(web.Environment, ","); got != "A,B=base" {
			t.Errorf("environment = %q, want A,B=base", got)
		}
	})
	t.Run("depends_on: a mapping over a list", func(t *testing.T) {
		web := load(t, "services:\n  d1: {image: alpine:3}\n  d2: {image: alpine:3}\n  base:\n    image: alpine:3\n    depends_on: [d1]\n  web:\n    extends: base\n    depends_on:\n      d2: {condition: service_started}\n")
		if got := strings.Join(web.DependsOn.Names(), ","); got != "d1,d2" {
			t.Errorf("depends_on = %q, want d1,d2", got)
		}
	})
	t.Run("depends_on: a list over a mapping keeps the condition", func(t *testing.T) {
		web := load(t, "services:\n  d1:\n    image: alpine:3\n    healthcheck: {test: [\"CMD\", \"true\"]}\n  d2: {image: alpine:3}\n  base:\n    image: alpine:3\n    depends_on:\n      d1: {condition: service_healthy}\n  web:\n    extends: base\n    depends_on: [d2]\n")
		if got := strings.Join(web.DependsOn.Names(), ","); got != "d1,d2" {
			t.Fatalf("depends_on = %q, want d1,d2", got)
		}
		if got := web.DependsOn[0].Condition; got != "service_healthy" {
			t.Errorf("d1's condition = %q, want service_healthy", got)
		}
	})
	t.Run("build: the long form over the short keeps the context", func(t *testing.T) {
		web := load(t, "services:\n  base:\n    build: ./base\n  web:\n    extends: base\n    build:\n      dockerfile: Dockerfile.web\n")
		if web.Build == nil || web.Build.Context != "./base" || web.Build.Dockerfile != "Dockerfile.web" {
			t.Errorf("build = %+v, want context ./base and dockerfile Dockerfile.web", web.Build)
		}
	})
	t.Run("build: the short form over the long keeps the dockerfile", func(t *testing.T) {
		web := load(t, "services:\n  base:\n    build:\n      context: ./base\n      dockerfile: Dockerfile.base\n  web:\n    extends: base\n    build: ./web\n")
		if web.Build == nil || web.Build.Context != "./web" || web.Build.Dockerfile != "Dockerfile.base" {
			t.Errorf("build = %+v, want context ./web and dockerfile Dockerfile.base", web.Build)
		}
	})
	// A bare list key in the extending service is "not given" too (docker
	// compose reads extends before it checks the shapes): the named
	// service's list stands — where without extends the bare key is
	// refused (#735).
	t.Run("a bare list key is not given: the named service's list stands", func(t *testing.T) {
		web := load(t, "services:\n  base:\n    image: alpine:3\n    ports: [\"1:1\"]\n    cap_add: [NET_ADMIN]\n  web:\n    extends: base\n    ports:\n    cap_add:\n")
		if got := strings.Join(web.Ports, ",") + " " + strings.Join(web.CapAdd, ","); got != "1:1 NET_ADMIN" {
			t.Errorf("ports, cap_add = %q, want the named service's", got)
		}
	})
	// The check as written prunes a copy: what the merge reads still has
	// the bare key, so `command:` stays "no command". A reference in the
	// file is what makes the parsed document shared between the two.
	t.Run("the check as written does not touch what the merge reads", func(t *testing.T) {
		web := load(t, "services:\n  base:\n    image: alpine:3\n    command: [\"sh\", \"-c\", \"base\"]\n  web:\n    extends: base\n    command:\n    environment: {A: \"${OPOSSUM_TEST_EXTENDS_UNSET-x}\"}\n")
		if len(web.Command) != 0 {
			t.Errorf("command = %q, want none", web.Command)
		}
	})
	// docker compose: `services.web.volumes must be a array`. The line is
	// one of the document after extends was read, and says so.
	t.Run("a bare key the named service has no value for is refused once extends is read", func(t *testing.T) {
		got := loadErr(t, "services:\n  base:\n    image: alpine:3\n  web:\n    extends: base\n    volumes:\n")
		if !strings.Contains(got, "volumes: expected a list, got nothing") {
			t.Fatalf("want the bare-key refusal, got:\n%s", got)
		}
		if !strings.Contains(got, "after extends was read, not in the file as written") {
			t.Errorf("the line counts in the resolved document and should say so, got:\n%s", got)
		}
		if strings.Contains(got, "merged document") {
			t.Errorf("one file was read; nothing about a merged document belongs here:\n%s", got)
		}
	})
}

// A file that resolves extends is read as written first, so a shape
// mistake still names the reader's own line and service, not a line of the
// resolved document.
func TestAShapeMistakeInAFileWithExtendsNamesTheLineAsWritten(t *testing.T) {
	// The extending service comes first and carries a key more than the
	// resolved document would keep, so its line 5 is a different line there.
	got := loadErr(t, "services:\n  web:\n    extends: base\n    volumes: []\n    image: [x]\n  base:\n    image: alpine:3\n")
	if !strings.Contains(got, `service "web"`) || !strings.Contains(got, "line 5:") {
		t.Errorf("want the service and line 5 as written, got:\n%s", got)
	}
	if strings.Contains(got, "as read") {
		t.Errorf("the mistake is in the file as written; nothing about the document as read belongs here:\n%s", got)
	}
}

func TestExtendsIsRefusedWhereItNamesNothingOfThisFile(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{
			"a cycle",
			"services:\n  a:\n    extends: b\n    image: x\n  b:\n    extends: a\n",
			`service "a": extends forms a cycle (a → b → a)`,
		},
		{
			"a service the file does not define",
			"services:\n  web:\n    extends: nope\n    image: x\n",
			`service "web" extends "nope", which the file does not define`,
		},
		{
			// docker compose: `services.base must be a mapping` — the
			// same refusal as without extends.
			"a service with nothing under it",
			"services:\n  base:\n  web:\n    extends: base\n    image: x\n",
			`service "base" must be a mapping`,
		},
		{
			// docker compose: `service "" not found`, as for `extends:` alone.
			"a null after the key",
			"services:\n  web:\n    image: alpine\n    extends: ~\n",
			`service "web" uses extends:, which names no service`,
		},
		{
			// The named service's own extends names another file: the
			// refusal names the named service, not the one that extends
			// it (whose copy of the settings would otherwise carry the
			// key). The extending service comes first by name, so it is
			// resolved first.
			"a service whose named service extends another file",
			"services:\n  a:\n    extends: z\n  z:\n    extends: {file: other.yml, service: s}\n",
			`service "a" extends "z", which itself uses extends: (service "s" in other.yml) — opossum does not read extends from another file yet; copy that service's settings into "z" and remove extends:`,
		},
		{
			"long form, another file",
			"services:\n  web:\n    extends:\n      file: common.yml\n      service: base\n",
			`service "web" uses extends: (service "base" in common.yml), which opossum does not read from another file yet`,
		},
		{
			// The second decoding pass keeps the value as a yaml.Node, in
			// which an alias stays an alias; the name must still come out.
			"through an alias",
			"x-ext: &x {service: base, file: common.yml}\nservices:\n  web:\n    image: alpine\n    extends: *x\n",
			`service "web" uses extends: (service "base" in common.yml)`,
		},
		{
			// docker compose tries to read "" and fails.
			"a file with nothing after it",
			"services:\n  base:\n    image: alpine\n  web:\n    extends: {file: \"\", service: base}\n",
			`service "web" uses extends: (service "base") with a ` + "`file:`" + ` that names no file`,
		},
		{
			// docker compose: `extends.web.service is required`.
			"a file without a service",
			"services:\n  web:\n    image: alpine\n    extends:\n      file: common.yml\n",
			`service "web" uses extends: (a service in common.yml), which names no service`,
		},
		{
			// Neither shape: still the key's meaning, with nothing to name
			// (docker compose: `service "" not found`).
			"a value of no known shape",
			"services:\n  web:\n    image: alpine\n    extends: [base]\n",
			`service "web" uses extends:, which names no service`,
		},
		{
			"nothing after the key",
			"services:\n  web:\n    image: alpine\n    extends:\n",
			`service "web" uses extends:, which names no service`,
		},
		{
			"an empty mapping",
			"services:\n  web:\n    image: alpine\n    extends: {}\n",
			`service "web" uses extends:, which names no service`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q in the error, got:\n%s", tc.want, got)
			}
			if strings.Contains(got, "must set either image or build") {
				t.Errorf("the refusal must replace the image message, got:\n%s", got)
			}
			// The bare-service refusal is the one for any service written so,
			// and says nothing of extends.
			if strings.Contains(got, "extends") && !strings.Contains(got, "remove extends:") {
				t.Errorf("the refusal should say what to do, got:\n%s", got)
			}
		})
	}
}

// A service *called* extends is a service; the key is only a field one
// level down.
func TestAServiceNamedExtendsIsJustAService(t *testing.T) {
	p, err := Load(writeTemp(t, "services:\n  extends:\n    image: alpine\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if p.Services["extends"] == nil || p.Services["extends"].Extends != nil {
		t.Errorf("a service named extends should load with no Extends: %+v", p.Services["extends"])
	}
}

// With two services at fault, the first by name is the one reported — not
// whichever the map hands out first.
func TestTheFirstServiceByNameIsReported(t *testing.T) {
	for i := 0; i < 20; i++ {
		got := loadErr(t, "services:\n  zzz:\n    build: .\n    extends: aaa\n  aaa:\n    extends: zzz\n  mmm:\n    command: x\n")
		if !strings.Contains(got, `service "aaa": extends forms a cycle (aaa → zzz → aaa)`) {
			t.Fatalf("run %d: want aaa reported first, got:\n%s", i, got)
		}
	}
}

// Across -f files each file's extends is resolved before the merge, against
// the services that file defines (docker compose, measured): the extending
// service takes the named service as its own file writes it, and the earlier
// file's version of the extending service is what that goes over; a file
// that extends a service only another file defines is refused naming that
// file.
func TestAcrossFilesExtendsIsResolvedInEachFileBeforeTheMerge(t *testing.T) {
	write := func(t *testing.T, dir, name, body string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	dir := t.TempDir()
	a := write(t, dir, "a.yml", "services:\n  base:\n    image: alpine:a\n    environment: {X: a}\n    ports: [\"1:1\"]\n  web:\n    extends: base\n    ports: [\"2:2\"]\n")
	b := write(t, dir, "b.yml", "services:\n  base:\n    image: alpine:b\n    environment: {Y: b}\n    ports: [\"3:3\"]\n  web:\n    ports: [\"4:4\"]\n")
	p, err := LoadFiles([]string{a, b}, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	web := p.Services["web"]
	for _, tc := range []struct{ name, got, want string }{
		{"the image is the first file's base, not the merged one", web.Image, "alpine:a"},
		{"the environment too", strings.Join(web.Environment, ","), "X=a"},
		{"the lists are the first file's base and web, then the later file's web", strings.Join(web.Ports, ","), "1:1,2:2,4:4"},
		{"the named service itself merges as before", p.Services["base"].Image + " " + strings.Join(p.Services["base"].Ports, ","), "alpine:b 1:1,3:3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
	t.Run("a later file's extends sees only what that file defines", func(t *testing.T) {
		ma := write(t, dir, "ma.yml", "services:\n  base:\n    image: alpine:a\n    environment: {Z: a}\n    ports: [\"1:1\"]\n  web:\n    ports: [\"2:2\"]\n")
		mb := write(t, dir, "mb.yml", "services:\n  base:\n    image: alpine:b\n    ports: [\"3:3\"]\n  web:\n    extends: base\n    ports: [\"4:4\"]\n")
		p, err := LoadFiles([]string{ma, mb}, nil)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		web := p.Services["web"]
		if got := web.Image + " " + strings.Join(web.Environment, ",") + " " + strings.Join(web.Ports, ","); got != "alpine:b  2:2,3:3,4:4" {
			t.Errorf("web = %q, want the later file's base (no Z, alpine:b) over the earlier web: %q", got, "alpine:b  2:2,3:3,4:4")
		}
	})
	t.Run("a later file extending only an earlier file's service is refused", func(t *testing.T) {
		c := write(t, dir, "c.yml", "services:\n  web:\n    extends: base\n    ports: [\"5:5\"]\n")
		_, err := LoadFiles([]string{a, c}, nil)
		if err == nil || !strings.Contains(err.Error(), `c.yml: service "web" extends "base", which the file does not define`) {
			t.Errorf("want the refusal naming c.yml, got: %v", err)
		}
	})
	t.Run("an earlier file extending only a later file's service is refused", func(t *testing.T) {
		d := write(t, dir, "d.yml", "services:\n  base:\n    image: alpine:d\n")
		e := write(t, dir, "e.yml", "services:\n  web:\n    image: alpine:e\n    extends: base\n")
		_, err := LoadFiles([]string{e, d}, nil)
		if err == nil || !strings.Contains(err.Error(), `e.yml: service "web" extends "base", which the file does not define`) {
			t.Errorf("want the refusal naming e.yml, got: %v", err)
		}
	})
	// In a later file a bare service key is "not given" for the merge, but
	// extends reads the service as this file defines it — nothing — and
	// refuses rather than reading the earlier file's (docker compose leaves
	// the key unread there, and the service without an image).
	t.Run("a later file extending a service it writes with nothing under it is refused", func(t *testing.T) {
		d := write(t, dir, "d.yml", "services:\n  base:\n    image: alpine:d\n")
		f := write(t, dir, "f.yml", "services:\n  base:\n  web:\n    extends: base\n    ports: [\"2:2\"]\n")
		_, err := LoadFiles([]string{d, f}, nil)
		if err == nil || !strings.Contains(err.Error(), `f.yml: service "web" extends "base", which this file writes with nothing under it`) {
			t.Errorf("want the refusal naming f.yml, got: %v", err)
		}
	})
}

// Across -f files the file form survives the merge, so an override that adds
// the image the base lacks does not make the extended settings appear either.
func TestExtendsInAnEarlierFileIsStillRefusedAfterAnOverride(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	over := filepath.Join(dir, "over.yml")
	if err := os.WriteFile(base, []byte("services:\n  web:\n    extends:\n      file: common.yml\n      service: base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(over, []byte("services:\n  web:\n    image: alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFiles([]string{base, over}, nil)
	if err == nil || !strings.Contains(err.Error(), `service "web" uses extends: (service "base" in common.yml)`) {
		t.Errorf("want the extends refusal after the merge, got: %v", err)
	}
}
