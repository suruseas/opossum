package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A value docker compose reads as another kind — a string outside a key's bounds
// or not a number, a `scale` below zero — is refused by the commands that read the
// project (an earlier opossum passed it on all the same), and the commands that
// take a project down name it on stderr and go on: a project an earlier opossum
// started from such a file is running, and `down`, `destroy`, `stop` and `kill`
// have to read the file to bring it down (#1468; found on a real runtime: a
// project up'd by the earlier binary with `scale: -1` was refused by every one of
// them from its own directory, with the container still running).
func TestValueFaultsAreRefusedButDoNotStopATakeDown(t *testing.T) {
	fakeShim(t)
	t.Setenv("COMPOSE_PROFILES", "")
	for _, tc := range []struct {
		name, line, want string
	}{
		{"scale below zero", "    scale: -1\n", "services.web.scale -1 is below the least, 0"},
		{"replicas below zero", "    deploy:\n      replicas: \"-1\"\n", "services.web.deploy.replicas -1 is below the least, 0"},
		{"scale and replicas that are different numbers", "    scale: 2\n    deploy:\n      replicas: 3\n", "services.web: can't set distinct values on `scale` (2) and `deploy.replicas` (3)"},
		{"oom_score_adj past its bound", "    oom_score_adj: \"2000\"\n", "services.web.oom_score_adj 2000 is above the most, 1000"},
		{"cpu_count below its least", "    cpu_count: \"-1\"\n", "services.web.cpu_count -1 is below the least, 0"},
		{"cpu_percent past its bound", "    cpu_percent: \"150\"\n", "services.web.cpu_percent 150 is above the most, 100"},
		{"a string that is not a number", "    cpu_shares: \"abc\"\n", `services.web.cpu_shares "abc" does not read as an integer`},
		{"an infinity written as a number", "    scale: .inf\n    x-note: .nan\n", "quote it to keep it a string"},
		{"a value tagged as an integer that is not one", "    x-note: !!int abc\n", "is tagged !!int and is not a value docker compose can read as one"},
		{"a mapping key that is not a string", "    x-note:\n      1: a\n", "the mapping key 1 is not a string — docker compose refuses a key of another type; quote it to keep it a string"},
		{"a port written as a float", "    ports: [!!float 80]\n", "services.web.ports[0] 80 is a float, and a port is an integer or a string — write it as `80` or `\"80\"`"},
		{"a merge key that holds no mapping", "    x-note:\n      <<: v\n", "the merge key `<<` takes a mapping or a list of mappings, and this is the scalar v — docker compose refuses it"},
		{"an empty name in sysctls", "    sysctls:\n      \"\": 1\n", "services.web.sysctls has a name of no characters — docker compose refuses an empty name; write the name, or remove the entry"},
		{"a deploy mode that is a number", "    deploy:\n      mode: 1\n", "services.web.deploy.mode must be a string (`replicated` or `global`), and this is the number 1"},
		{"a port name that is a number", "    ports:\n      - target: 80\n        name: 1\n", "services.web.ports[0].name must be a string, and this is the number 1"},
		{"a replicas that is not a whole number", "    deploy:\n      replicas: two\n", "services.web.deploy.replicas must be a whole number, and this is \"two\""},
		{"a value tagged as a float that is not one", "    x-note: !!float abc\n", "is tagged !!float and is not a number docker compose can read"},
		{"a string that is not a byte size", "    mem_reservation: \"1x\"\n", `services.web.mem_reservation "1x" does not read as a byte size`},
		{"a string that is not a duration", "    stop_grace_period: \"10\"\n", `services.web.stop_grace_period "10" does not read as a duration`},
		// The shape of a value, and a number outside a bound as a number (#1477): an
		// opossum before v0.38 read none of these keys and passed them all on.
		{"a number past its bound, written as a number", "    oom_score_adj: 2000\n", "services.web.oom_score_adj 2000 is above the most, 1000"},
		{"a number below its least, written as a number", "    cpu_count: -1\n", "services.web.cpu_count -1 is below the least, 0"},
		{"cpu_percent past its bound, written as a number", "    cpu_percent: 150\n", "services.web.cpu_percent 150 is above the most, 100"},
		{"a list where a string belongs", "    hostname: [a, b]\n", "services.web.hostname must be a string"},
		// shm_size is the one key both read by opossum (a typed field) and given a shape
		// by the schema: a list is taken as nothing there, the v0.37.0 binary ran on it,
		// and the take-down goes on past it like the others.
		{"a list in a key opossum reads and the schema shapes", "    shm_size: [a]\n", "services.web.shm_size must be a number or a string"},
		{"a boolean where a number belongs", "    pids_limit: true\n", "services.web.pids_limit must be a number or a string"},
		{"a number where a boolean belongs", "    stdin_open: 5\n", "services.web.stdin_open must be a boolean or a string"},
		// The other classes of refusal the shape check makes: a pattern, a place
		// inside a value, and the kinds of value that are neither a string nor a
		// number (a mapping, a null, a float where a whole number belongs).
		{"a name outside the pattern", "    container_name: \"x y\"\n", `services.web.container_name "x y" does not match the pattern [a-zA-Z0-9][a-zA-Z0-9_.-]+`},
		{"a wrong kind one level in", "    dns: [5]\n", "services.web.dns[0] must be a string"},
		{"a mapping where a string belongs", "    hostname: {a: b}\n", "services.web.hostname must be a string"},
		{"a null where a string belongs", "    hostname: null\n", "services.web.hostname must be a string"},
		{"a float where a whole number belongs", "    oom_score_adj: 1.5\n", "services.web.oom_score_adj must be a string or an integer"},
		{"a string a boolean refuses", "    use_api_socket: \"true\"\n", "services.web.use_api_socket must be a boolean"},
		{"a string that is not a boolean", "    privileged: \"maybe\"\n", `services.web.privileged "maybe" does not read as a boolean`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			compose := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web\n"+tc.line)
			// Every command that reads the project and does not take it down still
			// refuses it — held here one by one, so that reading one of them softly
			// is a change a test sees.
			for _, args := range [][]string{
				{"config"}, {"ps"}, {"up"}, {"run", "--no-deps", "web", "true"}, {"logs"}, {"restart"}, {"start"},
				{"images"}, {"volumes"}, {"stats"}, {"build"}, {"pull"}, {"import"}, {"exec", "web", "true"}, {"port", "web", "80"},
			} {
				t.Run("refuses "+strings.Join(args, " "), func(t *testing.T) {
					out, err := run(t, append([]string{"-f", compose}, args...)...)
					if err == nil || !strings.Contains(err.Error(), tc.want) {
						t.Errorf("want %q refused, got err %v, out:\n%s", tc.want, err, out)
					}
				})
			}
			for _, td := range []struct {
				args []string
				acts string // a line the fake runtime logs when the command acted
			}{
				{[]string{"down"}, "delete"},
				{[]string{"destroy", "--force"}, "delete"},
				{[]string{"stop"}, "stop"},
				{[]string{"kill"}, "kill"},
			} {
				t.Run("takes down: "+strings.Join(td.args, " "), func(t *testing.T) {
					readLog := fakeShim(t)
					t.Setenv("STATE_DIR", t.TempDir())
					t.Setenv("XDG_STATE_HOME", t.TempDir())
					strictContainers(t, "web.demo.opossum")
					stdout, stderr, err := runSplit(t, append([]string{"-f", compose}, td.args...)...)
					if err != nil {
						t.Fatalf("want the command to go on, got %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
					}
					if !strings.Contains(stderr, tc.want+" — `up` refuses this compose file; going on, as an earlier opossum may have started it") {
						t.Errorf("want the value named on stderr, got:\n%s", stderr)
					}
					if strings.Contains(stdout, tc.want) {
						t.Errorf("the note belongs on stderr, got stdout:\n%s", stdout)
					}
					acted := false
					for _, l := range readLog() {
						if strings.HasPrefix(l, td.acts+" ") && strings.Contains(l, "web.demo.opossum") {
							acted = true
						}
					}
					if !acted {
						t.Errorf("want %q sent for web.demo.opossum, got log:\n%s", td.acts, strings.Join(readLog(), "\n"))
					}
				})
			}
		})
	}
}

