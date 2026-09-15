package orchestrator

// Error-quality evals, batch 2 (#277): the long-tail lifecycle failures (start /
// restart / pull / logs) and the watch warnings must all point at a next step.

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

func TestStartFailedHasLogsHint(t *testing.T) {
	s := startFailed("web", fmt.Errorf("exit status 1")).Error()
	if !strings.Contains(s, `starting service "web"`) || !strings.Contains(s, "opossum logs web") {
		t.Errorf("a generic start failure should point at the logs, got: %s", s)
	}
}

func lifecycleProject() *compose.Project {
	return &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"web": {Name: "web", Image: "web:latest"},
	}}
}

func TestStartHasNextStep(t *testing.T) {
	// `container start` fails → o.Start must explain that the container must exist.
	shim := scriptShim(t, "  start) exit 1 ;;\n")
	err := New(lifecycleProject(), shim, "opossum", &bytes.Buffer{}).Start(nil)
	if err == nil {
		t.Fatal("expected Start to fail")
	}
	// One failure is the failure itself, not a list of one.
	if s := err.Error(); !strings.HasPrefix(s, `starting service "web": `) || !strings.Contains(s, "opossum up web") {
		t.Errorf("start failure should point at `opossum up`, got: %s", s)
	}
}

func TestRestartHasNextStep(t *testing.T) {
	shim := scriptShim(t, "  start) exit 1 ;;\n") // stop succeeds, start fails
	err := New(lifecycleProject(), shim, "opossum", &bytes.Buffer{}).Restart(nil)
	if err == nil {
		t.Fatal("expected Restart to fail")
	}
	if s := err.Error(); !strings.HasPrefix(s, `restarting service "web": `) || !strings.Contains(s, "opossum up web") {
		t.Errorf("restart failure should point at `opossum up`, got: %s", s)
	}
}

func TestPullHasNextStep(t *testing.T) {
	shim := scriptShim(t, "  image) if [ \"$2\" = pull ]; then exit 1; fi ;;\n")
	var out bytes.Buffer
	err := New(lifecycleProject(), shim, "opossum", &out).Pull(nil)
	if err == nil {
		t.Fatal("expected Pull to fail")
	}
	if s := err.Error(); !strings.Contains(s, "registry auth") || !strings.Contains(s, "web:latest") {
		t.Errorf("pull failure should name the image and hint at auth/network, got: %s", s)
	}
	// The progress line and the failure both name the service and then the
	// image; both are strings, and exchanged they read as pulling an image
	// called web (#559).
	if want := "Pulling web (web:latest)\n"; !strings.Contains(out.String(), want) {
		t.Errorf("the progress line should read %q, got:\n%s", want, out.String())
	}
	if s := err.Error(); !strings.HasPrefix(s, `pulling service "web": `) || !strings.Contains(s, `check the image name "web:latest" and`) {
		t.Errorf("the failure should open with the service and name the image in the hint, got: %s", s)
	}
}

func TestLogsHasNextStep(t *testing.T) {
	shim := scriptShim(t, "  logs) exit 1 ;;\n")
	err := New(lifecycleProject(), shim, "opossum", &bytes.Buffer{}).Logs(nil, runtime.LogsOptions{})
	if err == nil {
		t.Fatal("expected Logs to fail")
	}
	if s := err.Error(); !strings.Contains(s, "opossum ps") {
		t.Errorf("logs failure should point at `opossum ps`, got: %s", s)
	}
}

func TestWatchRebuildFailureHasNextStep(t *testing.T) {
	// A rebuild-action change whose rebuild (Up) fails must warn with a next step.
	dir := t.TempDir()
	shim := scriptShim(t, ""+
		"  system) echo 'status running' ;;\n"+
		"  ls) echo '[]' ;;\n"+
		"  run) exit 1 ;;\n")
	var out bytes.Buffer
	o := New(watchProject(dir, "rebuild"), shim, "opossum", &out)
	o.applyChanges([]string{dir + "/components/x.js"})
	if s := out.String(); !strings.Contains(s, "rebuild app failed") || !strings.Contains(s, "opossum up --build app") {
		t.Errorf("rebuild failure should warn with a next step, got: %s", s)
	}
}

