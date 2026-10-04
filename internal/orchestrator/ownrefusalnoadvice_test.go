package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A service's own `entrypoint:` that names no program is refused by opossum before the runtime is
// called (#1651). Nothing failed in the runtime, so the failed-start advice does not apply: it
// said "there is no container left to read logs from — the failure above is what there is;
// verify the image, command, and mounts", and the runtime had said nothing above and the file's
// mistake is not in the image, the command or the mounts (#1703). The refusal reads as one line.
//
// The same on the other path a service is started by, a run-to-completion dependency
// (`service_completed_successfully`): it did not exit non-zero, and there is no output above.
func TestAnEntrypointRefusalOfTheFileCarriesNoStartFailureAdvice(t *testing.T) {
	const (
		noneLeft = "there is no container left to read logs from"
		verify   = "verify the image, command, and mounts"
		logs     = "check why with"
	)
	for _, svc := range []struct {
		name string
		s    compose.Service
		want string
	}{
		{"an empty first word and a program after it", compose.Service{Image: "alpine:3", Entrypoint: compose.Command{"", "sh"}}, "starts with an empty word"},
		{"one empty word and no command", compose.Service{Image: "alpine:3", Entrypoint: compose.Command{""}}, "has no `command:` to run instead"},
		{"cleared, with no command and an image that names none", compose.Service{Image: "alpine:3", EntrypointCleared: true}, "names a command to run instead"},
		{"cleared, with a command whose first word is empty", compose.Service{Image: "alpine:3", EntrypointCleared: true, Command: compose.Command{"", "x"}}, "that word is empty"},
	} {
		for _, mode := range []struct {
			name   string
			detach bool
			dry    bool
		}{
			{"detached", true, false},
			{"foreground", false, false},
			{"dry run", true, true},
		} {
			t.Run(svc.name+"/"+mode.name, func(t *testing.T) {
				t.Setenv("XDG_STATE_HOME", t.TempDir())
				rt, _ := fakeShim(t)
				s := svc.s
				o := orchestrator.New(project("demo", map[string]*compose.Service{"s": &s}), rt, "opossum", &bytes.Buffer{})
				o.SetDryRun(mode.dry)
				err := o.Up(mode.detach)
				if err == nil {
					t.Fatal("the entrypoint names no program, so the up must refuse it")
				}
				msg := err.Error()
				if !strings.Contains(msg, svc.want) {
					t.Errorf("the refusal should say %q, got: %v", svc.want, err)
				}
				if !strings.Contains(msg, `starting service "s"`) {
					t.Errorf("the refusal should still name the service it was starting, got: %v", err)
				}
				for _, advice := range []string{noneLeft, verify, logs} {
					if strings.Contains(msg, advice) {
						t.Errorf("a refusal made before the runtime was called carries the start-failure advice %q: %v", advice, err)
					}
				}
				if strings.Contains(msg, "\n") {
					t.Errorf("the refusal should be one line, got: %q", msg)
				}
			})
		}
	}
}

func TestAnEntrypointRefusalOfARunToCompletionDependencyCarriesNoExitedAdvice(t *testing.T) {
	for _, mode := range []struct {
		name   string
		detach bool
	}{{"detached", true}, {"foreground", false}} {
		for _, svc := range []struct {
			name string
			s    compose.Service
			want string
		}{
			{"an empty first word and a program after it", compose.Service{Image: "alpine:3", Entrypoint: compose.Command{"", "sh"}}, "starts with an empty word"},
			{"cleared, with no command and an image that names none", compose.Service{Image: "alpine:3", EntrypointCleared: true}, "names a command to run instead"},
		} {
			t.Run(svc.name+"/"+mode.name, func(t *testing.T) {
				t.Setenv("XDG_STATE_HOME", t.TempDir())
				rt, _ := fakeShim(t)
				job := svc.s
				web := compose.Service{Image: "alpine:3", DependsOn: compose.DependsOn{{Name: "job", Condition: "service_completed_successfully"}}}
				o := orchestrator.New(project("demo", map[string]*compose.Service{"job": &job, "web": &web}), rt, "opossum", &bytes.Buffer{})
				err := o.Up(mode.detach)
				if err == nil {
					t.Fatal("the job's entrypoint names no program, so the up must refuse it")
				}
				msg := err.Error()
				if !strings.Contains(msg, svc.want) || !strings.Contains(msg, `starting service "job"`) {
					t.Errorf("the refusal should say %q and name the job, got: %v", svc.want, err)
				}
				for _, advice := range []string{"exited non-zero", "check its output above", "opossum run job", "did not complete successfully"} {
					if strings.Contains(msg, advice) {
						t.Errorf("a refusal made before the job ran carries the exit advice %q: %v", advice, err)
					}
				}
				if strings.Contains(msg, "\n") {
					t.Errorf("the refusal should be one line, got: %q", msg)
				}
			})
		}
	}
}
