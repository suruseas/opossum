// Command fakeshim is a compiled stand-in for the `container` CLI used by the
// CLI-level tests, replacing a per-test /bin/sh script. A compiled binary spawns
// in ~1-2ms versus ~50-80ms for a shell script. It logs each invocation to
// $FAKE_LOG and returns plausible output for the handful of commands the CLI
// tests drive (system dns list, network create, inspect).
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	args := os.Args[1:]
	if logPath := os.Getenv("FAKE_LOG"); logPath != "" {
		if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintln(f, strings.Join(args, " "))
			f.Close()
		}
	}
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	// SYSTEM_STOPPED simulates a stopped runtime for `system status`, but a
	// SYSTEM_START_FLAG file (created by `system start`) flips it to running — so a
	// test can drive the whole auto-start flow: status=stopped → `system start` →
	// status=running → the command proceeds.
	systemRunning := func() bool {
		if os.Getenv("SYSTEM_STOPPED") == "" {
			return true
		}
		flag := os.Getenv("SYSTEM_START_FLAG")
		if flag == "" {
			return false
		}
		_, err := os.Stat(flag)
		return err == nil
	}
	// $STATE_DIR turns on remembering what was deleted, so a test can assert that a
	// thing is *gone* rather than that a delete was requested. Without it every
	// object exists forever, which is what most tests want and all of them used to
	// get — but it makes "did the teardown work" unaskable, and a command whose whole
	// promise is "nothing left" needs to be asked exactly that.
	stateDir := os.Getenv("STATE_DIR")
	gonePath := func(kind, name string) string {
		safe := hex.EncodeToString([]byte(name))
		return filepath.Join(stateDir, "gone-"+kind+"-"+safe)
	}
	// A stop is remembered even without $STATE_DIR (beside the log, which every
	// test sets): stopping is not deleting, so it does not need the opt-in that
	// keeps every object alive forever — and a `stop` the shim forgot would make
	// the caller's "did it stop?" check read every stop as one that failed.
	stopDir := stateDir
	if stopDir == "" && os.Getenv("FAKE_LOG") != "" {
		stopDir = filepath.Dir(os.Getenv("FAKE_LOG"))
	}
	// With neither set there is nowhere to remember a stop; "" makes every
	// marker path relative to nothing, so the helpers below do nothing then.
	stoppedPath := func(name string) string {
		if stopDir == "" {
			return ""
		}
		return filepath.Join(stopDir, "stopped-"+hex.EncodeToString([]byte(name)))
	}
	// The project label a run gave a name, kept where a stop is.
	projectPath := func(name string) string {
		if stopDir == "" {
			return ""
		}
		return filepath.Join(stopDir, "project-"+hex.EncodeToString([]byte(name)))
	}
	// stoppedForStats reports whether inspect would call the container
	// stopped, by the same knobs: $INSPECT_STATE for every container,
	// $INSPECT_STOPPED by name, and a `stop` that took.
	stoppedForStats := func(name string) bool {
		if st := os.Getenv("INSPECT_STATE"); st != "" && st != "running" {
			return true
		}
		if slices.Contains(strings.Fields(os.Getenv("INSPECT_STOPPED")), name) {
			return true
		}
		if p := stoppedPath(name); p != "" {
			if _, err := os.Stat(p); err == nil {
				return true
			}
		}
		return false
	}
	// $DELETE_STICKY names objects whose delete succeeds and yet leaves them there.
	// That is not hypothetical: `container image delete --force` exits 0 for a ref it
	// does not recognise, and a volume held by another container survives its own
	// removal. A teardown that trusted the exit code would report a clean sweep.
	sticky := func(name string) bool {
		for _, m := range strings.Fields(os.Getenv("DELETE_STICKY")) {
			if name == m {
				return true
			}
		}
		return false
	}
	markGone := func(kind, name string) {
		if stateDir != "" && name != "" && !sticky(name) {
			_ = os.WriteFile(gonePath(kind, name), []byte("1"), 0o644)
		}
	}
	isGone := func(kind, name string) bool {
		if stateDir == "" {
			return false
		}
		_, err := os.Stat(gonePath(kind, name))
		return err == nil
	}
	// The object name is the last argument for every delete form the runtime takes
	// (`delete --force NAME`, `volume delete NAME`, …).
	lastArg := func() string {
		if len(args) == 0 {
			return ""
		}
		return args[len(args)-1]
	}
	// $APISERVER_DOWN makes every query fail, the way a dead apiserver does — not
	// just `system status`. Without it a test can only express "the daemon says it
	// is stopped while answering every question", which is not a state that exists
	// and which hides code that treats "couldn't ask" as "nothing there".
	if os.Getenv("APISERVER_DOWN") != "" && arg(0) != "system" {
		fmt.Fprintln(os.Stderr, "Error: failed to connect to apiserver")
		os.Exit(1)
	}
	// $FAKE_EXIT makes a command exit with a code of its own, the way the real
	// runtime hands back a container's (`container run` and `container exec`
	// return 3 for a command that exits 3; measured on container 1.4.1). It is
	// `;`-separated `<part of the argv>=<code>` entries, and the first whose part
	// the space-joined argv contains decides — so a test can fail the one-off's
	// own run and leave a dependency's alone, or the other way round.
	// $FAKE_DIE_SIGNAL is a part of the argv whose command dies of SIGKILL
	// instead, which has no exit code at all.
	joined := strings.Join(args, " ")
	if part := os.Getenv("FAKE_DIE_SIGNAL"); part != "" && strings.Contains(joined, part) {
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		time.Sleep(time.Second)
	}
	for _, entry := range strings.Split(os.Getenv("FAKE_EXIT"), ";") {
		part, code, ok := strings.Cut(entry, "=")
		if !ok || part == "" || !strings.Contains(joined, part) {
			continue
		}
		n, err := strconv.Atoi(code)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fakeshim: FAKE_EXIT entry %q: %v\n", entry, err)
			os.Exit(2)
		}
		os.Exit(n)
	}
	switch arg(0) {
	case "run":
		// A name the runtime would not create is refused before anything else:
		// nothing below is recorded for a container that was never made.
		if msg, code, refused := containerNameRefused(args); refused {
			fmt.Fprintln(os.Stderr, msg)
			os.Exit(code)
		}
		// A volume the runtime would not create is refused after the name, and
		// before anything is recorded (the real run makes no volume at all).
		if msg, refused := volumeNameRefused(args); refused {
			fmt.Fprintln(os.Stderr, msg)
			os.Exit(1)
		}
		// Running a name again means it is there again (the `delete` case) and no
		// longer stopped (the `stop` case), carrying the project label this run
		// gave it (none for a run without one), for a later inspect. `-l` is the
		// spelling opossum passes; `--label` is the long one.
		var runProject string
		for i, a := range args {
			if i > 0 && (args[i-1] == "-l" || args[i-1] == "--label") {
				if v, ok := strings.CutPrefix(a, "opossum.project="); ok {
					runProject = v
				}
			}
		}
		for i, a := range args {
			if i > 0 && args[i-1] == "--name" {
				if stateDir != "" {
					_ = os.Remove(gonePath("container", a))
				}
				if p := stoppedPath(a); p != "" {
					_ = os.Remove(p)
				}
				if p := projectPath(a); p != "" {
					_ = os.WriteFile(p, []byte(runProject), 0o644)
				}
			}
		}
		// A one-off's body: what the container would have written to its
		// stdout, so a test can see which of the CLI's streams it reaches.
		if says := os.Getenv("FAKE_RUN_SAYS"); says != "" {
			fmt.Println(says)
		}
		// A foreground run (no -d) of $RUN_HANG stays attached until this process
		// is killed — the window in which a Ctrl-C lands on an `up --foreground`
		// or a `run`. Long enough for a test to send the signal, short enough
		// that a wiring that never cancels shows up as a slow, wrong answer
		// rather than a hang.
		if hang := os.Getenv("RUN_HANG"); hang != "" {
			detached, named := false, false
			for i, a := range args {
				if a == "-d" {
					detached = true
				}
				if i > 0 && args[i-1] == "--name" && a == hang {
					named = true
				}
			}
			if named && !detached {
				time.Sleep(20 * time.Second)
			}
		}
	case "system":
		if arg(1) == "dns" && arg(2) == "list" {
			fmt.Print("DOMAIN\nopossum\n")
		}
		// `system status` is the daemon-liveness probe; report running (or stopped
		// under SYSTEM_STOPPED until `system start` runs). The running table is
		// container 1.4.1's (the rows under `status` are what it prints; the
		// readers only look for the `status running` row).
		if arg(1) == "status" {
			// With `--format json` the JSON container 1.4.1 prints (the
			// readers prefer it); down, exit 1 and `{"status":"unregistered"}`.
			if arg(2) == "--format" && arg(3) == "json" {
				if systemRunning() {
					fmt.Println(`{"client":{"appName":"container","build":"release","commit":"unspecified","version":"1.4.1"},"host":{"architecture":"arm64","cpus":8,"operatingSystem":"Version 26.6.2 (Build 25G83)"},"paths":{"appRoot":"/Users/<user>/Library/Application Support/com.apple.container/","installRoot":"/opt/homebrew/Cellar/container/1.4.1/"},"resources":{"containersRunning":0,"containersTotal":1,"images":3},"server":{"appName":"container-apiserver","build":"release","commit":"unspecified","version":"1.4.1"},"status":"running"}`)
				} else {
					fmt.Println(`{"status":"unregistered"}`)
					os.Exit(1)
				}
				return
			}
			if systemRunning() {
				fmt.Print("FIELD               VALUE\nstatus              running\nclient.version      1.4.1\nserver.version      1.4.1\npaths.appRoot       /Users/<user>/Library/Application Support/com.apple.container/\ncontainers.total    1\ncontainers.running  0\nimages.total        3\n")
			} else {
				fmt.Print("FIELD  VALUE\nstatus  stopped\n")
			}
		}
		// `system start` starts the runtime: mark it running for subsequent status.
		if arg(1) == "start" {
			if flag := os.Getenv("SYSTEM_START_FLAG"); flag != "" {
				os.WriteFile(flag, []byte("started"), 0o644)
			}
			fmt.Println("started")
		}
	case "network":
		// `network ls --format json` is how doctor finds the networks nothing is
		// running on. $NETWORK_LS is the JSON document to answer with; empty
		// means the machine has only the runtime's own `default`, which is the
		// shape of a machine nobody has left anything on.
		if arg(1) == "ls" {
			// Without `--format json` the real CLI prints a table, and a caller
			// that forgets the flag gets something no JSON parser will read.
			// Printing JSON either way would make forgetting it invisible here
			// and a silent nothing on a real machine.
			if !hasFlag("--format", "json") {
				fmt.Println("NETWORK  SUBNET")
				return
			}
			if doc := os.Getenv("NETWORK_LS"); doc != "" {
				fmt.Println(doc)
			} else {
				fmt.Println(`[{"configuration":{"name":"default","labels":{"com.apple.container.resource.role":"builtin"}}}]`)
			}
			return
		}
		if arg(1) == "create" {
			name := os.Args[len(os.Args)-1]
			if name == "create" || strings.HasPrefix(name, "-") {
				fmt.Fprintln(os.Stderr, "Error: Missing expected argument '<name>'")
				os.Exit(64)
			}
			if !validNetworkName(name) {
				fmt.Fprintf(os.Stderr, "Error: invalid network name: %s\n", name)
				os.Exit(1)
			}
			fmt.Println(name) // the real CLI echoes the network name on success
		}
		// `network inspect` exits non-zero for a network that isn't there, which is
		// how opossum decides whether one exists. $NETWORK_ABSENT lists the ones to
		// report missing; by default every network exists.
		if arg(1) == "delete" {
			markGone("network", arg(2))
		}
		if arg(1) == "inspect" {
			for _, m := range strings.Fields(os.Getenv("NETWORK_ABSENT")) {
				if arg(2) == m {
					fmt.Fprintf(os.Stderr, "Error: network not found: %s\n", arg(2))
					os.Exit(1)
				}
			}
			if isGone("network", arg(2)) {
				fmt.Fprintf(os.Stderr, "Error: network not found: %s\n", arg(2))
				os.Exit(1)
			}
		}
	case "ls":
		// `ls -a --format json` is the container listing. $CONTAINER_LS is the
		// JSON document to answer with; empty means no containers. The `-a` is
		// what makes stopped ones appear, so a caller that drops it gets a
		// listing with only the running ones — the same wrong answer as reading
		// the running attachments, and the reason this shim honours the flag
		// instead of ignoring it.
		if !hasFlag("--format", "json") {
			fmt.Println("ID  IMAGE  OS  ARCH  STATE")
			return
		}
		doc := os.Getenv("CONTAINER_LS")
		if doc == "" {
			doc = "[]"
		}
		if !hasArg("-a") {
			doc = onlyRunning(doc)
		}
		fmt.Println(doc)
	case "volume":
		// `volume ls` is a table whose first column is the name; opossum reads it to
		// decide whether a volume exists. $VOLUME_LS is that table.
		if arg(1) == "delete" {
			// A volume already deleted is not there to delete: the real CLI fails
			// (1.4.1: `Error: failed to delete one or more volumes: ["<name>"]`).
			if isGone("volume", arg(2)) {
				fmt.Fprintf(os.Stderr, "Error: failed to delete one or more volumes: [%q]\n", arg(2))
				os.Exit(1)
			}
			markGone("volume", arg(2))
		}
		// $VOLUME_LS_FAIL makes the listing itself fail, which is a different answer
		// from "no volumes": code that conflates the two reports a volume as surviving
		// a removal that worked. Same knob name as the internal shim.
		if arg(1) == "ls" && os.Getenv("VOLUME_LS_FAIL") != "" {
			fmt.Fprintln(os.Stderr, "Error: failed to list volumes")
			os.Exit(1)
		}
		if arg(1) == "ls" {
			for _, line := range strings.Split(os.Getenv("VOLUME_LS"), "\n") {
				if f := strings.Fields(line); len(f) > 0 && isGone("volume", f[0]) {
					continue
				}
				fmt.Println(line)
			}
		}
	case "stop":
		// The real CLI's exit code answers only whether the name exists (0 for a
		// running, a stopped and an exited container alike; measured on 1.4.1), so
		// a stop that took is visible only through the state a later inspect
		// reports — the marker below, cleared when the name is run again. Same
		// model as the orchestrator's shim.
		if isGone("container", lastArg()) {
			fmt.Fprintf(os.Stderr, "Error: internalError: \"failed to stop container\" (cause: \"notFound: \"container with ID %s not found\"\")\n", lastArg())
			os.Exit(1)
		}
		if p := stoppedPath(lastArg()); p != "" {
			_ = os.WriteFile(p, []byte("1"), 0o644)
		}
	case "delete", "rm":
		if isGone("container", lastArg()) {
			fmt.Fprintf(os.Stderr, "Error: internalError: \"failed to delete container\" (cause: \"notFound: \"container with ID %s not found\"\")\n", lastArg())
			os.Exit(1)
		}
		markGone("container", lastArg())
		if p := stoppedPath(lastArg()); p != "" {
			_ = os.Remove(p)
		}
	case "start":
		// Running again in place: the stop marker is cleared.
		if p := stoppedPath(lastArg()); p != "" {
			_ = os.Remove(p)
		}

	case "logs":
		// One line per container, then, with $LOGS_SLEEP, a stream that stays
		// open that many seconds, as `container logs -f` does (or a long read
		// of a large log without -f).
		// While the stream is open the real CLI catches SIGINT and SIGTERM and
		// exits 130 and 143 of its own (container 1.4.1).
		caught := make(chan os.Signal, 1)
		signal.Notify(caught, syscall.SIGINT, syscall.SIGTERM)
		fmt.Printf("log-line %s\n", args[len(args)-1])
		if n, err := strconv.Atoi(os.Getenv("LOGS_SLEEP")); err == nil {
			select {
			case sig := <-caught:
				if sig == syscall.SIGINT {
					os.Exit(130)
				}
				os.Exit(143)
			case <-time.After(time.Duration(n) * time.Second):
			}
		}
		return
	case "inspect":
		// $INSPECT_STATE overrides the reported state, so a test can stage a
		// container that exited right after starting. The orchestrator's shim has
		// had this knob for a while; without it here, every CLI-level test saw a
		// container that could never be anything but healthy — and a crash-path
		// test would pass while proving nothing.
		// $INSPECT_ABSENT names containers that do not exist: the real CLI exits
		// non-zero for those, which is how opossum tells "stopped" from "never
		// created". A shim that reports every container as present makes any test
		// about absence vacuous.
		for _, m := range strings.Fields(os.Getenv("INSPECT_ABSENT")) {
			if arg(1) == m {
				fmt.Fprintf(os.Stderr, "Error: container not found: %s\n", arg(1))
				os.Exit(1)
			}
		}
		if isGone("container", arg(1)) {
			fmt.Fprintf(os.Stderr, "Error: container not found: %s\n", arg(1))
			os.Exit(1)
		}
		// $INSPECT_STOPPED names individual containers that exist but are not
		// running. $INSPECT_STATE is the blunt version that applies to every
		// container, which cannot express "db is down while web is up" — the shape
		// most of the supervisor's decisions actually turn on.
		// $INSPECT_PROJECT puts an `opossum.project` label on every container, the
		// way a real one carries the label opossum stamped on it at `run` time.
		// Without it, code that checks ownership before acting sees no owner and a
		// test of that check proves nothing. Same knob as the internal shim.
		// Otherwise the label an earlier run of the name carried; then
		// $INSPECT_PROJECT_FROM_NAME and $INSPECT_UNLABELED, as in the internal shim.
		labels := ""
		proj := os.Getenv("INSPECT_PROJECT")
		recorded := false
		if p := projectPath(arg(1)); proj == "" && p != "" {
			if b, err := os.ReadFile(p); err == nil {
				proj, recorded = string(b), true
			}
		}
		if proj == "" && !recorded && os.Getenv("INSPECT_PROJECT_FROM_NAME") != "" {
			if parts := strings.Split(arg(1), "."); len(parts) >= 3 {
				proj = parts[len(parts)-2]
			}
		}
		for _, m := range strings.Fields(os.Getenv("INSPECT_UNLABELED")) {
			if arg(1) == m {
				proj = ""
			}
		}
		if proj != "" {
			labels = `"opossum.project":"` + proj + `"`
		}
		state := os.Getenv("INSPECT_STATE")
		for _, m := range strings.Fields(os.Getenv("INSPECT_STOPPED")) {
			if arg(1) == m {
				state = "stopped"
			}
		}
		// A `stop` that took (the `stop` case) shows as the real CLI shows it: the
		// container is still there, its state is "stopped".
		if p := stoppedPath(arg(1)); p != "" {
			if _, err := os.Stat(p); err == nil {
				state = "stopped"
			}
		}
		if state == "" {
			state = "running"
		}
		fmt.Printf(`[{"status":{"state":"%s","networks":[{"ipv4Address":"192.168.66.9/24"}]},"configuration":{"labels":{%s},"networks":[{"network":"demo-net-configured"}],"publishedPorts":[{"containerPort":80,"hostAddress":"0.0.0.0","hostPort":8080,"proto":"tcp"}]}}]`+"\n", state, labels)
	case "stats":
		// `stats --no-stream --format json <names…>` returns a guest-view JSON array.
		jsonForm := false
		var names []string
		for i, a := range args[1:] {
			switch {
			case a == "json" && args[i] == "--format":
				jsonForm = true
			case strings.HasPrefix(a, "-") || a == "json":
			default:
				names = append(names, a)
			}
		}
		// container 1.3.1: a name that does not exist fails the whole call — no
		// table for the ones that do (measured 2026-09-04, stats-absent-only.txt).
		for _, m := range strings.Fields(os.Getenv("INSPECT_ABSENT")) {
			for _, n := range names {
				if n == m {
					fmt.Fprintf(os.Stderr, "Error: no such container: %s\n", n)
					os.Exit(1)
				}
			}
		}
		if jsonForm {
			var objs []string
			// container 1.4.1 answers in id order, not in the order it was asked.
			slices.Sort(names)
			for _, n := range names {
				// container 1.4.1 prints no entry for a stopped container, and
				// `[]` when every one named is stopped.
				if stoppedForStats(n) {
					continue
				}
				// Every key container 1.4.1 prints, each with its own value, in
				// the runtime's (alphabetical) key order.
				objs = append(objs, fmt.Sprintf(`{"blockReadBytes":8192,"blockWriteBytes":16384,"cpuUsageUsec":1500000,"id":"%s",`+
					`"memoryLimitBytes":1073741824,"memoryUsageBytes":49283072,"networkRxBytes":2048,"networkTxBytes":4096,"numProcesses":3}`, n))
			}
			fmt.Printf("[%s]\n", strings.Join(objs, ","))
		}
	case "image":
		// `image inspect` exits non-zero for an image that isn't there, which is how
		// opossum decides whether a `build:` service still needs building. Without
		// this case the shim answered "every image exists", so `--no-build` could
		// never be seen to refuse — the same shape of hole as a hard-coded state.
		// $IMAGE_ABSENT lists the refs to report missing, matching the shim in
		// internal/orchestrator/testdata.
		if arg(1) == "delete" {
			markGone("image", lastArg())
		}
		if arg(1) == "inspect" {
			for _, m := range strings.Fields(os.Getenv("IMAGE_ABSENT")) {
				if arg(2) == m {
					fmt.Fprintf(os.Stderr, "Error: image not found: %s\n", arg(2))
					os.Exit(1)
				}
			}
			if isGone("image", arg(2)) {
				fmt.Fprintf(os.Stderr, "Error: image not found: %s\n", arg(2))
				os.Exit(1)
			}
		}
	}
}

