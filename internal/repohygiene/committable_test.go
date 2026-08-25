package repohygiene_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The commit hook, run against trees it is supposed to have an opinion about.
// A hook nobody runs is a hook that can stop working without anyone noticing:
// it lives outside the build, so nothing else here would fail if it started
// letting everything through — and letting everything through is what a broken
// shell script does.
func TestTheCommitHookRefusesATreeThatDoesNotBuild(t *testing.T) {
	hook, err := filepath.Abs(filepath.Join(repoRoot(t), ".githooks", "pre-commit"))
	if err != nil {
		t.Fatal(err)
	}
	if info, serr := os.Stat(hook); serr != nil {
		t.Fatalf("the hook is not there: %v", serr)
	} else if info.Mode()&0o111 == 0 {
		t.Errorf("git ignores a hook that is not executable, and says so in a hint nobody reads: %v", info.Mode())
	}

	for _, tc := range []struct {
		name, file, body string
		refuse           bool
		says             string
		absent           []string
		toolSaid         string // a phrase only the tool that found it would use
		absentFile       string // a file the answer must not name
		alsoNeutral      bool   // a file every platform compiles, so the package exists on both
	}{
		{
			name: "a tree with nothing wrong with it",
			file: "ok.go", body: "package probe\n\nfunc Fine() int { return 1 }\n",
		},
		{
			// gofmt runs and cannot parse: the most ordinary way to arrive here
			// is saving a file mid-edit. It is not gofmt failing to run, and
			// saying so would send the reader looking at their toolchain.
			// gofmt runs, reads, and cannot parse. Not "gofmt has something to
			// say about this tree" — `gofmt -w .` does not fix a file that does
			// not parse, and sending the reader to run it leaves them in a loop.
			name: "a tree with a file that will not parse", refuse: true,
			says: "gofmt stopped on this tree", absent: []string{"nothing to say", "gofmt -w"},
			toolSaid: "missing ',' in parameter list",
			file:     "halfwritten.go", body: "package probe\n\nfunc C( int {\n",
		},
		{
			// gofmt-clean on purpose: this has to be the vet arm answering, not
			// the formatting one. A syntax error would have been caught by both,
			// and then this would pass while saying nothing about vet.
			// The two vet arms say almost the same thing, so this names the one
			// that has to answer: the type error is on both platforms, and
			// without the distinction the linux arm alone would satisfy this.
			name: "a tree that does not compile", refuse: true,
			says: "cannot make sense of this tree:", absent: []string{"for linux"},
			toolSaid: "cannot use \"not an int\"",
			file:     "broken.go", body: "package probe\n\nvar n int = \"not an int\"\n",
		},
		{
			// gofmt names the files that need it. A version that answered with
			// every .go file it could find would satisfy a check for "the file
			// is named" — so the tidy sibling has to be absent from the answer.
			name: "a tree that is not formatted", refuse: true, says: "gofmt has something to say",
			absentFile: "tidy.go",
			file:       "unformatted.go", body: "package probe\nfunc  Wonky()  int {\nreturn 1\n}\n",
		},
		{
			// Only linux sees this one, and CI builds for linux.
			name: "a tree that only linux rejects", refuse: true, says: "of this tree for linux:", alsoNeutral: true,
			file: "linuxonly.go", body: "//go:build linux\n\npackage probe\n\nimport \"syscall\"\n\nvar _ = syscall.SYS_KQUEUE\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.alsoNeutral && runtime.GOOS == "linux" {
				// The hook checks this platform and then linux. On linux those
				// are the same check, and the first one answers everything the
				// second could — so a tree only the second sees cannot be built
				// here. The arm exists for the machines this is developed on.
				t.Skip("on linux the cross-platform check is the same check")
			}
			dir := t.TempDir()
			write(t, filepath.Join(dir, "go.mod"), "module probe\n\ngo 1.24\n")
			// Under a subdirectory: the hook is supposed to read the tree, and a
			// version that looked only at the top of it would pass every case
			// here if they all sat at the top.
			if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, "sub", tc.file), tc.body)
			if tc.absentFile != "" {
				write(t, filepath.Join(dir, "sub", tc.absentFile), "package probe\n\nfunc Tidy() int { return 2 }\n")
			}
			if tc.alsoNeutral {
				// Without this the package does not exist on darwin at all, and
				// the check for this platform refuses first — for the wrong
				// reason, with the wrong message.
				write(t, filepath.Join(dir, "sub", "neutral.go"), "package probe\n\nfunc Fine() int { return 1 }\n")
			}

			cmd := exec.Command(hook)
			cmd.Dir = dir
			// GOOS from the parent would decide which arm answers.
			cmd.Env = append(os.Environ(), "GOOS=")
			out, rerr := cmd.CombinedOutput()
			if tc.refuse {
				if rerr == nil {
					t.Errorf("this tree should not be committable:\n%s", out)
				}
				if !strings.Contains(string(out), tc.says) {
					t.Errorf("the reason should be the one that applies — %q is missing from:\n%s", tc.says, out)
				}
				for _, no := range tc.absent {
					if strings.Contains(string(out), no) {
						t.Errorf("this does not belong in the answer to this tree — %q is in:\n%s", no, out)
					}
				}
				// And what the tool said, not only what the hook said about it.
				// The reason this beats finding out from CI in a few minutes is
				// that the reason is right here; a refusal without it is just a
				// faster no.
				if !strings.Contains(string(out), tc.file) {
					t.Errorf("the reader should be told which file — %q is missing from:\n%s", tc.file, out)
				}
				// A file name is not a reason. Whatever the tool actually said
				// has to arrive, or this is a faster no rather than a faster
				// answer — and being the faster answer is the whole case for
				// running it here instead of waiting for CI.
				if tc.toolSaid != "" && !strings.Contains(string(out), tc.toolSaid) {
					t.Errorf("what the tool said should reach the reader — %q is missing from:\n%s", tc.toolSaid, out)
				}
				if tc.absentFile != "" && strings.Contains(string(out), tc.absentFile) {
					t.Errorf("this file is fine, so naming it means the answer is a listing rather than a finding — %q is in:\n%s", tc.absentFile, out)
				}
				return
			}
			if rerr != nil {
				t.Errorf("nothing is wrong with this tree and the hook refused it: %v\n%s", rerr, out)
			}
			if len(out) != 0 {
				t.Errorf("a tree with nothing wrong with it should draw no comment:\n%s", out)
			}
		})
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The check for linux, on any machine. The case above can only exist where this
// platform is not linux — on linux the two checks are the same one, so nothing
// reaches the second that the first lets by — and the merge gate runs on linux.
// That left the arm that exists *for* linux as the one arm CI never exercised.
//
// So the tool is stood in for instead of the tree: a `go` on PATH that answers
// for this platform and refuses for linux. What is being checked is that the
// hook asks twice, asks the second time with GOOS=linux, and says which answer
// it is reporting.
func TestTheCommitHookAsksAboutLinuxToo(t *testing.T) {
	hook := filepath.Join(repoRoot(t), ".githooks", "pre-commit")
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module probe\n\ngo 1.24\n")
	write(t, filepath.Join(dir, "ok.go"), "package probe\n\nfunc Fine() int { return 1 }\n")

	shim := t.TempDir()
	write(t, filepath.Join(shim, "go"), "#!/bin/sh\n"+
		"if [ \"$1\" = vet ] && [ \"$GOOS\" = linux ]; then\n"+
		"\techo 'sub/linuxonly.go:6:9: undefined: syscall.SYS_KQUEUE' >&2\n"+
		"\texit 1\n"+
		"fi\n"+
		"exit 0\n")
	if err := os.Chmod(filepath.Join(shim, "go"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(hook)
	cmd.Dir = dir
	// GOOS cleared: if the parent has one set, the first call would be for linux
	// too and the two answers would stop being distinguishable.
	cmd.Env = append(os.Environ(), "GOOS=", "PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("go said no for linux and the hook let it through:\n%s", out)
	}
	// The answer it reports has to be the one that came back, not the other.
	if !strings.Contains(string(out), "of this tree for linux:") {
		t.Errorf("the refusal should say which platform it is about:\n%s", out)
	}
	if !strings.Contains(string(out), "syscall.SYS_KQUEUE") {
		t.Errorf("what go said should reach the reader:\n%s", out)
	}
}

// gofmt not being there at all, which is not gofmt having something to say. The
// two arrive through the same variable and mean opposite things: one is a tree
// to fix, the other is a check that did not happen — and a check that did not
// happen must not read as a tree with nothing wrong with it.
func TestTheCommitHookSaysWhenGofmtCouldNotRunAtAll(t *testing.T) {
	hook := filepath.Join(repoRoot(t), ".githooks", "pre-commit")
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module probe\n\ngo 1.24\n")
	write(t, filepath.Join(dir, "ok.go"), "package probe\n\nfunc Fine() int { return 1 }\n")

	cmd := exec.Command(hook)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+t.TempDir()) // nothing on it, gofmt included
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("nothing was checked and the hook let the commit through:\n%s", out)
	}
	if !strings.Contains(string(out), "could not run") {
		t.Errorf("a check that did not happen should say so:\n%s", out)
	}
	// And why. "could not run" on its own leaves the reader guessing between a
	// missing toolchain, a broken PATH, and a hook that is simply wrong.
	if !strings.Contains(string(out), "not found") {
		t.Errorf("the reason should arrive with it:\n%s", out)
	}
	if strings.Contains(string(out), "gofmt -w") {
		t.Errorf("formatting the tree does not put gofmt back on PATH:\n%s", out)
	}
}