// A file that is fine says nothing when it is taken down, and one nothing can be
// read out of is still refused by all four: only a value docker compose reads as
// another kind, or of a shape it refuses, is let go.
func TestATakeDownStillRefusesAFileItCannotRead(t *testing.T) {
	fakeShim(t)
	good := writeCompose(t, "name: demo\nservices:\n  web:\n    image: web\n    scale: 1\n")
	if _, stderr, err := runSplit(t, "-f", good, "down"); err != nil || strings.Contains(stderr, "going on") {
		t.Errorf("a file with nothing wrong should come down without a note, got err %v, stderr:\n%s", err, stderr)
	}
	// A file nothing can read out of: invalid YAML, and a service named against the
	// rule (which an earlier opossum refused before it created anything, so no
	// project runs on one).
	for name, body := range map[string]string{
		"invalid YAML":            "name: demo\nservices:\n  web: [\n",
		"a service named wrongly": "name: demo\nservices:\n  \"we b\":\n    image: web\n",
		// A top-level refusal the loader made before 0.38.0 as well (the v0.37.0 binary
		// refuses it): a bare `version:` is refused by checkTopLevel and by nothing
		// after it, so a take-down that let every top-level refusal go would take it.
		// A top-level key docker compose does not take: the v0.37.0 binary refused it in
		// the first document (measured on five ways of writing it), so no project runs
		// on one, and it is refused here by all four like any other unreadable file.
		"a top-level key docker compose does not take": "foo: 1\nname: demo\nservices:\n  web:\n    image: web\n",
		"a bare version":                      "name: demo\nversion:\nservices:\n  web:\n    image: web\n",
		"a project name that is not a string": "name: [a, b]\nservices:\n  web:\n    image: web\n",
		"a key opossum reads, wrongly shaped": "name: demo\nservices:\n  web:\n    image: web\n    ports: 5\n",
	} {
		file := writeCompose(t, body)
		for _, args := range [][]string{{"down"}, {"destroy", "--force"}, {"stop"}, {"kill"}} {
			if _, _, err := runSplit(t, append([]string{"-f", file}, args...)...); err == nil {
				t.Errorf("%s: %s should refuse a file it cannot read, as before", name, args[0])
			}
		}
	}
}

