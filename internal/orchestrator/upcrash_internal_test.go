package orchestrator

// Evals for #283: a detached `up` must not report success over a service whose
// container exited right after starting (no healthcheck/depends_on to catch it).
// verifyStarted reports each crashed service with its logs and fails the up; the
// containers are left up for inspection (no rollback), and one-shots are exempt.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	rt "github.com/suruseas/opossum/internal/runtime"
)

const stoppedInspect = `[{"status":{"state":"stopped"},"configuration":{"id":"web"}}]`
const runningInspect = `[{"status":{"state":"running"},"configuration":{"id":"web"}}]`

func inspectShim(t *testing.T, inspectJSON, logsLine string) *rt.Runtime {
	return scriptShim(t, ""+
		"  inspect) echo '"+inspectJSON+"' ;;\n"+
		"  logs) echo '"+logsLine+"' ;;\n")
}

func webProject() *compose.Project {
	return &compose.Project{Name: "demo", Services: map[string]*compose.Service{
		"web": {Name: "web", Image: "web:latest"},
	}}
}

func TestVerifyStartedFlagsCrash(t *testing.T) {
	var out bytes.Buffer
	o := New(webProject(), inspectShim(t, stoppedInspect, "panic: bad config"), "", &out)

	err := o.verifyStarted([]string{"web"}, map[string]bool{})
	if err == nil || !strings.Contains(err.Error(), "[OPSM-407]") || !strings.Contains(err.Error(), `"web"`) {
		t.Errorf("a crashed service should fail the up with OPSM-407 naming it, got: %v", err)
	}
	// The per-service warning embeds the last log lines so the cause is visible.
	if s := out.String(); !strings.Contains(s, "[OPSM-407]") || !strings.Contains(s, "exited right after starting") || !strings.Contains(s, "panic: bad config") {
		t.Errorf("verifyStarted should warn with the crashed service's logs, got: %s", s)
	}
}

func TestVerifyStartedSkipsOneShot(t *testing.T) {
	// A completed-target (one-shot) is *supposed* to exit — never flag it.
	o := New(webProject(), inspectShim(t, stoppedInspect, "done"), "", &bytes.Buffer{})
	if err := o.verifyStarted([]string{"web"}, map[string]bool{"web": true}); err != nil {
		t.Errorf("a one-shot that exited must not be flagged, got: %v", err)
	}
}

func TestVerifyStartedOkWhenRunning(t *testing.T) {
	o := New(webProject(), inspectShim(t, runningInspect, ""), "", &bytes.Buffer{})
	if err := o.verifyStarted([]string{"web"}, map[string]bool{}); err != nil {
		t.Errorf("a running service should pass, got: %v", err)
	}
}

// upWithLog drives a full `up` against a shim that logs every invocation and runs
// `runBody` for the `run` subcommand (inspect always reports the container
// stopped). Returns the up error and the invocation log — so one harness can show
// BOTH that a bring-up failure rolls back (a `stop` appears) and that a post-start
// crash does not (no `stop`), making the differential explicit.
func upWithLog(t *testing.T, runBody string) (error, string) {
	t.Helper()
	dir := t.TempDir()
	logf := filepath.Join(dir, "invocations.log")
	shim := filepath.Join(dir, "c.sh")
	body := "#!/bin/sh\necho \"$*\" >> " + logf + "\n" +
		"case \"$1\" in\n" +
		"  system) echo 'status running' ;;\n" +
		"  inspect) echo '" + stoppedInspect + "' ;;\n" +
		"  logs) echo 'boom' ;;\n" +
		"  ls) echo '[]' ;;\n" +
		"  run) " + runBody + " ;;\n" +
		"esac\nexit 0\n"
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	err := New(webProject(), &rt.Runtime{Bin: shim}, "", &bytes.Buffer{}).Up(true)
	log, _ := os.ReadFile(logf)
	return err, string(log)
}

// A post-start crash fails the up but must NOT roll the stack back — the crashed
// container stays up for inspection (like docker compose). Guards the broughtUp
// suppression of the rollback defer.
func TestUpKeepsContainersOnPostStartCrash(t *testing.T) {
	err, log := upWithLog(t, "exit 0") // run succeeds; inspect says stopped -> OPSM-407
	if err == nil || !strings.Contains(err.Error(), "[OPSM-407]") {
		t.Fatalf("up should fail with OPSM-407 on a post-start crash, got: %v", err)
	}
	// Rollback is the only path that `stop`s a container; assert it never runs.
	if strings.Contains(log, "stop ") {
		t.Errorf("a post-start crash must not roll back (no `stop`), got invocations:\n%s", log)
	}
}

// The differential: a genuine bring-up failure (the run itself fails) STILL rolls
// back on the same harness — so the no-`stop` assertion above is meaningful (the
// harness would show `stop` if the rollback fired).
func TestUpRollsBackOnBringUpFailure(t *testing.T) {
	err, log := upWithLog(t, "exit 1") // the run fails mid-loop -> rollback
	if err == nil {
		t.Fatal("up should fail when the run fails")
	}
	if !strings.Contains(log, "stop ") {
		t.Errorf("a bring-up failure must roll back (expected a `stop`), got invocations:\n%s", log)
	}
}
