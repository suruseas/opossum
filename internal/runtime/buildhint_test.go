package runtime

import (
	"strings"
	"testing"
)

func TestBuildErrorDetector(t *testing.T) {
	cacheHint := func(h string) bool {
		return strings.Contains(h, "delete --force") && !strings.Contains(h, "start --cpus")
	}
	resourceHint := func(h string) bool { return strings.Contains(h, "start --cpus 4 --memory 8g") }
	diskHint := func(h string) bool {
		return strings.Contains(h, "ran out of disk space") && strings.Contains(h, "image prune -f")
	}

	t.Run("cache corruption", func(t *testing.T) {
		d := &buildErrorDetector{}
		d.Write([]byte("#8 failed to load cache key: unable to read root manifest\n"))
		if !cacheHint(d.hint()) {
			t.Errorf("cache-corruption hint expected, got: %q", d.hint())
		}
	})
	t.Run("resource exhaustion", func(t *testing.T) {
		d := &buildErrorDetector{}
		d.Write([]byte("Error: unavailable: rpc error: code = Unavailable desc = error reading from server: EOF\n"))
		if !resourceHint(d.hint()) {
			t.Errorf("resource hint expected, got: %q", d.hint())
		}
	})
	t.Run("disk full", func(t *testing.T) {
		d := &buildErrorDetector{}
		d.Write([]byte("#12 exporting layers: write /var/lib/.../blob: no space left on device\n"))
		if !diskHint(d.hint()) {
			t.Errorf("disk-full hint expected, got: %q", d.hint())
		}
	})
	t.Run("disk full outranks resource exhaustion", func(t *testing.T) {
		// A full volume makes the builder fail downstream (rpc/EOF), so the disk
		// remedy must win — growing the builder would make ENOSPC worse.
		d := &buildErrorDetector{}
		d.Write([]byte("no space left on device\nrpc error: code = Unavailable desc = error reading from server: EOF\n"))
		if !diskHint(d.hint()) {
			t.Errorf("disk-full hint should win over the resource hint, got: %q", d.hint())
		}
	})
	t.Run("plain build error gets no hint", func(t *testing.T) {
		d := &buildErrorDetector{}
		d.Write([]byte(`#5 ERROR: process "/bin/sh -c bogus" did not complete successfully: exit code 127` + "\n"))
		if h := d.hint(); h != "" {
			t.Errorf("no hint expected for an ordinary build error, got: %q", h)
		}
	})
	t.Run("signature split across writes", func(t *testing.T) {
		d := &buildErrorDetector{}
		d.Write([]byte("rpc error: unable to read "))
		d.Write([]byte("root manifest: ..."))
		if !cacheHint(d.hint()) {
			t.Error("a signature straddling two writes should still match")
		}
	})
	t.Run("both signatures pick the resource hint", func(t *testing.T) {
		d := &buildErrorDetector{}
		d.Write([]byte("failed to load cache key\nrpc error: code = Unavailable desc = error reading from server: EOF\n"))
		if !resourceHint(d.hint()) {
			t.Errorf("resource hint (the superset remedy) should win when both fire, got: %q", d.hint())
		}
	})
}

// Build turns a known builder failure into an actionable hint on the error, and
// leaves an ordinary build failure untouched.
func TestBuildAppendsHintOnKnownFailure(t *testing.T) {
	r := replayShim(t, "#4 transferring context\nfailed to load cache key: unable to read root manifest\n", 1)
	err := r.Build(BuildOptions{Tag: "x:1", Context: t.TempDir()})
	if err == nil {
		t.Fatal("expected a build error")
	}
	if !strings.Contains(err.Error(), "container builder delete --force") {
		t.Errorf("build error should carry the recovery hint, got: %v", err)
	}
}

func TestBuildResourceHintOnConnectionDrop(t *testing.T) {
	r := replayShim(t, "#8 resolve image...\nError: unavailable: rpc error: code = Unavailable desc = error reading from server: EOF\n", 1)
	err := r.Build(BuildOptions{Tag: "x:1", Context: t.TempDir()})
	if err == nil {
		t.Fatal("expected a build error")
	}
	if !strings.Contains(err.Error(), "start --cpus 4 --memory 8g") {
		t.Errorf("build error should carry the resource hint, got: %v", err)
	}
}

func TestBuildDiskFullHint(t *testing.T) {
	r := replayShim(t, "#12 exporting to image\nfailed to solve: write blob: no space left on device\n", 1)
	err := r.Build(BuildOptions{Tag: "x:1", Context: t.TempDir()})
	if err == nil {
		t.Fatal("expected a build error")
	}
	if !strings.Contains(err.Error(), "ran out of disk space") || !strings.Contains(err.Error(), "image prune -f") {
		t.Errorf("build error should carry the disk-full recovery hint, got: %v", err)
	}
}

func TestBuildNoHintOnPlainFailure(t *testing.T) {
	r := replayShim(t, "#5 ERROR: exit code 1\n", 1)
	err := r.Build(BuildOptions{Tag: "x:1", Context: t.TempDir()})
	if err == nil {
		t.Fatal("expected a build error")
	}
	if strings.Contains(err.Error(), "hint:") {
		t.Errorf("no hint expected for an ordinary build failure, got: %v", err)
	}
}
