package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// AGENTS.md documents each code twice, and the two entries do different jobs: the
// failure-signature section says what happened and how to recover, the list is the
// index an agent scans. A code in only one of them is half-documented.
//
// The sections are named here rather than searched for, because a check that looks
// at the whole file passes when either table is deleted outright — which is what
// this one used to do.
const (
	agentsMdSignatures = "## Failure signatures → fix"
	agentsMdCodeList   = "### Diagnostic codes"
)

var (
	// An entry in the failure-signature section, which opens `- **`[OPSM-NNN]` …`.
	// Matching the entry's head rather than any mention is what lets an entry refer
	// to a neighbouring code in passing without counting as documenting it.
	proseEntryRE = regexp.MustCompile("(?m)^- \\*\\*`\\[(OPSM-[0-9]+)\\]`")
	// A line in the index, which is `- `OPSM-NNN` — …`.
	indexEntryRE = regexp.MustCompile("(?m)^- `(OPSM-[0-9]+)`")
)

// codesWithRecoveryProse are the codes the failure-signature section explains at
// length: what happened, why, and how to get moving again.
//
// Not every code earns one. Some are notes rather than failures — an ignored
// compose field, a `watch` action that failed and will be retried — and writing a
// recovery narrative for those would pad the section an agent reads first. So the
// split is declared here instead of inferred: a code is on this list or it isn't,
// and the test says so either way when the document drifts from it.
var codesWithRecoveryProse = []diagCode{
	codePGDATADatadir, codeSharedVolume, codeVolumeAttachBusy, codeBindDirCreate,
	codeBindDataDirChown, codeHostDeviceMount, codeBindFilePlaceholder, codeVolumeNotSeeded,
	codeSymlinkedSocket, codePGVersionedLayout, codeDataDirNotThisMount,
	codeHostPortInUse, codeDNSDomainAbsent, codeInternalEgress, codeDockerSocket,
	codeExternalNetAbsent, codeHostPortRemapped,
	codeBuildTmpContext, codeBuildSymlink,
	codeOrphans,
	codeDepNotRunning, codeRuntimeAbsent, codeRuntimeStopped, codeRuntimeAutoStart,
	codeServiceExited, codeSupervisorStarted, codeSupervisorAction,
	codeImageNoArm64,
}

// The index is what an agent scans to turn a code it just saw into a fix, so it
// has to list every code opossum can emit and nothing else.
//
// Reading the section rather than the whole file is the point. The previous
// version searched all of AGENTS.md, so a code documented in either place counted
// as documented in both — either of a code's two entries could be deleted and the
// search still found the other. Deleting the whole prose section was green for the
// same reason. (Deleting the whole index was not: ten codes appear nowhere else,
// so the old test did catch that one. The hole was per-entry, and on the prose
// side it was total.)
func TestDiagCodesDocumentedInAgentsMd(t *testing.T) {
	listed := codesIn(indexEntryRE, agentsMdSection(t, readAgentsMd(t), agentsMdCodeList))
	compareCodeSets(t, agentsMdCodeList, allDiagCodes, listed,
		"add it to the index", "remove it, or add the code to the ledger")
}

// The failure-signature section is the other half, and it drifts the same way: an
// entry deleted with the code left in the ledger, or prose written for a code
// nobody declared worth explaining. Both directions are checked against the list
// above, so the section cannot be emptied without this failing.
func TestFailureSignaturesMatchTheDeclaredCodes(t *testing.T) {
	explained := codesIn(proseEntryRE, agentsMdSection(t, readAgentsMd(t), agentsMdSignatures))
	compareCodeSets(t, agentsMdSignatures, codesWithRecoveryProse, explained,
		"write the entry, or drop it from codesWithRecoveryProse",
		"add it to codesWithRecoveryProse, or fold the entry into an existing one")
	inLedger := map[diagCode]bool{}
	for _, c := range allDiagCodes {
		inLedger[c] = true
	}
	for _, c := range codesWithRecoveryProse {
		if !inLedger[c] {
			t.Errorf("codesWithRecoveryProse lists %q, which is not a code in the ledger", c)
		}
	}
}

// codesIn returns the codes an entry pattern finds in a section, in no order.
func codesIn(re *regexp.Regexp, section string) map[diagCode]bool {
	out := map[diagCode]bool{}
	for _, m := range re.FindAllStringSubmatch(section, -1) {
		out[diagCode(m[1])] = true
	}
	return out
}

// compareCodeSets reports both directions of a mismatch, since the two mean
// opposite things and need opposite fixes.
func compareCodeSets(t *testing.T, where string, want []diagCode, got map[diagCode]bool, missingFix, extraFix string) {
	t.Helper()
	if len(got) == 0 {
		t.Fatalf("no entries found in %q — the section's format changed, so this check is "+
			"reading nothing and would pass whatever the document said", where)
	}
	declared := map[diagCode]bool{}
	for _, c := range want {
		declared[c] = true
		if !got[c] {
			t.Errorf("%q is not in %q — %s", c, where, missingFix)
		}
	}
	for c := range got {
		if !declared[c] {
			t.Errorf("%q is in %q but not declared — %s", c, where, extraFix)
		}
	}
}

