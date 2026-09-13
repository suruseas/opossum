package orchestrator

import (
	"errors"
	"os/exec"
)

// ContainerExitError is the non-zero exit code of the runtime command `run` or
// `exec` attached to. That is the container command's own code — `container
// run` and `container exec` return 3 for a command that exits 3, and 137 for
// one killed from outside (measured on container 1.4.1) — or, when the runtime
// command itself fails, the runtime's (1 for an image it cannot pull, 64 for a
// usage error). The CLI exits with Code, as docker compose does for the
// container's, so a script can tell how the command failed and not only that it
// did.
//
// It is made only where the attached command returns. A failure before that —
// an unknown service, a dependency that would not start, a build — is
// opossum's, and keeps exiting 1 however the runtime command under it exited.
type ContainerExitError struct {
	Code int
	Err  error
}

func (e *ContainerExitError) Error() string { return e.Err.Error() }
func (e *ContainerExitError) Unwrap() error { return e.Err }

// attachedExit marks err as the attached command's exit when the runtime
// process exited with a code of its own. A process a signal killed has no code
// (ExitCode is -1), and neither does a runtime that could not be started; both
// are left as they are.
func attachedExit(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() > 0 {
		return &ContainerExitError{Code: ee.ExitCode(), Err: err}
	}
	return err
}