// The value may be written in a file another one includes, or set by an override:
// the project is one document by the time it is checked, and the take-down goes on
// past it wherever it came from.
func TestAValueFaultInAnIncludedOrOverridingFileDoesNotStopATakeDown(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("sub.yaml", "services:\n  web:\n    image: web\n    scale: -1\n")
	included := write("compose.yaml", "name: demo\ninclude:\n  - sub.yaml\n")
	// A value the per-file check refuses (a bound on a string), in an included file:
	// that check reads the included file on its own, before the merge.
	write("sub-bound.yaml", "services:\n  web:\n    image: web\n    oom_score_adj: \"2000\"\n")
	includedBound := write("compose-bound.yaml", "name: demo\ninclude:\n  - sub-bound.yaml\n")
	base := write("base.yaml", "name: demo\nservices:\n  web:\n    image: web\n")
	over := write("over.yaml", "services:\n  web:\n    cpu_count: \"-1\"\n")
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"an included file", []string{"-f", included}, "services.web.scale -1 is below the least, 0"},
		{"an included file, a bound on a string", []string{"-f", includedBound}, "services.web.oom_score_adj 2000 is above the most, 1000"},
		{"an override", []string{"-f", base, "-f", over}, "services.web.cpu_count -1 is below the least, 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := run(t, append(tc.args, "config")...); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("config should refuse %q, got %v", tc.want, err)
			}
			_, stderr, err := runSplit(t, append(tc.args, "down")...)
			if err != nil {
				t.Fatalf("down should go on, got %v", err)
			}
			if !strings.Contains(stderr, tc.want+" — `up` refuses this compose file; going on") {
				t.Errorf("want the value named on stderr, got:\n%s", stderr)
			}
		})
	}
}

// Two faults in one project: the take-down names the first — services in name
// order, and the per-file checks before the merged one — the way the commands
// that refuse do, so the line a reader sees does not depend on which was found last.
func TestTheFirstValueFaultIsTheOneNamed(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  bb:\n    image: web\n    cpu_count: \"-1\"\n  aa:\n    image: web\n    oom_score_adj: \"2000\"\n")
	if _, err := run(t, "-f", compose, "config"); err == nil || !strings.Contains(err.Error(), "services.aa.oom_score_adj 2000 is above the most, 1000") {
		t.Errorf("config should name aa's value first, got %v", err)
	}
	_, stderr, err := runSplit(t, "-f", compose, "down")
	if err != nil {
		t.Fatalf("down should go on, got %v", err)
	}
	if !strings.Contains(stderr, "services.aa.oom_score_adj 2000 is above the most, 1000 — `up` refuses") {
		t.Errorf("want aa's value named on stderr, got:\n%s", stderr)
	}
}

// The value may be in a file another one takes a service from with `extends`, and
// in a service nothing extends there: that file is read for the whole of it, and a
// take-down goes on past a value in it as past one in the file it started from
// (#1468 review: those were refused, hard, by every take-down command).
func TestAValueFaultInAnExtendedFileDoesNotStopATakeDown(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("base.yaml", "services:\n  basesvc:\n    image: web\n    cpu_count: \"-1\"\n")
	extending := write("compose.yaml", "name: demo\nservices:\n  web:\n    extends:\n      file: base.yaml\n      service: basesvc\n")
	// The same, one file further: an included file that extends, and a chain of two.
	write("inc.yaml", "services:\n  web:\n    extends:\n      file: base.yaml\n      service: basesvc\n")
	viaInclude := write("compose-inc.yaml", "name: demo\ninclude:\n  - inc.yaml\n")
	write("mid.yaml", "services:\n  midsvc:\n    extends:\n      file: base.yaml\n      service: basesvc\n")
	viaChain := write("compose-chain.yaml", "name: demo\nservices:\n  web:\n    extends:\n      file: mid.yaml\n      service: midsvc\n")
	// The extended service is fine here; only a service nothing extends, in the same
	// file, is at fault — the file is read for the whole of it.
	write("base-sib.yaml", "services:\n  basesvc:\n    image: web\n  sibling:\n    image: web\n    pids_limit: \"x\"\n")
	siblingOnly := write("compose-sib.yaml", "name: demo\nservices:\n  web:\n    extends:\n      file: base-sib.yaml\n      service: basesvc\n")
	// A `cpus` that does not read as a number, in a service nothing extends (#1566, #1579):
	// the fault a take-down goes on past for the four commands alike.
	write("base-cpus.yaml", "services:\n  basesvc:\n    image: web\n  sibling:\n    image: web\n    cpus: abc\n")
	untakenCpus := write("compose-cpus.yaml", "name: demo\nservices:\n  web:\n    extends:\n      file: base-cpus.yaml\n      service: basesvc\n")
	const untakenCpusFault = `services.sibling.cpus "abc" does not read as a number`
	// An infinity in the service it takes, once that service's own extends is resolved
	// (#1510): the service is `basesvc` here, and it holds it through its own chain.
	write("base-inf.yaml", "services:\n  basesvc:\n    image: web\n    extends: chained\n  chained:\n    image: web\n    x-a: .inf\n")
	infinity := write("compose-inf.yaml", "name: demo\nservices:\n  web:\n    extends:\n      file: base-inf.yaml\n      service: basesvc\n")
	const infinityFault = "services.basesvc.x-a is a number docker compose cannot read — infinity and NaN cannot go into its model; quote it to keep it a string"
	// The range of a number is put to the service the extends results in (#1461), which is named by its own name; the fault of a service
	// nothing extends in the same file is the row of siblingOnly.
	const basesvcFault = "services.web.cpu_count -1 is below the least, 0"
	for _, tc := range []struct {
		name string
		file string
		args []string
		want string // the value the refusal and the note name
	}{
		{"down", extending, []string{"down"}, basesvcFault},
		{"destroy", extending, []string{"destroy", "--force"}, basesvcFault},
		{"stop", extending, []string{"stop"}, basesvcFault},
		{"kill", extending, []string{"kill"}, basesvcFault},
		{"down, through an include", viaInclude, []string{"down"}, basesvcFault},
		{"down, through a chain of extends", viaChain, []string{"down"}, basesvcFault},
		{"down, an infinity in the service it takes", infinity, []string{"down"}, infinityFault},
		{"kill, an infinity in the service it takes", infinity, []string{"kill"}, infinityFault},
		{"down, a cpus of a service nothing extends", untakenCpus, []string{"down"}, untakenCpusFault},
		{"stop, a cpus of a service nothing extends", untakenCpus, []string{"stop"}, untakenCpusFault},
		{"kill, a cpus of a service nothing extends", untakenCpus, []string{"kill"}, untakenCpusFault},
		{"destroy, a cpus of a service nothing extends", untakenCpus, []string{"destroy", "--force"}, untakenCpusFault},
		{"down, a fault only in a service nothing extends", siblingOnly, []string{"down"}, `services.sibling.pids_limit "x" does not read as an integer`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readLog := fakeShim(t)
			t.Setenv("STATE_DIR", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			strictContainers(t, "web.demo.opossum")
			if _, err := run(t, "-f", tc.file, "config"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("config should refuse %q, got %v", tc.want, err)
			}
			_, stderr, err := runSplit(t, append([]string{"-f", tc.file}, tc.args...)...)
			if err != nil {
				t.Fatalf("%s should go on, got %v", tc.name, err)
			}
			if !strings.Contains(stderr, tc.want+" — `up` refuses this compose file; going on, as an earlier opossum may have started it") {
				t.Errorf("want %q named on stderr, got:\n%s", tc.want, stderr)
			}
			// And it went on to act: the runtime was asked to stop or delete the
			// project's container, not only told that a note had been printed.
			acted := false
			for _, l := range readLog() {
				if (strings.HasPrefix(l, "stop ") || strings.HasPrefix(l, "kill ") || strings.HasPrefix(l, "delete ")) && strings.Contains(l, "web.demo.opossum") {
					acted = true
				}
			}
			if !acted {
				t.Errorf("want the container acted on, got log:\n%s", strings.Join(readLog(), "\n"))
			}
		})
	}
}

