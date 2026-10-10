package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A later file that writes only the `required` of a dependency an earlier file gave (`db: {required: false}` over `depends_on: [db]`, or over the long form) leaves the
// dependency its condition: docker compose merges the entries (v5.5.1, `config`, every row measured), where the file read on its own would be a `required` with no `condition`,
// which docker compose refuses when it is all there is (#1766). A `required` of the wrong kind, a `condition` written, a `restart` and an empty entry are read as they were.
func TestALaterFileThatWritesOnlyRequiredKeepsTheEarlierCondition(t *testing.T) {
	for _, tc := range []struct {
		name, first, second string
		want                string // `name=condition,required` of each dependency of web, or ERR
	}{
		{"list | later required false", `services:
  web:
    image: wi
    depends_on: [db]
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: false}
`, "db=service_started,false"},
		{"list | later required true", `services:
  web:
    image: wi
    depends_on: [db]
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: true}
`, "db=service_started,true"},
		{"list | later required \"false\"", `services:
  web:
    image: wi
    depends_on: [db]
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: "false"}
`, "db=service_started,false"},
		{"list | later required ~", `services:
  web:
    image: wi
    depends_on: [db]
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: ~}
`, "db=service_started,true"},
		{"list | later required 5", `services:
  web:
    image: wi
    depends_on: [db]
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: 5}
`, "ERR"},
		{"list | later condition only", `services:
  web:
    image: wi
    depends_on: [db]
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {condition: service_started}
`, "db=service_started,true"},
		{"list | later restart true", `services:
  web:
    image: wi
    depends_on: [db]
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {restart: true}
`, "db=service_started,true"},
		{"list | later required false + restart", `services:
  web:
    image: wi
    depends_on: [db]
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: false, restart: true}
`, "db=service_started,false"},
		{"list | later empty", `services:
  web:
    image: wi
    depends_on: [db]
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {}
`, "db=service_started,true"},
		{"list | later condition + required false", `services:
  web:
    image: wi
    depends_on: [db]
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {condition: service_healthy, required: false}
`, "db=service_healthy,false"},
		{"list | later condition + required true", `services:
  web:
    image: wi
    depends_on: [db]
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {condition: service_started, required: true}
`, "db=service_started,true"},
		{"long started | later required false", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_started}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: false}
`, "db=service_started,false"},
		{"long started | later required true", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_started}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: true}
`, "db=service_started,true"},
		{"long started | later required \"false\"", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_started}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: "false"}
`, "db=service_started,false"},
		{"long started | later required ~", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_started}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: ~}
`, "db=service_started,true"},
		{"long started | later required 5", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_started}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: 5}
`, "ERR"},
		{"long started | later condition only", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_started}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {condition: service_started}
`, "db=service_started,true"},
		{"long started | later restart true", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_started}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {restart: true}
`, "db=service_started,true"},
		{"long started | later required false + restart", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_started}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: false, restart: true}
`, "db=service_started,false"},
		{"long started | later empty", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_started}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {}
`, "db=service_started,true"},
		{"long started | later condition + required false", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_started}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {condition: service_healthy, required: false}
`, "db=service_healthy,false"},
		{"long started | later condition + required true", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_started}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {condition: service_started, required: true}
`, "db=service_started,true"},
		{"long healthy | later required false", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: false}
`, "db=service_healthy,false"},
		{"long healthy | later required true", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: true}
`, "db=service_healthy,true"},
		{"long healthy | later required \"false\"", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: "false"}
`, "db=service_healthy,false"},
		{"long healthy | later required ~", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: ~}
`, "db=service_healthy,true"},
		{"long healthy | later required 5", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: 5}
`, "ERR"},
		{"long healthy | later condition only", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {condition: service_started}
`, "db=service_started,true"},
		{"long healthy | later restart true", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {restart: true}
`, "db=service_healthy,true"},
		{"long healthy | later required false + restart", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: false, restart: true}
`, "db=service_healthy,false"},
		{"long healthy | later empty", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {}
`, "db=service_healthy,true"},
		{"long healthy | later condition + required false", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {condition: service_healthy, required: false}
`, "db=service_healthy,false"},
		{"long healthy | later condition + required true", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {condition: service_started, required: true}
`, "db=service_started,true"},
		{"long completed | later required false", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_completed_successfully}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: false}
`, "db=service_completed_successfully,false"},
		{"long completed | later required true", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_completed_successfully}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: true}
`, "db=service_completed_successfully,true"},
		{"long completed | later required \"false\"", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_completed_successfully}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: "false"}
`, "db=service_completed_successfully,false"},
		{"long completed | later required ~", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_completed_successfully}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: ~}
`, "db=service_completed_successfully,true"},
		{"long completed | later required 5", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_completed_successfully}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: 5}
`, "ERR"},
		{"long completed | later condition only", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_completed_successfully}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {condition: service_started}
`, "db=service_started,true"},
		{"long completed | later restart true", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_completed_successfully}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {restart: true}
`, "db=service_completed_successfully,true"},
		{"long completed | later required false + restart", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_completed_successfully}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: false, restart: true}
`, "db=service_completed_successfully,false"},
		{"long completed | later empty", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_completed_successfully}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {}
`, "db=service_completed_successfully,true"},
		{"long completed | later condition + required false", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_completed_successfully}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {condition: service_healthy, required: false}
`, "db=service_healthy,false"},
		{"long completed | later condition + required true", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_completed_successfully}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {condition: service_started, required: true}
