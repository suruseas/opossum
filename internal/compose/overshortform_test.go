package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeFiles(t *testing.T, docs ...string) []string {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for i, d := range docs {
		p := filepath.Join(dir, fmt.Sprintf("f%d.yaml", i))
		if err := os.WriteFile(p, []byte(d), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return paths
}

// What a later `-f` file writes over a `depends_on` an earlier one gave: an entry written by name has a condition
// (`service_started`) and a `required` (true) as well, so a `required: ~` over it — or over a long entry that did not write
// `required` — is "not given" (docker compose v5.5.1, measured with `config`; the answers are docker compose's). Refused
// where nothing earlier gave it, and for a key that has no default (`restart: ~`). The value is `Optional`, which is
// `required: false`: a `required: ~` over `false` leaves it false.
func TestANullOverADependencyAnEarlierFileGaveIsNotGiven(t *testing.T) {
	head := "services:\n  db: {image: x}\n  web:\n    image: y\n"
	for _, tc := range []struct {
		name         string
		first, later string
		want         string // "REFUSE", or the Optional of the dependency
	}{
		{"a list, then required: ~ beside the condition", head + "    depends_on: [db]\n", head + "    depends_on: {db: {condition: service_started, required: ~}}\n", "false"},
		{"a long entry with no required, then required: ~", head + "    depends_on: {db: {condition: service_started}}\n", head + "    depends_on: {db: {condition: service_started, required: ~}}\n", "false"},
		{"a list, then required: ~ alone", head + "    depends_on: [db]\n", head + "    depends_on: {db: {required: ~}}\n", "false"},
		{"required: false, then required: ~ (stays false)", head + "    depends_on: {db: {condition: service_started, required: false}}\n", head + "    depends_on: {db: {condition: service_started, required: ~}}\n", "true"},
		{"a list, then condition: ~", head + "    depends_on: [db]\n", head + "    depends_on: {db: {condition: ~}}\n", "false"},
		{"nothing earlier, required: ~ alone", head, head + "    depends_on: {db: {required: ~}}\n", "REFUSE"},
		{"a list, then restart: ~ (no default)", head + "    depends_on: [db]\n", head + "    depends_on: {db: {condition: service_started, restart: ~}}\n", "REFUSE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := LoadFiles(writeFiles(t, tc.first, tc.later), nil)
			got := "REFUSE"
			if err == nil {
				deps := p.Services["web"].DependsOn
				if len(deps) != 1 {
					t.Fatalf("depends_on = %v, want the one dependency", deps)
				}
				got = fmt.Sprint(deps[0].Optional)
			}
			if got != tc.want {
				t.Errorf("%s: %s, want %s (err %v)", tc.name, got, tc.want, err)
			}
		})
	}
}

// What a later `-f` file writes over a limit in `ulimits` an earlier one gave as `{soft, hard}`: one side only, a null on
// a side, or an empty mapping, is merged into the earlier two (docker compose v5.5.1, measured with `config`;
// the limit is read as `soft:hard`). Over a limit the earlier file gave as one number it is refused there, and a
// limit that no earlier file gave, and a null for the whole limit, are refused as ever.
func TestAPartOfALimitOverOneAnEarlierFileGaveIsMergedIntoIt(t *testing.T) {
	w := "services:\n  web:\n    image: y\n"
	both := w + "    ulimits: {nofile: {soft: 1, hard: 2}}\n"
	one := w + "    ulimits: {nofile: 5}\n"
	for _, tc := range []struct {
		name         string
		first, later string
		want         string // "REFUSE", or "soft:hard" of nofile
	}{
		{"soft only", both, w + "    ulimits: {nofile: {soft: 9}}\n", "9:2"},
		{"hard only", both, w + "    ulimits: {nofile: {hard: 9}}\n", "1:9"},
		{"soft: ~", both, w + "    ulimits: {nofile: {soft: ~}}\n", "1:2"},
		{"hard: ~", both, w + "    ulimits: {nofile: {hard: ~}}\n", "1:2"},
		{"both ~", both, w + "    ulimits: {nofile: {soft: ~, hard: ~}}\n", "1:2"},
		{"soft with hard: ~", both, w + "    ulimits: {nofile: {soft: 5, hard: ~}}\n", "5:2"},
		{"an empty mapping", both, w + "    ulimits: {nofile: {}}\n", "1:2"},
		{"both written", both, w + "    ulimits: {nofile: {soft: 9, hard: 8}}\n", "9:8"},
		{"the whole limit ~", both, w + "    ulimits: {nofile: ~}\n", "REFUSE"},
		{"another limit, one side", both, w + "    ulimits: {nproc: {soft: 3}}\n", "REFUSE"},
		{"over one number, soft only", one, w + "    ulimits: {nofile: {soft: 9}}\n", "REFUSE"},
		{"over one number, an empty mapping", one, w + "    ulimits: {nofile: {}}\n", "REFUSE"},
		{"over one number, both written", one, w + "    ulimits: {nofile: {soft: 9, hard: 8}}\n", "9:8"},
		{"nothing earlier, hard: ~", w, w + "    ulimits: {nofile: {hard: ~}}\n", "REFUSE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := LoadFiles(writeFiles(t, tc.first, tc.later), nil)
			got := "REFUSE"
			if err == nil {
				u := p.Services["web"].Ulimits["nofile"]
				got = fmt.Sprintf("%d:%d", u.Soft, u.Hard)
			}
			if got != tc.want {
				t.Errorf("%s: %s, want %s (err %v)", tc.name, got, tc.want, err)
			}
		})
	}
}

