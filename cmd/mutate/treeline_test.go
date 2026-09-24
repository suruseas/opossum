package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// What the sweep looked at. Everything else in the report can be worked out
// from the rows; which tree they were measured on cannot, and a worktree
// left on yesterday's commit — or one with edits in it — produces the same
// table as the right one. That happened: a sweep was run against a stale
// `origin/main` and reported a guard as missing that had already landed.
//
// Read from git at the moment of the report, not from a flag: a value the
// author types is a value the author can be wrong about, which is the thing
// this line exists to catch.
func TestTheReportSaysWhichTreeItMeasured(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "first")

	head := strings.TrimSpace(gitMust(t, dir, "rev-parse", "--short", "HEAD"))

	t.Run("a clean tree is named and called clean", func(t *testing.T) {
		got := treeLine(dir)
		if !strings.Contains(got, head) {
			t.Errorf("treeLine = %q, want the commit %s in it", got, head)
		}
		if !strings.Contains(got, "clean") {
			t.Errorf("treeLine = %q, want it to say the tree was clean", got)
		}
	})

	t.Run("two edited paths are counted as two", func(t *testing.T) {
		// One path and "some paths" read the same when the count is always
		// one: the number has to move with what is there.
		for _, name := range []string{"a.txt", "b.txt"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("edited\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		run("add", "-A")
		run("commit", "-qm", "two files")
		for _, name := range []string{"a.txt", "b.txt"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("again\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := treeLine(dir); !strings.Contains(got, "2 uncommitted") {
			t.Errorf("treeLine = %q with two edited files, want the count to say 2", got)
		}
		run("checkout", "--", ".")
	})

	t.Run("a file git has never seen still counts", func(t *testing.T) {
		// Work in progress is as often a new file as an edited one. A
		// count that asks git to leave untracked files out calls that tree
		// clean, and the line then says the sweep read a tree it did not.
		newFile := filepath.Join(dir, "untracked.txt")
		if err := os.WriteFile(newFile, []byte("new\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(newFile)
		got := treeLine(dir)
		if strings.Contains(got, "clean") {
			t.Errorf("treeLine = %q with an untracked file present, want it counted", got)
		}
		if !strings.Contains(got, "1 uncommitted") {
			t.Errorf("treeLine = %q, want the untracked file in the count", got)
		}
	})

	t.Run("an edited tree says how much is uncommitted", func(t *testing.T) {
		// The count, not the word alone: "has changes" beside a report is a
		// warning, and a number is something the author can compare with
		// what they meant to have open.
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := treeLine(dir)
		if strings.Contains(got, "clean") {
			t.Errorf("treeLine = %q, want it to say the tree has uncommitted work", got)
		}
		if !strings.Contains(got, "1 uncommitted") {
			t.Errorf("treeLine = %q, want the number of paths in it", got)
		}
	})

	t.Run("a directory git cannot answer for says so rather than stopping", func(t *testing.T) {
		// A sweep is still worth reading where git has nothing to say. What
		// it must not do is print a line that looks like an answer.
		got := treeLine(t.TempDir())
		if strings.Contains(got, "clean") {
			t.Errorf("treeLine = %q outside a repository, want it to say git could not "+
				"identify the tree", got)
		}
		if !strings.Contains(got, "could not identify") {
			t.Errorf("treeLine = %q, want it to say why there is no commit", got)
		}
	})
}

func gitMust(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// The whole command, not the pieces. A control that survives is the sweep
// working, and the command has to leave by the status that says so — the
// table can be right about it while the exit status says the opposite, and
// a sweep in CI is read by its status.
func TestASweepWhoseControlSurvivedIsASuccess(t *testing.T) {
	mod := throwawayModule(t)
	sweep := `[` +
		`{"name":"the answer changes","file":"m.go",` +
		`"from":"func Answer() int { return 42 }","to":"func Answer() int { return 43 }",` +
		`"packages":["./..."]},` +
		`{"name":"a comment","file":"m.go",` +
		`"from":"package m","to":"// unchanged\npackage m",` +
		`"packages":["./..."],"control":true}` +
		`]`
	var out, errOut bytes.Buffer
	code := runIn(t, mod, []string{spec(t, sweep)}, &out, &errOut)

	t.Run("the status is a success", func(t *testing.T) {
		if code != exitAllCaught {
			t.Errorf("exit = %d, want %d — the defect was caught and the control survived, "+
				"which is every row doing what it was written to do.\nstdout:\n%s\nstderr:\n%s",
				code, exitAllCaught, out.String(), errOut.String())
		}
	})
	t.Run("the first line says which tree was measured", func(t *testing.T) {
		// Through the command, because the line is assembled there: a
		// function that returns the right string and is never printed
		// reads the same as one that is.
		first, _, _ := strings.Cut(out.String(), "\n")
		if !strings.HasPrefix(first, "Measured ") {
			t.Errorf("the first line of the report is %q, want it to name the tree", first)
		}
	})
	t.Run("the control is not counted among the mutations", func(t *testing.T) {
		if want := "**1 mutation: 1 caught.**"; !strings.Contains(out.String(), want) {
			t.Errorf("the report does not say %q:\n%s", want, out.String())
		}
	})
}

// runIn runs the command with the working directory set to dir, so that the
// tree it reports on is the throwaway module rather than this repository.
func runIn(t *testing.T, dir string, args []string, out, errOut *bytes.Buffer) int {
	t.Helper()
	was, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(was); err != nil {
			t.Fatal(err)
		}
	}()
	return run(args, out, errOut, nil, func(int) {})
}
