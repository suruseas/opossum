package repohygiene_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// An unrun check is no check, and this one cannot be run: whether a suite cleans
// up after a panic is a property of a process that is no longer there. So the
// wiring is read instead — every suite that builds under $TMPDIR has to go
// through internal/suitedir, because a directory made with a plain os.MkdirTemp
// carries no pid and no later run can tell whether it may remove it.
//
// Reading is what this does, and all it does: it says the call is written, not
// that it runs, and not that the sweep works. That is what the suitedir package's
// own tests are for, and the measurement in the pull request.
//
// It walks _test.go files only, so a helper that makes one of these directories
// from a non-test file is invisible to it — including the product's own
// `opossum-dryrun`, which leaks down the same path.
//
// And it reads three spellings of "the top of $TMPDIR". A first argument that is
// a variable names a directory only the compiler knows, so a suite that computes
// the path first is not seen. The set below is what holds the three that exist;
// the pattern is for the fourth one somebody writes.
// howItReads is the whole judgement this check makes about one file, pulled out
// so that it can be handed inputs. Left in the walk, it was unmeasurable: three
// mutations — the spelling the pattern reads, the broken-walk guard, the reverse
// direction of the set — all survived, and the check was silently wrong in two
// consecutive review rounds while the suite stayed green.
func howItReads(b []byte) (offender, wired bool) {
	return plain.Match(b), through.Match(b)
}

// The hazard is the top of $TMPDIR: that is where cmd/noleftovers looks (a
// non-recursive glob of os.TempDir()), and where a name with no pid in it has
// nobody to reclaim it. Measured, not assumed — a directory one level down is
// not reported by that tool.
//
// So these are the spellings that name that place. A variable or t.TempDir()
// names somewhere else, and matching those was worse than not matching them:
// os.MkdirTemp(t.TempDir(), …) is safe, was flagged, and the advice printed
// alongside would have moved it up into $TMPDIR.
var plain = regexp.MustCompile(
	`(os|ioutil)\.(MkdirTemp|TempDir|CreateTemp)\(\s*(""|os\.TempDir\(\)|os\.Getenv\("TMPDIR"\)|"/tmp")\s*,\s*"opossum-`)

var through = regexp.MustCompile(`suitedir\.Make\("opossum-`)

func TestWhatCountsAsMakingOneAtTheTop(t *testing.T) {
	for _, tc := range []struct {
		src      string
		offender bool
		wired    bool
	}{
		{`os.MkdirTemp("", "opossum-cmd-test-")`, true, false},
		{`os.MkdirTemp(os.TempDir(), "opossum-rt-test-")`, true, false},
		{`os.MkdirTemp(os.Getenv("TMPDIR"), "opossum-rt-test-")`, true, false},
		{`ioutil.TempDir("", "opossum-rt-test-")`, true, false},
		// A file, not a directory — the glob in cmd/noleftovers takes both, and
		// cmd/mutate's leftover .cover files are the proof.
		{`os.CreateTemp("", "opossum-mutate-")`, true, false},
		// Where os.TempDir() lands on Linux with TMPDIR unset, which is how the
		// CI runners come.
		{`os.MkdirTemp("/tmp", "opossum-rt-test-")`, true, false},
		{"os.MkdirTemp(\n\t\t\"\",\n\t\t\"opossum-multiline-\")", true, false},
		// Somewhere the toolchain cleans up. Safe, and flagging it would send the
		// reader the wrong way.
		{`os.MkdirTemp(t.TempDir(), "opossum-fixture-")`, false, false},
		// A path only the compiler knows. Out of reach either way.
		{`os.MkdirTemp(tmpRoot, "opossum-rt-test-")`, false, false},
		{`suitedir.Make("opossum-cmd-test-")`, false, true},
		// Another family entirely.
		{`os.MkdirTemp("", "something-else-")`, false, false},
	} {
		t.Run(tc.src, func(t *testing.T) {
			offender, wired := howItReads([]byte(tc.src))
			if offender != tc.offender || wired != tc.wired {
				t.Errorf("offender=%v wired=%v, want %v, %v", offender, wired, tc.offender, tc.wired)
			}
		})
	}
}

