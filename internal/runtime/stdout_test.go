package runtime

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// When Out is set, a streamed child's stdout goes there (so `run` can keep the
// real stdout for the one-off body); stderr is unaffected.
func TestStreamStdoutHonorsOut(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "shim")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\necho to-stdout\necho to-stderr >&2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	r := &Runtime{Bin: shim, Out: &out}
	if err := r.stream("x"); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if !strings.Contains(out.String(), "to-stdout") {
		t.Errorf("child stdout should be routed to Out; got %q", out.String())
	}
	if strings.Contains(out.String(), "to-stderr") {
		t.Errorf("child stderr should not be routed to Out; got %q", out.String())
	}
}

// Build's stdout (e.g. `container build`'s final image tag) also honors Out even
// though it's teed through the build-error detector — guards a regression where
// the tee branch hardcoded os.Stdout, leaking the tag into a `run` one-off's
// stdout.
func TestBuildStdoutHonorsOut(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "shim")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\necho image-tag-to-stdout\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	r := &Runtime{Bin: shim, Out: &out}
	if err := r.Build(BuildOptions{Tag: "x:1", Context: t.TempDir()}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(out.String(), "image-tag-to-stdout") {
		t.Errorf("build stdout should honor Out even with the tee; got %q", out.String())
	}
}
