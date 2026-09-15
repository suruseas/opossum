package orchestrator_test

// Evals for #1002 (3) and #1015: a service name puts its characters into the
// container's name (`<service>.<project>.<domain>`, or `<service>` without a DNS
// domain). A name container 1.4.1 does not create (`is not a valid container
// ID`), and two services `up` starts whose names differ only in case, are
// refused before anything is created; a name it creates but a peer may not reach
// (upper case, or `.`) is warned about, not refused and not rewritten.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

func TestAServiceNameTheRuntimeCannotNameAContainerIsRefusedBeforeCreating(t *testing.T) {
	rule := "it has to start with an ASCII letter or digit and hold only ASCII letters, digits, `_`, `.` and `-`"
	renameService := func(name, service string) string {
		return `container name "` + name + `" is not one the container runtime (1.4.1) creates — ` + rule + `, 2 to 63 characters; rename the service "` + service + `"`
	}
	for _, tc := range []struct {
		name, service, domain string
		refusal               map[string]string // by path; missing goes ahead
	}{
		{"a leading underscore", "_web", "opossum", map[string]string{
			"up": renameService("_web.demo.opossum", "_web"), "run": renameService("_web-run.demo.opossum", "_web"), "run --audit": renameService("_web-run.demo.opossum", "_web")}},
		{"a leading dot", ".web", "opossum", map[string]string{
			"up": renameService(".web.demo.opossum", ".web"), "run": renameService(".web-run.demo.opossum", ".web"), "run --audit": renameService(".web-run.demo.opossum", ".web")}},
		{"a leading dash", "-web", "opossum", map[string]string{
			"up": renameService("-web.demo.opossum", "-web"), "run": renameService("-web-run.demo.opossum", "-web"), "run --audit": renameService("-web-run.demo.opossum", "-web")}},
		{"a plus in the middle", "a+b", "opossum", map[string]string{
			"up": renameService("a+b.demo.opossum", "a+b"), "run": renameService("a+b-run.demo.opossum", "a+b"), "run --audit": renameService("a+b-run.demo.opossum", "a+b")}},
		{"a space in the middle", "a b", "opossum", map[string]string{
			"up": renameService("a b.demo.opossum", "a b"), "run": renameService("a b-run.demo.opossum", "a b"), "run --audit": renameService("a b-run.demo.opossum", "a b")}},
		{"a letter outside ASCII first", "é1", "opossum", map[string]string{
			"up": renameService("é1.demo.opossum", "é1"), "run": renameService("é1-run.demo.opossum", "é1"), "run --audit": renameService("é1-run.demo.opossum", "é1")}},
		{"one character without a DNS domain", "a", "", map[string]string{"up": renameService("a", "a")}},
		{"two characters without a DNS domain", "ab", "", nil},
		{"a plain name", "web", "opossum", nil},
		{"a name starting with a digit", "9db", "opossum", nil},
		{"upper case and dots (created, warned about)", "My.Db", "opossum", nil},
		// The service is fine and the DNS domain is what the runtime refuses.
		{"a DNS domain the runtime refuses", "web", "o+x", map[string]string{
			// `up` starts the dependency first, so its name is the one refused.
			"up":          `container name "db.demo.o+x" is not one the container runtime (1.4.1) creates — ` + rule + `; use a DNS domain of those characters instead of "o+x" (` + "`--dns-domain`)",
			"run":         `container name "web-run.demo.o+x" is not one the container runtime (1.4.1) creates — ` + rule + `; use a DNS domain of those characters instead of "o+x" (` + "`--dns-domain`)",
			"run --audit": `container name "web-run.demo.o+x" is not one the container runtime (1.4.1) creates — ` + rule + `; use a DNS domain of those characters instead of "o+x" (` + "`--dns-domain`)"}},
	} {
		for _, path := range []string{"up", "run", "run --audit"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				rt, log := fakeShim(t)
				setShimEnv(rt, "INSPECT_PROJECT=demo") // this project's containers, with a DNS domain or without one (the fake cannot read the project from a name without)
				proj := project("demo", map[string]*compose.Service{
					"db":       {Image: "alpine:3.20"},
					tc.service: {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "db"}}},
				})
				o := orchestrator.New(proj, rt, tc.domain, &bytes.Buffer{})
				var err error
				switch path {
				case "up":
					err = o.Up(true)
				case "run":
					err = o.RunOneOff(tc.service, []string{"true"}, orchestrator.RunOneOffOptions{})
				default:
					_, err = o.RunAudited(tc.service, []string{"true"}, orchestrator.RunOneOffOptions{})
				}
				want, refused := tc.refusal[path]
				if !refused {
					if err != nil {
						t.Errorf("want it to go ahead, got %v", err)
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
	// A service starting with a digit is one the runtime names a container
	// with, so a DNS domain it refuses is what the advice names.
	t.Run("a name starting with a digit and a DNS domain the runtime refuses", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "INSPECT_PROJECT=demo") // this project's containers, with a DNS domain or without one (the fake cannot read the project from a name without)
		err := orchestrator.New(project("demo", map[string]*compose.Service{"9web": {Image: "alpine:3.20"}}), rt, "o+x", &bytes.Buffer{}).Up(true)
		if want := `container name "9web.demo.o+x" is not one the container runtime (1.4.1) creates — ` + rule + `; use a DNS domain of those characters instead of "o+x" (` + "`--dns-domain`)"; err == nil || err.Error() != want {
			t.Errorf("\n got %v\nwant %s", err, want)
		}
	})
	// The service the runtime refuses starts second: `up` looks at each.
	t.Run("the second service to start", func(t *testing.T) {
		rt, log := fakeShim(t)
		setShimEnv(rt, "INSPECT_PROJECT=demo") // this project's containers, with a DNS domain or without one (the fake cannot read the project from a name without)
		proj := project("demo", map[string]*compose.Service{"ab": {Image: "alpine:3.20"}, "z+z": {Image: "alpine:3.20"}})
		err := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).Up(true)
		if want := renameService("z+z.demo.opossum", "z+z"); err == nil || err.Error() != want {
			t.Errorf("\n got %v\nwant %s", err, want)
		}
		if indexOf(log(), "network create") >= 0 || runLine(log()) >= 0 {
			t.Errorf("want nothing created or started before the refusal, got %v", log())
		}
	})
}

