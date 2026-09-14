package orchestrator_test

// Evals for #1002 (1): a container opossum would create under a name longer than
// the 63 characters container 1.4.1 takes (`is not a valid container ID`) is
// refused before anything is created or started. The service's container is
// named `<service>.<project>.<domain>`, a one-off `<service>-run.<project>.<domain>`,
// and without a DNS domain the service name alone.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
	"github.com/suruseas/opossum/internal/workspace"
)

// containerNameRefusal is the whole refusal for a container name: the advice
// offers only what changes the name — without a DNS domain, the service name.
func containerNameRefusal(name, service, domain string) string {
	fix := `shorten the service name "` + service + `"`
	if domain != "" {
		fix += " or the project name (`-p`, or `name:` in the compose file), or use a shorter DNS domain than \"" + domain + "\" (`--dns-domain`, created once with `sudo container system dns create <domain>`)"
	}
	return `container name "` + name + `" is ` + itoa(len(name)) + " characters, and the container runtime (1.4.1) creates at most 63 — " + fix
}

func TestAContainerNameLongerThanTheRuntimeTakesIsRefusedBeforeCreating(t *testing.T) {
	p := func(n int) string { return strings.Repeat("p", n) }
	// Every row's services have a short dependency, so a one-off that refused
	// after starting its dependencies would show a `run` of db first.
	withDB := func(project, service string) func() *compose.Project {
		return func() *compose.Project {
			return project1002(project, map[string]*compose.Service{
				"db":    {Image: "alpine:3.20"},
				service: {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "db"}}},
			})
		}
	}
	refused := func(name, service, domain string, paths ...string) map[string]string {
		m := map[string]string{}
		for _, path := range paths {
			n := name
			if path != "up" {
				n = strings.Replace(name, service, service+"-run", 1)
			}
			m[path] = containerNameRefusal(n, service, domain)
		}
		return m
	}
	oneOffs := []string{"run", "run --no-deps", "run --audit", "run --audit --no-deps"}
	for _, tc := range []struct {
		name    string
		service string
		project func() *compose.Project
		domain  string
		refusal map[string]string // by path; missing goes ahead
	}{
		// web.<51>.opossum is 63; web-run.<51>.opossum is 67.
		{"the service at 63, its one-off at 67", "web", withDB(p(51), "web"), "opossum", refused("web."+p(51)+".opossum", "web", "opossum", oneOffs...)},
		// web.<52>.opossum is 64.
		{"the service at 64", "web", withDB(p(52), "web"), "opossum", refused("web."+p(52)+".opossum", "web", "opossum", append([]string{"up"}, oneOffs...)...)},
		// web-run.<47>.opossum is 63.
		{"the one-off at 63", "web", withDB(p(47), "web"), "opossum", nil},
		// web-run.<48>.opossum is 64.
		{"the one-off at 64", "web", withDB(p(48), "web"), "opossum", refused("web."+p(48)+".opossum", "web", "opossum", oneOffs...)},
		// Without a DNS domain the container is named by the service alone, and
		// the one-off by the service and `-run`.
		{"no DNS domain, a service name of 64", p(64), withDB("demo", p(64)), "", refused(p(64), p(64), "", append([]string{"up"}, oneOffs...)...)},
		{"no DNS domain, a service name of 63", p(63), withDB("demo", p(63)), "", refused(p(63), p(63), "", oneOffs...)},
		{"no DNS domain, a service name of 59", p(59), withDB("demo", p(59)), "", nil},
	} {
		for _, path := range append([]string{"up"}, oneOffs...) {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				rt, log := fakeShim(t)
				o := orchestrator.New(tc.project(), rt, tc.domain, &bytes.Buffer{})
				opts := orchestrator.RunOneOffOptions{NoDeps: strings.HasSuffix(path, "--no-deps")}
				var err error
				switch {
				case path == "up":
					err = o.Up(true)
				case strings.HasPrefix(path, "run --audit"):
					_, err = o.RunAudited(tc.service, []string{"true"}, opts)
				default:
					err = o.RunOneOff(tc.service, []string{"true"}, opts)
				}
				want, refused := tc.refusal[path]
				if !refused {
					if err != nil || runLine(log()) < 0 {
						t.Errorf("want it run, got err %v and %v", err, log())
					}
					return
				}
				if err == nil || err.Error() != want {
					t.Errorf("\n got %v\nwant %s", err, want)
				}
				if indexOf(log(), "network create") >= 0 || runLine(log()) >= 0 {
					t.Errorf("want nothing created or started before the refusal, got %v", log())
				}
			})
		}
	}
	// Two services in startup order, the long one first or last: `up` looks at each.
	for _, tc := range []struct{ name, long, other string }{
		{"the second service to start at 64", "webx", "ab"},
		{"the first service to start at 64, one after it", "webx", "zz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			proj := project1002(p(51), map[string]*compose.Service{tc.long: {Image: "alpine:3.20"}, tc.other: {Image: "alpine:3.20"}})
			err := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).Up(true)
			want := containerNameRefusal(tc.long+"."+p(51)+".opossum", tc.long, "opossum")
			if err == nil || err.Error() != want {
				t.Errorf("\n got %v\nwant %s", err, want)
			}
			if indexOf(log(), "network create") >= 0 || runLine(log()) >= 0 {
				t.Errorf("want nothing created or started before the refusal, got %v", log())
			}
		})
	}
}

