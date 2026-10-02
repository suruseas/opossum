package runtime

import (
	"os"
	"strings"
	"testing"
)

func TestBuildErrorDetector(t *testing.T) {
	cacheHint := func(h string) bool {
		return strings.Contains(h, "delete --force") && !strings.Contains(h, "start --cpus")
	}
	resourceHint := func(h string) bool { return strings.Contains(h, "start --cpus 4 --memory 8g") }
	diskHint := func(h string) bool {
		return strings.Contains(h, "ran out of disk space") && strings.Contains(h, "image prune -f")
	}

	t.Run("cache corruption", func(t *testing.T) {
		d := &buildErrorDetector{}
		d.Write([]byte("#8 failed to load cache key: unable to read root manifest\n"))
		if !cacheHint(d.hint("opossum up")) {
			t.Errorf("cache-corruption hint expected, got: %q", d.hint("opossum up"))
		}
	})
	t.Run("resource exhaustion", func(t *testing.T) {
		d := &buildErrorDetector{}
		d.Write([]byte("Error: unavailable: rpc error: code = Unavailable desc = error reading from server: EOF\n"))
		if !resourceHint(d.hint("opossum up")) {
			t.Errorf("resource hint expected, got: %q", d.hint("opossum up"))
		}
	})
	t.Run("disk full", func(t *testing.T) {
		d := &buildErrorDetector{}
		d.Write([]byte("#12 exporting layers: write /var/lib/.../blob: no space left on device\n"))
		if !diskHint(d.hint("opossum up")) {
			t.Errorf("disk-full hint expected, got: %q", d.hint("opossum up"))
		}
	})
	t.Run("disk full outranks resource exhaustion", func(t *testing.T) {
		// A full volume makes the builder fail downstream (rpc/EOF), so the disk
		// remedy must win — growing the builder would make ENOSPC worse.
		d := &buildErrorDetector{}
		d.Write([]byte("no space left on device\nrpc error: code = Unavailable desc = error reading from server: EOF\n"))
		if !diskHint(d.hint("opossum up")) {
			t.Errorf("disk-full hint should win over the resource hint, got: %q", d.hint("opossum up"))
		}
	})
	t.Run("plain build error gets no hint", func(t *testing.T) {
		d := &buildErrorDetector{}
		d.Write([]byte(`#5 ERROR: process "/bin/sh -c bogus" did not complete successfully: exit code 127` + "\n"))
		if h := d.hint("opossum up"); h != "" {
			t.Errorf("no hint expected for an ordinary build error, got: %q", h)
		}
	})
	t.Run("signature split across writes", func(t *testing.T) {
		d := &buildErrorDetector{}
		d.Write([]byte("rpc error: unable to read "))
		d.Write([]byte("root manifest: ..."))
		if !cacheHint(d.hint("opossum up")) {
			t.Error("a signature straddling two writes should still match")
		}
	})
	t.Run("both signatures pick the resource hint", func(t *testing.T) {
		d := &buildErrorDetector{}
		d.Write([]byte("failed to load cache key\nrpc error: code = Unavailable desc = error reading from server: EOF\n"))
		if !resourceHint(d.hint("opossum up")) {
			t.Errorf("resource hint (the superset remedy) should win when both fire, got: %q", d.hint("opossum up"))
		}
	})
}

