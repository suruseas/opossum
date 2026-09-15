package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

// The supervisor watches this project's services by container name, and after
// `up` that name can come to belong to another project (with `--dns-domain ""`,
// or a container made outside opossum): this project's container removed, the
// other one put in its place. Restarting it would start another project's
// container that was stopped on purpose — measured on container 1.4.1, the
// supervisor did. Such a container is left, said once, and not counted as this
// project's; so is one that carries no project label, made outside opossum.
// This project's own is handled as before.
func TestTheSupervisorLeavesAnotherProjectsContainer(t *testing.T) {
	shim := func(t *testing.T, state, project string) (*runtime.Runtime, func() string) {
		dir := t.TempDir()
		log := filepath.Join(dir, "calls.log")
		path := filepath.Join(dir, "c.sh")
		labels := "{}"
		if project != "" {
			labels = fmt.Sprintf(`{\"opossum.project\":\"%s\"}`, project)
		}
		body := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %s\ncase \"$1\" in\n"+
			"  inspect) echo \"[{\\\"status\\\":{\\\"state\\\":\\\"%s\\\"},\\\"configuration\\\":{\\\"labels\\\":%s}}]\" ;;\n"+
			"  system) echo 'status running' ;;\nesac\nexit 0\n", log, state, labels)
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return &runtime.Runtime{Bin: path}, func() string {
			b, _ := os.ReadFile(log)
			return string(b)
		}
	}
	for _, tc := range []struct {
		name, state, project string
		restarted, exists    bool
		said                 string
	}{
		{"this project's stopped container is restarted", "stopped", "demo", true, true, ""},
		{"an unlabeled stopped container is left", "stopped", "", false, false, `leaving "web" alone: its container web.demo.opossum carries no opossum.project label`},
		{"another project's stopped container is left", "stopped", "otherproj", false, false, `leaving "web" alone: its container web.demo.opossum belongs to project "otherproj"`},
		{"another project's running container is not this project's", "running", "otherproj", false, false, `leaving "web" alone: its container web.demo.opossum belongs to project "otherproj"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, calls := shim(t, tc.state, tc.project)
			o := New(&compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"web": {Name: "web", Image: "w", Restart: "always"},
			}}, rt, "opossum", os.Stderr)
			pols := map[string]compose.RestartPolicy{"web": pol(t, "always")}
			state := map[string]serviceState{}
			var said []string
			logf := func(f string, a ...interface{}) { said = append(said, fmt.Sprintf(f, a...)) }
			exists, _ := o.superviseAt(time.Now(), pols, state, logf)
			// A second poll: the leaving is said once, not on every poll.
			o.superviseAt(time.Now().Add(time.Minute), pols, state, logf)
			if got := strings.Contains(calls(), "start web.demo.opossum"); got != tc.restarted {
				t.Errorf("restarted = %v, want %v; calls:\n%s", got, tc.restarted, calls())
			}
			if exists != tc.exists {
				t.Errorf("exists = %v, want %v (another project's container does not keep this project's watch going)", exists, tc.exists)
			}
			if tc.said != "" {
				n := 0
				for _, l := range said {
					if strings.Contains(l, tc.said) {
						n++
					}
				}
				if n != 1 {
					t.Errorf("want %q said once over two polls, got %d in %q", tc.said, n, said)
				}
			}
		})
	}
}

// When the name goes back to this project's container and then to another
// project's again, the second time is said too: "said once" is per time the
// name changes hands, not once for the life of the watch.
func TestTheSupervisorSaysEachTimeTheNameIsAnotherProjects(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	owner := filepath.Join(dir, "owner")
	path := filepath.Join(dir, "c.sh")
	body := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n"+
		"  inspect) p=$(cat %s); if [ \"$p\" = gone ]; then echo \"Error: container not found\" >&2; exit 1; fi; if [ \"$p\" = unknown ]; then echo \"Error: apiserver is not running\" >&2; exit 1; fi; echo \"[{\\\"status\\\":{\\\"state\\\":\\\"running\\\"},\\\"configuration\\\":{\\\"labels\\\":{\\\"opossum.project\\\":\\\"$p\\\"}}}]\" ;;\n"+
		"  system) echo 'status running' ;;\nesac\nexit 0\n", owner)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	o := New(&compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"web": {Name: "web", Image: "w", Restart: "always"},
	}}, &runtime.Runtime{Bin: path}, "opossum", os.Stderr)
	pols := map[string]compose.RestartPolicy{"web": pol(t, "always")}
	state := map[string]serviceState{}
	var said []string
	logf := func(f string, a ...interface{}) { said = append(said, fmt.Sprintf(f, a...)) }
	now := time.Now()
	// Back to this project's, and gone: either way the next time the name is
	// another project's is a new time.
	// An unanswered inspect in between says nothing about the owner, so it is
	// not a new time. A container with no project label is not this project's
	// either: it is said once when the name comes to it, and the project after
	// it is a new time again.
	for i, p := range []string{"otherproj", "otherproj", "demo", "otherproj", "gone", "otherproj", "thirdproj", "thirdproj", "unknown", "thirdproj", "", "", "thirdproj"} {
		if err := os.WriteFile(owner, []byte(p), 0o644); err != nil {
			t.Fatal(err)
		}
		o.superviseAt(now.Add(time.Duration(i)*time.Minute), pols, state, logf)
	}
	n := 0
	for _, l := range said {
		if strings.Contains(l, `leaving "web" alone`) {
			n++
		}
	}
	if n != 6 {
		t.Errorf("want it said each time the name changed hands — back from this project's, back from gone, on to a different project, on to no label once, back from no label, and not after an unanswered poll — six times over these polls, got %d in %q", n, said)
	}
	if last := said[len(said)-1]; !strings.Contains(last, `belongs to project "thirdproj"`) {
		t.Errorf("the last word should name the project the name is on now, got %q", last)
	}
}

// The owner and the state come from one inspect per service per poll: a
// second inspect could find the name handed to another project after the first
// said it was this project's, and the supervisor would start that one.
func TestTheSupervisorReadsTheOwnerFromTheSameInspect(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, owner := range []string{"demo", "otherproj"} {
		t.Run(owner, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "calls.log")
			path := filepath.Join(dir, "c.sh")
			body := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %s\ncase \"$1\" in\n"+
				"  inspect) echo \"[{\\\"status\\\":{\\\"state\\\":\\\"stopped\\\"},\\\"configuration\\\":{\\\"labels\\\":{\\\"opossum.project\\\":\\\"%s\\\"}}}]\" ;;\n"+
				"  system) echo 'status running' ;;\nesac\nexit 0\n", log, owner)
			if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
			o := New(&compose.Project{Name: "demo", Services: map[string]*compose.Service{
				"web": {Name: "web", Image: "w", Restart: "always"},
			}}, &runtime.Runtime{Bin: path}, "opossum", os.Stderr)
			o.superviseAt(time.Now(), map[string]compose.RestartPolicy{"web": pol(t, "always")}, map[string]serviceState{}, func(string, ...interface{}) {})
			b, _ := os.ReadFile(log)
			if n := strings.Count(string(b), "inspect "); n != 1 {
				t.Errorf("want one inspect for the poll, got %d:\n%s", n, b)
			}
		})
	}
}

// When the watch stops for having nothing to watch, it does not say the
// containers do not exist: another project's may hold the names.
func TestTheSupervisorsLastWordDoesNotSayTheNamesAreFree(t *testing.T) {
	var w watch
	var line string
	var stop bool
	for i := 0; i < idlePollsBeforeExit; i++ {
		line, stop = w.after(false, nil, func() bool { return true }, []string{"web"})
	}
	if !stop {
		t.Fatalf("the watch should stop after %d polls with nothing of this project's, got %q", idlePollsBeforeExit, line)
	}
	if strings.Contains(line, "exists") || !strings.Contains(line, "none of [web] has had a container of this project's") {
		t.Errorf("the last word should say none is this project's, not that none exists, got %q", line)
	}
}
