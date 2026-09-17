package orchestrator

import "github.com/suruseas/opossum/internal/runtime"

// DecodeStartErrorForTest is decodeStartError for the tests in this package's
// external test file: what a failed start is told to the reader as.
func (o *Orchestrator) DecodeStartErrorForTest(service string, err error) string {
	return o.decodeStartError(service, err).Error()
}

// RunErrorForTest is a failed run carrying the output that was captured from
// it — the runtime's and, for a foreground `up`, the container's own.
func RunErrorForTest(err error, stderr string) error {
	return &runtime.RunError{Err: err, Stderr: stderr}
}