`, "db=service_started,true"},
		{"long healthy+required true | later required false", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy, required: true}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: false}
`, "db=service_healthy,false"},
		{"long healthy+required true | later required true", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy, required: true}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: true}
`, "db=service_healthy,true"},
		{"long healthy+required true | later required \"false\"", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy, required: true}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: "false"}
`, "db=service_healthy,false"},
		{"long healthy+required true | later required ~", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy, required: true}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: ~}
`, "db=service_healthy,true"},
		{"long healthy+required true | later required 5", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy, required: true}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: 5}
`, "ERR"},
		{"long healthy+required true | later condition only", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy, required: true}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {condition: service_started}
`, "db=service_started,true"},
		{"long healthy+required true | later restart true", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy, required: true}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {restart: true}
`, "db=service_healthy,true"},
		{"long healthy+required true | later required false + restart", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy, required: true}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {required: false, restart: true}
`, "db=service_healthy,false"},
		{"long healthy+required true | later empty", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy, required: true}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {}
`, "db=service_healthy,true"},
		{"long healthy+required true | later condition + required false", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy, required: true}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {condition: service_healthy, required: false}
`, "db=service_healthy,false"},
		{"long healthy+required true | later condition + required true", `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy, required: true}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, `services:
  web:
    depends_on:
      db: {condition: service_started, required: true}
`, "db=service_started,true"},
		{"list | later new dep required false", `services:
  web:
    image: wi
    depends_on: [db]
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
  other:
    image: oi
`, `services:
  web:
    depends_on:
      other: {required: false}
`, "ERR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, text := range map[string]string{"a.yaml": tc.first, "b.yaml": tc.second} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			p, err := LoadFiles([]string{filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")}, nil)
			if tc.want == "ERR" {
				if err == nil {
					t.Errorf("docker compose refuses it, and it was read")
				}
				return
			}
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			var got []string
			for _, d := range p.Services["web"].DependsOn {
				got = append(got, fmt.Sprintf("%s=%s,%t", d.Name, d.Condition, !d.Optional))
			}
			sort.Strings(got)
			if joined := strings.Join(got, ";"); joined != tc.want {
				t.Errorf("dependencies = %q, want %q", joined, tc.want)
			}
		})
	}
}

// …and the same where the earlier source is not a second `-f` file beside it: a third file between, an `include`, an alias or a merge key (every row measured, v5.5.1).
func TestALaterRequiredAloneKeepsTheEarlierConditionAcrossFilesAndIncludes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		order []string
		want  string
	}{
		{"three files: list, then an environment, then required false", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on: [db]
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {required: false}
`, "m.yaml": `services:
  web:
    environment: {A: a}
`}, []string{"a.yaml", "m.yaml", "b.yaml"}, "db=service_started,false"},
		{"three files: list, then healthy, then required false", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on: [db]
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {required: false}
`, "m.yaml": `services:
  web:
    depends_on:
      db: {condition: service_healthy}
`}, []string{"a.yaml", "m.yaml", "b.yaml"}, "db=service_healthy,false"},
		{"three files: list, then required true, then required false", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on: [db]
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {required: false}
`, "m.yaml": `services:
  web:
    depends_on:
      db: {condition: service_started, required: true}
`}, []string{"a.yaml", "m.yaml", "b.yaml"}, "db=service_started,false"},
		{"an include gives the list, a later file writes required false", map[string]string{"a.yaml": `include: [inc.yaml]
`, "b.yaml": `services:
  web:
    depends_on:
      db: {required: false}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on: [db]
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml", "b.yaml"}, "db=service_started,false"},
		{"an included long form, a later file writes required false", map[string]string{"a.yaml": `include: [inc.yaml]
`, "b.yaml": `services:
  web:
    depends_on:
      db: {required: false}
`, "inc.yaml": `services:
  web:
    image: wi
    depends_on: {db: {condition: service_healthy}}
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`}, []string{"a.yaml", "b.yaml"}, "db=service_healthy,false"},
		{"the list is an alias, a later file writes required false", map[string]string{"a.yaml": `x-d: &d [db]
services:
  web:
    image: wi
    depends_on: *d
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `services:
  web:
    depends_on:
      db: {required: false}
`}, []string{"a.yaml", "b.yaml"}, "db=service_started,false"},
		{"the later entry is an alias to {required: false}", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on: [db]
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `x-r: &r {required: false}
services:
  web:
    depends_on:
      db: *r
`}, []string{"a.yaml", "b.yaml"}, "db=service_started,false"},
		{"the later entry is a merge key of {required: false}", map[string]string{"a.yaml": `services:
  web:
    image: wi
    depends_on: [db]
  db:
    image: di
    healthcheck: {test: [CMD, "true"]}
`, "b.yaml": `x-r: &r {required: false}
services:
  web:
    depends_on:
      db: {<<: *r}
`}, []string{"a.yaml", "b.yaml"}, "db=service_started,false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, text := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			paths := make([]string, len(tc.order))
			for i, f := range tc.order {
				paths[i] = filepath.Join(dir, f)
			}
			p, err := LoadFiles(paths, nil)
			if tc.want == "ERR" {
				if err == nil {
					t.Errorf("docker compose refuses it, and it was read")
				}
				return
			}
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			var got []string
			for _, d := range p.Services["web"].DependsOn {
				got = append(got, fmt.Sprintf("%s=%s,%t", d.Name, d.Condition, !d.Optional))
			}
			sort.Strings(got)
			if joined := strings.Join(got, ";"); joined != tc.want {
				t.Errorf("dependencies = %q, want %q", joined, tc.want)
			}
		})
	}
}
