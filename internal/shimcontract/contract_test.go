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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
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
	// signal, when set, is sent to the command once it has written its first
	// output, and rc is how it then ends: an exit code of its own, or -1 when
	// the signal ended it.
	signal syscall.Signal
	// stays, when set, is how long the command must still be running, having
	// written nothing if it has nothing to write; signal is sent after it
	// instead of after the first output.
	stays time.Duration
	// within, when set, is how soon the command must end by itself.
	within time.Duration
}

// A scenario runs its steps in order against one fresh fake with a fresh state
// directory (and env, if it sets any), so a later step can depend on what an
// earlier one did. NAME in an argument or in has/lacks is the scenario's
// container name; OTHER is a second container in the same state that nothing
// in the scenario touches.
//
// Held back behind a knob, and known to be wrong without it: a name never run or created at
// all. By default every fake answers `inspect <never-seen>` as a running container where the
// real CLI exits 1 with `container not found` — the opposite answer, on the very question
// opossum uses to tell "not there" from "could not be asked" — and lets `volume delete
// <never-seen>` succeed where the real CLI fails. Many tests lean on the first (14 tests of
// cmd/opossum, 55 subtests, with the answer changed outright; counted in #1551), so the default
// is kept and $INSPECT_STRICT turns the real answer on; the table pins it with the knob set.
// The table pins what a fake does after it has been told something: deleted, stopped, started.
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
	// `exec` and `stats` of a container this fake saw deleted: the real CLI
	// refuses both (1.4.1: `exec` — `Error: get failed: container <name> not
	// found`; `stats` — `Error: no such container: <name>`, testdata/real-cli-output.md).
	// `logs` is asked here too as the control that already worked, so a fake
	// that answered all three the same way for a container never made cannot
	// pass by coincidence.
	{"exec, logs and stats all refuse a container this fake saw deleted", nil, []step{
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"delete", "--force", "NAME"}},
		{argv: []string{"exec", "NAME", "true"}, rc: 1, has: "container NAME not found"},
		{argv: []string{"logs", "NAME"}, rc: 1, has: "container with ID NAME not found"},
		{argv: []string{"stats", "--no-stream", "--format", "json", "NAME"}, rc: 1, has: "no such container: NAME"},
	}},
	// A published port comes back as the run wrote it: the host port, the
	// container port, the protocol, and the address — kept as the address it
	// names rather than folded to the wildcard. A range is ONE entry at its low
	// port carrying a `count`, and a single port carries `count` 1 (measured on
	// container 1.4.1).
	//
	// Asked here because a fake that answers one fixed mapping for every
	// container passes every test that only needs a container to exist, and
	// quietly turns "which port does THIS one hold" into a constant — a
	// question opossum asks whenever a host port moves between services.
	{"a container publishes the ports its run was given", nil, []step{
		{argv: []string{"run", "-d", "--name", "NAME", "-p", "47080:80", "alpine"}},
		{argv: []string{"inspect", "NAME"}, has: `"hostPort":47080`},
		{argv: []string{"inspect", "NAME"}, has: `"containerPort":80,"count"`},
		{argv: []string{"inspect", "NAME"}, has: `"count":1`},
		// The defaults, spelled out: a spec that names no address is on the
		// wildcard, and one that names no protocol is tcp.
		{argv: []string{"inspect", "NAME"}, has: `"hostAddress":"0.0.0.0"`},
		{argv: []string{"inspect", "NAME"}, has: `"proto":"tcp"`},
		// And nothing else. A fixture that answered its historical fixed
		// mapping BESIDE what the run published would pass every `has` above
		// while giving this container a port nobody asked for.
		{argv: []string{"inspect", "NAME"}, lacks: `8080`},
		{argv: []string{"delete", "--force", "NAME"}},
		// Two entries are two answers, in the order the run gave them (measured
		// on container 1.4.1: the long and short spellings of the flag are not
		// told apart, and the order is the order of the arguments — including
		// when the arguments are not in port order).
		//
		// Three of them, and not in port order either way round: with two
		// entries there are only two arrangements, so "the order the run gave"
		// and "sorted by port" agree on one of them whichever way the fixture
		// is written — a fake that sorted descending passed a two-entry row
		// written descending. Three in an order that is neither ascending nor
		// descending is the smallest fixture that tells the two apart.
		{argv: []string{"run", "-d", "--name", "NAME", "-p", "47087:87", "-p", "47086:86", "-p", "47088:88", "alpine"}},
		{argv: []string{"inspect", "NAME"}, has: `"hostPort":47086`},
		{argv: []string{"inspect", "NAME"}, has: `"hostPort":47087`},
		{argv: []string{"inspect", "NAME"}, has: `"hostPort":47088`},
		// And in that order: said as the seams between them, naming both sides
		// of each, because `has` on the numbers alone passes whichever way they
		// are arranged.
		{argv: []string{"inspect", "NAME"}, has: `"hostPort":47087,"proto":"tcp"},{"containerPort":86`},
		{argv: []string{"inspect", "NAME"}, has: `"hostPort":47086,"proto":"tcp"},{"containerPort":88`},
		{argv: []string{"delete", "--force", "NAME"}},
		// The long spelling of the same flag is the same flag. On a port no
		// other row uses: a correct fixture overwrites what it recorded on
		// every run, but a `delete` does not clear it, so a fixture that both
		// ignores this flag AND stops overwriting can answer this row from the
		// earlier row's leftovers — and the row passes without the flag ever
		// being read. A number of its own makes the row stand on itself.
		{argv: []string{"run", "-d", "--name", "NAME", "--publish", "47090:90", "alpine"}},
		{argv: []string{"inspect", "NAME"}, has: `"hostPort":47090`},
		{argv: []string{"delete", "--force", "NAME"}},
		// A protocol is settled in lower case, as the runtime settles it.
		{argv: []string{"run", "-d", "--name", "NAME", "-p", "47089:89/UDP", "alpine"}},
		{argv: []string{"inspect", "NAME"}, has: `"proto":"udp"`},
		{argv: []string{"delete", "--force", "NAME"}},
		// A run with no `-p` publishes nothing, and says so with an empty list —
		// not with some port of its own.
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"inspect", "NAME"}, has: `"publishedPorts":[]`},
		{argv: []string{"delete", "--force", "NAME"}},
		{argv: []string{"run", "-d", "--name", "NAME", "-p", "127.0.0.1:47081:81/udp", "alpine"}},
		{argv: []string{"inspect", "NAME"}, has: `"hostAddress":"127.0.0.1"`},
		{argv: []string{"inspect", "NAME"}, has: `"proto":"udp"`},
		{argv: []string{"delete", "--force", "NAME"}},
		// A `-p` whose two sides name different numbers of ports is refused at the
		// run, whichever side is the range (container 1.4.1, measured 2026-09-24:
		// `Error: publish host and container port counts are not equal: <spelling>`,
		// exit 1) — where docker compose takes some of these (#1254). The next
		// row is the control: ranges of one length are taken.
		{argv: []string{"run", "-d", "--name", "NAME", "-p", "47010-47012:80", "alpine"}, rc: 1, has: "publish host and container port counts are not equal: 47010-47012:80"},
		{argv: []string{"run", "-d", "--name", "NAME", "-p", "47020:80-82", "alpine"}, rc: 1, has: "counts are not equal: 47020:80-82"},
		{argv: []string{"run", "-d", "--name", "NAME", "-p", "47030-47031:80-83", "alpine"}, rc: 1, has: "counts are not equal: 47030-47031:80-83"},
		// With an address and a protocol in front and behind, on either family:
		// the sides are the last two fields, and the number of colons in an
		// address is not a reason to read them wrong.
		{argv: []string{"run", "-d", "--name", "NAME", "-p", "127.0.0.1:47010-47012:80/udp", "alpine"}, rc: 1, has: "counts are not equal: 47010-47012:80", lacks: "127.0.0.1"},
		{argv: []string{"run", "-d", "--name", "NAME", "-p", "127.0.0.1:47010-47012:80/udp", "alpine"}, rc: 1, lacks: "/udp"},
		{argv: []string{"run", "-d", "--name", "NAME", "-p", "[::1]:47010-47012:80", "alpine"}, rc: 1, has: "counts are not equal: 47010-47012:80", lacks: "[::1]"},
		{argv: []string{"run", "-d", "--name", "NAME", "-p", "0.0.0.0:47050:80-81", "alpine"}, rc: 1, has: "counts are not equal: 47050:80-81", lacks: "0.0.0.0"},
		{argv: []string{"run", "-d", "--name", "NAME", "-p", "[::1]:47090-47092:90-92/udp", "alpine"}},
		{argv: []string{"delete", "--force", "NAME"}},
		{argv: []string{"run", "-d", "--name", "NAME", "-p", "47082-47084:82-84", "alpine"}},
		{argv: []string{"inspect", "NAME"}, has: `"count":3`},
		// The ports are the low end and nothing else — said with what follows
		// the number, because `"hostPort":47082` alone is a prefix of
		// `"hostPort":47082-47084` and would pass a fixture that never folded
		// the range at all.
		{argv: []string{"inspect", "NAME"}, has: `"hostPort":47082,"proto"`},
		{argv: []string{"inspect", "NAME"}, has: `{"containerPort":82,"count"`},
		// And the range is not carried across as written.
		{argv: []string{"inspect", "NAME"}, lacks: `47082-`},
		{argv: []string{"inspect", "NAME"}, lacks: `82-84`},
		// Nor are the other ports of the range entries of their own.
		{argv: []string{"inspect", "NAME"}, lacks: `"hostPort":47083`},
		{argv: []string{"inspect", "NAME"}, lacks: `"hostPort":47084`},
		{argv: []string{"inspect", "NAME"}, lacks: `"containerPort":83`},
		{argv: []string{"inspect", "NAME"}, lacks: `"containerPort":84`},
		{argv: []string{"delete", "--force", "NAME"}},
		{argv: []string{"run", "-d", "--name", "NAME", "-p", "[::1]:47085:85", "alpine"}},
		{argv: []string{"inspect", "NAME"}, has: `"hostAddress":"::1"`},
		{argv: []string{"delete", "--force", "NAME"}},
	}},
	// The project label is what the run gave the container, as `-l` (the
	// spelling opossum passes) or `--label`; a run without one leaves none
	// (1.4.1: `configuration.labels`, testdata/real-cli-output.md).
	{"a container carries the project label its run gave it, and none without one", nil, []step{
		{argv: []string{"run", "-d", "--name", "NAME", "-l", "opossum.project=demo", "alpine"}},
		{argv: []string{"inspect", "NAME"}, has: `"opossum.project":"demo"`},
		{argv: []string{"delete", "--force", "NAME"}},
		{argv: []string{"run", "-d", "--name", "NAME", "--label", "opossum.project=long", "alpine"}},
		{argv: []string{"inspect", "NAME"}, has: `"opossum.project":"long"`},
		{argv: []string{"delete", "--force", "NAME"}},
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"inspect", "NAME"}, has: `"state":"running"`, lacks: `opossum.project`},
		{argv: []string{"run", "-d", "--name", "second.demo.opossum", "--label", "opossum.project=other", "alpine"}},
		{argv: []string{"inspect", "second.demo.opossum"}, has: `"opossum.project":"other"`},
		{argv: []string{"inspect", "NAME"}, lacks: `opossum.project`},
	}},
	// opossum itself passes `--label=<v>` and `--env=<v>` as one argument each
	// (#996), not `-e`/`-l` followed by a separate value — because a
	// compose-sourced key can start with "-", which the real CLI reads as
	// another flag when the value is its own argument (measured on container
	// 1.4.1: `--env=-X=1` and `--label=-x=1` both take the value; the
	// two-argument form fails with `Missing value for '<flag>'` for the same
	// input). A fake that only recognised the two-argument form would silently
	// drop the label, or worse, misread the next flag as this one's value.
	{"the combined --flag=value form opossum passes is read the same as the separate form", nil, []step{
		{argv: []string{"run", "-d", "--name", "NAME", "--label=opossum.project=demo", "--env=-X=1", "alpine"}},
		{argv: []string{"inspect", "NAME"}, has: `"opossum.project":"demo"`},
		{argv: []string{"delete", "--force", "NAME"}},
	}},
	// The test knobs: a name never run carries the project its spelling names,
	// one that was run carries what its run gave it, and $INSPECT_UNLABELED
	// takes the label away.
	{"a name never run carries the project its spelling names, a run's label wins", []string{"INSPECT_PROJECT_FROM_NAME=1"}, []step{
		{argv: []string{"inspect", "NAME"}, has: `"opossum.project":"demo"`},
		{argv: []string{"inspect", "OTHER"}, has: `"opossum.project":"other"`},
		{argv: []string{"inspect", "bare"}, has: `"state":"running"`, lacks: `opossum.project`},
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"inspect", "NAME"}, has: `"state":"running"`, lacks: `opossum.project`},
		{argv: []string{"inspect", "OTHER"}, has: `"opossum.project":"other"`},
	}},
	{"an unlabeled name carries no label, a run's included", []string{"INSPECT_PROJECT_FROM_NAME=1", "INSPECT_UNLABELED=probe.demo.opossum"}, []step{
		{argv: []string{"inspect", "NAME"}, has: `"state":"running"`, lacks: `opossum.project`},
		{argv: []string{"inspect", "OTHER"}, has: `"opossum.project":"other"`},
		{argv: []string{"run", "-d", "--name", "NAME", "-l", "opossum.project=demo", "alpine"}},
		{argv: []string{"inspect", "NAME"}, has: `"state":"running"`, lacks: `opossum.project`},
	}},
	{"a container that is gone: stop, delete, start and logs all fail", nil, []step{
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"delete", "--force", "NAME"}},
		{argv: []string{"stop", "NAME"}, rc: 1, has: `notFound: "container with ID NAME not found"`},
		{argv: []string{"delete", "--force", "NAME"}, rc: 1, has: `notFound: "container with ID NAME not found"`},
		// A command that should have passed the service by would otherwise
		// look as though it worked: no logs read as an empty log, and a start
		// that says nothing reads as a container started (#1096).
		// In the real CLI's words, which are not the same for the two: a fake
		// that invents a message tells whoever reads it next something the
		// runtime never says (measured on 1.4.1, quotes left unclosed as they
		// come).
		{argv: []string{"start", "NAME"}, rc: 1, has: "get failed: container NAME not found"},
		{argv: []string{"logs", "NAME"}, rc: 1, has: `failed to open container logs: notFound: "container with ID NAME not found`},
		// Running the name again makes it there again.
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"inspect", "NAME"}, has: `"state":"running"`},
		{argv: []string{"stop", "NAME"}},
	}},
	// `kill` only truly succeeds on a running container: one already stopped
	// refuses with invalidState, and one gone refuses with notFound — both
	// measured on 1.4.1 (#962). Orchestrator.Kill re-inspects after either
	// failure and only reports it when the container is still running or
	// unreadable, so a container not running discards both — a fake that
	// answered rc 0 for all three (the state every fake gave kill before this
	// row) passed regardless, and hid that nothing here was ever asked what
	// kill does to a container it cannot affect.
	{"kill only takes on a running container: a stopped one and a gone one both refuse", nil, []step{
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"kill", "NAME"}},
		{argv: []string{"inspect", "NAME"}, has: `"state":"stopped"`},
		{argv: []string{"kill", "NAME"}, rc: 1, has: `invalidState: "no runtime client exists: container is stopped"`},
		{argv: []string{"delete", "--force", "NAME"}},
		{argv: []string{"kill", "NAME"}, rc: 1, has: `notFound: "container with ID NAME not found"`},
	}},
	// `run` of a name already there refuses, whether that container is
	// running or stopped — the same sentence either way (measured on 1.4.1,
	// #962). Before this every fake silently replaced the first container, a
	// call opossum never makes deliberately (a genuine replace always deletes
	// first) — so this asks a question nothing exercised until now. Once
	// deleted, the name is free.
	{"run of a name already there refuses, running or stopped; once deleted it is free", nil, []step{
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}, rc: 1, has: "container with id NAME already exists"},
		{argv: []string{"stop", "NAME"}},
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}, rc: 1, has: "container with id NAME already exists"},
		{argv: []string{"delete", "--force", "NAME"}},
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"inspect", "NAME"}, has: `"state":"running"`},
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
	// `rm` is `delete`'s alias on 1.4.1 — the same answer either spelling (#962).
	// opossum itself only ever issues `delete`, so this checks a spelling
	// nothing here depends on, not a caller's behaviour.
	{"volume rm is delete's alias: the same answer either spelling", nil, []step{
		{argv: []string{"run", "-d", "-v", "vol1:/d", "--name", "NAME", "alpine"}},
		{argv: []string{"volume", "rm", "vol1"}},
		{argv: []string{"volume", "rm", "vol1"}, rc: 1, has: `failed to delete one or more volumes`, lacks: "not found"},
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
	// `--debug`, a value starting with `-` given to another flag as a separate
	// argument (the real CLI calls it missing, exit 64; measured on `--user -1`
	// — opossum itself now passes such values combined, `--user=-1`, #996), and
	// a flag with no value at the end.
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
		// A `--flag=value` opossum passes combined (#996) is complete in one
		// argument — it must not consume the `--name` after it as if it were a
		// separate value the flag itself took.
		{argv: []string{"run", "-d", "--user=1000", "--name", "_afterequals", "alpine"}, rc: 1, has: "Error: container ID _afterequals is not a valid container ID"},
		{argv: []string{"run", "-d", "--user=1000", "--name", "-dash", "alpine"}, rc: 64, has: "Error: Missing value for '--name <name>'"},
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
		// A `--flag=value` opossum passes combined (#996) before the `-v` is
		// complete in one argument — it must not consume the `-v` after it as
		// if it were a separate value the flag itself took.
		{argv: []string{"run", "--rm", "--workdir=/a", "-v", "-dash:/y", "alpine"}, rc: 1, has: "Error: invalid volume name '-dash': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$"},
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
	// A network answers, when inspected, the labels it was made with
	// (`configuration.labels`, measured on 1.4.1: `--label k=v` on `network
	// create` reads back as `"k" : "v"`, and a network made without any has an
	// empty object). `down` removes a network under a name of the compose file's
	// own only when the project's label is on it, so a fake that answered the
	// same labels for every network — or none — would turn that question into a
	// constant.
	{"a network answers the labels it was made with", nil, []step{
		{argv: []string{"network", "create", "--label", "tier=back", "--label", "opossum.project=demo", "demo-lab"}},
		{argv: []string{"network", "create", "demo-plain"}},
		{argv: []string{"network", "inspect", "demo-lab"}, has: `"opossum.project" : "demo"`},
		{argv: []string{"network", "inspect", "demo-lab"}, has: `"tier" : "back"`},
		{argv: []string{"network", "inspect", "demo-lab"}, has: `"name" : "demo-lab"`},
		{argv: []string{"network", "inspect", "demo-plain"}, has: `"labels" : {`, lacks: `opossum.project`},
		{argv: []string{"network", "inspect", "demo-plain"}, lacks: `tier`},
	}},
	// A network already deleted is not there to delete a second time: the real
	// CLI fails (1.4.1: `Error: failed to delete one or more networks:
	// ["<name>"]`), a different shape from `network inspect`'s `network not
	// found: <name>` — DeleteNetwork's networkAlreadyGone reads both, and a
	// fake that let a second delete succeed never exercised the one it reads
	// first (#962). Recreating the name under the same run clears it: it is not
	// gone forever, only until made again.
	{"a network already deleted is not there to delete or inspect again", nil, []step{
		{argv: []string{"network", "create", "demo-once"}},
		{argv: []string{"network", "delete", "demo-once"}},
		{argv: []string{"network", "delete", "demo-once"}, rc: 1, has: `failed to delete one or more networks`, lacks: `not found`},
		{argv: []string{"network", "inspect", "demo-once"}, rc: 1, has: `network not found: demo-once`},
		{argv: []string{"network", "create", "demo-once"}},
		{argv: []string{"network", "inspect", "demo-once"}},
		{argv: []string{"network", "delete", "demo-once"}},
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
	// `container logs -f` catches SIGINT and SIGTERM and exits 130 and 143 of
	// its own; SIGHUP ends it (container 1.4.1, measured: the wait status of
	// `logs -f` in a process group of its own, `code=130 signal=0`,
	// `code=143 signal=0`, `code=0 signal=1`). opossum reads how the runtime
	// ended, so a fake that died of the signal would answer another question.
	// LOGS_SLEEP keeps the fake's stream open, as the real one stays open.
	{"logs exits 130 on SIGINT and 143 on SIGTERM, and a SIGHUP ends it", []string{"LOGS_SLEEP=30"}, []step{
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"logs", "-f", "NAME"}, signal: syscall.SIGINT, rc: 130},
		{argv: []string{"logs", "-f", "NAME"}, signal: syscall.SIGTERM, rc: 143},
		{argv: []string{"logs", "-f", "NAME"}, signal: syscall.SIGHUP, rc: -1},
		// Without -f too, while the log is still being read (container 1.4.1,
		// a reader that stalls the pipe: `code=130 signal=0`,
		// `code=143 signal=0`; the real CLI writes out what it has first).
		{argv: []string{"logs", "NAME"}, signal: syscall.SIGINT, rc: 130},
		{argv: []string{"logs", "NAME"}, signal: syscall.SIGTERM, rc: 143},
	}},
	// A container that has written nothing yet: `container logs` without -n
	// reads the log to its end and returns on the empty read, exit 0, before
	// it would follow; with -n it reads nothing back and, asked to follow,
	// follows the empty log, and a SIGINT or a SIGTERM ends it with 130 or 143
	// (container 1.4.1: ContainerLogs.swift, and measured — `-f` 0.1 s exit 0
	// and 0 bytes; `-f -n 1` still running after 2 s, `code=130 signal=0` and
	// `code=143 signal=0`). LOGS_SLEEP would keep a fake open that did not
	// tell the forms apart; OTHER has written its line and is read as ever.
	{"logs of a container that has written nothing ends at once unless followed with -n", []string{"LOGS_EMPTY=probe.demo.opossum", "LOGS_SLEEP=30"}, []step{
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"run", "-d", "--name", "OTHER", "alpine"}},
		{argv: []string{"logs", "-f", "NAME"}, within: 5 * time.Second, lacks: "NAME"},
		{argv: []string{"logs", "NAME"}, within: 5 * time.Second, lacks: "NAME"},
		{argv: []string{"logs", "-n", "1", "NAME"}, within: 5 * time.Second, lacks: "NAME"},
		{argv: []string{"logs", "-f", "OTHER"}, stays: time.Second, signal: syscall.SIGTERM, rc: 143, has: "OTHER"},
		{argv: []string{"logs", "-f", "-n", "1", "NAME"}, stays: time.Second, signal: syscall.SIGINT, rc: 130, lacks: "NAME"},
		{argv: []string{"logs", "-f", "-n", "9223372036854775807", "NAME"}, stays: time.Second, signal: syscall.SIGTERM, rc: 143, lacks: "NAME"},
	}},
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

