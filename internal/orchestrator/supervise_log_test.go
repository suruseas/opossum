package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLines appends n numbered lines and returns the file's size afterwards.
func writeLines(t *testing.T, c *cappedLog, path string, n int) int64 {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := c.Write([]byte(fmt.Sprintf("line %04d: the supervisor restarted something\n", i))); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// The claim the cap makes: however long a crash loop runs, the file stops
// growing. Without a cap this is roughly 270KB a day, forever, for a project the
// user may have forgotten about.
func TestSupervisorLogStopsGrowing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "supervisor.log")
	const max = 4000
	c, err := newCappedLog(path, max)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Far more than the cap: ~46 bytes a line, so 2000 lines is ~90KB into a 4KB file.
	size := writeLines(t, c, path, 2000)
	if size > max {
		t.Errorf("the log grew to %d bytes with a %d-byte cap", size, max)
	}
	if size == 0 {
		t.Error("the log was emptied rather than trimmed — the recent lines are the useful ones")
	}
}

// A trim keeps the newest lines. Two things make this hard to assert honestly:
// the last line written is appended *after* the trim, so it is present whatever
// the trim kept; and the file's final size depends on where in the grow/trim
// cycle the writing stopped. So this catches the trim itself — the write where the
// file gets smaller — and examines what it left behind.
func TestSupervisorLogKeepsTheNewestLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "supervisor.log")
	const max = 2000
	c, err := newCappedLog(path, max)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var afterTrim []byte
	var lastWritten int
	prev := int64(0)
	for i := 0; i < 500 && afterTrim == nil; i++ {
		if _, err := c.Write([]byte(fmt.Sprintf("line %04d: the supervisor restarted something\n", i))); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Size() < prev { // the file just shrank: that was a trim
			afterTrim, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			lastWritten = i
		}
		prev = fi.Size()
	}
	if afterTrim == nil {
		t.Fatal("the log never shrank, so no trim happened — nothing was measured")
	}

	var nums []int
	for _, line := range strings.Split(strings.TrimRight(string(afterTrim), "\n"), "\n") {
		var n int
		if _, err := fmt.Sscanf(line, "line %04d:", &n); err == nil {
			nums = append(nums, n)
		}
	}
	if len(nums) == 0 {
		t.Fatalf("the trim kept no records at all — the recent lines are the useful ones. Log:\n%s", afterTrim)
	}
	if last := nums[len(nums)-1]; last != lastWritten {
		t.Errorf("the newest record should be the one just written (%d), got %d — the trim kept "+
			"the wrong end", lastWritten, last)
	}
	for i := 1; i < len(nums); i++ {
		if nums[i] != nums[i-1]+1 {
			t.Fatalf("the kept records are not one unbroken run: %d follows %d", nums[i], nums[i-1])
		}
	}
	// About half, not a token few — measured on the trim itself, so this is not
	// sensitive to where the writing stopped.
	if kept := int64(len(afterTrim)); kept < max/3 {
		t.Errorf("a trim should keep about half the cap, it kept %d bytes of %d", kept, max)
	}
	if nums[0] == 0 {
		t.Error("the oldest record survived, so nothing was actually dropped")
	}
}

// The trim says it happened. A log that starts mid-story otherwise reads like
// something went wrong at the top.
func TestSupervisorLogSaysItWasTrimmed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "supervisor.log")
	c, err := newCappedLog(path, 2000)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	writeLines(t, c, path, 500)

	body, _ := os.ReadFile(path)
	first, _, _ := strings.Cut(string(body), "\n")
	if !strings.Contains(first, string(codeSupervisorLogTrimmed)) {
		t.Errorf("the first line should explain the gap with its diagnostic code, got %q", first)
	}
	if !strings.Contains(first, "trimmed") {
		t.Errorf("the note should say what happened in words too, got %q", first)
	}
}

