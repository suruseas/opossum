package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestAgentsMdDocumentsEveryCommand keeps AGENTS.md — the agent-facing reference —
// from rotting: every real CLI subcommand must be listed there (as a backtick-
// quoted command token). Adding a command therefore forces an AGENTS.md update, so
// an agent given only AGENTS.md never meets an undocumented command. Cobra's
// built-in `help`/`completion` are exempt.
func TestAgentsMdDocumentsEveryCommand(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "AGENTS.md"))
	if err != nil {
		t.Fatalf("reading AGENTS.md: %v", err)
	}
	md := string(data)

	for _, c := range newRootCmd().Commands() {
		name := c.Name()
		if c.Hidden || name == "help" || name == "completion" {
			continue
		}
		// The command must appear as a backtick-anchored token: `name`, `name `,
		// or `name [ (covers "down", "ps", "up [service…]", "start [service…]").
		if !strings.Contains(md, "`"+name+"`") &&
			!strings.Contains(md, "`"+name+" ") &&
			!strings.Contains(md, "`"+name+"\n") {
			t.Errorf("command %q is not documented in AGENTS.md — add it (agents rely on this file)", name)
		}
	}
}

// commandRow returns the row of a "| Command | …" table in md whose first cell
// names the command (as a backtick-anchored token), or "" when there is none.
// A cell may name several commands ("`start …` / `stop …` / `restart …`").
func commandRow(md, name string) string {
	for _, line := range strings.Split(md, "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cells := strings.SplitN(line, " | ", 2)
		first := cells[0]
		if strings.Contains(first, "`"+name+"`") || strings.Contains(first, "`"+name+" ") {
			return line
		}
	}
	return ""
}

// TestAgentsMdRowsListEveryFlag goes one level below the command list: the
// AGENTS.md row for a command must mention every flag that command accepts,
// as `--long` or its `-s` shorthand. An agent reading only that row otherwise
// never learns the flag exists (a `--dry-run` it could have used, a `--format`
// it could have parsed). Cobra's own `--help` is exempt.
func TestAgentsMdRowsListEveryFlag(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "AGENTS.md"))
	if err != nil {
		t.Fatalf("reading AGENTS.md: %v", err)
	}
	md := string(data)

	commands, flags := 0, 0
	var walk func(parent string, cs []*cobra.Command)
	walk = func(parent string, cs []*cobra.Command) {
		for _, c := range cs {
			name := c.Name()
			if c.Hidden || name == "help" || name == "completion" {
				continue
			}
			// A nested command is looked up by its full path (`ws prune`), so
			// the row that documents `ws` must spell out each subcommand.
			path := strings.TrimSpace(parent + " " + name)
			row := commandRow(md, path)
			if row == "" {
				t.Errorf("command %q has no row in the AGENTS.md command table", path)
				continue
			}
			commands++
			// LocalFlags: the command's own flags (including persistent ones it
			// declares), not the ones inherited from its parents — a global
			// `--file` is documented once, not per row.
			c.LocalFlags().VisitAll(func(f *pflag.Flag) {
				if f.Hidden || f.Name == "help" {
					return
				}
				flags++
				if !rowMentionsFlag(row, f.Name, f.Shorthand) {
					t.Errorf("AGENTS.md row for %q does not mention --%s (shorthand %q)", path, f.Name, f.Shorthand)
				}
			})
			walk(path, c.Commands())
		}
	}
	walk("", newRootCmd().Commands())
	t.Logf("%d flags across %d commands checked against their AGENTS.md rows", flags, commands)
}

// rowMentionsFlag reports whether row names the flag as `--long` (whole, not
// as a prefix of another flag), or by its shorthand sitting in a single-dash
// cluster (`-t`, `-it`) — a letter inside a long flag does not count (`--tail`
// is not `-t`), and an empty shorthand never matches.
func rowMentionsFlag(row, long, shorthand string) bool {
	// The long name must stand on its own: `--no-build` does not mention
	// `--build`, and `--audit-format` does not mention `--audit`.
	if regexp.MustCompile(`(?:^|[^-\w])--` + regexp.QuoteMeta(long) + `(?:[^-\w]|$)`).MatchString(row) {
		return true
	}
	if shorthand == "" {
		return false
	}
	return regexp.MustCompile(`(?:^|[^-\w])-[A-Za-z]*` + regexp.QuoteMeta(shorthand) + `[A-Za-z]*(?:[^-\w]|$)`).MatchString(row)
}

// TestAShorthandCountsOnlyInASingleDashCluster pins what rowMentionsFlag reads
// as "mentioned", so the ratchet above cannot be satisfied by a letter that
// merely occurs inside another flag.
func TestAShorthandCountsOnlyInASingleDashCluster(t *testing.T) {
	for _, tc := range []struct {
		row, long, short string
		want             bool
	}{
		{"| `exec [-it] <service>` | run |", "tty", "t", true},
		{"| `exec [-it] <service>` | run |", "interactive", "i", true},
		{"| `logs` | `--follow`, `-n/--tail` |", "tty", "t", false},
		{"| `logs` | `--follow`, `-n/--tail` |", "tail", "n", true},
		{"| `logs` | `--follow`, `-n/--tail` |", "tail", "", true},
		{"| `run [-T] <service>` | one-off |", "no-tty", "T", true},
		{"| `run [-T] <service>` | one-off |", "no-tty", "t", false},
		{"| `down` | `-v` also removes volumes |", "volumes", "v", true},
		{"| `down` | `--verbose` prints each command |", "volumes", "v", false},
		{"| `up` | build + start |", "dry-run", "", false},
		{"| `up` | `--dry-run` prints the plan |", "dry-run", "", true},
		{"| `up` | `--no-build` skips builds |", "build", "", false},
		{"| `up` | `--no-build`, `--build` |", "build", "", true},
		{"| `run` | `--audit-format json` |", "audit", "", false},
		{"| `run` | `--audit` reports; `--audit-format json` |", "audit", "", true},
		{"| `up` | `--profile <p>` |", "profile", "", true},
		{"| `up` | --profile=dev |", "profile", "", true},
	} {
		if got := rowMentionsFlag(tc.row, tc.long, tc.short); got != tc.want {
			t.Errorf("rowMentionsFlag(%q, %q, %q) = %v, want %v", tc.row, tc.long, tc.short, got, tc.want)
		}
	}
}

// TestCompatibilityDocumentsEveryCommand keeps the Commands table of
// docs/compatibility.md — the user-facing "which docker compose verbs exist"
// answer — in step with the binary the same way AGENTS.md is: every real
// subcommand has a row there.
func TestCompatibilityDocumentsEveryCommand(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "compatibility.md"))
	if err != nil {
		t.Fatalf("reading docs/compatibility.md: %v", err)
	}
	_, table, ok := strings.Cut(string(data), "\n## Commands\n")
	if !ok {
		t.Fatal("docs/compatibility.md has no '## Commands' section")
	}
	for _, c := range newRootCmd().Commands() {
		name := c.Name()
		if c.Hidden || name == "help" || name == "completion" {
			continue
		}
		if commandRow(table, name) == "" {
			t.Errorf("command %q has no row in the Commands table of docs/compatibility.md", name)
		}
	}
}
