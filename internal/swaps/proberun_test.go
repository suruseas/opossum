package swaps_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/swaps"
)

// The rows a sweep writes are the rows the probe reader parses — proved by
// writing one for real and reading it back, so the writer cannot change shape
// without this failing. A reader tested only against rows typed out here would
// be tested against its author's memory of the writer, which is the drift this
// file exists to close.
func TestTheRowReaderReadsWhatTheSweepWrites(t *testing.T) {
	t.Parallel()
	muts, _, _ := sweep(t, "func f(a, b string) error { return fmt.Errorf(\"%s and %s\", a, b) }\n")
	if len(muts) != 1 {
		t.Fatalf("%d mutations from the fixture, and this reads the one", len(muts))
	}
	// As a report would carry it: outcome appended, the way a reader pastes it.
	q, ok := swaps.ParseSurvivorRow(muts[0].Name + ": SURVIVED")
	if !ok {
		t.Fatalf("the row the sweep writes is one the reader cannot read: %q", muts[0].Name)
	}
	if q.File != muts[0].File {
		t.Errorf("file read back as %q, written as %q", q.File, muts[0].File)
	}
	if q.A != 0 || q.B != 1 {
		t.Errorf("pair read back as %d,%d — the row says arguments 1 and 2, counted from one",
			q.A, q.B)
	}
}

// Lines that carry no row say so, rather than being read as a row of zeroes.
func TestLinesWithoutARowAreRefusedNotInvented(t *testing.T) {
	t.Parallel()
	for _, line := range []string{
		"",
		"| name | outcome |",
		"20 mutations, 2 pairs not offered",
		"x.go:12 reads arguments 1 and 2 the other way round", // no column: not the writer's shape
	} {
		if _, ok := swaps.ParseSurvivorRow(line); ok {
			t.Errorf("read a survivor row out of %q", line)
		}
	}
}

// Probes answers every request: a mutation for a pair that can carry a probe,
// a skip with the reason for one that cannot, and nothing dropped in silence —
// the counts on the two sides add up to the requests.
func TestEveryProbeRequestIsAnsweredOneWayOrTheOther(t *testing.T) {
	t.Parallel()
	src := "package x\n\nimport \"fmt\"\n\nfunc f() string { return \"\" }\n\n" +
		"func g(a, b string) {\n" +
		"\t_ = fmt.Sprintf(\"%s %s\", a, b)\n" +
		"\t_ = fmt.Sprintf(\"%s %s\", a, f())\n" +
		"}\n"
	sites := findIn(t, src)
	if len(sites) != 2 {
		t.Fatalf("%d sites in the fixture, and this reads two", len(sites))
	}
	reqs := []swaps.ProbeRequest{
		{File: sites[0].File, Line: sites[0].Line, Col: sites[0].Col, A: 0, B: 1}, // probe-able
		{File: sites[1].File, Line: sites[1].Line, Col: sites[1].Col, A: 0, B: 1}, // impure
		{File: sites[0].File, Line: 999, Col: 1, A: 0, B: 1},                      // moved tree
	}
	muts, skips, err := swaps.Probes(sites, reqs)
	if err != nil {
		t.Fatal(err)
	}
	if len(muts)+len(skips) != len(reqs) {
		t.Fatalf("%d requests became %d mutations and %d skips — something was dropped in silence",
			len(reqs), len(muts), len(skips))
	}
	if len(muts) != 1 {
		t.Fatalf("%d mutations, and the fixture offers one probe-able pair", len(muts))
	}
	if !strings.Contains(muts[0].To, "|evaluated") || !strings.Contains(muts[0].To, "|differs") {
		t.Errorf("the probe mutation does not carry both markers:\n%s", muts[0].To)
	}
	if muts[0].Name == "" || !strings.Contains(muts[0].To, muts[0].Name) {
		t.Errorf("the mutation's name %q is the marker, and the injected code does not print it:\n%s",
			muts[0].Name, muts[0].To)
	}
	reasons := map[string]bool{}
	for _, s := range skips {
		reasons[s.Reason] = true
	}
	if !reasons[swaps.ProbeSkipImpure] || !reasons[swaps.ProbeSkipNoSite] {
		t.Errorf("the two refusals should carry their own reasons, got %v", reasons)
	}
}

