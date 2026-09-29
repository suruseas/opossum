package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/orchestrator"
)

// `up --foreground` reads a failed run's diagnosis from the same text as the
// detached `up`, but that text is not the same: a foreground run carries the
// container's own output next to the runtime's, and the matches behind the
// diagnoses (`Address already in use`, `Error: platform linux/arm64`) are words a
// job can print as well as the runtime. A service that printed them and exited
// was reported as a host port conflict or an image with no arm64 build (measured
// on 1.4.1: a service with no port and no platform, `up --foreground`).
//
// What tells the runtime's refusal from the job is whether a container was made:
// measured on 1.4.1, a port the runtime cannot bind, a mount it refuses, an image
// with no arm64 build and a volume it will not attach all leave nothing listed,
// and a service that printed the same words and exited leaves a stopped one. So
// the foreground run is diagnosed only where none was made; the detached run's
// text is the runtime's alone and keeps every diagnosis.
//
// The fake answers "there" for a container by default, which is the job's case;
// INSPECT_ABSENT_BEFORE_RUN is a container that the run made and
// RUN_FAIL_MAKES_NOTHING / INSPECT_ABSENT a run that made none.
func TestAForegroundServiceIsDiagnosedOnlyWhereNoContainerWasMade(t *testing.T) {
	const port = "Error: failed to run container: Address already in use\n"
	const platform = "Error: platform linux/arm64\n"
	// The runtime's own line for a bind mount it cannot resolve (a directory mounted
	// onto /etc/passwd, measured on 1.4.1: no container is left).
	const rootfs = "Error: mount failed with errno 20: failed to resolve '/etc/passwd' in rootfs\n"
	for _, tc := range []struct {
		name     string
		detach   bool
		stderr   string
		env      []string // beyond RUN_FAIL and the stderr
		want     string   // a code the error carries
		wantNot  string
		wantLogs bool // the generic start failure, which no code names
	}{
		{name: "a port conflict the runtime refused", stderr: port, env: []string{"INSPECT_ABSENT=app.demo.opossum"}, want: "[OPSM-201]"},
		{name: "an image with no arm64 build the runtime refused", stderr: platform, env: []string{"INSPECT_ABSENT=app.demo.opossum"}, want: "[OPSM-412]"},
		{name: "a bind mount the runtime could not resolve", stderr: rootfs, env: []string{"INSPECT_ABSENT=app.demo.opossum"}, want: "[OPSM-107]"},
		{name: "a service that made a container and printed the rootfs words", stderr: rootfs,
			env: []string{"INSPECT_ABSENT_BEFORE_RUN=app.demo.opossum"}, wantNot: "OPSM-107", wantLogs: true},
		// A second `up --foreground` replaces the container the first one made, which
		// is the usual case: the fake answers "there" before and after the run, so
		// the words are the job's, and the replacing changes nothing.
		{name: "a service that replaced a container and printed a port conflict", stderr: port,
			wantNot: "OPSM-201", wantLogs: true},
		{name: "a service that made a container and printed a port conflict", stderr: port,
			env: []string{"INSPECT_ABSENT_BEFORE_RUN=app.demo.opossum"}, wantNot: "OPSM-201", wantLogs: true},
		{name: "a service that made a container and printed the platform line", stderr: platform,
			env: []string{"INSPECT_ABSENT_BEFORE_RUN=app.demo.opossum"}, wantNot: "OPSM-412", wantLogs: true},
		// A runtime that would not say is not one that said no: nothing is
		// diagnosed on words that may be the job's.
		{name: "a runtime that cannot be asked", stderr: port,
			env:     []string{"INSPECT_ABSENT_BEFORE_RUN=app.demo.opossum", "RUN_FAIL_THEN_INSPECT_FAIL=@unaskable", "INSPECT_FAIL_WHILE=@unaskable"},
			wantNot: "OPSM-201", wantLogs: true},
		// The detached run's text is the runtime's alone, so the words are the
		// runtime's whether or not the fake says a container is there.
		{name: "detached, the same words with a container", detach: true, stderr: port, env: []string{"INSPECT_ABSENT_BEFORE_RUN=app.demo.opossum"}, want: "[OPSM-201]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, _ := fakeShim(t)
			env := []string{"RUN_FAIL=app.demo.opossum", "RUN_FAIL_STDERR=" + tc.stderr}
			unaskable := t.TempDir() + "/unaskable"
			for _, e := range tc.env {
				env = append(env, strings.ReplaceAll(e, "@unaskable", unaskable))
			}
			setShimEnv(rt, env...)
			proj, err := loadProject(t, "services:\n  app:\n    image: alpine:3\n    volumes:\n      - ./data:/etc/passwd\n")
			if err != nil {
				t.Fatal(err)
			}
			err = orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).Up(tc.detach)
			if err == nil {
				t.Fatal("want the up refused")
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the failure should carry %s, got: %v", tc.want, err)
			}
			if tc.wantNot != "" && strings.Contains(err.Error(), tc.wantNot) {
				t.Errorf("no container was refused here, so %s is not the runtime's to say, got: %v", tc.wantNot, err)
			}
			if tc.wantLogs && !strings.Contains(err.Error(), "verify the image, command, and mounts") {
				t.Errorf("a container was made, so this is the generic start failure, got: %v", err)
			}
		})
	}
}

