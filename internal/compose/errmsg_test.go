package compose

// Error-quality evals (#277): the compose errors a user hits most — a YAML typo,
// an undefined reference, a bad duration/resource value — must say not just WHAT is
// wrong but HOW to fix it (a concrete next step). These golden-substring tests lock
// that in so a future edit can't quietly regress a message back to a raw passthrough.

import (
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// loadErr loads a compose body expected to fail and returns the error text.
func loadErr(t *testing.T, body string) string {
	t.Helper()
	_, err := Load(writeTemp(t, body))
	if err == nil {
		t.Fatal("expected a load error, got nil")
	}
	return err.Error()
}

func TestErrMsgInvalidYAML(t *testing.T) {
	// A malformed compose file (bad indentation) is the most-hit failure — the
	// message must frame it as invalid YAML and point at the fix, not just dump the
	// raw go-yaml error.
	s := loadErr(t, "services:\n  web:\n  image: web\n   bad: : :\n")
	if !strings.Contains(s, "not valid YAML") || !strings.Contains(s, "indentation") {
		t.Errorf("YAML parse error should name the problem and hint at the fix, got: %s", s)
	}
}

func TestErrMsgUndefinedSecret(t *testing.T) {
	s := loadErr(t, `
services:
  web:
    image: web
    secrets: [dbpass]
`)
	if !strings.Contains(s, "undefined secret") || !strings.Contains(s, "top-level secrets:") {
		t.Errorf("undefined-secret error should say how to declare it, got: %s", s)
	}
}

func TestErrMsgUnknownDependency(t *testing.T) {
	s := loadErr(t, `
services:
  web:
    image: web
    depends_on: [db]
`)
	if !strings.Contains(s, "unknown service") || !strings.Contains(s, "depends_on") {
		t.Errorf("unknown-dependency error should say how to fix it, got: %s", s)
	}
}

func TestErrMsgUnsupportedCondition(t *testing.T) {
	s := loadErr(t, `
services:
  web:
    image: web
    depends_on:
      db:
        condition: service_bogus
  db:
    image: postgres
`)
	if !strings.Contains(s, "service_healthy") || !strings.Contains(s, "service_completed_successfully") {
		t.Errorf("unsupported-condition error should list the valid conditions, got: %s", s)
	}
}

func TestErrMsgBadDuration(t *testing.T) {
	// interval: 5 (no unit) is a classic mistake — Go's raw "missing unit" is
	// cryptic; the message must show a unit example.
	s := loadErr(t, `
services:
  web:
    image: web
    healthcheck:
      test: ["CMD", "true"]
      interval: 5
`)
	if !strings.Contains(s, "is not a duration") || !strings.Contains(s, "30s") {
		t.Errorf("bad-duration error should show a unit example, got: %s", s)
	}
	// A semantic error surfaced through the top-level decode must NOT be mislabeled
	// as a YAML syntax problem (that framing is only for real parse errors).
	if strings.Contains(s, "not valid YAML") {
		t.Errorf("a bad duration is a semantic error, not invalid YAML, got: %s", s)
	}
}

func TestErrMsgBadMemory(t *testing.T) {
	s := loadErr(t, `
services:
  web:
    image: web
    mem_limit: "lots"
`)
	if !strings.Contains(s, "memory") || !strings.Contains(s, "512m") {
		t.Errorf("bad-memory error should show a value example, got: %s", s)
	}
}

func TestErrMsgBadCPUs(t *testing.T) {
	s := loadErr(t, `
services:
  web:
    image: web
    cpus: "two"
`)
	if !strings.Contains(s, "cpus") || !strings.Contains(s, "1.5") {
		t.Errorf("bad-cpus error should show a value example, got: %s", s)
	}
}

// Four different things can go wrong at the same decode, and they need four
// different sentences. The one that was wrong is the middle one: a file that
// parses cleanly but holds a value of the wrong shape was announced as "not valid
// YAML", followed by "check the indentation and quoting" — sending the reader to
// hunt for a syntax mistake in a file that has none. The shape they actually hit
// comes from a `${...}` reference to a variable nobody set, so the value they are
// being told to inspect looks empty and blameless.
//
// Telling them apart by the "yaml:" prefix does not work, which is how this went
// wrong: the library writes that prefix on syntax errors and on the errors it
// collects during decoding alike. The collected ones arrive as a *yaml.TypeError
// — but that box holds two unrelated complaints, a value of the wrong shape and a
// key set twice, so the box alone is not the answer either. Reaching for it and
// stopping there gave a key set twice the shape wording and a line about unset
// variables, which was further from the truth than what it replaced.
//
// docker compose v5.3.1 keeps them apart too (measured 2026-08-21): the same file
// gets `volumes.data must be a mapping or null`, not a complaint about YAML.
//
// Each case says what its message must NOT contain as well as what it must. A
// message that carried all three sets of words would pass a test that only looks
// for its own.
func TestTheWaysADecodeFailsSayDifferentThings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		want    []string
		wantNot []string
	}{
		{
			name: "a value of the wrong shape",
			body: "services:\n  app:\n    image: app\nvolumes:\n  data: ${NOPE}\n",
			// The last two are a fixed sentence, so they say nothing about this
			// input on their own — `data: hello` gets them too. The line the
			// decoder produced is what ties the message to what actually happened,
			// so that is asserted as well.
			want: []string{"parsed, but a value is not the shape", "cannot unmarshal", "VolumeDecl", "${...}", "not set"},
			// The two ways of sending the reader somewhere there is nothing to find.
			wantNot: []string{"is not valid YAML", "check the indentation and quoting"},
		},
		{
			// The same key twice arrives in the same box as a shape error and needs
			// the opposite advice: nothing here is about a value's shape, and
			// nothing is about a variable.
			name:    "the same key set twice",
			body:    "services:\n  app:\n    image: a\n    image: b\n",
			want:    []string{"sets the same key twice", "already defined at line", "remove one"},
			wantNot: []string{"is not valid YAML", "parsed, but a value is not the shape", "${...}"},
		},
		{
			// Both at once: there is a real shape problem, so the shape wording is
			// the one that has to win. A file where only some complaints are about
			// duplicates is not a duplicates problem.
			name:    "a duplicate key and a bad shape together",
			body:    "services:\n  app:\n    image: a\n    image: b\nvolumes:\n  data: ${NOPE}\n",
			want:    []string{"parsed, but a value is not the shape", "cannot unmarshal"},
			wantNot: []string{"sets the same key twice", "is not valid YAML"},
		},
		{
			name:    "a real syntax mistake",
			body:    "services:\n  app:\n   image: app\n  bad\n",
			want:    []string{"is not valid YAML", "check the indentation and quoting"},
			wantNot: []string{"parsed, but a value is not the shape"},
		},
		{
			name:    "a value the field understands but rejects",
			body:    "services:\n  app:\n    image: app\n    healthcheck:\n      test: [\"CMD\", \"true\"]\n      interval: notaduration\n",
			want:    []string{"is not a duration", "30s"},
			wantNot: []string{"is not valid YAML", "parsed, but a value is not the shape"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unsetHostVars(t, "NOPE")
			got := loadErr(t, tc.body)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("the message should carry %q:\n%s", w, got)
				}
			}
			for _, w := range tc.wantNot {
				if strings.Contains(got, w) {
					t.Errorf("the message should not carry %q — that is another failure's words:\n%s", w, got)
				}
			}
		})
	}
}