func init() {
	// An image that is given a CMD answers `image inspect` with it in the real shape (#1635):
	// `variants[].config.config.Cmd`, after a variant that declares none, which is what a
	// multi-platform image answers (testdata/image-inspect/two-variants-second-declares-workdir.json
	// is one); what is held is the path the CMD is read from, not what the other variants hold. opossum reads the CMD of an image for `entrypoint: []` with no command, so a fake that
	// does not answer it turns that path into a refusal in every smoke.
	contract = append(contract, struct {
		name  string
		env   []string
		steps []step
	}{"an image given a CMD answers image inspect with it, in the variant that has it", []string{"IMAGE_CMD=img:1=postgres,-c,x"}, []step{
		{argv: []string{"image", "inspect", "img:1"}, rc: 0, has: `{"config":{"config":{"Cmd":["postgres","-c","x"]}}}`},
	}})
}

func init() {
	// What `container image` refuses before it does anything (container 1.5.0, testdata/real-cli-output.md,
	// #1472): a subcommand it does not have is rc 64, a subcommand that needs an image or a reference
	// and is given none is rc 64, and `delete` with none is rc 1. What is given its argument, or needs
	// none, is not refused. A fake that took any of these for a success would let a call that is
	// malformed pass every test that only looks for success.
	contract = append(contract, struct {
		name  string
		env   []string
		steps []step
	}{"image refuses an unknown subcommand and a missing argument, and nothing else", nil, []step{
		{argv: []string{"image", "bogus"}, rc: 64, has: "Error: Unexpected argument 'bogus'"},
		{argv: []string{"image", "inspect"}, rc: 64, has: "Error: Missing expected argument '<images> ...'"},
		{argv: []string{"image", "tag"}, rc: 64, has: "Error: Missing expected argument '<source>'"},
		{argv: []string{"image", "save"}, rc: 64, has: "Error: Missing expected argument '<references> ...'"},
		{argv: []string{"image", "pull"}, rc: 64, has: "Error: Missing expected argument '<reference>'"},
		{argv: []string{"image", "delete"}, rc: 1, has: "Error: no images specified and --all not supplied"},
		{argv: []string{"image", "rm"}, rc: 1, has: "Error: no images specified and --all not supplied"},
		{argv: []string{"image", "delete", "--force", "img:1"}, rc: 0, lacks: "Error"},
		{argv: []string{"image", "pull", "img:1"}, rc: 0, lacks: "Error"},
		{argv: []string{"image", "tag", "img:1", "img:2"}, rc: 0, lacks: "Error"},
		{argv: []string{"image", "ls"}, rc: 0, lacks: "Unexpected argument"},
		{argv: []string{"image", "prune"}, rc: 0, lacks: "Unexpected argument"},
	}})
}