// What a later file writes over a dependency an earlier file gave with `required: false` leaves the `required` as it is
// unless the later file writes one, or lists the dependency by name (a listed name is `{condition: service_started,
// required: true}`): docker compose gives `required` its default after the merge, not to each side of it (measured,
// v5.5.1). A default put in the later file's own entry — the first form of the change of #1585 — turned an optional
// dependency into a required one, for a later `{condition: service_healthy}` and for an `extends`. The value is
// `Optional`, which is `required: false`.
func TestARequiredFalseIsKeptWhereALaterEntryWritesNoRequired(t *testing.T) {
	head := "services:\n  db:\n    image: x\n    healthcheck: {test: [CMD, 'true']}\n"
	web := head + "  web:\n    image: y\n"
	optional := "    depends_on: {db: {condition: service_started, required: false}}\n"
	for _, tc := range []struct {
		name  string
		files []string
		want  string // Optional
	}{
		{"a later entry with another condition", []string{web + optional, web + "    depends_on: {db: {condition: service_healthy}}\n"}, "true"},
		{"a later entry with a key that is not required", []string{web + optional, web + "    depends_on: {db: {condition: service_started, restart: true}}\n"}, "true"},
		{"a later entry that lists the name (required: true)", []string{web + optional, web + "    depends_on: [db]\n"}, "false"},
		{"three files: false, a listed name, required: ~", []string{web + optional, web + "    depends_on: [db]\n", web + "    depends_on: {db: {condition: service_started, required: ~}}\n"}, "false"},
		{"extends: a base with false, a service with another condition", []string{head + "  base:\n    image: y\n" + optional + "  web:\n    extends: base\n    depends_on: {db: {condition: service_healthy}}\n"}, "true"},
		{"extends: a base with false, a service that lists the name", []string{head + "  base:\n    image: y\n" + optional + "  web:\n    extends: base\n    depends_on: [db]\n"}, "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := LoadFiles(writeFiles(t, tc.files...), nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			deps := p.Services["web"].DependsOn
			if len(deps) != 1 {
				t.Fatalf("depends_on = %v, want the one dependency", deps)
			}
			if got := fmt.Sprint(deps[0].Optional); got != tc.want {
				t.Errorf("%s: Optional = %s, want %s", tc.name, got, tc.want)
			}
		})
	}
}