// allDuplicateKeys decides which of two opposite messages a decode failure gets,
// so its edges are worth stating rather than inferring from the messages. The
// empty case cannot arrive from the library today — but "every complaint is about
// a duplicate" is vacuously true of no complaints, and answering yes to that would
// tell somebody to remove one of no keys.
func TestAllDuplicateKeysOnlySaysYesWhenEveryComplaintIsOne(t *testing.T) {
	const dup = `line 4: mapping key "image" already defined at line 3`
	const shape = "line 5: cannot unmarshal !!str `` into compose.VolumeDecl"
	for _, tc := range []struct {
		name string
		errs []string
		want bool
	}{
		{"no complaints at all", nil, false},
		{"one duplicate", []string{dup}, true},
		{"two duplicates", []string{dup, dup}, true},
		{"one of each", []string{dup, shape}, false},
		{"a shape complaint alone", []string{shape}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := allDuplicateKeys(&yaml.TypeError{Errors: tc.errs}); got != tc.want {
				t.Errorf("allDuplicateKeys(%q) = %v, want %v", tc.errs, got, tc.want)
			}
		})
	}
}

// Splitting the compose file in two reaches a different line of code: merging
// reads each file on its own before the final decode, so a key set twice is found
// there. That road used to give those readers the advice this change replaced,
// and it now gives them the same words — plus the name of the file the key is in,
// which the final decode cannot do, because by then the files are one document.
//
// "The same words" is not the same as "the same classification". The pre-pass
// reads into a plain map, which cannot notice a value of the wrong shape at all,
// so a file with BOTH a key set twice and a bad shape is called a duplicate here
// and a shape problem when it is read alone. That is stated below rather than
// claimed away: it is a true thing to say about that file either way, and the
// reader who fixes the duplicate meets the shape problem on the next run. An
// earlier version of this test was named for a parity it did not check.
func TestSplittingTheFileReachesTheSameWords(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	over := filepath.Join(dir, "over.yaml")
	mustWriteFile(t, base, "services:\n  app:\n    image: app\n")

	for _, tc := range []struct {
		name    string
		body    string
		want    []string
		wantNot []string
	}{
		{
			name:    "a key set twice in the second file",
			body:    "services:\n  app:\n    image: a\n    image: b\n",
			want:    []string{"sets the same key twice", "over.yaml", "remove one"},
			wantNot: []string{"is not valid YAML", "parsed, but a value is not the shape", "base.yaml"},
		},
		{
			// The one the pre-pass sees differently, written down so a change to it
			// is a decision rather than a surprise.
			name:    "a key set twice beside a bad shape",
			body:    "services:\n  app:\n    image: a\n    image: b\nvolumes:\n  data: \"\"\n",
			want:    []string{"sets the same key twice", "over.yaml"},
			wantNot: []string{"parsed, but a value is not the shape"},
		},
		{
			name:    "a real syntax mistake in the second file",
			body:    "services:\n  app:\n   image: app\n  bad\n",
			want:    []string{"is not valid YAML", "over.yaml"},
			wantNot: []string{"sets the same key twice", "base.yaml"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mustWriteFile(t, over, tc.body)
			_, err := LoadFiles([]string{base, over}, nil)
			if err == nil {
				t.Fatal("the pair loaded")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the message should carry %q:\n%v", w, err)
				}
			}
			for _, w := range tc.wantNot {
				if strings.Contains(err.Error(), w) {
					t.Errorf("the message should not carry %q:\n%v", w, err)
				}
			}
		})
	}
}

