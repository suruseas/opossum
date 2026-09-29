package orchestrator_test

// #1381: `opossum run` (a foreground one-off) gets the same coded diagnoses
// `up` gives a long-running service or a run-to-completion dependency — an
// image with no build for this platform, a host port pre-flight missed —
// but, like `up --foreground`, only where the run made no container: this
// run is never detached, so a job that printed the same words as the runtime
// and then ran is not to be misread as the runtime's refusal.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

func TestRunOneOffGetsTheSameCodedDiagnosesAsUp(t *testing.T) {
	const platform = "Error: platform linux/arm64\n"
	for _, tc := range []struct {
		name    string
		env     []string
		rm      bool
		want    string
		wantNot string
	}{
		{name: "an image with no arm64 build the runtime refused, no container made",
			env: []string{"RUN_FAIL_MAKES_NOTHING=1"}, want: "[OPSM-412]"},
		// The fake answers "there" for a container by default once a run of the
		// name has happened (INSPECT_ABSENT_BEFORE_RUN's absence), which is the
		// job's own case: it ran, and its output happens to carry the words.
		{name: "a container was made and printed the platform line", wantNot: "OPSM-412"},
		// Asked before --rm deletes it: a container --rm just removed answers
		// "gone" for the same reason as one never made, which must not turn a
		// made container's own words into the runtime's.
		{name: "a container was made and printed the platform line, with --rm", rm: true, wantNot: "OPSM-412"},
		// runMadeNothing reads Unknown (the runtime not answering) as "not
		// provably nothing" — the same as "something may be there" — not as
		// "nothing". A run whose container the runtime cannot even be asked
		// about after the failure must not be diagnosed as a coded refusal: the
		// job's own output, if any container was in fact made, is exactly what
		// this whole check exists to not misread.
		{name: "the runtime stops answering after the failure", env: []string{"@unaskable"}, wantNot: "OPSM-412"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, _ := fakeShim(t)
			cname := "app-run.demo.opossum"
			env := []string{"RUN_FAIL=" + cname, "RUN_FAIL_STDERR=" + platform}
			unaskable := t.TempDir() + "/unaskable"
			for _, e := range tc.env {
				if e == "@unaskable" {
					env = append(env, "RUN_FAIL_THEN_INSPECT_FAIL="+unaskable, "INSPECT_FAIL_WHILE="+unaskable)
					continue
				}
				env = append(env, e)
			}
			setShimEnv(rt, env...)
			proj := project("demo", map[string]*compose.Service{"app": {Image: "alpine:3.20"}})
			err := orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).RunOneOff("app", nil, orchestrator.RunOneOffOptions{NoDeps: true, Rm: tc.rm})
			if err == nil {
				t.Fatal("want the run refused")
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the failure should carry %s, got: %v", tc.want, err)
			}
			if tc.wantNot != "" && strings.Contains(err.Error(), tc.wantNot) {
				t.Errorf("a container was made, so %s is not the runtime's to say, got: %v", tc.wantNot, err)
			}
		})
	}
}
