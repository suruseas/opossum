package compose

// A key opossum does not read in a list's long-form item — a port's
// `mode`, a bind mount's `bind` options, a secret's `uid`, an env file's
// `format`, a volume option under `volume:` (`subpath`) — or in a
// dependency's mapping (`restart`, `required`) (#784's
// second group, the list side). docker compose (v5.5.0) refuses a key it
// does not know there (`services.web.ports.0 additional properties 'bogus'
// not allowed`) and reads the ones it knows. opossum names both among the
// ignored fields, by entry number or dependency name, where it used to
// drop them in silence.

import (
	"slices"
	"strings"
	"testing"
)

func TestAKeyOpossumDoesNotReadInAListItemIsNamedAmongTheIgnoredFields(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"a port's mode", "ports:\n  - {target: 80, published: 8080, mode: host}\n", "ports entry 1.mode"},
		{"a port's unknown key", "ports:\n  - {target: 80, bogus: 1}\n", "ports entry 1.bogus"},
		{"the second port", "ports:\n  - \"8080:80\"\n  - {target: 81, name: web}\n", "ports entry 2.name"},
		{"a bind mount's bind options", "volumes:\n  - {type: bind, source: ., target: /app, bind: {create_host_path: true}}\n", "volumes entry 1.bind"},
		{"a volume's consistency", "volumes:\n  - {type: volume, source: data, target: /data, consistency: cached}\n", "volumes entry 1.consistency"},
		{"a volume option opossum does not read", "volumes:\n  - {type: volume, source: data, target: /data, volume: {nocopy: true, subpath: sub}}\n", "volumes entry 1.volume.subpath"},
		{"a secret's uid", "secrets:\n  - {source: s, target: s2, uid: \"0\"}\n", "secrets entry 1.uid"},
		{"an env file's format", "env_file:\n  - {path: ./a.env, format: raw}\n", "env_file entry 1.format"},
		{"a dependency's restart", "depends_on:\n  db:\n    condition: service_started\n    restart: true\n", "depends_on.db.restart"},
		{"a dependency's required", "depends_on:\n  db:\n    required: false\n", "depends_on.db.required"},
		{"through an alias", "ports:\n  - *p\n", "ports entry 1.mode"},
		{"through a merge key", "ports:\n  - <<: *p\n    published: 8080\n", "ports entry 1.mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "x-p: &p {target: 80, mode: host}\nsecrets:\n  s: {file: ./s}\nvolumes:\n  data: {}\nservices:\n  db:\n    image: alpine\n  web:\n    image: alpine\n    " + strings.ReplaceAll(tc.body, "\n", "\n    ") + "\n"
			p, err := Load(writeTemp(t, body))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := p.Services["web"].Unsupported; !slices.Contains(got, tc.want) {
				t.Errorf("ignored fields = %v, want %q among them", got, tc.want)
			}
		})
	}
	// Nothing is named for the keys opossum reads, in every long form, nor
	// for a short-form item, nor for an `x-` extension in an item.
	p, err := Load(writeTemp(t, "secrets:\n  s: {file: ./s}\nvolumes:\n  data: {}\nservices:\n  db:\n    image: alpine\n  web:\n    image: alpine\n"+
		"    ports:\n      - \"8080:80\"\n      - {target: 81, published: 8081, protocol: tcp, host_ip: 127.0.0.1, x-note: 1}\n"+
		"    volumes:\n      - ./src:/src\n      - {type: volume, source: data, target: /data, read_only: true, volume: {nocopy: true}}\n"+
		"    secrets:\n      - s\n      - {source: s, target: s2}\n"+
		"    env_file:\n      - {path: ./a.env, required: false}\n"+
		"    depends_on:\n      db:\n        condition: service_started\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := p.Services["web"].Unsupported; len(got) != 0 {
		t.Errorf("every key here is read, yet these are listed as ignored: %v", got)
	}
}