// A long pull's progress lines (`[1/6] Fetching image [Ns]`, once a second)
// come before a failure that only shows once the pull ends — an arm64-less
// image, say — and used to push it past the captured stderr's old head-only
// cap before runErrorHint ever saw it (#1353): long enough padding lost the
// hint entirely. Keeping a tail alongside the head (cappedBuffer,
// internal/runtime) catches it again, however much padding comes first.
func TestALateRunFailureIsStillDiagnosedPastALongPullsProgressLines(t *testing.T) {
	for _, tc := range []struct {
		name    string
		padding int // repeats of a ~26-byte progress line
	}{
		{"padding past the old 8 KiB head-only cap", 400},     // ~10.8 KiB
		{"padding past the new head+tail cap entirely", 1000}, // ~27 KiB
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, _ := fakeShim(t)
			stderr := strings.Repeat("[1/6] Fetching image [1s]\n", tc.padding) + "Error: platform linux/arm64\n"
			setShimEnv(rt, "RUN_FAIL=app.demo.opossum", "RUN_FAIL_STDERR="+stderr, "INSPECT_ABSENT=app.demo.opossum")
			proj, err := loadProject(t, "services:\n  app:\n    image: alpine:3\n")
			if err != nil {
				t.Fatal(err)
			}
			err = orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).Up(false)
			if err == nil {
				t.Fatal("want the up refused")
			}
			if want := "[OPSM-412]"; !strings.Contains(err.Error(), want) {
				t.Errorf("want the arm64 hint %s despite the padding ahead of the failure, got: %v", want, err)
			}
		})
	}
}

// A failure line straddling the exact byte where head hands off to tail
// (headCap, internal/runtime's cappedBuffer) must not be split in two: it
// would still fit in full inside the combined head+tail capacity (nothing
// here overflows tailCap, so nothing is meant to be dropped), and String()
// must join head and tail bare in that case for the line to read whole.
func TestARunFailureStraddlingTheHeadTailBoundaryIsStillDiagnosed(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, _ := fakeShim(t)
	// headCap is 4 KiB (internal/runtime): padding to 4090 bytes puts "Error:
	// platform linux/arm64" starting 6 bytes before the boundary, ending
	// comfortably inside the tail — and the whole thing is well under
	// headCap+tailCap (8 KiB), so nothing should be dropped at all.
	stderr := strings.Repeat("x", 4090) + "Error: platform linux/arm64\n"
	setShimEnv(rt, "RUN_FAIL=app.demo.opossum", "RUN_FAIL_STDERR="+stderr, "INSPECT_ABSENT=app.demo.opossum")
	proj, err := loadProject(t, "services:\n  app:\n    image: alpine:3\n")
	if err != nil {
		t.Fatal(err)
	}
	err = orchestrator.New(proj, rt, "opossum", &bytes.Buffer{}).Up(false)
	if err == nil {
		t.Fatal("want the up refused")
	}
	if want := "[OPSM-412]"; !strings.Contains(err.Error(), want) {
		t.Errorf("want the arm64 hint %s even though the failure line straddles the head/tail boundary, got: %v", want, err)
	}
}
