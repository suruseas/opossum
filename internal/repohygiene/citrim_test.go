package repohygiene_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// The last step of each CI job trims the Go build cache when it has grown
// past a size (.github/scripts/ci-trim-cache.sh). The two runners share one
// machine, so the other job may be compiling while this one trims, and the
// step must never be the reason a job is red. These tests run the script
// against a cache directory of their own. How big the cache is, and how much
// room the watched drive has, come from a `du` and a `df` put first on the
// script's PATH (fakeDu, fakeDf), so that nothing depends on this machine's
// disk and a cache of 58 GiB costs nothing to make; the files it would delete
// are real, at known ages. Only the size of the cache trims; the free space of
// the drive under the runners' virtual disk only warns.

const gib = 1048576 // KiB

type trimFixture struct {
	cache string
	lock  string
	bin   string
	env   []string
}

func newTrimFixture(t *testing.T) *trimFixture {
	t.Helper()
	if _, err := exec.LookPath("flock"); err != nil {
		if runtime.GOOS == "linux" {
			t.Fatalf("flock is not installed on this Linux machine; the CI runners and the sieve both carry it: %v", err)
		}
		t.Skip("flock is not installed on this machine (macOS has none); the sieve and CI run this on Linux")
	}
	dir := t.TempDir()
	f := &trimFixture{cache: filepath.Join(dir, "go-build"), lock: filepath.Join(dir, "trim.lock"), bin: filepath.Join(dir, "bin")}
	if err := os.MkdirAll(f.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.cache, 0o755); err != nil {
		t.Fatal(err)
	}
	// The script's own defaults (20 GiB, 20 GiB, 180 minutes) are what ci.yml
	// runs, except the watched path, which ci.yml sets: the tests set none of
	// them unless the test is about one.
	f.env = os.Environ()
	for _, k := range []string{"OPOSSUM_CI_MAX_CACHE_GIB", "OPOSSUM_CI_WARN_FREE_GIB", "OPOSSUM_CI_WARN_FREE_PATH", "OPOSSUM_CI_WARN_DF_TIMEOUT_SEC", "OPOSSUM_CI_TRIM_MIN_AGE_MIN"} {
		f.env = withoutKey(f.env, k)
	}
	writeFakeDf(t, f.bin, fmt.Sprint(500*gib))
	f.env = append(f.env,
		"PATH="+f.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GOCACHE="+f.cache,
		"GOMODCACHE="+filepath.Join(dir, "mod"),
		"OPOSSUM_CI_TRIM_LOCK="+f.lock,
	)
	return f
}

func withoutKey(env []string, key string) []string {
	var out []string
	for _, e := range env {
		if !strings.HasPrefix(e, key+"=") {
			out = append(out, e)
		}
	}
	return out
}

// put writes a file `age` old at rel under dir.
func put(t *testing.T, dir, rel string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
	return p
}