// And the two directions of the set, driven from made-up sets rather than from a
// walk of the tree — the walk is what makes the real check unmeasurable.
func TestWhatTheSetSaysIsMissingOrNew(t *testing.T) {
	want := map[string]struct{}{"a_test.go": {}, "b_test.go": {}}
	for _, tc := range []struct {
		name    string
		got     []string
		missing []string
		extra   []string
	}{
		{"all present", []string{"a_test.go", "b_test.go"}, nil, nil},
		{"one stopped", []string{"a_test.go"}, []string{"b_test.go"}, nil},
		{"a fourth appeared", []string{"a_test.go", "b_test.go", "c_test.go"}, nil, []string{"c_test.go"}},
		{"one stopped and a fourth appeared", []string{"a_test.go", "c_test.go"}, []string{"b_test.go"}, []string{"c_test.go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := map[string]struct{}{}
			for _, g := range tc.got {
				got[g] = struct{}{}
			}
			missing, extra := compare(want, got)
			if !equal(missing, tc.missing) || !equal(extra, tc.extra) {
				t.Errorf("missing=%v extra=%v, want %v, %v", missing, extra, tc.missing, tc.extra)
			}
		})
	}
}

func compare(want, got map[string]struct{}) (missing, extra []string) {
	for name := range want {
		if _, ok := got[name]; !ok {
			missing = append(missing, name)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// scan is the walk, with the root as an argument so that it can be pointed at a
// tree made for the purpose. Left inline it was the last judgement here without
// an eval — and the line that matters most, the one separating "could not read"
// from "stopped calling it", was the one a mutation could delete in silence.
func scan(root, self string) (offenders, unreadable []string, wired map[string]struct{}, walked int) {
	wired = map[string]struct{}{}
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			unreadable = append(unreadable, rel(root, p))
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		if p == self {
			// This file holds every spelling as data, so reading it reports
			// itself. Skipped by the path the compiler recorded rather than by a
			// name written out here, because the name has already changed once in
			// this branch's life and would change again silently.
			//
			// What the skip gives up: a real call written in this file is
			// invisible. That is a real hole and it is left open on purpose —
			// this package tests checks, and will never be a suite that builds
			// under $TMPDIR.
			return nil
		}
		walked++
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			unreadable = append(unreadable, rel(root, p))
			return nil
		}
		offender, through := howItReads(b)
		if offender {
			offenders = append(offenders, rel(root, p))
		}
		if through {
			wired[rel(root, p)] = struct{}{}
		}
		return nil
	})
	sort.Strings(offenders)
	sort.Strings(unreadable)
	return offenders, unreadable, wired, walked
}

func rel(root, p string) string {
	return filepath.ToSlash(strings.TrimPrefix(p, root+string(os.PathSeparator)))
}

