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
	if s := err.Error(); !strings.Contains(s, "opossum up web") {
		t.Errorf("start failure should point at `opossum up`, got: %s", s)
	}
}

func TestRestartHasNextStep(t *testing.T) {
	shim := scriptShim(t, "  start) exit 1 ;;\n") // stop succeeds, start fails
	err := New(lifecycleProject(), shim, "opossum", &bytes.Buffer{}).Restart(nil)
	if err == nil {
		t.Fatal("expected Restart to fail")
	}
	if s := err.Error(); !strings.Contains(s, "opossum up web") {
		t.Errorf("restart failure should point at `opossum up`, got: %s", s)
	}
}

func TestPullHasNextStep(t *testing.T) {
	shim := scriptShim(t, "  image) if [ \"$2\" = pull ]; then exit 1; fi ;;\n")
	err := New(lifecycleProject(), shim, "opossum", &bytes.Buffer{}).Pull(nil)
	if err == nil {
		t.Fatal("expected Pull to fail")
	}
	if s := err.Error(); !strings.Contains(s, "registry auth") || !strings.Contains(s, "web:latest") {
		t.Errorf("pull failure should name the image and hint at auth/network, got: %s", s)
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
}