func init() {
	// What the real CLI refuses of an image that is not there (container 1.5.0, testdata/real-cli-output.md,
	// #1472): `inspect`, `tag` (its source), `save`, `push` and a `delete` without `--force` are rc 1, each
	// in its own words, and `delete --force` is rc 0. An image is not there when $IMAGE_ABSENT names it; one
	// it does not name is. A fake that took any of these for a success would let a path that asks for an
	// image that is gone, and goes on, pass every test that only looks for success.
	contract = append(contract, struct {
		name  string
		env   []string
		steps []step
	}{"an image that is not there is refused by inspect, tag, save, push and delete without --force", []string{"IMAGE_ABSENT=gone:1"}, []step{
		{argv: []string{"image", "inspect", "gone:1"}, rc: 1, has: "Error: image not found: gone:1"},
		{argv: []string{"image", "tag", "gone:1", "other:2"}, rc: 1, has: "Error: image with reference gone:1"},
		{argv: []string{"image", "push", "gone:1"}, rc: 1, has: "Error: image with reference gone:1"},
		{argv: []string{"image", "save", "-o", "gone.tar", "gone:1"}, rc: 1, has: "Error: failed to save image(s)"},
		{argv: []string{"image", "save", "-o", "gone.tar", "gone:1"}, rc: 1, has: `notFound: "image with reference gone:1"`},
		{argv: []string{"image", "delete", "gone:1"}, rc: 1, has: `Error: failed to delete one or more images: ["gone:1"]`},
		{argv: []string{"image", "rm", "gone:1"}, rc: 1, has: `failed to delete one or more images`},
		{argv: []string{"image", "delete", "--force", "gone:1"}, rc: 0, lacks: "Error"},
		{argv: []string{"image", "tag", "here:1", "other:2"}, rc: 0, lacks: "Error"},
		{argv: []string{"image", "push", "here:1"}, rc: 0, lacks: "Error"},
		{argv: []string{"image", "save", "-o", "here.tar", "here:1"}, rc: 0, lacks: "Error"},
		{argv: []string{"image", "delete", "here:1"}, rc: 0, lacks: "Error"},
	}})
}

