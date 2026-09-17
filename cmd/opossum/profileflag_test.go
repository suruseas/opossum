package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `--profile` is taken by every command, as docker compose v5.5.1 takes it on
// every subcommand (measured). What it changes is which services a command
// reads out of the compose file: `pull` and `build` leave out the ones gated
// behind a profile that is not active, as there, while the commands that work
// from the containers a project already has are unchanged by it.
func TestProfileFlagIsTakenByEveryCommand(t *testing.T) {
	var names []string
	for _, c := range newRootCmd().Commands() {
		if c.Name() == "help" || c.Name() == "completion" || strings.HasPrefix(c.Name(), "__") {
			continue
		}
		names = append(names, c.Name())
	}
	if len(names) < 15 {
		t.Fatalf("want the command tree, got %v", names)
	}
	for _, cmd := range names {
		t.Run(cmd, func(t *testing.T) {
			t.Setenv("COMPOSE_PROFILES", "")
			fakeShim(t)
			compose := writeCompose(t, `
name: demo
services:
  web:
    image: alpine:3.20
  db:
    image: busybox:1.36
    profiles: [debug]
`)
			// --help, so the command's own work (and the runtime) stays out of
			// it: what is being asked is whether the flag parses.
			out, err := run(t, "-f", compose, "--profile", "debug", cmd, "--help")
			if err != nil || strings.Contains(out, "unknown flag") {
				t.Errorf("want --profile taken by %s, got %v\n%s", cmd, err, out)
			}
			if !strings.Contains(out, "--profile") {
				t.Errorf("want %s --help to name --profile, got\n%s", cmd, out)
			}
		})
	}
}

// Which services `pull` and `build` work on, and that the flag leaves the
// commands that read the containers alone.
func TestProfileDecidesWhatIsPulledAndBuilt(t *testing.T) {
	const body = `
name: demo
services:
  web:
    image: alpine:3.20
  db:
    image: busybox:1.36
    profiles: [debug]
  tools:
    profiles: [debug]
    build:
      context: .
`
	for _, tc := range []struct {
		name     string
		env      string   // COMPOSE_PROFILES
		args     []string // after the compose file
		wantRun  []string // runtime calls that must be there
		wantNone []string // and ones that must not
	}{
		{name: "pull, no profile", args: []string{"pull"},
			wantRun: []string{"pull alpine:3.20"}, wantNone: []string{"pull busybox:1.36"}},
		{name: "pull with the profile", args: []string{"--profile", "debug", "pull"},
			wantRun: []string{"pull alpine:3.20", "pull busybox:1.36"}},
		{name: "pull with COMPOSE_PROFILES", env: "debug", args: []string{"pull"},
			wantRun: []string{"pull alpine:3.20", "pull busybox:1.36"}},
		{name: "pull with every profile", args: []string{"--profile", "*", "pull"},
			wantRun: []string{"pull busybox:1.36"}},
		// Repeated, each flag is a profile; one flag holding a comma is one
		// profile name, as docker compose reads it (measured: `--profile x,y`
		// enables neither x nor y there).
		{name: "pull with the flag twice", args: []string{"--profile", "debug", "--profile", "other", "pull"},
			wantRun: []string{"pull busybox:1.36", "pull alpine:3.20"}},
		{name: "pull with one flag holding a comma", args: []string{"--profile", "debug,other", "pull"},
			wantRun: []string{"pull alpine:3.20"}, wantNone: []string{"pull busybox:1.36"}},
		{name: "pull naming the gated service", args: []string{"pull", "db"},
			wantRun: []string{"pull busybox:1.36"}, wantNone: []string{"pull alpine:3.20"}},
		{name: "build, no profile", args: []string{"build"},
			wantNone: []string{"-t demo-tools:latest"}},
		{name: "build with the profile", args: []string{"--profile", "debug", "build"},
			wantRun: []string{"-t demo-tools:latest"}},
		{name: "build naming the gated service", args: []string{"build", "tools"},
			wantRun: []string{"-t demo-tools:latest"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := fakeShim(t)
			// The environment decides this as much as the flag does, so the
			// row says what it is rather than inheriting the one outside.
			t.Setenv("COMPOSE_PROFILES", tc.env)
			compose := writeCompose(t, body)
			if _, err := run(t, append([]string{"-f", compose}, tc.args...)...); err != nil {
				t.Fatalf("%v: %v", tc.args, err)
			}
			lines := strings.Join(log(), "\n")
			for _, want := range tc.wantRun {
				if !strings.Contains(lines, want) {
					t.Errorf("want %q asked of the runtime, got\n%s", want, lines)
				}
			}
			for _, none := range tc.wantNone {
				if strings.Contains(lines, none) {
					t.Errorf("want no %q asked of the runtime, got\n%s", none, lines)
				}
			}
		})
	}
}

