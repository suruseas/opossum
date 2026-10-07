package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// `cpus` is cast by docker compose before it knows the service is not taken, so a word or a blank of it is refused there too (#1574); a list or a mapping of it is not
// asked of a service nothing takes (measured, v5.5.1, `config -q`: a file `base.yaml` with the service taken and another `other` holding the value, and a
// `compose.yaml` that extends the one or the other). In the service taken, every one of them is refused but a number.
func TestACpusThatIsAListOrAMappingIsReadInAServiceNothingTakes(t *testing.T) {
	for _, tc := range []struct {
		name, value    string
		refusedUntaken bool
		refusedTaken   bool
	}{
		{"a list", "[1]", false, true},
		{"an empty list", "[]", false, true},
		{"a mapping", "{a: 1}", false, true},
		{"an empty mapping", "{}", false, true},
		{"a list through an alias", "*l", false, true},
		{"a mapping through an alias", "*m", false, true},
		{"a word", "abc", true, true},
		{"a number with a blank around it", `" 2 "`, true, true},
		{"a blank", `""`, true, true},
		{"a number", "2", false, false},
	} {
		for _, role := range []struct {
			name    string
			extends string
			refused bool
		}{{"not taken", "s", tc.refusedUntaken}, {"taken", "other", tc.refusedTaken}} {
			t.Run(tc.name+", "+role.name, func(t *testing.T) {
				dir := t.TempDir()
				for name, body := range map[string]string{
					"base.yaml":    "x-l: &l [1]\nx-m: &m {a: 1}\nservices:\n  s:\n    image: x\n  other:\n    image: y\n    cpus: " + tc.value + "\n",
					"compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: " + role.extends + "}\n",
				} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				_, err := Load(filepath.Join(dir, "compose.yaml"))
				if (err != nil) != role.refused {
					t.Errorf("cpus: %s in a service %s: refused = %v, want %v (%v)", tc.value, role.name, err != nil, role.refused, err)
				}
			})
		}
	}
}

// The service taken is asked of its own file, even where the extender writes a number over the list (measured, v5.5.1: refused, rc 1).
func TestACpusThatIsAListOfTheServiceTakenIsRefusedEvenWhereTheExtenderWritesOverIt(t *testing.T) {
	for _, value := range []string{"[1]", "{a: 1}"} {
		t.Run(value, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range map[string]string{
				"base.yaml":    "services:\n  other:\n    image: y\n    cpus: " + value + "\n",
				"compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: other}\n    cpus: 2\n",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Load(filepath.Join(dir, "compose.yaml")); err == nil {
				t.Errorf("cpus: %s of the service taken, written over with a number, is read (docker compose refuses it)", value)
			}
		})
	}
}