// gofmt failing with nothing to say. A wrapper that cannot resolve a toolchain
// version does this, and so does a broken install: non-zero, silent. Nothing
// was checked, and the one thing that must not happen is for that to arrive
// looking like a tree with nothing wrong with it.
func TestTheCommitHookRefusesWhenGofmtFailsWithNothingToSay(t *testing.T) {
	hook := filepath.Join(repoRoot(t), ".githooks", "pre-commit")
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module probe\n\ngo 1.24\n")
	write(t, filepath.Join(dir, "ok.go"), "package probe\n\nfunc Fine() int { return 1 }\n")

	shim := t.TempDir()
	write(t, filepath.Join(shim, "gofmt"), "#!/bin/sh\nexit 2\n")
	if err := os.Chmod(filepath.Join(shim, "gofmt"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(hook)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("gofmt failed and this run went through as if the tree were fine:\n%s", out)
	}
	if !strings.Contains(string(out), "nothing to say") {
		t.Errorf("a silent failure is still a failure and should say which:\n%s", out)
	}
}

// A file whose name contains the token the hook cuts on. One call brings both
// streams back through one capture, so the result has to be cut in two again,
// and the cut needs something to cut on. That something is reachable from the
// tree being checked: this name is legal and gofmt quotes it back. What keeps it safe
// is that the marker carries the shell's process id, which a fixture cannot
// know. The name here is built to collide with the marker as it would read
// without the pid: take the pid out and this reports the file as "we" and reads
// part of its name as an exit status. A name that only collided with a shorter
// marker would leave that untested — which is how this test was written first,
// and it passed against a hook with the pid removed.
//
// Cutting by length instead would need substring expansion, which dash does not
// have (`${v:1:2}` is a Bad substitution there), so the marker is the only cut
// available and this is what holds it shut.
// The hook is started through its `#!` line everywhere else here, which means
// `/bin/sh` and nothing else — so the shape that makes other shells agree is
// held by nothing. This runs the same tree through whatever other shells this
// machine has and requires the same answer from each.
//
// It names the shells it found. A machine with only `/bin/sh` checks nothing
// here, and should say so rather than pass quietly.
func TestOtherShellsReadTheHookTheSameWay(t *testing.T) {
	hook := filepath.Join(repoRoot(t), ".githooks", "pre-commit")
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module probe\n\ngo 1.24\n")
	write(t, filepath.Join(dir, "unformatted.go"), "package probe\nfunc  Wonky()  int {\nreturn 1\n}\n")
	write(t, filepath.Join(dir, "halfwritten.go"), "package probe\n\nfunc C( int {\n")

	answer := func(shell string) string {
		t.Helper()
		var cmd *exec.Cmd
		if shell == "" {
			cmd = exec.Command(hook)
		} else {
			cmd = exec.Command(shell, hook)
		}
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOOS=")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("this tree is not committable, and %q passed it:\n%s", shell, out)
		}
		return string(out)
	}

	want := answer("")
	found := []string{}
	for _, shell := range []string{"/bin/dash", "/bin/ksh", "/bin/zsh", "/bin/bash"} {
		if _, err := os.Stat(shell); err != nil {
			continue
		}
		found = append(found, shell)
		if got := answer(shell); got != want {
			t.Errorf("%s reads this differently:\n--- through #! ---\n%s\n--- through %s ---\n%s",
				shell, want, shell, got)
		}
	}
	if len(found) == 0 {
		t.Skip("no other shell to compare against, so nothing here was checked")
	}
	t.Logf("compared against: %s", strings.Join(found, " "))
}