func TestAServiceNameItsPeersCannotLookUpIsWarnedAboutAndRuns(t *testing.T) {
	const code = "[OPSM-209]"
	for _, tc := range []struct {
		name, service, domain string
		warned                bool
	}{
		{"upper case", "MyDb", "opossum", true},
		{"upper case last", "mydB", "opossum", true},
		{"a trailing dot", "web.", "opossum", true},
		{"two dots in the middle", "a..b", "opossum", true},
		{"a dot in the middle", "web.api", "opossum", true},
		{"upper case spelled like a top-level domain", "Web", "opossum", true},
		{"a name starting with a digit", "9db", "opossum", false},
		{"lower case, digits, underscore and dash", "my_db-2", "opossum", false},
		{"upper case without a DNS domain", "MyDb", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, "INSPECT_PROJECT=demo") // this project's containers, with a DNS domain or without one (the fake cannot read the project from a name without)
			var out bytes.Buffer
			proj := project("demo", map[string]*compose.Service{tc.service: {Image: "alpine:3.20"}})
			if err := orchestrator.New(proj, rt, tc.domain, &out).Up(true); err != nil {
				t.Fatalf("want it to run, got %v", err)
			}
			if runLine(log()) < 0 {
				t.Errorf("want the service started, got %v", log())
			}
			got := strings.Count(out.String(), code)
			if tc.warned && (got != 1 || !strings.Contains(out.String(), `service "`+tc.service+`" may not be reachable by that name`)) {
				t.Errorf("want one %s naming %q, got:\n%s", code, tc.service, out.String())
			}
			if tc.warned && !strings.Contains(out.String(), "another address when spelled like a top-level domain (Web)") {
				t.Errorf("want the warning to say a name spelled like a top-level domain gets another address, got:\n%s", out.String())
			}
			if !tc.warned && got != 0 {
				t.Errorf("want no %s, got:\n%s", code, out.String())
			}
		})
	}
	// Among several services, each warned about once and only those: the ones
	// warned about first, last, and around ones that are not.
	for _, tc := range []struct {
		name     string
		services []string
		warned   map[string]bool
	}{
		{"warned first and last", []string{"Bdb", "adb", "cdb", "dDb"}, map[string]bool{"Bdb": true, "dDb": true}},
		{"not warned first, warned after", []string{"adb", "bDb"}, map[string]bool{"bDb": true}},
		{"warned first, not warned after", []string{"aDb", "bdb"}, map[string]bool{"aDb": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			setShimEnv(rt, "INSPECT_PROJECT=demo") // this project's containers, with a DNS domain or without one (the fake cannot read the project from a name without)
			var out bytes.Buffer
			svcs := map[string]*compose.Service{}
			for _, n := range tc.services {
				svcs[n] = &compose.Service{Image: "alpine:3.20"}
			}
			if err := orchestrator.New(project("demo", svcs), rt, "opossum", &out).Up(true); err != nil {
				t.Fatalf("want it to run, got %v", err)
			}
			for _, n := range tc.services {
				got := strings.Count(out.String(), `service "`+n+`" may not be reachable`)
				if want := map[bool]int{true: 1, false: 0}[tc.warned[n]]; got != want {
					t.Errorf("service %q warned %d times, want %d:\n%s", n, got, want, out.String())
				}
			}
		})
	}
	// Brought up again by the same command (a `watch` rebuild does this on every
	// change), the warning is not said again.
	t.Run("up twice in one command", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "INSPECT_PROJECT=demo") // this project's containers, with a DNS domain or without one (the fake cannot read the project from a name without)
		var out bytes.Buffer
		o := orchestrator.New(project("demo", map[string]*compose.Service{"MyDb": {Image: "alpine:3.20"}}), rt, "opossum", &out)
		for i := 0; i < 2; i++ {
			if err := o.Up(true); err != nil {
				t.Fatal(err)
			}
		}
		if got := strings.Count(out.String(), code); got != 1 {
			t.Errorf("want one %s over two ups, got %d:\n%s", code, got, out.String())
		}
	})
	// A service `up` does not start is not warned about.
	t.Run("a service not started", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "INSPECT_PROJECT=demo") // this project's containers, with a DNS domain or without one (the fake cannot read the project from a name without)
		var out bytes.Buffer
		proj := project("demo", map[string]*compose.Service{"api": {Image: "alpine:3.20"}, "MyDb": {Image: "alpine:3.20"}})
		if err := orchestrator.New(proj, rt, "opossum", &out).Up(true, "api"); err != nil {
			t.Fatalf("up api: %v", err)
		}
		if strings.Contains(out.String(), code) {
			t.Errorf("want no %s for a service not started, got:\n%s", code, out.String())
		}
	})
	// The warning comes before the first container is created.
	t.Run("before anything is created", func(t *testing.T) {
		rt, _ := fakeShim(t)
		setShimEnv(rt, "INSPECT_PROJECT=demo") // this project's containers, with a DNS domain or without one (the fake cannot read the project from a name without)
		var out bytes.Buffer
		proj := project("demo", map[string]*compose.Service{"MyDb": {Image: "alpine:3.20"}})
		if err := orchestrator.New(proj, rt, "opossum", &out).Up(true); err != nil {
			t.Fatal(err)
		}
		w, c := strings.Index(out.String(), code), strings.Index(out.String(), "Creating network")
		if w < 0 || c < 0 || w > c {
			t.Errorf("want the warning before `Creating network`, got:\n%s", out.String())
		}
	})
}

