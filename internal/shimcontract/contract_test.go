// Package shimcontract holds one table of questions every fake `container` CLI
// in this repository must answer the same way — the way the real CLI answers
// them (testdata/real-cli-output.md). There are three fakes: the shell one
// people use by hand (testdata/fake-container.sh, in README), the one the CLI
// tests build (cmd/opossum/testdata/fakeshim), and the one the orchestrator
// tests build (internal/orchestrator/testdata/fakeshim). Each is kept by the
// package that uses it, so a behaviour taught to one and not the others passes
// that package's tests and quietly changes what another package's tests mean —
// and a fake that answers too leniently (a delete that always succeeds) passes
// every assertion that only looks for success. A contract is added here as a
// row; a fake that cannot answer it fails this test.
package shimcontract_test

import (
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// step is one command and what the real CLI does with it: its exit code, a
// piece of its output (stdout and stderr together) that must be there, and one
// that must not. The wording is part of the contract, not just the exit code:
// opossum tells "not there" from "could not be asked" by whether a failed
// inspect says `not found` — and the real CLI has several ways of saying a
// thing is not there, one of which (a volume or network delete) does not
// contain those words. A fake that failed with other words would turn an
// absent container into an unanswered one inside the tests.
type step struct {
	argv  []string
	rc    int
	has   string
	lacks string
}

// A scenario runs its steps in order against one fresh fake with a fresh state
// directory (and env, if it sets any), so a later step can depend on what an
// earlier one did. NAME in an argument or in has/lacks is the scenario's
// container name; OTHER is a second container in the same state that nothing
// in the scenario touches.
//
// Not in the table yet, and known to be wrong: a name never run or created at
// all. Every fake answers `inspect <never-seen>` as a running container where
// the real CLI exits 1 with `container not found` — the opposite answer, on the
// very question opossum uses to tell "not there" from "could not be asked" —
// and lets `volume delete <never-seen>` succeed where the real CLI fails. Many
// tests lean on the first (a test that needs absence sets INSPECT_ABSENT), so
// fixing it means counting them first; it is tracked as an open item, not a
// choice. The table pins what a fake does after it has been told something:
// deleted, stopped, started.
var contract = []struct {
	name  string
	env   []string
	steps []step
}{
	{"a container's life: run, stop, start, delete — and what inspect says at each point", nil, []step{
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"inspect", "NAME"}, has: `"state":"running"`},
		// `stop`'s exit code says only that the name exists; the state is what
		// changed (1.4.1).
		{argv: []string{"stop", "NAME"}},
		{argv: []string{"inspect", "NAME"}, has: `"state":"stopped"`},
		{argv: []string{"start", "NAME"}},
		{argv: []string{"inspect", "NAME"}, has: `"state":"running"`},
		{argv: []string{"delete", "--force", "NAME"}},
		{argv: []string{"inspect", "NAME"}, rc: 1, has: "container not found: NAME"},
	}},
	{"a container that is gone: stop and delete fail, and only then", nil, []step{
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"delete", "--force", "NAME"}},
		{argv: []string{"stop", "NAME"}, rc: 1, has: `notFound: "container with ID NAME not found"`},
		{argv: []string{"delete", "--force", "NAME"}, rc: 1, has: `notFound: "container with ID NAME not found"`},
		// Running the name again makes it there again.
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"inspect", "NAME"}, has: `"state":"running"`},
		{argv: []string{"stop", "NAME"}},
	}},
	{"a stopped container is deleted the way down and destroy delete it: stop, then delete", nil, []step{
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"stop", "NAME"}},
		{argv: []string{"delete", "--force", "NAME"}},
		{argv: []string{"inspect", "NAME"}, rc: 1, has: "container not found: NAME"},
		{argv: []string{"stop", "NAME"}, rc: 1, has: `notFound: "container with ID NAME not found"`},
		{argv: []string{"delete", "--force", "NAME"}, rc: 1, has: `notFound: "container with ID NAME not found"`},
	}},
	{"deleting one container leaves another alone", nil, []step{
		{argv: []string{"run", "-d", "--name", "NAME", "alpine"}},
		{argv: []string{"run", "-d", "--name", "OTHER", "alpine"}},
		{argv: []string{"delete", "--force", "NAME"}},
		{argv: []string{"inspect", "OTHER"}, has: `"state":"running"`},
		{argv: []string{"stop", "OTHER"}},
		{argv: []string{"inspect", "NAME"}, rc: 1, has: "container not found: NAME"},
	}},
	{"a volume that was already there, once deleted, is gone", []string{"VOLUME_LS=pre"}, []step{
		{argv: []string{"volume", "delete", "pre"}},
		{argv: []string{"volume", "delete", "pre"}, rc: 1, has: `failed to delete one or more volumes`, lacks: "not found"},
	}},
	{"a volume that is gone: deleting it again fails", nil, []step{
		{argv: []string{"run", "-d", "-v", "vol1:/d", "--name", "NAME", "alpine"}},
		{argv: []string{"volume", "delete", "vol1"}},
		{argv: []string{"volume", "delete", "vol1"}, rc: 1, has: `failed to delete one or more volumes`, lacks: "not found"},
	}},
	{"the daemon answers `system status --format json`", nil, []step{
		{argv: []string{"system", "status", "--format", "json"}, has: `"status":"running"`},
	}},
}

