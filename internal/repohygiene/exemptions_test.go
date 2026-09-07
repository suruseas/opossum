package repohygiene_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The changelog gate's exemption list — the packages under cmd/ and
// internal/ whose changes owe no release note — is the cheapest place to
// turn a red gate green: one `grep -v` in the workflow, no test touched.
// The other check here reads the list from the shipped side (a package the
// released binary links cannot be exempt), which says nothing about a
// package that ships to nobody through the binary but is seen by users all
// the same — the documentation site's builder, say. So the set itself is
// written down here, each entry with why it is exempt, and the workflow
// and this table have to agree in both directions: an exemption added to
// the workflow alone fails, and one added here alone fails too. Deciding
// whether a package's changes are user-visible is then done in a review of
// this file, not in a workflow edit nobody reads.
func TestTheChangelogGatesExemptionsAreTheOnesWrittenDownHere(t *testing.T) {
	root := repoRoot(t)
	g := readGate(t, root)

	// Each exemption and its reason. A package that is not here is checked:
	// cmd/swaps and internal/swaps among them.
	want := map[string]string{
		"internal/site":        "the documentation site's builder: how the pages look is not a change to the product (decided 2026-09-07); the pages' words are docs, checked as docs",
		"cmd/changelog":        "the fragment assembler: runs over this repository, ships in no binary",
		"internal/changelog":   "the assembler's library: same",
		"internal/repohygiene": "this package: checks of the repository, not of the product",
		"internal/claimcite":   "the claim-citation ratchet: a check of the repository's own prose",
		"cmd/mutate":           "the mutation sweep: a development tool",
		"internal/mutate":      "the sweep's library: same",
		"cmd/noleftovers":      "the leftover-directory check: a development tool",
		"cmd/busy":             "the parallel-test guard: a development tool",
		"internal/suitedir":    "test-suite directory layout: only tests and tools read it",
	}

	got := exemptedPackages(t, g.script)
	var missing, extra []string
	for pkg := range want {
		if !got[pkg] {
			missing = append(missing, pkg)
		}
	}
	for pkg := range got {
		if _, ok := want[pkg]; !ok {
			extra = append(extra, pkg)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("the workflow exempts %v from the changelog gate, and this table does not say why — write the reason here (or take the exemption out)", extra)
	}
	if len(missing) > 0 {
		t.Errorf("this table exempts %v, and the workflow does not — the table describes the gate, so make them agree", missing)
	}

	// An exemption for a package that is no longer there is a line nobody
	// will question when a package of that name comes back.
	for pkg := range want {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(pkg))); err != nil {
			t.Errorf("%s is exempt from the changelog gate but does not exist: %v", pkg, err)
		}
	}
}

// exemptedPackages reads the gate script's exclusion list: every
// `grep -v '^<dir>/'` on the line that computes what shipped. The line is
// found by its variable, not by its position, and a spelling this does
// not read (`grep -Ev`, a double-quoted pattern) is reported rather than
// passed over as "no exemptions".
func exemptedPackages(t *testing.T, script string) map[string]bool {
	t.Helper()
	var line string
	for _, l := range strings.Split(script, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "shipped=") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("the gate script has no `shipped=` line; this check reads the exemptions from it:\n%s", script)
	}
	got := map[string]bool{}
	for _, m := range regexp.MustCompile(`grep -v '\^((?:cmd|internal)/[^/']+)/'`).FindAllStringSubmatch(line, -1) {
		got[m[1]] = true
	}
	// Every grep on the line is the one that selects cmd/ and internal/, a
	// package exemption read above, or one of the two shape rules (test
	// files, testdata); anything else — `grep -Ev`, a double-quoted
	// pattern, a pattern without the anchor — is a spelling this check does
	// not read, and is reported rather than passed over.
	known := regexp.MustCompile(`grep -E '\^\(cmd\|internal\)/'|grep -v '(\^(?:cmd|internal)/[^/']+/|_test\\\.go\$|/testdata/)'`)
	if n, read := strings.Count(line, "grep "), len(known.FindAllString(line, -1)); n != read {
		t.Fatalf("the shipped= line has %d grep clauses and this check reads %d of them — a spelling it does not know:\n%s", n, read, line)
	}
	return got
}
