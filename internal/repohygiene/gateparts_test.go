package repohygiene_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// What each part of the gate is told: which part it is, of how many, and
// where to write down what it ran. N is the length of the matrix, read by
// GitHub, so a part added or taken away changes it for every part at once.
const (
	partSpec   = "${{ matrix.part }}/${{ strategy.job-total }}"
	partRecord = "/tmp/opossum-ci-part-${{ github.run_id }}.txt"
)

// The verdict job that reads each parts job's records, and so the prefix of
// the records' names.
var verdictOf = map[string]string{"gomod-parts": "ci", "stable-parts": "stable"}

// checkParts reads what makes a parts job a set of parts of one gate: how
// many there are, that one red part does not cancel the others, and that
// each part hands its record on, red or green.
func checkParts(t *testing.T, name string, job map[string]any) {
	t.Helper()
	strategy, _ := job["strategy"].(map[string]any)
	allowed(t, "the "+name+" job's strategy", strategy, "fail-fast", "matrix")
	if ff, ok := strategy["fail-fast"].(bool); !ok || ff {
		t.Errorf("%s: fail-fast is %v; one red part says nothing about the others' tests, and cancelling them leaves their records unwritten — so the verdict can say only that parts are missing", name, strategy["fail-fast"])
	}
	matrix, _ := strategy["matrix"].(map[string]any)
	allowed(t, "the "+name+" job's matrix", matrix, "part")
	parts, _ := matrix["part"].([]any)
	if len(parts) < 2 {
		t.Errorf("%s: the matrix has parts %v; this check knows a gate in two or more parts", name, parts)
	}
	for i, p := range parts {
		if n, ok := p.(int); !ok || n != i+1 {
			t.Errorf("%s: the matrix's parts are %v; each is told it is part k of the list's length, so they are 1 to N in order, or a part is told a k no part has", name, parts)
			break
		}
	}

	steps, _ := job["steps"].([]any)
	var uploads []map[string]any
	for _, st := range steps {
		m, _ := st.(map[string]any)
		if u, _ := m["uses"].(string); strings.HasPrefix(u, "actions/upload-artifact@") {
			uploads = append(uploads, m)
		}
	}
	if len(uploads) != 1 {
		t.Errorf("%s: %d steps upload a record; each part hands on one", name, len(uploads))
		return
	}
	up := uploads[0]
	allowed(t, "the "+name+" job's upload step", up, "uses", "if", "with")
	if u := up["uses"]; u != "actions/upload-artifact@v7" {
		t.Errorf("%s: the record goes up with %v; this check knows actions/upload-artifact@v7", name, u)
	}
	if c := up["if"]; c != "${{ always() }}" {
		t.Errorf("%s: the record goes up on %v, not ${{ always() }} — a red part's record is what says which of its tests ran", name, c)
	}
	with, _ := up["with"].(map[string]any)
	want := map[string]any{
		"name":              "gate-" + verdictOf[name] + "-part-${{ matrix.part }}",
		"path":              partRecord,
		"if-no-files-found": "error",
		"retention-days":    1,
	}
	if !reflect.DeepEqual(with, want) {
		t.Errorf("%s: the record goes up with %v; this check knows %v — the name is what the %s job looks for, the path is where the gate wrote it, and a missing file is an error rather than an empty record", name, with, want, verdictOf[name])
	}
}

// checkVerdictJob reads a compiler's verdict job: it waits for the parts,
// runs whatever they said, and stops in a time that fits what it does.
func checkVerdictJob(t *testing.T, what string, job map[string]any, parts string) {
	t.Helper()
	if n := job["needs"]; n != parts {
		t.Errorf("%s needs %v; it gives the verdict of %s, and waits for exactly that", what, n, parts)
	}
	// A job that `needs` another is skipped when that one fails, and a
	// skipped required check reads as a passed one. `always()` and nothing
	// else: `!cancelled()` skips it on a cancelled run in the same way.
	if c := job["if"]; c != "${{ always() }}" {
		t.Errorf("%s runs on %v; want ${{ always() }} — skipped, the check under its name reads as passed", what, c)
	}
	if got := fmt.Sprint(job["runs-on"]); got != runsOn {
		t.Errorf("%s runs on %s; this check knows %s", what, got, runsOn)
	}
	n, ok := job["timeout-minutes"].(int)
	if !ok || n < 5 || n > 20 {
		t.Errorf("%s stops at %v minutes; it waits for nothing once it starts, and reads the records and the pull request — 5 to 20 is a bound that does not cut that", what, job["timeout-minutes"])
	}
	env, _ := job["env"].(map[string]any)
	if len(env) != 1 || env["GOTOOLCHAIN"] != "local" {
		t.Errorf("%s hands every step env %v; this check knows GOTOOLCHAIN=local and nothing else", what, env)
	}
}

