package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A build that fails because a registry would not hand over an image the
// Dockerfile names was answered with the advice every build failure gets —
// build it with Docker and import it — which has nothing to do with a tag that
// is not there (#1104). The shapes below are the runtime's own output, read out
// of the capture rather than retyped: what is matched is upstream's wording,
// and a row written from memory would go on passing after upstream changed it.
const buildRefusedCapture = "../../testdata/error-wordings/build-image-refused-141.txt"

const refusedHint = "a registry refused an image this build pulls"

// captured returns what the runtime wrote for the one build in the capture
// whose command line ends in dir, as the capture holds it.
func captured(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(buildRefusedCapture)
	if err != nil {
		t.Fatal(err)
	}
	_, after, ok := strings.Cut(string(raw), "-t neko1104-"+dir+" "+dir+"\n")
	if !ok {
		t.Fatalf("the capture has no build of %q", dir)
	}
	_, after, ok = strings.Cut(after, " lines) ---\n")
	if !ok {
		t.Fatalf("the capture of %q has no stderr section", dir)
	}
	body, _, _ := strings.Cut(after, "\n\n")
	if !strings.Contains(body, "Error: ") {
		t.Fatalf("the capture of %q does not end in the runtime's error: %q", dir, body)
	}
	return body + "\n"
}

