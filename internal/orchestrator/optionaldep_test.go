package orchestrator_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A `required: false` dependency behind a profile that is not active is left
// out: `config` does not refuse it, `up` and `run` go ahead without it, and
// `up web` does not turn it on either (docker compose v5.5.1, measured
// 2026-09-19). A required one is refused as before, `up web` included
// (naming the dependent does not turn on a dependency outside its own
// profiles; see TestNamingCarriesAnOptionalDependencyOnlyUnderASharedProfile).
func TestAnOptionalDependencyBehindAProfileIsLeftOut(t *testing.T) {
	const file = "name: demo\nservices:\n  web:\n    image: alpine:3.20\n    depends_on:\n      cache:\n        condition: service_started\n        required: REQ\n  cache:\n    image: alpine:3.20\n    profiles: [tools]\n"
	for _, tc := range []struct {
		name          string
		required      string
		named         []string
		configRefused bool // `config` reads no names
		upRefused     bool
		starts        []string // containers run
	}{
		{"optional, up", "false", nil, false, false, []string{"web"}},
		{"optional, up web", "false", []string{"web"}, false, false, []string{"web"}},
		{"required, up", "true", nil, true, true, nil},
		{"required, up web", "true", []string{"web"}, true, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "compose.yaml")
			if err := os.WriteFile(p, []byte(strings.ReplaceAll(file, "REQ", tc.required)), 0o644); err != nil {
				t.Fatal(err)
			}
			// Each command reads the file afresh, as it does: what one command
			// left out of the project must not stand in for the next.
			load := func() *compose.Project {
				proj, err := compose.Load(p)
				if err != nil {
					t.Fatal(err)
				}
				return proj
			}
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			if err := orchestrator.New(load(), fakeShimInspect(t, "", 0), "opossum", &bytes.Buffer{}).ValidateProfiles(); (err != nil) != tc.configRefused || (err != nil && !strings.Contains(err.Error(), `depends on "cache"`)) {
				t.Fatalf("config: refused %v, want %v (%v)", err != nil, tc.configRefused, err)
			}
			rt, log := fakeShim(t)
			o := orchestrator.New(load(), rt, "opossum", &bytes.Buffer{})
			upErr := o.Up(true, tc.named...)
			if (upErr != nil) != tc.upRefused {
				t.Fatalf("up: err %v, want refused %v", upErr, tc.upRefused)
			}
			// `run web` reads the same way (a one-off starts the dependencies it
			// has, and an optional gated one is not among them).
			rt2, log2 := fakeShim(t)
			o2 := orchestrator.New(load(), rt2, "opossum", &bytes.Buffer{})
			runErr := o2.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{})
			if (runErr != nil) != tc.configRefused {
				t.Fatalf("run web: err %v, want refused %v", runErr, tc.configRefused)
			}
			if !tc.configRefused && indexOf(log2(), "cache.demo.") >= 0 {
				t.Errorf("run web started the optional gated dependency:\n%s", strings.Join(log2(), "\n"))
			}
			var ran []string
			for _, svc := range []string{"cache", "web"} {
				if indexOf(log(), "run -d --name "+svc+".demo.") >= 0 {
					ran = append(ran, svc)
				}
			}
			if strings.Join(ran, ",") != strings.Join(tc.starts, ",") {
				t.Errorf("ran %v, want %v\n%s", ran, tc.starts, strings.Join(log(), "\n"))
			}
		})
	}
}