// onlyRunning drops the stopped entries from a container listing, which is what
// the real `container ls` does without `-a`.
func onlyRunning(doc string) string {
	var entries []map[string]any
	if err := json.Unmarshal([]byte(doc), &entries); err != nil {
		return doc
	}
	kept := []map[string]any{}
	for _, e := range entries {
		st, _ := e["status"].(map[string]any)
		if st != nil && st["state"] == "running" {
			kept = append(kept, e)
		}
	}
	b, err := json.Marshal(kept)
	if err != nil {
		return doc
	}
	return string(b)
}

// hasArg reports whether the invocation carried this word.
func hasArg(want string) bool {
	for _, a := range os.Args[1:] {
		if a == want {
			return true
		}
	}
	return false
}

// hasFlag reports whether the invocation carried `name value`, which is how the
// real CLI takes `--format json`.
func hasFlag(name, value string) bool {
	for i, a := range os.Args[1:] {
		if a == name && i+2 < len(os.Args) && os.Args[i+2] == value {
			return true
		}
	}
	return false
}

// validNetworkName is the rule `container network create` 1.4.1 applies to a
// network's name: lower-case letters, digits, `.`, `_` and `-`, starting and
// ending with a letter or digit, at most 63 characters. Anything else is refused
// with `Error: invalid network name: <name>` and exit 1
// (testdata/real-cli-output.md). A fake that made any name would keep a caller
// that passes one the runtime refuses green. The real CLI takes flags on either
// side of the name; opossum passes the name last, and the fakes read the last
// argument as the name.
func validNetworkName(name string) bool {
	if name == "" || len(name) > 63 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		letterOrDigit := c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
		if !letterOrDigit && (i == 0 || i == len(name)-1 || c != '.' && c != '_' && c != '-') {
			return false
		}
	}
	return true
}

