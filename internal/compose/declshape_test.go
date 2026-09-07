package compose

// The keys of a top-level declaration (#822, from the declaration-key
// sweep): `name`, `file` and `driver` written as something other than a
// string. docker compose (v5.5.0) refuses a number, a boolean, a list, a
// mapping or nothing there (`networks.n.name must be a string`), and a
// date with its own words (`expected type 'string', got unconvertible
// type 'time.Time'`);
// the struct decode here read `name: 42` as the name "42", and `driver`,
// which opossum does not act on, took any shape at all (a bare `driver:`
// was a recorded difference). A quoted `"42"` is a string and is taken;
// `driver: ""` is taken by both. A key in a secret's declaration that
// opossum does not read used to be dropped without a word, where a
// network's or a volume's is listed among the ignored fields.

import (
	"strings"
	"testing"
)

func TestADeclarationKeyThatIsNotAStringIsRefused(t *testing.T) {
	net := "services:\n  web:\n    image: alpine\n    networks: [back]\nnetworks:\n  back:\n    "
	vol := "services:\n  web:\n    image: alpine\n    volumes: [data:/d]\nvolumes:\n  data:\n    "
	sec := "services:\n  web:\n    image: alpine\n    secrets: [s]\nsecrets:\n  s:\n    "
	for _, tc := range []struct{ name, body, want string }{
		{"a network's name, a number", net + "name: 42\n", "a network's declaration: name must be a string, got a number (line 7) — quote it (`\"42\"`) if it is meant literally"},
		{"a network's name, a boolean", net + "name: true\n", "a network's declaration: name must be a string, got true/false"},
		{"a network's name, a date", net + "name: 2020-01-01\n", "a network's declaration: name must be a string, got a date"},
		{"a network's driver, a number", net + "driver: 42\n", "a network's declaration: driver must be a string, got a number"},
		{"a network's driver, a list", net + "driver: [a]\n", "a network's declaration: driver must be a string, got a list (line 7)"},
		{"a network's driver, a mapping", net + "driver: {a: b}\n", "a network's declaration: driver must be a string, got a mapping"},
		{"a network's driver, nothing after it", net + "driver:\n", "a network's declaration: driver has nothing after it (line 7)"},
		{"a volume's name, a number", vol + "name: 42\n", "a volume's declaration: name must be a string, got a number"},
		{"a volume's driver, a boolean", vol + "driver: true\n", "a volume's declaration: driver must be a string, got true/false"},
		{"a volume's driver, nothing after it", vol + "driver:\n", "a volume's declaration: driver has nothing after it"},
		{"a secret's file, a number", sec + "file: 42\n", "a secret's declaration: file must be a string, got a number"},
		{"a secret's name, a number", sec + "file: ./s\n    name: 42\n", "a secret's declaration: name must be a string, got a number"},
		{"a secret's name, nothing after it", sec + "file: ./s\n    name:\n", "a secret's declaration: name has nothing after it (line 8)"},
		{"a secret's driver, a number", sec + "file: ./s\n    driver: 42\n", "a secret's declaration: driver must be a string, got a number"},
		{"a secret's driver, nothing after it", sec + "file: ./s\n    driver:\n", "a secret's declaration: driver has nothing after it (line 8)"},
		{"through an alias", "x-n: &n 42\n" + net + "name: *n\n", "a network's declaration: name must be a string, got a number (line 1)"},
		{"brought in by a merge key", "x-b: &b {name: 42}\n" + net + "<<: *b\n", "a network's declaration: name must be a string, got a number"},
		{"brought in by the second of two merge keys", "x-a: &a {driver: bridge}\nx-b: &b {driver: [x]}\n" + vol + "<<: [*a, *b]\n", "a volume's declaration: driver must be a string, got a list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, got)
			}
		})
	}
	// Taken, as docker compose takes them: a quoted number is a string; an
	// empty driver is a string too, and listed as ignored like any driver.
	p, err := Load(writeTemp(t, net+"name: \"42\"\n    driver: \"\"\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := p.Networks["back"].Name; got != "42" {
		t.Errorf("name = %q, want the quoted text", got)
	}
	if got := strings.Join(p.Unsupported, ","); !strings.Contains(got, "networks.back.driver") {
		t.Errorf("ignored fields = %q, want networks.back.driver listed", got)
	}
	// With several -f files a later file's bare `driver:` is "not given".
	base := writeTemp(t, net+"driver: bridge\n")
	if _, err := LoadFiles([]string{base, writeTemp(t, "networks:\n  back:\n    driver:\n")}, nil); err != nil {
		t.Errorf("a bare driver: in a later file should keep the earlier file's, got: %v", err)
	}
}

func TestASecretDeclarationKeyOpossumDoesNotReadIsListedAsIgnored(t *testing.T) {
	sec := "services:\n  web:\n    image: alpine\n    secrets: [s]\nsecrets:\n  s:\n    file: ./s\n    name: real\n    labels: {a: b}\n    x-note: 1\n"
	p, err := Load(writeTemp(t, sec))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := strings.Join(p.Unsupported, ",")
	for _, want := range []string{"secrets.s.name", "secrets.s.labels"} {
		if !strings.Contains(got, want) {
			t.Errorf("ignored fields = %q, want %s listed", got, want)
		}
	}
	if strings.Contains(got, "x-note") || strings.Contains(got, "secrets.s.file") {
		t.Errorf("ignored fields = %q: an x- key and a read key should not be listed", got)
	}
}
