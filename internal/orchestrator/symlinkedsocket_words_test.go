package orchestrator_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// What opossum says when a bind source is a symlink to a socket, word for word.
//
// Four values on one call — the diagnostic code, the service, the host path and
// the path inside the container — and a sweep found all six exchanges between
// them invisible. The existing test for this failure looks for `[OPSM-109]`,
// `errno 95` and the resolved path with Contains, which every one of those
// exchanges satisfies: the code can land where the service belongs and the
// sentence still holds all three substrings.
//
// The code is the half that matters most. It is what AGENTS.md is indexed by
// and what a person pastes into a search; a state or a path standing in its
// place does not merely read oddly, it takes the search away.
//
// Every value is distinct on purpose, and none is a substring of another: the
// service is not in either path, and the two paths differ in their last element
// rather than only in their directory.
func TestTheSymlinkedSocketRefusalIsWordForWord(t *testing.T) {
	sock, dir := socketAt(t)
	link := filepath.Join(dir, "agent.sock")
	if err := os.Symlink(sock, link); err != nil {
		t.Fatal(err)
	}

	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"gpg": {Image: "someimage", Volumes: []string{link + ":/run/user/1000/gnupg/S.gpg-agent"}},
	})
	var out bytes.Buffer
	err := orchestrator.New(p, rt, "opossum", &out).Up(true)
	if err == nil {
		t.Fatal("a mount that cannot work must fail the up rather than be attempted")
	}

	// What the link resolves to, as the message computes it. On macOS this is
	// not the path the test created — /var is itself a link to /private/var —
	// and writing the created path here would pin the wrong half of the very
	// distinction this message is about.
	resolved, rerr := filepath.EvalSymlinks(link)
	if rerr != nil {
		t.Fatal(rerr)
	}
	want := fmt.Sprintf("[OPSM-109] service %q mounts %s at %s, and that cannot be done here: "+
		"the path is a symlink to a socket, which this runtime refuses (`mount failed with errno 95`)\n"+
		"  a socket reached by its own path mounts fine, and so does a symlink to a file or a "+
		"directory — it is the combination that fails\n"+
		"  mounting %s — what the link points at — works, so that is the way out "+
		"if the service really needs this socket",
		"gpg", link, "/run/user/1000/gnupg/S.gpg-agent", resolved)
	if err.Error() != want {
		t.Errorf("err =\n%q\nwant\n%q", err.Error(), want)
	}
}

// The way out is the last thing in the message, on its own line, with nothing
// after it.
//
// The hint is appended with %s from a variable the code leaves empty when the
// link cannot be resolved, so the join between the body and the hint is where
// this sentence can come apart — a missing newline runs them together, an extra
// one leaves a gap, and a Contains check reads all three the same. This pins the
// ending as an ending.
//
// The unresolvable case itself is not reproduced here: getting EvalSymlinks to
// fail on a link that Lstat and Stat have both just succeeded on takes a race,
// and a test that needs one is a test that reports on the weather. What is
// pinned is the join, which is the part that has a shape either way.
func TestTheWayOutIsTheEndOfTheMessage(t *testing.T) {
	sock, dir := socketAt(t)
	link := filepath.Join(dir, "agent.sock")
	if err := os.Symlink(sock, link); err != nil {
		t.Fatal(err)
	}
	rt, _ := fakeShim(t)
	p := project("demo", map[string]*compose.Service{
		"gpg": {Image: "someimage", Volumes: []string{link + ":/run/agent.sock"}},
	})
	var out bytes.Buffer
	err := orchestrator.New(p, rt, "opossum", &out).Up(true)
	if err == nil {
		t.Fatal("a mount that cannot work must fail the up")
	}
	// The hint is one line, joined by a newline, and nothing follows it.
	resolved, rerr := filepath.EvalSymlinks(link)
	if rerr != nil {
		t.Fatal(rerr)
	}
	tail := "\n  mounting " + resolved + " — what the link points at — works, so that is the way out " +
		"if the service really needs this socket"
	if got := err.Error(); len(got) < len(tail) || got[len(got)-len(tail):] != tail {
		t.Errorf("the message should end with the way out and nothing after it:\n%q", got)
	}
}

