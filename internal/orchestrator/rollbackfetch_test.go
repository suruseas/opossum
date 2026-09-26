package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/orchestrator"
)

// The rollback line says what became of the containers this `up` made. A service
// whose image the registry would not hand over never had a container: the name
// goes on the rollback list before the run, in case the run made one before
// failing, and the runtime then confirms there is none. Naming it as "stopped and
// removed" says something was made and torn down, which is not what happened.
//
// Measured (Apple `container` 1.4.1, an image that is not there): the run fails,
// `container ls -a` has nothing of the name, and the output read
// `Rolled back web — stopped and removed; nothing this `up` started is left running`.
// The second half of that line was true and is kept; the first is what changes.
//
// The rows are what is named, not whether a line appears: a second service that
// WAS created before the failure is rolled back and is named, a service that was
// created and then failed is too, and so is one whose previous container this up
// removed to replace it (an image changed to one the registry does not have) —
// the old container is gone, and saying nothing about it would leave a running
// service vanishing without a word. Those rows are what tell "never made" from
// "made and removed"; a check on the line alone passes with the name dropped
// everywhere.
//
// INSPECT_ABSENT makes the shim report the named containers as not there. That is
// what the runtime says of a service with no earlier container (asked before this
// up removes one), and again after a rollback that removed it: the rollback line
// asks the runtime whether each container is gone. It says nothing of whether the
// rollback tried to remove it (rollbacksays_test.go holds that). The rows where the
// container exists (one the previous up made, and a run-to-completion dependency
// that was made and failed) leave it out, so the shim answers from what is there.
func TestARollbackDoesNotNameAServiceThatWasNeverMade(t *testing.T) {
	const bad = "docker.io/nosuchorg-inu1102/nosuchimage:latest"
	const reason = "401 Unauthorized. Reason: Unknown, no credentials found for host registry-1.docker.io"
	const url = "https://registry-1.docker.io/v2/nosuchorg-inu1102/nosuchimage/manifests/latest"
	for _, tc := range []struct {
		name string
		file string
		env  []string
		// previous is a file brought up first, successfully, so that the row starts
		// from a container the previous up made.
		previous string
		// wantLine is the rollback report, read back from the output; wantNot are
		// service names the report must not carry.
		wantLine string
		wantNot  []string
		// wantErr and wantErrNot are read from the error the up returns: what it
		// points the reader at, and what it must not say about a run that never
		// happened.
		wantErr, wantErrNot string
	}{
		{
			name: "the only service cannot get its image",
			file: "services:\n  web:\n    image: " + bad + "\n",
			env: []string{"RUN_IMAGE_FETCH_FAIL=" + bad, "RUN_IMAGE_FETCH_REASON=" + reason, "RUN_IMAGE_FETCH_URL=" + url,
				"INSPECT_ABSENT=web.demo.opossum"},
			wantLine: "Nothing this `up` started is left running",
			wantNot:  []string{"web"},
		},
		{
			name: "a service made before it is rolled back and named, this one is not",
			// zside sorts after web: it is first only because web depends on it, so the
			// row is about a service made BEFORE the failure and not one that happens
			// to come first by name.
			file: "services:\n  zside:\n    image: alpine:3\n  web:\n    image: " + bad + "\n    depends_on: [zside]\n",
			env: []string{"RUN_IMAGE_FETCH_FAIL=" + bad, "RUN_IMAGE_FETCH_REASON=" + reason, "RUN_IMAGE_FETCH_URL=" + url,
				"INSPECT_ABSENT=zside.demo.opossum web.demo.opossum"},
			wantLine: "Rolled back zside — stopped and removed; nothing this `up` started is left running",
			wantNot:  []string{"web"},
		},
		{
			// Made, then failed: the container existed, and is named. The image was
			// fetched, so nothing above applies.
			name:     "a service that was made and then failed is named",
			file:     "services:\n  web:\n    image: alpine:3\n",
			env:      []string{"RUN_FAIL=web.demo.opossum", "INSPECT_ABSENT=web.demo.opossum"},
			wantLine: "Rolled back web — stopped and removed; nothing this `up` started is left running",
		},
		{
			// Made by the previous `up`, replaced by this one: the old container is
			// removed before the run, the run cannot make a new one, and what the
			// reader is owed is that the service that was running is gone.
			name:     "a service the previous up made is replaced by one that cannot be made",
			file:     "services:\n  web:\n    image: " + bad + "\n",
			env:      []string{"RUN_IMAGE_FETCH_FAIL=" + bad, "RUN_IMAGE_FETCH_REASON=" + reason, "RUN_IMAGE_FETCH_URL=" + url},
			previous: "services:\n  web:\n    image: alpine:3\n",
			wantLine: "Rolled back web — stopped and removed; nothing this `up` started is left running",
		},
		{
			// A run-to-completion dependency whose image cannot be had: the second
			// branch that ends a service in `createdSvc` without a container. The
			// error said the service "exited non-zero — check its output above",
			// and the rollback said it was stopped and removed; neither happened.
			name: "a run-to-completion dependency that cannot get its image is not named",
			file: "services:\n  init:\n    image: " + bad + "\n  web:\n    image: alpine:3\n    depends_on:\n      init:\n        condition: service_completed_successfully\n",
			env: []string{"RUN_IMAGE_FETCH_FAIL=" + bad, "RUN_IMAGE_FETCH_REASON=" + reason, "RUN_IMAGE_FETCH_URL=" + url,
				"INSPECT_ABSENT=init.demo.opossum web.demo.opossum"},
			wantLine:   "Nothing this `up` started is left running",
			wantNot:    []string{"init"},
			wantErr:    "check the image name",
			wantErrNot: "exited non-zero",
		},
		{
			// The same dependency, made and then failed: a container exists, and it
			// is named. Held so a change cannot pass by dropping every one-shot.
			name:     "a run-to-completion dependency that was made and then failed is named",
			file:     "services:\n  init:\n    image: alpine:3\n  web:\n    image: alpine:3\n    depends_on:\n      init:\n        condition: service_completed_successfully\n",
			env:      []string{"RUN_FAIL=init.demo.opossum", "INSPECT_ABSENT=init.demo.opossum web.demo.opossum"},
			wantLine: "Rolled back init — stopped and removed; nothing this `up` started is left running",
			wantErr:  "exited non-zero",
		},
		{
			// A one-shot dependency that printed the registry's line itself and then
			// failed: its container is there, so the runtime's answer is what keeps
			// this from being read as an image failure. INSPECT_ABSENT does not
			// name it — the shim answers that the container exists, as the runtime
			// does for one that was made.
			name: "a run-to-completion dependency that printed a registry line and was made is named",
			file: "services:\n  init:\n    image: alpine:3\n  web:\n    image: alpine:3\n    depends_on:\n      init:\n        condition: service_completed_successfully\n",
			env: []string{"RUN_FAIL=init.demo.opossum",
				"RUN_FAIL_STDERR=Error: HTTP request to https://registry-1.docker.io/v2/x/manifests/latest failed with response: 404 Not Found. Reason: Unknown",
				"INSPECT_ABSENT=web.demo.opossum"},
			wantLine:   "Rolled back init — stopped and removed; nothing this `up` started is left running",
			wantErr:    "exited non-zero",
			wantErrNot: "check the image name",
		},
		{
			// The previous up made init's container; this up removes it to replace it
			// and the new image cannot be had: the old one is gone, and is named.
			name:     "a run-to-completion dependency that replaced a container it cannot remake is named",
			file:     "services:\n  init:\n    image: " + bad + "\n  web:\n    image: alpine:3\n    depends_on:\n      init:\n        condition: service_completed_successfully\n",
			env:      []string{"RUN_IMAGE_FETCH_FAIL=" + bad, "RUN_IMAGE_FETCH_REASON=" + reason, "RUN_IMAGE_FETCH_URL=" + url},
			previous: "services:\n  init:\n    image: alpine:3\n  web:\n    image: alpine:3\n    depends_on:\n      init:\n        condition: service_completed_successfully\n",
			wantLine: "Rolled back init — stopped and removed; nothing this `up` started is left running",
			wantErr:  "check the image name",
		},
		{
			// `required: false`: the up goes on past the dependency, and a later
			// failure rolls back what was made. The dependency was never made, so it
			// is not among them.
			name: "an optional run-to-completion dependency that cannot get its image is not named when a later service fails",
			file: "services:\n  init:\n    image: " + bad + "\n  web:\n    image: alpine:3\n    depends_on:\n      init:\n        condition: service_completed_successfully\n        required: false\n",
			env: []string{"RUN_IMAGE_FETCH_FAIL=" + bad, "RUN_IMAGE_FETCH_REASON=" + reason, "RUN_IMAGE_FETCH_URL=" + url,
				"RUN_FAIL=web.demo.opossum", "INSPECT_ABSENT=init.demo.opossum web.demo.opossum"},
			wantLine: "Rolled back web — stopped and removed; nothing this `up` started is left running",
			wantNot:  []string{"init"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, _ := fakeShim(t)
			if tc.previous != "" {
				before, err := loadProject(t, tc.previous)
				if err != nil {
					t.Fatal(err)
				}
				if err := orchestrator.New(before, rt, "opossum", &bytes.Buffer{}).Up(true); err != nil {
					t.Fatalf("the previous up, which makes the container: %v", err)
				}
			}
			setShimEnv(rt, tc.env...)
			proj, err := loadProject(t, tc.file)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			o := orchestrator.New(proj, rt, "opossum", &out)
			err = o.Up(true)
			if err == nil {
				t.Fatal("want the up refused")
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("the error does not say %q: %v", tc.wantErr, err)
			}
			if tc.wantErrNot != "" && strings.Contains(err.Error(), tc.wantErrNot) {
				t.Errorf("the error says %q, which is not what happened: %v", tc.wantErrNot, err)
			}
			var report string
			for _, l := range strings.Split(out.String(), "\n") {
				if strings.HasPrefix(l, "Rolled back ") || strings.HasPrefix(l, "Nothing this `up` started") {
					report = l
				}
			}
			if report != tc.wantLine {
				t.Errorf("the rollback reported %q, want %q\nwhole output:\n%s", report, tc.wantLine, out.String())
			}
			for _, name := range tc.wantNot {
				if strings.Contains(report, name) {
					t.Errorf("the rollback names %q, which was never made: %q", name, report)
				}
			}
		})
	}
}
