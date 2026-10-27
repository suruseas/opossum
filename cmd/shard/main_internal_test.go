package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestAPartIsReadOnlyAsKOfN(t *testing.T) {
	for _, c := range []struct {
		in   string
		want part
		ok   bool
	}{
		{"1/4", part{1, 4}, true},
		{"4/4", part{4, 4}, true},
		{"1/1", part{1, 1}, true},
		{"0/4", part{}, false},
		{"5/4", part{}, false},
		{"1/0", part{}, false},
		{"+1/4", part{}, false},
		{"1/ 4", part{}, false},
		{"1-4", part{}, false},
		{"1/4/2", part{}, false},
		{"", part{}, false},
	} {
		got, err := parsePart(c.in)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("parsePart(%q) = %v, %v; want %v, ok=%v", c.in, got, err, c.want, c.ok)
		}
	}
}

func TestANameFallsInOnePartOfN(t *testing.T) {
	names := []string{"TestA", "TestB", "TestUp", "TestDown", "TestAReal", "Test_x", "TestÜ"}
	for _, n := range []int{1, 2, 4, 7} {
		used := map[int]bool{}
		for _, name := range names {
			k := partOf(name, n)
			if k < 1 || k > n {
				t.Errorf("partOf(%q, %d) = %d, outside 1..%d", name, n, k, n)
			}
			if partOf(name, n) != k {
				t.Errorf("partOf(%q, %d) answered twice differently", name, n)
			}
			used[k] = true
		}
		if n == 4 && len(used) < 2 {
			t.Errorf("seven names all fell in one of four parts: %v", used)
		}
	}
}

func TestTheCommandsFlagsAndPackagesAreToldApart(t *testing.T) {
	flags, pkgs, err := splitArgs([]string{"./a", "-race", "-cover", "./b", "-count=1"})
	if err != nil || !reflect.DeepEqual(flags, []string{"-race", "-cover", "-count=1"}) || !reflect.DeepEqual(pkgs, []string{"./a", "./b"}) {
		t.Errorf("got flags %v, packages %v, %v", flags, pkgs, err)
	}
	if _, _, err := splitArgs([]string{"-race"}); err == nil {
		t.Error("a command with no packages was taken; a part is taken from the packages it names")
	}
	for _, own := range []string{"-run=X", "-run", "--run=X", "-test.run=X", "-skip=X", "-list=.", "-json", "-v", "-bench=."} {
		t.Run(own, func(t *testing.T) {
			if _, _, err := splitArgs([]string{"./a", own}); err == nil {
				t.Errorf("%s was taken; the part sets it itself, and one of the two would win", own)
			}
		})
	}
}

// fakeGo answers each call with the next scripted output and status, and
// keeps the arguments it was called with.
type fakeGo struct {
	calls   [][]string
	answers []answer
}