// The marker cannot be a constant, and no fixture can prove that on its own.
// The test below names a file after one particular marker, so it catches the
// one simplification somebody is likely to make and nothing else: change the
// constant and the fixture stops colliding. What actually keeps the cut safe is
// that the marker is not knowable from the tree at all, so that is checked
// where it is written rather than through behaviour.
func TestTheMarkerTheHookCutsOnIsNotAConstant(t *testing.T) {
	hook := filepath.Join(repoRoot(t), ".githooks", "pre-commit")
	body, err := os.ReadFile(hook)
	if err != nil {
		t.Fatal(err)
	}
	line := ""
	for _, l := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "mark=") {
			line = strings.TrimSpace(l)
			break
		}
	}
	if line == "" {
		t.Fatal("no marker is assigned in the hook; if the cut moved, this test has to move with it")
	}
	if !strings.Contains(line, "$$") {
		t.Errorf("the tree being checked can name a file after anything it can predict, and a "+
			"constant marker is predictable — the pid is what it cannot know: %s", line)
	}
}

func TestAFileNamedLikeASeparatorIsStillReportedWhole(t *testing.T) {
	hook := filepath.Join(repoRoot(t), ".githooks", "pre-commit")
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module probe\n\ngo 1.24\n")
	write(t, filepath.Join(dir, "unformatted.go"), "package probe\nfunc  Wonky()  int {\nreturn 1\n}\n")
	// The name goes on the file that does not parse, because the diagnosis is
	// the stream written first and so the one every cut scans through. Put it
	// on the listed file instead and nothing is tested: the listing arrives
	// after both markers, where a collision is never looked at.
	write(t, filepath.Join(dir, "we@@opossum-gofmt@@ird.go"), "package probe\n\nfunc C( int {\n")

	cmd := exec.Command(hook)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("neither of these is committable:\n%s", out)
	}
	if !strings.Contains(string(out), "we@@opossum-gofmt@@ird.go:3:13") {
		t.Errorf("the whole name has to arrive, not the part before the token:\n%s", out)
	}
	if !strings.Contains(string(out), "missing ',' in parameter list") {
		t.Errorf("the half a person has to fix is missing:\n%s", out)
	}
	// And the name has to be under the heading for files a command fixes, not
	// mixed into the diagnosis.
	stopped, rest, ok := strings.Cut(string(out), "gofmt has something to say")
	if !ok {
		t.Fatalf("both headings should be here:\n%s", out)
	}
	if !strings.Contains(stopped, "we@@opossum-gofmt@@ird.go") {
		t.Errorf("this file does not parse; its name belongs under this heading:\n%s", stopped)
	}
	if !strings.Contains(rest, "unformatted.go") {
		t.Errorf("this is the half `gofmt -w .` fixes, and it is not there:\n%s", rest)
	}
	if strings.Contains(rest, "missing ',' in parameter list") {
		t.Errorf("this is not something `gofmt -w .` fixes; it is under the wrong heading:\n%s", rest)
	}
	// And the half a person has to deal with is read first.
	if strings.Index(string(out), "stopped on this tree") > strings.Index(string(out), "has something to say") {
		t.Errorf("the half that needs a person should come before the half that needs a command:\n%s", out)
	}
}

