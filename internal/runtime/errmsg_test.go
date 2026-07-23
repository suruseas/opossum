package runtime

// Error-quality evals, batch 2 (#277): best-effort teardown no longer swallows a
// real failure silently (a volume/image that won't delete now warns with a next
// step), while a clean re-run (already gone) stays quiet. Plus the Docker-import
// load failure carries a hint.

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResourceInUse(t *testing.T) {
	// The real in-use message (captured from `container volume delete` of a held
	// volume) must be recognized; the real already-gone message must NOT (else a
	// clean `down -v` re-run warns spuriously — both use the generic "failed to
	// delete one or more" shape, so only "in use" distinguishes them).
	inUse := `failed to delete volume: ["id": v, "error": invalidArgument: "volume 'v' is currently in use and cannot be accessed by another container, or deleted"]`
	if !resourceInUse(inUse) {
		t.Errorf("the real in-use message should be recognized: %q", inUse)
	}
	for _, gone := range []string{
		`Error: failed to delete one or more volumes: ["no-such-volume-xyz"]`,
		`Error: failed to delete one or more images: ["no-such-image-xyz:latest"]`,
		"",
	} {
		if resourceInUse(gone) {
			t.Errorf("an already-gone/benign message must not read as in-use: %q", gone)
		}
	}
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns what was
// written — for the teardown warnings, which print to stderr directly.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = old
	var buf strings.Builder
	io.Copy(&buf, r)
	return buf.String()
}

func TestDeleteVolumeWarnsWhenInUse(t *testing.T) {
	// `volume delete` fails because the volume is in use → warn with a next step.
	shim := filepath.Join(t.TempDir(), "c")
	writeShimFile(t, shim, "#!/bin/sh\necho \"error: volume 'data' is currently in use\" >&2\necho \"volume 'data' is currently in use\"\nexit 1\n")
	r := &Runtime{Bin: shim}
	out := captureStderr(t, func() { r.DeleteVolume("data") })
	if !strings.Contains(out, "could not remove volume") || !strings.Contains(out, "container volume delete data") {
		t.Errorf("an in-use volume-delete failure should warn with a next step, got: %q", out)
	}
}

func TestDeleteVolumeSilentWhenAlreadyGone(t *testing.T) {
	// A clean re-run of `down -v`: the real already-gone shape (generic, no "in
	// use") must stay silent so teardown re-runs don't spew warnings.
	shim := filepath.Join(t.TempDir(), "c")
	writeShimFile(t, shim, "#!/bin/sh\necho 'Error: failed to delete one or more volumes: [\"data\"]' >&2\nexit 1\n")
	r := &Runtime{Bin: shim}
	if out := captureStderr(t, func() { r.DeleteVolume("data") }); out != "" {
		t.Errorf("an already-gone volume should not warn, got: %q", out)
	}
}

func TestDeleteImageWarnsWhenInUse(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "c")
	writeShimFile(t, shim, "#!/bin/sh\necho 'error: image is currently in use' >&2\necho 'image is currently in use'\nexit 1\n")
	r := &Runtime{Bin: shim}
	out := captureStderr(t, func() { r.DeleteImage("web:latest") })
	if !strings.Contains(out, "could not remove image") || !strings.Contains(out, "container image delete web:latest") {
		t.Errorf("an in-use image-delete failure should warn with a next step, got: %q", out)
	}
}

func TestDeleteImageSilentWhenAlreadyGone(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "c")
	writeShimFile(t, shim, "#!/bin/sh\necho 'Error: failed to delete one or more images: [\"web:latest\"]' >&2\nexit 1\n")
	r := &Runtime{Bin: shim}
	if out := captureStderr(t, func() { r.DeleteImage("web:latest") }); out != "" {
		t.Errorf("an already-gone image should not warn, got: %q", out)
	}
}

func TestImportLoadFailureHasHint(t *testing.T) {
	dir := t.TempDir()
	docker := filepath.Join(dir, "docker")
	container := filepath.Join(dir, "container")
	// docker save streams bytes fine; container load fails.
	writeShimFile(t, docker, "#!/bin/sh\n[ \"$1 $2\" = \"image save\" ] && echo TAR\nexit 0\n")
	writeShimFile(t, container, "#!/bin/sh\n[ \"$1 $2\" = \"image load\" ] && exit 1\nexit 0\n")
	r := &Runtime{Bin: container, DockerBin: docker}
	err := r.ImportFromDocker("proj-web:latest", "proj-web:latest")
	if err == nil {
		t.Fatal("expected an import load failure")
	}
	if s := err.Error(); !strings.Contains(s, "loading") || !strings.Contains(s, "opossum doctor") {
		t.Errorf("load failure should hint at the runtime health, got: %s", s)
	}
}
