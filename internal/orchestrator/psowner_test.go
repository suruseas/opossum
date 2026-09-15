package orchestrator_test

import (
	"bytes"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// stderrOf runs f with os.Stderr captured, and returns what it wrote there.
func stderrOf(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = saved }()
	f()
	w.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}

// `ps` lists this project's containers: a container of a service's name that
// is another project's, or carries no project label (made outside opossum),
// gets no row — docker compose v5.5.0 lists none for a container without its
// labels — and is named on stderr, so the reader sees why `exec` and the rest
// refuse it. The other services are listed as before.
func TestPsListsOnlyThisProjectsContainers(t *testing.T) {
	for _, tc := range []struct {
		name, domain string
		env          []string
		hidden       string // the services without a row, space-separated
		said         string
		left         string // "" when the exit is zero; else what the error says
	}{
		{"no project label", "opossum", []string{"INSPECT_UNLABELED=web.demo.opossum"}, "web", "web: container web.demo.opossum carries no opossum.project label, so it was not made by this project — not listed", ""},
		{"another project's", "opossum", []string{"INSPECT_OWNER=web.demo.opossum=otherproj"}, "web", `web: container web.demo.opossum belongs to project "otherproj" — not listed`, ""},
		{"bare names: no project label", "", []string{"INSPECT_PROJECT=demo", "INSPECT_UNLABELED=db"}, "db", "db: container db carries no opossum.project label", ""},
		{"both services not this project's, each said", "opossum", []string{"INSPECT_UNLABELED=db.demo.opossum", "INSPECT_OWNER=web.demo.opossum=otherproj"}, "db web", `web: container web.demo.opossum belongs to project "otherproj" — not listed`, ""},
		// A container the runtime gives no readable answer about has no row and
		// is named, as `up` and `down` name it.
		{"no readable answer about one", "opossum", []string{"INSPECT_FAIL=web.demo.opossum"}, "web", "web: container web.demo.opossum could not be asked: the runtime gave no readable answer about which project owns it — not listed",
			"the runtime gave no readable answer about which project owns 1 container(s), so they were left: web.demo.opossum"},
		{"this project's own", "opossum", nil, "", "", ""},
	} {
		for _, format := range []string{"", "json"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				rt, _ := fakeShim(t)
				setShimEnv(rt, tc.env...)
				p := project("demo", map[string]*compose.Service{
					"db":  {Image: "postgres:16"},
					"web": {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "db"}}},
				})
				var out bytes.Buffer
				var err error
				stderr := stderrOf(t, func() {
					err = orchestrator.New(p, rt, tc.domain, &out).Ps(orchestrator.PsOptions{Format: format})
				})
				// A container the runtime gave no readable answer about makes
				// the exit non-zero once the table is out, as `down` exits over
				// the ones it left: an empty table with a zero exit would read
				// as "nothing is running".
				if tc.left == "" && err != nil {
					t.Fatalf("Ps: %v", err)
				}
				if tc.left != "" && (err == nil || !strings.Contains(err.Error(), tc.left) || !strings.Contains(err.Error(), "run `opossum ps` again")) {
					t.Fatalf("want %q and `opossum ps` to run again, got %v", tc.left, err)
				}
				hidden := strings.Fields(tc.hidden)
				for _, svc := range []string{"db", "web"} {
					listed := strings.Contains(out.String(), `"Service":"`+svc+`"`) || strings.HasPrefix(out.String(), svc+" ") || strings.Contains(out.String(), "\n"+svc+" ")
					if want := !slices.Contains(hidden, svc); listed != want {
						t.Errorf("%s listed = %v, want %v; got:\n%s", svc, listed, want, out.String())
					}
				}
				if format == "json" && len(hidden) == 2 && strings.TrimSpace(out.String()) != "[]" {
					t.Errorf("with every service left out the JSON is an empty array, got %q", out.String())
				}
				if tc.said == "" {
					if strings.Contains(stderr, "not listed") {
						t.Errorf("nothing to say here, got stderr:\n%s", stderr)
					}
					return
				}
				// One line per container left out, each named.
				if !strings.Contains(stderr, tc.said) || strings.Count(stderr, "not listed") != len(hidden) {
					t.Errorf("want %q and one line per container left out (%d) on stderr, got:\n%s", tc.said, len(hidden), stderr)
				}
				for _, svc := range hidden {
					if !strings.Contains(stderr, svc+": container ") {
						t.Errorf("want %s named on stderr, got:\n%s", svc, stderr)
					}
				}
				if strings.Contains(out.String(), "not listed") {
					t.Errorf("the note belongs on stderr, not in the table, got:\n%s", out.String())
				}
			})
		}
	}
}

// `port` reads a port of this project's container: one that is another
// project's or carries no project label is reported as no container of this
// project's, whatever it publishes.
func TestPortRefusesAContainerThatIsNotThisProjects(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
		want string
	}{
		{"no project label", []string{"INSPECT_UNLABELED=web.demo.opossum"}, `service "web" has no container of this project's: web.demo.opossum carries no opossum.project label`},
		{"another project's", []string{"INSPECT_OWNER=web.demo.opossum=otherproj"}, `service "web" has no container of this project's: web.demo.opossum belongs to project "otherproj"`},
		// The owner is read before the state: a stopped container of someone
		// else's is still theirs.
		{"no project label, stopped", []string{"INSPECT_UNLABELED=web.demo.opossum", "INSPECT_STOPPED=web.demo.opossum"}, `service "web" has no container of this project's: web.demo.opossum carries no opossum.project label`},
		// No readable answer is not "not running": the container may be there.
		{"no readable answer", []string{"INSPECT_FAIL=web.demo.opossum"}, `service "web" has no container of this project's: web.demo.opossum could not be asked: the runtime gave no readable answer about which project owns it`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			setShimEnv(rt, tc.env...)
			var out bytes.Buffer
			err := orchestrator.New(project("demo", map[string]*compose.Service{"web": {Image: "web:latest", Ports: []string{"8080:80"}}}), rt, "opossum", &out).Port("web", 80, "tcp")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want %q, got %v", tc.want, err)
			}
			if out.Len() != 0 {
				t.Errorf("no mapping may be printed, got %q", out.String())
			}
		})
	}
}
