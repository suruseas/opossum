package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// part is one of the gate's N parts, k counted from 1.
type part struct{ k, n int }

func (p part) String() string { return fmt.Sprintf("%d/%d", p.k, p.n) }

// parsePart reads `k/N`. Digits only, both sides: a sign or a space is a
// value nobody meant, and reading it as some part would run a part nobody
// asked for.
func parsePart(s string) (part, error) {
	ks, ns, ok := strings.Cut(s, "/")
	digits := regexp.MustCompile(`^[0-9]+$`)
	if !ok || !digits.MatchString(ks) || !digits.MatchString(ns) {
		return part{}, fmt.Errorf("%q is not k/N", s)
	}
	k, kerr := strconv.Atoi(ks)
	n, nerr := strconv.Atoi(ns)
	if kerr != nil || nerr != nil || k < 1 || k > n {
		return part{}, fmt.Errorf("%q is not a part of a whole: k runs from 1 to N", s)
	}
	return part{k, n}, nil
}

// partOf is the part a test name falls in. The name alone decides it, not the
// package: a `-run` matches a name in every package it is given, so a name
// that fell in two parts in two packages would run twice in each.
//
// SHA-256 rather than a cheaper hash: FNV's low bits follow the parity of the
// bytes, and a remainder by a small N reads exactly those bits.
func partOf(name string, n int) int {
	sum := sha256.Sum256([]byte(name))
	return int(binary.BigEndian.Uint64(sum[:8])%uint64(n)) + 1
}

// The flags this builds itself. One already on the command would either be
// overridden by this one or override it, and either way the part would not
// be the part the record says ran.
var ownFlags = map[string]bool{"run": true, "skip": true, "list": true, "json": true, "v": true, "bench": true}

// splitArgs separates the command's flags from its packages. A flag is
// anything starting with `-`, so a flag's value has to be joined to it
// (`-timeout=30m`): written apart, the value would be read as a package.
func splitArgs(args []string) (flags, pkgs []string, err error) {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			pkgs = append(pkgs, a)
			continue
		}
		// `-run`, `--run` and `-test.run` are one flag to go test.
		name, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-"), "=")
		if ownFlags[strings.TrimPrefix(name, "test.")] {
			return nil, nil, fmt.Errorf("the command already says %s, which a part sets itself", a)
		}
		flags = append(flags, a)
	}
	if len(pkgs) == 0 {
		return nil, nil, errors.New("the command names no packages; a part is taken from the packages it is given")
	}
	return flags, pkgs, nil
}

// event is a line of `go test -json`. Build output comes with ImportPath
// rather than Package.
type event struct {
	Action     string
	Package    string
	ImportPath string
	Test       string
	Output     string
}

// tool runs `go` with the arguments it is given and hands back its standard
// output; wait ends the run and says how it ended.
type tool interface {
	start(args []string) (out io.Reader, wait func() int, err error)
}

type goTool struct{}

func (goTool) start(args []string) (io.Reader, func() int, error) {
	cmd := exec.Command("go", args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return out, func() int {
		if err := cmd.Wait(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) && ee.ExitCode() > 0 {
				return ee.ExitCode()
			}
			return 1
		}
		return 0
	}, nil
}

// A listed name: what `go test -list` prints for a test, a fuzz target or an
// example, alone on its line.
var listedName = regexp.MustCompile(`^(Test|Fuzz|Example)[\p{L}\p{N}_]*$`)

// listTests asks go for every test the packages have, built with the
// command's own flags.
func listTests(t tool, flags, pkgs []string, errw io.Writer) (map[string][]string, error) {
	args := append([]string{"test", "-list", "^(Test|Fuzz|Example)", "-json"}, flags...)
	out, wait, err := t.start(append(args, pkgs...))
	if err != nil {
		return nil, err
	}
	all := map[string][]string{}
	var said []string
	failed := false
	readErr := eachLine(out, func(line string) {
		var e event
		if json.Unmarshal([]byte(line), &e) != nil {
			said = append(said, line+"\n")
			return
		}
		if e.Output != "" {
			said = append(said, e.Output)
			if name := strings.TrimSpace(e.Output); e.Package != "" && listedName.MatchString(name) {
				all[e.Package] = append(all[e.Package], name)
			}
		}
		if e.Action == "fail" {
			failed = true
		}
	})
	code := wait()
	if readErr != nil {
		return nil, fmt.Errorf("reading the list (go exited %d): %v", code, readErr)
	}
	if code != 0 || failed {
		fmt.Fprint(errw, strings.Join(said, ""))
		return nil, fmt.Errorf("listing the tests failed (exit %d), so which of them are this part's is unknown", code)
	}
	return all, nil
}

// What `go test -v` prints that a plain run does not: the start, pause and
// resumption of each test, a passing or skipped test's verdict, and each
// package's PASS and its coverage on a line of its own (the `ok` line says
// it too). Left out, so a part's log reads like the plain gate's;
// everything else — a test's own lines, a failure, the package's `ok` or
// FAIL — comes through as it is.
//
// -v rather than -json, which would say all of this in a form made to be
// read: under -json the test binary runs with its stderr handed to its
// stdout, and a test that asks whether something writes to the process's
// stderr gets a different answer than in the plain gate.
var verboseOnly = regexp.MustCompile(`^\s*(=== (RUN|PAUSE|CONT|NAME)\s|--- (PASS|SKIP): )|^PASS$|^coverage: [^\t]* of statements$`)

