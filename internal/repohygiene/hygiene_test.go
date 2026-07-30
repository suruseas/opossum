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
