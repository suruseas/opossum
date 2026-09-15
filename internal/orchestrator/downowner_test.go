package orchestrator_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/workspace"
)

// `down` finds its containers by name, and a container of another project can
// carry the same one (with `--dns-domain ""`, names are bare service names).
// Before this, `down` stopped and deleted such a container without looking
// (reproduced on container 1.4.1, from a project never brought up). Every row
// names which containers `down` must leave, for a service container and a `run`
// leftover, first and last in the teardown order; the rest of the stack still
// comes down.
func TestDownLeavesContainersThatAreNotThisProjects(t *testing.T) {
	for _, tc := range []struct {
		name    string
		domain  string // "" names containers by the bare service name
		env     []string
		left    []string // neither stopped nor deleted
		said    []string // lines `down` must print
		notSaid []string // text `down` must not print
		wantErr string   // "" for success
	}{
		{
			// The real-runtime shape: the service container and its run leftover
			// both belong to the other project, for every service of the stack.
			name:   "bare names: every container is another project's",
			domain: "",
			env:    []string{"INSPECT_PROJECT=otherproj"},
			left:   []string{"*"},
		},
		{
			name:   "every container is another project's",
			domain: "opossum",
			env:    []string{"INSPECT_PROJECT=otherproj"},
			left:   []string{"*"},
		},
		{
			// Another project's container and one with no answer in the same down,
			// in the middle of the teardown: each is said the way it is.
			name:    "another project's container and one with no answer, mixed",
			domain:  "opossum",
			env:     []string{"INSPECT_PROJECT=demo", "INSPECT_OWNER=cache.demo.opossum=otherproj", "INSPECT_FAIL=web-run.demo.opossum"},
			left:    []string{"cache.demo.opossum", "web-run.demo.opossum"},
			said:    []string{`Leaving container cache.demo.opossum alone: it belongs to project "otherproj"`},
			notSaid: []string{"Leaving container web-run"},
			wantErr: "about which project owns 1 container(s), so they were left: web-run.demo.opossum — ",
		},
		{
			name:   "bare names: another project's container called web",
			domain: "",
			env:    []string{"INSPECT_PROJECT=demo", "INSPECT_OWNER=web=otherproj"},
			left:   []string{"web"},
			said:   []string{`Leaving container web alone: it belongs to project "otherproj"`},
		},
		{name: "this project's own containers all come down", domain: "opossum", env: []string{"INSPECT_PROJECT=demo"}},
		// A container that is not there carries no label either, and is not
		// "left": there is nothing to leave, and nothing is said about it.
		{
			name:    "a service container that is not there is not said to be left",
			domain:  "opossum",
			env:     []string{"INSPECT_ABSENT=web.demo.opossum"},
			notSaid: []string{"Leaving container web.demo.opossum"},
		},
		// A container of the name with no project label was made outside
		// opossum: `down` leaves it and says so, as it leaves another project's
		// (docker compose v5.5.0 leaves it too, silently).
		{
			name:   "a service container with no project label, first in the teardown",
			domain: "opossum",
			env:    []string{"INSPECT_UNLABELED=web.demo.opossum"},
			left:   []string{"web.demo.opossum"},
			said:   []string{"Leaving container web.demo.opossum alone: it carries no opossum.project label, so it was not made by this project (remove it with `container delete --force web.demo.opossum` for this project to use the name)"},
		},
		{
			name:   "a service container with no project label, last in the teardown",
			domain: "opossum",
			env:    []string{"INSPECT_UNLABELED=db.demo.opossum"},
			left:   []string{"db.demo.opossum"},
			said:   []string{"Leaving container db.demo.opossum alone: it carries no opossum.project label"},
		},
		{
			name:   "a run leftover with no project label",
			domain: "opossum",
			env:    []string{"INSPECT_UNLABELED=cache-run.demo.opossum"},
			left:   []string{"cache-run.demo.opossum"},
			said:   []string{"Leaving container cache-run.demo.opossum alone: it carries no opossum.project label"},
		},
		{
			name:   "bare names: a container called web with no project label",
			domain: "",
			env:    []string{"INSPECT_PROJECT=demo", "INSPECT_UNLABELED=web"},
			left:   []string{"web"},
			said:   []string{"Leaving container web alone: it carries no opossum.project label"},
		},
		{
			// Each is said the way it is: another project's, and no label.
			name:    "another project's container and one with no project label, mixed",
			domain:  "opossum",
			env:     []string{"INSPECT_OWNER=cache.demo.opossum=otherproj", "INSPECT_UNLABELED=db.demo.opossum"},
			left:    []string{"cache.demo.opossum", "db.demo.opossum"},
			said:    []string{`Leaving container cache.demo.opossum alone: it belongs to project "otherproj"`, "Leaving container db.demo.opossum alone: it carries no opossum.project label"},
			notSaid: []string{"Leaving container cache.demo.opossum alone: it carries no", `Leaving container db.demo.opossum alone: it belongs`},
		},
		{
			name:   "another project's service container, first in the teardown",
			domain: "opossum",
			env:    []string{"INSPECT_PROJECT=demo", "INSPECT_OWNER=web.demo.opossum=otherproj"},
			left:   []string{"web.demo.opossum"},
			said:   []string{`Leaving container web.demo.opossum alone: it belongs to project "otherproj" (give this project its own DNS domain so names don't collide)`},
		},
		{
			name:   "another project's service container, last in the teardown",
			domain: "opossum",
			env:    []string{"INSPECT_PROJECT=demo", "INSPECT_OWNER=db.demo.opossum=otherproj"},
			left:   []string{"db.demo.opossum"},
			said:   []string{`Leaving container db.demo.opossum alone: it belongs to project "otherproj"`},
		},
		{
			name:   "another project's run leftover",
			domain: "opossum",
			env:    []string{"INSPECT_PROJECT=demo", "INSPECT_OWNER=cache-run.demo.opossum=otherproj"},
			left:   []string{"cache-run.demo.opossum"},
			said:   []string{`Leaving container cache-run.demo.opossum alone: it belongs to project "otherproj"`},
		},
		{
			name:    "no readable answer about the first and the last service container",
			domain:  "opossum",
			env:     []string{"INSPECT_PROJECT=demo", "INSPECT_FAIL=web.demo.opossum db.demo.opossum"},
			left:    []string{"web.demo.opossum", "db.demo.opossum"},
			wantErr: "the runtime gave no readable answer about which project owns 2 container(s), so they were left: web.demo.opossum, db.demo.opossum — `container inspect <name>` shows what the runtime says; run `opossum down` again once it answers",
		},
		{
			name:    "no readable answer about a run leftover",
			domain:  "opossum",
			env:     []string{"INSPECT_PROJECT=demo", "INSPECT_FAIL=db-run.demo.opossum"},
			left:    []string{"db-run.demo.opossum"},
			wantErr: "about which project owns 1 container(s), so they were left: db-run.demo.opossum — ",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Teardown order is web, cache, db (the reverse of startup).
			name := func(svc string) string {
				if tc.domain == "" {
					return svc
				}
				return svc + ".demo." + tc.domain
			}
			all := []string{name("web"), name("cache"), name("db")}
			runs := []string{name("web-run"), name("cache-run"), name("db-run")}
			rt, log := fakeShim(t)
			setShimEnv(rt, tc.env...)
			p := project("demo", map[string]*compose.Service{
				"db":    {Image: "postgres:16"},
				"cache": {Image: "redis:7", DependsOn: compose.DependsOn{{Name: "db"}}},
				"web":   {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "cache"}}},
			})
			var out bytes.Buffer
			err := orchestrator.New(p, rt, tc.domain, &out).Down(false, "", false)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Down: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("want the error to contain %q, got: %v", tc.wantErr, err)
			}
			lines := log()
			left := tc.left
			if slices.Equal(left, []string{"*"}) {
				left = append(slices.Clone(all), runs...)
				for _, c := range left {
					tc.said = append(tc.said, "Leaving container "+c+` alone: it belongs to project "otherproj"`)
				}
			}
			for _, s := range tc.notSaid {
				if strings.Contains(out.String(), s) {
					t.Errorf("did not want %q in the output, got:\n%s", s, out.String())
				}
			}
			for _, c := range append(slices.Clone(all), runs...) {
				stopped := slices.Contains(lines, "stop "+c)
				deleted := slices.Contains(lines, "delete --force "+c)
				switch {
				case slices.Contains(left, c):
					if stopped || deleted {
						t.Errorf("%s is not this project's to remove, yet stop=%v delete=%v in %v", c, stopped, deleted, lines)
					}
				case slices.Contains(runs, c):
					if !deleted {
						t.Errorf("the run leftover %s should still be deleted, got %v", c, lines)
					}
				default:
					if !stopped || !deleted {
						t.Errorf("%s should still be stopped and deleted (stop=%v delete=%v), got %v", c, stopped, deleted, lines)
					}
				}
			}
			// Leaving a container is not stopping the teardown: the network goes too.
			if !slices.Contains(lines, "network delete demo-net") {
				t.Errorf("the project network should still be deleted, got %v", lines)
			}
			for _, s := range tc.said {
				if !strings.Contains(out.String(), s) {
					t.Errorf("want %q in the output, got:\n%s", s, out.String())
				}
			}
			for _, c := range left {
				svc := strings.TrimSuffix(strings.SplitN(c, ".", 2)[0], "-run")
				if !strings.HasSuffix(strings.SplitN(c, ".", 2)[0], "-run") && strings.Contains(out.String(), "Stopping "+svc+"\n") {
					t.Errorf("down must not say it is stopping %s, which it leaves, got:\n%s", svc, out.String())
				}
			}
			// One inspect per name, for both answers.
			for _, c := range append(slices.Clone(all), runs...) {
				if n := count(lines, "inspect "+c); n != 1 {
					t.Errorf("want %s's owner read from one inspect, got %d", c, n)
				}
			}
		})
	}
}

