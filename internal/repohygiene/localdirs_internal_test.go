package repohygiene

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The message for a file under a local directory says .gitignore ignores it,
// so git has to say a file there is ignored, by .gitignore. git decides, not a
// reading of the file, so a later `!/<dir>/` that takes the directory back is
// seen; and the pattern that decides has to come from .gitignore, not from
// .git/info/exclude or a global or system setting (those are kept out too).
func TestEveryLocalDirIsIgnored(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range localDirs {
		t.Run(d, func(t *testing.T) {
			probe := d + "/release.sh"
			cmd := exec.Command("git", "-c", "core.excludesFile=", "check-ignore", "--no-index", "--verbose", probe)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
			out, err := cmd.Output()
			var exit *exec.ExitError
			switch {
			case err == nil:
			case errors.As(err, &exit) && exit.ExitCode() == 1:
				t.Fatalf(".gitignore does not ignore %s, but the message for a file under %s/ says it does", probe, d)
			default:
				t.Fatalf("git check-ignore: %v", err)
			}
			// <source>:<line>:<pattern>\t<path>
			if source, _, _ := strings.Cut(string(out), ":"); source != ".gitignore" {
				t.Errorf("%s is ignored by %q, not by .gitignore, which the message names: %s", probe, source, out)
			}
		})
	}
}