type answer struct {
	events []event
	raw    string
	code   int
	// broken, when set, is what reading the output ends with instead of EOF.
	broken error
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func (f *fakeGo) start(args []string) (io.Reader, func() int, error) {
	f.calls = append(f.calls, args)
	a := f.answers[0]
	f.answers = f.answers[1:]
	var b bytes.Buffer
	b.WriteString(a.raw)
	for _, e := range a.events {
		j, _ := json.Marshal(e)
		b.Write(append(j, '\n'))
	}
	if a.broken != nil {
		return io.MultiReader(&b, failingReader{a.broken}), func() int { return a.code }, nil
	}
	return &b, func() int { return a.code }, nil
}

func out(pkg, test, text string) event {
	return event{Action: "output", Package: pkg, Test: test, Output: text}
}

func listed(pkg string, names ...string) []event {
	var es []event
	for _, n := range names {
		es = append(es, out(pkg, "", n+"\n"))
	}
	return append(es, out(pkg, "", "ok  \t"+pkg+"\t0.01s\n"), event{Action: "pass", Package: pkg})
}

func TestTheListIsTheNamesGoPrints(t *testing.T) {
	t.Run("names, per package, and nothing else", func(t *testing.T) {
		f := &fakeGo{answers: []answer{{events: append(listed("p/a", "TestX", "FuzzY", "ExampleZ"), listed("p/b", "TestX")...)}}}
		all, err := listTests(f, []string{"-race"}, []string{"./..."}, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string][]string{"p/a": {"TestX", "FuzzY", "ExampleZ"}, "p/b": {"TestX"}}
		if !reflect.DeepEqual(all, want) {
			t.Errorf("got %v, want %v", all, want)
		}
		wantArgs := []string{"test", "-list", "^(Test|Fuzz|Example)", "-json", "-race", "./..."}
		if !reflect.DeepEqual(f.calls[0], wantArgs) {
			t.Errorf("asked go %v, want %v — the list is of the binaries the command's own flags build", f.calls[0], wantArgs)
		}
	})
	t.Run("a line longer than any buffer does not stop the list", func(t *testing.T) {
		f := &fakeGo{answers: []answer{{raw: strings.Repeat("x", 2<<20) + "\n", events: listed("p/a", "TestX")}}}
		all, err := listTests(f, nil, []string{"./..."}, io.Discard)
		if err != nil || !reflect.DeepEqual(all, map[string][]string{"p/a": {"TestX"}}) {
			t.Errorf("got %v, %v", all, err)
		}
	})
	t.Run("a list that cannot be read to the end is refused", func(t *testing.T) {
		f := &fakeGo{answers: []answer{{events: listed("p/a", "TestX"), broken: errors.New("pipe broke")}}}
		if _, err := listTests(f, nil, []string{"./..."}, io.Discard); err == nil {
			t.Error("a list cut off by a read error was taken")
		}
	})
	t.Run("a list that exits non-zero is refused, and what it said is shown", func(t *testing.T) {
		f := &fakeGo{answers: []answer{{events: []event{{Action: "build-output", ImportPath: "p/a", Output: "a.go:1: oops\n"}}, code: 1}}}
		var errw bytes.Buffer
		if _, err := listTests(f, nil, []string{"./..."}, &errw); err == nil {
			t.Error("a list that exited 1 was taken")
		}
		if !strings.Contains(errw.String(), "a.go:1: oops") {
			t.Errorf("the list's own words were not shown: %q", errw.String())
		}
	})
	t.Run("a package that failed to list is refused even when go exits 0", func(t *testing.T) {
		f := &fakeGo{answers: []answer{{events: append(listed("p/a", "TestX"), event{Action: "fail", Package: "p/b"})}}}
		if _, err := listTests(f, nil, []string{"./..."}, io.Discard); err == nil {
			t.Error("a list with a failed package was taken")
		}
	})
}

// verbose is what `go test -v` prints for a package: each test's verdict at
// the start of a line, and the package's end.
func verbose(pkg string, verdicts ...string) string {
	var b strings.Builder
	for _, v := range verdicts {
		action, name, _ := strings.Cut(v, " ")
		b.WriteString("=== RUN   " + name + "\n--- " + action + ": " + name + " (0.00s)\n")
	}
	if strings.Contains(b.String(), "--- FAIL") {
		b.WriteString("FAIL\nFAIL\t" + pkg + "\t0.1s\n")
	} else {
		b.WriteString("PASS\nok  \t" + pkg + "\t0.1s\tcoverage: 50.0% of statements\n")
	}
	return b.String()
}

func TestWhatIsPrintedIsWhatAPlainRunPrints(t *testing.T) {
	for _, c := range []struct {
		line  string
		shown bool
	}{
		{"=== RUN   TestA", false},
		{"=== RUN   TestA/sub", false},
		{"=== PAUSE TestA", false},
		{"=== CONT  TestA", false},
		{"=== NAME  TestA", false},
		{"--- PASS: TestA (0.01s)", false},
		{"    --- PASS: TestA/sub (0.00s)", false},
		{"--- SKIP: TestA (0.00s)", false},
		{"PASS", false},
		{"coverage: 50.0% of statements", false},
		{"coverage: 50.0% of statements in ./...", true},
		{"--- FAIL: TestA (0.01s)", true},
		{"    --- FAIL: TestA/sub (0.00s)", true},
		{"    a_test.go:12: want 1", true},
		{"FAIL", true},
		{"ok  \tp\t0.1s\tcoverage: 50.0% of statements", true},
		{"FAIL\tp\t0.1s", true},
		{"panic: boom", true},
		{"PASS: a word in a sentence", true},
	} {
		if shown := !verboseOnly.MatchString(c.line); shown != c.shown {
			t.Errorf("%q shown = %v, want %v", c.line, shown, c.shown)
		}
	}
}

func readManifest(t *testing.T, path string) record {
	t.Helper()
	r, err := readRecord(path)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// namesIn finds, for a part of n, one name that falls in it and one that
// does not, so the fixtures do not depend on where a hash happens to land.
func namesIn(t *testing.T, k, n int) (in, out string) {
	t.Helper()
	for i := 0; in == "" || out == ""; i++ {
		name := "Test" + strings.Repeat("A", i%26) + string(rune('B'+i%20))
		if partOf(name, n) == k {
			if in == "" {
				in = name
			}
		} else if out == "" {
			out = name
		}
		if i > 10000 {
			t.Fatal("no names found")
		}
	}
	return in, out
}

func TestAPartRunsItsOwnTestsAndSaysSo(t *testing.T) {
	mine, other := namesIn(t, 2, 3)
	p := part{2, 3}

	t.Run("only its own names, quoted and anchored, in only the packages that have one", func(t *testing.T) {
		manifest := filepath.Join(t.TempDir(), "rec")
		f := &fakeGo{answers: []answer{
			{events: append(append(listed("p/a", mine, other), listed("p/b", other)...), listed("p/c", mine)...)},
			{raw: verbose("p/a", "PASS "+mine) + verbose("p/c", "SKIP "+mine)},
		}}
		var w bytes.Buffer
		if code := runPart(p, []string{"./...", "-race", "-count=1"}, manifest, f, &w, io.Discard); code != 0 {
			t.Fatalf("exit %d: %s", code, w.String())
		}
		want := []string{"test", "-v", "-run", "^(" + mine + ")$", "-race", "-count=1", "p/a", "p/c"}
		if !reflect.DeepEqual(f.calls[1], want) {
			t.Errorf("ran go %v, want %v", f.calls[1], want)
		}
		r := readManifest(t, manifest)
		if r.part != p || len(r.all) != 4 || len(r.mine) != 2 || r.ran[test{"p/a", mine}] != "pass" || r.ran[test{"p/c", mine}] != "skip" {
			t.Errorf("the record says %+v", r)
		}
	})
	t.Run("a name that is a pattern is run as itself", func(t *testing.T) {
		f := &fakeGo{answers: []answer{{events: listed("p/a", "Test")}, {raw: verbose("p/a", "PASS Test")}}}
		runPart(part{1, 1}, []string{"./..."}, filepath.Join(t.TempDir(), "rec"), f, io.Discard, io.Discard)
		if got := f.calls[1][3]; got != "^(Test)$" {
			t.Errorf("-run %s; unanchored, `Test` is every test there is", got)
		}
	})
	t.Run("the lines of a test that ended its binary are printed", func(t *testing.T) {
		f := &fakeGo{answers: []answer{{events: listed("p/a", mine)}, {raw: "=== RUN   " + mine + "\ncut off\nFAIL\tp/a\t0.1s\n", code: 1}}}
		var w bytes.Buffer
		runPart(p, []string{"./..."}, filepath.Join(t.TempDir(), "rec"), f, &w, io.Discard)
		if !strings.Contains(w.String(), "cut off\n") {
			t.Errorf("printed %q; what a run that ended under a test said is the reason it ended", w.String())
		}
	})
	t.Run("only a test's own verdict is a test that ran, in the package that printed it", func(t *testing.T) {
		manifest := filepath.Join(t.TempDir(), "rec")
		f := &fakeGo{answers: []answer{{events: listed("p/a", mine)}, {raw: "    --- PASS: " + mine + "/sub (0.00s)\n" + verbose("p/a", "PASS "+mine)}}}
		runPart(p, []string{"./..."}, manifest, f, io.Discard, io.Discard)
		if r := readManifest(t, manifest); len(r.ran) != 1 {
			t.Errorf("the record says %v ran; a subtest is part of its test, not one the list named", r.ran)
		}
	})
	t.Run("go's status is passed on, and the failure recorded", func(t *testing.T) {
		manifest := filepath.Join(t.TempDir(), "rec")
		f := &fakeGo{answers: []answer{{events: listed("p/a", mine)}, {raw: verbose("p/a", "FAIL "+mine), code: 3}}}
		if code := runPart(p, []string{"./..."}, manifest, f, io.Discard, io.Discard); code != 3 {
			t.Errorf("exit %d, want go's 3", code)
		}
		if r := readManifest(t, manifest); r.ran[test{"p/a", mine}] != "fail" {
			t.Errorf("the record says %v; a test that failed ran, and failed", r.ran)
		}
	})
	t.Run("a verdict on the end of a test's unfinished line is read", func(t *testing.T) {
		manifest := filepath.Join(t.TempDir(), "rec")
		f := &fakeGo{answers: []answer{{events: listed("p/a", mine)}, {raw: "=== RUN   " + mine + "\nAddress already in use--- PASS: " + mine + " (0.29s)\nsaid--- FAIL: " + mine + "/sub (0.00s)\nlast wordsok  \tp/a\t0.1s\n"}}}
		var w bytes.Buffer
		if code := runPart(p, []string{"./..."}, manifest, f, &w, io.Discard); code != 0 {
			t.Errorf("exit %d", code)
		}
		if r := readManifest(t, manifest); !reflect.DeepEqual(r.ran, map[test]string{{"p/a", mine}: "pass"}) {
			t.Errorf("the record says %v ran; want the one verdict, in its package, and not the subtest's", r.ran)
		}
		if !strings.Contains(w.String(), "Address already in use--- PASS") {
			t.Errorf("the test's own words were not printed: %q", w.String())
		}
	})
	t.Run("a record that cannot be written fails the part", func(t *testing.T) {
		manifest := filepath.Join(t.TempDir(), "no-such-dir", "rec")
		f := &fakeGo{answers: []answer{{events: listed("p/a", mine)}, {raw: verbose("p/a", "PASS "+mine)}}}
		var errw bytes.Buffer
		if code := runPart(p, []string{"./..."}, manifest, f, io.Discard, &errw); code == 0 {
			t.Errorf("exit 0 with no record written; the verdict job would find this part missing, and only it would say so")
		}
	})
	t.Run("a line longer than any buffer is printed, and what follows it read", func(t *testing.T) {
		manifest := filepath.Join(t.TempDir(), "rec")
		long := strings.Repeat("x", 2<<20)
		f := &fakeGo{answers: []answer{{events: listed("p/a", mine)}, {raw: long + "\n" + verbose("p/a", "PASS "+mine)}}}
		var w bytes.Buffer
		if code := runPart(p, []string{"./..."}, manifest, f, &w, io.Discard); code != 0 {
			t.Errorf("exit %d", code)
		}
		if !strings.Contains(w.String(), long+"\n") {
			t.Error("the long line was not printed whole")
		}
		if r := readManifest(t, manifest); r.ran[test{"p/a", mine}] != "pass" {
			t.Errorf("the record says %v; the verdict after the long line was lost", r.ran)
		}
	})
	t.Run("output that cannot be read to the end fails the part", func(t *testing.T) {
		f := &fakeGo{answers: []answer{{events: listed("p/a", mine)}, {raw: verbose("p/a", "PASS "+mine), broken: errors.New("pipe broke")}}}
		var errw bytes.Buffer
		if code := runPart(p, []string{"./..."}, filepath.Join(t.TempDir(), "rec"), f, io.Discard, &errw); code == 0 || !strings.Contains(errw.String(), "pipe broke") {
			t.Errorf("exit %d, said %q; what go said past the break is unknown", code, errw.String())
		}
	})
	t.Run("a last line with no newline is read", func(t *testing.T) {
		manifest := filepath.Join(t.TempDir(), "rec")
		f := &fakeGo{answers: []answer{{events: listed("p/a", mine)}, {raw: strings.TrimSuffix(verbose("p/a", "PASS "+mine), "\n")}}}
		runPart(p, []string{"./..."}, manifest, f, io.Discard, io.Discard)
		if r := readManifest(t, manifest); r.ran[test{"p/a", mine}] != "pass" {
			t.Errorf("the record says %v; the package's last line had no newline and was dropped", r.ran)
		}
	})
	t.Run("a package's verdicts are its own, not the next one's", func(t *testing.T) {
		second := ""
		for i := 0; second == ""; i++ {
			if n := fmt.Sprintf("TestOther%d", i); partOf(n, p.n) == p.k && n != mine {
				second = n
			}
		}
		manifest := filepath.Join(t.TempDir(), "rec")
		f := &fakeGo{answers: []answer{
			{events: append(listed("p/a", mine), listed("p/b", second)...)},
			{raw: verbose("p/a", "PASS "+mine) + verbose("p/b", "PASS "+second)},
		}}
		runPart(p, []string{"./..."}, manifest, f, io.Discard, io.Discard)
		r := readManifest(t, manifest)
		if want := map[test]string{{"p/a", mine}: "pass", {"p/b", second}: "pass"}; !reflect.DeepEqual(r.ran, want) {
			t.Errorf("the record says %v ran, want %v", r.ran, want)
		}
	})
	t.Run("a test of its own that never reported fails the part", func(t *testing.T) {
		f := &fakeGo{answers: []answer{{events: listed("p/a", mine)}, {}}}
		var errw bytes.Buffer
		if code := runPart(p, []string{"./..."}, filepath.Join(t.TempDir(), "rec"), f, io.Discard, &errw); code == 0 {
			t.Error("exit 0 with a test of this part never run")
		}
		if !strings.Contains(errw.String(), mine) {
			t.Errorf("the missing test was not named: %q", errw.String())
		}
	})
	t.Run("a part with nothing of its own runs nothing and records that", func(t *testing.T) {
		manifest := filepath.Join(t.TempDir(), "rec")
		f := &fakeGo{answers: []answer{{events: listed("p/a", other)}}}
		if code := runPart(p, []string{"./..."}, manifest, f, io.Discard, io.Discard); code != 0 {
			t.Errorf("exit %d", code)
		}
		if len(f.calls) != 1 {
			t.Errorf("go was called %d times; with nothing to run, only the list", len(f.calls))
		}
		if r := readManifest(t, manifest); len(r.all) != 1 || len(r.mine) != 0 {
			t.Errorf("the record says %+v", r)
		}
	})
	t.Run("a second command appends to the same record", func(t *testing.T) {
		manifest := filepath.Join(t.TempDir(), "rec")
		for _, pkg := range []string{"p/a", "p/b"} {
			f := &fakeGo{answers: []answer{{events: listed(pkg, mine)}, {raw: verbose(pkg, "PASS "+mine)}}}
			runPart(p, []string{"./x"}, manifest, f, io.Discard, io.Discard)
		}
		if r := readManifest(t, manifest); len(r.all) != 2 || len(r.ran) != 2 {
			t.Errorf("the record says %+v; the sweep's and busy's runs are one part's", r)
		}
	})
	t.Run("a failed list runs nothing", func(t *testing.T) {
		f := &fakeGo{answers: []answer{{code: 1}}}
		if code := runPart(p, []string{"./..."}, filepath.Join(t.TempDir(), "rec"), f, io.Discard, io.Discard); code == 0 || len(f.calls) != 1 {
			t.Errorf("exit %d after %d calls", code, len(f.calls))
		}
	})
}

// whole builds the records of a gate of n parts over the names, each part
// taking and running its own.
func whole(n int, names ...string) []record {
	var recs []record
	for k := 1; k <= n; k++ {
		r := record{file: "part" + string(rune('0'+k)), part: part{k, n}, all: map[test]bool{}, mine: map[test]bool{}, ran: map[test]string{}}
		for _, name := range names {
			tt := test{"p", name}
			r.all[tt] = true
			if partOf(name, n) == k {
				r.mine[tt] = true
				r.ran[tt] = "pass"
			}
		}
		recs = append(recs, r)
	}
	return recs
}

func TestTheWholeIsEveryTestOnce(t *testing.T) {
	a, b := namesIn(t, 1, 2)
	names := []string{a, b}
	if _, err := verify(whole(2, names...)); err != nil {
		t.Fatalf("a whole gate was refused: %v", err)
	}
	for _, c := range []struct {
		name   string
		break_ func(recs []record) []record
		want   string
	}{
		{"no records at all", func([]record) []record { return nil }, "no part's record"},
		{"a part missing", func(r []record) []record { return r[:1] }, "part 2/2 has no record"},
		{"a part twice", func(r []record) []record { return append(r, r[1]) }, "part 2/2 has 2 records"},
		{"a part three times", func(r []record) []record { return append(r, r[1], r[1]) }, "part 2/2 has 3 records"},
		{"parts of different wholes", func(r []record) []record { r[1].part = part{2, 3}; return r }, "agree on N"},
		{"a list missing a test", func(r []record) []record { r[1].all = map[test]bool{{"p", a}: true}; return r }, "listed different tests"},
		{"a list with one more", func(r []record) []record { r[1].all[test{"q", a}] = true; return r }, "listed different tests"},
		{"a test taken that was not listed", func(r []record) []record { r[0].mine[test{"q", a}] = true; r[0].ran[test{"q", a}] = "pass"; return r }, "which was not listed"},
		{"a test taken by the wrong part", func(r []record) []record { r[0].mine[test{"p", b}] = true; return r }, "which falls in part 2/2"},
		{"a test taken twice", func(r []record) []record { r[0].mine[test{"p", b}] = true; r[0].ran[test{"p", b}] = "pass"; return r }, "was taken by"},
		{"a test run by a part that did not take it", func(r []record) []record { r[0].ran[test{"p", b}] = "pass"; return r }, "which it did not take"},
		{"a test that failed", func(r []record) []record { r[0].ran[test{"p", a}] = "fail"; return r }, `ended "fail"`},
		{"a test taken and never run", func(r []record) []record { delete(r[0].ran, test{"p", a}); return r }, "never reported a result"},
		{"a test listed and taken by none", func(r []record) []record { delete(r[0].mine, test{"p", a}); delete(r[0].ran, test{"p", a}); return r }, "no part took it"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := verify(c.break_(whole(2, names...)))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want an error saying %q", err, c.want)
			}
		})
	}
}

