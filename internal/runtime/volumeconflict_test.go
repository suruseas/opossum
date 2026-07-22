package runtime

// Evals for the pieces OPSM-103 relies on: List surfacing each container's named
// volumes (so the orchestrator can find which running container holds a volume),
// and a failed detached Run carrying its stderr (so the cryptic VZError text is
// available to decode).

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestListParsesMountedVolumes(t *testing.T) {
	// Two running containers; the shim answers `ls -a --format json` with the real
	// inspect shape (mounts[].type.volume.name), which List must surface.
	lsJSON := `[` +
		`{"status":{"state":"running"},"configuration":{"id":"web","mounts":[{"type":{"volume":{"name":"proj_data"}}},{"destination":"/etc","type":{}}]}},` +
		`{"status":{"state":"stopped"},"configuration":{"id":"db","mounts":[{"type":{"volume":{"name":"proj_db"}}}]}}` +
		`]`
	shim := filepath.Join(t.TempDir(), "c")
	writeShimFile(t, shim, fmt.Sprintf("#!/bin/sh\ncase \"$1\" in ls) echo %q ;; esac\nexit 0\n", lsJSON))

	r := &Runtime{Bin: shim}
	got := r.List()
	if len(got) != 2 {
		t.Fatalf("expected 2 containers, got %d: %+v", len(got), got)
	}
	byName := map[string][]string{}
	for _, c := range got {
		byName[c.Name] = c.Volumes
	}
	if v := byName["web"]; len(v) != 1 || v[0] != "proj_data" {
		t.Errorf(`web should mount only the named volume "proj_data" (bind mount excluded), got %v`, v)
	}
	if v := byName["db"]; len(v) != 1 || v[0] != "proj_db" {
		t.Errorf(`db should mount "proj_db", got %v`, v)
	}
}

func TestRunDetachedWrapsStderr(t *testing.T) {
	// A failing detached run must return a *RunError carrying the child's stderr, so
	// the orchestrator can inspect the VZError text and decode it.
	shim := filepath.Join(t.TempDir(), "c")
	writeShimFile(t, shim, "#!/bin/sh\necho 'the storage device attachment is invalid' >&2\nexit 1\n")

	r := &Runtime{Bin: shim}
	err := r.Run(RunOptions{Image: "x", Detach: true})
	if err == nil {
		t.Fatal("expected an error from the failing run")
	}
	var re *RunError
	if !errors.As(err, &re) {
		t.Fatalf("expected a *RunError, got %T: %v", err, err)
	}
	if !strings.Contains(re.Stderr, "storage device attachment is invalid") {
		t.Errorf("RunError should carry the child's stderr, got %q", re.Stderr)
	}
}
