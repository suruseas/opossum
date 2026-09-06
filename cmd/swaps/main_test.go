package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suruseas/opossum/internal/swaps"
)

func writeGo(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\n\nimport \"fmt\"\n\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The totals are what gets quoted, so they are printed by the thing that counted
// them. Added up afterwards by a person is how the numbers went wrong.
func TestTheTotalsArePrintedByWhatCountedThem(t *testing.T) {
	dir := writeGo(t, "func f(a, b, c string) {\n\tfmt.Printf(\"%s %s %s\", a, b, c)\n\tfmt.Printf(\"%s %s\", a, b)\n}\n")
	var out, errOut strings.Builder
	if code := run([]string{dir}, strings.NewReader(""), &out, &errOut, nil, nil); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "2 sites, 4 pairs") {
		t.Errorf("three of a kind and two of a kind make four exchanges over two sites:\n%s", out.String())
	}
	// Each row is file:line, the pair count, then the head of the format — and
	// the line and the count are both ints, the file and the head both strings.
	// Exchanged, a row would still be a row (#559).
	if !regexp.MustCompile(`(?m)^\S+\.go:6  pairs=3  "%s %s %s"$`).MatchString(out.String()) {
		t.Errorf("the first site's row should read <file>.go:6  pairs=3  \"%%s %%s %%s\":\n%s", out.String())
	}
}

func TestTSVSaysOnlyWhatASweepReads(t *testing.T) {
	dir := writeGo(t, "func f(a, b string) { fmt.Printf(\"%s %s\", a, b) }\n")
	var out, errOut strings.Builder
	if code := run([]string{"-tsv", dir}, strings.NewReader(""), &out, &errOut, nil, nil); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	line := strings.TrimSpace(out.String())
	if got := strings.Count(line, "\t"); got != 2 {
		t.Errorf("file, line and pairs is two tabs, got %d: %q", got, line)
	}
	// The line number and the pair count are both ints: exchanged, the row
	// would still be three tab-separated fields (#559).
	if fields := strings.Split(line, "\t"); len(fields) == 3 && (fields[1] != "5" || fields[2] != "1") {
		t.Errorf("file, then line 5, then 1 pair; got %q", line)
	}
	if strings.Contains(out.String(), "sites,") {
		t.Errorf("the totals are prose, and a sweep reads rows:\n%s", out.String())
	}
}

