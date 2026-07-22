package orchestrator

// These evals cover OPSM-103: opossum decoding Apple `container`'s cryptic
// exclusive-attach VZError into an actionable diagnostic that names the volume and
// the running container holding it, plus the pre-flight warning that catches a
// busy volume before `up` even tries to start the service.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/runtime"
)

// realVZError is the exact stderr Apple `container` emits when a second container
// tries to attach a named volume already held by a running one. Captured from a
// real repro (two containers sharing one volume) so the signature matcher is
// tested against the genuine text, not a paraphrase.
const realVZError = `[6/6] Starting container [0s]
Error: failed to bootstrap container (cause: "internalError: "failed to bootstrap container vzsecond (cause: "unknown: "Error Domain=VZErrorDomain Code=2 "The storage device attachment is invalid." UserInfo={NSLocalizedFailure=Invalid virtual machine configuration., NSLocalizedFailureReason=The storage device attachment is invalid.}"")"")`

func TestIsStorageAttachmentError(t *testing.T) {
	if !isStorageAttachmentError(realVZError) {
		t.Errorf("real VZError storage-attachment stderr should match")
	}
	negatives := map[string]string{
		"empty":               "",
		"other VZError":       `Error Domain=VZErrorDomain Code=5 "Something else entirely"`,
		"unrelated bootstrap": `Error: failed to bootstrap container (cause: "image not found")`,
		"plain exit":          "exit status 1",
		// The storage phrase alone, without the VZError domain, must not match — the
		// three-way AND has to hold, not any single clause.
		"phrase without domain": `some driver: the storage device attachment is invalid`,
	}
	for name, s := range negatives {
		if isStorageAttachmentError(s) {
			t.Errorf("%s: should not match the attach-conflict signature: %q", name, s)
		}
	}
}

// lsShim returns a Runtime whose `ls -a --format json` prints lsJSON (and whose
// other subcommands succeed quietly), so a test can control which containers the
// orchestrator sees as running and what volumes they hold.
func lsShim(t *testing.T, lsJSON string) *runtime.Runtime {
	t.Helper()
	dir := t.TempDir()
	shim := filepath.Join(dir, "c.sh")
	script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n  ls) echo %q ;;\n  system) echo 'status running' ;;\nesac\nexit 0\n", lsJSON)
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &runtime.Runtime{Bin: shim}
}

// oneRunning builds the `ls -a --format json` for a single running container
// mounting the given named volumes.
func oneRunning(name string, vols ...string) string {
	mounts := make([]string, len(vols))
	for i, v := range vols {
		mounts[i] = fmt.Sprintf(`{"type":{"volume":{"name":%q}}}`, v)
	}
	return fmt.Sprintf(`[{"status":{"state":"running"},"configuration":{"id":%q,"mounts":[%s]}}]`,
		name, strings.Join(mounts, ","))
}

func TestDecodeStartErrorNamesVolumeAndHolder(t *testing.T) {
	// "ghost" is a stray container already holding this project's namespaced volume
	// pj_data — the classic exclusive-attach clash.
	rt := lsShim(t, oneRunning("ghost", "pj_data"))
	p := project103("pj", map[string]*compose.Service{
		"app": {Image: "app:latest", Volumes: []string{"data:/var/lib"}},
	})
	var buf bytes.Buffer
	o := New(p, rt, "", &buf)

	raw := &runtime.RunError{Err: fmt.Errorf("exit status 1"), Stderr: realVZError}
	err := o.decodeStartError("app", raw)
	if err == nil {
		t.Fatal("expected an error")
	}
	s := err.Error()
	for _, want := range []string{"[OPSM-103]", `"app"`, `"pj_data"`, `"ghost"`, "VZError"} {
		if !strings.Contains(s, want) {
			t.Errorf("decoded error missing %q; got: %s", want, s)
		}
	}
}

func TestDecodeStartErrorPassesThroughNonAttachError(t *testing.T) {
	rt := lsShim(t, `[]`)
	p := project103("pj", map[string]*compose.Service{
		"app": {Image: "app:latest", Volumes: []string{"data:/var/lib"}},
	})
	o := New(p, rt, "", &bytes.Buffer{})

	// A plain failure (image pull, bad command) must not be dressed up as OPSM-103.
	raw := &runtime.RunError{Err: fmt.Errorf("exit status 1"), Stderr: "Error: image not found"}
	s := o.decodeStartError("app", raw).Error()
	if strings.Contains(s, "OPSM-103") {
		t.Errorf("non-attach failure should pass through, not decode to OPSM-103; got: %s", s)
	}
	if !strings.Contains(s, `starting service "app"`) {
		t.Errorf("pass-through should keep the generic prefix; got: %s", s)
	}
}

func TestDecodeStartErrorSignatureButNoHolder(t *testing.T) {
	// The signature matched but the holder exited between the failed run and the
	// lookup — still decode to OPSM-103 and name the volume, just without a culprit.
	rt := lsShim(t, `[]`)
	p := project103("pj", map[string]*compose.Service{
		"app": {Image: "app:latest", Volumes: []string{"data:/var/lib"}},
	})
	o := New(p, rt, "", &bytes.Buffer{})

	raw := &runtime.RunError{Err: fmt.Errorf("exit status 1"), Stderr: realVZError}
	s := o.decodeStartError("app", raw).Error()
	if !strings.Contains(s, "OPSM-103") || !strings.Contains(s, `"pj_data"`) {
		t.Errorf("expected OPSM-103 naming the volume even without a holder; got: %s", s)
	}
	if strings.Contains(s, "held by") {
		t.Errorf("should not claim a holder when none is running; got: %s", s)
	}
}

