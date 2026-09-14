// Package shimcontract holds one table of questions every fake `container` CLI
// in this repository must answer the same way — the way the real CLI answers
// them (testdata/real-cli-output.md). There are three fakes: the shell one
// people use by hand (testdata/fake-container.sh, in README), the one the CLI
// tests build (cmd/opossum/testdata/fakeshim), and the one the orchestrator
// tests build (internal/orchestrator/testdata/fakeshim). Each is kept by the
// package that uses it, so a behaviour taught to one and not the others passes
// that package's tests and quietly changes what another package's tests mean —
// and a fake that answers too leniently (a delete that always succeeds) passes
// every assertion that only looks for success. A contract is added here as a
// row; a fake that cannot answer it fails this test.
package shimcontract_test

import (
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// step is one command and what the real CLI does with it: its exit code, a
// piece of its output (stdout and stderr together) that must be there, and one
// that must not. The wording is part of the contract, not just the exit code:
// opossum tells "not there" from "could not be asked" by whether a failed
// inspect says `not found` — and the real CLI has several ways of saying a
// thing is not there, one of which (a volume or network delete) does not
// contain those words. A fake that failed with other words would turn an
// absent container into an unanswered one inside the tests.
type step struct {
	argv  []string
	rc    int
	has   string
	lacks string
}

// A scenario runs its steps in order against one fresh fake with a fresh state
// directory (and env, if it sets any), so a later step can depend on what an
// earlier one did. NAME in an argument or in has/lacks is the scenario's
// container name; OTHER is a second container in the same state that nothing
// in the scenario touches.
//
// Not in the table yet, and known to be wrong: a name never run or created at
// all. Every fake answers `inspect <never-seen>` as a running container where
// the real CLI exits 1 with `container not found` — the opposite answer, on the
// very question opossum uses to tell "not there" from "could not be asked" —
// and lets `volume delete <never-seen>` succeed where the real CLI fails. Many
// tests lean on the first (a test that needs absence sets INSPECT_ABSENT), so
// fixing it means counting them first; it is tracked as an open item, not a
// choice. The table pins what a fake does after it has been told something:
// deleted, stopped, started.
var contract = []struct {
	name  string
	env   []string
	steps []step
}{
	{"a container's life: run, stop, start, delete — and what inspect says at each point", nil, []step{
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"inspect", "NAME"}, has: `"state":"running"`},
		// `stop`'s exit code says only that the name exists; the state is what
		// changed (1.4.1).
		{argv: []string{"stop", "NAME"}},
		{argv: []string{"inspect", "NAME"}, has: `"state":"stopped"`},
		{argv: []string{"start", "NAME"}},
		{argv: []string{"inspect", "NAME"}, has: `"state":"running"`},
		{argv: []string{"delete", "--force", "NAME"}},
		{argv: []string{"inspect", "NAME"}, rc: 1, has: "container not found: NAME"},
	}},
	{"a container that is gone: stop and delete fail, and only then", nil, []step{
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"delete", "--force", "NAME"}},
		{argv: []string{"stop", "NAME"}, rc: 1, has: `notFound: "container with ID NAME not found"`},
		{argv: []string{"delete", "--force", "NAME"}, rc: 1, has: `notFound: "container with ID NAME not found"`},
		// Running the name again makes it there again.
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"inspect", "NAME"}, has: `"state":"running"`},
		{argv: []string{"stop", "NAME"}},
	}},
	{"a stopped container is deleted the way down and destroy delete it: stop, then delete", nil, []step{
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"stop", "NAME"}},
		{argv: []string{"delete", "--force", "NAME"}},
		{argv: []string{"inspect", "NAME"}, rc: 1, has: "container not found: NAME"},
		{argv: []string{"stop", "NAME"}, rc: 1, has: `notFound: "container with ID NAME not found"`},
		{argv: []string{"delete", "--force", "NAME"}, rc: 1, has: `notFound: "container with ID NAME not found"`},
	}},
	{"deleting one container leaves another alone", nil, []step{
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"run", "-d", "--name", "OTHER", "alpine"}},
		{argv: []string{"delete", "--force", "NAME"}},
		{argv: []string{"inspect", "OTHER"}, has: `"state":"running"`},
		{argv: []string{"stop", "OTHER"}},
		{argv: []string{"inspect", "NAME"}, rc: 1, has: "container not found: NAME"},
	}},
	{"a volume that was already there, once deleted, is gone", []string{"VOLUME_LS=pre"}, []step{
		{argv: []string{"volume", "delete", "pre"}},
		{argv: []string{"volume", "delete", "pre"}, rc: 1, has: `failed to delete one or more volumes`, lacks: "not found"},
	}},
	{"a volume that is gone: deleting it again fails", nil, []step{
		{argv: []string{"run", "-d", "-v", "vol1:/d", "--name", "NAME", "alpine"}},
		{argv: []string{"volume", "delete", "vol1"}},
		{argv: []string{"volume", "delete", "vol1"}, rc: 1, has: `failed to delete one or more volumes`, lacks: "not found"},
	}},
	// The runtime and docker compose keep names apart that differ only by `.`
	// and `_`: `demo_.hid` and `demo__hid` are two volumes. A fake that folded
	// one character into the other when remembering a name answered the second
	// as if the first had been deleted.
	{"volumes whose names differ only by a dot and an underscore are two", nil, []step{
		{argv: []string{"run", "-d", "-v", "demo_.hid:/a", "-v", "demo__hid:/b", "--name", "NAME", "alpine"}},
		{argv: []string{"volume", "delete", "demo_.hid"}},
		{argv: []string{"volume", "delete", "demo__hid"}},
		{argv: []string{"volume", "delete", "demo__hid"}, rc: 1, has: `failed to delete one or more volumes`},
	}},
	// Both ways round and for both records: stopping one leaves the other
	// running, and deleting that one leaves the stopped one there.
	{"containers whose names differ only by a dot and an underscore are two", nil, []step{
		{argv: []string{"run", "-d", "--name", "web.demo", "alpine"}},
		{argv: []string{"run", "-d", "--name", "web_demo", "alpine"}},
		{argv: []string{"stop", "web.demo"}},
		{argv: []string{"inspect", "web_demo"}, has: `"state":"running"`},
		{argv: []string{"delete", "--force", "web_demo"}},
		{argv: []string{"inspect", "web.demo"}, has: `"state":"stopped"`},
		{argv: []string{"inspect", "web_demo"}, rc: 1, has: "container not found: web_demo"},
	}},
	// A container name the runtime refuses (1.4.1, `run --name`): fewer than 2 or
	// more than 63 characters, a first character that is not a letter or digit, a
	// later one outside letters, digits, `_`, `.` and `-`; a missing value, or one
	// that is `-` followed by more, is exit 64. Only the flags before the image are
	// the runtime's, and the last `--name` there counts. Not modelled, and in some
	// rows below the exit is the fake's rather than the real CLI's (said where so):
	// `--name=x`, `-e=x`, combined short flags, `--`, `-h`/`--help`/`--version`,
	// `--debug`, a value starting with `-` for another flag (the real CLI calls
	// it missing, exit 64; opossum can pass `--user -1`, #996), and a flag with no
	// value at the end.
	// Each rule has a row that holds and one that breaks it. A fake that ran any
	// name would keep a caller passing one green.
	{"a container name the runtime refuses is refused, and one it takes is run", nil, []step{
		{argv: []string{"run", "-d", "--name", "0first", "alpine"}, lacks: "not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "Xfirst", "alpine"}, lacks: "not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "_first", "alpine"}, rc: 1, has: "Error: container ID _first is not a valid container ID"},
		{argv: []string{"run", "-d", "--name", ".first", "alpine"}, rc: 1, has: "Error: container ID .first is not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "+first", "alpine"}, rc: 1, has: "Error: container ID +first is not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "mid_dle", "alpine"}, lacks: "not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "mid.dle", "alpine"}, lacks: "not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "mid-dle", "alpine"}, lacks: "not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "midZ9", "alpine"}, lacks: "not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "end-", "alpine"}, lacks: "not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "end.", "alpine"}, lacks: "not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "end_", "alpine"}, lacks: "not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "mid+dle", "alpine"}, rc: 1, has: "Error: container ID mid+dle is not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "end+", "alpine"}, rc: 1, has: "Error: container ID end+ is not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "mid dle", "alpine"}, rc: 1, has: "Error: container ID mid dle is not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "mid/dle", "alpine"}, rc: 1, has: "Error: container ID mid/dle is not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "mid:dle", "alpine"}, rc: 1, has: "Error: container ID mid:dle is not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "midé", "alpine"}, rc: 1, has: "Error: container ID midé is not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "", "alpine"}, rc: 1, has: "Error: container ID  is not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "ckkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk", "alpine"}, lacks: "not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "ckkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk", "alpine"}, rc: 1, has: "Error: container ID ckkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk is not a valid container ID"},
		{argv: []string{"run", "--name", "_flagsafter", "-d", "--rm", "alpine"}, rc: 1, has: "Error: container ID _flagsafter is not a valid container ID"},
		{argv: []string{"run", "--rm", "-d", "--name", "okafter", "-e", "A=b", "alpine"}, lacks: "not a valid container ID"},
		{argv: []string{"run", "-d", "alpine"}, lacks: "not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "-dash", "alpine"}, rc: 64, has: "Error: Missing value for '--name <name>'"},
		{argv: []string{"run", "-d", "--name", "--", "alpine"}, rc: 64, has: "Error: Missing value for '--name <name>'"},
		{argv: []string{"run", "-d", "--name"}, rc: 64, has: "Error: Missing value for '--name <name>'"},
		{argv: []string{"run", "-d", "--name", "-", "alpine"}, rc: 1, has: "Error: container ID - is not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "a", "alpine"}, rc: 1, has: "Error: container ID a is not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "7", "alpine"}, rc: 1, has: "Error: container ID 7 is not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "ab", "alpine"}, lacks: "not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "12", "alpine"}, lacks: "not a valid container ID"},
		// After the image the arguments are the process's (the real CLI runs these).
		{argv: []string{"run", "-d", "alpine", "echo", "--name", "_cmdarg"}, lacks: "not a valid container ID"},
		// An empty argument does not start with `-`: it is where the image is, and
		// what follows is the command's. Read as a flag taking a value, it would
		// swallow the `-d` and the `--name` after it would be judged. The exit here
		// is the fake's: the real CLI refuses "" as an image reference (exit 1),
		// which the fakes do not model; the row pins only that the name is not.
		{argv: []string{"run", "--rm", "", "-d", "--name", "_afterempty"}, lacks: "not a valid container ID"},
		// A value-taking flag at the very end is not `--name`: whatever is said
		// about it, it is not that `--name` lacks a value. (The real CLI says the
		// flag's own value is missing, exit 64, which the fakes do not model.)
		{argv: []string{"run", "-d", "-l"}, lacks: "Missing value for '--name"},
		{argv: []string{"run", "-d", "--name", "okimage", "alpine", "echo", "--name", "-cmdarg"}, lacks: "Missing value"},
		// The last `--name` before the image counts.
		{argv: []string{"run", "-d", "--name", "okfirst", "--name", "_second", "alpine"}, rc: 1, has: "Error: container ID _second is not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "_first", "--name", "oksecond", "alpine"}, lacks: "not a valid container ID"},
		// A flag that takes a value takes the next argument (here a plain word);
		// one that takes none does not.
		{argv: []string{"run", "-l", "tier", "--name", "_afterlabel", "alpine"}, rc: 1, has: "Error: container ID _afterlabel is not a valid container ID"},
		{argv: []string{"run", "--rm", "--init", "--read-only", "--ssh", "--rosetta", "-i", "-t", "--name", "_afterbools", "alpine"}, rc: 1, has: "Error: container ID _afterbools is not a valid container ID"},
	}},
	// A refused run records nothing: a name marked gone stays gone, as the real
	// CLI (which never made the container) answers it.
	{"a refused container name leaves what was recorded alone", nil, []step{
		{argv: []string{"run", "-d", "--name", "ab+c", "alpine"}, rc: 1, has: "Error: container ID ab+c is not a valid container ID"},
		{argv: []string{"delete", "--force", "ab+c"}},
		{argv: []string{"run", "-d", "--name", "ab+c", "alpine"}, rc: 1, has: "Error: container ID ab+c is not a valid container ID"},
		{argv: []string{"inspect", "ab+c"}, rc: 1, has: "container not found: ab+c"},
	}},
	// A `-v` source container 1.4.1 reads as a volume name, and refuses when it
	// does not start with a letter or digit, holds anything but letters, digits,
	// `_`, `.` and `-`, or is longer than 255 characters (measured with `run --rm
	// -v <source>:/x`). A source holding `/` is a path, an empty one an anonymous
	// volume. Nothing is created, not even a valid volume before the refused one.
	{"a volume name the runtime refuses: characters, length, where it sits", nil, []step{
		// The first character.
		{argv: []string{"run", "--rm", "-v", "_first:/x", "alpine"}, rc: 1, has: "Error: invalid volume name '_first': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		{argv: []string{"run", "--rm", "-v", ".first:/x", "alpine"}, rc: 1, has: "Error: invalid volume name '.first': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		{argv: []string{"run", "--rm", "-v", "+first:/x", "alpine"}, rc: 1, has: "Error: invalid volume name '+first': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		{argv: []string{"run", "--rm", "-v", "9first:/x", "alpine"}, lacks: "invalid volume name"},
		{argv: []string{"run", "--rm", "-v", "Xfirst:/x", "alpine"}, lacks: "invalid volume name"},
		// The characters after it, in the middle and at the end.
		{argv: []string{"run", "--rm", "-v", "mid_dle:/x", "alpine"}, lacks: "invalid volume name"},
		{argv: []string{"run", "--rm", "-v", "mid.dle:/x", "alpine"}, lacks: "invalid volume name"},
		{argv: []string{"run", "--rm", "-v", "mid-dle:/x", "alpine"}, lacks: "invalid volume name"},
		{argv: []string{"run", "--rm", "-v", "end_:/x", "alpine"}, lacks: "invalid volume name"},
		{argv: []string{"run", "--rm", "-v", "end.:/x", "alpine"}, lacks: "invalid volume name"},
		{argv: []string{"run", "--rm", "-v", "end-:/x", "alpine"}, lacks: "invalid volume name"},
		{argv: []string{"run", "--rm", "-v", "UpperCase:/x", "alpine"}, lacks: "invalid volume name"},
		{argv: []string{"run", "--rm", "-v", "mid+dle:/x", "alpine"}, rc: 1, has: "Error: invalid volume name 'mid+dle': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		{argv: []string{"run", "--rm", "-v", "mid dle:/x", "alpine"}, rc: 1, has: "Error: invalid volume name 'mid dle': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		{argv: []string{"run", "--rm", "-v", "mid@dle:/x", "alpine"}, rc: 1, has: "Error: invalid volume name 'mid@dle': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		{argv: []string{"run", "--rm", "-v", "midé:/x", "alpine"}, rc: 1, has: "Error: invalid volume name 'midé': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		{argv: []string{"run", "--rm", "-v", "end+:/x", "alpine"}, rc: 1, has: "Error: invalid volume name 'end+': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		// Length: one character is a name, 255 are, 256 are not.
		{argv: []string{"run", "--rm", "-v", "q:/x", "alpine"}, lacks: "invalid volume name"},
		{argv: []string{"run", "--rm", "-v", "vvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvv:/x", "alpine"}, lacks: "invalid volume name"},
		{argv: []string{"run", "--rm", "-v", "vvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvv:/x", "alpine"}, rc: 1, has: "Error: invalid volume name 'vvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvv': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		// With the options opossum adds after the target.
		{argv: []string{"run", "--rm", "-v", "ro+name:/x:ro", "alpine"}, rc: 1, has: "Error: invalid volume name 'ro+name': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		{argv: []string{"run", "--rm", "-v", "roname:/x:ro", "alpine"}, lacks: "invalid volume name"},
		// Not names: a path (holding `/`, as opossum passes a bind mount) and an
		// empty source. The exit of the two path rows is the fake's: the real CLI
		// refuses a path that is not there (`Error: path '<path>' does not exist`,
		// exit 1), which the fakes do not model; the rows pin only that the source
		// is not judged as a volume name.
		{argv: []string{"run", "--rm", "-v", "/abs/p+th:/x", "alpine"}, lacks: "invalid volume name"},
		{argv: []string{"run", "--rm", "-v", "rel/p+th:/x", "alpine"}, lacks: "invalid volume name"},
		{argv: []string{"run", "--rm", "-v", ":/x", "alpine"}, lacks: "invalid volume name"},
		// Among several: the first refused one is named, wherever it is.
		{argv: []string{"run", "--rm", "-v", "okfirst:/x", "-v", "bad+second:/y", "alpine"}, rc: 1, has: "Error: invalid volume name 'bad+second': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		{argv: []string{"run", "--rm", "-v", "bad+first:/x", "-v", "oksecond:/y", "alpine"}, rc: 1, has: "Error: invalid volume name 'bad+first': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		{argv: []string{"run", "--rm", "-v", "bad+one:/x", "-v", "bad+two:/y", "alpine"}, rc: 1, has: "Error: invalid volume name 'bad+one': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		{argv: []string{"run", "--rm", "-v", "okfirst:/x", "-v", "/abs:/y", "-v", "bad+third:/z", "alpine"}, rc: 1, has: "Error: invalid volume name 'bad+third': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		// Between other flags, and right after `run`: a value-taking flag takes the
		// next argument (a plain word here), and a `-v` after the image belongs to
		// the process. opossum can start with a value-taking flag (`--name`,
		// `--user`, `--shm-size`) when it passes none of `-d`, `-i`, `-t`.
		{argv: []string{"run", "-d", "-e", "A=b", "--name", "okname", "-v", "bad+vol:/x", "-l", "tier", "alpine"}, rc: 1, has: "Error: invalid volume name 'bad+vol': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		{argv: []string{"run", "--name", "okname", "-v", "bad+vol:/x", "alpine"}, rc: 1, has: "Error: invalid volume name 'bad+vol': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		{argv: []string{"run", "-l", "tier", "-v", "bad+vol:/x", "alpine"}, rc: 1, has: "Error: invalid volume name 'bad+vol': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		{argv: []string{"run", "-v", "bad+first:/x", "--rm", "alpine"}, rc: 1, has: "Error: invalid volume name 'bad+first': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		{argv: []string{"run", "--rm", "alpine", "echo", "-v", "bad+cmdarg:/x"}, lacks: "invalid volume name"},
		// A refused container name is said first, before or after the `-v`.
		{argv: []string{"run", "--rm", "--name", "_badname", "-v", "bad+vol:/x", "alpine"}, rc: 1, has: "Error: container ID _badname is not a valid container ID"},
		{argv: []string{"run", "--rm", "-v", "bad+vol:/x", "--name", "_badname", "alpine"}, rc: 1, has: "Error: container ID _badname is not a valid container ID"},
	}},
	// A refused volume records nothing: a name marked gone stays gone.
	{"a refused volume name leaves what was recorded alone", nil, []step{
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"delete", "--force", "NAME"}},
		{argv: []string{"run", "-d", "--name", "NAME", "-v", "bad+vol:/x", "alpine"}, rc: 1, has: "Error: invalid volume name 'bad+vol': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		{argv: []string{"inspect", "NAME"}, rc: 1, has: "container not found: NAME"},
	}},
	// The same answers where a range would take other letters (en_US.UTF-8 for the
	// shell fake): upper case is a letter, `é` is not.
	{"a container name is judged the same in a UTF-8 locale", []string{"LC_ALL=en_US.UTF-8", "LANG=en_US.UTF-8"}, []step{
		{argv: []string{"run", "-d", "--name", "midé", "alpine"}, rc: 1, has: "Error: container ID midé is not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "éfirst", "alpine"}, rc: 1, has: "Error: container ID éfirst is not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "MidZ", "alpine"}, lacks: "not a valid container ID"},
		{argv: []string{"run", "-d", "--name", "Zfirst", "alpine"}, lacks: "not a valid container ID"},
	}},
	{"a volume name is judged the same in a UTF-8 locale", []string{"LC_ALL=en_US.UTF-8", "LANG=en_US.UTF-8"}, []step{
		{argv: []string{"run", "--rm", "-v", "midé:/x", "alpine"}, rc: 1, has: "Error: invalid volume name 'midé': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		{argv: []string{"run", "--rm", "-v", "éfirst:/x", "alpine"}, rc: 1, has: "Error: invalid volume name 'éfirst': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		{argv: []string{"run", "--rm", "-v", "midÅ:/x", "alpine"}, rc: 1, has: "Error: invalid volume name 'midÅ': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
		{argv: []string{"run", "--rm", "-v", "MidZ:/x", "alpine"}, lacks: "invalid volume name"},
		{argv: []string{"run", "--rm", "-v", "Zfirst:/x", "alpine"}, lacks: "invalid volume name"},
	}},
	{"the daemon answers `system status --format json`", nil, []step{
		{argv: []string{"system", "status", "--format", "json"}, has: `"status":"running"`},
	}},
	// A network name the runtime refuses (1.4.1): upper case, a character outside
	// a-z, 0-9, `.`, `_` and `-`, a first or last character that is not a
	// lower-case letter or digit, nothing at all, more than 63 characters; and
	// no name (exit 64, the argument is missing). opossum passes the name last,
	// and a fake reads the last argument as the name. A fake that made any name
	// would keep a caller passing one green.
	{"a network name the runtime refuses is refused, and one it takes is made", nil, []step{
		{argv: []string{"network", "create", "demo-back"}, has: "demo-back"},
		{argv: []string{"network", "create", "demo-a..b"}, has: "demo-a..b"},
		{argv: []string{"network", "create", "0demo_z9"}, has: "0demo_z9"},
		{argv: []string{"network", "create", "9"}, has: "9"},
		{argv: []string{"network", "create", "demo-kkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk"}, has: "demo-kkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk"},
		{argv: []string{"network", "create", "--internal", "--label", "tier=back", "demo-lab"}, has: "demo-lab", lacks: "--internal"},
		{argv: []string{"network", "create", "demo-Back"}, rc: 1, has: "Error: invalid network name: demo-Back"},
		{argv: []string{"network", "create", "Xdemo"}, rc: 1, has: "Error: invalid network name: Xdemo"},
		{argv: []string{"network", "create", ".demo"}, rc: 1, has: "Error: invalid network name: .demo"},
		{argv: []string{"network", "create", "_demo"}, rc: 1, has: "Error: invalid network name: _demo"},
		{argv: []string{"network", "create", "+demo"}, rc: 1, has: "Error: invalid network name: +demo"},
		{argv: []string{"network", "create", ""}, rc: 1, has: "Error: invalid network name: "},
		{argv: []string{"network", "create", "demo-a+b"}, rc: 1, has: "Error: invalid network name: demo-a+b"},
		{argv: []string{"network", "create", "demo-x-"}, rc: 1, has: "Error: invalid network name: demo-x-"},
		{argv: []string{"network", "create", "demo-x."}, rc: 1, has: "Error: invalid network name: demo-x."},
		{argv: []string{"network", "create", "demo-x_"}, rc: 1, has: "Error: invalid network name: demo-x_"},
		{argv: []string{"network", "create", "demo-kkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk"}, rc: 1, has: "Error: invalid network name: demo-kkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk"},
		{argv: []string{"network", "create", "--internal", "--label", "tier=back", "demo-Lab"}, rc: 1, has: "Error: invalid network name: demo-Lab"},
		{argv: []string{"network", "create", "--label", "tier=back", "--internal", "demo-mid+"}, rc: 1, has: "Error: invalid network name: demo-mid+"},
		{argv: []string{"network", "create"}, rc: 64, has: "Error: Missing expected argument '<name>'"},
		{argv: []string{"network", "create", "--internal"}, rc: 64, has: "Error: Missing expected argument '<name>'"},
	}},
	// The same answers where `[a-z]` would take upper case (en_US.UTF-8 for the
	// shell fake): the rule must not lean on the locale.
	{"a network name with upper case is refused in a UTF-8 locale too", []string{"LC_ALL=en_US.UTF-8", "LANG=en_US.UTF-8"}, []step{
		{argv: []string{"network", "create", "demo-Back"}, rc: 1, has: "Error: invalid network name: demo-Back"},
		{argv: []string{"network", "create", "Bdemo"}, rc: 1, has: "Error: invalid network name: Bdemo"},
		{argv: []string{"network", "create", "demoZ"}, rc: 1, has: "Error: invalid network name: demoZ"},
		{argv: []string{"network", "create", "demo-back"}, has: "demo-back"},
	}},
}

// Every character a container name may hold, first and later: a fake that
// spells the set out (the shell one does) cannot drop one unseen.
func init() {
	const alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	var steps []step
	for _, c := range alnum {
		name := string(c) + "x"
		steps = append(steps, step{argv: []string{"run", "-d", "--name", name, "alpine"}, lacks: "not a valid container ID"})
	}
	for _, later := range []string{"x" + alnum[:31], "x" + alnum[31:] + "_.-"} {
		steps = append(steps, step{argv: []string{"run", "-d", "--name", later, "alpine"}, lacks: "not a valid container ID"})
	}
	// Each flag that takes no value, right before `--name`: read as taking one,
	// it would swallow `--name` and the refused name would go unseen.
	for _, flag := range []string{"-d", "--detach", "-i", "--interactive", "-t", "--tty", "--init", "--no-dns", "--read-only", "--rm", "--remove", "--rosetta", "--ssh", "--virtualization"} {
		steps = append(steps, step{argv: []string{"run", flag, "--name", "_after" + strings.TrimLeft(flag, "-"), "alpine"}, rc: 1,
			has: "Error: container ID _after" + strings.TrimLeft(flag, "-") + " is not a valid container ID"})
	}
	contract = append(contract, struct {
		name  string
		env   []string
		steps []step
	}{"every letter and digit is taken first and later in a container name, and after each flag that takes no value", nil, steps})

	const volumeRule = "must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"
	steps = nil
	for _, c := range alnum {
		steps = append(steps, step{argv: []string{"run", "--rm", "-v", string(c) + ":/x", "alpine"}, lacks: "invalid volume name"})
		steps = append(steps, step{argv: []string{"run", "--rm", "-v", string(c) + "x:/x", "alpine"}, lacks: "invalid volume name"})
	}
	for _, later := range []string{"x" + alnum[:31], "x" + alnum[31:] + "_.-"} {
		steps = append(steps, step{argv: []string{"run", "--rm", "-v", later + ":/x", "alpine"}, lacks: "invalid volume name"})
	}
	// Each flag that takes no value, right before `-v`: read as taking one, it
	// would swallow `-v` and the refused name would go unseen.
	for _, flag := range []string{"-d", "--detach", "-i", "--interactive", "-t", "--tty", "--init", "--no-dns", "--read-only", "--rm", "--remove", "--rosetta", "--ssh", "--virtualization"} {
		name := "_after" + strings.TrimLeft(flag, "-")
		steps = append(steps, step{argv: []string{"run", flag, "-v", name + ":/x", "alpine"}, rc: 1,
			has: "Error: invalid volume name '" + name + "': " + volumeRule})
	}
	contract = append(contract, struct {
		name  string
		env   []string
		steps []step
	}{"every letter and digit is taken first and later in a volume name, and after each flag that takes no value", nil, steps})
}

func TestEveryFakeAnswersTheContract(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	fakes := map[string]string{"testdata/fake-container.sh": filepath.Join(root, "testdata", "fake-container.sh")}
	for _, pkg := range []string{"cmd/opossum/testdata/fakeshim", "internal/orchestrator/testdata/fakeshim"} {
		out := filepath.Join(bin, strings.ReplaceAll(pkg, "/", "_"))
		cmd := exec.Command("go", "build", "-o", out, "./"+pkg)
		cmd.Dir = root
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("building %s: %v\n%s", pkg, err, b)
		}
		fakes[pkg] = out
	}
	// Three different programs, not three names for one: the two Go fakes are
	// built from different packages and must not come out the same binary.
	if len(fakes) != 3 {
		t.Fatalf("want the three fakes, got %v", fakes)
	}
	if a, b := digest(t, fakes["cmd/opossum/testdata/fakeshim"]), digest(t, fakes["internal/orchestrator/testdata/fakeshim"]); a == b {
		t.Fatalf("the two Go fakes built to the same binary — one of them is not being checked")
	}
	for fake, path := range fakes {
		for _, sc := range contract {
			t.Run(fake+"/"+sc.name, func(t *testing.T) {
				state := t.TempDir()
				fill := strings.NewReplacer("NAME", "probe.demo.opossum", "OTHER", "probe.other.opossum")
				for i, st := range sc.steps {
					argv := make([]string, len(st.argv))
					for j, a := range st.argv {
						argv[j] = fill.Replace(a)
					}
					cmd := exec.Command(path, argv...)
					// Only what the fake needs: an inherited knob (INSPECT_ABSENT, a
					// STATE_DIR of the caller's) must not decide the answer.
					cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"),
						"STATE_DIR=" + state, "FAKE_LOG=" + filepath.Join(state, "calls.log")}, sc.env...)
					out, err := cmd.CombinedOutput()
					rc := 0
					if ee, ok := err.(*exec.ExitError); ok {
						rc = ee.ExitCode()
					} else if err != nil {
						t.Fatalf("step %d %v: %v", i+1, argv, err)
					}
					if rc != st.rc {
						t.Errorf("step %d %v: exit %d, the real CLI exits %d; output:\n%s", i+1, argv, rc, st.rc, out)
					}
					if want := fill.Replace(st.has); want != "" && !strings.Contains(string(out), want) {
						t.Errorf("step %d %v: want %q in the output, got:\n%s", i+1, argv, want, out)
					}
					if bad := fill.Replace(st.lacks); bad != "" && strings.Contains(string(out), bad) {
						t.Errorf("step %d %v: the real CLI does not say %q here, got:\n%s", i+1, argv, bad, out)
					}
				}
			})
		}
	}
}

func digest(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}