// putTimes writes a file at rel under dir with its access time and its
// modification time apart: `put` gives them the same value, which is a file
// no test of "which of the two is read" can tell from either.
func putTimes(t *testing.T, dir, rel string, atimeAge, mtimeAge time.Duration) string {
	t.Helper()
	p := put(t, dir, rel, 0)
	now := time.Now()
	if err := os.Chtimes(p, now.Add(-atimeAge), now.Add(-mtimeAge)); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// writeFake writes a `tool` into bin that logs its arguments to bin/<tool>.args
// and prints out(value, lastArg) for its 1st, 2nd... call (the last one repeats;
// "" is a tool that prints nothing). It uses shell builtins only, so it also
// works on a PATH with nothing else on it.
func writeFake(t *testing.T, bin, tool string, out func(v string) string, values ...string) {
	t.Helper()
	var cases strings.Builder
	for i, v := range values {
		line := ":"
		if v != "" {
			line = out(v)
		}
		fmt.Fprintf(&cases, "%d) %s;;\n", i, line)
	}
	count := filepath.Join(bin, tool+".count")
	// The count is a file that a call reads, adds one to and writes back, which is not one step: a call that reads it while another has truncated it
	// reads nothing, matches no case, and prints nothing — a cache whose size could not be read, which the script takes as not wanting a trim — and four
	// scripts at once do that to about one call in eight (1600 calls on four loops: 215 printed nothing, #1756). So a tool with one answer, which is the
	// same for every call, keeps no count.
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> '" + filepath.Join(bin, tool+".args") + "'\n" +
		"for last; do :; done\n"
	if len(values) == 1 {
		script += "n=0\n"
	} else {
		script += "n=0; [ -f '" + count + "' ] && read n < '" + count + "'\n" +
			"echo $((n+1)) > '" + count + "'\n" +
			"[ \"$n\" -ge " + fmt.Sprint(len(values)) + " ] && n=" + fmt.Sprint(len(values)-1) + "\n"
	}
	script += "case $n in\n" + cases.String() + "esac\n"
	_ = os.Remove(count)
	if err := os.WriteFile(filepath.Join(bin, tool), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// du answers `<KiB>\t<last argument>`.
func writeFakeDu(t *testing.T, bin string, sizesKiB ...string) {
	t.Helper()
	writeFake(t, bin, "du", func(v string) string { return `printf '%s\t%s\n' ` + v + ` "$last"` }, sizesKiB...)
}

// df answers the POSIX format with the given Available column (KiB). Its Size
// column is enormous and its Used column is a different number, so a script
// that reads the wrong column or the wrong unit gets an answer that is wrong
// in a known direction.
func writeFakeDf(t *testing.T, bin string, availKiB ...string) {
	t.Helper()
	writeFake(t, bin, "df", func(v string) string {
		return "echo 'Filesystem 1024-blocks Used Available Capacity Mounted on'; echo 'fake 999999999999 7 " + v + " 1% /'"
	}, availKiB...)
}

func strs(ns []int) []string {
	var out []string
	for _, n := range ns {
		out = append(out, fmt.Sprint(n))
	}
	return out
}

// fakeDu makes the script see a cache of the given sizes (KiB) on its 1st,
// 2nd... look, and returns the file the arguments of du's calls are logged to.
func fakeDu(t *testing.T, f *trimFixture, sizesKiB ...int) (argsFile string) {
	t.Helper()
	writeFakeDu(t, f.bin, strs(sizesKiB)...)
	return filepath.Join(f.bin, "du.args")
}

// fakeDf makes the script see that much free space (KiB) on its 1st, 2nd...
// look, and returns the file the arguments of df's calls are logged to.
func fakeDf(t *testing.T, f *trimFixture, availKiB ...int) (argsFile string) {
	t.Helper()
	writeFakeDf(t, f.bin, strs(availKiB)...)
	return filepath.Join(f.bin, "df.args")
}

// big is a cache of 58 GiB, what the runner measured.
const big = 58 * gib

func (f *trimFixture) run(t *testing.T) (string, int) {
	t.Helper()
	// A script that waits for a lock it was told not to wait for would hang
	// the whole package until go test's own timeout; bounded here, it is a
	// failure of the subtest that says so.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", filepath.Join(repoRoot(t), ".github", "scripts", "ci-trim-cache.sh"))
	cmd.Env = f.env
	// The context kills the `sh`, not the `flock` it started, and a Wait that
	// is reading a pipe the grandchild still holds open never returns without
	// a WaitDelay.
	cmd.WaitDelay = 2 * time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	rc := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running the script: %v", err)
		}
		rc = ee.ExitCode()
		if ctx.Err() != nil {
			t.Errorf("the script did not finish in 20 seconds (a wait for the lock, or worse)\n%s", out.String())
		}
	}
	return out.String(), rc
}

func TestTheTrimStepKeepsItsPromises(t *testing.T) {
	// One subtest per guard: each is a line of the script that, taken out,
	// leaves exactly this subtest red.

	t.Run("a big cache: old files go, recent ones and every directory stay", func(t *testing.T) {
		f := newTrimFixture(t)
		fakeDu(t, f, big)
		old := put(t, f.cache, "ab/old-a", 5*time.Hour)
		old2 := put(t, f.cache, "cd/old-d", 200*time.Minute)
		recent := put(t, f.cache, "ab/recent-a", 2*time.Minute)
		emptied := filepath.Join(f.cache, "ef")
		put(t, f.cache, "ef/only-old", 9*time.Hour)
		out, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc %d\n%s", rc, out)
		}
		for _, p := range []string{old, old2, filepath.Join(emptied, "only-old")} {
			if exists(p) {
				t.Errorf("%s is 3+ hours old in a big cache and is still there\n%s", p, out)
			}
		}
		if !exists(recent) {
			t.Errorf("a file used two minutes ago was deleted: a running job may be reading it\n%s", out)
		}
		// A directory a running `go build` is about to write into: removed
		// (as `go clean -cache` does), the build fails on its next write.
		for _, d := range []string{"ab", "cd", "ef"} {
			if fi, err := os.Stat(filepath.Join(f.cache, d)); err != nil || !fi.IsDir() {
				t.Errorf("the cache subdirectory %s is gone after the trim (%v); a build running beside it fails on its next write there\n%s", d, err, out)
			}
		}
	})

	t.Run("the age line is 180 minutes: 170 stays, 190 goes", func(t *testing.T) {
		f := newTrimFixture(t)
		fakeDu(t, f, big)
		young := put(t, f.cache, "ab/young", 170*time.Minute)
		old := put(t, f.cache, "ab/old", 190*time.Minute)
		out, rc := f.run(t)
		if rc != 0 || !exists(young) || exists(old) {
			t.Errorf("rc %d, 170-minute file kept %v, 190-minute file kept %v; the go command refreshes a used file's mtime hourly, and a job is far shorter than the margin\n%s", rc, exists(young), exists(old), out)
		}
	})

	t.Run("the age is the mtime, not the atime", func(t *testing.T) {
		f := newTrimFixture(t)
		fakeDu(t, f, big)
		// atime says "just used", mtime says "5 hours ago": goes. The reverse
		// stays. (Under WSL the atime cannot be relied on, and the go command
		// writes the mtime when it uses a file.)
		oldMtime := putTimes(t, f.cache, "ab/old-mtime", time.Minute, 5*time.Hour)
		oldAtime := putTimes(t, f.cache, "ab/old-atime", 5*time.Hour, time.Minute)
		out, rc := f.run(t)
		if rc != 0 || exists(oldMtime) || !exists(oldAtime) {
			t.Errorf("rc %d, old-mtime file kept %v (want gone), old-atime file kept %v (want kept)\n%s", rc, exists(oldMtime), exists(oldAtime), out)
		}
	})

	t.Run("the size limit is read from the environment", func(t *testing.T) {
		for _, c := range []struct {
			name string
			du   int
			trim bool
		}{{"25 GiB under a limit of 30", 25 * gib, false}, {"31 GiB over a limit of 30", 31 * gib, true}} {
			f := newTrimFixture(t)
			fakeDu(t, f, c.du)
			f.env = append(f.env, "OPOSSUM_CI_MAX_CACHE_GIB=30")
			p := put(t, f.cache, "ab/old", 5*time.Hour)
			out, rc := f.run(t)
			if rc != 0 || exists(p) == c.trim {
				t.Errorf("%s: rc %d, old file kept %v, want trimmed %v\n%s", c.name, rc, exists(p), c.trim, out)
			}
		}
	})

	t.Run("the age is read from the environment", func(t *testing.T) {
		f := newTrimFixture(t)
		fakeDu(t, f, big)
		f.env = append(f.env, "OPOSSUM_CI_TRIM_MIN_AGE_MIN=60")
		young := put(t, f.cache, "ab/young", 30*time.Minute)
		old := put(t, f.cache, "ab/old", 90*time.Minute)
		if out, rc := f.run(t); rc != 0 || !exists(young) || exists(old) {
			t.Errorf("an age of 60 minutes: rc %d, 30-minute file kept %v (want kept), 90-minute file kept %v (want gone)\n%s", rc, exists(young), exists(old), out)
		}
	})

	t.Run("the top level of the cache is not a cache entry", func(t *testing.T) {
		f := newTrimFixture(t)
		fakeDu(t, f, big)
		readme := put(t, f.cache, "README", 30*24*time.Hour)
		logf := put(t, f.cache, "log.txt", 30*24*time.Hour)
		if out, rc := f.run(t); rc != 0 || !exists(readme) || !exists(logf) {
			t.Errorf("rc %d, README kept %v, log.txt kept %v\n%s", rc, exists(readme), exists(logf), out)
		}
	})

	t.Run("a small cache trims nothing, and does not open the lock", func(t *testing.T) {
		f := newTrimFixture(t)
		fakeDu(t, f, 3*gib)
		p := put(t, f.cache, "ab/old", 30*24*time.Hour)
		out, rc := f.run(t)
		if rc != 0 || !exists(p) {
			t.Errorf("rc %d, old file kept %v; the cache is under the limit\n%s", rc, exists(p), out)
		}
		// Not even the lock: a job with nothing to do does not queue behind
		// (or make the others skip because of) a trim it has no reason to run.
		if exists(f.lock) {
			t.Errorf("the lock file %s was opened by a job with nothing to trim\n%s", f.lock, out)
		}
	})

	t.Run("the limit is 20 GiB compared in KiB: 21 and 20 GiB + 1 KiB trim, 20 and 19 do not", func(t *testing.T) {
		for _, c := range []struct {
			name string
			kib  int
			trim bool
		}{{"21 GiB", 21*gib + 1000, true}, {"20 GiB and 1 KiB", 20*gib + 1, true}, {"exactly 20 GiB", 20 * gib, false}, {"19 GiB", 19 * gib, false}} {
			f := newTrimFixture(t)
			p := put(t, f.cache, "ab/old", 5*time.Hour)
			fakeDu(t, f, c.kib)
			out, rc := f.run(t)
			if rc != 0 || exists(p) == c.trim {
				t.Errorf("%s: rc %d, old file kept %v, want trimmed %v\n%s", c.name, rc, exists(p), c.trim, out)
			}
			// Under the limit is decided before the lock: the second look under
			// the lock would also say "nothing to do", so only the lock file
			// tells the two apart.
			if !c.trim && exists(f.lock) {
				t.Errorf("%s: the lock was opened though the cache is not above the limit\n%s", c.name, out)
			}
		}
	})

	t.Run("du is asked about the cache itself, in KiB", func(t *testing.T) {
		f := newTrimFixture(t)
		put(t, f.cache, "ab/old", 5*time.Hour)
		args := fakeDu(t, f, big)
		f.run(t)
		b, err := os.ReadFile(args)
		if err != nil {
			t.Fatalf("du was never called: %v", err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if line != "-sk "+f.cache {
				t.Errorf("du was called as %q, want %q: another path measures another tree, and without -k the unit is the platform's own", line, "-sk "+f.cache)
			}
		}
	})

	t.Run("the module cache is not touched", func(t *testing.T) {
		f := newTrimFixture(t)
		fakeDu(t, f, big)
		mod := put(t, filepath.Join(filepath.Dir(f.cache), "mod"), "github.com/x/y@v1/f.go", 30*24*time.Hour)
		if out, rc := f.run(t); rc != 0 || !exists(mod) {
			t.Errorf("rc %d, module file kept %v; a build reads these by path and no mtime says whether one is in use\n%s", rc, exists(mod), out)
		}
	})

	t.Run("a lock held by another job: skipped, not waited for, not failed", func(t *testing.T) {
		f := newTrimFixture(t)
		fakeDu(t, f, big)
		p := put(t, f.cache, "ab/old", 5*time.Hour)
		held, err := os.OpenFile(f.lock, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer held.Close()
		if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		out, rc := f.run(t)
		if rc != 0 {
			t.Errorf("rc %d: a held lock is another job trimming, not this job's failure\n%s", rc, out)
		}
		if !exists(p) {
			t.Errorf("the old file was deleted while another process held the lock\n%s", out)
		}
		if !strings.Contains(out, "skipped") {
			t.Errorf("the log does not say the trim was skipped:\n%s", out)
		}
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("the step waited %v for the lock; it must not wait", d)
		}
	})

	t.Run("four at once: every one exits 0 and the old files are gone (the lock decides who trims; this does not claim they overlapped)", func(t *testing.T) {
		f := newTrimFixture(t)
		fakeDu(t, f, big)
		var olds []string
		for i := 0; i < 200; i++ {
			olds = append(olds, put(t, f.cache, fmt.Sprintf("%02x/old-%d", i%16, i), 5*time.Hour))
		}
		recent := put(t, f.cache, "00/recent", time.Minute)
		var wg sync.WaitGroup
		results := make([]string, 4)
		for i := range results {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out, rc := f.run(t)
				results[i] = fmt.Sprintf("rc=%d\n%s", rc, out)
			}()
		}
		wg.Wait()
		for i, r := range results {
			if !strings.HasPrefix(r, "rc=0\n") {
				t.Errorf("run %d: %s", i, r)
			}
		}
		for _, p := range olds {
			if exists(p) {
				t.Fatalf("%s survived four trims\n%v", p, results)
			}
		}
		if !exists(recent) {
			t.Errorf("the recent file went")
		}
	})

	t.Run("a cache another job trimmed while this one waited for the lock is noticed once the lock is ours", func(t *testing.T) {
		f := newTrimFixture(t)
		p := put(t, f.cache, "ab/old", 5*time.Hour)
		// 58 GiB when this job looked, exactly 20 GiB by the time it holds the
		// lock (20 is "not above", the second look's own boundary).
		fakeDu(t, f, big, 20*gib)
		out, rc := f.run(t)
		if rc != 0 || !exists(p) || !strings.Contains(out, "another job trimmed first") {
			t.Errorf("rc %d, old file kept %v\n%s", rc, exists(p), out)
		}
		if !exists(f.lock) {
			t.Errorf("the lock was never opened: the second look is meant to be the one under the lock\n%s", out)
		}
	})

	t.Run("a du that prints nothing: no trim, a warning, no failure", func(t *testing.T) {
		f := newTrimFixture(t)
		p := put(t, f.cache, "ab/old", 5*time.Hour)
		writeFakeDu(t, f.bin, "")
		out, rc := f.run(t)
		if rc != 0 || !exists(p) || !strings.Contains(out, "could not measure") {
			t.Errorf("rc %d, old file kept %v; an unknown size is not a big one\n%s", rc, exists(p), out)
		}
	})

	t.Run("the log says what was freed: after is measured after the trim", func(t *testing.T) {
		f := newTrimFixture(t)
		put(t, f.cache, "ab/old", 5*time.Hour)
		fakeDu(t, f, big, big, 10*gib)
		out, rc := f.run(t)
		if rc != 0 || !strings.Contains(out, "58 GiB -> 10 GiB") || strings.Contains(out, "GiB after the trim") {
			t.Errorf("rc %d; the third look at the cache is the one after the trim, and 10 GiB is under the limit\n%s", rc, out)
		}
	})

	t.Run("exactly 20 GiB after the trim is not still over the limit", func(t *testing.T) {
		f := newTrimFixture(t)
		put(t, f.cache, "ab/old", 5*time.Hour)
		fakeDu(t, f, big, big, 20*gib)
		if out, rc := f.run(t); rc != 0 || strings.Contains(out, "GiB after the trim") {
			t.Errorf("rc %d\n%s", rc, out)
		}
	})

	t.Run("a lock file that cannot be opened: no trim, a warning, no failure", func(t *testing.T) {
		f := newTrimFixture(t)
		fakeDu(t, f, big)
		p := put(t, f.cache, "ab/old", 5*time.Hour)
		f.env = append(f.env, "OPOSSUM_CI_TRIM_LOCK="+filepath.Join(f.cache, "no", "such", "dir", "lock"))
		out, rc := f.run(t)
		if rc != 0 || !exists(p) || !strings.Contains(out, "could not open the lock file") {
			t.Errorf("rc %d, old file kept %v; a plain `exec` with a failing redirection ends dash with status 2\n%s", rc, exists(p), out)
		}
	})

	t.Run("a limit that is not a number: no trim, a warning, no failure", func(t *testing.T) {
		f := newTrimFixture(t)
		fakeDu(t, f, big)
		p := put(t, f.cache, "ab/old", 5*time.Hour)
		f.env = append(f.env, "OPOSSUM_CI_MAX_CACHE_GIB=twenty")
		out, rc := f.run(t)
		if rc != 0 || !exists(p) || !strings.Contains(out, "whole numbers") {
			t.Errorf("rc %d, old file kept %v\n%s", rc, exists(p), out)
		}
	})

	t.Run("a number `$(( ))` would misread is refused too: 08, 020, 8 digits", func(t *testing.T) {
		for _, name := range []string{"OPOSSUM_CI_MAX_CACHE_GIB", "OPOSSUM_CI_WARN_FREE_GIB", "OPOSSUM_CI_WARN_DF_TIMEOUT_SEC", "OPOSSUM_CI_TRIM_MIN_AGE_MIN"} {
			for _, v := range []string{"08", "020", "12345678", "99999999999999"} {
				f := newTrimFixture(t)
				fakeDu(t, f, big)
				p := put(t, f.cache, "ab/old", 5*time.Hour)
				f.env = append(f.env, name+"="+v)
				out, rc := f.run(t)
				if rc != 0 || !exists(p) || !strings.Contains(out, "whole numbers") {
					t.Errorf("%s=%s: rc %d, old file kept %v; dash ends with status 2 on 08, reads 020 as 16, and the multiplication overflows\n%s", name, v, rc, exists(p), out)
				}
			}
		}
	})

	t.Run("a low drive warns and trims nothing", func(t *testing.T) {
		f := newTrimFixture(t)
		fakeDu(t, f, 3*gib)
		fakeDf(t, f, 5*gib)
		drive := t.TempDir()
		f.env = append(f.env, "OPOSSUM_CI_WARN_FREE_PATH="+drive)
		p := put(t, f.cache, "ab/old", 5*time.Hour)
		out, rc := f.run(t)
		if rc != 0 || !exists(p) || !strings.Contains(out, "::warning::ci-trim: only 5 GiB is free on "+drive) || !strings.Contains(out, "does not shrink") {
			t.Errorf("rc %d, old file kept %v; a trim cannot give the drive's space back, so the free space only warns, and says why\n%s", rc, exists(p), out)
		}
		if exists(f.lock) {
			t.Errorf("the lock was opened for a warning\n%s", out)
		}
	})

	t.Run("a low drive does not stop the size from trimming, and does not start it either", func(t *testing.T) {
		f := newTrimFixture(t)
		fakeDu(t, f, big)
		fakeDf(t, f, 5*gib)
		f.env = append(f.env, "OPOSSUM_CI_WARN_FREE_PATH="+t.TempDir())
		p := put(t, f.cache, "ab/old", 5*time.Hour)
		out, rc := f.run(t)
		if rc != 0 || exists(p) || !strings.Contains(out, "is free on") {
			t.Errorf("rc %d, old file kept %v; the cache is 58 GiB and the drive is low: both are said and done\n%s", rc, exists(p), out)
		}
	})

	t.Run("the drive's floor is 20 GiB read from df's KiB: 19 warns, 20 and 21 do not", func(t *testing.T) {
		for _, c := range []struct {
			name string
			kib  int
			warn bool
		}{{"19 GiB", 19 * gib, true}, {"20 GiB less 1 KiB", 20*gib - 1, true}, {"exactly 20 GiB", 20 * gib, false}, {"21 GiB", 21 * gib, false}} {
			f := newTrimFixture(t)
			fakeDu(t, f, 3*gib)
			args := fakeDf(t, f, c.kib)
			drive := t.TempDir()
			f.env = append(f.env, "OPOSSUM_CI_WARN_FREE_PATH="+drive)
			out, rc := f.run(t)
			if got := strings.Contains(out, "is free on"); rc != 0 || got != c.warn {
				t.Errorf("%s free: rc %d, warned %v, want %v\n%s", c.name, rc, got, c.warn, out)
			}
			if b, err := os.ReadFile(args); err != nil || string(b) != "-Pk "+drive+"\n" {
				t.Errorf("%s free: df was called as %q (%v), want one call %q (the drive, in the POSIX format; without -P a long device name wraps the row)", c.name, b, err, "-Pk "+drive)
			}
		}
	})

	t.Run("the drive's floor is read from the environment", func(t *testing.T) {
		for _, c := range []struct {
			name string
			kib  int
			warn bool
		}{{"7 GiB over a floor of 5", 7 * gib, false}, {"4 GiB under a floor of 5", 4 * gib, true}} {
			f := newTrimFixture(t)
			fakeDu(t, f, 3*gib)
			fakeDf(t, f, c.kib)
			f.env = append(f.env, "OPOSSUM_CI_WARN_FREE_PATH="+t.TempDir(), "OPOSSUM_CI_WARN_FREE_GIB=5")
			out, rc := f.run(t)
			if got := strings.Contains(out, "is free on"); rc != 0 || got != c.warn {
				t.Errorf("%s: rc %d, warned %v, want %v\n%s", c.name, rc, got, c.warn, out)
			}
		}
	})

	t.Run("no usable drive to watch (unset, missing, df says nothing): no warning, and the size alone decides", func(t *testing.T) {
		for _, c := range []struct {
			name string
			path func() string
			df   string
		}{
			{"unset", func() string { return "" }, "100"},
			{"not there", func() string { return filepath.Join(t.TempDir(), "no", "such") }, "100"},
			{"a file, not a directory", func() string { return put(t, t.TempDir(), "f", 0) }, "100"},
			{"df prints nothing", func() string { return t.TempDir() }, ""},
		} {
			f := newTrimFixture(t)
			fakeDu(t, f, big)
			writeFakeDf(t, f.bin, c.df)
			if p := c.path(); p != "" {
				f.env = append(f.env, "OPOSSUM_CI_WARN_FREE_PATH="+p)
			}
			old := put(t, f.cache, "ab/old", 5*time.Hour)
			out, rc := f.run(t)
			if rc != 0 || exists(old) || strings.Contains(out, "is free on") {
				t.Errorf("%s: rc %d, old file kept %v (want gone), warned %v (want not)\n%s", c.name, rc, exists(old), strings.Contains(out, "is free on"), out)
			}
		}
	})

	t.Run("the warning comes before everything that can end the run: no cache, a held lock", func(t *testing.T) {
		// no cache directory
		f := newTrimFixture(t)
		fakeDf(t, f, 5*gib)
		f.env = append(f.env, "OPOSSUM_CI_WARN_FREE_PATH="+t.TempDir(), "GOCACHE="+filepath.Join(f.cache, "nope"))
		if out, rc := f.run(t); rc != 0 || !strings.Contains(out, "is free on") || !strings.Contains(out, "no Go cache directory") {
			t.Errorf("no cache: rc %d; the drive is low whether or not there is a cache to trim\n%s", rc, out)
		}
		// a lock another job holds
		f = newTrimFixture(t)
		fakeDu(t, f, big)
		fakeDf(t, f, 5*gib)
		f.env = append(f.env, "OPOSSUM_CI_WARN_FREE_PATH="+t.TempDir())
		held, err := os.OpenFile(f.lock, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer held.Close()
		if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
			t.Fatal(err)
		}
		if out, rc := f.run(t); rc != 0 || !strings.Contains(out, "is free on") || !strings.Contains(out, "skipped") {
			t.Errorf("held lock: rc %d; a job that skips the trim still says the drive is low\n%s", rc, out)
		}
	})

	// A drive that is stuck leaves `df` (and the `test -d` before it) waiting:
	// given up on after the limit, with no warning, and the size decides. The
	// third case runs with the script's own limit (10 seconds), in parallel with
	// the others so that it costs the suite no more than itself.
	for _, c := range []struct {
		name     string
		hang     string
		env      []string
		min, max time.Duration
	}{
		{"df that does not come back, with a limit of 1 second", "df", []string{"OPOSSUM_CI_WARN_DF_TIMEOUT_SEC=1"}, 0, 15 * time.Second},
		{"test -d that does not come back, with a limit of 1 second", "test", []string{"OPOSSUM_CI_WARN_DF_TIMEOUT_SEC=1"}, 0, 15 * time.Second},
		{"df that does not come back, with the default limit (10 seconds)", "df", nil, 8 * time.Second, 16 * time.Second},
	} {
		t.Run("a "+c.name+" is given up on, and the size decides", func(t *testing.T) {
			t.Parallel()
			f := newTrimFixture(t)
			fakeDu(t, f, big)
			// A low drive behind the hung call: given up on in time, the warning
			// is absent; given up on never (a `test -d` the shell runs as a
			// builtin does not reach the hung one), the low drive is what warns.
			fakeDf(t, f, 5*gib)
			// `exec`: the timeout kills this very process, not a shell that left
			// a sleep behind.
			if err := os.WriteFile(filepath.Join(f.bin, c.hang), []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			f.env = append(f.env, "OPOSSUM_CI_WARN_FREE_PATH="+t.TempDir())
			f.env = append(f.env, c.env...)
			old := put(t, f.cache, "ab/old", 5*time.Hour)
			start := time.Now()
			out, rc := f.run(t)
			el := time.Since(start)
			if rc != 0 || exists(old) || strings.Contains(out, "is free on") || el > c.max || el < c.min {
				t.Errorf("rc %d, old file kept %v (want gone), %v elapsed (want %v to %v), warned %v (want not)\n%s", rc, exists(old), el, c.min, c.max, strings.Contains(out, "is free on"), out)
			}
		})
	}

	t.Run("the edges of a whole number are accepted: 0, and 7 digits", func(t *testing.T) {
		f := newTrimFixture(t)
		fakeDu(t, f, big)
		fakeDf(t, f, 5*gib)
		f.env = append(f.env, "OPOSSUM_CI_WARN_FREE_PATH="+t.TempDir(), "OPOSSUM_CI_WARN_FREE_GIB=0", "OPOSSUM_CI_MAX_CACHE_GIB=9999999")
		old := put(t, f.cache, "ab/old", 5*time.Hour)
		out, rc := f.run(t)
		if rc != 0 || !exists(old) || strings.Contains(out, "whole numbers") || strings.Contains(out, "is free on") || !strings.Contains(out, "nothing to trim") {
			t.Errorf("rc %d, old file kept %v; a floor of 0 never warns, a limit of 9999999 GiB never trims, and neither is refused as a number\n%s", rc, exists(old), out)
		}
	})

	t.Run("an age that is not a number: no trim, a warning, no failure", func(t *testing.T) {
		f := newTrimFixture(t)
		fakeDu(t, f, big)
		p := put(t, f.cache, "ab/old", 5*time.Hour)
		f.env = append(f.env, "OPOSSUM_CI_TRIM_MIN_AGE_MIN=abc")
		out, rc := f.run(t)
		if rc != 0 || !exists(p) || !strings.Contains(out, "whole numbers") {
			t.Errorf("rc %d, old file kept %v\n%s", rc, exists(p), out)
		}
	})

	t.Run("what is left is said when the cache is still over the limit", func(t *testing.T) {
		f := newTrimFixture(t)
		fakeDu(t, f, big)
		put(t, f.cache, "ab/fresh", time.Minute)
		out, rc := f.run(t)
		if rc != 0 || !strings.Contains(out, "GiB after the trim") {
			t.Errorf("rc %d; a trim that freed nothing says so, or the next outage is a surprise\n%s", rc, out)
		}
	})

	t.Run("the contents of a -d directory are left alone, and so is the directory", func(t *testing.T) {
		f := newTrimFixture(t)
		fakeDu(t, f, big)
		// `go run` executables: the directory's mtime is what a use refreshes.
		exe := put(t, f.cache, "ab/0123-d/prog", 6*time.Hour)
		dir := filepath.Dir(exe)
		now := time.Now()
		if err := os.Chtimes(dir, now, now); err != nil {
			t.Fatal(err)
		}
		oldDir := filepath.Join(f.cache, "ab", "olddir")
		put(t, f.cache, "ab/olddir/x", 6*time.Hour)
		long := now.Add(-6 * time.Hour)
		if err := os.Chtimes(oldDir, long, long); err != nil {
			t.Fatal(err)
		}
		emptyOld := filepath.Join(f.cache, "ab", "emptyold")
		if err := os.Mkdir(emptyOld, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(emptyOld, long, long); err != nil {
			t.Fatal(err)
		}
		out, rc := f.run(t)
		if rc != 0 || !exists(exe) || !exists(oldDir) || !exists(emptyOld) || !exists(filepath.Join(oldDir, "x")) {
			t.Errorf("rc %d, executable kept %v, old directory kept %v, empty old directory kept %v; only files directly inside a cache subdirectory are entries\n%s", rc, exists(exe), exists(oldDir), exists(emptyOld), out)
		}
	})

	t.Run("no flock on the runner: no trim without a lock, a warning, no failure", func(t *testing.T) {
		f := newTrimFixture(t)
		p := put(t, f.cache, "ab/old", 5*time.Hour)
		bin := t.TempDir()
		writeFakeDu(t, bin, fmt.Sprint(big))
		writeFakeDf(t, bin, fmt.Sprint(500*gib))
		for _, tool := range []string{"awk", "find", "sh", "cat"} {
			src, err := exec.LookPath(tool)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(src, filepath.Join(bin, tool)); err != nil {
				t.Fatal(err)
			}
		}
		f.env = append(f.env, "PATH="+bin)
		out, rc := f.run(t)
		if rc != 0 || !exists(p) || !strings.Contains(out, "flock is not installed") {
			t.Errorf("rc %d, old file kept %v; without flock two jobs could trim at once, so it does not trim, and says why\n%s", rc, exists(p), out)
		}
	})

	t.Run("no cache directory: nothing to do, no failure", func(t *testing.T) {
		f := newTrimFixture(t)
		for _, c := range []string{filepath.Join(f.cache, "nope"), "off"} {
			f.env = append(f.env, "GOCACHE="+c)
			out, rc := f.run(t)
			if rc != 0 || !strings.Contains(out, "no Go cache directory") {
				t.Errorf("GOCACHE=%s: rc %d, and the log should say there is no cache directory\n%s", c, rc, out)
			}
		}
	})

	t.Run("an error while deleting does not fail the job", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can delete from a directory with no write permission")
		}
		f := newTrimFixture(t)
		fakeDu(t, f, big)
		put(t, f.cache, "ab/old", 5*time.Hour)
		if err := os.Chmod(filepath.Join(f.cache, "ab"), 0o555); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(filepath.Join(f.cache, "ab"), 0o755)
		out, rc := f.run(t)
		if rc != 0 {
			t.Errorf("rc %d: the cache could not be trimmed, and that is not a red job\n%s", rc, out)
		}
		if !strings.Contains(out, "find reported errors") {
			t.Errorf("a trim that could not delete says nothing:\n%s", out)
		}
	})
}