// The advice names the service whose container name is too long, the second
// one to start included, and without a DNS domain offers only the service name:
// the project name is not part of the container's name then.
func TestTheContainerNameRefusalNamesTheService(t *testing.T) {
	rt, _ := fakeShim(t)
	p := strings.Repeat("p", 51)
	proj := project1002(p, map[string]*compose.Service{"ab": {Image: "alpine:3.20"}, "webx": {Image: "alpine:3.20"}})
	err := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).Up(true)
	want := `container name "webx.` + p + `.opossum" is 64 characters, and the container runtime (1.4.1) creates at most 63 — shorten the service name "webx" or the project name (` + "`-p`, or `name:` in the compose file), or use a shorter DNS domain than \"opossum\" (`--dns-domain`, created once with `sudo container system dns create <domain>`)"
	if err == nil || err.Error() != want {
		t.Errorf("\n got %v\nwant %s", err, want)
	}
	rt, _ = fakeShim(t)
	proj = project1002("demo", map[string]*compose.Service{strings.Repeat("s", 64): {Image: "alpine:3.20"}})
	err = orchestrator.New(proj, rt, "", &bytes.Buffer{}).Up(true)
	want = `container name "` + strings.Repeat("s", 64) + `" is 64 characters, and the container runtime (1.4.1) creates at most 63 — shorten the service name "` + strings.Repeat("s", 64) + `"`
	if err == nil || err.Error() != want {
		t.Errorf("without a DNS domain\n got %v\nwant %s", err, want)
	}
}

// A dependency's container name too long is refused by the `up` that starts the
// dependencies, before it creates or starts anything; a service that is not
// started is not counted; with --no-deps the dependency is not the run's to
// create.
func TestAContainerNameOfAServiceNotCreatedIsNotCounted(t *testing.T) {
	long := strings.Repeat("d", 52) // <52>.demo.opossum is 65
	newProject := func() *compose.Project {
		return project1002("demo", map[string]*compose.Service{
			"api": {Image: "alpine:3.20"},
			long:  {Image: "alpine:3.20"},
			"web": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: long}}},
		})
	}
	refusal := `starting dependencies: container name "` + long + `.demo.opossum" is 65 characters`
	chain := func() *compose.Project {
		return project1002("demo", map[string]*compose.Service{
			long:  {Image: "alpine:3.20"},
			"mid": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: long}}},
			"web": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "mid"}}},
		})
	}
	gated := func() *compose.Project {
		return project1002("demo", map[string]*compose.Service{
			"api": {Image: "alpine:3.20"},
			long:  {Image: "alpine:3.20", Profiles: []string{"debug"}},
		})
	}
	for _, tc := range []struct {
		name    string
		project func() *compose.Project
		call    func(o *orchestrator.Orchestrator) error
		want    string // "" goes ahead
	}{
		{"run web whose dependency's dependency is long", chain, func(o *orchestrator.Orchestrator) error {
			return o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{})
		}, refusal},
		{"up with the long service behind an inactive profile", gated, func(o *orchestrator.Orchestrator) error { return o.Up(true) }, ""},
		{"up api", newProject, func(o *orchestrator.Orchestrator) error { return o.Up(true, "api") }, ""},
		{"run api", newProject, func(o *orchestrator.Orchestrator) error {
			return o.RunOneOff("api", []string{"true"}, orchestrator.RunOneOffOptions{})
		}, ""},
		{"run web", newProject, func(o *orchestrator.Orchestrator) error {
			return o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{})
		}, refusal},
		{"run --audit web", newProject, func(o *orchestrator.Orchestrator) error {
			_, err := o.RunAudited("web", []string{"true"}, orchestrator.RunOneOffOptions{})
			return err
		}, refusal},
		{"run --no-deps web", newProject, func(o *orchestrator.Orchestrator) error {
			return o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: true})
		}, ""},
		{"run --audit --no-deps web", newProject, func(o *orchestrator.Orchestrator) error {
			_, err := o.RunAudited("web", []string{"true"}, orchestrator.RunOneOffOptions{NoDeps: true})
			return err
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			err := tc.call(orchestrator.New(tc.project(), rt, "opossum", &bytes.Buffer{}))
			if tc.want == "" {
				if err != nil || runLine(log()) < 0 {
					t.Errorf("want it run, got err %v and %v", err, log())
				}
				return
			}
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Errorf("\n got %v\nwant it to start %q", err, tc.want)
			}
			if indexOf(log(), "network create") >= 0 || runLine(log()) >= 0 {
				t.Errorf("want nothing created or started before the refusal, got %v", log())
			}
		})
	}
}

