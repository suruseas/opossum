package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// A `deploy` that comes to a service by a merge key or an alias (`other: {<<: *d}`, `other: *o`) is asked of a service nothing takes as one written there is:
// docker compose reads none of it but the counts it still casts (replicas, a restart policy's max_attempts), and refuses it all in the service that is taken
// (measured, v5.5.1, `config -q`: `base.yaml` holding the file's anchors, the service taken and another `other`, extended from `compose.yaml` — the one or the other; #1764).
func TestADeployThatComesByAMergeKeyOrAnAliasIsReadInAServiceNothingTakes(t *testing.T) {
	for _, tc := range []struct {
		name, anchors, service string
		refusedUntaken         bool
		refusedTaken           bool
	}{
		{"a merge key brings a word", "x-d: &d {deploy: abc}\n", "  other:\n    <<: *d\n    image: y\n", false, true},
		{"a merge key brings a mapping with a key it does not have", "x-d: &d {deploy: {foo: 1}}\n", "  other:\n    <<: *d\n    image: y\n", false, true},
		{"the service's own deploy stands over the merged one", "x-d: &d {deploy: {replicas: abc}}\n", "  other:\n    <<: *d\n    image: y\n    deploy: {foo: 1}\n", false, true},
		{"the service's own word stands over a merged mapping", "x-d: &d {deploy: {replicas: 2}}\n", "  other:\n    <<: *d\n    image: y\n    deploy: abc\n", false, true},
		{"the service is an alias", "x-o: &o {image: y, deploy: abc}\n", "  other: *o\n", false, true},
		{"a list of merge keys", "x-d: &d {deploy: abc}\n", "  other:\n    <<: [*d]\n    image: y\n", false, true},
		{"a merge key of a merge key", "x-a: &a {deploy: abc}\nx-b: &b {<<: *a}\n", "  other:\n    <<: *b\n    image: y\n", false, true},
		{"the count of replicas is still cast, by a merge key", "x-d: &d {deploy: {replicas: abc}}\n", "  other:\n    <<: *d\n    image: y\n", true, true},
		{"the count of replicas is still cast, in a service that is an alias", "x-o: &o {image: y, deploy: {replicas: abc}}\n", "  other: *o\n", true, true},
		{"a restart policy's attempts are still cast, by a merge key", "x-d: &d {deploy: {restart_policy: {max_attempts: abc}}}\n", "  other:\n    <<: *d\n    image: y\n", true, true},
		{"a list of labels is read, by a merge key", "x-d: &d {deploy: {labels: [a=1]}}\n", "  other:\n    <<: *d\n    image: y\n", false, false},
		{"the key itself is an alias", "x-k: &k deploy\n", "  other:\n    image: y\n    *k : abc\n", false, true},
		// A key of the wrong kind that comes in by a merge key or an alias is read in a service nothing takes, beside a `deploy` or without one (measured, v5.5.1: rc 0).
		{"a merge key brings a key of the wrong kind beside a deploy of the service's own", "x-o: &o {image: 1}\n", "  other:\n    <<: *o\n    deploy: {replicas: 2}\n", false, true},
		{"the service is an alias holding a key of the wrong kind and a deploy", "x-o: &o {image: 1, deploy: {replicas: 2}}\n", "  other: *o\n", false, true},
		// ...and without a deploy as well: the keys docker compose reads as they are in a service nothing takes are read through the merge key too (#1936).
		{"a merge key brings a key of the wrong kind and the service has no deploy", "x-o: &o {image: 1}\n", "  other:\n    <<: *o\n", false, true},
		{"written there, as before", "", "  other:\n    image: y\n    deploy: abc\n", false, true},
	} {
		for _, role := range []struct {
			name, extends string
			refused       bool
		}{{"not taken", "s", tc.refusedUntaken}, {"taken", "other", tc.refusedTaken}} {
			t.Run(tc.name+", "+role.name, func(t *testing.T) {
				dir := t.TempDir()
				for name, body := range map[string]string{
					"base.yaml":    tc.anchors + "services:\n  s: {image: x}\n" + tc.service,
					"compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: " + role.extends + "}\n",
				} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				_, err := Load(filepath.Join(dir, "compose.yaml"))
				if (err != nil) != role.refused {
					t.Errorf("%s: refused = %v, want %v (%v)", role.name, err != nil, role.refused, err)
				}
			})
		}
	}
}

// The service taken is asked of its own file, though the extender writes a `deploy` over it (measured, v5.5.1: rc 1): a list is refused, written in the service
// or held by an alias, and a word, a number or a boolean is read (rc 0). A list was read before, written in the service too (#1936).
func TestADeployListOfTheServiceTakenIsRefusedEvenWhereTheExtenderWritesOverIt(t *testing.T) {
	for _, tc := range []struct {
		name, base string
		refused    bool
	}{
		{"a list written in the service", "services:\n  s: {image: x}\n  other:\n    image: y\n    deploy: [a]\n", true},
		{"an empty list written in the service", "services:\n  s: {image: x}\n  other:\n    image: y\n    deploy: []\n", true},
		{"the service is an alias holding a list", "x-o: &o {image: y, deploy: [a]}\nservices:\n  s: {image: x}\n  other: *o\n", true},
		{"a merge key brings a list", "x-d: &d {deploy: [a]}\nservices:\n  s: {image: x}\n  other:\n    <<: *d\n    image: y\n", true},
		{"a word written in the service", "services:\n  s: {image: x}\n  other:\n    image: y\n    deploy: abc\n", false},
		{"a number written in the service", "services:\n  s: {image: x}\n  other:\n    image: y\n    deploy: 1\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range map[string]string{
				"base.yaml":    tc.base,
				"compose.yaml": "services:\n  a:\n    extends: {file: base.yaml, service: other}\n    deploy: {}\n",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Load(filepath.Join(dir, "compose.yaml")); (err != nil) != tc.refused {
				t.Errorf("refused = %v, want %v (%v)", err != nil, tc.refused, err)
			}
		})
	}
}