// The step is in both jobs, last, and says when it runs; the checks above are
// about the script, this one about it being called.
func TestBothJobsEndWithTheTrim(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := yaml.Unmarshal(b, &top); err != nil {
		t.Fatal(err)
	}
	jobs, _ := top["jobs"].(map[string]any)
	for _, name := range []string{"ci", "stable"} {
		job, _ := jobs[name].(map[string]any)
		steps, _ := job["steps"].([]any)
		if len(steps) == 0 {
			t.Fatalf("%s: no steps", name)
		}
		last, _ := steps[len(steps)-1].(map[string]any)
		if run, _ := last["run"].(string); strings.TrimSpace(run) != "sh .github/scripts/ci-trim-cache.sh" {
			t.Errorf("%s: the last step runs %q, not the cache trim; a step after it is one the trim does not follow", name, run)
		}
		if cond, _ := last["if"].(string); cond != "${{ always() && github.event.repository.private }}" {
			t.Errorf("%s: the trim runs on %q; it runs whatever the steps before it said (a cancelled run is where the cache grows), and only where the runner keeps its own cache", name, cond)
		}
		if _, has := last["continue-on-error"]; has {
			t.Errorf("%s: the trim carries continue-on-error; the script exits 0 on every path, and that key would hide a script that stopped doing so", name)
		}
		env, _ := last["env"].(map[string]any)
		if env["OPOSSUM_CI_MAX_CACHE_GIB"] != "20" || env["OPOSSUM_CI_WARN_FREE_PATH"] != "/mnt/c" || env["OPOSSUM_CI_WARN_FREE_GIB"] != "20" || len(env) != 3 {
			t.Errorf("%s: the trim runs with env %v; the cache limit, the drive to watch and its floor are written here, with their reasons beside them, and nothing else is set", name, env)
		}
	}
}

