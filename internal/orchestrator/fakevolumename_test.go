package orchestrator_test

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// The fake refuses a `-v` volume name container 1.4.1 refuses (the three fakes
// share the wording in internal/shimcontract). What only this fake has is
// checked here: its $RUN_EXISTS lever, which on 1.4.1 is said before the volume,
// and its record of the volumes a run made, which a refused run must not add to.
func TestFakeRefusesAVolumeNameAfterATakenName(t *testing.T) {
	shim := func(t *testing.T, env []string, args ...string) (string, int) {
		t.Helper()
		cmd := exec.Command(fakeShimBin, args...)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(out), code
	}
	const refusedVolume = "Error: invalid volume name 'bad+vol': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"

	for _, args := range [][]string{
		{"run", "--rm", "--name", "holder", "-v", "bad+vol:/x", "alpine"},
		{"run", "--rm", "-v", "bad+vol:/x", "--name", "holder", "alpine"},
	} {
		t.Run("taken name first: "+strings.Join(args, " "), func(t *testing.T) {
			env := []string{"STATE_DIR=" + t.TempDir(), "RUN_EXISTS=holder"}
			out, code := shim(t, env, args...)
			if code != 1 || !strings.Contains(out, "Error: container with id holder already exists") || strings.Contains(out, "invalid volume name") {
				t.Errorf("want the taken name said (exit 1) and not the volume, got exit %d:\n%s", code, out)
			}
		})
	}

	t.Run("control: another name held, the volume is refused", func(t *testing.T) {
		env := []string{"STATE_DIR=" + t.TempDir(), "RUN_EXISTS=someoneelse"}
		out, code := shim(t, env, "run", "--rm", "--name", "holder", "-v", "bad+vol:/x", "alpine")
		if code != 1 || !strings.Contains(out, refusedVolume) {
			t.Errorf("want the volume refused (exit 1), got exit %d:\n%s", code, out)
		}
	})

	t.Run("a refused run records no volume, not even a valid one before it", func(t *testing.T) {
		env := []string{"STATE_DIR=" + t.TempDir()}
		if out, code := shim(t, env, "run", "--rm", "-v", "okfirst:/x", "-v", "bad+vol:/y", "alpine"); code != 1 || !strings.Contains(out, refusedVolume) {
			t.Fatalf("want the volume refused (exit 1), got exit %d:\n%s", code, out)
		}
		if out, _ := shim(t, env, "volume", "ls", "-q"); strings.Contains(out, "okfirst") {
			t.Errorf("want no volume recorded by a refused run, got:\n%s", out)
		}
		// Control: the same state records a run that goes ahead.
		if out, code := shim(t, env, "run", "--rm", "-v", "okfirst:/x", "alpine"); code != 0 {
			t.Fatalf("want the run to go ahead, got exit %d:\n%s", code, out)
		}
		if out, _ := shim(t, env, "volume", "ls", "-q"); !strings.Contains(out, "okfirst") {
			t.Errorf("want the volume of a run that went ahead recorded, got:\n%s", out)
		}
	})
}

// An anonymous volume whose path holds characters outside a volume name still
// comes up on the fake: the name opossum builds for it keeps to the rule the
// runtime (and now the fake) enforces. Each path puts the outside character in
// another place.
func TestUpMakesAnAnonymousVolumeNameTheRuntimeTakes(t *testing.T) {
	for _, path := range []string{"/a+b", "/café", "/sp ace/x", "/u@h"} {
		t.Run(path, func(t *testing.T) {
			rt, log := fakeShim(t)
			proj := project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20", Volumes: []string{path}}})
			var out bytes.Buffer
			if err := orchestrator.New(proj, rt, "opossum", &out).Up(true); err != nil {
				t.Fatalf("want up to succeed, got %v\n%s", err, out.String())
			}
			started := false
			for _, l := range log() {
				if strings.HasPrefix(l, "run -d") && strings.Contains(l, ":"+path) {
					started = true
				}
			}
			if !started {
				t.Errorf("want the service started with the volume mounted at %s, got %v", path, log())
			}
		})
	}
}

// An anonymous volume whose name would be longer than the runtime creates (255
// characters) comes up on the fake — which refuses a longer name as container
// 1.4.1 does — under a 255-character name, and `down -v` removes that same
// name. The path is long enough to need a cut, and a second path differs from
// it only past the cut.
func TestUpMakesALongAnonymousVolumeNameTheRuntimeTakes(t *testing.T) {
	long := "/" + strings.Repeat("deep/", 60)
	rt, log := fakeShim(t)
	proj := project("demo", map[string]*compose.Service{"web": {Image: "alpine:3.20", Volumes: []string{long + "a", long + "b"}}})
	var out bytes.Buffer
	o := orchestrator.New(proj, rt, "opossum", &out)
	if err := o.Up(true); err != nil {
		t.Fatalf("want up to succeed, got %v\n%s", err, out.String())
	}
	var names []string
	for _, l := range log() {
		if !strings.HasPrefix(l, "run -d") {
			continue
		}
		for _, f := range strings.Fields(l) {
			if name, target, ok := strings.Cut(f, ":"); ok && strings.HasPrefix(target, long) {
				names = append(names, name)
			}
		}
	}
	if len(names) != 2 || names[0] == names[1] || len(names[0]) != 255 || len(names[1]) != 255 {
		t.Fatalf("want two different 255-character volume names on the run, got %q in %v", names, log())
	}
	before := len(log())
	if err := o.Down(true, "", false); err != nil {
		t.Fatalf("down -v: %v", err)
	}
	for _, n := range names {
		if indexOf(log()[before:], "volume delete "+n) < 0 {
			t.Errorf("want down -v to delete %q, got %v", n, log()[before:])
		}
	}
}