func TestARecordIsReadAsWritten(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	t.Run("what appendManifest writes, readRecord reads", func(t *testing.T) {
		path := filepath.Join(dir, "round")
		listedTests := []test{{"p", "TestA"}, {"p", "TestB"}}
		if err := appendManifest(path, part{1, 2}, listedTests, listedTests[:1], map[test]string{{"p", "TestA"}: "skip"}); err != nil {
			t.Fatal(err)
		}
		r := readManifest(t, path)
		var all []string
		for tt := range r.all {
			all = append(all, tt.name)
		}
		sort.Strings(all)
		if r.part != (part{1, 2}) || !reflect.DeepEqual(all, []string{"TestA", "TestB"}) || !r.mine[test{"p", "TestA"}] || r.ran[test{"p", "TestA"}] != "skip" {
			t.Errorf("read back %+v", r)
		}
	})
	t.Run("a line of another shape is refused", func(t *testing.T) {
		if _, err := readRecord(write("odd", "part\t1/2\nran\tp\tTestA\n")); err == nil {
			t.Error("a ran line with no verdict was taken")
		}
	})
	t.Run("two parts in one record are refused", func(t *testing.T) {
		if _, err := readRecord(write("two", "part\t1/2\npart\t2/2\n")); err == nil {
			t.Error("a record of two parts was taken")
		}
	})
	t.Run("a record naming no part is refused", func(t *testing.T) {
		if _, err := readRecord(write("none", "all\tp\tTestA\n")); err == nil {
			t.Error("a record of no part was taken")
		}
	})
	t.Run("the directory's records are all read", func(t *testing.T) {
		d := t.TempDir()
		for _, r := range whole(2, "TestA", "TestB", "TestC") {
			var listedTests, mineTests []test
			for tt := range r.all {
				listedTests = append(listedTests, tt)
			}
			for tt := range r.mine {
				mineTests = append(mineTests, tt)
			}
			sub := filepath.Join(d, r.file)
			os.MkdirAll(sub, 0o755)
			if err := appendManifest(filepath.Join(sub, "rec"), r.part, listedTests, mineTests, r.ran); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := verifyDir(d); err != nil {
			t.Errorf("a whole gate's records were refused: %v", err)
		}
		os.RemoveAll(filepath.Join(d, "part2"))
		if _, err := verifyDir(d); err == nil {
			t.Error("a gate missing a part's record was taken")
		}
	})
}

// The command as the Makefile runs it: built, and started with the
// environment the gate gives it.
func TestTheCommandIsNotThereUnlessAPartIsAskedFor(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "shard")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("building: %v\n%s", err, out)
	}
	runIt := func(env []string, args ...string) (string, int) {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return string(out), code
	}
	t.Run("unset, it is the command it was given", func(t *testing.T) {
		got, code := runIt([]string{"OPOSSUM_GATE_SHARD="}, "go", "test", "-list", "^TestTheCommandIsNot", ".")
		if code != 0 || !strings.HasPrefix(got, "TestTheCommandIsNotThereUnlessAPartIsAskedFor\nok") {
			t.Errorf("exit %d, said %q; want what `go test -list` says", code, got)
		}
	})
	t.Run("unset, the command's status is its own", func(t *testing.T) {
		if _, code := runIt([]string{"OPOSSUM_GATE_SHARD="}, "go", "test", "./no/such/package"); code == 0 {
			t.Error("a failing go test came back 0")
		}
	})
	t.Run("a part with nowhere to write its record is refused", func(t *testing.T) {
		if got, code := runIt([]string{"OPOSSUM_GATE_SHARD=1/2", "OPOSSUM_GATE_SHARD_MANIFEST="}, "go", "test", "."); code != 2 {
			t.Errorf("exit %d, said %q", code, got)
		}
	})
	t.Run("a part that is not k/N is refused", func(t *testing.T) {
		if got, code := runIt([]string{"OPOSSUM_GATE_SHARD=3/2", "OPOSSUM_GATE_SHARD_MANIFEST=" + filepath.Join(t.TempDir(), "rec")}, "go", "test", "."); code != 2 {
			t.Errorf("exit %d, said %q", code, got)
		}
	})
	t.Run("a command that is not go test is refused", func(t *testing.T) {
		if _, code := runIt(nil, "go", "vet", "."); code != 2 {
			t.Errorf("exit %d", code)
		}
	})
	t.Run("-verify says what it found", func(t *testing.T) {
		if got, code := runIt(nil, "-verify", t.TempDir()); code != 1 || !strings.Contains(got, "no part's record") {
			t.Errorf("exit %d, said %q", code, got)
		}
	})
}

