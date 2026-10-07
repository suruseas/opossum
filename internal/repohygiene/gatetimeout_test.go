package repohygiene_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// `go test` stops a package's run at ten minutes unless it is told otherwise. The orchestrator package under -race takes 393 s and 405 s in the two lanes of a
// green CI run, and twice (#1798, #1813) it was stopped at 600 s because the machine the run was on was about four times slower for the whole run, so the gate
// gives `go test` thirty minutes, as GOFLAGS (which `go test` reads and every other go command ignores, so the gate's own lines keep the flags they are pinned
// to). What is asked here is what the child of each target is handed, not how the Makefile says it: a Makefile that said it a different way — an assignment that
// is not exported, one under an `ifndef  CI` or an `else`, one after a line that ends in a backslash — reads right and gives `go test` no timeout, and CI the
// ten minutes it had. A `go` that only records the GOFLAGS it is run with stands in for the toolchain; the recipe's own housekeeping runs as it is.
func TestTheGateHandsGoTestItsTimeoutInCIAndAway(t *testing.T) {
	make, err := exec.LookPath("make")
	if err != nil {
		t.Fatalf("make is not on the PATH, and the gate is run through it: %v", err)
	}
	root := repoRoot(t)
	outer := os.Getenv("TMPDIR") // the suite this runs in works in it: a row that removes it stops the builds that come after
	for _, target := range []string{"test", "test-shipped"} {
		for _, row := range []struct{ ci, preset string }{{"", ""}, {"true", ""}, {"", "-p=2"}, {"true", "-p=2"}} {
			ci, preset := row.ci, row.preset
			name := target + " away from CI"
			if ci != "" {
				name = target + " in CI"
			}
			if preset != "" {
				name += " with GOFLAGS already " + preset
			}
			t.Run(name, func(t *testing.T) {
				bin := t.TempDir()
				log := filepath.Join(bin, "goflags.log")
				// `go list …` answers nothing, so the gate's package list is empty, and every other call is recorded and succeeds.
				script := "#!/bin/sh\nprintf '%s\\t%s\\n' \"$1\" \"$GOFLAGS\" >> '" + log + "'\ncase \"$1\" in list) exit 0 ;; esac\nexit 0\n"
				if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(make, "-C", root, target)
				env := []string{}
				for _, kv := range os.Environ() {
					// TMPDIR is the suite's own: the gate this runs in has one of the form the recipe removes when it finishes (`/tmp/opossum-test-*`), and an
					// inner `make test` in CI, where the Makefile does not give it a directory of its own, would remove the one it was handed — the suite's
					// working directory, in the middle of the run, which is what stopped the push sieve's build of every package after this one.
					if strings.HasPrefix(kv, "CI=") || strings.HasPrefix(kv, "GOFLAGS=") || strings.HasPrefix(kv, "TMPDIR=") {
						continue
					}
					env = append(env, kv)
				}
				env = append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				env = append(env, "TMPDIR="+t.TempDir()) // not `/tmp/opossum-test-*`, so the recipe's `rm -rf "$$TMPDIR"` leaves it to the test
				if ci != "" {
					env = append(env, "CI="+ci)
				}
				if preset != "" {
					env = append(env, "GOFLAGS="+preset)
				}
				cmd.Env = env
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("make %s: %v\n%s", target, err, out)
				}
				raw, err := os.ReadFile(log)
				if err != nil {
					t.Fatalf("the gate ran no go at all: %v", err)
				}
				runs := 0
				for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
					verb, flags, _ := strings.Cut(line, "\t")
					if verb != "run" { // the gate is `go run ./cmd/noleftovers go test …`: the call that holds the tests
						continue
					}
					runs++
					if preset != "" && !strings.Contains(" "+flags+" ", " "+preset+" ") {
						t.Errorf("GOFLAGS was %q and the gate handed the test run %q: what was set is kept, and the timeout is added to it", preset, flags)
					}
					timeout := ""
					for _, f := range strings.Fields(flags) {
						if strings.HasPrefix(f, "-timeout=") {
							timeout = f // the last one is the one that counts
						}
					}
					if timeout != "-timeout=30m" {
						t.Errorf("`make %s`%s hands the test run GOFLAGS=%q, whose timeout is %q: want -timeout=30m, or `go test` stops a package at the ten minutes it stopped the orchestrator at (#1756)",
							target, map[bool]string{true: " in CI", false: ""}[ci != ""], flags, timeout)
					}
				}
				if outer != "" {
					if _, err := os.Stat(outer); err != nil {
						t.Fatalf("`make %s` removed the TMPDIR this test was started with (%s): %v", target, outer, err)
					}
				}
				if runs == 0 {
					t.Errorf("`make %s` ran no `go run` (the gate's `go run ./cmd/noleftovers go test …`), so nothing was asked; the log is %q", target, raw)
				}
			})
		}
	}
}
