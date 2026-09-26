package compose

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// docker compose gives a service, a secret and a config the rule it gives a
// volume: the name is letters, digits, `.`, `_` and `-` (v5.5.1, `config`,
// measured 2026-09-26 for every printable ASCII character between two letters,
// for the empty name and for non-ASCII letters; a network has no rule). Read
// past, a name with a space or a slash went on into a container name and a mount
// path as written.

// declaring is a file that declares one thing by name, `kind` being a service, a
// secret, a config or a network. The name is written as a JSON string, which is
// a YAML one, so that no character in it is read as syntax.
func declaring(kind, name string) string {
	q := func() string { b, _ := json.Marshal(name); return string(b) }()
	switch kind {
	case "service":
		return "services: {" + q + ": {image: alpine:3}}\n"
	case "secret":
		return "services: {a: {image: alpine:3}}\nsecrets: {" + q + ": {environment: HOME}}\n"
	case "config":
		return "services: {a: {image: alpine:3}}\nconfigs: {" + q + ": {content: x}}\n"
	case "network":
		return "services: {a: {image: alpine:3}}\nnetworks: {" + q + ": {}}\n"
	}
	panic(kind)
}

// refusalOf is what reading a file that declares one name says about it: for a
// service, the load; for a secret or a config, what CheckDeclaredNames says of
// the project the load gives (the load itself goes on — see CheckDeclaredNames).
func refusalOf(t *testing.T, kind, name string) error {
	t.Helper()
	p, err := Load(writeTemp(t, declaring(kind, name)))
	if kind == "service" {
		return err
	}
	if err != nil {
		t.Fatalf("want the %s %q read by the load, whatever its name: %v", kind, name, err)
	}
	return p.CheckDeclaredNames()
}

func allowedInName(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
}

// One row per kind and per character: a character outside the set is refused
// naming the kind, and one inside it is read.
func TestAServiceSecretOrConfigNameHasTheCharactersAVolumeNameHas(t *testing.T) {
	for _, kind := range []string{"service", "secret", "config"} {
		for c := byte(0x20); c < 0x7f; c++ {
			name := "a" + string(rune(c)) + "b"
			t.Run(fmt.Sprintf("%s %q", kind, name), func(t *testing.T) {
				err := refusalOf(t, kind, name)
				if allowedInName(c) {
					if err != nil {
						t.Fatalf("want the %s %q read, got %v", kind, name, err)
					}
					return
				}
				want := fmt.Sprintf("%s name %q %s", kind, name, volumeNameRule)
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("want a refusal saying %q, got %v", want, err)
				}
			})
		}
	}
}

// The edges of the set, a kind at a time: a name may start or end with any of
// `.`, `_`, `-`; it may not be empty or hold a letter outside ASCII; and a name
// docker compose takes is taken.
func TestTheEdgesOfAName(t *testing.T) {
	for _, kind := range []string{"service", "secret", "config"} {
		for _, tc := range []struct {
			name string
			ok   bool
		}{
			{"-a", true}, {".a", true}, {"_a", true}, {"a-", true}, {"a.", true}, {"a_", true},
			{"A", true}, {"0", true}, {"web-1.v2_x", true},
			{"", false}, {"é", false}, {"日本", false}, {"a b", false}, {" a", false}, {"a ", false},
		} {
			t.Run(fmt.Sprintf("%s %q", kind, tc.name), func(t *testing.T) {
				err := refusalOf(t, kind, tc.name)
				if tc.ok && err != nil {
					t.Fatalf("want it read, got %v", err)
				}
				if !tc.ok && (err == nil || !strings.Contains(err.Error(), kind+" name")) {
					t.Fatalf("want a refusal of the %s name, got %v", kind, err)
				}
			})
		}
	}
}

// A network has no rule in docker compose, and none is added: a key with a `+`
// is read (the runtime name folds it, see NetworkRuntimeKey).
func TestANetworkKeyIsNotHeldToTheRule(t *testing.T) {
	for _, name := range []string{"a+b", "a b", "a/b", "é"} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeTemp(t, declaring("network", name))); err != nil {
				t.Fatalf("want the network %q read, as docker compose reads it, got %v", name, err)
			}
		})
	}
}

// Where the name is written does not matter: in the second file of two, the
// refusal names both files (the merged document is what is read), and of two
// names outside the rule the same one is named every time.
func TestABadNameInASecondFileAndTwoBadNames(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "a.yaml")
	over := filepath.Join(dir, "b.yaml")
	write(t, base, "services: {web: {image: alpine:3}}\n")
	write(t, over, "services: {\"w b\": {image: alpine:3}}\n")
	_, err := LoadFiles([]string{base, over}, nil)
	if err == nil || !strings.Contains(err.Error(), `service name "w b"`) {
		t.Fatalf("want the name in the second file refused, got %v", err)
	}
	both := writeTemp(t, "services: {\"z z\": {image: alpine:3}, \"m m\": {image: alpine:3}, \"q q\": {image: alpine:3}}\n")
	for i := 0; i < 20; i++ {
		_, err := Load(both)
		if err == nil || !strings.Contains(err.Error(), `service name "m m"`) {
			t.Fatalf("run %d: want the first of them in sorted order, got %v", i, err)
		}
	}
}

// Of several names, the one refused is the first in sorted order — and the name
// that comes first is not always the bad one, so a check that looked at the
// first alone would pass every file above.
func TestTheBadNameIsFoundWhereverItSits(t *testing.T) {
	for _, kind := range []string{"service", "secret", "config"} {
		t.Run(kind, func(t *testing.T) {
			var body string
			switch kind {
			case "service":
				body = "services: {a: {image: alpine:3}, \"b c\": {image: alpine:3}, d: {image: alpine:3}}\n"
			case "secret":
				body = "services: {a: {image: alpine:3}}\nsecrets: {a: {environment: HOME}, \"b c\": {environment: HOME}, d: {environment: HOME}}\n"
			case "config":
				body = "services: {a: {image: alpine:3}}\nconfigs: {a: {content: x}, \"b c\": {content: x}, d: {content: x}}\n"
			}
			p, err := Load(writeTemp(t, body))
			if kind != "service" {
				if err != nil {
					t.Fatal(err)
				}
				err = p.CheckDeclaredNames()
			}
			if err == nil || !strings.Contains(err.Error(), kind+` name "b c"`) {
				t.Fatalf("want the %s name \"b c\" refused, got %v", kind, err)
			}
		})
	}
}

// A secret is looked at before a config, so a file with one of each is refused
// for the same one every time it is read.
func TestASecretIsRefusedBeforeAConfig(t *testing.T) {
	p, err := Load(writeTemp(t, "services: {a: {image: alpine:3}}\nsecrets: {\"s s\": {environment: HOME}}\nconfigs: {\"c c\": {content: x}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CheckDeclaredNames(); err == nil || !strings.Contains(err.Error(), `secret name "s s"`) {
		t.Fatalf("want the secret first, got %v", err)
	}
}
