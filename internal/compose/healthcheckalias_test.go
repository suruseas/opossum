package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// The keys inside a `healthcheck` written as aliases (`*r : abc`, `x-r: &r retries`) are the keys they stand for in a service nothing takes, as they are written plainly: docker compose
// casts `retries` and `disable` there, and reads the others (measured, v5.5.1, `config -q`, #1962). A service that is taken refuses what it refuses written plainly.
func TestAHealthcheckKeyThatIsAnAliasIsTheKeyItStandsFor(t *testing.T) {
	const header = "x-r: &r retries\nx-d: &d disable\nx-i: &i interval\nx-z: &z abc\nx-t: &t true\nx-hc: &hc {retries: ~}\nx-hm: &hm {<<: {retries: abc}, a: ~}\nx-hd: &hd {<<: {disable: abc}, test: ~}\nx-hk: &hk healthcheck\n"
	for _, tc := range []struct {
		name, spec string
		target     string // the service the extending one takes: x (nothing takes y) or y
		refused    bool
		over       bool // the extending service writes a `healthcheck` of its own
	}{
		{"retries a word, written plainly", "healthcheck: {retries: abc}", "x", true, false},
		{"retries a word, a key that is an alias", "healthcheck: {*r : abc}", "x", true, false},
		{"disable a word, written plainly", "healthcheck: {disable: abc}", "x", true, false},
		{"disable a word, a key that is an alias", "healthcheck: {*d : abc}", "x", true, false},
		{"disable a word through an alias, written plainly", "healthcheck: {disable: *z}", "x", true, false},
		{"disable a word through an alias, a key that is an alias", "healthcheck: {*d : *z}", "x", true, false},
		{"retries a number, a key that is an alias", "healthcheck: {*r : 3}", "x", false, false},
		{"interval a number, a key that is an alias", "healthcheck: {*i : 5}", "x", false, false},
		{"interval a number, written plainly", "healthcheck: {interval: 5}", "x", false, false},
		{"retries a word, a key that is an alias, in a service that is taken", "healthcheck: {*r : abc}", "y", true, false},
		{"interval a number, a key that is an alias, in a service that is taken", "healthcheck: {*i : 5}", "y", true, false},
		{"retries true, written plainly, is read in a service nothing takes", "healthcheck: {retries: true}", "x", false, false},
		{"retries true, a key that is an alias, is read in a service nothing takes", "healthcheck: {*r : true}", "x", false, false},
		{"retries 2024-01-01, written plainly, is read in a service nothing takes", "healthcheck: {retries: 2024-01-01}", "x", false, false},
		{"retries 2024-01-01, a key that is an alias, is read in a service nothing takes", "healthcheck: {*r : 2024-01-01}", "x", false, false},
		{"retries [a], written plainly, is read in a service nothing takes", "healthcheck: {retries: [a]}", "x", false, false},
		{"retries [a], a key that is an alias, is read in a service nothing takes", "healthcheck: {*r : [a]}", "x", false, false},
		{"retries {a: 1}, written plainly, is read in a service nothing takes", "healthcheck: {retries: {a: 1}}", "x", false, false},
		{"retries {a: 1}, a key that is an alias, is read in a service nothing takes", "healthcheck: {*r : {a: 1}}", "x", false, false},
		{"retries 3, written plainly, is read in a service nothing takes", "healthcheck: {retries: 3}", "x", false, false},
		{"retries 3, a key that is an alias, is read in a service nothing takes", "healthcheck: {*r : 3}", "x", false, false},
		{"retries *t, written plainly, is read in a service nothing takes", "healthcheck: {retries: *t}", "x", false, false},
		{"retries *t, a key that is an alias, is read in a service nothing takes", "healthcheck: {*r : *t}", "x", false, false},
		{"disable 5, a key that is an alias, is read in a service nothing takes", "healthcheck: {*d : 5}", "x", false, false},
		{"disable true, a key that is an alias, is read in a service nothing takes", "healthcheck: {*d : true}", "x", false, false},
		{"retries [a], written plainly, the extender writes a healthcheck over: refused in the file as it was", "healthcheck: {retries: [a]}", "y", true, true},
		{"retries [a], a key that is an alias, the extender writes a healthcheck over: refused in the file", "healthcheck: {*r : [a]}", "y", true, true},
		{"retries {a: 1}, written plainly, the extender writes a healthcheck over: refused in the file as it was", "healthcheck: {retries: {a: 1}}", "y", true, true},
		{"retries {a: 1}, a key that is an alias, the extender writes a healthcheck over: refused in the file", "healthcheck: {*r : {a: 1}}", "y", true, true},
		{"retries true, written plainly, the extender writes a healthcheck over: read", "healthcheck: {retries: true}", "y", false, true},
		{"retries true, a key that is an alias, the extender writes a healthcheck over: read", "healthcheck: {*r : true}", "y", false, true},
		{"retries true, a key that is an alias, nothing is written over it: refused", "healthcheck: {*r : true}", "y", true, false},
		{"retries 2024-01-01, written plainly, the extender writes a healthcheck over: read", "healthcheck: {retries: 2024-01-01}", "y", false, true},
		{"retries 2024-01-01, a key that is an alias, the extender writes a healthcheck over: read", "healthcheck: {*r : 2024-01-01}", "y", false, true},
		{"retries 2024-01-01, a key that is an alias, nothing is written over it: refused", "healthcheck: {*r : 2024-01-01}", "y", true, false},
		{"retries nothing, in a service nothing takes", "healthcheck: {retries: ~}", "x", false, false},
		{"retries nothing beside a test, in a service nothing takes", "healthcheck: {test: [CMD, \"true\"], retries: ~}", "x", false, false},
		{"retries nothing and disable nothing", "healthcheck: {retries: ~, disable: ~}", "x", false, false},
		{"retries nothing beside a key that is read", "healthcheck: {retries: ~, interval: 5}", "x", false, false},
		{"retries nothing beside a disable that is true", "healthcheck: {retries: ~, disable: true}", "x", false, false},
		{"retries nothing, a key that is an alias", "healthcheck: {*r : ~}", "x", false, false},
		{"retries nothing beside a word for disable", "healthcheck: {retries: ~, disable: abc}", "x", true, false},
		{"retries a word beside a disable of nothing", "healthcheck: {retries: abc, disable: ~}", "x", true, false},
		{"retries nothing, in a service that is taken", "healthcheck: {retries: ~}", "y", true, false},
		{"retries nothing, in a service that is taken, the extender writes a healthcheck over", "healthcheck: {retries: ~}", "y", false, true},
		{"retries nothing in a healthcheck that is the value of an alias", "healthcheck: *hc", "x", false, false},
		{"a retries that is a word, through a merge key in a healthcheck that is an alias, beside a key of nothing", "healthcheck: *hm", "x", true, false},
		{"a disable that is a word, through a merge key in a healthcheck that is an alias, beside a key of nothing", "healthcheck: *hd", "x", true, false},
		{"retries nothing, the key healthcheck an alias", "*hk : {retries: ~}", "x", false, false},
		{"retries a word, a key that is an alias, the extender writes a healthcheck over", "healthcheck: {*r : abc}", "y", true, true},
		{"disable a word, a key that is an alias, the extender writes a healthcheck over", "healthcheck: {*d : abc}", "y", true, true},
		{"retries a word, written plainly, the extender writes a healthcheck over", "healthcheck: {retries: abc}", "y", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			own := ""
			if tc.over {
				own = "    healthcheck: {retries: 3}\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  web:\n    extends: {file: base.yaml, service: "+tc.target+"}\n"+own), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "base.yaml"), []byte(header+"services:\n  x:\n    image: xi\n  y:\n    image: yi\n    "+tc.spec+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFiles([]string{filepath.Join(dir, "compose.yaml")}, nil); (err != nil) != tc.refused {
				t.Errorf("refused = %v (%v), want %v", err != nil, err, tc.refused)
			}
		})
	}
}
