package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A build puts the labels it is given on the image.
func TestBuildLabelsTheImage(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "argv.log")
	r := &Runtime{Bin: fakeShimBin, Env: []string{"SHIM_LOG=" + logFile}}
	if err := r.Build(BuildOptions{Tag: "org/app:v1", Context: "/ctx", Labels: []string{"opossum.project=demo", "a=b"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != "build --progress plain -t org/app:v1 -l opossum.project=demo -l a=b /ctx" {
		t.Errorf("unexpected build argv: %q", got)
	}
}