// A fake tool with one answer gives it to every call, however many run at once: `four at once` runs four scripts that each ask `du`, and a call that was answered
// nothing is a cache whose size could not be read, which the script takes as not wanting a trim, so that "nothing to trim now (another job trimmed first)" was
// said where nobody had trimmed (#1756). The count a call kept in a file, read and written back by each of four, was empty for about one call in eight.
func TestAFakeToolWithOneAnswerAnswersEveryCallWhenFourAskAtOnce(t *testing.T) {
	bin := t.TempDir()
	writeFakeDu(t, bin, "60817408")
	// What makes it so, said without waiting for the race to show: a tool that has one answer keeps no count file at all, and one that has several does.
	one, err := os.ReadFile(filepath.Join(bin, "du"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(one), "du.count") {
		t.Errorf("a fake `du` with one answer keeps a count that four callers read and write at once:\n%s", one)
	}
	several := t.TempDir()
	writeFakeDu(t, several, "60817408", "1")
	if two, err := os.ReadFile(filepath.Join(several, "du")); err != nil || !strings.Contains(string(two), "du.count") {
		t.Errorf("a fake `du` with two answers has to count its calls to give the second: %v\n%s", err, two)
	}
	var wg sync.WaitGroup
	const callers, calls = 4, 120
	unanswered := make([]int, callers)
	for c := 0; c < callers; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < calls; i++ {
				out, err := exec.Command(filepath.Join(bin, "du"), "-sk", "/x").Output()
				if err != nil || !strings.HasPrefix(string(out), "60817408\t") {
					unanswered[c]++
				}
			}
		}()
	}
	wg.Wait()
	if n := unanswered[0] + unanswered[1] + unanswered[2] + unanswered[3]; n != 0 {
		t.Errorf("%d of %d calls of a fake `du` that answers one size were not answered with it when %d callers asked at once", n, callers*calls, callers)
	}
}