func init() {
	// `container image load` reads an archive from its standard input (opossum's `docker image save |
	// container image load`), and the real CLI refuses one with nothing in it (container 1.5.0,
	// testdata/real-cli-output.md, #1660): rc 1, `failed to extract archive: no entries found in archive`.
	// A fake that took an empty archive for a success would let an import whose docker side wrote nothing
	// pass every test that only looks for the load's success. Nothing here feeds the fake an archive, so
	// the step is the empty one; the runner gives a command no input.
	contract = append(contract, struct {
		name  string
		env   []string
		steps []step
	}{"image load refuses an archive that has nothing in it", nil, []step{
		{argv: []string{"image", "load"}, rc: 1, has: "Error: failed to extract archive: no entries found in archive"},
	}})
}

// buildFakes builds the three fakes and returns each one's path by name.
func buildFakes(t *testing.T) map[string]string {
	t.Helper()
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
	return fakes
}

// What the fakes take for `image load` where the real CLI answers otherwise, so that it is not a row
// of the contract (#1660): any non-empty input is an archive (the real CLI refuses what is no tar —
// rc 1, `unable to open the archive, code -30`), and `-i`/`--input` is a file they do not look at
// (the real CLI says `file does not exist`, rc 1, for one that is not there). They are held here so
// that the three fakes keep taking them — an import test feeds a fake docker's bytes to the fake
// container's load — and so that a fake that refuses every load, or reads a `--input=<path>` for its
// standard input, is seen.
func TestEveryFakeLoadsWhatItIsGivenToLoad(t *testing.T) {
	for fake, path := range buildFakes(t) {
		for _, tc := range []struct {
			name  string
			argv  []string
			stdin string
		}{
			{"a non-empty archive on the standard input", []string{"image", "load"}, "archive"},
			{"a file named by -i", []string{"image", "load", "-i", "x.tar"}, ""},
			{"a file named by --input", []string{"image", "load", "--input", "x.tar"}, ""},
			{"a file named by --input=", []string{"image", "load", "--input=x.tar"}, ""},
		} {
			t.Run(fake+"/"+tc.name, func(t *testing.T) {
				cmd := exec.Command(path, tc.argv...)
				cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"),
					"STATE_DIR=" + t.TempDir(), "FAKE_LOG=" + filepath.Join(t.TempDir(), "calls.log")}
				cmd.Stdin = strings.NewReader(tc.stdin)
				if out, err := cmd.CombinedOutput(); err != nil || strings.Contains(string(out), "Error") {
					t.Errorf("%v with %q on its standard input: want it taken, got %v\n%s", tc.argv, tc.stdin, err, out)
				}
			})
		}
	}
}

