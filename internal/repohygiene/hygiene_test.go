package repohygiene_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/repohygiene"
)

// repoRoot is two levels up from this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(wd))
}

// machO is the 32-bit Mach-O magic — the header of the binary that actually got
// committed. Using a real magic number rather than "\x00\x00" keeps the fixture
// honest about what it stands in for.
var machO = []byte{0xCE, 0xFA, 0xED, 0xFE, 0x07, 0x00, 0x00, 0x01}

// pngHead is a real PNG signature followed by the start of the IHDR chunk. The
// NUL bytes are in the length field, so a truncated 8-byte signature would sniff
// as text and quietly make the two image cases below prove nothing.
var pngHead = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func TestOffenseFlagsBuildArtifacts(t *testing.T) {
	cases := []struct {
		name string
		path string
		size int64
		head []byte
		want string // substring the message must contain; "" means no offense
	}{
		{"a stray Mach-O binary", "fakeshim", 2_700_000, machO, "looks like a binary"},
		{"one in a testdata dir", "internal/orchestrator/testdata/fakeshim", 900_000, machO, "looks like a binary"},
		{"an ELF binary", "opossum", 5_000_000, []byte("\x7fELF\x02\x01\x01\x00"), "looks like a binary"},
		{"a large text file", "docs/huge.md", repohygiene.MaxTrackedBytes + 1, []byte("# hello"), "over the"},
		{"an image where art belongs", "docs/assets/readme-banner.png", 238_982, pngHead, ""},
		{"an image somewhere else", "internal/compose/logo.png", 1000, pngHead, "looks like a binary"},
		{"a shell script", "testdata/fake-container.sh", 12_000, []byte("#!/bin/bash\n"), ""},
		{"ordinary Go source", "internal/compose/load.go", 40_000, []byte("package compose\n"), ""},
		{"a file at exactly the limit", "docs/big.md", repohygiene.MaxTrackedBytes, []byte("# hello"), ""},
		// A reviewer's probe arrived exactly this way: named zz_, test-shaped, asserting
		// nothing, swept in by `git add -A` and green in CI.
		{"a reviewer's throwaway probe", "internal/orchestrator/zz_probe_test.go", 1500, []byte("package orchestrator\n"), "throwaway"},
		{"a temp copy", "internal/compose/tmp_load.go", 900, []byte("package compose\n"), "throwaway"},
		{"a debug script", "scripts/dbg_sweep.sh", 400, []byte("#!/bin/bash\n"), "throwaway"},
		{"a real file whose name merely contains those letters", "internal/compose/probes.go", 900, []byte("package compose\n"), ""},
		// The release sync lived here and shipped with every public release, which is
		// how internal process ends up in a distribution.
		{"maintainer-only release tooling", "scripts/sync-public.sh", 4_000, []byte("#!/bin/bash\n"), "maintainer-only"},
		{"anything else that lands there", "scripts/lib/helpers.sh", 900, []byte("#!/bin/sh\n"), "maintainer-only"},
		// The rule is about that directory, not about the word: a script a user runs
		// is fine wherever users would look for it.
		{"a script users are meant to run", "examples/run-demo.sh", 900, []byte("#!/bin/sh\n"), ""},
		{"a directory that merely starts the same way", "scripts-for-users/x.sh", 900, []byte("#!/bin/sh\n"), ""},
		// A driver written to see what a new tool printed, with the shell in the
		// repository root. It compiled, it was not named like a throwaway, and it
		// made the module's root a `package main` — which is what `go install
		// <module>@latest` installs under the product's name.
		{"a driver left at the top", "sweepgen.go", 700, []byte("package main\n"), "top of the repository"},
		{"one that is not even main", "notes.go", 300, []byte("package notes\n"), "top of the repository"},
		{"the same name one level down", "cmd/opossum/main.go", 40_000, []byte("package main\n"), ""},
		{"the module file, which is not Go source", "go.mod", 400, []byte("module github.com/suruseas/opossum\n"), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := repohygiene.Offense(c.path, c.size, c.head)
			switch {
			case c.want == "" && got != "":
				t.Errorf("Offense(%q) rejected a legitimate file: %s", c.path, got)
			case c.want != "" && got == "":
				t.Errorf("Offense(%q) accepted a file it should reject", c.path)
			case c.want != "" && !strings.Contains(got, c.want):
				t.Errorf("Offense(%q) = %q, want it to mention %q", c.path, got, c.want)
			}
		})
	}
}