// A tree with both kinds of trouble in it. gofmt names the files it would
// reformat on one stream and the ones it could not take in on the other, and an
// earlier version of this hook reported whichever it looked at first — so the
// half that needs a person, rather than a command, was the half that went
// missing.
func TestBothKindsOfGofmtTroubleAreReported(t *testing.T) {
	hook := filepath.Join(repoRoot(t), ".githooks", "pre-commit")
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module probe\n\ngo 1.24\n")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "sub", "unformatted.go"), "package probe\nfunc  Wonky()  int {\nreturn 1\n}\n")
	write(t, filepath.Join(dir, "sub", "halfwritten.go"), "package probe\n\nfunc C( int {\n")

	cmd := exec.Command(hook)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("neither of these is committable:\n%s", out)
	}
	for _, want := range []string{
		"unformatted.go",                // the one a command fixes
		"missing ',' in parameter list", // the one a person fixes
		"gofmt -w",                      // and the command, for the half it fits
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("both halves have to arrive — %q is missing from:\n%s", want, out)
		}
	}
	// Each heading owns what is under it. Merging the two streams again would
	// put the tidy-but-unformatted file under "stopped on", where it reads as a
	// file that could not be parsed.
	stopped, rest, ok := strings.Cut(string(out), "gofmt has something to say")
	if !ok {
		t.Fatalf("both headings should be here:\n%s", out)
	}
	if strings.Contains(stopped, "unformatted.go") {
		t.Errorf("this file parses; it is under the wrong heading:\n%s", stopped)
	}
	if strings.Contains(rest, "missing ',' in parameter list") {
		t.Errorf("this is not something `gofmt -w .` fixes; it is under the wrong heading:\n%s", rest)
	}
	// And the half a person has to deal with is read first.
	if strings.Index(string(out), "stopped on this tree") > strings.Index(string(out), "has something to say") {
		t.Errorf("the half that needs a person should come before the half that needs a command:\n%s", out)
	}
}

