package orchestrator_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/runtime"
)

// `start`, `stop`, `kill` and `restart` act on a service's container by name,
// and a container of another project can carry the same one (with
// `--dns-domain ""`, names are bare service names). They leave such a container
// — and one the runtime gives no readable answer about — the way `down` does,
// and act on the rest of the project's services. Every command runs every row,
// so a check wired into one command and not another shows as a failing row, and
// the rows put the other project's container first and last in each order.
func TestServiceCommandsLeaveContainersThatAreNotThisProjects(t *testing.T) {
	type command struct {
		name string
		verb []string // runtime subcommands it sends a container
		run  func(o *orchestrator.Orchestrator) error
	}
	commands := []command{
		{"start", []string{"start"}, func(o *orchestrator.Orchestrator) error { return o.Start(nil) }},
		{"stop", []string{"stop"}, func(o *orchestrator.Orchestrator) error { return o.Stop(nil) }},
		{"kill", []string{"kill"}, func(o *orchestrator.Orchestrator) error { return o.Kill(nil, "") }},
		{"restart", []string{"stop", "start"}, func(o *orchestrator.Orchestrator) error { return o.Restart(nil) }},
		{"start web", []string{"start"}, func(o *orchestrator.Orchestrator) error { return o.Start([]string{"web"}) }},
		{"stop web", []string{"stop"}, func(o *orchestrator.Orchestrator) error { return o.Stop([]string{"web"}) }},
	}
	for _, tc := range []struct {
		name    string
		domain  string
		env     []string
		left    []string // the containers no runtime command may name
		said    []string // lines the command must print
		wantErr string   // a part of the error; "" for success
	}{
		{name: "this project's containers", domain: "opossum", env: []string{"INSPECT_PROJECT=demo"}},
		{
			// Made outside opossum: no project label (docker compose v5.5.0 leaves it too).
			name:   "a container with no project label, first to start",
			domain: "opossum",
			env:    []string{"INSPECT_UNLABELED=db.demo.opossum"},
			left:   []string{"db.demo.opossum"},
			said:   []string{"Leaving container db.demo.opossum alone: it carries no opossum.project label, so it was not made by this project (remove it with `container delete --force db.demo.opossum` for this project to use the name)"},
		},
		{
			name:   "a container with no project label, last to start",
			domain: "opossum",
			env:    []string{"INSPECT_UNLABELED=web.demo.opossum"},
			left:   []string{"web.demo.opossum"},
			said:   []string{"Leaving container web.demo.opossum alone: it carries no opossum.project label"},
		},
		{
			name:   "another project's container, first to start",
			domain: "opossum",
			env:    []string{"INSPECT_PROJECT=demo", "INSPECT_OWNER=db.demo.opossum=otherproj"},
			left:   []string{"db.demo.opossum"},
			said:   []string{`Leaving container db.demo.opossum alone: it belongs to project "otherproj"`},
		},
		{
			name:   "another project's container, last to start",
			domain: "opossum",
			env:    []string{"INSPECT_PROJECT=demo", "INSPECT_OWNER=web.demo.opossum=otherproj"},
			left:   []string{"web.demo.opossum"},
			said:   []string{`Leaving container web.demo.opossum alone: it belongs to project "otherproj"`},
		},
		{
			name:   "bare names: every container is another project's",
			domain: "",
			env:    []string{"INSPECT_PROJECT=otherproj"},
			left:   []string{"web", "cache", "db"},
			said:   []string{`Leaving container web alone: it belongs to project "otherproj"`},
		},
		{
			name:    "no readable answer about the last to start",
			domain:  "opossum",
			env:     []string{"INSPECT_PROJECT=demo", "INSPECT_FAIL=web.demo.opossum"},
			left:    []string{"web.demo.opossum"},
			wantErr: "the runtime gave no readable answer about which project owns 1 container(s), so they were left: web.demo.opossum — ",
		},
		{
			name:    "no readable answer about the first to start",
			domain:  "opossum",
			env:     []string{"INSPECT_PROJECT=demo", "INSPECT_FAIL=db.demo.opossum"},
			left:    []string{"db.demo.opossum"},
			wantErr: "the runtime gave no readable answer about which project owns 1 container(s), so they were left: db.demo.opossum — ",
		},
	} {
		for _, c := range commands {
			t.Run(c.name+"/"+tc.name, func(t *testing.T) {
				name := func(svc string) string {
					if tc.domain == "" {
						return svc
					}
					return svc + ".demo." + tc.domain
				}
				targets := []string{name("web"), name("cache"), name("db")}
				if strings.HasSuffix(c.name, " web") {
					targets = []string{name("web")}
				}
				rt, log := fakeShim(t)
				setShimEnv(rt, tc.env...)
				p := project("demo", map[string]*compose.Service{
					"db":    {Image: "postgres:16"},
					"cache": {Image: "redis:7", DependsOn: compose.DependsOn{{Name: "db"}}},
					"web":   {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "cache"}}},
				})
				var out bytes.Buffer
				err := c.run(orchestrator.New(p, rt, tc.domain, &out))
				wantErr := tc.wantErr
				left := slices.DeleteFunc(slices.Clone(tc.left), func(n string) bool { return !slices.Contains(targets, n) })
				if len(left) == 0 {
					wantErr = ""
				}
				switch {
				case wantErr == "" && err != nil:
					t.Fatalf("%s: %v", c.name, err)
				case wantErr != "" && (err == nil || !strings.Contains(err.Error(), wantErr)):
					t.Fatalf("want the error to contain %q, got: %v", wantErr, err)
				}
				// The advice names the command to run again: this one, not `down`'s.
				if again := "run `opossum " + strings.Fields(c.name)[0] + "` again once it answers"; wantErr != "" && !strings.Contains(err.Error(), again) {
					t.Errorf("want the error to say %q, got: %v", again, err)
				}
				lines := log()
				for _, cname := range targets {
					for _, verb := range c.verb {
						sent := slices.ContainsFunc(lines, func(l string) bool {
							return strings.HasPrefix(l, verb+" ") && strings.HasSuffix(l, " "+cname)
						})
						if slices.Contains(left, cname) && sent {
							t.Errorf("%s is not this project's, yet %q was sent to it: %v", cname, verb, lines)
						}
						if !slices.Contains(left, cname) && !sent {
							t.Errorf("%s is this project's and should get %q: %v", cname, verb, lines)
						}
					}
				}
				// What it leaves is said, for each container this command was asked
				// about; a container outside the targets is not this command's to mention.
				for _, s := range tc.said {
					about := slices.ContainsFunc(left, func(n string) bool { return strings.Contains(s, "container "+n+" alone") })
					if about && !strings.Contains(out.String(), s) {
						t.Errorf("want %q in the output, got:\n%s", s, out.String())
					}
				}
			})
		}
	}
}

