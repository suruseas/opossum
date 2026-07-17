package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