// containerNameRefused answers a `run` whose `--name` container 1.4.1 refuses
// (testdata/real-cli-output.md). The flags are read up to the image — the first
// argument that does not start with `-` — skipping the value of each flag that
// takes one, so a `--name` among the command's own arguments is the process's;
// the value of the last `--name` before the image is the one taken, as the real
// CLI takes the later one. A `--name` with no value, or whose value is `-`
// followed by more, is a missing value (exit 64). A name that is not 2 to 63
// characters starting with a letter or digit and holding only letters, digits,
// `_`, `.` and `-` is `Error: container ID <name> is not a valid container ID`
// (exit 1). refused is false when the run may go ahead.
//
// This reads the shapes opossum passes (`--name <name>` among separate flags).
// Not read the way the real CLI reads them: `--name=<name>`, `-e=<value>`,
// combined short flags (`-it`), `--`, `-h`/`--help`/`--version`, `--debug` (an
// unknown option to `run`), a value starting with `-` for another flag (the
// real CLI calls that flag's value missing, exit 64 — opossum can pass one, as
// `--user -1` from `user: "-1"`, #996), and a flag with no value at the end.
func containerNameRefused(args []string) (msg string, code int, refused bool) {
	const missing = "Error: Missing value for '--name <name>'"
	name, given := "", false
	for i := 1; i < len(args); i++ { // args[0] is "run"
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			break // the image
		}
		if runFlagsWithoutValue[a] {
			continue
		}
		if i+1 == len(args) {
			if a == "--name" {
				return missing, 64, true
			}
			break
		}
		if a == "--name" {
			v := args[i+1]
			if len(v) > 1 && strings.HasPrefix(v, "-") {
				return missing, 64, true
			}
			name, given = v, true
		}
		i++ // the flag's value
	}
	if given && !validContainerName(name) {
		return "Error: container ID " + name + " is not a valid container ID", 1, true
	}
	return "", 0, false
}

