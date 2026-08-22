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
	// The field and a worked example. Not the value: it is read from the compose
	// file, where a `${...}` reference can put a password in.
	for _, want := range []string{"healthcheck interval", "not a duration", "30s"} {
		if !strings.Contains(s, want) {
			t.Errorf("the bad-duration error should carry %q, got: %s", want, s)
		}
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
			want:    []string{"parsed, but a value is not the shape", "cannot unmarshal", "already defined"},
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
			want:    []string{"not a duration", "30s"},
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
			// A key set twice is named, not quoted, and a name has to survive whole —
			// the reader has to be able to find it.
			//
			// Restored after being deleted from this test with a reason that turned
			// out to be wrong. It was also the only thing holding that a service
			// name written twice is refused at all: a change that read the services
			// one at a time made the second silently win, and nothing else noticed.
			// A test that looks like it is about one thing can be the last guard on
			// another.
			name:  "a service name set twice, beside a bad shape",
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

// Which file a failure is about, when there is more than one.
//
// Several files are merged into a new document before the last check, so the line
// the parser gives counts in that document and no record survives of which file a
// value came from. Naming the first file was a guess dressed as a fact: it sent
// the reader to a file that need not contain the problem, at a line they could not
// find. Both are named instead, and the line is marked as belonging to the merged
// text — the number still orders several failures, so it is the claim about WHERE
// that has to go, not the number.
//
// The single-file case is here as the control: it does name one file and one real
// line, and must keep doing so.
func TestWhichFileAFailureIsAbout(t *testing.T) {
	unsetHostVars(t, "NOPE")
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	over := filepath.Join(dir, "override.yaml")
	// The problem is in override.yaml, which is NOT the file that used to be named.
	mustWriteFile(t, base, "services:\n  app:\n    image: app\n")
	mustWriteFile(t, over, "services:\n  app:\n\n    environment:\n      X: ${NOPE}\n\nvolumes:\n  data: \"\"\n")

	t.Run("several files", func(t *testing.T) {
		_, err := LoadFiles([]string{base, over}, nil)
		if err == nil {
			t.Fatal("the pair loaded")
		}
		got := err.Error()
		if !strings.Contains(got, "parsed, but a value is not the shape") {
			t.Fatalf("a different failure got there first:\n%s", got)
		}
		for _, want := range []string{"base.yaml", "override.yaml", "merged document"} {
			if !strings.Contains(got, want) {
				t.Errorf("the failure should carry %q:\n%s", want, got)
			}
		}
	})

	// Three, because the message said "either file" while naming three of them —
	// an arity the two-file case could not see. Written as a sweep so a fourth
	// costs nothing.
	t.Run("three files", func(t *testing.T) {
		third := filepath.Join(dir, "third.yaml")
		mustWriteFile(t, third, "services:\n  app:\n    user: someone\n")
		_, err := LoadFiles([]string{base, third, over}, nil)
		if err == nil {
			t.Fatal("the three loaded")
		}
		got := err.Error()
		for _, want := range []string{"base.yaml", "third.yaml", "override.yaml", "merged document"} {
			if !strings.Contains(got, want) {
				t.Errorf("the failure should carry %q:\n%s", want, got)
			}
		}
		// Whatever the message says about how many files there are has to hold for
		// three as well as two.
		if strings.Contains(got, "either file") {
			t.Errorf("three files were named and the message calls them two:\n%s", got)
		}
	})

	// A compose file with no services, which takes a different exit and used to
	// name the first file too.
	t.Run("no services, several files", func(t *testing.T) {
		empty1 := filepath.Join(dir, "one.yaml")
		empty2 := filepath.Join(dir, "two.yaml")
		mustWriteFile(t, empty1, "volumes:\n  data:\n")
		mustWriteFile(t, empty2, "networks:\n  net:\n")
		_, err := LoadFiles([]string{empty1, empty2}, nil)
		if err == nil {
			t.Fatal("a project with no services loaded")
		}
		got := err.Error()
		for _, want := range []string{"one.yaml", "two.yaml", "defines no services"} {
			if !strings.Contains(got, want) {
				t.Errorf("the failure should carry %q:\n%s", want, got)
			}
		}
	})

	t.Run("control: one file", func(t *testing.T) {
		_, err := LoadFiles([]string{over}, nil)
		if err == nil {
			t.Fatal("the file loaded")
		}
		got := err.Error()
		// The real line in that file, and no hedging about a merged document.
		if !strings.Contains(got, "line 8") {
			t.Errorf("one file should name the line in it:\n%s", got)
		}
		if strings.Contains(got, "merged document") {
			t.Errorf("one file was not merged with anything:\n%s", got)
		}
	})
}

// A field that checks its own value must not read that value back.
//
// These are the fields opossum understands well enough to reject on its own —
// a memory size, a CPU count, a duration, a mount type. What they are given comes
// out of the compose file, where a `${...}` reference can put a password or a
// token, and the error goes to the terminal, the CI log, and whatever issue the
// output is pasted into. Unlike the decoder's own message, these did not shorten
// what they quoted: the whole value went out.
//
// Both directions, in one table. Taking the value out is easy to overdo, and a
// message with nothing to locate — no service, no field, no mount — would leave
// the reader worse off than the leak did. Two of these had nothing but the value
// to go on before, so the locator is new.
func TestAFieldThatChecksItsOwnValueDoesNotReadItBack(t *testing.T) {
	const secret = "ghp-canary-do-not-print"
	for _, tc := range []struct {
		name  string
		body  string
		keeps []string
	}{
		{
			name:  "a memory size",
			body:  "services:\n  app:\n    image: app\n    mem_limit: ${SECRET}\n",
			keeps: []string{`service "app"`, "mem_limit", "not a memory size"},
		},
		{
			// The same number can be set under two keys, so which one is complained
			// about is part of being able to act on it.
			name:  "a memory size under deploy",
			body:  "services:\n  app:\n    image: app\n    deploy:\n      resources:\n        limits:\n          memory: ${SECRET}\n",
			keeps: []string{`service "app"`, "deploy.resources.limits.memory", "not a memory size"},
		},
		{
			name:  "a memory unit",
			body:  "services:\n  app:\n    image: app\n    mem_limit: 5${SECRET}\n",
			keeps: []string{`service "app"`, "mem_limit", "not a memory unit"},
		},
		{
			name:  "a CPU count",
			body:  "services:\n  app:\n    image: app\n    cpus: ${SECRET}\n",
			keeps: []string{`service "app"`, "cpus", "not a number of CPUs"},
		},
		{
			name:  "a CPU count under deploy",
			body:  "services:\n  app:\n    image: app\n    deploy:\n      resources:\n        limits:\n          cpus: ${SECRET}\n",
			keeps: []string{`service "app"`, "deploy.resources.limits.cpus", "not a number of CPUs"},
		},
		{
			name:  "a restart policy",
			body:  "services:\n  app:\n    image: app\n    restart: ${SECRET}\n",
			keeps: []string{`service "app"`, "restart", "not a policy"},
		},
		{
			name:  "a retry count after on-failure",
			body:  "services:\n  app:\n    image: app\n    restart: on-failure:${SECRET}\n",
			keeps: []string{`service "app"`, "restart", "not a number"},
		},
		{
			name:  "a depends_on condition",
			body:  "services:\n  app:\n    image: app\n    depends_on:\n      db:\n        condition: ${SECRET}\n  db:\n    image: db\n",
			keeps: []string{`service "app"`, `"db"`, "service_healthy"},
		},
		{
			name: "a duration",
			body: "services:\n  a:\n    image: app\n  db:\n    image: db\n    healthcheck:\n      test: [\"CMD\", \"true\"]\n      interval: ${SECRET}\n",
			// The service too. Without it this message named a field in a file with
			// several services and no way to tell which — the name is worked out
			// after the decode has failed, by reading each service on its own.
			keeps: []string{`service "db"`, "healthcheck interval", "not a duration"},
		},
		{
			// There are only three types, so naming them is the whole of what the
			// reader needs to know about the value — but they still have to be told
			// which mount.
			name:  "a mount type",
			body:  "services:\n  app:\n    image: app\n    volumes:\n      - type: ${SECRET}\n        source: ./a\n        target: /somewhere\n",
			keeps: []string{"/somewhere", "bind, volume, tmpfs"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SECRET", secret)
			got := loadErr(t, tc.body)
			// Not just the whole secret: a message that shortened it — which is what
			// the parser's own complaints do, and what one of these was doing before
			// — would slip past a check for the full string. Any run of it counts,
			// from either end or the middle, so the runs are generated rather than
			// assumed to start at the front.
			for lo := 0; lo < len(secret); lo++ {
				for hi := len(secret); hi-lo >= 8; hi-- {
					if strings.Contains(got, secret[lo:hi]) {
						t.Errorf("the message reads back %q, %d characters of the value:\n%s",
							secret[lo:hi], hi-lo, got)
						return
					}
				}
			}
			// And structurally: these messages quote their worked examples, so a
			// value that slipped through in quotes would look like one of those.
			// What must not be there is the text of the value, checked above, and
			// the locator that must be there is checked below.
			for _, k := range tc.keeps {
				if !strings.Contains(got, k) {
					t.Errorf("the message should still carry %q — without it there is nothing to act on:\n%s", k, got)
				}
			}
		})
	}

	// The negative CPU count takes its own exit and has no value to quote, but it
	// used to quote one anyway.
	t.Run("a negative CPU count", func(t *testing.T) {
		got := loadErr(t, "services:\n  app:\n    image: app\n    cpus: -2\n")
		for _, k := range []string{`service "app"`, "cpus", "must not be negative"} {
			if !strings.Contains(got, k) {
				t.Errorf("the message should carry %q:\n%s", k, got)
			}
		}
	})
}

// The complaint about a key set twice names the key, and a name has to survive
// whole — the reader has to be able to find it. Stripping the quoted value out of
// a shape complaint must not reach into this one: a key with two backticks in it
// would come out with its middle removed, pointing at something that is not in the
// file.
//
// Tested on the helper rather than through a document. Getting a decode to report
// a duplicate and a bad shape at once turns out to depend on where in the file
// they are, and a test built on one such document tests the document as much as
// the rule. The helper is where the rule lives.
func TestTakingTheValueOutLeavesAKeyNameWhole(t *testing.T) {
	in := &yaml.TypeError{Errors: []string{
		"line 5: cannot unmarshal !!str `sk-live...` into compose.VolumeDecl",
		"line 6: mapping key \"a`b`c\" already defined at line 5",
		"line 7: cannot unmarshal !!seq into compose.VolumeDecl",
	}}
	got := withoutEchoedValues(in).Errors
	want := []string{
		"line 5: cannot unmarshal !!str into compose.VolumeDecl",
		"line 6: mapping key \"a`b`c\" already defined at line 5",
		"line 7: cannot unmarshal !!seq into compose.VolumeDecl",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d complaints, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("complaint %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}

// Naming the service a failure came from is a guess made after the fact — the
// decode has already failed and this re-reads each service on its own to see
// which one does the same thing. A guess is only worth making when it is certain.
//
// When two services fail the same way, there is no single answer, so it says
// nothing rather than picking one. Getting that wrong is worse than saying
// nothing: the reader goes to a service whose only fault is being first.
func TestTheServiceIsNamedOnlyWhenThereIsNoDoubt(t *testing.T) {
	const bad = "services:\n  a:\n    image: app\n    healthcheck:\n      test: [\"CMD\", \"true\"]\n      interval: bogus\n"
	t.Run("one service fails", func(t *testing.T) {
		got := loadErr(t, bad+"  b:\n    image: app\n")
		if !strings.Contains(got, `service "a"`) {
			t.Errorf("the one service that fails should be named:\n%s", got)
		}
	})
	t.Run("two services fail the same way", func(t *testing.T) {
		got := loadErr(t, bad+"  b:\n    image: app\n    healthcheck:\n      test: [\"CMD\", \"true\"]\n      interval: bogus\n")
		if !strings.Contains(got, "not a duration") {
			t.Fatalf("a different failure got there first, so this checked nothing:\n%s", got)
		}
		for _, name := range []string{`service "a"`, `service "b"`} {
			if strings.Contains(got, name) {
				t.Errorf("both services fail the same way, so naming %s is a guess:\n%s", name, got)
			}
		}
	})
}

// Working out which service failed reads the same document the decode read, not
// the bytes it was built from.
//
// Those bytes still carry the marks that stand where a reference expanded to
// nothing, so a service that is perfectly fine once they are taken out can look
// broken with them in. Two apparent failures instead of one, and the name is
// dropped — on files that merely mention an unset variable somewhere else, which
// is most of them.
func TestAnotherServiceMentioningAnUnsetVariableDoesNotCostTheName(t *testing.T) {
	unsetHostVars(t, "NOPE")
	const hc = "    healthcheck:\n      test: [\"CMD\", \"true\"]\n      interval: "
	// `a` is the one that fails. `b` is fine — `5s${NOPE}` is `5s` once the mark
	// is out — but with the mark still in it fails in exactly the same words.
	got := loadErr(t, "services:\n  a:\n    image: app\n"+hc+"bogus\n  b:\n    image: app\n"+hc+"5s${NOPE}\n")
	if !strings.Contains(got, "not a duration") {
		t.Fatalf("a different failure got there first, so this checked nothing:\n%s", got)
	}
	if !strings.Contains(got, `service "a"`) {
		t.Errorf("the service that actually fails should still be named:\n%s", got)
	}
	if strings.Contains(got, `service "b"`) {
		t.Errorf("the service that is fine was named:\n%s", got)
	}
}

// A command whose quoting does not close no longer reads the command back.
//
// The command is free text and can hold a token — `sh -c 'curl -H "Bearer
// ${TOKEN}"'` is an ordinary thing to write — so quoting the whole of it into an
// error puts the token in the terminal, the CI log, and the issue somebody pastes
// it into. That it took a mistake elsewhere in the line to get there is no
// comfort: the mistake is why the message exists.
//
// This one was left out of the sweep that took the values out of the fields with
// a fixed set of accepted values, on the grounds that here the text is the only
// way to see what is wrong. Measuring said otherwise: Docker Compose says
// `'services[app].command' invalid command line string` and quotes nothing, and
// what the reader needs is which field to look at, which is a thing we can say.
func TestACommandThatDoesNotCloseIsNotReadBack(t *testing.T) {
	const secret = "ghp-canary-do-not-print"
	for _, tc := range []struct {
		name  string
		body  string
		keeps []string
	}{
		{
			name:  "a single quote that never closes",
			body:  "services:\n  app:\n    image: app\n    command: sh -c '${SECRET}\n",
			keeps: []string{`service "app"`, "command", "single quote is never closed"},
		},
		{
			name:  "a double quote that never closes",
			body:  "services:\n  app:\n    image: app\n    command: sh -c \"${SECRET}\n",
			keeps: []string{`service "app"`, "command", "double quote is never closed"},
		},
		{
			// Reached through `entrypoint:`, which is read by the same code. It used
			// to say "command" whichever it was, sending anyone with a bad
			// entrypoint to the wrong line; it now names both, which is true.
			name:  "an entrypoint ending in a backslash",
			body:  "services:\n  app:\n    image: app\n    entrypoint: \"${SECRET} \\\\\"\n",
			keeps: []string{`service "app"`, "command or entrypoint", "ends in a backslash"},
		},
		{
			// The other exit from the same code: not a string at all. It named only
			// `command` too, six lines below the one that was fixed first — half a
			// fix is what happens when a message is corrected where it was noticed
			// rather than everywhere it is written.
			name:  "neither a string nor a list",
			body:  "services:\n  app:\n    image: app\n    entrypoint:\n      ${SECRET}: v\n",
			keeps: []string{`service "app"`, "command or entrypoint", "expected a string or a list", "a mapping"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SECRET", secret)
			got := loadErr(t, tc.body)
			// And no raw kind number: "yaml kind 4" is the decoder's own counting
			// and means nothing to the reader.
			if strings.Contains(got, "yaml kind") {
				t.Errorf("the message shows the decoder's internal numbering:\n%s", got)
			}
			for lo := 0; lo < len(secret); lo++ {
				for hi := len(secret); hi-lo >= 8; hi-- {
					if strings.Contains(got, secret[lo:hi]) {
						t.Errorf("the message reads back %q:\n%s", secret[lo:hi], got)
						return
					}
				}
			}
			for _, k := range tc.keeps {
				if !strings.Contains(got, k) {
					t.Errorf("the message should carry %q — without it there is nothing to act on:\n%s", k, got)
				}
			}
		})
	}
}

// No message tells the reader what the YAML decoder calls a thing internally.
//
// "yaml kind 4" is the decoder's own counting; nobody can act on it. Each of
// these fields reads its own value and says so when what it got is the wrong
// shape, and each of them said it with a number.
//
// Written as a sweep over every field that has one of these messages, because the
// first fix was applied where it was noticed — one of six, in a function whose own
// comment says that half a fix is what happens when a message is corrected in the
// place it was seen rather than everywhere it is written.
func TestNoMessageNamesTheDecodersOwnNumbering(t *testing.T) {
	// A mapping given to a field that takes a string or a list, and a scalar given
	// to one that takes a list or a mapping: between them these reach every field
	// that used to answer with a number.
	for _, tc := range []struct{ name, body, want string }{
		{"networks", "services:\n  app:\n    image: app\n    networks: 3\n", "a single value"},
		{"env_file", "services:\n  app:\n    image: app\n    env_file:\n      k: v\n", "a mapping"},
		{"healthcheck test", "services:\n  app:\n    image: app\n    healthcheck:\n      test:\n        k: v\n", "a mapping"},
		{"environment", "services:\n  app:\n    image: app\n    environment: 3\n", "a single value"},
		{"depends_on", "services:\n  app:\n    image: app\n    depends_on: 3\n", "a single value"},
		{"command", "services:\n  app:\n    image: app\n    command:\n      k: v\n", "a mapping"},
		// These three answered with the YAML tag rather than the number — `got
		// !!str`, which is the spec's own notation and no more use to a reader
		// than the number was. The tag and the kind say the same thing about the
		// shape (measured: !!int, !!str and !!null are all one kind), so nothing
		// is lost by saying it in words.
		{"ports", "services:\n  app:\n    image: app\n    ports: 3\n", "a single value"},
		{"service volumes", "services:\n  app:\n    image: app\n    volumes: 3\n", "a single value"},
		{"service secrets", "services:\n  app:\n    image: app\n    secrets: 3\n", "a single value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadErr(t, tc.body)
			if strings.Contains(got, "yaml kind") {
				t.Errorf("the message shows the decoder's internal numbering:\n%s", got)
			}
			// Nor the tag, which is the same thing wearing the spec's notation.
			if strings.Contains(got, "!!") {
				t.Errorf("the message shows a YAML tag:\n%s", got)
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("the message should say what it found in words (%q):\n%s", tc.want, got)
			}
		})
	}
}

// What each shape is called, stated directly.
//
// The messages that use this cannot reach every case: the fields that complain
// about a shape all accept a list, so no message can be produced that calls a
// list by the wrong name. Left to the messages alone, that half of the mapping is
// unguarded — and a name that is only wrong in the case nobody can produce is
// still wrong the day a field stops accepting lists.
func TestWhatEachShapeIsCalled(t *testing.T) {
	for _, tc := range []struct {
		kind yaml.Kind
		want string
	}{
		{yaml.ScalarNode, "a single value"},
		{yaml.SequenceNode, "a list"},
		{yaml.MappingNode, "a mapping"},
		{yaml.DocumentNode, "a document"},
		{0, "something else"},
	} {
		if got := kindName(tc.kind); got != tc.want {
			t.Errorf("kindName(%d) = %q, want %q", tc.kind, got, tc.want)
		}
	}
}