// A value of the wrong shape is still a value, and it can have come from a
// `${...}` reference — which is where passwords and tokens live. The decoder
// writes it into its own message, so the message has to be taken apart before it
// is handed on.
//
// The check is on the first seven characters, not the whole secret: the decoder
// shortens anything over ten to seven and an ellipsis, so looking for the whole
// thing would pass while the front of a token — the part that says what kind it
// is — went out. That is the same trap as looking for a value a `%q` had escaped.
//
// Both directions. Taking the value out is easy to overdo, and a complaint with
// no line, no kind and no field would leave the reader worse off than the leak
// did.
func TestAValueOfTheWrongShapeIsNotReadBack(t *testing.T) {
	const secret = "ghp-canary-do-not-print"
	const front = "ghp-can" // as much of it as the decoder would ever write
	for _, tc := range []struct {
		name  string
		body  string
		env   map[string]string
		keeps []string
	}{
		{
			name:  "a value from a variable",
			body:  "services:\n  app:\n    image: app\nvolumes:\n  data: ${SECRET}\n",
			env:   map[string]string{"SECRET": secret},
			keeps: []string{"line 5", "!!str", "VolumeDecl"},
		},
		{
			name:  "a value written into the file",
			body:  "services:\n  app:\n    image: app\nnetworks:\n  n: \"" + secret + "\"\n",
			keeps: []string{"line 5", "!!str", "NetworkDecl"},
		},
		{
			// A backtick in the value must not leave half of it behind.
			name:  "a value holding a backtick",
			body:  "services:\n  app:\n    image: app\nvolumes:\n  data: \"`" + secret + "\"\n",
			keeps: []string{"line 5", "!!str", "VolumeDecl"},
		},
		{
			// A key set twice is named, not quoted, and a name has to survive whole
			// — the reader has to be able to find it. A key with two backticks in it
			// would otherwise come out with its middle removed, pointing at
			// something that is not in the file. It takes a shape error in the same
			// file to reach this, which is why it is here and not with the
			// duplicates.
			name:  "a key holding backticks, beside a bad shape",
			body:  "services:\n  \"a`b`c\":\n    image: app\n  \"a`b`c\":\n    image: app2\nvolumes:\n  data: \"" + secret + "\"\n",
			keeps: []string{"line 7", "!!str", "VolumeDecl", "a`b`c", "already defined"},
		},
		{
			// Nothing is quoted for a sequence, so nothing should be removed.
			name:  "a sequence, which quotes nothing",
			body:  "services:\n  app:\n    image: app\nvolumes:\n  data: [a, b]\n",
			keeps: []string{"line 5", "!!seq", "VolumeDecl"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unsetHostVars(t, "SECRET")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got := loadErr(t, tc.body)
			if !strings.Contains(got, "parsed, but a value is not the shape") {
				t.Fatalf("a different failure got there first, so this checked nothing:\n%s", got)
			}
			if strings.Contains(got, front) {
				t.Errorf("the message reads the value back:\n%s", got)
			}
			// And structurally, because looking for the secret only finds a leak
			// that kept its first letter: a rule that took one character too few or
			// too many would put most of the value out and slip past the check
			// above. The decoder puts the value it quotes in backticks and nothing
			// else on that line, so the line having none is the property.
			for _, line := range strings.Split(got, "\n") {
				if strings.Contains(line, "cannot unmarshal") && strings.Contains(line, "`") {
					t.Errorf("the decoder's line still quotes something:\n%s", line)
				}
			}
			for _, k := range tc.keeps {
				if !strings.Contains(got, k) {
					t.Errorf("the message should still carry %q — without it there is nothing to act on:\n%s", k, got)
				}
			}
		})
	}
}