// An optional dependency that is active is waited for as any other — the
// whole healthcheck, retries and all — and, not healthy, is noted and passed
// over; a required one refuses the dependent. The wait is what separates
// "waited, then passed over" (docker compose's way) from "not waited for":
// the probes are counted, and their number follows the retries.
func TestAnOptionalDependencyNotHealthyIsWaitedForThenPassedOver(t *testing.T) {
	for _, tc := range []struct {
		name     string
		optional bool
		retries  int
		webRuns  bool
	}{
		{"optional, 2 retries", true, 2, true},
		{"optional, 5 retries", true, 5, true},
		{"required, 2 retries", false, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, "HEALTH_OK_AT=999")
			p := project("demo", map[string]*compose.Service{
				"db":  {Image: "postgres:16", Healthcheck: &compose.Healthcheck{Test: []string{"pg_isready"}, Interval: time.Millisecond, Retries: tc.retries}},
				"web": {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "db", Condition: compose.ConditionHealthy, Optional: tc.optional}}},
			})
			var out bytes.Buffer
			o := orchestrator.New(p, rt, "opossum", &out)
			err := o.Up(true)
			if (err == nil) != tc.webRuns {
				t.Fatalf("up: %v, want to go ahead %v", err, tc.webRuns)
			}
			if n := countLines(log(), "exec db.demo.opossum"); n != tc.retries {
				t.Errorf("want %d probes (the retries), got %d", tc.retries, n)
			}
			if got := indexOf(log(), "run -d --name web.demo.opossum") >= 0; got != tc.webRuns {
				t.Errorf("web ran: %v, want %v", got, tc.webRuns)
			}
			if tc.optional && !strings.Contains(out.String(), `[OPSM-413] optional dependency "db" of service "web" is not healthy`) {
				t.Errorf("want the note that the optional dependency is not healthy, got\n%s", out.String())
			}
		})
	}
}

// An optional run-to-completion dependency that exits non-zero is noted and
// the dependent starts; a required one fails the up (docker compose v5.5.1:
// `warning: optional dependency "cache" didn't complete successfully: exit 1`).
func TestAnOptionalDependencyThatDoesNotCompleteIsPassedOver(t *testing.T) {
	for _, tc := range []struct {
		name     string
		optional bool
		webRuns  bool
	}{
		{"optional", true, true},
		{"required", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, "RUN_FAIL=migrate.demo.opossum")
			p := project("demo", map[string]*compose.Service{
				"migrate": {Image: "migrate:1"},
				"web":     {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "migrate", Condition: compose.ConditionCompleted, Optional: tc.optional}}},
			})
			var out bytes.Buffer
			o := orchestrator.New(p, rt, "opossum", &out)
			err := o.Up(true)
			if (err == nil) != tc.webRuns {
				t.Fatalf("up: %v, want to go ahead %v", err, tc.webRuns)
			}
			if got := indexOf(log(), "run -d --name web.demo.opossum") >= 0; got != tc.webRuns {
				t.Errorf("web ran: %v, want %v", got, tc.webRuns)
			}
			if tc.optional && !strings.Contains(out.String(), `[OPSM-413] optional dependency "migrate" didn't complete successfully`) {
				t.Errorf("want the note, got\n%s", out.String())
			}
		})
	}
}

// What naming a service carries is decided by profiles shared, and
// `required` does not change it (docker compose v5.5.1, measured 2026-09-19):
// `up web` with web and cache both under `tools` starts cache, optional or
// not; with cache under another profile an optional cache is left out and a
// required one refuses the run.
func TestNamingCarriesAnOptionalDependencyOnlyUnderASharedProfile(t *testing.T) {
	const file = "name: demo\nservices:\n  web:\n    image: alpine:3.20\n    profiles: [tools]\n    depends_on:\n      cache:\n        condition: service_started\n        required: REQ\n  cache:\n    image: alpine:3.20\n    profiles: [PROF]\n"
	for _, tc := range []struct {
		name, required, profile string
		refused                 bool
		starts                  []string
	}{
		{"shared profile, optional", "false", "tools", false, []string{"cache", "web"}},
		{"shared profile, required", "true", "tools", false, []string{"cache", "web"}},
		{"another profile, optional", "false", "other", false, []string{"web"}},
		{"another profile, required", "true", "other", true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "compose.yaml")
			body := strings.ReplaceAll(strings.ReplaceAll(file, "REQ", tc.required), "PROF", tc.profile)
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			proj, err := compose.Load(p)
			if err != nil {
				t.Fatal(err)
			}
			rt, log := fakeShim(t)
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			o := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{})
			err = o.Up(true, "web")
			if (err != nil) != tc.refused {
				t.Fatalf("up web: %v, want refused %v", err, tc.refused)
			}
			var ran []string
			for _, svc := range []string{"cache", "web"} {
				if indexOf(log(), "run -d --name "+svc+".demo.") >= 0 {
					ran = append(ran, svc)
				}
			}
			if strings.Join(ran, ",") != strings.Join(tc.starts, ",") {
				t.Errorf("ran %v, want %v", ran, tc.starts)
			}
		})
	}
}