// Two services `up` starts whose names differ only in case cannot both run:
// container 1.4.1 fails the second with "failed to bootstrap container", with a
// DNS domain in the names or without (measured). Refused before anything is
// created or an orphan removed. A file that never starts both runs, as under
// docker compose: one behind a profile that is not active, `up` of one of them,
// and a one-off (named `<service>-run`).
func TestServiceNamesThatDifferOnlyInCaseAreRefusedBeforeCreating(t *testing.T) {
	pair := func(a, b string) string {
		return `services "` + a + `" and "` + b + `" differ only in case, and the container runtime (1.4.1) cannot run both — the second container fails with "failed to bootstrap container"; rename one of them`
	}
	svc := func(profiles ...string) *compose.Service {
		return &compose.Service{Image: "alpine:3.20", Profiles: profiles}
	}
	for _, tc := range []struct {
		name     string
		services map[string]*compose.Service
		call     func(o *orchestrator.Orchestrator) error
		refusal  string // "" goes ahead
	}{
		{"up of two", map[string]*compose.Service{"Com": svc(), "com": svc()}, func(o *orchestrator.Orchestrator) error { return o.Up(true) }, pair("Com", "com")},
		// Sorted, upper case comes before lower case, so another name falls between.
		{"up with another name between them", map[string]*compose.Service{"Api": svc(), "Web": svc(), "api": svc()}, func(o *orchestrator.Orchestrator) error { return o.Up(true) }, pair("Api", "api")},
		{"up with one behind a profile that is not active", map[string]*compose.Service{"Com": svc("debug"), "com": svc()}, func(o *orchestrator.Orchestrator) error { return o.Up(true) }, ""},
		{"up of one of them", map[string]*compose.Service{"Com": svc(), "com": svc()}, func(o *orchestrator.Orchestrator) error { return o.Up(true, "com") }, ""},
		{"a one-off", map[string]*compose.Service{"Com": svc(), "com": svc()}, func(o *orchestrator.Orchestrator) error {
			return o.RunOneOff("com", []string{"true"}, orchestrator.RunOneOffOptions{})
		}, ""},
		{"an audited one-off", map[string]*compose.Service{"Com": svc(), "com": svc()}, func(o *orchestrator.Orchestrator) error {
			_, err := o.RunAudited("com", []string{"true"}, orchestrator.RunOneOffOptions{})
			return err
		}, ""},
		{"names that differ in more than case", map[string]*compose.Service{"Com": svc(), "coms": svc()}, func(o *orchestrator.Orchestrator) error { return o.Up(true) }, ""},
	} {
		for _, domain := range []string{"opossum", ""} {
			t.Run(tc.name+"/"+domain, func(t *testing.T) {
				rt, log := fakeShim(t)
				setShimEnv(rt, "INSPECT_PROJECT=demo") // this project's containers, with a DNS domain or without one (the fake cannot read the project from a name without)
				svcs := map[string]*compose.Service{}
				for n, s := range tc.services {
					c := *s
					svcs[n] = &c
				}
				err := tc.call(orchestrator.New(project("demo", svcs), rt, domain, &bytes.Buffer{}))
				if tc.refusal == "" {
					if err != nil {
						t.Errorf("want it to go ahead, got %v", err)
					}
					return
				}
				if err == nil || err.Error() != tc.refusal {
					t.Errorf("\n got %v\nwant %s", err, tc.refusal)
				}
				if indexOf(log(), "network create") >= 0 || runLine(log()) >= 0 {
					t.Errorf("want nothing created or started before the refusal, got %v", log())
				}
			})
		}
	}
	t.Run("before orphans are removed", func(t *testing.T) {
		rt, log := fakeShim(t)
		setShimEnv(rt, "INSPECT_PROJECT=demo") // this project's containers, with a DNS domain or without one (the fake cannot read the project from a name without)
		setShimEnv(rt, "LS_CONTAINERS=com.demo.opossum old.demo.opossum", "LS_PROJECT=demo")
		o := orchestrator.New(project("demo", map[string]*compose.Service{"Com": svc(), "com": svc()}), rt, "opossum", &bytes.Buffer{})
		o.SetUpOptions(false, false, false, true, false) // --remove-orphans
		if err := o.Up(true); err == nil || err.Error() != pair("Com", "com") {
			t.Fatalf("want the pair refused, got %v", err)
		}
		for _, l := range log() {
			if strings.HasPrefix(l, "stop ") || strings.HasPrefix(l, "delete ") {
				t.Errorf("want no orphan touched before the refusal, got %v", log())
				break
			}
		}
	})
}