// replayBuild returns a Runtime whose `container` writes output to stderr and
// exits 1. Stderr, because that is where the runtime writes all of a build's
// output (the capture says so: stdout is 0 bytes) — the detector reads both
// streams, and a replay through the other one would pass with the stream a real
// build uses left unread.
func replayBuild(t *testing.T, output string) *Runtime {
	t.Helper()
	f := filepath.Join(t.TempDir(), "err.txt")
	if err := os.WriteFile(f, []byte(output), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Runtime{Bin: fakeShimBin, Env: []string{"SHIM_ERR=" + f, "SHIM_EXIT=1"}}
}

func TestABuildTheRegistryRefusedIsToldAsOne(t *testing.T) {
	for _, tc := range []struct {
		dir, shape string
		want       bool
	}{
		{"a", "a tag that is not there (404, wrapped as `unknown`)", true},
		{"b", "a repository that is not there (401, wrapped as `internalError`)", true},
		{"c", "a repository that cannot be seen (401, another registry)", true},
		{"d", "the second stage of two", true},
		{"e", "an image named by COPY --from", true},
		// Not refusals. The first is an ordinary failing step. The second prints
		// the refusal's own text from inside a step, and the runtime's closing
		// line quotes it — the mirror of the shape above, and the one a match on
		// the words alone would take for it.
		{"f", "an ordinary failing step", false},
		{"g", "a step that prints the refusal's own words", false},
	} {
		t.Run(tc.shape, func(t *testing.T) {
			r := replayBuild(t, captured(t, tc.dir))
			err := r.Build(BuildOptions{Tag: "x:1", Context: t.TempDir(), Redo: "opossum up"})
			if err == nil {
				t.Fatal("expected a build error")
			}
			if got := strings.Contains(err.Error(), refusedHint); got != tc.want {
				t.Errorf("want the refusal told: %v, got: %v", tc.want, err)
			}
			// What a caller branches on, asked separately from the prose: the
			// two have to agree, or a caller drops its own advice for a failure
			// that was never explained.
			if got := errors.Is(err, ErrBuildImageRefused); got != tc.want {
				t.Errorf("errors.Is(err, ErrBuildImageRefused) = %v, want %v", got, tc.want)
			}
			if tc.want && !strings.Contains(err.Error(), "opossum up") {
				t.Errorf("the hint should end in the command to type again, got: %v", err)
			}
		})
	}
}

// The decision, asked directly, one condition a row. The line below is shape
// (a) from the capture, and each row changes one thing about it.
func TestWhatIsReadAsARefusedImage(t *testing.T) {
	const line = `Error: unknown: "HTTP request to https://registry-1.docker.io/v2/library/alpine/manifests/nosuchtag-neko1104 failed with response: 404 Not Found. Reason: Unknown"`
	if !strings.Contains(captured(t, "a"), line) {
		t.Fatal("the line these rows vary is no longer the one in the capture")
	}
	for _, tc := range []struct {
		name   string
		writes []string
		want   bool
	}{
		{"the runtime's line", []string{line + "\n"}, true},
		{"the same line with nothing after it", []string{line}, true},
		{"after other output", []string{"#1 [resolver] fetching image...\n", line + "\n"}, true},
		{"split across two writes", []string{line[:40], line[40:] + "\n"}, true},
		{"split inside the first word", []string{"Err", "or: " + line[len("Error: "):] + "\n"}, true},
		// The wrapper names a kind and the kind varies with the answer (measured:
		// `unknown` for a 404, `internalError` for a 401), so it is not read.
		{"another kind of wrapper", []string{strings.Replace(line, "unknown", "internalError", 1) + "\n"}, true},
		// Everything a step writes has the builder's prefix in front of it: the
		// step's number and the seconds elapsed, whatever they were.
		{"a step's copy of the line", []string{"#5 0.045 " + line + "\n"}, false},
		{"a step's copy, in the closing summary", []string{"0.045 " + line + "\n"}, false},
		{"a step that returns the carriage first", []string{"#5 0.045 progress\r" + line + "\n"}, false},
		// The runtime's closing line for a failed step quotes the step's command.
		{"the closing line of a failed step that quotes it",
			[]string{`Error: unknown: "failed to solve: process "/bin/sh -c echo '` + line + `'" did not complete successfully: exit code: 1"` + "\n"}, false},
		// Not measured through a build, so left as it was: a layer, a token.
		{"a request for a layer", []string{strings.Replace(line, "/manifests/", "/blobs/", 1) + "\n"}, false},
		{"a request that names no manifest", []string{strings.Replace(line, "/manifests/nosuchtag-neko1104", "/token", 1) + "\n"}, false},
		{"a request that was not refused", []string{strings.Replace(line, " failed with response: ", " returned: ", 1) + "\n"}, false},
		// The line is read from its first byte, and these two are what say so.
		// The rows above with a prefix in front are also turned away by what
		// the prefix does to the kind (a space in it), so they would pass with
		// the start of the line not looked at; here the kind is one word and
		// the only thing wrong is where `Error: ` is. What is in front of the
		// kind is exactly as long as `Error: ` (seven bytes) on purpose: a
		// reading that found `Error: ` anywhere and then skipped seven bytes
		// lands on the kind, and answers yes.
		{"`Error: ` further along a step's line",
			[]string{`#5 0.1 x: "HTTP request to https://r/v2/a/manifests/b failed with response: 404 Not Found" Error: y` + "\n"}, false},
		{"`Error: ` at the end of the line",
			[]string{`0.1234 x: "HTTP request to https://r/v2/a/manifests/b failed with response: 404 Not Found", Error: ` + "\n"}, false},
		{"a tab inside the kind", []string{strings.Replace(line, "unknown", "un\tknown", 1) + "\n"}, false},
		{"no kind of wrapper at all", []string{strings.Replace(line, "unknown", "", 1) + "\n"}, false},
		{"a wrapper of two words", []string{strings.Replace(line, "unknown:", "not known:", 1) + "\n"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &buildErrorDetector{}
			for _, w := range tc.writes {
				if _, err := d.Write([]byte(w)); err != nil {
					t.Fatal(err)
				}
			}
			if got := strings.Contains(d.hint(""), refusedHint); got != tc.want {
				t.Errorf("told as a refused image: %v, want %v (hint: %q)", got, tc.want, d.hint(""))
			}
		})
	}
}

// A failure the detector already explained keeps its explanation. What happens
// when a full disk and a refusal arrive together has not been measured, so the
// three older signatures answer first and nothing about them changes — the
// caller is not told the image was refused either, since that is not what it
// was told the failure was.
func TestAnOlderSignatureStillAnswersFirst(t *testing.T) {
	refusal := captured(t, "a")
	for _, tc := range []struct{ name, also, want string }{
		{"disk full", "failed to solve: write blob: no space left on device\n", "ran out of disk space"},
		{"resources", "rpc error: code = Unavailable desc = error reading from server: EOF\n", "start --cpus 4 --memory 8g"},
		{"cache", "failed to load cache key: unable to read root manifest\n", "builder cache looks corrupted"},
	} {
		// Either way round: which of the two was written first is not what
		// decides, and one order alone would pass with "the first one seen".
		for _, order := range []struct{ name, output string }{
			{"before the refusal", tc.also + refusal},
			{"after the refusal", refusal + tc.also},
		} {
			t.Run(tc.name+"/"+order.name, func(t *testing.T) {
				r := replayBuild(t, order.output)
				err := r.Build(BuildOptions{Tag: "x:1", Context: t.TempDir()})
				if err == nil {
					t.Fatal("expected a build error")
				}
				if !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), refusedHint) {
					t.Errorf("want the older hint alone, got: %v", err)
				}
				if errors.Is(err, ErrBuildImageRefused) {
					t.Errorf("the failure was told as something else, so it is not reported as a refusal: %v", err)
				}
			})
		}
	}
}