func TestEveryFakeAnswersTheContract(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	fakes := map[string]string{"testdata/fake-container.sh": filepath.Join(root, "testdata", "fake-container.sh")}
	for _, pkg := range []string{"cmd/opossum/testdata/fakeshim", "internal/orchestrator/testdata/fakeshim"} {
		out := filepath.Join(bin, strings.ReplaceAll(pkg, "/", "_"))
		cmd := exec.Command("go", "build", "-o", out, "./"+pkg)
		cmd.Dir = root
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("building %s: %v\n%s", pkg, err, b)
		}
		fakes[pkg] = out
	}
	// Three different programs, not three names for one: the two Go fakes are
	// built from different packages and must not come out the same binary.
	if len(fakes) != 3 {
		t.Fatalf("want the three fakes, got %v", fakes)
	}
	if a, b := digest(t, fakes["cmd/opossum/testdata/fakeshim"]), digest(t, fakes["internal/orchestrator/testdata/fakeshim"]); a == b {
		t.Fatalf("the two Go fakes built to the same binary — one of them is not being checked")
	}
	for fake, path := range fakes {
		for _, sc := range contract {
			t.Run(fake+"/"+sc.name, func(t *testing.T) {
				state := t.TempDir()
				fill := strings.NewReplacer("NAME", "probe.demo.opossum", "OTHER", "probe.other.opossum")
				for i, st := range sc.steps {
					argv := make([]string, len(st.argv))
					for j, a := range st.argv {
						argv[j] = fill.Replace(a)
					}
					cmd := exec.Command(path, argv...)
					// Only what the fake needs: an inherited knob (INSPECT_ABSENT, a
					// STATE_DIR of the caller's) must not decide the answer.
					cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"),
						"STATE_DIR=" + state, "FAKE_LOG=" + filepath.Join(state, "calls.log")}, sc.env...)
					out, err := cmd.CombinedOutput()
					rc := 0
					if ee, ok := err.(*exec.ExitError); ok {
						rc = ee.ExitCode()
					} else if err != nil {
						t.Fatalf("step %d %v: %v", i+1, argv, err)
					}
					if rc != st.rc {
						t.Errorf("step %d %v: exit %d, the real CLI exits %d; output:\n%s", i+1, argv, rc, st.rc, out)
					}
					if want := fill.Replace(st.has); want != "" && !strings.Contains(string(out), want) {
						t.Errorf("step %d %v: want %q in the output, got:\n%s", i+1, argv, want, out)
					}
					if bad := fill.Replace(st.lacks); bad != "" && strings.Contains(string(out), bad) {
						t.Errorf("step %d %v: the real CLI does not say %q here, got:\n%s", i+1, argv, bad, out)
					}
				}
			})
		}
	}
}

func digest(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}
