package runtime

import (
	"fmt"
	"os"
	"os/exec"
)

// ImportFromDocker copies an image out of Docker's image store into container's
// store. Images are OCI-standard, but the two runtimes keep separate stores, so
// this streams `docker image save <dockerRef>` into `container image load`. If
// targetTag differs from dockerRef (e.g. a service builds to a custom `image:`
// name but opossum expects `<project>-<service>:latest`), the loaded image is
// retagged so a later `up` finds it present and skips the build.
//
// docker is invoked ONLY here: opossum's normal path never shells out to docker,
// and this reports a clear message when the CLI is missing.
func (r *Runtime) ImportFromDocker(dockerRef, targetTag string) error {
	docker := r.dockerBin()
	if _, err := exec.LookPath(docker); err != nil {
		return fmt.Errorf("the docker CLI isn't installed, which is needed to export %q from "+
			"Docker (alternatively, push the image to a registry and let opossum pull it)", dockerRef)
	}

	// Use an explicit pipe (not StdoutPipe) so the parent can close both ends
	// after starting the children: then if `load` dies, `save` sees EPIPE instead
	// of blocking forever on a full pipe buffer. Both are tied to the cancellation
	// context so Ctrl-C stops the whole import.
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	save := exec.CommandContext(r.baseCtx(), docker, "image", "save", dockerRef)
	load := exec.CommandContext(r.baseCtx(), r.Bin, "image", "load")
	save.Stdout = pw
	save.Stderr = os.Stderr // surface "No such image" / "Cannot connect to the Docker daemon"
	load.Stdin = pr
	load.Stdout = os.Stdout
	load.Stderr = os.Stderr

	if err := load.Start(); err != nil {
		pr.Close()
		pw.Close()
		return fmt.Errorf("container image load: %w", err)
	}
	if err := save.Start(); err != nil {
		pr.Close()
		pw.Close()
		load.Wait()
		return fmt.Errorf("docker image save: %w", err)
	}
	// The children hold their own dups; the parent must drop its ends so EOF/EPIPE
	// propagate when either side exits.
	pw.Close()
	pr.Close()

	saveErr := save.Wait()
	loadErr := load.Wait()
	// Prefer the load error: a broken-pipe save error is usually a symptom of load
	// having failed first.
	if loadErr != nil {
		return fmt.Errorf("loading %q into container failed: %w", dockerRef, loadErr)
	}
	if saveErr != nil {
		return fmt.Errorf("exporting %q from Docker failed — is it built and is Docker running? "+
			"(build it with `docker compose build`): %w", dockerRef, saveErr)
	}

	if targetTag != "" && targetTag != dockerRef {
		if _, err := r.capture("image", "tag", dockerRef, targetTag); err != nil {
			return fmt.Errorf("tagging %q as %q: %w", dockerRef, targetTag, err)
		}
	}
	return nil
}