// gofmt saying something while succeeding — a wrapper fetching a toolchain, for
// one. Nothing is wrong with the tree, and a hook that reads any noise on that
// stream as trouble would stop every commit on such a machine.
func TestGofmtTalkingWhileSucceedingIsNotTrouble(t *testing.T) {
	hook := filepath.Join(repoRoot(t), ".githooks", "pre-commit")
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module probe\n\ngo 1.24\n")
	write(t, filepath.Join(dir, "ok.go"), "package probe\n\nfunc Fine() int { return 1 }\n")

	shim := t.TempDir()
	write(t, filepath.Join(shim, "gofmt"), "#!/bin/sh\necho 'go: downloading a toolchain' >&2\nexit 0\n")
	if err := os.Chmod(filepath.Join(shim, "gofmt"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(hook)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=", "PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("gofmt said something and then said the tree was fine; that is fine: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "stopped on this tree") {
		t.Errorf("nothing stopped; saying so would send the reader looking for a fault that is not there:\n%s", out)
	}
}

// And nothing is left behind. An earlier version wrote gofmt's second stream to
// a file in TMPDIR and removed it afterwards with `rm` — which is not there in
// the one case that matters, so the runs that could say the least were also the
// ones that left the most.
func TestTheHookLeavesNothingInTMPDIR(t *testing.T) {
	hook := filepath.Join(repoRoot(t), ".githooks", "pre-commit")
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module probe\n\ngo 1.24\n")
	write(t, filepath.Join(dir, "ok.go"), "package probe\n\nfunc Fine() int { return 1 }\n")

	// The paths that refuse, not only the ones that pass: an earlier version wrote
	// its file on every run and removed it at the end, so the runs that ended
	// early were the ones that left something.
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	for _, tc := range []struct{ name, file, body, path string }{
		{name: "nothing wrong", path: os.Getenv("PATH")},
		{name: "unformatted", file: "u.go", body: "package probe\nfunc  W()  int {\nreturn 1\n}\n", path: os.Getenv("PATH")},
		{name: "will not parse", file: "h.go", body: "package probe\n\nfunc C( int {\n", path: os.Getenv("PATH")},
		{name: "no gofmt", path: ""},
	} {
		if tc.file != "" {
			write(t, filepath.Join(dir, "sub", tc.file), tc.body)
		}
		cmd := exec.Command(hook)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOOS=", "TMPDIR="+tmp, "PATH="+tc.path)
		_, _ = cmd.CombinedOutput() // either answer is fine; what is left over is not
		if tc.file != "" {
			if err := os.Remove(filepath.Join(dir, "sub", tc.file)); err != nil {
				t.Fatal(err)
			}
		}
	}
	left, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		names := make([]string, 0, len(left))
		for _, e := range left {
			names = append(names, e.Name())
		}
		t.Errorf("the hook left files behind: %v", names)
	}
}

// gofmt failing does not mean the tree is clean, and it does not mean gofmt had
// nothing to say. One call brings back both halves whatever the status, so a
// refusal that reads "failed with nothing to say" while a diagnosis and a
// listing are sitting in hand is simply wrong.
//
// This used to be three tests about two calls disagreeing — which one failed,
// which one was believed, which one the sentence named. One call cannot
// disagree with itself, so what is left to check is that neither half is
// dropped on the floor.
func TestAGofmtThatFailsWithBothHalvesInHandSaysBoth(t *testing.T) {
	hook := filepath.Join(repoRoot(t), ".githooks", "pre-commit")
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module probe\n\ngo 1.24\n")

	shim := t.TempDir()
	write(t, filepath.Join(shim, "gofmt"), "#!/bin/sh\n"+
		"echo 'listed.go'\n"+
		"echo 'half.go:3:13: missing '\\'','\\'' in parameter list' >&2\n"+
		"exit 2\n")
	if err := os.Chmod(filepath.Join(shim, "gofmt"), 0o755); err != nil {
		t.Fatal(err)
	}
	real, err := exec.LookPath("go")
	if err != nil {
		t.Fatal("go is not on PATH")
	}
	if err := os.Symlink(real, filepath.Join(shim, "go")); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(hook)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=", "PATH="+shim+string(os.PathListSeparator)+"/usr/bin:/bin")
	out, rerr := cmd.CombinedOutput()
	if rerr == nil {
		t.Errorf("gofmt exited 2 and this passed anyway:\n%s", out)
	}
	for _, want := range []string{
		"missing ',' in parameter list",
		"listed.go",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("this was in hand and is not here — %q:\n%s", want, out)
		}
	}
	if strings.Contains(string(out), "nothing to say") {
		t.Errorf("it had both halves; this is the sentence for having neither:\n%s", out)
	}
	// Each under its own heading, which is the reason the streams are kept
	// apart at all.
	stopped, rest, ok := strings.Cut(string(out), "gofmt has something to say")
	if !ok {
		t.Fatalf("both headings should be here:\n%s", out)
	}
	if strings.Contains(stopped, "listed.go") {
		t.Errorf("this is the half a command fixes; it is under the wrong heading:\n%s", stopped)
	}
	if strings.Contains(rest, "missing ',' in parameter list") {
		t.Errorf("this is not something `gofmt -w .` fixes:\n%s", rest)
	}
}

