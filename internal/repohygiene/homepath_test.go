package repohygiene_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/repohygiene"
)

func TestHomeLeakFindsAHomeDirectory(t *testing.T) {
	for _, c := range []struct{ name, content, want string }{
		{"a captured lsof line", "probe  50290 root  3u  /Users/someone/projects/x\n", "/Users/someone"},
		{"the path alone at the end of a line", "appRoot            /Users/someone\n", "/Users/someone"},
		{"a one-letter account", "cd /Users/j/work\n", "/Users/j"},
		{"redacted, which is what a capture should carry", "appRoot            /Users/<user>\n", ""},
		{"a placeholder in prose", "home (`/Users/...`) succeeded\n", ""},
		{"a home inside an image, which is the service's own", `"PGDATA=/home/postgres/pgdata/data"`, ""},
		{"ordinary text", "the mount is left alone\n", ""},
	} {
		if got := repohygiene.HomeLeak([]byte(c.content)); got != c.want {
			t.Errorf("%s: HomeLeak(%q) = %q, want %q", c.name, c.content, got, c.want)
		}
	}
}

// The whole file is read, not a prefix: a capture prints the path wherever the
// command it recorded happened to.
func TestHomeLeakReadsPastTheHead(t *testing.T) {
	content := strings.Repeat("a line of output\n", 5000) + "cd /Users/someone/x\n"
	if got := repohygiene.HomeLeak([]byte(content)); got == "" {
		t.Error("a path far into a long capture should still be found")
	}
}

// The ratchet. It is green today with nothing exempted, which is the moment to
// put it in: the two that got through were removed by hand, and this is what
// stops the third.
func TestNoTrackedFileCarriesAHomeDirectory(t *testing.T) {
	root := repoRoot(t)
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("listing tracked files: %v", err)
	}
	names := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	read := 0
	for _, name := range names {
		if name == "" || name == "internal/repohygiene/homepath_test.go" {
			continue // this file names the shape it is looking for
		}
		fi, err := os.Stat(filepath.Join(root, name))
		if err != nil || fi.IsDir() || fi.Size() > repohygiene.MaxTrackedBytes {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		read++
		if leak := repohygiene.HomeLeak(b); leak != "" {
			t.Errorf("%s carries %q — a capture keeps whatever the machine printed, and this is somebody's home directory; redact it as /Users/<user>", name, leak)
		}
	}
	if read == 0 {
		t.Fatal("no tracked files were read; this check is passing on nothing")
	}
}
