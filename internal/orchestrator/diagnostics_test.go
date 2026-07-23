package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every diagnostic code opossum can emit must be documented in AGENTS.md, so an
// agent that sees a `[OPSM-NNN]` can always look up its fix. Adding a code forces
// documenting it (1:1 with the failure-signature / diagnostic-codes tables).
func TestDiagCodesDocumentedInAgentsMd(t *testing.T) {
	md := readAgentsMd(t)
	for _, c := range allDiagCodes {
		if !strings.Contains(md, string(c)) {
			t.Errorf("diagnostic code %q is not documented in AGENTS.md — add it to the Diagnostic codes list", c)
		}
	}
}

// The reverse of the above, closing the 1:1 loop: every `OPSM-NNN` that AGENTS.md
// mentions must be a real code in the ledger. This catches a stale reference (a
// code removed from the ledger but left in the docs) or a typo like `OPSM-4004`,
// either of which would send an agent looking up a fix that doesn't exist.
func TestNoPhantomDiagCodesInAgentsMd(t *testing.T) {
	real := map[string]bool{}
	for _, c := range allDiagCodes {
		real[string(c)] = true
	}
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`OPSM-\d+`).FindAllString(readAgentsMd(t), -1) {
		if seen[m] {
			continue
		}
		seen[m] = true
		if !real[m] {
			t.Errorf("AGENTS.md references %q, which is not a defined diagnostic code — fix the typo or remove the stale reference (the ledger is the source of truth)", m)
		}
	}
}

func readAgentsMd(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "AGENTS.md"))
	if err != nil {
		t.Fatalf("reading AGENTS.md: %v", err)
	}
	return string(data)
}

// Every warning the orchestrator emits must carry a code, i.e. go through warnf.
// This forbids a bare `"warning: …"` string literal anywhere else in the package
// — whether via `logf` or `fmt.Fprintf(o.out, …)` — so a new warning can't ship
// uncoded. (warnf's own literal in diagnostics.go is the sole exemption.)
func TestNoUncodedWarnings(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "diagnostics.go" {
			continue // diagnostics.go holds the one legitimate `"warning: [%s] "` in warnf
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"warning:`) {
			t.Errorf("%s emits a bare uncoded warning (a %q string literal) — route it through o.warnf(code, …) so it carries an [OPSM-NNN] code", name, "warning:")
		}
	}
}

// The runtime-stopped error, the auto-start notice, and the auto-start-failed
// error must all TEACH why the runtime needs starting (not just name the command),
// so an agent that reads "doesn't start on demand" won't loop. Guards the #271
// requirement that the reason text ships in every runtime-not-running message.
func TestRuntimeMessagesExplainWhy(t *testing.T) {
	const why = "doesn't start on demand"
	cases := map[string]string{
		"ErrRuntimeStopped":         ErrRuntimeStopped().Error(),
		"NoticeRuntimeAutoStart":    NoticeRuntimeAutoStart(),
		"ErrRuntimeAutoStartFailed": ErrRuntimeAutoStartFailed(fmt.Errorf("boom")).Error(),
	}
	for name, msg := range cases {
		if !strings.Contains(msg, why) {
			t.Errorf("%s must explain why (%q), got: %s", name, why, msg)
		}
		if !strings.Contains(msg, "OPSM-40") {
			t.Errorf("%s must carry an OPSM code, got: %s", name, msg)
		}
	}
}
