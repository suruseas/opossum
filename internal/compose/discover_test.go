package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscover(t *testing.T) {
	write := func(dir, name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("services:\n  a:\n    image: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A docker-compose.yml alone is discovered.
	dir := t.TempDir()
	write(dir, "docker-compose.yml")
	if got, err := Discover(dir); err != nil || filepath.Base(got) != "docker-compose.yml" {
		t.Errorf("Discover = %q, %v; want docker-compose.yml", got, err)
	}

	// Precedence: compose.yaml wins over docker-compose.yml.
	dir2 := t.TempDir()
	write(dir2, "docker-compose.yml")
	write(dir2, "compose.yaml")
	if got, err := Discover(dir2); err != nil || filepath.Base(got) != "compose.yaml" {
		t.Errorf("Discover precedence = %q, %v; want compose.yaml", got, err)
	}

	// Nothing found -> an error that names what it looked for. The comment said
	// that before the check did: `err == nil` reads neither the directory nor
	// the list, and both are strings on one format call, so swapping them cost
	// nothing.
	empty := t.TempDir()
	_, err := Discover(empty)
	want := fmt.Sprintf("no compose file found in %q (looked for %s)", empty, strings.Join(DefaultFileNames, ", "))
	if err == nil || err.Error() != want {
		t.Errorf("Discover on an empty directory = %v, want %s", err, want)
	}
}

func TestDiscoverOpossumOverlay(t *testing.T) {
	write := func(dir, name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("services:\n  a:\n    image: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// No overlay -> "".
	if got := DiscoverOpossumOverlay(t.TempDir()); got != "" {
		t.Errorf("DiscoverOpossumOverlay with no overlay = %q; want \"\"", got)
	}

	// compose.opossum.yaml is discovered.
	dir := t.TempDir()
	write(dir, "compose.opossum.yaml")
	if got := DiscoverOpossumOverlay(dir); filepath.Base(got) != "compose.opossum.yaml" {
		t.Errorf("DiscoverOpossumOverlay = %q; want compose.opossum.yaml", got)
	}

	// The .yaml spelling wins over .yml when both are present.
	dir2 := t.TempDir()
	write(dir2, "compose.opossum.yml")
	write(dir2, "compose.opossum.yaml")
	if got := DiscoverOpossumOverlay(dir2); filepath.Base(got) != "compose.opossum.yaml" {
		t.Errorf("DiscoverOpossumOverlay precedence = %q; want compose.opossum.yaml", got)
	}

	// A directory named like the overlay is ignored (must be a file).
	dir3 := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir3, "compose.opossum.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DiscoverOpossumOverlay(dir3); got != "" {
		t.Errorf("DiscoverOpossumOverlay with a same-named directory = %q; want \"\"", got)
	}
}