// Builds are reached from `opossum up`, `opossum run`, and `opossum build`, and
// each hint ends by telling the reader what to type again. That has to be the
// command they typed — advice to `opossum up` after an `opossum build` failed
// sends them somewhere else (#667). Every hint × every verb, because each of the
// three hints embeds the command in a different sentence and any one of them
// could keep a literal behind.
func TestHintNamesTheCommandThatWasTyped(t *testing.T) {
	verbs := []string{"opossum up", "opossum run", "opossum build"}
	states := map[string]func() *buildErrorDetector{
		"disk full":          func() *buildErrorDetector { return &buildErrorDetector{diskFull: true} },
		"resource exhausted": func() *buildErrorDetector { return &buildErrorDetector{resourceExhausted: true} },
		"cache corrupt":      func() *buildErrorDetector { return &buildErrorDetector{cacheCorrupt: true} },
	}
	for name, mk := range states {
		t.Run(name, func(t *testing.T) {
			for _, verb := range verbs {
				h := mk().hint(verb)
				if !strings.Contains(h, verb) {
					t.Errorf("hint for %q should tell the reader to retype it, got: %q", verb, h)
				}
				// `opossum up` must appear only as the caller's verb, never as a
				// leftover literal beside it.
				for _, other := range verbs {
					if other != verb && strings.Contains(h, other) {
						t.Errorf("hint for %q also names %q: %q", verb, other, h)
					}
				}
			}
			// A caller that never said which command it serves gets wording that
			// names none — naming no command beats naming a wrong one.
			h := mk().hint("")
			for _, verb := range verbs {
				if strings.Contains(h, verb) {
					t.Errorf("with no redo given the hint must not guess %q, got: %q", verb, h)
				}
			}
			if !strings.Contains(h, "rerun the opossum command") {
				t.Errorf("with no redo given the hint should still say to rerun, got: %q", h)
			}
		})
	}
}

// Build hands its caller's command through to the hint — the field exists so the
// orchestrator's up/run/build paths can each name themselves.
func TestBuildHintEchoesTheCallersCommand(t *testing.T) {
	r := replayShim(t, "#12 exporting to image\nfailed to solve: write blob: no space left on device\n", 1)
	err := r.Build(BuildOptions{Tag: "x:1", Context: t.TempDir(), Redo: "opossum run"})
	if err == nil {
		t.Fatal("expected a build error")
	}
	if !strings.Contains(err.Error(), "then: opossum run") {
		t.Errorf("the hint should retype the caller's command, got: %v", err)
	}
	if strings.Contains(err.Error(), "opossum up") {
		t.Errorf("the hint should not name a command nobody typed, got: %v", err)
	}
}

// Build turns a known builder failure into an actionable hint on the error, and
// leaves an ordinary build failure untouched.
func TestBuildAppendsHintOnKnownFailure(t *testing.T) {
	r := replayShim(t, "#4 transferring context\nfailed to load cache key: unable to read root manifest\n", 1)
	err := r.Build(BuildOptions{Tag: "x:1", Context: t.TempDir()})
	if err == nil {
		t.Fatal("expected a build error")
	}
	if !strings.Contains(err.Error(), "container builder delete --force") {
		t.Errorf("build error should carry the recovery hint, got: %v", err)
	}
}

func TestBuildResourceHintOnConnectionDrop(t *testing.T) {
	r := replayShim(t, "#8 resolve image...\nError: unavailable: rpc error: code = Unavailable desc = error reading from server: EOF\n", 1)
	err := r.Build(BuildOptions{Tag: "x:1", Context: t.TempDir()})
	if err == nil {
		t.Fatal("expected a build error")
	}
	if !strings.Contains(err.Error(), "start --cpus 4 --memory 8g") {
		t.Errorf("build error should carry the resource hint, got: %v", err)
	}
}

func TestBuildDiskFullHint(t *testing.T) {
	r := replayShim(t, "#12 exporting to image\nfailed to solve: write blob: no space left on device\n", 1)
	err := r.Build(BuildOptions{Tag: "x:1", Context: t.TempDir()})
	if err == nil {
		t.Fatal("expected a build error")
	}
	if !strings.Contains(err.Error(), "ran out of disk space") || !strings.Contains(err.Error(), "image prune -f") {
		t.Errorf("build error should carry the disk-full recovery hint, got: %v", err)
	}
}

func TestBuildNoHintOnPlainFailure(t *testing.T) {
	r := replayShim(t, "#5 ERROR: exit code 1\n", 1)
	err := r.Build(BuildOptions{Tag: "x:1", Context: t.TempDir()})
	if err == nil {
		t.Fatal("expected a build error")
	}
	if strings.Contains(err.Error(), "hint:") {
		t.Errorf("no hint expected for an ordinary build failure, got: %v", err)
	}
}

// The runtime's closing line for a build the builder ran out of resources for, as 1.5.0 wrote
// it (a `RUN tail /dev/zero`, #1619): the hint for resources has to come with it.
const resourceExhaustedClosing = `Error: resourceExhausted: "failed to solve: ResourceExhausted: process "/bin/sh -c tail /dev/zero" did not complete successfully: cannot allocate memory"`