// The three verdicts, read from transcripts that say exactly one thing each —
// and a marker that is a prefix of another marker stays its own row.
func TestAVerdictIsReadFromTheTranscript(t *testing.T) {
	t.Parallel()
	const m = "probe x.go:7:6 arguments 1 and 2"
	for _, tc := range []struct {
		name, transcript, want string
	}{
		{"never ran", "ok  \tx\t0.1s\n", swaps.VerdictUnreached},
		{"ran on equal values", m + swaps.Evaluated + "\n" + m + swaps.Evaluated + "\n", swaps.VerdictNeverDiffered},
		{"told them apart", m + swaps.Evaluated + "\n" + m + swaps.Differs + "\n", swaps.VerdictDiffers},
		{"another pair's rows are not this pair's",
			"probe x.go:7:6 arguments 1 and 3" + swaps.Evaluated + "\n", swaps.VerdictUnreached},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := swaps.ProbeVerdict(m, tc.transcript); got != tc.want {
				t.Errorf("verdict %q, want %q", got, tc.want)
			}
		})
	}
}

// findIn writes src as one file and returns its sites, in source order.
func findIn(t *testing.T, src string) []swaps.Site {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return mustFind(t, dir)
}

func mustFind(t *testing.T, dir string) []swaps.Site {
	t.Helper()
	sites, err := swaps.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	return sites
}

// The probe's whole claim, run for real: applied to a call the program
// reaches, it prints the marker when the two arguments format differently and
// stays silent when they do not — and the probed program still builds and
// runs, which is the part a textual rewrite has to keep proving.
//
// `go run` rather than a parse: a probe that does not compile turns a whole
// sweep into "could not be measured", which is the failure the star verbs
// taught — worse than no answer, because it looks like one.
func TestAProbeSaysWhenTheArgumentsDifferAndOnlyThen(t *testing.T) {
	t.Parallel()
	const src = `package main

import "fmt"

func main() {
	a, b := "x", "y"
	_ = fmt.Sprintf("%s %s", a, b)
	c := "z"
	_ = fmt.Sprintf("%s %s", c, c)
}
`
	for _, tc := range []struct {
		name   string
		line   int // the site to probe, named by line so a fixture edit is loud
		prints bool
	}{
		{"different values print the marker", 7, true},
		{"equal values stay silent", 9, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "main.go")
			if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
			var site *swaps.Site
			for _, s := range mustFind(t, dir) {
				if s.Line == tc.line {
					s := s
					site = &s
				}
			}
			if site == nil {
				t.Fatalf("no site on line %d — the fixture and this table have drifted", tc.line)
			}
			const marker = "OPOSSUM_PROBE_570e"
			from, to, reason := site.Probe(0, 1, marker)
			if reason != "" {
				t.Fatalf("no probe: %s", reason)
			}
			if strings.Count(src, from) != 1 {
				t.Fatalf("the call %q does not name one place in the fixture", from)
			}
			probed := strings.Replace(src, from, to, 1)
			if err := os.WriteFile(path, []byte(probed), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("go", "run", "main.go")
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("the probed program did not run — a probe that breaks the build turns "+
					"a sweep into \"could not be measured\":\n%s\n\nprobed source:\n%s", out, probed)
			}
			if got := strings.Count(string(out), marker+swaps.Evaluated); got != 1 {
				t.Errorf("the call ran once and the probe said so %d times — without this line, "+
					"silence about a difference could mean the call never ran\nprogram output:\n%s", got, out)
			}
			if got := strings.Count(string(out), marker+swaps.Differs); got != map[bool]int{true: 1, false: 0}[tc.prints] {
				t.Errorf("difference reported %d times\nprogram output:\n%s", got, out)
			}
		})
	}
}

// What a probe refuses, and that it says why in the words the package names.
// The wrapper reads its second argument twice and ahead of its turn, so
// anything that could answer differently on the second read — or be changed by
// an argument evaluated in between — makes the probed run a different run, and
// the difference the probe reports could then be its own.
func TestAProbeRefusesWhatItsOwnReadingCouldChange(t *testing.T) {
	t.Parallel()
	const header = "package x\n\nimport \"fmt\"\n\nfunc f() string { return \"\" }\n\n"
	for _, tc := range []struct {
		name string
		body string
		a, b int
		want string
	}{
		{"the doubled argument is a call",
			"func g(a string) { _ = fmt.Sprintf(\"%s %s\", a, f()) }\n", 0, 1,
			swaps.ProbeSkipImpure},
		{"a call sits between the two",
			"func g(a, b string) { _ = fmt.Sprintf(\"%s %s %s\", a, f(), b) }\n", 0, 2,
			swaps.ProbeSkipImpure},
		{"selectors and indexes are simple enough",
			"type s struct{ A, B string }\nfunc g(v s, m map[int]string) { _ = fmt.Sprintf(\"%s %s\", v.A, m[0]) }\n", 0, 1,
			""},
		{"a pair the site does not have",
			"func g(a, b string) { _ = fmt.Sprintf(\"%s %s\", a, b) }\n", 0, 5,
			swaps.ProbeSkipNoPair},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sites := findIn(t, header+tc.body)
			if len(sites) != 1 {
				t.Fatalf("%d sites in the fixture, and this reads the one", len(sites))
			}
			_, _, reason := sites[0].Probe(tc.a, tc.b, "m")
			if reason != tc.want {
				t.Errorf("reason %q, want %q", reason, tc.want)
			}
		})
	}
}