// A container left behind does not cut the teardown short: orphans
// (--remove-orphans), volumes (-v) and built images (--rmi local) still go,
// whether db was left for want of an answer (then `down` says so and fails) or
// because another project holds it (then `down` says so and succeeds).
func TestDownFinishesTheTeardownBeforeSayingWhatItLeft(t *testing.T) {
	for _, tc := range []struct {
		name, env, wantErr string
	}{
		{"no answer about db", "INSPECT_FAIL=db.demo.opossum", "so they were left: db.demo.opossum — "},
		{"db is another project's", "INSPECT_OWNER=db.demo.opossum=otherproj", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, "INSPECT_PROJECT=demo", tc.env,
				"LS_CONTAINERS=web.demo.opossum db.demo.opossum old.demo.opossum", "LS_PROJECT=demo")
			p := project("demo", map[string]*compose.Service{
				"db":  {Image: "postgres:16", Volumes: []string{"pgdata:/var/lib/postgresql/data"}},
				"web": {Build: &compose.Build{Context: "."}, DependsOn: compose.DependsOn{{Name: "db"}}},
			})
			p.Volumes = map[string]compose.VolumeDecl{"pgdata": {}}
			err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Down(true, "local", true)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Down: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("want the error to contain %q, got: %v", tc.wantErr, err)
			}
			lines := log()
			for _, want := range []string{"delete --force old.demo.opossum", "volume delete demo_pgdata", "image delete --force demo-web:latest", "network delete demo-net"} {
				if !slices.Contains(lines, want) {
					t.Errorf("the rest of the teardown should still run, missing %q in %v", want, lines)
				}
			}
			if slices.Contains(lines, "stop db.demo.opossum") || slices.Contains(lines, "delete --force db.demo.opossum") {
				t.Errorf("db must be left, got %v", lines)
			}
		})
	}
}