// The two jobs under the names the branch rules require are verdicts: each
// waits for its compiler's parts, reads their records, and is green only when
// every part passed and the parts together ran every test of the gate once.
// Their checks are what a merge waits for, so each piece of that — the wait,
// the run-whatever-happened, the reading of the records, the verdict on the
// parts' result — is pinned here, and the verdict's script is run.
func TestTheVerdictsAreTheWholeGate(t *testing.T) {
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := yaml.Unmarshal(b, &top); err != nil {
		t.Fatalf("reading the workflow: %v", err)
	}
	jobs, _ := top["jobs"].(map[string]any)
	for parts, name := range verdictOf {
		t.Run(name, func(t *testing.T) {
			job, _ := jobs[name].(map[string]any)
			if job == nil {
				t.Fatalf("no `%s` job; it is the check the branch rules require for %s", name, parts)
			}
			// No `name:`: the check is reported under it, and the branch
			// rules require the job's id.
			allowed(t, "the "+name+" job", job, "needs", "if", "runs-on", "env", "steps", "timeout-minutes")
			checkVerdictJob(t, "the "+name+" job", job, parts)

			steps, _ := job["steps"].([]any)
			at := map[string]int{}
			byName := map[string]map[string]any{}
			for i, st := range steps {
				m, _ := st.(map[string]any)
				key, _ := m["name"].(string)
				if key == "" {
					key, _ = m["uses"].(string)
				}
				if _, dup := at[key]; dup {
					t.Errorf("two steps are %q", key)
				}
				at[key], byName[key] = i, m
				if _, has := m["continue-on-error"]; has && key != "validate .goreleaser.yaml" {
					t.Errorf("step %d (%q) carries continue-on-error: its red would read as the job's green", i, key)
				}
				// The same rule as the parts': a step that reads the pull
				// request runs on pull requests, after a red step.
				text := fmt.Sprint(m["env"]) + fmt.Sprint(m["run"]) + fmt.Sprint(m["with"])
				if strings.Contains(text, "github.event.pull_request") && m["if"] != onEveryPullRequest {
					t.Errorf("step %d (%q) reads github.event.pull_request but runs on %v, not %s", i, key, m["if"], onEveryPullRequest)
				}
			}

			dir := "/tmp/opossum-ci-parts-${{ github.run_id }}-${{ github.job }}"
			down, ok := byName["actions/download-artifact@v8"]
			if !ok {
				t.Fatal("no actions/download-artifact@v8 step; the parts' records are not fetched")
			}
			wantDown := map[string]any{"pattern": "gate-" + name + "-part-*", "path": dir}
			if w, _ := down["with"].(map[string]any); !reflect.DeepEqual(w, wantDown) {
				t.Errorf("the records are fetched with %v; want %v — every part's record, and only this compiler's", w, wantDown)
			}
			if c := down["if"]; c != "${{ !cancelled() }}" {
				t.Errorf("the records are fetched on %v, not ${{ !cancelled() }} — the check of them runs after a red step", c)
			}

			const verifyName = "the parts ran every test of the gate once"
			verify, ok := byName[verifyName]
			if !ok {
				t.Fatalf("no step %q; nothing reads the parts' records", verifyName)
			}
			allowed(t, "the "+verifyName+" step", verify, "name", "if", "run")
			if r := verify["run"]; r != `go run ./cmd/shard -verify "`+dir+`"` {
				t.Errorf("the records are read with %q; this check knows cmd/shard -verify over the directory they were fetched into", r)
			}
			if c := verify["if"]; c != "${{ !cancelled() }}" {
				t.Errorf("the records are read on %v, not ${{ !cancelled() }}", c)
			}
			if at[verifyName] < at["actions/download-artifact@v8"] {
				t.Error("the records are read before they are fetched")
			}
			goAt, ok := at["actions/setup-go@v7"]
			if !ok || goAt > at[verifyName] {
				t.Error("no setup-go before the records are read; `go run` would take whatever the runner image carries")
			} else if w, _ := byName["actions/setup-go@v7"]["with"].(map[string]any); !reflect.DeepEqual(w, map[string]any{"go-version-file": "go.mod", "cache": true}) {
				t.Errorf("the records are read with setup-go %v; this check knows the version go.mod asks for, cached", w)
			}

			const resultName = "every part of the gate passed"
			result, ok := byName[resultName]
			if !ok {
				t.Fatalf("no step %q; nothing says the parts' own verdict", resultName)
			}
			if at[resultName] != len(steps)-1 {
				t.Errorf("%q is step %d of %d; it is the last word", resultName, at[resultName]+1, len(steps))
			}
			allowed(t, "the "+resultName+" step", result, "name", "if", "env", "run")
			if c := result["if"]; c != "${{ always() }}" {
				t.Errorf("the parts' verdict is read on %v, not ${{ always() }}", c)
			}
			env, _ := result["env"].(map[string]any)
			if want := map[string]any{"PARTS": "${{ needs." + parts + ".result }}"}; !reflect.DeepEqual(env, want) {
				t.Fatalf("the parts' verdict is read from %v; want %v", env, want)
			}
			script, _ := result["run"].(string)
			for _, c := range []struct {
				result string
				pass   bool
			}{
				{"success", true},
				{"failure", false},
				{"cancelled", false},
				{"skipped", false},
				{"", false},
			} {
				cmd := exec.Command("bash", "-e", "-s")
				cmd.Stdin = strings.NewReader(script)
				cmd.Env = append(os.Environ(), "PARTS="+c.result)
				out, err := cmd.CombinedOutput()
				if (err == nil) != c.pass {
					t.Errorf("with the parts %q the verdict step ended %v, want passing=%v:\n%s", c.result, err, c.pass, out)
				}
			}
		})
	}
}
