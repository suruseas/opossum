package repohygiene

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The message for a file under a local directory says .gitignore ignores it,
// so each one has to be there, as a directory at the top: `/<dir>/` on a line
// of its own.
func TestEveryLocalDirIsIgnored(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	lines := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		lines[strings.TrimSpace(l)] = true
	}
	for _, d := range localDirs {
		t.Run(d, func(t *testing.T) {
			if !lines["/"+d+"/"] {
				t.Errorf(".gitignore has no line /%s/, but the message for a file under %s/ says .gitignore ignores it", d, d)
			}
		})
	}
}
