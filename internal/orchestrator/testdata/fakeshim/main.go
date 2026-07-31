// Command fakeshim is a compiled stand-in for the `container` CLI used by the
// orchestrator tests. It's a faithful port of the shell shim fakeShim used to
// write per-test — but compiled once and reused, so each test spawns a ~1-2ms
// binary instead of a ~50-80ms /bin/sh, which dominated the suite's runtime.
//
// It logs each invocation's arguments (space-joined) to $FAKE_LOG and returns
// output shaped like the real CLI. Behaviour is steered entirely through the
// environment (FAKE_LOG, STATE_DIR, DELETE_STICKY, INSPECT_STATE, INSPECT_STOPPED,
// INSPECT_ABSENT, NET_EXISTS, NETWORK_ABSENT, RUN_FAIL, HEALTH_*, VOLUME_*, LS_*,
// IMAGE_ABSENT),
// so tests need no t.Setenv and stay
// isolated: the orchestrator passes these per-Runtime via RunOptions-style Env.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
			fmt.Println("status running")
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
			sticky := false
			for _, m := range strings.Fields(os.Getenv("DELETE_STICKY")) {
				if name == m {
					sticky = true
				}
			}
			if !sticky {
				_ = os.WriteFile(gonePath(dir, name), []byte("1"), 0o644)
			}
		}

	case "inspect":
		// Gone until something creates it again (see the `run` case).
		if dir := os.Getenv("STATE_DIR"); dir != "" {
			if _, err := os.Stat(gonePath(dir, arg(1))); err == nil {
				fmt.Fprintf(os.Stderr, "Error: container not found: %s\n", arg(1))
				return 1
			}
		}
		// Build the labels object from INSPECT_PROJECT and any recorded config-hash.
		var labels []string
		if p := os.Getenv("INSPECT_PROJECT"); p != "" {
			labels = append(labels, `"opossum.project":"`+p+`"`)
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
		}
		if arg(1) == "create" {
			if os.Getenv("NET_EXISTS") != "" {
				fmt.Fprintf(os.Stderr, "network %s already exists\n", arg(2))
				return 1
			}
			fmt.Println(arg(2)) // real CLI echoes the network name on success
		}

	case "run":
		// Creating it again means it is no longer gone (see the `delete` case).
		if dir := os.Getenv("STATE_DIR"); dir != "" {
			for k, a := range args {
				if k > 0 && args[k-1] == "--name" {
					_ = os.Remove(gonePath(dir, a))
				}
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
		// A foreground run of $RUN_FAIL exits non-zero (drives failure evals).
		if fail := os.Getenv("RUN_FAIL"); fail != "" {
			for i, a := range args {
				if i > 0 && args[i-1] == "--name" && a == fail {
					return 1
				}
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
		if arg(1) == "ls" {
			fmt.Println(os.Getenv("VOLUME_LS"))
		}

	case "logs":
		last := ""
		if len(args) > 0 {
			last = args[len(args)-1]
		}
		fmt.Printf("log-line %s\n", last)

	case "ls":
		var items []string
		for _, n := range strings.Fields(os.Getenv("LS_CONTAINERS")) {
			items = append(items, fmt.Sprintf(`{"status":{"state":"running"},"configuration":{"id":"%s","labels":{"opossum.project":"%s"}}}`, n, os.Getenv("LS_PROJECT")))
		}
		for _, n := range strings.Fields(os.Getenv("LS_FOREIGN")) {
			items = append(items, fmt.Sprintf(`{"status":{"state":"running"},"configuration":{"id":"%s","labels":{"opossum.project":"otherproj"}}}`, n))
		}
		fmt.Printf("[%s]", strings.Join(items, ","))

	case "stats":
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
			for _, n := range names {
				objs = append(objs, fmt.Sprintf(`{"id":"%s","memoryUsageBytes":49283072,"memoryLimitBytes":1073741824}`, n))
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
func gonePath(dir, name string) string {
	return filepath.Join(dir, "gone-"+strings.NewReplacer("/", "_", ":", "_", ".", "_").Replace(name))
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