// The line a failure names has to be the line in the reader's file.
//
// Expansion writes a marker where a reference produced nothing, and the marker is
// taken back out after parsing. Rebuilding the document to do that changed how
// many lines it had — blank lines went away, a literal block collapsed, a flow
// sequence opened out — so every later failure named a line that was several off
// from the one in front of the reader. It moved BOTH ways, which is why both are
// here: a test written only on a document that shrinks passes on a rule that
// happens to shift the other way, and this defect shipped once already.
//
// The controls matter as much as the cases. A document with no reference, and one
// whose reference is set, go through a different path and were always right; if
// they ever start failing, the repair has begun rewriting documents it should not
// touch.
//
// One compose file only. Splitting the file across several `-f` arguments merges
// them into a new document before the final decode, so the line a failure names
// there is a line in the merged text — the same problem this fixes, by a road
// this does not reach. Tracked separately; what is written down here is what is
// true.
func TestAFailureNamesTheLineInTheReadersFile(t *testing.T) {
	unsetHostVars(t, "NOPE")
	for _, tc := range []struct {
		name string
		body string
		want string // the line the reader would count to
	}{
		{
			// Blank lines are dropped when the document is rebuilt: it shrinks.
			name: "blank lines, and a reference that empties",
			body: "services:\n  app:\n    image: app\n\n    environment:\n      X: ${NOPE}\n\nvolumes:\n  data: \"\"\n",
			want: "line 9",
		},
		{
			// A literal block is rewritten as a quoted scalar: it shrinks, and by
			// more the longer the block is.
			name: "a literal block, and a reference that empties",
			body: "services:\n  app:\n    image: app\n    command: |\n      one ${NOPE}\n      two\n      three\n      four\nvolumes:\n  data: \"\"\n",
			want: "line 10",
		},
		{
			// A flow sequence was opened out into a block: it GREW. Measured: this
			// case is only red when the flow-flattening goes back too, not on the
			// rebuild alone — so what it holds is that specific pair, not "any rule
			// that shifts the other way". Kept because growing is real (a long
			// plain scalar gets folded at about eighty columns, which this does not
			// cover) and because the other three would pass on a shrink-only rule.
			name: "a flow sequence, and a reference that empties",
			body: "services:\n  app:\n    image: app\n    command: [a, ${NOPE}, b]\nvolumes:\n  data: \"\"\n",
			want: "line 6",
		},
		{
			// The same file with the variable set: no marker, no rebuild.
			name: "control: the reference is set",
			body: "services:\n  app:\n    image: app\n\n    environment:\n      X: ${SET_ONE}\n\nvolumes:\n  data: \"\"\n",
			want: "line 9",
		},
		{
			name: "control: no reference at all",
			body: "services:\n  app:\n    image: app\n\n    environment:\n      X: 1\n\nvolumes:\n  data: \"\"\n",
			want: "line 9",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SET_ONE", "1")
			got := loadErr(t, tc.body)
			if !strings.Contains(got, "parsed, but a value is not the shape") {
				t.Fatalf("a different failure got there first, so this checked nothing:\n%s", got)
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("the failure should name %s — the line in the file as written:\n%s", tc.want, got)
			}
		})
	}
}