// A file whose sites come from its own formatting wrapper needs no fmt import,
// and a probe written into it would not build. Refused, with the reason that
// names the shape — today every file with a site imports fmt (measured, 28 of
// 28 when this was written), so this is the guard for the first one that
// does not.
func TestAProbeRefusesAFileWithoutFmt(t *testing.T) {
	t.Parallel()
	sites := findIn(t, "package x\n\nfunc logf(format string, args ...any) {}\n\n"+
		"func g(a, b string) { logf(\"%s %s\", a, b) }\n")
	if len(sites) != 1 {
		t.Fatalf("%d sites in the fixture, and this reads the one", len(sites))
	}
	_, _, reason := sites[0].Probe(0, 1, "m")
	if reason != swaps.ProbeSkipNoFmt {
		t.Errorf("reason %q, want %q", reason, swaps.ProbeSkipNoFmt)
	}
}

// The reasons a probe gives are the named ones and nothing else — the same
// bind the sweep's skips carry. A reason added to the code without being added
// here is a red test, so prose that says "the reasons are these" cannot
// quietly fall behind the set.
func TestTheProbeReasonsAreTheOnesThisPackageCanGive(t *testing.T) {
	t.Parallel()
	named := map[string]bool{
		"":                    true,
		swaps.ProbeSkipNoFmt:  true,
		swaps.ProbeSkipImpure: true,
		swaps.ProbeSkipNoPair: true,
	}
	if len(named) != 4 {
		t.Fatalf("%d distinct reasons named, and there are four — two sharing a string are "+
			"two the reader cannot tell apart", len(named))
	}
	fixtures := []string{
		"package x\n\nimport \"fmt\"\n\nfunc g(a, b string) { _ = fmt.Sprintf(\"%s %s\", a, b) }\n",
		"package x\n\nimport \"fmt\"\n\nfunc f() string { return \"\" }\nfunc g(a string) { _ = fmt.Sprintf(\"%s %s\", a, f()) }\n",
		"package x\n\nfunc logf(format string, args ...any) {}\nfunc g(a, b string) { logf(\"%s %s\", a, b) }\n",
	}
	for _, src := range fixtures {
		for _, s := range findIn(t, src) {
			for _, p := range [][2]int{{0, 1}, {1, 0}, {0, 9}} {
				if _, _, reason := s.Probe(p[0], p[1], "m"); !named[reason] {
					t.Errorf("%s:%d: probe gave a reason nothing names: %q", s.File, s.Line, reason)
				}
			}
		}
	}
}

// A call written twice is that pair's problem, not the batch's: the machinery
// that applies a mutation refuses a text naming two places, and refuses the
// whole batch on the first one. So the pair is dropped here, with the sweep's
// own reason, and every other row still gets its run.
func TestADuplicatedCallIsAPairsProblemNotTheBatches(t *testing.T) {
	t.Parallel()
	src := "package x\n\nimport \"fmt\"\n\nfunc g(a, b string) {\n" +
		"\t_ = fmt.Sprintf(\"%s %s\", a, b)\n" +
		"\t_ = fmt.Sprintf(\"%s %s\", a, b)\n" +
		"}\n"
	sites := findIn(t, src)
	if len(sites) != 2 {
		t.Fatalf("%d sites in the fixture, and the same call is written twice", len(sites))
	}
	var reqs []swaps.ProbeRequest
	for _, s := range sites {
		reqs = append(reqs, swaps.ProbeRequest{File: s.File, Line: s.Line, Col: s.Col, A: 0, B: 1})
	}
	muts, skips, err := swaps.Probes(sites, reqs)
	if err != nil {
		t.Fatal(err)
	}
	if len(muts) != 0 {
		t.Errorf("%d mutations from a call that names two places — applying one would refuse "+
			"the whole batch", len(muts))
	}
	if len(skips) != 2 {
		t.Fatalf("%d skips, want both pairs back with a reason", len(skips))
	}
	for _, s := range skips {
		if s.Reason != swaps.SkipNotUnique {
			t.Errorf("reason %q, want %q", s.Reason, swaps.SkipNotUnique)
		}
	}
}