func TestWatchRestartFailureHasNextStep(t *testing.T) {
	dir := t.TempDir()
	shim := scriptShim(t, "  start) exit 1 ;;\n") // sync+stop ok, start fails
	var out bytes.Buffer
	o := New(watchProject(dir, "sync+restart"), shim, "opossum", &out)
	o.applyChanges([]string{dir + "/components/x.js"})
	if s := out.String(); !strings.Contains(s, "restart app failed") || !strings.Contains(s, "opossum up app") {
		t.Errorf("restart failure should warn with a next step, got: %s", s)
	}
}

// The watch sync warning must name the file and the target service (not the bare
// "sync failed" it used to print), plus a next step.
func TestWatchSyncFailureNamesFileAndService(t *testing.T) {
	dir := t.TempDir()
	shim := scriptShim(t, "  cp) exit 1 ;;\n") // the file copy fails
	var out bytes.Buffer
	o := New(watchProject(dir, "sync"), shim, "opossum", &out)

	changed := dir + "/components/x.js"
	o.handleChange(changed)

	s := out.String()
	if !strings.Contains(s, "x.js") || !strings.Contains(s, `"app"`) || !strings.Contains(s, "opossum ps") {
		t.Errorf("sync failure should name the file + service and point at `opossum ps`, got: %s", s)
	}
	// Both lines go host file first, container target second — two strings on
	// one format call each, and exchanged they read as copying out of the
	// container (#559).
	for _, want := range []string{"sync " + changed + " → app:", "sync of " + changed + " to app:"} {
		if !strings.Contains(s, want) {
			t.Errorf("the line should read %q…, got: %s", want, s)
		}
	}
}

// A watch action that an owner check declined did not fail: the container is
// another project's, or the runtime would not say whose it is. The warning says
// it was skipped, with advice for that case and no advice about a container
// that may be gone or not running — while a real failure keeps that advice,
// each action its own.
func TestWatchSaysAnOwnerRefusalWithoutFailureAdvice(t *testing.T) {
	foreign := `  inspect) echo '[{"status":{"state":"running"},"configuration":{"id":"app.demo.opossum","labels":{"opossum.project":"otherproj"}}}]' ;;` + "\n"
	unreadable := "  inspect) echo 'boom' >&2; exit 2 ;;\n"
	noLabel := `  inspect) echo '[{"status":{"state":"running"},"configuration":{"id":"app.demo.opossum","labels":{}}}]' ;;` + "\n"
	sync := func(dir string) string { return "sync of " + dir + "/components/x.js to app:/app/src/components/x.js" }
	syncNoLabel := func(dir string) string {
		return "warning: [OPSM-603] " + sync(dir) + ` skipped: container "app.demo.opossum" already exists and carries no opossum.project label, so it was not made by this project and is left alone; ` +
			"remove it (`container delete --force app.demo.opossum`) to free the name, or give this project its own DNS domain (e.g. --dns-domain demo)"
	}
	syncUnanswered := func(dir string) string {
		return "warning: [OPSM-603] " + sync(dir) + " skipped: the runtime gave no readable answer about which project owns app.demo.opossum, so it was left alone — " +
			"`container inspect app.demo.opossum` shows what the runtime says; save " + dir + "/components/x.js again once it answers"
	}
	for _, tc := range []struct {
		name, action, cases string
		// warnings, in order; one ending in "…" is a prefix and a suffix
		// around the failure's own message, which is the runtime's.
		want func(dir string) []string
		log  string // a line the output must also carry
	}{
		{"sync to another project's container", "sync", foreign, func(dir string) []string {
			return []string{"warning: [OPSM-603] " + sync(dir) + ` skipped: container "app.demo.opossum" is already in use by project "otherproj"; ` +
				"give this project its own DNS domain so names don't collide (e.g. --dns-domain demo, created once with `sudo container system dns create demo`) — see README (multi-project)"}
		}, ""},
		{"sync with no readable answer", "sync", unreadable, func(dir string) []string { return []string{syncUnanswered(dir)} }, ""},
		{"sync and restart with no readable answer", "sync+restart", unreadable, func(dir string) []string {
			return []string{syncUnanswered(dir),
				"warning: [OPSM-602] restart app skipped: the runtime gave no readable answer about which project owns app.demo.opossum, so it was left alone — " +
					"`container inspect app.demo.opossum` shows what the runtime says; watch tries the restart again when a file under a sync+restart rule of app next changes"}
		}, ""},
		{"sync and restart with another project's container", "sync+restart", foreign, func(dir string) []string {
			return []string{"warning: [OPSM-603] " + sync(dir) + ` skipped: container "app.demo.opossum" is already in use by project "otherproj"; ` +
				"give this project its own DNS domain so names don't collide (e.g. --dns-domain demo, created once with `sudo container system dns create demo`) — see README (multi-project)"}
		}, `Leaving container app.demo.opossum alone: it belongs to project "otherproj"`},
		// A container with no project label is refused the way another project's
		// is — its own reason, not "no readable answer" (which would say to save
		// again once the runtime answers, when it has answered).
		{"sync to a container with no project label", "sync", noLabel, func(dir string) []string { return []string{syncNoLabel(dir)} }, ""},
		{"sync and restart with a container with no project label", "sync+restart", noLabel, func(dir string) []string { return []string{syncNoLabel(dir)} },
			"Leaving container app.demo.opossum alone: it carries no opossum.project label"},
		{"a copy that fails keeps its advice", "sync", "  cp) exit 1 ;;\n", func(dir string) []string {
			return []string{"warning: [OPSM-603] " + sync(dir) + " failed: …— check the container \"app\" is running (`opossum ps`)"}
		}, ""},
		{"a restart that fails keeps its advice", "sync+restart", "  start) exit 1 ;;\n", func(string) []string {
			return []string{"warning: [OPSM-602] restart app failed: …— the container may be gone; run `opossum up app` to recreate it"}
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var out bytes.Buffer
			o := New(watchProject(dir, tc.action), scriptShim(t, tc.cases), "opossum", &out)
			o.applyChanges([]string{dir + "/components/x.js"})
			// A warning is its "warning: " line and the indented lines under it:
			// a failure's own message can span lines.
			var warnings []string
			for _, l := range strings.Split(out.String(), "\n") {
				switch {
				case strings.HasPrefix(l, "warning: "):
					warnings = append(warnings, l)
				case strings.HasPrefix(l, "  ") && len(warnings) > 0:
					warnings[len(warnings)-1] += "\n" + l
				}
			}
			want := tc.want(dir)
			if len(warnings) != len(want) {
				t.Fatalf("want %d warning(s), got %d:\n%s", len(want), len(warnings), out.String())
			}
			for i, w := range want {
				if head, tail, around := strings.Cut(w, "…"); around {
					if !strings.HasPrefix(warnings[i], head) || !strings.HasSuffix(warnings[i], tail) {
						t.Errorf("warning %d:\n got %s\nwant %s", i, warnings[i], w)
					}
				} else if warnings[i] != w {
					t.Errorf("warning %d:\n got %s\nwant %s", i, warnings[i], w)
				}
			}
			if tc.log != "" && !strings.Contains(out.String(), tc.log) {
				t.Errorf("want %q in the output, got:\n%s", tc.log, out.String())
			}
		})
	}
}