// agentsMdSection returns the body under heading, up to the next heading of any
// level. It fails rather than returning nothing when the heading has moved: a
// section check that silently scans an empty string passes every assertion made
// against it, which is the failure this whole test is here to stop.
func agentsMdSection(t *testing.T, md, heading string) string {
	t.Helper()
	// Anchored to the start of a line: `### X` is a substring of `#### X`, so an
	// unanchored count keeps saying 1 while the heading changes level underneath it.
	if n := strings.Count("\n"+md, "\n"+heading+"\n"); n != 1 {
		t.Fatalf("AGENTS.md has %d headings %q, want exactly 1 — if it was renamed, "+
			"update the constant here so this keeps checking something", n, heading)
	}
	_, rest, _ := strings.Cut(md, heading+"\n")
	var body []string
	for _, line := range strings.Split(rest, "\n") {
		if strings.HasPrefix(line, "#") {
			break
		}
		body = append(body, line)
	}
	out := strings.Join(body, "\n")
	if strings.TrimSpace(out) == "" {
		t.Fatalf("the section %q in AGENTS.md is empty", heading)
	}
	return out
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
//
// Since those three messages are pinned whole below, no change to the code alone
// can fail this without failing that. What is left to it is the other direction:
// someone updating the golden to match a message they just edited. This is what
// says the reason may not leave in the course of that.
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

// The three runtime-not-running messages, word for word.
//
// TestRuntimeMessagesExplainWhy above asks whether each one carries the reason
// and a code. Both of those are Contains, and a message with the code and the
// reason exchanged still carries both — the swap puts the explanation where the
// code belongs and the reader gets `[opossum drives Apple's...] ... OPSM-405`.
// A mutation sweep found the exchange in all three, and every one of them left
// this repository green.
//
// These are what a person reads when nothing is working, so what they say is the
// product. Pinned whole, in one place, so that a sentence added to one sibling
// and not the others fails here rather than being noticed by a user.
func TestTheRuntimeMessagesAreWordForWordWhatWeMeanToSay(t *testing.T) {
	// The clause all three share. Written once here because it is written once
	// there: a copy that drifted in one message would read as three explanations
	// where there is one.
	const why = "opossum drives Apple's `container` CLI, which manages the VM through a background " +
		"service (apiserver) that doesn't start on demand, so it needs starting after a reboot " +
		"or a `container system stop`."
	cases := map[string]struct{ got, want string }{
		"ErrRuntimeStopped": {ErrRuntimeStopped().Error(),
			"[OPSM-405] the `container` system isn't running. " + why +
				" Start it with `container system start` (or run `opossum doctor`); opossum starts " +
				"it for you on a mutating command unless OPOSSUM_NO_AUTO_START is set"},
		"NoticeRuntimeAutoStart": {NoticeRuntimeAutoStart(),
			"[OPSM-406] the container runtime isn't running — starting it now " +
				"(`container system start`). " + why},
		"ErrRuntimeAutoStartFailed": {ErrRuntimeAutoStartFailed(fmt.Errorf("boom")).Error(),
			"[OPSM-405] the `container` system isn't running and opossum couldn't start it " +
				"(`container system start` failed: boom). " + why +
				" Try starting it yourself, or run `opossum doctor`"},
	}
	// A golden that can be made smaller can be made to pass. Deleting one case
	// and exchanging that message's own arguments puts the hole straight back,
	// with every remaining row green. The test thirty lines below refuses to run
	// on an empty list for the same reason; this is the same rule counted.
	if len(cases) != 3 {
		t.Fatalf("%d messages pinned, and there are three: the runtime is stopped, opossum is "+
			"starting it, opossum could not", len(cases))
	}
	for name, c := range cases {
		if c.got != c.want {
			t.Errorf("%s is not what this file says it should be\n got: %s\nwant: %s", name, c.got, c.want)
		}
	}
}

// A code written as a literal is a code the ledger does not own: retiring or
// renaming the constant leaves it behind, and nothing here would notice. One had
// already drifted that way, so the constants are made the only spelling.
//
// The declarations hold bare "OPSM-NNN" values; what this bans is the emitted
// form, the bracketed code baked into a message instead of formatted from a
// constant.
//
// Scope is this package, which today is the only one that emits codes at all. The
// day another package does, this has to grow to reach it.
func TestCodesAreEmittedFromConstantsNotLiterals(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no sources found — this test would pass by looking at nothing")
	}
	var sources []string
	for _, f := range files {
		if !strings.HasSuffix(f, "_test.go") {
			sources = append(sources, f)
		}
	}
	if len(sources) == 0 {
		t.Fatal("every source was skipped — this test would pass by looking at nothing")
	}
	literalCode := regexp.MustCompile(`"[^"]*\[OPSM-\d+\]`)
	var scanned int
	for _, f := range sources {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		for i, line := range strings.Split(string(src), "\n") {
			if literalCode.MatchString(line) {
				t.Errorf("%s:%d writes a code as a literal; format it from its constant instead:\n  %s",
					f, i+1, strings.TrimSpace(line))
			}
		}
	}
	// Counted against the whole list, not against zero. A loop that stops early
	// leaves most of the package unread while every "did it look at anything"
	// guard still passes, so the guard has to be "did it look at all of it".
	if scanned != len(sources) {
		t.Fatalf("scanned %d of %d sources — the loop is stopping early, so most of the "+
			"package is unchecked", scanned, len(sources))
	}
}