// The merged project's check comes after the per-file ones, so of two faults — a
// negative scale (the merged check) in `aa` and a bound (a per-file check) in `bb` —
// the one named is bb's, the way the commands that refuse name it.
func TestAPerFileValueFaultIsNamedBeforeAMergedOne(t *testing.T) {
	fakeShim(t)
	compose := writeCompose(t, "name: demo\nservices:\n  aa:\n    image: web\n    scale: -1\n  bb:\n    image: web\n    cpu_count: \"-1\"\n")
	want := "services.bb.cpu_count -1 is below the least, 0"
	if _, err := run(t, "-f", compose, "config"); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("config should name %q, got %v", want, err)
	}
	_, stderr, err := runSplit(t, "-f", compose, "down")
	if err != nil {
		t.Fatalf("down should go on, got %v", err)
	}
	if !strings.Contains(stderr, want) {
		t.Errorf("want %q named on stderr, got:\n%s", want, stderr)
	}
}

// What 0.38.0 started to refuse in the file itself, and a release before it took
// (#1475; measured against the v0.37.0 binary, which ran a project from each of
// these): a key written twice inside a mapping the loader reads as it comes, an
// alias that refers to the block that contains it, and a `---` after the one
// document the file holds. Every command that reads the project still refuses
// them; the four that take it down name the first on stderr and go on.
func TestAFileFaultAnEarlierVersionTookDoesNotStopATakeDown(t *testing.T) {
	fakeShim(t)
	t.Setenv("COMPOSE_PROFILES", "")
	const svc = "name: demo\nservices:\n  web:\n    image: web\n"
	for _, tc := range []struct {
		name, body, want string
	}{
		{"a key twice in sysctls", svc + "    sysctls: {a: 1, a: 2}\n", "sets the same key twice"},
		{"a key twice in extra_hosts", svc + "    extra_hosts: {a: 1.1.1.1, a: 2.2.2.2}\n", "sets the same key twice"},
		{"a key twice in logging.options", svc + "    logging:\n      driver: json-file\n      options: {a: 1, a: 2}\n", "sets the same key twice"},
		{"an alias that refers to its own block", "x-a: &a {b: *a}\n" + svc, "an alias refers to the block that contains it"},
		{"a trailing ---", svc + "---\n", "is empty or not a mapping"},
		{"a --- with a comment after it", svc + "--- # nothing here\n", "is empty or not a mapping"},
		{"several --- in a row at the end", svc + "---\n---\n", "is empty or not a mapping"},
		{"a document after it that is not a mapping", svc + "--- hello\n", "is empty or not a mapping"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			compose := writeCompose(t, tc.body)
			for _, args := range [][]string{{"config"}, {"ps"}, {"up"}} {
				t.Run("refuses "+strings.Join(args, " "), func(t *testing.T) {
					out, err := run(t, append([]string{"-f", compose}, args...)...)
					if err == nil || !strings.Contains(err.Error(), tc.want) {
						t.Errorf("want %q refused, got err %v, out:\n%s", tc.want, err, out)
					}
				})
			}
			for _, td := range []struct {
				args []string
				acts string
			}{
				{[]string{"down"}, "delete"},
				{[]string{"destroy", "--force"}, "delete"},
				{[]string{"stop"}, "stop"},
				{[]string{"kill"}, "kill"},
			} {
				t.Run("takes down: "+strings.Join(td.args, " "), func(t *testing.T) {
					readLog := fakeShim(t)
					t.Setenv("STATE_DIR", t.TempDir())
					t.Setenv("XDG_STATE_HOME", t.TempDir())
					strictContainers(t, "web.demo.opossum")
					stdout, stderr, err := runSplit(t, append([]string{"-f", compose}, td.args...)...)
					if err != nil {
						t.Fatalf("want the command to go on, got %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
					}
					if !strings.Contains(stderr, tc.want) || !strings.Contains(stderr, "going on, as an earlier opossum may have started it") {
						t.Errorf("want %q named on stderr, got:\n%s", tc.want, stderr)
					}
					acted := false
					for _, l := range readLog() {
						if strings.HasPrefix(l, td.acts+" ") && strings.Contains(l, "web.demo.opossum") {
							acted = true
						}
					}
					if !acted {
						t.Errorf("want %q sent for web.demo.opossum, got log:\n%s", td.acts, strings.Join(readLog(), "\n"))
					}
				})
			}
		})
	}
}