// flakyPipe hands out its first chunk, then an error once, then the rest:
// what is left in it after a read is what the writer is still blocked on.
type flakyPipe struct {
	chunks []string
	failed bool
}

func (p *flakyPipe) Read(b []byte) (int, error) {
	if len(p.chunks) == 0 {
		return 0, io.EOF
	}
	if len(p.chunks) == 1 && !p.failed {
		p.failed = true
		return 0, errors.New("pipe broke")
	}
	n := copy(b, p.chunks[0])
	p.chunks[0] = p.chunks[0][n:]
	if p.chunks[0] == "" {
		p.chunks = p.chunks[1:]
	}
	return n, nil
}

func TestAReadThatFailsStillEmptiesThePipe(t *testing.T) {
	p := &flakyPipe{chunks: []string{"first\n", strings.Repeat("more\n", 1000)}}
	var got []string
	err := eachLine(p, func(line string) { got = append(got, line) })
	if err == nil || !strings.Contains(err.Error(), "pipe broke") {
		t.Errorf("eachLine returned %v; the failed read is the answer", err)
	}
	if len(got) != 1 || got[0] != "first" {
		t.Errorf("handed on %q before the failure, want the one line", got)
	}
	if len(p.chunks) != 0 {
		t.Errorf("%d chunks left unread after the failure; a writer blocked on them never exits", len(p.chunks))
	}
}