// A long line is a step's output, not the runtime's closing line, and holding
// all of it to look at its first bytes is not needed: only the start decides.
// The bound is written out here rather than read from the code — a test that
// compared against the constant would agree with whatever the constant became.
func TestALongLineIsNotHeldWhole(t *testing.T) {
	d := &buildErrorDetector{}
	long := "#5 0.1 " + strings.Repeat("x", 1<<20)
	if _, err := d.Write([]byte(long)); err != nil {
		t.Fatal(err)
	}
	if len(d.line) > 16<<10 {
		t.Errorf("held %d bytes of one line; a refusal is a few hundred, so a few KiB is all that is needed", len(d.line))
	}
	// And the line after it is still read from its own start.
	const line = `Error: unknown: "HTTP request to https://r/v2/a/manifests/b failed with response: 404 Not Found"`
	if _, err := d.Write([]byte("\n" + line + "\n")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.hint(""), refusedHint) {
		t.Errorf("the line after a long one should still be read, got: %q", d.hint(""))
	}
}

// What is let through of an over-long line is still that line. A step can write
// the refusal's words anywhere in a line of any length; if the part past the
// bound were read as though a new line began there, the words would be at a
// "line start" that is not one. Tried at every offset around the bound, since
// the wrong reading only shows when the words land exactly where holding stops.
//
// The offsets are made from the bound itself. That is not the second reading of
// one value the test above avoids: this is a fixture that has to reach the
// bound wherever it is, and a bound written out here would go quiet — green,
// guarding nothing — the day the constant moved. How large the bound may be is
// the other test's to say. Each end of the sweep is checked to be on its own
// side of the bound, so a sweep that stopped crossing it fails rather than
// passes.
func TestTheRestOfALongLineIsNotReadAsANewLine(t *testing.T) {
	const line = `Error: unknown: "HTTP request to https://r/v2/a/manifests/b failed with response: 404 Not Found"`
	const prefix = "#5 0.1 "
	lo, hi := maxHeldLine-len(prefix)-8, maxHeldLine-len(prefix)+8
	for pad := lo; pad <= hi; pad++ {
		d := &buildErrorDetector{}
		// Without its newline first, so the state the line leaves can be seen.
		if _, err := d.Write([]byte(prefix + strings.Repeat("x", pad) + line)); err != nil {
			t.Fatal(err)
		}
		if !d.longLine {
			t.Fatalf("with %d bytes in front, the line did not pass the bound — this sweep is not where the bound is", len(prefix)+pad)
		}
		if _, err := d.Write([]byte("\n")); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(d.hint(""), refusedHint) {
			t.Fatalf("with %d bytes in front of it on the same line, a step's copy was read as the runtime's line", len(prefix)+pad)
		}
	}
	// The other side: a line that stays under the bound is held whole, which is
	// what makes the offsets above the ones where holding stops.
	d := &buildErrorDetector{}
	if _, err := d.Write([]byte(strings.Repeat("x", maxHeldLine))); err != nil {
		t.Fatal(err)
	}
	if d.longLine || len(d.line) != maxHeldLine {
		t.Fatalf("a line of exactly the bound (%d bytes) should be held whole, got %d held, over-long: %v", maxHeldLine, len(d.line), d.longLine)
	}
}