// A top-level test's verdict and the line that ends a package's run: go
// prints each package's lines together and then the latter, so the verdicts
// before it are that package's. Neither is looked for only at the start of a
// line: a test that writes without a final newline leaves the next line go
// prints on the end of its own (measured: "…Address already in use--- PASS:
// TestA…"). A subtest's name has a `/` in it, so its verdict is not a
// top-level one wherever it is.
var (
	verdictLine = regexp.MustCompile(`--- (PASS|FAIL|SKIP): ([^\s/]+) \(`)
	packageEnd  = regexp.MustCompile(`(?:ok  |FAIL)\t(\S+)`)
)

// eachLine hands f every line of r, however long, without its newline. It
// reads r to the end whatever happens: a pipe left unread fills, and the
// process writing it — `go test`, which this then waits for — blocks on the
// write and never exits.
func eachLine(r io.Reader, f func(string)) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			f(strings.TrimSuffix(line, "\n"))
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			io.Copy(io.Discard, br)
			return err
		}
	}
}

type test struct{ pkg, name string }

// runPart runs part p of `go test <args>` and appends its record to the
// manifest. Its status is go's, or 1 when go said nothing was wrong and a
// test of this part did not run.
func runPart(p part, args []string, manifest string, t tool, w, errw io.Writer) int {
	flags, pkgs, err := splitArgs(args)
	if err != nil {
		fmt.Fprintf(errw, "shard: %v\n", err)
		return 2
	}
	all, err := listTests(t, flags, pkgs, errw)
	if err != nil {
		fmt.Fprintf(errw, "shard: %v\n", err)
		return 1
	}
	var listed, mine []test
	names := map[string]bool{}
	var runPkgs []string
	for pkg, ns := range all {
		took := false
		for _, n := range ns {
			listed = append(listed, test{pkg, n})
			if partOf(n, p.n) == p.k {
				mine = append(mine, test{pkg, n})
				names[n] = true
				took = true
			}
		}
		if took {
			runPkgs = append(runPkgs, pkg)
		}
	}
	sort.Strings(runPkgs)

	ran := map[test]string{}
	code := 0
	if len(runPkgs) == 0 {
		fmt.Fprintf(w, "part %s: none of the %d tests listed falls in this part\n", p, len(listed))
	} else {
		// A listed name is letters, digits and underscores (listedName), so
		// it is its own pattern; anchored, it matches itself and no longer
		// name that starts with it.
		var alts []string
		for n := range names {
			alts = append(alts, n)
		}
		sort.Strings(alts)
		args := append([]string{"test", "-v", "-run", "^(" + strings.Join(alts, "|") + ")$"}, flags...)
		out, wait, err := t.start(append(args, runPkgs...))
		if err != nil {
			fmt.Fprintf(errw, "shard: %v\n", err)
			return 1
		}
		type verdict struct{ name, action string }
		var pending []verdict
		readErr := eachLine(out, func(line string) {
			if m := verdictLine.FindStringSubmatch(line); m != nil {
				pending = append(pending, verdict{m[2], strings.ToLower(m[1])})
			}
			if m := packageEnd.FindStringSubmatch(line); m != nil {
				for _, v := range pending {
					ran[test{m[1], v.name}] = v.action
				}
				pending = nil
			}
			if !verboseOnly.MatchString(line) {
				fmt.Fprintln(w, line)
			}
		})
		if readErr != nil {
			fmt.Fprintf(errw, "shard: reading go test: %v\n", readErr)
			code = 1
		}
		if c := wait(); c != 0 {
			code = c
		}
	}

	var missing []string
	for _, m := range mine {
		if _, ok := ran[m]; !ok {
			missing = append(missing, m.pkg+" "+m.name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		fmt.Fprintf(errw, "shard: part %s took %d tests and %d of them never reported a result:\n", p, len(mine), len(missing))
		for _, m := range missing {
			fmt.Fprintf(errw, "  %s\n", m)
		}
		if code == 0 {
			code = 1
		}
	}
	if err := appendManifest(manifest, p, listed, mine, ran); err != nil {
		fmt.Fprintf(errw, "shard: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	fmt.Fprintf(w, "part %s: ran %d of the %d tests listed in %d packages\n", p, len(ran), len(listed), len(all))
	return code
}

func appendManifest(path string, p part, listed, mine []test, ran map[test]string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "part\t%s\n", p)
	for _, t := range listed {
		fmt.Fprintf(&b, "all\t%s\t%s\n", t.pkg, t.name)
	}
	for _, t := range mine {
		fmt.Fprintf(&b, "mine\t%s\t%s\n", t.pkg, t.name)
	}
	for t, action := range ran {
		fmt.Fprintf(&b, "ran\t%s\t%s\t%s\n", t.pkg, t.name, action)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// record is what one machine's manifest says.
type record struct {
	file  string
	part  part
	all   map[test]bool
	mine  map[test]bool
	ran   map[test]string
	parts map[string]bool
}

func readRecord(path string) (record, error) {
	r := record{file: path, all: map[test]bool{}, mine: map[test]bool{}, ran: map[test]string{}, parts: map[string]bool{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	for i, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		f := strings.Split(line, "\t")
		switch {
		case len(f) == 2 && f[0] == "part":
			r.parts[f[1]] = true
		case len(f) == 3 && f[0] == "all":
			r.all[test{f[1], f[2]}] = true
		case len(f) == 3 && f[0] == "mine":
			r.mine[test{f[1], f[2]}] = true
		case len(f) == 4 && f[0] == "ran":
			r.ran[test{f[1], f[2]}] = f[3]
		default:
			return r, fmt.Errorf("%s:%d: %q is not a line of a part's record", path, i+1, line)
		}
	}
	if len(r.parts) != 1 {
		return r, fmt.Errorf("%s names %d parts; one machine's record is one part's", path, len(r.parts))
	}
	for s := range r.parts {
		if r.part, err = parsePart(s); err != nil {
			return r, fmt.Errorf("%s: %v", path, err)
		}
	}
	return r, nil
}

// verifyDir reads every part's record under dir and refuses unless together
// they are the whole: every part of the N there once, the same tests listed
// by each, the tests taken by each part its own and none of another's, and
// every test listed run — passed or skipped — by the part that took it.
func verifyDir(dir string) (string, error) {
	var recs []record
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		r, rerr := readRecord(path)
		if rerr != nil {
			return rerr
		}
		recs = append(recs, r)
		return nil
	})
	if err != nil {
		return "", err
	}
	return verify(recs)
}

func verify(recs []record) (string, error) {
	if len(recs) == 0 {
		return "", errors.New("no part's record was found, so nothing says the gate ran at all")
	}
	var problems []string
	say := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	n := recs[0].part.n
	seen := map[int][]string{}
	for _, r := range recs {
		if r.part.n != n {
			say("%s is part %s, and %s is part %s; the parts of one whole agree on N", recs[0].file, recs[0].part, r.file, r.part)
		}
		seen[r.part.k] = append(seen[r.part.k], r.file)
	}
	for k := 1; k <= n; k++ {
		switch len(seen[k]) {
		case 0:
			say("part %d/%d has no record; the tests it would have taken are not known to have run", k, n)
		case 1:
		default:
			say("part %d/%d has %d records (%s); a part runs once", k, n, len(seen[k]), strings.Join(seen[k], ", "))
		}
	}

	all := recs[0].all
	for _, r := range recs[1:] {
		if d := differ(all, r.all); d != "" {
			say("%s and %s listed different tests (%s); the parts are of one whole only when they list the same one", recs[0].file, r.file, d)
		}
	}

	owner := map[test]string{}
	for _, r := range recs {
		for t := range r.mine {
			if !all[t] {
				say("%s took %s %s, which was not listed", r.file, t.pkg, t.name)
			}
			if partOf(t.name, r.part.n) != r.part.k {
				say("%s took %s %s, which falls in part %d/%d", r.file, t.pkg, t.name, partOf(t.name, r.part.n), r.part.n)
			}
			if o, ok := owner[t]; ok {
				say("%s %s was taken by %s and by %s", t.pkg, t.name, o, r.file)
			}
			owner[t] = r.file
		}
		for t, action := range r.ran {
			if !r.mine[t] {
				say("%s ran %s %s, which it did not take", r.file, t.pkg, t.name)
			}
			if action != "pass" && action != "skip" {
				say("%s %s ended %q in %s", t.pkg, t.name, action, r.file)
			}
		}
		for t := range r.mine {
			if _, ok := r.ran[t]; !ok {
				say("%s took %s %s and it never reported a result", r.file, t.pkg, t.name)
			}
		}
	}
	for t := range all {
		if _, ok := owner[t]; !ok {
			say("%s %s was listed and no part took it", t.pkg, t.name)
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return "", fmt.Errorf("the parts are not the whole gate:\n  %s", strings.Join(problems, "\n  "))
	}
	pkgs := map[string]bool{}
	skipped := 0
	for t := range all {
		pkgs[t.pkg] = true
	}
	for _, r := range recs {
		for _, a := range r.ran {
			if a == "skip" {
				skipped++
			}
		}
	}
	return fmt.Sprintf("%d parts ran all %d tests listed in %d packages, each once (%d of them skipped)", n, len(all), len(pkgs), skipped), nil
}

// differ names a test one set has and the other lacks, or nothing.
func differ(a, b map[test]bool) string {
	var only []string
	for t := range a {
		if !b[t] {
			only = append(only, "only the first: "+t.pkg+" "+t.name)
		}
	}
	for t := range b {
		if !a[t] {
			only = append(only, "only the second: "+t.pkg+" "+t.name)
		}
	}
	if len(only) == 0 {
		return ""
	}
	sort.Strings(only)
	return fmt.Sprintf("%d differ, first %s", len(only), only[0])
}
