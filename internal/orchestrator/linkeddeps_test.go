package orchestrator_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// `up a` starts what `a` links to first, and brings it along, as docker compose does (`links:` and `network_mode: service:` are dependencies, #1802).
func TestUpStartsWhatAServiceLinksToFirst(t *testing.T) {
	for _, tc := range []struct{ name, extra string }{
		{"links", "    links: [\"b:alias\"]\n"},
		{"network_mode service", "    network_mode: service:b\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			rt, _ := fakeShim(t)
			path := filepath.Join(t.TempDir(), "c.yaml")
			body := "name: lk\nservices:\n  a:\n    image: x\n" + tc.extra + "  b:\n    image: y\n"
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			project, err := compose.LoadFiles([]string{path}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := orchestrator.New(project, rt, "opossum", &out).Up(true, "a"); err != nil {
				t.Fatalf("Up: %v\n%s", err, out.String())
			}
			ib, ia := strings.Index(out.String(), "Starting b"), strings.Index(out.String(), "Starting a")
			if ib < 0 || ia < 0 || ib > ia {
				t.Errorf("want b started, and before a, got:\n%s", out.String())
			}
		})
	}
}
