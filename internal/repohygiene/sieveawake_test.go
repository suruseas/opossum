package repohygiene_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The sieve builds its image only when the image is not there. Asked of a
// daemon that Docker Desktop's Resource Saver had stopped, `docker image
// inspect` answered "No such image" without waking it, and the build that
// followed — a registry round-trip, the one that times out on that machine —
// was what woke it: the image had been there all along (#706). So the
// question has to be one that wakes the daemon first. The stand-in docker
// below answers inspect "no" until something has listed images, the way the
// stopped daemon did, and records every call.
//
// Both sides are asked, because the predicate can be wrong in both
// directions: one that never wakes the daemon builds needlessly, and one that
// is always true never builds again — `image ls` without -q prints a header
// line even for a repository it does not have, and the stand-in prints one
// too, so that mutation is caught here rather than on the next `docker image
// rm`.
func TestTheSieveWakesTheDaemonBeforeAskingForItsImage(t *testing.T) {
	t.Run("the image is there, so nothing is built", func(t *testing.T) {
		calls, out := makeSieveWithAStoppedDaemon(t, true)
		if built(calls) {
			t.Errorf("the sieve built its image although the image was there: the daemon was asked "+
				"before anything woke it, calls:\n%s", calls)
		}
		if !ran(calls) {
			t.Errorf("the sieve never ran the gate, calls:\n%s\nout:\n%s", calls, out)
		}
	})
	t.Run("the image is not there, so it is built", func(t *testing.T) {
		calls, out := makeSieveWithAStoppedDaemon(t, false)
		if !built(calls) {
			t.Errorf("the image was not there and the sieve did not build it: the question is "+
				"answered yes whatever the daemon holds, calls:\n%s\nout:\n%s", calls, out)
		}
		if !ran(calls) {
			t.Errorf("the sieve never ran the gate, calls:\n%s\nout:\n%s", calls, out)
		}
	})
}

// makeSieveWithAStoppedDaemon runs `make sieve` in this repository with a
// stand-in docker first on PATH, and returns the calls it recorded and make's
// output. The stand-in plays a daemon Resource Saver has stopped: `info`
// answers, `image inspect` says "No such image" until something has listed
// images, and `image ls` wakes it — and lists the image only when present,
// -q printing ids alone and the plain form a header line whatever is there.
func makeSieveWithAStoppedDaemon(t *testing.T, present bool) (calls, out string) {
	t.Helper()
	root := repoRoot(t)
	shim := t.TempDir()
	log := filepath.Join(shim, "calls.log")
	awake := filepath.Join(shim, "awake")
	write(t, filepath.Join(shim, "docker"), "#!/bin/sh\n"+
		"echo \"$*\" >> \"$DOCKER_LOG\"\n"+
		"case \"$1 $2\" in\n"+
		"  'image inspect')\n"+
		"    [ -e \"$DOCKER_AWAKE\" ] && exit 0\n"+
		"    echo 'Error response from daemon: No such image: opossum-sieve' >&2; exit 1 ;;\n"+
		"  'image ls')\n"+
		"    touch \"$DOCKER_AWAKE\"\n"+
		"    case \" $* \" in\n"+
		"      *' -q '*) [ -n \"$DOCKER_HAS_IMAGE\" ] && echo 61ac2374d0b6 ;;\n"+
		"      *) echo 'REPOSITORY      TAG       IMAGE ID       CREATED       SIZE'\n"+
		"         [ -n \"$DOCKER_HAS_IMAGE\" ] && echo 'opossum-sieve   latest    61ac2374d0b6   8 days ago    1.19GB' ;;\n"+
		"    esac\n"+
		"    exit 0 ;;\n"+
		"  'run '*) cat >/dev/null; exit 0 ;;\n"+
		"esac\n"+
		"exit 0\n")
	if err := os.Chmod(filepath.Join(shim, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("make", "sieve")
	cmd.Dir = root
	// /usr/bin and /bin for make, git and sh; the shim first, for docker.
	env := []string{
		"PATH=" + shim + string(os.PathListSeparator) + "/usr/bin:/bin",
		"HOME=" + shim,
		"DOCKER_LOG=" + log,
		"DOCKER_AWAKE=" + awake,
	}
	if present {
		env = append(env, "DOCKER_HAS_IMAGE=1")
	}
	cmd.Env = env
	got, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make sieve: %v\n%s", err, got)
	}
	made, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("the stand-in docker was never called: %v\n%s", err, got)
	}
	return string(made), string(got)
}

func built(calls string) bool { return hasCall(calls, "build ") }
func ran(calls string) bool   { return hasCall(calls, "run ") }

func hasCall(calls, prefix string) bool {
	for _, line := range strings.Split(calls, "\n") {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// The pre-push hook says, before the sieve runs, whether the image is there —
// so that a build that follows can be told from one that should not have. That
// line is worth exactly as much as its agreement with the Makefile's own
// question: asked differently, it reports on a decision the Makefile is not
// about to make, which is how #706 took seven data points to read. So the two
// ask in the same words.
func TestTheHookAsksForTheSieveImageTheWayTheMakefileDoes(t *testing.T) {
	root := repoRoot(t)
	const question = `docker image ls -q opossum-sieve:latest 2>/dev/null`
	for _, f := range []string{"Makefile", ".githooks/pre-push"} {
		if !strings.Contains(read(t, root, f), question) {
			t.Errorf("%s does not ask %q; the hook's evidence line and the Makefile's build decision "+
				"have to ask the same question, or the line reports on a decision that is not being made", f, question)
		}
	}
}
