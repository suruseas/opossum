package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// In a file that is only extended from (`extends: {file: base.yaml, service: s}`), a service the extending service
// does not name is not taken, and docker compose v5.5.1 reads its `deploy` as it is whatever it holds: a word, a
// list, a number, `true`, a key it does not know (measured with `config`; the answers are docker compose's). A
// number of replicas it still casts, so `replicas: abc` is refused there and `replicas: [1]` is not. The service
// that is extended is taken, and `deploy` there is refused when it is not a mapping of the keys docker compose takes.
// `deploy.resources.limits.cpus: -1` is read by docker compose in both and refused here in both, on purpose (see the
// test of `cpus`; the runtime refuses a negative count at start).
func TestTheDeployOfAServiceThatIsNotTakenIsReadAsItIs(t *testing.T) {
	for _, tc := range []struct {
		deploy    string
		untaken   string // "read" or "REFUSE": docker compose's answer for a service of the file that is not taken
		taken     string // and for the service that is extended
		takenHere string // what is given here for the taken one, where it differs (deliberate or another difference)
	}{
		{"abc", "read", "REFUSE", ""},
		{"[1]", "read", "REFUSE", ""},
		{"1", "read", "REFUSE", ""},
		{"true", "read", "REFUSE", ""},
		{"{foo: 1}", "read", "REFUSE", ""},
		{"~", "read", "read", ""},
		{"{resources: abc}", "read", "REFUSE", ""},
		{"{resources: {limits: {memory: [1]}}}", "read", "REFUSE", ""},
		{"{replicas: abc}", "REFUSE", "REFUSE", ""},
		{"{replicas: [1]}", "read", "REFUSE", ""},
		{"{mode: 5}", "read", "REFUSE", ""},
		{"{resources: {limits: {cpus: -1}}}", "read", "read", "REFUSE"}, // refused here on purpose
	} {
		for place, doc := range map[string]string{
			"not taken": "services:\n  s: {image: x}\n  other:\n    image: y\n    deploy: %s\n",
			"taken":     "services:\n  s:\n    image: x\n    deploy: %s\n",
		} {
			want := tc.untaken
			if place == "taken" {
				want = tc.taken
				if tc.takenHere != "" {
					want = tc.takenHere
				}
			}
			t.Run(place+"/"+tc.deploy, func(t *testing.T) {
				dir := t.TempDir()
				body := fmt.Sprintf(doc, tc.deploy)
				if err := os.WriteFile(filepath.Join(dir, "base.yaml"), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
				main := filepath.Join(dir, "compose.yaml")
				if err := os.WriteFile(main, []byte("services:\n  a:\n    extends: {file: base.yaml, service: s}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				_, err := LoadFiles([]string{main}, nil)
				got := "read"
				if err != nil {
					got = "REFUSE"
				}
				if got != want {
					t.Errorf("deploy: %s in a service that is %s: %s, want %s (err %v)", tc.deploy, place, got, want, err)
				}
			})
		}
	}
}

// What docker compose reads of a `deploy` it does not take is what the file comes to, not what the node looks like:
// `replicas` behind an alias (`deploy: *d`) or a merge key (`deploy: {<<: *d}`, `<<: [*a, *b]`) is cast there as one
// written in the service, so `replicas: abc` is refused, and a `deploy` that holds only a key it does not know is read.
// Dropped as a node that is not a mapping, or looked for in a mapping that does not name `replicas`, the first was passed
// (a regression found in review: `x-deploy: &default` is a common way to write it). And what follows a `deploy` in the
// service is still asked: a copy that dropped the keys beside it passed `privileged: "7"`, `scale: two` and `ports: abc`,
// which docker compose refuses there.
func TestTheDeployOfAServiceThatIsNotTakenIsReadAsTheFileComesToIt(t *testing.T) {
	for _, tc := range []struct{ name, base, want string }{
		{"alias: deploy: *d (d = {replicas: abc})", "x-d: &d {replicas: abc}\nservices:\n  s: {image: x}\n  other:\n    image: y\n    deploy: *d\n", "REFUSE"},
		{"merge key: deploy: {<<: *d} (d = {replicas: abc})", "x-d: &d {replicas: abc}\nservices:\n  s: {image: x}\n  other:\n    image: y\n    deploy: {<<: *d}\n", "REFUSE"},
		{"merge list: deploy: {<<: [*a, *b]}", "x-a: &a {foo: 1}\nx-b: &b {replicas: abc}\nservices:\n  s: {image: x}\n  other:\n    image: y\n    deploy: {<<: [*a, *b]}\n", "REFUSE"},
		{"alias: deploy: *d (d = abc)", "x-d: &d abc\nservices:\n  s: {image: x}\n  other:\n    image: y\n    deploy: *d\n", "read"},
		{"merge key: deploy: {<<: *d} (d = {foo: 1})", "x-d: &d {foo: 1}\nservices:\n  s: {image: x}\n  other:\n    image: y\n    deploy: {<<: *d}\n", "read"},
		{"alias: replicas: *r", "x-r: &r abc\nservices:\n  s: {image: x}\n  other:\n    image: y\n    deploy: {replicas: *r}\n", "REFUSE"},
		{"alias: deploy: *d (d = {replicas: 2, foo: 1})", "x-d: &d {replicas: 2, foo: 1}\nservices:\n  s: {image: x}\n  other:\n    image: y\n    deploy: *d\n", "read"},
		{"deploy then another refused key (privileged)", "services:\n  s: {image: x}\n  other:\n    image: y\n    deploy: {replicas: 2}\n    privileged: \"7\"\n", "REFUSE"},
		{"deploy (a word) then a refused scale", "services:\n  s: {image: x}\n  other:\n    deploy: abc\n    scale: two\n    image: y\n", "REFUSE"},
		{"deploy (a word) then a refused ports", "services:\n  s: {image: x}\n  other:\n    image: y\n    deploy: abc\n    ports: abc\n", "REFUSE"},
		{"deploy {replicas: 2, foo: 1}", "services:\n  s: {image: x}\n  other:\n    image: y\n    deploy: {replicas: 2, foo: 1}\n", "read"},
		{"deploy {foo: 1, replicas: abc}", "services:\n  s: {image: x}\n  other:\n    image: y\n    deploy: {foo: 1, replicas: abc}\n", "REFUSE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "base.yaml"), []byte(tc.base), 0o644); err != nil {
				t.Fatal(err)
			}
			main := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(main, []byte("services:\n  a:\n    extends: {file: base.yaml, service: s}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadFiles([]string{main}, nil)
			got := "read"
			if err != nil {
				got = "REFUSE"
			}
			if got != tc.want {
				t.Errorf("%s: %s, want %s (err %v)", tc.name, got, tc.want, err)
			}
		})
	}
}