// The commands that work from the containers a project already has take the
// flag and ask the runtime the same things either way. For `ps` and `images`
// that is what docker compose v5.5.1 does; for the rest it is a known
// difference, written in docs/compatibility.md: measured there, `logs` shows
// only the active services, `stop`, `start`, `restart` and `kill` touch only
// those, and `down` without the flag leaves a gated container running and the
// project's network with it, where opossum takes the whole project down.
func TestProfileDoesNotNarrowTheCommandsThatWorkFromContainers(t *testing.T) {
	const body = `
name: demo
services:
  web:
    image: alpine:3.20
  db:
    image: busybox:1.36
    profiles: [debug]
`
	for _, tc := range []struct {
		cmd  []string
		asIn string // "docker compose" where this is parity, the difference otherwise
	}{
		{cmd: []string{"ps"}, asIn: "docker compose"},
		{cmd: []string{"images"}, asIn: "docker compose"},
		{cmd: []string{"logs"}, asIn: "a known difference"},
		{cmd: []string{"stop"}, asIn: "a known difference"},
		{cmd: []string{"start"}, asIn: "a known difference"},
		{cmd: []string{"restart"}, asIn: "a known difference"},
		{cmd: []string{"kill"}, asIn: "a known difference"},
		{cmd: []string{"down"}, asIn: "a known difference"},
	} {
		cmd := tc.cmd
		// The row says whether this is parity or a difference; the docs say the
		// same thing, and a reader who changes one must change the other.
		t.Run(strings.Join(cmd, " ")+" as "+tc.asIn+" in docs/compatibility.md", func(t *testing.T) {
			known := knownDifferenceCommands(t)
			if got := known[cmd[0]]; got != (tc.asIn == "a known difference") {
				t.Errorf("want %q written as %q, the profiles row of docs/compatibility.md has known difference=%v", cmd[0], tc.asIn, got)
			}
		})
		t.Run(strings.Join(cmd, " ")+" ("+tc.asIn+")", func(t *testing.T) {
			t.Setenv("COMPOSE_PROFILES", "")
			asked := func(args ...string) string {
				log := fakeShim(t)
				compose := writeCompose(t, body)
				if _, err := run(t, append([]string{"-f", compose}, args...)...); err != nil {
					t.Fatalf("%v: %v", args, err)
				}
				return strings.Join(log(), "\n")
			}
			plain := asked(cmd...)
			withFlag := asked(append([]string{"--profile", "debug"}, cmd...)...)
			if plain != withFlag {
				t.Errorf("want %v to ask the same with and without the profile\nwithout:\n%s\nwith:\n%s", cmd, plain, withFlag)
			}
			if plain == "" {
				t.Errorf("want %v to ask the runtime something, got nothing", cmd)
			}
		})
	}
}

// knownDifferenceCommands reads the `profiles` row of docs/compatibility.md and
// reports, per command named in it, whether it is named after "Known
// difference:" — where the row says opossum acts on containers docker compose
// would leave alone — or before it, where the row claims docker's behaviour.
func knownDifferenceCommands(t *testing.T) map[string]bool {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "compatibility.md"))
	if err != nil {
		t.Fatal(err)
	}
	row := ""
	for _, l := range strings.Split(string(doc), "\n") {
		if strings.HasPrefix(l, "| `profiles` |") {
			row = l
		}
	}
	if row == "" {
		t.Fatal("want the profiles row in docs/compatibility.md")
	}
	before, after, found := strings.Cut(row, "Known difference:")
	if !found {
		t.Fatalf("want the profiles row to name its known difference, got:\n%s", row)
	}
	named := map[string]bool{}
	for _, cmd := range []string{"ps", "images", "logs", "stop", "start", "restart", "kill", "down"} {
		inBefore := strings.Contains(before, "`"+cmd+"`")
		inAfter := strings.Contains(after, "`"+cmd+"`")
		if !inBefore && !inAfter {
			t.Errorf("want the profiles row to say something about %q", cmd)
		}
		if inBefore && inAfter {
			t.Errorf("want %q named on one side of the known difference, it is on both", cmd)
		}
		named[cmd] = inAfter
	}
	return named
}
