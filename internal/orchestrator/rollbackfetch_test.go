package orchestrator_test

import (
	"bytes"
	"path/filepath"
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
// container exists (one the previous up made) leave it out, so the shim answers
// from what is there. A container the failed run itself made is
// INSPECT_ABSENT_BEFORE_RUN: not there when this up looks before the run, there
// after it — which is what tells "the run made nothing" from "the run made it and
// failed", the two the rollback line must tell apart. Not left out: the shim
// answers "there" by default, and the row would then be one whose previous
// container this up replaced.
func TestARollbackDoesNotNameAServiceThatWasNeverMade(t *testing.T) {
	const bad = "docker.io/nosuchorg-inu1102/nosuchimage:latest"
	const reason = "401 Unauthorized. Reason: Unknown, no credentials found for host registry-1.docker.io"
	const url = "https://registry-1.docker.io/v2/nosuchorg-inu1102/nosuchimage/manifests/latest"
	// The file the failed run creates to stop the runtime answering (see the shim).
	// One per row that uses it: the file outlasts the row that made it.
	unaskable := filepath.Join(t.TempDir(), "unaskable")
	unaskableDep := filepath.Join(t.TempDir(), "unaskable-dep")
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
			env:      []string{"RUN_FAIL=web.demo.opossum", "INSPECT_ABSENT_BEFORE_RUN=web.demo.opossum"},
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
			// Refused before a container was made, in a way that is not the registry's:
			// container 1.4.1 says an image has no build for this machine and lists
			// nothing afterwards (measured). The rollback has nothing to name, and
			// the failure has no logs to point at.
			name: "an image with no build for this machine is not named",
			file: "services:\n  web:\n    image: alpine:3\n",
			env: []string{"RUN_FAIL=web.demo.opossum",
				"RUN_FAIL_STDERR=Error: unsupported platform Platform(osVersion: nil, _rawArch: \"arm64\")\n",
				"INSPECT_ABSENT=web.demo.opossum"},
			wantLine: "Nothing this `up` started is left running",
			wantNot:  []string{"web"},
			// The failure is diagnosed, and the diagnosis does not send anyone to logs.
			wantErr:    "[OPSM-412]",
			wantErrNot: "opossum logs",
		},
		{
			// Refused before a container was made, in words no diagnosis names: the
			// generic start failure, which must not point at the logs of a container
			// that is not there.
			name: "a refusal no diagnosis names, before a container was made, does not point at logs",
			file: "services:\n  web:\n    image: alpine:3\n",
			env: []string{"RUN_FAIL=web.demo.opossum",
				"RUN_FAIL_STDERR=Error: the runtime refused this run in words no diagnosis names\n",
				"INSPECT_ABSENT=web.demo.opossum"},
			wantLine:   "Nothing this `up` started is left running",
			wantNot:    []string{"web"},
			wantErr:    "no container left to read logs from",
			wantErrNot: "opossum logs",
		},
		{
			// A named volume the runtime refuses to attach (`[OPSM-103]`): nothing is
			// listed afterwards (measured on 1.4.1, the volume held by another running
			// container). No holder is set up here — the row is about what the
			// rollback names, and the message is the same with or without one.
			name: "a named volume the runtime refuses to attach is not named",
			file: "services:\n  web:\n    image: alpine:3\n    volumes: [data:/d]\nvolumes:\n  data: {}\n",
			env: []string{"RUN_FAIL=web.demo.opossum",
				"RUN_FAIL_STDERR=Error Domain=VZErrorDomain Code=2 \"The storage device attachment is invalid.\"\n",
				"INSPECT_ABSENT=web.demo.opossum"},
			wantLine: "Nothing this `up` started is left running",
			wantNot:  []string{"web"},
		},
		{
			// The same refusal, but the previous up's container was there and this up
			// removed it to replace it: it is gone, and is named.
			name:     "an image with no build for this machine replaces a container and is named",
			file:     "services:\n  web:\n    image: alpine:3\n",
			env:      []string{"RUN_FAIL=web.demo.opossum", "RUN_FAIL_MAKES_NOTHING=1", "RUN_FAIL_STDERR=Error: unsupported platform Platform(osVersion: nil, _rawArch: \"arm64\")\n"},
			previous: "services:\n  web:\n    image: alpine:3.19\n",
			wantLine: "Rolled back web — stopped and removed; nothing this `up` started is left running",
		},
		{
			// `required: false` passes over the dependency whatever the runtime said of
			// it: the diagnosis is for the one the up stops at, and this up goes on
			// (and stops at web, which fails so the row has a rollback to read).
			name: "an optional run-to-completion dependency refused with a diagnosis is passed over",
			file: "services:\n  init:\n    image: alpine:3\n  web:\n    image: alpine:3\n    depends_on:\n      init:\n        condition: service_completed_successfully\n        required: false\n",
			env: []string{"RUN_FAIL=init.demo.opossum web.demo.opossum", "RUN_FAIL_STDERR=Error: platform linux/arm64\n",
				"INSPECT_ABSENT=init.demo.opossum", "INSPECT_ABSENT_BEFORE_RUN=web.demo.opossum"},
			wantLine: "Rolled back web — stopped and removed; nothing this `up` started is left running",
			wantNot:  []string{"init"},
		},
		{
			// A runtime that would not say whether the container is there is not one
			// that said it is not: the name stays on the rollback list, and the line
			// says the rollback could not be confirmed.
			name: "a runtime that cannot be asked keeps the service on the list",
			file: "services:\n  web:\n    image: alpine:3\n",
			env: []string{"RUN_FAIL=web.demo.opossum",
				"RUN_FAIL_STDERR=Error: the runtime refused this run in words no diagnosis names\n",
				"INSPECT_ABSENT_BEFORE_RUN=web.demo.opossum", "RUN_FAIL_THEN_INSPECT_FAIL=" + unaskable, "INSPECT_FAIL_WHILE=" + unaskable},
			wantLine: "Tried to roll back web, but the runtime could not be asked whether it is gone — `container ls -a` shows it",
			// Not asked is not gone: the logs are not said to be gone either.
			wantErr:    "opossum logs web",
			wantErrNot: "no container left",
		},
		{
			// The run made the container and the rollback could not remove it: it is
			// there, and so are its logs. The failure is the same arm64 wording as the
			// rows that made nothing, so what tells them apart is the runtime's answer.
			name: "a service the run made and the rollback could not remove keeps its logs",
			file: "services:\n  web:\n    image: alpine:3\n",
			env: []string{"RUN_FAIL=web.demo.opossum", "INSPECT_ABSENT_BEFORE_RUN=web.demo.opossum", "DELETE_STICKY=web.demo.opossum",
				"RUN_FAIL_STDERR=Error: the runtime refused this run in words no diagnosis names\n"},
			wantLine:   "Tried to roll back web, but the container is still there — `opossum down` removes it",
			wantErr:    "opossum logs web",
			wantErrNot: "no container left",
		},
		{
			// The same refusal for a run-to-completion dependency: the branch that
			// reads the failure separately from the long-running one.
			name: "a run-to-completion dependency refused before it was made is not named",
			file: "services:\n  init:\n    image: alpine:3\n  web:\n    image: alpine:3\n    depends_on:\n      init:\n        condition: service_completed_successfully\n",
			env: []string{"RUN_FAIL=init.demo.opossum",
				"RUN_FAIL_STDERR=Error: platform linux/arm64\n",
				"INSPECT_ABSENT=init.demo.opossum web.demo.opossum"},
			wantLine: "Nothing this `up` started is left running",
			wantNot:  []string{"init"},
			// The diagnosis a long-running service gets: no container was made, so the
			// failure's text is the runtime's alone.
			wantErr:    "[OPSM-412]",
			wantErrNot: "exited non-zero",
		},
		{
			// The previous up's container was there and this up removed it, and the
			// runtime then refused the run: nothing ran this time either, so the text
			// is the runtime's alone — the diagnosis is given, and the old container,
			// which is gone, is named.
			name:       "a run-to-completion dependency that replaced a container and was refused says so",
			file:       "services:\n  init:\n    image: alpine:3\n  web:\n    image: alpine:3\n    depends_on:\n      init:\n        condition: service_completed_successfully\n",
			env:        []string{"RUN_FAIL=init.demo.opossum", "RUN_FAIL_MAKES_NOTHING=1", "RUN_FAIL_STDERR=Error: platform linux/arm64\n"},
			previous:   "services:\n  init:\n    image: alpine:3.19\n  web:\n    image: alpine:3\n    depends_on:\n      init:\n        condition: service_completed_successfully\n",
			wantLine:   "Rolled back init — stopped and removed; nothing this `up` started is left running",
			wantErr:    "[OPSM-412]",
			wantErrNot: "exited non-zero",
		},
		{
			// The fresh-pull wording the runtime uses for an image with no arm64 build
			// is diagnosed for a run-to-completion dependency as it is for any service.
			name: "a run-to-completion dependency refused with the fresh-pull wording says so",
			file: "services:\n  init:\n    image: alpine:3\n  web:\n    image: alpine:3\n    depends_on:\n      init:\n        condition: service_completed_successfully\n",
			env: []string{"RUN_FAIL=init.demo.opossum",
				"RUN_FAIL_STDERR=Error: unsupported platform Platform(osVersion: nil, _rawArch: \"arm64\")\n",
				"INSPECT_ABSENT=init.demo.opossum web.demo.opossum"},
			wantLine:   "Nothing this `up` started is left running",
			wantNot:    []string{"init"},
			wantErr:    "[OPSM-412]",
			wantErrNot: "exited non-zero",
		},
		{
			name: "a run-to-completion dependency refused for a volume it cannot attach says so",
			file: "services:\n  init:\n    image: alpine:3\n    volumes: [data:/d]\n  web:\n    image: alpine:3\n    depends_on:\n      init:\n        condition: service_completed_successfully\nvolumes:\n  data: {}\n",
			env: []string{"RUN_FAIL=init.demo.opossum",
				"RUN_FAIL_STDERR=Error Domain=VZErrorDomain Code=2 \"The storage device attachment is invalid.\"\n",
				"INSPECT_ABSENT=init.demo.opossum web.demo.opossum"},
			wantLine:   "Nothing this `up` started is left running",
			wantNot:    []string{"init"},
			wantErr:    "OPSM-103",
			wantErrNot: "exited non-zero",
		},
		{
			// What a job that ran printed is read by the same matches as the
			// runtime's own text, and is not a diagnosis: it made a container, so
			// it stays "exited non-zero". One row for each of the two kinds of
			// match (a word anywhere in the text, a line that begins the way the
			// runtime's does).
			name: "a run-to-completion dependency that ran and printed a port conflict is not diagnosed as one",
			file: "services:\n  init:\n    image: alpine:3\n  web:\n    image: alpine:3\n    depends_on:\n      init:\n        condition: service_completed_successfully\n",
			env: []string{"RUN_FAIL=init.demo.opossum", "INSPECT_ABSENT_BEFORE_RUN=init.demo.opossum", "INSPECT_ABSENT=web.demo.opossum",
				"RUN_FAIL_STDERR=migrate: Error: bind(descriptor:ptr:bytes:): Address already in use\n"},
			wantLine:   "Rolled back init — stopped and removed; nothing this `up` started is left running",
			wantErr:    "exited non-zero",
			wantErrNot: "OPSM-201",
		},
		{
			name: "a run-to-completion dependency that ran and printed the runtime's platform line is not diagnosed as one",
			file: "services:\n  init:\n    image: alpine:3\n  web:\n    image: alpine:3\n    depends_on:\n      init:\n        condition: service_completed_successfully\n",
			env: []string{"RUN_FAIL=init.demo.opossum", "INSPECT_ABSENT_BEFORE_RUN=init.demo.opossum", "INSPECT_ABSENT=web.demo.opossum",
				"RUN_FAIL_STDERR=Error: platform linux/arm64\n"},
			wantLine:   "Rolled back init — stopped and removed; nothing this `up` started is left running",
			wantErr:    "exited non-zero",
			wantErrNot: "OPSM-412",
		},
		{
			// And a runtime that cannot be asked, for that branch.
			name: "a run-to-completion dependency whose runtime cannot be asked stays on the list",
			file: "services:\n  init:\n    image: alpine:3\n  web:\n    image: alpine:3\n    depends_on:\n      init:\n        condition: service_completed_successfully\n",
			env: []string{"RUN_FAIL=init.demo.opossum", "INSPECT_ABSENT_BEFORE_RUN=init.demo.opossum", "RUN_FAIL_THEN_INSPECT_FAIL=" + unaskableDep, "INSPECT_FAIL_WHILE=" + unaskableDep,
				"RUN_FAIL_STDERR=Error: unsupported platform Platform(osVersion: nil, _rawArch: \"arm64\")\n"},
			wantLine: "Tried to roll back init, but the runtime could not be asked whether it is gone — `container ls -a` shows it",
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
			env:      []string{"RUN_FAIL=init.demo.opossum", "INSPECT_ABSENT_BEFORE_RUN=init.demo.opossum", "INSPECT_ABSENT=web.demo.opossum"},
			wantLine: "Rolled back init — stopped and removed; nothing this `up` started is left running",
			wantErr:  "exited non-zero",
		},
		{
			// A one-shot dependency that printed the registry's line itself and then
			// failed: its container is there, so the runtime's answer is what keeps
			// this from being read as an image failure. The shim answers that the
			// container is not there until the run, and is there after it, as the
			// runtime does for one that was made.
			name: "a run-to-completion dependency that printed a registry line and was made is named",
			file: "services:\n  init:\n    image: alpine:3\n  web:\n    image: alpine:3\n    depends_on:\n      init:\n        condition: service_completed_successfully\n",
			env: []string{"RUN_FAIL=init.demo.opossum",
				"RUN_FAIL_STDERR=Error: HTTP request to https://registry-1.docker.io/v2/x/manifests/latest failed with response: 404 Not Found. Reason: Unknown",
				"INSPECT_ABSENT_BEFORE_RUN=init.demo.opossum", "INSPECT_ABSENT=web.demo.opossum"},
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
				"RUN_FAIL=web.demo.opossum", "INSPECT_ABSENT_BEFORE_RUN=web.demo.opossum", "INSPECT_ABSENT=init.demo.opossum"},
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
				if strings.HasPrefix(l, "Rolled back ") || strings.HasPrefix(l, "Tried to roll back ") || strings.HasPrefix(l, "Nothing this `up` started") {
					report = l
				}
			}
			if report != tc.wantLine {
				t.Errorf("the rollback reported %q, want %q\nwhole output:\n%s\nerr: %v", report, tc.wantLine, out.String(), err)
			}
			for _, name := range tc.wantNot {
				if strings.Contains(report, name) {
					t.Errorf("the rollback names %q, which was never made: %q", name, report)
				}
			}
		})
	}
}
