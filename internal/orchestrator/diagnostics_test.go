package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every diagnostic code opossum can emit must be documented in AGENTS.md, so an
// agent that sees a `[OPSM-NNN]` can always look up its fix. Adding a code forces
// documenting it (1:1 with the failure-signature / diagnostic-codes tables).
func TestDiagCodesDocumentedInAgentsMd(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "AGENTS.md"))
	if err != nil {
		t.Fatalf("reading AGENTS.md: %v", err)
	}
	md := string(data)
	for _, c := range allDiagCodes {
		if !strings.Contains(md, string(c)) {
			t.Errorf("diagnostic code %q is not documented in AGENTS.md — add it to the Diagnostic codes list", c)
		}
	}
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