// `config` prints an optional dependency behind a profile as written, with
// `required: false` (docker compose v5.5.1, measured 2026-09-19) — the check
// that refuses a required gated dependency passes an optional one over and
// leaves the project as read, in the order `opossum config` does it.
func TestConfigPrintsAnOptionalGatedDependencyAsWritten(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(p, []byte("name: demo\nservices:\n  web:\n    image: alpine:3.20\n    depends_on:\n      cache:\n        condition: service_started\n        required: false\n      db:\n        condition: service_started\n  cache:\n    image: alpine:3.20\n    profiles: [tools]\n  db:\n    image: alpine:3.20\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := compose.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	o := orchestrator.New(proj, fakeShimInspect(t, "", 0), "opossum", &bytes.Buffer{})
	if err := o.ValidateProfiles(); err != nil {
		t.Fatal(err)
	}
	out, err := compose.RenderConfig(o.Project)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cache:\n                condition: service_started\n                required: false", "db:\n                condition: service_started\n                required: true"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in the config, got\n%s", want, out)
		}
	}
}

// An optional dependency's failure passes over that dependency only: the
// others are still waited for, in full, and the dependent starts after them.
func TestAnOptionalDependencyNotHealthyLeavesTheOthersWaitedFor(t *testing.T) {
	rt, log := fakeShim(t)
	setShimEnv(rt, "HEALTH_OK_AT=999")
	p := project("demo", map[string]*compose.Service{
		"db":    {Image: "postgres:16", Healthcheck: &compose.Healthcheck{Test: []string{"pg_isready"}, Interval: time.Millisecond, Retries: 2}},
		"cache": {Image: "redis:7", Healthcheck: &compose.Healthcheck{Test: []string{"redis-cli", "ping"}, Interval: time.Millisecond, Retries: 3}},
		"web": {Image: "web:latest", DependsOn: compose.DependsOn{
			{Name: "db", Condition: compose.ConditionHealthy, Optional: true},
			{Name: "cache", Condition: compose.ConditionHealthy, Optional: true},
		}},
	})
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatal(err)
	}
	if n := countLines(log(), "exec db.demo.opossum"); n != 2 {
		t.Errorf("db: want 2 probes, got %d", n)
	}
	if n := countLines(log(), "exec cache.demo.opossum"); n != 3 {
		t.Errorf("cache: want 3 probes (waited for in full after db was passed over), got %d", n)
	}
	if indexOf(log(), "run -d --name web.demo.opossum") < 0 {
		t.Errorf("web did not start:\n%s", strings.Join(log(), "\n"))
	}
}