func project1002(name string, svcs map[string]*compose.Service) *compose.Project {
	return project(name, svcs)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// An audited run refuses its one-off's long container name before it snapshots
// the workspace (whose directory would outlive the refusal); one that goes ahead
// does snapshot it. Snapshots are kept beside the workspace.
func TestAnAuditedRunRefusesItsLongContainerNameBeforeTheSnapshot(t *testing.T) {
	for _, project := range []string{strings.Repeat("p", 48), strings.Repeat("p", 47)} { // web-run.<n>.opossum: 64, 63
		t.Run(itoa(len(project)), func(t *testing.T) {
			base := t.TempDir()
			if err := os.Mkdir(filepath.Join(base, "work"), 0o755); err != nil {
				t.Fatal(err)
			}
			proj := project1002(project, map[string]*compose.Service{
				"db":  {Image: "alpine:3.20"},
				"web": {Image: "alpine:3.20", WorkingDir: "/work", Volumes: compose.Volumes{"./work:/work"}, DependsOn: compose.DependsOn{{Name: "db"}}},
			})
			proj.BaseDir = base
			rt, log := fakeShim(t)
			_, err := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).RunAudited("web", []string{"true"}, orchestrator.RunOneOffOptions{})
			_, statErr := os.Stat(filepath.Join(base, workspace.SnapshotDirName))
			snapshotted := statErr == nil
			if len(project) == 47 {
				if err != nil || !snapshotted {
					t.Errorf("want the run to go ahead and snapshot the workspace, got err %v, snapshotted %v", err, snapshotted)
				}
				return
			}
			want := containerNameRefusal("web-run."+project+".opossum", "web", "opossum")
			if err == nil || err.Error() != want {
				t.Errorf("\n got %v\nwant %s", err, want)
			}
			if snapshotted || runLine(log()) >= 0 {
				t.Errorf("want no snapshot and nothing started before the refusal, got snapshotted %v, %v", snapshotted, log())
			}
		})
	}
}

// With --remove-orphans, the refusal comes before an orphan is removed: the
// check asks nothing of the runtime.
func TestALongContainerNameIsRefusedBeforeOrphansAreRemoved(t *testing.T) {
	rt, log := fakeShim(t)
	project := strings.Repeat("p", 52) // web.<52>.opossum is 64
	setShimEnv(rt, "LS_CONTAINERS=web."+project+".opossum old."+project+".opossum", "LS_PROJECT="+project)
	o := orchestrator.New(project1002(project, map[string]*compose.Service{"web": {Image: "alpine:3.20"}}), rt, "opossum", &bytes.Buffer{})
	o.SetUpOptions(false, false, false, true, false) // --remove-orphans
	if err := o.Up(true); err == nil || !strings.HasPrefix(err.Error(), `container name "web.`+project+`.opossum" is 64 characters`) {
		t.Fatalf("want the long name refused, got %v", err)
	}
	for _, l := range log() {
		if strings.HasPrefix(l, "stop ") || strings.HasPrefix(l, "delete ") {
			t.Errorf("want no orphan touched before the refusal, got %v", log())
			break
		}
	}
}