func TestTheResourceExhaustedClosingLineGetsTheResourceHint(t *testing.T) {
	hasResourceHint := func(h string) bool { return strings.Contains(h, "start --cpus 4 --memory 8g") }
	// The step's own line, which the builder writes before the runtime's closing line.
	stepLine := `#5 ERROR: process "/bin/sh -c tail /dev/zero" did not complete successfully: cannot allocate memory`
	for _, tc := range []struct {
		name   string
		writes []string
		want   bool
	}{
		{"the closing line", []string{stepLine + "\n------\n" + resourceExhaustedClosing + "\n"}, true},
		{"the closing line with no newline at its end", []string{stepLine + "\n" + resourceExhaustedClosing}, true},
		{"the closing line split across two writes", []string{resourceExhaustedClosing[:30], resourceExhaustedClosing[30:] + "\n"}, true},
		{"the step's line alone, which a step that fails for another reason can also write", []string{stepLine + "\n"}, false},
		{"the words in a step's own output", []string{"#5 0.2 cannot allocate memory\n#5 0.2 Error: resourceExhausted: something\n"}, false},
		{"the kind behind the builder's prefix", []string{`#5 0.045 Error: resourceExhausted: "x"` + "\n"}, false},
		{"another kind", []string{`Error: unknown: "failed to solve: process "/bin/sh -c false" did not complete successfully: exit code: 1"` + "\n"}, false},
		// One row for each way the line's start can be read too loosely.
		{"the kind behind the builder's prefix, in two writes", []string{"#5 0.2 ", "Error: resourceExhausted: x\n"}, false},
		{"the kind behind spaces", []string{"  Error: resourceExhausted: x\n"}, false},
		{"the kind in other letters", []string{"Error: resourceexhausted: x\n"}, false},
		{"a kind that only starts with it", []string{"Error: resourceExhaustedFoo: x\n"}, false},
		// A step's command can be long, and the closing line quotes all of it. The command is as long
		// as the bound, so the line is longer than what is held whatever the bound is.
		{"a closing line longer than a line is held", []string{`Error: resourceExhausted: "failed to solve: process "/bin/sh -c ` + strings.Repeat("x", maxHeldLine) + `" did not complete successfully: cannot allocate memory"` + "\n"}, true},
		{"a line that long which is not it", []string{`Error: unknown: "failed to solve: process "/bin/sh -c ` + strings.Repeat("x", maxHeldLine) + `" did not complete successfully: cannot allocate memory"` + "\n"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &buildErrorDetector{}
			for _, w := range tc.writes {
				d.Write([]byte(w))
			}
			if got := hasResourceHint(d.hint("opossum up")); got != tc.want {
				t.Errorf("resource hint = %v, want %v for %q", got, tc.want, tc.writes)
			}
		})
	}
	t.Run("a full disk outranks it", func(t *testing.T) {
		d := &buildErrorDetector{}
		d.Write([]byte("no space left on device\n" + resourceExhaustedClosing + "\n"))
		if h := d.hint("opossum up"); !strings.Contains(h, "ran out of disk space") {
			t.Errorf("the disk hint should win, got: %q", h)
		}
	})
	t.Run("through a build, as the runtime wrote it", func(t *testing.T) {
		// Read out of the capture and replayed on stderr, which is where the runtime writes all
		// of a build's output: a retyped row on stdout would pass with the stream a real build
		// uses left unread.
		raw, err := os.ReadFile("../../testdata/error-wordings/build-resource-exhausted-150.txt")
		if err != nil {
			t.Fatal(err)
		}
		_, after, ok := strings.Cut(string(raw), "--- stderr ---\n")
		if !ok || !strings.Contains(after, resourceExhaustedClosing) {
			t.Fatalf("the capture has no stderr section ending in the closing line: %q", after)
		}
		r := replayBuild(t, after)
		berr := r.Build(BuildOptions{Tag: "x:1", Context: t.TempDir()})
		if berr == nil || !strings.Contains(berr.Error(), "start --cpus 4 --memory 8g") {
			t.Errorf("the build error should carry the resource hint, got: %v", berr)
		}
	})
}
