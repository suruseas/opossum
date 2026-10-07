package runtime

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// dockerProbeTimeout is how long DockerImageCreated waits for the docker CLI to answer: a Docker Desktop that is starting, or one that is not running at all,
// answers slowly or never, and `opossum up` asks only to say a word before a build, so it does not wait for what it can do without (#1905).
const dockerProbeTimeout = 3 * time.Second

// DockerProbe is what asking Docker about an image came to.
type DockerProbe int

const (
	// DockerProbeNothing: there is nothing to say — Docker has no such image, the docker CLI is not installed, the daemon is not there (the CLI says so at
	// once), the run is a dry-run, or what it printed is not a time that means anything.
	DockerProbeNothing DockerProbe = iota
	// DockerProbeFound: Docker holds the image, and the time returned is when it was built.
	DockerProbeFound
	// DockerProbeSlow: Docker did not answer in time, or the run was interrupted while it was asked. A caller that asks again does so at the same price, which
	// is not one to pay once for every image.
	DockerProbeSlow
)

// DockerImageCreated asks Docker, read-only, when the image ref was built there (`docker image inspect`: nothing is pulled, loaded, tagged or written, in Docker's
// store or the runtime's), and says what it came to: when the image was built (DockerProbeFound), that there is nothing to say, or that Docker was too slow
// (DockerProbeSlow). It never returns an error. A time at the start of 1970 is no time that means anything — a build made reproducible sets it — and is nothing
// to say. A caller that wants to say a word about an image Docker has says it only for DockerProbeFound.
func (r *Runtime) DockerImageCreated(ref string) (time.Time, DockerProbe) {
	if r.DryRun || ref == "" {
		return time.Time{}, DockerProbeNothing
	}
	docker := r.dockerBin()
	timeout := r.DockerProbeTimeout
	if timeout <= 0 {
		timeout = dockerProbeTimeout
	}
	ctx, cancel := context.WithTimeout(r.baseCtx(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, docker, "image", "inspect", "--format", "{{.Created}}", ref)
	// The context stops the docker CLI and not what it started, and a Wait that reads a pipe a grandchild still holds open does not return without a delay.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return time.Time{}, DockerProbeSlow
	}
	if err != nil {
		return time.Time{}, DockerProbeNothing
	}
	created, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(out)))
	if err != nil || created.Year() < 2000 {
		return time.Time{}, DockerProbeNothing
	}
	return created, DockerProbeFound
}
