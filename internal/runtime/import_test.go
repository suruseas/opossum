package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeShimFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestImportFromDockerMissingCLI(t *testing.T) {
	r := &Runtime{Bin: "/bin/true", DockerBin: filepath.Join(t.TempDir(), "no-such-docker")}
	err := r.ImportFromDocker("proj-web:latest", "proj-web:latest")
	if err == nil || !strings.Contains(err.Error(), "docker CLI isn't installed") {
		t.Fatalf("expected a docker-missing error, got %v", err)
	}
}

// The image bytes from `docker image save` actually reach `container image load`
// (not just that the two commands were invoked).
func TestImportFromDockerPipesData(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	capture := filepath.Join(dir, "capture")
	docker := filepath.Join(dir, "docker")
	container := filepath.Join(dir, "container")
	writeShimFile(t, docker, fmt.Sprintf("#!/bin/sh\necho \"docker $*\" >> %s\n[ \"$1 $2\" = \"image save\" ] && yes TARLINE | head -n 5000\nexit 0\n", log))
	writeShimFile(t, container, fmt.Sprintf("#!/bin/sh\necho \"container $*\" >> %s\n[ \"$1 $2\" = \"image load\" ] && cat > %s\nexit 0\n", log, capture))

	r := &Runtime{Bin: container, DockerBin: docker}
	if err := r.ImportFromDocker("proj-web:latest", "proj-web:latest"); err != nil {
		t.Fatalf("ImportFromDocker: %v", err)
	}
	got, err := os.ReadFile(capture)
	if err != nil || len(got) == 0 || !strings.Contains(string(got), "TARLINE") {
		t.Errorf("container image load did not receive docker save's output (%d bytes, err %v)", len(got), err)
	}
	l, _ := os.ReadFile(log)
	if s := string(l); !strings.Contains(s, "docker image save proj-web:latest") || !strings.Contains(s, "container image load") {
		t.Errorf("unexpected calls: %s", s)
	}
}

// A load failure returns promptly instead of deadlocking, even for an image
// larger than the OS pipe buffer, and reports the load error.
func TestImportFromDockerLoadFailsNoHang(t *testing.T) {
	dir := t.TempDir()
	docker := filepath.Join(dir, "docker")
	container := filepath.Join(dir, "container")
	// save streams ~256 KB (past the ~64 KB pipe buffer); load exits at once
	// without reading — save must see EPIPE, not block forever.
	writeShimFile(t, docker, "#!/bin/sh\n[ \"$1 $2\" = \"image save\" ] && dd if=/dev/zero bs=1024 count=256 2>/dev/null\nexit 0\n")
	writeShimFile(t, container, "#!/bin/sh\nexit 1\n")

	r := &Runtime{Bin: container, DockerBin: docker}
	done := make(chan error, 1)
	go func() { done <- r.ImportFromDocker("proj-web:latest", "proj-web:latest") }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "loading") {
			t.Errorf("expected a load error, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ImportFromDocker deadlocked on a failed load")
	}
}

func TestImportFromDockerSaveFails(t *testing.T) {
	dir := t.TempDir()
	docker := filepath.Join(dir, "docker")
	container := filepath.Join(dir, "container")
	writeShimFile(t, docker, "#!/bin/sh\necho 'No such image' >&2\nexit 1\n")
	writeShimFile(t, container, "#!/bin/sh\ncat >/dev/null\nexit 0\n")
	r := &Runtime{Bin: container, DockerBin: docker}
	if err := r.ImportFromDocker("proj-web:latest", "proj-web:latest"); err == nil || !strings.Contains(err.Error(), "exporting") {
		t.Errorf("expected an export error, got %v", err)
	}
}

// A service that builds to a custom `image:` name is loaded under that name, then
// retagged to what `up` expects.
func TestImportFromDockerRetags(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	docker := filepath.Join(dir, "docker")
	container := filepath.Join(dir, "container")
	writeShimFile(t, docker, "#!/bin/sh\n[ \"$1 $2\" = \"image save\" ] && echo TAR\nexit 0\n")
	writeShimFile(t, container, fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %s\n[ \"$1 $2\" = \"image load\" ] && cat >/dev/null\nexit 0\n", log))
	r := &Runtime{Bin: container, DockerBin: docker}
	if err := r.ImportFromDocker("myco/web:1.2", "proj-web:latest"); err != nil {
		t.Fatalf("ImportFromDocker: %v", err)
	}
	if l, _ := os.ReadFile(log); !strings.Contains(string(l), "image tag myco/web:1.2 proj-web:latest") {
		t.Errorf("expected a retag, got: %s", string(l))
	}
}
