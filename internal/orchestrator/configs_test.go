package orchestrator_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A config is placed in the container as a read-only bind mount at its
// target (#872): a `file:` config is the host file, a `content:` or
// `environment:` config is first written under the project's state
// directory (XDG_STATE_HOME/opossum/<project>/configs/<service>/<name>).
func TestUpPlacesConfigsAsReadOnlyMounts(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	rt, log := fakeShim(t)
	text := "inline content\n"
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "alpine:3.20", Configs: compose.ConfigRefs{
			{Source: "app_conf", Target: "/app_conf"},
			{Source: "app_conf", Target: "/etc/renamed.conf"},
			{Source: "inline", Target: "/inline"},
			{Source: "fromenv", Target: "/fromenv"},
		}},
	})
	// The file is written relative, as in a compose file: it resolves
	// against the project directory, not the working directory.
	p.Configs = map[string]compose.Config{
		"app_conf": {File: "./app.conf"},
		"inline":   {Content: &text},
		"fromenv":  {EnvVar: "CFG_ENV", EnvValue: "envvalue", EnvSet: true},
	}
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	host := filepath.Join(testBaseDir, "app.conf")
	if indexOf(log(), "-v "+host+":/app_conf:ro") < 0 || indexOf(log(), "-v "+host+":/etc/renamed.conf:ro") < 0 {
		t.Errorf("expected the file config resolved against the project dir and mounted read-only at both targets, got %v", log())
	}
	inline := filepath.Join(state, "opossum", "demo", "configs", "web", "inline")
	fromenv := filepath.Join(state, "opossum", "demo", "configs", "web", "fromenv")
	if indexOf(log(), "-v "+inline+":/inline:ro") < 0 || indexOf(log(), "-v "+fromenv+":/fromenv:ro") < 0 {
		t.Errorf("expected the written configs mounted from the state dir, got %v", log())
	}
	for path, want := range map[string]string{inline: text, fromenv: "envvalue"} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s should have been written: %v", path, err)
			continue
		}
		if string(b) != want {
			t.Errorf("%s = %q, want %q", path, b, want)
		}
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o444 {
			t.Errorf("%s mode = %v, want 0444 (read-only, as docker compose places it)", path, fi.Mode())
		}
	}

	// The next `up` finds a read-only file where it writes, and must still
	// put the new content there; a second reference to the same config in
	// one `up` writes it twice.
	text = "second run\n"
	p.Services["web"].Configs = append(p.Services["web"].Configs, compose.ConfigRef{Source: "inline", Target: "/etc/inline.conf"})
	o = orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("second Up: %v", err)
	}
	if b, err := os.ReadFile(inline); err != nil || string(b) != "second run\n" {
		t.Errorf("after the second up %s = %q (%v), want the new content", inline, b, err)
	}
	if indexOf(log(), "-v "+inline+":/etc/inline.conf:ro") < 0 {
		t.Errorf("expected the second reference mounted too, got %v", log())
	}
}

// Config names share one directory per service, and `x.tmp` is a name
// docker compose takes: writing `x` must not disturb `x.tmp`, whichever
// order the service lists them in.
// When the config's path cannot be renamed into (a directory sits there),
// the `up` is refused and the temporary file does not stay behind.
func TestAConfigThatCannotBeRenamedIntoPlaceLeavesNoTempFile(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	rt, _ := fakeShim(t)
	text := "blocked\n"
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "alpine:3.20", Configs: compose.ConfigRefs{{Source: "inline", Target: "/inline"}}},
	})
	p.Configs = map[string]compose.Config{"inline": {Content: &text}}
	dir := filepath.Join(state, "opossum", "demo", "configs", "web")
	if err := os.MkdirAll(filepath.Join(dir, "inline"), 0o755); err != nil {
		t.Fatal(err)
	}
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	err := o.Up(true)
	if err == nil || !strings.Contains(err.Error(), `writing config "inline"`) {
		t.Fatalf("want the write refused naming the config, got: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "inline" {
		t.Errorf("the temporary file must not stay behind, got %v", entries)
	}
}