func init() {
	// With $INSPECT_STRICT a name nothing here ran is not a container the runtime has (container 1.4.1,
	// testdata/real-cli-output.md, #1551): `inspect` is rc 1 with `container not found`, as it is for
	// one that was deleted. A name that was run is there; one deleted since is not.
	contract = append(contract, struct {
		name  string
		env   []string
		steps []step
	}{"with INSPECT_STRICT a name nothing ran is not a container, and one that was run is", []string{"INSPECT_STRICT=1"}, []step{
		{argv: []string{"inspect", "OTHER"}, rc: 1, has: "Error: container not found: probe.other.opossum"},
		{argv: []string{"inspect", "NAME"}, rc: 1, has: "Error: container not found: probe.demo.opossum"},
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}, rc: 0},
		{argv: []string{"inspect", "NAME"}, rc: 0, lacks: "Error"},
		{argv: []string{"inspect", "OTHER"}, rc: 1, has: "Error: container not found: probe.other.opossum"},
		{argv: []string{"delete", "--force", "NAME"}, rc: 0},
		{argv: []string{"inspect", "NAME"}, rc: 1, has: "Error: container not found: probe.demo.opossum"},
	}})
}

func init() {
	// With $INSPECT_STRICT a volume nothing here made is not one the runtime has (container 1.4.1,
	// testdata/real-cli-output.md, #1551): `volume delete` of it is rc 1, `failed to delete one or more
	// volumes: ["<name>"]`, as it is for one deleted already. A volume a `run -v NAME:/x` made is there to
	// delete, once; a path in the source (a bind) makes none.
	contract = append(contract, struct {
		name  string
		env   []string
		steps []step
	}{"with INSPECT_STRICT a volume nothing made cannot be deleted, and one a run made can, once", []string{"INSPECT_STRICT=1"}, []step{
		{argv: []string{"volume", "delete", "neverseen"}, rc: 1, has: `Error: failed to delete one or more volumes: ["neverseen"]`},
		{argv: []string{"volume", "rm", "neverseen"}, rc: 1, has: `failed to delete one or more volumes`},
		{argv: []string{"run", "-d", "--name", "NAME", "-v", "madeone:/data", "alpine"}, rc: 0},
		{argv: []string{"volume", "delete", "madeone"}, rc: 0, lacks: "Error"},
		{argv: []string{"volume", "delete", "madeone"}, rc: 1, has: `Error: failed to delete one or more volumes: ["madeone"]`},
		// Made again by another run, it is there to delete again.
		{argv: []string{"run", "-d", "--name", "probe.third.opossum", "-v", "madeone:/data", "alpine"}, rc: 0},
		{argv: []string{"volume", "delete", "madeone"}, rc: 0, lacks: "Error"},
		{argv: []string{"run", "-d", "--name", "OTHER", "-v", "/abs/path:/data", "alpine"}, rc: 0},
		{argv: []string{"volume", "delete", "/abs/path"}, rc: 1, has: `failed to delete one or more volumes`},
	}})
}