// `cp` reads and writes files inside a service's container, on either side of
// the copy. Another project's container, or one the runtime gives no readable
// answer about, is refused on each side; a host path and this project's
// container are copied as before.
func TestCopyRefusesAContainerThatIsNotThisProjects(t *testing.T) {
	copied := map[string]string{
		"into this project's container":                         "cp ./f web.demo.opossum:/tmp/f",
		"out of a container named for this project":             "cp web.demo.opossum:/tmp/f ./f",
		"into this project's container, at a path with a colon": "cp ./f web.demo.opossum:/tmp/a:b",
		"a prefix that is not a service is a host path":         "cp notaservice:/tmp/f ./f",
	}
	rows := []struct {
		name     string
		src, dst string
		env      []string
		wantErr  string
	}{
		{"into this project's container", "./f", "web:/tmp/f", []string{"INSPECT_PROJECT=demo"}, ""},
		{"out of a container named for this project", "web:/tmp/f", "./f", nil, ""},
		{"out of a container with no project label", "web:/tmp/f", "./f", []string{"INSPECT_UNLABELED=web.demo.opossum"}, `container "web.demo.opossum" already exists and carries no opossum.project label, so it was not made by this project and is left alone`},
		{"into a container with no project label", "./f", "web:/tmp/f", []string{"INSPECT_UNLABELED=web.demo.opossum"}, `container "web.demo.opossum" already exists and carries no opossum.project label`},
		{"into another project's container", "./f", "web:/tmp/f", []string{"INSPECT_OWNER=web.demo.opossum=otherproj"}, `container "web.demo.opossum" is already in use by project "otherproj"`},
		{"out of another project's container", "web:/tmp/f", "./f", []string{"INSPECT_OWNER=web.demo.opossum=otherproj"}, `container "web.demo.opossum" is already in use by project "otherproj"`},
		{"out of this project's container into another project's", "db:/tmp/f", "web:/tmp/f", []string{"INSPECT_PROJECT=demo", "INSPECT_OWNER=web.demo.opossum=otherproj"}, `container "web.demo.opossum" is already in use by project "otherproj"`},
		{"out of a container with no readable answer", "web:/tmp/f", "./f", []string{"INSPECT_FAIL=web.demo.opossum"}, "the runtime gave no readable answer about which project owns it, so it is left alone; `container inspect web.demo.opossum` shows what the runtime says — run `opossum cp` again once it answers"},
		// A `:` in the path inside the container: the service is what comes
		// before the first `:`, for the check as for the copy.
		{"into another project's container, at a path with a colon", "./f", "web:/tmp/a:b", []string{"INSPECT_OWNER=web.demo.opossum=otherproj"}, `container "web.demo.opossum" is already in use by project "otherproj"`},
		{"into this project's container, at a path with a colon", "./f", "web:/tmp/a:b", []string{"INSPECT_PROJECT=demo"}, ""},
		{"a prefix that is not a service is a host path", "notaservice:/tmp/f", "./f", []string{"INSPECT_FAIL=notaservice"}, ""},
	}
	for name := range copied {
		if !slices.ContainsFunc(rows, func(r struct {
			name     string
			src, dst string
			env      []string
			wantErr  string
		}) bool {
			return r.name == name
		}) {
			t.Fatalf("%q names no row", name)
		}
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, tc.env...)
			p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}, "db": {Image: "postgres:16"}})
			err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Copy(tc.src, tc.dst)
			sent := slices.ContainsFunc(log(), func(l string) bool { return strings.HasPrefix(l, "cp ") })
			if tc.wantErr == "" {
				if err != nil || !sent {
					t.Fatalf("cp should run (err %v, sent %v): %v", err, sent, log())
				}
				// What was copied where, written out: the container the copy goes
				// to has to be the one that was checked, and the path is kept whole.
				if want, ok := copied[tc.name]; ok && !slices.Contains(log(), want) {
					t.Errorf("want %q in the runtime calls, got %v", want, log())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want the error to contain %q, got: %v", tc.wantErr, err)
			}
			if sent {
				t.Errorf("cp was sent for a container that is not this project's: %v", log())
			}
		})
	}
}