// 126 and up is the shell saying it could not run the thing. Sending the reader
// at their own source when nothing was ever parsed wastes the worst minutes of
// their day.
func TestAGofmtThatCannotBeExecutedIsNotAParseProblem(t *testing.T) {
	real, err := exec.LookPath("go")
	if err != nil {
		t.Fatal("go is not on PATH")
	}
	for _, tc := range []struct{ name, status, says string }{
		{"not executable", "126", ""},
		{"not found", "127", ""},
		{"and with a message of its own", "126", "sh: gofmt: Permission denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hook := filepath.Join(repoRoot(t), ".githooks", "pre-commit")
			dir := t.TempDir()
			write(t, filepath.Join(dir, "go.mod"), "module probe\n\ngo 1.24\n")

			shim := t.TempDir()
			body := "#!/bin/sh\n"
			if tc.says != "" {
				body += "echo '" + tc.says + "' >&2\n"
			}
			write(t, filepath.Join(shim, "gofmt"), body+"exit "+tc.status+"\n")
			if err := os.Chmod(filepath.Join(shim, "gofmt"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, filepath.Join(shim, "go")); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command(hook)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GOOS=", "PATH="+shim+string(os.PathListSeparator)+"/usr/bin:/bin")
			out, rerr := cmd.CombinedOutput()
			if rerr == nil {
				t.Errorf("a gofmt that cannot be executed is not a clean tree, and this passed anyway:\n%s", out)
			}
			if !strings.Contains(string(out), "gofmt could not run at all (exit "+tc.status+")") {
				t.Errorf("126 and up is the shell saying it could not run the thing, said:\n%s", out)
			}
			if strings.Contains(string(out), "this has to parse") {
				t.Errorf("nothing was parsed; telling the reader to make it readable sends them at the "+
					"wrong thing entirely:\n%s", out)
			}
			if !strings.Contains(string(out), "nothing here was checked") {
				t.Errorf("a tree nothing looked at should not read as a tree that passed:\n%s", out)
			}
			// A message it did have has to come out; one it never had must not
			// be invented.
			if tc.says != "" && !strings.Contains(string(out), tc.says) {
				t.Errorf("it said this, and the hook dropped it:\n%s", out)
			}
		})
	}
}