func TestAConfigNamedLikeATempFileSurvivesItsNeighbour(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	rt, _ := fakeShim(t)
	a, b := "a-content\n", "b-content\n"
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "alpine:3.20", Configs: compose.ConfigRefs{
			{Source: "x.tmp", Target: "/x.tmp"},
			{Source: "x", Target: "/x"},
		}},
	})
	p.Configs = map[string]compose.Config{"x.tmp": {Content: &a}, "x": {Content: &b}}
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.Up(true); err != nil {
		t.Fatalf("Up: %v", err)
	}
	dir := filepath.Join(state, "opossum", "demo", "configs", "web")
	for name, want := range map[string]string{"x.tmp": a, "x": b} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != want {
			t.Errorf("config %q = %q (%v), want %q", name, got, err, want)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("the service's config dir should hold exactly its two configs, got %v", entries)
	}
}

// A one-off `run` places the service's configs as `up` does.
func TestRunOneOffPlacesConfigs(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	rt, log := fakeShim(t)
	text := "one-off\n"
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "alpine:3.20", Configs: compose.ConfigRefs{
			{Source: "app_conf", Target: "/app_conf"},
			{Source: "inline", Target: "/inline"},
		}},
	})
	p.Configs = map[string]compose.Config{"app_conf": {File: "./app.conf"}, "inline": {Content: &text}}
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	if err := o.RunOneOff("web", []string{"true"}, orchestrator.RunOneOffOptions{}); err != nil {
		t.Fatalf("RunOneOff: %v", err)
	}
	inline := filepath.Join(state, "opossum", "demo", "configs", "web", "inline")
	if indexOf(log(), "-v "+filepath.Join(testBaseDir, "app.conf")+":/app_conf:ro") < 0 || indexOf(log(), "-v "+inline+":/inline:ro") < 0 {
		t.Errorf("expected both configs mounted on the one-off run, got %v", log())
	}
	if b, err := os.ReadFile(inline); err != nil || string(b) != text {
		t.Errorf("the content config should be written for the one-off run: %q %v", b, err)
	}
}

// A dry run resolves the mounts but writes nothing.
func TestADryRunWritesNoConfigFile(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	rt, _ := fakeShim(t)
	text := "dry\n"
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "alpine:3.20", Configs: compose.ConfigRefs{{Source: "inline", Target: "/inline"}}},
	})
	p.Configs = map[string]compose.Config{"inline": {Content: &text}}
	var out bytes.Buffer
	o := orchestrator.New(p, rt, "opossum", &out)
	o.SetDryRun(true)
	if err := o.Up(true); err != nil {
		t.Fatalf("dry-run Up: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "opossum")); !os.IsNotExist(err) {
		t.Errorf("a dry run must write nothing under the state dir, got err=%v", err)
	}
	// The plan still shows where the config would be mounted from.
	inline := filepath.Join(state, "opossum", "demo", "configs", "web", "inline")
	if !strings.Contains(out.String(), "-v "+inline+":/inline:ro") {
		t.Errorf("the dry-run plan should show the config mount, got:\n%s", out.String())
	}
}

func TestUpRefusesAConfigWhoseVariableIsNotSet(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// The load marked the variable unset; the shell having it now must not
	// matter — the project's environment is what was read.
	t.Setenv("OPOSSUM_TEST_CFG_UNSET", "late")
	rt, log := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"web": {Image: "alpine:3.20", Configs: compose.ConfigRefs{{Source: "c", Target: "/c"}}},
	})
	p.Configs = map[string]compose.Config{"c": {EnvVar: "OPOSSUM_TEST_CFG_UNSET", EnvSet: false}}
	o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
	err := o.Up(true)
	if err == nil || !strings.Contains(err.Error(), `environment variable "OPOSSUM_TEST_CFG_UNSET" required by config "c" is not set`) {
		t.Errorf("want the unset variable named, got: %v", err)
	}
	if indexOf(log(), "run ") >= 0 {
		t.Errorf("nothing should have been started, got %v", log())
	}
}