// A file the take-down reads as more than one document is read as 0.38.0 and later
// read it, whole, and is refused when it holds an empty document, as by every
// command: the v0.37.0 binary read the first document and no other (a second one
// with a `services:` of its own started nothing of it), so a project it started
// from such a file is the first document's, and reading all of them here could
// name another project's containers (`name:` in a second document). Only a `---`
// after the one document the file holds is let go.
func TestAnEmptyDocumentAmongSeveralIsStillRefusedByATakeDown(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string // want: the document the refusal names
	}{
		{"an empty document in the middle", "name: demo\nservices:\n  web:\n    image: web\n---\n---\nservices:\n  db:\n    image: db\n", "document 2"},
		{"two mapping documents and then an empty one", "name: demo\nservices:\n  web:\n    image: web\n---\nservices:\n  db:\n    image: db\n---\n", "document 3"},
		{"another project's name in a second document, then one", "name: demo\nservices:\n  web:\n    image: web\n---\nname: other\n---\n", "document 3"},
		// The first document is the empty one: it is the one named, not a later one.
		{"an empty document first", "---\n---\nname: demo\nservices:\n  web:\n    image: web\n", "document 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readLog := fakeShim(t)
			compose := writeCompose(t, tc.body)
			for _, args := range [][]string{{"down"}, {"stop"}, {"kill"}, {"destroy", "--force"}} {
				_, _, err := runSplit(t, append([]string{"-f", compose}, args...)...)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("%s should refuse this file naming %q, as before; got %v", args[0], tc.want, err)
				}
			}
			for _, l := range readLog() {
				if strings.HasPrefix(l, "delete ") || strings.HasPrefix(l, "stop ") || strings.HasPrefix(l, "kill ") {
					t.Errorf("nothing of this file should be acted on, the runtime saw: %s", l)
				}
			}
		})
	}
}