// The way out does not depend on who owns the socket.
//
// It used to. A Docker socket was refused the general advice — mount what the
// link points at — on the reading that the mount would succeed and leave the
// service exactly as unable to work, because Apple container is not the Docker
// daemon. The first half is true and the second does not follow: `/var/run/
// docker.sock` is a symlink on the machines that have Docker Desktop, and a
// container started here reaches that daemon through the resolved path. It was
// measured (#597), and it made the special case wrong in exactly the situation
// that produced it.
//
// So the special case is gone, and this says so: the same offer, whichever
// socket it is. Put it back and these go red.
//
// What is still worth saying about Docker's — that the daemon answering there
// is not the one running these containers — is said by the note, once. This
// asserts what opossum says, not that mounting works; whether a host socket is
// reachable from inside a container was a separate question until #597 measured
// it for this one; what a session socket mount reaches here has not been
// measured (`OPSM-106`).
func TestTheWayOutDoesNotDependOnWhoOwnsTheSocket(t *testing.T) {
	for _, c := range []struct {
		name     string
		linkName string
		target   string
	}{
		// The Docker socket, in the three shapes the old special case looked
		// for: either end, or both. Each one used to get a different message.
		{"named on the host side", "docker.sock", "/run/inner.sock"},
		{"named on the container side", "outer.sock", "/var/run/docker.sock"},
		{"named on both", "docker.sock", "/var/run/docker.sock"},
		// And sockets that were never Docker's. The last two are the shapes the
		// note's predicate had to be narrowed for; the refusal no longer asks it
		// at all, so what they hold here is only that the general way out did
		// not change — `adaptwords_internal_test.go` is what guards the
		// narrowing itself.
		{"an agent socket", "S.gpg-agent", "/run/user/1000/gnupg/S.gpg-agent"},
		{"a directory that merely says docker.sock", "docker.sock.d/S.gpg-agent", "/run/user/1000/gnupg/S.gpg-agent"},
		{"a name that merely ends in docker.sock", "my-docker.sock", "/run/my-docker.sock"},
	} {
		t.Run(c.name, func(t *testing.T) {
			sock, dir := socketAt(t)
			link := filepath.Join(dir, c.linkName)
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(sock, link); err != nil {
				t.Fatal(err)
			}
			rt, _ := fakeShim(t)
			p := project("demo", map[string]*compose.Service{
				"svc": {Image: "someimage", Volumes: []string{link + ":" + c.target}},
			})
			var out bytes.Buffer
			err := orchestrator.New(p, rt, "opossum", &out).Up(true)
			if err == nil {
				t.Fatal("a mount that cannot work must fail the up")
			}
			// The resolved value, not the path we made: on macOS the temp dir
			// is reached through a link, and the message names what
			// EvalSymlinks returned.
			resolved, rerr := filepath.EvalSymlinks(link)
			if rerr != nil {
				t.Fatal(rerr)
			}
			want := refusal(link, c.target, resolved)
			if err.Error() != want {
				t.Errorf("err =\n%q\nwant\n%q", err.Error(), want)
			}
		})
	}
}

// refusal is the whole message for a socket reached through a symlink, built
// the way a reader sees it.
func refusal(src, target, resolved string) string {
	return fmt.Sprintf("[OPSM-109] service %q mounts %s at %s, and that cannot be done here: "+
		"the path is a symlink to a socket, which this runtime refuses (`mount failed with errno 95`)\n"+
		"  a socket reached by its own path mounts fine, and so does a symlink to a file or a "+
		"directory — it is the combination that fails\n"+
		"  mounting %s — what the link points at — works, so that is the way out if the service "+
		"really needs this socket",
		"svc", src, target, resolved)
}
