// Command fakeshim is a compiled stand-in for the `container` CLI used by the
// CLI-level tests, replacing a per-test /bin/sh script. A compiled binary spawns
// in ~1-2ms versus ~50-80ms for a shell script. It logs each invocation to
// $FAKE_LOG and returns plausible output for the handful of commands the CLI
// tests drive (system dns list, network create, inspect).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		safe := strings.NewReplacer("/", "_", ":", "_", ".", "_").Replace(name)
		return filepath.Join(stateDir, "gone-"+kind+"-"+safe)
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
	switch arg(0) {
	case "system":
		if arg(1) == "dns" && arg(2) == "list" {
			fmt.Print("DOMAIN\nopossum\n")
		}
		// `system status` is the daemon-liveness probe; report running (or stopped
		// under SYSTEM_STOPPED until `system start` runs).
		if arg(1) == "status" {
			if systemRunning() {
				fmt.Print("FIELD  VALUE\nstatus  running\n")
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
		if arg(1) == "create" {
			fmt.Println(arg(2))
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
	case "volume":
		// `volume ls` is a table whose first column is the name; opossum reads it to
		// decide whether a volume exists. $VOLUME_LS is that table.
		if arg(1) == "delete" {
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
	case "delete", "rm":
		markGone("container", lastArg())

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
		labels := ""
		if proj := os.Getenv("INSPECT_PROJECT"); proj != "" {
			labels = `"opossum.project":"` + proj + `"`
		}
		state := os.Getenv("INSPECT_STATE")
		for _, m := range strings.Fields(os.Getenv("INSPECT_STOPPED")) {
			if arg(1) == m {
				state = "stopped"
			}
		}
		if state == "" {
			state = "running"
		}
		fmt.Printf(`[{"status":{"state":"%s","networks":[{"ipv4Address":"192.168.66.9/24"}]},"configuration":{"labels":{%s},"publishedPorts":[{"containerPort":80,"hostAddress":"0.0.0.0","hostPort":8080,"proto":"tcp"}]}}]`+"\n", state, labels)
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
		if jsonForm {
			var objs []string
			for _, n := range names {
				objs = append(objs, fmt.Sprintf(`{"id":"%s","memoryUsageBytes":49283072,"memoryLimitBytes":1073741824}`, n))
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
