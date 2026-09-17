// Command fakeshim is a compiled stand-in for the `container` CLI used by the
// orchestrator tests. It's a faithful port of the shell shim fakeShim used to
// write per-test — but compiled once and reused, so each test spawns a ~1-2ms
// binary instead of a ~50-80ms /bin/sh, which dominated the suite's runtime.
//
// It logs each invocation's arguments (space-joined) to $FAKE_LOG and returns
// output shaped like the real CLI. Behaviour is steered entirely through the
// environment (FAKE_LOG, STATE_DIR, DELETE_STICKY, STOP_FAIL, INSPECT_STATE, INSPECT_STOPPED, INSPECT_OWNER, INSPECT_FAIL, LOGS_FAIL, STATS_FAIL, INSPECT_FAIL_ONCE_STOP_ASKED, INSPECT_FAIL_ONCE_GONE, INSPECT_FAIL_ONCE_GONE_ALL,
// INSPECT_ABSENT, NET_EXISTS, NET_CREATE_{HANG,FAIL}, NETWORK_ABSENT, BUILD_{HANG,FAIL,FAIL_STDERR}, RUN_FAIL,
// RUN_IMAGE_FETCH_FAIL, RUN_IMAGE_FETCH_REASON, RUN_IMAGE_FETCH_URL, RUN_IMAGE_FETCH_TRUNCATED,
// RUN_FAIL_STDERR,
// RUN_HANG, RUN_DIE_SIGNAL, RUN_EXISTS[_WORDING|_HASH], RUN_EXISTS_ANY, HEALTH_*,
// VOLUME_*, LS_*,
// IMAGE_ABSENT, INSPECT_HANG_WHILE, INSPECT_ANSWERED, INSPECT_GATE,
// LOGS_EMPTY[_AFTER], LOGS_FAIL_N, LOGS_DRIP[_AFTER], LOGS_SLEEP, LOGS_SELF_INT,
// LOGS_TEXT, STOP_THEN_SLEEP_MS, START_THEN_SLEEP_MS, SLOW_ONLY),
// so tests need no t.Setenv and stay
// isolated: the orchestrator passes these per-Runtime via RunOptions-style Env.
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

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	// Log the invocation (args space-joined), matching the old `echo "$*"`.
	if logPath := os.Getenv("FAKE_LOG"); logPath != "" {
		if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintln(f, strings.Join(args, " "))
			f.Close()
		}
	}
	if len(args) == 0 {
		return 0
	}
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}

	// SYSTEM_STOPPED simulates an installed-but-stopped runtime: with the daemon
	// down, EVERY invocation fails (this is what makes `inspect` fail and, absent
	// the liveness probe, drove the empty-`ps`/`PRESENT=no` bug). Modelling the
	// whole-runtime failure — not just `system status` — keeps the fake faithful.
	if os.Getenv("SYSTEM_STOPPED") != "" {
		fmt.Fprintln(os.Stderr, "Error: the container system is not running")
		return 1
	}

	switch args[0] {
	case "system":
		// `system status` is the daemon-liveness probe (Ps/Images call it before
		// rendering); report running (the stopped case is handled above).
		if arg(1) == "status" {
			if arg(2) == "--format" && arg(3) == "json" {
				fmt.Println(`{"client":{"version":"1.4.1"},"resources":{"containersRunning":0,"containersTotal":1,"images":3},"server":{"version":"1.4.1"},"status":"running"}`)
			} else {
				fmt.Println("status running")
			}
		}
	case "stop":
		// The real CLI's exit code answers only whether the name exists: 0 for a
		// running, a stopped and an exited container alike, 1 (`notFound`) when
		// there is no such container — measured on 1.4.1. So a stop that "worked"
		// is only visible through the state a later inspect reports, which is what
		// the marker below is for; the `run` case clears it when the name is made
		// again. $STOP_FAIL names containers whose stop returns 0 and yet leaves
		// them running — a defensive knob: no real form of it was found on 1.4.1
		// (eight shapes tried), so an eval built on it says nothing about the
		// runtime, only about a caller that trusted the exit code.
		if dir := os.Getenv("STATE_DIR"); dir != "" && len(args) > 0 {
			name := args[len(args)-1]
			_ = os.WriteFile(stopAskedPath(dir, name), []byte("1"), 0o644)
			if _, err := os.Stat(gonePath(dir, name)); err == nil {
				fmt.Fprintf(os.Stderr, "Error: internalError: \"failed to stop container\" (cause: \"notFound: \"container with ID %s not found\"\")\n", name)
				return 1
			}
			for _, m := range strings.Fields(os.Getenv("STOP_FAIL")) {
				if name == m {
					return 0
				}
			}
			_ = os.WriteFile(stoppedPath(dir, name), []byte("1"), 0o644)
			// $STOP_THEN_SLEEP_MS keeps the stop from returning that long after
			// the container is stopped — a stop of several services, or a slow
			// one, that leaves this container stopped for a while. With
			// $SLOW_ONLY it is that container's stop alone.
			if ms, err := strconv.Atoi(os.Getenv("STOP_THEN_SLEEP_MS")); err == nil && slowHere(name) {
				time.Sleep(time.Duration(ms) * time.Millisecond)
			}
		}

	case "start":
		// A container that is not there cannot be started: the real CLI exits 1
		// saying so — whether it was never made ($INSPECT_ABSENT) or deleted
		// (the gone marker).
		if len(args) > 0 && !there(args[len(args)-1]) {
			fmt.Fprintf(os.Stderr, "Error: get failed: container %s not found\n", args[len(args)-1])
			return 1
		}
		// Starting a stopped container in place: it is running again (the marker
		// the `stop` case left is cleared), as the real CLI reports it.
		if dir := os.Getenv("STATE_DIR"); dir != "" && len(args) > 0 {
			_ = os.Remove(stoppedPath(dir, args[len(args)-1]))
			// $START_THEN_SLEEP_MS keeps the start from returning that long
			// after — a slow start that keeps the containers started after it
			// stopped a while longer. $SLOW_ONLY narrows it as for the stop.
			if ms, err := strconv.Atoi(os.Getenv("START_THEN_SLEEP_MS")); err == nil && slowHere(args[len(args)-1]) {
				time.Sleep(time.Duration(ms) * time.Millisecond)
			}
		}

	case "delete", "rm":
		// Remember it as gone, so a later `inspect` can answer "not there". Gated on
		// $STATE_DIR, which the orchestrator tests' fakeShim helper always sets — so
		// in this package deletion is modelled everywhere, not opt-in. Without it
		// every container would exist forever, and "did the teardown work?" could not
		// be asked at all.
		// $DELETE_STICKY names containers whose delete succeeds and yet leaves them
		// there — a teardown that trusted the exit code would report them gone. Same
		// knob and meaning as the shim in cmd/opossum/testdata.
		if dir := os.Getenv("STATE_DIR"); dir != "" && len(args) > 0 {
			name := args[len(args)-1]
			// Deleting what is not there is the one failure the real CLI reports
			// (rc 1, `notFound`) — measured on 1.4.1.
			if _, err := os.Stat(gonePath(dir, name)); err == nil {
				fmt.Fprintf(os.Stderr, "Error: internalError: \"failed to delete container\" (cause: \"notFound: \"container with ID %s not found\"\")\n", name)
				return 1
			}
			sticky := false
			for _, m := range strings.Fields(os.Getenv("DELETE_STICKY")) {
				if name == m {
					sticky = true
				}
			}
			if !sticky {
				_ = os.WriteFile(gonePath(dir, name), []byte("1"), 0o644)
				_ = os.Remove(stoppedPath(dir, name))
			}
		}

	case "inspect":
		// $INSPECT_FAIL names containers the runtime cannot answer about at all
		// — the CLI fails for a reason that is not "not found" (the apiserver is
		// down). Different from absent: a caller must not read it as "gone".
		for _, m := range strings.Fields(os.Getenv("INSPECT_FAIL")) {
			if arg(1) == m {
				fmt.Fprintln(os.Stderr, "Error: apiserver is not running")
				return 1
			}
		}
		// $INSPECT_HANG_WHILE is a file: while it exists, an inspect does not
		// return (up to 20 s) — a runtime that has stopped answering at all.
		// $INSPECT_GATE is a directory: each inspect writes a file named after
		// its process there and waits (up to 20 s) for one of the same name with
		// `-go` appended, so a test can hold the looks and let them answer one
		// by one — and see which ones it never let through.
		if d := os.Getenv("INSPECT_GATE"); d != "" {
			p := filepath.Join(d, strconv.Itoa(os.Getpid())+"-"+strconv.FormatInt(time.Now().UnixNano(), 10))
			_ = os.WriteFile(p, nil, 0o644)
			for i := 0; i < 1000; i++ {
				if _, err := os.Stat(p + "-go"); err == nil {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		if f := os.Getenv("INSPECT_HANG_WHILE"); f != "" {
			for i := 0; i < 1000; i++ {
				if _, err := os.Stat(f); err != nil {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		// $INSPECT_ANSWERED is a file every inspect held by either appends a
		// line to as it answers, so a test can tell whether one was still
		// running.
		if a := os.Getenv("INSPECT_ANSWERED"); a != "" && (os.Getenv("INSPECT_HANG_WHILE") != "" || os.Getenv("INSPECT_GATE") != "") {
			if af, err := os.OpenFile(a, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				fmt.Fprintln(af, "answered")
				af.Close()
			}
		}
		// $INSPECT_FAIL_WHILE is a file: while it exists, every inspect fails
		// that way — a runtime that stops answering for a while, and then answers
		// again.
		if f := os.Getenv("INSPECT_FAIL_WHILE"); f != "" {
			if _, err := os.Stat(f); err == nil {
				fmt.Fprintln(os.Stderr, "Error: apiserver is not running")
				return 1
			}
		}
		// $INSPECT_FAIL_ONCE_GONE is the same failure, but only after the container
		// was deleted — a runtime that stops answering during a teardown, while
		// the checks before it (ownership, presence) still got answers.
		if dir := os.Getenv("STATE_DIR"); dir != "" {
			// $INSPECT_FAIL_ONCE_STOP_ASKED: the same failure, but only after a `stop`
			// was issued for the container — a runtime that stops answering while a
			// run is being interrupted, after the checks before the run got answers.
			for _, m := range strings.Fields(os.Getenv("INSPECT_FAIL_ONCE_STOP_ASKED")) {
				if _, err := os.Stat(stopAskedPath(dir, arg(1))); arg(1) == m && err == nil {
					fmt.Fprintln(os.Stderr, "Error: apiserver is not running")
					return 1
				}
			}
			for _, m := range strings.Fields(os.Getenv("INSPECT_FAIL_ONCE_GONE")) {
				if _, err := os.Stat(gonePath(dir, arg(1))); arg(1) == m && err == nil {
					fmt.Fprintln(os.Stderr, "Error: apiserver is not running")
					return 1
				}
			}
			// $INSPECT_FAIL_ONCE_GONE_ALL: once the named container has been deleted,
			// every inspect fails — the runtime dying in the middle of a teardown,
			// after the removals and before the checks that follow them.
			for _, m := range strings.Fields(os.Getenv("INSPECT_FAIL_ONCE_GONE_ALL")) {
				if _, err := os.Stat(gonePath(dir, m)); err == nil {
					fmt.Fprintln(os.Stderr, "Error: apiserver is not running")
					return 1
				}
			}
		}
		// Gone until something creates it again (see the `run` case).
		if dir := os.Getenv("STATE_DIR"); dir != "" {
			if _, err := os.Stat(gonePath(dir, arg(1))); err == nil {
				fmt.Fprintf(os.Stderr, "Error: container not found: %s\n", arg(1))
				return 1
			}
		}
		// Build the labels object from INSPECT_PROJECT and any recorded config-hash.
		var labels []string
		project := os.Getenv("INSPECT_PROJECT")
		// $INSPECT_OWNER gives single containers their own project label
		// (`name=project` pairs), over $INSPECT_PROJECT, which labels every one.
		for _, pair := range strings.Fields(os.Getenv("INSPECT_OWNER")) {
			if name, p, ok := strings.Cut(pair, "="); ok && name == arg(1) {
				project = p
			}
		}
		// Otherwise the label an earlier run of the name carried, if one was run.
		// $INSPECT_PROJECT_FROM_NAME: a container never run here, named
		// <service>.<project>.<domain>, carries that project's label, as every
		// container opossum makes does. $INSPECT_UNLABELED names containers that
		// carry no label at all.
		recorded := false
		if dir := os.Getenv("STATE_DIR"); project == "" && dir != "" {
			if b, err := os.ReadFile(filepath.Join(dir, arg(1)+".project")); err == nil {
				project, recorded = string(b), true
			}
		}
		if project == "" && !recorded && os.Getenv("INSPECT_PROJECT_FROM_NAME") != "" {
			if parts := strings.Split(arg(1), "."); len(parts) >= 3 {
				project = parts[len(parts)-2]
			}
		}
		for _, m := range strings.Fields(os.Getenv("INSPECT_UNLABELED")) {
			if arg(1) == m {
				project = ""
			}
		}
		if project != "" {
			labels = append(labels, `"opossum.project":"`+project+`"`)
		}
		if dir := os.Getenv("STATE_DIR"); dir != "" {
			if h, err := os.ReadFile(filepath.Join(dir, arg(1)+".hash")); err == nil {
				labels = append(labels, `"opossum.config-hash":"`+strings.TrimSpace(string(h))+`"`)
			}
		}
		// $INSPECT_ABSENT names containers that do not exist: the real CLI exits
		// non-zero for those, which is how opossum tells "stopped" from "never
		// created". Same knob and meaning as the shim in cmd/opossum/testdata.
		for _, m := range strings.Fields(os.Getenv("INSPECT_ABSENT")) {
			if arg(1) == m {
				fmt.Fprintf(os.Stderr, "Error: container not found: %s\n", arg(1))
				return 1
			}
		}
		// $INSPECT_STOPPED names individual containers that exist but are not
		// running — $INSPECT_STATE is the blunt version that applies to every
		// container and cannot express "db is down while web is up". Same knob and
		// meaning as the shim in cmd/opossum/testdata.
		state := os.Getenv("INSPECT_STATE")
		for _, m := range strings.Fields(os.Getenv("INSPECT_STOPPED")) {
			if arg(1) == m {
				state = "stopped"
			}
		}
		// A `stop` that took (see the `stop` case) shows here as the real CLI
		// shows it: the container is still there, its state is "stopped".
		if dir := os.Getenv("STATE_DIR"); dir != "" {
			if _, err := os.Stat(stoppedPath(dir, arg(1))); err == nil {
				state = "stopped"
			}
		}
		if state == "" {
			state = "running"
		}
		fmt.Printf(`[{"status":{"state":"%s","networks":[{"network":"n","ipv4Address":"192.168.64.10/24","ipv4Gateway":"192.168.64.1"}]},"configuration":{"labels":{%s},"publishedPorts":[%s]}}]`+"\n",
			state, strings.Join(labels, ","), publishedPorts(arg(1)))

	case "network":
		// `network inspect` exits non-zero for a network that isn't there, which is
		// how opossum decides whether one exists. $NETWORK_ABSENT lists the ones to
		// report missing; same knob as the shim in cmd/opossum/testdata.
		if arg(1) == "inspect" {
			for _, m := range strings.Fields(os.Getenv("NETWORK_ABSENT")) {
				if arg(2) == m {
					fmt.Fprintf(os.Stderr, "Error: network not found: %s\n", arg(2))
					return 1
				}
			}
			// $NETWORK_SUBNETS gives an existing network its subnets, as
			// `name=v4[,v6]` pairs, in the shape the real `network inspect`
			// prints them (1.4.1: `configuration.ipv4Subnet` when created with
			// `--subnet`, and `status.ipv4Subnet` / `ipv6Subnet` — the latter
			// written with the gateway's address). Without it the inspect answers
			// nothing, as the shim always did, which opossum reads as "unknown".
			for _, pair := range strings.Fields(os.Getenv("NETWORK_SUBNETS")) {
				name, subs, ok := strings.Cut(pair, "=")
				if !ok || name != arg(2) {
					continue
				}
				v4, v6, _ := strings.Cut(subs, ",")
				conf := fmt.Sprintf(`"ipv4Subnet":%q`, v4)
				status := fmt.Sprintf(`"ipv4Gateway":"10.0.0.1","ipv4Subnet":%q`, v4)
				if v6 != "" {
					conf += fmt.Sprintf(`,"ipv6Subnet":%q`, v6)
					status += fmt.Sprintf(`,"ipv6Subnet":%q`, strings.Replace(v6, "::/", "::1/", 1))
				}
				fmt.Printf(`[{"configuration":{"name":%q,%s},"status":{%s}}]`+"\n", name, conf, status)
			}
		}
		if arg(1) == "create" {
			name := os.Args[len(os.Args)-1]
			if name == "create" || strings.HasPrefix(name, "-") {
				fmt.Fprintln(os.Stderr, "Error: Missing expected argument '<name>'")
				return 64
			}
			if !validNetworkName(name) {
				fmt.Fprintf(os.Stderr, "Error: invalid network name: %s\n", name)
				return 1
			}
			if os.Getenv("NET_EXISTS") != "" {
				fmt.Fprintf(os.Stderr, "Error: network %s already exists\n", name)
				return 1
			}
			// $NET_CREATE_HANG holds the create until this process is killed — a
			// Ctrl-C landing while the runtime is still making the network.
			if os.Getenv("NET_CREATE_HANG") != "" {
				time.Sleep(30 * time.Second)
			}
			// $NET_CREATE_FAIL refuses it the way a sick runtime would.
			if os.Getenv("NET_CREATE_FAIL") != "" {
				fmt.Fprintln(os.Stderr, "Error: internalError: \"failed to create network\"")
				return 1
			}
			fmt.Println(name) // real CLI echoes the network name on success
		}

	case "build":
		// $BUILD_HANG holds the build until this process is killed (a Ctrl-C
		// mid-build); $BUILD_FAIL fails it the way the builder does.
		if os.Getenv("BUILD_HANG") != "" {
			time.Sleep(30 * time.Second)
		}
		// $BUILD_FAIL_STDERR is what it says on the way out instead: the runtime's
		// closing line for some other failure, given whole by the test that
		// wants it (a registry refusing an image, read out of a capture).
		if os.Getenv("BUILD_FAIL") != "" {
			// The shape of the closing line container 1.4.1 writes for a step
			// that exits non-zero. The capture it is taken from
			// (testdata/error-wordings/build-image-refused-141.txt) holds it
			// for another command and another exit code; the command and the
			// code here are this fake's own.
			out := "Error: unknown: \"failed to solve: process \"/bin/sh -c false\" did not complete successfully: exit code: 1\""
			if s := os.Getenv("BUILD_FAIL_STDERR"); s != "" {
				out = s
			}
			fmt.Fprintln(os.Stderr, out)
			return 1
		}

	case "run":
		// A name the runtime would not create is refused before anything else:
		// nothing below is recorded for a container that was never made.
		if msg, code, refused := containerNameRefused(args); refused {
			fmt.Fprintln(os.Stderr, msg)
			return code
		}
		// A run of $RUN_EXISTS is refused the way container 1.4.1 refuses a name
		// that is taken (the same sentence whether the holder runs or is stopped),
		// and records nothing — it comes first so that nothing below (the hash, the
		// ports, the gone marker) is written for a container this run never made;
		// what an inspect then reports is whatever an earlier run, or a test, put
		// in $STATE_DIR.
		if taken := os.Getenv("RUN_EXISTS"); taken != "" {
			for i, a := range args {
				if i > 0 && args[i-1] == "--name" && a == taken {
					// The holder of the name is there to be inspected (a delete just
					// before this run marked the name gone; whoever took it since has
					// un-gone it), with whatever hash and ports were recorded earlier.
					if dir := os.Getenv("STATE_DIR"); dir != "" {
						_ = os.Remove(gonePath(dir, taken))
						// What the holder was made with, if the test says: a hash of its
						// own, or "none" for a container made by hand (no opossum labels).
						// Unset, the holder inspects with whatever the earlier run recorded —
						// another opossum starting the same compose file.
						switch h := os.Getenv("RUN_EXISTS_HASH"); h {
						case "":
						case "none":
							_ = os.Remove(filepath.Join(dir, taken+".hash"))
						default:
							_ = os.WriteFile(filepath.Join(dir, taken+".hash"), []byte(h), 0o644)
						}
					}
					// Two spellings on 1.4.1: the holder fully there, or (with
					// RUN_EXISTS_WORDING=race) still being created.
					if os.Getenv("RUN_EXISTS_WORDING") == "race" {
						fmt.Fprintf(os.Stderr, "Error: container already exists: %s\n", taken)
					} else {
						fmt.Fprintf(os.Stderr, "Error: container with id %s already exists\n", taken)
					}
					return 1
				}
			}
		}
		// $RUN_EXISTS_ANY refuses every run with the sentence naming that one
		// container — what a caller sees when the refusal is about some other name.
		if taken := os.Getenv("RUN_EXISTS_ANY"); taken != "" {
			fmt.Fprintf(os.Stderr, "Error: container with id %s already exists\n", taken)
			return 1
		}
		// A volume the runtime would not create is refused after the name (a taken
		// name is said first on 1.4.1) and before anything is recorded: the real
		// run makes no volume, not even a valid one before it.
		if msg, refused := volumeNameRefused(args); refused {
			fmt.Fprintln(os.Stderr, msg)
			return 1
		}
		// $RUN_IMAGE_FETCH_FAIL names images the registry will not hand over:
		// the run fails before any container is made. The line it writes is
		// the one from the capture the row names ($RUN_IMAGE_FETCH_URL and
		// $RUN_IMAGE_FETCH_REASON), so what is read here is what the runtime
		// really wrote — the captures are in testdata/error-wordings. The progress
		// lines come first, as they do there, and the `Error:` line last: a
		// fake that wrote only the last line would let code that reads the
		// whole of stderr pass here and fail on the real thing.
		if img := os.Getenv("RUN_IMAGE_FETCH_FAIL"); img != "" {
			for _, a := range args {
				if a != img {
					continue
				}
				// $RUN_IMAGE_FETCH_URL is the request the runtime says it
				// made, copied from a capture. Left out, the fake would have
				// to work the URL out from the image name, and what it made
				// up did not match any capture — one line said the request
				// went to one registry while its own reason named another.
				url := os.Getenv("RUN_IMAGE_FETCH_URL")
				fmt.Fprintln(os.Stderr, "[0/6] [0s]")
				fmt.Fprintln(os.Stderr, "[1/6] Fetching image [0s]")
				if strings.Contains(os.Getenv("RUN_IMAGE_FETCH_URL"), "/blobs/") || os.Getenv("RUN_IMAGE_FETCH_TRUNCATED") != "" {
					fmt.Fprintln(os.Stderr, "[1/6] Fetching image (4 blobs) [0s]")
				}
				// $RUN_IMAGE_FETCH_TRUNCATED is the shape that names no
				// request: the blob's download ends early and the runtime says
				// only that (measured).
				if os.Getenv("RUN_IMAGE_FETCH_TRUNCATED") != "" {
					fmt.Fprintln(os.Stderr, "Error: stream ended at an unexpected time")
					return 1
				}
				fmt.Fprintf(os.Stderr, "Error: HTTP request to %s failed with response: %s\n",
					url, os.Getenv("RUN_IMAGE_FETCH_REASON"))
				return 1
			}
		}
		// Creating it again means it is no longer gone (see the `delete` case),
		// and no longer stopped (see the `stop` case).
		if dir := os.Getenv("STATE_DIR"); dir != "" {
			for k, a := range args {
				if k > 0 && args[k-1] == "--name" {
					_ = os.Remove(gonePath(dir, a))
					_ = os.Remove(stoppedPath(dir, a))
				}
			}
		}
		// A `-v name:/path` mount brings the volume into being — the real runtime
		// creates it here, which is why opossum never issues a `volume create`.
		// Recording it is what lets `volume ls` answer for what opossum has made,
		// so a second `up` can be asked whether it left the first one's volume alone.
		if dir := os.Getenv("STATE_DIR"); dir != "" {
			for k, a := range args {
				if k == 0 || args[k-1] != "-v" {
					continue
				}
				// name:/target[:opts]. A bind mount's source is a path, not a volume,
				// and the runtime does not create anything for it.
				name, _, ok := strings.Cut(a, ":")
				if !ok || name == "" || strings.HasPrefix(name, "/") || strings.HasPrefix(name, ".") {
					continue
				}
				_ = os.WriteFile(volumePath(dir, name), []byte(name), 0o644)
				_ = os.Remove(goneVolumePath(dir, name))
			}
		}
		// Record the config-hash (from -l opossum.config-hash=…) keyed by --name,
		// so a later inspect reports it and up-idempotency evals can detect it.
		if dir := os.Getenv("STATE_DIR"); dir != "" {
			var cname, chash string
			for i, a := range args {
				if i > 0 && args[i-1] == "--name" {
					cname = a
				}
				if v, ok := strings.CutPrefix(a, "opossum.config-hash="); ok {
					chash = v
				}
			}
			if cname != "" && chash != "" {
				os.WriteFile(filepath.Join(dir, cname+".hash"), []byte(chash), 0o644)
			}
			// Record the project label the run carried (empty for a run without
			// one), so a later inspect reports what the container was made with.
			// `-l` is the spelling opossum passes; `--label` is the long one.
			if cname != "" {
				var proj string
				for i, a := range args {
					if i > 0 && (args[i-1] == "-l" || args[i-1] == "--label") {
						if v, ok := strings.CutPrefix(a, "opossum.project="); ok {
							proj = v
						}
					}
				}
				os.WriteFile(filepath.Join(dir, cname+".project"), []byte(proj), 0o644)
			}
			// Record what was actually published, so a later inspect reports the
			// real mapping. Without this the fake always claims 8080:8080 and no
			// test can express "last time we published on <some other port>",
			// which is the only interesting input to port stickiness.
			if cname != "" {
				var pub []string
				for i, a := range args {
					if i > 0 && args[i-1] == "-p" {
						pub = append(pub, a)
					}
				}
				os.WriteFile(filepath.Join(dir, cname+".ports"), []byte(strings.Join(pub, ",")), 0o644)
			}
		}
		// $SEED_FAIL makes the seeding container fail the way an image with no shell
		// does: the runtime cannot start the process, so nothing is copied. The seed
		// run could be failed by RUN_FAIL on its name too; this knob exists to reproduce the real runtime's nested wording below.
		if os.Getenv("SEED_FAIL") != "" {
			for _, a := range args {
				if strings.Contains(a, "/__opossum_seed__") {
					// The real runtime's shape, captured from `container` 1.1.0: the
					// same sentence nested four deep inside quoted `internalError:`
					// wrappers, behind two container UUIDs. A flat one-liner here would
					// let the code that digs the reason out of this be deleted without
					// any test noticing.
					fmt.Fprintln(os.Stderr, `Error: failed to start process 2eb0ceac-5ba0-434f-b339-62f39be9203b in container `+
						`2eb0ceac-5ba0-434f-b339-62f39be9203b (cause: "internalError: "failed to start process `+
						`(cause: "internalError: "startProcess: failed to start process: internalError: "vmexec error: `+
						`internalError: "failed to find target executable sh"""")"")`)
					return 1
				}
			}
		}
		// $SEED_NONROOT makes the image declare a non-root `USER`, the way node:*
		// and most database images do. A fresh volume's root belongs to 0:0 and is
		// mode 755, so a seed that does not ask for root cannot create anything in
		// it — measured on the real runtime, where the copy wrote nothing and the
		// volume came up empty. Asking for root (`--user 0`) makes it succeed, so
		// this models the difference the flag actually makes rather than its
		// presence in the argv.
		if os.Getenv("SEED_NONROOT") != "" {
			seed, root := false, false
			for i, a := range args {
				if strings.Contains(a, "/__opossum_seed__") {
					seed = true
				}
				if i > 0 && args[i-1] == "--user" && (a == "0" || a == "root" || a == "0:0") {
					root = true
				}
			}
			if seed && !root {
				// busybox `cp`'s wording, captured from the same run.
				fmt.Fprintln(os.Stderr, `cp: can't create '/__opossum_seed__/./rootfile': Permission denied`)
				return 1
			}
		}
		// The read-only look inside an existing volume (VolumeEntries). $LOOK_ENTRIES
		// is what it holds, space-separated; $LOOK_FAIL makes the look fail the way a
		// shell-less image or a busy volume does, which must read as "unknown", never
		// as "empty". `.` and `..` are printed because a real `ls -a` prints them and
		// the parser has to drop them.
		for _, a := range args {
			if !strings.Contains(a, "/__opossum_look__") {
				continue
			}
			if os.Getenv("LOOK_FAIL") != "" {
				fmt.Fprintln(os.Stderr, "Error: failed to start process (cause: \"internalError: \"failed to find target executable sh\"\")")
				return 1
			}
			// Progress goes to stderr, exactly as the real runtime does — a caller that
			// folded the two streams together would read this as volume contents.
			fmt.Fprintln(os.Stderr, "[6/6] Starting container [0s]")
			fmt.Println(".")
			fmt.Println("..")
			for _, e := range strings.Fields(os.Getenv("LOOK_ENTRIES")) {
				fmt.Println(e)
			}
			return 0
		}
		// $SEED_COPY_FAIL is the other half of SEED_FAIL: the container starts and
		// the copy itself fails. The distinction matters because the two used to be
		// indistinguishable from outside — the script ended in `|| true`, so only a
		// container that could not start at all was ever reported. The message is the
		// busybox wording captured from the non-root run above; the shim has no
		// filesystem of its own to fail on, so it stands in for any copy that starts
		// and cannot finish.
		//
		// Note what this shim cannot model: it never runs the seed script, it decides
		// an exit code from the argv. So it says nothing about whether that script
		// still reports its own failures — only the runtime package's real-`sh` eval
		// can see that. A test here that reads as if it guarded the script would be
		// claiming a coverage the shim structurally cannot provide.
		if os.Getenv("SEED_COPY_FAIL") != "" {
			for _, a := range args {
				if strings.Contains(a, "/__opossum_seed__") {
					fmt.Fprintln(os.Stderr, `cp: can't create '/__opossum_seed__/./rootfile': Permission denied`)
					return 1
				}
			}
		}
		// A foreground run of $RUN_FAIL exits non-zero (drives failure evals).
		// $RUN_FAIL_STDERR is what it writes on the way out — the container's
		// own output, which a foreground `up` captures along with the
		// runtime's.
		if fail := os.Getenv("RUN_FAIL"); fail != "" {
			for i, a := range args {
				if i > 0 && args[i-1] == "--name" && a == fail {
					if out := os.Getenv("RUN_FAIL_STDERR"); out != "" {
						fmt.Fprint(os.Stderr, out)
					}
					return 1
				}
			}
		}
		// A foreground run of $RUN_DIE_SIGNAL dies of a signal on its own — what a
		// kill from outside, not a Ctrl-C, looks like: the same "signal: killed"
		// error, with nothing cancelled.
		if die := os.Getenv("RUN_DIE_SIGNAL"); die != "" {
			detached := false
			for i, a := range args {
				if a == "-d" {
					detached = true
				}
				if i > 0 && args[i-1] == "--name" && a == die && !detached {
					_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
				}
			}
		}
		// A foreground run (no -d) of $RUN_HANG stays attached the way the real
		// runtime does until the container exits — here, until the orchestrator's
		// cancelled context kills this process. That is what a Ctrl-C during
		// `up --foreground` looks like from the orchestrator's side: the run
		// returns "signal: killed", not an exit status of the container's own.
		if hang := os.Getenv("RUN_HANG"); hang != "" {
			detached := false
			named := false
			for i, a := range args {
				if a == "-d" {
					detached = true
				}
				if i > 0 && args[i-1] == "--name" && a == hang {
					named = true
				}
			}
			if named && !detached {
				time.Sleep(30 * time.Second)
			}
		}

	case "exec":
		if os.Getenv("HEALTH_HANG") != "" {
			time.Sleep(30 * time.Second) // never returns within the probe timeout
		}
		counter := os.Getenv("HEALTH_COUNTER")
		n := 0
		if b, err := os.ReadFile(counter); err == nil {
			fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &n)
		}
		n++
		os.WriteFile(counter, []byte(fmt.Sprintf("%d", n)), 0o644)
		okAt := 1
		if v := os.Getenv("HEALTH_OK_AT"); v != "" {
			fmt.Sscanf(v, "%d", &okAt)
		}
		if n < okAt {
			return 1
		}

	case "volume":
		if os.Getenv("VOLUME_LS_FAIL") != "" {
			return 1
		}
		switch arg(1) {
		case "ls":
			// $VOLUME_LS_TABLE is the whole table the real CLI prints — a NAME /
			// TYPE / DRIVER / OPTIONS header and one row per volume — for a test
			// that reads more than the name (the driver). The volumes opossum has
			// made since are rows too, as `named local`, the way the runtime would
			// list them. Same idea as $CONTAINER_LS: a document, not a list of
			// names, when the shape of the row is what the code under test reads.
			if table := os.Getenv("VOLUME_LS_TABLE"); table != "" {
				fmt.Println(table)
				listed := map[string]bool{}
				for _, line := range strings.Split(table, "\n") {
					if f := strings.Fields(line); len(f) > 0 {
						listed[f[0]] = true
					}
				}
				for _, v := range madeVolumes() {
					if !listed[v] {
						fmt.Printf("%s  named  local\n", v)
					}
				}
				return 0
			}
			// One name per line, as the real `container volume ls` prints them. A
			// single line holding several names would answer "exists" only for the
			// first — the parser reads the first field of each line — and a test that
			// listed three volumes would silently be testing one.
			//
			// $VOLUME_LS is what existed before this test started; on top of it come
			// the volumes opossum has made since, which the real runtime would list
			// too. Without that half, "a volume opossum created last time is still
			// there this time" can only be asserted by a test asserting its own
			// setup — which says nothing about whether opossum created anything.
			seen := map[string]bool{}
			for _, v := range append(strings.Fields(os.Getenv("VOLUME_LS")), madeVolumes()...) {
				if seen[v] || volumeGone(v) {
					continue
				}
				seen[v] = true
				fmt.Println(v)
			}
		case "delete", "rm":
			// `down -v` removes them, and then they are gone: the next `up` finds no
			// volume and seeds a fresh one. A shim that kept them forever would make
			// that sequence untestable.
			//
			// A deleted volume is remembered as gone — whether a run made it or
			// $VOLUME_LS listed it — so deleting it again fails, as on the real CLI
			// (1.4.1: `Error: failed to delete one or more volumes: ["<name>"]`,
			// exit 1), and `volume ls` no longer lists it. A name never seen at all
			// is let through, as the other two fakes let it through (a known
			// leniency: nothing opossum does depends on that answer).
			if dir := os.Getenv("STATE_DIR"); dir != "" {
				var absent []string
				for _, v := range args[2:] {
					if _, err := os.Stat(goneVolumePath(dir, v)); err == nil {
						absent = append(absent, `"`+v+`"`)
						continue
					}
					os.Remove(volumePath(dir, v))
					_ = os.WriteFile(goneVolumePath(dir, v), []byte(v), 0o644)
				}
				if len(absent) > 0 {
					fmt.Fprintf(os.Stderr, "Error: failed to delete one or more volumes: [%s]\n", strings.Join(absent, ", "))
					return 1
				}
			}
		}

	case "logs":
		last := ""
		if len(args) > 0 {
			last = args[len(args)-1]
		}
		// A container that is not there has no logs: the real CLI exits 1
		// saying so, where reading them as empty would let a command that
		// should have passed the service by look as though it worked.
		if last != "" && !there(last) {
			fmt.Fprintf(os.Stderr, "Error: failed to get logs for container %s (cause: \"internalError: \"failed to open container logs: notFound: \"container with ID %s not found\"\"\")\n", last, last)
			return 1
		}
		// $LOGS_FAIL names containers whose logs cannot be read: the real CLI
		// prints an error and exits 1.
		// $LOGS_FAIL_N names the same, but only when asked with -n — a container
		// removed between one ask and the next.
		if slices.Contains(strings.Fields(os.Getenv("LOGS_FAIL")), last) || slices.Contains(args, "-n") && slices.Contains(strings.Fields(os.Getenv("LOGS_FAIL_N")), last) {
			fmt.Fprintf(os.Stderr, "Error: failed to get logs for container %s\n", last)
			return 1
		}
		// $LOGS_EMPTY names containers that have written nothing yet. The real
		// CLI then ends at once, exit 0 and nothing written, unless it is asked
		// to follow with -n, when it follows the empty log (container 1.4.1:
		// ContainerLogs.swift returns on an empty read before following, and
		// reads nothing back with -n; measured).
		empty := slices.Contains(strings.Fields(os.Getenv("LOGS_EMPTY")), last)
		if empty && !(slices.Contains(args, "-f") && slices.Contains(args, "-n")) {
			// $LOGS_EMPTY_AFTER=<ms> is how long the real CLI takes to find the
			// log empty (0.1 s measured), stretched so a Ctrl-C can come within it.
			if ms, err := strconv.Atoi(os.Getenv("LOGS_EMPTY_AFTER")); err == nil {
				time.Sleep(time.Duration(ms) * time.Millisecond)
			}
			return 0
		}
		// $LOGS_TEXT is what every container's logs hold, written as is
		// ({name} is the container): several lines, a blank one, a last one
		// without its newline. The real CLI writes a container's stdout and
		// stderr lines alike to its own stdout (container 1.4.1, measured).
		// While the stream is open the real CLI catches SIGINT and SIGTERM and
		// exits 130 and 143 of its own (container 1.4.1); caught before the
		// first line, so a signal after it is answered the same way.
		caught := make(chan os.Signal, 1)
		signal.Notify(caught, syscall.SIGINT, syscall.SIGTERM)
		switch text, ok := os.LookupEnv("LOGS_TEXT"); {
		case empty:
		case ok:
			fmt.Print(strings.ReplaceAll(text, "{name}", last))
		default:
			fmt.Printf("log-line %s\n", last)
		}
		// $LOGS_DRIP=<n> writes n more lines after that, 20 ms apart — what the
		// runtime still hands over a moment after the container has ended.
		if n, err := strconv.Atoi(os.Getenv("LOGS_DRIP")); err == nil {
			// $LOGS_DRIP_AFTER=<ms> waits that long before the first of them.
			if ms, err := strconv.Atoi(os.Getenv("LOGS_DRIP_AFTER")); err == nil {
				time.Sleep(time.Duration(ms) * time.Millisecond)
			}
			for i := 1; i <= n; i++ {
				time.Sleep(20 * time.Millisecond)
				fmt.Printf("drip %d %s\n", i, last)
			}
		}
		// $LOGS_SLEEP keeps the stream open that many seconds after the lines,
		// as `container logs -f` stays open, or as a long read of a large log
		// does without -f (the real CLI without -f ends once the log is read).
		if n, err := strconv.Atoi(os.Getenv("LOGS_SLEEP")); err == nil {
			// $LOGS_SELF_INT=<ms> streams that long after the lines, then writes
			// `self-int` and sends this process the SIGINT a terminal's Ctrl-C
			// gives the runtime: the stream ends as the real CLI ends it, 130
			// with -f or without (the contract in internal/shimcontract), before
			// the caller has been told anything. The line tells a test when.
			if ms, err := strconv.Atoi(os.Getenv("LOGS_SELF_INT")); err == nil {
				time.Sleep(time.Duration(ms) * time.Millisecond)
				fmt.Println("self-int")
				syscall.Kill(os.Getpid(), syscall.SIGINT)
			}
			select {
			case sig := <-caught:
				if sig == syscall.SIGINT {
					return 130
				}
				return 143
			case <-time.After(time.Duration(n) * time.Second):
			}
		}

	case "ls":
		// $CONTAINER_LS is a whole `ls -a --format json` document, for a test that
		// needs states other than running or more than one project — the same
		// knob the CLI shim has. `-a` is honoured: without it only the running
		// entries are answered, as the real CLI does, so a caller that drops the
		// flag sees fewer containers here too.
		if doc := os.Getenv("CONTAINER_LS"); doc != "" {
			if !hasArg("-a") {
				doc = onlyRunning(doc)
			}
			fmt.Println(doc)
			return 0
		}
		var items []string
		for _, n := range strings.Fields(os.Getenv("LS_CONTAINERS")) {
			items = append(items, fmt.Sprintf(`{"status":{"state":"running"},"configuration":{"id":"%s","labels":{"opossum.project":"%s"}}}`, n, os.Getenv("LS_PROJECT")))
		}
		for _, n := range strings.Fields(os.Getenv("LS_FOREIGN")) {
			items = append(items, fmt.Sprintf(`{"status":{"state":"running"},"configuration":{"id":"%s","labels":{"opossum.project":"otherproj"}}}`, n))
		}
		// $LS_UNLABELED lists containers with no project label at all (made
		// outside opossum).
		for _, n := range strings.Fields(os.Getenv("LS_UNLABELED")) {
			items = append(items, fmt.Sprintf(`{"status":{"state":"running"},"configuration":{"id":"%s","labels":{}}}`, n))
		}
		fmt.Printf("[%s]", strings.Join(items, ","))

	case "stats":
		// $STATS_FAIL=1 makes every `stats` call fail on stderr with exit 1, in
		// the words container 1.4.1 uses for a name it does not know (the one
		// failure of `stats` recorded in testdata/real-cli-output.md).
		if os.Getenv("STATS_FAIL") == "1" {
			fmt.Fprintln(os.Stderr, "Error: no such container: "+strings.Join(args[1:], " "))
			return 1
		}
		// `stats --no-stream --format json <names…>` returns a guest-view JSON
		// array, one entry per named container (echoing the id back so callers can
		// join it to a service). The streaming form (no --format json) just logs.
		jsonForm := false
		var names []string
		for i, a := range args[1:] {
			switch {
			case a == "json" && i > 0 && args[i] == "--format":
				jsonForm = true
			case strings.HasPrefix(a, "-") || a == "json":
				// flag or its value — not a container name
			default:
				names = append(names, a)
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
				// Every key 1.4.1 prints, each with its own value, in the
				// runtime's (alphabetical) key order.
				objs = append(objs, fmt.Sprintf(
					`{"blockReadBytes":8192,"blockWriteBytes":16384,"cpuUsageUsec":1500000,"id":"%s",`+
						`"memoryLimitBytes":1073741824,"memoryUsageBytes":49283072,"networkRxBytes":2048,"networkTxBytes":4096,"numProcesses":3}`,
					n,
				))
			}
			fmt.Printf("[%s]\n", strings.Join(objs, ","))
		}

	case "image":
		if arg(1) == "inspect" {
			for _, m := range strings.Fields(os.Getenv("IMAGE_ABSENT")) {
				if arg(2) == m {
					return 1
				}
			}
		}
	}
	return 0
}

// gonePath is where a removed object's marker lives. The name is sanitised the
// same way the shim in cmd/opossum/testdata does it, so a name carrying `/` or
// `:` — an image ref, if this ever covers more than containers — can't escape the
// state directory.
// volumePath is where a volume opossum created is remembered. The real runtime
// creates a volume as a side effect of running a container that mounts one, so
// the record is written from the `run` case rather than from any `volume create`
// — opossum never issues one.
func volumePath(dir, name string) string {
	return filepath.Join(dir, "volume-"+hex.EncodeToString([]byte(name)))
}

// goneVolumePath marks a volume deleted (see the `volume delete` case); a run
// that mounts the name again clears it.
func goneVolumePath(dir, name string) string {
	return filepath.Join(dir, "gonevolume-"+hex.EncodeToString([]byte(name)))
}

func volumeGone(name string) bool {
	dir := os.Getenv("STATE_DIR")
	if dir == "" {
		return false
	}
	_, err := os.Stat(goneVolumePath(dir, name))
	return err == nil
}

// madeVolumes lists the volumes recorded so far, in the order their state
// files are named — the names' hex, so byte order of the names — not in the
// order they were made. A name longer than about 120 bytes would not fit a
// state file name once doubled into hex; no test uses one.
func madeVolumes() []string {
	dir := os.Getenv("STATE_DIR")
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil && strings.HasPrefix(e.Name(), "volume-") {
			out = append(out, strings.TrimSpace(string(b)))
		}
	}
	return out
}

func gonePath(dir, name string) string {
	return filepath.Join(dir, "gone-"+hex.EncodeToString([]byte(name)))
}

// stopAskedPath is the marker every `stop` of a name leaves, whatever it did.
func stopAskedPath(dir, name string) string {
	return filepath.Join(dir, "stopasked-"+hex.EncodeToString([]byte(name)))
}

// stoppedPath is the marker a `stop` leaves so that inspect reports the
// container stopped (and still there) until something runs it again.
func stoppedPath(dir, name string) string {
	return filepath.Join(dir, "stopped-"+hex.EncodeToString([]byte(name)))
}

// publishedPorts renders the ports a previous `run` recorded for this container,
// falling back to the historical fixed mapping when nothing was recorded (most
// tests don't care, and changing their expectations would be noise).
func publishedPorts(name string) string {
	dir := os.Getenv("STATE_DIR")
	if dir == "" || name == "" {
		return `{"containerPort":8080,"hostAddress":"0.0.0.0","hostPort":8080,"proto":"tcp"}`
	}
	b, err := os.ReadFile(filepath.Join(dir, name+".ports"))
	if err != nil || len(strings.TrimSpace(string(b))) == 0 {
		return `{"containerPort":8080,"hostAddress":"0.0.0.0","hostPort":8080,"proto":"tcp"}`
	}
	var out []string
	for _, spec := range strings.Split(strings.TrimSpace(string(b)), ",") {
		proto := "tcp"
		if i := strings.LastIndex(spec, "/"); i >= 0 {
			proto, spec = spec[i+1:], spec[:i]
		}
		parts := strings.Split(spec, ":")
		if len(parts) < 2 {
			continue
		}
		host, container := parts[len(parts)-2], parts[len(parts)-1]
		out = append(out, fmt.Sprintf(`{"containerPort":%s,"hostAddress":"0.0.0.0","hostPort":%s,"proto":"%s"}`,
			container, host, proto))
	}
	if len(out) == 0 {
		return `{"containerPort":8080,"hostAddress":"0.0.0.0","hostPort":8080,"proto":"tcp"}`
	}
	return strings.Join(out, ",")
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

// onlyRunning keeps the entries of a `ls --format json` document whose state is
// running — what the real CLI answers without `-a`.
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

// stoppedForStats reports whether inspect would call the container stopped,
// by the same knobs: $INSPECT_STATE for every container, $INSPECT_STOPPED by
// name, and a `stop` recorded under $STATE_DIR.
func stoppedForStats(name string) bool {
	if st := os.Getenv("INSPECT_STATE"); st != "" && st != "running" {
		return true
	}
	if slices.Contains(strings.Fields(os.Getenv("INSPECT_STOPPED")), name) {
		return true
	}
	if dir := os.Getenv("STATE_DIR"); dir != "" {
		if _, err := os.Stat(stoppedPath(dir, name)); err == nil {
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

// slowHere reports whether a slow knob applies to the container name: every
// container, or the one $SLOW_ONLY names.
func slowHere(name string) bool {
	only := os.Getenv("SLOW_ONLY")
	return only == "" || only == name
}
