package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
)

// A crashed container's report decodes its logs into hints, and some name the
// mount they are about. A volume whose name starts with `.` has to be named as
// written there, and get the hints a plainly named volume gets. `up` only
// reaches that decode when a container crashes, which the transcript test in
// dotvolume_test.go does not make happen, so every recorded real crash log is
// fed to it here for a service mounting `.pg` and one mounting `zpg`, at each
// data directory those logs are about; the two hints must be the same with the
// name swapped, and neither may show the loader's spelling.
func TestADotNamedVolumeGetsTheCrashHintsAPlainOneDoes(t *testing.T) {
	logs, err := filepath.Glob(filepath.Join("..", "..", "testdata", "error-wordings", "*.txt"))
	if err != nil || len(logs) == 0 {
		t.Fatalf("no recorded logs to feed the decode (%v)", err)
	}
	service := func(t *testing.T, image, name, target string) *compose.Service {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "compose.yaml")
		body := "name: demo\nservices:\n  svc:\n    image: " + image + "\n    volumes:\n      - {type: volume, source: " + name + ", target: " + target + "}\nvolumes:\n  " + name + ": {}\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := compose.Load(path)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		return p.Services["svc"]
	}
	hinted, named := 0, 0
	for _, mount := range []struct{ image, target string }{
		{"postgres:18", "/var/lib/postgresql/data"},
		{"postgres:18", "/var/lib/postgresql"},
		{"mysql:8", "/var/lib/mysql"},
		{"redis:7", "/data"},
	} {
		dot := service(t, mount.image, ".pg", mount.target)
		plain := service(t, mount.image, "zpg", mount.target)
		for _, log := range logs {
			body, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			d, p := hintFor(t, string(body), dot), hintFor(t, string(body), plain)
			if p != "" {
				hinted++
			}
			if strings.Contains(p, "zpg") {
				named++
			}
			if compose.ShowsLoaderSpelling(d) {
				t.Errorf("%s at %s, %s: the hint shows the loader's spelling: %q", mount.image, mount.target, filepath.Base(log), d)
			}
			if swapped := strings.ReplaceAll(d, ".pg", "zpg"); swapped != p {
				t.Errorf("%s at %s, %s: the dot name gets a different hint:\n--- .pg (name swapped)\n%s\n--- zpg\n%s", mount.image, mount.target, filepath.Base(log), swapped, p)
			}
		}
	}
	// A decode that hinted nothing for either name would make every case agree.
	if hinted == 0 {
		t.Fatal("no recorded log produced a hint for the plain name, so nothing was compared")
	}
	// And one that names the volume, or the name was never on the line.
	if named == 0 {
		t.Fatal("no hint named the volume, so the name was never compared")
	}
}
