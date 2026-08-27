package orchestrator

import (
	"strings"
	"testing"
)

// The two sentences opossum uses to say it moved a data directory, word for word.
//
// A sweep that exchanges arguments of the same kind found twelve exchanges
// between them — six each — and not one changed anything a test could see. Both
// sentences do the same job: they put four things side by side and say which is
// which. The service, the path inside the container, the host directory it used
// to be, and the volume it is now. Exchanged, each still reads as a sentence
// about a data directory and sends the reader somewhere else.
//
// One goes on the screen (the summary of what `--from-docker-compose` did) and
// one goes into the generated overlay, where it outlives the run.
//
// The overlay one was half-guarded, and by something on its own line rather
// than by a neighbour: TestPlanOverlayCommentContract counts the marker that
// opens it, so the four exchanges involving that marker were already caught
// while the six between the values it introduces were not. Nothing looked at
// the sentence as a sentence.
//
// Every value here is distinct on purpose — a different service name, container
// path, host path and volume name. With any two the same, half of these
// exchanges produce the identical string and the check cannot see them.
func TestTheDataDirectoryMoveIsWordForWord(t *testing.T) {
	body, changes := planFor(t, `
name: demo
services:
  ca$$he:
    image: mysql:8
    volumes:
      - ./my$$data:/var/lib/mysql
`)
	if len(changes) != 1 {
		t.Fatalf("one adaptation, got %+v", changes)
	}
	// On the screen. The order is what the sentence is for: which service, what
	// moved, where from, where to.
	// The service name and the host path both carry a `$`, so that the escaping
	// is visible on two of the values rather than one. The summary goes on a
	// terminal and keeps them as written; the overlay is a compose file and
	// doubles them, or the next reader of that file gets a variable expansion
	// where a name should be.
	//
	// Not every esc() in this block is reached from here: the container path is
	// fixed (it has to be a data directory the adaptation recognises), and the
	// NOTE line further down and the generated volumes entry take values this
	// fixture does not put a `$` into. Those are still unguarded — see #559.
	want := `service "ca$he": data directory /var/lib/mysql moved from the host path ./my$data to a named volume "ca-he-data"`
	if changes[0].Summary != want {
		t.Errorf("summary =\n %q\nwant\n %q", changes[0].Summary, want)
	}

	// And in the overlay, which the reader keeps. Written the other way round
	// this says the volume is being replaced by the host path — the opposite of
	// what happened, in a file that stays behind after the run that made it.
	wantComment := `  # [opossum --from-docker-compose] service "ca$$he": /var/lib/mysql now uses the named volume "ca-he-data" instead of the host path "./my$$data".`
	// Once, and as a whole line. Picking the last line that mentions the volume
	// would pass for an overlay that said it twice, or said it somewhere the
	// reader does not look.
	if n := strings.Count(body, wantComment+"\n"); n != 1 {
		var got []string
		for _, line := range strings.Split(body, "\n") {
			if strings.Contains(line, "now uses the named volume") {
				got = append(got, line)
			}
		}
		t.Errorf("the overlay should carry this line exactly once, got %d:\n %q\nwant\n %q", n, got, wantComment)
	}
}

// The note says the same of a mount named on either end.
//
// The refusal's copy of this question is covered above with one-sided fixtures;
// the note's is not. Every note fixture in this package writes
// `/var/run/docker.sock:/var/run/docker.sock`, so narrowing the note's call to
// one end passes — which is the same hole the refusal had, in the place that
// owns the predicate.
func TestTheNoteReadsEitherEndOfTheMount(t *testing.T) {
	for _, c := range []struct {
		name, mount string
		want        bool
	}{
		{"named on the host side only", "/somewhere/docker.sock:/run/inner.sock", true},
		{"named on the container side only", "/somewhere/outer.sock:/var/run/docker.sock", true},
		// And it is the file name that decides, not the directory it sits in.
		// This is the same case the pre-flight refusal covers, and it belongs
		// here too: the refusal no longer asks who owns the socket, so this
		// predicate is what decides the name, and pinning its answer here is
		// what keeps the note from drifting off the case it was written for.
		{
			"a socket whose directory merely says docker.sock",
			"/somewhere/docker.sock.d/S.gpg-agent:/run/user/1000/gnupg/S.gpg-agent",
			false,
		},
		{
			"a socket whose name merely ends in docker.sock",
			"/somewhere/my-docker.sock:/run/my-docker.sock",
			false,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, changes := planFor(t, "\nname: demo\nservices:\n  ci:\n    image: someci\n    volumes:\n      - "+c.mount+"\n")
			var codes []string
			for _, ch := range changes {
				codes = append(codes, ch.Code)
			}
			found := false
			for _, code := range codes {
				if code == "OPSM-204" {
					found = true
				}
			}
			if found != c.want {
				t.Errorf("noted as a Docker socket = %v, want %v; codes %v", found, c.want, codes)
			}
		})
	}
}