// Whether a run-to-completion failure is passed over is decided by every
// dependent in the file that needs the completion: a required one keeps the
// failure fatal, gated or not — the same set completedTargets reads when it
// decides what runs to completion. Known difference, kept on purpose: docker
// compose v5.5.1 (measured 2026-09-19) reads only the dependents it starts,
// so `admin`, gated and requiring `migrate`, does not keep `web` from
// starting there. One that waits with another condition does not count.
func TestACompletionFailureIsJudgedByEveryDependentInTheFile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		other   *compose.Service
		webRuns bool
	}{
		{"a gated required dependent counts (docker compose: not read)", &compose.Service{Image: "admin:1", Profiles: []string{"tools"}, DependsOn: compose.DependsOn{{Name: "migrate", Condition: compose.ConditionCompleted}}}, false},
		{"a gated optional dependent does not count", &compose.Service{Image: "admin:1", Profiles: []string{"tools"}, DependsOn: compose.DependsOn{{Name: "migrate", Condition: compose.ConditionCompleted, Optional: true}}}, true},
		{"an active required dependent counts", &compose.Service{Image: "admin:1", DependsOn: compose.DependsOn{{Name: "migrate", Condition: compose.ConditionCompleted}}}, false},
		{"a dependent waiting for a start only does not count", &compose.Service{Image: "admin:1", DependsOn: compose.DependsOn{{Name: "migrate", Condition: compose.ConditionStarted}}}, true},
		// Judged by name: a required completion of another service (`seed`,
		// which completes) says nothing about `migrate`. Without the name in
		// the judgment, any required completion in the file made every
		// failure fatal (a mutation the rows above did not see).
		{"a required completion of another service does not count", &compose.Service{Image: "admin:1", DependsOn: compose.DependsOn{{Name: "seed", Condition: compose.ConditionCompleted}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, "RUN_FAIL=migrate.demo.opossum")
			p := project("demo", map[string]*compose.Service{
				"migrate": {Image: "migrate:1"},
				"seed":    {Image: "seed:1"},
				"web":     {Image: "web:latest", DependsOn: compose.DependsOn{{Name: "migrate", Condition: compose.ConditionCompleted, Optional: true}}},
				"admin":   tc.other,
			})
			o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
			err := o.Up(true)
			if (err == nil) != tc.webRuns {
				t.Fatalf("up: %v, want to go ahead %v", err, tc.webRuns)
			}
			if got := indexOf(log(), "run -d --name web.demo.opossum") >= 0; got != tc.webRuns {
				t.Errorf("web ran: %v, want %v", got, tc.webRuns)
			}
		})
	}
}

// A one-off's own service is a dependent in the file: `run web` with a
// required run-to-completion dependency that fails is refused (and the exit
// code is the dependency's, as cmd/opossum checks), where an optional one is
// passed over — whether or not a profile gates `web` (docker compose v5.5.1:
// `run --rm web` of a gated `web` whose required `migrate` exits 3 is
// refused, `service "migrate" didn't complete successfully`; measured
// 2026-09-19). Through `run --audit` as through `run`.
func TestARunOneOffJudgesACompletionFailureByItsOwnService(t *testing.T) {
	for _, tc := range []struct {
		name     string
		optional bool
		gated    bool
		audited  bool
		runs     bool
	}{
		{"required", false, false, false, false},
		{"optional", true, false, false, true},
		{"required, the service behind a profile", false, true, false, false},
		{"optional, the service behind a profile", true, true, false, true},
		{"required, behind a profile, audited", false, true, true, false},
		{"optional, behind a profile, audited", true, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, "RUN_FAIL=migrate.demo.opossum")
			web := &compose.Service{Image: "web:latest", DependsOn: compose.DependsOn{{Name: "migrate", Condition: compose.ConditionCompleted, Optional: tc.optional}}}
			if tc.gated {
				web.Profiles = []string{"tools"}
			}
			p := project("demo", map[string]*compose.Service{
				"migrate": {Image: "migrate:1"},
				"web":     web,
			})
			o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
			var err error
			if tc.audited {
				_, err = o.RunAudited("web", []string{"true"}, orchestrator.RunOneOffOptions{})
			} else {
				err = o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{})
			}
			if (err == nil) != tc.runs {
				t.Fatalf("run web: %v, want to go ahead %v", err, tc.runs)
			}
			if got := indexOf(log(), "--name web-run.demo.opossum") >= 0; got != tc.runs {
				t.Errorf("web ran: %v, want %v\n%s", got, tc.runs, strings.Join(log(), "\n"))
			}
		})
	}
}
