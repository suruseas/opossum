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
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
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
	// Where a `network create` leaves the labels it was given (one `key=value`
	// per line), so a later `network inspect` answers with them.
	netLabelsPath := func(name string) string {
		if stopDir == "" {
			return ""
		}
		return filepath.Join(stopDir, "netlabels-"+hex.EncodeToString([]byte(name)))
	}
	// Where a run's published ports are remembered, so a later inspect can
	// answer what THIS container holds. Without it every container answers one
	// fixed mapping, and a question about whose port a number is has the same
	// answer for all of them.
	portsPath := func(name string) string {
		if stopDir == "" {
			return ""
		}
		return filepath.Join(stopDir, "ports-"+hex.EncodeToString([]byte(name)))
	}
	// publishedPorts renders what a run recorded, in the shape the real CLI
	// answers: the address as the address it names (bare, where a compose file
	// writes it bracketed), and a range as ONE entry at its low port with a
	// count. A single port carries a count of 1 (measured on container 1.4.1).
	publishedPorts := func(name string) string {
		fixed := `{"containerPort":80,"count":1,"hostAddress":"0.0.0.0","hostPort":8080,"proto":"tcp"}`
		p := portsPath(name)
		if p == "" {
			return fixed
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return fixed // never run here
		}
		// Run without a `-p`: nothing published, and the real CLI answers with
		// an empty list.
		if len(strings.TrimSpace(string(b))) == 0 {
			return ""
		}
		var out []string
		for _, spec := range strings.Split(strings.TrimSpace(string(b)), ",") {
			proto := "tcp"
			if i := strings.LastIndex(spec, "/"); i >= 0 {
				proto, spec = strings.ToLower(spec[i+1:]), spec[:i] // as the runtime settles it
			}
			parts := strings.Split(spec, ":")
			if len(parts) < 2 {
				continue
			}
			host, container := parts[len(parts)-2], parts[len(parts)-1]
			addr := "0.0.0.0"
			if len(parts) > 2 {
				addr = strings.Trim(strings.Join(parts[:len(parts)-2], ":"), "[]")
			}
			count := 1
			if lo, hi, ok := strings.Cut(host, "-"); ok {
				l, errLo := strconv.Atoi(lo)
				h, errHi := strconv.Atoi(hi)
				if errLo == nil && errHi == nil && h >= l {
					count, host = h-l+1, lo
				}
			}
			if c, _, ok := strings.Cut(container, "-"); ok {
				container = c
			}
			out = append(out, fmt.Sprintf(`{"containerPort":%s,"count":%d,"hostAddress":%q,"hostPort":%s,"proto":"%s"}`,
				container, count, addr, host, proto))
		}
		if len(out) == 0 {
			return fixed
		}
		return strings.Join(out, ",")
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
	// there says whether a container of this name is there as far as this fake
	// is concerned: one named in $INSPECT_ABSENT was never made, one the
	// delete case marked gone is no longer there. Both answers have to reach
	// every command, not only `inspect` — a fake that says "no such container"
	// to one question and hands logs to the next lets a command that should
	// have passed the service by look as though it worked (#1096).
	there := func(name string) bool {
		for _, m := range strings.Fields(os.Getenv("INSPECT_ABSENT")) {
			if name == m {
				return false
			}
		}
		if isGone("container", name) {
			return false
		}
		// $INSPECT_STRICT: a name nothing here ran is not a container the runtime has, for `start`,
		// `logs`, `exec` and `kill` as for `inspect` (#1551).
		if os.Getenv("INSPECT_STRICT") != "" && stateDir != "" {
			if _, err := os.Stat(filepath.Join(stateDir, "created-"+hex.EncodeToString([]byte(name)))); err != nil {
				return false
			}
		}
		return true
	}
	// seenVolumePath marks a named volume a `run` made here (#1551).
	seenVolumePath := func(name string) string {
		return filepath.Join(stateDir, "volseen-"+hex.EncodeToString([]byte(name)))
	}
	// createdPath marks a name `run` has made here and `delete` has not since
	// cleared — what the default "already exists" refusal in the `run` case
	// checks. "" (no $STATE_DIR) disables it the same way the other markers do.
	createdPath := func(name string) string {
		if stateDir == "" {
			return ""
		}
		return filepath.Join(stateDir, "created-"+hex.EncodeToString([]byte(name)))
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
		// A `-p` whose two sides name different numbers of ports is refused next.
		if msg, refused := publishCountsRefused(args); refused {
			fmt.Fprintln(os.Stderr, msg)
			os.Exit(1)
		}
		// A named volume `-v NAME:/target` is made by the run, as the real runtime makes one; a path
		// (a bind) is not. $INSPECT_STRICT asks `volume delete` of a name nothing made to be refused.
		if stateDir != "" {
			for i, a := range args {
				if i == 0 || args[i-1] != "-v" {
					continue
				}
				if src, _, ok := strings.Cut(a, ":"); ok && src != "" && !strings.ContainsAny(src[:1], "/.~") {
					_ = os.WriteFile(seenVolumePath(src), []byte(src), 0o644)
					_ = os.Remove(gonePath("volume", src))
				}
			}
		}
		// A name this fake itself has already run and not since deleted is taken:
		// 1.4.1 refuses `run` of an existing name the same way whether it is
		// running or stopped (measured, testdata/real-cli-output.md), and until
		// this a second run of one name silently replaced the first — a call
		// opossum never makes deliberately (a genuine replace always deletes
		// first), so nothing exercised this question until #962's contract row
		// asked it. Said before the volume check, as the real CLI does.
		for i, a := range args {
			if i > 0 && args[i-1] == "--name" {
				if p := createdPath(a); p != "" {
					if _, err := os.Stat(p); err == nil {
						fmt.Fprintf(os.Stderr, "Error: container with id %s already exists\n", a)
						os.Exit(1)
					}
				}
			}
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
		// short spelling; `--label` is the long one; opossum passes `--label=<v>`
		// as one argument (#996 — a value taken as a separate argument is read as
		// another flag when it starts with "-"), so that combined form is read too.
		var runProject string
		var runPorts []string
		for i, a := range args {
			if v, ok := strings.CutPrefix(a, "--label="); ok {
				if pv, ok := strings.CutPrefix(v, "opossum.project="); ok {
					runProject = pv
				}
			} else if i > 0 && (args[i-1] == "-l" || args[i-1] == "--label") {
				if v, ok := strings.CutPrefix(a, "opossum.project="); ok {
					runProject = v
				}
			}
			if i > 0 && (args[i-1] == "-p" || args[i-1] == "--publish") {
				runPorts = append(runPorts, a)
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
				if p := portsPath(a); p != "" {
					_ = os.WriteFile(p, []byte(strings.Join(runPorts, ",")), 0o644)
				}
				if p := createdPath(a); p != "" {
					_ = os.WriteFile(p, []byte("1"), 0o644)
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
			// A network deleted, then made again under the same name, is not
			// gone any more: the mark from an earlier delete is this run's alone.
			if stateDir != "" {
				_ = os.Remove(gonePath("network", name))
			}
			// A label key the runtime refuses (1.5.0, rc 1; it takes lower-case words and digits joined by `.`, `/` or `-`, so
			// a key that starts with `-`, is empty, or holds a space or a control character is among them, and an upper-case letter or a
			// `_` is another that this fake lets through) is named in the metadata as it was written (testdata/real-cli-output.md).
			for _, a := range os.Args[1:] {
				if v, ok := strings.CutPrefix(a, "--label="); ok {
					key, _, _ := strings.Cut(v, "=")
					bad := key == "" || key[0] == '-' || strings.ContainsFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
					if v == key+"=" {
						bad = true // a label written with no value: the runtime names the whole `k=`
					}
					if bad {
						if key == "" || v == key+"=" {
							key = v // an empty key: the runtime names the whole `=value`
						}
						var shown strings.Builder // the runtime writes a tab as `\t` and another control character as `\u{01}`
						for _, r := range key {
							switch {
							case r == '\t':
								shown.WriteString(`\t`)
							case r == '\n':
								shown.WriteString(`\n`)
							case unicode.IsControl(r):
								fmt.Fprintf(&shown, `\u{%02X}`, r)
							default:
								shown.WriteRune(r)
							}
						}
						fmt.Fprintf(os.Stderr, "Error: LabelError(code: ContainerResource.AppErrorCode(rawValue: \"invalid_label_key_content\"), metadata: [\"key\": \"%s\"])\n", shown.String())
						os.Exit(1)
					}
				}
			}
			if p := netLabelsPath(name); p != "" {
				var labels []string
				for i, a := range os.Args[1:] {
					if a == "--label" && i+2 < len(os.Args) {
						labels = append(labels, os.Args[i+2])
					}
					// opossum passes `--label=<k=v>` as one argument (#1423); the real CLI reads both.
					if v, ok := strings.CutPrefix(a, "--label="); ok {
						labels = append(labels, v)
					}
				}
				_ = os.WriteFile(p, []byte(strings.Join(labels, "\n")), 0o644)
			}
			fmt.Println(name) // the real CLI echoes the network name on success
		}
		// `network inspect` exits non-zero for a network that isn't there, which is
		// how opossum decides whether one exists. $NETWORK_ABSENT lists the ones to
		// report missing; by default every network exists.
		if arg(1) == "delete" {
			// A network already deleted is not there to delete: the real CLI
			// fails (1.4.1: `Error: failed to delete one or more networks:
			// ["<name>"]`) — a different shape from `network inspect`'s
			// `network not found: <name>` (measured; DeleteNetwork's
			// networkAlreadyGone reads both shapes, so a fake that always
			// succeeded here never exercised the one it reads first).
			if isGone("network", arg(2)) {
				fmt.Fprintf(os.Stderr, "Error: failed to delete one or more networks: [%q]\n", arg(2))
				os.Exit(1)
			}
			markGone("network", arg(2))
			if p := netLabelsPath(arg(2)); p != "" {
				_ = os.Remove(p)
			}
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
			// The labels the network was made with, in the shape the real CLI
			// prints (an empty `labels` for one made without any); a network
			// this shim did not see made answers nothing, as it always did.
			if p := netLabelsPath(arg(2)); p != "" {
				if b, err := os.ReadFile(p); err == nil {
					lines := []string{}
					for _, l := range strings.Split(string(b), "\n") {
						if l == "" {
							continue
						}
						k, v, _ := strings.Cut(l, "=")
						lines = append(lines, fmt.Sprintf("        %q : %q", k, v))
					}
					body := "\n"
					if len(lines) > 0 {
						body = "\n" + strings.Join(lines, ",\n") + "\n      "
					}
					fmt.Printf("[\n  {\n    \"configuration\" : {\n      \"labels\" : {%s},\n      \"name\" : %q\n    }\n  }\n]\n", body, arg(2))
				}
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
		// `rm` is `delete`'s alias on 1.4.1 (opossum itself only ever issues
		// `delete`, so nothing here depended on this until #962's contract row
		// asked what the fake says about the one it never uses).
		if arg(1) == "delete" || arg(1) == "rm" {
			// A volume already deleted is not there to delete: the real CLI fails
			// (1.4.1: `Error: failed to delete one or more volumes: ["<name>"]`).
			if isGone("volume", arg(2)) {
				fmt.Fprintf(os.Stderr, "Error: failed to delete one or more volumes: [%q]\n", arg(2))
				os.Exit(1)
			}
			// $INSPECT_STRICT: a volume nothing here made, and $VOLUME_LS does not list, is not one the
			// runtime has, and the real CLI refuses to delete it (container 1.4.1,
			// testdata/real-cli-output.md); without it every name is taken to be there (#1551).
			if os.Getenv("INSPECT_STRICT") != "" && stateDir != "" {
				listed := false
				for _, line := range strings.Split(os.Getenv("VOLUME_LS"), "\n") {
					if f := strings.Fields(line); len(f) > 0 && f[0] == arg(2) {
						listed = true
					}
				}
				if _, err := os.Stat(seenVolumePath(arg(2))); err != nil && !listed {
					fmt.Fprintf(os.Stderr, "Error: failed to delete one or more volumes: [%q]\n", arg(2))
					os.Exit(1)
				}
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
		// $INSPECT_STRICT: a name nothing here ran is not a container the runtime has, for `stop` as
		// for `inspect` (#1551).
		if os.Getenv("INSPECT_STRICT") != "" && stateDir != "" {
			if _, err := os.Stat(createdPath(lastArg())); err != nil {
				fmt.Fprintf(os.Stderr, "Error: internalError: \"failed to stop container\" (cause: \"notFound: \"container with ID %s not found\"\")\n", lastArg())
				os.Exit(1)
			}
		}
		if p := stoppedPath(lastArg()); p != "" {
			_ = os.WriteFile(p, []byte("1"), 0o644)
		}
	case "delete", "rm":
		if isGone("container", lastArg()) {
			fmt.Fprintf(os.Stderr, "Error: internalError: \"failed to delete container\" (cause: \"notFound: \"container with ID %s not found\"\")\n", lastArg())
			os.Exit(1)
		}
		// $INSPECT_STRICT: a name nothing here ran is not a container the runtime has, for `delete` as
		// for `inspect` (#1551).
		if os.Getenv("INSPECT_STRICT") != "" && stateDir != "" {
			if _, err := os.Stat(createdPath(lastArg())); err != nil {
				fmt.Fprintf(os.Stderr, "Error: internalError: \"failed to delete container\" (cause: \"notFound: \"container with ID %s not found\"\")\n", lastArg())
				os.Exit(1)
			}
		}
		markGone("container", lastArg())
		if p := stoppedPath(lastArg()); p != "" {
			_ = os.Remove(p)
		}
		if p := createdPath(lastArg()); p != "" {
			_ = os.Remove(p)
		}
	case "start":
		// A container that is not there cannot be started (container 1.4.1).
		if !there(lastArg()) {
			fmt.Fprintf(os.Stderr, "Error: get failed: container %s not found\n", lastArg())
			os.Exit(1)
		}
		// Running again in place: the stop marker is cleared.
		if p := stoppedPath(lastArg()); p != "" {
			_ = os.Remove(p)
		}

	case "kill":
		// The real CLI only truly kills a running container: one already
		// stopped refuses with invalidState, and one not there (never made or
		// since deleted) refuses with notFound — both measured on 1.4.1, same
		// pair as the orchestrator's shim. Orchestrator.Kill re-inspects after
		// either failure and only reports it when the container is still
		// running or unreadable, so a container not running discards both —
		// answering them correctly here changes no caller's behaviour.
		if !there(lastArg()) {
			fmt.Fprintf(os.Stderr, "Error: internalError: \"failed to kill container\" (cause: \"notFound: \"container with ID %s not found\"\")\n", lastArg())
			os.Exit(1)
		}
		if p := stoppedPath(lastArg()); p != "" {
			if _, err := os.Stat(p); err == nil {
				fmt.Fprintln(os.Stderr, `Error: internalError: "failed to kill container" (cause: "invalidState: "no runtime client exists: container is stopped"")`)
				os.Exit(1)
			}
			_ = os.WriteFile(p, []byte("1"), 0o644)
		}

	case "exec":
		// A container that is not there cannot be exec'd into (container
		// 1.4.1, same wording as `start`).
		// The container is the first argument that is not a flag (`exec -t NAME …`), and a flag that takes its
		// value apart (`-e A=1`, `--user u`; container 1.5.0, testdata/real-cli-output.md) is read with the value,
		// which is not the name.
		target := ""
		valued := false
		for _, a := range args[1:] {
			if valued {
				valued = false
				continue
			}
			if strings.HasPrefix(a, "-") {
				valued = execTakesValue[a]
				continue
			}
			target = a
			break
		}
		if target != "" && !there(target) {
			fmt.Fprintf(os.Stderr, "Error: get failed: container %s not found\n", target)
			os.Exit(1)
		}

	case "logs":
		// A container that is not there has no logs (container 1.4.1): reading
		// them as empty would let a command that should have passed the
		// service by look as though it worked.
		if !there(lastArg()) {
			fmt.Fprintf(os.Stderr, "Error: failed to get logs for container %s (cause: \"internalError: \"failed to open container logs: notFound: \"container with ID %s not found\"\"\")\n", lastArg(), lastArg())
			os.Exit(1)
		}
		// One line per container, then, with $LOGS_SLEEP, a stream that stays
		// open that many seconds, as `container logs -f` does (or a long read
		// of a large log without -f).
		// While the stream is open the real CLI catches SIGINT and SIGTERM and
		// exits 130 and 143 of its own (container 1.4.1).
		// $LOGS_EMPTY names containers that have written nothing yet: the real
		// CLI ends at once, exit 0 and nothing written, unless it is asked to
		// follow with -n, when it follows the empty log (container 1.4.1).
		empty := slices.Contains(strings.Fields(os.Getenv("LOGS_EMPTY")), args[len(args)-1])
		if empty && !(slices.Contains(args, "-f") && slices.Contains(args, "-n")) {
			return
		}
		caught := make(chan os.Signal, 1)
		signal.Notify(caught, syscall.SIGINT, syscall.SIGTERM)
		if !empty {
			fmt.Printf("log-line %s\n", args[len(args)-1])
		}
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
		// $INSPECT_STRICT: a name nothing here ran is not a container the runtime has, as the real
		// CLI answers for one it has never seen (container 1.4.1, testdata/real-cli-output.md);
		// without it every name is taken to be there (#1551).
		if os.Getenv("INSPECT_STRICT") != "" && stateDir != "" {
			if _, err := os.Stat(createdPath(arg(1))); err != nil {
				fmt.Fprintf(os.Stderr, "Error: container not found: %s\n", arg(1))
				os.Exit(1)
			}
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
		fmt.Printf(`[{"status":{"state":"%s","networks":[{"ipv4Address":"192.168.66.9/24"}]},"configuration":{"labels":{%s},"networks":[{"network":"demo-net-configured"}],"publishedPorts":[%s]}}]`+"\n",
			state, labels, publishedPorts(arg(1)))
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
		// `there` reads $INSPECT_ABSENT and a name the `delete` case marked
		// gone alike: both are "not there" to the real CLI (1.4.1).
		for _, n := range names {
			if !there(n) {
				fmt.Fprintf(os.Stderr, "Error: no such container: %s\n", n)
				os.Exit(1)
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
		refuseImageArguments(arg)
		refuseAbsentImage(args)
		refuseEmptyLoad(args)
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
			if cmd := imageCmd(arg(2)); cmd != nil {
				fmt.Println(imageCmdDocument(cmd))
			}
		}
	}
}

// imageCmd is the CMD $IMAGE_CMD gives ref (#1635), as `ref=word,word` entries separated by
// spaces, or nil for an image not named there.
func imageCmd(ref string) []string {
	for _, entry := range strings.Fields(os.Getenv("IMAGE_CMD")) {
		r, rest, _ := strings.Cut(entry, "=")
		if r == ref && rest != "" {
			return strings.Split(rest, ",")
		}
	}
	return nil
}

// imageCmdDocument is what `image inspect` answers for an image with a CMD, in the real shape: a
// variant without one (an attestation) and the variant that has it.
func imageCmdDocument(cmd []string) string {
	words, _ := json.Marshal(cmd)
	return `[{"variants":[{"config":{"config":{}}},{"config":{"config":{"Cmd":` + string(words) + `}}}]}]`
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
// This reads the shapes opossum passes (`--name <name>` among separate flags,
// and `--flag=value` combined for the flags opossum passes that way, #996).
// Not read the way the real CLI reads them: `--name=<name>`, `-e=<value>`,
// combined short flags (`-it`), `--`, `-h`/`--help`/`--version`, `--debug` (an
// unknown option to `run`), a value starting with `-` given to another flag as
// a separate argument (the real CLI calls that flag's value missing, exit 64 —
// measured on `--user -1`; opossum itself now passes such values combined,
// `--user=-1` from `user: "-1"`, #996), and a flag with no value at the end.
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
		// A long flag opossum passes as one `--flag=value` argument (#996) is
		// already complete — it does not consume the next word too. `--name`
		// itself is never passed this way (name comes as a separate argument),
		// so this cannot hide a genuine `--name=...` from the check below.
		if strings.HasPrefix(a, "--") && strings.Contains(a, "=") {
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
		// Same reason as containerNameRefused: a combined `--flag=value`
		// argument (#996) does not consume the next word too.
		if strings.HasPrefix(a, "--") && strings.Contains(a, "=") {
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

// publishCountsRefused answers a `run` with a `-p` whose host side and container
// side are ranges of different lengths, the way container 1.4.1 does
// (`Error: publish host and container port counts are not equal: <host>:<container>`,
// the two sides as written, without the address or the protocol in front of and behind them,
// exit 1, measured for a host range with one container port, one host port with
// a container range, and two ranges of different lengths). Nothing is recorded
// for such a run. A spelling with no range, or two ranges of one length, is not
// asked about here.
func publishCountsRefused(args []string) (msg string, refused bool) {
	for i, a := range args {
		if i == 0 || (args[i-1] != "-p" && args[i-1] != "--publish") {
			continue
		}
		s := a
		if j := strings.LastIndex(s, "/"); j >= 0 {
			s = s[:j] // the protocol
		}
		if strings.HasPrefix(s, "[") { // an IPv6 address
			if j := strings.Index(s, "]"); j >= 0 {
				s = strings.TrimPrefix(s[j+1:], ":")
			}
		}
		parts := strings.Split(s, ":")
		if len(parts) < 2 {
			continue // a bare container port
		}
		host, ctr := parts[len(parts)-2], parts[len(parts)-1]
		if publishCount(host) != publishCount(ctr) {
			return "Error: publish host and container port counts are not equal: " + host + ":" + ctr, true
		}
	}
	return "", false
}

// publishCount is how many ports one side of a `-p` names: `80` is one, `80-82` three.
func publishCount(side string) int {
	lo, hi, ok := strings.Cut(side, "-")
	if !ok {
		return 1
	}
	a, errA := strconv.Atoi(lo)
	b, errB := strconv.Atoi(hi)
	if errA != nil || errB != nil {
		return 1
	}
	return b - a + 1
}

// refuseImageArguments answers what the real CLI refuses before it does anything (container 1.5.0,
// testdata/real-cli-output.md): a subcommand it does not have is rc 64, and so is a subcommand that
// needs an image or a reference and is given none; `delete` with none is rc 1. `ls`, `prune` and
// `load` need no argument. The shim in internal/orchestrator/testdata and the shell one answer the
// same (internal/shimcontract).
func refuseImageArguments(arg func(int) string) {
	sub := arg(1)
	switch sub {
	case "", "inspect", "tag", "save", "pull", "push", "delete", "rm", "list", "ls", "load", "prune":
	default:
		fmt.Fprintf(os.Stderr, "Error: Unexpected argument '%s'\nUsage: container image [--debug] <subcommand>\n", sub)
		os.Exit(64)
	}
	if sub == "" || arg(2) != "" {
		return
	}
	switch sub {
	case "inspect":
		fmt.Fprintln(os.Stderr, "Error: Missing expected argument '<images> ...'")
		os.Exit(64)
	case "tag":
		fmt.Fprintln(os.Stderr, "Error: Missing expected argument '<source>'")
		os.Exit(64)
	case "save":
		fmt.Fprintln(os.Stderr, "Error: Missing expected argument '<references> ...'")
		os.Exit(64)
	case "pull":
		fmt.Fprintln(os.Stderr, "Error: Missing expected argument '<reference>'")
		os.Exit(64)
	case "delete", "rm":
		fmt.Fprintln(os.Stderr, "Error: no images specified and --all not supplied")
		os.Exit(1)
	}
}

// refuseAbsentImage answers what the real CLI refuses of an image that is not there (container 1.5.0,
// testdata/real-cli-output.md): `tag` (its source), `save` and `push` (the last argument) and a
// `delete` without `--force` are rc 1; `delete --force` of it is rc 0. An image is not there when
// $IMAGE_ABSENT names it. (`inspect` of it is answered below, with the images deleted here.)
func refuseAbsentImage(args []string) {
	if len(args) < 3 {
		return
	}
	absent := func(ref string) bool {
		for _, m := range strings.Fields(os.Getenv("IMAGE_ABSENT")) {
			if ref == m {
				return true
			}
		}
		return false
	}
	last := args[len(args)-1]
	switch args[1] {
	case "tag":
		if absent(args[2]) {
			fmt.Fprintf(os.Stderr, "Error: image with reference %s\n", args[2])
			os.Exit(1)
		}
	case "push":
		if absent(last) {
			fmt.Fprintf(os.Stderr, "Error: image with reference %s\n", last)
			os.Exit(1)
		}
	case "save":
		if absent(last) {
			fmt.Fprintf(os.Stderr, "failed to get image for reference %s: notFound: \"image with reference %s\"\nError: failed to save image(s)\n", last, last)
			os.Exit(1)
		}
	case "delete", "rm":
		for _, a := range args[2:] {
			if a == "--force" {
				return
			}
		}
		if absent(last) {
			fmt.Fprintf(os.Stderr, "Error: failed to delete one or more images: [\"%s\"]\n", last)
			os.Exit(1)
		}
	}
}

// refuseEmptyLoad answers what the real CLI does with an archive that has nothing in it (container
// 1.5.0, testdata/real-cli-output.md): `load` reads it from its standard input (opossum's `docker
// image save | container image load`) and refuses an empty one, rc 1. With `-i`/`--input` it reads a
// file (`--input=<path>` too), which this does not look at. Two simplifications, neither in
// internal/shimcontract because the real CLI answers otherwise: any non-empty input is taken (the real
// CLI refuses what is no tar: rc 1, `unable to open the archive, code -30`), and a file named by `-i`
// is taken whether or not it is there (the real CLI says `file does not exist`, rc 1).
func refuseEmptyLoad(args []string) {
	if len(args) < 2 || args[1] != "load" {
		return
	}
	for _, a := range args[2:] {
		if a == "-i" || a == "--input" || strings.HasPrefix(a, "--input=") {
			return
		}
	}
	if b, _ := io.ReadAll(os.Stdin); len(b) == 0 {
		fmt.Fprintln(os.Stderr, "Error: failed to extract archive: no entries found in archive")
		os.Exit(1)
	}
}

// execTakesValue are the flags of `container exec` that take their value as the next argument (container 1.5.0).
var execTakesValue = map[string]bool{"-e": true, "--env": true, "--env-file": true, "--gid": true, "--uid": true, "-u": true, "--user": true,
	"-w": true, "--workdir": true, "--cwd": true, "--ulimit": true}