// `restart` asks who owns each container once, before it stops any: a
// container that was this project's when it was stopped is started again even
// if the runtime stops answering about it in between. Asking again before the
// start would leave this project's service stopped.
func TestRestartAsksOnceAndStartsWhatItStopped(t *testing.T) {
	rt, log := fakeShim(t)
	setShimEnv(rt, "INSPECT_PROJECT=demo", "INSPECT_FAIL_ONCE_STOP_ASKED=web.demo.opossum")
	p := project("demo", map[string]*compose.Service{
		"db":  {Image: "postgres:16"},
		"web": {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "db"}}},
	})
	if err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Restart(nil); err != nil {
		t.Fatalf("restart: %v", err)
	}
	for _, want := range []string{"stop web.demo.opossum", "start web.demo.opossum", "stop db.demo.opossum", "start db.demo.opossum"} {
		if !slices.Contains(log(), want) {
			t.Errorf("want %q in the runtime calls, got %v", want, log())
		}
	}
}

// `exec` runs a command inside the container. Inside another project's, or one
// the runtime gives no readable answer about, it is refused, as `up` and `run`
// refuse to reuse such a name; this project's and unlabeled ones run.
func TestExecRefusesAContainerThatIsNotThisProjects(t *testing.T) {
	for _, tc := range []struct {
		name    string
		domain  string
		env     []string
		wantErr string
	}{
		{"this project's container", "opossum", []string{"INSPECT_PROJECT=demo"}, ""},
		{"a container named for this project", "opossum", nil, ""},
		{"a container with no project label", "opossum", []string{"INSPECT_UNLABELED=web.demo.opossum"}, `container "web.demo.opossum" already exists and carries no opossum.project label, so it was not made by this project and is left alone`},
		{"bare names: a container with no project label", "", []string{"INSPECT_PROJECT=demo", "INSPECT_UNLABELED=web"}, `container "web" already exists and carries no opossum.project label`},
		{"another project's container", "opossum", []string{"INSPECT_OWNER=web.demo.opossum=otherproj"}, `container "web.demo.opossum" is already in use by project "otherproj"`},
		{"bare names: another project's container", "", []string{"INSPECT_OWNER=web=otherproj"}, `container "web" is already in use by project "otherproj"`},
		{"no readable answer", "opossum", []string{"INSPECT_FAIL=web.demo.opossum"}, "the runtime gave no readable answer about which project owns it, so it is left alone; `container inspect web.demo.opossum` shows what the runtime says — run `opossum exec` again once it answers"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, tc.env...)
			p := project("demo", map[string]*compose.Service{"web": {Image: "web:latest"}})
			err := orchestrator.New(p, rt, tc.domain, &bytes.Buffer{}).Exec("web", []string{"true"}, runtime.ExecOptions{})
			sent := slices.ContainsFunc(log(), func(l string) bool { return strings.HasPrefix(l, "exec ") })
			if tc.wantErr == "" {
				if err != nil || !sent {
					t.Fatalf("exec should run (err %v, sent %v): %v", err, sent, log())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want the error to contain %q, got: %v", tc.wantErr, err)
			}
			if sent {
				t.Errorf("exec was sent into a container that is not this project's: %v", log())
			}
		})
	}
}
