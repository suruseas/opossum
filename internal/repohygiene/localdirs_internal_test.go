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
// seen; the pattern that decides has to come from .gitignore, not from
// .git/info/exclude or a global or system setting (those are kept out too),
// and it has to be `/<dir>/`, the whole directory.
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
			// GIT_DIR and GIT_WORK_TREE from the environment (a hook's) would point
			// git at another repository.
			var env []string
			for _, e := range os.Environ() {
				if !strings.HasPrefix(e, "GIT_DIR=") && !strings.HasPrefix(e, "GIT_WORK_TREE=") {
					env = append(env, e)
				}
			}
			cmd.Env = append(env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
			out, err := cmd.Output()
			var exit *exec.ExitError
			switch {
			case err == nil:
			case errors.As(err, &exit) && exit.ExitCode() == 1:
				t.Fatalf(".gitignore does not ignore %s, but the message for a file under %s/ says it does", probe, d)
			default:
				t.Fatalf("git check-ignore: %v", err)
			}
			// <source>:<line>:<pattern>\t<path>. The pattern has to be the whole
			// directory, `/<dir>/`: the message is about any file under it, and a
			// pattern that ignores the probe alone (`*.sh`, `/<dir>/release.sh`)
			// says nothing of the rest.
			fields, _, _ := strings.Cut(string(out), "\t")
			parts := strings.SplitN(fields, ":", 3)
			if len(parts) != 3 || parts[0] != ".gitignore" {
				t.Errorf("%s is ignored by %q, not by .gitignore, which the message names", probe, out)
			} else if parts[2] != "/"+d+"/" {
				t.Errorf("%s is ignored by the pattern %q, not by /%s/ for the whole directory: %s", probe, parts[2], d, out)
			}
		})
	}
}
