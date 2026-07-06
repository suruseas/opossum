package compose

import (
	"os"
	"path/filepath"
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

	// Nothing found -> an error that names what it looked for.
	if _, err := Discover(t.TempDir()); err == nil {
		t.Error("Discover should error when no compose file is present")
	}
}
