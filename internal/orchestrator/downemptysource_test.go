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

// A short mount with nothing before its first colon (`:/es`, which `${DATA}:/es` gives when
// DATA is not set) is refused when the file is read, as docker compose refuses it (#1637), but
// an earlier version started such a project, taking the empty source for an anonymous volume.
// Taking that project down reads the file and goes on past the mount, and `down -v` has to
// remove the anonymous volume the earlier version made for it — not read the empty source as a
// host path, which would leave the volume behind. This is the row the load-time refusal took
// away from TestAMountRunsAsWhatItLoadedAs (#1649): that test loads through the strict reader.
func TestDownRemovesTheAnonymousVolumeOfAnEmptySourceMount(t *testing.T) {
	for _, tc := range []struct {
		name, entry, volume string
	}{
		{"an empty source", ":/es", "demo_web_es_"},
		{"an empty source with a mode", ":/es:ro", "demo_web_es_"},
		{"an anonymous volume written the usual way", "/anon", "demo_web_anon_"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "compose.yaml")
			body := "name: demo\nservices:\n  web:\n    image: alpine\n    volumes: [\"" + tc.entry + "\"]\n"
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := compose.LoadFilesEnvDirSoft([]string{path}, nil, "")
			if err != nil {
				t.Fatalf("taking down reads the file and goes on, got: %v", err)
			}
			rt, log := fakeShim(t)
			var out bytes.Buffer
			o := orchestrator.New(p, rt, "opossum", &out)
			if err := o.Down(true, "", false); err != nil {
				t.Fatalf("down -v: %v", err)
			}
			if got := indexOfPrefix(log(), "volume delete "+tc.volume); got < 0 {
				t.Errorf("down -v should remove %s*, got:\n%s", tc.volume, strings.Join(log(), "\n"))
			}
		})
	}
}

func indexOfPrefix(lines []string, prefix string) int {
	for i, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return i
		}
	}
	return -1
}
