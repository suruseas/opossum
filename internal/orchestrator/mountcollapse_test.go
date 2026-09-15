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

// A `volumes:` entry a later one replaces at its target is not looked at by
// the checks that follow: an `external: true` volume that does not exist is
// refused (`[OPSM-210]`) only while its entry is the one at the target, as
// docker compose v5.5.0 loads such a file. The control keeps the entry and is
// refused.
func TestAReplacedExternalVolumeEntryIsNotChecked(t *testing.T) {
	for _, tc := range []struct {
		name, volumes string
		refused       bool
	}{
		{"replaced by a later tmpfs at its target", "      - ext:/t\n      - {type: tmpfs, target: /t}\n", false},
		{"kept, the tmpfs at another target", "      - ext:/t\n      - {type: tmpfs, target: /u}\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, log := fakeShim(t)
			setShimEnv(rt, "VOLUME_LS=other", "INSPECT_ABSENT=web.demo.opossum")
			p := loadCompose(t, "name: demo\nservices:\n  web:\n    image: alpine:3.20\n    volumes:\n"+tc.volumes+"volumes:\n  ext: {external: true}\n")
			err := orchestrator.New(p, rt, "opossum", &bytes.Buffer{}).Up(true)
			ran := runLine(log()) >= 0
			if tc.refused {
				if err == nil || !strings.Contains(err.Error(), "[OPSM-210]") || ran {
					t.Fatalf("want the missing external volume refused before the run, got err %v, ran %v", err, ran)
				}
				return
			}
			if err != nil || !ran {
				t.Fatalf("want the service run without the replaced entry being checked, got err %v, ran %v\n%v", err, ran, log())
			}
		})
	}
}

// loadCompose loads a compose file written to a temp dir, so the loader's own
// reading of the mounts (the collapse) is what the orchestrator sees.
func loadCompose(t *testing.T, body string) *compose.Project {
	t.Helper()
	path := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := compose.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return p
}
