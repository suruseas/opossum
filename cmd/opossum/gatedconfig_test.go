package main

import (
	"strings"
	"testing"
)

// `config` reads what a command reads: the services the active profiles leave
// active. A service behind a profile nothing turned on is dropped before its
// depends_on is read, so a dependency it names that no service defines, and a
// cycle among such services, are not the file's problem yet — docker compose
// v5.5.1 prints the rest of the project and exits 0 (measured, #1088). Turn the
// profile on and the same file is refused, in the words the rest of opossum
// uses for it.
func TestConfigReadsOnlyTheActiveServices(t *testing.T) {
	const undefinedGated = `
name: demo
services:
  keep:
    image: alpine:3.20
  a:
    image: alpine:3.20
    profiles: [x]
    depends_on: [nosuch]
`
	const undefinedPlain = `
name: demo
services:
  keep:
    image: alpine:3.20
  a:
    image: alpine:3.20
    depends_on: [nosuch]
`
	const cycleGated = `
name: demo
services:
  keep:
    image: alpine:3.20
  other:
    image: alpine:3.20
    profiles: [x]
    depends_on: [zed]
  zed:
    image: alpine:3.20
    profiles: [x]
    depends_on: [other]
`
	const cyclePlain = `
name: demo
services:
  keep:
    image: alpine:3.20
  other:
    image: alpine:3.20
    depends_on: [zed]
  zed:
    image: alpine:3.20
    depends_on: [other]
`
	for _, tc := range []struct {
		name     string
		body     string
		env      string   // COMPOSE_PROFILES
		args     []string // after the compose file
		says     string   // the refusal, or "" where the project is printed
		wantOut  []string // what a printed project must name
		wantNone []string // and must not
	}{
		{name: "a cycle behind an inactive profile", body: cycleGated,
			args: []string{"config"}, wantOut: []string{"keep"}, wantNone: []string{"other"}},
		{name: "a cycle behind an inactive profile, listing the services", body: cycleGated,
			args: []string{"config", "--services"}, wantOut: []string{"keep"}, wantNone: []string{"other", "zed"}},
		// `config` prints the project and does not order it, so a cycle among
		// the services it prints is not what it refuses over — `--services`,
		// which orders them, is. (docker compose refuses both: a difference of
		// its own, older than this change and untouched by it.)
		{name: "that profile turned on", body: cycleGated, env: "x",
			args: []string{"config", "--services"}, says: "dependency cycle detected"},
		{name: "a cycle nothing gates", body: cyclePlain,
			args: []string{"config", "--services"}, says: "dependency cycle detected"},
		// The third of the three reading the project refuses: an active
		// service that depends on one behind a profile that is not active.
		// `config` refuses the projects `up` refuses, so a reader who runs it
		// first learns the same thing.
		{name: "a dependency behind a profile that is not active", body: `
name: demo
services:
  keep:
    image: alpine:3.20
    depends_on: [gated]
  gated:
    image: alpine:3.20
    profiles: [x]
`, args: []string{"config"}, says: `service "keep" depends on "gated", whose profile is not active`},
	} {
		t.Run(tc.name+" | "+strings.Join(tc.args, " "), func(t *testing.T) {
			t.Setenv("COMPOSE_PROFILES", tc.env)
			fakeShim(t)
			compose := writeCompose(t, tc.body)
			out, err := run(t, append([]string{"-f", compose}, tc.args...)...)
			if tc.says != "" {
				if err == nil || !strings.Contains(err.Error(), tc.says) {
					t.Fatalf("want %q refused, got %v\n%s", tc.says, err, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("want the project printed, got %v\n%s", err, out)
			}
			for _, want := range tc.wantOut {
				if !strings.Contains(out, want) {
					t.Errorf("want %q named, got\n%s", want, out)
				}
			}
			for _, none := range tc.wantNone {
				if strings.Contains(out, none) {
					t.Errorf("want no %q, got\n%s", none, out)
				}
			}
		})
	}
}

// A file with two things mounted at one target and a dependency cycle as well:
// the pair is what the reader is told about. That is docker compose v5.5.1's
// order too (measured: the pair, in 24 runs of 24 over three commands, and 24
// more independently). A cycle behind a profile that is not active is not read
// at all, so there the pair is named as it is in a file with nothing else
// wrong.
func TestWhatIsSaidAboutAFileWithAMountPairAndSomethingElseWrong(t *testing.T) {
	const pair = "\n    volumes: [\"./a:/same\"]\n    tmpfs: [\"/same\"]"
	const conflict = "target /same already mounted"
	for _, tc := range []struct {
		name, body, env, says string
	}{
		{name: "nothing else wrong", says: conflict, body: `
name: demo
services:
  web:
    image: alpine:3.20` + pair + `
`},
		{name: "an ungated cycle", says: conflict, body: `
name: demo
services:
  web:
    image: alpine:3.20` + pair + `
  p1:
    image: alpine:3.20
    depends_on: [p2]
  p2:
    image: alpine:3.20
    depends_on: [p1]
`},
		{name: "a cycle behind a profile that is off", says: conflict, body: `
name: demo
services:
  web:
    image: alpine:3.20` + pair + `
  p1:
    image: alpine:3.20
    profiles: [x]
    depends_on: [p2]
  p2:
    image: alpine:3.20
    profiles: [x]
    depends_on: [p1]
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("COMPOSE_PROFILES", tc.env)
			fakeShim(t)
			compose := writeCompose(t, tc.body)
			// `config` is asked because it refuses over a pair; the commands
			// that work from the containers a project already has say the same
			// thing as a warning and go on, so that a project an earlier
			// opossum started can still be taken down.
			out, err := run(t, "-f", compose, "config")
			if err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("want %q said, got %v\n%s", tc.says, err, out)
			}
		})
	}
}

// Every command that reads the compose file reads it the same way: a
// dependency cycle among services a profile gates is nobody's business while
// that profile is off, and refuses the command once it is on. docker compose
// v5.5.1 refuses all of these over such a file (measured, `-f` given, `-p`
// not). The table is the command tree itself, so a command added later is a
// row here the day it appears — every round of review this file came from
// turned on a command that had been left out of a hand-written list.
func TestEveryCommandReadsTheProjectTheSameWay(t *testing.T) {
	const body = `
name: demo
services:
  web:
    image: alpine:3.20
  other:
    image: alpine:3.20
    profiles: [g]
    depends_on: [zed]
  zed:
    image: alpine:3.20
    profiles: [g]
    depends_on: [other]
`
	// What each command needs beyond its name to get as far as reading the
	// file. A command missing from here is a failure, not a skip.
	args := map[string][]string{
		"build": nil, "config": nil, "cp": {"web:/etc/hostname", "./here"}, "destroy": {"--dry-run"},
		"down": nil, "exec": {"web", "true"}, "images": nil, "import": nil, "kill": nil,
		"logs": {"--tail", "1"}, "port": {"web", "80"}, "ps": nil, "pull": nil, "restart": nil,
		"run": {"--no-deps", "web", "true"}, "start": nil, "stats": {"--no-stream"}, "stop": nil,
		"up": {"--dry-run"}, "volumes": nil, "watch": nil,
	}
	// Left out, each for a reason of its own rather than by a pattern: `ls`
	// reads every project on the machine and none of them from a file, `ws`
	// works on a directory given by `--path`, `doctor` reads the file but goes
	// on whatever it says (it is diagnosing the machine, and a project it
	// cannot read is one of the things it reports), and the last three are
	// cobra's own or opossum's supervisor, which is started by `up` rather
	// than by a person.
	noRow := map[string]string{
		"ls": "reads the machine's projects, not a file", "ws": "works on a --path directory",
		"doctor": "reads the file and reports rather than refuses",
		"help":   "cobra's", "completion": "cobra's", "__supervise": "started by up, not by a person",
	}
	// Two say "nothing here" over a project whose only containers are gated
	// and were never started: that is their answer, not a failure to read.
	worksOnAnEmptyProject := map[string]bool{"watch": true, "port": true}
	// And four never build a startup order, so a cycle is not what they read
	// the file for: `config` prints it (`config --services`, which does order
	// them, refuses), and `exec`, `cp` and `port` work from one container.
	// docker compose refuses all four — a difference of its own, as old as
	// these commands and not touched here.
	readsNoOrder := map[string]bool{"config": true, "exec": true, "cp": true, "port": true}
	var commands []string
	for _, c := range newRootCmd().Commands() {
		name := c.Name()
		if _, skip := noRow[name]; skip {
			continue
		}
		commands = append(commands, name)
	}
	if len(commands) < 15 {
		t.Fatalf("want the command tree, got %v", commands)
	}
	for _, cmd := range commands {
		rest, ok := args[cmd]
		if !ok {
			t.Errorf("want %q in the table: say what it needs to reach the file, or why it reads none", cmd)
			continue
		}
		for _, on := range []bool{false, true} {
			name, env := cmd+" with the gate on it", ""
			if on {
				name, env = cmd+" with the profile turned on", "g"
			}
			t.Run(name, func(t *testing.T) {
				t.Setenv("COMPOSE_PROFILES", env)
				t.Setenv("XDG_STATE_HOME", t.TempDir())
				fakeShim(t)
				compose := writeCompose(t, body)
				out, err := run(t, append([]string{"-f", compose, cmd}, rest...)...)
				said := err != nil && strings.Contains(err.Error(), "dependency cycle detected")
				if want := on && !readsNoOrder[cmd]; said != want {
					t.Errorf("want the cycle said=%v, got %v\n%s", want, err, out)
				}
				// With the gate on it the command has to get on with its work,
				// not fail for something else: a row that fails either way
				// would say nothing about whether the cycle was read.
				if !on && err != nil && !worksOnAnEmptyProject[cmd] {
					t.Errorf("want %s to do its work while the cycle is gated, got %v\n%s", cmd, err, out)
				}
			})
		}
	}
}

// A file with more than one service depending on one behind a profile that is
// not active is refused for the same service every time it is read. The
// services are held in a map, whose order changes from run to run, so a check
// that walks it as it comes would name one service now and another in a
// moment — and a reader who runs the command twice would be told to fix two
// different things.
func TestTheSameFileIsRefusedForTheSameServiceEveryRun(t *testing.T) {
	const body = `
name: demo
services:
  a1:
    image: alpine:3.20
    depends_on: [g1]
  a2:
    image: alpine:3.20
    depends_on: [g2]
  g1:
    image: alpine:3.20
    profiles: [x]
  g2:
    image: alpine:3.20
    profiles: [x]
`
	t.Setenv("COMPOSE_PROFILES", "")
	fakeShim(t)
	compose := writeCompose(t, body)
	first := ""
	for i := 0; i < 20; i++ {
		_, err := run(t, "-f", compose, "config")
		if err == nil {
			t.Fatalf("want the file refused, got nil on run %d", i)
		}
		if first == "" {
			first = err.Error()
			continue
		}
		if err.Error() != first {
			t.Fatalf("run %d says\n%s\nwhere the first said\n%s", i, err, first)
		}
	}
}
