package compose

// Docs-freshness ratchet (#288): the code is the source of truth for which compose
// service fields opossum acts on — serviceKnownKeys is built from the Service
// struct's yaml tags. This test fails if a field opossum DOES act on is documented
// as "ignored" in README.md or AGENTS.md, the exact class of rot #285 fixed
// (README claimed `networks`/`cap_add`/`cap_drop` were ignored when they're
// supported). It locks the docs to the code so a future field addition can't drift.
//
// Scope: it checks the ignored-field enumerations (not arbitrary prose). Fields
// that are only PARTIALLY acted on legitimately appear in an ignored context and
// are exempted. Parenthetical clarifications and "under/beyond/except/other than
// <x>" references (e.g. "static IPs under `networks`") are stripped so a supported field
// mentioned as context isn't mistaken for a claimed-ignored one.

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// partiallySupported fields are acted on for only part of their surface, so they
// legitimately show up in an "ignored" sentence (e.g. `network_mode` — only `none`
// is acted on; `deploy` — only `resources.limits`).
var partiallySupported = map[string]bool{"network_mode": true, "deploy": true}

var (
	// (…) clarifications, in both the ASCII and the full-width form the Japanese
	// README uses — without the latter, its "（`networks` は効きます）" aside reads as
	// a claim that networks is ignored.
	parenRE = regexp.MustCompile(`\([^)]*\)|（[^）]*）`)
	refRE   = regexp.MustCompile("(?i)\\b(under|beyond|except|other than)\\s+`[^`]+`") // "under `networks`", "beyond `resources.limits`"
	tokenRE = regexp.MustCompile("`([a-z_]+)`")                                        // a backtick-quoted field name
	// Capture only the ignored-field enumeration, stopping at the next bold span so
	// the following prose (e.g. AGENTS.md's "Don't set `dns`…" paragraph) — which
	// isn't an ignored list and could name a supported field — is never scanned.
	agentsIgn = regexp.MustCompile(`(?s)Ignored \(file still loads\):\*\*(.*?)\*\*`)
	// The field-level list used to live in both READMEs, and the Japanese copy
	// drifted independently: `restart` stayed in it for a whole release after the
	// English one was corrected (the translation ratchet compares only headings,
	// tables and fences, so prose divergence like that is invisible to it). The
	// list now has exactly one home for people — docs/compatibility.md — and one
	// for agents, AGENTS.md. That is the fix for that class of drift: not a third
	// scanner, but nowhere left to drift to.
	compatIgn = regexp.MustCompile(`(?s)\*\*Ignored fields\*\*(.*?)\n- `)
)

func TestSupportedFieldsNotDocumentedAsIgnored(t *testing.T) {
	docs := map[string]*regexp.Regexp{
		filepath.Join("..", "..", "AGENTS.md"):                agentsIgn,
		filepath.Join("..", "..", "docs", "compatibility.md"): compatIgn,
	}
	for path, blockRE := range docs {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		m := blockRE.FindStringSubmatch(string(data))
		if m == nil {
			t.Fatalf("%s: couldn't locate the ignored-fields list — did its heading change? (the ratchet's anchor needs updating)", path)
		}
		// Strip clarifying parentheticals and "under/beyond/except <x>" references so
		// a supported field named only as context isn't read as claimed-ignored.
		block := refRE.ReplaceAllString(parenRE.ReplaceAllString(m[1], " "), " ")
		for _, tok := range tokenRE.FindAllStringSubmatch(block, -1) {
			field := tok[1]
			if serviceKnownKeys[field] && !partiallySupported[field] {
				t.Errorf("%s lists %q among ignored fields, but opossum acts on it (it's in serviceKnownKeys) — "+
					"the docs contradict the code; move it to the supported list (this is the #285 rot)", path, field)
			}
		}
	}
}