// runFlagsWithoutValue are the `container run` flags that take no value
// (`container run --help`, 1.4.1, measured before `--name`); every other flag
// takes the next argument.
var runFlagsWithoutValue = map[string]bool{
	"-d": true, "--detach": true, "-i": true, "--interactive": true, "-t": true, "--tty": true,
	"--init": true, "--no-dns": true, "--read-only": true, "--rm": true, "--remove": true,
	"--rosetta": true, "--ssh": true, "--virtualization": true,
}

// volumeNameRefused answers a `run` with a `-v <source>:<target>` whose source
// container 1.4.1 reads as a volume name and refuses (testdata/real-cli-output.md):
// `Error: invalid volume name '<name>': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$`,
// exit 1, for the first such `-v` before the image. A source holding `/` is a
// path, not a name. A name is refused when its first character is not a letter
// or digit, it holds anything but letters, digits, `_`, `.` and `-`, or it is
// longer than 255 characters — so an empty source, which the runtime makes an
// anonymous volume of, has nothing refused.
//
// This reads the shapes opossum passes (`-v <absolute path or volume>:<target>`,
// with the compose mode such as `:ro` after it when there is one), walking the
// flags the way containerNameRefused does
// and with the same forms left unread. Also not read: `--volume`, `--mount`, and
// a `-v` value with no `:`.
func volumeNameRefused(args []string) (msg string, refused bool) {
	for i := 1; i < len(args); i++ { // args[0] is "run"
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			break // the image
		}
		if runFlagsWithoutValue[a] {
			continue
		}
		if i+1 == len(args) {
			break
		}
		if a == "-v" {
			src, _, ok := strings.Cut(args[i+1], ":")
			if ok && !strings.Contains(src, "/") && !validVolumeName(src) {
				return "Error: invalid volume name '" + src + "': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*$", true
			}
		}
		i++ // the flag's value
	}
	return "", false
}

func validVolumeName(name string) bool {
	if len(name) > 255 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || c != '_' && c != '.' && c != '-') {
			return false
		}
	}
	return true
}

func validContainerName(name string) bool {
	if len(name) < 2 || len(name) > 63 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || c != '_' && c != '.' && c != '-') {
			return false
		}
	}
	return true
}
