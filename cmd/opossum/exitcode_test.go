package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// `run` and `exec` exit with the container's own code, as docker compose does
// (measured on v5.5.0: `run --rm app sh -c 'exit 3'` is 3, `exec` of `exit 5`
// is 5, a one-off killed from outside is 137), so a script can tell how the
// command failed. Everything that fails before the attached command returns is
// opossum's and exits 1 — docker compose exits 1 for an unknown service and an
// image it cannot pull too. Both halves are in one table: with only the first,
// a CLI that exited with whatever code the last runtime command had would pass;
// with only the second, one that never passed a code through would.
//
// The rows that must stay 1 are the ones where a runtime command under opossum
// did exit with a code of its own, just not the attached one: a dependency's
// run-to-completion, a dependency that would not start, a runtime process a
// signal killed. This drives the built binary, so the code is the process's.
func TestRunAndExecExitWithTheContainersCode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(`name: ec
services:
  web:
    image: alpine
  app:
    image: alpine
    depends_on:
      migrate: {condition: service_completed_successfully}
  migrate:
    image: alpine
  api:
    image: alpine
    depends_on: [db]
  db:
    image: alpine
  built:
    build: .
`), 0o644); err != nil {
		t.Fatal(err)
	}
	audit := func(args ...string) []string {
		return append([]string{"run", "--audit", "--audit-format", "json"}, args...)
	}
	for _, tc := range []struct {
		name string
		args []string
		env  []string
		want int
		// For an audited run, the exit code its JSON report shows; the report
		// and the process agree on every code the one-off has, and a run
		// without one (-1) exits 1.
		report *int
	}{
		{"a one-off that exits 3", []string{"run", "--rm", "web", "sh", "-c", "exit 3"}, []string{"FAKE_EXIT=--name web-run.=3"}, 3, nil},
		{"a one-off that exits 255", []string{"run", "web", "sh", "-c", "exit 255"}, []string{"FAKE_EXIT=--name web-run.=255"}, 255, nil},
		{"a one-off killed from outside", []string{"run", "--rm", "web", "sleep", "100"}, []string{"FAKE_EXIT=--name web-run.=137"}, 137, nil},
		{"a one-off that exits 1", []string{"run", "--rm", "web", "false"}, []string{"FAKE_EXIT=--name web-run.=1"}, 1, nil},
		{"a one-off that succeeds", []string{"run", "--rm", "web", "true"}, nil, 0, nil},
		{"an exec that exits 7", []string{"exec", "web", "sh", "-c", "exit 7"}, []string{"FAKE_EXIT=exec web.=7"}, 7, nil},
		{"an exec that succeeds", []string{"exec", "web", "true"}, nil, 0, nil},
		{"an audited one-off that exits 3", audit("--rm", "web", "sh", "-c", "exit 3"), []string{"FAKE_EXIT=--name web-run.=3"}, 3, code(3)},
		{"an audited one-off that exits 42", audit("--rm", "web", "sh", "-c", "exit 42"), []string{"FAKE_EXIT=--name web-run.=42"}, 42, code(42)},
		// 1 is the code a failing command most often has, and the one opossum's
		// own failures exit with: only the report tells the two apart.
		{"an audited one-off that exits 1", audit("--rm", "web", "false"), []string{"FAKE_EXIT=--name web-run.=1"}, 1, code(1)},
		{"an audited one-off that succeeds", audit("--rm", "web", "true"), nil, 0, code(0)},
		{"an audited one-off whose runtime process a signal kills", audit("--rm", "web", "true"), []string{"FAKE_DIE_SIGNAL=--name web-run."}, 1, code(-1)},
		// A build before the one-off runs is not the one-off's result: the report
		// says -1, not the build's code.
		{"an audited one-off whose build exits 2", audit("--rm", "built", "true"), []string{"FAKE_EXIT=build=2"}, 1, code(-1)},
		// opossum's own failures.
		{"a run-to-completion dependency that exits 3", []string{"run", "--rm", "app", "true"}, []string{"FAKE_EXIT=--name migrate.=3"}, 1, nil},
		{"a dependency whose start exits 5", []string{"run", "--rm", "api", "true"}, []string{"FAKE_EXIT=--name db.=5"}, 1, nil},
		{"a one-off whose runtime process a signal kills", []string{"run", "--rm", "web", "true"}, []string{"FAKE_DIE_SIGNAL=--name web-run."}, 1, nil},
		{"an exec whose runtime process a signal kills", []string{"exec", "web", "true"}, []string{"FAKE_DIE_SIGNAL=exec web."}, 1, nil},
		{"a one-off of a service that is not there", []string{"run", "--rm", "nosuch", "true"}, []string{"FAKE_EXIT=run=3"}, 1, nil},
		{"an exec into a service that is not there", []string{"exec", "nosuch", "true"}, []string{"FAKE_EXIT=exec=7"}, 1, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "invocations.log")
			cmd := exec.Command(opossumBin, tc.args...)
			cmd.Dir = dir
			cmd.Env = append(append(os.Environ(),
				"OPOSSUM_CONTAINER_BIN="+fakeShimBin,
				"INSPECT_PROJECT_FROM_NAME=1", // the containers opossum made carry their project's label
				"FAKE_LOG="+logPath,
				"XDG_STATE_HOME="+t.TempDir(),
				"OPOSSUM_CRASH_GRACE=0"), tc.env...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			got := 0
			var ee *exec.ExitError
			switch {
			case errors.As(err, &ee):
				got = ee.ExitCode()
			case err != nil:
				t.Fatalf("opossum %s did not run: %v", strings.Join(tc.args, " "), err)
			}
			log, _ := os.ReadFile(logPath)
			if got != tc.want {
				t.Fatalf("opossum %s exited %d, want %d\nstderr:\n%s\nruntime calls:\n%s", strings.Join(tc.args, " "), got, tc.want, stderr.String(), log)
			}
			// The failure is still said, whichever code it exits with.
			if tc.want != 0 && !strings.Contains(stderr.String(), "opossum: ") {
				t.Errorf("a failure should still be said on stderr, got:\n%s", stderr.String())
			}
			if tc.report != nil {
				var report struct {
					ExitCode int `json:"exitCode"`
				}
				if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
					t.Fatalf("under --audit, stdout should be the JSON report: %v\n%s", err, stdout.String())
				}
				if report.ExitCode != *tc.report {
					t.Errorf("the report says exit %d, want %d (opossum exited %d)", report.ExitCode, *tc.report, got)
				}
			}
		})
	}
}

func code(n int) *int { return &n }