// The message is the whole point of the gate — a bare "failed" would leave the
// next person to rediscover the `-o` footgun the hard way.
func TestOffenseExplainsTheCause(t *testing.T) {
	msg := repohygiene.Offense("fakeshim", 2_700_000, machO)
	for _, want := range []string{"go build", "-o", "git rm --cached"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the binary-file message should tell the reader about %q, got:\n%s", want, msg)
		}
	}
}

// TestNoBuildArtifactsTracked is the ratchet: it walks what git actually tracks.
// The unit tests above prove Offense rejects the right things; this proves the
// repository is clean by that standard, and stays clean.
func TestNoBuildArtifactsTracked(t *testing.T) {
	root := repoRoot(t)
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Skipf("git ls-files unavailable (not a checkout?): %v", err)
	}

	var findings []string
	checked := 0
	for _, p := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if p == "" {
			continue
		}
		fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			continue // tracked but absent (a partial checkout); not this test's business
		}
		if fi.IsDir() {
			continue // a submodule
		}
		f, err := os.Open(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			t.Fatalf("opening tracked file %s: %v", p, err)
		}
		head := make([]byte, repohygiene.SniffBytes)
		n, _ := f.Read(head)
		f.Close()
		checked++
		if msg := repohygiene.Offense(p, fi.Size(), head[:n]); msg != "" {
			findings = append(findings, msg)
		}
	}

	// Without this floor the test would pass loudly on an empty list — exactly the
	// shape of green-but-guarding-nothing that let the binary through in the first
	// place. The repo has ~200 tracked files; 50 is a floor, not a target.
	if checked < 50 {
		t.Fatalf("only %d tracked files were examined — the walk is not doing its job", checked)
	}
	if len(findings) > 0 {
		t.Errorf("%d tracked file(s) should not be in the repository:\n\n%s",
			len(findings), strings.Join(findings, "\n\n"))
	}
}

// A NUL anywhere in the head counts, including its very last byte — an artifact
// with a long text preamble (a shell wrapper around an embedded payload, say)
// shouldn't slip through by keeping its first few bytes clean.
func TestSniffLooksAtTheWholeHead(t *testing.T) {
	head := append(bytes.Repeat([]byte("a"), repohygiene.SniffBytes-1), 0)
	if msg := repohygiene.Offense("some/artifact", int64(len(head)), head); msg == "" {
		t.Error("a NUL at the last byte of the sniffed head should still be a finding")
	}
	clean := bytes.Repeat([]byte("a"), repohygiene.SniffBytes)
	if msg := repohygiene.Offense("some/text.txt", int64(len(clean)), clean); msg != "" {
		t.Errorf("text with no NUL should pass, got: %s", msg)
	}
}

// A compose file at the top of the repository is a leftover, and the gate says so.
//
// Written from both sides on purpose. A rule that starts out with nothing to find
// is green whether it looks or not, so the cases that must be caught are here
// beside the ones that must not be — examples/ holds real compose files, and a
// name that merely contains one of these is somebody's own file.
func TestAComposeFileAtTheTopIsALeftover(t *testing.T) {
	for _, p := range []string{
		"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml",
		"compose.override.yaml", "compose.opossum.yaml",
		// The file system this is developed on does not tell these apart, so a
		// tracked `Compose.yaml` is what `opossum up` opens when it asks for
		// `compose.yaml`. Refusing one spelling and not the other would guard
		// nothing.
		"Compose.yaml", "COMPOSE.YML", "Docker-Compose.yaml",
	} {
		if got := repohygiene.Offense(p, 40, []byte("services:\n  app:\n    image: app\n")); got == "" {
			t.Errorf("%s should be refused", p)
		} else if !strings.Contains(got, "examples/") {
			t.Errorf("%s: the message should say where one does belong:\n%s", p, got)
		}
	}
	for _, p := range []string{
		// The real ones, which are the reason the rule is about the top level only.
		"examples/hello/compose.yaml",
		"internal/compose/testdata/compose.yaml",
		// A name of somebody's own that happens to contain one.
		"my-compose.yaml",
		"compose.yaml.tmpl",
		"docs/compose.yaml.md",
	} {
		if got := repohygiene.Offense(p, 40, []byte("services:\n")); got != "" {
			t.Errorf("%s should be allowed, got: %s", p, got)
		}
	}
}

