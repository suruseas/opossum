package orchestrator

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// A watch warning prints a whole `up` failure through %v, and an up failure
// can carry a project's own text with its line breaks intact — a bind source
// is quoted raw into OPSM-104, so a path with a newline in it used to walk to
// column zero, where opossum's sentences start (#688; measured before fixing:
// three forged lines). The CLI's error printer indents exactly these errors
// via quoted(); logf now draws the same line for error-typed arguments.
func TestAWatchRebuildFailureCannotSpeakForOpossum(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	parent := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o700) })
	// A carriage return rides inside the value too: on a terminal it rewinds
	// the line, and it must come out as a space like any other control byte.
	forged := "FORGED opossum\rsays all clear"
	shim := scriptShim(t, "  system) echo 'status running' ;;\n  ls) echo '[]' ;;\n")
	p := &compose.Project{Name: "w", BaseDir: t.TempDir(), Services: map[string]*compose.Service{
		"web": {Name: "web", Image: "app:1",
			Volumes: []string{filepath.Join(parent, "x\n"+forged) + ":/data"}},
	}}
	var out bytes.Buffer
	o := New(p, shim, "opossum", &out)
	err := o.rebuildService("web")
	if err == nil {
		t.Fatal("the bind source cannot be created, so the rebuild had to fail")
	}
	// The line watch.go prints on this failure, wording and all.
	o.warnf(codeWatchRebuild, "rebuild %s failed: %v — fix the error above and save again, or re-run `opossum up --build %s`\n", "web", err, "web")

	s := out.String()
	// The planted text must still be readable — quoting is not deleting. The
	// carriage return comes out as a space, and no control byte survives.
	if !strings.Contains(s, "FORGED opossum says all clear") {
		t.Fatalf("the value should survive, flattened and quoted, got:\n%q", s)
	}
	if strings.Contains(s, "\r") {
		t.Errorf("a carriage return survived into the warning:\n%q", s)
	}
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "FORGED") {
			t.Errorf("a project's text speaks from column zero:\n%s", s)
		}
	}
}

// The same treatment keeps a deliberately multi-line error in shape: a build
// failure's hint carries an indented command block, and indented lines pass
// through untouched — only the block's opening prose moves in by two.
func TestAWatchRebuildFailureKeepsTheHintBlockShape(t *testing.T) {
	shim := scriptShim(t, ""+
		"  system) echo 'status running' ;;\n"+
		"  ls) echo '[]' ;;\n"+
		"  image) exit 1 ;;\n"+
		"  build) echo 'no space left on device' >&2; exit 1 ;;\n")
	p := &compose.Project{Name: "w", BaseDir: t.TempDir(), Services: map[string]*compose.Service{
		"web": {Name: "web", Build: &compose.Build{Context: "."}},
	}}
	var out bytes.Buffer
	o := New(p, shim, "opossum", &out)
	err := o.rebuildService("web")
	if err == nil {
		t.Fatal("the shim fails every build")
	}
	o.warnf(codeWatchRebuild, "rebuild %s failed: %v — fix the error above and save again, or re-run `opossum up --build %s`\n", "web", err, "web")
	s := out.String()
	// The command block's own indentation survives byte for byte…
	if !strings.Contains(s, "\n    container image prune -f") {
		t.Errorf("the hint's command block should keep its indentation, got:\n%s", s)
	}
	// …and no line of the warning starts at the margin except opossum's own
	// (the warning itself, and the build/network progress lines around it).
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "hint:") {
			t.Errorf("the hint's opening line should be indented as a quoted continuation, got:\n%s", s)
		}
	}
}
