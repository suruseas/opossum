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

// literal is the marker as bytes, built rather than typed: a source file that
// spelled it out would be the first thing the sweep below reports.
var literal = string(repohygiene.EmptiedMarker)

// What the marker is, written out once as bytes so the rest of this file can go
// on being self-referential without the whole thing floating.
//
// Everything else here asks its questions in terms of EmptiedMarker, which
// means moving the constant moves the questions with it: changing it to U+E001
// leaves this package green — and leaves a tracked file carrying a real U+E000
// unreported, which is the one thing the sweep exists for. Review measured
// exactly that. So the value is pinned to bytes here, and to the constant the
// other half of the pair uses in internal/compose, which is what actually gets
// written into a document at run time. A marker guarded here but not written
// there would be a sweep for something nobody produces.
func TestTheMarkerIsTheOneThatGetsWritten(t *testing.T) {
	if got := []byte(string(repohygiene.EmptiedMarker)); !bytes.Equal(got, []byte{0xEE, 0x80, 0x80}) {
		t.Errorf("EmptiedMarker encodes to % x, want ee 80 80 (U+E000)", got)
	}
	// internal/compose keeps its own copy, unexported. Read it as source rather
	// than importing: what matters is that the two spellings agree, and the
	// spelling there is the escape (as it must be, or this sweep would report
	// that file).
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "compose", "interpolate.go"))
	if err != nil {
		t.Fatalf("reading the other half of the pair: %v", err)
	}
	if want := `const emptied = "\ue000"`; !strings.Contains(string(src), want) {
		t.Errorf("internal/compose no longer declares %s — the sweep here and the marker "+
			"written there have to be the same character, or one of them is guarding "+
			"nothing", want)
	}
}

// No tracked file carries a literal U+E000.
//
// The character is invisible, so a stray one is not caught by reading. It is
// caught by what it does: code that looks for the marker fires on the fixture
// carrying it, so a test reaches its branch and passes for the wrong reason.
// That is not a hypothetical — it happened in #436, and the sweep it hollowed
// out stayed green until review found it.
//
// This is a ratchet, put in while the count is zero: nothing needs cleaning up,
// and the next one to arrive is reported instead of working (#438).
func TestNoTrackedFileCarriesTheInvisibleMarker(t *testing.T) {
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
		full := filepath.Join(root, filepath.FromSlash(p))
		fi, err := os.Stat(full)
		if err != nil || fi.IsDir() {
			continue // absent in a partial checkout, or a submodule
		}
		// The whole file, not a head: the point of this character is that it
		// can sit anywhere and be seen nowhere.
		content, err := os.ReadFile(full)
		if err != nil {
			t.Fatalf("reading tracked file %s: %v", p, err)
		}
		checked++
		if msg := repohygiene.InvisibleLiteral(p, content); msg != "" {
			findings = append(findings, msg)
		}
	}

	// The same floor the other sweep keeps, for the same reason: a walk that
	// found nothing and a walk that looked at nothing report identically.
	if checked < 50 {
		t.Fatalf("only %d tracked files were examined — the walk is not doing its job", checked)
	}
	if len(findings) > 0 {
		t.Errorf("%d tracked file(s) carry a character nobody can see:\n\n%s",
			len(findings), strings.Join(findings, "\n\n"))
	}
}

// Whether the sweep above is green because it looked or because there is
// nothing to find. It cannot answer that itself — every file is clean today, so
// the walk and a walk over an empty list agree. So the checker is asked
// directly, with content that carries one.
func TestTheInvisibleMarkerIsActuallyLookedFor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    bool
	}{
		{name: "alone", content: literal, want: true},
		{name: "inside a word", content: "SECRET" + literal + "CANARY", want: true},
		{name: "on a later line", content: "a\nb\nc" + literal + "\n", want: true},
		// The escaped spelling is what a person writes when they mean the
		// character and want it visible: backslash, u, e, 0, 0, 0. Letting it
		// through is the distinction this whole check rests on. It is built
		// from a rune so that neither this line nor a later edit can smuggle
		// the real character in — which is how the first version of this case
		// came to hold seven characters while its comment said six.
		{name: "written as an escape", content: `x := "` + string(rune(92)) + `ue000"`, want: false},
		{name: "nothing", content: "plain text\n", want: false},
		// A neighbouring private-use character is not this one. The range was
		// deliberately not widened: one character, until something asks for more.
		{name: "the next private-use character", content: string(repohygiene.EmptiedMarker + 1), want: false},
		// A file with a NUL in its head is not text, and the advice this gives
		// — write the escape instead — has no meaning for one. Three bytes
		// turning up by chance in a compressed image would otherwise be a red
		// with no way out.
		// Past the window the binary sniff looks at. Reading only the head
		// would be a one-token edit now that a head exists, and the tracked
		// files here run to hundreds of thousands of bytes — a sweep that
		// stopped at 8000 would be hollow for most of the repository and say
		// nothing about it.
		{name: "past the sniff window", content: strings.Repeat("a", repohygiene.SniffBytes+10) + literal, want: true},
		// A NUL is what says "not text", and only a NUL. The head here starts
		// with one so that widening the sniff to some other byte is a change
		// this case notices.
		{name: "inside something binary", content: "\x00" + literal, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := repohygiene.InvisibleLiteral("fixture.txt", []byte(tc.content)) != ""
			if got != tc.want {
				// The content is described rather than printed: one case is
				// longer than the sniff window, and a %q of it would bury the
				// line that matters.
				t.Errorf("InvisibleLiteral(%d bytes) reported %v, want %v", len(tc.content), got, tc.want)
			}
		})
	}
}

// The message has to make an invisible thing findable: which file, where in it,
// and how to look. A reader who cannot type the character has no way to grep
// for it.
func TestTheMessageSaysWhereToLook(t *testing.T) {
	msg := repohygiene.InvisibleLiteral("testdata/probe.txt", []byte("line one\nline two"+literal+"\n"))
	if msg == "" {
		t.Fatal("no finding for content that carries the marker")
	}
	// `sed -n '2,3p'` is where byte 17 lands with od's sixteen bytes to a line,
	// and the two-line window is there because the three bytes can straddle a
	// boundary. Reading the numbers back is the only thing that holds the
	// arithmetic; a check for the command alone passes on any of them.
	for _, want := range []string{"testdata/probe.txt", "byte 17", "line 2", "LC_ALL=C od -v -c", "sed -n '2,3p'", "\\ue000"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message should carry %q so the reader can find it, got:\n%s", want, msg)
		}
	}
}