// A directory that could not be read is not a directory with nothing in it.
func TestAFileThatDoesNotParseIsAFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\nfunc ( {"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	if code := run([]string{dir}, strings.NewReader(""), &out, &errOut, nil, nil); code == 0 {
		t.Errorf("a file that does not parse should not report zero pairs:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "swaps: ") {
		t.Errorf("the failure should say who is speaking:\n%s", errOut.String())
	}
}

func TestUsageIsAFailure(t *testing.T) {
	var out, errOut strings.Builder
	if code := run(nil, strings.NewReader(""), &out, &errOut, nil, nil); code == 0 {
		t.Error("no directory is not a run that found nothing")
	}
	if !strings.Contains(errOut.String(), "usage:") {
		t.Errorf("say how to call it:\n%s", errOut.String())
	}
}

// The sweep goes to stdout on its own, so that `swaps -sweep dir > s.json`
// leaves a file `mutate` can read and nothing else.
func TestTheSweepIsTheOnlyThingOnStdout(t *testing.T) {
	dir := writeGo(t, "func f(a, b string) { fmt.Printf(\"%s %s\", a, b) }\n")
	var out, errOut strings.Builder
	if code := run([]string{"-sweep", dir}, strings.NewReader(""), &out, &errOut, nil, nil); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var muts []struct {
		Name, File, From, To string
		Packages             []string
	}
	if err := json.Unmarshal([]byte(out.String()), &muts); err != nil {
		t.Fatalf("stdout is a sweep file: %v\n%s", err, out.String())
	}
	if len(muts) != 1 || muts[0].From == muts[0].To {
		t.Errorf("one pair, one mutation, and it changes something: %+v", muts)
	}
	if !strings.Contains(errOut.String(), "1 mutations, 0 pairs not offered") {
		t.Errorf("the counts belong on stderr, beside the sweep rather than in it:\n%s", errOut.String())
	}
}

// What was not offered is said, and said where redirecting the sweep does not
// hide it. A tool that answers "20 mutations" without saying it dropped three
// is the reading four earlier counts got wrong.
func TestWhatWasNotOfferedIsSaidOnStderr(t *testing.T) {
	dir := writeGo(t, "type T struct{ N string }\n\nfunc f(t T) { fmt.Printf(\"%s needs %s\", t.N, t.N) }\n")
	var out, errOut strings.Builder
	if code := run([]string{"-sweep", dir}, strings.NewReader(""), &out, &errOut, nil, nil); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	// Word for word. Asking only whether the words appear leaves the sentence
	// unread: the line names a file, a line, two argument numbers and a reason,
	// and a version with any two of those the other way round satisfies a
	// check for the pieces. That is the shape of mistake this whole tool exists
	// to find, so the line reporting it is not the place to make it.
	want := fmt.Sprintf("not offered: %s:7 arguments 1 and 2 — %s\n",
		filepath.Join(dir, "x.go"), swaps.SkipNoChange)
	if !strings.Contains(errOut.String(), want) {
		t.Errorf("stderr:\n%s\nwant a line reading:\n%s", errOut.String(), want)
	}
	if strings.Contains(out.String(), "not offered") {
		t.Errorf("and not in the sweep file:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "0 mutations, 1 pairs not offered") {
		t.Errorf("the totals count it too:\n%s", errOut.String())
	}
}

// Two flags that write different things to the same place is a question, not a
// default. Answering it silently would put a sweep where a script wanted rows.
func TestTSVAndSweepTogetherIsAFailure(t *testing.T) {
	dir := writeGo(t, "func f(a, b string) { fmt.Printf(\"%s %s\", a, b) }\n")
	var out, errOut strings.Builder
	if code := run([]string{"-tsv", "-sweep", dir}, strings.NewReader(""), &out, &errOut, nil, nil); code != 2 {
		t.Errorf("exit %d, want 2: %s", code, errOut.String())
	}
	if out.String() != "" {
		t.Errorf("and nothing written:\n%s", out.String())
	}
}

// writeModule lays out a small real module — go.mod, one source file, one test
// — and moves the test process into it, because -probe runs the real
// toolchain and the toolchain wants a module around it.
func writeModule(t *testing.T, src, test string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":    "module probefix\n\ngo 1.21\n",
		"x.go":      src,
		"x_test.go": test,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
}

// survivorRows writes the rows a sweep report would carry for every pair the
// tree offers, in the shape the sweep writes them — built from Find so the
// fixture's positions cannot drift out of the rows.
func survivorRows(t *testing.T) string {
	t.Helper()
	sites, err := swaps.Find(".")
	if err != nil {
		t.Fatal(err)
	}
	var rows strings.Builder
	for _, s := range sites {
		for _, p := range s.Exchanges {
			fmt.Fprintf(&rows, "%s:%d:%d reads arguments %d and %d the other way round: SURVIVED\n",
				s.File, s.Line, s.Col, p[0]+1, p[1]+1)
		}
	}
	return rows.String()
}

// The whole point of a probe, measured on a real run: the same silence — no
// test fails either way — comes back as three different verdicts, one per
// cause, and the tree is byte-identical afterwards.
func TestAProbeRunTellsTheThreeSilencesApart(t *testing.T) {
	const src = `package x

import "fmt"

func Msg(entry, svc string) string {
	return fmt.Sprintf("tool %q refers to %q", entry, svc)
}

func Unvisited(a, b string) string {
	return fmt.Sprintf("%s then %s", a, b)
}
`
	// Msg is reached on values a test could tell apart but does not; Unvisited
	// is never called. Both swaps survive a sweep; the probe splits them.
	writeModule(t, src, `package x

import "testing"

func TestMsg(t *testing.T) {
	if Msg("web", "db") == "" {
		t.Fatal("empty")
	}
}
`)
	before := readTree(t)
	var out, errOut strings.Builder
	if code := run([]string{"-probe", "."}, strings.NewReader(survivorRows(t)), &out, &errOut, nil, nil); code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	if got := out.String(); !strings.Contains(got, "x.go:6:9 arguments 1 and 2: "+swaps.VerdictDiffers) {
		t.Errorf("the reached pair with different values should say write-the-test:\n%s", got)
	}
	if got := out.String(); !strings.Contains(got, "x.go:10:9 arguments 1 and 2: "+swaps.VerdictUnreached) {
		t.Errorf("the never-called pair should say so:\n%s", got)
	}
	for name, b := range before {
		after, err := os.ReadFile(name)
		if err != nil || string(after) != b {
			t.Errorf("%s is not the file it was before the probes ran", name)
		}
	}
}

// The fixture that cannot tell the arguments apart is named as the thing to
// fix — the verdict this mode exists to separate from "write a test".
func TestAProbeRunNamesTheFixtureWhenValuesNeverDiffer(t *testing.T) {
	writeModule(t, `package x

import "fmt"

func Msg(entry, svc string) string {
	return fmt.Sprintf("tool %q refers to %q", entry, svc)
}
`, `package x

import "testing"

func TestMsg(t *testing.T) {
	if Msg("nope", "nope") == "" {
		t.Fatal("empty")
	}
}
`)
	var out, errOut strings.Builder
	if code := run([]string{"-probe", "."}, strings.NewReader(survivorRows(t)), &out, &errOut, nil, nil); code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	if got := out.String(); !strings.Contains(got, swaps.VerdictNeverDiffered) {
		t.Errorf("equal values every time should point at the fixture:\n%s", got)
	}
}

func readTree(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, name := range []string{"x.go", "x_test.go"} {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = string(b)
	}
	return out
}

// A probe is built not to change what the tests see, so a run where a test
// fails under it cannot be read as any of the three verdicts — and saying
// "unreached" there would send someone to fix reach that is fine. The row says
// "no verdict" instead, and the exit status refuses to call the run a success:
// nothing measured must not read as nothing found.
func TestAProbeRunThatIsNotCleanSaysNoVerdictAndFails(t *testing.T) {
	writeModule(t, `package x

import "fmt"

func Msg(entry, svc string) string {
	return fmt.Sprintf("tool %q refers to %q", entry, svc)
}
`, `package x

import (
	"os"
	"strings"
	"testing"
)

func TestSourceCarriesNoWrapper(t *testing.T) {
	b, err := os.ReadFile("x.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "func(x, y any)") {
		t.Fatal("the source has been rewritten")
	}
	if Msg("web", "db") == "" {
		t.Fatal("empty")
	}
}
`)
	var out, errOut strings.Builder
	code := run([]string{"-probe", "."}, strings.NewReader(survivorRows(t)), &out, &errOut, nil, nil)
	if code != 1 {
		t.Errorf("exit %d, want 1 — a run that could not be read is not a success\nstderr:\n%s",
			code, errOut.String())
	}
	if got := out.String(); !strings.Contains(got, "no verdict") {
		t.Errorf("the unclean run should say so:\n%s", got)
	}
	// The mutation's name opens the line and the outcome sits in the
	// parentheses; both are strings, and exchanged the line would still parse
	// as prose (#559).
	if !regexp.MustCompile(`(?m)^probe x\.go:\d+:\d+ arguments 1 and 2: no verdict — the probe run was not clean \(caught by `).MatchString(out.String()) {
		t.Errorf("the no-verdict line should open with the mutation's name:\n%s", out.String())
	}
	for _, v := range []string{swaps.VerdictDiffers, swaps.VerdictNeverDiffered, swaps.VerdictUnreached} {
		if strings.Contains(out.String(), v) {
			t.Errorf("an unclean run must not carry a verdict, found %q:\n%s", v, out.String())
		}
	}
}

// An interrupt puts the probed file back and exits as interrupted — the same
// promise cmd/mutate makes, tested the same way, because the block making it
// here is a copy and a copy is exactly what nothing else guards.
func TestAnInterruptRestoresTheProbedFile(t *testing.T) {
	const src = `package x

import "fmt"

func Msg(entry, svc string) string {
	return fmt.Sprintf("tool %q refers to %q", entry, svc)
}
`
	writeModule(t, src, `package x

import (
	"testing"
	"time"
)

func TestMsg(t *testing.T) {
	time.Sleep(3 * time.Second)
	if Msg("web", "db") == "" {
		t.Fatal("empty")
	}
}
`)
	rows := survivorRows(t)
	sigs := make(chan os.Signal, 1)
	exited := make(chan int, 4)
	var out bytes.Buffer
	errOut := &lockedBuf{}

	// Fire once the probe is actually on disk, not after a delay — a timer
	// lands wherever the run happens to be, and the baseline runs first.
	landed := make(chan bool, 1)
	go func() {
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
			b, _ := os.ReadFile("x.go")
			if strings.Contains(string(b), "func(x, y any)") {
				landed <- true
				sigs <- os.Interrupt
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		landed <- false
		sigs <- os.Interrupt // so the run does not sit here waiting for one
	}()

	run([]string{"-probe", "."}, strings.NewReader(rows), &out, errOut, sigs,
		func(c int) { exited <- c })

	if !<-landed {
		t.Fatal("the interrupt never landed on a probed tree, so this measured nothing")
	}
	select {
	case code := <-exited:
		if code != 130 {
			t.Errorf("exit = %d, want 130", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the signal was never heard: nothing tried to exit")
	}
	if s := errOut.String(); !strings.Contains(s, "put back") {
		t.Errorf("stderr = %q, want it to say the file was restored", s)
	}
	b, err := os.ReadFile("x.go")
	if err != nil || string(b) != src {
		t.Errorf("the file was left probed:\n%s", b)
	}
}

// lockedBuf is a bytes.Buffer two goroutines may write to — the handler
// writes its message while the sweep is still running.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// A pair a probe cannot carry is said with its place: file, line, the two
// argument numbers and the reason, in that order. Five values on one format
// call, and no test reached the line (#559's swap sweep reported it unreached).
// A row naming a line no site sits on is the one skip nothing else can turn
// into a probe.
func TestAPairThatCannotCarryAProbeIsSaidWithItsPlace(t *testing.T) {
	writeModule(t, `package x

import "fmt"

func Msg(entry, svc string) string {
	return fmt.Sprintf("tool %q refers to %q", entry, svc)
}
`, `package x

import "testing"

func TestMsg(t *testing.T) {
	if Msg("web", "db") == "" {
		t.Fatal("empty")
	}
}
`)
	var out, errOut strings.Builder
	rows := "x.go:99:9 reads arguments 1 and 2 the other way round: SURVIVED\n"
	code := run([]string{"-probe", "."}, strings.NewReader(rows), &out, &errOut, nil, nil)
	if code != 1 {
		t.Errorf("exit = %d, want 1 — nothing could be probed", code)
	}
	if want := "not probed: x.go:99 arguments 1 and 2 — " + swaps.ProbeSkipNoSite; !strings.Contains(errOut.String(), want) {
		t.Errorf("the skip should be said as %q, got:\n%s", want, errOut.String())
	}
}