// The three things the walk decides that the table above cannot reach: a file it
// could not read, a tree with nothing in it, and — the one `walked == 0` does not
// catch — a tree that is full of the wrong files.
func TestWhatTheWalkMakesOfATree(t *testing.T) {
	for _, tc := range []struct {
		name       string
		files      map[string]string
		unreadable string
		wantWalked int
		wantUnread []string
		wantWired  []string
	}{
		{
			name: "a suite that goes through suitedir",
			files: map[string]string{
				"a/a_test.go": "package a\n\nvar _ = suitedir.Make(\"opossum-a-test-\")\n",
			},
			wantWalked: 1,
			wantWired:  []string{"a/a_test.go"},
		},
		{
			name: "a file it cannot read",
			files: map[string]string{
				"a/a_test.go": "package a\n",
			},
			unreadable: "a/a_test.go",
			wantWalked: 1,
			wantUnread: []string{"a/a_test.go"},
		},
		{
			name:       "nothing to walk",
			files:      map[string]string{"a/notatest.go": "package a\n"},
			wantWalked: 0,
		},
		{
			name: "full of the wrong files",
			files: map[string]string{
				"a/unrelated_test.go": "package a\n",
			},
			wantWalked: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for name, body := range tc.files {
				full := filepath.Join(root, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.unreadable != "" {
				full := filepath.Join(root, filepath.FromSlash(tc.unreadable))
				if err := os.Chmod(full, 0o000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(full, 0o644) })
				if _, err := os.ReadFile(full); err == nil {
					t.Skip("this user can read a file with no permissions, so there is nothing to measure")
				}
			}
			_, unreadable, wired, walked := scan(root, filepath.Join(root, "nothing-here"))
			if walked != tc.wantWalked {
				t.Errorf("walked %d files, want %d", walked, tc.wantWalked)
			}
			if !equal(unreadable, tc.wantUnread) {
				t.Errorf("unreadable = %v, want %v", unreadable, tc.wantUnread)
			}
			var names []string
			for name := range wired {
				names = append(names, name)
			}
			sort.Strings(names)
			if !equal(names, tc.wantWired) {
				t.Errorf("wired = %v, want %v", names, tc.wantWired)
			}
		})
	}
}

// It skips the file it is written in, and only that one.
func TestItSkipsItselfAndNothingElse(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"me_test.go", "other_test.go"} {
		body := "package a\n\nvar _ = suitedir.Make(\"opossum-x-\")\n"
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, _, wired, walked := scan(root, filepath.Join(root, "me_test.go"))
	if walked != 1 {
		t.Errorf("walked %d files, want 1 (the other one)", walked)
	}
	if _, ok := wired["me_test.go"]; ok {
		t.Error("it read the file it is written in")
	}
	if _, ok := wired["other_test.go"]; !ok {
		t.Error("it skipped a file that is not itself")
	}
}

func TestEverySuiteThatBuildsUnderTMPDIRGoesThroughSuitedir(t *testing.T) {
	want := map[string]struct{}{
		"cmd/mutate/interruptduringadd_test.go":            {},
		"cmd/opossum/main_test.go":                         {},
		"internal/orchestrator/orchestrator_test.go":       {},
		"internal/orchestrator/realruntime_socket_test.go": {},
		"internal/runtime/runtime_test.go":                 {},
	}

	root := repoRoot(t)
	// Pointed at the wrong subtree, the walk finds _test.go files and reports all
	// three suites as having stopped — innocent, and walked != 0 so no guard
	// fires. repoRoot climbs two directories, so it moves when this package does,
	// which has already happened twice in this branch.
	if b, err := os.ReadFile(filepath.Join(root, "go.mod")); err != nil ||
		!strings.Contains(string(b), "module github.com/suruseas/opossum") {
		t.Fatalf("%s is not the module root, so the walk below would be looking at the wrong tree", root)
	}

	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot find the path of this file, so it cannot be skipped")
	}
	offenders, unreadable, got, walked := scan(root, self)

	if len(unreadable) > 0 {
		t.Fatalf("could not read %v, so nothing can be concluded about them", unreadable)
	}
	if walked == 0 {
		t.Fatalf("the walk found no _test.go files under %s at all", root)
	}

	missing, extra := compare(want, got)
	for _, name := range missing {
		t.Errorf("%s no longer goes through suitedir.Make: a directory it leaves behind carries no "+
			"pid, and no later run can tell whether it may remove it", name)
	}
	for _, name := range extra {
		t.Errorf("%s goes through suitedir.Make and is not in the list here: add it, so that the "+
			"day it stops is a day this fails", name)
	}
	if len(offenders) > 0 {
		t.Errorf("these make an opossum-* entry at the top of $TMPDIR without a pid in the name, so a "+
			"run that dies before its own cleanup leaves it for nobody: %v\n"+
			"Use internal/suitedir.Make instead — it names the directory after this process and removes "+
			"the ones whose maker is gone.", offenders)
	}
}