// The `---` after the only document is let go in a file another one includes, and
// in one a service extends, as in the file itself (the v0.37.0 binary read each of
// them as its first document).
func TestATrailingDocumentMarkerInAnIncludedOrExtendedFileDoesNotStopATakeDown(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("inc.yaml", "services:\n  web:\n    image: web\n---\n")
	included := write("compose-inc.yaml", "name: demo\ninclude:\n  - inc.yaml\n")
	write("ext.yaml", "services:\n  basesvc:\n    image: web\n---\n")
	extended := write("compose-ext.yaml", "name: demo\nservices:\n  web:\n    extends:\n      file: ext.yaml\n      service: basesvc\n")
	for name, file := range map[string]string{"an included file": included, "an extended file": extended} {
		t.Run(name, func(t *testing.T) {
			readLog := fakeShim(t)
			t.Setenv("STATE_DIR", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			if _, err := run(t, "-f", file, "config"); err == nil || !strings.Contains(err.Error(), "is empty or not a mapping") {
				t.Errorf("config should refuse the trailing `---`, got %v", err)
			}
			_, stderr, err := runSplit(t, "-f", file, "down")
			if err != nil {
				t.Fatalf("down should go on, got %v", err)
			}
			if !strings.Contains(stderr, "is empty or not a mapping") || !strings.Contains(stderr, "going on, as an earlier opossum may have started it") {
				t.Errorf("want the trailing `---` named on stderr, got:\n%s", stderr)
			}
			acted := false
			for _, l := range readLog() {
				if strings.HasPrefix(l, "delete ") && strings.Contains(l, "web.demo.opossum") {
					acted = true
				}
			}
			if !acted {
				t.Errorf("want the container deleted, got log:\n%s", strings.Join(readLog(), "\n"))
			}
		})
	}
}

// A file of several YAML documents that names the project differently in a later
// one (#1483): 0.38.0 and later read every document, so the project is the later
// one's; a release before it read the first document alone, so a project it started
// from the file is the first one's. A take-down cannot tell which is meant, and
// naming the wrong one stops another project's containers with nothing said — found
// on a real runtime with a bystander project — so the four commands refuse and ask
// for the name; a name given (-p and COMPOSE_PROJECT_NAME) is
// the reader's answer and is taken; documents that agree, or none that names it
// twice, are read as before.
func TestATakeDownAsksForTheNameWhenTheDocumentsDisagreeAboutIt(t *testing.T) {
	const web = "services:\n  web:\n    image: web\n"
	disagree := writeCompose(t, "name: first\n"+web+"---\nname: second\n"+web)
	agree := writeCompose(t, "name: first\n"+web+"---\nname: first\n"+web)
	onlyLater := writeCompose(t, web+"---\nname: second\n"+web)
	readsOneName := writeCompose(t, "name: first\n"+web+"---\n"+web)
	// Two spellings docker compose reads as one project name (`_` and `-`): the
	// documents do not disagree.
	sameAfterSanitising := writeCompose(t, "name: my_app\n"+web+"---\nname: my-app\n"+web)

	noOne := func(t *testing.T, log []string) {
		t.Helper()
		for _, l := range log {
			if strings.HasPrefix(l, "delete ") || strings.HasPrefix(l, "stop ") || strings.HasPrefix(l, "kill ") {
				t.Errorf("no project's container should be acted on, the runtime saw: %s", l)
			}
		}
	}
	for _, args := range [][]string{{"down"}, {"destroy", "--force"}, {"stop"}, {"kill"}} {
		t.Run("refuses "+args[0], func(t *testing.T) {
			readLog := fakeShim(t)
			t.Setenv("STATE_DIR", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			_, _, err := runSplit(t, append([]string{"-f", disagree}, args...)...)
			if err == nil || !strings.Contains(err.Error(), `"first" where only the first YAML document of each file is read`) ||
				!strings.Contains(err.Error(), `"second" where every document is`) ||
				!strings.Contains(err.Error(), "-f "+disagree+" -p first "+args[0]+"` for the one an earlier version started") ||
				!strings.Contains(err.Error(), "-f "+disagree+" -p second "+args[0]+"` for the one every document names") {
				t.Errorf("want the two names and the way out for each named, with the files given, got %v", err)
			}
			// The generic "take it down by naming it" line that `down` adds to an
			// unreadable file would recommend a third name; it is not added here.
			if err != nil && strings.Contains(err.Error(), "can still be taken down without its file") {
				t.Errorf("no second, different way out should follow, got %v", err)
			}
			noOne(t, readLog())
		})
	}
	t.Run("a name given with -p is taken", func(t *testing.T) {
		readLog := fakeShim(t)
		t.Setenv("STATE_DIR", t.TempDir())
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		if _, _, err := runSplit(t, "-f", disagree, "-p", "first", "down"); err != nil {
			t.Fatalf("down -p first should go on, got %v", err)
		}
		acted := false
		for _, l := range readLog() {
			if strings.HasPrefix(l, "delete ") && strings.Contains(l, "web.first.opossum") {
				acted = true
			}
			if strings.Contains(l, ".second.opossum") {
				t.Errorf("the other project must not be touched, the runtime saw: %s", l)
			}
		}
		if !acted {
			t.Errorf("want web.first.opossum deleted, got log:\n%s", strings.Join(readLog(), "\n"))
		}
	})
	t.Run("a name given in the project's .env is taken", func(t *testing.T) {
		readLog := fakeShim(t)
		t.Setenv("STATE_DIR", t.TempDir())
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		dir := t.TempDir()
		file := filepath.Join(dir, "compose.yaml")
		if err := os.WriteFile(file, []byte("name: first\n"+web+"---\nname: second\n"+web), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("COMPOSE_PROJECT_NAME=first\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runSplit(t, "-f", file, "down"); err != nil {
			t.Fatalf("down with the name in .env should go on, got %v", err)
		}
		for _, l := range readLog() {
			if strings.Contains(l, ".second.opossum") {
				t.Errorf("the other project must not be touched, the runtime saw: %s", l)
			}
		}
	})
	t.Run("a name given with COMPOSE_PROJECT_NAME is taken", func(t *testing.T) {
		fakeShim(t)
		t.Setenv("STATE_DIR", t.TempDir())
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		t.Setenv("COMPOSE_PROJECT_NAME", "first")
		if _, _, err := runSplit(t, "-f", disagree, "down"); err != nil {
			t.Errorf("down with COMPOSE_PROJECT_NAME set should go on, got %v", err)
		}
	})
	// The first document writes no name, so the project a release before 0.38.0 started
	// is the directory's, and a later document that names another is the same
	// disagreement.
	t.Run("a name only a later document writes", func(t *testing.T) {
		readLog := fakeShim(t)
		t.Setenv("STATE_DIR", t.TempDir())
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		_, _, err := runSplit(t, "-f", onlyLater, "down")
		dir := filepath.Base(filepath.Dir(onlyLater))
		if err == nil || !strings.Contains(err.Error(), `"`+dir+`" where only the first YAML document of each file is read`) ||
			!strings.Contains(err.Error(), `"second" where every document is`) {
			t.Errorf("want the disagreement named with the directory %q as the project a release before 0.38.0 started, got %v", dir, err)
		}
		noOne(t, readLog())
	})
	for name, file := range map[string]string{
		"documents that agree":                     agree,
		"names that read the same once sanitised":  sameAfterSanitising,
		"a name the first writes and no later one": readsOneName,
	} {
		t.Run(name+" are read as before", func(t *testing.T) {
			fakeShim(t)
			t.Setenv("STATE_DIR", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			if _, _, err := runSplit(t, "-f", file, "down"); err != nil {
				t.Errorf("down should go on, got %v", err)
			}
		})
	}
	t.Run("config and up read every document as before", func(t *testing.T) {
		fakeShim(t)
		if _, err := run(t, "-f", disagree, "config"); err != nil {
			t.Errorf("config should read the file, got %v", err)
		}
	})
}

// The name compared is the one the loader comes to, not the text written: what a
// `${VAR:-default}`, an alias, an empty `name:` or a `$$` makes of it, and the
// directory's where there is none (#1483 review: read from the raw text, five of
// these were let through as the same project and took another one down).
func TestTheNameTheDocumentsComeToIsTheOneCompared(t *testing.T) {
	const web = "services:\n  web:\n    image: web\n"
	t.Setenv("X", "")
	os.Unsetenv("X")
	for _, tc := range []struct {
		name   string
		docs   []string // one file each; a file's documents are joined with ---
		refuse bool
		after  string // the name every document comes to, where it is worth checking; "<dir>" is the directory's
	}{
		{"a default that comes to another name in a later document", []string{"name: alpha\n" + web + "---\nname: ${X:-beta}\n" + web}, true, "beta"},
		{"a default in the first document, another name later", []string{"name: ${X:-alpha}\n" + web + "---\nname: beta\n" + web}, true, "beta"},
		{"an empty name in a later document is the directory's", []string{"name: alpha\n" + web + "---\nname: ''\n" + web}, true, "<dir>"},
		{"an alias in a later document", []string{"name: alpha\n" + web + "---\nx-n: &n beta\nname: *n\n" + web}, true, "beta"},
		{"a $$ in a later name", []string{"name: alpha\n" + web + "---\nname: be$$ta\n" + web}, true, "be-ta"},
		{"a default that comes to the same name", []string{"name: alpha\n" + web + "---\nname: ${X:-alpha}\n" + web}, false, ""},
		{"an alias in the first document that comes to the same name", []string{"x-n: &n alpha\nname: *n\n" + web + "---\nname: alpha\n" + web}, false, ""},
		{"a later null name changes nothing", []string{"name: alpha\n" + web + "---\nname: null\n" + web}, false, ""},
		{"several files that each name the project, one document each", []string{"name: alpha\n" + web, "name: beta\nservices:\n  db:\n    image: db\n"}, false, ""},
		{"a file of several documents and another file that names it", []string{"name: alpha\n" + web + "---\nservices:\n  db:\n    image: db\n", "name: beta\nservices:\n  cache:\n    image: c\n"}, false, ""},
		// The last file names it, so both readings end at beta: the same project.
		{"a later file that names it settles a disagreement between documents", []string{"name: alpha\n" + web + "---\nname: gamma\n" + web, "name: beta\nservices:\n  cache:\n    image: c\n"}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readLog := fakeShim(t)
			t.Setenv("STATE_DIR", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			dir := t.TempDir()
			var args []string
			for i, body := range tc.docs {
				f := filepath.Join(dir, "f"+string(rune('a'+i))+".yaml")
				if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
				args = append(args, "-f", f)
			}
			_, _, err := runSplit(t, append(args, "down")...)
			if tc.refuse {
				if err == nil || !strings.Contains(err.Error(), "where every document is") {
					t.Errorf("want the disagreement refused, got %v", err)
				}
				if tc.after != "" && err != nil {
					after := tc.after
					if after == "<dir>" {
						after = filepath.Base(dir)
					}
					if !strings.Contains(err.Error(), `"`+after+`" where every document is`) {
						t.Errorf("want the name every document comes to (%q) named, got %v", after, err)
					}
				}
				for _, l := range readLog() {
					if strings.HasPrefix(l, "delete ") || strings.HasPrefix(l, "stop ") || strings.HasPrefix(l, "kill ") {
						t.Errorf("no project's container should be acted on, the runtime saw: %s", l)
					}
				}
				return
			}
			if err != nil {
				t.Errorf("the documents agree about the name, down should go on, got %v", err)
			}
		})
	}
}

// The directory's name is sanitised the way a project name is before it is compared
// (`My_App` is `my-app`): with a first document that names the project `my-app`
// and a later empty `name:`, the documents do not disagree — a directory taken as
// written would be read as another project (#1485 review; a `t.TempDir()` is
// digits, already in the sanitised form, and hid this).
func TestTheDirectorysNameIsSanitisedBeforeItIsCompared(t *testing.T) {
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "My_App")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const web = "services:\n  web:\n    image: web\n"
	file := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(file, []byte("name: my-app\n"+web+"---\nname: ''\n"+web), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runSplit(t, "-f", file, "down"); err != nil {
		t.Errorf("my-app and the directory My_App are one project name, down should go on, got %v", err)
	}
}

// A fault the take-down goes past, and a real refusal after it in the same file: the
// real one is still made, and nothing is acted on (#1484). A recording that returned
// at once would skip the checks that follow it in validateOne — the shapes, the
// repeated group_add entries, a service with nothing under it.
func TestARealRefusalAfterASoftOneIsStillMade(t *testing.T) {
	const soft = "    sysctls: {a: 1, a: 2}\n"
	for name, body := range map[string]string{
		"an empty service after a repeated key": "name: demo\nservices:\n  web:\n    image: web\n" + soft + "  db:\n",
		"a key opossum reads, wrongly shaped":   "name: demo\nservices:\n  web:\n    image: web\n" + soft + "    ports: 5\n",
		"a repeated group_add entry":            "name: demo\nservices:\n  web:\n    image: web\n" + soft + "    group_add: [16, 16]\n",
	} {
		t.Run(name, func(t *testing.T) {
			readLog := fakeShim(t)
			t.Setenv("STATE_DIR", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			compose := writeCompose(t, body)
			for _, args := range [][]string{{"down"}, {"stop"}, {"kill"}, {"destroy", "--force"}} {
				if _, _, err := runSplit(t, append([]string{"-f", compose}, args...)...); err == nil {
					t.Errorf("%s should still refuse the file, as before", args[0])
				}
			}
			for _, l := range readLog() {
				if strings.HasPrefix(l, "delete ") || strings.HasPrefix(l, "stop ") || strings.HasPrefix(l, "kill ") {
					t.Errorf("nothing of this file should be acted on, the runtime saw: %s", l)
				}
			}
		})
	}
}

// `down` that asks for the project's name touches nothing before it does (#1489):
// its restart supervisor is stopped, and its record cleared, by a name guessed
// without the file — the directory's where the first document names nothing, which
// is the project an earlier version started (left running with no supervisor) or
// another project's of that name. A file that cannot be read still has its
// supervisor stopped, as before: that is what the early stop is for.
func TestDownThatAsksForTheNameDoesNotStopASupervisor(t *testing.T) {
	const web = "services:\n  web:\n    image: web\n"
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)
	var stopped []string
	saved := stopSupervisorFn
	stopSupervisorFn = func(name string) (bool, bool) { stopped = append(stopped, name); return true, true }
	t.Cleanup(func() { stopSupervisorFn = saved })

	// The first document names nothing: the project is the directory's, and a later
	// document names another.
	disagree := writeCompose(t, web+"---\nname: other\n"+web)
	// What a running supervisor of the directory's project would have recorded.
	guessed := compose.SanitizeName(filepath.Base(filepath.Dir(disagree)))
	if err := os.MkdirAll(filepath.Join(xdg, "opossum", guessed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.RecordWatched(guessed, []string{"web"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runSplit(t, "-f", disagree, "down"); err == nil {
		t.Fatal("down should ask for the name")
	}
	if len(stopped) != 0 {
		t.Errorf("no supervisor should be stopped when the name is asked for, the early stop saw %v", stopped)
	}
	if got := orchestrator.Watched(guessed); len(got) != 1 || got[0] != "web" {
		t.Errorf("the record of what the supervisor watches must be left where it is, got %v", got)
	}
	// The name given: the supervisor of that project is stopped, as before.
	stopped = nil
	if _, _, err := runSplit(t, "-f", disagree, "-p", "given", "down"); err != nil {
		t.Fatalf("down -p given should go on, got %v", err)
	}
	// The early stop asks for the named project, and `Down` asks again under the lock it takes
	// when the first ask stopped it (the same seam, so both are seen): both are for the named
	// project, and none is for the directory's, which is what this is about.
	if strings.Join(stopped, ",") != "given,given" {
		t.Errorf("want the supervisor of the named project asked by the early stop and again by Down, saw %v", stopped)
	}
	// A file that cannot be read: its supervisor is still stopped.
	stopped = nil
	broken := writeCompose(t, "name: demo\nservices:\n  web: [\n")
	if _, _, err := runSplit(t, "-f", broken, "down"); err == nil {
		t.Fatal("down should refuse a file that cannot be read")
	}
	if len(stopped) != 1 {
		t.Errorf("want the early stop still made for a file that cannot be read, saw %v", stopped)
	}
}

// What a release before 0.38.0 read is the first document of EACH file, merged (#1488):
// the name it gave the project comes from the earlier file when the last one's first
// document names nothing. A first-documents merge built from the last file alone
// would name the directory, and the guidance would send the reader to a project
// that was never started.
func TestTheEarlierNameComesFromEveryFilesFirstDocument(t *testing.T) {
	fakeShim(t)
	t.Setenv("STATE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yaml")
	b := filepath.Join(dir, "b.yaml")
	const web = "services:\n  web:\n    image: web\n"
	if err := os.WriteFile(a, []byte("name: alpha\n"+web), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("services:\n  db:\n    image: db\n---\nname: gamma\nservices:\n  cache:\n    image: c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := runSplit(t, "-f", a, "-f", b, "down")
	if err == nil || !strings.Contains(err.Error(), `"alpha" where only the first YAML document of each file is read`) ||
		!strings.Contains(err.Error(), `"gamma" where every document is`) {
		t.Errorf("want alpha (from a.yaml) as the earlier name and gamma as the later one, got %v", err)
	}
}

// A value tagged `!!float` that is no float, in a file that `extends` or `include`
// reads, is refused by every command, the take-downs too: every earlier version read
// such a file the same way (measured, v0.37.0 at its roll commit: `config` rc 1 for
// each place), so no project started from one is left to come down (#1518).
func TestAFloatTagThatIsNoFloatInAnExtendedOrIncludedFileIsRefusedByEveryCommand(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("base.yaml", "services:\n  b:\n    image: web\n    x-a: !!float abc\n")
	extending := write("compose.yaml", "name: demo\nservices:\n  web:\n    extends:\n      file: base.yaml\n      service: b\n")
	including := write("compose-inc.yaml", "name: demo\ninclude:\n  - base.yaml\n")
	for _, file := range []string{extending, including} {
		for _, args := range [][]string{{"config"}, {"down"}, {"stop"}, {"kill"}, {"destroy", "--force"}} {
			t.Run(filepath.Base(file)+" "+strings.Join(args, " "), func(t *testing.T) {
				t.Setenv("STATE_DIR", t.TempDir())
				t.Setenv("XDG_STATE_HOME", t.TempDir())
				_, stderr, err := runSplit(t, append([]string{"-f", file}, args...)...)
				if err == nil {
					t.Fatalf("want the command to refuse, it went on:\n%s", stderr)
				}
				if !strings.Contains(err.Error(), "abc") || !strings.Contains(err.Error(), "!!float") {
					t.Errorf("want the value and its tag named, got %v", err)
				}
			})
		}
	}
}

// A key the first file writes with nothing after it, where no file gives it a value, is
// refused by the commands that take a project down as by any other: they go on past what a
// later file's override holds (an earlier opossum took it as "not given"), not past a first
// file's own mistake, which docker compose refuses and which every release before refused
// too (#1597).
func TestAFirstFilesBareKeyStopsATakeDownAsAnyOtherCommand(t *testing.T) {
	fakeShim(t)
	dir := t.TempDir()
	first := filepath.Join(dir, "s1.yml")
	later := filepath.Join(dir, "s0.yml")
	if err := os.WriteFile(first, []byte("name: demo\nservices:\n  web:\n    image: web\n    networks: ~\nnetworks:\n  n: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(later, []byte("services:\n  web:\n    image: web\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"stop"}, {"kill"}, {"down"}, {"destroy", "--force"}} {
		t.Run(command[0], func(t *testing.T) {
			t.Setenv("STATE_DIR", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			if _, err := run(t, "-f", first, "-f", later, "config"); err == nil {
				t.Fatalf("config should refuse the first file's bare networks")
			}
			if _, err := run(t, append([]string{"-f", first, "-f", later}, command...)...); err == nil {
				t.Errorf("%s should refuse a bare key of the first file, and went on", command[0])
			}
		})
	}
}