func TestWarnBusyNamedVolumeCrossProject(t *testing.T) {
	// A container from another project ("otherapp") is holding the external volume
	// "cache" that this project's "web" service also mounts — a conflict only the
	// live runtime knows about, so the compose-only OPSM-102 check can't see it.
	rt := lsShim(t, oneRunning("otherapp", "cache"))
	p := project103("pj", map[string]*compose.Service{
		"web": {Image: "web:latest", Volumes: []string{"cache:/data"}},
	})
	p.Volumes = map[string]compose.VolumeDecl{"cache": {External: true}}
	var buf bytes.Buffer
	o := New(p, rt, "", &buf)

	o.warnBusyNamedVolumes([]string{"web"})
	s := buf.String()
	for _, want := range []string{"[OPSM-103]", `"web"`, `"cache"`, `"otherapp"`} {
		if !strings.Contains(s, want) {
			t.Errorf("busy-volume warning missing %q; got: %s", want, s)
		}
	}
}

func TestWarnBusyNamedVolumeIgnoresOwnContainer(t *testing.T) {
	// The only container holding the volume is *this* project's own container (which
	// opossum deletes and recreates during up), so there's nothing to warn about.
	rt := lsShim(t, oneRunning("web", "cache"))
	p := project103("pj", map[string]*compose.Service{
		"web": {Image: "web:latest", Volumes: []string{"cache:/data"}},
	})
	p.Volumes = map[string]compose.VolumeDecl{"cache": {External: true}}
	var buf bytes.Buffer
	o := New(p, rt, "", &buf)

	o.warnBusyNamedVolumes([]string{"web"})
	if s := buf.String(); strings.Contains(s, "OPSM-103") {
		t.Errorf("should not warn when only our own container holds the volume; got: %s", s)
	}
}

func TestDecodeStartErrorPassesThroughBindMountOnly(t *testing.T) {
	// A VZError signature on a service that mounts no opossum-owned named volume
	// (only a bind mount) isn't an attach conflict we can explain — pass it through
	// rather than mislabel it OPSM-103.
	rt := lsShim(t, `[]`)
	p := project103("pj", map[string]*compose.Service{
		"app": {Image: "app:latest", Volumes: []string{"/host/path:/data"}},
	})
	o := New(p, rt, "", &bytes.Buffer{})

	raw := &runtime.RunError{Err: fmt.Errorf("exit status 1"), Stderr: realVZError}
	s := o.decodeStartError("app", raw).Error()
	if strings.Contains(s, "OPSM-103") {
		t.Errorf("a bind-mount-only service should not decode to OPSM-103; got: %s", s)
	}
}

// TestUpWiresBusyVolumeWarningAndDecode drives Up() end to end against a shim so
// the *call sites* are guarded, not just the helpers: the pre-flight warning
// (Up → warnBusyNamedVolumes) and the failure decode (Up → decodeStartError) both
// have to fire, or reverting either wiring line would leave a green suite.
func TestUpWiresBusyVolumeWarningAndDecode(t *testing.T) {
	// A foreign container "ghost" holds this project's namespaced volume vzint_data.
	// The shim: runtime running, that volume already exists (skip seeding), `ls`
	// reports the holder, `inspect` says our container is absent, and every `run`
	// fails with the exclusive-attach VZError.
	holder := oneRunning("ghost", "vzint_data")
	dir := t.TempDir()
	shim := filepath.Join(dir, "c.sh")
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"  system) echo 'status running' ;;\n" +
		"  volume) echo 'vzint_data' ;;\n" +
		"  ls) echo '" + holder + "' ;;\n" +
		"  inspect) exit 1 ;;\n" +
		"  run) echo 'Error Domain=VZErrorDomain Code=2 \"The storage device attachment is invalid.\"' >&2; exit 1 ;;\n" +
		"esac\nexit 0\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p := project103("vzint", map[string]*compose.Service{
		"app": {Image: "app:latest", Volumes: []string{"data:/var/lib"}},
	})
	var buf bytes.Buffer
	o := New(p, &runtime.Runtime{Bin: shim}, "", &buf)

	err := o.Up(true) // detached: the start run's stderr is captured for decoding
	if err == nil {
		t.Fatal("Up should fail when the volume is already attached")
	}
	// The failure decode (call site: Up → decodeStartError).
	if s := err.Error(); !strings.Contains(s, "[OPSM-103]") || !strings.Contains(s, `"vzint_data"`) || !strings.Contains(s, `"ghost"`) {
		t.Errorf("Up error should be the decoded OPSM-103 naming volume+holder; got: %v", err)
	}
	// The pre-flight warning (call site: Up → warnBusyNamedVolumes).
	if s := buf.String(); !strings.Contains(s, "[OPSM-103]") || !strings.Contains(s, `"ghost"`) {
		t.Errorf("Up should also emit the pre-flight busy-volume warning naming the holder; got: %s", s)
	}
}

// project103 is a minimal Project for these evals (own base dir avoids polluting
// the shared testBaseDir with bind-dir side effects — there are none here).
func project103(name string, svcs map[string]*compose.Service) *compose.Project {
	for n, s := range svcs {
		s.Name = n
	}
	return &compose.Project{Name: name, Services: svcs}
}