// Trimming cuts at a line boundary. Half a line at the top of a log is
// indistinguishable from a corrupt file.
func TestSupervisorLogTrimsAtALineBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "supervisor.log")
	c, err := newCappedLog(path, 2000)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	writeLines(t, c, path, 500)

	body, _ := os.ReadFile(path)
	for i, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
		if i == 0 {
			continue // the trim note
		}
		if !strings.HasPrefix(line, "line ") {
			t.Fatalf("line %d starts mid-record: %q", i, line)
		}
	}
}

// The cap applies to a file that was already full when this supervisor started —
// a restart must not get a fresh allowance. What shows this is the size *while* it
// writes, not the size at the end: an implementation that counted its own bytes
// instead of reading the file's would let it reach two caps before the first trim.
func TestSupervisorLogBoundsAFileThatWasAlreadyFull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "supervisor.log")
	const max = 2000
	// A log left at the cap by an earlier supervisor.
	prefill := strings.Repeat("an earlier supervisor said something here\n", max/41)
	if err := os.WriteFile(path, []byte(prefill), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := newCappedLog(path, max)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var peak int64
	for i := 0; i < 200; i++ {
		if _, err := c.Write([]byte(fmt.Sprintf("line %04d: the supervisor restarted something\n", i))); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Size() > peak {
			peak = fi.Size()
		}
	}
	// One line of slack: the check happens after the write that crosses the line.
	if peak > max+64 {
		t.Errorf("the log reached %d bytes against a %d-byte cap — a restarted supervisor "+
			"counted from zero against a file that was already full", peak, max)
	}
}

// opossum must never write to a file it is not going to keep writing to. If the
// log is replaced under the supervisor — deleted, rotated by something else — the
// handle it holds points at the old file. Writing a trim note to the *path* at
// that point creates the worst state available: a file that looks like a live log,
// carrying opossum's own note, that opossum will never touch again.
func TestSupervisorLogNeverWritesToAFileItHasStoppedUsing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "supervisor.log")
	const max = 2000
	c, err := newCappedLog(path, max)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	writeLines(t, c, path, 20)

	// A second name for the file the supervisor actually holds, so it stays readable
	// after the path is pointed elsewhere.
	peek := filepath.Join(dir, "peek.log")
	if err := os.Link(path, peek); err != nil {
		t.Fatal(err)
	}

	// Something replaces the log: the path now holds a different file, and the
	// supervisor's handle refers to the old one.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// Bigger than the cap and many lines: a one-line placeholder would leave a trim
	// that read the path with nothing to keep, and the copy this guards against
	// would not happen.
	placeholder := strings.Repeat("SOMETHING-ELSE-WROTE-THIS-LINE\n", max/31+50)
	if err := os.WriteFile(path, []byte(placeholder), 0o644); err != nil {
		t.Fatal(err)
	}

	// Enough writes to cross the cap and force a trim.
	for i := 0; i < 200; i++ {
		if _, err := c.Write([]byte(fmt.Sprintf("after-replace %04d: still restarting\n", i))); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != placeholder {
		t.Errorf("the file at %s is no longer the one the supervisor writes to, so opossum must "+
			"leave it alone. It now holds:\n%s", filepath.Base(path), firstBytes(body, 200))
	}

	// …and the other direction, which the check above cannot see: nothing from the
	// replacement may end up in the log the supervisor is actually writing, and the
	// cap still has to hold there. Reading the path would satisfy the assertion
	// above while copying a stranger's bytes into this log and blowing the bound.
	mine, err := os.ReadFile(peek)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mine), "SOMETHING-ELSE-WROTE-THIS-LINE") {
		t.Errorf("the replacement's contents were copied into the supervisor's own log:\n%s",
			firstBytes(mine, 300))
	}
	if int64(len(mine)) > max {
		t.Errorf("the supervisor's own log is %d bytes against a %d-byte cap — the bound is not "+
			"unconditional after all", len(mine), max)
	}
	if !strings.Contains(string(mine), "after-replace") {
		t.Errorf("the supervisor's own history was lost, the log holds:\n%s", firstBytes(mine, 300))
	}
}

func firstBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