// `run` deletes a stale one-off of the same name before it starts, found by
// name too. A name another project holds, or one the runtime gives no readable
// answer about, is refused before anything starts — dependencies included.
func TestRunRefusesAOneOffNameThatIsNotThisProjects(t *testing.T) {
	type shape struct {
		name  string
		svcs  func() map[string]*compose.Service
		opts  orchestrator.RunOneOffOptions
		audit bool
	}
	withDep := func() map[string]*compose.Service {
		return map[string]*compose.Service{
			"db":  {Image: "postgres:16"},
			"web": {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "db"}}},
		}
	}
	alone := func() map[string]*compose.Service {
		return map[string]*compose.Service{"web": {Image: "web:latest"}}
	}
	shapes := []shape{
		{"with a dependency", withDep, orchestrator.RunOneOffOptions{Rm: true}, false},
		{"with a dependency and --no-deps", withDep, orchestrator.RunOneOffOptions{Rm: true, NoDeps: true}, false},
		{"without dependencies", alone, orchestrator.RunOneOffOptions{Rm: true}, false},
		{"--audit, with a dependency", withDep, orchestrator.RunOneOffOptions{Rm: true}, true},
		{"--audit, with a dependency and --no-deps", withDep, orchestrator.RunOneOffOptions{Rm: true, NoDeps: true}, true},
		{"--audit, without dependencies", alone, orchestrator.RunOneOffOptions{Rm: true}, true},
	}
	for _, tc := range []struct {
		name, want string
		env        []string
	}{
		{"held by another project", `container "web-run.demo.opossum" is already in use by project "otherproj"; give this project its own DNS domain so names don't collide ` +
			"(e.g. --dns-domain demo, created once with `sudo container system dns create demo`) — see README (multi-project)",
			[]string{"INSPECT_OWNER=web-run.demo.opossum=otherproj"}},
		{"no readable answer", `container "web-run.demo.opossum": the runtime gave no readable answer about which project owns it, so it is left alone; ` +
			"`container inspect web-run.demo.opossum` shows what the runtime says — run `opossum run` again once it answers, " +
			"or, if it belongs to another project, give this project its own DNS domain (e.g. --dns-domain demo)",
			[]string{"INSPECT_FAIL=web-run.demo.opossum"}},
		// A one-off of the name made outside opossum carries no project label.
		{"no project label", `container "web-run.demo.opossum" already exists and carries no opossum.project label, so it was not made by this project and is left alone; ` +
			"remove it (`container delete --force web-run.demo.opossum`) to free the name, or give this project its own DNS domain (e.g. --dns-domain demo)",
			[]string{"INSPECT_UNLABELED=web-run.demo.opossum"}},
	} {
		for _, sh := range shapes {
			t.Run(tc.name+", "+sh.name, func(t *testing.T) {
				rt, log := fakeShim(t)
				setShimEnv(rt, tc.env...)
				svcs := sh.svcs()
				// An audited run snapshots the workspace bound at working_dir before
				// it starts anything; the refusal must come before that too, or the
				// snapshot directory is left beside the workspace.
				ws := filepath.Join(t.TempDir(), "work")
				if err := os.MkdirAll(ws, 0o755); err != nil {
					t.Fatal(err)
				}
				if sh.audit {
					svcs["web"].WorkingDir = "/w"
					svcs["web"].Volumes = []string{ws + ":/w"}
				}
				o := orchestrator.New(project("demo", svcs), rt, "opossum", &bytes.Buffer{})
				var err error
				if sh.audit {
					var r *orchestrator.AuditReport
					r, err = o.RunAudited("web", []string{"true"}, sh.opts)
					if r != nil {
						t.Errorf("a refusal is not an audit result, got a report: %+v", r)
					}
				} else {
					err = o.RunOneOff("web", []string{"true"}, sh.opts)
				}
				if err == nil || err.Error() != tc.want {
					t.Fatalf("want exactly the refusal %q, got: %v", tc.want, err)
				}
				if _, statErr := os.Stat(filepath.Join(filepath.Dir(ws), workspace.SnapshotDirName)); statErr == nil {
					t.Errorf("the refusal must come before the workspace snapshot, but %s was made", workspace.SnapshotDirName)
				}
				for _, l := range log() {
					for _, verb := range []string{"delete", "run ", "stop ", "network create", "volume create", "build"} {
						if strings.HasPrefix(l, verb) {
							t.Errorf("nothing may start or be removed before the refusal, got %q", l)
						}
					}
				}
			})
		}
	}
	// The name is the one-off's own: another project holding the service's up
	// container, or one with no project label holding it, is not this
	// refusal's business (up refuses that one).
	for _, env := range []string{"INSPECT_OWNER=web.demo.opossum=otherproj", "INSPECT_UNLABELED=web.demo.opossum"} {
		t.Run(env, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, env)
			p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
			if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{Rm: true, NoDeps: true}); err != nil {
				t.Fatalf("a one-off whose own name is free should run, got: %v", err)
			}
			if !slices.ContainsFunc(log(), func(l string) bool { return strings.HasPrefix(l, "run ") }) {
				t.Errorf("the one-off should have run, got %v", log())
			}
		})
	}
}

func count(lines []string, want string) int {
	n := 0
	for _, l := range lines {
		if l == want {
			n++
		}
	}
	return n
}
