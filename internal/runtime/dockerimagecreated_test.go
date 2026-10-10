package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func probeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// What the public docs and the changelog say is that `up` waits three seconds at most for Docker, and so the number is the default and not a number that
// something else decides.
func TestAskingDockerWaitsThreeSecondsByDefault(t *testing.T) {
	if dockerProbeTimeout != 3*time.Second {
		t.Errorf("the default wait for Docker is %v, and the docs say three seconds", dockerProbeTimeout)
	}
}

// A Docker that does not answer is not waited for longer than the timeout the Runtime was given, however it is slow: a docker that exec's the sleep, one that
// runs it as a child and holds the pipe open (the wait for the output would last as long as the child does), and one asked while the run is interrupted (Ctrl-C).
func TestAskingDockerIsBoundedByTheTimeoutAndByAnInterrupt(t *testing.T) {
	type row struct {
		name  string
		body  string
		setup func(rt *Runtime) (cancel func())
		under time.Duration
	}
	for _, tc := range []row{
		{"the docker sleeps in place of itself", "exec sleep 30\n", func(rt *Runtime) func() { rt.DockerProbeTimeout = 300 * time.Millisecond; return nil }, 10 * time.Second},
		{"the docker's child holds the pipe open", "sleep 8\necho x\n", func(rt *Runtime) func() { rt.DockerProbeTimeout = 300 * time.Millisecond; return nil }, 5 * time.Second},
		{"the run is interrupted while it is asked", "exec sleep 30\n", func(rt *Runtime) func() {
			ctx, cancel := context.WithCancel(context.Background())
			rt.Ctx = ctx
			rt.DockerProbeTimeout = 30 * time.Second
			time.AfterFunc(200*time.Millisecond, cancel)
			return cancel
		}, 10 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &Runtime{DockerBin: probeScript(t, tc.body)}
			if cancel := tc.setup(rt); cancel != nil {
				defer cancel()
			}
			start := time.Now()
			_, probe := rt.DockerImageCreated("pj-web:latest")
			if d := time.Since(start); d > tc.under {
				t.Errorf("asking took %v, which is not under %v", d, tc.under)
			}
			if probe != DockerProbeSlow {
				t.Errorf("a Docker that did not answer in time is DockerProbeSlow, got %v", probe)
			}
		})
	}
}

// What Docker answers is a time, or nothing to say, and a Docker that is slow is told apart from one that has no such image (a caller does not ask again of the
// first, and does of the second).
func TestAskingDockerSaysWhenItBuiltTheImageOrThatThereIsNothingToSay(t *testing.T) {
	ago := time.Now().Add(-90 * time.Minute).UTC().Format(time.RFC3339Nano)
	for _, tc := range []struct {
		name  string
		body  string
		probe DockerProbe
	}{
		{"it holds the image", "echo " + ago + "\n", DockerProbeFound},
		{"it holds no such image", "echo 'Error response from daemon: No such image' >&2\nexit 1\n", DockerProbeNothing},
		{"it prints a time and fails", "echo " + ago + "\nexit 1\n", DockerProbeNothing},
		{"it prints no time", "echo yesterday\n", DockerProbeNothing},
		{"it prints nothing", "exit 0\n", DockerProbeNothing},
		{"the image is dated at the start of 1970, as a reproducible build makes it", "echo 1970-01-01T00:00:00Z\n", DockerProbeNothing},
		{"the image is dated in 1999", "echo 1999-12-31T23:59:59Z\n", DockerProbeNothing},
		{"the image is dated in 2000", "echo 2000-01-01T00:00:00Z\n", DockerProbeFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &Runtime{DockerBin: probeScript(t, tc.body)}
			created, probe := rt.DockerImageCreated("pj-web:latest")
			if probe != tc.probe {
				t.Fatalf("probe %v, want %v", probe, tc.probe)
			}
			if probe == DockerProbeFound && (created.IsZero() || time.Since(created) < 0) {
				t.Errorf("the time Docker built it is %v", created)
			}
		})
	}
	dry := &Runtime{DockerBin: probeScript(t, "echo "+ago+"\n"), DryRun: true}
	if _, probe := dry.DockerImageCreated("pj-web:latest"); probe != DockerProbeNothing {
		t.Errorf("a dry-run asks nothing, got %v", probe)
	}
	if _, probe := (&Runtime{DockerBin: filepath.Join(t.TempDir(), "not-there")}).DockerImageCreated("x"); probe != DockerProbeNothing {
		t.Errorf("a docker that is not installed has nothing to say, got %v", probe)
	}
	if _, probe := (&Runtime{DockerBin: probeScript(t, "echo "+ago+"\n")}).DockerImageCreated(""); probe != DockerProbeNothing {
		t.Errorf("no name is asked about nothing")
	}
	_ = strings.TrimSpace
}