func TestEveryFakeAnswersTheContract(t *testing.T) {
	fakes := buildFakes(t)
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
					var out []byte
					var err error
					switch {
					case st.stays != 0:
						out, err = runStaying(t, cmd, st.stays, st.signal)
					case st.signal != 0:
						out, err = runSignalled(t, cmd, st.signal)
					case st.within != 0:
						out, err = runWithin(t, cmd, st.within)
					default:
						out, err = cmd.CombinedOutput()
					}
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

// runSignalled starts cmd, sends it sig once it has written something, and
// returns all it wrote and how it ended.
func runSignalled(t *testing.T, cmd *exec.Cmd, sig syscall.Signal) ([]byte, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	first := make([]byte, 1)
	got := make(chan int, 1)
	go func() { n, _ := r.Read(first); got <- n }()
	select {
	case n := <-got:
		if n == 0 {
			cmd.Wait()
			t.Fatalf("%v ended before writing anything to signal after", cmd.Args)
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("%v wrote nothing to signal after", cmd.Args)
	}
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("%v did not end on %v", cmd.Args, sig)
	}
	rest, _ := io.ReadAll(r)
	return append(first[:1], rest...), err
}

// runStaying starts cmd, fails the test unless it is still running after d,
// then sends sig and returns what it wrote and how it ended.
func runStaying(t *testing.T, cmd *exec.Cmd, d time.Duration, sig syscall.Signal) ([]byte, error) {
	t.Helper()
	var buf lockedBuf
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		t.Fatalf("%v ended within %v; the real CLI is still running", cmd.Args, d)
	case <-time.After(d):
	}
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		return buf.bytes(), err
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("%v did not end on %v", cmd.Args, sig)
	}
	return nil, nil
}

// runWithin runs cmd and fails the test unless it ends by itself within d.
func runWithin(t *testing.T, cmd *exec.Cmd, d time.Duration) ([]byte, error) {
	t.Helper()
	var buf lockedBuf
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return buf.bytes(), err
	case <-time.After(d):
		cmd.Process.Kill()
		<-done
		t.Fatalf("%v did not end within %v; the real CLI ends at once", cmd.Args, d)
	}
	return nil, nil
}

// lockedBuf is a buffer a command's stdout and stderr write into together.
type lockedBuf struct {
	mu sync.Mutex
	b  []byte
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b = append(l.b, p...)
	return len(p), nil
}

func (l *lockedBuf) bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.b...)
}

func digest(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}