// What the skipped restart promises — another try when a file under a
// sync+restart rule of the service changes — is what watch does: a file under
// the same service's plain sync rule is synced and restarts nothing, and one
// under its sync+restart rule tries the restart again.
func TestWatchTriesASkippedRestartOnlyForASyncRestartFile(t *testing.T) {
	dir := t.TempDir()
	p := &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"app": {Name: "app", Image: "app", Develop: &compose.Develop{Watch: []compose.WatchRule{
			{Action: "sync", Path: dir + "/src", Target: "/app/src"},
			{Action: "sync+restart", Path: dir + "/package.json", Target: "/app/package.json"},
		}}},
	}}
	for _, tc := range []struct {
		name, changed string
		restart       bool
	}{
		{"a file under the sync rule", dir + "/src/x.js", false},
		{"the file under the sync+restart rule", dir + "/package.json", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			o := New(p, scriptShim(t, "  inspect) echo 'boom' >&2; exit 2 ;;\n"), "opossum", &out)
			o.applyChanges([]string{tc.changed})
			if got := strings.Count(out.String(), "warning: [OPSM-602] restart app skipped:"); got != map[bool]int{false: 0, true: 1}[tc.restart] {
				t.Errorf("restart warnings = %d, want restart=%v:\n%s", got, tc.restart, out.String())
			}
			if got := strings.Count(out.String(), "warning: [OPSM-603] sync of "+tc.changed+" "); got != 1 {
				t.Errorf("want the file synced (skipped) once, got %d:\n%s", got, out.String())
			}
		})
	}
}