// What opossum writes about a project it is running does not belong in the tree.
//
// The same reason as the compose rule and a wider reach: this is state a fresh
// clone acts on, and a project can live in a subdirectory, so it is not only the
// top level. Nothing of the sort is tracked today — which is what makes it cheap
// to say, and also what makes the second half of this table the important half,
// because a rule with nothing to find is green whether it looks or not.
func TestWhatOpossumWritesAtRunTimeIsNotTracked(t *testing.T) {
	for _, p := range []string{
		".opossum/mcp/db.json",
		".opossum/chown-failures.json",
		"examples/hello/.opossum/mcp/web.json",
		"internal/orchestrator/testdata/proj/.opossum/anything",
		// The file system does not tell these apart, so opossum reads back what is
		// tracked under either spelling.
		".Opossum/mcp/db.json", ".OPOSSUM/chown-failures.json",
	} {
		if got := repohygiene.Offense(p, 40, []byte("{}")); got == "" {
			t.Errorf("%s should be refused", p)
		} else if !strings.Contains(got, "run time") {
			t.Errorf("%s: the message should say why it matters:\n%s", p, got)
		}
	}
	for _, p := range []string{
		// Named after it without being it.
		"docs/opossum-state.md",
		"internal/compose/opossum.go",
		"compose.opossum.yaml.md",
		".opossumrc",
		"docs/.opossum-notes.md",
	} {
		if got := repohygiene.Offense(p, 40, []byte("x")); got != "" {
			t.Errorf("%s should be allowed, got: %s", p, got)
		}
	}
}

// No rule here is stricter about case than the file system is.
//
// Written across the rules rather than inside any one of them, because that is
// where the defect lives: each rule used to choose its own comparison, and three
// of them chose the strict one — in three separate sittings, the third of them
// hours after the second had been corrected for exactly this. `Scripts/` was
// still passing when this was written.
//
// A rule added later that compares with `==` fails here, which is the point:
// the table is over the rules, not over one rule's inputs.
func TestNoRuleIsStricterAboutCaseThanTheFileSystem(t *testing.T) {
	for _, tc := range []struct{ rule, lower, upper string }{
		{"maintainer directory", "scripts/release.sh", "Scripts/release.sh"},
		{"compose at the top", "compose.yaml", "Compose.yaml"},
		{"runtime state", ".opossum/mcp/db.json", ".Opossum/mcp/db.json"},
		{"a throwaway name", "zz_tmp.go", "ZZ_TMP.go"},
		// This one's reason differs from the rest — the go tool does tell the
		// spellings apart, and the uppercase one builds nothing — but the
		// answer is the same, and it is here so that a later change of mind
		// about it has to be a change to this table.
		{"a Go file at the top", "sweepgen.go", "SWEEPGEN.GO"},
	} {
		t.Run(tc.rule, func(t *testing.T) {
			lower := repohygiene.Offense(tc.lower, 40, []byte("x"))
			upper := repohygiene.Offense(tc.upper, 40, []byte("x"))
			if lower == "" {
				t.Fatalf("%s is not refused at all, so this compares nothing", tc.lower)
			}
			if upper == "" {
				t.Errorf("%s is refused but %s is not — the file system does not tell them apart, "+
					"so whichever is tracked is the one that gets read", tc.lower, tc.upper)
			}
		})
	}
}
